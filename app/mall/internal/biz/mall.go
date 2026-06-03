package biz

import (
	"context"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
)

type Product struct {
	ID          int64
	CategoryID  int64
	Name        string
	Price       decimal.Decimal
	Discount    decimal.Decimal
	Stock       int32
	Status      int16
	CoverImage  []MediaInfo
	MediaAssets []MediaInfo
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

type MediaInfo struct {
	OssURL      string
	BucketName  string
	ObjectKey   string
	ContentType string
	Size        int64
}

type ProductRepo interface {
	CreateProduct(ctx context.Context, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, status int16, coverImage []MediaInfo, mediaAssets []MediaInfo, descrption string) (*Product, error)
	DecrProductStock(ctx context.Context, ID int64, amount int32) (int32, error)
	GetProduct(ctx context.Context, id int64) (*Product, error)
	ListProducts(ctx context.Context, limit int32, offset int32) ([]Product, error)
	ListProductsByCategory(ctx context.Context, categoryID int64, limit int32, offset int32) ([]Product, error)
	SoftDeleteProduct(ctx context.Context, id int64) error
	UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, coverImage []MediaInfo, mediaAssets []MediaInfo, descrption string) (*Product, error)
	UpdateProductStatus(ctx context.Context, ID int64, status int32) error
}

type ProductUsecase struct {
	repo ProductRepo
	log  *log.Helper
}

func NewProductUsecase(repo ProductRepo, logger log.Logger) *ProductUsecase {
	return &ProductUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *ProductUsecase) CreateProduct(ctx context.Context, categoryID int64, name string, priceStr string, discountStr string, stock int32, status int16, coverImage string, mediaAssets []MediaInfo, descrption string) (*Product, error) {
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid price: %v", err)
		return nil, err
	}
	discount, err := decimal.NewFromString(discountStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid discount: %v", err)
		return nil, err
	}
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.CreateProduct(ctx, categoryID, name, price, discount, stock, status, cover, mediaAssets, descrption)
}

func (uc *ProductUsecase) GetProduct(ctx context.Context, id int64) (*Product, error) {
	return uc.repo.GetProduct(ctx, id)
}

func (uc *ProductUsecase) ListProducts(ctx context.Context, categoryID int64, pageSize int32, page int32) ([]Product, int32, error) {
	offset := (page - 1) * pageSize
	if categoryID > 0 {
		ps, err := uc.repo.ListProductsByCategory(ctx, categoryID, pageSize, offset)
		return ps, pageSize, err
	}
	ps, err := uc.repo.ListProducts(ctx, pageSize, offset)
	return ps, pageSize, err
}

func (uc *ProductUsecase) UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, priceStr string, discountStr string, stock int32, coverImage string, mediaAssets []MediaInfo, descrption string) (*Product, error) {
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid price: %v", err)
		return nil, err
	}
	discount, err := decimal.NewFromString(discountStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid discount: %v", err)
		return nil, err
	}
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.UpdateProduct(ctx, id, categoryID, name, price, discount, stock, cover, mediaAssets, descrption)
}

func (uc *ProductUsecase) UpdateProductStatus(ctx context.Context, id int64, status int32) error {
	return uc.repo.UpdateProductStatus(ctx, id, status)
}

func (uc *ProductUsecase) DeleteProduct(ctx context.Context, id int64) error {
	return uc.repo.SoftDeleteProduct(ctx, id)
}

func mediaFromCoverURL(coverURL string) []MediaInfo {
	if coverURL == "" {
		return nil
	}
	return []MediaInfo{{OssURL: coverURL}}
}

type Category struct {
	ID        int64
	ParentID  int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CategoryRepo interface {
	CreateCategory(ctx context.Context, parentID int64, name string) (Category, error)
	DeleteCategory(ctx context.Context, id int64) error
	GetCategory(ctx context.Context, id int64) (Category, error)
	ListSubCategories(ctx context.Context, parentID int64) ([]Category, error)
	ListTopCategories(ctx context.Context) ([]Category, error)
	UpdateCategory(ctx context.Context, id int64, name string) (Category, error)
}

type CategoryUsecase struct {
	repo CategoryRepo
	log  *log.Helper
}

func NewCategoryUsecase(repo CategoryRepo, logger *log.Logger) *CategoryUsecase {
	return &CategoryUsecase{repo: repo, log: log.NewHelper(*logger)}
}
