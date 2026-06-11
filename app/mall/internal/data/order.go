package data

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ biz.OrderRepo = (*OrderRepo)(nil)

type OrderRepo struct {
	pool *pgxpool.Pool
	q    db.Querier
}

func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo {
	return &OrderRepo{pool: pool, q: db.New(pool)}
}

func (r *OrderRepo) CancelOrder(ctx context.Context, id int64) error {
	return r.q.CancelOrder(ctx, id)
}

func (r *OrderRepo) CompleteOrder(ctx context.Context, id int64) error {
	return r.q.CompleteOrder(ctx, id)
}

func (r *OrderRepo) CreateOrder(ctx context.Context, userID int64, addressID int64, amount int32) (biz.Order, error) {
	order, err := r.q.CreateOrder(ctx, db.CreateOrderParams{
		UserID:      userID,
		AddressID:   addressID,
		TotalAmount: amount,
	})
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func (r *OrderRepo) GetOrder(ctx context.Context, id int64) (biz.Order, error) {
	order, err := r.q.GetOrder(ctx, id)
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func (r *OrderRepo) GetOrderByUser(ctx context.Context, id int64, userID int64) (biz.Order, error) {
	order, err := r.q.GetOrderByUser(ctx, db.GetOrderByUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func (r *OrderRepo) HasOngoingOrders(ctx context.Context, userID int64) (bool, error) {
	return r.q.HasOngoingOrders(ctx, userID)
}

func (r *OrderRepo) ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]biz.Order, error) {
	orders, err := r.q.ListOngoingOrdersByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	result := make([]biz.Order, len(orders))
	for i, o := range orders {
		result[i] = toBizOrder(o)
	}
	return result, nil
}

func (r *OrderRepo) ListOrdersByUser(ctx context.Context, userID int64, limit int32, offset int32) ([]biz.Order, error) {
	orders, err := r.q.ListOrdersByUser(ctx, db.ListOrdersByUserParams{
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	result := make([]biz.Order, len(orders))
	for i, o := range orders {
		result[i] = toBizOrder(o)
	}
	return result, nil
}

func (r *OrderRepo) UpdateOrderStatus(ctx context.Context, id int64, status string) (biz.Order, error) {
	order, err := r.q.UpdateOrderStatus(ctx, db.UpdateOrderStatusParams{
		ID:     id,
		Status: status,
	})
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func toBizOrder(o db.Order) biz.Order {
	return biz.Order{
		ID:          o.ID,
		UserID:      o.UserID,
		AddressID:   o.AddressID,
		TotalAmount: o.TotalAmount,
		Status:      o.Status,
		IsCompleted: o.IsCompleted,
		CreatedAt:   o.CreatedAt.Time,
		UpdatedAt:   o.UpdatedAt.Time,
	}
}

