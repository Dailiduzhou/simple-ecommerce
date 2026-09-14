package data

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

func (r *ProductRepo) CreateProduct(ctx context.Context, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, status int16, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, descrption string) (*biz.Product, error) {
	minor, err := biz.ProductPriceMinor(price)
	if err != nil {
		return nil, err
	}
	coverImageJSON, err := json.Marshal(coverImage)
	if err != nil {
		return nil, err
	}
	mediaAssetsJSON, err := json.Marshal(mediaAssets)
	if err != nil {
		return nil, err
	}
	p, err := querierFromContext(ctx, r.data.q).CreateProduct(ctx, db.CreateProductParams{
		CategoryID:  categoryID,
		Name:        name,
		PriceMinor:  minor,
		Discount:    discount,
		Stock:       stock,
		Status:      status,
		CoverImage:  coverImageJSON,
		MediaAssets: mediaAssetsJSON,
		Description: pgtype.Text{String: descrption, Valid: descrption != ""},
	})
	if err != nil {
		return nil, err
	}
	bizProduct := toBizProduct(p)
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", bizProduct.ID, "gen"))
	r.invalidateProductLists(ctx, 0, categoryID)
	return &bizProduct, nil
}

func (r *ProductRepo) DecrProductStock(ctx context.Context, ID int64, amount int32) (int32, error) {
	q := querierFromContext(ctx, r.data.q)
	product, err := q.GetProduct(ctx, ID)
	if err != nil {
		return 0, err
	}
	stock, err := q.DecrProductStock(ctx, db.DecrProductStockParams{
		ID:    ID,
		Stock: amount,
	})
	if err != nil {
		return 0, err
	}
	r.deleteCache(ctx, redisKey("product", ID))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", ID, "gen"))
	r.invalidateProductLists(ctx, product.CategoryID)
	return stock, nil
}

func (r *ProductRepo) GetProduct(ctx context.Context, id int64) (*biz.Product, error) {
	generation := readCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", id, "gen"))
	key := generationCacheKey(generation, redisKey("product", id, "g", generation))
	return cacheAside(ctx, r.data, r.log, key, r.getCache, r.setCache, func() (*biz.Product, error) {
		row, err := r.data.DB(ctx).GetProduct(ctx, id)
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		value := toBizProduct(row)
		return &value, nil
	})
}

func (r *ProductRepo) ListProducts(ctx context.Context, limit int32, offset int32) ([]biz.Product, error) {
	generation := readCacheGeneration(ctx, r.data.rdb, r.log, "product:list:gen")
	key := generationCacheKey(generation, redisKey("product", "list", generation, limit, offset))
	return cacheAside(ctx, r.data, r.log, key, r.getListCache, r.setListCache, func() ([]biz.Product, error) {
		rows, err := r.data.DB(ctx).ListProducts(ctx, db.ListProductsParams{Limit: limit, Offset: offset})
		if err != nil {
			return nil, err
		}
		return toBizProducts(rows), nil
	})
}

func (r *ProductRepo) ListProductsByCategory(ctx context.Context, categoryID int64, limit int32, offset int32) ([]biz.Product, error) {
	generation := readCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", "category", categoryID, "gen"))
	key := generationCacheKey(generation, redisKey("product", "category", categoryID, generation, limit, offset))
	return cacheAside(ctx, r.data, r.log, key, r.getListCache, r.setListCache, func() ([]biz.Product, error) {
		rows, err := r.data.DB(ctx).ListProductsByCategory(ctx, db.ListProductsByCategoryParams{CategoryID: categoryID, Limit: limit, Offset: offset})
		if err != nil {
			return nil, err
		}
		return toBizProducts(rows), nil
	})
}

func (r *ProductRepo) SoftDeleteProduct(ctx context.Context, id int64) error {
	q := querierFromContext(ctx, r.data.q)
	existing, err := q.GetProduct(ctx, id)
	if err != nil {
		return err
	}
	err = q.SoftDeleteProduct(ctx, id)
	if err != nil {
		return err
	}
	r.deleteCache(ctx, redisKey("product", id))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", id, "gen"))
	r.invalidateProductLists(ctx, existing.CategoryID)
	return nil
}

