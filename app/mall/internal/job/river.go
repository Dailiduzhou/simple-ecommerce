package job

import (
	"context"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/riverqueue/river"
)

type CheckWechatPayArgs struct {
	OrderID int64 `json:"order_id"`
}

func (CheckWechatPayArgs) Kind() string {
	return "check_wechat_pay"
}

type CheckWechatPayWorker struct {
	orderRepo biz.OrderRepo
	// TODO: wechat JSAPI
}

func (w *CheckWechatPayWorker) Work(ctx context.Context, job *river.Job[CheckWechatPayArgs]) error {
	orderID := job.Args.OrderID

	order, err := w.orderRepo.GetOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if order.Status != "creating" {
		return nil
	}

	// TODO: 调用微信查询接口

	return nil
}
