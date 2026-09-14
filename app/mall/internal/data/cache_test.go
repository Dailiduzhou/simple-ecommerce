package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestDetailNegativeCachesExpire(t *testing.T) {
	for _, entity := range []string{"user", "product", "category", "event", "shipping_addr"} {
		t.Run(entity, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			mr := miniredis.RunT(t)
			d := newTestData(t, q, mr)
			ctx := context.Background()
			key := redisKey(entity, 42)
			var read func()
			switch entity {
			case "user":
				q.EXPECT().GetUserByID(gomock.Any(), int64(42)).Return(db.User{}, pgx.ErrNoRows).Times(2)
				r := NewUserRepo(d, log.DefaultLogger)
				read = func() {
					value, err := r.GetUserByID(ctx, 42)
					require.NoError(t, err)
					require.Nil(t, value)
				}
			case "product":
				key = redisKey("product", 42, "g", 0)
				q.EXPECT().GetProduct(gomock.Any(), int64(42)).Return(db.Product{}, pgx.ErrNoRows).Times(2)
				r := NewProductRepo(d, log.DefaultLogger)
				read = func() {
					value, err := r.GetProduct(ctx, 42)
					require.NoError(t, err)
					require.Nil(t, value)
				}
			case "category":
				q.EXPECT().GetCategory(gomock.Any(), int64(42)).Return(db.Category{}, pgx.ErrNoRows).Times(2)
				r := NewCategoryRepo(d, log.DefaultLogger)
				read = func() {
					value, err := r.GetCategory(ctx, 42)
					require.NoError(t, err)
					require.Nil(t, value)
				}
			case "event":
				q.EXPECT().GetEvent(gomock.Any(), int64(42)).Return(db.Event{}, pgx.ErrNoRows).Times(2)
				r := NewEventRepo(d, log.DefaultLogger)
				read = func() {
					value, err := r.GetEvent(ctx, 42)
					require.NoError(t, err)
					require.Nil(t, value)
				}
			case "shipping_addr":
				key = shippingAddressCacheKey(7, 42)
				q.EXPECT().GetShippingAddress(gomock.Any(), db.GetShippingAddressParams{ID: 42, UserID: 7}).Return(db.ShippingAddress{}, pgx.ErrNoRows).Times(2)
				r := NewShippingAddressRepo(d, nil, log.DefaultLogger)
				read = func() {
					value, err := r.GetShippingAddress(ctx, 42, 7)
					require.ErrorIs(t, err, biz.ErrShippingAddressNotFound)
					require.Nil(t, value)
				}
			}
			read()
			read() // No second database call before TTL expires.
			encoded, err := mr.Get(key)
			require.NoError(t, err)
			require.Equal(t, "null", encoded)
			require.Equal(t, negativeCacheTTL, mr.TTL(key))
			mr.FastForward(negativeCacheTTL)
			read()
		})
	}
}

func TestCacheFallbackAndDatabaseErrors(t *testing.T) {
	for _, failure := range []string{"invalid_json", "wrong_type", "unavailable", "database_error"} {
		t.Run(failure, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			mr := miniredis.RunT(t)
			d := newTestData(t, q, mr)
			r := NewProductRepo(d, log.DefaultLogger)
			ctx := context.Background()
			switch failure {
			case "invalid_json":
				require.NoError(t, mr.Set("product:42:g:0", "{broken"))
			case "wrong_type":
				_, err := mr.Lpush("product:42:g:0", "wrong")
				require.NoError(t, err)
			case "unavailable":
				require.NoError(t, d.rdb.Close())
			}
			if failure == "database_error" {
				q.EXPECT().GetProduct(gomock.Any(), int64(42)).Return(db.Product{}, pgx.ErrTxClosed).Times(2)
			} else {
				calls := 1
				if failure == "unavailable" {
					calls = 2
				}
				q.EXPECT().GetProduct(gomock.Any(), int64(42)).Return(db.Product{ID: 42, Name: "database"}, nil).Times(calls)
			}
			for range 2 {
				value, err := r.GetProduct(ctx, 42)
				if failure == "database_error" {
					require.ErrorIs(t, err, pgx.ErrTxClosed)
					require.Nil(t, value)
					require.False(t, mr.Exists("product:42:g:0"))
				} else {
					require.NoError(t, err)
					require.Equal(t, "database", value.Name)
				}
			}
		})
	}
}

