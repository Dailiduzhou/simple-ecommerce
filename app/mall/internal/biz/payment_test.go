package biz

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/require"
)

type paymentTestGateway struct {
	prepayReq    PaymentPrepayRequest
	prepayResult *PaymentPrepayResult
	closeResult  *PaymentCloseResult
	capabilities PaymentCapabilities
	txActive     *bool
}

func (g *paymentTestGateway) Capabilities(PaymentMethod) (PaymentCapabilities, error) {
	return g.capabilities, nil
}
func (g *paymentTestGateway) Prepay(_ context.Context, req PaymentPrepayRequest) (*PaymentPrepayResult, error) {
	if g.txActive != nil && *g.txActive {
		panic("provider call executed in database transaction")
	}
	g.prepayReq = req
	return g.prepayResult, nil
}
func (g *paymentTestGateway) Query(context.Context, PaymentQueryRequest) (*PaymentQueryResult, error) {
	return nil, nil
}
func (g *paymentTestGateway) Close(context.Context, PaymentCloseRequest) (*PaymentCloseResult, error) {
	if g.txActive != nil && *g.txActive {
		panic("provider close executed in database transaction")
	}
	return g.closeResult, nil
}
func (g *paymentTestGateway) ParseAndVerifyNotification(string, *http.Request) (*PaymentNotification, error) {
	return nil, nil
}
func (g *paymentTestGateway) NotificationAck(string, bool) (PaymentNotificationAck, error) {
	return DefaultPaymentNotificationAck(), nil
}

type paymentTestRepo struct {
	payment       *PaymentDO
	created       CreatePaymentArgs
	pendingAction PaymentAction
	closePending  bool
	applied       bool
}

func (r *paymentTestRepo) CreatePayment(_ context.Context, args CreatePaymentArgs) (*PaymentDO, error) {
	r.created = args
	r.payment = &PaymentDO{ID: 7, OrderID: args.OrderID, UserID: args.UserID, Amount: args.Amount, Currency: args.Currency, Method: args.Method, OutTradeNo: args.OutTradeNo, Status: PaymentStatusCreating}
	return r.payment, nil
}
func (r *paymentTestRepo) MarkPaymentPending(_ context.Context, _ int64, action PaymentAction) (*PaymentDO, error) {
	r.pendingAction = action
	r.payment.Status = PaymentStatusPending
	r.payment.Action = action
	return r.payment, nil
}
func (r *paymentTestRepo) GetPayment(context.Context, int64) (*PaymentDO, error) {
	return r.payment, nil
}
func (r *paymentTestRepo) GetPaymentByUser(context.Context, int64, int64) (*PaymentDO, error) {
	return r.payment, nil
}
func (r *paymentTestRepo) GetLatestPaymentByOrder(context.Context, int64) (*PaymentDO, error) {
	return r.payment, nil
}
func (r *paymentTestRepo) GetActivePaymentByOrderMethod(context.Context, int64, string) (*PaymentDO, error) {
	return nil, ErrPaymentNotFound
}
func (r *paymentTestRepo) GetPaymentByOutTradeNo(context.Context, string) (*PaymentDO, error) {
	return r.payment, nil
}
func (r *paymentTestRepo) ApplyPayQuery(context.Context, CheckPayArgs, *PaymentQueryResult) error {
	r.applied = true
	return nil
}
func (r *paymentTestRepo) MarkPayClosePending(context.Context, CheckPayArgs) error {
	r.closePending = true
	return nil
}
func (r *paymentTestRepo) MarkReconciliationRequired(context.Context, ReconciliationFailure) error {
	return nil
}
func (r *paymentTestRepo) RecordReconciliationFailure(context.Context, ReconciliationFailure) error {
	return nil
}

type orderTestRepo struct{ order Order }

func (r *orderTestRepo) CreateOrder(context.Context, CreateOrderArgs) (Order, error) {
	return Order{}, nil
}
func (r *orderTestRepo) GetOrder(context.Context, int64) (Order, error) { return r.order, nil }
func (r *orderTestRepo) GetOrderByOrderNo(context.Context, string) (Order, error) {
	return r.order, nil
}
func (r *orderTestRepo) GetOrderByUser(context.Context, int64, int64) (Order, error) {
	return r.order, nil
}
func (r *orderTestRepo) HasOngoingOrders(context.Context, int64) (bool, error) { return false, nil }
func (r *orderTestRepo) ListOngoingOrdersByUser(context.Context, int64) ([]Order, error) {
	return nil, nil
}
func (r *orderTestRepo) ListOrdersByUser(context.Context, int64, int32, int32) ([]Order, error) {
	return nil, nil
}
func (r *orderTestRepo) CountOrdersByUser(context.Context, int64) (int64, error) { return 0, nil }
func (r *orderTestRepo) CancelOrderByUser(context.Context, int64, int64) error   { return nil }

type paymentTestTx struct{ active bool }

func (t *paymentTestTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	t.active = true
	defer func() { t.active = false }()
	return fn(ctx)
}

type paymentTestJobs struct {
	tx       *paymentTestTx
	enqueued bool
	args     CheckPayArgs
}

