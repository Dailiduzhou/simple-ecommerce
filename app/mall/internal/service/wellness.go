package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
)

func (s *MallService) GetTodayWellness(ctx context.Context, _ *pb.GetTodayWellnessRequest) (*pb.TodayWellnessReply, error) {
	if _, err := authenticatedClaims(ctx); err != nil {
		return nil, err
	}
	card, err := s.wellnessUc.GetTodayWellness(ctx)
	if err != nil {
		return nil, err
	}
	return &pb.TodayWellnessReply{
		Date:      card.Date,
		SolarTerm: card.SolarTerm,
		Advice:    card.Advice,
	}, nil
}
