package service

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/observability"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type PaymentService struct {
	pb.UnimplementedPaymentServer
	paymentUc       biz.PaymentUsecase
	paymentJobs     biz.PaymentJobUsecase
	log             *log.Helper
	callbackLimiter *providerCallbackLimiter
}

func NewPaymentService(paymentUc biz.PaymentUsecase, paymentJobs biz.PaymentJobUsecase, logger log.Logger) *PaymentService {
	return &PaymentService{paymentUc: paymentUc, paymentJobs: paymentJobs, log: log.NewHelper(logger), callbackLimiter: newProviderCallbackLimiter(120)}
}

func (s *PaymentService) CreatePayment(ctx context.Context, req *pb.CreatePaymentReq) (*pb.CreatePaymentReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.OrderNo) == "" {
		return nil, errors.BadRequest("ORDER_NO_REQUIRED", "order_no is required")
	}
	method, err := biz.ParsePaymentMethod(req.Method)
	if err != nil {
		return nil, err
	}
	result, err := s.paymentUc.PrepayForOrder(ctx, biz.PrepayForOrderArgs{OrderNo: req.OrderNo, UserID: claims.UserID, Method: method, ClientIP: req.ClientIp, Extension: req.ExtraParams, Description: req.Description})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Prepay == nil {
		return nil, errors.InternalServer("PREPAY_RESULT_EMPTY", "prepay result is empty")
	}
	return &pb.CreatePaymentReply{
		PaymentId: result.Payment.ID, OutTradeNo: result.Payment.OutTradeNo,
		ActionType: string(result.Prepay.Action.Type), Payload: append([]byte(nil), result.Prepay.Action.Payload...),
	}, nil
}

