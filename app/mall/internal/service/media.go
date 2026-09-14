package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/media/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type MediaService struct {
	pb.UnimplementedMediaServer
	uc *biz.MediaUsecase
}

func NewMediaService(u *biz.MediaUsecase) *MediaService { return &MediaService{uc: u} }
func mediaProto(m *biz.MediaAsset) *pb.ImageAsset {
	if m == nil {
		return nil
	}
	return &pb.ImageAsset{Id: m.ID, Url: m.URL, ContentType: m.ContentType, SizeBytes: m.SizeBytes, Width: m.Width, Height: m.Height, Status: m.Status}
}

func (s *MediaService) CreateImageUpload(ctx context.Context, r *pb.CreateImageUploadRequest) (*pb.CreateImageUploadReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	m, g, e := s.uc.CreateUpload(ctx, a, r.ContentType, r.SizeBytes)
	if e != nil {
		return nil, e
	}
	return &pb.CreateImageUploadReply{Id: m.ID, UploadUrl: g.URL, FormFields: g.Fields, ExpiresAt: timestamppb.New(g.ExpiresAt)}, nil
}

func (s *MediaService) CompleteImageUpload(ctx context.Context, r *pb.ImageRequest) (*pb.ImageAsset, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	m, e := s.uc.Complete(ctx, a, r.Id)
	return mediaProto(m), e
}

func (s *MediaService) GetImage(ctx context.Context, r *pb.ImageRequest) (*pb.ImageAsset, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	m, e := s.uc.Get(ctx, a, r.Id)
	return mediaProto(m), e
}
