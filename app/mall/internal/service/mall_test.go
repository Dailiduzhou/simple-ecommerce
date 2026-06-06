package service

import (
	"context"
	"errors"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCategoryRepo struct {
	createCategory func(ctx context.Context, parentID int64, name string, sortOrder int32) (*biz.Category, error)
	deleteCategory func(ctx context.Context, id int64) error
	getCategory    func(ctx context.Context, id int64) (*biz.Category, error)
	listSub        func(ctx context.Context, parentID int64) ([]biz.Category, error)
	listTop        func(ctx context.Context) ([]biz.Category, error)
	updateCategory func(ctx context.Context, id int64, name string, sortOrder int32) (*biz.Category, error)
}

func (r *fakeCategoryRepo) CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*biz.Category, error) {
	return r.createCategory(ctx, parentID, name, sortOrder)
}

func (r *fakeCategoryRepo) DeleteCategory(ctx context.Context, id int64) error {
	return r.deleteCategory(ctx, id)
}

func (r *fakeCategoryRepo) GetCategory(ctx context.Context, id int64) (*biz.Category, error) {
	return r.getCategory(ctx, id)
}

func (r *fakeCategoryRepo) ListSubCategories(ctx context.Context, parentID int64) ([]biz.Category, error) {
	return r.listSub(ctx, parentID)
}

func (r *fakeCategoryRepo) ListTopCategories(ctx context.Context) ([]biz.Category, error) {
	return r.listTop(ctx)
}

func (r *fakeCategoryRepo) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*biz.Category, error) {
	return r.updateCategory(ctx, id, name, sortOrder)
}

func newTestMallService(repo biz.CategoryRepo) *MallService {
	return NewMallService(nil, biz.NewCategoryUsecase(repo, log.DefaultLogger), log.DefaultLogger)
}

func TestMallService_CreateCategory(t *testing.T) {
	s := newTestMallService(&fakeCategoryRepo{
		createCategory: func(ctx context.Context, parentID int64, name string, sortOrder int32) (*biz.Category, error) {
			assert.Equal(t, int64(7), parentID)
			assert.Equal(t, "phones", name)
			assert.Equal(t, int32(10), sortOrder)
			return &biz.Category{ID: 1, ParentID: parentID, Name: name, SortOrder: sortOrder}, nil
		},
	})

	got, err := s.CreateCategory(context.Background(), &pb.CreateCategoryRequest{
		ParentId:  7,
		Name:      "phones",
		SortOrder: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Id)
	assert.Equal(t, int64(7), got.ParentId)
	assert.Equal(t, "phones", got.Name)
	assert.Equal(t, int32(10), got.SortOrder)
}

func TestMallService_ListCategories(t *testing.T) {
	s := newTestMallService(&fakeCategoryRepo{
		listSub: func(ctx context.Context, parentID int64) ([]biz.Category, error) {
			assert.Equal(t, int64(3), parentID)
			return []biz.Category{
				{ID: 4, ParentID: parentID, Name: "cases", SortOrder: 2},
				{ID: 5, ParentID: parentID, Name: "chargers", SortOrder: 3},
			}, nil
		},
		listTop: func(ctx context.Context) ([]biz.Category, error) {
			t.Fatalf("ListTopCategories should not be called for parent_id > 0")
			return nil, nil
		},
	})

	got, err := s.ListCategories(context.Background(), &pb.ListCategoriesRequest{ParentId: 3})
	require.NoError(t, err)
	require.Len(t, got.Categories, 2)
	assert.Equal(t, int64(4), got.Categories[0].Id)
	assert.Equal(t, "cases", got.Categories[0].Name)
	assert.Equal(t, int32(2), got.Categories[0].SortOrder)
	assert.Equal(t, int64(5), got.Categories[1].Id)
}

func TestMallService_UpdateCategory_NotFound(t *testing.T) {
	s := newTestMallService(&fakeCategoryRepo{
		updateCategory: func(ctx context.Context, id int64, name string, sortOrder int32) (*biz.Category, error) {
			assert.Equal(t, int64(9), id)
			return nil, nil
		},
	})

	got, err := s.UpdateCategory(context.Background(), &pb.UpdateCategoryRequest{
		Id:        9,
		Name:      "missing",
		SortOrder: 1,
	})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, pb.IsCategoryNotFound(err))
}

func TestMallService_DeleteCategory(t *testing.T) {
	s := newTestMallService(&fakeCategoryRepo{
		deleteCategory: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(3), id)
			return nil
		},
	})

	got, err := s.DeleteCategory(context.Background(), &pb.DeleteCategoryRequest{Id: 3})
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestMallService_DeleteCategory_PropagatesError(t *testing.T) {
	wantErr := errors.New("delete failed")
	s := newTestMallService(&fakeCategoryRepo{
		deleteCategory: func(ctx context.Context, id int64) error {
			return wantErr
		},
	})

	got, err := s.DeleteCategory(context.Background(), &pb.DeleteCategoryRequest{Id: 3})
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}
