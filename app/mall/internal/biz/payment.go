package biz

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
)

type WechatPayProvider interface {
	PrepayJSAPI(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error)
	QueryOrder(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error)
	CloseOrder(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error)
}
