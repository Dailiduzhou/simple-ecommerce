package biz

import (
	"context"
	"errors"
	"testing"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUserRepo struct {
	createUser         func(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error)
	getUserByID        func(ctx context.Context, id int64) (*User, error)
	getUserByPhoneHash func(ctx context.Context, phoneHash string) (*User, error)
	updateUser         func(ctx context.Context, id int64, nickname, realName string) (*User, error)
	deleteUser         func(ctx context.Context, id int64) error
}

func (r *fakeUserRepo) CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error) {
	return r.createUser(ctx, nickname, phoneHash, phoneEncrypt, passwordHash)
}

func (r *fakeUserRepo) GetUserByID(ctx context.Context, id int64) (*User, error) {
	return r.getUserByID(ctx, id)
}

func (r *fakeUserRepo) GetUserByPhoneHash(ctx context.Context, phoneHash string) (*User, error) {
	return r.getUserByPhoneHash(ctx, phoneHash)
}

func (r *fakeUserRepo) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error) {
	return r.updateUser(ctx, id, nickname, realName)
}

func (r *fakeUserRepo) DeleteUser(ctx context.Context, id int64) error {
	return r.deleteUser(ctx, id)
}

func testUserAuth() *conf.Auth {
	return &conf.Auth{PhoneSecret: "phone-secret"}
}

func TestUserUsecase_Register(t *testing.T) {
	phone := "13800138000"
	password := "secret-pass"
	phoneHash := phonecrypto.HashPhone(phone, []byte(testUserAuth().PhoneSecret))

	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, gotHash string) (*User, error) {
			assert.Equal(t, phoneHash, gotHash)
			return nil, nil
		},
		createUser: func(ctx context.Context, nickname, gotHash, phoneEncrypt, passwordHash string) (*User, error) {
			assert.Equal(t, "u"+phoneHash[:8], nickname)
			assert.Equal(t, phoneHash, gotHash)
			decrypted, err := phonecrypto.DecryptPhone(phoneEncrypt, []byte(testUserAuth().PhoneSecret))
			require.NoError(t, err)
			assert.Equal(t, phone, decrypted)
			assert.NoError(t, pwdhash.ComparePassword(passwordHash, password))
			return &User{ID: 1, Nickname: nickname, PhoneHash: gotHash, PhoneEncrypt: phoneEncrypt, PasswordHash: passwordHash, Role: "user"}, nil
		},
	}
	uc := NewUserUsecase(repo, testUserAuth(), log.DefaultLogger)

	u, err := uc.Register(context.Background(), phone, password)
	require.NoError(t, err)
	assert.Equal(t, int64(1), u.ID)
	assert.Equal(t, phoneHash, u.PhoneHash)
}

func TestUserUsecase_Register_InvalidPhone(t *testing.T) {
	uc := NewUserUsecase(&fakeUserRepo{}, testUserAuth(), log.DefaultLogger)

	u, err := uc.Register(context.Background(), "12345", "secret-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	assert.True(t, userv1.IsInvalidPhone(err))
}

func TestUserUsecase_Register_UserAlreadyExists(t *testing.T) {
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return &User{ID: 1}, nil
		},
	}
	uc := NewUserUsecase(repo, testUserAuth(), log.DefaultLogger)

	u, err := uc.Register(context.Background(), "13800138000", "secret-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	assert.True(t, userv1.IsUserAlreadyExists(err))
}

func TestUserUsecase_Login(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)

	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			assert.Equal(t, phonecrypto.HashPhone("13800138000", []byte(testUserAuth().PhoneSecret)), phoneHash)
			return &User{ID: 2, PasswordHash: passwordHash, Role: "user"}, nil
		},
	}
	uc := NewUserUsecase(repo, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "secret-pass")
	require.NoError(t, err)
	assert.Equal(t, int64(2), u.ID)
}

func TestUserUsecase_Login_UserNotFound(t *testing.T) {
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return nil, nil
		},
	}
	uc := NewUserUsecase(repo, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "secret-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	assert.True(t, userv1.IsUserNotFound(err))
}

func TestUserUsecase_Login_InvalidPassword(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)

	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return &User{ID: 2, PasswordHash: passwordHash}, nil
		},
	}
	uc := NewUserUsecase(repo, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "wrong-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	assert.True(t, userv1.IsInvalidPassword(err))
}

func TestUserUsecase_GetUpdateDelete(t *testing.T) {
	deleteErr := errors.New("delete failed")
	repo := &fakeUserRepo{
		getUserByID: func(ctx context.Context, id int64) (*User, error) {
			assert.Equal(t, int64(3), id)
			return &User{ID: id, Nickname: "old"}, nil
		},
		updateUser: func(ctx context.Context, id int64, nickname, realName string) (*User, error) {
			assert.Equal(t, int64(3), id)
			assert.Equal(t, "new", nickname)
			assert.Equal(t, "real", realName)
			return &User{ID: id, Nickname: nickname, RealName: realName}, nil
		},
		deleteUser: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(3), id)
			return deleteErr
		},
	}
	uc := NewUserUsecase(repo, testUserAuth(), log.DefaultLogger)

	u, err := uc.GetUser(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, "old", u.Nickname)

	u, err = uc.UpdateUser(context.Background(), 3, "new", "real")
	require.NoError(t, err)
	assert.Equal(t, "new", u.Nickname)

	err = uc.DeleteUser(context.Background(), 3)
	assert.ErrorIs(t, err, deleteErr)
}
