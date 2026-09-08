package data

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	"github.com/go-pay/gopay/alipay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestAlipayCloseStatusConflictIsNotClosed(t *testing.T) {
	requester := &fakeAlipayTradeRequester{closeRsp: &alipayv3.TradeCloseRsp{
		StatusCode: http.StatusBadRequest, ErrResponse: alipayv3.ErrResponse{Code: "ACQ.TRADE_STATUS_ERROR"},
	}}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Close(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "pay_1"})
	require.ErrorIs(t, err, biz.ErrProviderTradeStateConflict)
	require.False(t, result.Success)
}

func TestAlipaySignedParametersBindProductAndAbsoluteExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	rawKey := base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key))
	client, err := alipayv3.NewClientV3("app_1", rawKey, false)
	require.NoError(t, err)
	signer, err := alipay.NewClient("app_1", rawKey, false)
	require.NoError(t, err)
	client.AppCertSN, signer.AppCertSN = "app_cert", "app_cert"
	client.AliPayRootCertSN, signer.AliPayRootCertSN = "root_cert", "root_cert"
	adapter := NewAlipayPaymentAdapter(client, log.DefaultLogger)
	adapter.wapSigner = signer
	adapter.notifyURL = "https://merchant.example/notify"
	deadline := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Second)
	for _, product := range []string{"wap", "app"} {
		t.Run(product, func(t *testing.T) {
			request := biz.PaymentPrepayRequest{Method: biz.PaymentMethod{Provider: "alipay", Product: product},
				OutTradeNo: "pay_1", Amount: 12345, Currency: "CNY", Description: "order", ExpiresAt: deadline}
			result, err := adapter.Prepay(context.Background(), request)
			require.NoError(t, err)
			var action biz.SignedPaymentPayload
			require.NoError(t, json.Unmarshal(result.Action.Payload, &action))
			require.True(t, deadline.Equal(action.ExpiresAt))
			require.Equal(t, adapter.providerAccount(), action.ProviderAccount)
			params := action.Payload
			wantProduct := "QUICK_MSECURITY_PAY"
			if product == "wap" {
				u, err := url.Parse(params)
				require.NoError(t, err)
				require.Equal(t, "https", u.Scheme)
				require.Equal(t, "openapi-sandbox.dl.alipaydev.com", u.Host)
				params = u.RawQuery
				wantProduct = "QUICK_WAP_WAY"
			}
			values, err := url.ParseQuery(params)
			require.NoError(t, err)
			require.Equal(t, "alipay.trade."+product+".pay", values.Get("method"))
			require.Equal(t, "app_1", values.Get("app_id"))
			require.Equal(t, "app_cert", values.Get("app_cert_sn"))
			require.Equal(t, "root_cert", values.Get("alipay_root_cert_sn"))
			require.Equal(t, adapter.notifyURL, values.Get("notify_url"))
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(values.Get("biz_content")), &body))
			require.Equal(t, wantProduct, body["product_code"])
			require.Equal(t, "123.45", body["total_amount"])
			require.Equal(t, "pay_1", body["out_trade_no"])
			require.Equal(t, deadline.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05"), body["time_expire"])
			// Verify the actual final URL/order string, not just pre-SDK input.
			signature, err := base64.StdEncoding.DecodeString(values.Get("sign"))
			require.NoError(t, err)
			values.Del("sign")
			signed := make(gopay.BodyMap)
			for k := range values {
				signed.Set(k, values.Get(k))
			}
			digest := sha256.Sum256([]byte(signed.EncodeAliPaySignParams()))
			require.NoError(t, rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature))
			for _, expired := range []time.Time{{}, time.Now().Add(-time.Second)} {
				request.ExpiresAt = expired
				_, err := adapter.Prepay(context.Background(), request)
				require.ErrorIs(t, err, biz.ErrOrderExpired)
			}
		})
	}
}

