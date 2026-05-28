package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
)

type MallService struct {
	pb.UnimplementedMallServer
}

func NewMallService() *MallService {
	return &MallService{}
}

func (s *MallService) CreateCategory(ctx context.Context, req *pb.CreateCategoryRequest) (*pb.Category, error) {
    return &pb.Category{}, nil
}
func (s *MallService) ListCategories(ctx context.Context, req *pb.ListCategoriesRequest) (*pb.ListCategoriesReply, error) {
    return &pb.ListCategoriesReply{}, nil
}
func (s *MallService) UpdateCategory(ctx context.Context, req *pb.UpdateCategoryRequest) (*pb.Category, error) {
    return &pb.Category{}, nil
}
func (s *MallService) DeleteCategory(ctx context.Context, req *pb.DeleteCategoryRequest) (*pb.DeleteCategoryReply, error) {
    return &pb.DeleteCategoryReply{}, nil
}
func (s *MallService) CreateProduct(ctx context.Context, req *pb.CreateProductRequest) (*pb.Product, error) {
    return &pb.Product{}, nil
}
func (s *MallService) GetProduct(ctx context.Context, req *pb.GetProductRequest) (*pb.Product, error) {
    return &pb.Product{}, nil
}
func (s *MallService) ListProducts(ctx context.Context, req *pb.ListProductsRequest) (*pb.ListProductsReply, error) {
    return &pb.ListProductsReply{}, nil
}
func (s *MallService) UpdateProduct(ctx context.Context, req *pb.UpdateProductRequest) (*pb.Product, error) {
    return &pb.Product{}, nil
}
func (s *MallService) UpdateProductStatus(ctx context.Context, req *pb.UpdateProductStatusRequest) (*pb.UpdateProductStatusReply, error) {
    return &pb.UpdateProductStatusReply{}, nil
}
func (s *MallService) DeleteProduct(ctx context.Context, req *pb.DeleteProductRequest) (*pb.DeleteProductReply, error) {
    return &pb.DeleteProductReply{}, nil
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
