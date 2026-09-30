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
	mu      sync.Mutex
	used    map[string]bool
	fail    bool
	owners  map[string]int64
	revoked map[string]bool
}

func (r *reviewAuthStore) CreateSession(_ context.Context, id string, userID int64, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("unavailable")
	}
	if r.owners == nil {
		r.owners = map[string]int64{}
		r.revoked = map[string]bool{}
		r.used = map[string]bool{}
	}
	r.owners[id] = userID
	return nil
}
func (r *reviewAuthStore) SessionActive(_ context.Context, id string, userID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return false, errors.New("unavailable")
	}
	return r.owners[id] == userID && !r.revoked[id], nil
}
func (r *reviewAuthStore) ConsumeSessionRefresh(_ context.Context, sid string, userID int64, id string, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return false, errors.New("unavailable")
	}
	if r.owners[sid] != userID || r.revoked[sid] || r.used[id] {
		return false, nil
	}
	r.used[id] = true
	return true, nil
}
func (r *reviewAuthStore) RevokeSession(_ context.Context, id string, userID int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return false, errors.New("unavailable")
	}
	if owner := r.owners[id]; owner != 0 && owner != userID {
		return false, nil
	}
	r.revoked[id] = true
	return true, nil
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
	pair, e := auth.StartSession(ctx, &biz.User{ID: 1, Role: "admin", AuthVersion: 1})
	require.NoError(t, e)
	user.Role = "user"
	claims, e := auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, e)
	handler := custommid.CheckBlacklist(auth)(func(_ context.Context, _ any) (any, error) { require.Equal(t, "user", claims.Role); return nil, nil })
	_, e = handler(kratosjwt.NewContext(ctx, claims), nil)
	require.NoError(t, e)
	reply, e := s.RefreshToken(ctx, &pb.RefreshRequest{RefreshToken: pair.RefreshToken})
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
	pair, e := auth.StartSession(context.Background(), &biz.User{ID: 1, Role: "admin", AuthVersion: 1})
	require.NoError(t, e)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := s.RefreshToken(context.Background(), &pb.RefreshRequest{RefreshToken: pair.RefreshToken}); e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), successes.Load())
	pair, e = auth.StartSession(context.Background(), &biz.User{ID: 1, Role: "user", AuthVersion: 1})
	require.NoError(t, e)
	store.fail = true
	reply, e := s.RefreshToken(context.Background(), &pb.RefreshRequest{RefreshToken: pair.RefreshToken})
	require.Error(t, e)
	require.Nil(t, reply)
}
