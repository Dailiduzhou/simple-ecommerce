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
	history        *biz.BrowsingHistoryUsecase
	log            *log.Helper
}

func NewUserService(authUc biz.AuthUsecase, userUc biz.UserUsecase, shippingAddrUc biz.ShippingAddressUsecase, history *biz.BrowsingHistoryUsecase, logger log.Logger) *UserService {
	return &UserService{
		authUc:         authUc,
		history:        history,
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
	pair, err := s.authUc.StartSession(ctx, u)
	if err != nil {
		return nil, err
	}
	return &pb.LoginReply{Id: u.ID, Token: pair.AccessToken, RefreshToken: pair.RefreshToken}, nil
}

func (s *UserService) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserInfo, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrAdmin(ctx, actor, req.Id); err != nil {
		return nil, err
	}
	u, err := s.uc.GetUser(ctx, targetUserID(actor, req.Id))
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
		Role:      string(u.Role),
		CreatedAt: timestamppb.New(u.CreatedAt),
	}, nil
}

func (s *UserService) UpdateUser(ctx context.Context, req *pb.UpdateUserRequest) (*pb.UserInfo, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrAdmin(ctx, actor, req.Id); err != nil {
		return nil, err
	}
	u, err := s.uc.UpdateUser(ctx, targetUserID(actor, req.Id), req.Nickname, req.RealName)
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
		Role:     string(u.Role),
	}, nil
}

func (s *UserService) ChangePassword(ctx context.Context, req *pb.ChangePasswordRequest) (*pb.ChangePasswordReply, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.uc.ChangePassword(ctx, actor.ID, req.OldPassword, req.NewPassword); err != nil {
		return nil, err
	}
	return &pb.ChangePasswordReply{}, nil
}

func (s *UserService) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*pb.DeleteUserReply, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireSelfOrAdmin(ctx, actor, req.Id); err != nil {
		return nil, err
	}
	// Deleting another account is a back-office action; the usecase still
	// refuses removing the last administrator and the database still decides
	// whether retained history allows the physical delete.
	err = s.uc.DeleteUser(ctx, actor, targetUserID(actor, req.Id))
	if err != nil {
		s.log.WithContext(ctx).Errorf("Error deleting User %d", req.Id)
		return nil, err
	}
	return &pb.DeleteUserReply{}, nil
}

// targetUserID resolves the path id, where 0 is the conventional "me".
func targetUserID(actor biz.Actor, requested int64) int64 {
	if requested <= 0 {
		return actor.ID
	}
	return requested
}

// UpdateUserRole is the only supported way to grant merchant or administrator
// rights. It is administrator-only and refuses self-changes and demoting the
// last administrator.
func (s *UserService) UpdateUserRole(ctx context.Context, req *pb.UpdateUserRoleRequest) (*pb.UserInfo, error) {
	actor, err := requireCapability(ctx, biz.CapManageRoles)
	if err != nil {
		return nil, err
	}
	u, err := s.uc.UpdateUserRole(ctx, actor, req.Id, req.Role)
	if err != nil {
		return nil, err
	}
	return &pb.UserInfo{Id: u.ID, Nickname: u.Nickname, RealName: u.RealName, Role: string(u.Role), CreatedAt: timestamppb.New(u.CreatedAt)}, nil
}

func (s *UserService) CreateShippingAddress(ctx context.Context, req *pb.CreateShippingAddressRequest) (*pb.ShippingAddress, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(ctx, actor, req.UserId); err != nil {
		return nil, err
	}
	sa, err := s.shippingAddrUc.CreateShippingAddress(ctx, actor.ID, req.ReceiverName, req.ReceiverPhone, req.Province, req.City, req.District, req.DetailAddress, req.AddressTag, req.IsDefault)
	if err != nil {
		return nil, err
	}
	return toProtoShippingAddress(sa), nil
}

func (s *UserService) ListShippingAddresses(ctx context.Context, req *pb.ListShippingAddressesRequest) (*pb.ListShippingAddressesReply, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(ctx, actor, req.UserId); err != nil {
		return nil, err
	}
	sas, err := s.shippingAddrUc.ListShippingAddressesByUser(ctx, actor.ID, req.Page, req.PageSize)
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
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(ctx, actor, req.UserId); err != nil {
		return nil, err
	}
	sa, err := s.shippingAddrUc.UpdateShippingAddress(ctx, req.Id, actor.ID, req.ReceiverName, req.ReceiverPhone, req.Province, req.City, req.District, req.DetailAddress, req.AddressTag)
	if err != nil {
		return nil, err
	}
	return toProtoShippingAddress(sa), nil
}

func (s *UserService) SetDefaultShippingAddress(ctx context.Context, req *pb.SetDefaultShippingAddressRequest) (*pb.SetDefaultShippingAddressReply, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(ctx, actor, req.UserId); err != nil {
		return nil, err
	}
	err = s.shippingAddrUc.SetDefaultShippingAddress(ctx, req.Id, actor.ID)
	if err != nil {
		return nil, err
	}
	return &pb.SetDefaultShippingAddressReply{}, nil
}

func (s *UserService) DeleteShippingAddress(ctx context.Context, req *pb.DeleteShippingAddressRequest) (*pb.DeleteShippingAddressReply, error) {
	actor, err := authenticatedActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireResourceOwner(ctx, actor, req.UserId); err != nil {
		return nil, err
	}
	err = s.shippingAddrUc.DeleteShippingAddress(ctx, req.Id, actor.ID)
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
	pair, err := s.authUc.RefreshSession(ctx, req.RefreshToken)
	if err != nil {
		return nil, err
	}
	return &pb.RefreshReply{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken}, nil
}

// Logout revokes the caller's entire session, including refresh descendants.
func (s *UserService) Logout(ctx context.Context, req *pb.LogoutRequest) (*pb.LogoutReply, error) {
	if _, err := authenticatedActor(ctx); err != nil {
		return nil, err
	}
	claims, _ := biz.ClaimsFromContext(ctx)
	if err := s.authUc.Logout(ctx, claims, req.RefreshToken); err != nil {
		return nil, err
	}
	return &pb.LogoutReply{}, nil
}
