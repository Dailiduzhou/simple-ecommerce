package biz

import (
	"context"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type validatedProductRepo struct {
	ProductRepo
	name string
}

func (r *validatedProductRepo) CreateProduct(_ context.Context, _ int64, name string, _, _ decimal.Decimal, _ int32, _ int16, _, _ []MediaInfo, _ string) (*Product, error) {
	r.name = name
	return &Product{Name: name}, nil
}
func (r *validatedProductRepo) UpdateProduct(_ context.Context, _, _ int64, name string, _, _ decimal.Decimal, _, _ []MediaInfo, _ string) (*Product, error) {
	r.name = name
	return &Product{Name: name}, nil
}

func TestProductWriteValidation(t *testing.T) {
	for _, update := range []bool{false, true} {
		for _, tc := range []struct{ name, price, discount, reason string }{
			{"x", "", "1", "PRODUCT_PRICE_INVALID"},
			{"x", "abc", "1", "PRODUCT_PRICE_INVALID"},
			{"x", "1", "", "PRODUCT_DISCOUNT_INVALID"},
			{"x", "1", "abc", "PRODUCT_DISCOUNT_INVALID"},
			{"x", "-1", "1", "PRODUCT_PRICE_INVALID"},
			{"x", "1.001", "1", "PRODUCT_PRICE_INVALID"},
			{"x", "1", "0", "PRODUCT_DISCOUNT_INVALID"},
			{"x", "1", "1.1", "PRODUCT_DISCOUNT_INVALID"},
			{"x", "1", "0.999", "PRODUCT_DISCOUNT_INVALID"},
			{"", "1", "1", "PRODUCT_NAME_INVALID"},
			{" \t\n　", "1", "1", "PRODUCT_NAME_INVALID"},
			{strings.Repeat("茶", 256), "1", "1", "PRODUCT_NAME_INVALID"},
		} {
			uc := NewProductUsecase(nil, log.DefaultLogger) // must not reach persistence
			var err error
			if update {
				_, err = uc.UpdateProduct(context.Background(), 1, 1, tc.name, tc.price, tc.discount, "", nil, "")
			} else {
				_, err = uc.CreateProduct(context.Background(), 1, tc.name, tc.price, tc.discount, 1, 1, "", nil, "")
			}
			require.Equal(t, 400, errors.Code(err), "update=%v case=%+v", update, tc)
			require.Equal(t, tc.reason, errors.Reason(err))
		}
	}
	uc := NewProductUsecase(nil, log.DefaultLogger)
	_, err := uc.CreateProduct(context.Background(), 1, "x", "1", "1", -5, 1, "", nil, "")
	require.Equal(t, "PRODUCT_STOCK_INVALID", errors.Reason(err))
	r := &validatedProductRepo{}
	uc = NewProductUsecase(r, log.DefaultLogger)
	_, err = uc.CreateProduct(context.Background(), 1, " 茶 ", "1", "1", 1, 1, "", nil, "")
	require.NoError(t, err)
	require.Equal(t, "茶", r.name)
	_, err = uc.UpdateProduct(context.Background(), 1, 1, " 茶 ", "1", "1", "", nil, "")
	require.NoError(t, err)
	require.Equal(t, "茶", r.name)
}
