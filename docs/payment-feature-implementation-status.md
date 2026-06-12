# Payment Feature Implementation Status

Date: 2026-06-10

## Context

This document records the current implementation status of the payment subsystem in the Go Kratos mall service. It covers the payment adapter pattern, message queue (River), async order query (polling), and callback handling.

## Architecture Overview

```
HTTP/gRPC Transport
  PaymentService       WechatPayService (JSAPI)
  ─ CreatePayment      ─ PrepayJSAPI
  ─ NotifyPayment      ─ QueryOrder
  ─ CreateWechatCheck  ─ CloseOrder
  ─ GetMQJob
       │                    │
Biz Layer                   │
  PaymentJobUsecase         PaymentGateway (router)
  ─ EnqueueCheckWechat      ─ Prepay / Query / Close
       │                    │
       │            ┌───────┘
  PaymentMQRepo (iface)     │
       │                    │
Data Layer                  │
  PaymentMQRepo (River)     │
  PaymentSyncRepo           │
       │              ┌─────┴──────┐
       │              WechatAdapter │
       │              AlipayAdapter │
       │              └────────────┘
       │
Job Layer (River Workers)
  CheckWechatPayWorker
  ─ Work(): poll Wechat order status
  ─ NextRetry(): backoff retry strategy
```

## Component Status

### 1. Payment Adapters (支付适配器)

| Component | Status | File |
|-----------|--------|------|
| `PaymentAdapter` interface | **Done** | `app/mall/internal/biz/payment.go:133-138` |
| `PaymentGateway` interface | **Done** | `app/mall/internal/biz/payment.go:140-144` |
| `paymentGateway` (router impl) | **Done** | `app/mall/internal/biz/payment_gateway.go` |
| `WechatPaymentAdapter` | **Done** | `app/mall/internal/data/payment.go:32-136` |
| `AlipayPaymentAdapter` | **Done** | `app/mall/internal/data/payment.go:138-234` |
| `NewPaymentAdapters` (wire factory) | **Done** | `app/mall/internal/data/payment_adapters.go` |

**Adapter methods:**

| Operation | Wechat (gopay/wechat) | Alipay (gopay/alipay) |
|-----------|----------------------|----------------------|
| Prepay | `UnifiedOrder` (JSAPI) | `TradePrecreate` (QR code) |
| QueryOrder | `QueryOrder` | `TradeQuery` |
| CloseOrder | `CloseOrder` | `TradeClose` |

**Notes:**
- Both adapters use `go-pay/gopay` library clients
- Adapter client instances are nil by default — they return `SERVICE_UNAVAILABLE` if not configured
- Channel routing is case-insensitive via `NormalizePayChannel()`

### 2. Trade State Machine (交易状态机)

| Component | Status | File |
|-----------|--------|------|
| `TradeState` type + constants | **Done** | `app/mall/internal/biz/payment.go:19-30` |
| `IsTerminal()` | **Done** | `app/mall/internal/biz/payment.go:36-43` |
| `IsPending()` | **Done** | `app/mall/internal/biz/payment.go:45-52` |
| `ParseTradeState()` | **Done** | `app/mall/internal/biz/payment.go:58-77` |
| Wechat → unified mapping | **Done** | `app/mall/internal/data/payment.go:103-113` |
| Alipay → unified mapping | **Done** | `app/mall/internal/data/payment.go:426-436` |

**States:**

```
NOTPAY ─────────────┐
USERPAYING ─────────┤  IsPending() → continue polling
UNSPECIFIED ────────┘

SUCCESS ────────────┐
REFUND ─────────────┤
CLOSED ─────────────┤  IsTerminal() → write final state
REVOKED ────────────┤
PAYERROR ───────────┘
```

### 3. Message Queue (River)

| Component | Status | File |
|-----------|--------|------|
| River client setup | **Done** | `app/mall/internal/data/data.go:94-115` |
| `payments` queue (max 10 workers) | **Done** | `app/mall/internal/data/data.go:107` |
| `RiverServer` (lifecycle) | **Done** | `app/mall/internal/job/job.go:14-29` |
| `PaymentMQRepo.EnqueueCheckWechatPay()` | **Done** | `app/mall/internal/data/payment.go:253-277` |
| `PaymentMQRepo.GetMQJob()` | **Done** | `app/mall/internal/data/payment.go:280-289` |
| `PaymentJobUsecase` (biz layer) | **Done** | `app/mall/internal/biz/payment.go:193-240` |

