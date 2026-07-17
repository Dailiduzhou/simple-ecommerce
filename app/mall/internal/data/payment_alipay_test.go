package data

import (
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/stretchr/testify/require"
)

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
