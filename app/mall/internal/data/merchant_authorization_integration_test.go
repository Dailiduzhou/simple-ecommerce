//go:build integration

package data

import (
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMerchantOwnershipIntegration pins the single-merchant order invariant:
// checkout derives the merchant from the products, rejects mixed carts, and
// dispatch is scoped to that merchant rather than to any merchant.
func TestMerchantOwnershipIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	var otherMerchant int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO users (nickname, phone_hash, phone_encrypt, password_hash, role)
		VALUES ('other-merchant', $1, 'ciphertext', 'hash', 'merchant') RETURNING id`,
		f.prefix+"_other_merchant").Scan(&otherMerchant))
	t.Cleanup(func() {
		// This cleanup runs before the fixture cleanup (LIFO), so remove the
		// merchant's products before the account that owns them.
		_, err := f.pool.Exec(f.ctx, `DELETE FROM products WHERE merchant_id=$1`, otherMerchant)
		require.NoError(t, err)
		_, err = f.pool.Exec(f.ctx, `DELETE FROM users WHERE id=$1`, otherMerchant)
		require.NoError(t, err)
	})
	products := NewProductRepo(f.data, f.tx, log.DefaultLogger)
	other, err := products.CreateProduct(f.ctx, otherMerchant, f.categoryID, "other product",
		decimal.RequireFromString("12.34"), decimal.NewFromInt(1), 10, 1, nil, nil, "")
	require.NoError(t, err)

	repo := NewOrderRepoWithJobs(f.data, f.tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), integrationAuthConfig(), log.DefaultLogger)
	mixed := biz.CreateOrderArgs{
		UserID: f.userID, AddressID: f.addressID, OutTradeNo: f.prefix + "_mixed",
		Currency: "CNY", IdempotencyKey: f.prefix + "_mixed", RequestHash: "mixed",
		Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}, {ProductID: other.ID, Quantity: 1}},
	}
	_, err = repo.CreateOrder(f.ctx, mixed)
	require.ErrorIs(t, err, biz.ErrOrderMultiMerchant)

	order, err := repo.CreateOrder(f.ctx, biz.CreateOrderArgs{
		UserID: f.userID, AddressID: f.addressID, OutTradeNo: f.prefix + "_single",
		Currency: "CNY", IdempotencyKey: f.prefix + "_single", RequestHash: "single",
		Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}},
	})
	require.NoError(t, err)
	require.Equal(t, f.merchantID, order.MerchantID)

	// Pay the order so dispatch is legal.
	var paymentID int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO payments (order_id,user_id,merchant_id,amount_minor,status,pay_channel,out_trade_no,currency)
		VALUES ($1,$2,$3,$4,'success','wechat:native',$5,'CNY') RETURNING id`,
		order.ID, f.userID, f.merchantID, order.TotalAmount, f.prefix+"_merchant_pay").Scan(&paymentID))
	_, err = f.pool.Exec(f.ctx, `UPDATE orders SET status='paid', paid_payment_id=$2 WHERE id=$1`, order.ID, paymentID)
	require.NoError(t, err)

	ship := shipmentInput(order.ID)
	_, err = repo.ApplyFulfillment(f.ctx, biz.Actor{ID: otherMerchant, Role: biz.RoleMerchant}, ship)
	require.ErrorIs(t, err, biz.ErrOrderNotFound, "another merchant must not dispatch this order")
	_, err = repo.ApplyFulfillment(f.ctx, biz.Actor{ID: f.userID, Role: biz.RoleUser}, ship)
	require.Equal(t, int32(403), kerrors.FromError(err).Code, "buyers cannot dispatch")
	_, err = repo.ApplyFulfillment(f.ctx, biz.Actor{ID: f.merchantID, Role: biz.RoleMerchant}, ship)
	require.NoError(t, err)
}

func TestMerchantPaymentCarriesMerchantIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
	var merchantID int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT merchant_id FROM payments WHERE id=$1`, paymentID).Scan(&merchantID))
	require.Equal(t, f.merchantID, merchantID)

	payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	owned, err := payments.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	require.Equal(t, orderID, owned.OrderID)
	require.Equal(t, f.merchantID, owned.MerchantID)
}
