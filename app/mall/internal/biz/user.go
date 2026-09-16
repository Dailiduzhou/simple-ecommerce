package biz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang-jwt/jwt/v5"
)

var ErrShippingAddressNotFound = mallv1.ErrorShippingAddressNotFound("shipping address not found")

type UserRepo interface {
	CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)
	GetAuthUser(ctx context.Context, id int64) (*User, error)
	GetUserByPhoneHash(ctx context.Context, phoneHash string) (*User, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error)
	UpdateUserPassword(ctx context.Context, id int64, passwordHash string) error
	DeleteUser(ctx context.Context, id int64) error
}

type ShippingAddress struct {
	ID                   int64
	UserID               int64
	ReceiverName         string
	ReceiverPhone        string
	ReceiverPhoneHash    string
	ReceiverPhoneEncrypt string
	Province             string
	City                 string
	District             string
	DetailAddress        string
	AddressTag           string
	IsDefault            bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ShippingAddressRepo interface {
	CreateShippingAddress(ctx context.Context, userID int64, receiverName string, receiverPhoneHash string, receiverPhoneEncrypt string, province string, city string, district string, detailAddress string, addressTag string, isDefault bool) (*ShippingAddress, error)
	GetShippingAddress(ctx context.Context, id int64, userID int64) (*ShippingAddress, error)
	ListShippingAddressesByUser(ctx context.Context, userID int64) ([]ShippingAddress, error)
	UpdateShippingAddress(ctx context.Context, id int64, userID int64, receiverName string, receiverPhoneHash string, receiverPhoneEncrypt string, province string, city string, district string, detailAddress string, addressTag string) (*ShippingAddress, error)
	SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error
	DeleteShippingAddress(ctx context.Context, id int64, userID int64) error
}

type ShippingAddressUsecase interface {
	CreateShippingAddress(ctx context.Context, userID int64, receiverName, receiverPhone, province, city, district, detailAddress, addressTag string, isDefault bool) (*ShippingAddress, error)
	GetShippingAddress(ctx context.Context, id int64, userID int64) (*ShippingAddress, error)
	ListShippingAddressesByUser(ctx context.Context, userID int64) ([]ShippingAddress, error)
	UpdateShippingAddress(ctx context.Context, id int64, userID int64, receiverName, receiverPhone, province, city, district, detailAddress, addressTag string) (*ShippingAddress, error)
	SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error
	DeleteShippingAddress(ctx context.Context, id int64, userID int64) error
}

type shippingAddressUsecase struct {
	addressRepo ShippingAddressRepo
	phoneSecret string
	log         *log.Helper
}

func NewShippingAddressUsecase(addressRepo ShippingAddressRepo, ac *conf.Auth, logger log.Logger) ShippingAddressUsecase {
	return &shippingAddressUsecase{
		addressRepo: addressRepo,
		phoneSecret: ac.PhoneSecret,
		log:         log.NewHelper(logger),
	}
}

type User struct {
	ID                int64
	Nickname          string
	RealName          string
	PhoneHash         string
	PhoneEncrypt      string
	PasswordHash      string
	PasswordChangedAt time.Time
	Role              string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type UserUsecase interface {
	Register(ctx context.Context, phone string, password string) (*User, error)
	Login(ctx context.Context, phone string, password string) (*User, error)
	GetUser(ctx context.Context, id int64) (*User, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error)
	ChangePassword(ctx context.Context, id int64, oldPassword, newPassword string) error
	DeleteUser(ctx context.Context, id int64) error
}

type userUsecase struct {
	userRepo    UserRepo
	authRepo    AuthRepo
	phoneSecret string
	// loginMaxAttempts is the recent-failure count (per phone hash) at which
	// Login is rejected outright; loginLockoutDuration arms that window.
	loginMaxAttempts     int64
	loginLockoutDuration time.Duration
	log                  *log.Helper
}

const (
	defaultLoginMaxAttempts     = 5
	defaultLoginLockoutDuration = 15 * time.Minute
)

func NewUserUsecase(userRepo UserRepo, authRepo AuthRepo, ac *conf.Auth, logger log.Logger) UserUsecase {
	maxAttempts := int64(defaultLoginMaxAttempts)
	if v := ac.GetLoginMaxAttempts(); v > 0 {
		maxAttempts = int64(v)
	}
	lockout := defaultLoginLockoutDuration
	if ac.GetLoginLockoutDuration() != nil {
		if d := ac.GetLoginLockoutDuration().AsDuration(); d > 0 {
			lockout = d
		}
	}
	return &userUsecase{
		userRepo:             userRepo,
		authRepo:             authRepo,
		phoneSecret:          ac.PhoneSecret,
		loginMaxAttempts:     maxAttempts,
		loginLockoutDuration: lockout,
		log:                  log.NewHelper(logger),
	}
}

type AuthRepo interface {
	ConsumeRefresh(ctx context.Context, tokenID string, expiration time.Duration) (bool, error)
	SetBlacklist(ctx context.Context, tokenID string, expiration time.Duration) error
	IsBlacklisted(ctx context.Context, tokenID string) (bool, error)
	LoginFailures(ctx context.Context, phoneHash string) (int64, error)
	RecordLoginFailure(ctx context.Context, phoneHash string, window time.Duration) error
	ClearLoginFailures(ctx context.Context, phoneHash string) error
}

type EcommerceClaims struct {
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

type AuthUsecase interface {
	ValidateAccount(ctx context.Context, claims *EcommerceClaims) error
	ConsumeRefresh(ctx context.Context, claims *EcommerceClaims) error
	GenerateAccessToken(userID int64, role string) (string, error)
	GenerateRefreshToken(userID int64, role string) (string, error)
	ParseAccessToken(tokenStr string) (*EcommerceClaims, error)
	ParseRefreshToken(tokenStr string) (*EcommerceClaims, error)
	BlacklistToken(ctx context.Context, tokenID string, expiresAt time.Time) error
	IsTokenBlacklisted(ctx context.Context, tokenID string) (bool, error)
	Logout(ctx context.Context, claims *EcommerceClaims, refreshToken string) error
}

type authUsecase struct {
	userRepo       UserRepo
	authRepo       AuthRepo
	accessSecret   string
	accessTimeout  time.Duration
	refreshSecret  string
	refreshTimeout time.Duration
}

func NewAuthUsecase(userRepo UserRepo, authRepo AuthRepo, ac *conf.Auth) AuthUsecase {
	return &authUsecase{
		userRepo:       userRepo,
		authRepo:       authRepo,
		accessSecret:   ac.AccessTokenSecret,
		accessTimeout:  ac.AccessTokenTimeout.AsDuration(),
		refreshSecret:  ac.RefreshTokenSecret,
		refreshTimeout: ac.RefreshTokenTimeout.AsDuration(),
	}
}

// ValidateAccount always uses the authoritative account, so deleting an account
// revokes every session, role changes apply to already-issued tokens, and a
// password change invalidates every token issued before it (the only global
// revocation path: no per-token sweep is needed).
func (uc *authUsecase) ValidateAccount(ctx context.Context, claims *EcommerceClaims) error {
	u, err := uc.userRepo.GetAuthUser(ctx, claims.UserID)
	if err != nil {
		return err
	}
	if u == nil {
		return userv1.ErrorUnauthorized("account no longer exists")
	}
	if claims.IssuedAt != nil && !u.PasswordChangedAt.IsZero() && claims.IssuedAt.Time.Before(u.PasswordChangedAt) {
		return userv1.ErrorUnauthorized("credentials changed; sign in again")
	}
	claims.Role = u.Role
	return nil
}

func (uc *authUsecase) ConsumeRefresh(ctx context.Context, claims *EcommerceClaims) error {
	if claims.ID == "" || claims.ExpiresAt == nil {
		return userv1.ErrorUnauthorized("invalid refresh claims")
	}
	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		return userv1.ErrorTokenExpired("refresh expired")
	}
	ok, err := uc.authRepo.ConsumeRefresh(ctx, claims.ID, ttl)
	if err != nil {
		return userv1.ErrorUnauthorized("refresh store unavailable")
	}
	if !ok {
		return userv1.ErrorTokenExpired("refresh already consumed")
	}
	return uc.ValidateAccount(ctx, claims)
}

func (uc *authUsecase) GenerateAccessToken(userID int64, role string) (string, error) {
	now := time.Now()
	tokenID := generateTokenID()
	claims := EcommerceClaims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(uc.accessTimeout)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(uc.accessSecret))
}

func (uc *authUsecase) GenerateRefreshToken(userID int64, role string) (string, error) {
	now := time.Now()
	tokenID := generateTokenID()
	claims := EcommerceClaims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(uc.refreshTimeout)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(uc.refreshSecret))
}

