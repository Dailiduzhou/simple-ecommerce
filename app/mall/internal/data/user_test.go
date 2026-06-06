package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

func newTestData(t *testing.T, mockQ db.Querier, mr *miniredis.Miniredis) *Data {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return &Data{
		q:   mockQ,
		rdb: rdb,
		sg:  &singleflight.Group{},
	}
}

func TestUserRepo_GetUserByID_Singleflight(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockUser := db.User{
		ID:           1,
		Nickname:     "kratos",
		PhoneHash:    "hash1",
		PhoneEncrypt: "enc1",
		PasswordHash: "pwd1",
		Role:         "user",
	}

	mockQ.EXPECT().
		GetUserByID(gomock.Any(), int64(1)).
		Times(1).
		DoAndReturn(func(ctx context.Context, id int64) (db.User, error) {
			time.Sleep(100 * time.Millisecond)
			return mockUser, nil
		})

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)

	results := make([]*biz.User, concurrency)
	errs := make([]error, concurrency)

	for i := range concurrency {
		go func(index int) {
			defer wg.Done()
			user, err := repo.GetUserByID(context.Background(), 1)
			results[index] = user
			errs[index] = err
		}(i)
	}

	wg.Wait()

	for i := range concurrency {
		require.NoError(t, errs[i])
		assert.Equal(t, mockUser.ID, results[i].ID)
		assert.Equal(t, mockUser.Nickname, results[i].Nickname)
	}
}

func TestUserRepo_GetUserByID_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockUser := db.User{
		ID:       2,
		Nickname: "cached_user",
		Role:     "user",
	}

	mockQ.EXPECT().
		GetUserByID(gomock.Any(), int64(2)).
		Times(1).
		Return(mockUser, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	u1, err := repo.GetUserByID(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), u1.ID)
	assert.Equal(t, "cached_user", u1.Nickname)

	u2, err := repo.GetUserByID(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), u2.ID)
	assert.Equal(t, "cached_user", u2.Nickname)
}

func TestUserRepo_GetUserByID_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		GetUserByID(gomock.Any(), int64(999)).
		Times(1).
		Return(db.User{}, pgx.ErrNoRows)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	u, err := repo.GetUserByID(context.Background(), 999)
	require.NoError(t, err)
	assert.Nil(t, u)
}

func TestUserRepo_GetUserByID_DBError(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	dbErr := pgx.ErrTxClosed

	mockQ.EXPECT().
		GetUserByID(gomock.Any(), int64(1)).
		Times(1).
		Return(db.User{}, dbErr)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	u, err := repo.GetUserByID(context.Background(), 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, pgx.ErrTxClosed)
	assert.Nil(t, u)
}

func TestUserRepo_CreateUser_CachesByIDAndPhoneHash(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockUser := db.User{
		ID:           3,
		Nickname:     "new_user",
		PhoneHash:    "hash3",
		PhoneEncrypt: "enc3",
		PasswordHash: "pwd3",
		Role:         "user",
	}

	mockQ.EXPECT().
		CreateUser(gomock.Any(), db.CreateUserParams{
			Nickname:     "new_user",
			PhoneHash:    "hash3",
			PhoneEncrypt: "enc3",
			PasswordHash: "pwd3",
			Role:         "user",
		}).
		Times(1).
		Return(mockUser, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	u, err := repo.CreateUser(context.Background(), "new_user", "hash3", "enc3", "pwd3")
	require.NoError(t, err)
	assert.Equal(t, int64(3), u.ID)

	byID, err := repo.GetUserByID(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, int64(3), byID.ID)

	byPhone, err := repo.GetUserByPhoneHash(context.Background(), "hash3")
	require.NoError(t, err)
	assert.Equal(t, int64(3), byPhone.ID)
}

func TestUserRepo_GetUserByPhoneHash_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockUser := db.User{
		ID:           4,
		Nickname:     "phone_cached",
		PhoneHash:    "hash4",
		PhoneEncrypt: "enc4",
		PasswordHash: "pwd4",
		Role:         "user",
	}

	mockQ.EXPECT().
		GetUserByPhoneHash(gomock.Any(), "hash4").
		Times(1).
		Return(mockUser, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	u1, err := repo.GetUserByPhoneHash(context.Background(), "hash4")
	require.NoError(t, err)
	assert.Equal(t, int64(4), u1.ID)

	u2, err := repo.GetUserByPhoneHash(context.Background(), "hash4")
	require.NoError(t, err)
	assert.Equal(t, int64(4), u2.ID)
}

func TestUserRepo_GetUserByPhoneHash_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		GetUserByPhoneHash(gomock.Any(), "missing").
		Times(1).
		Return(db.User{}, pgx.ErrNoRows)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)

	u, err := repo.GetUserByPhoneHash(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, u)
}

func TestUserRepo_UpdateUser_RefreshesCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), "user:5", &biz.User{
		ID:        5,
		Nickname:  "old",
		PhoneHash: "hash5",
		Role:      "user",
	})
	repo.setCache(context.Background(), "user:phone:hash5", &biz.User{
		ID:        5,
		Nickname:  "old",
		PhoneHash: "hash5",
		Role:      "user",
	})

	mockQ.EXPECT().
		UpdateUser(gomock.Any(), db.UpdateUserParams{
			ID:       5,
			Nickname: "new",
			RealName: "real",
		}).
		Times(1).
		Return(db.User{
			ID:        5,
			Nickname:  "new",
			RealName:  "real",
			PhoneHash: "hash5",
			Role:      "user",
		}, nil)

	u, err := repo.UpdateUser(context.Background(), 5, "new", "real")
	require.NoError(t, err)
	assert.Equal(t, "new", u.Nickname)

	byID, err := repo.GetUserByID(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, "new", byID.Nickname)
	assert.Equal(t, "real", byID.RealName)

	byPhone, err := repo.GetUserByPhoneHash(context.Background(), "hash5")
	require.NoError(t, err)
	assert.Equal(t, "new", byPhone.Nickname)
	assert.Equal(t, "real", byPhone.RealName)
}

func TestUserRepo_DeleteUser_ClearsCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewUserRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), "user:6", &biz.User{
		ID:        6,
		Nickname:  "delete_me",
		PhoneHash: "hash6",
		Role:      "user",
	})
	repo.setCache(context.Background(), "user:phone:hash6", &biz.User{
		ID:        6,
		Nickname:  "delete_me",
		PhoneHash: "hash6",
		Role:      "user",
	})

	mockQ.EXPECT().
		DeleteUser(gomock.Any(), int64(6)).
		Times(1).
		Return(nil)

	err := repo.DeleteUser(context.Background(), 6)
	require.NoError(t, err)

	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "user:6").Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "user:phone:hash6").Val())
}
