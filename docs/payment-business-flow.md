# Payment Business Flow

Date: 2026-06-10

## Overview

This document traces the complete business flow of the payment subsystem, covering every database write, cache access, and message queue operation at each stage.

**Key design decisions:**

- Payment data has **no caching** — every read goes directly to PostgreSQL for strong consistency.
- State updates are **transactional** — payment and order status changes are wrapped in a single PostgreSQL transaction.
- Async polling is powered by **River** (PostgreSQL-backed job queue), not a separate message broker.
- Wechat callback is a **stub** — it returns `SUCCESS` without signature verification, decryption, or state updates.

---

## Stage 1: Prepay

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
 │                           │ in memory:                 │
 │                           │   AppID, TimeStamp,        │
 │                           │   NonceStr, Package,       │
 │                           │   SignType, PaySign(MD5)   │
 │                           │                            │
 │  PrepayJSAPIReply          │                            │
 │  {app_id, time_stamp,     │                            │
 │   nonce_str, package,     │                            │
 │   sign_type, pay_sign}    │                            │
 │◄──────────────────────────│                            │
 │                           │                            │
 │  Client calls             │                            │
 │  wx.requestPayment()      │                            │
```

**Database:** None. No records are written at this stage.

**Cache:** None.

**MQ:** None.

**Files:**
- `app/mall/internal/service/payment.go:74-96` — service entry
- `app/mall/internal/biz/payment_gateway.go:29-36` — channel routing
- `app/mall/internal/data/payment.go:45-83` — Wechat Prepay implementation via `go-pay/gopay/wechat`

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

## Stage 4: Wechat Callback (Stub)

```
Wechat Pay                     Mall Server
 │                               │
 │  POST /v1/pay/wechat/notify   │
 │  {encrypted+signed pay result}│
 │──────────────────────────────►│
 │                               │
 │                               │ io.ReadAll(body)
 │                               │ ── no signature verification
 │                               │ ── no decryption
 │                               │ ── no PaymentSyncRepo call
 │                               │ ── no state update
 │                               │
 │  HTTP 200                     │
 │  {"code":"SUCCESS",           │
 │   "message":"success"}        │
 │◄──────────────────────────────│
```

**Missing from callback implementation:**

- Signature verification (验签) — no check that the notification actually came from Wechat
- Body decryption (解密) — Wechat encrypts callback bodies with AES-256-GCM
- State update — no call to `PaymentSyncRepo.ApplyWechatPayQuery()`
- Idempotency — no guard against duplicate notifications

**Callback vs Polling relationship:** When properly implemented, callback and polling complement each other. If callback arrives first, the River job on its next poll sees the terminal state and returns nil (idempotent). If polling completes first, the callback is already redundant and can safely return `SUCCESS`.

**Files:**
- `app/mall/internal/server/http.go:70` — route registration (raw HTTP, outside proto-generated routes)
- `app/mall/internal/service/payment.go:131-139` — stub handler

---

## Cache Strategy

**Payment uses zero caching.**

This contrasts with other modules in the project:

| Module | Cache | Pattern | Reason |
|--------|-------|---------|--------|
| **Event** | Redis + singleflight | Read-through cache with jittered TTL (~20 min) | Read-heavy, stale data acceptable |
| **Product** | Partial Redis | Read-through | Read-heavy, stale data acceptable |
| **Payment** | **None** | Direct PostgreSQL reads | Payment state must be strongly consistent |
| **Order** | **None** | Direct PostgreSQL reads | Order state must be strongly consistent |

The Event module's caching pattern (from `app/mall/internal/data/event.go`) uses:
- `singleflight.Group` to deduplicate concurrent cache-miss DB reads
- Redis with jittered TTL (`10 + rand(0-10) min`) to avoid thundering herd on expiry
- Scan-based cache invalidation on writes

Payment intentionally avoids this pattern because:

1. Payment reads are low-frequency (only during explicit user lookup).
2. Payment state changes are irreversible — showing stale data is a correctness bug.
3. The Wechat API is the source of truth; the local DB mirrors it. Caching would add a third layer of staleness.

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

- PrepayJSAPI → Wechat UnifiedOrder → return JSAPI params
- CreateWechatPayCheckJob → River INSERT with dedup + delayed schedule
- CheckWechatPayWorker → poll → transactional DB sync → retry/expire
- QueryOrder / CloseOrder via PaymentGateway

### Stubs / not implemented

| Component | Priority | What's missing |
|-----------|----------|----------------|
| `CreatePayment` service | High | No payment record is created before Prepay; the flow has no initial DB write |
| Wechat callback (`HandleWechatPayNotify`) | High | No signature verification, no decryption, no state update |
| `GetPayment` / `GetPaymentByOrder` | Low | Read endpoints return empty stubs |
| `NotifyPayment` proto endpoint | Low | Returns empty stub |
| Order service (all methods) | Medium | `app/mall/internal/service/order.go` is entirely stubbed |
| Adapter client config | Medium | Both Wechat and Alipay adapter clients are nil (credentials not wired) |
| Refund | Medium | See `docs/refund-feature-plan-and-progress.md` |
