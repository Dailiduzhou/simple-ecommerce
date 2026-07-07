# Payment Business Flow

Date: 2026-07-07 (updated)

## Overview

This document traces the complete business flow of the payment subsystem, covering every database write, cache access, and message queue operation at each stage.

**Key design decisions:**

- Payment reads use **Redis + singleflight** caching with jittered TTL (~10-20 min).
- State updates are **transactional** — payment and order status changes are wrapped in a single PostgreSQL transaction.
- Async polling is powered by **River** (PostgreSQL-backed job queue), not a separate message broker.
- The unified `CreatePayment` API creates a payment record before calling the third-party prepay.
- Wechat callback is **implemented** — it parses XML, verifies signature, and enqueues a check job.
- Alipay callback is **not yet implemented** — no route registered.
- The `CheckPayWorker` is channel-agnostic (replaces the old `CheckWechatPayWorker`).

> For the Alipay-specific flow, see [payment-business-flow-alipay.md](payment-business-flow-alipay.md).

---

## Stage 1: Prepay (Legacy — superseded by unified CreatePayment)

> **Note:** The old `PrepayJSAPI` endpoint is preserved for backward compatibility but the recommended entry point is now `POST /v1/payments` (unified `CreatePayment`). The unified flow creates a payment record before calling the third-party API. See [payment-business-flow-alipay.md](payment-business-flow-alipay.md) for the current flow.

The legacy `PrepayJSAPI` flow:

```
Client                   PaymentService               Wechat Pay API
 │                           │                            │
 │  POST /v1/pay/wechat/     │                            │
 │  prepay/jsapi             │                            │
 │  {out_trade_no, amount,   │                            │
 │   openid, description}    │                            │
 │──────────────────────────►│                            │
 │                           │ ─ PaymentGateway.Prepay →  │
 │                           │   WechatPaymentAdapter     │
 │                           │                            │
 │                           │          UnifiedOrder ────►│
 │                           │         ◄── prepay_id     │
 │                           │                            │
 │                           │ Build JSAPI signing params │
 │                           │                            │
 │  PrepayJSAPIReply          │                            │
 │  {app_id, time_stamp, ...}│                            │
 │◄──────────────────────────│                            │
```

**Database:** None. No records are written at this stage (legacy flow only).

**Files:**
- `app/mall/internal/service/payment.go` — service entry (legacy)
- `app/mall/internal/biz/payment_gateway.go` — channel routing
- `app/mall/internal/data/payment.go` — Wechat Prepay implementation via `go-pay/gopay/wechat`

### Stage 1 (Current): Unified CreatePayment

The current unified flow (`POST /v1/payments`):

1. `protoToBizChannel()` maps proto enum → biz channel string (`wechat`/`alipay`)
2. `PaymentUsecase.PrepayForOrder()`:
   - `orderRepo.GetOrderByOrderNo()` — **SELECT** from `orders`
   - `CreatePayment()` — **INSERT** into `payments` (with idempotency via `idx_payments_active_order_channel`)
   - `gateway.Prepay()` → adapter → third-party API
3. For **Wechat** channels: `PrepayForOrderWithCheckJob()` wraps steps 2 + River enqueue in a single transaction
4. `encodePrepayPayload()` encodes the third-party response into `action_type` + `payload` JSON

**Database:**

| Table | Operation | Notes |
|-------|-----------|-------|
| `orders` | **SELECT** | `GetOrderByOrderNo` — look up order by merchant order number |
| `payments` | **INSERT** | `CreatePaymentWithOutTradeNo` — create payment record with `status=pending` |

**Cache:**

| Operation | Key | Notes |
|-----------|-----|-------|
| **SET** | `payment:{id}` | Cache the new payment record |
| **SET** | `payment:out_trade_no:{no}` | Cache by merchant order number |
| **SET** | `payment:order:{id}:active:{channel}` | Cache active payment by order+channel |

**Files:**
- `app/mall/internal/service/payment.go:55-119` — unified entry point
- `app/mall/internal/biz/payment.go:450-509` — `PrepayForOrder` / `PrepayForOrderWithCheckJob`
- `app/mall/internal/data/payment.go:415-449` — `CreatePayment` DB write + cache

