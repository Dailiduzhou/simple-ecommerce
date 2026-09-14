package data

import (
	"context"
	"math"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestReviewPriceRange(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{
		{"92233720368547758.07", true}, {"92233720368547758.08", false}, {"184467440737095516.17", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			n, e := biz.ProductPriceMinor(decimal.RequireFromString(tc.value))
			if tc.valid {
				require.NoError(t, e)
				require.Equal(t, int64(math.MaxInt64), n)
			} else {
				require.Error(t, e)
			}
		})
	}
}
func TestReviewProductLateFill(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	d := newTestData(t, q, mr)
	r := NewProductRepo(d, log.DefaultLogger)
	ctx := context.Background()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	q.EXPECT().GetProduct(gomock.Any(), int64(1)).DoAndReturn(func(context.Context, int64) (db.Product, error) {
		close(entered)
		<-release
		return db.Product{ID: 1, PriceMinor: 100}, nil
	})
	go func() { defer close(done); _, e := r.GetProduct(ctx, 1); require.NoError(t, e) }()
	<-entered
	q.EXPECT().GetProduct(gomock.Any(), int64(1)).Return(db.Product{ID: 1}, nil)
	q.EXPECT().UpdateProductStatus(gomock.Any(), gomock.Any()).Return(nil)
	require.NoError(t, r.UpdateProductStatus(ctx, 1, 1))
	q.EXPECT().GetProduct(gomock.Any(), int64(1)).Return(db.Product{ID: 1, PriceMinor: 200}, nil)
	fresh, e := r.GetProduct(ctx, 1)
	require.NoError(t, e)
	require.Equal(t, "2", fresh.Price.String())
	close(release)
	<-done
	fresh, e = r.GetProduct(ctx, 1)
	require.NoError(t, e)
	require.Equal(t, "2", fresh.Price.String())
}

func TestReviewEventZeroIsFilter(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	r := NewEventRepo(&Data{q: q}, log.DefaultLogger)
	q.EXPECT().ListEvents(gomock.Any(), db.ListEventsParams{Limit: 10}).Return([]db.Event{{ID: 1, Status: 1}}, nil)
	q.EXPECT().ListEventsByStatus(gomock.Any(), db.ListEventsByStatusParams{Status: 0, Limit: 10}).Return([]db.Event{{ID: 2, Status: 0}}, nil)
	all, e := r.ListEvents(context.Background(), nil, 10, 0)
	require.NoError(t, e)
	require.Equal(t, int64(1), all[0].ID)
	zero := int32(0)
	filtered, e := r.ListEvents(context.Background(), &zero, 10, 0)
	require.NoError(t, e)
	require.Equal(t, int64(2), filtered[0].ID)
	require.NotEqual(t, eventListPresenceCacheKey(0, nil, 10, 0), eventListPresenceCacheKey(0, &zero, 10, 0))
}
