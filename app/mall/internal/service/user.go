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
	authUc         biz.AuthUsecase
	uc             biz.UserUsecase
	shippingAddrUc biz.ShippingAddressUsecase
	log            *log.Helper
}

func NewUserService(authUc biz.AuthUsecase, userUc biz.UserUsecase, shippingAddrUc biz.ShippingAddressUsecase, logger log.Logger) *UserService {
	return &UserService{
		authUc:         authUc,
		uc:             userUc,
		shippingAddrUc: shippingAddrUc,
		log:            log.NewHelper(logger),
	}
}

func (s *UserService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterReply, error) {
	u, err := s.uc.Register(ctx, req.Phone, req.Password)
	if err != nil {
		return nil, err
	}
	return &pb.RegisterReply{Id: u.ID}, nil
}

func (s *UserService) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginReply, error) {
	u, err := s.uc.Login(ctx, req.Phone, req.Password)
	if err != nil {
		return nil, err
	}
	token, err := s.authUc.GenerateAccessToken(u.ID, u.Role)
	if err != nil {
		return nil, pb.ErrorUnauthorized("generate access token failed")
	}
	refreshToken, err := s.authUc.GenerateRefreshToken(u.ID, u.Role)
	if err != nil {
		return nil, pb.ErrorUnauthorized("generate refresh token failed")
	}
	return &pb.LoginReply{Id: u.ID, Token: token, RefreshToken: refreshToken}, nil
}

func (s *UserService) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserInfo, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.Id); err != nil {
		return nil, err
	}
	u, err := s.uc.GetUser(ctx, claims.UserID)
	if err != nil {
		return nil, err
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
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.Id); err != nil {
		return nil, err
	}
	u, err := s.uc.UpdateUser(ctx, claims.UserID, req.Nickname, req.RealName)
	if err != nil {
		return nil, err
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
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.Id); err != nil {
		return nil, err
	}
	err = s.uc.DeleteUser(ctx, claims.UserID)
	if err != nil {
		s.log.WithContext(ctx).Errorf("Error deleting User %d", req.Id)
		return nil, err
	}
	return &pb.DeleteUserReply{}, nil
}

func (s *UserService) CreateShippingAddress(ctx context.Context, req *pb.CreateShippingAddressRequest) (*pb.ShippingAddress, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	sa, err := s.shippingAddrUc.CreateShippingAddress(ctx, claims.UserID, req.ReceiverName, req.ReceiverPhone, req.Province, req.City, req.District, req.DetailAddress, req.AddressTag, req.IsDefault)
	if err != nil {
		return nil, err
	}
	return toProtoShippingAddress(sa), nil
}

func (s *UserService) ListShippingAddresses(ctx context.Context, req *pb.ListShippingAddressesRequest) (*pb.ListShippingAddressesReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	sas, err := s.shippingAddrUc.ListShippingAddressesByUser(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}
	var addrs []*pb.ShippingAddress
	for _, sa := range sas {
		addrs = append(addrs, toProtoShippingAddress(&sa))
	}
	return &pb.ListShippingAddressesReply{Addresses: addrs}, nil
}

func (s *UserService) UpdateShippingAddress(ctx context.Context, req *pb.UpdateShippingAddressRequest) (*pb.ShippingAddress, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	sa, err := s.shippingAddrUc.UpdateShippingAddress(ctx, req.Id, claims.UserID, req.ReceiverName, req.ReceiverPhone, req.Province, req.City, req.District, req.DetailAddress, req.AddressTag)
	if err != nil {
		return nil, err
	}
	return toProtoShippingAddress(sa), nil
}

func (s *UserService) SetDefaultShippingAddress(ctx context.Context, req *pb.SetDefaultShippingAddressRequest) (*pb.SetDefaultShippingAddressReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	err = s.shippingAddrUc.SetDefaultShippingAddress(ctx, req.Id, claims.UserID)
	if err != nil {
		return nil, err
	}
	return &pb.SetDefaultShippingAddressReply{}, nil
}

func (s *UserService) DeleteShippingAddress(ctx context.Context, req *pb.DeleteShippingAddressRequest) (*pb.DeleteShippingAddressReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(claims, req.UserId); err != nil {
		return nil, err
	}
	err = s.shippingAddrUc.DeleteShippingAddress(ctx, req.Id, claims.UserID)
	if err != nil {
		return nil, err
	}
	return &pb.DeleteShippingAddressReply{}, nil
}

func toProtoShippingAddress(sa *biz.ShippingAddress) *pb.ShippingAddress {
	return &pb.ShippingAddress{
		Id:            sa.ID,
		UserId:        sa.UserID,
		ReceiverName:  sa.ReceiverName,
		ReceiverPhone: sa.ReceiverPhone,
		Province:      sa.Province,
		City:          sa.City,
		District:      sa.District,
		DetailAddress: sa.DetailAddress,
		AddressTag:    sa.AddressTag,
		IsDefault:     sa.IsDefault,
	}
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
