package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
)

type UserService struct {
	pb.UnimplementedUserServer
}

func NewUserService() *UserService {
	return &UserService{}
}

func (s *UserService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterReply, error) {
    return &pb.RegisterReply{}, nil
}
func (s *UserService) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginReply, error) {
    return &pb.LoginReply{}, nil
}
func (s *UserService) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserInfo, error) {
    return &pb.UserInfo{}, nil
}
func (s *UserService) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.UserInfo, error) {
    return &pb.UserInfo{}, nil
}
func (s *UserService) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*pb.DeleteUserReply, error) {
    return &pb.DeleteUserReply{}, nil
}
func (s *UserService) CreateShippingAddress(ctx context.Context, req *pb.CreateShippingAddressRequest) (*pb.ShippingAddress, error) {
    return &pb.ShippingAddress{}, nil
}
func (s *UserService) ListShippingAddresses(ctx context.Context, req *pb.ListShippingAddressesRequest) (*pb.ListShippingAddressesReply, error) {
    return &pb.ListShippingAddressesReply{}, nil
}
func (s *UserService) UpdateShippingAddress(ctx context.Context, req *pb.UpdateShippingAddressRequest) (*pb.ShippingAddress, error) {
    return &pb.ShippingAddress{}, nil
}
func (s *UserService) SetDefaultShippingAddress(ctx context.Context, req *pb.SetDefaultShippingAddressRequest) (*pb.SetDefaultShippingAddressReply, error) {
    return &pb.SetDefaultShippingAddressReply{}, nil
}
func (s *UserService) DeleteShippingAddress(ctx context.Context, req *pb.DeleteShippingAddressRequest) (*pb.DeleteShippingAddressReply, error) {
    return &pb.DeleteShippingAddressReply{}, nil
}
