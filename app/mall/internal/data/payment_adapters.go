package data

import "github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"

func NewPaymentAdapters(wechat *WechatPaymentAdapter, alipay *AlipayPaymentAdapter) []biz.PaymentAdapter {
	return []biz.PaymentAdapter{wechat, alipay}
}
