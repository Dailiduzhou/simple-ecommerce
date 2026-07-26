package job

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type workerGateway struct {
	result  *biz.PaymentQueryResult
	err     error
	query   biz.PaymentQueryRequest
	queries int
}

func (g *workerGateway) Capabilities(biz.PaymentMethod) (biz.PaymentCapabilities, error) {
	return biz.PaymentCapabilities{SupportsClose: true}, nil
}
func (g *workerGateway) Prepay(context.Context, biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	return nil, nil
}
func (g *workerGateway) Query(_ context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	g.query = req
	g.queries++
	return g.result, g.err
}
func (g *workerGateway) Close(context.Context, biz.PaymentCloseRequest) (*biz.PaymentCloseResult, error) {
	return &biz.PaymentCloseResult{Success: true}, nil
}
func (g *workerGateway) ParseAndVerifyNotification(string, *http.Request) (*biz.PaymentNotification, error) {
	return nil, nil
}
func (g *workerGateway) NotificationAck(string, bool) (biz.PaymentNotificationAck, error) {
	return biz.DefaultPaymentNotificationAck(), nil
}

type workerRepo struct {
	payment              *biz.PaymentDO
	applied              bool
	appliedArgs          biz.CheckPayArgs
	notificationBegun    int64
	notificationBeginErr error
	skipNotification     bool
	notificationError    string
	notificationFailed   string
}

func (r *workerRepo) CreatePayment(context.Context, biz.CreatePaymentArgs) (*biz.PaymentDO, error) {
	return nil, nil
}
func (r *workerRepo) MarkPaymentPending(context.Context, int64, biz.PaymentAction) (*biz.PaymentDO, error) {
	return nil, nil
}
func (r *workerRepo) GetPayment(context.Context, int64) (*biz.PaymentDO, error) {
	return r.payment, nil
}
func (r *workerRepo) GetPaymentByUser(context.Context, int64, int64) (*biz.PaymentDO, error) {
	return r.payment, nil
}
func (r *workerRepo) GetLatestPaymentByOrder(context.Context, int64) (*biz.PaymentDO, error) {
	return r.payment, nil
}
func (r *workerRepo) GetActivePaymentByOrderMethod(context.Context, int64, string) (*biz.PaymentDO, error) {
	return r.payment, nil
}
func (r *workerRepo) GetPaymentByOutTradeNo(context.Context, string) (*biz.PaymentDO, error) {
	return r.payment, nil
}
func (r *workerRepo) BeginPaymentNotificationProcessing(_ context.Context, id int64, _, _ string) (bool, error) {
	r.notificationBegun = id
	return !r.skipNotification, r.notificationBeginErr
}
func (r *workerRepo) RecordPaymentNotificationError(_ context.Context, _ int64, lastError string) error {
	r.notificationError = lastError
	return nil
}
func (r *workerRepo) MarkPaymentNotificationFailed(_ context.Context, _ int64, lastError string) error {
	r.notificationFailed = lastError
	return nil
}
func (r *workerRepo) ApplyPayQuery(_ context.Context, args biz.CheckPayArgs, _ *biz.PaymentQueryResult) error {
	r.applied = true
	r.appliedArgs = args
	return nil
}
func (r *workerRepo) MarkPayClosePending(context.Context, biz.CheckPayArgs) error { return nil }
func (r *workerRepo) MarkReconciliationRequired(context.Context, biz.ReconciliationFailure) error {
	return nil
}
func (r *workerRepo) RecordReconciliationFailure(context.Context, biz.ReconciliationFailure) error {
	return nil
}

