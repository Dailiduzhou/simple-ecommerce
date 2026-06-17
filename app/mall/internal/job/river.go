package job

import (
	"context"
	"fmt"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
)

type CheckPayWorker struct {
	river.WorkerDefaults[biz.CheckPayArgs]

	paymentGateway biz.PaymentGateway
	paymentRepo    biz.PaymentRepo
	log            *log.Helper
}

func NewCheckPayWorker(paymentGateway biz.PaymentGateway, paymentRepo biz.PaymentRepo, logger log.Logger) *CheckPayWorker {
	return &CheckPayWorker{
		paymentGateway: paymentGateway,
		paymentRepo:    paymentRepo,
		log:            log.NewHelper(logger),
	}
}

func (w *CheckPayWorker) Work(ctx context.Context, job *river.Job[biz.CheckPayArgs]) error {
	args := biz.NormalizeCheckPayArgs(job.Args)
	if err := validateCheckPayArgs(args); err != nil {
		return river.JobCancel(err)
	}
	if w.paymentGateway == nil || w.paymentRepo == nil {
		return river.JobCancel(fmt.Errorf("pay worker dependencies are not configured"))
	}

	result, err := w.paymentGateway.QueryOrder(ctx, biz.PaymentQueryRequest{
		Channel:    args.Channel,
		OutTradeNo: args.OutTradeNo,
	})
	if err != nil {
		return err
	}

	switch {
	case result.TradeState.IsTerminal():
		return w.paymentRepo.ApplyPayQuery(ctx, args, result)
	case result.TradeState.IsPending():
		if job.Attempt >= args.MaxPolls {
			w.log.WithContext(ctx).Infof("pay check expired payment_id=%d out_trade_no=%s channel=%s attempts=%d", args.PaymentID, args.OutTradeNo, args.Channel, job.Attempt)
			return w.paymentRepo.MarkPayExpired(ctx, args)
		}
		return fmt.Errorf("pay order %s is still pending: %s", args.OutTradeNo, result.TradeState.String())
	default:
		return fmt.Errorf("pay order %s has unsupported trade state: %s", args.OutTradeNo, result.TradeState.String())
	}
}

func (w *CheckPayWorker) NextRetry(job *river.Job[biz.CheckPayArgs]) time.Time {
	args := biz.NormalizeCheckPayArgs(job.Args)
	return time.Now().Add(time.Duration(args.PollIntervalSeconds) * time.Second)
}

func validateCheckPayArgs(args biz.CheckPayArgs) error {
	if args.PaymentID <= 0 {
		return fmt.Errorf("payment_id is required")
	}
	if args.OutTradeNo == "" {
		return fmt.Errorf("out_trade_no is required")
	}
	return nil
}
