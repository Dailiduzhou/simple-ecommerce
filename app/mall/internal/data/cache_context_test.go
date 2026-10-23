package data

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/alicebob/miniredis/v2"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

type cacheCallResult[T any] struct {
	value T
	err   error
}

func startCacheCall[T any](call func() (T, error)) <-chan cacheCallResult[T] {
	result := make(chan cacheCallResult[T], 1)
	go func() {
		value, err := call()
		result <- cacheCallResult[T]{value, err}
	}()
	return result
}

// Pure-memory callbacks let synctest.Wait prove that waiters have joined the
// flight, without sleeps, network I/O, or assumptions about goroutine scheduling.
type cacheContextFixture struct {
	data   *Data
	logger *log.Helper
	mu     sync.Mutex
	values map[string]int
	reads  atomic.Int32
	loads  atomic.Int32
	writes atomic.Int32
}

func newCacheContextFixture() *cacheContextFixture {
	return &cacheContextFixture{
		// The empty client only makes cacheAside take the cached path; every Redis
		// interaction is supplied by this fixture's read/write callbacks.
		data:   &Data{rdb: &redis.Client{}, sg: &singleflight.Group{}},
		logger: log.NewHelper(log.NewStdLogger(io.Discard)),
		values: make(map[string]int),
	}
}

func (f *cacheContextFixture) read(ctx context.Context, key string) (int, error) {
	f.reads.Add(1)
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if value, ok := f.values[key]; ok {
		return value, nil
	}
	return 0, redis.Nil
}

func (f *cacheContextFixture) write(_ context.Context, key string, value int) {
	f.writes.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = value
}

func (f *cacheContextFixture) call(ctx context.Context, load func(context.Context) (int, error)) <-chan cacheCallResult[int] {
	return startCacheCall(func() (int, error) {
		return cacheAside(ctx, f.data, f.logger, "product:42", f.read, f.write, func(loadCtx context.Context) (int, error) {
			f.loads.Add(1)
			return load(loadCtx)
		})
	})
}

func TestCacheAside_LeaderCancellationDoesNotCancelSharedLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		load := func(workCtx context.Context) (int, error) {
			started <- workCtx
			select {
			case <-release:
				return 42, workCtx.Err()
			case <-workCtx.Done():
				return 0, workCtx.Err()
			}
		}
		leader := f.call(ctx, load)
		workCtx := <-started
		waiter := f.call(context.Background(), load)
		synctest.Wait()
		require.Equal(t, int32(3), f.reads.Load())
		cancel()
		synctest.Wait()
		require.ErrorIs(t, (<-leader).err, context.Canceled)
		require.NoError(t, workCtx.Err())
		close(release)
		result := <-waiter
		synctest.Wait()
		require.NoError(t, result.err)
		require.Equal(t, 42, result.value)
		require.Equal(t, int32(1), f.loads.Load())
		require.Equal(t, int32(1), f.writes.Load())
		require.ErrorIs(t, workCtx.Err(), context.Canceled, "callback releases its context")
	})
}

func TestCacheAside_WaiterCancellationDoesNotCancelSharedLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		load := func(ctx context.Context) (int, error) {
			started <- ctx
			<-release
			return 42, ctx.Err()
		}
		leader := f.call(context.Background(), load)
		workCtx := <-started
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waiter := f.call(ctx, load)
		other := f.call(context.Background(), load)
		synctest.Wait()
		cancel()
		synctest.Wait()
		require.ErrorIs(t, (<-waiter).err, context.Canceled)
		require.NoError(t, workCtx.Err())
		close(release)
		for _, result := range []cacheCallResult[int]{<-leader, <-other} {
			require.NoError(t, result.err)
			require.Equal(t, 42, result.value)
		}
		synctest.Wait()
		require.Equal(t, int32(1), f.loads.Load())
	})
}

func TestCacheAside_LeaderDeadlineIsNotInherited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		f.data.sharedCacheWorkTimeout = time.Second
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		load := func(ctx context.Context) (int, error) {
			started <- ctx
			<-release
			return 42, ctx.Err()
		}
		leader := f.call(ctx, load)
		workCtx := <-started
		deadline, ok := workCtx.Deadline()
		require.True(t, ok)
		require.Equal(t, time.Second, time.Until(deadline))
		waiter := f.call(context.Background(), load)
		synctest.Wait()
		time.Sleep(100 * time.Millisecond) // Advance fake time to the leader's deadline.
		synctest.Wait()
		require.ErrorIs(t, (<-leader).err, context.DeadlineExceeded)
		require.NoError(t, workCtx.Err())
		close(release)
		result := <-waiter
		require.NoError(t, result.err)
		require.Equal(t, 42, result.value)
		require.Equal(t, int32(1), f.loads.Load())
	})
}

