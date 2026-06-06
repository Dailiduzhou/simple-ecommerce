package job

import (
	"context"
	"fmt"
	"time"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/riverqueue/river"
)

type CheckWechatPayWorker struct {
	river.WorkerDefaults[biz.CheckWechatPayArgs]

	wechatPay biz.WechatPayProvider
	syncRepo  biz.PaymentSyncRepo
	log       *log.Helper
}

func NewCheckWechatPayWorker(wechatPay biz.WechatPayProvider, syncRepo biz.PaymentSyncRepo, logger log.Logger) *CheckWechatPayWorker {
	return &CheckWechatPayWorker{
		wechatPay: wechatPay,
		syncRepo:  syncRepo,
		log:       log.NewHelper(logger),
	}
}

func (w *CheckWechatPayWorker) Work(ctx context.Context, job *river.Job[biz.CheckWechatPayArgs]) error {
	args := normalizeCheckWechatPayArgs(job.Args)
	if err := validateCheckWechatPayArgs(args); err != nil {
		return river.JobCancel(err)
	}
	if w.wechatPay == nil || w.syncRepo == nil {
		return river.JobCancel(fmt.Errorf("wechat pay worker dependencies are not configured"))
	}

	result, err := w.wechatPay.QueryOrder(ctx, args.OutTradeNo)
	if err != nil {
		return err
	}

	switch result.TradeState {
	case pb.TradeState_SUCCESS, pb.TradeState_REFUND, pb.TradeState_CLOSED, pb.TradeState_REVOKED, pb.TradeState_PAYERROR:
		return w.syncRepo.ApplyWechatPayQuery(ctx, args, result)
	case pb.TradeState_NOTPAY, pb.TradeState_USERPAYING, pb.TradeState_TRADE_STATE_UNSPECIFIED:
		if job.Attempt >= args.MaxPolls {
			w.log.WithContext(ctx).Infof("wechat pay check expired payment_id=%d out_trade_no=%s attempts=%d", args.PaymentID, args.OutTradeNo, job.Attempt)
			return w.syncRepo.MarkWechatPayExpired(ctx, args)
		}
		return fmt.Errorf("wechat pay order %s is still pending: %s", args.OutTradeNo, result.TradeState.String())
	default:
		return fmt.Errorf("wechat pay order %s has unsupported trade state: %s", args.OutTradeNo, result.TradeState.String())
	}
}

func (w *CheckWechatPayWorker) NextRetry(job *river.Job[biz.CheckWechatPayArgs]) time.Time {
	args := normalizeCheckWechatPayArgs(job.Args)
	return time.Now().Add(time.Duration(args.PollIntervalSeconds) * time.Second)
}

func normalizeCheckWechatPayArgs(args biz.CheckWechatPayArgs) biz.CheckWechatPayArgs {
	if args.MaxPolls <= 0 {
		args.MaxPolls = 5
	}
	if args.PollIntervalSeconds <= 0 {
		args.PollIntervalSeconds = 30
	}
	return args
}

func validateCheckWechatPayArgs(args biz.CheckWechatPayArgs) error {
	if args.PaymentID <= 0 {
		return fmt.Errorf("payment_id is required")
	}
	if args.OutTradeNo == "" {
		return fmt.Errorf("out_trade_no is required")
	}
	return nil
}