func (s *PaymentService) QueryPayment(ctx context.Context, req *pb.QueryPaymentReq) (*pb.QueryPaymentReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.OutTradeNo == "" {
		return nil, errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	result, err := s.paymentUc.QueryPayment(ctx, req.OutTradeNo, claims.UserID)
	if err != nil {
		return nil, err
	}
	return &pb.QueryPaymentReply{OutTradeNo: result.OutTradeNo, TransactionId: result.TransactionID, TradeState: toProtoTradeState(result.TradeState), AmountMinor: result.Amount, Currency: result.Currency}, nil
}

func (s *PaymentService) ClosePayment(ctx context.Context, req *pb.ClosePaymentReq) (*pb.ClosePaymentReply, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.OutTradeNo == "" {
		return nil, errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
	}
	result, err := s.paymentUc.ClosePayment(ctx, req.OutTradeNo, claims.UserID)
	if err != nil {
		return nil, err
	}
	return &pb.ClosePaymentReply{Success: result.Success}, nil
}

func (s *PaymentService) GetPayment(ctx context.Context, req *pb.GetPaymentRequest) (*pb.PaymentInfo, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("PAYMENT_REQUEST_REQUIRED", "request is required")
	}
	payment, err := s.paymentUc.GetPayment(ctx, req.Id, claims.UserID)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

func (s *PaymentService) GetPaymentByOrder(ctx context.Context, req *pb.GetPaymentByOrderRequest) (*pb.PaymentInfo, error) {
	claims, err := authenticatedClaims(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("PAYMENT_REQUEST_REQUIRED", "request is required")
	}
	payment, err := s.paymentUc.GetPaymentByOrder(ctx, req.OrderId, claims.UserID)
	if err != nil {
		return nil, err
	}
	return toProtoPaymentInfo(payment), nil
}

func (s *PaymentService) RefundPayment(ctx context.Context, req *pb.RefundPaymentRequest) (*pb.RefundPaymentReply, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.Id <= 0 {
		return nil, errors.BadRequest("PAYMENT_REQUEST_REQUIRED", "request is required")
	}
	if _, err := s.paymentUc.RefundPayment(ctx, req.Id); err != nil {
		return nil, err
	}
	return &pb.RefundPaymentReply{}, nil
}

func (s *PaymentService) CreatePaymentCheckJob(ctx context.Context, req *pb.CreatePaymentCheckJobRequest) (*pb.MQJobInfo, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if req == nil || req.PaymentId <= 0 || req.DelaySeconds < 0 {
		return nil, errors.BadRequest("PAYMENT_CHECK_REQUEST_INVALID", "payment_id and non-negative delay are required")
	}
	pollInterval := time.Duration(req.PollIntervalSeconds) * time.Second
	job, err := s.paymentUc.CreateCheckJob(ctx, req.PaymentId, int(req.MaxPolls), pollInterval, time.Duration(req.DelaySeconds)*time.Second, req.Trigger)
	if err != nil {
		return nil, err
	}
	return toProtoMQJob(job), nil
}

func (s *PaymentService) GetMQJob(ctx context.Context, req *pb.GetMQJobRequest) (*pb.MQJobInfo, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if s.paymentJobs == nil {
		return nil, errors.ServiceUnavailable("PAYMENT_MQ_NOT_CONFIGURED", "payment mq is not configured")
	}
	job, err := s.paymentJobs.GetMQJob(ctx, req.JobId)
	if err != nil {
		return nil, err
	}
	return toProtoMQJob(job), nil
}

func (s *PaymentService) HandlePaymentNotify(ctx khttp.Context) error {
	return s.handleNotify(ctx, ctx.Vars().Get("provider"))
}

func (s *PaymentService) handleNotify(ctx khttp.Context, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	checker, ok := s.paymentUc.(biz.PaymentNotificationProviderChecker)
	if !ok || !checker.SupportsNotificationProvider(provider) {
		s.log.WithContext(ctx).Warnw("msg", "unsupported payment callback provider", "provider", provider)
		return s.writeProviderAck(ctx, provider, false)
	}
	limiterKey := callbackLimiterKey(provider, ctx.Request().RemoteAddr)
	if !s.callbackLimiter.Allow(limiterKey, time.Now()) {
		s.log.WithContext(ctx).Warnw("msg", "payment callback rate limited", "provider", provider)
		return s.writeProviderAck(ctx, provider, false)
	}
	callbackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := s.paymentUc.HandleNotification(callbackCtx, provider, ctx.Request())
	if err != nil {
		observability.PaymentCallback(ctx, provider, "failure")
		observability.PaymentCallbackPersistFailure(ctx, provider)
		s.log.WithContext(ctx).Errorw("msg", "payment callback rejected", "provider", provider, "result", "failure", "error", err)
		return s.writeProviderAck(ctx, provider, false)
	}
	observability.PaymentCallback(ctx, provider, "success")
	s.log.WithContext(ctx).Infow("msg", "payment callback persisted", "provider", provider, "result", "success")
	return s.writeProviderAck(ctx, provider, true)
}

type providerCallbackLimiter struct {
	mu                sync.Mutex
	limit             int
	maxKeys           int
	lastCleanupMinute int64
	windows           map[string]callbackWindow
}
type callbackWindow struct {
	minute int64
	count  int
}

func newProviderCallbackLimiter(limit int) *providerCallbackLimiter {
	return &providerCallbackLimiter{limit: limit, maxKeys: 4096, windows: make(map[string]callbackWindow)}
}
func (l *providerCallbackLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	minute := now.Unix() / 60
	if l.lastCleanupMinute != minute {
		for existingKey, window := range l.windows {
			if window.minute < minute {
				delete(l.windows, existingKey)
			}
		}
		l.lastCleanupMinute = minute
	}
	window, exists := l.windows[key]
	if !exists && len(l.windows) >= l.maxKeys {
		return false
	}
	if window.minute != minute {
		window = callbackWindow{minute: minute}
	}
	if window.count >= l.limit {
		l.windows[key] = window
		return false
	}
	window.count++
	l.windows[key] = window
	return true
}

// callbackLimiterKey deliberately uses the transport-level RemoteAddr and
// ignores proxy headers like X-Forwarded-For, which callers could forge to
// bypass the limiter. Consequence: behind a load balancer all callbacks share
// one bucket and are limited as a whole. If this service is ever deployed
// behind a trusted proxy, plumb the real client IP through a configured,
// trusted proxy list instead of changing this function to read headers.
func callbackLimiterKey(provider, remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		remoteAddr = host
	}
	if remoteAddr == "" {
		remoteAddr = "unknown"
	}
	return provider + "|" + remoteAddr
}

