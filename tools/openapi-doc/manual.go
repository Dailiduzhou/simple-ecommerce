package main

import "gopkg.in/yaml.v3"

// manualRouteDoc documents a route the HTTP server registers by hand. Those
// routes have no google.api.http annotation, so protoc-gen-openapi cannot see
// them; the guard test compares this list with what server/http.go registers so
// a new manual route cannot disappear from the document.
type manualRouteDoc struct {
	// ID follows the protoc-gen-go-http naming (Service_Method) so the manual
	// route can be treated like a generated operation elsewhere.
	ID        string
	Method    string
	Path      string
	Operation func() *yaml.Node
}

// paymentNotifyPath is registered in app/mall/internal/server/http.go with
// srv.Route("/").POST(...) and handled by PaymentService.HandlePaymentNotify,
// which forwards the raw request to the channel adapter (biz.PaymentAdapter
// .ParseAndVerifyNotification) and answers with the acknowledgement the
// provider expects (Alipay: "success"/"fail", WeChat: return_code XML).
func manualRouteDocs() []manualRouteDoc {
	return []manualRouteDoc{{
		ID:     "Payment_NotifyCallback",
		Method: "POST",
		Path:   "/v1/payments/{provider}/notify",
		Operation: func() *yaml.Node {
			return mapNode(
				keyNode("tags"), seqNode(strNode("Payment")),
				keyNode("summary"), strNode("支付渠道异步回调"),
				keyNode("description"), strNode(`支付宝/微信等渠道服务器直接调用的异步通知入口，不使用 Bearer 令牌，按 provider 校验签名后更新支付单状态。

请求体是渠道原始报文（支付宝为 application/x-www-form-urlencoded，微信为 JSON 或 XML），服务端原样交给渠道适配器解析。
响应体是渠道约定的确认串，必须原样回给渠道，否则渠道会重试：支付宝为 success / fail，微信为 <xml><return_code><![CDATA[SUCCESS]]></return_code></xml>。
未注册的 provider 返回 400 unsupported provider（text/plain）。`),
				keyNode("operationId"), strNode("Payment_NotifyCallback"),
				keyNode("parameters"), seqNode(mapNode(
					keyNode("name"), strNode("provider"),
					keyNode("in"), strNode("path"),
					keyNode("required"), boolNode(true),
					keyNode("description"), strNode("渠道标识，与支付适配器注册名一致（本仓库为 alipay、wechat）"),
					keyNode("schema"), mapNode(keyNode("type"), strNode("string"), keyNode("example"), strNode("wechat")),
				)),
				keyNode("requestBody"), mapNode(
					keyNode("required"), boolNode(true),
					keyNode("content"), mapNode(
						keyNode("application/x-www-form-urlencoded"), mapNode(
							keyNode("schema"), mapNode(
								keyNode("type"), strNode("object"),
								keyNode("additionalProperties"), mapNode(keyNode("type"), strNode("string")),
							),
						),
						keyNode("application/json"), mapNode(
							keyNode("schema"), mapNode(
								keyNode("type"), strNode("object"),
								keyNode("description"), strNode("渠道原始报文，字段随渠道不同"),
								keyNode("additionalProperties"), boolNode(true),
							),
						),
					),
				),
				keyNode("responses"), mapNode(
					quotedKeyNode("200"), mapNode(
						keyNode("description"), strNode("渠道确认串：支付宝 success/fail，微信返回码 XML"),
						keyNode("content"), mapNode(
							keyNode("text/plain"), mapNode(
								keyNode("schema"), mapNode(keyNode("type"), strNode("string"), keyNode("example"), strNode("success")),
							),
							keyNode("application/xml"), mapNode(
								keyNode("schema"), mapNode(keyNode("type"), strNode("string")),
							),
						),
					),
					quotedKeyNode("400"), mapNode(
						keyNode("description"), strNode("provider 未注册或回调被判为无效渠道"),
						keyNode("content"), mapNode(
							keyNode("text/plain"), mapNode(
								keyNode("schema"), mapNode(keyNode("type"), strNode("string"), keyNode("example"), strNode("unsupported provider")),
							),
						),
					),
					quotedKeyNode("500"), mapNode(
						keyNode("description"), strNode("服务内部错误（panic 由 recovery 中间件转成统一错误体）"),
						keyNode("content"), mapNode(
							keyNode("application/json"), mapNode(
								keyNode("schema"), schemasRef(errorSchemaName),
							),
						),
					),
				),
				// Channel callbacks cannot present a JWT.
				keyNode("security"), seqNode(),
			)
		},
	}}
}
