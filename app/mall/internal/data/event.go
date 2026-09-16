package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ biz.EventRepo = (*EventRepo)(nil)

type EventRepo struct {
	data *Data
	log  *log.Helper
}

func NewEventRepo(data *Data, logger log.Logger) *EventRepo {
	return &EventRepo{data: data, log: log.NewHelper(logger)}
}

func (r *EventRepo) CreateEvent(ctx context.Context, name string, status int16, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error) {
	coverImageJSON, err := json.Marshal(coverImage)
	if err != nil {
		return nil, err
	}
	mediaAssetsJSON, err := json.Marshal(mediaAssets)
	if err != nil {
		return nil, err
	}

	e, err := r.data.DB(ctx).CreateEvent(ctx, db.CreateEventParams{
		Name:        name,
		Status:      status,
		StartAt:     toPgTimestamp(startAt),
		EndAt:       toPgTimestamp(endAt),
		CoverImage:  coverImageJSON,
		MediaAssets: mediaAssetsJSON,
		Description: pgtype.Text{String: description, Valid: description != ""},
	})
	if err != nil {
		return nil, err
	}

	bizEvent := toBizEvent(e)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, eventGenerationKey(bizEvent.ID))
	r.deleteListCaches(ctx)
	return &bizEvent, nil
}

func (r *EventRepo) DeleteEvent(ctx context.Context, id int64) error {
	if err := r.data.DB(ctx).SoftDeleteEvent(ctx, id); err != nil {
		return err
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, eventGenerationKey(id))
	r.deleteListCaches(ctx)
	return nil
}

func (r *EventRepo) GetEvent(ctx context.Context, id int64) (*biz.Event, error) {
	key := generatedEntityCacheKey(ctx, r.data, r.log, eventGenerationKey(id), eventCacheKey(id))
	return cacheAside(ctx, r.data, r.log, key, r.getCache, r.setCache, func() (*biz.Event, error) {
		row, err := r.data.DB(ctx).GetEvent(ctx, id)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		value := toBizEvent(row)
		return &value, nil
	})
}

func (r *EventRepo) ListEvents(ctx context.Context, status *int32, limit int32, offset int32) ([]biz.Event, error) {
	generation := readCacheGeneration(ctx, r.data.rdb, r.log, "event:list:gen")
	key := generationCacheKey(generation, eventListPresenceCacheKey(generation, status, limit, offset))
	return cacheAside(ctx, r.data, r.log, key, r.getListCache, r.setListCache, func() ([]biz.Event, error) {
		var rows []db.Event
		var err error
		q := r.data.DB(ctx)
		if status != nil {
			rows, err = q.ListEventsByStatus(ctx, db.ListEventsByStatusParams{Status: int16(*status), Limit: limit, Offset: offset})
		} else {
			rows, err = q.ListEvents(ctx, db.ListEventsParams{Limit: limit, Offset: offset})
		}
		if err != nil {
			return nil, err
		}
		return toBizEvents(rows), nil
	})
}

func (r *EventRepo) UpdateEvent(ctx context.Context, id int64, name string, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, description string, startAt time.Time, endAt time.Time) (*biz.Event, error) {
	coverImageJSON, err := json.Marshal(coverImage)
	if err != nil {
		return nil, err
	}
	mediaAssetsJSON, err := json.Marshal(mediaAssets)
	if err != nil {
		return nil, err
	}

	e, err := r.data.DB(ctx).UpdateEvent(ctx, db.UpdateEventParams{
		ID:          id,
		Name:        name,
		StartAt:     toPgTimestamp(startAt),
		EndAt:       toPgTimestamp(endAt),
		CoverImage:  coverImageJSON,
		MediaAssets: mediaAssetsJSON,
		Description: pgtype.Text{String: description, Valid: description != ""},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	bizEvent := toBizEvent(e)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, eventGenerationKey(id))
	r.deleteListCaches(ctx)
	return &bizEvent, nil
}

func (r *EventRepo) UpdateEventStatus(ctx context.Context, id int64, status int32) error {
	if err := r.data.DB(ctx).UpdateEventStatus(ctx, db.UpdateEventStatusParams{
		ID:     id,
		Status: int16(status),
	}); err != nil {
		return err
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, eventGenerationKey(id))
	r.deleteListCaches(ctx)
	return nil
}

func (r *EventRepo) getCache(ctx context.Context, key string) (*biz.Event, error) {
	return readJSONCache[*biz.Event](ctx, r.data, key)
}

func (r *EventRepo) getListCache(ctx context.Context, key string) ([]biz.Event, error) {
	return readJSONCache[[]biz.Event](ctx, r.data, key)
}

func (r *EventRepo) setCache(ctx context.Context, key string, value *biz.Event) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *EventRepo) setListCache(ctx context.Context, key string, value []biz.Event) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *EventRepo) deleteListCaches(ctx context.Context) {
	bumpCacheGeneration(ctx, r.data.rdb, r.log, "event:list:gen")
}

func eventGenerationKey(id int64) string {
	return redisKey("event", id, "gen")
}

func eventCacheKey(id int64) string {
	return redisKey("event", id)
}

func eventListCacheKey(generationOrStatus int64, values ...int32) string {
	var generation int64
	var status, limit, offset int32
	if len(values) == 2 { // legacy test helper form: status, limit, offset
		status, limit, offset = int32(generationOrStatus), values[0], values[1]
	} else if len(values) == 3 {
		generation, status, limit, offset = generationOrStatus, values[0], values[1], values[2]
	} else {
		return "event:list:invalid"
	}
	if status > 0 {
		return redisKey("event", "list", generation, "status", status, limit, offset)
	}
	return redisKey("event", "list", generation, "all", limit, offset)
}

func toPgTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

func toBizEvent(e db.Event) biz.Event {
	return biz.Event{
		ID:          e.ID,
		Name:        e.Name,
		Status:      e.Status,
		CoverImage:  parseMediaInfoJSON(e.CoverImage),
		MediaAssets: parseMediaInfoJSON(e.MediaAssets),
		Description: e.Description.String,
		StartAt:     e.StartAt.Time,
		EndAt:       e.EndAt.Time,
		CreatedAt:   e.CreatedAt.Time,
		UpdatedAt:   e.UpdatedAt.Time,
		DeletedAt:   timePtr(e.DeletedAt),
	}
}

func toBizEvents(es []db.Event) []biz.Event {
	result := make([]biz.Event, len(es))
	for i, e := range es {
		result[i] = toBizEvent(e)
	}
	return result
}

func eventListPresenceCacheKey(generation int64, status *int32, limit, offset int32) string {
	if status == nil {
		return redisKey("event", "list", generation, "all", limit, offset)
	}
	return redisKey("event", "list", generation, "status", *status, limit, offset)
}
