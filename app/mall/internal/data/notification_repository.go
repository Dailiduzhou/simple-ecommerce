package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *PaymentRepo) BeginPaymentNotificationProcessing(ctx context.Context, id int64, provider, outTradeNo string) (bool, error) {
	if id <= 0 {
		return true, nil
	}
	q := querierFromContext(ctx, r.data.q)
	notification, err := q.GetPaymentNotification(ctx, id)
	if err != nil {
		return false, err
	}
	if notification.Provider != provider || notification.OutTradeNo != outTradeNo {
		return false, biz.ErrPaymentNotificationBinding
	}
	if notification.Status == biz.PaymentNotificationStatusProcessed {
		return false, nil
	}
	if _, err := q.BeginPaymentNotificationProcessing(ctx, id); err != nil {
		return false, err
	}
	return true, nil
}

func (r *PaymentRepo) RecordPaymentNotificationError(ctx context.Context, id int64, lastError string) error {
	if id <= 0 {
		return nil
	}
	_, err := querierFromContext(ctx, r.data.q).RecordPaymentNotificationError(ctx, db.RecordPaymentNotificationErrorParams{
		ID: id, LastError: notificationErrorText(lastError),
	})
	return err
}

func (r *PaymentRepo) MarkPaymentNotificationFailed(ctx context.Context, id int64, lastError string) error {
	if id <= 0 {
		return nil
	}
	q := querierFromContext(ctx, r.data.q)
	rows, err := q.MarkPaymentNotificationFailed(ctx, db.MarkPaymentNotificationFailedParams{
		ID: id, LastError: notificationErrorText(lastError),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return notificationStateAfterCAS(ctx, q, id, biz.PaymentNotificationStatusProcessed)
	}
	return nil
}

func notificationErrorText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func notificationStateAfterCAS(ctx context.Context, q db.Querier, id int64, desired string) error {
	current, err := q.GetPaymentNotification(ctx, id)
	if err != nil {
		return err
	}
	if current.Status == desired {
		return nil
	}
	return errors.Conflict("PAYMENT_NOTIFICATION_STATE_CONFLICT", "payment notification state transition conflicted")
}

func markNotificationProcessed(ctx context.Context, q db.Querier, id int64) error {
	if id <= 0 {
		return nil
	}
	rows, err := q.MarkPaymentNotificationProcessed(ctx, id)
	if err != nil {
		return err
	}
	if rows == 0 {
		return notificationStateAfterCAS(ctx, q, id, biz.PaymentNotificationStatusProcessed)
	}
	return nil
}

type PaymentNotificationRepo struct {
	tx   biz.TxManager
	jobs biz.PaymentMQRepo
}

func NewPaymentNotificationRepo(tx biz.TxManager, jobs biz.PaymentMQRepo) *PaymentNotificationRepo {
	return &PaymentNotificationRepo{tx: tx, jobs: jobs}
}

func (r *PaymentNotificationRepo) PersistAndEnqueueNotification(ctx context.Context, notification *biz.PaymentNotification, args biz.CheckPayArgs) (bool, error) {
	duplicate := false
	err := r.tx.InTx(ctx, func(ctx context.Context) error {
		q := querierFromContext(ctx, nil)
		row, err := q.CreatePaymentNotification(ctx, db.CreatePaymentNotificationParams{Provider: notification.Provider, ProviderEventID: pgtype.Text{String: notification.ProviderEventID, Valid: notification.ProviderEventID != ""}, OutTradeNo: notification.OutTradeNo, PayloadHash: notification.PayloadHash, VerifiedAt: pgtype.Timestamptz{Time: notification.VerifiedAt, Valid: true}})
		if stderrors.Is(err, pgx.ErrNoRows) {
			duplicate = true
			row, err = getExistingPaymentNotification(ctx, q, notification)
		}
		if err != nil {
			return err
		}
		if row.Provider != notification.Provider || row.OutTradeNo != notification.OutTradeNo || row.PayloadHash != notification.PayloadHash {
			return errors.Conflict("PAYMENT_NOTIFICATION_IDENTITY_CONFLICT", "payment notification identity conflicts with an existing notification")
		}
		if row.Status == biz.PaymentNotificationStatusProcessed {
			return nil
		}
		args = biz.NormalizeCheckPayArgs(args)
		args.NotificationID = row.ID
		job, err := r.jobs.EnqueueCheckPayTx(ctx, args, time.Time{})
		if err != nil {
			return err
		}
		if job == nil || job.ID <= 0 {
			return fmt.Errorf("payment notification enqueue returned an empty job")
		}
		return q.SetPaymentNotificationRiverJob(ctx, db.SetPaymentNotificationRiverJobParams{
			ID: row.ID, RiverJobID: pgtype.Int8{Int64: job.ID, Valid: true},
		})
	})
	return duplicate, err
}

func getExistingPaymentNotification(ctx context.Context, q db.Querier, notification *biz.PaymentNotification) (db.PaymentNotification, error) {
	if notification.ProviderEventID != "" {
		row, err := q.GetPaymentNotificationByEvent(ctx, db.GetPaymentNotificationByEventParams{
			Provider:        notification.Provider,
			ProviderEventID: pgtype.Text{String: notification.ProviderEventID, Valid: true},
		})
		if err == nil || !stderrors.Is(err, pgx.ErrNoRows) {
			return row, err
		}
	}
	return q.GetPaymentNotificationByPayload(ctx, db.GetPaymentNotificationByPayloadParams{
		Provider: notification.Provider, OutTradeNo: notification.OutTradeNo, PayloadHash: notification.PayloadHash,
	})
}
