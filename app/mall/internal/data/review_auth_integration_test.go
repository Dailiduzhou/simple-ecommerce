//go:build integration

package data

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

type authReadBarrier struct {
	db.Querier
	loaded, release chan struct{}
}

func (q *authReadBarrier) GetUserByPhoneHash(ctx context.Context, hash string) (db.User, error) {
	row, err := q.Querier.GetUserByPhoneHash(ctx, hash)
	close(q.loaded)
	<-q.release
	return row, err
}
func integrationAuthConfig() *conf.Auth {
	return &conf.Auth{PhoneSecret: "integration-phone-secret", AccessTokenSecret: "integration-access-secret",
		RefreshTokenSecret: "integration-refresh-secret", AccessTokenTimeout: durationpb.New(time.Hour), RefreshTokenTimeout: durationpb.New(24 * time.Hour)}
}

func TestReviewCredentialVersionsAndCASIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	repo := NewUserRepo(f.data, log.DefaultLogger)
	store := NewAuthRepo(f.rdb, log.DefaultLogger)
	auth := biz.NewAuthUsecase(repo, store, integrationAuthConfig())
	user, err := repo.GetAuthUser(f.ctx, f.userID)
	require.NoError(t, err)
	require.EqualValues(t, 1, user.AuthVersion)
	pair, err := auth.StartSession(f.ctx, user)
	require.NoError(t, err)
	claims, err := auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	require.NoError(t, auth.ValidateAccount(f.ctx, claims), "new account token is valid immediately, independent of timestamp truncation")
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- repo.UpdateUserPassword(f.ctx, f.userID, 1, "replacement-hash")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			require.Contains(t, err.Error(), "CREDENTIAL_VERSION_CONFLICT")
		}
	}
	require.Equal(t, 1, success)
	require.Error(t, auth.ValidateAccount(f.ctx, claims))
	// A delayed signer can only sign the version of the credentials it verified.
	pair, err = auth.StartSession(f.ctx, user)
	require.NoError(t, err)
	claims, err = auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	require.Error(t, auth.ValidateAccount(f.ctx, claims))
	newUser, err := repo.GetAuthUser(f.ctx, f.userID)
	require.NoError(t, err)
	require.EqualValues(t, 2, newUser.AuthVersion)
	pair, err = auth.StartSession(f.ctx, newUser)
	require.NoError(t, err)
	claims, err = auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	require.NoError(t, auth.ValidateAccount(f.ctx, claims))
}

func TestReviewPausedLoginCannotSurvivePasswordChangeIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	config := integrationAuthConfig()
	phone := "13800138000"
	oldHash, err := pwdhash.HashPassword("old-password")
	require.NoError(t, err)
	phoneHash := phonecrypto.HashPhone(phone, []byte(config.PhoneSecret))
	_, err = f.pool.Exec(f.ctx, `UPDATE users SET phone_hash=$2,password_hash=$3 WHERE id=$1`, f.userID, phoneHash, oldHash)
	require.NoError(t, err)
	barrier := &authReadBarrier{Querier: f.data.q, loaded: make(chan struct{}), release: make(chan struct{})}
	f.data.q = barrier
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(barrier.release) }) })
	repo := NewUserRepo(f.data, log.DefaultLogger)
	store := NewAuthRepo(f.rdb, log.DefaultLogger)
	users := biz.NewUserUsecase(repo, store, config, log.DefaultLogger)
	type outcome struct {
		user *biz.User
		err  error
	}
	result := make(chan outcome, 1)
	go func() { u, err := users.Login(f.ctx, phone, "old-password"); result <- outcome{u, err} }()
	<-barrier.loaded
	require.NoError(t, users.ChangePassword(f.ctx, f.userID, "old-password", "new-password"))
	unblock.Do(func() { close(barrier.release) })
	login := <-result
	require.NoError(t, login.err)
	require.EqualValues(t, 1, login.user.AuthVersion)
	auth := biz.NewAuthUsecase(repo, store, config)
	pair, err := auth.StartSession(f.ctx, login.user)
	require.NoError(t, err)
	for _, refresh := range []bool{false, true} {
		var token string
		if refresh {
			token = pair.RefreshToken
		} else {
			token = pair.AccessToken
		}
		require.NoError(t, err)
		var claims *biz.EcommerceClaims
		if refresh {
			claims, err = auth.ParseRefreshToken(token)
		} else {
			claims, err = auth.ParseAccessToken(token)
		}
		require.NoError(t, err)
		require.ErrorIs(t, auth.ValidateAccount(f.ctx, claims), userv1.ErrorUnauthorized(""))
	}
}

