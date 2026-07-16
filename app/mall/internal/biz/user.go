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

	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang-jwt/jwt/v5"
)

var ErrShippingAddressNotFound = mallv1.ErrorShippingAddressNotFound("shipping address not found")

type UserRepo interface {
	CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)
	GetUserByPhoneHash(ctx context.Context, phoneHash string) (*User, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error)
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
	ID           int64
	Nickname     string
	RealName     string
	PhoneHash    string
	PhoneEncrypt string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UserUsecase interface {
	Register(ctx context.Context, phone string, password string) (*User, error)
	Login(ctx context.Context, phone string, password string) (*User, error)
	GetUser(ctx context.Context, id int64) (*User, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error)
	DeleteUser(ctx context.Context, id int64) error
}

type userUsecase struct {
	userRepo    UserRepo
	phoneSecret string
	log         *log.Helper
}

func NewUserUsecase(userRepo UserRepo, ac *conf.Auth, logger log.Logger) UserUsecase {
	return &userUsecase{
		userRepo:    userRepo,
		phoneSecret: ac.PhoneSecret,
		log:         log.NewHelper(logger),
	}
}

type AuthRepo interface {
	SetBlacklist(ctx context.Context, tokenID string, expiration time.Duration) error
	IsBlacklisted(ctx context.Context, tokenID string) (bool, error)
}

type EcommerceClaims struct {
	UserID int64  `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

type AuthUsecase interface {
	GenerateAccessToken(userID int64, role string) (string, error)
	GenerateRefreshToken(userID int64, role string) (string, error)
	ParseAccessToken(tokenStr string) (*EcommerceClaims, error)
	ParseRefreshToken(tokenStr string) (*EcommerceClaims, error)
	BlacklistToken(ctx context.Context, tokenID string, expiresAt time.Time) error
	IsTokenBlacklisted(ctx context.Context, tokenID string) (bool, error)
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

func (uc *userUsecase) Login(ctx context.Context, phone string, password string) (*User, error) {
	if !IsValidCNMobile(phone) {
		return nil, userv1.ErrorInvalidPhone("invalid phone number: %s", phone)
	}

	secret := []byte(uc.phoneSecret)
	phoneHash := phonecrypto.HashPhone(phone, secret)

	u, err := uc.userRepo.GetUserByPhoneHash(ctx, phoneHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("get user by phone hash failed: %v", err)
		return nil, fmt.Errorf("get user by phone hash: %w", err)
	}
	if u == nil {
		return nil, userv1.ErrorUserNotFound("phone not registered")
	}

	if err := pwdhash.ComparePassword(u.PasswordHash, password); err != nil {
		return nil, userv1.ErrorInvalidPassword("wrong password")
	}

	return u, nil
}

func (uc *userUsecase) GetUser(ctx context.Context, id int64) (*User, error) {
	return uc.userRepo.GetUserByID(ctx, id)
}

func (uc *userUsecase) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error) {
	return uc.userRepo.UpdateUser(ctx, id, nickname, realName)
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
