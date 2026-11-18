package biz

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestParseProductPrice(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		minor int64
	}{
		{"0", 0}, {"-0.00", 0}, {".01", 1}, {"+1.20", 120}, {"1.2300", 123},
		{"123e-2", 123}, {"1.2E+1", 1200}, {"0e64", 0}, {"0e-64", 0},
		{"92233720368547758.07", math.MaxInt64},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			p, err := ParseProductPrice(tc.raw)
			require.NoError(t, err)
			n, err := ProductPriceMinor(p)
			require.NoError(t, err)
			require.Equal(t, tc.minor, n)
		})
	}
	for _, raw := range []string{"", " ", "NaN", "inf", "1/2", "1.2.3", "1e", "1e+", "1e1e1", "-1", "0.001", "1.234", "92233720368547758.08", "1e64", "1e65", "1e-65", "1e2147483647", "1e-2147483648", strings.Repeat("0", 65), strings.Repeat("9", 10000)} {
		t.Run(raw[:min(len(raw), 30)], func(t *testing.T) { _, err := ParseProductPrice(raw); require.EqualValues(t, 400, errors.Code(err)) })
	}
}

func TestParseProductDiscount(t *testing.T) {
	for _, raw := range []string{"1", "0.01", ".50", "1.0000", "10e-1", "50e-2"} {
		_, err := ParseProductDiscount(raw)
		require.NoError(t, err, raw)
	}
	for _, raw := range []string{"", "bad", "0", "-1", "1.01", "0.001", "1e2147483647", "1e-2147483648", strings.Repeat("9", 65)} {
		_, err := ParseProductDiscount(raw)
		require.EqualValues(t, 400, errors.Code(err), raw)
	}
}

func TestProductDecimalInternalComplexityGuard(t *testing.T) {
	for _, value := range []decimal.Decimal{decimal.New(1, math.MaxInt32), decimal.New(1, math.MinInt32), decimal.New(0, math.MaxInt32), decimal.RequireFromString(strings.Repeat("9", 10000))} {
		_, err := ProductPriceMinor(value)
		require.EqualValues(t, 400, errors.Code(err))
		require.EqualValues(t, 400, errors.Code(ProductDiscount(value)))
	}
}

func TestProductInputValidationBeforeRepository(t *testing.T) {
	actor := Actor{ID: 1, Role: RoleMerchant}
	// An unimplemented repository panics on any unexpected read/write.
	uc := NewProductUsecase(&authzProducts{}, log.DefaultLogger)
	for _, tc := range []struct {
		name                                string
		category                            int64
		stock                               int32
		price, discount, cover, description string
		images                              []MediaInfo
	}{
		{name: " \t\u3000", category: 1, price: "1", discount: "1"},
		{name: strings.Repeat("字", 256), category: 1, price: "1", discount: "1"},
		{name: "a\x00", category: 1, price: "1", discount: "1"},
		{name: "\xff", category: 1, price: "1", discount: "1"},
		{name: "valid", price: "1", discount: "1"},
		{name: "valid", category: 1, stock: -1, price: "1", discount: "1"},
		{name: "valid", category: 1, price: "bad", discount: "1"},
		{name: "valid", category: 1, price: "1", discount: "bad"},
		{name: "valid", category: 1, price: "1", discount: "1", description: strings.Repeat("字", 10001)},
		{name: "valid", category: 1, price: "1", discount: "1", cover: "javascript:alert(1)"},
		{name: "valid", category: 1, price: "1", discount: "1", images: make([]MediaInfo, 10)},
		{name: "valid", category: 1, price: "1", discount: "1", images: []MediaInfo{{BucketName: "b", ObjectKey: "key"}, {BucketName: "b", ObjectKey: "key"}}},
	} {
		_, err := uc.CreateProduct(context.Background(), actor, 0, tc.category, tc.name, tc.price, tc.discount, tc.stock, 1, tc.cover, tc.images, tc.description)
		require.EqualValues(t, 400, errors.Code(err), "%+v", tc)
		if tc.stock >= 0 {
			_, err = uc.UpdateProduct(context.Background(), actor, 1, tc.category, tc.name, tc.price, tc.discount, tc.cover, tc.images, tc.description)
			require.EqualValues(t, 400, errors.Code(err))
		}
	}
	repo := &authzProducts{}
	uc = NewProductUsecase(repo, log.DefaultLogger)
	product, err := uc.CreateProduct(context.Background(), actor, 0, 1, "  商品  ", "1", "1", 0, 1, "https://cdn.test/cover.png", nil, "")
	require.NoError(t, err)
	require.Equal(t, "商品", product.Name)
}

func FuzzParseProductAmounts(f *testing.F) {
	for _, s := range []string{"0", "1.000", "0.01", "1e2147483647", "1e-2147483648", "92233720368547758.07", "NaN", "1e+"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		price, err := ParseProductPrice(raw)
		if err == nil {
			_, err = ProductPriceMinor(price)
			require.NoError(t, err)
		} else {
			require.EqualValues(t, 400, errors.Code(err))
		}
		discount, err := ParseProductDiscount(raw)
		if err == nil {
			require.NoError(t, ProductDiscount(discount))
		} else {
			require.EqualValues(t, 400, errors.Code(err))
		}
	})
}

func BenchmarkParseProductPrice(b *testing.B) {
	for _, raw := range []string{"123.45", "92233720368547758.07", "1e2147483647", "1e-2147483648", strings.Repeat("9", 10000)} {
		b.Run(raw[:min(len(raw), 25)], func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = ParseProductPrice(raw)
			}
		})
	}
}
