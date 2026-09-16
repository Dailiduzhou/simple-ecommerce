package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	userv1 "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/phonecrypto"
	"github.com/Dailiduzhou/simple-ecommerce/pkg/pwdhash"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

type transportAccountRepo struct {
	biz.UserRepo
	mu        sync.Mutex
	user      *biz.User
	phoneHash string // the only phone hash that resolves to the account
}

func (r *transportAccountRepo) GetAuthUser(context.Context, int64) (*biz.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.user == nil {
		return nil, nil
	}
	u := *r.user
	return &u, nil
}
func (r *transportAccountRepo) GetUserByPhoneHash(_ context.Context, phoneHash string) (*biz.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if phoneHash != r.phoneHash || r.user == nil {
		return nil, nil
	}
	u := *r.user
	return &u, nil
}
func (r *transportAccountRepo) DeleteUser(context.Context, int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.user = nil
	return nil
}
func (r *transportAccountRepo) demote() { r.mu.Lock(); defer r.mu.Unlock(); r.user.Role = "user" }

type transportCategoryRepo struct{ biz.CategoryRepo }

func (*transportCategoryRepo) CreateCategory(context.Context, int64, string, int32) (*biz.Category, error) {
	return &biz.Category{ID: 1, Name: "test"}, nil
}

func TestAccountRevocationThroughTransports(t *testing.T) {
	for _, transport := range []string{"http", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			repo := &transportAccountRepo{user: &biz.User{ID: 1, Role: "admin"}}
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			ac := &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32), RefreshTokenSecret: strings.Repeat("b", 32), AccessTokenTimeout: durationpb.New(time.Hour), RefreshTokenTimeout: durationpb.New(24 * time.Hour), PhoneSecret: "phone-secret"}
			repo.phoneHash = phonecrypto.HashPhone("13800138000", []byte(ac.PhoneSecret))
			passwordHash, e := pwdhash.HashPassword("secret-pass")
			require.NoError(t, e)
			repo.user.PasswordHash = passwordHash
			authRepo := data.NewAuthRepo(rdb, log.DefaultLogger)
			auth := biz.NewAuthUsecase(repo, authRepo, ac)
			user := service.NewUserService(auth, biz.NewUserUsecase(repo, authRepo, ac, log.DefaultLogger), nil, nil, log.DefaultLogger)
			mall := service.NewMallService(nil, biz.NewCategoryUsecase(&transportCategoryRepo{}, log.DefaultLogger), nil, nil, log.DefaultLogger)
			order := service.NewOrderService(nil)
			payment := service.NewPaymentService(&callbackPaymentUsecase{}, nil, log.DefaultLogger)
			community := service.NewCommunityService(nil, nil)
			media := service.NewMediaService(nil)
			access, e := auth.GenerateAccessToken(1, "admin")
			require.NoError(t, e)
			refresh, e := auth.GenerateRefreshToken(1, "admin")
			require.NoError(t, e)
			if transport == "http" {
				srv := NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, ac, auth, mall, user, order, payment, community, media, &communityLimiter{}, log.DefaultLogger)
				invoke := func(method, path, body string) *httptest.ResponseRecorder {
					r := httptest.NewRequest(method, path, strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+access)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					srv.ServeHTTP(w, r)
					return w
				}
				require.Equal(t, 200, invoke("POST", "/v1/categories", `{"name":"test"}`).Code)
				repo.demote()
				require.Equal(t, 403, invoke("POST", "/v1/categories", `{"name":"test"}`).Code)
				// A wrong password and an unknown phone must be indistinguishable,
				// or login doubles as an account-enumeration oracle.
				wrongPassword := invoke("POST", "/v1/users/login", `{"phone":"13800138000","password":"totally-wrong-pass"}`)
				unknownPhone := invoke("POST", "/v1/users/login", `{"phone":"13900139000","password":"secret-pass"}`)
				require.Equal(t, 401, wrongPassword.Code)
				require.Equal(t, wrongPassword.Code, unknownPhone.Code)
				require.Equal(t, wrongPassword.Body.String(), unknownPhone.Body.String(), "login failures must not reveal whether the phone is registered")
				// Logout revokes the access token and burns the supplied refresh
				// token, so neither half of the pair keeps working afterwards.
				login := invoke("POST", "/v1/users/login", `{"phone":"13800138000","password":"secret-pass"}`)
				require.Equal(t, 200, login.Code, login.Body.String())
				var loginBody map[string]any
				require.NoError(t, json.Unmarshal(login.Body.Bytes(), &loginBody))
				token, _ := loginBody["token"].(string)
				refreshToken, _ := loginBody["refreshToken"].(string)
				if refreshToken == "" {
					refreshToken, _ = loginBody["refresh_token"].(string)
				}
				require.NotEmpty(t, token)
				require.NotEmpty(t, refreshToken)
				authed := func(method, path, body, tok string) *httptest.ResponseRecorder {
					req := httptest.NewRequest(method, path, strings.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+tok)
					req.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					srv.ServeHTTP(w, req)
					return w
				}
				logout := authed("POST", "/v1/users/logout", `{"refresh_token":"`+refreshToken+`"}`, token)
				require.Equal(t, 200, logout.Code, logout.Body.String())
				require.Equal(t, 401, authed("POST", "/v1/categories", `{"name":"test"}`, token).Code)
				require.Equal(t, 401, invoke("POST", "/v1/users/refresh", `{"refresh_token":"`+refreshToken+`"}`).Code)
				require.Equal(t, 200, invoke("DELETE", "/v1/users/1", "").Code)
				require.Equal(t, 401, invoke("POST", "/v1/categories", `{"name":"test"}`).Code)
				require.Equal(t, 401, invoke("POST", "/v1/users/refresh", `{"refresh_token":"`+refresh+`"}`).Code)
				for _, route := range []string{"register", "login"} {
					require.Equal(t, 400, invoke("POST", "/v1/users/"+route, `{"phone":"13800138000"}`).Code)
				}
				return
			}
			srv := NewGRPCServer(&conf.Server{Grpc: &conf.Server_GRPC{Addr: "127.0.0.1:0"}}, ac, auth, mall, user, order, payment, community, media, &communityLimiter{}, log.DefaultLogger)
			endpoint, e := srv.Endpoint()
			require.NoError(t, e)
			done := make(chan error, 1)
			go func() { done <- srv.Start(context.Background()) }()
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				require.NoError(t, srv.Stop(ctx))
				require.NoError(t, <-done)
			})
			conn, e := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
			require.NoError(t, e)
			defer conn.Close()
			// The subtest performs several bcrypt verifications (cost 12) and
			// runs under -race in CI, so the transport deadline needs headroom.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			signed := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+access))
			mc := mallv1.NewMallClient(conn)
			uc := userv1.NewUserClient(conn)
			_, e = mc.CreateCategory(signed, &mallv1.CreateCategoryRequest{Name: "test"})
			require.NoError(t, e)
			repo.demote()
			_, e = mc.CreateCategory(signed, &mallv1.CreateCategoryRequest{Name: "test"})
			require.Equal(t, codes.PermissionDenied, status.Code(e))
			// A wrong password and an unknown phone must be indistinguishable.
			_, e1 := uc.Login(ctx, &userv1.LoginRequest{Phone: "13800138000", Password: "totally-wrong-pass"})
			_, e2 := uc.Login(ctx, &userv1.LoginRequest{Phone: "13900139000", Password: "secret-pass"})
			require.Error(t, e1)
			st1, ok := status.FromError(e1)
			require.True(t, ok)
			st2, ok := status.FromError(e2)
			require.True(t, ok)
			require.Equal(t, codes.Unauthenticated, st1.Code())
			require.Equal(t, st1.Code(), st2.Code())
			require.Equal(t, st1.Message(), st2.Message())
			// Logout revokes the access token and burns the refresh token.
			loginReply, e := uc.Login(ctx, &userv1.LoginRequest{Phone: "13800138000", Password: "secret-pass"})
			require.NoError(t, e)
			loginSigned := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+loginReply.Token))
			_, e = uc.Logout(loginSigned, &userv1.LogoutRequest{RefreshToken: loginReply.RefreshToken})
			require.NoError(t, e)
			_, e = mc.CreateCategory(loginSigned, &mallv1.CreateCategoryRequest{Name: "test"})
			require.Equal(t, codes.Unauthenticated, status.Code(e))
			_, e = uc.RefreshToken(ctx, &userv1.RefreshRequest{RefreshToken: loginReply.RefreshToken})
			require.Equal(t, codes.Unauthenticated, status.Code(e))
			_, e = uc.DeleteUser(signed, &userv1.DeleteUserRequest{Id: 1})
			require.NoError(t, e)
			_, e = mc.CreateCategory(signed, &mallv1.CreateCategoryRequest{Name: "test"})
			require.Equal(t, codes.Unauthenticated, status.Code(e))
			_, e = uc.RefreshToken(ctx, &userv1.RefreshRequest{RefreshToken: refresh})
			require.Equal(t, codes.Unauthenticated, status.Code(e))
			_, e = uc.Register(ctx, &userv1.RegisterRequest{Phone: "13800138000"})
			require.Equal(t, codes.InvalidArgument, status.Code(e))
			_, e = uc.Login(ctx, &userv1.LoginRequest{Phone: "13800138000"})
			require.Equal(t, codes.InvalidArgument, status.Code(e))
		})
	}
}