---

## Stage 2: Enqueue Check Job

```
External caller            PaymentService              River (PostgreSQL)
 │                           │                            │
 │  POST /v1/pay/wechat/     │                            │
 │  checks                   │                            │
 │  {payment_id, order_id,   │                            │
 │   out_trade_no,           │                            │
 │   delay_seconds,          │                            │
 │   max_polls=5,            │                            │
 │   poll_interval=30}       │                            │
 │──────────────────────────►│                            │
 │                           │                            │
 │                           │ 1. Validate:               │
 │                           │    delay_seconds >= 0       │
 │                           │                            │
 │                           │ 2. Normalize defaults:      │
 │                           │    max_polls = 5            │
 │                           │    poll_interval = 30s      │
 │                           │    source = "api"           │
 │                           │                            │
 │                           │ 3. PaymentJobUsecase        │
 │                           │    .EnqueueCheckWechat()    │
 │                           │    => validate required     │
 │                           │    => scheduledAt =         │
 │                           │       now + delay_seconds   │
 │                           │                            │
 │                           │ 4. PaymentMQRepo            │
 │                           │    .EnqueueCheckWechat()    │
 │                           │                            │
 │                           │───────── INSERT ──────────►│
 │                           │          river_job          │
 │                           │          queue = "payments" │
 │                           │          kind =             │
 │                           │            "check_wechat_   │
 │                           │             pay"            │
 │                           │          args = {payment_id,│
 │                           │            order_id,        │
 │                           │            out_trade_no,    │
 │                           │            max_polls,       │
 │                           │            poll_interval,   │
 │                           │            source}          │
 │                           │          max_attempts = 5   │
 │                           │          scheduled_at =     │
 │                           │            now + delay      │
 │                           │          tags = [           │
 │                           │            "wechat-pay",    │
 │                           │            "payment-12"]    │
 │                           │          unique_by_args =   │
 │                           │            true             │
 │                           │          unique_by_queue =  │
 │                           │            true             │
 │                           │                            │
 │  MQJobInfo                │                            │
 │  {job_id, kind, queue,    │                            │
 │   state="scheduled",      │                            │
 │   tags, max_attempts,     │                            │
 │   scheduled_at, ...}       │                            │
 │◄──────────────────────────│                            │
```

**Database:**

| Table | Operation | Notes |
|-------|-----------|-------|
| `river_job` | **INSERT** | River's own table storing job metadata (kind, args, state, queue, scheduled_at, tags, max_attempts) |

**Idempotency:** `UniqueOpts{ByArgs: true, ByQueue: true}` prevents duplicate jobs with the same args in the same queue. If a duplicate is inserted, River returns `UniqueSkippedAsDuplicate` and skips silently.

**Cache:** None.

**MQ properties:**

| Property | Value | Purpose |
|----------|-------|---------|
| `queue` | `payments` | Dedicated queue with 10 max workers |
| `kind` | `check_wechat_pay` | Routes to `CheckWechatPayWorker` |
| `scheduled_at` | `now + delay_seconds` | Delayed execution — River won't pick up the job before this time |
| `max_attempts` | `args.MaxPolls` (default 5) | Hard cap on retries before River discards the job |
| `tags` | `["wechat-pay", "payment-{id}"]` | Operational visibility for debugging and monitoring |

**Files:**
- `app/mall/internal/service/payment.go:42-61` — API endpoint
- `app/mall/internal/biz/payment.go:202-240` — use case (validation + enqueue)
- `app/mall/internal/data/payment.go:253-277` — River insert with dedup

---

## Stage 3: Async Polling (CheckWechatPayWorker)

This is the core async flow. River's background scheduler picks up ready jobs and dispatches them to `CheckWechatPayWorker`.

### Polling Logic

