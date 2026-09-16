package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"github.com/golang/mock/gomock"
)

func TestBrowsingHistoryListMapsEffectivePrice(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	viewedAt := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	rows := []db.ListBrowsingHistoryRow{
		{ProductID: 7, LastViewedAt: viewedAt, Name: "p7", PriceMinor: 1234, Discount: decimal.NewFromFloat(0.85), Available: true},
		{ProductID: 8, LastViewedAt: viewedAt, Name: "p8", PriceMinor: 1234, Discount: decimal.NewFromInt(1), Available: false},
	}
	q.EXPECT().ListBrowsingHistory(gomock.Any(), gomock.Any()).Return(rows, nil)
	d := newTestData(t, q, redisServer)
	repo := NewBrowsingHistoryRepo(d, testTxManager{q: q}, biz.CommunityPolicy{HistoryRetention: 90 * 24 * time.Hour})
	items, next, e := repo.List(context.Background(), 3, biz.Page{Limit: 10}, biz.HistoryFilter{})
	require.NoError(t, e)
	require.Empty(t, next)
	require.Len(t, items, 2)
	// The browsing history must show the discounted charge price, exactly
	// like the product endpoints (1234 * 0.85 rounds to 1049).
	require.Equal(t, int64(1049), items[0].EffectivePriceMinor)
	require.Equal(t, int64(1234), items[0].PriceMinor)
	// Unavailable products keep hiding prices, both list and effective.
	require.Equal(t, int64(0), items[1].PriceMinor)
	require.Equal(t, int64(0), items[1].EffectivePriceMinor)
}

func TestBrowsingHistoryListRejectsCorruptDiscount(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	rows := []db.ListBrowsingHistoryRow{
		{ProductID: 7, LastViewedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, Name: "p7", PriceMinor: 1234, Discount: decimal.NewFromInt(2), Available: true},
	}
	q.EXPECT().ListBrowsingHistory(gomock.Any(), gomock.Any()).Return(rows, nil)
	d := newTestData(t, q, redisServer)
	repo := NewBrowsingHistoryRepo(d, testTxManager{q: q}, biz.CommunityPolicy{HistoryRetention: 90 * 24 * time.Hour})
	_, _, e := repo.List(context.Background(), 3, biz.Page{Limit: 10}, biz.HistoryFilter{})
	// A stored discount outside (0,1] means corrupted pricing data: refuse to
	// render a possibly-wrong price instead of guessing.
	require.Error(t, e)
}