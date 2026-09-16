package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/user/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *UserService) RecordProductView(ctx context.Context, r *pb.RecordProductViewRequest) (*pb.RecordProductViewReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	t, e := s.history.Record(ctx, a, r.ProductId)
	if e != nil {
		return nil, e
	}
	return &pb.RecordProductViewReply{LastViewedAt: timestamppb.New(t)}, nil
}

func (s *UserService) ListBrowsingHistory(ctx context.Context, r *pb.ListBrowsingHistoryRequest) (*pb.ListBrowsingHistoryReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	var f biz.HistoryFilter
	if r.StartTime != nil {
		f.Start, f.HasStart = r.StartTime.AsTime(), true
	}
	if r.EndTime != nil {
		f.End, f.HasEnd = r.EndTime.AsTime(), true
	}
	items, next, e := s.history.List(ctx, a, r.Cursor, r.PageSize, f)
	if e != nil {
		return nil, e
	}
	out := &pb.ListBrowsingHistoryReply{NextCursor: next}
	for _, h := range items {
		out.Items = append(out.Items, &pb.BrowsingHistoryItem{ProductId: h.ProductID, Name: h.Name, PriceMinor: h.PriceMinor, EffectivePriceMinor: h.EffectivePriceMinor, Available: h.Available, LastViewedAt: timestamppb.New(h.LastViewedAt), CoverImageJson: h.CoverImageJSON})
	}
	return out, nil
}

func (s *UserService) DeleteBrowsingHistoryItem(ctx context.Context, r *pb.DeleteBrowsingHistoryItemRequest) (*pb.DeleteBrowsingHistoryItemReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	e = s.history.Delete(ctx, a, r.ProductId)
	return &pb.DeleteBrowsingHistoryItemReply{}, e
}

func (s *UserService) ClearBrowsingHistory(ctx context.Context, r *pb.ClearBrowsingHistoryRequest) (*pb.ClearBrowsingHistoryReply, error) {
	a, e := actor(ctx)
	if e != nil {
		return nil, e
	}
	e = s.history.Clear(ctx, a)
	return &pb.ClearBrowsingHistoryReply{}, e
}
