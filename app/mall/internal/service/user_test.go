package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

type fakeUserRepo struct {
	createUser         func(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*biz.User, error)
	getUserByID        func(ctx context.Context, id int64) (*biz.User, error)
	getUserByPhoneHash func(ctx context.Context, phoneHash string) (*biz.User, error)
	updateUser         func(ctx context.Context, id int64, nickname, realName string) (*biz.User, error)
	updateUserPassword func(ctx context.Context, id int64, passwordHash string) error
	deleteUser         func(ctx context.Context, id int64) error
}

func (r *fakeUserRepo) CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*biz.User, error) {
	return r.createUser(ctx, nickname, phoneHash, phoneEncrypt, passwordHash)
}

func (r *fakeUserRepo) GetUserByID(ctx context.Context, id int64) (*biz.User, error) {
	return r.getUserByID(ctx, id)
}

func (r *fakeUserRepo) GetUserByPhoneHash(ctx context.Context, phoneHash string) (*biz.User, error) {
	return r.getUserByPhoneHash(ctx, phoneHash)
}

func (r *fakeUserRepo) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*biz.User, error) {
	return r.updateUser(ctx, id, nickname, realName)
}

func (r *fakeUserRepo) UpdateUserPassword(ctx context.Context, id int64, passwordHash string) error {
	if r.updateUserPassword == nil {
		return nil
	}
	return r.updateUserPassword(ctx, id, passwordHash)
}

func (r *fakeUserRepo) DeleteUser(ctx context.Context, id int64) error {
	return r.deleteUser(ctx, id)
}

type fakeAuthRepo struct {
	mu       sync.Mutex
	consumed map[string]bool
}

func (r *fakeAuthRepo) SetBlacklist(ctx context.Context, tokenID string, expiration time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumed == nil {
		r.consumed = map[string]bool{}
	}
	r.consumed[tokenID] = true
	return nil
}

func (r *fakeAuthRepo) IsBlacklisted(ctx context.Context, tokenID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consumed[tokenID], nil
}

func (r *fakeAuthRepo) LoginFailures(context.Context, string) (int64, error) { return 0, nil }

func (r *fakeAuthRepo) RecordLoginFailure(context.Context, string, time.Duration) error { return nil }

func (r *fakeAuthRepo) ClearLoginFailures(context.Context, string) error { return nil }

func testAuthConf() *conf.Auth {
	return &conf.Auth{
		AccessTokenSecret:   "access-secret",
		AccessTokenTimeout:  durationpb.New(time.Hour),
		RefreshTokenSecret:  "refresh-secret",
		RefreshTokenTimeout: durationpb.New(24 * time.Hour),
		PhoneSecret:         "phone-secret",
	}
}

func newTestUserService(userRepo biz.UserRepo) *UserService {
	ac := testAuthConf()
	authUc := biz.NewAuthUsecase(userRepo, &fakeAuthRepo{}, ac)
	userUc := biz.NewUserUsecase(userRepo, &fakeAuthRepo{}, ac, log.DefaultLogger)
	return NewUserService(authUc, userUc, nil, nil, log.DefaultLogger)
}

func TestUserService_Register(t *testing.T) {
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*biz.User, error) {
			return nil, nil
		},
		createUser: func(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*biz.User, error) {
			return &biz.User{ID: 7, Nickname: nickname, PhoneHash: phoneHash, PhoneEncrypt: phoneEncrypt, PasswordHash: passwordHash, Role: "user"}, nil
		},
	}
	s := newTestUserService(repo)

	got, err := s.Register(context.Background(), &pb.RegisterRequest{
		Phone:    "13800138000",
		Password: "secret-pass",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(7), got.Id)
}

func TestUserService_Login(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*biz.User, error) {
			return &biz.User{ID: 9, PasswordHash: passwordHash, Role: "admin"}, nil
		},
	}
	s := newTestUserService(repo)

	got, err := s.Login(context.Background(), &pb.LoginRequest{
		Phone:    "13800138000",
		Password: "secret-pass",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(9), got.Id)
	assert.NotEmpty(t, got.Token)
	assert.NotEmpty(t, got.RefreshToken)

	claims, err := s.authUc.ParseAccessToken(got.Token)
	require.NoError(t, err)
	assert.Equal(t, int64(9), claims.UserID)
	assert.Equal(t, "admin", claims.Role)
}

func TestUserService_GetUser_NotFound(t *testing.T) {
	repo := &fakeUserRepo{
		getUserByID: func(ctx context.Context, id int64) (*biz.User, error) {
			assert.Equal(t, int64(404), id)
			return nil, nil
		},
	}
	s := newTestUserService(repo)

	got, err := s.GetUser(authenticatedPaymentContext(404, "user"), &pb.GetUserRequest{Id: 404})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, pb.IsUserNotFound(err))
}

