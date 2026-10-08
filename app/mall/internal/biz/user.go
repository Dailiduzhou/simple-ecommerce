package biz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"time"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
)

var ErrShippingAddressNotFound = mallv1.ErrorShippingAddressNotFound("shipping address not found")

type UserRepo interface {
	CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*UserProfile, error)
	GetAuthUser(ctx context.Context, id int64) (*User, error)
	GetUserByPhoneHash(ctx context.Context, phoneHash string) (*User, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*UserProfile, error)
	UpdateUserPassword(ctx context.Context, id, expectedVersion int64, passwordHash string) error
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
	ListShippingAddressesByUser(ctx context.Context, userID int64, limit, offset int32) ([]ShippingAddress, error)
	UpdateShippingAddress(ctx context.Context, id int64, userID int64, receiverName string, receiverPhoneHash string, receiverPhoneEncrypt string, province string, city string, district string, detailAddress string, addressTag string) (*ShippingAddress, error)
	SetDefaultShippingAddress(ctx context.Context, id int64, userID int64) error
	DeleteShippingAddress(ctx context.Context, id int64, userID int64) error
}

type ShippingAddressUsecase interface {
	CreateShippingAddress(ctx context.Context, userID int64, receiverName, receiverPhone, province, city, district, detailAddress, addressTag string, isDefault bool) (*ShippingAddress, error)
	GetShippingAddress(ctx context.Context, id int64, userID int64) (*ShippingAddress, error)
	ListShippingAddressesByUser(ctx context.Context, userID int64, page, pageSize int32) ([]ShippingAddress, error)
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
	ID           int64
	Nickname     string
	RealName     string
	PhoneHash    string
	PhoneEncrypt string
	PasswordHash string
	AuthVersion  int64
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Public profile reads have no credential fields, including on cache misses.
type UserProfile struct {
	ID                       int64
	Nickname, RealName, Role string
	CreatedAt, UpdatedAt     time.Time
}

func (u *User) Profile() *UserProfile {
	if u == nil {
		return nil
	}
	return &UserProfile{ID: u.ID, Nickname: u.Nickname, RealName: u.RealName, Role: u.Role, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

type UserUsecase interface {
	Register(ctx context.Context, phone string, password string) (*User, error)
	Login(ctx context.Context, phone string, password string) (*User, error)
	GetUser(ctx context.Context, id int64) (*UserProfile, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*UserProfile, error)
	ChangePassword(ctx context.Context, id int64, oldPassword, newPassword string) error
	DeleteUser(ctx context.Context, id int64) error
}

type userUsecase struct {
	userRepo             UserRepo
	authRepo             AuthRepo
	phoneSecret          string
	loginAccountBurst    int64
	loginAccountInterval time.Duration
	log                  *log.Helper
}

func NewUserUsecase(userRepo UserRepo, authRepo AuthRepo, ac *conf.Auth, logger log.Logger) UserUsecase {
	burst := int64(5)
	if v := ac.GetLoginAccountBurst(); v > 0 {
		burst = int64(v)
	}
	interval := 30 * time.Second
	if d := ac.GetLoginAccountInterval().AsDuration(); d > 0 {
		interval = d
	}
	return &userUsecase{
		userRepo: userRepo, authRepo: authRepo, phoneSecret: ac.GetPhoneSecret(),
		loginAccountBurst: burst, loginAccountInterval: interval, log: log.NewHelper(logger),
	}
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

	// Reserve before reading credentials: a burst of concurrent requests from
	// different IPs must not all bypass a failure counter still at zero.
	wait, err := uc.authRepo.ReserveLoginAttempt(ctx, phoneHash, uc.loginAccountBurst, uc.loginAccountInterval)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("reserve account login quota failed: %v", err)
		return nil, errors.ServiceUnavailable("AUTH_STORE_UNAVAILABLE", "login quota unavailable; retry later")
	}
	if wait > 0 {
		seconds := (wait + time.Second - 1) / time.Second
		return nil, userv1.ErrorUserLoginLocked("account login rate exceeded; retry after the indicated delay").WithMetadata(map[string]string{
			"retry_after_seconds": strconv.FormatInt(int64(seconds), 10),
		})
	}

	u, err := uc.userRepo.GetUserByPhoneHash(ctx, phoneHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("get user by phone hash failed: %v", err)
		return nil, fmt.Errorf("get user by phone hash: %w", err)
	}
	if u == nil {
		// Burn the same bcrypt work as the wrong-password path. Unknown
		// phones consume exactly the same quota and return the same error.
		_ = pwdhash.ComparePassword(dummyPasswordHash, password)
		return nil, userv1.ErrorInvalidCredentials("invalid credentials")
	}

	if err := pwdhash.ComparePassword(u.PasswordHash, password); err != nil {
		return nil, userv1.ErrorInvalidCredentials("invalid credentials")
	}

	// Success consumes its reservation too; it cannot reset another concurrent
	// request's budget. Existing sessions and refresh are unaffected.
	return u, nil
}

func (uc *userUsecase) GetUser(ctx context.Context, id int64) (*UserProfile, error) {
	return uc.userRepo.GetUserByID(ctx, id)
}

func (uc *userUsecase) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*UserProfile, error) {
	return uc.userRepo.UpdateUser(ctx, id, nickname, realName)
}

// ChangePassword rotates the caller's password. It verifies the current
// password against the authoritative row (never a cached profile), stores the
// new hash with a version CAS. Only one request verified against a given
// credential version can commit; every token of that version is then rejected.
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
	if err := uc.userRepo.UpdateUserPassword(ctx, id, u.AuthVersion, passwordHash); err != nil {
		return err
	}
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

func (uc *shippingAddressUsecase) ListShippingAddressesByUser(ctx context.Context, userID int64, page, pageSize int32) ([]ShippingAddress, error) {
	p, err := NewPage(page, pageSize)
	if err != nil {
		return nil, err
	}
	sas, err := uc.addressRepo.ListShippingAddressesByUser(ctx, userID, p.Limit, p.Offset)
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
