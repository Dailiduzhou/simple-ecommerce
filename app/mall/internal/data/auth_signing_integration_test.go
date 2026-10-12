//go:build integration

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

func TestPausedRefreshCannotSurvivePasswordChangeIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	repo := NewUserRepo(f.data, log.DefaultLogger)
	store := NewAuthRepo(f.rdb, log.DefaultLogger)
	barrier := &sessionRefreshBarrier{AuthRepo: store, committed: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(barrier.release) }) }
	defer unblock()
	auth := biz.NewAuthUsecase(repo, barrier, integrationAuthConfig())
	user, err := repo.GetAuthUser(f.ctx, f.userID)
	require.NoError(t, err)
	pair, err := auth.StartSession(f.ctx, user)
	require.NoError(t, err)
	type outcome struct {
		pair *biz.TokenPair
		err  error
	}
	finished := make(chan outcome, 1)
	go func() {
		pair, err := auth.RefreshSession(ctx, pair.RefreshToken)
		finished <- outcome{pair, err}
	}()
	select {
	case <-barrier.committed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, repo.UpdateUserPassword(ctx, f.userID, user.AuthVersion, "replacement-hash"))
	unblock()
	var refreshed outcome
	select {
	case refreshed = <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, refreshed.err)
	access, err := auth.ParseAccessToken(refreshed.pair.AccessToken)
	require.NoError(t, err)
	refresh, err := auth.ParseRefreshToken(refreshed.pair.RefreshToken)
	require.NoError(t, err)
	for _, claims := range []*biz.EcommerceClaims{access, refresh} {
		require.Equal(t, user.AuthVersion, claims.AuthVersion, "delayed signing must retain the verified version")
		require.Error(t, auth.ValidateAccount(ctx, claims))
	}
	_, err = auth.RefreshSession(f.ctx, refreshed.pair.RefreshToken)
	require.Error(t, err)
}
