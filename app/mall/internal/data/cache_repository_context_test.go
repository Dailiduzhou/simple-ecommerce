package data

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type repositoryCacheFill struct {
	ctx  context.Context
	key  string
	err  error
	args []any
}

// Repository cancellation tests use real JSON cache helpers and mock SQL, but
// intercept Redis commands to avoid mixing synctest with real socket I/O.
func newRepositoryContextData(t *testing.T, q db.Querier) (*Data, <-chan repositoryCacheFill) {
	t.Helper()
	fills := make(chan repositoryCacheFill, 1)
	d := newCacheHookData(t, func(ctx context.Context, cmd redis.Cmder) error {
		switch cmd.Name() {
		case "get":
			return redis.Nil
		case "set":
			fills <- repositoryCacheFill{ctx: ctx, key: cmd.Args()[1].(string), err: ctx.Err(), args: cmd.Args()}
			return ctx.Err()
		default:
			return fmt.Errorf("unexpected Redis command: %s", cmd.Name())
		}
	})
	d.q = q
	return d, fills
}

func TestPaymentRepo_SharedLoadUsesWorkContext(t *testing.T) {
	for _, lookup := range []string{"id", "latest_by_order", "active_by_method", "out_trade_no"} {
		t.Run(lookup, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := mockdb.NewMockQuerier(gomock.NewController(t))
				d, fills := newRepositoryContextData(t, q)
				r := NewPaymentRepo(d, nil, log.NewStdLogger(io.Discard))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started := make(chan context.Context, 1)
				release := make(chan struct{})
				load := func(workCtx context.Context) (db.Payment, error) {
					started <- workCtx
					<-release
					return db.Payment{ID: 42, OrderID: 7, UserID: 9, OutTradeNo: "trade-42", PayChannel: "alipay"}, workCtx.Err()
				}
				var get func(context.Context) (*biz.PaymentDO, error)
				var key string
				switch lookup {
				case "id":
					q.EXPECT().GetPayment(gomock.Any(), int64(42)).DoAndReturn(func(ctx context.Context, _ int64) (db.Payment, error) { return load(ctx) })
					get = func(ctx context.Context) (*biz.PaymentDO, error) { return r.GetPaymentByUser(ctx, 42, 9) }
					key = "payment:42:g:0"
				case "latest_by_order":
					q.EXPECT().GetLatestPaymentByOrder(gomock.Any(), int64(7)).DoAndReturn(func(ctx context.Context, _ int64) (db.Payment, error) { return load(ctx) })
					get = func(ctx context.Context) (*biz.PaymentDO, error) { return r.GetLatestPaymentByOrder(ctx, 7) }
					key = "payment:order:7:g:0"
				case "active_by_method":
					q.EXPECT().GetActivePaymentByOrderChannel(gomock.Any(), db.GetActivePaymentByOrderChannelParams{OrderID: 7, PayChannel: "alipay"}).DoAndReturn(func(ctx context.Context, _ db.GetActivePaymentByOrderChannelParams) (db.Payment, error) {
						return load(ctx)
					})
					get = func(ctx context.Context) (*biz.PaymentDO, error) {
						return r.GetActivePaymentByOrderMethod(ctx, 7, "alipay")
					}
					key = "payment:order:7:active:alipay:g:0"
				case "out_trade_no":
					q.EXPECT().GetPaymentByOutTradeNo(gomock.Any(), "trade-42").DoAndReturn(func(ctx context.Context, _ string) (db.Payment, error) { return load(ctx) })
					get = func(ctx context.Context) (*biz.PaymentDO, error) { return r.GetPaymentByOutTradeNo(ctx, "trade-42") }
					key = "payment:out_trade_no:trade-42:g:0"
				}
				leader := startCacheCall(func() (*biz.PaymentDO, error) { return get(ctx) })
				workCtx := <-started
				waiter := startCacheCall(func() (*biz.PaymentDO, error) { return get(context.Background()) })
				synctest.Wait()
				cancel()
				require.ErrorIs(t, (<-leader).err, context.Canceled)
				require.NoError(t, workCtx.Err())
				deadline, ok := workCtx.Deadline()
				require.True(t, ok)
				require.Equal(t, 3*time.Second, time.Until(deadline))
				close(release)
				result := <-waiter
				require.NoError(t, result.err)
				require.Equal(t, int64(42), result.value.ID)
				fill := <-fills
				require.Equal(t, key, fill.key)
				require.NoError(t, fill.err)
				require.Equal(t, []any{"ex", int64(900)}, fill.args[3:], "payment TTL remains fifteen minutes")
				// fill derives a bounded child rather than detaching from workCtx.
				require.ErrorIs(t, fill.ctx.Err(), context.Canceled)
			})
		})
	}
}

