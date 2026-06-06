package data

import (
	"context"
	"testing"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCategoryRepo_GetCategory_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockCategory := db.Category{
		ID:        1,
		Name:      "phones",
		SortOrder: 10,
	}

	mockQ.EXPECT().
		GetCategory(gomock.Any(), int64(1)).
		Times(1).
		Return(mockCategory, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewCategoryRepo(d, log.DefaultLogger)

	c1, err := repo.GetCategory(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c1.ID)
	assert.Equal(t, "phones", c1.Name)
	assert.Equal(t, int32(10), c1.SortOrder)

	c2, err := repo.GetCategory(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c2.ID)
	assert.Equal(t, "phones", c2.Name)
	assert.Equal(t, int32(10), c2.SortOrder)
}

func TestCategoryRepo_GetCategory_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		GetCategory(gomock.Any(), int64(404)).
		Times(1).
		Return(db.Category{}, pgx.ErrNoRows)

	d := newTestData(t, mockQ, mr)
	repo := NewCategoryRepo(d, log.DefaultLogger)

	c, err := repo.GetCategory(context.Background(), 404)
	require.NoError(t, err)
	assert.Nil(t, c)
}

func TestCategoryRepo_CreateCategory_InvalidatesListCache(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewCategoryRepo(d, log.DefaultLogger)
	repo.setListCache(context.Background(), categoryListCacheKey(0), []biz.Category{
		{ID: 99, Name: "stale"},
	})

	mockQ.EXPECT().
		CreateCategory(gomock.Any(), db.CreateCategoryParams{
			ParentID:  pgtype.Int8{Valid: false},
			Name:      "phones",
			SortOrder: 10,
		}).
		Times(1).
		Return(db.Category{
			ID:        1,
			Name:      "phones",
			SortOrder: 10,
		}, nil)

	mockQ.EXPECT().
		ListTopCategories(gomock.Any()).
		Times(1).
		Return([]db.Category{
			{ID: 1, Name: "phones", SortOrder: 10},
		}, nil)

	c, err := repo.CreateCategory(context.Background(), 0, "phones", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c.ID)

	cs, err := repo.ListTopCategories(context.Background())
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, int64(1), cs[0].ID)
	assert.Equal(t, "phones", cs[0].Name)
}

func TestCategoryRepo_ListSubCategories_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewCategoryRepo(d, log.DefaultLogger)
	repo.setListCache(context.Background(), categoryListCacheKey(2), []biz.Category{
		{ID: 3, ParentID: 2, Name: "cases", SortOrder: 1},
	})

	cs, err := repo.ListSubCategories(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, int64(3), cs[0].ID)
	assert.Equal(t, int64(2), cs[0].ParentID)
	assert.Equal(t, "cases", cs[0].Name)
}

func TestCategoryRepo_UpdateCategory_InvalidatesParentListCache(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewCategoryRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), "category:3", &biz.Category{
		ID:        3,
		ParentID:  2,
		Name:      "old",
		SortOrder: 1,
	})
	repo.setListCache(context.Background(), categoryListCacheKey(2), []biz.Category{
		{ID: 99, ParentID: 2, Name: "stale"},
	})

	mockQ.EXPECT().
		UpdateCategory(gomock.Any(), db.UpdateCategoryParams{
			ID:        3,
			Name:      "new",
			SortOrder: 4,
		}).
		Times(1).
		Return(db.Category{
			ID:        3,
			ParentID:  pgtype.Int8{Int64: 2, Valid: true},
			Name:      "new",
			SortOrder: 4,
		}, nil)

	mockQ.EXPECT().
		ListSubCategories(gomock.Any(), pgtype.Int8{Int64: 2, Valid: true}).
		Times(1).
		Return([]db.Category{
			{
				ID:        3,
				ParentID:  pgtype.Int8{Int64: 2, Valid: true},
				Name:      "new",
				SortOrder: 4,
			},
		}, nil)

	c, err := repo.UpdateCategory(context.Background(), 3, "new", 4)
	require.NoError(t, err)
	assert.Equal(t, "new", c.Name)

	cached, err := repo.GetCategory(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, "new", cached.Name)
	assert.Equal(t, int32(4), cached.SortOrder)

	cs, err := repo.ListSubCategories(context.Background(), 2)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, "new", cs[0].Name)
}

func TestCategoryRepo_DeleteCategory_ClearsRelatedCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewCategoryRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), "category:3", &biz.Category{
		ID:       3,
		ParentID: 2,
		Name:     "parent",
	})
	repo.setCache(context.Background(), "category:4", &biz.Category{
		ID:       4,
		ParentID: 3,
		Name:     "child",
	})
	repo.setListCache(context.Background(), categoryListCacheKey(0), []biz.Category{{ID: 99, Name: "stale top"}})
	repo.setListCache(context.Background(), categoryListCacheKey(2), []biz.Category{{ID: 3, ParentID: 2, Name: "stale parent list"}})
	repo.setListCache(context.Background(), categoryListCacheKey(3), []biz.Category{{ID: 4, ParentID: 3, Name: "child"}})

	mockQ.EXPECT().
		DeleteCategory(gomock.Any(), int64(3)).
		Times(1).
		Return(nil)

	err := repo.DeleteCategory(context.Background(), 3)
	require.NoError(t, err)

	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "category:3").Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), "category:4").Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), categoryListCacheKey(0)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), categoryListCacheKey(2)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), categoryListCacheKey(3)).Val())
}