func TestCheckPayWorker_AppliesTerminalProviderResult(t *testing.T) {
	method := biz.PaymentMethod{Provider: "wechat", Product: "native"}
	payment := &biz.PaymentDO{ID: 8, Method: method.String(), OutTradeNo: "pay_8", Amount: 10000, Currency: "CNY"}
	gateway := &workerGateway{result: &biz.PaymentQueryResult{Method: method, OutTradeNo: "pay_8", TransactionID: "tx_8", TradeState: biz.TradeStateSuccess, Amount: 10000, Currency: "CNY"}}
	repo := &workerRepo{payment: payment}
	worker := NewCheckPayWorker(gateway, repo, log.DefaultLogger)
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat", NotificationID: 17, Trigger: "callback"}}
	require.NoError(t, worker.Work(context.Background(), job))
	require.True(t, repo.applied)
	require.Equal(t, "pay_8", gateway.query.OutTradeNo)
	require.Equal(t, "callback", repo.appliedArgs.Trigger)
	require.Equal(t, int64(17), repo.notificationBegun)
}

func TestCheckPayWorker_TechnicalQueryErrorUsesRiverRetry(t *testing.T) {
	payment := &biz.PaymentDO{ID: 8, Method: "wechat:native", OutTradeNo: "pay_8"}
	repo := &workerRepo{payment: payment}
	worker := NewCheckPayWorker(&workerGateway{err: fmt.Errorf("timeout")}, repo, log.DefaultLogger)
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 2}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat", NotificationID: 17}}
	err := worker.Work(context.Background(), job)
	require.EqualError(t, err, "timeout")
	require.Equal(t, "timeout", repo.notificationError)
	require.True(t, worker.NextRetry(job).After(time.Now()))
}

func TestCheckPayWorker_AlreadyProcessedNotificationSkipsProviderQuery(t *testing.T) {
	payment := &biz.PaymentDO{ID: 8, Method: "wechat:native", OutTradeNo: "pay_8"}
	gateway := &workerGateway{}
	repo := &workerRepo{payment: payment, skipNotification: true}
	worker := NewCheckPayWorker(gateway, repo, log.DefaultLogger)
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat", NotificationID: 17}}
	require.NoError(t, worker.Work(context.Background(), job))
	require.Zero(t, gateway.queries)
	require.False(t, repo.applied)
}

func TestCheckPayWorker_PermanentMismatchFailsNotification(t *testing.T) {
	payment := &biz.PaymentDO{ID: 8, Method: "alipay:app", OutTradeNo: "pay_8"}
	repo := &workerRepo{payment: payment}
	worker := NewCheckPayWorker(&workerGateway{}, repo, log.DefaultLogger)
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat", NotificationID: 17}}
	err := worker.Work(context.Background(), job)
	require.Error(t, err)
	require.Equal(t, "job provider does not match payment method", repo.notificationFailed)
}

func TestCheckPayWorker_NotificationBindingMismatchIsCancelled(t *testing.T) {
	payment := &biz.PaymentDO{ID: 8, Method: "wechat:native", OutTradeNo: "pay_8"}
	repo := &workerRepo{payment: payment, notificationBeginErr: biz.ErrPaymentNotificationBinding}
	worker := NewCheckPayWorker(&workerGateway{}, repo, log.DefaultLogger)
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat", NotificationID: 17}}
	err := worker.Work(context.Background(), job)
	require.Error(t, err)
	require.Equal(t, biz.ErrPaymentNotificationBinding.Error(), repo.notificationFailed)
}

func TestClosePayWorker_AppliesAuthoritativeTerminalState(t *testing.T) {
	method := biz.PaymentMethod{Provider: "wechat", Product: "native"}
	payment := &biz.PaymentDO{ID: 8, Method: method.String(), OutTradeNo: "pay_8", Amount: 10000, Currency: "CNY"}
	gateway := &workerGateway{result: &biz.PaymentQueryResult{
		Method: method, OutTradeNo: "pay_8", TransactionID: "tx_8",
		TradeState: biz.TradeStateSuccess, Amount: 10000, Currency: "CNY",
	}}
	repo := &workerRepo{payment: payment}
	worker := NewClosePayWorker(gateway, repo)
	job := &river.Job[biz.ClosePayArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1},
		Args:   biz.ClosePayArgs{PaymentID: 8, Provider: "wechat", Reason: "order_expired"},
	}
	require.NoError(t, worker.Work(context.Background(), job))
	require.True(t, repo.applied)
	require.Equal(t, "close_pay", repo.appliedArgs.Trigger)
}
