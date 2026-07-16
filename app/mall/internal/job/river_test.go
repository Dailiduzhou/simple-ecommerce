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
	result *biz.PaymentQueryResult
	err    error
	query  biz.PaymentQueryRequest
}

func (g *workerGateway) Capabilities(biz.PaymentMethod) (biz.PaymentCapabilities, error) {
	return biz.PaymentCapabilities{SupportsClose: true}, nil
}
func (g *workerGateway) Prepay(context.Context, biz.PaymentPrepayRequest) (*biz.PaymentPrepayResult, error) {
	return nil, nil
}
func (g *workerGateway) Query(_ context.Context, req biz.PaymentQueryRequest) (*biz.PaymentQueryResult, error) {
	g.query = req
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
	payment     *biz.PaymentDO
	applied     bool
	appliedArgs biz.CheckPayArgs
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
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 1}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat", Trigger: "callback"}}
	require.NoError(t, worker.Work(context.Background(), job))
	require.True(t, repo.applied)
	require.Equal(t, "pay_8", gateway.query.OutTradeNo)
	require.Equal(t, "callback", repo.appliedArgs.Trigger)
}

func TestCheckPayWorker_TechnicalQueryErrorUsesRiverRetry(t *testing.T) {
	payment := &biz.PaymentDO{ID: 8, Method: "wechat:native", OutTradeNo: "pay_8"}
	worker := NewCheckPayWorker(&workerGateway{err: fmt.Errorf("timeout")}, &workerRepo{payment: payment}, log.DefaultLogger)
	job := &river.Job[biz.CheckPayArgs]{JobRow: &rivertype.JobRow{Attempt: 2}, Args: biz.CheckPayArgs{PaymentID: 8, Provider: "wechat"}}
	err := worker.Work(context.Background(), job)
	require.EqualError(t, err, "timeout")
	require.True(t, worker.NextRetry(job).After(time.Now()))
}
