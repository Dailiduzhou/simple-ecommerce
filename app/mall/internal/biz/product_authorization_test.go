package biz

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// authzProducts records the merchant the usecase authorized against.
type authzProducts struct {
	ProductRepo
	product    *Product
	createdFor int64
	updated    bool
}

func (r *authzProducts) CreateProduct(_ context.Context, merchantID, _ int64, name string, _ decimal.Decimal, _ decimal.Decimal, _ int32, _ int16, _ []MediaInfo, _ []MediaInfo, _ string) (*Product, error) {
	r.createdFor = merchantID
	return &Product{MerchantID: merchantID, Name: name}, nil
}

func (r *authzProducts) GetProductForManagement(context.Context, int64) (*Product, error) {
	return r.product, nil
}

func (r *authzProducts) UpdateProduct(_ context.Context, id, _ int64, _ string, _ decimal.Decimal, _ decimal.Decimal, _ []MediaInfo, _ []MediaInfo, _ string) (*Product, error) {
	r.updated = true
	return r.product, nil
}

func (r *authzProducts) UpdateProductStatus(context.Context, int64, int32) error {
	r.updated = true
	return nil
}

func (r *authzProducts) SoftDeleteProduct(context.Context, int64) error {
	r.updated = true
	return nil
}

func TestProductAuthorization(t *testing.T) {
	ctx := context.Background()
	merchant := Actor{ID: 7, Role: RoleMerchant}
	otherMerchant := int64(8)
	admin := Actor{ID: 1, Role: RoleAdmin}
	user := Actor{ID: 9, Role: RoleUser}

	repo := &authzProducts{}
	uc := NewProductUsecase(repo, log.DefaultLogger)

	// A merchant can only create for themselves; the body field is optional.
	_, err := uc.CreateProduct(ctx, merchant, 0, 1, "own", "10.00", "1", 5, 1, "", nil, "")
	require.NoError(t, err)
	require.Equal(t, int64(7), repo.createdFor)
	_, err = uc.CreateProduct(ctx, merchant, otherMerchant, 1, "foreign", "10.00", "1", 5, 1, "", nil, "")
	require.Equal(t, int32(403), errors.FromError(err).Code)

	// Administrators must name the merchant explicitly.
	_, err = uc.CreateProduct(ctx, admin, 0, 1, "platform", "10.00", "1", 5, 1, "", nil, "")
	require.Equal(t, int32(400), errors.FromError(err).Code)
	require.Contains(t, err.Error(), "MERCHANT_REQUIRED")
	_, err = uc.CreateProduct(ctx, admin, otherMerchant, 1, "managed", "10.00", "1", 5, 1, "", nil, "")
	require.NoError(t, err)
	require.Equal(t, otherMerchant, repo.createdFor)

	// Ordinary users have no catalog capability at all.
	_, err = uc.CreateProduct(ctx, user, 0, 1, "nope", "10.00", "1", 5, 1, "", nil, "")
	require.Equal(t, int32(403), errors.FromError(err).Code)

	// Existing products are scoped to their owning merchant.
	repo.product = &Product{ID: 5, MerchantID: otherMerchant}
	repo.updated = false
	_, err = uc.UpdateProduct(ctx, merchant, 5, 1, "hijack", "10.00", "1", "", nil, "")
	require.Equal(t, int32(403), errors.FromError(err).Code)
	require.False(t, repo.updated)

	err = uc.UpdateProductStatus(ctx, merchant, 5, 0)
	require.Equal(t, int32(403), errors.FromError(err).Code)
	err = uc.DeleteProduct(ctx, merchant, 5)
	require.Equal(t, int32(403), errors.FromError(err).Code)
	require.False(t, repo.updated)

	_, err = uc.UpdateProduct(ctx, admin, 5, 1, "managed", "10.00", "1", "", nil, "")
	require.NoError(t, err)
	require.True(t, repo.updated)
	require.NoError(t, uc.UpdateProductStatus(ctx, admin, 5, 0))
	require.NoError(t, uc.DeleteProduct(ctx, admin, 5))

	// Missing products are reported as not-found, not as forbidden.
	repo.product = nil
	err = uc.DeleteProduct(ctx, admin, 5)
	require.ErrorIs(t, err, ErrProductNotFound)
}
