package biz

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
)

type StockAdjustmentInput struct {
	ProductID, ActorID     int64
	Delta                  int32
	Reason, IdempotencyKey string
}
type StockAdjustment struct {
	ID, ProductID, ActorID int64
	Delta, ResultingStock  int32
	Reason, IdempotencyKey string
	CreatedAt              time.Time
}

func (input StockAdjustmentInput) Validate() error {
	if input.ProductID <= 0 || input.ActorID <= 0 || input.Delta == 0 ||
		strings.TrimSpace(input.Reason) == "" || utf8.RuneCountInString(input.Reason) > 255 ||
		len(input.IdempotencyKey) < 8 || len(input.IdempotencyKey) > 64 || strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey {
		return errors.BadRequest("STOCK_ADJUSTMENT_INVALID", "positive product/actor IDs, nonzero delta, reason and 8-64 byte idempotency key are required")
	}
	return nil
}
func (uc *productUsecase) AdjustStock(ctx context.Context, input StockAdjustmentInput) (*StockAdjustment, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	return uc.repo.AdjustStock(ctx, input)
}
