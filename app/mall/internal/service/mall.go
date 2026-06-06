package service

import (
	"context"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type MallService struct {
	pb.UnimplementedMallServer
	productUc  *biz.ProductUsecase
	categoryUc *biz.CategoryUsecase
	eventUc    *biz.EventUsecase
	log        *log.Helper
}

func NewMallService(productUc *biz.ProductUsecase, categoryUc *biz.CategoryUsecase, eventUc *biz.EventUsecase, logger log.Logger) *MallService {
	return &MallService{productUc: productUc, categoryUc: categoryUc, eventUc: eventUc, log: log.NewHelper(logger)}
}

func (s *MallService) CreateProduct(ctx context.Context, req *pb.CreateProductRequest) (*pb.Product, error) {
	mediaAssets := protoToMediaInfo(req.MediaAssets)

	p, err := s.productUc.CreateProduct(ctx, req.CategoryId, req.Name, req.Price, req.Discount, req.Stock, 1, req.CoverImage, mediaAssets, req.Description)
	if err != nil {
		return nil, err
	}
	return toProtoProduct(p), nil
}

func (s *MallService) GetProduct(ctx context.Context, req *pb.GetProductRequest) (*pb.Product, error) {
	p, err := s.productUc.GetProduct(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, pb.ErrorProductNotFound("product %d not found", req.Id)
	}
	return toProtoProduct(p), nil
}

func (s *MallService) ListProducts(ctx context.Context, req *pb.ListProductsRequest) (*pb.ListProductsReply, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}

	ps, total, err := s.productUc.ListProducts(ctx, req.CategoryId, pageSize, page)
	if err != nil {
		return nil, err
	}

	var products []*pb.Product
	for i := range ps {
		products = append(products, toProtoProduct(&ps[i]))
	}

	return &pb.ListProductsReply{
		Products: products,
		Total:    total,
	}, nil
}

func (s *MallService) UpdateProduct(ctx context.Context, req *pb.UpdateProductRequest) (*pb.Product, error) {
	mediaAssets := protoToMediaInfo(req.MediaAssets)

	p, err := s.productUc.UpdateProduct(ctx, req.Id, req.CategoryId, req.Name, req.Price, req.Discount, req.Stock, req.CoverImage, mediaAssets, req.Description)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, pb.ErrorProductNotFound("product %d not found", req.Id)
	}
	return toProtoProduct(p), nil
}

func (s *MallService) UpdateProductStatus(ctx context.Context, req *pb.UpdateProductStatusRequest) (*pb.UpdateProductStatusReply, error) {
	err := s.productUc.UpdateProductStatus(ctx, req.Id, req.Status)
	if err != nil {
		return nil, err
	}
	return &pb.UpdateProductStatusReply{}, nil
}

func (s *MallService) DeleteProduct(ctx context.Context, req *pb.DeleteProductRequest) (*pb.DeleteProductReply, error) {
	err := s.productUc.DeleteProduct(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return &pb.DeleteProductReply{}, nil
}

func (s *MallService) CreateCategory(ctx context.Context, req *pb.CreateCategoryRequest) (*pb.Category, error) {
	c, err := s.categoryUc.CreateCategory(ctx, req.ParentId, req.Name, req.SortOrder)
	if err != nil {
		return nil, err
	}
	return toProtoCategory(c), nil
}

func (s *MallService) ListCategories(ctx context.Context, req *pb.ListCategoriesRequest) (*pb.ListCategoriesReply, error) {
	cs, err := s.categoryUc.ListCategories(ctx, req.ParentId)
	if err != nil {
		return nil, err
	}

	categories := make([]*pb.Category, len(cs))
	for i := range cs {
		categories[i] = toProtoCategory(&cs[i])
	}

	return &pb.ListCategoriesReply{Categories: categories}, nil
}

func (s *MallService) UpdateCategory(ctx context.Context, req *pb.UpdateCategoryRequest) (*pb.Category, error) {
	c, err := s.categoryUc.UpdateCategory(ctx, req.Id, req.Name, req.SortOrder)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, pb.ErrorCategoryNotFound("category %d not found", req.Id)
	}
	return toProtoCategory(c), nil
}

func (s *MallService) DeleteCategory(ctx context.Context, req *pb.DeleteCategoryRequest) (*pb.DeleteCategoryReply, error) {
	if err := s.categoryUc.DeleteCategory(ctx, req.Id); err != nil {
		return nil, err
	}
	return &pb.DeleteCategoryReply{}, nil
}

func (s *MallService) CreateEvent(ctx context.Context, req *pb.CreateEventRequest) (*pb.Event, error) {
	mediaAssets := protoToMediaInfo(req.MediaAssets)

	e, err := s.eventUc.CreateEvent(
		ctx,
		req.Name,
		0,
		req.CoverImage,
		mediaAssets,
		req.Description,
		timestampToTime(req.StartAt),
		timestampToTime(req.EndAt),
	)
	if err != nil {
		return nil, err
	}
	return toProtoEvent(e), nil
}