**Enqueue features:**
- Delayed execution via `ScheduledAt`
- Idempotent deduplication via `UniqueOpts{ByArgs: true}`
- Tagging for operational visibility (`wechat-pay`, `payment-{id}`)
- Configurable max polls and poll interval

### 4. Async Order Query (异步轮询查单)

| Component | Status | File |
|-----------|--------|------|
| `CheckWechatPayArgs` (River job arg) | **Done** | `app/mall/internal/biz/payment.go:148-155` |
| `CheckWechatPayWorker.Work()` | **Done** | `app/mall/internal/job/river.go:29-57` |
| `CheckWechatPayWorker.NextRetry()` | **Done** | `app/mall/internal/job/river.go:60-63` |
| Worker registration | **Done** | `app/mall/internal/job/job.go:31-35` |

**Polling flow:**

```
1.  Poll wechat order status via PaymentGateway.QueryOrder()
2.  IsTerminal()?
    ├── Yes → ApplyWechatPayQuery() [write DB atomically]
    │         ├── SUCCESS → UpdatePaymentSuccess + CompleteOrder
    │         ├── REFUND  → UpdatePaymentRefunded
    │         └── CLOSED/REVOKED/PAYERROR → UpdatePaymentFailed + CancelOrder
    └── IsPending()?
        ├── attempt < MaxPolls → return error (triggers River retry)
        └── attempt >= MaxPolls → MarkWechatPayExpired()
```

**Default polling parameters:**
- `MaxPolls = 5`
- `PollIntervalSeconds = 30`
- Max total wait: 5 × 30 = 150 seconds before expiry

### 5. Result Sync to DB (PaymentSyncRepo)

| Component | Status | File |
|-----------|--------|------|
| `PaymentSyncRepo.ApplyWechatPayQuery()` | **Done** | `app/mall/internal/data/payment.go:299-354` |
| `PaymentSyncRepo.MarkWechatPayExpired()` | **Done** | `app/mall/internal/data/payment.go:356-383` |

Both methods use PostgreSQL transactions to atomically update payment + order records.

### 6. Callback Handling (回调处理)

| Component | Status | File |
|-----------|--------|------|
| HTTP route (`POST /v1/pay/wechat/notify`) | **Done** | `app/mall/internal/server/http.go:70` |
| `HandleWechatPayNotify()` | **Stub** | `app/mall/internal/service/payment.go:131-139` |
| Proto `NotifyPayment` RPC | **Stub** | `app/mall/internal/service/payment.go:35-37` |

**Current callback implementation:**

```go
func (s *PaymentService) HandleWechatPayNotify(ctx khttp.Context) error {
    if _, err := io.ReadAll(ctx.Request().Body); err != nil {
        return errors.BadRequest("WECHAT_PAY_NOTIFY_BODY", err.Error())
    }
    return ctx.JSON(200, wechatPayNotifyAck{
        Code:    "SUCCESS",
        Message: "success",
    })
}
```

**Missing:**
- Signature verification (验签)
- Callback body decryption (解密)
- Actual payment status update via `PaymentSyncRepo`
- Order status transition
- Idempotency handling for duplicate callbacks

### 7. API Endpoints (Service Layer)

| RPC | Status | File |
|-----|--------|------|
| `CreatePayment` | **Stub** | `app/mall/internal/service/payment.go:26-28` |
| `GetPayment` | **Stub** | `app/mall/internal/service/payment.go:29-31` |
| `GetPaymentByOrder` | **Stub** | `app/mall/internal/service/payment.go:32-34` |
| `NotifyPayment` | **Stub** | `app/mall/internal/service/payment.go:35-37` |
| `RefundPayment` | **Stub** | `app/mall/internal/service/payment.go:38-40` |
| `PrepayJSAPI` | **Done** | `app/mall/internal/service/payment.go:74-96` |
| `QueryOrder` | **Done** | `app/mall/internal/service/payment.go:98-115` |
| `CloseOrder` | **Done** | `app/mall/internal/service/payment.go:117-129` |
| `CreateWechatPayCheckJob` | **Done** | `app/mall/internal/service/payment.go:42-61` |
| `GetMQJob` | **Done** | `app/mall/internal/service/payment.go:63-72` |

