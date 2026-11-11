//go:build integration

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type accountMutationBarrier struct {
	counted chan struct{}
	release chan struct{}
	waiting chan uint32
}

type accountMutationQuerier struct {
	db.Querier
	tx      pgx.Tx
	barrier *accountMutationBarrier
	first   bool
}

func (q accountMutationQuerier) LockUserRoleChanges(ctx context.Context) error {
	if !q.first {
		q.barrier.waiting <- q.tx.Conn().PgConn().PID()
	}
	return q.Querier.LockUserRoleChanges(ctx)
}

func (q accountMutationQuerier) CountAdmins(ctx context.Context) (int64, error) {
	n, err := q.Querier.CountAdmins(ctx)
	if err != nil || !q.first {
		return n, err
	}
	close(q.barrier.counted)
	select {
	case <-q.barrier.release:
		return n, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type accountMutationTx struct {
	biz.TxManager
	barrier *accountMutationBarrier
	first   bool
}

func (tx accountMutationTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		pgTx := pgTxFromContext(ctx)
		q := accountMutationQuerier{querierFromContext(ctx, nil), pgTx, tx.barrier, tx.first}
		return fn(WithQuerier(ctx, q, pgTx))
	})
}

func TestAccountMutationsPreserveLastAdminIntegration(t *testing.T) {
	for _, tc := range []struct {
		name         string
		firstDelete  bool
		secondDelete bool
	}{
		{"two demotions", false, false},
		{"two deletions", true, true},
		{"demotion and deletion", false, true},
		{"deletion and demotion", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
			ids := make([]int64, 2)
			for i, suffix := range []string{"_admin_a", "_admin_b"} {
				require.NoError(t, f.pool.QueryRow(ctx, `INSERT INTO users(nickname,phone_hash,phone_encrypt,password_hash,role)
					VALUES ('admin',$1,'cipher','hash','admin') RETURNING id`, f.prefix+suffix).Scan(&ids[i]))
			}
			t.Cleanup(func() {
				_, err := f.pool.Exec(f.ctx, `DELETE FROM users WHERE id=ANY($1)`, ids)
				require.NoError(t, err)
			})
			barrier := &accountMutationBarrier{make(chan struct{}), make(chan struct{}), make(chan uint32, 1)}
			firstTx := accountMutationTx{f.tx, barrier, true}
			secondTx := accountMutationTx{f.tx, barrier, false}
			first := NewUserRepo(f.data, log.DefaultLogger)
			first.tx = firstTx
			second := NewUserRepo(f.data, log.DefaultLogger)
			second.tx = secondTx
			// The production deletion wrapper must take the same global lock
			// before acquiring any community row locks or changing content.
			community := NewCommunityUserRepo(f.data, secondTx, nil, log.DefaultLogger)
			var once sync.Once
			var workers sync.WaitGroup
			release := func() { once.Do(func() { close(barrier.release) }) }
			defer func() { release(); cancel(); workers.Wait() }()
			firstDone := make(chan error, 1)
			workers.Go(func() {
				if tc.firstDelete {
					firstDone <- first.DeleteUser(ctx, ids[0])
				} else {
					firstDone <- first.UpdateUserRole(ctx, ids[0], biz.RoleUser)
				}
			})
			select {
			case <-barrier.counted:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			secondDone := make(chan error, 1)
			workers.Go(func() {
				if tc.secondDelete {
					secondDone <- community.DeleteUser(ctx, ids[1])
				} else {
					secondDone <- second.UpdateUserRole(ctx, ids[1], biz.RoleUser)
				}
			})
			var pid uint32
			select {
			case pid = <-barrier.waiting:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.Eventually(t, func() bool {
				var blocked bool
				err := f.pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, int32(pid)).Scan(&blocked)
				return err == nil && blocked
			}, 3*time.Second, 10*time.Millisecond, "the second mutation must wait for the first transaction")
			release()
			require.NoError(t, <-firstDone)
			require.ErrorIs(t, <-secondDone, biz.ErrLastAdmin)
			admins, err := second.CountAdmins(ctx)
			require.NoError(t, err)
			require.Equal(t, int64(1), admins)
		})
	}
}

