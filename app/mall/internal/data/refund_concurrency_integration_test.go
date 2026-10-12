//go:build integration

package data

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type refundInsertBarrier struct {
	db.Querier
	ready, release chan struct{}
}

func (q refundInsertBarrier) GetOrderRefundByPaymentID(ctx context.Context, id pgtype.Int8) (db.OrderRefund, error) {
	row, err := q.Querier.GetOrderRefundByPaymentID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		close(q.ready)
		select {
		case <-q.release:
		case <-ctx.Done():
			return row, ctx.Err()
		}
	}
	return row, err
}

type refundInsertBarrierTx struct {
	biz.TxManager
	ready, release chan struct{}
}

func (tx refundInsertBarrierTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		q := refundInsertBarrier{querierFromContext(ctx, nil), tx.ready, tx.release}
		return fn(WithQuerier(ctx, q, pgTxFromContext(ctx)))
	})
}

type paymentQueryPIDTx struct {
	biz.TxManager
	pid chan uint32
}

func (tx paymentQueryPIDTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		tx.pid <- pgTxFromContext(ctx).Conn().PgConn().PID()
		return fn(ctx)
	})
}

func TestFirstRefundInsertSerializesWithPaymentQueryIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, paymentID := fulfillmentOrder(t, f)
	payment, err := f.data.q.GetPayment(f.ctx, paymentID)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var workers sync.WaitGroup
	defer func() { unblock(); cancel(); workers.Wait() }()
	refundRepo := NewPaymentRepo(f.data, refundInsertBarrierTx{f.tx, ready, release}, log.DefaultLogger)
	refundDone := make(chan error, 1)
	workers.Go(func() {
		_, _, err := refundRepo.PreparePaymentRefund(ctx, paymentID, f.prefix+"_refund")
		refundDone <- err
	})
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	// Hold refund preparation immediately before its first INSERT. A query
	// on another connection must block in the same order -> payment lock order,
	// not hold the order while waiting for a payment locked by preparation.
	pid := make(chan uint32, 1)
	queryRepo := NewPaymentRepo(f.data, paymentQueryPIDTx{f.tx, pid}, log.DefaultLogger)
	queryDone := make(chan error, 1)
	workers.Go(func() {
		queryDone <- queryRepo.ApplyPayQuery(ctx, biz.CheckPayArgs{PaymentID: paymentID, Provider: "wechat"}, &biz.PaymentQueryResult{
			Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: payment.OutTradeNo,
			TransactionID: f.prefix + "_transaction", TradeState: biz.TradeStateSuccess, Amount: payment.AmountMinor, Currency: payment.Currency,
		})
	})
	var queryPID uint32
	select {
	case queryPID = <-pid:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Eventually(t, func() bool {
		var blocked bool
		err := f.pool.QueryRow(ctx, "SELECT cardinality(pg_blocking_pids($1)) > 0", int32(queryPID)).Scan(&blocked)
		return err == nil && blocked
	}, 3*time.Second, 10*time.Millisecond, "the two transactions must overlap their row locks")
	unblock()
	for _, done := range []<-chan error{refundDone, queryDone} {
		select {
		case err := <-done:
			require.NoError(t, err, "first refund insertion and query must not deadlock")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	order, err := f.data.q.GetOrder(f.ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, biz.OrderStatusPaid, order.Status)
	refund, err := f.data.q.GetOrderRefundByPaymentID(f.ctx, pgtype.Int8{Int64: paymentID, Valid: true})
	require.NoError(t, err)
	require.Equal(t, biz.PaymentRefundStatusPending, refund.Status)
	require.Equal(t, f.prefix+"_refund", refund.OutRefundNo)
}
