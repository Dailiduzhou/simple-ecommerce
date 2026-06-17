package data

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
	"github.com/go-pay/gopay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAlipayCloseRequester 替换 alipayv3.ClientV3.DoAliPayAPISelfV3 的实现。
// 一次构造可复用多次;close 收到的 path/method/body 被记录下来便于断言。
type fakeAlipayCloseRequester struct {
	resp     alipayCloseRsp
	httpCode int
	err      error

	gotPath   string
	gotMethod string
	gotBody   gopay.BodyMap
}

func (f *fakeAlipayCloseRequester) DoAliPayAPISelfV3(_ context.Context, method, path string, bm gopay.BodyMap, rsp any) (*http.Response, error) {
	f.gotPath = path
	f.gotMethod = method
	f.gotBody = bm
	if f.err != nil {
		return nil, f.err
	}
	*rsp.(*alipayCloseRsp) = f.resp
	return &http.Response{StatusCode: f.httpCode}, nil
}

// nopClientV3 仅用来满足构造器签名;CloseOrder 的 client-nil 分支不走
// closeRequester,其它分支靠 fakeRequester 注入。
func nopClientV3() *alipayv3.ClientV3 { return &alipayv3.ClientV3{} }

func newAdapterForTest(cr alipayCloseRequester) *AlipayPaymentAdapter {
	return newAlipayPaymentAdapterForTest(nopClientV3(), cr, log.DefaultLogger)
}

func TestAlipayPaymentAdapter_CloseOrder_NotConfigured(t *testing.T) {
	a := newAlipayPaymentAdapterForTest(nil, nil, log.DefaultLogger)
	_, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-1"})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "ALIPAY_NOT_CONFIGURED"),
		"expected ALIPAY_NOT_CONFIGURED, got %v", err)
}

func TestAlipayPaymentAdapter_CloseOrder_OrderIDRequired(t *testing.T) {
	cr := &fakeAlipayCloseRequester{}
	a := newAdapterForTest(cr)
	_, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ALIPAY_ORDER_ID_REQUIRED")
	assert.Equal(t, "", cr.gotPath, "requester must not be called when params invalid")
}

func TestAlipayPaymentAdapter_CloseOrder_BusinessSuccess(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		httpCode: http.StatusOK,
		resp: alipayCloseRsp{
			Code:       "10000",
			OutTradeNo: "otn-1",
			TradeNo:    "20240616000001",
		},
	}
	a := newAdapterForTest(cr)
	got, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-1"})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Success)
	assert.Equal(t, "otn-1", got.OutTradeNo)
	assert.Equal(t, "20240616000001", got.TransactionID)
	assert.Equal(t, "10000", got.RawCode)
	assert.Equal(t, "", got.RawSubCode)

	// 入参透传
	assert.Equal(t, alipayTradeClosePath, cr.gotPath)
	assert.Equal(t, alipayv3.MethodPost, cr.gotMethod)
	assert.Equal(t, "otn-1", cr.gotBody.GetString("out_trade_no"))
}

func TestAlipayPaymentAdapter_CloseOrder_BusinessFail_OrderNotExist(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		httpCode: http.StatusOK,
		resp: alipayCloseRsp{
			Code:    "40004",
			SubCode: "ACQ.TRADE_NOT_EXIST",
			SubMsg:  "交易不存在",
		},
	}
	a := newAdapterForTest(cr)
	got, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-missing"})
	require.Error(t, err)
	require.NotNil(t, got, "选项 A: 业务失败时仍返回 result,err 仅作信号")
	assert.False(t, got.Success)
	assert.Equal(t, "ACQ.TRADE_NOT_EXIST", got.RawSubCode)
	assert.Equal(t, "40004", got.RawCode)
	assert.Contains(t, err.Error(), "ACQ.TRADE_NOT_EXIST")
}

func TestAlipayPaymentAdapter_CloseOrder_IdempotentSuccess_StatusError(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		httpCode: http.StatusOK,
		resp: alipayCloseRsp{
			Code:    "40004",
			SubCode: "ACQ.TRADE_STATUS_ERROR",
			SubMsg:  "交易状态不合法",
		},
	}
	a := newAdapterForTest(cr)
	got, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-paid"})
	require.NoError(t, err, "幂等成功不应返回 err")
	require.NotNil(t, got)
	assert.True(t, got.Success, "ACQ.TRADE_STATUS_ERROR 必须视为幂等成功")
	assert.Equal(t, "ACQ.TRADE_STATUS_ERROR", got.RawSubCode)
}

func TestAlipayPaymentAdapter_CloseOrder_IdempotentSuccess_AlreadyClosed(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		httpCode: http.StatusOK,
		resp: alipayCloseRsp{
			Code:    "40004",
			SubCode: "ACQ.TRADE_ALREADY_CLOSED",
		},
	}
	a := newAdapterForTest(cr)
	got, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-already"})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Success)
	assert.Equal(t, "ACQ.TRADE_ALREADY_CLOSED", got.RawSubCode)
}

func TestAlipayPaymentAdapter_CloseOrder_HTTPError(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		httpCode: http.StatusInternalServerError,
		resp: alipayCloseRsp{
			Code:    "20000",
			SubCode: "SYSTEM_ERROR",
			Msg:     "system error",
		},
	}
	a := newAdapterForTest(cr)
	got, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-1"})
	require.Error(t, err)
	require.NotNil(t, got)
	assert.False(t, got.Success)
	assert.Equal(t, "20000", got.RawCode)
	assert.Contains(t, err.Error(), "http 500")
}

func TestAlipayPaymentAdapter_CloseOrder_TransportError(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		err: errors.New("dial tcp: i/o timeout"),
	}
	a := newAdapterForTest(cr)
	got, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{OutTradeNo: "otn-1"})
	require.Error(t, err)
	assert.Nil(t, got, "transport 错误时不返回 result,直接 propagate")
	assert.Contains(t, err.Error(), "i/o timeout")
}

func TestAlipayPaymentAdapter_CloseOrder_PassesTradeNo(t *testing.T) {
	cr := &fakeAlipayCloseRequester{
		httpCode: http.StatusOK,
		resp:     alipayCloseRsp{Code: "10000", OutTradeNo: "otn-x", TradeNo: "2024xxx"},
	}
	a := newAdapterForTest(cr)
	_, err := a.CloseOrder(context.Background(), biz.PaymentCloseRequest{
		OutTradeNo:    "otn-x",
		TransactionID: "2024xxx",
	})
	require.NoError(t, err)
	assert.Equal(t, "otn-x", cr.gotBody.GetString("out_trade_no"))
	assert.Equal(t, "2024xxx", cr.gotBody.GetString("trade_no"))
}