// Anonymous login/register/refresh requests are throttled on the IP dimension
// with the real Redis-backed limiter; the JWT whitelist no longer exempts them.
func TestAuthEndpointsAreThrottledThroughHTTP(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ac := &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32), RefreshTokenSecret: strings.Repeat("b", 32), AccessTokenTimeout: durationpb.New(time.Hour), RefreshTokenTimeout: durationpb.New(24 * time.Hour), AuthRequestsPerMinute: 3}
	limiter, e := data.NewWriteLimiter(rdb, &conf.Community{}, ac)
	require.NoError(t, e)
	authRepo := data.NewAuthRepo(rdb, log.DefaultLogger)
	auth := biz.NewAuthUsecase(&transportAccountRepo{user: &biz.User{ID: 1, Role: "admin"}}, authRepo, ac)
	user := service.NewUserService(auth, biz.NewUserUsecase(&transportAccountRepo{}, authRepo, ac, log.DefaultLogger), nil, nil, log.DefaultLogger)
	srv := NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, ac, auth, service.NewMallService(nil, nil, nil, nil, log.DefaultLogger), user, service.NewOrderService(nil), service.NewPaymentService(&callbackPaymentUsecase{}, nil, log.DefaultLogger), service.NewCommunityService(nil, nil), service.NewMediaService(nil), limiter, log.DefaultLogger)
	invoke := func(path, body, remoteAddr string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr = remoteAddr
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	body := `{"phone":"13800138000","password":"secret-pass"}`
	// Within the quota the requests reach the handler (they fail on the
	// unknown account, which is the uniform INVALID_CREDENTIALS response).
	for i := 0; i < 3; i++ {
		require.Equal(t, 401, invoke("/v1/users/login", body, "1.2.3.4:1000").Code)
	}
	// The fourth same-IP attempt is rejected by the limiter before the handler.
	require.Equal(t, 429, invoke("/v1/users/login", body, "1.2.3.4:1000").Code)
	// One shared auth window covers register and refresh too.
	require.Equal(t, 429, invoke("/v1/users/register", body, "1.2.3.4:1000").Code)
	require.Equal(t, 429, invoke("/v1/users/refresh", `{"refresh_token":"x"}`, "1.2.3.4:1000").Code)
	// Anonymous requests from another IP keep their own window.
	require.Equal(t, 401, invoke("/v1/users/login", body, "5.6.7.8:1000").Code)
}
