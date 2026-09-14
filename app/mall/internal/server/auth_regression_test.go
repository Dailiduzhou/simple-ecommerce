package server

import (
	"context"
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
	mu   sync.Mutex
	user *biz.User
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
			ac := &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32), RefreshTokenSecret: strings.Repeat("b", 32), AccessTokenTimeout: durationpb.New(time.Hour), RefreshTokenTimeout: durationpb.New(24 * time.Hour)}
			auth := biz.NewAuthUsecase(repo, data.NewAuthRepo(rdb, log.DefaultLogger), ac)
			user := service.NewUserService(auth, biz.NewUserUsecase(repo, ac, log.DefaultLogger), nil, nil, log.DefaultLogger)
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
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			signed := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+access))
			mc := mallv1.NewMallClient(conn)
			uc := userv1.NewUserClient(conn)
			_, e = mc.CreateCategory(signed, &mallv1.CreateCategoryRequest{Name: "test"})
			require.NoError(t, e)
			repo.demote()
			_, e = mc.CreateCategory(signed, &mallv1.CreateCategoryRequest{Name: "test"})
			require.Equal(t, codes.PermissionDenied, status.Code(e))
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
