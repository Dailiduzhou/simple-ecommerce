package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func statePayment(status string) db.Payment {
	return db.Payment{ID: 1, OrderID: 2, UserID: 3, AmountMinor: 10000, Currency: "CNY", Status: status, PayChannel: "wechat:native", OutTradeNo: "pay_1"}
}
func stateResult(amount int64) *biz.PaymentQueryResult {
	return &biz.PaymentQueryResult{Method: biz.PaymentMethod{Provider: "wechat", Product: "native"}, OutTradeNo: "pay_1", TransactionID: "tx_1", TradeState: biz.TradeStateSuccess, Amount: amount, Currency: "CNY"}
}

func TestOrderExpiry_RejectsEarlyDelivery(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{
		ID:        2,
		Status:    biz.OrderStatusPendingPayment,
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}, nil)
	repo := NewOrderExpiryRepo(nil, testTxManager{q: q}, nil, log.DefaultLogger)
	require.ErrorIs(t, repo.ExpireOrder(context.Background(), 2), biz.ErrOrderNotExpired)
}

func TestApplyPayQuery_AmountMismatchPersistsReconciliationWithoutSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	reconciled := payment
	reconciled.ReconciliationStatus = biz.ReconciliationStatusRequired
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).Return(reconciled, nil)
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Contains(t, args.LastError, "amount")
		require.Equal(t, "amount_mismatch", args.Reason)
		return db.PaymentReconciliationFailure{}, nil
	})
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(1)))
}

func TestApplyPayQuery_SuccessUsesPaymentAndOrderCAS(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	succeeded := payment
	succeeded.Status = biz.PaymentStatusSuccess
	succeeded.ThirdPartyTxID = pgtype.Text{String: "tx_1", Valid: true}
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().RecordPaymentSuccess(gomock.Any(), db.RecordPaymentSuccessParams{ID: 1, ThirdPartyTxID: pgtype.Text{String: "tx_1", Valid: true}}).Return(succeeded, nil)
	q.EXPECT().MarkOrderPaid(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(10000)))
}

func TestApplyPayQuery_LateSuccessMovesClosedPaymentToReconciliation(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusClosed)
	succeeded := payment
	succeeded.Status = biz.PaymentStatusSuccess
	succeeded.ThirdPartyTxID = pgtype.Text{String: "tx_1", Valid: true}
	reconciled := payment
	reconciled.Status = biz.PaymentStatusSuccess
	reconciled.ReconciliationStatus = biz.ReconciliationStatusRequired
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusCancelled}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().RecordPaymentSuccess(gomock.Any(), gomock.Any()).Return(succeeded, nil)
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).Return(reconciled, nil)
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Equal(t, "late_success_after_cancel", args.Reason)
		return db.PaymentReconciliationFailure{}, nil
	})
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusCancelled}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(10000)))
}

func TestApplyPayQuery_ClosingOnePaymentKeepsOrderWhenAnotherPaymentIsActive(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	other := statePayment(biz.PaymentStatusCreating)
	other.ID = 9
	closed := payment
	closed.Status = biz.PaymentStatusClosed
	result := stateResult(10000)
	result.TradeState = biz.TradeStateClosed
	result.TransactionID = ""
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment, other}, nil)
	q.EXPECT().MarkPaymentClosed(gomock.Any(), int64(1)).Return(closed, nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, result))
}

func TestApplyPayQuery_DuplicateSuccessRecordsFactAndRequiresReconciliation(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	other := statePayment(biz.PaymentStatusSuccess)
	other.ID = 9
	succeeded := payment
	succeeded.Status = biz.PaymentStatusSuccess
	reconciled := succeeded
	reconciled.ReconciliationStatus = biz.ReconciliationStatusRequired
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment, other}, nil)
	q.EXPECT().RecordPaymentSuccess(gomock.Any(), gomock.Any()).Return(succeeded, nil)
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).Return(reconciled, nil)
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Equal(t, "duplicate_success", args.Reason)
		return db.PaymentReconciliationFailure{}, nil
	})
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(10000)))
}

