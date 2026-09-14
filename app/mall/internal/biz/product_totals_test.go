package biz

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type reviewProducts struct {
	ProductRepo
	category int64
	total    int64
}

func (r *reviewProducts) CountProducts(_ context.Context, id int64) (int64, error) {
	r.category = id
	return r.total, nil
}
func (r *reviewProducts) ListProducts(context.Context, int32, int32) ([]Product, error) {
	return []Product{{ID: 1}}, nil
}
func (r *reviewProducts) ListProductsByCategory(context.Context, int64, int32, int32) ([]Product, error) {
	return []Product{{ID: 2}}, nil
}
func TestReviewProductTotals(t *testing.T) {
	for _, category := range []int64{0, 9} {
		for _, total := range []int64{0, 25} {
			r := &reviewProducts{total: total}
			uc := NewProductUsecase(r, log.DefaultLogger)
			_, got, e := uc.ListProducts(context.Background(), category, 10, 2)
			require.NoError(t, e)
			require.Equal(t, int32(total), got)
			require.Equal(t, category, r.category)
		}
	}
}
