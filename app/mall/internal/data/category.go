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

var _ biz.CategoryRepo = (*CategoryRepo)(nil)

type CategoryRepo struct {
	data *Data
	log  *log.Helper
}

func NewCategoryRepo(data *Data, logger log.Logger) *CategoryRepo {
	return &CategoryRepo{data: data, log: log.NewHelper(logger)}
}

func (r *CategoryRepo) CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*biz.Category, error) {
	c, err := r.data.q.CreateCategory(ctx, db.CreateCategoryParams{
		ParentID:  toPgParentID(parentID),
		Name:      name,
		SortOrder: sortOrder,
	})
	if err != nil {
		return nil, err
	}
	bizCategory := toBizCategory(c)
	r.setCache(ctx, redisKey("category", bizCategory.ID), &bizCategory)
	r.deleteListCache(ctx, parentID)
	return &bizCategory, nil
}

func (r *CategoryRepo) DeleteCategory(ctx context.Context, id int64) error {
	c, err := r.GetCategory(ctx, id)
	if err != nil {
		return err
	}
	children, err := r.ListSubCategories(ctx, id)
	if err != nil {
		return err
	}

	if err := r.data.q.DeleteCategory(ctx, id); err != nil {
		return err
	}

	r.deleteCache(ctx, redisKey("category", id))
	for i := range children {
		r.deleteCache(ctx, redisKey("category", children[i].ID))
	}
	r.deleteListCache(ctx, id)
	r.deleteListCache(ctx, 0)
	if c != nil {
		r.deleteListCache(ctx, c.ParentID)
	}
	return nil
}

func (r *CategoryRepo) GetCategory(ctx context.Context, id int64) (*biz.Category, error) {
	cacheKey := redisKey("category", id)

	c, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return c, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get category cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:category:%d", id)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		c, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return c, nil
		}
		dbc, err := r.data.q.GetCategory(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.Category)(nil), nil
			}
			return (*biz.Category)(nil), err
		}
		bizCategory := toBizCategory(dbc)
		r.setCache(ctx, cacheKey, &bizCategory)
		return &bizCategory, nil
	})
	if err != nil {
		return nil, err
	}
	return val.(*biz.Category), nil
}

func (r *CategoryRepo) ListSubCategories(ctx context.Context, parentID int64) ([]biz.Category, error) {
	cacheKey := categoryListCacheKey(parentID)

	cs, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return cs, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get category sub list cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:%s", cacheKey)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		cs, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return cs, nil
		}
		dbcs, err := r.data.q.ListSubCategories(ctx, toPgParentID(parentID))
		if err != nil {
			return nil, err
		}
		bizCategories := toBizCategories(dbcs)
		r.setListCache(ctx, cacheKey, bizCategories)
		return bizCategories, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]biz.Category), nil
}

func (r *CategoryRepo) ListTopCategories(ctx context.Context) ([]biz.Category, error) {
	cacheKey := categoryListCacheKey(0)

	cs, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return cs, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get category top list cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:%s", cacheKey)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		cs, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return cs, nil
		}
		dbcs, err := r.data.q.ListTopCategories(ctx)
		if err != nil {
			return nil, err
		}
		bizCategories := toBizCategories(dbcs)
		r.setListCache(ctx, cacheKey, bizCategories)
		return bizCategories, nil
	})
	if err != nil {
		return nil, err
	}
	return val.([]biz.Category), nil
}

func (r *CategoryRepo) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*biz.Category, error) {
	oldCategory, err := r.GetCategory(ctx, id)
	if err != nil {
		return nil, err
	}

	c, err := r.data.q.UpdateCategory(ctx, db.UpdateCategoryParams{
		ID:        id,
		Name:      name,
		SortOrder: sortOrder,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	bizCategory := toBizCategory(c)
	r.deleteCache(ctx, redisKey("category", id))
	r.setCache(ctx, redisKey("category", id), &bizCategory)
	if oldCategory != nil {
		r.deleteListCache(ctx, oldCategory.ParentID)
	}
	r.deleteListCache(ctx, bizCategory.ParentID)
	return &bizCategory, nil
}

func (r *CategoryRepo) getCache(ctx context.Context, key string) (*biz.Category, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var c biz.Category
	if err := json.Unmarshal(val, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *CategoryRepo) getListCache(ctx context.Context, key string) ([]biz.Category, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var cs []biz.Category
	if err := json.Unmarshal(val, &cs); err != nil {
		return nil, err
	}
	return cs, nil
}

func (r *CategoryRepo) setCache(ctx context.Context, key string, c *biz.Category) {
	data, err := json.Marshal(c)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal category cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *CategoryRepo) setListCache(ctx context.Context, key string, cs []biz.Category) {
	data, err := json.Marshal(cs)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal category list cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *CategoryRepo) deleteCache(ctx context.Context, key string) {
	if err := r.data.rdb.Unlink(ctx, key).Err(); err != nil {
		r.log.WithContext(ctx).Errorf("delete cache %s", key)
		return
	}
}

func (r *CategoryRepo) deleteListCache(ctx context.Context, parentID int64) {
	r.deleteCache(ctx, categoryListCacheKey(parentID))
}

func categoryListCacheKey(parentID int64) string {
	if parentID > 0 {
		return redisKey("category", "list", parentID)
	}
	return "category:list:top"
}

func toPgParentID(parentID int64) pgtype.Int8 {
	return pgtype.Int8{Int64: parentID, Valid: parentID > 0}
}

func toBizCategory(c db.Category) biz.Category {
	return biz.Category{
		ID:        c.ID,
		ParentID:  c.ParentID.Int64,
		Name:      c.Name,
		SortOrder: c.SortOrder,
		CreatedAt: c.CreatedAt.Time,
		UpdatedAt: c.UpdatedAt.Time,
	}
}

func toBizCategories(cs []db.Category) []biz.Category {
	result := make([]biz.Category, len(cs))
	for i, c := range cs {
		result[i] = toBizCategory(c)
	}
	return result
}
