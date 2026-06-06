package biz

import (
	"context"
	"errors"
	"testing"
	"time"

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

type fakeEventRepo struct {
	createEvent       func(ctx context.Context, name string, status int16, coverImage string, mediaAssets map[string]any, description string, startAt time.Time, endAt time.Time) (*Event, error)
	deleteEvent       func(ctx context.Context, id int64) error
	getEvent          func(ctx context.Context, id int64) (*Event, error)
	listEvents        func(ctx context.Context, status int32, limit int32, offset int32) ([]Event, error)
	updateEvent       func(ctx context.Context, id int64, name string, coverImage string, mediaAssets map[string]any, description string, startAt time.Time, endAt time.Time) (*Event, error)
	updateEventStatus func(ctx context.Context, id int64, status int32) error
}

func (r *fakeEventRepo) CreateEvent(ctx context.Context, name string, status int16, coverImage string, mediaAssets map[string]any, description string, startAt time.Time, endAt time.Time) (*Event, error) {
	return r.createEvent(ctx, name, status, coverImage, mediaAssets, description, startAt, endAt)
}

func (r *fakeEventRepo) DeleteEvent(ctx context.Context, id int64) error {
	return r.deleteEvent(ctx, id)
}

func (r *fakeEventRepo) GetEvent(ctx context.Context, id int64) (*Event, error) {
	return r.getEvent(ctx, id)
}

func (r *fakeEventRepo) ListEvents(ctx context.Context, status int32, limit int32, offset int32) ([]Event, error) {
	return r.listEvents(ctx, status, limit, offset)
}

func (r *fakeEventRepo) UpdateEvent(ctx context.Context, id int64, name string, coverImage string, mediaAssets map[string]any, description string, startAt time.Time, endAt time.Time) (*Event, error) {
	return r.updateEvent(ctx, id, name, coverImage, mediaAssets, description, startAt, endAt)
}

func (r *fakeEventRepo) UpdateEventStatus(ctx context.Context, id int64, status int32) error {
	return r.updateEventStatus(ctx, id, status)
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

func TestEventUsecase_CreateEvent(t *testing.T) {
	startAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	endAt := startAt.Add(2 * time.Hour)
	mediaAssets := map[string]any{"banner": "oss://events/banner.png"}
	repo := &fakeEventRepo{
		createEvent: func(ctx context.Context, name string, status int16, coverImage string, gotMediaAssets map[string]any, description string, gotStartAt time.Time, gotEndAt time.Time) (*Event, error) {
			assert.Equal(t, "launch", name)
			assert.Equal(t, int16(0), status)
			assert.Equal(t, "https://cdn.test/cover.png", coverImage)
			assert.Equal(t, mediaAssets, gotMediaAssets)
			assert.Equal(t, "new launch", description)
			assert.Equal(t, startAt, gotStartAt)
			assert.Equal(t, endAt, gotEndAt)
			return &Event{ID: 11, Name: name, Status: status}, nil
		},
	}
	uc := NewEventUsecase(repo, log.DefaultLogger)

	e, err := uc.CreateEvent(context.Background(), "launch", 0, "https://cdn.test/cover.png", mediaAssets, "new launch", startAt, endAt)
	require.NoError(t, err)
	assert.Equal(t, int64(11), e.ID)
}

func TestEventUsecase_ListEvents_ComputesOffset(t *testing.T) {
	repo := &fakeEventRepo{
		listEvents: func(ctx context.Context, status int32, limit int32, offset int32) ([]Event, error) {
			assert.Equal(t, int32(1), status)
			assert.Equal(t, int32(20), limit)
			assert.Equal(t, int32(40), offset)
			return []Event{{ID: 3, Name: "active"}}, nil
		},
	}
	uc := NewEventUsecase(repo, log.DefaultLogger)

	es, err := uc.ListEvents(context.Background(), 1, 20, 3)
	require.NoError(t, err)
	require.Len(t, es, 1)
	assert.Equal(t, int64(3), es[0].ID)
}

func TestEventUsecase_UpdateEvent(t *testing.T) {
	startAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	endAt := startAt.Add(2 * time.Hour)
	mediaAssets := map[string]any{"banner": "oss://events/banner.png"}
	repo := &fakeEventRepo{
		updateEvent: func(ctx context.Context, id int64, name string, coverImage string, gotMediaAssets map[string]any, description string, gotStartAt time.Time, gotEndAt time.Time) (*Event, error) {
			assert.Equal(t, int64(7), id)
			assert.Equal(t, "launch", name)
			assert.Equal(t, "https://cdn.test/cover.png", coverImage)
			assert.Equal(t, mediaAssets, gotMediaAssets)
			assert.Equal(t, "updated launch", description)
			assert.Equal(t, startAt, gotStartAt)
			assert.Equal(t, endAt, gotEndAt)
			return &Event{ID: id, Name: name}, nil
		},
	}
	uc := NewEventUsecase(repo, log.DefaultLogger)

	e, err := uc.UpdateEvent(context.Background(), 7, "launch", "https://cdn.test/cover.png", mediaAssets, "updated launch", startAt, endAt)
	require.NoError(t, err)
	assert.Equal(t, int64(7), e.ID)
	assert.Equal(t, "launch", e.Name)
}

func TestEventUsecase_UpdateEventStatus_PropagatesError(t *testing.T) {
	wantErr := errors.New("status failed")
	repo := &fakeEventRepo{
		updateEventStatus: func(ctx context.Context, id int64, status int32) error {
			assert.Equal(t, int64(9), id)
			assert.Equal(t, int32(2), status)
			return wantErr
		},
	}
	uc := NewEventUsecase(repo, log.DefaultLogger)

	err := uc.UpdateEventStatus(context.Background(), 9, 2)
	assert.ErrorIs(t, err, wantErr)
}

func TestEventUsecase_DeleteEvent_PropagatesError(t *testing.T) {
	wantErr := errors.New("delete failed")
	repo := &fakeEventRepo{
		deleteEvent: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(8), id)
			return wantErr
		},
	}
	uc := NewEventUsecase(repo, log.DefaultLogger)

	err := uc.DeleteEvent(context.Background(), 8)
	assert.ErrorIs(t, err, wantErr)
}
