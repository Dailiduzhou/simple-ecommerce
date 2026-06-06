package data

import (
	"context"
	"encoding/json"
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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDBEvent(id int64) db.Event {
	startAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	endAt := startAt.Add(2 * time.Hour)
	now := startAt.Add(-24 * time.Hour)
	return db.Event{
		ID:          id,
		Name:        "launch",
		Status:      1,
		StartAt:     pgtype.Timestamptz{Time: startAt, Valid: true},
		EndAt:       pgtype.Timestamptz{Time: endAt, Valid: true},
		CoverImage:  []byte(`[{"OssURL":"https://cdn.test/cover.png"}]`),
		MediaAssets: []byte(`[{"OssURL":"oss://events/banner.png","BucketName":"bucket","ObjectKey":"events/banner.png","ContentType":"image/png","Size":123}]`),
		Description: pgtype.Text{String: "new launch", Valid: true},
		CreatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:   pgtype.Timestamptz{Time: now, Valid: true},
	}
}

func TestEventRepo_GetEvent_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockEvent := testDBEvent(1)
	mockQ.EXPECT().
		GetEvent(gomock.Any(), int64(1)).
		Times(1).
		Return(mockEvent, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)

	e1, err := repo.GetEvent(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), e1.ID)
	assert.Equal(t, "launch", e1.Name)
	require.Len(t, e1.CoverImage, 1)
	assert.Equal(t, "https://cdn.test/cover.png", e1.CoverImage[0].OssURL)
	require.Len(t, e1.MediaAssets, 1)
	assert.Equal(t, "oss://events/banner.png", e1.MediaAssets[0].OssURL)
	assert.Equal(t, "bucket", e1.MediaAssets[0].BucketName)

	e2, err := repo.GetEvent(context.Background(), 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), e2.ID)
	assert.Equal(t, "launch", e2.Name)
}

func TestEventRepo_GetEvent_Singleflight(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockEvent := testDBEvent(2)
	mockQ.EXPECT().
		GetEvent(gomock.Any(), int64(2)).
		Times(1).
		DoAndReturn(func(ctx context.Context, id int64) (db.Event, error) {
			time.Sleep(100 * time.Millisecond)
			return mockEvent, nil
		})

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)

	const concurrency = 10
	var wg sync.WaitGroup
	wg.Add(concurrency)

	results := make([]*biz.Event, concurrency)
	errs := make([]error, concurrency)
	for i := range concurrency {
		go func(index int) {
			defer wg.Done()
			event, err := repo.GetEvent(context.Background(), 2)
			results[index] = event
			errs[index] = err
		}(i)
	}
	wg.Wait()

	for i := range concurrency {
		require.NoError(t, errs[i])
		assert.Equal(t, int64(2), results[i].ID)
		assert.Equal(t, "launch", results[i].Name)
	}
}

func TestEventRepo_GetEvent_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		GetEvent(gomock.Any(), int64(404)).
		Times(1).
		Return(db.Event{}, pgx.ErrNoRows)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)

	e, err := repo.GetEvent(context.Background(), 404)
	require.NoError(t, err)
	assert.Nil(t, e)
}

func TestEventRepo_ListEvents_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		ListEvents(gomock.Any(), db.ListEventsParams{Limit: 10, Offset: 20}).
		Times(1).
		Return([]db.Event{testDBEvent(3)}, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)

	es1, err := repo.ListEvents(context.Background(), 0, 10, 20)
	require.NoError(t, err)
	require.Len(t, es1, 1)
	assert.Equal(t, int64(3), es1[0].ID)

	es2, err := repo.ListEvents(context.Background(), 0, 10, 20)
	require.NoError(t, err)
	require.Len(t, es2, 1)
	assert.Equal(t, int64(3), es2[0].ID)
}