func TestCacheAside_SharedWorkTimeout(t *testing.T) {
	for _, loadReturnsSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "load_error", true: "late_success"}[loadReturnsSuccess], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCacheContextFixture()
				f.data.sharedCacheWorkTimeout = 50 * time.Millisecond
				started := make(chan context.Context, 1)
				load := func(ctx context.Context) (int, error) {
					started <- ctx
					<-ctx.Done()
					if loadReturnsSuccess {
						return 42, nil
					}
					return 0, ctx.Err()
				}
				leader := f.call(context.Background(), load)
				workCtx := <-started
				deadline, ok := workCtx.Deadline()
				require.True(t, ok)
				require.Equal(t, 50*time.Millisecond, time.Until(deadline))
				waiter := f.call(context.Background(), load)
				synctest.Wait()
				for _, result := range []cacheCallResult[int]{<-leader, <-waiter} {
					require.Equal(t, int32(500), kerrors.FromError(result.err).Code)
					require.Equal(t, "CACHE_WORK_TIMEOUT", kerrors.FromError(result.err).Reason)
				}
				synctest.Wait()
				require.Equal(t, int32(1), f.loads.Load())
				require.Zero(t, f.writes.Load())
			})
		})
	}
}

// A shared-budget timeout must stay distinguishable from the caller's own
// deadline and from ordinary database errors, so slow reads are not reported as
// opaque internal failures and healthy callers get a retryable reason.
func TestCacheAside_SharedWorkTimeoutIsDistinguishable(t *testing.T) {
	t.Run("caller_deadline_wins", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newCacheContextFixture()
			f.data.sharedCacheWorkTimeout = time.Second
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			result := f.call(ctx, func(workCtx context.Context) (int, error) {
				started <- workCtx
				<-release
				return 42, workCtx.Err()
			})
			workCtx := <-started
			synctest.Wait()
			time.Sleep(50 * time.Millisecond) // Advance fake time to the caller's deadline.
			synctest.Wait()
			require.ErrorIs(t, (<-result).err, context.DeadlineExceeded, "the caller observes its own deadline")
			require.NoError(t, workCtx.Err(), "shared work keeps its own budget")
			close(release)
			synctest.Wait()
		})
	})

	t.Run("database_error_unchanged", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := newCacheContextFixture()
			databaseErr := errors.New("connection reset")
			result := <-f.call(context.Background(), func(context.Context) (int, error) {
				return 0, databaseErr
			})
			require.Equal(t, databaseErr, result.err, "only shared-budget timeouts are relabelled")
		})
	})
}

// A waiter whose own deadline is shorter than the shared budget leaves on time
// and keeps waiting for nothing; the leader still receives the shared result.
func TestCacheAside_WaiterDeadlineShorterThanWorkBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		f.data.sharedCacheWorkTimeout = time.Second
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		load := func(workCtx context.Context) (int, error) {
			started <- workCtx
			<-release
			return 42, workCtx.Err()
		}
		leader := f.call(context.Background(), load)
		workCtx := <-started
		waiterCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		waiter := f.call(waiterCtx, load)
		synctest.Wait()
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		require.ErrorIs(t, (<-waiter).err, context.DeadlineExceeded)
		require.NoError(t, workCtx.Err())
		close(release)
		result := <-leader
		require.NoError(t, result.err)
		require.Equal(t, 42, result.value)
		require.Equal(t, int32(1), f.loads.Load())
	})
}

func TestCacheAside_AllWaitersCancelStillFillsCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		ctxA, cancelA := context.WithCancel(context.Background())
		defer cancelA()
		ctxB, cancelB := context.WithCancel(context.Background())
		defer cancelB()
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		load := func(ctx context.Context) (int, error) {
			started <- ctx
			<-release
			return 42, ctx.Err()
		}
		leader := f.call(ctxA, load)
		workCtx := <-started
		waiter := f.call(ctxB, load)
		synctest.Wait()
		cancelA()
		cancelB()
		require.ErrorIs(t, (<-leader).err, context.Canceled)
		require.ErrorIs(t, (<-waiter).err, context.Canceled)
		require.NoError(t, workCtx.Err())
		close(release)
		synctest.Wait() // Includes synchronous fill and completion of the abandoned flight.
		result := <-f.call(context.Background(), load)
		require.NoError(t, result.err)
		require.Equal(t, 42, result.value)
		require.Equal(t, int32(1), f.loads.Load())
		require.Equal(t, int32(1), f.writes.Load())
	})
}