```
River Scheduler            CheckWechatPayWorker         Wechat API           PostgreSQL
 │                              │                         │                     │
 │  scheduled_at reached,       │                         │                     │
 │  dispatch job (attempt=1)    │                         │                     │
 │─────────────────────────────►│                         │                     │
 │                              │                         │                     │
 │                              │ 1. normalize + validate │                     │
 │                              │    payment_id > 0       │                     │
 │                              │    out_trade_no != ""   │                     │
 │                              │                         │                     │
 │                              │ 2. PaymentGateway       │                     │
 │                              │    .QueryOrder()        │                     │
 │                              │────────────────────────►│                     │
 │                              │           QueryOrder    │                     │
 │                              │           (out_trade_no)│                     │
 │                              │                         │                     │
 │                              │    PaymentQueryResult   │                     │
 │                              │    {trade_state,        │                     │
 │                              │     transaction_id,     │                     │
 │                              │     total_amount}       │                     │
 │                              │◄────────────────────────│                     │
 │                              │                         │                     │
 │                              │ 3. State decision:      │                     │
 │                              │                         │                     │
 │                              │    ┌── IsPending()? ────┤                     │
 │                              │    │  (NOTPAY,          │                     │
 │                              │    │   USERPAYING,      │                     │
 │                              │    │   UNSPECIFIED)     │                     │
 │                              │    │                    │                     │
 │                              │    ├── attempt >=       │                     │
 │                              │    │   max_polls?       │                     │
 │                              │    │   YES → expired    │                     │
 │                              │    │   NO  → retry      │                     │
 │                              │    │                    │                     │
 │                              │    └── IsTerminal()? ───┤                     │
 │                              │       (SUCCESS, REFUND,│                     │
 │                              │        CLOSED, REVOKED, │                     │
 │                              │        PAYERROR)        │                     │
 │                              │       → write DB        │                     │
```

### Database Writes (Transactional)

Both `ApplyWechatPayQuery` and `MarkWechatPayExpired` use `pool.Begin(ctx)` + `defer tx.Rollback()` for atomic updates.

**SUCCESS path** (`ApplyWechatPayQuery`):

| Table | SQL | Operation |
|-------|-----|-----------|
| `payments` | `UPDATE SET status='success', third_party_tx_id=?, paid_at=CURRENT_TIMESTAMP WHERE id=?` | Mark payment paid |
| `orders` | `UPDATE SET status='completed', is_completed=TRUE WHERE id=?` | Complete the order |

**REFUND path** (`ApplyWechatPayQuery`):

| Table | SQL | Operation |
|-------|-----|-----------|
| `payments` | `UPDATE SET status='refunded' WHERE id=?` | Mark payment refunded |

**CLOSED / REVOKED / PAYERROR path** (`ApplyWechatPayQuery`):

| Table | SQL | Operation |
|-------|-----|-----------|
| `payments` | `UPDATE SET status='failed' WHERE id=?` | Mark payment failed |
| `orders` | `UPDATE SET status='cancelled', is_completed=TRUE WHERE id=?` | Cancel the order |

**Expired path** (`MarkWechatPayExpired`):

| Table | SQL | Operation |
|-------|-----|-----------|
| `payments` | `UPDATE SET status='failed' WHERE id=?` | Mark payment failed |
| `orders` | `UPDATE SET status='cancelled', is_completed=TRUE WHERE id=?` | Cancel the order |

**Transaction guarantee:** If the payment update succeeds but the order update fails, the entire transaction rolls back. This ensures payment and order never diverge.

### Retry Strategy

| Parameter | Default | Description |
|-----------|---------|-------------|
| `MaxPolls` | 5 | Maximum polling attempts before forced expiry |
| `PollIntervalSeconds` | 30 | Seconds between retry attempts |
| Max total wait | 150s (2.5 min) | Beyond this, the payment is marked expired |

When `CheckWechatPayWorker.Work()` returns an error for a pending state, River automatically calls `NextRetry()` to compute the next scheduled time:

```go
func (w *CheckWechatPayWorker) NextRetry(job *river.Job[biz.CheckWechatPayArgs]) time.Time {
    args := normalizeCheckWechatPayArgs(job.Args)
    return time.Now().Add(time.Duration(args.PollIntervalSeconds) * time.Second)
}
```

**Cache:** None. Payment data is not cached — every poll reads directly from the Wechat API and writes directly to PostgreSQL.

**Files:**
- `app/mall/internal/job/river.go:29-63` — worker logic
- `app/mall/internal/job/river.go:60-63` — retry scheduling
- `app/mall/internal/data/payment.go:299-383` — transactional DB writes (PaymentSyncRepo)