func (uc *authUsecase) ParseAccessToken(tokenStr string) (*EcommerceClaims, error) {
	return uc.parseToken(tokenStr, uc.accessSecret)
}

func (uc *authUsecase) ParseRefreshToken(tokenStr string) (*EcommerceClaims, error) {
	return uc.parseToken(tokenStr, uc.refreshSecret)
}

func (uc *authUsecase) BlacklistToken(ctx context.Context, tokenID string, expiresAt time.Time) error {
	expiration := time.Until(expiresAt)
	if expiration <= 0 {
		return nil
	}
	return uc.authRepo.SetBlacklist(ctx, tokenID, expiration)
}

func (uc *authUsecase) IsTokenBlacklisted(ctx context.Context, tokenID string) (bool, error) {
	return uc.authRepo.IsBlacklisted(ctx, tokenID)
}

// Logout revokes the caller's access token via the blacklist and, when the
// client supplies its refresh token, burns that token too. The refresh burn
// is best effort: a token that fails to parse or is already expired has
// nothing left to revoke, and a store failure only leaves an unused token
// that the client discarded and that expires on its own.
func (uc *authUsecase) Logout(ctx context.Context, claims *EcommerceClaims, refreshToken string) error {
	if claims == nil || claims.ID == "" || claims.ExpiresAt == nil {
		return userv1.ErrorUnauthorized("invalid token claims")
	}
	if err := uc.BlacklistToken(ctx, claims.ID, claims.ExpiresAt.Time); err != nil {
		// Fail closed: the token was NOT revoked, so the session stays
		// active; surface it as infrastructure trouble, not auth trouble.
		return errors.ServiceUnavailable("LOGOUT_UNAVAILABLE", "logout failed; the access token was not revoked")
	}
	if refreshToken != "" {
		if refreshClaims, err := uc.ParseRefreshToken(refreshToken); err == nil && refreshClaims.ID != "" && refreshClaims.ExpiresAt != nil {
			if ttl := time.Until(refreshClaims.ExpiresAt.Time); ttl > 0 {
				_, _ = uc.authRepo.ConsumeRefresh(ctx, refreshClaims.ID, ttl)
			}
		}
	}
	return nil
}