func TestCacheAside_CanceledRequestDoesNotStartLoad(t *testing.T) {
	for _, when := range []string{"before_call", "during_miss", "during_hit"} {
		t.Run(when, func(t *testing.T) {
			f := newCacheContextFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if when == "before_call" {
				cancel()
			}
			reads := 0
			_, err := cacheAside(ctx, f.data, f.logger, "product:42", func(context.Context, string) (int, error) {
				reads++
				cancel()
				if when == "during_hit" {
					return 42, nil
				}
				return 0, redis.Nil
			}, f.write, func(context.Context) (int, error) {
				t.Fatal("canceled request started a load")
				return 0, nil
			})
			require.ErrorIs(t, err, context.Canceled)
			if when == "before_call" {
				require.Zero(t, reads)
			} else {
				require.Equal(t, 1, reads)
			}
			require.Zero(t, f.writes.Load())
		})
	}
}

func testCacheAsideRequestContext(t *testing.T, mode string) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		f.data.sg = nil // These paths must not register shared work.
		key := "product:42"
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		switch mode {
		case "empty_key":
			key = ""
		case "nil_redis":
			f.data.rdb = nil
		case "transaction_querier":
			ctx = WithQuerier(ctx, &db.Queries{}, nil)
		case "transaction_state":
			ctx = context.WithValue(ctx, txStateKey{}, &txState{})
		}
		started := make(chan context.Context, 1)
		result := startCacheCall(func() (int, error) {
			return cacheAside(ctx, f.data, f.logger, key, f.read, f.write, func(loadCtx context.Context) (int, error) {
				started <- loadCtx
				<-loadCtx.Done()
				return 0, loadCtx.Err()
			})
		})
		require.Same(t, ctx, <-started)
		cancel()
		require.ErrorIs(t, (<-result).err, context.Canceled)
		require.Zero(t, f.reads.Load())
		require.Zero(t, f.writes.Load())
	})
}

func TestCacheAside_TransactionUsesRequestContext(t *testing.T) {
	for _, mode := range []string{"transaction_querier", "transaction_state"} {
		t.Run(mode, func(t *testing.T) { testCacheAsideRequestContext(t, mode) })
	}
}

func TestCacheAside_DisabledCacheUsesRequestContext(t *testing.T) {
	for _, mode := range []string{"empty_key", "nil_redis"} {
		t.Run(mode, func(t *testing.T) { testCacheAsideRequestContext(t, mode) })
	}
}

func TestCacheAside_SecondReadUsesWorkContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		secondRead := make(chan context.Context, 1)
		release := make(chan struct{})
		read := func(readCtx context.Context, key string) (int, error) {
			if readCtx == ctx {
				return 0, redis.Nil
			}
			secondRead <- readCtx
			<-release
			return 42, readCtx.Err()
		}
		leader := startCacheCall(func() (int, error) {
			return cacheAside(ctx, f.data, f.logger, "product:42", read, f.write, nil)
		})
		workCtx := <-secondRead
		waiter := f.call(context.Background(), nil)
		synctest.Wait()
		cancel()
		require.ErrorIs(t, (<-leader).err, context.Canceled)
		require.NoError(t, workCtx.Err())
		close(release)
		result := <-waiter
		require.NoError(t, result.err)
		require.Equal(t, 42, result.value)
		require.Zero(t, f.loads.Load())
		require.Zero(t, f.writes.Load())
	})
}

func TestCacheAside_SecondReadExhaustsBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newCacheContextFixture()
		f.data.sharedCacheWorkTimeout = 50 * time.Millisecond
		var reads atomic.Int32
		result := startCacheCall(func() (int, error) {
			return cacheAside(context.Background(), f.data, f.logger, "product:42", func(ctx context.Context, _ string) (int, error) {
				if reads.Add(1) == 1 {
					return 0, redis.Nil
				}
				<-ctx.Done()
				return 0, ctx.Err()
			}, f.write, nil) // Any attempted load would panic.
		})
		require.Equal(t, "CACHE_WORK_TIMEOUT", kerrors.FromError((<-result).err).Reason)
		require.Equal(t, int32(2), reads.Load())
		require.Zero(t, f.writes.Load())
	})
}

func TestCacheAside_PreservesContextValues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type stableKey struct{}
		f := newCacheContextFixture()
		ctx := context.WithValue(context.Background(), stableKey{}, "stable-trace-value")
		value := make(chan any, 1)
		result := <-f.call(ctx, func(loadCtx context.Context) (int, error) {
			value <- loadCtx.Value(stableKey{})
			return 42, nil
		})
		require.NoError(t, result.err)
		require.Equal(t, "stable-trace-value", <-value)
	})
}

// Intercept Redis commands without opening sockets, so context/deadline tests
// remain entirely inside the synctest bubble.
type cacheProcessHook struct {
	process redis.ProcessHook
}

