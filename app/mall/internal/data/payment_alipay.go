package data

import (
	"context"

	"github.com/go-pay/gopay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
)

// alipayCodeTradeNotExist:查询接口的"交易不存在"错误码,
// 代表渠道侧从未建单或订单已被清理。
const alipayCodeTradeNotExist = "ACQ.TRADE_NOT_EXIST"

// alipayCodeAlreadyClosed 是 gopay v3 ErrResponse.Code 中可幂等视为已关单的终态码。
var alipayCodeAlreadyClosed = map[string]struct{}{
	"ACQ.TRADE_STATUS_ERROR":   {},
	"ACQ.TRADE_ALREADY_CLOSED": {},
	"ACQ.REASON_TRADE_CLOSED":  {},
}

// alipayTradeRequester keeps the adapter testable while production delegates
// directly to gopay's built-in v3 trade methods.
type alipayTradeRequester interface {
	TradeClose(context.Context, gopay.BodyMap) (*alipayv3.TradeCloseRsp, error)
	TradeRefund(context.Context, gopay.BodyMap) (*alipayv3.TradeRefundRsp, error)
}