func TestOrderRepo_ListOrdersUsesWorkContextForAllSQL(t *testing.T) {
	for _, ongoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "all", true: "ongoing"}[ongoing], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				q := mockdb.NewMockQuerier(gomock.NewController(t))
				d, fills := newRepositoryContextData(t, q)
				r := NewOrderRepo(d, nil, log.NewStdLogger(io.Discard))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				started := make(chan context.Context, 1)
				release := make(chan struct{})
				primary := func(ctx context.Context) ([]db.Order, error) {
					started <- ctx
					<-release
					return []db.Order{{ID: 42, UserID: 9}}, ctx.Err()
				}
				var get func(context.Context) ([]biz.Order, error)
				if ongoing {
					q.EXPECT().ListOngoingOrdersByUser(gomock.Any(), db.ListOngoingOrdersByUserParams{UserID: 9, Limit: 10}).DoAndReturn(func(ctx context.Context, _ db.ListOngoingOrdersByUserParams) ([]db.Order, error) {
						return primary(ctx)
					})
					get = func(ctx context.Context) ([]biz.Order, error) { return r.ListOngoingOrdersByUser(ctx, 9, 10, 0) }
				} else {
					q.EXPECT().ListOrdersByUser(gomock.Any(), db.ListOrdersByUserParams{UserID: 9, Limit: 10}).DoAndReturn(func(ctx context.Context, _ db.ListOrdersByUserParams) ([]db.Order, error) {
						return primary(ctx)
					})
					get = func(ctx context.Context) ([]biz.Order, error) { return r.ListOrdersByUser(ctx, 9, 10, 0) }
				}
				itemContext := make(chan context.Context, 1)
				itemError := make(chan error, 1)
				q.EXPECT().ListOrderItemsByOrderIDs(gomock.Any(), []int64{42}).DoAndReturn(func(ctx context.Context, _ []int64) ([]db.OrderItem, error) {
					itemContext <- ctx
					itemError <- ctx.Err()
					return []db.OrderItem{{OrderID: 42, ProductID: 7, Quantity: 1}}, ctx.Err()
				})
				leader := startCacheCall(func() ([]biz.Order, error) { return get(ctx) })
				workCtx := <-started
				waiter := startCacheCall(func() ([]biz.Order, error) { return get(context.Background()) })
				synctest.Wait()
				cancel() // Cancel between the main query starting and the item query.
				require.ErrorIs(t, (<-leader).err, context.Canceled)
				require.NoError(t, workCtx.Err())
				close(release)
				result := <-waiter
				require.NoError(t, result.err)
				require.Len(t, result.value, 1)
				require.Len(t, result.value[0].Items, 1)
				require.Equal(t, int64(7), result.value[0].Items[0].ProductID)
				require.Same(t, workCtx, <-itemContext)
				require.NoError(t, <-itemError, "item SQL must remain valid after the leader leaves")
				require.NoError(t, (<-fills).err)
			})
		})
	}
}

// Unlike cacheProcessHook, this hook retains real miniredis I/O. Barriers pause
// the second GET (after the snapshot, before SQL), not an estimated sleep.
type eventSnapshotHook struct {
	mux     sync.Mutex
	ready   chan struct{}
	release chan struct{}
	filled  chan struct{}
	once    sync.Once
	key     string
	reads   int
}

func (h *eventSnapshotHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *eventSnapshotHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *eventSnapshotHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "get" && cmd.Args()[1] == h.key {
			h.mux.Lock()
			h.reads++
			second := h.reads == 2
			h.mux.Unlock()
			if second {
				close(h.ready)
				<-h.release
			}
		}
		err := next(ctx, cmd)
		if cmd.Name() == "set" && cmd.Args()[1] == h.key {
			h.once.Do(func() { close(h.filled) })
		}
		return err
	}
}

func TestEventRepo_ListEventsSnapshotsStatusBeforeSharedWork(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	d := newTestData(t, q, mr)
	t.Cleanup(func() { _ = d.rdb.Close() })
	r := NewEventRepo(d, log.NewStdLogger(io.Discard))
	status := int32(1)
	originalStatus := status
	key := eventListPresenceCacheKey(0, &originalStatus, 10, 0)
	hook := &eventSnapshotHook{ready: make(chan struct{}), release: make(chan struct{}), filled: make(chan struct{}), key: key}
	d.rdb.AddHook(hook)
	observed := make(chan db.ListEventsByStatusParams, 1)
	queryError := make(chan error, 1)
	q.EXPECT().ListEventsByStatus(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, args db.ListEventsByStatusParams) ([]db.Event, error) {
		observed <- args
		queryError <- ctx.Err()
		return []db.Event{{ID: 42, Status: args.Status}}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := startCacheCall(func() ([]biz.Event, error) { return r.ListEvents(ctx, &status, 10, 0) })
	select {
	case <-hook.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("shared second GET did not start")
	}
	status = 2 // Synchronized after the snapshot and before SQL dereferences its condition.
	cancel()
	require.ErrorIs(t, (<-leader).err, context.Canceled)
	close(hook.release)
	select {
	case <-hook.filled:
	case <-time.After(3 * time.Second):
		t.Fatal("shared work did not fill the cache after cancellation")
	}
	// Wait for any still-finishing callback before closing its Redis client.
	d.sg.Do("sf:"+key, func() (any, error) { return nil, nil })
	require.NoError(t, <-queryError)
	require.Equal(t, db.ListEventsByStatusParams{Status: 1, Limit: 10}, <-observed)
	encoded, err := mr.Get(key)
	require.NoError(t, err)
	require.Contains(t, encoded, `"Status":1`)
	require.False(t, mr.Exists(eventListPresenceCacheKey(0, &status, 10, 0)))
	// All waiters left, but a later request still hits the original filter's cache.
	events, err := r.ListEvents(context.Background(), &originalStatus, 10, 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, int16(1), events[0].Status)
}
