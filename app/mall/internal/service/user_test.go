package service

import (
	"context"
	"errors"
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

func (r *fakeUserRepo) DeleteUser(ctx context.Context, id int64) error {
	return r.deleteUser(ctx, id)
}

type fakeAuthRepo struct{}

func (r *fakeAuthRepo) SetBlacklist(ctx context.Context, tokenID string, expiration time.Duration) error {
	return nil
}

func (r *fakeAuthRepo) IsBlacklisted(ctx context.Context, tokenID string) (bool, error) {
	return false, nil
}

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
	userUc := biz.NewUserUsecase(userRepo, ac, log.DefaultLogger)
	return NewUserService(authUc, userUc, nil, ac, log.DefaultLogger)
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

	got, err := s.GetUser(context.Background(), &pb.GetUserRequest{Id: 404})
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

	got, err := s.UpdateUser(context.Background(), &pb.UpdateUserRequest{
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

func TestUserService_DeleteUser(t *testing.T) {
	repo := &fakeUserRepo{
		deleteUser: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(5), id)
			return nil
		},
	}
	s := newTestUserService(repo)

	got, err := s.DeleteUser(context.Background(), &pb.DeleteUserRequest{Id: 5})
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

	got, err := s.DeleteUser(context.Background(), &pb.DeleteUserRequest{Id: 5})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Equal(t, int32(500), kratoserrors.FromError(err).Code)
}
