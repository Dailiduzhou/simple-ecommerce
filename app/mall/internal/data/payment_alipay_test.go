package data

import (
	"context"
	stderrors "errors"
	"net/http"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-pay/gopay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/stretchr/testify/require"
)

type fakeAlipayTradeRequester struct {
	closeBody   gopay.BodyMap
	closeRsp    *alipayv3.TradeCloseRsp
	closeErr    error
	refundBody  gopay.BodyMap
	refundRsp   *alipayv3.TradeRefundRsp
	refundErr   error
	hadDeadline bool
}

func (f *fakeAlipayTradeRequester) TradeClose(ctx context.Context, body gopay.BodyMap) (*alipayv3.TradeCloseRsp, error) {
	f.closeBody = body
	_, f.hadDeadline = ctx.Deadline()
	return f.closeRsp, f.closeErr
}

func (f *fakeAlipayTradeRequester) TradeRefund(ctx context.Context, body gopay.BodyMap) (*alipayv3.TradeRefundRsp, error) {
	f.refundBody = body
	_, f.hadDeadline = ctx.Deadline()
	return f.refundRsp, f.refundErr
}

func TestAlipayMoneyConversionUsesExactMinorUnits(t *testing.T) {
	require.Equal(t, "100.00", fenToYuan(10000))
	amount, err := yuanToFen("100.00")
	require.NoError(t, err)
	require.Equal(t, int64(10000), amount)
	_, err = yuanToFen("0.001")
	require.Error(t, err)
}

func TestMapAlipayTradeState(t *testing.T) {
	state, _ := mapAlipayTradeState("TRADE_SUCCESS")
	require.Equal(t, biz.TradeStateSuccess, state)
	state, _ = mapAlipayTradeState("WAIT_BUYER_PAY")
	require.Equal(t, biz.TradeStateNotPay, state)
	state, _ = mapAlipayTradeState("TRADE_CLOSED")
	require.Equal(t, biz.TradeStateClosed, state)
}

func TestAlipayCloseUsesGopayTradeClose(t *testing.T) {
	requester := &fakeAlipayTradeRequester{closeRsp: &alipayv3.TradeCloseRsp{
		StatusCode: http.StatusOK, TradeNo: "trade_1", OutTradeNo: "payment_1",
	}}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Close(context.Background(), biz.PaymentCloseRequest{
		Method:     biz.PaymentMethod{Provider: "alipay", Product: "wap"},
		OutTradeNo: "payment_1", TransactionID: "trade_1",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, requester.hadDeadline)
	require.Equal(t, "payment_1", requester.closeBody.GetString("out_trade_no"))
	require.Equal(t, "trade_1", requester.closeBody.GetString("trade_no"))
}

func TestAlipayCloseTreatsAlreadyClosedCodeAsSuccess(t *testing.T) {
	requester := &fakeAlipayTradeRequester{closeRsp: &alipayv3.TradeCloseRsp{
		StatusCode:  http.StatusBadRequest,
		ErrResponse: alipayv3.ErrResponse{Code: "ACQ.TRADE_ALREADY_CLOSED"},
	}}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Close(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "payment_1"})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "ACQ.TRADE_ALREADY_CLOSED", result.RawCode)
}

func TestAlipayClosePropagatesGopayError(t *testing.T) {
	requester := &fakeAlipayTradeRequester{closeErr: stderrors.New("verify response signature")}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Close(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "payment_1"})
	require.Error(t, err)
	require.Nil(t, result)
}

func TestAlipayRefundUsesGopayTradeRefundAndExactAmount(t *testing.T) {
	requester := &fakeAlipayTradeRequester{refundRsp: &alipayv3.TradeRefundRsp{
		StatusCode: http.StatusOK, TradeNo: "trade_1", OutTradeNo: "payment_1",
		FundChange: "Y", RefundFee: "100.00",
	}}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Refund(context.Background(), biz.PaymentRefundRequest{
		Method:     biz.PaymentMethod{Provider: "alipay", Product: "wap"},
		OutTradeNo: "payment_1", TransactionID: "trade_1", OutRefundNo: "refund_1",
		Amount: 10000, Currency: "CNY",
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, result.FundChanged)
	require.Equal(t, int64(10000), result.Amount)
	require.Equal(t, "100.00", requester.refundBody.GetString("refund_amount"))
	require.Equal(t, "refund_1", requester.refundBody.GetString("out_request_no"))
	require.True(t, requester.hadDeadline)
}

func TestAlipayRefundRejectsResponseMismatch(t *testing.T) {
	requester := &fakeAlipayTradeRequester{refundRsp: &alipayv3.TradeRefundRsp{
		StatusCode: http.StatusOK, OutTradeNo: "different", RefundFee: "99.99",
	}}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Refund(context.Background(), biz.PaymentRefundRequest{
		OutTradeNo: "payment_1", OutRefundNo: "refund_1", Amount: 10000, Currency: "CNY",
	})
	require.ErrorContains(t, err, "out_trade_no mismatch")
	require.False(t, result.Success)
}

func TestAlipayRefundReturnsProviderCode(t *testing.T) {
	requester := &fakeAlipayTradeRequester{refundRsp: &alipayv3.TradeRefundRsp{
		StatusCode:  http.StatusBadRequest,
		ErrResponse: alipayv3.ErrResponse{Code: "ACQ.TRADE_NOT_EXIST", Message: "trade not found"},
	}}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Refund(context.Background(), biz.PaymentRefundRequest{
		OutTradeNo: "payment_1", OutRefundNo: "refund_1", Amount: 10000, Currency: "CNY",
	})
	require.Error(t, err)
	require.Equal(t, "ACQ.TRADE_NOT_EXIST", result.RawCode)
}

func TestAlipayRefundKeepsUncertainGopayErrorRetryable(t *testing.T) {
	requester := &fakeAlipayTradeRequester{
		refundRsp: &alipayv3.TradeRefundRsp{StatusCode: http.StatusOK, OutTradeNo: "payment_1"},
		refundErr: stderrors.New("verify response signature"),
	}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	result, err := adapter.Refund(context.Background(), biz.PaymentRefundRequest{
		OutTradeNo: "payment_1", OutRefundNo: "refund_1", Amount: 10000, Currency: "CNY",
	})
	require.Error(t, err)
	require.NotNil(t, result)
	require.Empty(t, result.RawCode)
}

func TestAlipayRefundTimeoutIsBounded(t *testing.T) {
	requester := &fakeAlipayTradeRequester{refundErr: context.DeadlineExceeded}
	adapter := newAlipayPaymentAdapterForTest(nil, requester, log.DefaultLogger)
	started := time.Now()
	_, _ = adapter.Refund(context.Background(), biz.PaymentRefundRequest{
		OutTradeNo: "payment_1", OutRefundNo: "refund_1", Amount: 10000,
	})
	require.True(t, requester.hadDeadline)
	require.Less(t, time.Since(started), time.Second)
}
