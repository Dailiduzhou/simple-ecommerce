package service

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PaymentService) RetryPaymentReconciliation(ctx context.Context, req *pb.ReconciliationCommandRequest) (*pb.ReconciliationActionInfo, error) {
	return s.applyReconciliation(ctx, req, biz.ReconciliationActionRetry)
}

func (s *PaymentService) ResolvePaymentReconciliation(ctx context.Context, req *pb.ReconciliationCommandRequest) (*pb.ReconciliationActionInfo, error) {
	return s.applyReconciliation(ctx, req, biz.ReconciliationActionResolve)
}

func (s *PaymentService) applyReconciliation(ctx context.Context, req *pb.ReconciliationCommandRequest, action string) (*pb.ReconciliationActionInfo, error) {
	claims, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("RECONCILIATION_INVALID", "request is required")
	}
	result, err := s.reconciliation.Apply(ctx, biz.Actor{ID: claims.UserID, Admin: true}, biz.ReconciliationInput{PaymentID: req.Id, ExpectedVersion: req.ExpectedVersion, Action: action, IdempotencyKey: req.IdempotencyKey, Reason: req.Reason, Evidence: req.Evidence})
	if err != nil {
		return nil, err
	}
	return toProtoReconciliationAction(*result), nil
}

func (s *PaymentService) ListPaymentReconciliations(ctx context.Context, req *pb.ListReconciliationCasesRequest) (*pb.ListReconciliationCasesReply, error) {
	claims, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("RECONCILIATION_INVALID", "request is required")
	}
	rows, err := s.reconciliation.ListCases(ctx, biz.Actor{ID: claims.UserID, Admin: true}, req.AfterId, req.PageSize)
	if err != nil {
		return nil, err
	}
	result := &pb.ListReconciliationCasesReply{Cases: make([]*pb.ReconciliationCaseInfo, len(rows))}
	for i, row := range rows {
		result.Cases[i] = &pb.ReconciliationCaseInfo{Payment: toProtoPaymentInfo(row.Payment), Detail: row.Detail}
	}
	return result, nil
}

func (s *PaymentService) ListPaymentReconciliationActions(ctx context.Context, req *pb.ListReconciliationActionsRequest) (*pb.ListReconciliationActionsReply, error) {
	claims, err := requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("RECONCILIATION_INVALID", "request is required")
	}
	rows, err := s.reconciliation.ListActions(ctx, biz.Actor{ID: claims.UserID, Admin: true}, req.Id, req.AfterId, req.PageSize)
	if err != nil {
		return nil, err
	}
	result := &pb.ListReconciliationActionsReply{Actions: make([]*pb.ReconciliationActionInfo, len(rows))}
	for i, row := range rows {
		result.Actions[i] = toProtoReconciliationAction(row)
	}
	return result, nil
}

func toProtoReconciliationAction(row biz.ReconciliationAction) *pb.ReconciliationActionInfo {
	return &pb.ReconciliationActionInfo{Id: row.ID, PaymentId: row.PaymentID, ActorId: row.ActorID, Action: row.Action, FromStatus: row.FromStatus, ToStatus: row.ToStatus, FromVersion: row.FromVersion, ToVersion: row.ToVersion, IdempotencyKey: row.IdempotencyKey, Reason: row.Reason, Evidence: row.Evidence, JobId: row.JobID, CreatedAt: timestamppb.New(row.CreatedAt)}
}
