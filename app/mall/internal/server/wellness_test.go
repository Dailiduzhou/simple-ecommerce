package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"
	"github.com/go-kratos/kratos/v2/log"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type wellnessAuthStub struct {
	biz.AuthUsecase
	revoked    bool
	err        error
	accountErr error
}

func (u *wellnessAuthStub) IsTokenBlacklisted(context.Context, string) (bool, error) {
	return u.revoked, u.err
}

func (u *wellnessAuthStub) ValidateAccount(context.Context, *biz.EcommerceClaims) error {
	return u.accountErr
}

func wellnessToken(t *testing.T, secret string, userID int64, role string, expiry time.Time) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, &biz.EcommerceClaims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID: "wellness-test-token", ExpiresAt: jwt.NewNumericDate(expiry),
		},
	}).SignedString([]byte(secret))
	require.NoError(t, err)
	return token
}

func TestTodayWellnessHTTPAuthenticationAndJSON(t *testing.T) {
	secret := strings.Repeat("a", 32)
	valid := wellnessToken(t, secret, 1, "user", time.Now().Add(time.Hour))
	admin := wellnessToken(t, secret, 2, "admin", time.Now().Add(time.Hour))
	expired := wellnessToken(t, secret, 1, "user", time.Now().Add(-time.Hour))
	wrongKey := wellnessToken(t, strings.Repeat("b", 32), 1, "user", time.Now().Add(time.Hour))
	invalidUser := wellnessToken(t, secret, 0, "user", time.Now().Add(time.Hour))
	for _, tt := range []struct {
		name, token string
		auth        wellnessAuthStub
		status      int
	}{
		{name: "anonymous", status: http.StatusUnauthorized},
		{name: "malformed", token: "not-a-jwt", status: http.StatusUnauthorized},
		{name: "expired", token: expired, status: http.StatusUnauthorized},
		{name: "wrong_signature", token: wrongKey, status: http.StatusUnauthorized},
		{name: "revoked", token: valid, auth: wellnessAuthStub{revoked: true}, status: http.StatusUnauthorized},
		{name: "blacklist_unavailable", token: valid, auth: wellnessAuthStub{err: errors.New("redis unavailable")}, status: http.StatusUnauthorized},
		{name: "account_invalid", token: valid, auth: wellnessAuthStub{accountErr: errors.New("account unavailable")}, status: http.StatusUnauthorized},
		{name: "invalid_user", token: invalidUser, status: http.StatusUnauthorized},
		{name: "member", token: valid, status: http.StatusOK},
		{name: "admin", token: admin, status: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			uc := biz.NewWellnessUsecase()
			srv := NewHTTPServer(
				&conf.Server{Http: &conf.Server_HTTP{}}, &conf.Auth{AccessTokenSecret: secret}, &tt.auth,
				service.NewMallService(nil, nil, nil, uc, log.DefaultLogger),
				service.NewUserService(nil, nil, nil, nil, log.DefaultLogger), service.NewOrderService(nil),
				service.NewPaymentService(nil, nil, log.DefaultLogger),
				service.NewCommunityService(nil, nil), service.NewMediaService(nil), &communityLimiter{}, log.DefaultLogger,
			)
			// Unknown query fields must not override the server's date or user.
			request := httptest.NewRequest(http.MethodGet, "/v1/wellness/today?date=2000-01-01&user_id=999", nil)
			if tt.token != "" {
				request.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			before, err := uc.GetTodayWellness(context.Background())
			require.NoError(t, err)
			srv.ServeHTTP(response, request)
			require.Equal(t, tt.status, response.Code, response.Body.String())
			if tt.status != http.StatusOK {
				return
			}
			after, err := uc.GetTodayWellness(context.Background())
			require.NoError(t, err)
			require.Contains(t, response.Header().Get("Content-Type"), "application/json")
			var body map[string]string
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			// Match protojson's lowerCamelCase names and expose only the three agreed fields.
			require.Len(t, body, 3)
			want := before
			if body["date"] == after.Date {
				want = after
			}
			require.Equal(t, map[string]string{"date": want.Date, "solarTerm": want.SolarTerm, "advice": want.Advice}, body)
		})
	}
}

func TestTodayWellnessGRPCRequiresLogin(t *testing.T) {
	secret := strings.Repeat("a", 32)
	srv := NewGRPCServer(
		&conf.Server{Grpc: &conf.Server_GRPC{Addr: "127.0.0.1:0"}}, &conf.Auth{AccessTokenSecret: secret}, &wellnessAuthStub{},
		service.NewMallService(nil, nil, nil, biz.NewWellnessUsecase(), log.DefaultLogger),
		service.NewUserService(nil, nil, nil, nil, log.DefaultLogger), service.NewOrderService(nil),
		service.NewPaymentService(nil, nil, log.DefaultLogger),
		service.NewCommunityService(nil, nil), service.NewMediaService(nil), &communityLimiter{}, log.DefaultLogger,
	)
	endpoint, err := srv.Endpoint()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(ctx))
		require.NoError(t, <-done)
	})
	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewMallClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.GetTodayWellness(ctx, &pb.GetTodayWellnessRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	token := wellnessToken(t, secret, 1, "user", time.Now().Add(time.Hour))
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	card, err := client.GetTodayWellness(ctx, &pb.GetTodayWellnessRequest{})
	require.NoError(t, err)
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}$`, card.Date)
	require.NotEmpty(t, card.SolarTerm)
	require.NotEmpty(t, card.Advice)
}