func (h cacheProcessHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h cacheProcessHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return h.process
}
func (h cacheProcessHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func newCacheHookData(t *testing.T, process redis.ProcessHook) *Data {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379", Protocol: 2})
	rdb.AddHook(cacheProcessHook{process: process})
	t.Cleanup(func() { _ = rdb.Close() })
	return &Data{rdb: rdb, sg: &singleflight.Group{}}
}

func TestFillJSONCache_RespectsParentDeadline(t *testing.T) {
	for _, mode := range []string{"parent_deadline", "parent_cancel", "local_limit"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				budget := 100 * time.Millisecond
				if mode == "local_limit" {
					budget = 5 * time.Second
				}
				ctx, cancel := context.WithTimeout(context.Background(), budget)
				defer cancel()
				started := make(chan context.Context, 1)
				setError := make(chan error, 1)
				d := newCacheHookData(t, func(setCtx context.Context, _ redis.Cmder) error {
					started <- setCtx
					<-setCtx.Done()
					setError <- setCtx.Err()
					return setCtx.Err()
				})
				done := make(chan struct{})
				go func() {
					defer close(done)
					fillJSONCache(ctx, d, newCacheContextFixture().logger, "product:42", 42, time.Minute)
				}()
				setCtx := <-started
				deadline, ok := setCtx.Deadline()
				require.True(t, ok)
				require.Equal(t, min(budget, 2*time.Second), time.Until(deadline))
				wantErr := context.DeadlineExceeded
				if mode == "parent_cancel" {
					cancel()
					wantErr = context.Canceled
				}
				<-done
				require.ErrorIs(t, <-setError, wantErr)
			})
		})
	}
}

func TestFillJSONCache_FailureDoesNotFailSuccessfulLoad(t *testing.T) {
	for _, mode := range []string{"failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newCacheContextFixture()
				var sets atomic.Int32
				d := newCacheHookData(t, func(ctx context.Context, _ redis.Cmder) error {
					sets.Add(1)
					if mode == "timeout" {
						<-ctx.Done()
						return ctx.Err()
					}
					return errors.New("redis unavailable")
				})
				d.sharedCacheWorkTimeout = 50 * time.Millisecond
				result := <-startCacheCall(func() (int, error) {
					return cacheAside(context.Background(), d, f.logger, "product:42", f.read, func(ctx context.Context, key string, value int) {
						fillJSONCache(ctx, d, f.logger, key, value, time.Minute)
					}, func(context.Context) (int, error) { return 42, nil })
				})
				require.NoError(t, result.err)
				require.Equal(t, 42, result.value)
				require.Equal(t, int32(1), sets.Load())
			})
		})
	}
}

func TestFillJSONCache_TransactionDoesNotPublish(t *testing.T) {
	state := &txState{}
	ctx := context.WithValue(context.Background(), txStateKey{}, state)
	f := newCacheContextFixture()
	d := newCacheHookData(t, func(context.Context, redis.Cmder) error {
		t.Fatal("transaction snapshot was published")
		return nil
	})
	fillJSONCache(ctx, d, f.logger, "product:42", 42, time.Minute)
	fillJSONCache(WithQuerier(context.Background(), &db.Queries{}, nil), d, f.logger, "product:42", 42, time.Minute)
	require.Empty(t, state.afterCommit)
}

func TestFillJSONCache_NegativeTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	d := newTestData(t, nil, mr)
	t.Cleanup(func() { _ = d.rdb.Close() })
	logger := newCacheContextFixture().logger
	var missing *int
	fillJSONCache(context.Background(), d, logger, "product:missing", missing, time.Minute)
	fillJSONCache(context.Background(), d, logger, "product:list", []int{}, time.Minute)
	require.Equal(t, 30*time.Second, mr.TTL("product:missing"))
	require.Equal(t, time.Minute, mr.TTL("product:list"))
	encoded, err := mr.Get("product:missing")
	require.NoError(t, err)
	require.Equal(t, "null", encoded)
	encoded, err = mr.Get("product:list")
	require.NoError(t, err)
	require.Equal(t, "[]", encoded)
}

func TestFillJSONCache_DisabledPaths(t *testing.T) {
	logger := newCacheContextFixture().logger
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Empty keys disable caching on their own; a canceled parent must never
	// publish either. Both are proven by a Redis client that fails the test.
	d := newCacheHookData(t, func(context.Context, redis.Cmder) error {
		t.Fatal("disabled fill reached Redis")
		return nil
	})
	fillJSONCache(context.Background(), d, logger, "", 42, time.Minute)
	fillJSONCache(ctx, d, logger, "product:42", 42, time.Minute)
	fillJSONCache(context.Background(), &Data{}, logger, "product:42", 42, time.Minute)
}
