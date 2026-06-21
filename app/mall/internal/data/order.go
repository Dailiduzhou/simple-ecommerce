package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	mrand "math/rand"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
)

var _ biz.OrderRepo = (*OrderRepo)(nil)

type OrderRepo struct {
	data *Data
	log  *log.Helper
}

func NewOrderRepo(data *Data, logger log.Logger) *OrderRepo {
	return &OrderRepo{data: data, log: log.NewHelper(logger)}
}

func (r *OrderRepo) CreateOrder(ctx context.Context, userID int64, addressID int64, amount int32) (biz.Order, error) {
	order, err := querierFromContext(ctx, r.data.q).CreateOrder(ctx, db.CreateOrderParams{
		UserID:      userID,
		AddressID:   addressID,
		TotalAmount: amount,
	})
	if err != nil {
		return biz.Order{}, err
	}
	bizOrder := toBizOrder(order)
	r.setCache(ctx, fmt.Sprintf("order:%d", bizOrder.ID), &bizOrder)
	r.deleteListCache(ctx, fmt.Sprintf("order:user:ongoing:%d", userID))
	return bizOrder, nil
}

func (r *OrderRepo) GetOrder(ctx context.Context, id int64) (biz.Order, error) {
	cacheKey := fmt.Sprintf("order:%d", id)

	o, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return *o, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get order cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:order:%d", id)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		o, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return *o, nil
		}
		order, err := querierFromContext(ctx, r.data.q).GetOrder(ctx, id)
		if err != nil {
			return biz.Order{}, err
		}
		bizOrder := toBizOrder(order)
		r.setCache(ctx, cacheKey, &bizOrder)
		if bizOrder.OutTradeNo != "" {
			r.setCache(ctx, fmt.Sprintf("order:no:%s", bizOrder.OutTradeNo), &bizOrder)
		}
		return bizOrder, nil
	})

	if err != nil {
		return biz.Order{}, err
	}
	return val.(biz.Order), nil
}

// GetOrderByOrderNo 通过 orders.out_trade_no 查询订单。
// 统一支付 API 的入口:order_no -> order。
func (r *OrderRepo) GetOrderByOrderNo(ctx context.Context, orderNo string) (biz.Order, error) {
	cacheKey := fmt.Sprintf("order:no:%s", orderNo)

	o, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return *o, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get order by order_no cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:order:no:%s", orderNo)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		o, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return *o, nil
		}
		order, err := querierFromContext(ctx, r.data.q).GetOrderByOrderNo(ctx, pgtype.Text{String: orderNo, Valid: true})
		if err != nil {
			return biz.Order{}, err
		}
		bizOrder := toBizOrder(order)
		r.setCache(ctx, cacheKey, &bizOrder)
		r.setCache(ctx, fmt.Sprintf("order:%d", bizOrder.ID), &bizOrder)
		return bizOrder, nil
	})

	if err != nil {
		return biz.Order{}, err
	}
	return val.(biz.Order), nil
}

func (r *OrderRepo) GetOrderByUser(ctx context.Context, id int64, userID int64) (biz.Order, error) {
	cacheKey := fmt.Sprintf("order:user:%d:%d", id, userID)

	o, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return *o, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get order by user cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:order:user:%d:%d", id, userID)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		o, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return *o, nil
		}
		order, err := querierFromContext(ctx, r.data.q).GetOrderByUser(ctx, db.GetOrderByUserParams{
			ID:     id,
			UserID: userID,
		})
		if err != nil {
			return biz.Order{}, err
		}
		bizOrder := toBizOrder(order)
		r.setCache(ctx, cacheKey, &bizOrder)
		return bizOrder, nil
	})

	if err != nil {
		return biz.Order{}, err
	}
	return val.(biz.Order), nil
}

func (r *OrderRepo) HasOngoingOrders(ctx context.Context, userID int64) (bool, error) {
	return querierFromContext(ctx, r.data.q).HasOngoingOrders(ctx, userID)
}

func (r *OrderRepo) ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]biz.Order, error) {
	cacheKey := fmt.Sprintf("order:user:ongoing:%d", userID)

	orders, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return orders, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get ongoing orders cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:order:user:ongoing:%d", userID)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		orders, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return orders, nil
		}
		dbOrders, err := querierFromContext(ctx, r.data.q).ListOngoingOrdersByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		result := make([]biz.Order, len(dbOrders))
		for i, o := range dbOrders {
			result[i] = toBizOrder(o)
		}
		r.setListCache(ctx, cacheKey, result)
		return result, nil
	})

	if err != nil {
		return nil, err
	}
	return val.([]biz.Order), nil
}

