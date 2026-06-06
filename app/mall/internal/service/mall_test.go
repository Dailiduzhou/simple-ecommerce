package service

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
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

type fakeEventRepo struct {
	createEvent       func(ctx context.Context, name string, status int16, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error)
	deleteEvent       func(ctx context.Context, id int64) error
	getEvent          func(ctx context.Context, id int64) (*biz.Event, error)
	listEvents        func(ctx context.Context, status int32, limit int32, offset int32) ([]biz.Event, error)
	updateEvent       func(ctx context.Context, id int64, name string, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error)
	updateEventStatus func(ctx context.Context, id int64, status int32) error
}

func (r *fakeEventRepo) CreateEvent(ctx context.Context, name string, status int16, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error) {
	return r.createEvent(ctx, name, status, coverImage, mediaAssets, description, startAt, endAt)
}

func (r *fakeEventRepo) DeleteEvent(ctx context.Context, id int64) error {
	return r.deleteEvent(ctx, id)
}

func (r *fakeEventRepo) GetEvent(ctx context.Context, id int64) (*biz.Event, error) {
	return r.getEvent(ctx, id)
}

func (r *fakeEventRepo) ListEvents(ctx context.Context, status int32, limit int32, offset int32) ([]biz.Event, error) {
	return r.listEvents(ctx, status, limit, offset)
}

func (r *fakeEventRepo) UpdateEvent(ctx context.Context, id int64, name string, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error) {
	return r.updateEvent(ctx, id, name, coverImage, mediaAssets, description, startAt, endAt)
}

func (r *fakeEventRepo) UpdateEventStatus(ctx context.Context, id int64, status int32) error {
	return r.updateEventStatus(ctx, id, status)
}

func newTestMallService(repo biz.CategoryRepo) *MallService {
	return NewMallService(nil, biz.NewCategoryUsecase(repo, log.DefaultLogger), nil, log.DefaultLogger)
}