func TestEventRepo_ListEventsByStatus_CacheHit(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		ListEventsByStatus(gomock.Any(), db.ListEventsByStatusParams{Status: 1, Limit: 10, Offset: 0}).
		Times(1).
		Return([]db.Event{testDBEvent(4)}, nil)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)

	es1, err := repo.ListEvents(context.Background(), 1, 10, 0)
	require.NoError(t, err)
	require.Len(t, es1, 1)
	assert.Equal(t, int64(4), es1[0].ID)

	es2, err := repo.ListEvents(context.Background(), 1, 10, 0)
	require.NoError(t, err)
	require.Len(t, es2, 1)
	assert.Equal(t, int64(4), es2[0].ID)
}

func TestEventRepo_CreateEvent_CachesDetailAndInvalidatesListCache(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)
	repo.setListCache(context.Background(), eventListCacheKey(0, 10, 0), []biz.Event{{ID: 99, Name: "stale"}})
	repo.setListCache(context.Background(), eventListCacheKey(1, 10, 0), []biz.Event{{ID: 98, Name: "stale status"}})

	startAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	endAt := startAt.Add(2 * time.Hour)
	coverImage := []biz.MediaInfo{{OssURL: "https://cdn.test/created.png"}}
	mediaAssets := []biz.MediaInfo{{OssURL: "oss://events/banner.png", BucketName: "bucket"}}
	mockEvent := testDBEvent(5)
	mockEvent.Name = "created"
	mockEvent.Status = 0
	mockEvent.StartAt = pgtype.Timestamptz{Time: startAt, Valid: true}
	mockEvent.EndAt = pgtype.Timestamptz{Time: endAt, Valid: true}
	mockEvent.CoverImage = []byte(`[{"OssURL":"https://cdn.test/created.png"}]`)
	mockEvent.MediaAssets = []byte(`[{"OssURL":"oss://events/banner.png","BucketName":"bucket"}]`)

	mockQ.EXPECT().
		CreateEvent(gomock.Any(), gomock.Any()).
		Times(1).
		DoAndReturn(func(ctx context.Context, arg db.CreateEventParams) (db.Event, error) {
			assert.Equal(t, "created", arg.Name)
			assert.Equal(t, int16(0), arg.Status)
			assert.Equal(t, startAt, arg.StartAt.Time)
			assert.True(t, arg.StartAt.Valid)
			assert.Equal(t, endAt, arg.EndAt.Time)
			assert.True(t, arg.EndAt.Valid)
			assert.Equal(t, pgtype.Text{String: "created event", Valid: true}, arg.Description)

			var gotCoverImage []biz.MediaInfo
			require.NoError(t, json.Unmarshal(arg.CoverImage, &gotCoverImage))
			assert.Equal(t, coverImage, gotCoverImage)

			var gotMediaAssets []biz.MediaInfo
			require.NoError(t, json.Unmarshal(arg.MediaAssets, &gotMediaAssets))
			assert.Equal(t, mediaAssets, gotMediaAssets)
			return mockEvent, nil
		})

	e, err := repo.CreateEvent(context.Background(), "created", 0, coverImage, mediaAssets, "created event", startAt, endAt)
	require.NoError(t, err)
	assert.Equal(t, int64(5), e.ID)
	assert.Equal(t, "created", e.Name)

	cached, err := repo.GetEvent(context.Background(), 5)
	require.NoError(t, err)
	assert.Equal(t, int64(5), cached.ID)
	assert.Equal(t, "created", cached.Name)
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(0, 10, 0)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(1, 10, 0)).Val())
}