func (r *OrderRepo) ListOrdersByUser(ctx context.Context, userID int64, limit int32, offset int32) ([]biz.Order, error) {
	cacheKey := fmt.Sprintf("order:user:%d:%d:%d", userID, limit, offset)

	orders, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return orders, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get orders by user cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:order:user:%d:%d:%d", userID, limit, offset)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		orders, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return orders, nil
		}
		dbOrders, err := querierFromContext(ctx, r.data.q).ListOrdersByUser(ctx, db.ListOrdersByUserParams{
			UserID: userID,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		result := make([]biz.Order, len(dbOrders))
		for i, o := range dbOrders {
			result[i] = toBizOrder(o)
		}
		r.setListCache(ctx, cacheKey, result)
		return result, nil
	})

	if err != nil {
		return nil, err
	}
	return val.([]biz.Order), nil
}

func (r *OrderRepo) UpdateOrderStatus(ctx context.Context, id int64, status string) (biz.Order, error) {
	order, err := querierFromContext(ctx, r.data.q).UpdateOrderStatus(ctx, db.UpdateOrderStatusParams{
		ID:     id,
		Status: status,
	})
	if err != nil {
		return biz.Order{}, err
	}
	bizOrder := toBizOrder(order)
	r.deleteCache(ctx, fmt.Sprintf("order:%d", id))
	if bizOrder.OutTradeNo != "" {
		r.deleteCache(ctx, fmt.Sprintf("order:no:%s", bizOrder.OutTradeNo))
	}
	r.deleteCache(ctx, fmt.Sprintf("order:user:%d:%d", id, bizOrder.UserID))
	r.deleteListCache(ctx, fmt.Sprintf("order:user:ongoing:%d", bizOrder.UserID))
	r.setCache(ctx, fmt.Sprintf("order:%d", id), &bizOrder)
	if bizOrder.OutTradeNo != "" {
		r.setCache(ctx, fmt.Sprintf("order:no:%s", bizOrder.OutTradeNo), &bizOrder)
	}
	r.setCache(ctx, fmt.Sprintf("order:user:%d:%d", id, bizOrder.UserID), &bizOrder)
	return bizOrder, nil
}

func (r *OrderRepo) CancelOrder(ctx context.Context, id int64) error {
	existing, err := r.GetOrder(ctx, id)
	if err != nil {
		return err
	}
	if err := querierFromContext(ctx, r.data.q).CancelOrder(ctx, id); err != nil {
		return err
	}
	r.deleteCache(ctx, fmt.Sprintf("order:%d", id))
	if existing.OutTradeNo != "" {
		r.deleteCache(ctx, fmt.Sprintf("order:no:%s", existing.OutTradeNo))
	}
	r.deleteCache(ctx, fmt.Sprintf("order:user:%d:%d", id, existing.UserID))
	r.deleteListCache(ctx, fmt.Sprintf("order:user:ongoing:%d", existing.UserID))
	return nil
}

func (r *OrderRepo) CompleteOrder(ctx context.Context, id int64) error {
	existing, err := r.GetOrder(ctx, id)
	if err != nil {
		return err
	}
	if err := querierFromContext(ctx, r.data.q).CompleteOrder(ctx, id); err != nil {
		return err
	}
	r.deleteCache(ctx, fmt.Sprintf("order:%d", id))
	if existing.OutTradeNo != "" {
		r.deleteCache(ctx, fmt.Sprintf("order:no:%s", existing.OutTradeNo))
	}
	r.deleteCache(ctx, fmt.Sprintf("order:user:%d:%d", id, existing.UserID))
	r.deleteListCache(ctx, fmt.Sprintf("order:user:ongoing:%d", existing.UserID))
	return nil
}

func (r *OrderRepo) getCache(ctx context.Context, key string) (*biz.Order, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var o biz.Order
	if err := json.Unmarshal(val, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *OrderRepo) getListCache(ctx context.Context, key string) ([]biz.Order, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var orders []biz.Order
	if err := json.Unmarshal(val, &orders); err != nil {
		return nil, err
	}
	return orders, nil
}

func (r *OrderRepo) setCache(ctx context.Context, key string, o *biz.Order) {
	afterCommit(ctx, func() {
		data, err := json.Marshal(o)
		if err != nil {
			r.log.WithContext(ctx).Errorf("marshal order cache: %v", err)
			return
		}
		jitter := time.Duration(mrand.Intn(10)) * time.Minute
		exp := jitter + 10*time.Minute
		r.data.rdb.Set(ctx, key, data, exp)
	})
}

func (r *OrderRepo) setListCache(ctx context.Context, key string, orders []biz.Order) {
	afterCommit(ctx, func() {
		data, err := json.Marshal(orders)
		if err != nil {
			r.log.WithContext(ctx).Errorf("marshal order list cache: %v", err)
			return
		}
		jitter := time.Duration(mrand.Intn(10)) * time.Minute
		exp := jitter + 10*time.Minute
		r.data.rdb.Set(ctx, key, data, exp)
	})
}

func (r *OrderRepo) deleteCache(ctx context.Context, key string) {
	afterCommit(ctx, func() {
		if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
			r.log.WithContext(ctx).Errorf("delete cache %s", key)
		}
	})
}

func (r *OrderRepo) deleteListCache(ctx context.Context, key string) {
	afterCommit(ctx, func() {
		if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
			r.log.WithContext(ctx).Errorf("delete list cache %s", key)
		}
	})
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
