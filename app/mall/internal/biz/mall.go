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
	CoverImage  string
	MediaAssets []byte
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

type ProductRepo interface {
	CreateProduct(ctx context.Context, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, status int16, coverImage string, mediaAssets []byte, descrption string) (*Product, error)
	DecrProductStock(ctx context.Context, ID int64, amount int32) (int32, error)
	GetProduct(ctx context.Context, id int64) (*Product, error)
	ListProducts(ctx context.Context, limit int32, offset int32) ([]Product, error)
	ListProductsByCategory(ctx context.Context, categoryID int64, limit int32, offset int32) ([]Product, error)
	SoftDeleteProduct(ctx context.Context, id int64) error
	UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, coverImage string, mediaAssets []byte, descrption string) (*Product, error)
	UpdateProductStatus(ctx context.Context, ID int64, status int32) error
}

type ProductUsecase struct {
	repo ProductRepo
	log  *log.Helper
}

func NewProductUsecase(repo ProductRepo, logger log.Logger) *ProductUsecase {
	return &ProductUsecase{repo: repo, log: log.NewHelper(logger)}
}