func TestEventRepo_UpdateEvent_RefreshesDetailAndInvalidatesListCache(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), eventCacheKey(6), &biz.Event{ID: 6, Name: "old"})
	repo.setListCache(context.Background(), eventListCacheKey(0, 10, 0), []biz.Event{{ID: 6, Name: "stale"}})

	startAt := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	endAt := startAt.Add(2 * time.Hour)
	coverImage := []biz.MediaInfo{{OssURL: "https://cdn.test/updated.png"}}
	mediaAssets := []biz.MediaInfo{{OssURL: "oss://events/updated.png", BucketName: "bucket"}}
	mockEvent := testDBEvent(6)
	mockEvent.Name = "updated"
	mockEvent.StartAt = pgtype.Timestamptz{Time: startAt, Valid: true}
	mockEvent.EndAt = pgtype.Timestamptz{Time: endAt, Valid: true}
	mockEvent.CoverImage = []byte(`[{"OssURL":"https://cdn.test/updated.png"}]`)
	mockEvent.MediaAssets = []byte(`[{"OssURL":"oss://events/updated.png","BucketName":"bucket"}]`)

	mockQ.EXPECT().
		UpdateEvent(gomock.Any(), gomock.Any()).
		Times(1).
		DoAndReturn(func(ctx context.Context, arg db.UpdateEventParams) (db.Event, error) {
			assert.Equal(t, int64(6), arg.ID)
			assert.Equal(t, "updated", arg.Name)
			assert.Equal(t, startAt, arg.StartAt.Time)
			assert.Equal(t, endAt, arg.EndAt.Time)
			var gotCoverImage []biz.MediaInfo
			require.NoError(t, json.Unmarshal(arg.CoverImage, &gotCoverImage))
			assert.Equal(t, coverImage, gotCoverImage)
			var gotMediaAssets []biz.MediaInfo
			require.NoError(t, json.Unmarshal(arg.MediaAssets, &gotMediaAssets))
			assert.Equal(t, mediaAssets, gotMediaAssets)
			return mockEvent, nil
		})

	e, err := repo.UpdateEvent(context.Background(), 6, "updated", coverImage, mediaAssets, "updated event", startAt, endAt)
	require.NoError(t, err)
	assert.Equal(t, "updated", e.Name)

	cached, err := repo.GetEvent(context.Background(), 6)
	require.NoError(t, err)
	assert.Equal(t, "updated", cached.Name)
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(0, 10, 0)).Val())
}

func TestEventRepo_UpdateEvent_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	mockQ.EXPECT().
		UpdateEvent(gomock.Any(), gomock.Any()).
		Times(1).
		Return(db.Event{}, pgx.ErrNoRows)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)

	e, err := repo.UpdateEvent(context.Background(), 404, "missing", nil, nil, "", time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Nil(t, e)
}

func TestEventRepo_UpdateEventStatus_ClearsCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), eventCacheKey(7), &biz.Event{ID: 7, Name: "active"})
	repo.setListCache(context.Background(), eventListCacheKey(0, 10, 0), []biz.Event{{ID: 7, Name: "stale all"}})
	repo.setListCache(context.Background(), eventListCacheKey(1, 10, 0), []biz.Event{{ID: 7, Name: "stale status"}})

	mockQ.EXPECT().
		UpdateEventStatus(gomock.Any(), db.UpdateEventStatusParams{ID: 7, Status: 2}).
		Times(1).
		Return(nil)

	err := repo.UpdateEventStatus(context.Background(), 7, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventCacheKey(7)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(0, 10, 0)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(1, 10, 0)).Val())
}

func TestEventRepo_DeleteEvent_ClearsCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockQ := mockdb.NewMockQuerier(ctrl)
	mr := miniredis.RunT(t)

	d := newTestData(t, mockQ, mr)
	repo := NewEventRepo(d, log.DefaultLogger)
	repo.setCache(context.Background(), eventCacheKey(8), &biz.Event{ID: 8, Name: "delete"})
	repo.setListCache(context.Background(), eventListCacheKey(0, 10, 0), []biz.Event{{ID: 8, Name: "stale all"}})
	repo.setListCache(context.Background(), eventListCacheKey(1, 10, 0), []biz.Event{{ID: 8, Name: "stale status"}})

	mockQ.EXPECT().
		SoftDeleteEvent(gomock.Any(), int64(8)).
		Times(1).
		Return(nil)

	err := repo.DeleteEvent(context.Background(), 8)
	require.NoError(t, err)
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventCacheKey(8)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(0, 10, 0)).Val())
	assert.Equal(t, int64(0), d.rdb.Exists(context.Background(), eventListCacheKey(1, 10, 0)).Val())
}
