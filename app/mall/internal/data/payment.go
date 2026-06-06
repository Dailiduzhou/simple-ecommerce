package data

import (
	"context"

	pb "github.com/Dailiduzhou/simple-ecommerce/api/payment/v1"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
)

var _ biz.WechatPayProvider = (*WechatPayDataProvider)(nil)

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