func TestUserService_UpdateUser(t *testing.T) {
	repo := &fakeUserRepo{
		updateUser: func(ctx context.Context, id int64, nickname, realName string) (*biz.User, error) {
			assert.Equal(t, int64(3), id)
			assert.Equal(t, "nick", nickname)
			assert.Equal(t, "real", realName)
			return &biz.User{ID: id, Nickname: nickname, RealName: realName, Role: "user"}, nil
		},
	}
	s := newTestUserService(repo)

	got, err := s.UpdateUser(authenticatedPaymentContext(3, "user"), &pb.UpdateUserRequest{
		Id:       3,
		Nickname: "nick",
		RealName: "real",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.Id)
	assert.Equal(t, "nick", got.Nickname)
	assert.Equal(t, "real", got.RealName)
	assert.Equal(t, "user", got.Role)
}

func TestUserService_RejectsCrossUserAccess(t *testing.T) {
	s := newTestUserService(&fakeUserRepo{})
	_, err := s.GetUser(authenticatedPaymentContext(3, "user"), &pb.GetUserRequest{Id: 4})
	require.True(t, kratoserrors.IsForbidden(err))
}

func TestUserService_ChangePasswordUsesTheAuthenticatedAccount(t *testing.T) {
	oldHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	var rotated int64
	repo := &fakeUserRepo{
		getUserByID: func(_ context.Context, id int64) (*biz.User, error) {
			return &biz.User{ID: id, PasswordHash: oldHash}, nil
		},
		updateUserPassword: func(_ context.Context, id int64, _ string) error {
			rotated = id
			return nil
		},
	}
	s := newTestUserService(repo)

	// The handler takes the account from the verified claims, never the body.
	_, err = s.ChangePassword(authenticatedPaymentContext(9, "user"), &pb.ChangePasswordRequest{OldPassword: "secret-pass", NewPassword: "brand-new-pass"})
	require.NoError(t, err)
	require.Equal(t, int64(9), rotated)
}

func TestUserService_ChangePasswordRequiresAuthentication(t *testing.T) {
	s := newTestUserService(&fakeUserRepo{})
	_, err := s.ChangePassword(context.Background(), &pb.ChangePasswordRequest{OldPassword: "secret-pass", NewPassword: "brand-new-pass"})
	require.Error(t, err)
	require.True(t, kratoserrors.IsUnauthorized(err))
}

func TestUserService_DeleteUser(t *testing.T) {
	repo := &fakeUserRepo{
		deleteUser: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(5), id)
			return nil
		},
	}
	s := newTestUserService(repo)

	got, err := s.DeleteUser(authenticatedPaymentContext(5, "user"), &pb.DeleteUserRequest{Id: 5})
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestUserService_DeleteUser_Error(t *testing.T) {
	repo := &fakeUserRepo{
		deleteUser: func(ctx context.Context, id int64) error {
			return errors.New("db failed")
		},
	}
	s := newTestUserService(repo)

	got, err := s.DeleteUser(authenticatedPaymentContext(5, "user"), &pb.DeleteUserRequest{Id: 5})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Equal(t, int32(500), kratoserrors.FromError(err).Code)
}

func (r *fakeUserRepo) GetAuthUser(ctx context.Context, id int64) (*biz.User, error) {
	return r.GetUserByID(ctx, id)
}

func (r *fakeAuthRepo) ConsumeRefresh(ctx context.Context, id string, ttl time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.consumed == nil {
		r.consumed = map[string]bool{}
	}
	if r.consumed[id] {
		return false, nil
	}
	r.consumed[id] = true
	return true, nil
}

func TestUserService_Logout(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*biz.User, error) {
			return &biz.User{ID: 9, PasswordHash: passwordHash, Role: "user"}, nil
		},
	}
	store := &fakeAuthRepo{}
	ac := testAuthConf()
	authUc := biz.NewAuthUsecase(repo, store, ac)
	userUc := biz.NewUserUsecase(repo, store, ac, log.DefaultLogger)
	s := NewUserService(authUc, userUc, nil, nil, log.DefaultLogger)
	ctx := context.Background()

	login, err := s.Login(ctx, &pb.LoginRequest{Phone: "13800138000", Password: "secret-pass"})
	require.NoError(t, err)
	claims, err := authUc.ParseAccessToken(login.Token)
	require.NoError(t, err)

	// Logout revokes the access token and burns the supplied refresh token.
	_, err = s.Logout(biz.WithClaims(ctx, claims), &pb.LogoutRequest{RefreshToken: login.RefreshToken})
	require.NoError(t, err)
	revoked, err := authUc.IsTokenBlacklisted(ctx, claims.ID)
	require.NoError(t, err)
	require.True(t, revoked, "logout must blacklist the access token jti")
	_, err = s.RefreshToken(ctx, &pb.RefreshRequest{RefreshToken: login.RefreshToken})
	require.Error(t, err, "logout must burn the refresh token")

	// Unauthenticated logout is rejected before any revocation happens.
	_, err = s.Logout(ctx, &pb.LogoutRequest{})
	require.True(t, kratoserrors.IsUnauthorized(err))

	// A garbage refresh token is ignored: the access token is still revoked.
	second, err := authUc.GenerateAccessToken(9, "user")
	require.NoError(t, err)
	secondClaims, err := authUc.ParseAccessToken(second)
	require.NoError(t, err)
	_, err = s.Logout(biz.WithClaims(ctx, secondClaims), &pb.LogoutRequest{RefreshToken: "garbage-token"})
	require.NoError(t, err)
	revoked, err = authUc.IsTokenBlacklisted(ctx, secondClaims.ID)
	require.NoError(t, err)
	require.True(t, revoked)
}
