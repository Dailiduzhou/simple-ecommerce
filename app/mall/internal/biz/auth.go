package biz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/golang-jwt/jwt/v5"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
)

type AuthRepo interface {
	CreateSession(ctx context.Context, sessionID string, userID int64, expiration time.Duration) error
	SessionActive(ctx context.Context, sessionID string, userID int64) (bool, error)
	ConsumeSessionRefresh(ctx context.Context, sessionID string, userID int64, tokenID string, expiration time.Duration) (bool, error)
	RevokeSession(ctx context.Context, sessionID string, userID int64) (bool, error)
	LoginFailures(ctx context.Context, phoneHash string) (int64, error)
	RecordLoginFailure(ctx context.Context, phoneHash string, window time.Duration) error
	ClearLoginFailures(ctx context.Context, phoneHash string) error
}

type EcommerceClaims struct {
	UserID      int64  `json:"user_id"`
	Role        string `json:"role"`
	AuthVersion int64  `json:"auth_version"`
	SessionID   string `json:"sid"`
	TokenKind   string `json:"token_kind"`
	jwt.RegisteredClaims
}

type TokenPair struct{ AccessToken, RefreshToken string }

type AuthUsecase interface {
	ValidateAccount(ctx context.Context, claims *EcommerceClaims) error
	ValidateSession(ctx context.Context, claims *EcommerceClaims) error
	StartSession(ctx context.Context, verifiedUser *User) (*TokenPair, error)
	RefreshSession(ctx context.Context, refreshToken string) (*TokenPair, error)
	ParseAccessToken(tokenStr string) (*EcommerceClaims, error)
	ParseRefreshToken(tokenStr string) (*EcommerceClaims, error)
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
	return &authUsecase{userRepo: userRepo, authRepo: authRepo,
		accessSecret: ac.AccessTokenSecret, accessTimeout: ac.AccessTokenTimeout.AsDuration(),
		refreshSecret: ac.RefreshTokenSecret, refreshTimeout: ac.RefreshTokenTimeout.AsDuration()}
}

// Bind signatures to the credential version which was actually verified.
// Role and account existence are authoritative on every protected request.
func (uc *authUsecase) ValidateAccount(ctx context.Context, claims *EcommerceClaims) error {
	if claims == nil || claims.UserID <= 0 || claims.AuthVersion <= 0 {
		return userv1.ErrorUnauthorized("invalid account claims")
	}
	u, err := uc.userRepo.GetAuthUser(ctx, claims.UserID)
	if err != nil {
		return err
	}
	if u == nil {
		return userv1.ErrorUnauthorized("account no longer exists")
	}
	if claims.AuthVersion != u.AuthVersion {
		return userv1.ErrorUnauthorized("credentials changed; sign in again")
	}
	claims.Role = u.Role
	return nil
}

func (uc *authUsecase) ValidateSession(ctx context.Context, claims *EcommerceClaims) error {
	if claims == nil || claims.SessionID == "" || claims.ID == "" || claims.UserID <= 0 || claims.TokenKind != "access" {
		return userv1.ErrorUnauthorized("invalid session claims")
	}
	active, err := uc.authRepo.SessionActive(ctx, claims.SessionID, claims.UserID)
	if err != nil {
		return errors.ServiceUnavailable("AUTH_STORE_UNAVAILABLE", "session store unavailable")
	}
	if !active {
		return userv1.ErrorTokenExpired("session has been revoked or expired")
	}
	return nil
}

func (uc *authUsecase) StartSession(ctx context.Context, verifiedUser *User) (*TokenPair, error) {
	if verifiedUser == nil || verifiedUser.ID <= 0 || verifiedUser.AuthVersion <= 0 {
		return nil, userv1.ErrorUnauthorized("missing verified credential version")
	}
	now := time.Now()
	// A fixed session deadline also bounds every descendant token after rotation.
	lifetime := max(uc.refreshTimeout, uc.accessTimeout)
	if lifetime <= 0 || uc.accessTimeout <= 0 || uc.refreshTimeout <= 0 {
		return nil, fmt.Errorf("invalid token timeouts")
	}
	sessionID := generateTokenID()
	deadline := now.Add(lifetime).Truncate(time.Second)
	pair, err := uc.signPair(verifiedUser.ID, verifiedUser.Role, verifiedUser.AuthVersion, sessionID, now, deadline)
	if err != nil {
		return nil, err
	}
	if err := uc.authRepo.CreateSession(ctx, sessionID, verifiedUser.ID, time.Until(deadline)); err != nil {
		return nil, errors.ServiceUnavailable("AUTH_STORE_UNAVAILABLE", "session creation failed")
	}
	return pair, nil
}

