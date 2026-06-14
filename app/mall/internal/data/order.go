package data

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/jackc/pgx/v5/pgtype"
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
	return querierFromContext(ctx, r.q).CancelOrder(ctx, id)
}

func (r *OrderRepo) CompleteOrder(ctx context.Context, id int64) error {
	return querierFromContext(ctx, r.q).CompleteOrder(ctx, id)
}

func (r *OrderRepo) CreateOrder(ctx context.Context, userID int64, addressID int64, amount int32) (biz.Order, error) {
	order, err := querierFromContext(ctx, r.q).CreateOrder(ctx, db.CreateOrderParams{
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
	order, err := querierFromContext(ctx, r.q).GetOrder(ctx, id)
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

// GetOrderByOrderNo 通过 orders.out_trade_no 查询订单。
// 统一支付 API 的入口:order_no -> order。
func (r *OrderRepo) GetOrderByOrderNo(ctx context.Context, orderNo string) (biz.Order, error) {
	order, err := querierFromContext(ctx, r.q).GetOrderByOrderNo(ctx, pgtype.Text{String: orderNo, Valid: true})
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func (r *OrderRepo) GetOrderByUser(ctx context.Context, id int64, userID int64) (biz.Order, error) {
	order, err := querierFromContext(ctx, r.q).GetOrderByUser(ctx, db.GetOrderByUserParams{
		ID:     id,
		UserID: userID,
	})
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func (r *OrderRepo) HasOngoingOrders(ctx context.Context, userID int64) (bool, error) {
	return querierFromContext(ctx, r.q).HasOngoingOrders(ctx, userID)
}

func (r *OrderRepo) ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]biz.Order, error) {
	orders, err := querierFromContext(ctx, r.q).ListOngoingOrdersByUser(ctx, userID)
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
	orders, err := querierFromContext(ctx, r.q).ListOrdersByUser(ctx, db.ListOrdersByUserParams{
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
	order, err := querierFromContext(ctx, r.q).UpdateOrderStatus(ctx, db.UpdateOrderStatusParams{
		ID:     id,
		Status: status,
	})
	if err != nil {
		return biz.Order{}, err
	}
	return toBizOrder(order), nil
}

func toBizOrder(o db.Order) biz.Order {
	// OutTradeNo 列允许为 NULL;只在 Valid 时回填到 biz 层,避免空字符串污染业务判断。
	var outTradeNo string
	if o.OutTradeNo.Valid {
		outTradeNo = o.OutTradeNo.String
	}
	return biz.Order{
		ID:          o.ID,
		UserID:      o.UserID,
		AddressID:   o.AddressID,
		TotalAmount: o.TotalAmount,
		Status:      o.Status,
		IsCompleted: o.IsCompleted,
		CreatedAt:   o.CreatedAt.Time,
		UpdatedAt:   o.UpdatedAt.Time,
		OutTradeNo:  outTradeNo,
	}
}
