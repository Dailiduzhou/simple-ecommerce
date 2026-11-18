package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/service"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestCatalogInvalidInputThroughHTTPAndGRPC(t *testing.T) {
	// nil repository proves malformed inputs are rejected before any DB call.
	mall := service.NewMallService(biz.NewProductUsecase(nil, log.DefaultLogger), nil, nil, nil, log.DefaultLogger)
	ac := &conf.Auth{AccessTokenSecret: strings.Repeat("a", 32)}
	user, community, media, _ := communityServices()
	order := service.NewOrderService(nil, nil)
	payment := service.NewPaymentService(&callbackPaymentUsecase{}, nil, nil, log.DefaultLogger)
	httpServer := NewHTTPServer(&conf.Server{Http: &conf.Server_HTTP{}}, ac, &communityAuth{}, mall, user, order, payment, community, media, &communityLimiter{}, log.DefaultLogger)
	grpcServer := NewGRPCServer(&conf.Server{Grpc: &conf.Server_GRPC{Addr: "127.0.0.1:0"}}, ac, &communityAuth{}, mall, user, order, payment, community, media, &communityLimiter{}, log.DefaultLogger)
	endpoint, err := grpcServer.Endpoint()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- grpcServer.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, grpcServer.Stop(ctx))
		require.NoError(t, <-done)
	})
	conn, err := grpc.NewClient(endpoint.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	client := mallv1.NewMallClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token := communityToken(t, 1, "admin", "live")
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
	for _, tc := range []struct {
		name   string
		change func(*mallv1.CreateProductRequest)
	}{
		{"empty name", func(r *mallv1.CreateProductRequest) { r.Name = "" }},
		{"blank Unicode name", func(r *mallv1.CreateProductRequest) { r.Name = " \u3000\u00a0" }},
		{"NUL name", func(r *mallv1.CreateProductRequest) { r.Name = "a\x00" }},
		{"long name", func(r *mallv1.CreateProductRequest) { r.Name = strings.Repeat("字", 256) }},
		{"invalid price", func(r *mallv1.CreateProductRequest) { r.Price = "not-a-number" }},
		{"huge exponent", func(r *mallv1.CreateProductRequest) { r.Price = "1e2147483647" }},
		{"negative huge exponent", func(r *mallv1.CreateProductRequest) { r.Price = "1e-2147483648" }},
		{"long coefficient", func(r *mallv1.CreateProductRequest) { r.Price = strings.Repeat("9", 65) }},
		{"invalid discount", func(r *mallv1.CreateProductRequest) { r.Discount = "bad" }},
		{"negative stock", func(r *mallv1.CreateProductRequest) { r.Stock = -1 }},
		{"invalid category", func(r *mallv1.CreateProductRequest) { r.CategoryId = 0 }},
		{"invalid cover", func(r *mallv1.CreateProductRequest) { r.CoverImage = "javascript:alert(1)" }},
		{"too many images", func(r *mallv1.CreateProductRequest) {
			for range 10 {
				r.MediaAssets = append(r.MediaAssets, &mallv1.MediaInfo{OssUrl: "https://cdn.test/image.png"})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create := &mallv1.CreateProductRequest{MerchantId: 1, CategoryId: 1, Name: "valid", Price: "1.23", Discount: "1", Stock: 1}
			tc.change(create)
			body, err := protojson.Marshal(create)
			require.NoError(t, err)
			request := httptest.NewRequest("POST", "/v1/products", strings.NewReader(string(body)))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			httpServer.ServeHTTP(response, request)
			require.Equal(t, 400, response.Code, response.Body.String())
			_, err = client.CreateProduct(ctx, create)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			if create.Stock < 0 {
				return
			}
			update := &mallv1.UpdateProductRequest{Id: 1, CategoryId: create.CategoryId, Name: create.Name, Price: create.Price, Discount: create.Discount, CoverImage: create.CoverImage, MediaAssets: create.MediaAssets, Description: create.Description}
			body, err = protojson.Marshal(update)
			require.NoError(t, err)
			request = httptest.NewRequest("PUT", "/v1/products/1", strings.NewReader(string(body)))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response = httptest.NewRecorder()
			httpServer.ServeHTTP(response, request)
			require.Equal(t, 400, response.Code, response.Body.String())
			_, err = client.UpdateProduct(ctx, update)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
