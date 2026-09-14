package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestProductListInvalidationUsesGlobalAndCategoryGenerations(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	d := newTestData(t, q, redisServer)
	repo := NewProductRepo(d, log.DefaultLogger)
	repo.invalidateProductLists(context.Background(), 7, 8, 7)
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:list:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:category:7:gen").Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), "product:category:8:gen").Val())
}

func TestProductStockInvalidationSurvivesRequestCancellation(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	d := newTestData(t, q, mr)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q.EXPECT().ListOrderItems(gomock.Any(), int64(10)).DoAndReturn(func(ctx context.Context, _ int64) ([]db.OrderItem, error) {
		require.NoError(t, ctx.Err())
		_, bounded := ctx.Deadline()
		require.True(t, bounded)
		return []db.OrderItem{{ProductID: 42}}, nil
	})
	q.EXPECT().GetProduct(gomock.Any(), int64(42)).DoAndReturn(func(ctx context.Context, _ int64) (db.Product, error) {
		require.NoError(t, ctx.Err())
		return db.Product{ID: 42, CategoryID: 7}, nil
	})
	invalidateProductCachesForOrder(ctx, d, log.NewHelper(log.DefaultLogger), 10)
	for _, key := range []string{"product:42:gen", "product:list:gen", "product:category:7:gen"} {
		value, err := mr.Get(key)
		require.NoError(t, err)
		require.Equal(t, "1", value)
	}
}

func TestProductDetailGenerationOnlyChangesAfterCommit(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[commit], func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			mr := miniredis.RunT(t)
			d := newTestData(t, q, mr)
			state := &txState{}
			ctx := context.WithValue(WithQuerier(context.Background(), q, nil), txStateKey{}, state)
			q.EXPECT().GetProduct(gomock.Any(), int64(42)).Return(db.Product{ID: 42, CategoryID: 7}, nil)
			q.EXPECT().UpdateProductStatus(gomock.Any(), db.UpdateProductStatusParams{ID: 42, Status: 1}).Return(nil)
			require.NoError(t, NewProductRepo(d, log.DefaultLogger).UpdateProductStatus(ctx, 42, 1))
			require.False(t, mr.Exists("product:42:gen"))
			if commit {
				for _, fn := range state.afterCommit {
					fn()
				}
				value, e := mr.Get("product:42:gen")
				require.NoError(t, e)
				require.Equal(t, "1", value)
			}
		})
	}
}
