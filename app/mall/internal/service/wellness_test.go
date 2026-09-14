package service

import (
	"context"
	"testing"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/mall/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type wellnessStub struct {
	card   *biz.TodayWellness
	err    error
	called bool
	ctx    context.Context
}

func (u *wellnessStub) GetTodayWellness(ctx context.Context) (*biz.TodayWellness, error) {
	u.called = true
	u.ctx = ctx
	return u.card, u.err
}

func TestGetTodayWellnessRequiresLogin(t *testing.T) {
	for _, claims := range []*biz.EcommerceClaims{nil, {}, {UserID: -1}} {
		uc := &wellnessStub{}
		svc := NewMallService(nil, nil, nil, uc, log.DefaultLogger)
		card, err := svc.GetTodayWellness(biz.WithClaims(context.Background(), claims), &pb.GetTodayWellnessRequest{})
		require.Equal(t, 401, errors.Code(err))
		require.Nil(t, card)
		require.False(t, uc.called)
	}
}

func TestGetTodayWellnessMapsSameCardForAllUsers(t *testing.T) {
	uc := &wellnessStub{card: &biz.TodayWellness{Date: "2026-04-08", SolarTerm: "清明", Advice: "通用生活建议"}}
	svc := NewMallService(nil, nil, nil, uc, log.DefaultLogger)
	for _, claims := range []*biz.EcommerceClaims{{UserID: 1, Role: "user"}, {UserID: 2, Role: "admin"}} {
		ctx := biz.WithClaims(context.Background(), claims)
		card, err := svc.GetTodayWellness(ctx, &pb.GetTodayWellnessRequest{})
		require.NoError(t, err)
		require.Equal(t, "2026-04-08", card.Date)
		require.Equal(t, "清明", card.SolarTerm)
		require.Equal(t, "通用生活建议", card.Advice)
		require.Same(t, ctx, uc.ctx)
	}
}

func TestGetTodayWellnessPropagatesError(t *testing.T) {
	wantErr := errors.InternalServer("WELLNESS_UNAVAILABLE", "unavailable")
	uc := &wellnessStub{err: wantErr}
	svc := NewMallService(nil, nil, nil, uc, log.DefaultLogger)
	ctx := biz.WithClaims(context.Background(), &biz.EcommerceClaims{UserID: 1})
	card, err := svc.GetTodayWellness(ctx, &pb.GetTodayWellnessRequest{})
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, card)
}
