package biz

import (
	"context"
	"math"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
)

const (
	OrderStatusCreating       = "creating"
	OrderStatusPendingPayment = "pending_payment"
	OrderStatusPaid           = "paid"
	OrderStatusShipped        = "shipped"
	OrderStatusCompleted      = "completed"
	OrderStatusCancelling     = "cancelling"
	OrderStatusCancelled      = "cancelled"
	DefaultCurrency           = "CNY"
)

var (
	ErrOrderNotFound         = errors.NotFound("ORDER_NOT_FOUND", "order not found")
	ErrAddressNotFound       = errors.NotFound("ADDRESS_NOT_FOUND", "shipping address not found")
	ErrProductNotFound       = errors.NotFound("PRODUCT_NOT_FOUND", "product not found")
	ErrInsufficientStock     = errors.Conflict("INSUFFICIENT_STOCK", "insufficient product stock")
	ErrOrderCannotCancel     = errors.Conflict("ORDER_CANNOT_CANCEL", "order cannot be cancelled in its current state")
	ErrOrderHasActivePayment = errors.Conflict("ORDER_HAS_ACTIVE_PAYMENT", "close the active payment before cancelling the order")
	ErrOrderAlreadyPaid      = errors.Conflict("ORDER_ALREADY_PAID", "paid order must use the refund flow")
	ErrOrderInputInvalid     = errors.BadRequest("ORDER_INPUT_INVALID", "order items are invalid")
)

type Order struct {
	ID          int64
	UserID      int64
	AddressID   int64
	TotalAmount int64 // minor units
	Currency    string
	Status      string
	IsCompleted bool
	Items       []OrderItem
	CreatedAt   time.Time
	UpdatedAt   time.Time
	OutTradeNo  string
}

type OrderItem struct {
	ProductID   int64
	CategoryID  int64
	ProductName string
	CoverImage  string
	Quantity    int32
	UnitPrice   int64 // minor units
}

type OrderItemInput struct {
	ProductID int64
	Quantity  int32
}

type CreateOrderArgs struct {
	UserID     int64
	AddressID  int64
	OutTradeNo string
	Currency   string
	Items      []OrderItemInput
}

type OrderRepo interface {
	CreateOrder(ctx context.Context, args CreateOrderArgs) (Order, error)
	GetOrder(ctx context.Context, id int64) (Order, error)
	GetOrderByOrderNo(ctx context.Context, orderNo string) (Order, error)
	GetOrderByUser(ctx context.Context, id, userID int64) (Order, error)
	HasOngoingOrders(ctx context.Context, userID int64) (bool, error)
	ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]Order, error)
	ListOrdersByUser(ctx context.Context, userID int64, limit, offset int32) ([]Order, error)
	CountOrdersByUser(ctx context.Context, userID int64) (int64, error)
	CancelOrderByUser(ctx context.Context, id, userID int64) error
}

type OrderUsecase interface {
	CreateOrder(ctx context.Context, req *CreateOrderReq) (*Order, error)
	GetOrder(ctx context.Context, id, userID int64) (*Order, error)
	ListOrders(ctx context.Context, req *ListOrdersReq) ([]Order, int64, error)
	CancelOrder(ctx context.Context, id, userID int64) error
}

type orderUsecase struct {
	repo  OrderRepo
	idGen IDGenerator
	log   *log.Helper
}

func NewOrderUsecase(repo OrderRepo, idGen IDGenerator, logger log.Logger) OrderUsecase {
	return &orderUsecase{repo: repo, idGen: idGen, log: log.NewHelper(logger)}
}

type CreateOrderReq struct {
	UserID    int64
	AddressID int64
	Items     []OrderItemInput
}

type ListOrdersReq struct {
	UserID  int64
	Ongoing bool
	Limit   int32
	Offset  int32
}

func (uc *orderUsecase) CreateOrder(ctx context.Context, req *CreateOrderReq) (*Order, error) {
	if req == nil || req.UserID <= 0 || req.AddressID <= 0 || len(req.Items) == 0 {
		return nil, ErrOrderInputInvalid
	}
	seen := make(map[int64]struct{}, len(req.Items))
	for _, item := range req.Items {
		if item.ProductID <= 0 || item.Quantity <= 0 {
			return nil, ErrOrderInputInvalid
		}
		if _, duplicate := seen[item.ProductID]; duplicate {
			return nil, errors.BadRequest("DUPLICATE_ORDER_ITEM", "duplicate product in order items")
		}
		seen[item.ProductID] = struct{}{}
	}
	if uc.idGen == nil {
		return nil, errors.InternalServer("ORDER_ID_GENERATOR_MISSING", "order number generator is unavailable")
	}
	orderNo := uc.idGen.GenerateString()
	order, err := uc.repo.CreateOrder(ctx, CreateOrderArgs{
		UserID: req.UserID, AddressID: req.AddressID, OutTradeNo: orderNo,
		Currency: DefaultCurrency, Items: req.Items,
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func (uc *orderUsecase) GetOrder(ctx context.Context, id, userID int64) (*Order, error) {
	if id <= 0 || userID <= 0 {
		return nil, ErrOrderNotFound
	}
	order, err := uc.repo.GetOrderByUser(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func (uc *orderUsecase) ListOrders(ctx context.Context, req *ListOrdersReq) ([]Order, int64, error) {
	if req == nil || req.UserID <= 0 {
		return nil, 0, ErrOrderInputInvalid
	}
	if req.Ongoing {
		orders, err := uc.repo.ListOngoingOrdersByUser(ctx, req.UserID)
		return orders, int64(len(orders)), err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset := req.Offset
	if offset < 0 || int64(offset) > math.MaxInt32 {
		return nil, 0, ErrOrderInputInvalid
	}
	orders, err := uc.repo.ListOrdersByUser(ctx, req.UserID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := uc.repo.CountOrdersByUser(ctx, req.UserID)
	if err != nil {
		return nil, 0, err
	}
	return orders, total, nil
}

func (uc *orderUsecase) CancelOrder(ctx context.Context, id, userID int64) error {
	if id <= 0 || userID <= 0 {
		return ErrOrderNotFound
	}
	return uc.repo.CancelOrderByUser(ctx, id, userID)
}
