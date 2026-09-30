package service

import (
	"context"
	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
	"testing"
)

type reconciliationServiceRepo struct {
	biz.PaymentReconciliationRepo
	actor biz.Actor
	input biz.ReconciliationInput
	calls int
}

func (r *reconciliationServiceRepo) ApplyReconciliation(_ context.Context, a biz.Actor, in biz.ReconciliationInput) (*biz.ReconciliationAction, error) {
	r.actor, r.input = a, in
	r.calls++
	return &biz.ReconciliationAction{ID: 7, ActorID: a.ID, PaymentID: in.PaymentID, Action: in.Action}, nil
}
func TestReconciliationServiceAuthorizationAndActorBinding(t *testing.T) {
	r := &reconciliationServiceRepo{}
	s := NewPaymentService(nil, nil, biz.NewPaymentReconciliationUsecase(r), log.DefaultLogger)
	request := &pb.ReconciliationCommandRequest{Id: 1, ExpectedVersion: 1, IdempotencyKey: "retry-001", Reason: "retry"}
	for _, ctx := range []context.Context{context.Background(), authenticatedPaymentContext(42, "user")} {
		_, err := s.RetryPaymentReconciliation(ctx, request)
		require.True(t, errors.FromError(err).Code == 401 || errors.FromError(err).Code == 403)
		_, err = s.ResolvePaymentReconciliation(ctx, request)
		require.Error(t, err)
		_, err = s.ListPaymentReconciliations(ctx, &pb.ListReconciliationCasesRequest{})
		require.Error(t, err)
		_, err = s.ListPaymentReconciliationActions(ctx, &pb.ListReconciliationActionsRequest{Id: 1})
		require.Error(t, err)
	}
	require.Zero(t, r.calls)
	_, err := s.RetryPaymentReconciliation(authenticatedPaymentContext(42, "admin"), request)
	require.NoError(t, err)
	require.Equal(t, biz.Actor{ID: 42, Admin: true}, r.actor)
	require.Equal(t, biz.ReconciliationActionRetry, r.input.Action)
	request.Evidence = "verified provider statement"
	_, err = s.ResolvePaymentReconciliation(authenticatedPaymentContext(42, "admin"), request)
	require.NoError(t, err)
	require.Equal(t, biz.ReconciliationActionResolve, r.input.Action)
}