func TestGenerationFailureNeverReadsOrWritesGenerationZero(t *testing.T) {
	for _, entity := range []string{"product", "product_category", "event", "order", "ongoing_order", "payment"} {
		t.Run(entity, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			mr := miniredis.RunT(t)
			d := newTestData(t, q, mr)
			ctx := context.Background()
			var genKey, oldKey string
			var read func()
			switch entity {
			case "product", "product_category":
				r := NewProductRepo(d, log.DefaultLogger)
				if entity == "product" {
					genKey, oldKey = "product:list:gen", "product:list:0:10:0"
					q.EXPECT().ListProducts(gomock.Any(), db.ListProductsParams{Limit: 10}).Return([]db.Product{}, nil).Times(2)
					read = func() {
						rows, err := r.ListProducts(ctx, 10, 0)
						require.NoError(t, err)
						require.Empty(t, rows)
					}
				} else {
					genKey, oldKey = "product:category:7:gen", "product:category:7:0:10:0"
					q.EXPECT().ListProductsByCategory(gomock.Any(), db.ListProductsByCategoryParams{CategoryID: 7, Limit: 10}).Return([]db.Product{}, nil).Times(2)
					read = func() {
						rows, err := r.ListProductsByCategory(ctx, 7, 10, 0)
						require.NoError(t, err)
						require.Empty(t, rows)
					}
				}
			case "event":
				genKey, oldKey = "event:list:gen", "event:list:0:all:10:0"
				q.EXPECT().ListEvents(gomock.Any(), db.ListEventsParams{Limit: 10}).Return([]db.Event{}, nil).Times(2)
				r := NewEventRepo(d, log.DefaultLogger)
				read = func() {
					rows, err := r.ListEvents(ctx, nil, 10, 0)
					require.NoError(t, err)
					require.Empty(t, rows)
				}
			case "order", "ongoing_order":
				r := &OrderRepo{data: d, log: log.NewHelper(log.DefaultLogger)}
				if entity == "order" {
					genKey, oldKey = "order:user:7:gen", "order:user:7:0:10:0"
					q.EXPECT().ListOrdersByUser(gomock.Any(), db.ListOrdersByUserParams{UserID: 7, Limit: 10}).Return([]db.Order{}, nil).Times(2)
					read = func() {
						rows, err := r.ListOrdersByUser(ctx, 7, 10, 0)
						require.NoError(t, err)
						require.Empty(t, rows)
					}
				} else {
					genKey, oldKey = "order:user:ongoing:7:gen", "order:user:ongoing:7:0"
					q.EXPECT().ListOngoingOrdersByUser(gomock.Any(), int64(7)).Return([]db.Order{}, nil).Times(2)
					read = func() {
						rows, err := r.ListOngoingOrdersByUser(ctx, 7)
						require.NoError(t, err)
						require.Empty(t, rows)
					}
				}
			case "payment":
				genKey, oldKey = "payment:gen", "payment:42:g:0"
				q.EXPECT().GetPayment(gomock.Any(), int64(42)).Return(db.Payment{ID: 42}, nil).Times(2)
				r := &PaymentRepo{data: d, log: log.NewHelper(log.DefaultLogger)}
				read = func() {
					row, err := r.GetPayment(ctx, 42)
					require.NoError(t, err)
					require.Equal(t, int64(42), row.ID)
				}
			}
			require.NoError(t, mr.Set(genKey, "invalid-generation"))
			oldValue := `[{"ID":99}]`
			if entity == "payment" {
				oldValue = `{"ID":99}`
			}
			require.NoError(t, mr.Set(oldKey, oldValue))
			read()
			read()
			require.Len(t, mr.Keys(), 2, "disabled cache must not publish a fallback key")
			value, err := mr.Get(oldKey)
			require.NoError(t, err)
			require.Equal(t, oldValue, value)
		})
	}
}

