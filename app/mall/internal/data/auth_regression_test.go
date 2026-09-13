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
