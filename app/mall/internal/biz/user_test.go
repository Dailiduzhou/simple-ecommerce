package biz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUserRepo struct {
	createUser         func(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error)
	getUserByID        func(ctx context.Context, id int64) (*User, error)
	getUserByPhoneHash func(ctx context.Context, phoneHash string) (*User, error)
	updateUser         func(ctx context.Context, id int64, nickname, realName string) (*User, error)
	updateUserPassword func(ctx context.Context, id int64, passwordHash string) error
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

func (r *fakeUserRepo) UpdateUserPassword(ctx context.Context, id int64, passwordHash string) error {
	if r.updateUserPassword == nil {
		return nil
	}
	return r.updateUserPassword(ctx, id, passwordHash)
}

func (r *fakeUserRepo) DeleteUser(ctx context.Context, id int64) error {
	return r.deleteUser(ctx, id)
}

func testUserAuth() *conf.Auth {
	return &conf.Auth{PhoneSecret: "phone-secret"}
}

// fakeAuthRepo records lockout interactions so tests can assert the failure
// and clear behaviour around Login.
type fakeAuthRepo struct {
	loginFailures      func(ctx context.Context, phoneHash string) (int64, error)
	recordLoginFailure func(ctx context.Context, phoneHash string, window time.Duration) error
	clearLoginFailures func(ctx context.Context, phoneHash string) error
	mu                 sync.Mutex
	recorded           []string
	cleared            []string
}

func (r *fakeAuthRepo) ConsumeRefresh(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func (r *fakeAuthRepo) SetBlacklist(context.Context, string, time.Duration) error { return nil }

func (r *fakeAuthRepo) IsBlacklisted(context.Context, string) (bool, error) { return false, nil }

func (r *fakeAuthRepo) LoginFailures(ctx context.Context, phoneHash string) (int64, error) {
	if r.loginFailures != nil {
		return r.loginFailures(ctx, phoneHash)
	}
	return 0, nil
}

func (r *fakeAuthRepo) RecordLoginFailure(ctx context.Context, phoneHash string, window time.Duration) error {
	if r.recordLoginFailure != nil {
		return r.recordLoginFailure(ctx, phoneHash, window)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recorded = append(r.recorded, phoneHash)
	return nil
}

func (r *fakeAuthRepo) ClearLoginFailures(ctx context.Context, phoneHash string) error {
	if r.clearLoginFailures != nil {
		return r.clearLoginFailures(ctx, phoneHash)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleared = append(r.cleared, phoneHash)
	return nil
}

func (r *fakeAuthRepo) recordedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.recorded)
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
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

	u, err := uc.Register(context.Background(), phone, password)
	require.NoError(t, err)
	assert.Equal(t, int64(1), u.ID)
	assert.Equal(t, phoneHash, u.PhoneHash)
}

func TestUserUsecase_Register_InvalidPhone(t *testing.T) {
	uc := NewUserUsecase(&fakeUserRepo{}, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

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
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

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
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

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
	auth := &fakeAuthRepo{}
	uc := NewUserUsecase(repo, auth, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "secret-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	// An unregistered phone must look exactly like a wrong password, so it
	// cannot be used to enumerate accounts.
	assert.True(t, userv1.IsInvalidCredentials(err))
	assert.Equal(t, 1, auth.recordedCount(), "unknown phone still counts toward lockout")
}

func TestUserUsecase_Login_InvalidPassword(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)

	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return &User{ID: 2, PasswordHash: passwordHash}, nil
		},
	}
	auth := &fakeAuthRepo{}
	uc := NewUserUsecase(repo, auth, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "wrong-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	assert.True(t, userv1.IsInvalidCredentials(err))
	assert.Equal(t, 1, auth.recordedCount(), "wrong password counts toward lockout")
}

// The two failure paths must be indistinguishable: same error reason and the
// same rendered message, or account enumeration would resurface via either.
func TestUserUsecase_Login_FailuresAreIndistinguishable(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	registered := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return &User{ID: 2, PasswordHash: passwordHash}, nil
		},
	}
	unknown := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return nil, nil
		},
	}
	ucRegistered := NewUserUsecase(registered, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)
	ucUnknown := NewUserUsecase(unknown, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

	_, wrongPassword := ucRegistered.Login(context.Background(), "13800138000", "wrong-pass")
	_, unknownPhone := ucUnknown.Login(context.Background(), "13800138000", "secret-pass")
	require.Error(t, wrongPassword)
	require.Error(t, unknownPhone)
	assert.Equal(t, wrongPassword.Error(), unknownPhone.Error())
}

func TestUserUsecase_Login_LockoutBlocksBeforeCredentialWork(t *testing.T) {
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			t.Fatal("locked login must not touch the user repository")
			return nil, nil
		},
	}
	auth := &fakeAuthRepo{
		loginFailures: func(ctx context.Context, phoneHash string) (int64, error) {
			return 5, nil
		},
	}
	uc := NewUserUsecase(repo, auth, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "secret-pass")
	require.Error(t, err)
	assert.Nil(t, u)
	assert.True(t, userv1.IsUserLoginLocked(err))
}

