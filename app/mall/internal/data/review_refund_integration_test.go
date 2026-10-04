//go:build integration

package data

import (
	"fmt"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	malljob "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/job"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"
)

func TestReviewRefundPurposesAndCacheIntegration(t *testing.T) {
	for _, queryRecovery := range []bool{false, true} {
		for _, purpose := range []biz.RefundPurpose{biz.RefundOrderCancel, biz.RefundLate, biz.RefundDuplicate} {
			t.Run(fmt.Sprintf("%s/query=%t", purpose, queryRecovery), func(t *testing.T) {
				f := newCorrectnessFixture(t)
				orderID, paymentID, outTradeNo := f.seedPayment(t, biz.PaymentStatusSuccess)
				orderStatus, initialStock, paidID := biz.OrderStatusPaid, 99, paymentID
				if purpose == biz.RefundLate {
					orderStatus, initialStock, paidID = biz.OrderStatusCancelled, 100, 0
				}
				if purpose == biz.RefundDuplicate {
					require.NoError(t, f.pool.QueryRow(f.ctx, `
						INSERT INTO payments (order_id,user_id,merchant_id,amount_minor,status,pay_channel,out_trade_no,currency)
						VALUES ($1,$2,0,12345,'success','wechat:native',$3,'CNY') RETURNING id`,
						orderID, f.userID, outTradeNo+"_primary").Scan(&paidID))
				}
				_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status=$2::varchar, is_completed=($2::varchar='cancelled'), paid_payment_id=NULLIF($3::bigint,0) WHERE id=$1`, orderID, orderStatus, paidID)
				require.NoError(t, err)
				_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items (order_id,product_id,quantity,unit_price_minor,product_name_snapshot) VALUES ($1,$2,1,12345,'snapshot')`, orderID, f.productID)
				require.NoError(t, err)
				_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=$2 WHERE id=$1`, f.productID, initialStock)
				require.NoError(t, err)
				products := NewProductRepo(f.data, f.tx, log.DefaultLogger)
				orders := NewOrderRepo(f.data, f.tx, log.DefaultLogger)
				payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
				// Warm entity, category, all-products and both order-list projections.
				_, err = products.GetProduct(f.ctx, f.productID)
				require.NoError(t, err)
				_, err = products.ListProducts(f.ctx, 20, 0)
				require.NoError(t, err)
				_, err = products.ListProductsByCategory(f.ctx, f.categoryID, 20, 0)
				require.NoError(t, err)
				_, err = orders.ListOrdersByUser(f.ctx, f.userID, 20, 0)
				require.NoError(t, err)
				_, err = orders.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
				require.NoError(t, err)
				_, refund, err := payments.PreparePaymentRefund(f.ctx, paymentID, outTradeNo+"_refund")
				require.NoError(t, err)
				require.Equal(t, purpose, refund.Purpose)
				for replay := 0; replay < 2; replay++ {
					if queryRecovery {
						err = payments.ApplyPayQuery(f.ctx, biz.CheckPayArgs{PaymentID: paymentID, Provider: "wechat"}, &biz.PaymentQueryResult{
							Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: outTradeNo,
							TradeState: biz.TradeStateRefund, Amount: 12345, Currency: "CNY",
						})
					} else {
						err = payments.ApplyPaymentRefund(f.ctx, paymentID, refund.ID)
					}
					require.NoError(t, err)
				}
				wantStock := initialStock
				if purpose == biz.RefundOrderCancel {
					orderStatus, wantStock = biz.OrderStatusRefunded, 100
				}
				var stock int
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
				require.Equal(t, wantStock, stock)
				product, err := products.GetProduct(f.ctx, f.productID)
				require.NoError(t, err)
				require.EqualValues(t, wantStock, product.Stock)
				for _, byCategory := range []bool{false, true} {
					var page []biz.Product
					if byCategory {
						page, err = products.ListProductsByCategory(f.ctx, f.categoryID, 20, 0)
					} else {
						page, err = products.ListProducts(f.ctx, 20, 0)
					}
					require.NoError(t, err)
					require.Len(t, page, 1)
					require.EqualValues(t, wantStock, page[0].Stock)
				}
				order, err := orders.GetOrder(f.ctx, orderID)
				require.NoError(t, err)
				require.Equal(t, orderStatus, order.Status)
				page, err := orders.ListOrdersByUser(f.ctx, f.userID, 20, 0)
				require.NoError(t, err)
				require.Equal(t, orderStatus, page[0].Status)
				ongoing, err := orders.ListOngoingOrdersByUser(f.ctx, f.userID, 20, 0)
				require.NoError(t, err)
				if purpose == biz.RefundDuplicate {
					require.Len(t, ongoing, 1)
					primary, err := payments.GetPayment(f.ctx, paidID)
					require.NoError(t, err)
					require.Equal(t, biz.PaymentStatusSuccess, primary.Status)
				} else {
					require.Empty(t, ongoing)
				}
				payment, err := payments.GetPayment(f.ctx, paymentID)
				require.NoError(t, err)
				require.Equal(t, biz.PaymentStatusRefunded, payment.Status)
				var refundStatus string
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM order_refunds WHERE id=$1`, refund.ID).Scan(&refundStatus))
				require.Equal(t, biz.PaymentRefundStatusSuccess, refundStatus)
			})
		}
	}
}

func TestReviewExpiryReconciliationCommitsBeforeBusinessErrorIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusRefunded)
	_, err := f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=now()-interval '1 hour' WHERE id=$1`, orderID)
	require.NoError(t, err)
	repo := NewOrderExpiryRepo(f.data, f.tx, nil, log.DefaultLogger)
	for attempt := 0; attempt < 2; attempt++ {
		require.ErrorIs(t, repo.ExpireOrder(f.ctx, orderID), biz.ErrPaymentReconciliationRequired)
	}
	// Exercise the worker against the same persisted anomaly: it must defer
	// review without losing the database facts or consuming technical retries.
	err = malljob.NewExpireOrderWorker(repo).Work(f.ctx, &river.Job[biz.ExpireOrderArgs]{Args: biz.ExpireOrderArgs{OrderID: orderID}})
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, err, &snooze)
	require.Equal(t, 5*time.Minute, snooze.Duration)
	var status, reason string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT reconciliation_status, reconciliation_reason FROM payments WHERE id=$1`, paymentID).Scan(&status, &reason))
	require.Equal(t, biz.ReconciliationStatusRequired, status)
	require.Equal(t, "refunded_on_pending_order", reason)
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM payment_reconciliation_failures WHERE payment_id=$1`, paymentID).Scan(&count))
	require.Equal(t, 1, count)
}
