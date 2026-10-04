package data

import (
	"context"
	"math"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestStockAdjustmentReservesRestorationCapacity(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stock, delta int32
		reserved     int64
		conflict     bool
	}{
		{"available overflow", math.MaxInt32, 1, 0, true},
		{"reserved overflow", 99, math.MaxInt32 - 99, 1, true},
		{"exact total limit", 99, math.MaxInt32 - 100, 1, false},
		{"no reservations", 99, math.MaxInt32 - 99, 0, false},
		{"underflow", 99, -100, 1, true},
		{"zero available", 99, -99, 1, false},
		{"wide reservation sum", 99, 1, math.MaxInt32 + 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			input := biz.StockAdjustmentInput{ProductID: 1, ActorID: 2, Delta: tc.delta, Reason: "restock", IdempotencyKey: "stock-boundary"}
			gomock.InOrder(
				q.EXPECT().GetProductForOrder(gomock.Any(), input.ProductID).Return(db.Product{Stock: tc.stock}, nil),
				q.EXPECT().GetStockAdjustment(gomock.Any(), gomock.Any()).Return(db.StockAdjustment{}, pgx.ErrNoRows),
				q.EXPECT().GetRestorableProductStock(gomock.Any(), input.ProductID).Return(tc.reserved, nil),
			)
			if !tc.conflict {
				balance := tc.stock + tc.delta
				q.EXPECT().AdjustProductStock(gomock.Any(), db.AdjustProductStockParams{ID: input.ProductID, Delta: input.Delta}).Return(db.Product{Stock: balance}, nil)
				q.EXPECT().CreateStockAdjustment(gomock.Any(), db.CreateStockAdjustmentParams{ProductID: input.ProductID, ActorID: input.ActorID, Delta: input.Delta, Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, ResultingStock: balance}).Return(db.StockAdjustment{ResultingStock: balance}, nil)
			}
			repo := NewProductRepo(&Data{q: q}, testTxManager{q: q}, log.DefaultLogger)
			result, err := repo.AdjustStock(context.Background(), input)
			if tc.conflict {
				require.Error(t, err)
				require.Equal(t, int32(409), kerrors.FromError(err).Code)
				require.Equal(t, "STOCK_ADJUSTMENT_CONFLICT", kerrors.FromError(err).Reason)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.stock+tc.delta, result.ResultingStock)
			}
		})
	}
}