func (uc *authUsecase) RefreshSession(ctx context.Context, refreshToken string) (*TokenPair, error) {
	claims, err := uc.ParseRefreshToken(refreshToken)
	if err != nil {
		return nil, userv1.ErrorTokenExpired("refresh token invalid or expired")
	}
	if err := uc.ValidateAccount(ctx, claims); err != nil {
		return nil, err
	}
	ttl := time.Until(claims.ExpiresAt.Time)
	// The same atomic operation tests session ownership/revocation AND burns the
	// refresh JTI. Logout racing after this step still revokes the new pair's SID.
	ok, err := uc.authRepo.ConsumeSessionRefresh(ctx, claims.SessionID, claims.UserID, claims.ID, ttl)
	if err != nil {
		return nil, errors.ServiceUnavailable("AUTH_STORE_UNAVAILABLE", "refresh store unavailable")
	}
	if !ok {
		return nil, userv1.ErrorTokenExpired("refresh already consumed or session revoked")
	}
	return uc.signPair(claims.UserID, claims.Role, claims.AuthVersion, claims.SessionID, time.Now(), claims.ExpiresAt.Time)
}

func (uc *authUsecase) signPair(userID int64, role string, version int64, sessionID string, now, deadline time.Time) (*TokenPair, error) {
	accessDeadline := now.Add(uc.accessTimeout)
	if deadline.Before(accessDeadline) {
		accessDeadline = deadline
	}
	sign := func(kind, secret string, expires time.Time) (string, error) {
		claims := EcommerceClaims{UserID: userID, Role: role, AuthVersion: version, SessionID: sessionID, TokenKind: kind,
			RegisteredClaims: jwt.RegisteredClaims{ID: generateTokenID(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expires)}}
		return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	}
	access, err := sign("access", uc.accessSecret, accessDeadline)
	if err != nil {
		return nil, err
	}
	refresh, err := sign("refresh", uc.refreshSecret, deadline)
	if err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: access, RefreshToken: refresh}, nil
}

func (uc *authUsecase) ParseAccessToken(tokenStr string) (*EcommerceClaims, error) {
	return uc.parseToken(tokenStr, uc.accessSecret, "access")
}

func (uc *authUsecase) ParseRefreshToken(tokenStr string) (*EcommerceClaims, error) {
	return uc.parseToken(tokenStr, uc.refreshSecret, "refresh")
}

// One revocation covers both halves and every refresh descendant. A valid but
// unrelated refresh token is rejected without modifying either user's session.
// Invalid/expired optional refresh tokens cannot weaken access-authorized logout.
func (uc *authUsecase) Logout(ctx context.Context, claims *EcommerceClaims, refreshToken string) error {
	if claims == nil || claims.SessionID == "" || claims.UserID <= 0 || claims.ID == "" || claims.TokenKind != "access" {
		return userv1.ErrorUnauthorized("invalid session claims")
	}
	if refreshToken != "" {
		if refresh, err := uc.ParseRefreshToken(refreshToken); err == nil && (refresh.UserID != claims.UserID || refresh.SessionID != claims.SessionID) {
			return userv1.ErrorUnauthorized("refresh token belongs to another session")
		}
	}
	ok, err := uc.authRepo.RevokeSession(ctx, claims.SessionID, claims.UserID)
	if err != nil {
		return errors.ServiceUnavailable("LOGOUT_UNAVAILABLE", "session revocation failed; retry logout")
	}
	if !ok {
		return userv1.ErrorUnauthorized("session owner mismatch")
	}
	return nil
}

func (uc *authUsecase) parseToken(tokenStr, secret, kind string) (*EcommerceClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &EcommerceClaims{}, func(t *jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*EcommerceClaims)
	if !ok || !token.Valid || claims.TokenKind != kind || claims.UserID <= 0 || claims.AuthVersion <= 0 || claims.SessionID == "" || claims.ID == "" {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

func generateTokenID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
