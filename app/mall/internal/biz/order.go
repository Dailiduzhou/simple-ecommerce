package biz

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
)

type Order struct {
	ID          int64
	UserID      int64
	AddressID   int64
	TotalAmount int32
	Status      string
	IsCompleted bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// OutTradeNo 是商户订单号。统一支付 API 把它作为 order_no
	// 暴露给前端,用于按商户号反查订单。
	OutTradeNo string
}

type OrderRepo interface {
	CancelOrder(ctx context.Context, id int64) error
	CompleteOrder(ctx context.Context, id int64) error
	CreateOrder(ctx context.Context, userID int64, addressID int64, amount int32) (Order, error)
	GetOrder(ctx context.Context, id int64) (Order, error)
	// GetOrderByOrderNo 通过商户订单号(orders.out_trade_no)查询订单。
	// 找不到时返回 pgx.ErrNoRows。
	GetOrderByOrderNo(ctx context.Context, orderNo string) (Order, error)
	GetOrderByUser(ctx context.Context, id int64, userID int64) (Order, error)
	HasOngoingOrders(ctx context.Context, userID int64) (bool, error)
	ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]Order, error)
	ListOrdersByUser(ctx context.Context, userID int64, limit int32, offset int32) ([]Order, error)
	UpdateOrderStatus(ctx context.Context, id int64, status string) (Order, error)
}

type OrderUsecase interface {
	CreateOrder(ctx context.Context, req *CreateOrderReq) (*Order, error)
	GetOrder(ctx context.Context, id int64) (*Order, error)
	ListOrders(ctx context.Context, req *ListOrdersReq) ([]Order, error)
	CancelOrder(ctx context.Context, id int64) error
}

type orderUsecase struct {
	repo OrderRepo
	log  *log.Helper
}

func (uc *orderUsecase) CreateOrder(ctx context.Context, req *CreateOrderReq) (*Order, error) {
	return nil, nil
}

func (uc *orderUsecase) GetOrder(ctx context.Context, id int64) (*Order, error) {
	return nil, nil
}

func (uc *orderUsecase) ListOrders(ctx context.Context, req *ListOrdersReq) ([]Order, error) {
	return nil, nil
}

func (uc *orderUsecase) CancelOrder(ctx context.Context, id int64) error {
	return nil
}

func NewOrderUsecase(repo OrderRepo, logger log.Logger) OrderUsecase {
	return &orderUsecase{repo: repo, log: log.NewHelper(logger)}
}

type CreateOrderReq struct {
	UserID    int64
	AddressID int64
	Amount    int32
}

type ListOrdersReq struct {
	UserID   int64
	Limit    int32
	Offset   int32
}