func newTestMallServiceWithEvent(repo biz.EventRepo) *MallService {
	return NewMallService(nil, nil, biz.NewEventUsecase(repo, log.DefaultLogger), log.DefaultLogger)
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

func TestMallService_CreateEvent(t *testing.T) {
	startAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	endAt := startAt.Add(2 * time.Hour)
	createdAt := startAt.Add(-24 * time.Hour)
	mediaAssets := []*pb.MediaInfo{
		{
			OssUrl:      "oss://events/banner.png",
			BucketName:  "bucket",
			ObjectKey:   "events/banner.png",
			ContentType: "image/png",
			Size:        123,
		},
	}
	wantMediaAssets := []biz.MediaInfo{
		{
			OssURL:      "oss://events/banner.png",
			BucketName:  "bucket",
			ObjectKey:   "events/banner.png",
			ContentType: "image/png",
			Size:        123,
		},
	}
	s := newTestMallServiceWithEvent(&fakeEventRepo{
		createEvent: func(ctx context.Context, name string, status int16, coverImage []biz.MediaInfo, gotMediaAssets []biz.MediaInfo, description string, gotStartAt time.Time, gotEndAt time.Time) (*biz.Event, error) {
			assert.Equal(t, "launch", name)
			assert.Equal(t, int16(0), status)
			assert.Equal(t, []biz.MediaInfo{{OssURL: "https://cdn.test/cover.png"}}, coverImage)
			assert.Equal(t, wantMediaAssets, gotMediaAssets)
			assert.Equal(t, "new launch", description)
			assert.Equal(t, startAt, gotStartAt)
			assert.Equal(t, endAt, gotEndAt)
			return &biz.Event{
				ID:          21,
				Name:        name,
				Status:      status,
				CoverImage:  coverImage,
				MediaAssets: gotMediaAssets,
				Description: description,
				StartAt:     gotStartAt,
				EndAt:       gotEndAt,
				CreatedAt:   createdAt,
			}, nil
		},
	})

	got, err := s.CreateEvent(context.Background(), &pb.CreateEventRequest{
		Name:        "launch",
		CoverImage:  "https://cdn.test/cover.png",
		MediaAssets: mediaAssets,
		Description: "new launch",
		StartAt:     timestamppb.New(startAt),
		EndAt:       timestamppb.New(endAt),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(21), got.Id)
	assert.Equal(t, "launch", got.Name)
	assert.Equal(t, int32(0), got.Status)
	assert.Equal(t, "https://cdn.test/cover.png", got.CoverImage)
	require.Len(t, got.MediaAssets, 1)
	assert.Equal(t, "oss://events/banner.png", got.MediaAssets[0].OssUrl)
	assert.Equal(t, "bucket", got.MediaAssets[0].BucketName)
	assert.Equal(t, "events/banner.png", got.MediaAssets[0].ObjectKey)
	assert.Equal(t, "image/png", got.MediaAssets[0].ContentType)
	assert.Equal(t, int64(123), got.MediaAssets[0].Size)
	assert.Equal(t, startAt, got.StartAt.AsTime())
	assert.Equal(t, endAt, got.EndAt.AsTime())
	assert.Equal(t, createdAt, got.CreatedAt.AsTime())
}

func TestMallService_GetEvent_NotFound(t *testing.T) {
	s := newTestMallServiceWithEvent(&fakeEventRepo{
		getEvent: func(ctx context.Context, id int64) (*biz.Event, error) {
			assert.Equal(t, int64(404), id)
			return nil, nil
		},
	})

	got, err := s.GetEvent(context.Background(), &pb.GetEventRequest{Id: 404})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, pb.IsEventNotFound(err))
}

func TestMallService_ListEvents_DefaultPagination(t *testing.T) {
	startAt := time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
	s := newTestMallServiceWithEvent(&fakeEventRepo{
		listEvents: func(ctx context.Context, status int32, limit int32, offset int32) ([]biz.Event, error) {
			assert.Equal(t, int32(1), status)
			assert.Equal(t, int32(10), limit)
			assert.Equal(t, int32(0), offset)
			return []biz.Event{
				{
					ID:          31,
					Name:        "active event",
					Status:      1,
					CoverImage:  []biz.MediaInfo{{OssURL: "https://cdn.test/active.png"}},
					MediaAssets: []biz.MediaInfo{{OssURL: "oss://events/active.png", BucketName: "bucket"}},
					Description: "active",
					StartAt:     startAt,
					EndAt:       startAt.Add(time.Hour),
				},
			}, nil
		},
	})

	got, err := s.ListEvents(context.Background(), &pb.ListEventsRequest{Status: 1})
	require.NoError(t, err)
	require.Len(t, got.Events, 1)
	assert.Equal(t, int64(31), got.Events[0].Id)
	assert.Equal(t, "active event", got.Events[0].Name)
	assert.Equal(t, "https://cdn.test/active.png", got.Events[0].CoverImage)
	require.Len(t, got.Events[0].MediaAssets, 1)
	assert.Equal(t, "oss://events/active.png", got.Events[0].MediaAssets[0].OssUrl)
	assert.Equal(t, "bucket", got.Events[0].MediaAssets[0].BucketName)
	assert.Equal(t, startAt, got.Events[0].StartAt.AsTime())
}

func TestMallService_UpdateEvent_NotFound(t *testing.T) {
	s := newTestMallServiceWithEvent(&fakeEventRepo{
		updateEvent: func(ctx context.Context, id int64, name string, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error) {
			assert.Equal(t, int64(99), id)
			return nil, nil
		},
	})

	got, err := s.UpdateEvent(context.Background(), &pb.UpdateEventRequest{
		Id:   99,
		Name: "missing",
	})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, pb.IsEventNotFound(err))
}

func TestMallService_UpdateEventStatus(t *testing.T) {
	s := newTestMallServiceWithEvent(&fakeEventRepo{
		updateEventStatus: func(ctx context.Context, id int64, status int32) error {
			assert.Equal(t, int64(21), id)
			assert.Equal(t, int32(2), status)
			return nil
		},
	})

	got, err := s.UpdateEventStatus(context.Background(), &pb.UpdateEventStatusRequest{Id: 21, Status: 2})
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestMallService_DeleteEvent_PropagatesError(t *testing.T) {
	wantErr := errors.New("delete failed")
	s := newTestMallServiceWithEvent(&fakeEventRepo{
		deleteEvent: func(ctx context.Context, id int64) error {
			assert.Equal(t, int64(21), id)
			return wantErr
		},
	})

	got, err := s.DeleteEvent(context.Background(), &pb.DeleteEventRequest{Id: 21})
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, got)
}