func TestApplyPayQuery_PayErrorOnClosePendingFailsPaymentAndCancelsOrder(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusClosePending)
	failed := payment
	failed.Status = biz.PaymentStatusFailed
	result := stateResult(0)
	result.TradeState = biz.TradeStatePayError
	result.TradeStateDesc = "user payment failed"
	result.TransactionID = ""
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().MarkPaymentFailed(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.MarkPaymentFailedParams) (db.Payment, error) {
		require.Equal(t, int64(1), args.ID)
		require.Equal(t, "user payment failed", args.LastError.String)
		return failed, nil
	})
	q.EXPECT().MarkOrderCancelling(gomock.Any(), int64(2)).Return(db.Order{ID: 2, Status: biz.OrderStatusCancelling}, nil)
	q.EXPECT().RestoreOrderItemStock(gomock.Any(), int64(2)).Return(nil)
	q.EXPECT().MarkOrderCancelled(gomock.Any(), int64(2)).Return(db.Order{ID: 2, Status: biz.OrderStatusCancelled}, nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusCancelled}, nil)
	q.EXPECT().ListOrderItems(gomock.Any(), int64(2)).Return([]db.OrderItem{{OrderID: 2, ProductID: 7}}, nil)
	q.EXPECT().GetProduct(gomock.Any(), int64(7)).Return(db.Product{ID: 7, CategoryID: 5}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat", Trigger: "close_pay"}, result))
}

func TestApplyPayQuery_PayErrorOnPendingKeepsOrderPayable(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	failed := payment
	failed.Status = biz.PaymentStatusFailed
	result := stateResult(0)
	result.TradeState = biz.TradeStatePayError
	result.TransactionID = ""
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().MarkPaymentFailed(gomock.Any(), gomock.Any()).Return(failed, nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	// 轮询触发的 PAYERROR:支付置为 failed,订单保持可支付,不做取消。
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat", Trigger: "prepay"}, result))
}

func TestOrderExpiry_RefundedPaymentOnPendingOrderRequiresReconciliation(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	refunded := statePayment(biz.PaymentStatusRefunded)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{refunded}, nil)
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.RequirePaymentReconciliationParams) (db.Payment, error) {
		require.Equal(t, "refunded_on_pending_order", args.ReconciliationReason.String)
		reconciled := refunded
		reconciled.ReconciliationStatus = biz.ReconciliationStatusRequired
		return reconciled, nil
	})
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Equal(t, "refunded_on_pending_order", args.Reason)
		require.Equal(t, "wechat", args.Provider)
		return db.PaymentReconciliationFailure{}, nil
	})
	d := newTestData(t, q, redisServer)
	repo := NewOrderExpiryRepo(d, testTxManager{q: q}, nil, log.DefaultLogger)
	err := repo.ExpireOrder(context.Background(), 2)
	require.ErrorIs(t, err, biz.ErrPaymentReconciliationRequired)
}

func TestOrderExpiry_CancelInvalidatesProductCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	failed := statePayment(biz.PaymentStatusFailed)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{failed}, nil)
	q.EXPECT().MarkOrderCancelling(gomock.Any(), int64(2)).Return(db.Order{ID: 2, Status: biz.OrderStatusCancelling}, nil)
	q.EXPECT().RestoreOrderItemStock(gomock.Any(), int64(2)).Return(nil)
	q.EXPECT().MarkOrderCancelled(gomock.Any(), int64(2)).Return(db.Order{ID: 2, Status: biz.OrderStatusCancelled}, nil)
	q.EXPECT().ListOrderItems(gomock.Any(), int64(2)).Return([]db.OrderItem{{OrderID: 2, ProductID: 7}}, nil)
	q.EXPECT().GetProduct(gomock.Any(), int64(7)).Return(db.Product{ID: 7, CategoryID: 5}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewOrderExpiryRepo(d, testTxManager{q: q}, nil, log.DefaultLogger)
	require.NoError(t, repo.ExpireOrder(context.Background(), 2))
	require.True(t, redisServer.Exists("product:list:gen"))
}

