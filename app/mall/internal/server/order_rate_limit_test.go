package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	orderv1 "github.com/Dailiduzhou/simple-ecommerce/api/order/v1"
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
)

type limitedOrders struct {
	biz.OrderUsecase
	calls atomic.Int32
	uid   atomic.Int64
}

func (u *limitedOrders) CreateOrder(_ context.Context, req *biz.CreateOrderReq) (*biz.Order, error) {
	u.calls.Add(1)
	u.uid.Store(req.UserID)
	return &biz.Order{ID: 1, UserID: req.UserID}, nil
}

func TestCheckoutQuotaUsesAuthenticatedUserAcrossHTTPAndGRPC(t *testing.T) {
	m := miniredis.RunT(t)
	r := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { _ = r.Close() })
	limiter, err := data.NewWriteLimiter(r, nil, nil)
	require.NoError(t, err)
	user, community, media, _ := communityServices()
	uc := &limitedOrders{}
	orders := service.NewOrderService(uc, nil)
	mall := service.NewMallService(nil, nil, nil, nil, log.DefaultLogger)
	payment := service.NewPaymentService(&callbackPaymentUsecase{}, nil, nil, log.DefaultLogger)
	ac := &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}
	httpServer := NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, ac, &communityAuth{}, mall, user, orders, payment, community, media, limiter, log.DefaultLogger)
	invoke := func(uid int64, jti, ip string) int {
		body := `{"address_id":"1","items":[{"product_id":"1","quantity":1}],"idempotency_key":"checkout-key","user_id":"999"}`
		req := httptest.NewRequest("POST", "/v1/orders", strings.NewReader(body))
		req.RemoteAddr = ip + ":1234"
		req.Header.Set("Content-Type", "application/json")
		if uid > 0 {
			req.Header.Set("Authorization", "Bearer "+communityToken(t, uid, "user", jti))
		}
		w := httptest.NewRecorder()
		httpServer.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, 401, invoke(0, "", "192.0.2.1"))
	for i := 0; i < 10; i++ {
		require.Equal(t, 200, invoke(41, fmt.Sprint(i), fmt.Sprintf("192.0.2.%d", i+1)))
	}
	require.EqualValues(t, 41, uc.uid.Load(), "body user_id cannot select the quota or owner")
	require.Equal(t, 429, invoke(41, "another-session", "192.0.2.99"))
	require.Equal(t, 200, invoke(42, "live", "192.0.2.99"))

	srv := NewGRPCServer(&conf.Server{Grpc: &conf.Server_GRPC{Addr: "127.0.0.1:0"}}, ac, &communityAuth{}, mall, user, orders, payment, community, media, limiter, log.DefaultLogger)
	endpoint, err := srv.Endpoint()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, srv.Stop(ctx))
		require.NoError(t, <-done)
	})
	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	client := orderv1.NewOrderClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+communityToken(t, 41, "user", "grpc-session")))
	req := &orderv1.CreateOrderRequest{AddressId: 1, Items: []*orderv1.OrderItemInput{{ProductId: 1, Quantity: 1}}, IdempotencyKey: "checkout-key"}
	_, err = client.CreateOrder(ctx, req)
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.EqualValues(t, 11, uc.calls.Load(), "denied requests never reach checkout")
	m.FastForward(time.Minute)
	_, err = client.CreateOrder(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 12, uc.calls.Load())
}
