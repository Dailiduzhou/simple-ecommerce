package biz

import (
	"context"
	"math"
	"time"

	mallv1 "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/shopspring/decimal"
)

// ProductPriceMinor validates before converting: decimal.IntPart truncates
// overflowing big integers instead of reporting an error.
func ProductPriceMinor(price decimal.Decimal) (int64, error) {
	minor := price.Shift(2)
	if minor.IsNegative() || !minor.Equal(minor.Truncate(0)) || minor.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.BadRequest("PRODUCT_PRICE_INVALID", "price must be non-negative, use at most two decimal places and fit int64 minor units")
	}
	return minor.IntPart(), nil
}

// ProductDiscount validates the discount rate: a rate outside (0, 1] would
// either raise the charged price above the list price or zero it out, so
// both are rejected instead of silently mischarging.
func ProductDiscount(discount decimal.Decimal) error {
	if !discount.IsPositive() || discount.GreaterThan(decimal.NewFromInt(1)) {
		return errors.BadRequest("PRODUCT_DISCOUNT_INVALID", "discount must be greater than 0 and at most 1")
	}
	return nil
}

// EffectivePriceMinor applies the discount rate to a minor-unit price with
// half-up rounding. Order pricing and product responses must both go through
// this helper so the displayed price can never diverge from the charged one.
// A result of zero is legal here: order creation already rejects non-positive
// totals with a domain conflict, and a rendered price of 0 is truthful.
func EffectivePriceMinor(priceMinor int64, discount decimal.Decimal) (int64, error) {
	if priceMinor < 0 {
		return 0, errors.BadRequest("PRODUCT_PRICE_INVALID", "price must be non-negative")
	}
	if err := ProductDiscount(discount); err != nil {
		return 0, err
	}
	effective := decimal.NewFromInt(priceMinor).Mul(discount).Round(0)
	return effective.IntPart(), nil
}

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
	CountProducts(ctx context.Context, categoryID int64) (int64, error)
	CreateProduct(ctx context.Context, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, status int16, coverImage []MediaInfo, mediaAssets []MediaInfo, descrption string) (*Product, error)
	DecrProductStock(ctx context.Context, ID int64, amount int32) (int32, error)
	GetProduct(ctx context.Context, id int64) (*Product, error)
	ListProducts(ctx context.Context, limit int32, offset int32) ([]Product, error)
	ListProductsByCategory(ctx context.Context, categoryID int64, limit int32, offset int32) ([]Product, error)
	SoftDeleteProduct(ctx context.Context, id int64) error
	UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, price decimal.Decimal, discount decimal.Decimal, stock int32, coverImage []MediaInfo, mediaAssets []MediaInfo, descrption string) (*Product, error)
	UpdateProductStatus(ctx context.Context, ID int64, status int32) error
}

type ProductUsecase interface {
	CreateProduct(ctx context.Context, categoryID int64, name string, priceStr string, discountStr string, stock int32, status int16, coverImage string, mediaAssets []MediaInfo, descrption string) (*Product, error)
	GetProduct(ctx context.Context, id int64) (*Product, error)
	ListProducts(ctx context.Context, categoryID int64, pageSize int32, page int32) ([]Product, int32, error)
	UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, priceStr string, discountStr string, stock int32, coverImage string, mediaAssets []MediaInfo, descrption string) (*Product, error)
	UpdateProductStatus(ctx context.Context, id int64, status int32) error
	DeleteProduct(ctx context.Context, id int64) error
}

type productUsecase struct {
	repo ProductRepo
	log  *log.Helper
}

func NewProductUsecase(repo ProductRepo, logger log.Logger) ProductUsecase {
	return &productUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *productUsecase) CreateProduct(ctx context.Context, categoryID int64, name string, priceStr string, discountStr string, stock int32, status int16, coverImage string, mediaAssets []MediaInfo, descrption string) (*Product, error) {
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid price: %v", err)
		return nil, err
	}
	if _, err := ProductPriceMinor(price); err != nil {
		return nil, err
	}
	discount, err := decimal.NewFromString(discountStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid discount: %v", err)
		return nil, err
	}
	if err := ProductDiscount(discount); err != nil {
		return nil, err
	}
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.CreateProduct(ctx, categoryID, name, price, discount, stock, status, cover, mediaAssets, descrption)
}

func (uc *productUsecase) GetProduct(ctx context.Context, id int64) (*Product, error) {
	return uc.repo.GetProduct(ctx, id)
}

func (uc *productUsecase) ListProducts(ctx context.Context, categoryID int64, pageSize int32, page int32) ([]Product, int32, error) {
	offset := (page - 1) * pageSize
	total, err := uc.repo.CountProducts(ctx, categoryID)
	if err != nil {
		return nil, 0, err
	}
	if categoryID > 0 {
		ps, err := uc.repo.ListProductsByCategory(ctx, categoryID, pageSize, offset)
		return ps, int32(total), err
	}
	ps, err := uc.repo.ListProducts(ctx, pageSize, offset)
	return ps, int32(total), err
}