func (uc *authUsecase) parseToken(tokenStr, secret string) (*EcommerceClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &EcommerceClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*EcommerceClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

func generateTokenID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateDefaultNickname(seed string) string {
	if len(seed) >= 8 {
		return "u" + seed[:8]
	}
	b := make([]byte, 4)
	rand.Read(b)
	return "u" + hex.EncodeToString(b)
}

func (uc *userUsecase) Register(ctx context.Context, phone string, password string) (*User, error) {
	if len(password) < 8 || len(password) > 72 {
		return nil, userv1.ErrorInvalidPassword("password must be 8 to 72 bytes")
	}
	if !IsValidCNMobile(phone) {
		return nil, userv1.ErrorInvalidPhone("invalid phone number: %s", phone)
	}

	secret := []byte(uc.phoneSecret)
	phoneHash := phonecrypto.HashPhone(phone, secret)

	passwordHash, err := pwdhash.HashPassword(password)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("hash password failed: %v", err)
		return nil, fmt.Errorf("hash password: %w", err)
	}

	existing, err := uc.userRepo.GetUserByPhoneHash(ctx, phoneHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("check phone hash failed: %v", err)
		return nil, fmt.Errorf("check phone hash: %w", err)
	}
	if existing != nil {
		return nil, userv1.ErrorUserAlreadyExists("phone already registered")
	}

	phoneEncrypt, err := phonecrypto.EncryptPhone(phone, secret)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("encrypt phone failed: %v", err)
		return nil, fmt.Errorf("encrypt phone: %w", err)
	}

	nickname := generateDefaultNickname(phoneHash)
	u, err := uc.userRepo.CreateUser(ctx, nickname, phoneHash, phoneEncrypt, passwordHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("create user failed: %v", err)
		return nil, fmt.Errorf("create user: %w", err)
	}

	return u, nil
}

