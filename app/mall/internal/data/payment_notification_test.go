package data

import (
	"context"
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	mockdb "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db/mock"
	"github.com/golang/mock/gomock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

type notificationMQ struct {
	args  biz.CheckPayArgs
	calls int
	err   error
}

func (m *notificationMQ) EnqueueCheckPay(context.Context, biz.CheckPayArgs, time.Time) (*biz.MQJob, error) {
	return nil, nil
}
func (m *notificationMQ) EnqueueCheckPayTx(_ context.Context, args biz.CheckPayArgs, _ time.Time) (*biz.MQJob, error) {
	m.args = args
	m.calls++
	return &biz.MQJob{ID: 1}, m.err
}
func (m *notificationMQ) EnqueueClosePay(context.Context, biz.ClosePayArgs, time.Time) (*biz.MQJob, error) {
	return &biz.MQJob{ID: 2}, nil
}
func (m *notificationMQ) EnqueueClosePayTx(context.Context, biz.ClosePayArgs, time.Time) (*biz.MQJob, error) {
	return &biz.MQJob{ID: 2}, nil
}
func (m *notificationMQ) GetMQJob(context.Context, int64) (*biz.MQJob, error) { return nil, nil }

func TestPaymentNotificationInboxAndRiverEnqueueShareTransaction(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mq := &notificationMQ{}
	q.EXPECT().CreatePaymentNotification(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{ID: 17, Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", Status: biz.PaymentNotificationStatusReceived}, nil)
	q.EXPECT().SetPaymentNotificationRiverJob(gomock.Any(), gomock.Any()).Return(nil)
	repo := NewPaymentNotificationRepo(testTxManager{q: q}, mq)
	duplicate, err := repo.PersistAndEnqueueNotification(context.Background(), &biz.PaymentNotification{Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", VerifiedAt: time.Now()}, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"})
	require.NoError(t, err)
	require.False(t, duplicate)
	require.Equal(t, 1, mq.calls)
	require.Equal(t, int64(17), mq.args.NotificationID)
}

func TestDuplicateUnprocessedPaymentNotificationReenqueuesExistingInboxRow(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mq := &notificationMQ{}
	q.EXPECT().CreatePaymentNotification(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{}, pgx.ErrNoRows)
	q.EXPECT().GetPaymentNotificationByPayload(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{ID: 17, Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", Status: biz.PaymentNotificationStatusReceived}, nil)
	q.EXPECT().SetPaymentNotificationRiverJob(gomock.Any(), gomock.Any()).Return(nil)
	repo := NewPaymentNotificationRepo(testTxManager{q: q}, mq)
	duplicate, err := repo.PersistAndEnqueueNotification(context.Background(), &biz.PaymentNotification{Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", VerifiedAt: time.Now()}, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"})
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Equal(t, 1, mq.calls)
	require.Equal(t, int64(17), mq.args.NotificationID)
}

func TestDuplicateProcessedPaymentNotificationSkipsRiverEnqueue(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mq := &notificationMQ{}
	q.EXPECT().CreatePaymentNotification(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{}, pgx.ErrNoRows)
	q.EXPECT().GetPaymentNotificationByPayload(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{ID: 17, Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", Status: biz.PaymentNotificationStatusProcessed}, nil)
	repo := NewPaymentNotificationRepo(testTxManager{q: q}, mq)
	duplicate, err := repo.PersistAndEnqueueNotification(context.Background(), &biz.PaymentNotification{Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", VerifiedAt: time.Now()}, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"})
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Zero(t, mq.calls)
}

func TestDuplicateProviderEventRejectsChangedPayload(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mq := &notificationMQ{}
	q.EXPECT().CreatePaymentNotification(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{}, pgx.ErrNoRows)
	q.EXPECT().GetPaymentNotificationByEvent(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{
		ID: 17, Provider: "wechat", ProviderEventID: pgtype.Text{String: "event_1", Valid: true},
		OutTradeNo: "pay_1", PayloadHash: "original_hash", Status: biz.PaymentNotificationStatusReceived,
	}, nil)
	repo := NewPaymentNotificationRepo(testTxManager{q: q}, mq)
	duplicate, err := repo.PersistAndEnqueueNotification(context.Background(), &biz.PaymentNotification{
		Provider: "wechat", ProviderEventID: "event_1", OutTradeNo: "pay_1", PayloadHash: "changed_hash", VerifiedAt: time.Now(),
	}, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"})
	require.Error(t, err)
	require.True(t, duplicate)
	require.Zero(t, mq.calls)
}

func TestBeginPaymentNotificationProcessingTransitionsReceivedNotification(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	q.EXPECT().GetPaymentNotification(gomock.Any(), int64(17)).Return(db.PaymentNotification{ID: 17, Provider: "wechat", OutTradeNo: "pay_1", Status: biz.PaymentNotificationStatusReceived}, nil)
	q.EXPECT().BeginPaymentNotificationProcessing(gomock.Any(), int64(17)).Return(db.PaymentNotification{ID: 17, Status: biz.PaymentNotificationStatusProcessing}, nil)
	repo := &PaymentRepo{data: &Data{q: q}}
	proceed, err := repo.BeginPaymentNotificationProcessing(context.Background(), 17, "wechat", "pay_1")
	require.NoError(t, err)
	require.True(t, proceed)
}

func TestBeginPaymentNotificationProcessingSkipsProcessedNotification(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	q.EXPECT().GetPaymentNotification(gomock.Any(), int64(17)).Return(db.PaymentNotification{ID: 17, Provider: "wechat", OutTradeNo: "pay_1", Status: biz.PaymentNotificationStatusProcessed}, nil)
	repo := &PaymentRepo{data: &Data{q: q}}
	proceed, err := repo.BeginPaymentNotificationProcessing(context.Background(), 17, "wechat", "pay_1")
	require.NoError(t, err)
	require.False(t, proceed)
}

func TestBeginPaymentNotificationProcessingRejectsMismatchedBinding(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	q.EXPECT().GetPaymentNotification(gomock.Any(), int64(17)).Return(db.PaymentNotification{ID: 17, Provider: "wechat", OutTradeNo: "pay_other", Status: biz.PaymentNotificationStatusReceived}, nil)
	repo := &PaymentRepo{data: &Data{q: q}}
	proceed, err := repo.BeginPaymentNotificationProcessing(context.Background(), 17, "wechat", "pay_1")
	require.ErrorIs(t, err, biz.ErrPaymentNotificationBinding)
	require.False(t, proceed)
}

func TestMarkPaymentNotificationFailedPersistsFinalError(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	q.EXPECT().MarkPaymentNotificationFailed(gomock.Any(), db.MarkPaymentNotificationFailedParams{
		ID: 17, LastError: pgtype.Text{String: "provider timeout", Valid: true},
	}).Return(int64(1), nil)
	repo := &PaymentRepo{data: &Data{q: q}}
	require.NoError(t, repo.MarkPaymentNotificationFailed(context.Background(), 17, "provider timeout"))
}