func (uc *productUsecase) UpdateProduct(ctx context.Context, id int64, categoryID int64, name string, priceStr string, discountStr string, stock int32, coverImage string, mediaAssets []MediaInfo, descrption string) (*Product, error) {
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid price: %v", err)
		return nil, err
	}
	if _, err := ProductPriceMinor(price); err != nil {
		return nil, err
	}
	discount, err := decimal.NewFromString(discountStr)
	if err != nil {
		uc.log.WithContext(ctx).Errorf("invalid discount: %v", err)
		return nil, err
	}
	if err := ProductDiscount(discount); err != nil {
		return nil, err
	}
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.UpdateProduct(ctx, id, categoryID, name, price, discount, stock, cover, mediaAssets, descrption)
}

func (uc *productUsecase) UpdateProductStatus(ctx context.Context, id int64, status int32) error {
	return uc.repo.UpdateProductStatus(ctx, id, status)
}

func (uc *productUsecase) DeleteProduct(ctx context.Context, id int64) error {
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

type CategoryUsecase interface {
	CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error)
	GetCategory(ctx context.Context, id int64) (*Category, error)
	ListCategories(ctx context.Context, parentID int64) ([]Category, error)
	UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error)
	DeleteCategory(ctx context.Context, id int64) error
}

type categoryUsecase struct {
	repo CategoryRepo
	log  *log.Helper
}

func NewCategoryUsecase(repo CategoryRepo, logger log.Logger) CategoryUsecase {
	return &categoryUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *categoryUsecase) CreateCategory(ctx context.Context, parentID int64, name string, sortOrder int32) (*Category, error) {
	return uc.repo.CreateCategory(ctx, parentID, name, sortOrder)
}

func (uc *categoryUsecase) GetCategory(ctx context.Context, id int64) (*Category, error) {
	return uc.repo.GetCategory(ctx, id)
}

func (uc *categoryUsecase) ListCategories(ctx context.Context, parentID int64) ([]Category, error) {
	if parentID > 0 {
		return uc.repo.ListSubCategories(ctx, parentID)
	}
	return uc.repo.ListTopCategories(ctx)
}

func (uc *categoryUsecase) UpdateCategory(ctx context.Context, id int64, name string, sortOrder int32) (*Category, error) {
	return uc.repo.UpdateCategory(ctx, id, name, sortOrder)
}

func (uc *categoryUsecase) DeleteCategory(ctx context.Context, id int64) error {
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
	ListEvents(ctx context.Context, status *int32, limit int32, offset int32) ([]Event, error)
	UpdateEvent(ctx context.Context, id int64, name string, coverImage []MediaInfo, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error)
	UpdateEventStatus(ctx context.Context, id int64, status int32) error
}

type EventUsecase interface {
	CreateEvent(ctx context.Context, name string, status int16, coverImage string, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error)
	GetEvent(ctx context.Context, id int64) (*Event, error)
	ListEvents(ctx context.Context, status *int32, pageSize int32, page int32) ([]Event, error)
	UpdateEvent(ctx context.Context, id int64, name string, coverImage string, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error)
	UpdateEventStatus(ctx context.Context, id int64, status int32) error
	DeleteEvent(ctx context.Context, id int64) error
}

type eventUsecase struct {
	repo EventRepo
	log  *log.Helper
}

func NewEventUsecase(repo EventRepo, logger log.Logger) EventUsecase {
	return &eventUsecase{repo: repo, log: log.NewHelper(logger)}
}

func (uc *eventUsecase) CreateEvent(ctx context.Context, name string, status int16, coverImage string, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error) {
	if err := validateEventWindow(startAt, endAt); err != nil {
		return nil, err
	}
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.CreateEvent(ctx, name, status, cover, mediaAssets, description, startAt, endAt)
}

// validateEventWindow mirrors the events_window_check constraint so an invalid
// window is a 400 instead of a 500 from the check violation.
func validateEventWindow(startAt, endAt time.Time) error {
	if startAt.IsZero() || endAt.IsZero() || !endAt.After(startAt) {
		return mallv1.ErrorEventInvalidWindow("event end must be after its start")
	}
	return nil
}

func (uc *eventUsecase) GetEvent(ctx context.Context, id int64) (*Event, error) {
	return uc.repo.GetEvent(ctx, id)
}

func (uc *eventUsecase) ListEvents(ctx context.Context, status *int32, pageSize int32, page int32) ([]Event, error) {
	offset := (page - 1) * pageSize
	return uc.repo.ListEvents(ctx, status, pageSize, offset)
}

func (uc *eventUsecase) UpdateEvent(ctx context.Context, id int64, name string, coverImage string, mediaAssets []MediaInfo, description string, startAt time.Time, endAt time.Time) (*Event, error) {
	if err := validateEventWindow(startAt, endAt); err != nil {
		return nil, err
	}
	cover := mediaFromCoverURL(coverImage)
	return uc.repo.UpdateEvent(ctx, id, name, cover, mediaAssets, description, startAt, endAt)
}

func (uc *eventUsecase) UpdateEventStatus(ctx context.Context, id int64, status int32) error {
	return uc.repo.UpdateEventStatus(ctx, id, status)
}

func (uc *eventUsecase) DeleteEvent(ctx context.Context, id int64) error {
	return uc.repo.DeleteEvent(ctx, id)
}