func TestApplyPayQuery_RefundSettlesPendingRefundRecord(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusSuccess)
	result := stateResult(10000)
	result.TradeState = biz.TradeStateRefund
	refund := db.OrderRefund{ID: 11, OrderID: 2, UserID: 3, OutRefundNo: "rfnd_11", Status: biz.PaymentRefundStatusPending}
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), gomock.Any()).Return(refund, nil)
	q.EXPECT().MarkOrderRefundSuccess(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.MarkOrderRefundSuccessParams) (db.OrderRefund, error) {
		require.Equal(t, int64(11), args.ID)
		settled := refund
		settled.Status = biz.PaymentRefundStatusSuccess
		return settled, nil
	})
	q.EXPECT().UpdatePaymentRefunded(gomock.Any(), int64(1)).Return(int64(1), nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, result))
}

func TestApplyPayQuery_RefundWithoutLocalRecordRequiresReconciliation(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusSuccess)
	reconciled := payment
	reconciled.ReconciliationStatus = biz.ReconciliationStatusRequired
	result := stateResult(10000)
	result.TradeState = biz.TradeStateRefund
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), gomock.Any()).Return(db.OrderRefund{}, pgx.ErrNoRows)
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.RequirePaymentReconciliationParams) (db.Payment, error) {
		require.Equal(t, "provider_side_refund", args.ReconciliationReason.String)
		return reconciled, nil
	})
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.CreatePaymentReconciliationFailureParams) (db.PaymentReconciliationFailure, error) {
		require.Equal(t, "provider_side_refund", args.Reason)
		return db.PaymentReconciliationFailure{}, nil
	})
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPaid}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, result))
}

func TestApplyPayQuery_ReconciliationAlreadyProcessingIsIdempotent(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Times(2).DoAndReturn(func(_ context.Context, _ int64) (db.Payment, error) {
		// First call snapshots the payment; the second is the reentry read
		// after RequirePaymentReconciliation matched no rows.
		return payment, nil
	})
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).Return(db.Payment{}, pgx.ErrNoRows)
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).Return(db.PaymentReconciliationFailure{}, nil)
	q.EXPECT().GetOrder(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	// The amount mismatch path must not blow up when reconciliation was
	// already marked processing/resolved: the transaction still commits.
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(9999)))
}

func TestApplyPayQuery_DuplicateThirdPartyTxSettlesInFreshTransaction(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	redisServer := miniredis.RunT(t)
	payment := statePayment(biz.PaymentStatusPending)
	reconciled := payment
	reconciled.ReconciliationStatus = biz.ReconciliationStatusRequired
	q.EXPECT().GetPayment(gomock.Any(), int64(1)).Return(payment, nil)
	q.EXPECT().GetOrderForUpdate(gomock.Any(), int64(2)).Return(db.Order{ID: 2, UserID: 3, Status: biz.OrderStatusPendingPayment}, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), int64(2)).Return([]db.Payment{payment}, nil)
	q.EXPECT().RecordPaymentSuccess(gomock.Any(), gomock.Any()).Return(db.Payment{}, &pgconn.PgError{
		Code: "23505", ConstraintName: "idx_payments_third_party_tx_id_channel",
	})
	q.EXPECT().RequirePaymentReconciliation(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, args db.RequirePaymentReconciliationParams) (db.Payment, error) {
		require.Equal(t, "duplicate_third_party_tx", args.ReconciliationReason.String)
		return reconciled, nil
	})
	q.EXPECT().CreatePaymentReconciliationFailure(gomock.Any(), gomock.Any()).Return(db.PaymentReconciliationFailure{}, nil)
	d := newTestData(t, q, redisServer)
	repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
	require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"}, stateResult(10000)))
}
