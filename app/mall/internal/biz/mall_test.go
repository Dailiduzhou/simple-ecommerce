package biz

import (
	"context"
	"errors"
	"testing"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCategoryRepo struct {
	createCategory func(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error)
	deleteCategory func(ctx context.Context, id int64) error
	getCategory    func(ctx context.Context, id int64) (*Category, error)
	listSub        func(ctx context.Context, parentID int64) ([]Category, error)
	listTop        func(ctx context.Context) ([]Category, error)
	updateCategory func(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error)
}

func (r *fakeCategoryRepo) CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error) {
	return r.createCategory(ctx, parentID, name, sortOrder)
}

func (r *fakeCategoryRepo) DeleteCategory(ctx context.Context, id int64) error {
	return r.deleteCategory(ctx, id)
}

func (r *fakeCategoryRepo) GetCategory(ctx context.Context, id int64) (*Category, error) {
	return r.getCategory(ctx, id)
}

func (r *fakeCategoryRepo) ListSubCategories(ctx context.Context, parentID int64) ([]Category, error) {
	return r.listSub(ctx, parentID)
}

func (r *fakeCategoryRepo) ListTopCategories(ctx context.Context) ([]Category, error) {
	return r.listTop(ctx)
}

func (r *fakeCategoryRepo) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error) {
	return r.updateCategory(ctx, id, name, sortOrder)
}

func TestCategoryUsecase_CreateCategory(t *testing.T) {
	repo := &fakeCategoryRepo{
		createCategory: func(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error) {
			assert.Equal(t, int64(7), parentID)
			assert.Equal(t, "phones", name)
			assert.Equal(t, int32(10), sortOrder)
			return &Category{ID: 1, ParentID: parentID, Name: name, SortOrder: sortOrder}, nil
		},
	}
	uc := NewCategoryUsecase(repo, log.DefaultLogger)

	c, err := uc.CreateCategory(context.Background(), 7, "phones", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), c.ID)
	assert.Equal(t, int64(7), c.ParentID)
	assert.Equal(t, "phones", c.Name)
	assert.Equal(t, int32(10), c.SortOrder)
}

func TestCategoryUsecase_ListCategories_Top(t *testing.T) {
	repo := &fakeCategoryRepo{
		listTop: func(ctx context.Context) ([]Category, error) {
			return []Category{{ID: 1, Name: "root"}}, nil
		},
		listSub: func(ctx context.Context, parentID int64) ([]Category, error) {
			t.Fatalf("ListSubCategories should not be called for top categories")
			return nil, nil
		},
	}
	uc := NewCategoryUsecase(repo, log.DefaultLogger)

	cs, err := uc.ListCategories(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, int64(1), cs[0].ID)
}

func TestCategoryUsecase_ListCategories_Sub(t *testing.T) {
	repo := &fakeCategoryRepo{
		listTop: func(ctx context.Context) ([]Category, error) {
			t.Fatalf("ListTopCategories should not be called for sub categories")
			return nil, nil
		},
		listSub: func(ctx context.Context, parentID int64) ([]Category, error) {
			assert.Equal(t, int64(5), parentID)
			return []Category{{ID: 2, ParentID: parentID, Name: "cases"}}, nil
		},
	}
	uc := NewCategoryUsecase(repo, log.DefaultLogger)

	cs, err := uc.ListCategories(context.Background(), 5)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, int64(5), cs[0].ParentID)
}

func TestCategoryUsecase_UpdateCategory(t *testing.T) {
	repo := &fakeCategoryRepo{
		updateCategory: func(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error) {
			assert.Equal(t, int64(3), id)
			assert.Equal(t, "updated", name)
			assert.Equal(t, int32(6), sortOrder)
			return &Category{ID: id, Name: name, SortOrder: sortOrder}, nil
		},
	}
	uc := NewCategoryUsecase(repo, log.DefaultLogger)

	c, err := uc.UpdateCategory(context.Background(), 3, "updated", 6)
	require.NoError(t, err)
	assert.Equal(t, int64(3), c.ID)
	assert.Equal(t, "updated", c.Name)
	assert.Equal(t, int32(6), c.SortOrder)
}

func TestCategoryUsecase_DeleteCategory_PropagatesError(t *testing.T) {
	wantErr := errors.New("delete failed")
	repo := &fakeCategoryRepo{
		deleteCategory: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(8), id)
			return wantErr
		},
	}
	uc := NewCategoryUsecase(repo, log.DefaultLogger)

	err := uc.DeleteCategory(context.Background(), 8)
	assert.ErrorIs(t, err, wantErr)
}
