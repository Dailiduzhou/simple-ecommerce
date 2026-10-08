//go:build integration

package data

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/server/middleware"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type loginCountingRepo struct {
	biz.UserRepo
	reads atomic.Int64
	hash  string
}

func (r *loginCountingRepo) GetUserByPhoneHash(context.Context, string) (*biz.User, error) {
	r.reads.Add(1)
	return &biz.User{ID: 1, PasswordHash: r.hash}, nil
}

func TestReviewLoginQuotaAcrossIPsAndInstancesIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	hash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	repo := &loginCountingRepo{hash: hash}
	clients := make([]biz.UserUsecase, 4)
	for i := range clients {
		rdb := redis.NewClient(f.rdb.Options())
		t.Cleanup(func() { _ = rdb.Close() })
		clients[i] = biz.NewUserUsecase(repo, NewAuthRepo(rdb, log.DefaultLogger), integrationAuthConfig(), log.DefaultLogger)
	}
	start := make(chan struct{})
	results := make(chan error, 40)
	for i := range 40 {
		go func() {
			<-start
			ctx := middleware.WithClientIP(f.ctx, fmt.Sprintf("192.0.2.%d", i+1))
			_, err := clients[i%len(clients)].Login(ctx, "13800138000", "wrong-pass")
			results <- err
		}()
	}
	close(start)
	checked, denied := 0, 0
	for range 40 {
		err := <-results
		switch {
		case userv1.IsInvalidCredentials(err):
			checked++
		case userv1.IsUserLoginLocked(err):
			denied++
		default:
			t.Fatalf("unexpected login result: %v", err)
		}
	}
	require.Equal(t, 5, checked)
	require.Equal(t, 35, denied)
	require.EqualValues(t, 5, repo.reads.Load(), "only admitted attempts reach password lookup")
	// Successful attempts also consume the shared budget; changing IP or
	// alternating a known password cannot erase concurrent reservations.
	for range 5 {
		_, err := clients[0].Login(f.ctx, "13900139000", "secret-pass")
		require.NoError(t, err)
	}
	_, err = clients[1].Login(f.ctx, "13900139000", "secret-pass")
	require.True(t, userv1.IsUserLoginLocked(err))
	// Real Redis expiration restores quota without an application clock.
	store := NewAuthRepo(f.rdb, log.DefaultLogger)
	wait, err := store.ReserveLoginAttempt(f.ctx, "recovery", 1, time.Second)
	require.NoError(t, err)
	require.Zero(t, wait)
	wait, err = store.ReserveLoginAttempt(f.ctx, "recovery", 1, time.Second)
	require.NoError(t, err)
	require.Positive(t, wait)
	require.Eventually(t, func() bool {
		wait, err := store.ReserveLoginAttempt(f.ctx, "recovery", 1, time.Second)
		return err == nil && wait == 0
	}, 3*time.Second, 25*time.Millisecond)
}
