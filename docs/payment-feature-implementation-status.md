# Payment Feature Implementation Status

Date: 2026-07-07 (updated)

## Context

This document records the current implementation status of the payment subsystem in the Go Kratos mall service. It covers the payment adapter pattern, message queue (River), async order query (polling), and callback handling.

## Architecture Overview

```
HTTP/gRPC Transport
  PaymentService (unified)
  ─ CreatePayment      ← unified prepay (wechat + alipay)
  ─ QueryPayment       ← unified query
  ─ ClosePayment       ← unified close
  ─ GetPayment         ← read payment by ID
  ─ GetPaymentByOrder  ← read payment by order ID
  ─ CreateWechatCheck  ← enqueue poll job
  ─ GetMQJob
  ─ HandleWechatPayNotify (HTTP callback)
       │
Biz Layer
  PaymentUsecase
  ─ PrepayForOrder / PrepayForOrderWithCheckJob
  ─ QueryOrder / CloseOrder
  ─ EnqueueWechatCheckJobByOutTradeNo
  PaymentJobUsecase
  ─ EnqueueCheckPay / EnqueueCheckPayTx
       │
  PaymentGateway (router)
  ─ Prepay / Query / Close → adapter by channel
       │
Data Layer
  PaymentRepo (DB + Redis cache)
  PaymentMQRepo (River)
       │              ┌─────┴──────┐
       │              WechatAdapter │
       │              AlipayAdapter │
       │              └────────────┘
       │
Job Layer (River Workers)
  CheckPayWorker (channel-agnostic)
  ─ Work(): poll order status via gateway
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

| Operation | Wechat (gopay/wechat) | Alipay (gopay/alipay/v3) |
|-----------|----------------------|----------------------|
| Prepay | `UnifiedOrder` (JSAPI) | `TradeWapPay` (WAP redirect) |
| QueryOrder | `QueryOrder` | `TradeQuery` |
| CloseOrder | `CloseOrder` | `POST /v3/alipay/trade/close` (custom via `DoAliPayAPISelfV3`) |

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

**Alipay → unified mapping** (`mapAlipayTradeState`):

| Alipay `trade_status` | Unified `TradeState` |
|----------------------|---------------------|
| `WAIT_BUYER_PAY` | `NOTPAY` |
| `TRADE_SUCCESS` | `SUCCESS` |
| `TRADE_FINISHED` | `SUCCESS` |
| `TRADE_CLOSED` | `CLOSED` |

### 3. Message Queue (River)

| Component | Status | File |
|-----------|--------|------|
| River client setup | **Done** | `app/mall/internal/data/data.go:101-122` |
| `payments` queue (max 10 workers) | **Done** | `app/mall/internal/data/data.go:114` |
| `RiverServer` (lifecycle) | **Done** | `app/mall/internal/job/job.go:15-29` |
| `PaymentMQRepo.EnqueueCheckPay()` | **Done** | `app/mall/internal/data/payment.go:789-799` |
| `PaymentMQRepo.EnqueueCheckPayTx()` | **Done** | `app/mall/internal/data/payment.go:801-815` |
| `PaymentMQRepo.GetMQJob()` | **Done** | `app/mall/internal/data/payment.go:842-851` |
| `PaymentJobUsecase` (biz layer) | **Done** | `app/mall/internal/biz/payment.go:244-289` |

**Enqueue features:**
- Delayed execution via `ScheduledAt`
- Idempotent deduplication via `UniqueOpts{ByArgs: true, ByQueue: true}` (dedup by `out_trade_no` which carries `river:"unique"` tag)
- Tagging for operational visibility (`pay-channel-{channel}`, `payment-{id}`)
- Configurable max polls and poll interval
- Transactional enqueue via `InsertTx` (used in `PrepayForOrderWithCheckJob`)

### 4. Async Order Query (异步轮询查单)

| Component | Status | File |
|-----------|--------|------|
| `CheckPayArgs` (River job arg, channel-agnostic) | **Done** | `app/mall/internal/biz/payment.go:152-164` |
| `CheckPayWorker.Work()` | **Done** | `app/mall/internal/job/river.go:29-57` |
| `CheckPayWorker.NextRetry()` | **Done** | `app/mall/internal/job/river.go:60-63` |
| Worker registration | **Done** | `app/mall/internal/job/job.go:31-35` |

**Polling flow:**

```
1.  Poll order status via PaymentGateway.QueryOrder() (channel from args)
2.  IsTerminal()?
    ├── Yes → ApplyPayQuery() [write DB atomically]
    │         ├── SUCCESS → UpdatePaymentSuccess + CompleteOrder
    │         ├── REFUND  → UpdatePaymentRefunded
    │         └── CLOSED/REVOKED/PAYERROR → UpdatePaymentFailed + CancelOrder
    └── IsPending()?
        ├── attempt < MaxPolls → return error (triggers River retry)
        └── attempt >= MaxPolls → MarkPayExpired()
