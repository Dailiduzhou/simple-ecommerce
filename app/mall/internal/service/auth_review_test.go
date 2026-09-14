package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	custommid "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/server/middleware"
	"github.com/go-kratos/kratos/v2/log"
	kratosjwt "github.com/go-kratos/kratos/v2/middleware/auth/jwt"
	"github.com/stretchr/testify/require"
)

type reviewAuthStore struct {
	mu   sync.Mutex
	used map[string]bool
	fail bool
}

func (r *reviewAuthStore) ConsumeRefresh(_ context.Context, id string, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return false, errors.New("unavailable")
	}
	if r.used[id] {
		return false, nil
	}
	r.used[id] = true
	return true, nil
}
func (r *reviewAuthStore) SetBlacklist(ctx context.Context, id string, ttl time.Duration) error {
	_, e := r.ConsumeRefresh(ctx, id, ttl)
	return e
}
func (r *reviewAuthStore) IsBlacklisted(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used[id], nil
}

func (r *reviewAuthStore) LoginFailures(_ context.Context, _ string) (int64, error) { return 0, nil }

func (r *reviewAuthStore) RecordLoginFailure(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func (r *reviewAuthStore) ClearLoginFailures(_ context.Context, _ string) error { return nil }
func TestReviewRefreshAndAccountRevocation(t *testing.T) {
	ctx := context.Background()
	user := &biz.User{ID: 1, Role: "admin"}
	repo := &fakeUserRepo{getUserByID: func(context.Context, int64) (*biz.User, error) { return user, nil }, deleteUser: func(context.Context, int64) error { user = nil; return nil }}
	store := &reviewAuthStore{used: map[string]bool{}}
	ac := testAuthConf()
	auth := biz.NewAuthUsecase(repo, store, ac)
	s := NewUserService(auth, biz.NewUserUsecase(repo, store, ac, log.DefaultLogger), nil, nil, log.DefaultLogger)
	access, e := auth.GenerateAccessToken(1, "admin")
	require.NoError(t, e)
	refresh, e := auth.GenerateRefreshToken(1, "admin")
	require.NoError(t, e)
	user.Role = "user"
	claims, e := auth.ParseAccessToken(access)
	require.NoError(t, e)
	handler := custommid.CheckBlacklist(auth)(func(_ context.Context, _ any) (any, error) { require.Equal(t, "user", claims.Role); return nil, nil })
	_, e = handler(kratosjwt.NewContext(ctx, claims), nil)
	require.NoError(t, e)
	reply, e := s.RefreshToken(ctx, &pb.RefreshRequest{RefreshToken: refresh})
	require.NoError(t, e)
	newClaims, e := auth.ParseAccessToken(reply.AccessToken)
	require.NoError(t, e)
	require.Equal(t, "user", newClaims.Role)
	_, e = s.DeleteUser(biz.WithClaims(ctx, claims), &pb.DeleteUserRequest{Id: 1})
	require.NoError(t, e)
	_, e = handler(kratosjwt.NewContext(ctx, claims), nil)
	require.Error(t, e)
	_, e = s.RefreshToken(ctx, &pb.RefreshRequest{RefreshToken: reply.RefreshToken})
	require.Error(t, e)
}
func TestReviewRefreshConcurrentAndStoreFailure(t *testing.T) {
	repo := &fakeUserRepo{getUserByID: func(context.Context, int64) (*biz.User, error) { return &biz.User{ID: 1, Role: "user"}, nil }}
	store := &reviewAuthStore{used: map[string]bool{}}
	auth := biz.NewAuthUsecase(repo, store, testAuthConf())
	s := NewUserService(auth, nil, nil, nil, log.DefaultLogger)
	token, e := auth.GenerateRefreshToken(1, "admin")
	require.NoError(t, e)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.RefreshToken(context.Background(), &pb.RefreshRequest{RefreshToken: token}); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), successes.Load())
	store.fail = true
	token, e = auth.GenerateRefreshToken(1, "user")
	require.NoError(t, e)
	reply, e := s.RefreshToken(context.Background(), &pb.RefreshRequest{RefreshToken: token})
	require.Error(t, e)
	require.Nil(t, reply)
}