func (s *PaymentService) writeProviderAck(ctx khttp.Context, provider string, success bool) error {
	ack := biz.DefaultPaymentNotificationAck()
	if acknowledger, ok := s.paymentUc.(biz.PaymentNotificationAcknowledger); ok {
		ack = acknowledger.NotificationAck(provider, success)
	}
	if ack.ContentType != "" {
		ctx.Response().Header().Set("Content-Type", ack.ContentType)
	}
	if ack.StatusCode > 0 {
		ctx.Response().WriteHeader(ack.StatusCode)
	}
	_, err := ctx.Response().Write(ack.Body)
	return err
}

func toProtoTradeState(state biz.TradeState) pb.TradeState {
	switch state {
	case biz.TradeStateSuccess:
		return pb.TradeState_SUCCESS
	case biz.TradeStateRefund:
		return pb.TradeState_REFUND
	case biz.TradeStateNotPay:
		return pb.TradeState_NOTPAY
	case biz.TradeStateClosed:
		return pb.TradeState_CLOSED
	case biz.TradeStateRevoked:
		return pb.TradeState_REVOKED
	case biz.TradeStateUserPaying:
		return pb.TradeState_USERPAYING
	case biz.TradeStatePayError:
		return pb.TradeState_PAYERROR
	default:
		return pb.TradeState_TRADE_STATE_UNSPECIFIED
	}
}
func toProtoPaymentInfo(payment *biz.PaymentDO) *pb.PaymentInfo {
	if payment == nil {
		return nil
	}
	result := &pb.PaymentInfo{
		Id: payment.ID, OrderId: payment.OrderID, UserId: payment.UserID, MerchantId: payment.MerchantID,
		AmountMinor: payment.Amount, Currency: payment.Currency, Status: payment.Status, Method: payment.Method,
		ThirdPartyTxId: payment.ThirdPartyTxID, OutTradeNo: payment.OutTradeNo, CreatedAt: timestamppb.New(payment.CreatedAt),
		ReconciliationStatus: payment.ReconciliationStatus, ReconciliationReason: payment.ReconciliationReason,
	}
	if payment.PaidAt != nil {
		result.PaidAt = timestamppb.New(*payment.PaidAt)
	}
	return result
}
func toProtoMQJob(job *biz.MQJob) *pb.MQJobInfo {
	if job == nil {
		return nil
	}
	result := &pb.MQJobInfo{JobId: job.ID, Kind: job.Kind, Queue: job.Queue, State: job.State, Attempt: int32(job.Attempt), MaxAttempts: int32(job.MaxAttempts), ArgsJson: job.ArgsJSON, Tags: job.Tags, CreatedAt: timestamppb.New(job.CreatedAt), ScheduledAt: timestamppb.New(job.ScheduledAt), Errors: make([]*pb.MQJobError, len(job.Errors))}
	if job.AttemptedAt != nil {
		result.AttemptedAt = timestamppb.New(*job.AttemptedAt)
	}
	if job.FinalizedAt != nil {
		result.FinalizedAt = timestamppb.New(*job.FinalizedAt)
	}
	for i, item := range job.Errors {
		result.Errors[i] = &pb.MQJobError{Attempt: int32(item.Attempt), Error: item.Error, At: timestamppb.New(item.At)}
	}
	return result
}