func (r *ProductRepo) UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, coverImage []biz.MediaInfo, mediaAssets []biz.MediaInfo, descrption string) (*biz.Product, error) {
	minor, err := biz.ProductPriceMinor(price)
	if err != nil {
		return nil, err
	}
	coverImageJSON, err := json.Marshal(coverImage)
	if err != nil {
		return nil, err
	}
	mediaAssetsJSON, err := json.Marshal(mediaAssets)
	if err != nil {
		return nil, err
	}
	q := querierFromContext(ctx, r.data.q)
	existing, err := q.GetProduct(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := q.UpdateProduct(ctx, db.UpdateProductParams{
		ID:          id,
		CategoryID:  categoryID,
		Name:        name,
		PriceMinor:  minor,
		Discount:    discount,
		Stock:       stock,
		CoverImage:  coverImageJSON,
		MediaAssets: mediaAssetsJSON,
		Description: pgtype.Text{String: descrption, Valid: descrption != ""},
	})
	if err != nil {
		return nil, err
	}
	bizProduct := toBizProduct(p)
	r.deleteCache(ctx, redisKey("product", id))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", id, "gen"))
	r.invalidateProductLists(ctx, existing.CategoryID, categoryID)
	return &bizProduct, nil
}

func (r *ProductRepo) UpdateProductStatus(ctx context.Context, ID int64, status int32) error {
	q := querierFromContext(ctx, r.data.q)
	existing, err := q.GetProduct(ctx, ID)
	if err != nil {
		return err
	}
	err = q.UpdateProductStatus(ctx, db.UpdateProductStatusParams{
		ID:     ID,
		Status: int16(status),
	})
	if err != nil {
		return err
	}
	r.deleteCache(ctx, redisKey("product", ID))
	bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", ID, "gen"))
	r.invalidateProductLists(ctx, existing.CategoryID)
	return nil
}

func (r *ProductRepo) getCache(ctx context.Context, key string) (*biz.Product, error) {
	return readJSONCache[*biz.Product](ctx, r.data, key)
}

func (r *ProductRepo) getListCache(ctx context.Context, key string) ([]biz.Product, error) {
	return readJSONCache[[]biz.Product](ctx, r.data, key)
}

func (r *ProductRepo) setCache(ctx context.Context, key string, value *biz.Product) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *ProductRepo) setListCache(ctx context.Context, key string, value []biz.Product) {
	writeJSONCache(ctx, r.data, r.log, key, value, cacheTTL())
}

func (r *ProductRepo) deleteCache(ctx context.Context, key string) {
	deleteJSONCache(ctx, r.data, r.log, key)
}

func (r *ProductRepo) invalidateProductLists(ctx context.Context, categoryIDs ...int64) {
	bumpCacheGeneration(ctx, r.data.rdb, r.log, "product:list:gen")
	seen := make(map[int64]struct{}, len(categoryIDs))
	for _, categoryID := range categoryIDs {
		if categoryID <= 0 {
			continue
		}
		if _, ok := seen[categoryID]; ok {
			continue
		}
		seen[categoryID] = struct{}{}
		bumpCacheGeneration(ctx, r.data.rdb, r.log, redisKey("product", "category", categoryID, "gen"))
	}
}

func toBizProduct(p db.Product) biz.Product {
	return biz.Product{
		ID:          p.ID,
		CategoryID:  p.CategoryID,
		Name:        p.Name,
		Price:       decimal.NewFromInt(p.PriceMinor).Shift(-2),
		Discount:    p.Discount,
		Stock:       p.Stock,
		Status:      p.Status,
		CoverImage:  parseMediaInfoJSON(p.CoverImage),
		MediaAssets: parseMediaInfoJSON(p.MediaAssets),
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

func parseMediaInfoJSON(data []byte) []biz.MediaInfo {
	if len(data) == 0 {
		return nil
	}
	var result []biz.MediaInfo
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

func (r *ProductRepo) CountProducts(ctx context.Context, categoryID int64) (int64, error) {
	if categoryID > 0 {
		return r.data.DB(ctx).CountProductsByCategory(ctx, categoryID)
	}
	return r.data.DB(ctx).CountProducts(ctx)
}
