package data

import (
	"context"
	"testing"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type testTxManager struct{ q db.Querier }

func (t testTxManager) InTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, ctxTxKey{}, t.q))
}

// InTxSnapshot mirrors InTx: unit tests inject the querier directly.
func (t testTxManager) InTxSnapshot(ctx context.Context, fn func(context.Context) error) error {
	return t.InTx(ctx, fn)
}

func TestShippingAddressCacheCannotCrossOwners(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	d := newTestData(t, q, redisServer)
	repo := NewShippingAddressRepo(d, testTxManager{q: q}, log.DefaultLogger)
	repo.setCache(context.Background(), redisKey(shippingAddressCacheKey(2, 9), "g", 0), &biz.ShippingAddress{ID: 9, UserID: 1})
	address, err := repo.GetShippingAddress(context.Background(), 9, 2)
	require.True(t, mallv1.IsShippingAddressNotFound(err))
	require.Nil(t, address)
}

func TestShippingAddressNotFoundReturnsExplicitErrorNotTypedNil(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	q.EXPECT().GetShippingAddress(gomock.Any(), db.GetShippingAddressParams{ID: 9, UserID: 2}).Return(db.ShippingAddress{}, pgx.ErrNoRows)
	d := newTestData(t, q, redisServer)
	repo := NewShippingAddressRepo(d, testTxManager{q: q}, log.DefaultLogger)
	address, err := repo.GetShippingAddress(context.Background(), 9, 2)
	require.True(t, mallv1.IsShippingAddressNotFound(err))
	require.Nil(t, address)
}

func TestSetDefaultShippingAddressIsAtomicAndInvalidatesBothDetails(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	q.EXPECT().GetShippingAddress(gomock.Any(), db.GetShippingAddressParams{ID: 9, UserID: 2}).Return(db.ShippingAddress{ID: 9, UserID: 2}, nil)
	q.EXPECT().GetDefaultShippingAddress(gomock.Any(), int64(2)).Return(db.ShippingAddress{ID: 8, UserID: 2, IsDefault: true}, nil)
	q.EXPECT().ClearDefaultShippingAddress(gomock.Any(), int64(2)).Return(nil)
	q.EXPECT().SetDefaultShippingAddress(gomock.Any(), db.SetDefaultShippingAddressParams{ID: 9, UserID: 2}).Return(nil)
	d := newTestData(t, q, redisServer)
	repo := NewShippingAddressRepo(d, testTxManager{q: q}, log.DefaultLogger)
	repo.setCache(context.Background(), redisKey(shippingAddressCacheKey(2, 8), "g", 0), &biz.ShippingAddress{ID: 8, UserID: 2})
	repo.setCache(context.Background(), redisKey(shippingAddressCacheKey(2, 9), "g", 0), &biz.ShippingAddress{ID: 9, UserID: 2})
	require.NoError(t, repo.SetDefaultShippingAddress(context.Background(), 9, 2))
	// Both details and the list moved to a new generation, so the previously
	// cached values can no longer be served.
	require.Equal(t, "1", d.rdb.Get(context.Background(), shippingAddressGenerationKey(2, 8)).Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), shippingAddressGenerationKey(2, 9)).Val())
	require.Equal(t, "1", d.rdb.Get(context.Background(), shippingAddressListGenerationKey(2)).Val())
}
