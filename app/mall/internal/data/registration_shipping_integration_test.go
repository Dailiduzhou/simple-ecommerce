//go:build integration

package data

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type registrationRaceRepo struct {
	biz.UserRepo
	arrived chan struct{}
	release chan struct{}
}

func (r registrationRaceRepo) GetUserByPhoneHash(ctx context.Context, hash string) (*biz.User, error) {
	u, err := r.UserRepo.GetUserByPhoneHash(ctx, hash)
	r.arrived <- struct{}{}
	select {
	case <-r.release:
		return u, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestConcurrentRegistrationReturnsConflictIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	cfg := integrationAuthConfig()
	phone := fmt.Sprintf("139%08d", f.userID)
	hash := phonecrypto.HashPhone(phone, []byte(cfg.PhoneSecret))
	t.Cleanup(func() {
		_, err := f.pool.Exec(f.ctx, `DELETE FROM users WHERE phone_hash=$1`, hash)
		require.NoError(t, err)
	})
	r := registrationRaceRepo{NewUserRepo(f.data, log.DefaultLogger), make(chan struct{}, 2), make(chan struct{})}
	uc := biz.NewUserUsecase(r, nil, cfg, log.DefaultLogger)
	var workers sync.WaitGroup
	var once sync.Once
	release := func() { once.Do(func() { close(r.release) }) }
	defer func() { release(); cancel(); workers.Wait() }()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		workers.Go(func() { _, err := uc.Register(ctx, phone, "Password123!"); results <- err })
	}
	for i := 0; i < 2; i++ {
		select {
		case <-r.arrived:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	release()
	workers.Wait()
	first, second := <-results, <-results
	if first == nil {
		first, second = second, first
	}
	require.NoError(t, second)
	require.True(t, userv1.IsUserAlreadyExists(first), "%v", first)
	require.EqualValues(t, 409, errors.FromError(first).Code)
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE phone_hash=$1`, hash).Scan(&count))
	require.Equal(t, 1, count)
}

func TestInvalidShippingCannotCreateOrMutateOrdersIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	addresses := biz.NewShippingAddressUsecase(NewShippingAddressRepo(f.data, f.tx, log.DefaultLogger), integrationAuthConfig(), log.DefaultLogger)
	_, err := addresses.CreateShippingAddress(f.ctx, f.userID, "Receiver", "", "p", "c", "d", "detail", "", false)
	require.True(t, userv1.IsInvalidPhone(err))
	_, err = addresses.UpdateShippingAddress(f.ctx, f.addressID, f.userID, "Receiver", "", "p", "c", "d", "detail", "")
	require.True(t, userv1.IsInvalidPhone(err))
	// Simulate an invalid stored address: checkout must validate its own locked
	// snapshot, not assume an earlier address write or cached read validated it.
	_, err = f.pool.Exec(f.ctx, `UPDATE shipping_addresses SET receiver_phone_encrypt=$2 WHERE id=$1`, f.addressID, shippingCipher(t, ""))
	require.NoError(t, err)
	orders := shippingOrderUsecase(f, f.tx)
	req := shippingCheckout(f)
	_, err = orders.CreateOrder(f.ctx, req)
	require.True(t, userv1.IsInvalidPhone(err))
	var count, stock int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, f.userID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 100, stock)
	_, err = addresses.UpdateShippingAddress(f.ctx, f.addressID, f.userID, "Receiver", "13800138000", "p", "c", "d", "detail", "")
	require.NoError(t, err)
	order, err := orders.CreateOrder(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "13800138000", order.Shipping.ReceiverPhone)
	// Replay must still use the immutable order, even if the address later
	// becomes invalid. It must neither revalidate the address nor reserve again.
	_, err = f.pool.Exec(f.ctx, `UPDATE shipping_addresses SET receiver_phone_encrypt=$2 WHERE id=$1`, f.addressID, shippingCipher(t, ""))
	require.NoError(t, err)
	replay, err := orders.CreateOrder(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, order.ID, replay.ID)
	require.Equal(t, "13800138000", replay.Shipping.ReceiverPhone)
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT stock FROM products WHERE id=$1`, f.productID).Scan(&stock))
	require.Equal(t, 99, stock)
}