### 8. Database Schema

| Table | Status | File |
|-------|--------|------|
| `orders` | **Done** | `app/mall/db/migrations/000001_init_schema.up.sql:91-111` |
| `payments` | **Done** | `app/mall/db/migrations/000001_init_schema.up.sql:130-154` |
| sqlc generated queries | **Done** | `app/mall/internal/data/db/{payments,orders}.sql.go` |

**`payments` table columns:** id, order_id, user_id, merchant_id, amount, status (pending/success/failed/refunded), pay_channel (wechat/alipay), third_party_tx_id (unique index), paid_at, created_at, updated_at.

**`orders` table columns:** id, user_id, address_id, total_amount, status (creating/paid/shipped/completed/cancelled), is_completed, created_at, updated_at.

### 9. Wire Dependency Injection

| Component | Status | File |
|-----------|--------|------|
| `data.ProviderSet` | **Done** | `app/mall/internal/data/data.go:28-38` |
| `biz.ProviderSet` | **Done** | `app/mall/internal/biz/biz.go:6-15` |
| `service.ProviderSet` | **Done** | `app/mall/internal/service/service.go:5` |
| `job.ProviderSet` | **Done** | `app/mall/internal/job/job.go:13` |
| `server.ProviderSet` | **Done** | `app/mall/internal/server/server.go:8` |
| Generated wire_gen.go | **Done** | `app/mall/cmd/mall/wire_gen.go` |

**Injection chain:**

```
WechatPaymentAdapter ─┐
AlipayPaymentAdapter ─┤
                      ├→ PaymentAdapters → PaymentGateway ─┐
PgxPool ─────────────────────────────────────┘             │
  │                                                       │
  ├→ PaymentSyncRepo ────────────────────┐                │
  │                                      ├→ CheckWechatPayWorker → Workers
  ├→ RiverClient ───────┐               │
  │                     ├→ PaymentMQRepo ┤
  │                     │               │
  │                     └→ PaymentJobUsecase → PaymentService
  │                          (enqueue facade)       (service layer)
  │
  └→ Data → Repos → Usecases → Services
```

## Gap Summary

### Working

- Wechat/Alipay prepay, query, and close operations via adapters
- Channel routing through `PaymentGateway`
- River job queue for async polling
- `CheckWechatPayWorker` polling loop with retry and expiry
- `PaymentSyncRepo` transaction-based state updates to DB
- gRPC and HTTP endpoints for prepay, query, close, and check job APIs
- Complete Wire DI wiring from data through biz through service to server

### Not Yet Implemented

| Feature | Priority | Description |
|---------|----------|-------------|
| Wechat callback validation | High | Verify signature, decrypt body, actually update payment status in `HandleWechatPayNotify` |
| `CreatePayment` service | High | Create payment record + enqueue check job atomically |
| `NotifyPayment` service | Medium | Protocol-based notification handling |
| `GetPayment` / `GetPaymentByOrder` | Low | Read payment records from DB |
| Order service layer | Medium | `app/mall/internal/service/order.go` is entirely stubbed out |
| Adapter client configuration | Medium | Both `WechatPaymentAdapter` and `AlipayPaymentAdapter` clients are nil (not wired with actual credentials) |

### Related

- Refund functionality is documented in `docs/refund-feature-plan-and-progress.md`

## Dependencies

| Library | Usage |
|---------|-------|
| `github.com/go-pay/gopay/wechat` | Wechat payment API client |
| `github.com/go-pay/gopay/alipay` | Alipay API client |
| `github.com/riverqueue/river` | PostgreSQL-backed job queue |
| `github.com/jackc/pgx/v5` | PostgreSQL driver |
| `github.com/go-kratos/kratos/v2` | Microservice framework |
| `github.com/google/wire` | Compile-time dependency injection |
| `github.com/golang-jwt/jwt/v5` | JWT authentication |
