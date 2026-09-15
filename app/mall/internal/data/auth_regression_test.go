package data

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestReviewAtomicRefresh(t *testing.T) {
	mr := miniredis.RunT(t)
	d := newTestData(t, mockdb.NewMockQuerier(gomock.NewController(t)), mr)
	r := NewAuthRepo(d.rdb, log.DefaultLogger)
	ctx := context.Background()
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := r.ConsumeRefresh(ctx, "same", time.Minute)
			require.NoError(t, e)
			if ok {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), successes.Load())
	require.Equal(t, time.Minute, mr.TTL("jwt:blacklist:same"))
	require.NoError(t, r.SetBlacklist(ctx, "logout", time.Minute))
	ok, e := r.ConsumeRefresh(ctx, "logout", time.Minute)
	require.NoError(t, e)
	require.False(t, ok)
	mr.SetError("store failed")
	_, e = r.ConsumeRefresh(ctx, "new", time.Minute)
	require.Error(t, e)
}
func TestReviewAuthBypassesDeletedProfile(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	r := NewUserRepo(&Data{q: q}, log.DefaultLogger)
	q.EXPECT().GetUserByID(gomock.Any(), int64(1)).Return(db.User{}, pgx.ErrNoRows)
	u, e := r.GetAuthUser(context.Background(), 1)
	require.NoError(t, e)
	require.Nil(t, u)
}

// The login-failure window is armed once from the first failure and is never
// extended by later attempts, so an attacker cannot stretch a lockout or keep
// a counter alive by probing.
func TestLoginFailureWindowArmsOnceAndExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	d := newTestData(t, mockdb.NewMockQuerier(gomock.NewController(t)), mr)
	r := NewAuthRepo(d.rdb, log.DefaultLogger)
	ctx := context.Background()

	n, e := r.LoginFailures(ctx, "hash-a")
	require.NoError(t, e)
	require.EqualValues(t, 0, n)

	for i := 0; i < 3; i++ {
		require.NoError(t, r.RecordLoginFailure(ctx, "hash-a", 15*time.Minute))
	}
	n, e = r.LoginFailures(ctx, "hash-a")
	require.NoError(t, e)
	require.EqualValues(t, 3, n)
	first := mr.TTL("auth:login:fail:hash-a")
	require.Greater(t, first, time.Duration(0))
	require.LessOrEqual(t, first, 15*time.Minute)

	// A later failure must not extend the window set by the first one.
	mr.FastForward(10 * time.Minute)
	require.NoError(t, r.RecordLoginFailure(ctx, "hash-a", 15*time.Minute))
	second := mr.TTL("auth:login:fail:hash-a")
	require.LessOrEqual(t, second, 5*time.Minute)

	require.NoError(t, r.ClearLoginFailures(ctx, "hash-a"))
	n, e = r.LoginFailures(ctx, "hash-a")
	require.NoError(t, e)
	require.EqualValues(t, 0, n)

	// A non-positive window is rejected instead of arming a sticky counter.
	require.Error(t, r.RecordLoginFailure(ctx, "hash-a", 0))
}

func TestLoginFailureStoreErrorsPropagate(t *testing.T) {
	mr := miniredis.RunT(t)
	d := newTestData(t, mockdb.NewMockQuerier(gomock.NewController(t)), mr)
	r := NewAuthRepo(d.rdb, log.DefaultLogger)
	ctx := context.Background()
	mr.SetError("store failed")
	_, e := r.LoginFailures(ctx, "hash-a")
	require.Error(t, e)
	require.Error(t, r.RecordLoginFailure(ctx, "hash-a", time.Minute))
	require.Error(t, r.ClearLoginFailures(ctx, "hash-a"))
}
