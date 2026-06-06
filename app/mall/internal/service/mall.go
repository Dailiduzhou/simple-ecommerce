package service

import (
	"context"
	"encoding/json"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type MallService struct {
	pb.UnimplementedMallServer
	productUc  *biz.ProductUsecase
	categoryUc *biz.CategoryUsecase
	log        *log.Helper
}

func NewMallService(productUc *biz.ProductUsecase, categoryUc *biz.CategoryUsecase, logger log.Logger) *MallService {
	return &MallService{productUc: productUc, categoryUc: categoryUc, log: log.NewHelper(logger)}
}

func (s *MallService) CreateProduct(ctx context.Context, req *pb.CreateProductRequest) (*pb.Product, error) {
	mediaAssets := structToMediaInfo(req.MediaAssets)

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
	mediaAssets := structToMediaInfo(req.MediaAssets)

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
	return &pb.Event{}, nil
}

func (s *MallService) GetEvent(ctx context.Context, req *pb.GetEventRequest) (*pb.Event, error) {
	return &pb.Event{}, nil
}

func (s *MallService) ListEvents(ctx context.Context, req *pb.ListEventsRequest) (*pb.ListEventsReply, error) {
	return &pb.ListEventsReply{}, nil
}

func (s *MallService) UpdateEvent(ctx context.Context, req *pb.UpdateEventRequest) (*pb.Event, error) {
	return &pb.Event{}, nil
}

func (s *MallService) UpdateEventStatus(ctx context.Context, req *pb.UpdateEventStatusRequest) (*pb.UpdateEventStatusReply, error) {
	return &pb.UpdateEventStatusReply{}, nil
}

func (s *MallService) DeleteEvent(ctx context.Context, req *pb.DeleteEventRequest) (*pb.DeleteEventReply, error) {
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

func coverImageURL(infos []biz.MediaInfo) string {
	if len(infos) == 0 {
		return ""
	}
	return infos[0].OssURL
}

func structToMediaInfo(s *structpb.Struct) []biz.MediaInfo {
	if s == nil {
		return nil
	}
	data, err := json.Marshal(s.AsMap())
	if err != nil {
		return nil
	}
	var result []biz.MediaInfo
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}
