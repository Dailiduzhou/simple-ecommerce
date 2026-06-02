package biz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"

	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang-jwt/jwt/v5"
)

type UserRepo interface {
	CreateUser(ctx context.Context, nickname, phoneHash, phoneEncrypt, passwordHash string) (*User, error)
	GetUserByID(ctx context.Context, id int64) (*User, error)
	GetUserByPhoneHash(ctx context.Context, phoneHash string) (*User, error)
	UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error)
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

type UserUsecase struct {
	userRepo    UserRepo
	phoneSecret string
	log         *log.Helper
}

func NewUserUsecase(userRepo UserRepo, ac *conf.Auth, logger log.Logger) *UserUsecase {
	return &UserUsecase{
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

type AuthUsecase struct {
	userRepo       UserRepo
	authRepo       AuthRepo
	accessSecret   string
	accessTimeout  time.Duration
	refreshSecret  string
	refreshTimeout time.Duration
}

func NewAuthUsecase(userRepo UserRepo, authRepo AuthRepo, ac *conf.Auth) *AuthUsecase {
	return &AuthUsecase{
		userRepo:       userRepo,
		authRepo:       authRepo,
		accessSecret:   ac.AccessTokenSecret,
		accessTimeout:  ac.AccessTokenTimeout.AsDuration(),
		refreshSecret:  ac.RefreshTokenSecret,
		refreshTimeout: ac.RefreshTokenTimeout.AsDuration(),
	}
}

func (uc *AuthUsecase) GenerateAccessToken(userID int64, role string) (string, error) {
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

func (uc *AuthUsecase) GenerateRefreshToken(userID int64, role string) (string, error) {
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

func (uc *AuthUsecase) ParseAccessToken(tokenStr string) (*EcommerceClaims, error) {
	return uc.parseToken(tokenStr, uc.accessSecret)
}

func (uc *AuthUsecase) ParseRefreshToken(tokenStr string) (*EcommerceClaims, error) {
	return uc.parseToken(tokenStr, uc.refreshSecret)
}

func (uc *AuthUsecase) BlacklistToken(ctx context.Context, tokenID string, expiresAt time.Time) error {
	expiration := time.Until(expiresAt)
	if expiration <= 0 {
		return nil
	}
	return uc.authRepo.SetBlacklist(ctx, tokenID, expiration)
}

func (uc *AuthUsecase) IsTokenBlacklisted(ctx context.Context, tokenID string) (bool, error) {
	return uc.authRepo.IsBlacklisted(ctx, tokenID)
}

func (uc *AuthUsecase) parseToken(tokenStr, secret string) (*EcommerceClaims, error) {
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

func (uc *UserUsecase) Register(ctx context.Context, phone string, password string) (*User, error) {
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

	nickname := "u" + phoneHash[:8]
	u, err := uc.userRepo.CreateUser(ctx, nickname, phoneHash, phoneEncrypt, passwordHash)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("create user failed: %v", err)
		return nil, fmt.Errorf("create user: %w", err)
	}

	return u, nil
}

func (uc *UserUsecase) Login(ctx context.Context, phone string, password string) (*User, error) {
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

func (uc *UserUsecase) GetUser(ctx context.Context, id int64) (*User, error) {
	return uc.userRepo.GetUserByID(ctx, id)
}

func (uc *UserUsecase) UpdateUser(ctx context.Context, id int64, nickname, realName string) (*User, error) {
	return uc.userRepo.UpdateUser(ctx, id, nickname, realName)
}

func IsValidCNMobile(phone string) bool {
	// 匹配规则：1开头，第二位3-9，后面9位数字
	re := regexp.MustCompile(`^1[3-9]\d{9}$`)
	return re.MatchString(phone)
}
