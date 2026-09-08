//go:build integration

package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

const integrationPostgresEnv = "ECOMMERCE_INTEGRATION_POSTGRES_URL"

type correctnessFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	rdb         *redis.Client
	data        *Data
	tx          biz.TxManager
	riverClient *river.Client[pgx.Tx]
	handler     *PaymentRiverErrorHandler
	provider    string
	userID      int64
	addressID   int64
	categoryID  int64
	productID   int64
	prefix      string
}

type integrationCheckPayWorker struct {
	river.WorkerDefaults[biz.CheckPayArgs]
}

func (integrationCheckPayWorker) Work(context.Context, *river.Job[biz.CheckPayArgs]) error {
	return nil
}

type integrationExpireOrderWorker struct {
	river.WorkerDefaults[biz.ExpireOrderArgs]
}

func (integrationExpireOrderWorker) Work(context.Context, *river.Job[biz.ExpireOrderArgs]) error {
	return nil
}

func newCorrectnessFixture(t *testing.T) *correctnessFixture {
	t.Helper()
	dsn := os.Getenv(integrationPostgresEnv)
	if dsn == "" {
		t.Skipf("set %s to run real PostgreSQL/Redis/River integration tests", integrationPostgresEnv)
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(poolConfig.ConnConfig.Database), "integration", "integration tests refuse to use a database whose name does not contain integration")

	ctx := context.Background()
	require.NoError(t, RunMigrations(&conf.Data{Database: &conf.Data_Database{Source: dsn}}))
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	require.NoError(t, err)
	require.NoError(t, pool.Ping(ctx))

	redisAddr := os.Getenv("ECOMMERCE_INTEGRATION_REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "127.0.0.1:6379"
	}
	// DB 15 is reserved for this suite so it never flushes the application's DB 0.
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr, DB: 15})
	require.NoError(t, rdb.Ping(ctx).Err())
	require.NoError(t, rdb.FlushDB(ctx).Err())

	data, closeData, err := NewData(nil, pool, rdb)
	require.NoError(t, err)
	t.Cleanup(closeData)
	t.Cleanup(func() { _ = rdb.FlushDB(ctx).Err() })

	handler := NewPaymentRiverErrorHandler(pool, rdb, log.DefaultLogger)
	workers := river.NewWorkers()
	river.AddWorker(workers, integrationCheckPayWorker{})
	river.AddWorker(workers, integrationExpireOrderWorker{})
	riverClient, err := NewRiverClient(pool, workers, nil, handler)
	require.NoError(t, err)

	prefix := fmt.Sprintf("it_%d", time.Now().UnixNano())
	provider := strings.ReplaceAll(prefix, "_", "")
	f := &correctnessFixture{
		ctx: ctx, pool: pool, rdb: rdb, data: data, tx: NewTransaction(pool, log.DefaultLogger),
		riverClient: riverClient, handler: handler, provider: provider, prefix: prefix,
	}
	f.seedBase(t)
	t.Cleanup(func() { f.cleanup(t) })
	return f
}

