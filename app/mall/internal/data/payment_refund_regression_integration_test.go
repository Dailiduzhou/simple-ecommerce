//go:build integration

package data

import (
	"fmt"
	"sync"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

// No provider calls: these tests model already-confirmed refunds and verify
// real row locking, atomic settlement, replay and inventory ownership.
func TestCompensatingRefundSettlementIntegration(t *testing.T) {
	for _, scenario := range []string{"duplicate", "late_cancelled", "late_refunded", "concurrent_duplicates"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
			initialStatus := biz.OrderStatusPaid
			initialStock := 99
			if scenario == "late_cancelled" {
				initialStatus = biz.OrderStatusCancelled
				initialStock = 100
			}
			if scenario == "late_refunded" {
				initialStatus = biz.OrderStatusRefunded
				initialStock = 100
			}
			_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status=$2,is_completed=$3 WHERE id=$1`, orderID, initialStatus, initialStatus != biz.OrderStatusPaid)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `UPDATE payments SET pay_channel='alipay:wap' WHERE id=$1`, paymentID)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items(order_id,product_id,quantity,unit_price_minor,product_name_snapshot) VALUES($1,$2,1,12345,'snapshot')`, orderID, f.productID)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=$2 WHERE id=$1`, f.productID, initialStock)
			require.NoError(t, err)
			var siblingID int64
			if initialStatus == biz.OrderStatusPaid {
				require.NoError(t, f.pool.QueryRow(f.ctx, `INSERT INTO payments(order_id,user_id,merchant_id,amount_minor,currency,status,pay_channel,out_trade_no)
     VALUES($1,$2,0,12345,'CNY','success','alipay:wap',$3) RETURNING id`, orderID, f.userID, f.prefix+"_second").Scan(&siblingID))
			}
			repo := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			_, refund, err := repo.PreparePaymentRefund(f.ctx, paymentID, f.prefix+"_refund")
			require.NoError(t, err)
			expectedStatus := initialStatus
			expectedStock := initialStock
			if scenario == "concurrent_duplicates" {
				_, second, err := repo.PreparePaymentRefund(f.ctx, siblingID, f.prefix+"_second_refund")
				require.NoError(t, err)
				start := make(chan struct{})
				results := make(chan error, 2)
				var wg sync.WaitGroup
				for _, pair := range [][2]int64{{paymentID, refund.ID}, {siblingID, second.ID}} {
					wg.Add(1)
					go func(p [2]int64) { defer wg.Done(); <-start; results <- repo.ApplyPaymentRefund(f.ctx, p[0], p[1]) }(pair)
				}
				close(start)
				wg.Wait()
				close(results)
				for err := range results {
					require.NoError(t, err)
				}
				expectedStatus = biz.OrderStatusRefunded
				expectedStock = 100
				require.NoError(t, repo.ApplyPaymentRefund(f.ctx, siblingID, second.ID))
			} else {
				require.NoError(t, repo.ApplyPaymentRefund(f.ctx, paymentID, refund.ID))
			}
			// A replay never restores the same reservation again.
			require.NoError(t, repo.ApplyPaymentRefund(f.ctx, paymentID, refund.ID))
			var status string
			var stock int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status))
			require.Equal(t, expectedStatus, status)
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
			require.Equal(t, expectedStock, stock)
			for _, entry := range []struct {
				table    string
				id       int64
				expected string
			}{{"payments", paymentID, biz.PaymentStatusRefunded}, {"order_refunds", refund.ID, biz.PaymentRefundStatusSuccess}} {
				require.NoError(t, f.pool.QueryRow(f.ctx, fmt.Sprintf(`SELECT status FROM %s WHERE id=$1`, entry.table), entry.id).Scan(&status))
				require.Equal(t, entry.expected, status)
			}
		})
	}
}
