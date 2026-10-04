package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	custommid "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/server/middleware"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	kratosjwt "github.com/go-kratos/kratos/v2/middleware/auth/jwt"
	"github.com/stretchr/testify/require"
	"time"
)

type ambiguousSessionStore struct {
	biz.AuthRepo
	failResponse bool
}

func (r *ambiguousSessionStore) RevokeSession(ctx context.Context, sid string, userID int64) (bool, error) {
	ok, err := r.AuthRepo.RevokeSession(ctx, sid, userID)
	if err == nil && r.failResponse {
		r.failResponse = false
		return false, errors.New("response lost after commit")
	}
	return ok, err
}

type refreshSessionBarrier struct {
	biz.AuthRepo
	committed, release chan struct{}
}

func (r *refreshSessionBarrier) ConsumeSessionRefresh(ctx context.Context, sid string, uid int64, jti string, ttl time.Duration) (bool, error) {
	ok, err := r.AuthRepo.ConsumeSessionRefresh(ctx, sid, uid, jti, ttl)
	close(r.committed)
	<-r.release
	return ok, err
}

func sessionAccount() *fakeUserRepo {
	return &fakeUserRepo{getUserByID: func(_ context.Context, id int64) (*biz.User, error) {
		return &biz.User{ID: id, Role: "user", AuthVersion: 1}, nil
	}}
}
func TestLogoutRetriesAfterStoreFailureAndAmbiguousCommit(t *testing.T) {
	ctx := context.Background()
	store := &reviewAuthStore{}
	wrapped := &ambiguousSessionStore{AuthRepo: store}
	auth := biz.NewAuthUsecase(sessionAccount(), wrapped, testAuthConf())
	pair, err := auth.StartSession(ctx, &biz.User{ID: 1, Role: "user", AuthVersion: 1})
	require.NoError(t, err)
	claims, err := auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	store.fail = true
	err = auth.Logout(ctx, claims, pair.RefreshToken)
	require.Equal(t, int32(503), kratoserrors.FromError(err).Code)
	store.fail = false
	require.NoError(t, auth.ValidateSession(ctx, claims), "failed write cannot report success")
	wrapped.failResponse = true
	err = auth.Logout(ctx, claims, pair.RefreshToken)
	require.Equal(t, int32(503), kratoserrors.FromError(err).Code)
	require.Error(t, auth.ValidateSession(ctx, claims))
	// Retry reaches the handler despite an already-revoked SID, but ordinary
	// authenticated routes cannot use that exception.
	handler := custommid.CheckBlacklist(auth)(func(ctx context.Context, _ any) (any, error) { return nil, auth.Logout(ctx, claims, pair.RefreshToken) })
	_, err = handler(kratosjwt.NewContext(ctx, claims), &pb.LogoutRequest{})
	require.NoError(t, err)
	_, err = handler(kratosjwt.NewContext(ctx, claims), &pb.GetUserRequest{})
	require.Error(t, err)
	_, err = auth.RefreshSession(ctx, pair.RefreshToken)
	require.Error(t, err)
}

func TestLogoutRejectsUnrelatedRefreshWithoutRevokingEither(t *testing.T) {
	ctx := context.Background()
	auth := biz.NewAuthUsecase(sessionAccount(), &reviewAuthStore{}, testAuthConf())
	a, err := auth.StartSession(ctx, &biz.User{ID: 1, Role: "user", AuthVersion: 1})
	require.NoError(t, err)
	ac, err := auth.ParseAccessToken(a.AccessToken)
	require.NoError(t, err)
	for _, owner := range []int64{1, 2} {
		b, err := auth.StartSession(ctx, &biz.User{ID: owner, Role: "user", AuthVersion: 1})
		require.NoError(t, err)
		bc, err := auth.ParseAccessToken(b.AccessToken)
		require.NoError(t, err)
		require.Error(t, auth.Logout(ctx, ac, b.RefreshToken))
		require.NoError(t, auth.ValidateSession(ctx, ac))
		require.NoError(t, auth.ValidateSession(ctx, bc))
	}
	require.NoError(t, auth.Logout(ctx, ac, ""), "refresh is optional because the whole SID is revoked")
	_, err = auth.RefreshSession(ctx, a.RefreshToken)
	require.Error(t, err)
}

func TestRefreshCommittedBeforeLogoutCannotEscapeRevocation(t *testing.T) {
	ctx := context.Background()
	barrier := &refreshSessionBarrier{AuthRepo: &reviewAuthStore{}, committed: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(barrier.release) }) })
	auth := biz.NewAuthUsecase(sessionAccount(), barrier, testAuthConf())
	pair, err := auth.StartSession(ctx, &biz.User{ID: 1, Role: "user", AuthVersion: 1})
	require.NoError(t, err)
	claims, err := auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	type result struct {
		pair *biz.TokenPair
		err  error
	}
	finished := make(chan result, 1)
	go func() { pair, err := auth.RefreshSession(ctx, pair.RefreshToken); finished <- result{pair, err} }()
	<-barrier.committed
	require.NoError(t, auth.Logout(ctx, claims, ""))
	once.Do(func() { close(barrier.release) })
	rotated := <-finished
	require.NoError(t, rotated.err)
	newClaims, err := auth.ParseAccessToken(rotated.pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, claims.SessionID, newClaims.SessionID)
	require.Error(t, auth.ValidateSession(ctx, newClaims), "tokens signed after logout share the revoked SID")
	// Use the underlying store without the one-shot test barrier.
	plain := biz.NewAuthUsecase(sessionAccount(), barrier.AuthRepo, testAuthConf())
	_, err = plain.RefreshSession(ctx, rotated.pair.RefreshToken)
	require.Error(t, err)
	oldRefresh, err := auth.ParseRefreshToken(pair.RefreshToken)
	require.NoError(t, err)
	newRefresh, err := auth.ParseRefreshToken(rotated.pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, oldRefresh.ExpiresAt, newRefresh.ExpiresAt, "rotation never extends the fixed family deadline")
}
