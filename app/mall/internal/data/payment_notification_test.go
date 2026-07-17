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
func (m *notificationMQ) GetMQJob(context.Context, int64) (*biz.MQJob, error) { return nil, nil }

func TestPaymentNotificationInboxAndRiverEnqueueShareTransaction(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mq := &notificationMQ{}
	q.EXPECT().CreatePaymentNotification(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{ID: 17}, nil)
	repo := NewPaymentNotificationRepo(testTxManager{q: q}, mq)
	duplicate, err := repo.PersistAndEnqueueNotification(context.Background(), &biz.PaymentNotification{Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", VerifiedAt: time.Now()}, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"})
	require.NoError(t, err)
	require.False(t, duplicate)
	require.Equal(t, 1, mq.calls)
	require.Equal(t, int64(17), mq.args.NotificationID)
}

func TestDuplicatePaymentNotificationAcknowledgesWithoutSecondJob(t *testing.T) {
	ctrl := gomock.NewController(t)
	q := mockdb.NewMockQuerier(ctrl)
	mq := &notificationMQ{}
	q.EXPECT().CreatePaymentNotification(gomock.Any(), gomock.Any()).Return(db.PaymentNotification{}, pgx.ErrNoRows)
	repo := NewPaymentNotificationRepo(testTxManager{q: q}, mq)
	duplicate, err := repo.PersistAndEnqueueNotification(context.Background(), &biz.PaymentNotification{Provider: "wechat", OutTradeNo: "pay_1", PayloadHash: "hash", VerifiedAt: time.Now()}, biz.CheckPayArgs{PaymentID: 1, Provider: "wechat"})
	require.NoError(t, err)
	require.True(t, duplicate)
	require.Zero(t, mq.calls)
}
