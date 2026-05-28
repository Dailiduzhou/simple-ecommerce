package job

import "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"

type CheckWechatPayArgs struct {
	OrderID int64 `json:"order_id"`
}

func (CheckWechatPayArgs) Kind() string {
	return "check_wechat_pay"
}

type CheckWechatPayWorker struct {
	orderRepo biz.OrderRepo
}
