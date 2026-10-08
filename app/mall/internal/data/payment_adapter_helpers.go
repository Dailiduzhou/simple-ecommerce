package data

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/biz"
	"github.com/go-kratos/kratos/v2/errors"
)

const (
	EnvPaymentCallbackBaseURL = "PAYMENT_CALLBACK_BASE_URL"
	EnvAlipayNotifyURL        = "ALIPAY_NOTIFY_URL"
	EnvWechatNotifyURL        = "WECHAT_NOTIFY_URL"
	maxNotificationBody       = 1 << 20
)

func notifyURLFromEnv(provider string) string {
	if base := strings.TrimRight(os.Getenv(EnvPaymentCallbackBaseURL), "/"); base != "" {
		return base + "/v1/payments/" + provider + "/notify"
	}
	if provider == "wechat" {
		return os.Getenv(EnvWechatNotifyURL)
	}
	return os.Getenv(EnvAlipayNotifyURL)
}

func validateCNYAmount(amount int64, currency string) error {
	if amount <= 0 {
		return errors.BadRequest("PAYMENT_AMOUNT_INVALID", "payment amount must be positive")
	}
	if strings.ToUpper(strings.TrimSpace(currency)) != biz.DefaultCurrency {
		return errors.BadRequest("PAYMENT_CURRENCY_UNSUPPORTED", "payment provider only supports CNY")
	}
	return nil
}

func paymentAction(actionType biz.PaymentActionType, payload any) biz.PaymentAction {
	encoded, _ := json.Marshal(payload)
	return biz.PaymentAction{Type: actionType, Payload: encoded}
}

func boundedRequestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, fmt.Errorf("request body is required")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxNotificationBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxNotificationBody {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxNotificationBody)
	}
	return body, nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func paymentProviderNotConfigured(provider string) error {
	return errors.ServiceUnavailable("PAYMENT_PROVIDER_NOT_AVAILABLE", provider+" payment provider is not configured")
}

func orEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}
