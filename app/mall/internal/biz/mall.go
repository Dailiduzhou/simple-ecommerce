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
	SortOrder int32
	CreatedAt time.Time
	UpdatedAt time.Time
}

type CategoryRepo interface {
	CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error)
	DeleteCategory(ctx context.Context, id int64) error
	GetCategory(ctx context.Context, id int64) (*Category, error)
	ListSubCategories(ctx context.Context, parentID int64) ([]Category, error)
	ListTopCategories(ctx context.Context) ([]Category, error)
	UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error)
}

type CategoryUsecase struct {
	repo CategoryRepo
	log  *log.Helper
}

func NewCategoryUsecase(repo CategoryRepo, logger log.Logger) *CategoryUsecase {
	return &CategoryUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *CategoryUsecase) CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error) {
	return uc.repo.CreateCategory(ctx, parentID, name, sortOrder)
}

func (uc *CategoryUsecase) GetCategory(ctx context.Context, id int64) (*Category, error) {
	return uc.repo.GetCategory(ctx, id)
}

func (uc *CategoryUsecase) ListCategories(ctx context.Context, parentID int64) ([]Category, error) {
	if parentID > 0 {
		return uc.repo.ListSubCategories(ctx, parentID)
	}
	return uc.repo.ListTopCategories(ctx)
}

func (uc *CategoryUsecase) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error) {
	return uc.repo.UpdateCategory(ctx, id, name, sortOrder)
}

func (uc *CategoryUsecase) DeleteCategory(ctx context.Context, id int64) error {
	return uc.repo.DeleteCategory(ctx, id)
}

type Event struct {
	ID          int64
	Name        string
	Status      int16
	CoverImage  []MediaInfo
	MediaAssets []MediaInfo
	Description string
	StartAt     time.Time
	EndAt       time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

type EventRepo interface {
	CreateEvent(ctx context.Context, name string, status int16, coverImage []MediaInfo, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error)
	DeleteEvent(ctx context.Context, id int64) error
	GetEvent(ctx context.Context, id int64) (*Event, error)
	ListEvents(ctx context.Context, status int32, limit int32, offset int32) ([]Event, error)
	UpdateEvent(ctx context.Context, id int64, name string, coverImage []MediaInfo, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error)
	UpdateEventStatus(ctx context.Context, id int64, status int32) error
}

type EventUsecase struct {
	repo EventRepo
	log  *log.Helper
}

func NewEventUsecase(repo EventRepo, logger log.Logger) *EventUsecase {
	return &EventUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *EventUsecase) CreateEvent(ctx context.Context, name string, status int16, coverImage string, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error) {
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.CreateEvent(ctx, name, status, cover, mediaAssets, description, startAt, endAt)
}

func (uc *EventUsecase) GetEvent(ctx context.Context, id int64) (*Event, error) {
	return uc.repo.GetEvent(ctx, id)
}

func (uc *EventUsecase) ListEvents(ctx context.Context, status int32, pageSize int32, page int32) ([]Event, error) {
	offset := (page - 1) * pageSize
	return uc.repo.ListEvents(ctx, status, pageSize, offset)
}

func (uc *EventUsecase) UpdateEvent(ctx context.Context, id int64, name string, coverImage string, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error) {
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.UpdateEvent(ctx, id, name, cover, mediaAssets, description, startAt, endAt)
}

func (uc *EventUsecase) UpdateEventStatus(ctx context.Context, id int64, status int32) error {
	return uc.repo.UpdateEventStatus(ctx, id, status)
}

func (uc *EventUsecase) DeleteEvent(ctx context.Context, id int64) error {
	return uc.repo.DeleteEvent(ctx, id)
}
