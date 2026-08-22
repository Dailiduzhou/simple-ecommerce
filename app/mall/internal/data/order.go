package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"math"
	mrand "math/rand"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
)

var _ biz.OrderRepo = (*OrderRepo)(nil)

type OrderRepo struct {
	data *Data
	tx   biz.TxManager
	jobs biz.OrderMQRepo
	log  *log.Helper
}

func NewOrderRepo(data *Data, tx biz.TxManager, logger log.Logger) *OrderRepo {
	return NewOrderRepoWithJobs(data, tx, nil, logger)
}

func NewOrderRepoWithJobs(data *Data, tx biz.TxManager, jobs biz.OrderMQRepo, logger log.Logger) *OrderRepo {
	return &OrderRepo{data: data, tx: tx, jobs: jobs, log: log.NewHelper(logger)}
}

func (r *OrderRepo) CreateOrder(ctx context.Context, args biz.CreateOrderArgs) (biz.Order, error) {
	var result biz.Order
	created := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		if q == nil {
			return fmt.Errorf("missing transaction querier")
		}
		existing, err := q.GetOrderByUserIdempotency(ctx, db.GetOrderByUserIdempotencyParams{
			UserID: args.UserID, IdempotencyKey: args.IdempotencyKey,
		})
		if err == nil {
			if existing.RequestHash != args.RequestHash {
				return biz.ErrIdempotencyKeyConflict
			}
			if existing.Status == biz.OrderStatusCancelled {
				// Returning a cancelled order would silently swallow the new
				// intent behind a "successful" response; force a fresh key.
				return biz.ErrIdempotencyKeyReused
			}
			result = toBizOrder(existing)
			result.Items, err = r.loadItemsWithQuerier(ctx, q, existing.ID)
			return err
		}
		if !stderrors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := q.GetShippingAddress(ctx, db.GetShippingAddressParams{ID: args.AddressID, UserID: args.UserID}); err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrAddressNotFound
			}
			return err
		}

		type itemSnapshot struct {
			input   biz.OrderItemInput
			product db.Product
		}
		snapshots := make([]itemSnapshot, 0, len(args.Items))
		var total int64
		for _, item := range args.Items {
			product, err := q.GetProductForOrder(ctx, item.ProductID)
			if err != nil {
				if stderrors.Is(err, pgx.ErrNoRows) {
					return biz.ErrProductNotFound
				}
				return err
			}
			if product.Status != 1 || product.Stock < item.Quantity {
				return biz.ErrInsufficientStock
			}
			lineTotal, overflow := multiplyMoney(product.PriceMinor, int64(item.Quantity))
			if overflow || total > math.MaxInt64-lineTotal {
				return fmt.Errorf("order total overflow")
			}
			total += lineTotal
			snapshots = append(snapshots, itemSnapshot{input: item, product: product})
		}
		if total <= 0 {
			return biz.ErrOrderAmountInvalid
		}

		order, err := q.CreateOrder(ctx, db.CreateOrderParams{
			UserID: args.UserID, AddressID: args.AddressID, TotalAmountMinor: total,
			Currency: args.Currency, OutTradeNo: args.OutTradeNo, IdempotencyKey: args.IdempotencyKey,
			RequestHash: args.RequestHash, ExpiresAt: pgtype.Timestamptz{Time: args.ExpiresAt, Valid: true},
		})
		if err != nil {
			return err
		}
		items := make([]biz.OrderItem, 0, len(snapshots))
		for _, snapshot := range snapshots {
			if _, err := q.DecrProductStock(ctx, db.DecrProductStockParams{ID: snapshot.input.ProductID, Stock: snapshot.input.Quantity}); err != nil {
				if stderrors.Is(err, pgx.ErrNoRows) {
					return biz.ErrInsufficientStock
				}
				return err
			}
			if _, err := q.CreateOrderItem(ctx, db.CreateOrderItemParams{
				OrderID: order.ID, ProductID: snapshot.input.ProductID, Quantity: snapshot.input.Quantity,
				UnitPriceMinor: snapshot.product.PriceMinor, ProductNameSnapshot: snapshot.product.Name,
				CoverImageSnapshot: snapshot.product.CoverImage,
			}); err != nil {
				return err
			}
			items = append(items, toBizOrderItem(snapshot.input.ProductID, snapshot.product.CategoryID, snapshot.input.Quantity, snapshot.product.PriceMinor, snapshot.product.Name, snapshot.product.CoverImage))
		}
		result = toBizOrder(order)
		result.Items = items
		if r.jobs == nil {
			return fmt.Errorf("order mq is not configured")
		}
		if _, err := r.jobs.EnqueueExpireOrderTx(ctx, biz.ExpireOrderArgs{OrderID: order.ID}, args.ExpiresAt); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if stderrors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "fk_order_address" {
			return biz.Order{}, biz.ErrAddressNotFound
		}
		if stderrors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_orders_user_idempotency" {
			existing, loadErr := r.data.q.GetOrderByUserIdempotency(ctx, db.GetOrderByUserIdempotencyParams{
				UserID: args.UserID, IdempotencyKey: args.IdempotencyKey,
			})
			if loadErr != nil {
				// Under read committed the winning transaction has committed
				// before the unique violation surfaces, so the row must exist;
				// a miss would mean the key was concurrently freed.
				if stderrors.Is(loadErr, pgx.ErrNoRows) {
					return biz.Order{}, biz.ErrIdempotencyKeyConflict
				}
				return biz.Order{}, loadErr
			}
			if existing.RequestHash != args.RequestHash {
				return biz.Order{}, biz.ErrIdempotencyKeyConflict
			}
			if existing.Status == biz.OrderStatusCancelled {
				return biz.Order{}, biz.ErrIdempotencyKeyReused
			}
			result = toBizOrder(existing)
			result.Items, loadErr = r.loadItems(ctx, existing.ID)
			if loadErr != nil {
				return biz.Order{}, loadErr
			}
		} else {
			return biz.Order{}, err
		}
	}
	if created {
		r.invalidateUserLists(ctx, result.UserID)
		for _, item := range result.Items {
			r.deleteKey(ctx, redisKey("product", item.ProductID))
			bumpCacheGeneration(ctx, r.data.rdb, r.log, "product:list:gen")
			bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", "category", item.CategoryID, "gen"))
		}
	}
	r.setCache(ctx, redisKey("order", result.ID), &result)
	r.setCache(ctx, redisKey("order", "user", result.ID, result.UserID), &result)
	if result.OutTradeNo != "" {
		r.setCache(ctx, redisKey("order", "no", result.OutTradeNo), &result)
	}
	return result, nil
}