func TestReviewAuthLookupCannotPolluteNewProfileGenerationIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	barrier := &authReadBarrier{Querier: f.data.q, loaded: make(chan struct{}), release: make(chan struct{})}
	f.data.q = barrier
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(barrier.release) }) })
	repo := NewUserRepo(f.data, log.DefaultLogger)
	result := make(chan *biz.User, 1)
	failure := make(chan error, 1)
	go func() { u, err := repo.GetUserByPhoneHash(f.ctx, f.prefix); result <- u; failure <- err }()
	<-barrier.loaded
	_, err := repo.UpdateUser(f.ctx, f.userID, "updated-B", "")
	require.NoError(t, err)
	unblock.Do(func() { close(barrier.release) })
	u := <-result
	require.NoError(t, <-failure)
	require.Equal(t, "integration", u.Nickname)
	profile, err := repo.GetUserByID(f.ctx, f.userID)
	require.NoError(t, err)
	require.Equal(t, "updated-B", profile.Nickname)
	profile, err = repo.GetUserByID(f.ctx, f.userID)
	require.NoError(t, err)
	require.Equal(t, "updated-B", profile.Nickname)
}

type sessionRefreshBarrier struct {
	biz.AuthRepo
	committed, release chan struct{}
}

func (r *sessionRefreshBarrier) ConsumeSessionRefresh(ctx context.Context, sid string, uid int64, jti string, ttl time.Duration) (bool, error) {
	ok, err := r.AuthRepo.ConsumeSessionRefresh(ctx, sid, uid, jti, ttl)
	close(r.committed)
	<-r.release
	return ok, err
}

type sessionAmbiguousRevoke struct {
	biz.AuthRepo
	failResponse bool
}

func (r *sessionAmbiguousRevoke) RevokeSession(ctx context.Context, sid string, uid int64) (bool, error) {
	ok, err := r.AuthRepo.RevokeSession(ctx, sid, uid)
	if err == nil && r.failResponse {
		r.failResponse = false
		return false, errors.New("lost response after Redis commit")
	}
	return ok, err
}

func TestReviewSessionRevocationAndRotationIntegration(t *testing.T) {
	f := newCorrectnessFixture(t)
	repo := NewUserRepo(f.data, log.DefaultLogger)
	store := NewAuthRepo(f.rdb, log.DefaultLogger)
	barrier := &sessionRefreshBarrier{AuthRepo: store, committed: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(barrier.release) }) })
	auth := biz.NewAuthUsecase(repo, barrier, integrationAuthConfig())
	user, err := repo.GetAuthUser(f.ctx, f.userID)
	require.NoError(t, err)
	pair, err := auth.StartSession(f.ctx, user)
	require.NoError(t, err)
	claims, err := auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	type result struct {
		pair *biz.TokenPair
		err  error
	}
	finished := make(chan result, 1)
	go func() { pair, err := auth.RefreshSession(f.ctx, pair.RefreshToken); finished <- result{pair, err} }()
	<-barrier.committed
	require.NoError(t, auth.Logout(f.ctx, claims, ""))
	once.Do(func() { close(barrier.release) })
	rotated := <-finished
	require.NoError(t, rotated.err)
	newClaims, err := auth.ParseAccessToken(rotated.pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, claims.SessionID, newClaims.SessionID)
	require.Error(t, auth.ValidateSession(f.ctx, newClaims))
	plain := biz.NewAuthUsecase(repo, store, integrationAuthConfig())
	_, err = plain.RefreshSession(f.ctx, rotated.pair.RefreshToken)
	require.Error(t, err)
	require.NoError(t, plain.Logout(f.ctx, claims, pair.RefreshToken))
	// A failed response after a successful write must remain retryable.
	wrapped := &sessionAmbiguousRevoke{AuthRepo: store, failResponse: true}
	auth = biz.NewAuthUsecase(repo, wrapped, integrationAuthConfig())
	pair, err = auth.StartSession(f.ctx, user)
	require.NoError(t, err)
	claims, err = auth.ParseAccessToken(pair.AccessToken)
	require.NoError(t, err)
	err = auth.Logout(f.ctx, claims, "")
	require.Equal(t, int32(503), kratoserrors.FromError(err).Code)
	require.Error(t, auth.ValidateSession(f.ctx, claims))
	require.NoError(t, auth.Logout(f.ctx, claims, ""))
}