```

**Default polling parameters:**
- `MaxPolls = 5` (NormalizeCheckPayArgs default), overridable to 30 via service config
- `PollIntervalSeconds = 30` (NormalizeCheckPayArgs default), overridable to 10 via service config
- Max total wait: MaxPolls × PollIntervalSeconds

### 5. Result Sync to DB (PaymentRepo)

| Component | Status | File |
|-----------|--------|------|
| `PaymentRepo.ApplyPayQuery()` | **Done** | `app/mall/internal/data/payment.go:629-668` |
| `PaymentRepo.MarkPayExpired()` | **Done** | `app/mall/internal/data/payment.go:670-686` |
| `PaymentRepo.ClosePayment()` | **Done** | `app/mall/internal/data/payment.go:598-627` |

All methods use PostgreSQL transactions to atomically update payment + order records, then invalidate Redis caches via `afterCommit` hooks.

### 6. Callback Handling (回调处理)

| Component | Status | File |
|-----------|--------|------|
| Wechat HTTP route (`POST /v1/pay/wechat/notify`) | **Done** | `app/mall/internal/server/http.go:71` |
| `HandleWechatPayNotify()` (XML parse + sign verify + enqueue) | **Done** | `app/mall/internal/service/payment.go:246-290` |
| Alipay HTTP route (`POST /v1/pay/alipay/notify`) | **Not implemented** | — |
| Proto `NotifyPayment` RPC | **Stub** | `app/mall/internal/service/payment.go:206-208` |

**Current Wechat callback implementation:**

```go
func (s *PaymentService) HandleWechatPayNotify(ctx khttp.Context) error {
    req := ctx.Request()
    notifyReq, err := wechat.ParseNotify(req)
    // ... signature verification with apiKey ...
    if notifyReq.ReturnCode == "SUCCESS" && notifyReq.ResultCode == "SUCCESS" && notifyReq.OutTradeNo != "" {
        // enqueue a check job (delay=0) to actively query payment result
        s.paymentUc.EnqueueWechatCheckJobByOutTradeNo(ctx, notifyReq.OutTradeNo, checkJob)
    }
    ack := wechat.NotifyResponse{ReturnCode: "SUCCESS", ReturnMsg: "OK"}
    // return XML response
}
```

**Alipay callback — not yet implemented:**
- No route registered for `/v1/pay/alipay/notify`
- No signature verification (RSA2)
- No payment status update
- `ALIPAY_NOTIFY_URL` env var is configured and sent during prepay, but server has no handler

### 7. API Endpoints (Service Layer)

| RPC | Status | File |
|-----|--------|------|
| `CreatePayment` (unified) | **Done** | `app/mall/internal/service/payment.go:55-119` |
| `QueryPayment` (unified) | **Done** | `app/mall/internal/service/payment.go:143-164` |
| `ClosePayment` (unified) | **Done** | `app/mall/internal/service/payment.go:167-183` |
| `GetPayment` | **Done** | `app/mall/internal/service/payment.go:188-194` |
| `GetPaymentByOrder` | **Done** | `app/mall/internal/service/payment.go:197-203` |
| `RefundPayment` | **Stub** | `app/mall/internal/service/payment.go:206-208` |
| `CreateWechatPayCheckJob` | **Done** | `app/mall/internal/service/payment.go:211-230` |
| `GetMQJob` | **Done** | `app/mall/internal/service/payment.go:233-242` |
| `HandleWechatPayNotify` (HTTP) | **Done** | `app/mall/internal/service/payment.go:246-290` |

### 8. Database Schema

| Table | Status | File |
|-------|--------|------|
| `orders` | **Done** | `app/mall/db/migrations/000001_init_schema.up.sql:91-111` |
| `payments` | **Done** | `app/mall/db/migrations/000001_init_schema.up.sql:130-154` |
| sqlc generated queries | **Done** | `app/mall/internal/data/db/{payments,orders}.sql.go` |

**`payments` table columns:** id, order_id, user_id, merchant_id, amount, status (pending/success/failed/refunded), pay_channel (wechat/alipay), out_trade_no (merchant order number), third_party_tx_id (unique index per channel), paid_at, created_at, updated_at.

**Key indexes:**
- `idx_payments_active_out_trade_no_channel`: UNIQUE PARTIAL `(out_trade_no, pay_channel) WHERE status IN ('pending','success')`
- `idx_payments_active_order_channel`: UNIQUE PARTIAL `(order_id, pay_channel) WHERE status IN ('pending','success')`
- `idx_payments_third_party_tx_id_channel`: UNIQUE PARTIAL `(third_party_tx_id, pay_channel) WHERE third_party_tx_id IS NOT NULL`

**`orders` table columns:** id, user_id, address_id, total_amount (in cents), out_trade_no (merchant order number), status (creating/paid/shipped/completed/cancelled), is_completed, created_at, updated_at.

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
                      ├→ NewPaymentAdapters → PaymentGateway ─┐
PgxPool ─────────────────────────────────────────┘             │
  │                                                           │
  ├→ PaymentRepo (DB + Redis cache) ─────────┐                │
  │                                          ├→ CheckPayWorker → Workers
  ├→ RiverClient ───────┐                   │
  │                     ├→ PaymentMQRepo ────┤
  │                     │                   │
  │                     └→ PaymentJobUsecase ┤
  │                          (enqueue facade) │
  │                                          │
  ├→ TxManager ──────────────────────────────┤
  ├→ IDGenerator ────────────────────────────┤
  │                                          │
  └→ NewPaymentUsecase(gateway, repos, ...) ─┤
                                             │
  NewPaymentService(uc, jobs, conf) ←────────┘
```