// dummyPasswordHash only exists so the unregistered-phone path performs the
// same bcrypt work as the wrong-password path; the compare result is discarded.
// It is hashed at the current pwdhash.Cost so both paths cost the same.
const dummyPasswordHash = "$2a$12$TvxjCNzidttpnrGKGqHDce40Y2TrV3./JnQmzFdrGqMJgkW9uB/gW"

func (uc *userUsecase) Login(ctx context.Context, phone string, password string) (*User, error) {
	if len(password) < 8 || len(password) > 72 {
		return nil, userv1.ErrorInvalidPassword("password must be 8 to 72 bytes")
	}
	if !IsValidCNMobile(phone) {
		return nil, userv1.ErrorInvalidPhone("invalid phone number: %s", phone)
	}

	secret := []byte(uc.phoneSecret)
	phoneHash := phonecrypto.HashPhone(phone, secret)

	// Account lockout: reject before any credential work once the recent
	// failure count for this phone hash reached the configured maximum. The
	// lookup is fail-open on store errors: the transport limiter already fails
	// closed for auth operations, so a Redis outage keeps login unavailable
	// and a second fail-closed layer would add no protection.
	failures, err := uc.authRepo.LoginFailures(ctx, phoneHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("get login failures failed: %v", err)
	} else if failures >= uc.loginMaxAttempts {
		return nil, userv1.ErrorUserLoginLocked("too many failed attempts, try again later")
	}

	u, err := uc.userRepo.GetUserByPhoneHash(ctx, phoneHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("get user by phone hash failed: %v", err)
		return nil, fmt.Errorf("get user by phone hash: %w", err)
	}
	if u == nil {
		// Burn the same bcrypt work as the wrong-password path and count the
		// failure identically, so an unregistered phone is indistinguishable
		// from a wrong password in both the response and the lockout state.
		_ = pwdhash.ComparePassword(dummyPasswordHash, password)
		uc.recordLoginFailure(ctx, phoneHash)
		return nil, userv1.ErrorInvalidCredentials("invalid credentials")
	}

	if err := pwdhash.ComparePassword(u.PasswordHash, password); err != nil {
		uc.recordLoginFailure(ctx, phoneHash)
		return nil, userv1.ErrorInvalidCredentials("invalid credentials")
	}

	// Best effort: a stale failure counter must never block a valid login.
	uc.clearLoginFailures(ctx, phoneHash)
	return u, nil
}

func (uc *userUsecase) recordLoginFailure(ctx context.Context, phoneHash string) {
	if err := uc.authRepo.RecordLoginFailure(ctx, phoneHash, uc.loginLockoutDuration); err != nil {
		uc.log.WithContext(ctx).Errorf("record login failure failed: %v", err)
	}
}

func (uc *userUsecase) clearLoginFailures(ctx context.Context, phoneHash string) {
	if err := uc.authRepo.ClearLoginFailures(ctx, phoneHash); err != nil {
		uc.log.WithContext(ctx).Errorf("clear login failures failed: %v", err)
	}
}

func (uc *userUsecase) GetUser(ctx context.Context, id int64) (*User, error) {
	return uc.userRepo.GetUserByID(ctx, id)
}

func (uc *userUsecase) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error) {
	return uc.userRepo.UpdateUser(ctx, id, nickname, realName)
}

// ChangePassword rotates the caller's password. It verifies the current
// password against the authoritative row (never a cached profile), stores the
// new hash and lets users.password_changed_at revoke every previously issued
// access and refresh token: ValidateAccount rejects tokens issued before the
// change, so a leaked password can be cut off globally without blacklisting
// tokens one by one.
func (uc *userUsecase) ChangePassword(ctx context.Context, id int64, oldPassword, newPassword string) error {
	if id <= 0 {
		return userv1.ErrorUnauthorized("invalid account")
	}
	if len(newPassword) < 8 || len(newPassword) > 72 {
		return userv1.ErrorInvalidPassword("password must be 8 to 72 bytes")
	}
	if newPassword == oldPassword {
		return userv1.ErrorInvalidPassword("new password must differ from the current one")
	}

	u, err := uc.userRepo.GetAuthUser(ctx, id)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("load account for password change failed: %v", err)
		return fmt.Errorf("load account: %w", err)
	}
	if u == nil {
		return userv1.ErrorUnauthorized("account no longer exists")
	}
	if err := pwdhash.ComparePassword(u.PasswordHash, oldPassword); err != nil {
		// Same generic credential error as Login: never reveal whether the
		// password was wrong or the account state changed.
		return userv1.ErrorInvalidCredentials("invalid credentials")
	}

	passwordHash, err := pwdhash.HashPassword(newPassword)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("hash new password failed: %v", err)
		return fmt.Errorf("hash password: %w", err)
	}
	if err := uc.userRepo.UpdateUserPassword(ctx, id, passwordHash); err != nil {
		return err
	}
	// Best effort: the old password's failure window must not survive the
	// rotation.
	uc.clearLoginFailures(ctx, u.PhoneHash)
	return nil
}