func TestCacheMutationsWaitForCommitAndSnapshotValues(t *testing.T) {
	for _, entity := range []string{"user", "category"} {
		t.Run(entity, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			mr := miniredis.RunT(t)
			d := newTestData(t, nil, mr) // All SQL must use the transaction querier.
			state := &txState{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			txCtx := context.WithValue(WithQuerier(ctx, q, nil), txStateKey{}, state)
			var get func(context.Context) (string, error)
			key := redisKey(entity, 42)
			if entity == "user" {
				r := NewUserRepo(d, log.DefaultLogger)
				r.setCache(ctx, key, &biz.User{ID: 42, Nickname: "old"})
				q.EXPECT().UpdateUser(gomock.Any(), gomock.Any()).Return(db.User{ID: 42, Nickname: "new", PasswordHash: "secret"}, nil)
				q.EXPECT().GetUserByID(gomock.Any(), int64(42)).Return(db.User{ID: 42, Nickname: "transaction"}, nil)
				value, err := r.UpdateUser(txCtx, 42, "new", "")
				require.NoError(t, err)
				value.Nickname = "caller mutation"
				get = func(ctx context.Context) (string, error) {
					u, err := r.GetUserByID(ctx, 42)
					if err != nil {
						return "", err
					}
					return u.Nickname, nil
				}
			} else {
				r := NewCategoryRepo(d, log.DefaultLogger)
				r.setCache(ctx, key, &biz.Category{ID: 42, Name: "old"})
				q.EXPECT().GetCategory(gomock.Any(), int64(42)).Return(db.Category{ID: 42, Name: "transaction"}, nil).Times(2)
				q.EXPECT().UpdateCategory(gomock.Any(), gomock.Any()).Return(db.Category{ID: 42, Name: "new"}, nil)
				value, err := r.UpdateCategory(txCtx, 42, "new", 0)
				require.NoError(t, err)
				value.Name = "caller mutation"
				get = func(ctx context.Context) (string, error) {
					c, err := r.GetCategory(ctx, 42)
					if err != nil {
						return "", err
					}
					return c.Name, nil
				}
			}
			value, err := get(ctx)
			require.NoError(t, err)
			require.Equal(t, "old", value, "uncommitted writes must not be visible")
			callbacks := len(state.afterCommit)
			value, err = get(txCtx)
			require.NoError(t, err)
			require.Equal(t, "transaction", value, "transaction reads bypass old shared cache")
			require.Len(t, state.afterCommit, callbacks, "transaction snapshots must not be queued for publication")
			// Dropping these callbacks (rollback/failed commit) leaves 'old' intact.
			// A successful commit must still publish after client cancellation.
			cancel()
			for _, callback := range state.afterCommit {
				callback()
			}
			value, err = get(context.Background())
			require.NoError(t, err)
			require.Equal(t, "new", value)
			encoded, err := mr.Get(key)
			require.NoError(t, err)
			require.NotContains(t, encoded, "secret")
			require.GreaterOrEqual(t, mr.TTL(key), 10*time.Minute)
			require.Less(t, mr.TTL(key), 20*time.Minute)
		})
	}
}

func TestProductSingleflightSeparatesGenerations(t *testing.T) {
	for _, byCategory := range []bool{false, true} {
		t.Run(map[bool]string{false: "all", true: "category"}[byCategory], func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			mr := miniredis.RunT(t)
			r := NewProductRepo(newTestData(t, q, mr), log.DefaultLogger)
			started, release := make(chan struct{}), make(chan struct{})
			oldDone := make(chan struct{})
			defer func() {
				close(release)
				<-oldDone
			}()
			loadOld := func() ([]db.Product, error) {
				close(started)
				<-release
				return []db.Product{{ID: 1, Name: "old"}}, nil
			}
			var read func() ([]biz.Product, error)
			genKey := "product:list:gen"
			if byCategory {
				params := db.ListProductsByCategoryParams{CategoryID: 7, Limit: 10}
				old := q.EXPECT().ListProductsByCategory(gomock.Any(), params).DoAndReturn(func(context.Context, db.ListProductsByCategoryParams) ([]db.Product, error) { return loadOld() })
				q.EXPECT().ListProductsByCategory(gomock.Any(), params).Return([]db.Product{{ID: 1, Name: "new"}}, nil).After(old)
				genKey = "product:category:7:gen"
				read = func() ([]biz.Product, error) { return r.ListProductsByCategory(context.Background(), 7, 10, 0) }
			} else {
				params := db.ListProductsParams{Limit: 10}
				old := q.EXPECT().ListProducts(gomock.Any(), params).DoAndReturn(func(context.Context, db.ListProductsParams) ([]db.Product, error) { return loadOld() })
				q.EXPECT().ListProducts(gomock.Any(), params).Return([]db.Product{{ID: 1, Name: "new"}}, nil).After(old)
				read = func() ([]biz.Product, error) { return r.ListProducts(context.Background(), 10, 0) }
			}
			go func() {
				defer close(oldDone)
				_, _ = read()
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("old load did not start")
			}
			bumpCacheGeneration(context.Background(), r.data.rdb, r.log, genKey)
			type result struct {
				rows []biz.Product
				err  error
			}
			finished := make(chan result, 1)
			go func() {
				rows, err := read()
				finished <- result{rows, err}
			}()
			select {
			case result := <-finished:
				require.NoError(t, result.err)
				require.Equal(t, "new", result.rows[0].Name)
			case <-time.After(3 * time.Second):
				t.Fatal("new generation incorrectly joined old singleflight")
			}
		})
	}
}

