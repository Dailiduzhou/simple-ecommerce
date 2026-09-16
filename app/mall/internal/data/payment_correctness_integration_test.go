//go:build integration

package data

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	malljob "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/job"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

func TestLastStockIdempotencyIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	_, err := f.pool.Exec(f.ctx, `UPDATE products SET stock = 1 WHERE id = $1`, f.productID)
	require.NoError(t, err)
	repo := NewOrderRepoWithJobs(f.data, f.tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
	start := make(chan struct{})
	type outcome struct {
		order biz.Order
		err   error
	}
	results := make(chan outcome, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			order, err := repo.CreateOrder(f.ctx, biz.CreateOrderArgs{UserID: f.userID, AddressID: f.addressID, Currency: "CNY",
				OutTradeNo: fmt.Sprintf("%s_order_%d", f.prefix, i), IdempotencyKey: f.prefix + "_key", RequestHash: f.prefix + "_hash",
				ExpiresAt: time.Now().Add(30 * time.Minute), Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}}})
			results <- outcome{order, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner int64
	for result := range results {
		require.NoError(t, result.err)
		if winner == 0 {
			winner = result.order.ID
		}
		require.Equal(t, winner, result.order.ID)
	}
	var stock, count int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id = $1`, f.productID).Scan(&stock))
	require.Zero(t, stock)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, f.userID).Scan(&count))
	require.Equal(t, int64(1), count)
}

var simulatedRefundCrash = errors.New("crash before refund settlement commit")

type crashRefundRepo struct{ biz.PaymentRepo }

func (r crashRefundRepo) ApplyPaymentRefund(context.Context, int64, int64) error {
	return simulatedRefundCrash
}

type integrationRefundIDs struct{ biz.IDGenerator }

func (integrationRefundIDs) GenerateOrderNo64(string, int64) string { return "unused_retry_number" }

type integrationRefundGateway struct {
	biz.PaymentGateway
	f         *correctnessFixture
	t         *testing.T
	number    string
	calls     int
	transfers int
}

func (g *integrationRefundGateway) Capabilities(biz.PaymentMethod) (biz.PaymentCapabilities, error) {
	return biz.PaymentCapabilities{SupportsRefund: true}, nil
}
func (g *integrationRefundGateway) Refund(ctx context.Context, req biz.PaymentRefundRequest) (*biz.PaymentRefundResult, error) {
	// Read from another pool connection: pending MUST already be committed.
	var status string
	require.NoError(g.t, g.f.pool.QueryRow(ctx, `SELECT status FROM order_refunds WHERE out_refund_no=$1`, req.OutRefundNo).Scan(&status))
	require.Equal(g.t, biz.PaymentRefundStatusPending, status)
	if g.number == "" {
		g.number = req.OutRefundNo
		g.transfers++
	}
	require.Equal(g.t, g.number, req.OutRefundNo)
	g.calls++
	return &biz.PaymentRefundResult{OutRefundNo: req.OutRefundNo, Amount: req.Amount, Currency: req.Currency, Success: true}, nil
}

func TestFailedRefundRetryCrashCompensationIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
	// seedPayment defaults to a pending order. A successful payment being
	// refunded must instead belong to a paid order with reserved stock.
	_, err := f.pool.Exec(f.ctx, `UPDATE orders SET status='paid' WHERE id=$1`, orderID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `INSERT INTO order_items (order_id, product_id, quantity, unit_price_minor, product_name_snapshot) VALUES ($1,$2,1,12345,'snapshot')`, orderID, f.productID)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=99 WHERE id=$1`, f.productID)
	require.NoError(t, err)
	repo := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
	_, refund, err := repo.PreparePaymentRefund(f.ctx, paymentID, f.prefix+"_original_refund")
	require.NoError(t, err)
	require.NoError(t, repo.RecordPaymentRefundError(f.ctx, refund.ID, "definitive rejection", true))
	gateway := &integrationRefundGateway{f: f, t: t}
	uc := biz.NewPaymentUsecase(gateway, crashRefundRepo{repo}, nil, nil, nil, f.tx, integrationRefundIDs{}, log.DefaultLogger)
	_, err = uc.RefundPayment(f.ctx, paymentID)
	require.ErrorIs(t, err, simulatedRefundCrash)
	require.Equal(t, refund.OutRefundNo, gateway.number)
	_, err = f.pool.Exec(f.ctx, `UPDATE order_refunds SET updated_at=now()-interval '1 hour' WHERE id=$1`, refund.ID)
	require.NoError(t, err)
	uc = biz.NewPaymentUsecase(gateway, repo, nil, nil, nil, f.tx, integrationRefundIDs{}, log.DefaultLogger)
	settled, err := uc.ReconcilePendingRefunds(f.ctx, time.Minute, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, settled, 1)
	require.Equal(t, 2, gateway.calls)
	require.Equal(t, 1, gateway.transfers, "same refund number must only move money once")
	var status string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM payments WHERE id=$1`, paymentID).Scan(&status))
	require.Equal(t, biz.PaymentStatusRefunded, status)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM order_refunds WHERE id=$1`, refund.ID).Scan(&status))
	require.Equal(t, biz.PaymentRefundStatusSuccess, status)
	var completed bool
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status,is_completed FROM orders WHERE id=$1`, orderID).Scan(&status, &completed))
	require.Equal(t, biz.OrderStatusRefunded, status)
	require.True(t, completed)
	var stock int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.EqualValues(t, 100, stock)
	// Replaying settlement must not restore the same stock twice.
	require.NoError(t, repo.ApplyPaymentRefund(f.ctx, paymentID, refund.ID))
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.EqualValues(t, 100, stock)
}

type integrationCloseGateway struct {
	biz.PaymentGateway
	payment *biz.PaymentDO
	queries int
	absent  bool
}

func (g *integrationCloseGateway) Capabilities(biz.PaymentMethod) (biz.PaymentCapabilities, error) {
	return biz.PaymentCapabilities{SupportsClose: true}, nil
}
func (g *integrationCloseGateway) Query(context.Context, biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	g.queries++
	if g.absent {
		return nil, biz.ErrProviderOrderNotExist
	}
	state := biz.TradeStateNotPay
	if g.queries > 1 {
		state = biz.TradeStateSuccess
	}
	return &biz.PaymentQueryResult{Method: biz.PaymentMethod{Provider: "alipay", Product: "app"}, OutTradeNo: g.payment.OutTradeNo,
		TransactionID: "tx_" + g.payment.OutTradeNo, Amount: g.payment.Amount, Currency: g.payment.Currency, TradeState: state}, nil
}
func (g *integrationCloseGateway) Close(context.Context, biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	return nil, biz.ErrProviderTradeStateConflict
}

func TestCloseRaceAndUnopenedAlipayStockIntegration(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprintf("absent=%t", absent), func(t *testing.T) {
			f := newCorrectnessFixture(t)
			orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusClosePending)
			_, err := f.pool.Exec(f.ctx, `INSERT INTO order_items (order_id, product_id, quantity, unit_price_minor, product_name_snapshot) VALUES ($1,$2,1,12345,'snapshot')`, orderID, f.productID)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `UPDATE products SET stock=99 WHERE id=$1`, f.productID)
			require.NoError(t, err)
			_, err = f.pool.Exec(f.ctx, `UPDATE payments SET pay_channel='alipay:app', prepay_attempts=1, action_type='invoke', action_payload=jsonb_build_object('payload','signed','provider_account','account_1','channel_expires_at',now()-interval '1 hour') WHERE id=$1`, paymentID)
			require.NoError(t, err)
			repo := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			payment, err := repo.GetPayment(f.ctx, paymentID)
			require.NoError(t, err)
			gateway := &integrationCloseGateway{payment: payment, absent: absent}
			worker := malljob.NewClosePayWorker(gateway, repo)
			job := &river.Job[biz.ClosePayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.ClosePayArgs{PaymentID: paymentID, Provider: "alipay"}}
			require.NoError(t, worker.Work(f.ctx, job))
			require.NoError(t, worker.Work(f.ctx, job)) // duplicate job must not restore twice
			var status string
			var stock int64
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, orderID).Scan(&status))
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
			if absent {
				require.Equal(t, biz.OrderStatusCancelled, status)
				require.Equal(t, int64(100), stock)
			} else {
				require.Equal(t, biz.OrderStatusPaid, status)
				require.Equal(t, int64(99), stock)
			}
		})
	}
}

func TestOverdueRiverScanReachesLost101stJobIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
	var lostID int64
	for i := 0; i < 101; i++ {
		orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusPending)
		_, err := f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=now()-interval '1 hour' WHERE id=$1`, orderID)
		require.NoError(t, err)
		if i < 100 {
			_, err = f.pool.Exec(f.ctx, `UPDATE payments SET reconciliation_status='required' WHERE id=$1`, paymentID)
			require.NoError(t, err)
			_, err = mq.EnqueueExpireOrder(f.ctx, biz.ExpireOrderArgs{OrderID: orderID}, time.Now().Add(time.Hour))
			require.NoError(t, err)
		} else {
			lostID = orderID
		}
	}
	ids, err := NewOrderExpiryRepo(f.data, f.tx, mq, log.DefaultLogger).ReapOverdueOrders(f.ctx, 5*time.Minute, 100)
	require.NoError(t, err)
	require.Contains(t, ids, lostID)
	var count int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM river_job WHERE kind='expire_order' AND args->>'order_id'=$1`, fmt.Sprint(lostID)).Scan(&count))
	require.Equal(t, int64(1), count)
}

func TestUndispatchedUnsupportedPaymentRecoveryIntegration(t *testing.T) {
	for _, action := range []string{"pay", "cancel", "expire"} {
		t.Run(action, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			orderID, paymentID, _ := f.seedPayment(t, biz.PaymentStatusCreating)
			_, err := f.pool.Exec(f.ctx, `UPDATE payments SET pay_channel='alipay:invalid' WHERE id=$1`, paymentID)
			require.NoError(t, err)
			mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
			switch action {
			case "pay":
				payment, err := NewPaymentRepo(f.data, f.tx, log.DefaultLogger).CreatePayment(f.ctx, biz.CreatePaymentArgs{
					OrderID: orderID, UserID: f.userID, Amount: 12345, Currency: "CNY", Method: "alipay:wap", OutTradeNo: f.prefix + "_replacement"})
				require.NoError(t, err)
				require.NotEqual(t, paymentID, payment.ID)
			case "cancel":
				require.NoError(t, NewOrderRepo(f.data, f.tx, log.DefaultLogger).CancelOrderByUser(f.ctx, orderID, f.userID))
			case "expire":
				_, err := f.pool.Exec(f.ctx, `UPDATE orders SET expires_at=now()-interval '1 hour' WHERE id=$1`, orderID)
				require.NoError(t, err)
				require.NoError(t, NewOrderExpiryRepo(f.data, f.tx, mq, log.DefaultLogger).ExpireOrder(f.ctx, orderID))
			}
			var status string
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM payments WHERE id=$1`, paymentID).Scan(&status))
			require.Equal(t, biz.PaymentStatusFailed, status)
		})
	}
}