## Gap Summary

### Working

- Unified `CreatePayment` API: order lookup → payment record → third-party prepay → frontend action encoding
- Wechat: `PrepayForOrderWithCheckJob` — atomic payment write + River enqueue in transaction
- Alipay: `PrepayForOrder` → `TradeWapPay` → redirect URL
- Channel routing through `PaymentGateway` (Wechat + Alipay adapters)
- River job queue for async polling (`CheckPayWorker` — channel-agnostic)
- `CheckPayWorker` polling loop with retry and expiry
- `PaymentRepo.ApplyPayQuery()` / `MarkPayExpired()` transaction-based state updates to DB
- Redis + singleflight caching for payment reads with `afterCommit` invalidation
- Wechat callback: XML parsing, signature verification, check job enqueue
- gRPC and HTTP endpoints for unified payment APIs
- Complete Wire DI wiring from data through biz through service to server

### Not Yet Implemented

| Feature | Priority | Description |
|---------|----------|-------------|
| Alipay callback handler | High | No route for `/v1/pay/alipay/notify`; no RSA2 signature verification; no state sync |
| `RefundPayment` service | Medium | Returns empty stub |
| Order usecase layer | Medium | `OrderUsecase` methods in `biz/order.go` return nil |
| Adapter client configuration | Medium | Both `WechatPaymentAdapter` and `AlipayPaymentAdapter` clients may be nil if credentials not configured |

### Related

- Refund functionality is documented in `docs/refund-feature-plan-and-progress.md`
- Alipay-specific flow is documented in `docs/payment-business-flow-alipay.md`

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