func TestShippingAddressSingleflightSeparatesOwners(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	r := NewShippingAddressRepo(newTestData(t, q, mr), nil, log.DefaultLogger)
	started, release := make(chan struct{}), make(chan struct{})
	oldDone := make(chan struct{})
	defer func() {
		close(release)
		<-oldDone
	}()
	q.EXPECT().GetShippingAddress(gomock.Any(), db.GetShippingAddressParams{ID: 42, UserID: 1}).DoAndReturn(func(context.Context, db.GetShippingAddressParams) (db.ShippingAddress, error) {
		close(started)
		<-release
		return db.ShippingAddress{ID: 42, UserID: 1}, nil
	})
	q.EXPECT().GetShippingAddress(gomock.Any(), db.GetShippingAddressParams{ID: 42, UserID: 2}).Return(db.ShippingAddress{}, pgx.ErrNoRows)
	go func() {
		defer close(oldDone)
		_, _ = r.GetShippingAddress(context.Background(), 42, 1)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("owner load did not start")
	}
	finished := make(chan error, 1)
	go func() {
		_, err := r.GetShippingAddress(context.Background(), 42, 2)
		finished <- err
	}()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, biz.ErrShippingAddressNotFound)
	case <-time.After(3 * time.Second):
		t.Fatal("different owners shared a singleflight")
	}
}

func TestEmptyListIsCachedAndFailedMutationDoesNotInvalidate(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	r := NewCategoryRepo(newTestData(t, q, mr), log.DefaultLogger)
	q.EXPECT().ListTopCategories(gomock.Any()).Return(nil, nil).Times(1)
	q.EXPECT().CreateCategory(gomock.Any(), gomock.Any()).Return(db.Category{}, errors.New("database failure"))
	for range 2 {
		rows, err := r.ListTopCategories(context.Background())
		require.NoError(t, err)
		require.Empty(t, rows)
	}
	value, err := mr.Get(categoryListCacheKey(0))
	require.NoError(t, err)
	require.Equal(t, "[]", value)
	_, err = r.CreateCategory(context.Background(), 0, "new", 0)
	require.Error(t, err)
	require.True(t, mr.Exists(categoryListCacheKey(0)))
}

func TestCreateReplacesNegativeCache(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	r := NewCategoryRepo(newTestData(t, q, mr), log.DefaultLogger)
	ctx := context.Background()
	q.EXPECT().GetCategory(gomock.Any(), int64(42)).Return(db.Category{}, pgx.ErrNoRows)
	q.EXPECT().CreateCategory(gomock.Any(), gomock.Any()).Return(db.Category{ID: 42, Name: "created"}, nil)
	value, err := r.GetCategory(ctx, 42)
	require.NoError(t, err)
	require.Nil(t, value)
	_, err = r.CreateCategory(ctx, 0, "created", 0)
	require.NoError(t, err)
	value, err = r.GetCategory(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, "created", value.Name)
	require.GreaterOrEqual(t, mr.TTL("category:42"), 10*time.Minute)
}

func TestGenerationCommitAndFailureSemantics(t *testing.T) {
	mr := miniredis.RunT(t)
	d := newTestData(t, nil, mr)
	logger := log.NewHelper(log.DefaultLogger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := "product:list:gen"
	require.Zero(t, cacheGeneration(ctx, d.rdb, logger, key))
	state := &txState{}
	txCtx := context.WithValue(ctx, txStateKey{}, state)
	bumpCacheGeneration(txCtx, d.rdb, logger, key)
	require.False(t, mr.Exists(key), "a discarded transaction must not advance generation")
	require.Len(t, state.afterCommit, 1)
	cancel()
	state.afterCommit[0]()
	require.Equal(t, int64(1), cacheGeneration(context.Background(), d.rdb, logger, key))
	for _, invalid := range []string{"not-an-integer", "-2"} {
		require.NoError(t, mr.Set(key, invalid))
		require.Equal(t, int64(-1), cacheGeneration(context.Background(), d.rdb, logger, key))
	}
	require.NoError(t, d.rdb.Close())
	require.Equal(t, int64(-1), cacheGeneration(context.Background(), d.rdb, logger, key))
}

func TestLoginAlwaysLoadsCurrentCredentials(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	r := NewUserRepo(newTestData(t, q, mr), log.DefaultLogger)
	ctx := context.Background()
	r.setCache(ctx, "user:42", &biz.User{ID: 42, Nickname: "cached"})
	require.NoError(t, mr.Set("user:phone:hash42", `{"ID":42,"PasswordHash":"legacy"}`))
	first := q.EXPECT().GetUserByPhoneHash(gomock.Any(), "hash42").Return(db.User{ID: 42, PasswordHash: "password-one"}, nil)
	q.EXPECT().GetUserByPhoneHash(gomock.Any(), "hash42").Return(db.User{ID: 42, PasswordHash: "password-two"}, nil).After(first)
	for _, password := range []string{"password-one", "password-two"} {
		u, err := r.GetUserByPhoneHash(ctx, "hash42")
		require.NoError(t, err)
		require.Equal(t, password, u.PasswordHash)
		profile, err := mr.Get("user:42")
		require.NoError(t, err)
		require.NotContains(t, profile, "PasswordHash")
		require.NotContains(t, profile, password)
	}
}
