package data

import (
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func canonicalPaymentResultFixture() (db.Payment, biz.PaymentMethod, *biz.PaymentQueryResult) {
	method := biz.PaymentMethod{Provider: "wechat", Product: "native"}
	payment := db.Payment{ID: 1, AmountMinor: 10000, Currency: "CNY", PayChannel: method.String(), OutTradeNo: pgtype.Text{String: "pay_1", Valid: true}}
	result := &biz.PaymentQueryResult{Method: method, OutTradeNo: "pay_1", TransactionID: "tx_1", TradeState: biz.TradeStateSuccess, Amount: 10000, Currency: "CNY"}
	return payment, method, result
}

func TestValidateProviderResult_AcceptsAllTrustedFields(t *testing.T) {
	payment, method, result := canonicalPaymentResultFixture()
	require.Empty(t, validateProviderResult(payment, method, result))
}

func TestValidateProviderResult_RejectsAmountTampering(t *testing.T) {
	payment, method, result := canonicalPaymentResultFixture()
	result.Amount = 1
	require.Equal(t, "amount mismatch", validateProviderResult(payment, method, result))
}

func TestValidateProviderResult_RejectsIdentifierMethodAndCurrencyMismatch(t *testing.T) {
	payment, method, result := canonicalPaymentResultFixture()
	result.OutTradeNo = "other"
	require.Equal(t, "out_trade_no mismatch", validateProviderResult(payment, method, result))
	result.OutTradeNo = "pay_1"
	result.Method = biz.PaymentMethod{Provider: "alipay", Product: "wap"}
	require.Equal(t, "payment method mismatch", validateProviderResult(payment, method, result))
	result.Method = method
	result.Currency = "USD"
	require.Equal(t, "currency mismatch", validateProviderResult(payment, method, result))
}
