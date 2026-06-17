package data

import (
	"context"
	"net/http"

	"github.com/go-pay/gopay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
)

// alipayTradeClosePath 是 alipay.trade.close 的 v3 路径。
// gopay 内部 v3TradeClose 常量未导出(constant.go:36),需要保持字符串一致;
// 升级 gopay 时务必核对 openapi 文档或该库的 constant.go。
const alipayTradeClosePath = "/v3/alipay/trade/close"

const alipaySuccessCode = "10000"

// alipaySubCodeAlreadyClosed 是"订单已经是终态(已关/已付)"的业务错误码白名单,
// 关单接口在这些情况下应当幂等视为成功,避免重复关单。
//  - ACQ.TRADE_STATUS_ERROR:   订单状态不允许关闭(已支付/已关闭)
//  - ACQ.TRADE_ALREADY_CLOSED: 显式已关闭(部分版本)
//  - ACQ.REASON_TRADE_CLOSED:  兼容老版本
var alipaySubCodeAlreadyClosed = map[string]struct{}{
	"ACQ.TRADE_STATUS_ERROR":   {},
	"ACQ.TRADE_ALREADY_CLOSED": {},
	"ACQ.REASON_TRADE_CLOSED":  {},
}

// alipayCloseRsp 完整接住 alipay.trade.close 的响应字段。
// 复用 alipayv3.ErrResponse 拿 HTTP 非 200 时的 code/message,
// 自己补齐业务层字段(code/sub_code/msg/sub_msg/trade_no/out_trade_no)。
type alipayCloseRsp struct {
	ErrResponse alipayv3.ErrResponse `json:"-"`

	Code       string `json:"code"`
	SubCode    string `json:"sub_code"`
	Msg        string `json:"msg"`
	SubMsg     string `json:"sub_msg"`
	Action     string `json:"action,omitempty"`
	TradeNo    string `json:"trade_no"`
	OutTradeNo string `json:"out_trade_no"`
}

// alipayCloseRequester 是 *alipayv3.ClientV3 的 close 调用注入点。
// 默认实现直接调 DoAliPayAPISelfV3;测试时换成返回固定响应的 fake。
type alipayCloseRequester interface {
	DoAliPayAPISelfV3(ctx context.Context, method, path string, bm gopay.BodyMap, aliRsp any) (*http.Response, error)
}

type defaultAlipayCloseRequester struct {
	client *alipayv3.ClientV3
}

func (d *defaultAlipayCloseRequester) DoAliPayAPISelfV3(ctx context.Context, method, path string, bm gopay.BodyMap, aliRsp any) (*http.Response, error) {
	return d.client.DoAliPayAPISelfV3(ctx, method, path, bm, aliRsp)
}
