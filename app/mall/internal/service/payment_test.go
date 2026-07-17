package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type servicePaymentUsecase struct {
	prepayArgs biz.PrepayForOrderArgs
	payment    *biz.PaymentDO
}

func (f *servicePaymentUsecase) PrepayForOrder(_ context.Context, args biz.PrepayForOrderArgs) (*biz.PrepayForOrderResult, error) {
	f.prepayArgs = args
	return &biz.PrepayForOrderResult{Payment: f.payment, Prepay: &biz.PaymentPrepayResult{Action: biz.PaymentAction{Type: biz.PaymentActionInvoke, Payload: []byte(`{"sdk":"value"}`)}}}, nil
}
func (f *servicePaymentUsecase) GetPayment(context.Context, int64, int64) (*biz.PaymentDO, error) {
	if f.payment == nil {
		return nil, biz.ErrPaymentNotFound
	}
	return f.payment, nil
}
func (f *servicePaymentUsecase) GetPaymentByOrder(context.Context, int64, int64) (*biz.PaymentDO, error) {
	return f.payment, nil
}
func (f *servicePaymentUsecase) QueryPayment(context.Context, string, int64) (*biz.PaymentQueryResult, error) {
	return nil, nil
}
func (f *servicePaymentUsecase) ClosePayment(context.Context, string, int64) (*biz.PaymentCloseResult, error) {
	return nil, nil
}
func (f *servicePaymentUsecase) CreateCheckJob(context.Context, int64, int, time.Duration, time.Duration, string) (*biz.MQJob, error) {
	return nil, nil
}
func (f *servicePaymentUsecase) HandleNotification(context.Context, string, *http.Request) error {
	return nil
}

func authenticatedPaymentContext(userID int64, role string) context.Context {
	return biz.WithClaims(context.Background(), &biz.EcommerceClaims{UserID: userID, Role: role})
}

func TestPaymentService_CreatePaymentPassesNeutralMethodAndAction(t *testing.T) {
	uc := &servicePaymentUsecase{payment: &biz.PaymentDO{ID: 1, UserID: 42}}
	service := NewPaymentService(uc, nil, log.DefaultLogger)
	reply, err := service.CreatePayment(authenticatedPaymentContext(42, "user"), &pb.CreatePaymentReq{OrderNo: "order_1", Method: "newpay:embedded", TotalAmount: 1})
	require.NoError(t, err)
	require.Equal(t, biz.PaymentMethod{Provider: "newpay", Product: "embedded"}, uc.prepayArgs.Method)
	require.Equal(t, int64(42), uc.prepayArgs.UserID)
	require.Equal(t, "invoke", reply.ActionType)
	require.JSONEq(t, `{"sdk":"value"}`, reply.Payload)
}

func TestPaymentService_CreatePaymentRequiresAuthentication(t *testing.T) {
	service := NewPaymentService(&servicePaymentUsecase{}, nil, log.DefaultLogger)
	_, err := service.CreatePayment(context.Background(), &pb.CreatePaymentReq{OrderNo: "order_1", Method: "alipay:wap"})
	require.True(t, errors.IsUnauthorized(err))
}

func TestPaymentService_RefundReturnsNotImplementedAfterOwnershipCheck(t *testing.T) {
	service := NewPaymentService(&servicePaymentUsecase{payment: &biz.PaymentDO{ID: 1, UserID: 42}}, nil, log.DefaultLogger)
	_, err := service.RefundPayment(authenticatedPaymentContext(42, "user"), &pb.RefundPaymentRequest{Id: 1})
	require.Equal(t, int32(501), errors.FromError(err).Code)
}