func TestUserUsecase_Login_LockoutCheckFailsOpen(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return &User{ID: 2, PasswordHash: passwordHash}, nil
		},
	}
	auth := &fakeAuthRepo{
		loginFailures: func(ctx context.Context, phoneHash string) (int64, error) {
			return 0, errors.New("redis unavailable")
		},
	}
	uc := NewUserUsecase(repo, auth, testUserAuth(), log.DefaultLogger)

	// The transport limiter already fails closed for auth operations, so the
	// lockout counter itself must fail open instead of blocking all logins.
	u, err := uc.Login(context.Background(), "13800138000", "secret-pass")
	require.NoError(t, err)
	assert.Equal(t, int64(2), u.ID)
}

func TestUserUsecase_Login_SuccessClearsFailures(t *testing.T) {
	passwordHash, err := pwdhash.HashPassword("secret-pass")
	require.NoError(t, err)
	repo := &fakeUserRepo{
		getUserByPhoneHash: func(ctx context.Context, phoneHash string) (*User, error) {
			return &User{ID: 2, PasswordHash: passwordHash}, nil
		},
	}
	auth := &fakeAuthRepo{}
	uc := NewUserUsecase(repo, auth, testUserAuth(), log.DefaultLogger)

	u, err := uc.Login(context.Background(), "13800138000", "secret-pass")
	require.NoError(t, err)
	assert.Equal(t, int64(2), u.ID)
	auth.mu.Lock()
	require.Len(t, auth.cleared, 1)
	auth.mu.Unlock()
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
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)

	u, err := uc.GetUser(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, "old", u.Nickname)

	u, err = uc.UpdateUser(context.Background(), 3, "new", "real")
	require.NoError(t, err)
	assert.Equal(t, "new", u.Nickname)

	err = uc.DeleteUser(context.Background(), 3)
	assert.ErrorIs(t, err, deleteErr)
}

func (r *fakeUserRepo) GetAuthUser(ctx context.Context, id int64) (*User, error) {
	return r.GetUserByID(ctx, id)
}

func TestUserUsecase_ChangePasswordRotatesHashAndClearsFailures(t *testing.T) {
	oldHash, err := pwdhash.HashPassword("old-secret")
	require.NoError(t, err)
	var gotID int64
	var gotHash string
	repo := &fakeUserRepo{
		getUserByID: func(_ context.Context, id int64) (*User, error) {
			return &User{ID: id, PhoneHash: "phone-hash", PasswordHash: oldHash}, nil
		},
		updateUserPassword: func(_ context.Context, id int64, passwordHash string) error {
			gotID, gotHash = id, passwordHash
			return nil
		},
	}
	authRepo := &fakeAuthRepo{}
	uc := NewUserUsecase(repo, authRepo, testUserAuth(), log.DefaultLogger)

	require.NoError(t, uc.ChangePassword(context.Background(), 7, "old-secret", "new-secret"))
	require.Equal(t, int64(7), gotID)
	require.NoError(t, pwdhash.ComparePassword(gotHash, "new-secret"))
	require.Equal(t, []string{"phone-hash"}, authRepo.cleared, "the rotation must clear the login-failure window")
}

func TestUserUsecase_ChangePasswordRejectsBadInputAndCredentials(t *testing.T) {
	oldHash, err := pwdhash.HashPassword("old-secret")
	require.NoError(t, err)
	updated := false
	repo := &fakeUserRepo{
		getUserByID: func(_ context.Context, id int64) (*User, error) {
			if id == 0 {
				return nil, nil
			}
			return &User{ID: id, PasswordHash: oldHash}, nil
		},
		updateUserPassword: func(context.Context, int64, string) error {
			updated = true
			return errors.New("must not be called")
		},
	}
	uc := NewUserUsecase(repo, &fakeAuthRepo{}, testUserAuth(), log.DefaultLogger)
	ctx := context.Background()

	require.ErrorIs(t, uc.ChangePassword(ctx, 0, "old-secret", "new-secret"), userv1.ErrorUnauthorized(""))
	require.ErrorIs(t, uc.ChangePassword(ctx, 1, "old-secret", "short"), userv1.ErrorInvalidPassword(""))
	require.ErrorIs(t, uc.ChangePassword(ctx, 1, "old-secret", "old-secret"), userv1.ErrorInvalidPassword(""))
	require.ErrorIs(t, uc.ChangePassword(ctx, 1, "wrong-secret", "new-secret"), userv1.ErrorInvalidCredentials(""))
	require.False(t, updated, "no invalid request may reach the database")
}

func TestAuthUsecase_ValidateAccountRevokesTokensIssuedBeforePasswordChange(t *testing.T) {
	changedAt := time.Now().Truncate(time.Second)
	repo := &fakeUserRepo{
		getUserByID: func(context.Context, int64) (*User, error) {
			return &User{ID: 1, Role: "user", PasswordChangedAt: changedAt}, nil
		},
	}
	uc := NewAuthUsecase(repo, &fakeAuthRepo{}, testUserAuth())

	stale := &EcommerceClaims{UserID: 1, RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(changedAt.Add(-time.Minute))}}
	require.Error(t, uc.ValidateAccount(context.Background(), stale))

	// A token issued in the same second as the rotation stays valid: JWTs only
	// carry second-resolution iat values.
	fresh := &EcommerceClaims{UserID: 1, RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(changedAt)}}
	require.NoError(t, uc.ValidateAccount(context.Background(), fresh))
	require.Equal(t, "user", fresh.Role)
}
