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
	"github.com/shopspring/decimal"
)

var _ biz.ProductRepo = (*ProductRepo)(nil)

type ProductRepo struct {
	data *Data
	log  *log.Helper
}

func NewProductRepo(data *Data, logger log.Logger) *ProductRepo {
	return &ProductRepo{data: data, log: log.NewHelper(logger)}
}

func (r *ProductRepo) CreateProduct(ctx context.Context, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, status int16, coverImage string, mediaAssets []byte, descrption string) (*biz.Product, error) {
	p, err := r.data.q.CreateProduct(ctx, db.CreateProductParams{
		CategoryID:  categoryID,
		Name:        name,
		Price:       price,
		Discount:    discount,
		Stock:       stock,
		Status:      status,
		CoverImage:  coverImage,
		MediaAssets: mediaAssets,
		Description: pgtype.Text{String: descrption, Valid: descrption != ""},
	})
	if err != nil {
		return nil, err
	}
	bizProduct := toBizProduct(p)
	r.setCache(ctx, fmt.Sprintf("product:%d", bizProduct.ID), &bizProduct)
	return &bizProduct, nil
}

func (r *ProductRepo) DecrProductStock(ctx context.Context, ID int64, amount int32) (int32, error) {
	stock, err := r.data.q.DecrProductStock(ctx, db.DecrProductStockParams{
		ID:    ID,
		Stock: amount,
	})
	if err != nil {
		return 0, err
	}
	r.deleteCache(ctx, fmt.Sprintf("product:%d", ID))
	return stock, nil
}

func (r *ProductRepo) GetProduct(ctx context.Context, id int64) (*biz.Product, error) {
	cacheKey := fmt.Sprintf("product:%d", id)

	p, err := r.getCache(ctx, cacheKey)
	if err == nil {
		return p, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get product cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:product:%d", id)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		p, err := r.getCache(ctx, cacheKey)
		if err == nil {
			return p, nil
		}
		dbp, err := r.data.q.GetProduct(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return (*biz.Product)(nil), nil
			}
			return (*biz.Product)(nil), err
		}
		bizProduct := toBizProduct(dbp)
		r.setCache(ctx, cacheKey, &bizProduct)
		return &bizProduct, nil
	})

	if err != nil {
		return nil, err
	}

	return val.(*biz.Product), nil
}

func (r *ProductRepo) ListProducts(ctx context.Context, limit int32, offset int32) ([]biz.Product, error) {
	cacheKey := fmt.Sprintf("product:list:%d:%d", limit, offset)

	ps, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return ps, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get product list cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:product:list:%d:%d", limit, offset)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		ps, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return ps, nil
		}
		dbps, err := r.data.q.ListProducts(ctx, db.ListProductsParams{
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		bizProducts := toBizProducts(dbps)
		r.setListCache(ctx, cacheKey, bizProducts)
		return bizProducts, nil
	})

	if err != nil {
		return nil, err
	}

	return val.([]biz.Product), nil
}

func (r *ProductRepo) ListProductsByCategory(ctx context.Context, categoryID int64, limit int32, offset int32) ([]biz.Product, error) {
	cacheKey := fmt.Sprintf("product:cat:%d:%d:%d", categoryID, limit, offset)

	ps, err := r.getListCache(ctx, cacheKey)
	if err == nil {
		return ps, nil
	}
	if !stderrors.Is(err, redis.Nil) {
		r.log.WithContext(ctx).Errorf("get product category list cache: %v", err)
	}

	sfKey := fmt.Sprintf("sf:product:cat:%d:%d:%d", categoryID, limit, offset)
	val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
		ps, err := r.getListCache(ctx, cacheKey)
		if err == nil {
			return ps, nil
		}
		dbps, err := r.data.q.ListProductsByCategory(ctx, db.ListProductsByCategoryParams{
			CategoryID: categoryID,
			Limit:      limit,
			Offset:     offset,
		})
		if err != nil {
			return nil, err
		}
		bizProducts := toBizProducts(dbps)
		r.setListCache(ctx, cacheKey, bizProducts)
		return bizProducts, nil
	})

	if err != nil {
		return nil, err
	}

	return val.([]biz.Product), nil
}

func (r *ProductRepo) SoftDeleteProduct(ctx context.Context, id int64) error {
	err := r.data.q.SoftDeleteProduct(ctx, id)
	if err != nil {
		return err
	}
	r.deleteCache(ctx, fmt.Sprintf("product:%d", id))
	return nil
}

func (r *ProductRepo) UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, coverImage string, mediaAssets []byte, descrption string) (*biz.Product, error) {
	p, err := r.data.q.UpdateProduct(ctx, db.UpdateProductParams{
		ID:          id,
		CategoryID:  categoryID,
		Name:        name,
		Price:       price,
		Discount:    discount,
		Stock:       stock,
		CoverImage:  coverImage,
		MediaAssets: mediaAssets,
		Description: pgtype.Text{String: descrption, Valid: descrption != ""},
	})
	if err != nil {
		return nil, err
	}
	bizProduct := toBizProduct(p)
	r.deleteCache(ctx, fmt.Sprintf("product:%d", id))
	r.setCache(ctx, fmt.Sprintf("product:%d", id), &bizProduct)
	return &bizProduct, nil
}

func (r *ProductRepo) UpdateProductStatus(ctx context.Context, ID int64, status int32) error {
	err := r.data.q.UpdateProductStatus(ctx, db.UpdateProductStatusParams{
		ID:     ID,
		Status: int16(status),
	})
	if err != nil {
		return err
	}
	r.deleteCache(ctx, fmt.Sprintf("product:%d", ID))
	return nil
}

func (r *ProductRepo) getCache(ctx context.Context, key string) (*biz.Product, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var p biz.Product
	if err := json.Unmarshal(val, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ProductRepo) getListCache(ctx context.Context, key string) ([]biz.Product, error) {
	val, err := r.data.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var ps []biz.Product
	if err := json.Unmarshal(val, &ps); err != nil {
		return nil, err
	}
	return ps, nil
}

func (r *ProductRepo) setCache(ctx context.Context, key string, p *biz.Product) {
	data, err := json.Marshal(p)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal product cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *ProductRepo) setListCache(ctx context.Context, key string, ps []biz.Product) {
	data, err := json.Marshal(ps)
	if err != nil {
		r.log.WithContext(ctx).Errorf("marshal product list cache: %v", err)
		return
	}
	jitter := time.Duration(mrand.Intn(10)) * time.Minute
	exp := jitter + 10*time.Minute
	r.data.rdb.Set(ctx, key, data, exp)
}

func (r *ProductRepo) deleteCache(ctx context.Context, key string) {
	if err := r.data.rdb.Del(ctx, key).Err(); err != nil {
		r.log.WithContext(ctx).Errorf("delete cache %s", key)
		return
	}
}

func toBizProduct(p db.Product) biz.Product {
	return biz.Product{
		ID:          p.ID,
		CategoryID:  p.CategoryID,
		Name:        p.Name,
		Price:       p.Price,
		Discount:    p.Discount,
		Stock:       p.Stock,
		Status:      p.Status,
		CoverImage:  p.CoverImage,
		MediaAssets: p.MediaAssets,
		Description: p.Description.String,
		CreatedAt:   p.CreatedAt.Time,
		UpdatedAt:   p.UpdatedAt.Time,
		DeletedAt:   timePtr(p.DeletedAt),
	}
}

func toBizProducts(ps []db.Product) []biz.Product {
	result := make([]biz.Product, len(ps))
	for i, p := range ps {
		result[i] = toBizProduct(p)
	}
	return result
}

func timePtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
