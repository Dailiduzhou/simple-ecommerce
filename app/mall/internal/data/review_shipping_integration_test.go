//go:build integration

package data

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type shippingIDGenerator struct{ prefix string }

func (g shippingIDGenerator) GenerateString() string {
	return fmt.Sprintf("%s_%d", g.prefix, time.Now().UnixNano())
}
func (g shippingIDGenerator) GenerateOrderNo32(string) string        { return g.GenerateString() }
func (g shippingIDGenerator) GenerateOrderNo64(string, int64) string { return g.GenerateString() }

func shippingOrderUsecase(f *correctnessFixture, tx biz.TxManager) biz.OrderUsecase {
	repo := NewOrderRepoWithJobs(f.data, tx, NewPaymentMQRepo(f.riverClient, log.DefaultLogger), log.DefaultLogger)
	return biz.NewConfiguredOrderUsecase(repo, shippingIDGenerator{f.prefix}, biz.OrderPolicy{}, integrationAuthConfig(), log.DefaultLogger)
}
func shippingCheckout(f *correctnessFixture) *biz.CreateOrderReq {
	return &biz.CreateOrderReq{UserID: f.userID, AddressID: f.addressID, IdempotencyKey: f.prefix + "_checkout",
		Items: []biz.OrderItemInput{{ProductID: f.productID, Quantity: 1}}}
}
func shippingCipher(t *testing.T, phone string) string {
	t.Helper()
	cipher, err := phonecrypto.EncryptPhone(phone, []byte(integrationAuthConfig().PhoneSecret))
	require.NoError(t, err)
	return cipher
}

