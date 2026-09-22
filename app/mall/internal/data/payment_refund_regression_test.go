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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func refundRow(payment db.Payment) db.OrderRefund {
	return db.OrderRefund{ID: 11, PaymentID: pgtype.Int8{Int64: payment.ID, Valid: true}, OrderID: payment.OrderID, UserID: payment.UserID,
		OutRefundNo: "refund_1", TotalAmountMinor: payment.AmountMinor, RefundAmountMinor: payment.AmountMinor, Currency: payment.Currency, Status: biz.PaymentRefundStatusPending}
}

func TestRefundSettlementKeepsCompensatingRefundSeparateFromOrder(t *testing.T) {
	for _, mode := range []string{"admin", "query"} {
		for _, scenario := range []string{"duplicate", "late_cancelled", "late_refunded", "last_payment"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				q := mockdb.NewMockQuerier(gomock.NewController(t))
				d := newTestData(t, q, miniredis.RunT(t))
				t.Cleanup(func() { _ = d.rdb.Close() })
				p := statePayment(biz.PaymentStatusSuccess)
				p.PayChannel = "alipay:wap"
				p.ThirdPartyTxID = pgtype.Text{String: "tx_1", Valid: true}
				refund := refundRow(p)
				order := db.Order{ID: p.OrderID, UserID: p.UserID, Status: biz.OrderStatusPaid}
				sibling := p
				sibling.ID = 9
				switch scenario {
				case "late_cancelled":
					order.Status = biz.OrderStatusCancelled
				case "late_refunded":
					order.Status = biz.OrderStatusRefunded
				case "last_payment":
					sibling.Status = biz.PaymentStatusRefunded
				}
				payments := []db.Payment{p, sibling}
				settled := p
				settled.Status = biz.PaymentStatusRefunded
				repo := NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger)
				if mode == "admin" {
					q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), p.ID).Return(order, nil)
					q.EXPECT().GetPaymentForUpdate(gomock.Any(), p.ID).Return(p, nil)
					q.EXPECT().ConfirmPaymentRefunded(gomock.Any(), p.ID).Return(settled, nil)
				} else {
					q.EXPECT().GetPayment(gomock.Any(), p.ID).Return(p, nil)
					q.EXPECT().GetOrderForUpdate(gomock.Any(), order.ID).Return(order, nil)
					q.EXPECT().UpdatePaymentRefunded(gomock.Any(), p.ID).Return(int64(1), nil)
					q.EXPECT().GetOrder(gomock.Any(), order.ID).Return(order, nil)
				}
				q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), refund.PaymentID).Return(refund, nil)
				q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), order.ID).Return(payments, nil)
				q.EXPECT().MarkOrderRefundSuccess(gomock.Any(), db.MarkOrderRefundSuccessParams{ID: refund.ID, PaymentID: refund.PaymentID}).Return(refund, nil)
				if scenario == "last_payment" {
					q.EXPECT().MarkOrderRefunded(gomock.Any(), order.ID).Return(order, nil)
					q.EXPECT().RestoreOrderItemStock(gomock.Any(), order.ID).Return(nil)
					q.EXPECT().ListOrderItems(gomock.Any(), order.ID).Return(nil, nil)
				}
				// For compensating refunds, unexpected order/inventory writes fail via gomock.
				if mode == "admin" {
					require.NoError(t, repo.ApplyPaymentRefund(context.Background(), p.ID, refund.ID))
				} else {
					result := &biz.PaymentQueryResult{Method: biz.PaymentMethod{Provider: "alipay", Product: "wap"}, OutTradeNo: p.OutTradeNo,
						TransactionID: p.ThirdPartyTxID.String, TradeState: biz.TradeStateRefund, Amount: p.AmountMinor, Currency: p.Currency}
					require.NoError(t, repo.ApplyPayQuery(context.Background(), biz.CheckPayArgs{PaymentID: p.ID, Provider: "alipay"}, result))
				}
			})
		}
	}
}

func TestPrepareRefundRejectsUnsupportedOrderBeforeCreatingIntent(t *testing.T) {
	for _, status := range []string{biz.OrderStatusPendingPayment, biz.OrderStatusCancelling, biz.OrderStatusShipped, biz.OrderStatusCompleted} {
		t.Run(status, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), int64(1)).Return(db.Order{ID: 2, Status: status}, nil)
			repo := NewPaymentRepo(nil, testTxManager{q: q}, log.DefaultLogger)
			_, _, err := repo.PreparePaymentRefund(context.Background(), 1, "refund")
			require.ErrorIs(t, err, biz.ErrPaymentStateConflict)
		})
	}
}

func TestPrepareRefundRejectsInFlightSibling(t *testing.T) {
	for _, status := range []string{biz.PaymentStatusCreating, biz.PaymentStatusPending, biz.PaymentStatusClosePending} {
		t.Run(status, func(t *testing.T) {
			q := mockdb.NewMockQuerier(gomock.NewController(t))
			p := statePayment(biz.PaymentStatusSuccess)
			sibling := p
			sibling.ID = 9
			sibling.Status = status
			gomock.InOrder(
				q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), p.ID).Return(db.Order{ID: p.OrderID, Status: biz.OrderStatusPaid}, nil),
				q.EXPECT().GetPaymentForUpdate(gomock.Any(), p.ID).Return(p, nil),
				q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), p.OrderID).Return([]db.Payment{p, sibling}, nil),
			)
			_, _, err := NewPaymentRepo(nil, testTxManager{q: q}, log.DefaultLogger).PreparePaymentRefund(context.Background(), p.ID, "refund")
			require.ErrorIs(t, err, biz.ErrPaymentStateConflict)
		})
	}
}

