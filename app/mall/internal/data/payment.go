package data

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/data/db"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

var _ biz.WechatPayProvider = (*WechatPayDataProvider)(nil)
var _ biz.PaymentMQRepo = (*PaymentMQRepo)(nil)
var _ biz.PaymentSyncRepo = (*PaymentSyncRepo)(nil)

type WechatPayDataProvider struct {
	log *log.Helper
}

func NewWechatPayProvider(logger log.Logger) *WechatPayDataProvider {
	return &WechatPayDataProvider{log: log.NewHelper(logger)}
}

func (p *WechatPayDataProvider) PrepayJSAPI(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error) {
	p.log.WithContext(ctx).Warn("wechat pay provider is not configured")
	return nil, wechatPayNotConfigured()
}

func (p *WechatPayDataProvider) QueryOrder(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error) {
	p.log.WithContext(ctx).Warnf("wechat pay provider is not configured, out_trade_no=%s", outTradeNo)
	return nil, wechatPayNotConfigured()
}

func (p *WechatPayDataProvider) CloseOrder(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error) {
	p.log.WithContext(ctx).Warnf("wechat pay provider is not configured, out_trade_no=%s", outTradeNo)
	return nil, wechatPayNotConfigured()
}

func wechatPayNotConfigured() error {
	return errors.ServiceUnavailable("WECHAT_PAY_NOT_CONFIGURED", "wechat pay provider is not configured")
}

type PaymentMQRepo struct {
	client *river.Client[pgx.Tx]
	log    *log.Helper
}

func NewPaymentMQRepo(client *river.Client[pgx.Tx], logger log.Logger) *PaymentMQRepo {
	return &PaymentMQRepo{client: client, log: log.NewHelper(logger)}
}

func (r *PaymentMQRepo) EnqueueCheckWechatPay(ctx context.Context, args biz.CheckWechatPayArgs, scheduledAt time.Time) (*biz.MQJob, error) {
	opts := &river.InsertOpts{
		MaxAttempts: args.MaxPolls,
		Queue:       "payments",
		Tags: []string{
			"wechat-pay",
			fmt.Sprintf("payment-%d", args.PaymentID),
		},
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
		},
	}
	if !scheduledAt.IsZero() {
		opts.ScheduledAt = scheduledAt
	}

	result, err := r.client.Insert(ctx, args, opts)
	if err != nil {
		return nil, err
	}
	if result.UniqueSkippedAsDuplicate {
		r.log.WithContext(ctx).Infof("skipped duplicate river job kind=%s job_id=%d", args.Kind(), result.Job.ID)
	}
	return toBizMQJob(result.Job), nil
}

func (r *PaymentMQRepo) GetMQJob(ctx context.Context, jobID int64) (*biz.MQJob, error) {
	job, err := r.client.JobGet(ctx, jobID)
	if err != nil {
		if stderrors.Is(err, rivertype.ErrNotFound) {
			return nil, errors.NotFound("MQ_JOB_NOT_FOUND", "mq job not found")
		}
		return nil, err
	}
	return toBizMQJob(job), nil
}

type PaymentSyncRepo struct {
	pool *pgxpool.Pool
}

func NewPaymentSyncRepo(pool *pgxpool.Pool) *PaymentSyncRepo {
	return &PaymentSyncRepo{pool: pool}
}

func (r *PaymentSyncRepo) ApplyWechatPayQuery(ctx context.Context, args biz.CheckWechatPayArgs, result *pb.QueryOrderReply) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := db.New(tx)
	orderID := args.OrderID

	switch result.TradeState {
	case pb.TradeState_SUCCESS:
		payment, err := q.UpdatePaymentSuccess(ctx, db.UpdatePaymentSuccessParams{
			ID: args.PaymentID,
			ThirdPartyTxID: pgtype.Text{
				String: result.TransactionId,
				Valid:  result.TransactionId != "",
			},
		})
		if err != nil {
			return err
		}
		if orderID <= 0 {
			orderID = payment.OrderID
		}
		if orderID > 0 {
			if err := q.CompleteOrder(ctx, orderID); err != nil {
				return err
			}
		}
	case pb.TradeState_REFUND:
		if err := q.UpdatePaymentRefunded(ctx, args.PaymentID); err != nil {
			return err
		}
	case pb.TradeState_CLOSED, pb.TradeState_REVOKED, pb.TradeState_PAYERROR:
		if err := q.UpdatePaymentFailed(ctx, args.PaymentID); err != nil {
			return err
		}
		if orderID <= 0 {
			payment, err := q.GetPayment(ctx, args.PaymentID)
			if err != nil {
				return err
			}
			orderID = payment.OrderID
		}
		if orderID > 0 {
			if err := q.CancelOrder(ctx, orderID); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("wechat pay state %s is not terminal", result.TradeState.String())
	}

	return tx.Commit(ctx)
}

func (r *PaymentSyncRepo) MarkWechatPayExpired(ctx context.Context, args biz.CheckWechatPayArgs) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	q := db.New(tx)
	if err := q.UpdatePaymentFailed(ctx, args.PaymentID); err != nil {
		return err
	}

	orderID := args.OrderID
	if orderID <= 0 {
		payment, err := q.GetPayment(ctx, args.PaymentID)
		if err != nil {
			return err
		}
		orderID = payment.OrderID
	}
	if orderID > 0 {
		if err := q.CancelOrder(ctx, orderID); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func toBizMQJob(row *rivertype.JobRow) *biz.MQJob {
	if row == nil {
		return nil
	}
	errors := make([]biz.MQJobError, len(row.Errors))
	for i, err := range row.Errors {
		errors[i] = biz.MQJobError{
			Attempt: err.Attempt,
			Error:   err.Error,
			At:      err.At,
		}
	}
	return &biz.MQJob{
		ID:          row.ID,
		Kind:        row.Kind,
		Queue:       row.Queue,
		State:       string(row.State),
		Attempt:     row.Attempt,
		MaxAttempts: row.MaxAttempts,
		ArgsJSON:    string(row.EncodedArgs),
		Tags:        row.Tags,
		CreatedAt:   row.CreatedAt,
		ScheduledAt: row.ScheduledAt,
		AttemptedAt: row.AttemptedAt,
		FinalizedAt: row.FinalizedAt,
		Errors:      errors,
	}
}
