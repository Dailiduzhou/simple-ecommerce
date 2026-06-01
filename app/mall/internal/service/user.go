package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type UserService struct {
	pb.UnimplementedUserServer
	authUc   *biz.AuthUsecase
	userRepo biz.UserRepo
	log      *log.Helper
}

func NewUserService(authUc *biz.AuthUsecase, userRepo biz.UserRepo, logger log.Logger) *UserService {
	return &UserService{
		authUc:   authUc,
		userRepo: userRepo,
		log:      log.NewHelper(logger),
	}
}

func (s *UserService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterReply, error) {
	// TODO: implement phone hash/encrypt and password hashing
	return &pb.RegisterReply{}, nil
}

func (s *UserService) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginReply, error) {
	// TODO: implement phone lookup and password verification
	return &pb.LoginReply{}, nil
}

func (s *UserService) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserInfo, error) {
	u, err := s.userRepo.GetUserByID(ctx, req.Id)
	if err != nil {
		return nil, pb.ErrorUserNotFound("user %d not found", req.Id)
	}
	if u == nil {
		return nil, pb.ErrorUserNotFound("user %d not found", req.Id)
	}
	return &pb.UserInfo{
		Id:        u.ID,
		Nickname:  u.Nickname,
		RealName:  u.RealName,
		Role:      u.Role,
		CreatedAt: timestamppb.New(u.CreatedAt),
	}, nil
}

func (s *UserService) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.UserInfo, error) {
	u, err := s.userRepo.UpdateUser(ctx, req.Id, req.Nickname, req.RealName)
	if err != nil {
		return nil, pb.ErrorUserNotFound("user %d not found", req.Id)
	}
	if u == nil {
		return nil, pb.ErrorUserNotFound("user %d not found", req.Id)
	}
	return &pb.UserInfo{
		Id:       u.ID,
		Nickname: u.Nickname,
		RealName: u.RealName,
		Role:     u.Role,
	}, nil
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

func (s *UserService) RefreshToken(ctx context.Context, req *pb.RefreshRequest) (*pb.RefreshReply, error) {
	claims, err := s.authUc.ParseRefreshToken(req.RefreshToken)
	if err != nil {
		s.log.WithContext(ctx).Errorf("refresh token invalid or expired: %v", err)
		return nil, pb.ErrorTokenExpired("refresh token invalid or expired")
	}

	blacklisted, err := s.authUc.IsTokenBlacklisted(ctx, claims.ID)
	if err != nil {
		s.log.WithContext(ctx).Errorf("check blacklist failed: %v", err)
		return nil, pb.ErrorUnauthorized("check blacklist failed")
	}
	if blacklisted {
		return nil, pb.ErrorTokenExpired("refresh token has been revoked")
	}

	if err := s.authUc.BlacklistToken(ctx, claims.ID, claims.ExpiresAt.Time); err != nil {
		s.log.Errorf("blacklist old refresh token failed: %v", err)
	}

	accessToken, err := s.authUc.GenerateAccessToken(claims.UserID, claims.Role)
	if err != nil {
		s.log.WithContext(ctx).Errorf("generate access token failed: %v", err)
		return nil, pb.ErrorUnauthorized("generate access token failed")
	}

	refreshToken, err := s.authUc.GenerateRefreshToken(claims.UserID, claims.Role)
	if err != nil {
		s.log.WithContext(ctx).Errorf("generate refresh token failed: %v", err)
		return nil, pb.ErrorUnauthorized("generate refresh token failed")
	}

	return &pb.RefreshReply{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}