func TestRefundCommitInvalidatesOrderAndProductCaches(t *testing.T) {
	ctx := context.Background()
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	d := newTestData(t, q, mr)
	t.Cleanup(func() { _ = d.rdb.Close() })
	p := statePayment(biz.PaymentStatusSuccess)
	p.PayChannel = "alipay:wap"
	refund := refundRow(p)
	order := db.Order{ID: p.OrderID, UserID: p.UserID, Status: biz.OrderStatusPaid}
	items := []db.OrderItem{{OrderID: order.ID, ProductID: 7, Quantity: 1}}
	orders := NewOrderRepo(d, testTxManager{q: q}, log.DefaultLogger)
	q.EXPECT().ListOngoingOrdersByUser(gomock.Any(), p.UserID).Return([]db.Order{order}, nil)
	q.EXPECT().ListOrdersByUser(gomock.Any(), db.ListOrdersByUserParams{UserID: p.UserID, Limit: 10}).Return([]db.Order{order}, nil)
	q.EXPECT().ListOrderItems(gomock.Any(), order.ID).Return(items, nil).Times(4)
	_, err := orders.ListOngoingOrdersByUser(ctx, p.UserID)
	require.NoError(t, err)
	_, err = orders.ListOrdersByUser(ctx, p.UserID, 10, 0)
	require.NoError(t, err)
	require.NoError(t, d.rdb.Set(ctx, redisKey("product", 7), "old stock", time.Hour).Err())
	q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), p.ID).Return(order, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), p.ID).Return(p, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), refund.PaymentID).Return(refund, nil)
	q.EXPECT().MarkOrderRefundSuccess(gomock.Any(), gomock.Any()).Return(refund, nil)
	refunded := p
	refunded.Status = biz.PaymentStatusRefunded
	q.EXPECT().ConfirmPaymentRefunded(gomock.Any(), p.ID).Return(refunded, nil)
	q.EXPECT().ListPaymentsByOrderForUpdate(gomock.Any(), order.ID).Return([]db.Payment{refunded}, nil)
	q.EXPECT().MarkOrderRefunded(gomock.Any(), order.ID).Return(order, nil)
	q.EXPECT().RestoreOrderItemStock(gomock.Any(), order.ID).Return(nil)
	q.EXPECT().GetProduct(gomock.Any(), int64(7)).Return(db.Product{ID: 7, CategoryID: 5}, nil)
	tx := paymentCommitHookTx{q: q, after: func() {
		// No invalidation may run before the transaction commits.
		require.True(t, mr.Exists(redisKey("product", 7)))
		require.False(t, mr.Exists(redisKey("order", "user", p.UserID, "gen")))
	}}
	require.NoError(t, NewPaymentRepo(d, tx, log.DefaultLogger).ApplyPaymentRefund(ctx, p.ID, refund.ID))
	require.False(t, mr.Exists(redisKey("product", 7)))
	for _, key := range []string{redisKey("product", 7, "gen"), "product:list:gen", redisKey("product", "category", 5, "gen"), redisKey("order", "user", p.UserID, "gen"), redisKey("order", "user", "ongoing", p.UserID, "gen")} {
		require.Equal(t, "1", d.rdb.Get(ctx, key).Val(), key)
	}
	q.EXPECT().ListOngoingOrdersByUser(gomock.Any(), p.UserID).Return(nil, nil)
	order.Status = biz.OrderStatusRefunded
	order.IsCompleted = true
	q.EXPECT().ListOrdersByUser(gomock.Any(), db.ListOrdersByUserParams{UserID: p.UserID, Limit: 10}).Return([]db.Order{order}, nil)
	remaining, err := orders.ListOngoingOrdersByUser(ctx, p.UserID)
	require.NoError(t, err)
	require.Empty(t, remaining)
	all, err := orders.ListOrdersByUser(ctx, p.UserID, 10, 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, biz.OrderStatusRefunded, all[0].Status)
	require.True(t, all[0].IsCompleted)
}

func TestListStaleRefundsPassesKeysetCursor(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	q.EXPECT().ListStalePendingRefunds(gomock.Any(), db.ListStalePendingRefundsParams{AfterID: 100, OlderThanSeconds: 60, LimitRows: 100}).Return([]db.OrderRefund{{ID: 101}}, nil)
	rows, err := NewPaymentRepo(&Data{q: q}, nil, log.DefaultLogger).ListStalePendingRefunds(context.Background(), time.Minute, 100, 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, 101, rows[0].ID)
}

func TestRefundSettlementReplayDoesNotRestoreStockAgain(t *testing.T) {
	q := mockdb.NewMockQuerier(gomock.NewController(t))
	mr := miniredis.RunT(t)
	d := newTestData(t, q, mr)
	t.Cleanup(func() { _ = d.rdb.Close() })
	p := statePayment(biz.PaymentStatusRefunded)
	refund := refundRow(p)
	refund.Status = biz.PaymentRefundStatusSuccess
	q.EXPECT().GetOrderForUpdateByPaymentID(gomock.Any(), p.ID).Return(db.Order{ID: p.OrderID, UserID: p.UserID, Status: biz.OrderStatusRefunded}, nil)
	q.EXPECT().GetPaymentForUpdate(gomock.Any(), p.ID).Return(p, nil)
	q.EXPECT().GetOrderRefundByPaymentID(gomock.Any(), refund.PaymentID).Return(refund, nil)
	require.NoError(t, NewPaymentRepo(d, testTxManager{q: q}, log.DefaultLogger).ApplyPaymentRefund(context.Background(), p.ID, refund.ID))
	require.False(t, mr.Exists("product:list:gen"))
}
