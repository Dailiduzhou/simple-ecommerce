package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	mrand "math/rand"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
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

	e, err := r.data.q.CreateEvent(ctx, db.CreateEventParams{
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
	r.setCache(ctx, eventCacheKey(bizEvent.ID), &bizEvent)
	r.deleteListCaches(ctx)
	return &bizEvent, nil
}

func (r *EventRepo) DeleteEvent(ctx context.Context, id int64) error {
	if err := r.data.q.SoftDeleteEvent(ctx, id); err != nil {
		return err
	}
	r.deleteCache(ctx, eventCacheKey(id))
	r.deleteListCaches(ctx)
	return nil
}

func (r *EventRepo) GetEvent(ctx context.Context, id int64) (*biz.Event, error) {
	cacheKey := eventCacheKey(id)

	e, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return e, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get event cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:%s", cacheKey)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		e, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return e, nil
		}
		dbe, err := r.data.q.GetEvent(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.Event)(nil), nil
			}
			return (*biz.Event)(nil), err
		}
		bizEvent := toBizEvent(dbe)
		r.setCache(ctx, cacheKey, &bizEvent)
		return &bizEvent, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*biz.Event), nil
}

func (r *EventRepo) ListEvents(ctx context.Context, status int32, limit int32, offset int32) ([]biz.Event, error) {
	cacheKey := eventListCacheKey(status, limit, offset)

	es, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return es, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get event list cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:%s", cacheKey)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		es, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return es, nil
		}

		var dbes []db.Event
		if status > 0 {
			dbes, err = r.data.q.ListEventsByStatus(ctx, db.ListEventsByStatusParams{
				Status: int16(status),
				Limit:  limit,
				Offset: offset,
			})
		} else {
			dbes, err = r.data.q.ListEvents(ctx, db.ListEventsParams{
				Limit:  limit,
				Offset: offset,
			})
		}
		if err != nil {
			return nil, err
		}

		bizEvents := toBizEvents(dbes)
		r.setListCache(ctx, cacheKey, bizEvents)
		return bizEvents, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]biz.Event), nil
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

	e, err := r.data.q.UpdateEvent(ctx, db.UpdateEventParams{
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
	r.deleteCache(ctx, eventCacheKey(id))
	r.setCache(ctx, eventCacheKey(id), &bizEvent)
	r.deleteListCaches(ctx)
	return &bizEvent, nil
}

func (r *EventRepo) UpdateEventStatus(ctx context.Context, id int64, status int32) error {
	if err := r.data.q.UpdateEventStatus(ctx, db.UpdateEventStatusParams{
		ID:     id,
		Status: int16(status),
	}); err != nil {
		return err
	}
	r.deleteCache(ctx, eventCacheKey(id))
	r.deleteListCaches(ctx)
	return nil
}

func (r *EventRepo) getCache(ctx context.Context, key string) (*biz.Event, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var e biz.Event
	if err := json.Unmarshal(val, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *EventRepo) getListCache(ctx context.Context, key string) ([]biz.Event, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var es []biz.Event
	if err := json.Unmarshal(val, &es); err != nil {
		return nil, err
	}
	return es, nil
}

func (r *EventRepo) setCache(ctx context.Context, key string, e *biz.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal event cache: %v", err)
		return
	}
	r.data.rdb.Set(ctx, key, data, eventCacheExpiration())
}

func (r *EventRepo) setListCache(ctx context.Context, key string, es []biz.Event) {
	data, err := json.Marshal(es)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal event list cache: %v", err)
		return
	}
	r.data.rdb.Set(ctx, key, data, eventCacheExpiration())
}

func (r *EventRepo) deleteCache(ctx context.Context, key string) {
	if err := r.data.rdb.Del(ctx, key).Err(); err != nil {
		r.log.WithContext(ctx).Errorf("delete cache %s", key)
	}
}

func (r *EventRepo) deleteListCaches(ctx context.Context) {
	iter := r.data.rdb.Scan(ctx, 0, "event:list:*", 0).Iterator()
	for iter.Next(ctx) {
		r.deleteCache(ctx, iter.Val())
	}
	if err := iter.Err(); err != nil {
		r.log.WithContext(ctx).Errorf("scan event list cache: %v", err)
	}
}

func eventCacheKey(id int64) string {
	return fmt.Sprintf("event:%d", id)
}

func eventListCacheKey(status int32, limit int32, offset int32) string {
	if status > 0 {
		return fmt.Sprintf("event:list:status:%d:%d:%d", status, limit, offset)
	}
	return fmt.Sprintf("event:list:all:%d:%d", limit, offset)
}

func eventCacheExpiration() time.Duration {
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	return jitter + 10*time.Minute
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