func multiplyMoney(amount, quantity int64) (int64, bool) {
	if amount < 0 || quantity < 0 || (quantity != 0 && amount > math.MaxInt64/quantity) {
		return 0, true
	}
	return amount * quantity, false
}

func (r *OrderRepo) GetOrder(ctx context.Context, id int64) (biz.Order, error) {
	return r.getOrder(ctx, redisKey("order", id), func() (db.Order, error) {
		return querierFromContext(ctx, r.data.q).GetOrder(ctx, id)
	})
}

func (r *OrderRepo) GetOrderByOrderNo(ctx context.Context, orderNo string) (biz.Order, error) {
	return r.getOrder(ctx, redisKey("order", "no", orderNo), func() (db.Order, error) {
		return querierFromContext(ctx, r.data.q).GetOrderByOrderNo(ctx, orderNo)
	})
}

func (r *OrderRepo) GetOrderByUser(ctx context.Context, id, userID int64) (biz.Order, error) {
	return r.getOrder(ctx, redisKey("order", "user", id, userID), func() (db.Order, error) {
		return querierFromContext(ctx, r.data.q).GetOrderByUser(ctx, db.GetOrderByUserParams{ID: id, UserID: userID})
	})
}

func (r *OrderRepo) getOrder(ctx context.Context, cacheKey string, load func() (db.Order, error)) (biz.Order, error) {
	if cached, err := r.getCache(ctx, cacheKey); err == nil {
		return *cached, nil
	} else if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorw("msg", "read order cache failed", "key", cacheKey, "error", err)
	}
	value, err, _ := r.data.sg.Do("sf:"+cacheKey, func() (any, error) {
		if cached, err := r.getCache(ctx, cacheKey); err == nil {
			return *cached, nil
		}
		row, err := load()
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.Order{}, biz.ErrOrderNotFound
			}
			return biz.Order{}, err
		}
		order := toBizOrder(row)
		items, err := r.loadItems(ctx, row.ID)
		if err != nil {
			return biz.Order{}, err
		}
		order.Items = items
		r.setCache(ctx, cacheKey, &order)
		return order, nil
	})
	if err != nil {
		return biz.Order{}, err
	}
	return value.(biz.Order), nil
}