func (s *MallService) GetEvent(ctx context.Context, req *pb.GetEventRequest) (*pb.Event, error) {
	e, err := s.eventUc.GetEvent(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, pb.ErrorEventNotFound("event %d not found", req.Id)
	}
	return toProtoEvent(e), nil
}

func (s *MallService) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsReply, error) {
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 10
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}

	es, err := s.eventUc.ListEvents(ctx, req.Status, pageSize, page)
	if err != nil {
		return nil, err
	}

	events := make([]*pb.Event, len(es))
	for i := range es {
		events[i] = toProtoEvent(&es[i])
	}

	return &pb.ListEventsReply{Events: events}, nil
}

func (s *MallService) UpdateEvent(ctx context.Context, req *pb.UpdateEventRequest) (*pb.Event, error) {
	mediaAssets := protoToMediaInfo(req.MediaAssets)

	e, err := s.eventUc.UpdateEvent(
		ctx,
		req.Id,
		req.Name,
		req.CoverImage,
		mediaAssets,
		req.Description,
		timestampToTime(req.StartAt),
		timestampToTime(req.EndAt),
	)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, pb.ErrorEventNotFound("event %d not found", req.Id)
	}
	return toProtoEvent(e), nil
}

func (s *MallService) UpdateEventStatus(ctx context.Context, req *pb.UpdateEventStatusRequest) (*pb.UpdateEventStatusReply, error) {
	if err := s.eventUc.UpdateEventStatus(ctx, req.Id, req.Status); err != nil {
		return nil, err
	}
	return &pb.UpdateEventStatusReply{}, nil
}

func (s *MallService) DeleteEvent(ctx context.Context, req *pb.DeleteEventRequest) (*pb.DeleteEventReply, error) {
	if err := s.eventUc.DeleteEvent(ctx, req.Id); err != nil {
		return nil, err
	}
	return &pb.DeleteEventReply{}, nil
}

func toProtoProduct(p *biz.Product) *pb.Product {
	proto := &pb.Product{
		Id:          p.ID,
		CategoryId:  p.CategoryID,
		Name:        p.Name,
		Price:       p.Price.String(),
		Discount:    p.Discount.String(),
		Stock:       p.Stock,
		Status:      int32(p.Status),
		Description: p.Description,
		CoverImage:  coverImageURL(p.CoverImage),
		MediaAssets: mediaInfoToProto(p.MediaAssets),
	}
	if !p.CreatedAt.IsZero() {
		proto.CreatedAt = timestamppb.New(p.CreatedAt)
	}
	return proto
}

func toProtoCategory(c *biz.Category) *pb.Category {
	return &pb.Category{
		Id:        c.ID,
		ParentId:  c.ParentID,
		Name:      c.Name,
		SortOrder: c.SortOrder,
	}
}

func toProtoEvent(e *biz.Event) *pb.Event {
	proto := &pb.Event{
		Id:          e.ID,
		Name:        e.Name,
		Status:      int32(e.Status),
		CoverImage:  coverImageURL(e.CoverImage),
		MediaAssets: mediaInfoToProto(e.MediaAssets),
		Description: e.Description,
	}
	if !e.StartAt.IsZero() {
		proto.StartAt = timestamppb.New(e.StartAt)
	}
	if !e.EndAt.IsZero() {
		proto.EndAt = timestamppb.New(e.EndAt)
	}
	if !e.CreatedAt.IsZero() {
		proto.CreatedAt = timestamppb.New(e.CreatedAt)
	}
	return proto
}

func mediaInfoToProto(infos []biz.MediaInfo) []*pb.MediaInfo {
	result := make([]*pb.MediaInfo, len(infos))
	for i, info := range infos {
		result[i] = &pb.MediaInfo{
			OssUrl:      info.OssURL,
			BucketName:  info.BucketName,
			ObjectKey:   info.ObjectKey,
			ContentType: info.ContentType,
			Size:        info.Size,
		}
	}
	return result
}

func protoToMediaInfo(infos []*pb.MediaInfo) []biz.MediaInfo {
	if len(infos) == 0 {
		return nil
	}
	result := make([]biz.MediaInfo, len(infos))
	for i, info := range infos {
		if info == nil {
			continue
		}
		result[i] = biz.MediaInfo{
			OssURL:      info.OssUrl,
			BucketName:  info.BucketName,
			ObjectKey:   info.ObjectKey,
			ContentType: info.ContentType,
			Size:        info.Size,
		}
	}
	return result
}

func timestampToTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func coverImageURL(infos []biz.MediaInfo) string {
	if len(infos) == 0 {
		return ""
	}
	return infos[0].OssURL
}
