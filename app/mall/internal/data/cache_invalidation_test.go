package data

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type invalidationHook struct {
	pipelines int
	commands  int
	deadline  time.Time
}

func (h *invalidationHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *invalidationHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }
func (h *invalidationHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.pipelines++
		h.commands += len(cmds)
		h.deadline, _ = ctx.Deadline()
		return next(ctx, cmds)
	}
}

func TestCacheInvalidationDeduplicatesAcrossCommitAndLargeOrders(t *testing.T) {
	for _, transactional := range []bool{false, true} {
		t.Run(map[bool]string{false: "post-commit", true: "transaction"}[transactional], func(t *testing.T) {
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), ContextTimeoutEnabled: true})
			t.Cleanup(func() { _ = rdb.Close() })
			require.NoError(t, rdb.Ping(context.Background()).Err())
			hook := &invalidationHook{}
			rdb.AddHook(hook)
			state := &txState{}
			apply := func(ctx context.Context) {
				for range 100 {
					scheduleCacheInvalidation(ctx, rdb, log.NewHelper(log.DefaultLogger), []string{"product:42:gen", "product:list:gen", "product:category:7:gen"}, []string{"product:42"})
				}
			}
			if transactional {
				apply(context.WithValue(context.Background(), txStateKey{}, state))
				require.Zero(t, hook.pipelines)
				require.Len(t, state.afterCommit, 1)
				state.afterCommit[0]()
			} else {
				batchCacheInvalidations(context.Background(), apply)
			}
			require.Equal(t, 1, hook.pipelines)
			require.Equal(t, 4, hook.commands)
			require.WithinDuration(t, time.Now().Add(2*time.Second), hook.deadline, time.Second)
			value, err := mr.Get("product:list:gen")
			require.NoError(t, err)
			require.Equal(t, "1", value)
		})
	}
}

func TestPaymentInvalidationPreservesUnrelatedPaymentCaches(t *testing.T) {
	mr := miniredis.RunT(t)
	d := newTestData(t, nil, mr)
	repo := &PaymentRepo{data: d, log: log.NewHelper(log.DefaultLogger)}
	require.NoError(t, d.rdb.Set(context.Background(), "payment:99:g:0", "unrelated", time.Minute).Err())
	repo.invalidatePayment(context.Background(), db.Payment{ID: 42, OrderID: 7, OutTradeNo: "trade-42"})
	for _, key := range []string{"payment:42:gen", "payment:order:7:gen", "payment:out_trade_no:trade-42:gen"} {
		require.Equal(t, "1", d.rdb.Get(context.Background(), key).Val())
	}
	require.False(t, mr.Exists("payment:gen"))
	require.False(t, mr.Exists("payment:99:gen"))
	require.True(t, mr.Exists("payment:99:g:0"))
}