func (r *OrderRepo) HasOngoingOrders(ctx context.Context, userID int64) (bool, error) {
	return querierFromContext(ctx, r.data.q).HasOngoingOrders(ctx, userID)
}

func (r *OrderRepo) ListOngoingOrdersByUser(ctx context.Context, userID int64) ([]biz.Order, error) {
	genKey := redisKey("order", "user", "ongoing", userID, "gen")
	generation := cacheGeneration(ctx, r.data.rdb, r.log, genKey)
	cacheKey := redisKey("order", "user", "ongoing", userID, generation)
	return r.listOrders(ctx, cacheKey, func() ([]db.Order, error) {
		return querierFromContext(ctx, r.data.q).ListOngoingOrdersByUser(ctx, userID)
	})
}

func (r *OrderRepo) ListOrdersByUser(ctx context.Context, userID int64, limit, offset int32) ([]biz.Order, error) {
	genKey := redisKey("order", "user", userID, "gen")
	generation := cacheGeneration(ctx, r.data.rdb, r.log, genKey)
	cacheKey := redisKey("order", "user", userID, generation, limit, offset)
	return r.listOrders(ctx, cacheKey, func() ([]db.Order, error) {
		return querierFromContext(ctx, r.data.q).ListOrdersByUser(ctx, db.ListOrdersByUserParams{UserID: userID, Limit: limit, Offset: offset})
	})
}

func (r *OrderRepo) listOrders(ctx context.Context, cacheKey string, load func() ([]db.Order, error)) ([]biz.Order, error) {
	if cached, err := r.getListCache(ctx, cacheKey); err == nil {
		return cached, nil
	}
	value, err, _ := r.data.sg.Do("sf:"+cacheKey, func() (any, error) {
		if cached, err := r.getListCache(ctx, cacheKey); err == nil {
			return cached, nil
		}
		rows, err := load()
		if err != nil {
			return nil, err
		}
		orders := make([]biz.Order, len(rows))
		for i, row := range rows {
			orders[i] = toBizOrder(row)
			orders[i].Items, err = r.loadItems(ctx, row.ID)
			if err != nil {
				return nil, err
			}
		}
		r.setListCache(ctx, cacheKey, orders)
		return orders, nil
	})
	if err != nil {
		return nil, err
	}
	return value.([]biz.Order), nil
}

func (r *OrderRepo) CountOrdersByUser(ctx context.Context, userID int64) (int64, error) {
	return querierFromContext(ctx, r.data.q).CountOrdersByUser(ctx, userID)
}

func (r *OrderRepo) CancelOrderByUser(ctx context.Context, id, userID int64) error {
	var order db.Order
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		var err error
		order, err = q.GetOrderByUserForUpdate(ctx, db.GetOrderByUserForUpdateParams{ID: id, UserID: userID})
		if err != nil {
			if stderrors.Is(err, pgx.ErrNoRows) {
				return biz.ErrOrderNotFound
			}
			return err
		}
		if order.Status == biz.OrderStatusCancelled {
			return nil
		}
		if order.Status != biz.OrderStatusPendingPayment {
			return biz.ErrOrderCannotCancel
		}
		payments, err := q.ListPaymentsByOrderForUpdate(ctx, id)
		if err != nil {
			return err
		}
		hasActive := false
		for _, payment := range payments {
			if payment.ReconciliationStatus == biz.ReconciliationStatusRequired {
				return biz.ErrPaymentReconciliationRequired
			}
			switch payment.Status {
			case biz.PaymentStatusSuccess, biz.PaymentStatusRefunded:
				return biz.ErrOrderAlreadyPaid
			case biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending:
				hasActive = true
			}
		}
		if hasActive {
			return biz.ErrOrderHasActivePayment
		}
		if _, err := q.MarkOrderCancelling(ctx, id); err != nil {
			return err
		}
		if err := q.RestoreOrderItemStock(ctx, id); err != nil {
			return err
		}
		_, err = q.MarkOrderCancelled(ctx, id)
		return err
	})
	if err != nil {
		return err
	}
	r.invalidateOrder(ctx, toBizOrder(order))
	invalidateProductCachesForOrder(ctx, r.data, r.log, id)
	return nil
}

