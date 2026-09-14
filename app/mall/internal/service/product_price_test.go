package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	http "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
)

func TestProductPriceOverflowReturnsBadRequest(t *testing.T) {
	// Invalid amounts must be rejected by the real usecase before touching a repo.
	uc := biz.NewProductUsecase(nil, log.DefaultLogger)
	srv := http.NewServer(http.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			return next(biz.WithClaims(ctx, &biz.EcommerceClaims{UserID: 1, Role: "admin"}), req)
		}
	}))
	pb.RegisterMallHTTPServer(srv, NewMallService(uc, nil, nil, nil, log.DefaultLogger))
	for _, price := range []string{"92233720368547758.08", "184467440737095516.17", "-0.01", "0.001"} {
		for _, route := range []struct{ method, path string }{{"POST", "/v1/products"}, {"PUT", "/v1/products/1"}} {
			t.Run(route.method+"/"+price, func(t *testing.T) {
				r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"price":"`+price+`","discount":"1"}`))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				srv.ServeHTTP(w, r)
				require.Equal(t, 400, w.Code, w.Body.String())
				require.Contains(t, w.Body.String(), "PRODUCT_PRICE_INVALID")
			})
		}
	}
}
