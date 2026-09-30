package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type PaymentMQRepo struct {
	client *river.Client[pgx.Tx]
	log    *log.Helper
}

func NewPaymentMQRepo(client *river.Client[pgx.Tx], logger log.Logger) *PaymentMQRepo {
	return &PaymentMQRepo{client: client, log: log.NewHelper(logger)}
}

func NewPaymentMQRepoForWire(insertClient *RiverInsertClient, logger log.Logger) *PaymentMQRepo {
	return NewPaymentMQRepo(insertClient.client, logger)
}

func (r *PaymentMQRepo) insert(ctx context.Context, args biz.CheckPayArgs, scheduledAt time.Time, tx pgx.Tx) (*biz.MQJob, error) {
	opts := r.checkPayInsertOpts(args, scheduledAt)
	var result *rivertype.JobInsertResult
	var err error
	if tx == nil {
		result, err = r.client.Insert(ctx, args, opts)
	} else {
		result, err = r.client.InsertTx(ctx, tx, args, opts)
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river insert returned no job")
	}
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infow("msg", "deduplicated active reconciliation job", "job_id", result.Job.ID, "payment_id", args.PaymentID)
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}

func (r *PaymentMQRepo) EnqueueCheckPay(ctx context.Context, args biz.CheckPayArgs, at time.Time) (*biz.MQJob, error) {
	return r.insert(ctx, args, at, nil)
}

func (r *PaymentMQRepo) EnqueueCheckPayTx(ctx context.Context, args biz.CheckPayArgs, at time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("missing transaction")
	}
	return r.insert(ctx, args, at, tx)
}

func (r *PaymentMQRepo) EnqueueExpireOrder(ctx context.Context, args biz.ExpireOrderArgs, at time.Time) (*biz.MQJob, error) {
	opts := expireOrderInsertOpts(args, at)
	result, err := r.client.Insert(ctx, args, opts)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river expire order insert returned no job")
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}

func (r *PaymentMQRepo) EnqueueExpireOrderTx(ctx context.Context, args biz.ExpireOrderArgs, at time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("missing transaction")
	}
	result, err := r.client.InsertTx(ctx, tx, args, expireOrderInsertOpts(args, at))
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river expire order insert returned no job")
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}

func expireOrderInsertOpts(args biz.ExpireOrderArgs, at time.Time) *river.InsertOpts {
	opts := &river.InsertOpts{
		MaxAttempts: 8,
		Queue:       "orders",
		Tags:        []string{fmt.Sprintf("order-%d", args.OrderID)},
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
	if !at.IsZero() {
		opts.ScheduledAt = at
	}
	return opts
}

func (r *PaymentMQRepo) EnqueueClosePay(ctx context.Context, args biz.ClosePayArgs, at time.Time) (*biz.MQJob, error) {
	return r.insertClosePay(ctx, args, at, nil)
}

func (r *PaymentMQRepo) EnqueueClosePayTx(ctx context.Context, args biz.ClosePayArgs, at time.Time) (*biz.MQJob, error) {
	tx := pgTxFromContext(ctx)
	if tx == nil {
		return nil, fmt.Errorf("missing transaction")
	}
	return r.insertClosePay(ctx, args, at, tx)
}

func (r *PaymentMQRepo) insertClosePay(ctx context.Context, args biz.ClosePayArgs, at time.Time, tx pgx.Tx) (*biz.MQJob, error) {
	opts := &river.InsertOpts{
		MaxAttempts: 8,
		Queue:       "payments",
		Tags:        []string{fmt.Sprintf("provider-%s", args.Provider), fmt.Sprintf("payment-%d", args.PaymentID)},
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
				rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			},
		},
	}
	if !at.IsZero() {
		opts.ScheduledAt = at
	}
	var result *rivertype.JobInsertResult
	var err error
	if tx == nil {
		result, err = r.client.Insert(ctx, args, opts)
	} else {
		result, err = r.client.InsertTx(ctx, tx, args, opts)
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.Job == nil {
		return nil, fmt.Errorf("river close payment insert returned no job")
	}
	job := toBizMQJob(result.Job)
	job.Deduplicated = result.UniqueSkippedAsDuplicate
	return job, nil
}

func (r *PaymentMQRepo) checkPayInsertOpts(args biz.CheckPayArgs, at time.Time) *river.InsertOpts {
	opts := &river.InsertOpts{MaxAttempts: 8, Queue: "payments", Tags: []string{fmt.Sprintf("provider-%s", args.Provider), fmt.Sprintf("payment-%d", args.PaymentID)}, UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}
	if !at.IsZero() {
		opts.ScheduledAt = at
	}
	return opts
}

func (r *PaymentMQRepo) GetMQJob(ctx context.Context, id int64) (*biz.MQJob, error) {
	row, err := r.client.JobGet(ctx, id)
	if stderrors.Is(err, rivertype.ErrNotFound) {
		return nil, errors.NotFound("MQ_JOB_NOT_FOUND", "mq job not found")
	}
	if err != nil {
		return nil, err
	}
	return toBizMQJob(row), nil
}

func toBizMQJob(row *rivertype.JobRow) *biz.MQJob {
	if row == nil {
		return nil
	}
	result := &biz.MQJob{ID: row.ID, Kind: row.Kind, Queue: row.Queue, State: string(row.State), Attempt: row.Attempt, MaxAttempts: row.MaxAttempts, ArgsJSON: string(row.EncodedArgs), Tags: row.Tags, CreatedAt: row.CreatedAt, ScheduledAt: row.ScheduledAt, AttemptedAt: row.AttemptedAt, FinalizedAt: row.FinalizedAt, Errors: make([]biz.MQJobError, len(row.Errors))}
	for i, item := range row.Errors {
		result.Errors[i] = biz.MQJobError{Attempt: item.Attempt, Error: item.Error, At: item.At}
	}
	return result
}