func (r *OrderRepo) loadItems(ctx context.Context, orderID int64) ([]biz.OrderItem, error) {
	return r.loadItemsWithQuerier(ctx, querierFromContext(ctx, r.data.q), orderID)
}

func (r *OrderRepo) loadItemsWithQuerier(ctx context.Context, q db.Querier, orderID int64) ([]biz.OrderItem, error) {
	rows, err := q.ListOrderItems(ctx, orderID)
	if err != nil {
		return nil, err
	}
	items := make([]biz.OrderItem, len(rows))
	for i, row := range rows {
		items[i] = toBizOrderItem(row.ProductID, 0, row.Quantity, row.UnitPriceMinor, row.ProductNameSnapshot, row.CoverImageSnapshot)
	}
	return items, nil
}

func toBizOrderItem(productID, categoryID int64, quantity int32, price int64, name string, cover []byte) biz.OrderItem {
	var media []biz.MediaInfo
	_ = json.Unmarshal(cover, &media)
	coverURL := ""
	if len(media) > 0 {
		coverURL = media[0].OssURL
	}
	return biz.OrderItem{ProductID: productID, CategoryID: categoryID, ProductName: name, CoverImage: coverURL, Quantity: quantity, UnitPrice: price}
}

func (r *OrderRepo) invalidateOrder(ctx context.Context, order biz.Order) {
	r.deleteKey(ctx, redisKey("order", order.ID))
	r.deleteKey(ctx, redisKey("order", "user", order.ID, order.UserID))
	if order.OutTradeNo != "" {
		r.deleteKey(ctx, redisKey("order", "no", order.OutTradeNo))
	}
	r.invalidateUserLists(ctx, order.UserID)
}

func (r *OrderRepo) invalidateUserLists(ctx context.Context, userID int64) {
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("order", "user", userID, "gen"))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("order", "user", "ongoing", userID, "gen"))
}

func (r *OrderRepo) getCache(ctx context.Context, key string) (*biz.Order, error) {
	value, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var order biz.Order
	if err := json.Unmarshal(value, &order); err != nil {
		return nil, err
	}
	return &order, nil
}

func (r *OrderRepo) getListCache(ctx context.Context, key string) ([]biz.Order, error) {
	value, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var orders []biz.Order
	if err := json.Unmarshal(value, &orders); err != nil {
		return nil, err
	}
	return orders, nil
}

func (r *OrderRepo) setCache(ctx context.Context, key string, order *biz.Order) {
	afterCommit(ctx, func() {
		value, err := json.Marshal(order)
		if err != nil {
			r.log.WithContext(ctx).Errorw("msg", "marshal order cache failed", "error", err)
			return
		}
		if err := r.data.rdb.Set(ctx, key, value, 10*time.Minute+time.Duration(mrand.Intn(600))*time.Second).Err(); err != nil {
			r.log.WithContext(ctx).Errorw("msg", "write order cache failed", "key", key, "error", err)
		}
	})
}

func (r *OrderRepo) setListCache(ctx context.Context, key string, orders []biz.Order) {
	afterCommit(ctx, func() {
		value, err := json.Marshal(orders)
		if err != nil {
			return
		}
		if err := r.data.rdb.Set(ctx, key, value, 10*time.Minute+time.Duration(mrand.Intn(600))*time.Second).Err(); err != nil {
			r.log.WithContext(ctx).Errorw("msg", "write order list cache failed", "key", key, "error", err)
		}
	})
}

func (r *OrderRepo) deleteKey(ctx context.Context, key string) {
	afterCommit(ctx, func() {
		if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
			r.log.WithContext(ctx).Errorw("msg", "delete cache failed", "key", key, "error", err)
		}
	})
}

func toBizOrder(row db.Order) biz.Order {
	return biz.Order{
		ID: row.ID, UserID: row.UserID, AddressID: row.AddressID,
		TotalAmount: row.TotalAmountMinor, Currency: row.Currency, Status: row.Status,
		IsCompleted: row.IsCompleted, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		OutTradeNo: row.OutTradeNo, IdempotencyKey: row.IdempotencyKey, RequestHash: row.RequestHash,
		ExpiresAt: row.ExpiresAt.Time,
	}
}