func TestWechatUntrustedMissingOrderNeverAuthorizesClose(t *testing.T) {
	for _, problem := range []string{"signature", "appid", "mch_id", "transport"} {
		t.Run(problem, func(t *testing.T) {
			fields := gopay.BodyMap{"return_code": "SUCCESS", "result_code": "FAIL", "err_code": "ORDERNOTEXIST", "appid": "app_1", "mch_id": "mch_1"}
			if problem == "appid" || problem == "mch_id" {
				fields.Set(problem, "other")
			}
			if problem == "transport" {
				fields.Set("return_code", "FAIL")
			}
			response := signedWechatXML(t, fields)
			if problem == "signature" {
				fields.Set("sign", "invalid")
				encoded, err := xml.Marshal(fields)
				require.NoError(t, err)
				response = string(encoded)
			}
			_, err := testWechatAdapterXML(t, response).Query(context.Background(), biz.PaymentQueryRequest{OutTradeNo: "pay_1"})
			require.Error(t, err)
			require.NotErrorIs(t, err, biz.ErrProviderOrderNotExist)
		})
	}
}

func TestAlipayRefundAmbiguousRejectionsRemainPending(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		err    error
	}{
		{429, "ACQ.TRADE_NOT_EXIST", nil}, {408, "ACQ.TRADE_NOT_EXIST", nil},
		{400, "ACQ.SYSTEM_ERROR", nil}, {400, "UNKNOWN", nil},
		{400, "ACQ.TRADE_NOT_EXIST", context.DeadlineExceeded},
	} {
		requester := &fakeAlipayTradeRequester{refundRsp: &alipayv3.TradeRefundRsp{StatusCode: tc.status, ErrResponse: alipayv3.ErrResponse{Code: tc.code}}, refundErr: tc.err}
		result, err := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger).Refund(context.Background(), biz.PaymentRefundRequest{OutTradeNo: "pay", OutRefundNo: "refund", Amount: 100, Currency: "CNY"})
		require.Error(t, err)
		require.False(t, result.Rejection)
	}
}

func TestRefundRetryRestoresPendingBeforeReturningOriginalNumber(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	payment := statePayment(biz.PaymentStatusSuccess)
	refund := db.OrderRefund{ID: 11, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}, OrderID: payment.OrderID, UserID: payment.UserID,
		OutRefundNo: "original", Currency: payment.Currency, TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor, Status: biz.PaymentRefundStatusFailed}
	pending := refund
	pending.Status = biz.PaymentRefundStatusPending
	gomock.InOrder(
		q.EXPECT().GetPaymentForUpdate(gomock.Any(), payment.ID).Return(payment, nil),
		q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), refund.PaymentID).Return(refund, nil),
		q.EXPECT().RetryOrderRefund(gomock.Any(), refund.ID).Return(pending, nil),
	)
	d := newTestData(t, q, miniredis.RunT(t))
	_, got, err := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger).PreparePaymentRefund(context.Background(), payment.ID, "unused_new_number")
	require.NoError(t, err)
	require.Equal(t, "original", got.OutRefundNo)
	require.Equal(t, biz.PaymentRefundStatusPending, got.Status)
}

