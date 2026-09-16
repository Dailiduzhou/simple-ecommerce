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
	bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryGenerationKey(bizCategory.ID))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryListGenerationKey)
	return &bizCategory, nil
}

func (r *CategoryRepo) DeleteCategory(ctx context.Context, id int64) error {
	if _, err := r.GetCategory(ctx, id); err != nil {
		return err
	}
	children, err := r.ListSubCategories(ctx, id)
	if err != nil {
		return err
	}

	if err := r.data.DB(ctx).DeleteCategory(ctx, id); err != nil {
		return err
	}

	bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryGenerationKey(id))
	for i := range children {
		bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryGenerationKey(children[i].ID))
	}
	bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryListGenerationKey)
	return nil
}

func (r *CategoryRepo) GetCategory(ctx context.Context, id int64) (*biz.Category, error) {
	key := generatedEntityCacheKey(ctx, r.data, r.log, categoryGenerationKey(id), redisKey("category", id))
	return cacheAside(ctx, r.data, r.log, key, r.getCache, r.setCache, func() (*biz.Category, error) {
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
	key := generatedEntityCacheKey(ctx, r.data, r.log, categoryListGenerationKey, categoryListCacheKey(parentID))
	return cacheAside(ctx, r.data, r.log, key, r.getListCache, r.setListCache, func() ([]biz.Category, error) {
		rows, err := r.data.DB(ctx).ListSubCategories(ctx, toPgParentID(parentID))
		if err != nil {
			return nil, err
		}
		return toBizCategories(rows), nil
	})
}

func (r *CategoryRepo) ListTopCategories(ctx context.Context) ([]biz.Category, error) {
	key := generatedEntityCacheKey(ctx, r.data, r.log, categoryListGenerationKey, categoryListCacheKey(0))
	return cacheAside(ctx, r.data, r.log, key, r.getListCache, r.setListCache, func() ([]biz.Category, error) {
		rows, err := r.data.DB(ctx).ListTopCategories(ctx)
		if err != nil {
			return nil, err
		}
		return toBizCategories(rows), nil
	})
}

func (r *CategoryRepo) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*biz.Category, error) {
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
	bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryGenerationKey(id))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, categoryListGenerationKey)
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

const categoryListGenerationKey = "category:list:gen"

func categoryGenerationKey(id int64) string {
	return redisKey("category", id, "gen")
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
