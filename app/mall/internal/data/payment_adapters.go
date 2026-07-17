package data

import (
	"fmt"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/go-kratos/kratos/v2/log"
)

// NewPaymentAdapters registers only explicitly enabled providers. A missing or
// disabled provider is not an application startup dependency; an enabled but
// incomplete provider fails startup so signature verification cannot degrade.
func NewPaymentAdapters(c *conf.Payment, logger log.Logger) ([]biz.PaymentAdapter, error) {
	if c == nil {
		return nil, nil
	}
	adapters := make([]biz.PaymentAdapter, 0, 2)
	if c.Wechat != nil && c.Wechat.Enabled {
		if c.Wechat.AppId == "" || c.Wechat.MchId == "" || c.Wechat.ApiKey == "" {
			return nil, fmt.Errorf("wechat payment is enabled but app_id, mch_id, or api_key is missing")
		}
		adapters = append(adapters, NewWechatPaymentAdapter(c, logger))
	}
	if c.Alipay != nil && c.Alipay.Enabled {
		if c.Alipay.AppId == "" || c.Alipay.PrivateKey == "" || c.Alipay.AppCertPath == "" || c.Alipay.AlipayPublicCertPath == "" || c.Alipay.AlipayRootCertPath == "" {
			return nil, fmt.Errorf("alipay payment is enabled but required key or certificate configuration is missing")
		}
		client, err := NewAlipayClient(c)
		if err != nil {
			return nil, err
		}
		adapter := NewAlipayPaymentAdapter(client, logger)
		adapter.publicCertPath = c.Alipay.AlipayPublicCertPath
		adapters = append(adapters, adapter)
	}
	return adapters, nil
}