---

## Stage 4: Wechat Callback (Implemented)

```
Wechat Pay                     Mall Server
 │                               │
 │  POST /v1/pay/wechat/notify   │
 │  {XML with signature}         │
 │──────────────────────────────►│
 │                               │
 │                               │ wechat.ParseNotify(req)
 │                               │ wechat.VerifySign(apiKey, signType, notifyReq)
 │                               │
 │                               │ if ReturnCode=SUCCESS && ResultCode=SUCCESS:
 │                               │   EnqueueWechatCheckJobByOutTradeNo()
 │                               │   ── tx: SELECT payment by out_trade_no
 │                               │   ── tx: INSERT river_job (delay=0)
 │                               │
 │  XML Response                 │
 │  <xml><return_code>SUCCESS    │
 │  </return_code>               │
 │  <return_msg>OK</return_msg>  │
 │  </xml>                       │
 │◄──────────────────────────────│
```

**Implementation details:**

- Signature verification uses `wechat.VerifySign()` with the configured `ApiKey`
- On success, enqueues a check job (delay=0) via `EnqueueWechatCheckJobByOutTradeNo()` in a transaction
- If enqueue fails, logs a warning but still returns SUCCESS to Wechat (avoids infinite retry loops)
- Returns XML response (not JSON) as required by Wechat

**Callback vs Polling relationship:** When callback arrives first, the River job on its next poll sees the terminal state and returns nil (idempotent). If polling completes first, the callback's check job will also see the terminal state.

**Files:**
- `app/mall/internal/server/http.go:71` — route registration
- `app/mall/internal/service/payment.go:246-290` — callback handler with XML parsing + signature verification + check job enqueue

### Alipay Callback (Implemented)

Route registered at `http.go:72`:

```go
srv.Route("/").POST("/v1/pay/alipay/notify", payment.HandleAlipayPayNotify)
```

**Implementation details:**

- Parses form-urlencoded parameters via `alipay.ParseNotifyToBodyMap(req)`
- Signature verification uses `alipay.VerifySignWithCert(certPath, notifyReq)` (certificate mode)
- On `trade_status=TRADE_SUCCESS` or `TRADE_FINISHED`, enqueues a check job (delay=0) via `EnqueueCheckJobByOutTradeNo()` in a transaction
- If enqueue fails, logs a warning but still returns "success" to Alipay (avoids infinite retry loops)
- Returns plain text response: `"success"` or `"fail"`

**Response format:**

| Scenario | Response | Alipay behavior |
|----------|----------|-----------------|
| Parse failure | `"fail"` | Retry (8 times within 25h) |
| Signature verification failure | `"fail"` | Retry |
| Enqueue failure | `"success"` | Stop retry (logged for debugging) |
| Success | `"success"` | Stop retry |

**Files:**
- `app/mall/internal/server/http.go:72` — route registration
- `app/mall/internal/service/payment.go:293-342` — callback handler with form parsing + certificate verification + check job enqueue

---

## Cache Strategy

**Payment uses Redis + singleflight caching for reads.**

| Module | Cache | Pattern | Reason |
|--------|-------|---------|--------|
| **Event** | Redis + singleflight | Read-through cache with jittered TTL (~20 min) | Read-heavy, stale data acceptable |
| **Product** | Partial Redis | Read-through | Read-heavy, stale data acceptable |
| **Payment** | Redis + singleflight | Read-through with jittered TTL (10-20 min) | Read-heavy during lookups; writes invalidate caches via `afterCommit` hooks |
| **Order** | Redis + singleflight | Read-through with jittered TTL (10-20 min) | Same pattern as payment |

The payment caching pattern (from `app/mall/internal/data/payment.go`) uses:
- `singleflight.Group` to deduplicate concurrent cache-miss DB reads
- Redis with jittered TTL (`10 + rand(0-10) min`) to avoid thundering herd on expiry
- `afterCommit` cache invalidation on writes (ApplyPayQuery, MarkPayExpired, ClosePayment)

Cache keys:
- `payment:{id}` — by payment ID
- `payment:order:{order_id}` — by order ID
- `payment:order:{order_id}:active:{channel}` — active payment by order+channel
- `payment:out_trade_no:{no}` — by merchant order number
- `order:{id}`, `order:no:{out_trade_no}`, `order:user:{user_id}:{order_id}`, `order:user:ongoing:{user_id}` — order caches

