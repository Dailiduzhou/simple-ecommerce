package data

import (
	"context"
	stderrors "errors"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	c, err := r.data.DB(ctx).CreateCategory(ctx, db.CreateCategoryParams{
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

	if err := r.data.DB(ctx).DeleteCategory(ctx, id); err != nil {
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
	return cacheAside(ctx, r.data, r.log, redisKey("category", id), r.getCache, r.setCache, func() (*biz.Category, error) {
		row, err := r.data.DB(ctx).GetCategory(ctx, id)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		value := toBizCategory(row)
		return &value, nil
	})
}

func (r *CategoryRepo) ListSubCategories(ctx context.Context, parentID int64) ([]biz.Category, error) {
	return cacheAside(ctx, r.data, r.log, categoryListCacheKey(parentID), r.getListCache, r.setListCache, func() ([]biz.Category, error) {
		rows, err := r.data.DB(ctx).ListSubCategories(ctx, toPgParentID(parentID))
		if err != nil {
			return nil, err
		}
		return toBizCategories(rows), nil
	})
}

func (r *CategoryRepo) ListTopCategories(ctx context.Context) ([]biz.Category, error) {
	return cacheAside(ctx, r.data, r.log, categoryListCacheKey(0), r.getListCache, r.setListCache, func() ([]biz.Category, error) {
		rows, err := r.data.DB(ctx).ListTopCategories(ctx)
		if err != nil {
			return nil, err
		}
		return toBizCategories(rows), nil
	})
}

func (r *CategoryRepo) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*biz.Category, error) {
	oldCategory, err := r.GetCategory(ctx, id)
	if err != nil {
		return nil, err
	}

	c, err := r.data.DB(ctx).UpdateCategory(ctx, db.UpdateCategoryParams{
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
	return readJSONCache[*biz.Category](ctx, r.data, key)
}

func (r *CategoryRepo) getListCache(ctx context.Context, key string) ([]biz.Category, error) {
	return readJSONCache[[]biz.Category](ctx, r.data, key)
}

func (r *CategoryRepo) setCache(ctx context.Context, key string, value *biz.Category) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *CategoryRepo) setListCache(ctx context.Context, key string, value []biz.Category) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *CategoryRepo) deleteCache(ctx context.Context, key string) {
	deleteJSONCache(ctx, r.data, r.log, key)
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
