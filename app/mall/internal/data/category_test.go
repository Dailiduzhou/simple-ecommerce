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