---

## River (MQ) Operations Summary

River is a PostgreSQL-backed job queue. All jobs are stored in the `river_job` table. No separate message broker (Redis Streams, Kafka, RabbitMQ) is required.

| Trigger | River Table Operation | State Transition |
|---------|----------------------|------------------|
| `CreateWechatPayCheckJob` | **INSERT** | `scheduled` (deferred by `scheduled_at`) |
| Scheduler picks up job | **SELECT + UPDATE** | `scheduled` → `running` |
| Worker returns error (pending state) | **UPDATE** | `running` → `retryable` (rescheduled via `NextRetry()`) |
| Worker returns nil (terminal state) | **UPDATE** | `running` → `completed` |
| Worker exceeds `max_attempts` | **UPDATE** | `retryable` → `discarded` |
| Duplicate enqueue (same args) | **SKIP** | No insert, returns `UniqueSkippedAsDuplicate` |

**Queue configuration** (`app/mall/internal/data/data.go:104-115`):

```go
Queues: map[string]river.QueueConfig{
    river.QueueDefault: {MaxWorkers: 10},
    "payments":         {MaxWorkers: 10},
}
```

---

## End-to-End Timeline

```
Time ──────────────────────────────────────────────────────────────►

T+0ms     Client calls PrepayJSAPI
          ── Wechat UnifiedOrder ⟶ returns prepay_id
          ── Response sent to client (NO DB writes)
          
T+100ms   Client calls CreateWechatPayCheckJob
          ── INSERT river_job (scheduled_at = now + delay)
          ── Response sent with job_id
          
T+3s      River picks up job (attempt 1)
          ── QueryOrder ⟶ trade_state = USERPAYING (pending)
          ── Worker returns error ⟶ NextRetry = now + 30s
          ── River reschedules job
          
T+15s     User completes payment on Wechat side
          ── Wechat state: NOTPAY → SUCCESS
          
T+33s     River picks up job (attempt 2)
          ── QueryOrder ⟶ trade_state = SUCCESS
          ── BEGIN TRANSACTION
          ──   UPDATE payments SET status='success', third_party_tx_id='...'
          ──   UPDATE orders SET status='completed', is_completed=TRUE
          ── COMMIT
          ── Worker returns nil ⟶ job marked completed

Duration: ~33 seconds from prepay to payment confirmed
```

### Edge Cases

**Callback arrives during polling:**
```
T+20s    Wechat sends notify (SUCCESS)
         ── Stub returns 200 OK (no DB update, NOOP)
T+33s    River poll completes the state update
```

**Polling expires before user pays:**
```
T+150s   attempt=5, max_polls=5, trade_state=NOTPAY
         ── BEGIN TRANSACTION
         ──   UPDATE payments SET status='failed'
         ──   UPDATE orders SET status='cancelled', is_completed=TRUE
         ── COMMIT
         ── Worker returns nil ⟶ job completed
```

**Payment closes immediately (unpaid after timeout):**
```
T+33s    QueryOrder ⟶ trade_state = CLOSED
         ── BEGIN TRANSACTION
         ──   UPDATE payments SET status='failed'
         ──   UPDATE orders SET status='cancelled', is_completed=TRUE
         ── COMMIT
```

---

## SQL Queries Reference

All payment and order SQL is generated by sqlc. Sources:

| File | Generated From |
|------|---------------|
| `app/mall/internal/data/db/payments.sql.go` | `app/mall/db/query/payments.sql` |
| `app/mall/internal/data/db/orders.sql.go` | `app/mall/db/query/orders.sql` |

**Key payment queries:**

```sql
-- name: CreatePayment :one
INSERT INTO payments (order_id, user_id, merchant_id, amount, status, pay_channel)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: UpdatePaymentSuccess :one
UPDATE payments
SET status = 'success', third_party_tx_id = $2, paid_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 RETURNING *;

-- name: UpdatePaymentFailed :exec
UPDATE payments SET status = 'failed', updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: UpdatePaymentRefunded :exec
UPDATE payments SET status = 'refunded', updated_at = CURRENT_TIMESTAMP WHERE id = $1;
```