func (f *correctnessFixture) seedBase(t *testing.T) {
	t.Helper()
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO users (nickname, phone_hash, phone_encrypt, password_hash)
		VALUES ('integration', $1, 'ciphertext', 'hash') RETURNING id`, f.prefix).Scan(&f.userID))
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO shipping_addresses (
			user_id, receiver_name, receiver_phone_hash, receiver_phone_encrypt,
			province, city, district, detail_address, is_default
		) VALUES ($1, 'receiver', 'phone-hash', 'phone-cipher', 'p', 'c', 'd', 'detail', false)
		RETURNING id`, f.userID).Scan(&f.addressID))
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`INSERT INTO categories (name) VALUES ($1) RETURNING id`, f.prefix).Scan(&f.categoryID))
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO products (category_id, name, price_minor, stock, status, cover_image, media_assets)
		VALUES ($1, $2, 12345, 100, 1, '[]'::jsonb, '[]'::jsonb) RETURNING id`,
		f.categoryID, f.prefix).Scan(&f.productID))
}

func (f *correctnessFixture) cleanup(t *testing.T) {
	t.Helper()
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM river_job
			WHERE kind = $1
			  AND (args->>'order_id')::bigint IN (SELECT id FROM orders WHERE user_id = $2)`,
			[]any{biz.ExpireOrderJobKind, f.userID}},
		{`DELETE FROM river_job
			WHERE kind IN ($1, $2)
			  AND (args->>'payment_id')::bigint IN (SELECT id FROM payments WHERE user_id = $3)`,
			[]any{biz.CheckPayJobKind, biz.ClosePayJobKind, f.userID}},
		{`DELETE FROM river_job WHERE kind = $1 AND args->>'provider' = $2`, []any{biz.CheckPayJobKind, f.provider}},
		{`DELETE FROM payment_notifications WHERE provider = $1`, []any{f.provider}},
		{`DELETE FROM payment_reconciliation_failures WHERE payment_id IN (SELECT id FROM payments WHERE user_id = $1)`, []any{f.userID}},
		{`DELETE FROM order_refunds WHERE user_id = $1`, []any{f.userID}},
		{`DELETE FROM payments WHERE user_id = $1`, []any{f.userID}},
		{`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE user_id = $1)`, []any{f.userID}},
		{`DELETE FROM orders WHERE user_id = $1`, []any{f.userID}},
		{`DELETE FROM shipping_addresses WHERE user_id = $1`, []any{f.userID}},
		{`DELETE FROM products WHERE category_id = $1`, []any{f.categoryID}},
		{`DELETE FROM categories WHERE id = $1`, []any{f.categoryID}},
		{`DELETE FROM users WHERE id = $1`, []any{f.userID}},
	}
	for _, statement := range statements {
		_, err := f.pool.Exec(f.ctx, statement.sql, statement.args...)
		require.NoError(t, err)
	}
}

func (f *correctnessFixture) seedPayment(t *testing.T, status string) (int64, int64, string) {
	t.Helper()
	orderNo := fmt.Sprintf("%s_order_%d", f.prefix, time.Now().UnixNano())
	var orderID int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO orders (
			user_id, address_id, total_amount_minor, status, out_trade_no, currency,
			idempotency_key, request_hash, expires_at
		)
		VALUES ($1, $2, 12345, 'pending_payment', $3, 'CNY', $4, $5, CURRENT_TIMESTAMP + INTERVAL '30 minutes')
		RETURNING id`,
		f.userID, f.addressID, orderNo, orderNo, strings.Repeat("a", 64)).Scan(&orderID))
	outTradeNo := fmt.Sprintf("%s_pay_%d", f.prefix, time.Now().UnixNano())
	var paymentID int64
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		INSERT INTO payments (
			order_id, user_id, merchant_id, amount_minor, status, pay_channel, out_trade_no, currency
		) VALUES ($1, $2, 0, 12345, $3, 'wechat:native', $4, 'CNY') RETURNING id`,
		orderID, f.userID, status, outTradeNo).Scan(&paymentID))
	return orderID, paymentID, outTradeNo
}

type failingPaymentMQRepo struct{ err error }

func (r failingPaymentMQRepo) EnqueueCheckPay(context.Context, biz.CheckPayArgs, time.Time) (*biz.MQJob, error) {
	return nil, r.err
}
func (r failingPaymentMQRepo) EnqueueCheckPayTx(context.Context, biz.CheckPayArgs, time.Time) (*biz.MQJob, error) {
	return nil, r.err
}
func (r failingPaymentMQRepo) EnqueueClosePay(context.Context, biz.ClosePayArgs, time.Time) (*biz.MQJob, error) {
	return nil, r.err
}
func (r failingPaymentMQRepo) EnqueueClosePayTx(context.Context, biz.ClosePayArgs, time.Time) (*biz.MQJob, error) {
	return nil, r.err
}
func (r failingPaymentMQRepo) EnqueueExpireOrder(context.Context, biz.ExpireOrderArgs, time.Time) (*biz.MQJob, error) {
	return nil, r.err
}
func (r failingPaymentMQRepo) EnqueueExpireOrderTx(context.Context, biz.ExpireOrderArgs, time.Time) (*biz.MQJob, error) {
	return nil, r.err
}
func (r failingPaymentMQRepo) GetMQJob(context.Context, int64) (*biz.MQJob, error) {
	return nil, r.err
}

func TestCorrectnessIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)

	t.Run("callback inbox and River commit, rollback, duplicate, and late requeue", func(t *testing.T) {
		_, paymentID, outTradeNo := f.seedPayment(t, biz.PaymentStatusPending)
		mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
		repo := NewPaymentNotificationRepo(f.tx, mq)
		args := biz.CheckPayArgs{PaymentID: paymentID, Provider: f.provider, Trigger: "callback", MaxPolls: 30}
		notification := &biz.PaymentNotification{
			Provider: f.provider, ProviderEventID: f.prefix + "_event_1", OutTradeNo: outTradeNo,
			PayloadHash: f.prefix + "_payload_1", VerifiedAt: time.Now().UTC(),
		}

		duplicate, err := repo.PersistAndEnqueueNotification(f.ctx, notification, args)
		require.NoError(t, err)
		require.False(t, duplicate)
		require.Equal(t, int64(1), f.countNotifications(t, outTradeNo))
		jobs := f.paymentJobIDs(t, paymentID)
		require.Len(t, jobs, 1)

		duplicate, err = repo.PersistAndEnqueueNotification(f.ctx, notification, args)
		require.NoError(t, err)
		require.True(t, duplicate)
		require.Len(t, f.paymentJobIDs(t, paymentID), 1)

		active := *notification
		active.ProviderEventID = f.prefix + "_event_active"
		active.PayloadHash = f.prefix + "_payload_active"
		duplicate, err = repo.PersistAndEnqueueNotification(f.ctx, &active, args)
		require.NoError(t, err)
		require.False(t, duplicate)
		jobs = f.paymentJobIDs(t, paymentID)
		require.Len(t, jobs, 2, "a distinct callback must not deduplicate against an active notification job")

		for _, jobID := range jobs {
			_, err = f.pool.Exec(f.ctx, `UPDATE river_job SET state = 'completed', finalized_at = now() WHERE id = $1`, jobID)
			require.NoError(t, err)
		}
		late := *notification
		late.ProviderEventID = f.prefix + "_event_2"
		late.PayloadHash = f.prefix + "_payload_2"
		duplicate, err = repo.PersistAndEnqueueNotification(f.ctx, &late, args)
		require.NoError(t, err)
		require.False(t, duplicate)
		require.Len(t, f.paymentJobIDs(t, paymentID), 3)

		failed := *notification
		failed.ProviderEventID = f.prefix + "_event_rollback"
		failed.PayloadHash = f.prefix + "_payload_rollback"
		rollbackRepo := NewPaymentNotificationRepo(f.tx, failingPaymentMQRepo{err: stderrors.New("insert failed")})
		_, err = rollbackRepo.PersistAndEnqueueNotification(f.ctx, &failed, args)
		require.Error(t, err)
		var rollbackRows int64
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM payment_notifications WHERE provider_event_id = $1`, failed.ProviderEventID).Scan(&rollbackRows))
		require.Zero(t, rollbackRows)
	})

	t.Run("payment CAS permits exactly one concurrent terminal transition", func(t *testing.T) {
		q := db.New(f.pool)
		_, paymentID, _ := f.seedPayment(t, biz.PaymentStatusPending)
		var successes atomic.Int32
		unexpected := make(chan error, 16)
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := q.RecordPaymentSuccess(f.ctx, db.RecordPaymentSuccessParams{ID: paymentID, ThirdPartyTxID: pgText(fmt.Sprintf("%s_tx", f.prefix))})
				if err == nil {
					successes.Add(1)
				} else if !stderrors.Is(err, pgx.ErrNoRows) {
					unexpected <- err
				}
			}()
		}
		wg.Wait()
		close(unexpected)
		for err := range unexpected {
			require.NoError(t, err)
		}
		require.Equal(t, int32(1), successes.Load())

		_, competingID, _ := f.seedPayment(t, biz.PaymentStatusPending)
		results := make(chan error, 2)
		go func() {
			_, err := q.RecordPaymentSuccess(f.ctx, db.RecordPaymentSuccessParams{ID: competingID, ThirdPartyTxID: pgText(fmt.Sprintf("%s_competing_tx", f.prefix))})
			results <- err
		}()
		go func() {
			_, err := q.MarkPaymentClosed(f.ctx, competingID)
			results <- err
		}()
		terminalWinners := 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				terminalWinners++
			} else {
				require.ErrorIs(t, err, pgx.ErrNoRows)
			}
		}
		require.Equal(t, 1, terminalWinners)
	})

	t.Run("concurrent CreateOrder with one idempotency key creates exactly one order", func(t *testing.T) {
		var stockBefore int32
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id = $1`, f.productID).Scan(&stockBefore))
		mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
		repo := NewOrderRepoWithJobs(f.data, f.tx, mq, log.DefaultLogger)
		idempotencyKey := f.prefix + "_concurrent_key"

		const workers = 8
		orderIDs := make(chan int64, workers)
		failures := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				order, err := repo.CreateOrder(f.ctx, biz.CreateOrderArgs{
					UserID: f.userID, AddressID: f.addressID, OutTradeNo: f.prefix + "_concurrent_order",
					Currency: "CNY", Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}},
					IdempotencyKey: idempotencyKey, RequestHash: f.prefix + "_concurrent_hash",
					ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
				})
				if err != nil {
					failures <- err
					return
				}
				orderIDs <- order.ID
			}()
		}
		wg.Wait()
		close(orderIDs)
		close(failures)
		for err := range failures {
			require.NoError(t, err)
		}
		var winner int64
		seen := 0
		for id := range orderIDs {
			if seen == 0 {
				winner = id
			} else {
				require.Equal(t, winner, id, "every replay must observe the same order")
			}
			seen++
		}
		require.Equal(t, workers, seen)

		var orderCount int32
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM orders WHERE idempotency_key = $1`, idempotencyKey).Scan(&orderCount))
		require.Equal(t, int32(1), orderCount, "the unique index plus recovery path must yield one row")
		var stockAfter int32
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id = $1`, f.productID).Scan(&stockAfter))
		require.Equal(t, stockBefore-1, stockAfter, "stock must be decremented exactly once")

		_, err := repo.CreateOrder(f.ctx, biz.CreateOrderArgs{
			UserID: f.userID, AddressID: f.addressID, OutTradeNo: f.prefix + "_conflict_order",
			Currency: "CNY", Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}},
			IdempotencyKey: idempotencyKey, RequestHash: f.prefix + "_different_hash",
			ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
		})
		require.ErrorIs(t, err, biz.ErrIdempotencyKeyConflict)
	})

	t.Run("concurrent refund preparation yields a single refund record", func(t *testing.T) {
		_, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
		repo := NewPaymentRepo(f.data, f.tx, log.DefaultLogger)

		refundIDs := make(chan int64, 2)
		failures := make(chan error, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, refund, err := repo.PreparePaymentRefund(f.ctx, paymentID, fmt.Sprintf("%s_rfnd_%d", f.prefix, i))
				if err != nil {
					failures <- err
					return
				}
				refundIDs <- refund.ID
			}(i)
		}
		wg.Wait()
		close(refundIDs)
		close(failures)
		for err := range failures {
			require.NoError(t, err)
		}
		first, second := <-refundIDs, <-refundIDs
		require.Equal(t, first, second, "both callers must share the single refund record")

		var refundCount int32
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM order_refunds WHERE payment_id = $1`, paymentID).Scan(&refundCount))
		require.Equal(t, int32(1), refundCount, "the payment row lock must serialize refund creation")
	})

	t.Run("order creation uses database price and advances every related cache generation", func(t *testing.T) {
		keys := []string{
			"product:list:gen",
			redisKey("product", "category", f.categoryID, "gen"),
			redisKey("order", "user", f.userID, "gen"),
			redisKey("order", "user", "ongoing", f.userID, "gen"),
		}
		for _, key := range keys {
			require.NoError(t, f.rdb.Set(f.ctx, key, 7, 0).Err())
		}
		mq := NewPaymentMQRepo(f.riverClient, log.DefaultLogger)
		repo := NewOrderRepoWithJobs(f.data, f.tx, mq, log.DefaultLogger)
		expiresAt := time.Now().UTC().Add(30 * time.Minute)
		order, err := repo.CreateOrder(f.ctx, biz.CreateOrderArgs{
			UserID: f.userID, AddressID: f.addressID, OutTradeNo: f.prefix + "_created_order",
			Currency: "CNY", Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 2}},
			IdempotencyKey: f.prefix + "_idempotency", RequestHash: f.prefix + "_request_hash",
			ExpiresAt: expiresAt,
		})
		require.NoError(t, err)
		require.Equal(t, int64(24690), order.TotalAmount)
		require.Equal(t, int64(12345), order.Items[0].UnitPrice)
		require.Equal(t, f.prefix, order.Items[0].ProductName)
		for _, key := range keys {
			require.Equal(t, "8", f.rdb.Get(f.ctx, key).Val(), key)
		}
		var stock int32
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id = $1`, f.productID).Scan(&stock))
		require.Equal(t, int32(98), stock)
		var snapshotName string
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT product_name_snapshot FROM order_items WHERE order_id = $1`, order.ID).Scan(&snapshotName))
		require.Equal(t, f.prefix, snapshotName)
		var expireJobs int64
		require.NoError(t, f.pool.QueryRow(f.ctx, `
			SELECT count(*) FROM river_job
			WHERE kind = $1 AND args->>'order_id' = $2`,
			biz.ExpireOrderJobKind, fmt.Sprint(order.ID)).Scan(&expireJobs))
		require.Equal(t, int64(1), expireJobs)
	})

	t.Run("concurrent default-address switches leave exactly one default", func(t *testing.T) {
		var secondAddressID int64
		require.NoError(t, f.pool.QueryRow(f.ctx, `
			INSERT INTO shipping_addresses (
				user_id, receiver_name, receiver_phone_hash, receiver_phone_encrypt,
				province, city, district, detail_address, is_default
			) VALUES ($1, 'receiver2', 'phone-hash2', 'phone-cipher2', 'p', 'c', 'd', 'detail2', false)
			RETURNING id`, f.userID).Scan(&secondAddressID))
		repo := NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, id := range []int64{f.addressID, secondAddressID} {
			go func(addressID int64) {
				<-start
				errs <- repo.SetDefaultShippingAddress(f.ctx, addressID, f.userID)
			}(id)
		}
		close(start)
		for i := 0; i < 2; i++ {
			require.NoError(t, <-errs)
		}
		var defaults int64
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM shipping_addresses WHERE user_id = $1 AND is_default`, f.userID).Scan(&defaults))
		require.Equal(t, int64(1), defaults)
	})

	t.Run("discarded River job persists reconciliation and evicts stale caches", func(t *testing.T) {
		orderID, paymentID, outTradeNo := f.seedPayment(t, biz.PaymentStatusPending)
		q := db.New(f.pool)
		notification, err := q.CreatePaymentNotification(f.ctx, db.CreatePaymentNotificationParams{
			Provider: f.provider, ProviderEventID: pgText(f.prefix + "_discarded_event"), OutTradeNo: outTradeNo,
			PayloadHash: f.prefix + "_discarded_payload", VerifiedAt: pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		})
		require.NoError(t, err)
		_, err = q.BeginPaymentNotificationProcessing(f.ctx, notification.ID)
		require.NoError(t, err)
		cacheKeys := []string{
			redisKey("payment", paymentID),
			redisKey("payment", "order", orderID),
			redisKey("payment", "order", orderID, "active", "wechat", "native"),
			redisKey("payment", "out_trade_no", outTradeNo),
			redisKey("order", orderID),
		}
		for _, key := range cacheKeys {
			require.NoError(t, f.rdb.Set(f.ctx, key, `{"Status":"pending"}`, time.Hour).Err())
		}
		encoded, err := json.Marshal(biz.CheckPayArgs{PaymentID: paymentID, Provider: f.provider, NotificationID: notification.ID})
		require.NoError(t, err)
		job := &rivertype.JobRow{ID: paymentID + 1_000_000_000, Kind: biz.CheckPayJobKind, Attempt: 8, MaxAttempts: 8, EncodedArgs: encoded}
		f.handler.HandleError(f.ctx, job, stderrors.New("provider timeout"))

		var reconciliationStatus string
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT reconciliation_status FROM payments WHERE id = $1`, paymentID).Scan(&reconciliationStatus))
		require.Equal(t, biz.ReconciliationStatusRequired, reconciliationStatus)
		var failures int64
		require.NoError(t, f.pool.QueryRow(f.ctx,
			`SELECT count(*) FROM payment_reconciliation_failures WHERE payment_id = $1 AND river_job_id = $2`, paymentID, job.ID).Scan(&failures))
		require.Equal(t, int64(1), failures)
		storedNotification, err := q.GetPaymentNotification(f.ctx, notification.ID)
		require.NoError(t, err)
		require.Equal(t, biz.PaymentNotificationStatusFailed, storedNotification.Status)
		require.Equal(t, "provider timeout", storedNotification.LastError.String)
		require.Equal(t, int64(0), f.rdb.Exists(f.ctx, cacheKeys...).Val())
	})

	t.Run("discarded stale River job preserves terminal payment state", func(t *testing.T) {
		_, paymentID, _ := f.seedPayment(t, biz.PaymentStatusSuccess)
		encoded, err := json.Marshal(biz.CheckPayArgs{PaymentID: paymentID, Provider: f.provider})
		require.NoError(t, err)
		job := &rivertype.JobRow{ID: paymentID + 2_000_000_000, Kind: biz.CheckPayJobKind, Attempt: 8, MaxAttempts: 8, EncodedArgs: encoded}
		f.handler.HandleError(f.ctx, job, stderrors.New("stale provider timeout"))

		var status string
		require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT status FROM payments WHERE id = $1`, paymentID).Scan(&status))
		require.Equal(t, biz.PaymentStatusSuccess, status)
	})
}

func (f *correctnessFixture) countNotifications(t *testing.T, outTradeNo string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM payment_notifications WHERE provider = $1 AND out_trade_no = $2`, f.provider, outTradeNo).Scan(&count))
	return count
}

func (f *correctnessFixture) paymentJobIDs(t *testing.T, paymentID int64) []int64 {
	t.Helper()
	rows, err := f.pool.Query(f.ctx, `
		SELECT id FROM river_job
		WHERE kind = $1 AND args->>'provider' = $2 AND args->>'payment_id' = $3
		ORDER BY id`, biz.CheckPayJobKind, f.provider, fmt.Sprint(paymentID))
	require.NoError(t, err)
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

func pgText(value string) (result pgtype.Text) {
	result.String, result.Valid = value, true
	return result
}