func (uc *userUsecase) DeleteUser(ctx context.Context, id int64) error {
	return uc.userRepo.DeleteUser(ctx, id)
}

func (uc *shippingAddressUsecase) CreateShippingAddress(ctx context.Context, userID int64, receiverName, receiverPhone, province, city, district, detailAddress, addressTag string, isDefault bool) (*ShippingAddress, error) {
	secret := []byte(uc.phoneSecret)
	phoneHash := phonecrypto.HashPhone(receiverPhone, secret)
	phoneEncrypt, err := phonecrypto.EncryptPhone(receiverPhone, secret)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("encrypt phone failed: %v", err)
		return nil, fmt.Errorf("encrypt phone: %w", err)
	}

	sa, err := uc.addressRepo.CreateShippingAddress(ctx, userID, receiverName, phoneHash, phoneEncrypt, province, city, district, detailAddress, addressTag, isDefault)
	if err != nil {
		return nil, err
	}
	sa.ReceiverPhone = receiverPhone
	return sa, nil
}

func (uc *shippingAddressUsecase) GetShippingAddress(ctx context.Context, id int64, userID int64) (*ShippingAddress, error) {
	sa, err := uc.addressRepo.GetShippingAddress(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if sa != nil {
		uc.decryptPhone(sa)
	}
	return sa, nil
}

func (uc *shippingAddressUsecase) ListShippingAddressesByUser(ctx context.Context, userID int64) ([]ShippingAddress, error) {
	sas, err := uc.addressRepo.ListShippingAddressesByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range sas {
		uc.decryptPhone(&sas[i])
	}
	return sas, nil
}

func (uc *shippingAddressUsecase) UpdateShippingAddress(ctx context.Context, id int64, userID int64, receiverName, receiverPhone, province, city, district, detailAddress, addressTag string) (*ShippingAddress, error) {
	secret := []byte(uc.phoneSecret)
	phoneHash := phonecrypto.HashPhone(receiverPhone, secret)
	phoneEncrypt, err := phonecrypto.EncryptPhone(receiverPhone, secret)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("encrypt phone failed: %v", err)
		return nil, fmt.Errorf("encrypt phone: %w", err)
	}

	sa, err := uc.addressRepo.UpdateShippingAddress(ctx, id, userID, receiverName, phoneHash, phoneEncrypt, province, city, district, detailAddress, addressTag)
	if err != nil {
		return nil, err
	}
	sa.ReceiverPhone = receiverPhone
	return sa, nil
}

func (uc *shippingAddressUsecase) SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error {
	return uc.addressRepo.SetDefaultShippingAddress(ctx, id, userID)
}

func (uc *shippingAddressUsecase) DeleteShippingAddress(ctx context.Context, id int64, userID int64) error {
	return uc.addressRepo.DeleteShippingAddress(ctx, id, userID)
}

func (uc *shippingAddressUsecase) decryptPhone(sa *ShippingAddress) {
	if sa == nil || sa.ReceiverPhoneEncrypt == "" {
		return
	}
	phone, err := phonecrypto.DecryptPhone(sa.ReceiverPhoneEncrypt, []byte(uc.phoneSecret))
	if err != nil {
		uc.log.Errorf("decrypt phone failed: %v", err)
		return
	}
	sa.ReceiverPhone = phone
}

func IsValidCNMobile(phone string) bool {
	re := regexp.MustCompile(`^1[3-9]\d{9}$`)
	return re.MatchString(phone)
}