func TestPaymentSettlementInvalidatesEveryOrderListIntegration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  biz.TradeState
		status string
	}{
		{"success", biz.TradeStateSuccess, biz.OrderStatusPaid},
		{"closed", biz.TradeStateClosed, biz.OrderStatusCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCorrectnessFixture(t)
			orderID, paymentID, tradeNo := f.seedPayment(t, biz.PaymentStatusPending)
			orders := NewOrderRepoWithJobs(f.data, f.tx, nil, integrationAuthConfig(), log.DefaultLogger)
			for _, ongoing := range []bool{false, true} {
				merchant, err := orders.ListOrdersByMerchant(f.ctx, f.merchantID, ongoing, 10, 0)
				require.NoError(t, err)
				require.Len(t, merchant, 1)
				all, err := orders.ListAllOrders(f.ctx, ongoing, 10, 0)
				require.NoError(t, err)
				require.Len(t, all, 1)
			}
			payments := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)
			require.NoError(t, payments.ApplyPayQuery(f.ctx, biz.CheckPayArgs{PaymentID: paymentID, Provider: "wechat"}, &biz.PaymentQueryResult{
				Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: tradeNo,
				TransactionID: f.prefix + "_tx", TradeState: tc.state, Amount: 12345, Currency: "CNY",
			}))
			var actual string
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM orders WHERE id=$1`, orderID).Scan(&actual))
			require.Equal(t, tc.status, actual)
			for _, ongoing := range []bool{false, true} {
				merchant, err := orders.ListOrdersByMerchant(f.ctx, f.merchantID, ongoing, 10, 0)
				require.NoError(t, err)
				all, err := orders.ListAllOrders(f.ctx, ongoing, 10, 0)
				require.NoError(t, err)
				for _, rows := range [][]biz.Order{merchant, all} {
					if ongoing && tc.status == biz.OrderStatusCancelled {
						require.Empty(t, rows)
					} else {
						require.Len(t, rows, 1)
						require.Equal(t, actual, rows[0].Status)
					}
				}
			}
		})
	}
}

func TestMerchantRetainsPurchaseAccessAfterPromotionIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	orderID, _, _ := f.seedPayment(t, biz.PaymentStatusPending)
	users := NewUserRepo(f.data, log.DefaultLogger)
	require.NoError(t, users.UpdateUserRole(f.ctx, f.userID, biz.RoleMerchant))
	repo := NewOrderRepoWithJobs(f.data, f.tx, nil, integrationAuthConfig(), log.DefaultLogger)
	uc := biz.NewConfiguredOrderUsecase(repo, nil, biz.OrderPolicy{}, integrationAuthConfig(), log.DefaultLogger)
	buyer := biz.Actor{ID: f.userID, Role: biz.RoleMerchant}
	order, err := uc.GetOrder(f.ctx, orderID, buyer)
	require.NoError(t, err)
	require.Equal(t, orderID, order.ID)
	for _, ongoing := range []bool{false, true} {
		rows, total, err := uc.ListOrders(f.ctx, buyer, &biz.ListOrdersReq{UserID: buyer.ID, Ongoing: ongoing, Limit: 10})
		require.NoError(t, err)
		require.Equal(t, int64(1), total)
		require.Len(t, rows, 1)
		require.Equal(t, orderID, rows[0].ID)
	}
	// The sales default remains separate from purchases.
	sales, total, err := uc.ListOrders(f.ctx, buyer, &biz.ListOrdersReq{Limit: 10})
	require.NoError(t, err)
	require.Empty(t, sales)
	require.Zero(t, total)
	// The original merchant still sees the sale and its shipping snapshot.
	_, err = uc.GetOrder(f.ctx, orderID, biz.Actor{ID: f.merchantID, Role: biz.RoleMerchant})
	require.NoError(t, err)
	_, err = uc.GetOrder(f.ctx, orderID, biz.Actor{ID: f.merchantID + f.userID + 1000, Role: biz.RoleMerchant})
	require.ErrorIs(t, err, biz.ErrOrderNotFound)
}
