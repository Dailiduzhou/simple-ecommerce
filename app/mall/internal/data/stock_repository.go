package data

import (
	"context"
	"errors"
	"math"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5"
)

func (r *ProductRepo) AdjustStock(ctx context.Context, input biz.StockAdjustmentInput) (*biz.StockAdjustment, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	var adjustment db.StockAdjustment
	var categoryID int64
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		product, err := q.GetProductForOrder(ctx, input.ProductID)
		if errors.Is(err, pgx.ErrNoRows) {
			return biz.ErrProductNotFound
		}
		if err != nil {
			return err
		}
		categoryID = product.CategoryID
		adjustment, err = q.GetStockAdjustment(ctx, db.GetStockAdjustmentParams{ProductID: input.ProductID, IdempotencyKey: input.IdempotencyKey})
		if err == nil {
			if adjustment.ActorID != input.ActorID || adjustment.Delta != input.Delta || adjustment.Reason != input.Reason {
				return biz.ErrIdempotencyKeyConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Keep room for every outstanding cancellation/full order refund.
		// The product lock serializes this fresh snapshot with checkout and
		// restoration; an uncommitted restoration still counts as reserved.
		reserved, err := q.GetRestorableProductStock(ctx, input.ProductID)
		if err != nil {
			return err
		}
		balance := int64(product.Stock) + int64(input.Delta)
		if balance < 0 || reserved > math.MaxInt32-balance {
			return kratoserrors.Conflict("STOCK_ADJUSTMENT_CONFLICT", "adjustment would put stock below zero or exceed 2147483647 including restorable order stock")
		}
		updated, err := q.AdjustProductStock(ctx, db.AdjustProductStockParams{ID: input.ProductID, Delta: input.Delta})
		if err != nil {
			return err
		}
		adjustment, err = q.CreateStockAdjustment(ctx, db.CreateStockAdjustmentParams{
			ProductID: input.ProductID, ActorID: input.ActorID, Delta: input.Delta,
			Reason: input.Reason, IdempotencyKey: input.IdempotencyKey, ResultingStock: updated.Stock,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	r.invalidateProduct(ctx, input.ProductID, categoryID)
	return &biz.StockAdjustment{
		ID: adjustment.ID, ProductID: adjustment.ProductID, ActorID: adjustment.ActorID,
		Delta: adjustment.Delta, ResultingStock: adjustment.ResultingStock,
		Reason: adjustment.Reason, IdempotencyKey: adjustment.IdempotencyKey, CreatedAt: adjustment.CreatedAt.Time,
	}, nil
}
