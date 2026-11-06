package service

import (
	"context"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// recordingProductUsecase captures the includeUnpublished flag the service
// derives from the caller's role.
type recordingProductUsecase struct {
	biz.ProductUsecase
	getIncludeUnpublished  bool
	listIncludeUnpublished bool
}

func (r *recordingProductUsecase) GetProduct(_ context.Context, _ int64, includeUnpublished bool) (*biz.Product, error) {
	r.getIncludeUnpublished = includeUnpublished
	return unlistedTestProduct(), nil
}

func (r *recordingProductUsecase) ListProducts(_ context.Context, _ int64, includeUnpublished bool, _ int32, _ int32) ([]biz.Product, int32, error) {
	r.listIncludeUnpublished = includeUnpublished
	return []biz.Product{*unlistedTestProduct()}, 1, nil
}

// unlistedTestProduct is a status=0 product with field values that satisfy the
// proto conversion (discount in (0,1]).
func unlistedTestProduct() *biz.Product {
	return &biz.Product{ID: 1, Status: 0, Price: decimal.NewFromInt(10), Discount: decimal.NewFromInt(1)}
}

func newProductVisibilityService(uc biz.ProductUsecase) *MallService {
	return NewMallService(uc, nil, nil, nil, log.DefaultLogger)
}

func TestMallService_CatalogReadsHideUnlistedFromNonAdmins(t *testing.T) {
	for _, tc := range []struct {
		role       string
		wantIncAll bool
	}{
		{role: "user", wantIncAll: false},
		{role: "admin", wantIncAll: true},
	} {
		t.Run(tc.role, func(t *testing.T) {
			uc := &recordingProductUsecase{}
			s := newProductVisibilityService(uc)
			ctx := authenticatedPaymentContext(1, tc.role)

			_, err := s.GetProduct(ctx, &pb.GetProductRequest{Id: 1})
			require.NoError(t, err)
			require.Equal(t, tc.wantIncAll, uc.getIncludeUnpublished)

			_, err = s.ListProducts(ctx, &pb.ListProductsRequest{})
			require.NoError(t, err)
			require.Equal(t, tc.wantIncAll, uc.listIncludeUnpublished)
		})
	}
}

// An unauthenticated context (claims absent) must behave like a regular user:
// unlisted products stay hidden.
func TestMallService_CatalogReadsWithoutClaimsHideUnlisted(t *testing.T) {
	uc := &recordingProductUsecase{}
	s := newProductVisibilityService(uc)

	_, err := s.ListProducts(context.Background(), &pb.ListProductsRequest{})
	require.NoError(t, err)
	require.False(t, uc.listIncludeUnpublished)
}
