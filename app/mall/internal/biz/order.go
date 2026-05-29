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
}

type OrderRepo interface {
	CancelOrder(ctx context.Context, id int64) error
	CompleteOrder(ctx context.Context, id int64) error
	CreateOrder(ctx context.Context, userID int64, addressID int64, amount int32) (Order, error)
	GetOrder(ctx context.Context, id int64) (Order, error)
	GetOrderByUser(ctx context.Context, id int64, userID int64) (Order, error)
	HasOngoingOrders(ctx context.Context, userID int64) (bool, error)
	ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]Order, error)
	ListOrdersByUser(ctx context.Context, userID int64, limit int32, offset int32) ([]Order, error)
	UpdateOrderStatus(ctx context.Context, id int64, status string) (Order, error)
}

type OrderUsecase struct {
	repo OrderRepo
	log  *log.Helper
}