func TestUnsupportedPaymentRecoveryRequiresNonDispatchEvidence(t *testing.T) {
	base := db.Payment{ID: 1, Status: biz.PaymentStatusCreating, PayChannel: "alipay:invalid", ReconciliationStatus: biz.ReconciliationStatusNone}
	require.True(t, unsupportedPaymentNeverDispatched(base))
	for _, mutate := range []func(*db.Payment){
		func(p *db.Payment) { p.PrepayAttempts = 1 },
		func(p *db.Payment) { p.PrepayLeaseToken = pgtype.Text{String: "lease", Valid: true} },
		func(p *db.Payment) { p.PrepayLeaseUntil = pgtype.Timestamptz{Time: time.Now(), Valid: true} },
		func(p *db.Payment) { p.ActionType = pgtype.Text{String: "invoke", Valid: true} },
		func(p *db.Payment) { p.ActionPayload = []byte(`{"payload":"signed"}`) },
		func(p *db.Payment) { p.ThirdPartyTxID = pgtype.Text{String: "trade", Valid: true} },
		func(p *db.Payment) { p.Status = biz.PaymentStatusPending },
		func(p *db.Payment) { p.ReconciliationStatus = biz.ReconciliationStatusRequired },
		func(p *db.Payment) { p.PayChannel = "alipay:wap" },
		func(p *db.Payment) { p.PayChannel = "wechat:native" },
	} {
		p := base
		mutate(&p)
		require.False(t, unsupportedPaymentNeverDispatched(p), "%+v", p)
	}
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	failed := base
	failed.Status = biz.PaymentStatusFailed
	q.EXPECT().MarkPaymentFailed(gomock.Any(), gomock.Any()).Return(failed, nil)
	d := newTestData(t, q, miniredis.RunT(t))
	payments := []db.Payment{base}
	require.NoError(t, recoverUnsupportedPayments(context.Background(), q, d, log.NewHelper(log.DefaultLogger), payments))
	require.Equal(t, biz.PaymentStatusFailed, payments[0].Status)
}

type starvingReaperMQ struct {
	biz.PaymentMQRepo
	ids []int64
}

func (m *starvingReaperMQ) EnqueueExpireOrder(_ context.Context, args biz.ExpireOrderArgs, _ time.Time) (*biz.MQJob, error) {
	m.ids = append(m.ids, args.OrderID)
	return &biz.MQJob{ID: args.OrderID, Deduplicated: args.OrderID <= 100}, nil
}
func TestOverdueSweepAdvancesBeyondHundredReconcilingOrders(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	first := make([]int64, 100)
	for i := range first {
		first[i] = int64(i + 1)
	}
	gomock.InOrder(
		q.EXPECT().ListOverduePendingOrders(gomock.Any(), db.ListOverduePendingOrdersParams{GraceSeconds: 300, LimitRows: 100, AfterID: 0}).Return(first, nil),
		q.EXPECT().ListOverduePendingOrders(gomock.Any(), db.ListOverduePendingOrdersParams{GraceSeconds: 300, LimitRows: 100, AfterID: 100}).Return([]int64{101}, nil),
	)
	d := newTestData(t, q, miniredis.RunT(t))
	mq := &starvingReaperMQ{}
	ids, err := NewOrderExpiryRepo(d, testTxManager{q: q}, mq, log.DefaultLogger).ReapOverdueOrders(context.Background(), 5*time.Minute, 100)
	require.NoError(t, err)
	require.Equal(t, []int64{101}, ids)
	require.Len(t, mq.ids, 101)
}

func TestAlipayMissingTradeMustUseOriginalAccountAndEnvironment(t *testing.T) {
	adapter := testAlipayAdapter(t, http.StatusBadRequest, `{"code":"ACQ.TRADE_NOT_EXIST"}`)
	original := adapter.providerAccount()
	for _, mismatch := range []string{"app", "environment", "authorization"} {
		t.Run(mismatch, func(t *testing.T) {
			app, prod, token := adapter.client.AppId, adapter.client.IsProd, adapter.client.AppAuthToken
			defer func() { adapter.client.AppId, adapter.client.IsProd, adapter.client.AppAuthToken = app, prod, token }()
			switch mismatch {
			case "app":
				adapter.client.AppId = "other_app"
			case "environment":
				adapter.client.IsProd = !prod
			case "authorization":
				adapter.client.AppAuthToken = "other_account_token"
			}
			_, err := adapter.Query(context.Background(), biz.PaymentQueryRequest{OutTradeNo: "pay_1", ExpectedProviderAccount: original})
			require.ErrorContains(t, err, "account or environment")
			require.NotErrorIs(t, err, biz.ErrProviderOrderNotExist)
		})
	}
}
