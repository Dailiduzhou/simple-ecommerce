package data

import (
	"context"
	"fmt"

	"github.com/go-pay/gopay"
	alipayv3 "github.com/go-pay/gopay/alipay/v3"
)

// alipayCodeTradeNotExist:查询接口的"交易不存在"错误码,
// 代表渠道侧从未建单或订单已被清理。
const alipayCodeTradeNotExist = "ACQ.TRADE_NOT_EXIST"

// alipayCodeAlreadyClosed 是 gopay v3 ErrResponse.Code 中可幂等视为已关单的终态码。
var alipayCodeAlreadyClosed = map[string]struct{}{
	"ACQ.TRADE_ALREADY_CLOSED": {},
	"ACQ.REASON_TRADE_CLOSED":  {},
}

// alipayTradeRequester keeps the adapter testable while production delegates
// directly to gopay's built-in v3 trade methods.
type alipayTradeRequester interface {
	TradeClose(context.Context, gopay.BodyMap) (*alipayv3.TradeCloseRsp, error)
	TradeRefund(context.Context, gopay.BodyMap) (*alipayv3.TradeRefundRsp, error)
}

func alipayDefinitiveRefundRejection(code string) bool {
	switch code {
	case "ACQ.TRADE_NOT_EXIST", "ACQ.REFUND_AMT_NOT_EQUAL_TOTAL", "ACQ.REASON_TRADE_REFUND_FEE_ERR", "ACQ.TRADE_HAS_FINISHED":
		return true
	default:
		return false
	}
}

// Account/environment binding is persisted with locally signed actions so a
// later configuration switch cannot turn a paid trade into "not exist".
func (a *AlipayPaymentAdapter) providerAccount() string {
	return fmt.Sprintf("%s:%t:%s", a.client.AppId, a.client.IsProd, sha256Hex([]byte(a.client.AppAuthToken)))
}