func (j *paymentTestJobs) EnqueueCheckPay(context.Context, CheckPayArgs, time.Duration) (*MQJob, error) {
	return nil, stderrors.New("non-transactional enqueue is forbidden in this test")
}
func (j *paymentTestJobs) EnqueueCheckPayTx(_ context.Context, args CheckPayArgs, _ time.Duration) (*MQJob, error) {
	if j.tx == nil || !j.tx.active {
		return nil, stderrors.New("enqueue did not run in transaction")
	}
	j.enqueued, j.args = true, args
	return &MQJob{ID: 1}, nil
}
func (j *paymentTestJobs) GetMQJob(context.Context, int64) (*MQJob, error) { return nil, nil }

type paymentTestID struct{}

func (paymentTestID) GenerateString() string                 { return "payment_99" }
func (paymentTestID) GenerateOrderNo32(string) string        { return "payment_99" }
func (paymentTestID) GenerateOrderNo64(string, int64) string { return "payment_99" }

func TestPrepayForOrder_UsesDatabaseAmountAndCallsProviderOutsideTransaction(t *testing.T) {
	tx := &paymentTestTx{}
	actionPayload := json.RawMessage(`{"url":"https://pay.example"}`)
	gateway := &paymentTestGateway{txActive: &tx.active, prepayResult: &PaymentPrepayResult{Action: PaymentAction{Type: PaymentActionRedirect, Payload: actionPayload}}}
	repo := &paymentTestRepo{}
	orders := &orderTestRepo{order: Order{ID: 5, UserID: 42, TotalAmount: 10000, Currency: "CNY", Status: OrderStatusPendingPayment, OutTradeNo: "order_5"}}
	uc := NewPaymentUsecase(gateway, repo, nil, orders, nil, tx, paymentTestID{}, log.DefaultLogger)

	result, err := uc.PrepayForOrder(context.Background(), PrepayForOrderArgs{OrderNo: "order_5", UserID: 42, Method: PaymentMethod{Provider: "alipay", Product: "wap"}})
	require.NoError(t, err)
	require.Equal(t, int64(10000), gateway.prepayReq.Amount)
	require.Equal(t, int64(10000), repo.created.Amount)
	require.Equal(t, "CNY", gateway.prepayReq.Currency)
	require.Equal(t, "Order order_5", gateway.prepayReq.Description)
	require.Equal(t, PaymentActionRedirect, result.Prepay.Action.Type)
}

func TestClosePayment_PersistsIntentAndJobBeforeProviderCall(t *testing.T) {
	tx := &paymentTestTx{}
	payment := &PaymentDO{ID: 7, OrderID: 5, UserID: 42, Amount: 10000, Currency: "CNY", Method: "newpay:app", OutTradeNo: "payment_7", Status: PaymentStatusPending}
	repo := &paymentTestRepo{payment: payment}
	jobs := &paymentTestJobs{tx: tx}
	gateway := &paymentTestGateway{
		txActive: &tx.active, capabilities: PaymentCapabilities{SupportsClose: true},
		closeResult: &PaymentCloseResult{Method: PaymentMethod{Provider: "newpay", Product: "app"}, OutTradeNo: payment.OutTradeNo, Success: true},
	}
	uc := NewPaymentUsecase(gateway, repo, nil, &orderTestRepo{}, jobs, tx, paymentTestID{}, log.DefaultLogger)
	result, err := uc.ClosePayment(context.Background(), payment.OutTradeNo, payment.UserID)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, repo.closePending)
	require.True(t, jobs.enqueued)
	require.Equal(t, 1, jobs.args.MaxPolls)
	require.True(t, repo.applied)
}

func TestPrepayForOrder_RejectsDifferentOwner(t *testing.T) {
	orders := &orderTestRepo{order: Order{ID: 5, UserID: 42, TotalAmount: 10000, Currency: "CNY", Status: OrderStatusPendingPayment}}
	uc := NewPaymentUsecase(&paymentTestGateway{}, &paymentTestRepo{}, nil, orders, nil, &paymentTestTx{}, paymentTestID{}, log.DefaultLogger)
	_, err := uc.PrepayForOrder(context.Background(), PrepayForOrderArgs{OrderNo: "order_5", UserID: 7, Method: PaymentMethod{Provider: "alipay", Product: "wap"}})
	require.Error(t, err)
}

func TestPaymentMethodRequiresProviderAndProduct(t *testing.T) {
	method, err := ParsePaymentMethod(" WeChat:JSAPI ")
	require.NoError(t, err)
	require.Equal(t, "wechat:jsapi", method.String())
	_, err = ParsePaymentMethod("wechat")
	require.Error(t, err)
}

func TestPaymentGatewayWithoutProvidersReturnsAvailabilityError(t *testing.T) {
	gateway := NewPaymentGateway(nil)
	_, err := gateway.Prepay(context.Background(), PaymentPrepayRequest{Method: PaymentMethod{Provider: "alipay", Product: "wap"}})
	require.ErrorIs(t, err, ErrPaymentProviderUnavailable)
}

var _ = time.Second