func TestReviewOrderShippingSnapshotSurvivesAddressBookChangesIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	oldCipher := shippingCipher(t, "13800138000")
	_, err := f.pool.Exec(f.ctx, `UPDATE shipping_addresses SET receiver_name='Receiver A',receiver_phone_encrypt=$2,detail_address='Address A' WHERE id=$1`, f.addressID, oldCipher)
	require.NoError(t, err)
	orders := shippingOrderUsecase(f, f.tx)
	order, err := orders.CreateOrder(f.ctx, shippingCheckout(f))
	require.NoError(t, err)
	require.Equal(t, "Receiver A", order.Shipping.ReceiverName)
	require.Equal(t, "13800138000", order.Shipping.ReceiverPhone)
	var storedCipher string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT receiver_phone_encrypt FROM orders WHERE id=$1`, order.ID).Scan(&storedCipher))
	require.Equal(t, oldCipher, storedCipher)
	require.NotEqual(t, "13800138000", storedCipher)
	addresses := NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger)
	_, err = addresses.UpdateShippingAddress(f.ctx, f.addressID, f.userID, "Receiver B", "hash B", shippingCipher(t, "13900139000"), "New Province", "New City", "New District", "Address B", "")
	require.NoError(t, err)
	require.NoError(t, addresses.DeleteShippingAddress(f.ctx, f.addressID, f.userID))
	read, err := orders.GetOrder(f.ctx, order.ID, biz.Actor{ID: f.userID})
	require.NoError(t, err)
	require.Equal(t, "Receiver A", read.Shipping.ReceiverName)
	require.Equal(t, "Address A", read.Shipping.DetailAddress)
	require.Equal(t, "13800138000", read.Shipping.ReceiverPhone)
	page, _, err := orders.ListOrders(f.ctx, biz.Actor{ID: f.userID}, &biz.ListOrdersReq{UserID: f.userID, Limit: 20})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "Address A", page[0].Shipping.DetailAddress)
	// A different authenticated actor may read the immutable fulfillment
	// snapshot only with the administrator role, including cached order pages.
	other := biz.Actor{ID: f.userID + 1}
	_, err = orders.GetOrder(f.ctx, order.ID, other)
	require.ErrorIs(t, err, biz.ErrOrderNotFound)
	_, _, err = orders.ListOrders(f.ctx, other, &biz.ListOrdersReq{UserID: f.userID, Limit: 20})
	require.Error(t, err)
	other.Admin = true
	read, err = orders.GetOrder(f.ctx, order.ID, other)
	require.NoError(t, err)
	require.Equal(t, "Address A", read.Shipping.DetailAddress)
	require.Equal(t, "13800138000", read.Shipping.ReceiverPhone)
	page, _, err = orders.ListOrders(f.ctx, other, &biz.ListOrdersReq{UserID: f.userID, Limit: 20})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, "Address A", page[0].Shipping.DetailAddress)
	require.Equal(t, "13800138000", page[0].Shipping.ReceiverPhone)
	replay, err := orders.CreateOrder(f.ctx, shippingCheckout(f))
	require.NoError(t, err)
	require.Equal(t, order.ID, replay.ID, "idempotent replay does not resolve deleted address IDs")
	_, err = f.pool.Exec(f.ctx, `UPDATE orders SET receiver_name='overwrite' WHERE id=$1`, order.ID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
}

type shippingSnapshotBarrier struct {
	db.Querier
	loaded, release chan struct{}
}

func (q *shippingSnapshotBarrier) GetShippingAddressForSnapshot(ctx context.Context, args db.GetShippingAddressForSnapshotParams) (db.ShippingAddress, error) {
	row, err := q.Querier.GetShippingAddressForSnapshot(ctx, args)
	close(q.loaded)
	<-q.release
	return row, err
}

type shippingSnapshotTx struct {
	biz.TxManager
	loaded, release chan struct{}
}

func (tx shippingSnapshotTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	return tx.TxManager.InTx(ctx, func(ctx context.Context) error {
		q := &shippingSnapshotBarrier{Querier: querierFromContext(ctx, nil), loaded: tx.loaded, release: tx.release}
		return fn(context.WithValue(ctx, ctxTxKey{}, q))
	})
}

func TestReviewConcurrentAddressDeleteWaitsForCheckoutSnapshotIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	_, err := f.pool.Exec(f.ctx, `UPDATE shipping_addresses SET receiver_phone_encrypt=$2 WHERE id=$1`, f.addressID, shippingCipher(t, "13800138000"))
	require.NoError(t, err)
	tx := shippingSnapshotTx{TxManager: f.tx, loaded: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(tx.release) }) })
	type result struct {
		order *biz.Order
		err   error
	}
	finished := make(chan result, 1)
	go func() {
		o, err := shippingOrderUsecase(f, tx).CreateOrder(f.ctx, shippingCheckout(f))
		finished <- result{o, err}
	}()
	<-tx.loaded
	deleter, err := f.pool.Begin(f.ctx)
	require.NoError(t, err)
	_, err = deleter.Exec(f.ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	_, err = deleter.Exec(f.ctx, `DELETE FROM shipping_addresses WHERE id=$1`, f.addressID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "55P03", pgErr.Code, "snapshot lock excludes concurrent mutation")
	require.NoError(t, deleter.Rollback(f.ctx))
	once.Do(func() { close(tx.release) })
	created := <-finished
	require.NoError(t, created.err)
	require.NoError(t, NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger).DeleteShippingAddress(f.ctx, f.addressID, f.userID))
	read, err := shippingOrderUsecase(f, f.tx).GetOrder(f.ctx, created.order.ID, biz.Actor{ID: f.userID})
	require.NoError(t, err)
	require.Equal(t, "13800138000", read.Shipping.ReceiverPhone)
}

func TestReviewConcurrentDefaultAddressSwitchesIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	repo := NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := repo.CreateShippingAddress(f.ctx, f.userID, fmt.Sprintf("Receiver %d", i), "hash", "cipher", "P", "C", "D", "Detail", "", true)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM shipping_addresses WHERE user_id=$1 AND is_default`, f.userID).Scan(&count))
	require.Equal(t, 1, count, "user-row lock handles the initially empty/default-less case")
}