**Key order queries:**

```sql
-- name: CompleteOrder :exec
UPDATE orders SET is_completed = TRUE, status = 'completed', updated_at = CURRENT_TIMESTAMP WHERE id = $1;

-- name: CancelOrder :exec
UPDATE orders SET is_completed = TRUE, status = 'cancelled', updated_at = CURRENT_TIMESTAMP WHERE id = $1;
```

---

## Database Schema (Payment & Order)

**`payments` table** (`000001_init_schema.up.sql:130-154`):

| Column | Type | Notes |
|--------|------|-------|
| `id` | BIGINT PK | Auto-generated identity |
| `order_id` | BIGINT FK → orders(id) | Associated order |
| `user_id` | BIGINT FK → users(id) | Paying user |
| `merchant_id` | BIGINT FK → users(id) | Receiving merchant |
| `amount` | NUMERIC(10,2) | Payment amount |
| `status` | VARCHAR(20) | `pending` / `success` / `failed` / `refunded` |
| `pay_channel` | VARCHAR(30) | `wechat` / `alipay` |
| `third_party_tx_id` | VARCHAR(128) UNIQUE (partial) | Wechat/Alipay transaction ID |
| `paid_at` | TIMESTAMPTZ | When payment succeeded |
| `created_at` | TIMESTAMPTZ | DEFAULT CURRENT_TIMESTAMP |
| `updated_at` | TIMESTAMPTZ | DEFAULT CURRENT_TIMESTAMP |

**`orders` table** (`000001_init_schema.up.sql:91-111`):

| Column | Type | Notes |
|--------|------|-------|
| `id` | BIGINT PK | Auto-generated identity |
| `user_id` | BIGINT FK → users(id) | Order owner |
| `address_id` | BIGINT FK → shipping_addresses(id) | Shipping address |
| `total_amount` | INTEGER | Amount in cents |
| `status` | VARCHAR(20) | `creating` / `paid` / `shipped` / `completed` / `cancelled` |
| `is_completed` | BOOLEAN | Terminal flag for index filtering |
| `created_at` | TIMESTAMPTZ | DEFAULT CURRENT_TIMESTAMP |
| `updated_at` | TIMESTAMPTZ | DEFAULT CURRENT_TIMESTAMP |

---

## Wire DI Chain

The full dependency injection chain for payment components (`app/mall/cmd/mall/wire_gen.go`):

```
WechatPaymentAdapter ─┐
AlipayPaymentAdapter ─┤
                      ├─ NewPaymentAdapters → PaymentGateway ──────────┐
PgxPool ────────────────────────────────────┘                         │
  │                                                                    │
  ├─ PaymentSyncRepo ──────────────────┐                              │
  │                                    ├─ CheckWechatPayWorker → Workers
  ├─ RiverClient ─────┐               │
  │                   ├─ PaymentMQRepo ┤
  │                   │               │
  │                   └─ PaymentJobUsecase → PaymentService
  │                        (enqueue facade)      (gRPC/HTTP handlers)
  │
  └─ Data → Repos → Usecases → Services
```

---

## Gap Summary

### Working end-to-end

- Unified `CreatePayment` → order lookup → payment record → third-party prepay → return frontend action
- Wechat: `PrepayForOrderWithCheckJob` → atomic payment write + River enqueue in transaction
- Alipay: `PrepayForOrder` → payment record → `TradeWapPay` → return redirect URL
- `CheckPayWorker` → poll → transactional DB sync → retry/expire (channel-agnostic)
- Wechat callback → XML parse → signature verify → enqueue check job
- Alipay callback → form parse → certificate verify → enqueue check job
- QueryOrder / CloseOrder via PaymentGateway (both channels)
- Redis + singleflight caching for payment reads with `afterCommit` invalidation

### Stubs / not implemented

| Component | Priority | What's missing |
|-----------|----------|----------------|
| `RefundPayment` service | Medium | Returns empty stub |
| Order service (all methods) | Medium | `OrderUsecase` methods return nil |
| Adapter client config | Medium | Both Wechat and Alipay adapter clients may be nil if credentials not configured |
