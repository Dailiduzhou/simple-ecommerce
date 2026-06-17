# Out-Trade-No Uniqueness Plan

Date: 2026-06-14

## Context

`out_trade_no` (商户订单号) is the merchant-side order id passed to Wechat/Alipay gateways, and `transaction_id` / `trade_no` (collectively `third_party_tx_id` in this codebase) is the third-party-assigned id returned by the gateway. Today the system has *partial* uniqueness for `third_party_tx_id` (a Postgres partial unique index) but **no uniqueness for `out_trade_no`** anywhere — neither in the database, nor in the application, nor in the River job dedup.

This document records the plan to make `out_trade_no` and `third_party_tx_id` both unique across the system, in a way that is idempotent, channel-scoped, and survives retries.

## Locked Decisions

| # | Decision |
|---|---|
| 1 | **Idempotent** on duplicate `(order_id, pay_channel)`. A second `CreatePayment` for the same order + channel returns the existing active row, no second row created. |
| 2 | **Snowflake id** is the source of truth. Server generates `out_trade_no` by default. Client may override. |
| 3 | The unique index on `(out_trade_no, pay_channel)` includes `'success'` — the id is "burnt" once a payment succeeds. A new attempt must use a new id. |
| 4 | **Keep River dedup forever** (`UniqueOpts.ByArgs: true`, no `ByPeriod`). |
| 5 | No backfill of legacy `payments` rows; the new column is nullable until a later operation flips it. |
| 6 | `out_trade_no` is returned in `PaymentInfo` proto. |
| 7 | Work is split into three phases: A (schema + sqlc), B (biz + data + service), C (River dedup). |
| 8 | Polling config (in Phase C if needed) is re-derived from defaults in `normalizeCheckPayArgs`, no new column or table. |
| 9 | Bilingual (Chinese + English) comments in migration SQL, matching the style of `migrations/000001_init_schema.up.sql`. |
| 10 | `validateOutTradeNo` lives in `biz` — the domain owns the invariant. |

## Architectural Target

```
┌─────────────────────────────────────────────────────────────────┐
│ proto / wire: optional out_trade_no in CreatePaymentRequest     │
│               returned in PaymentInfo                            │
├─────────────────────────────────────────────────────────────────┤
│ service:   no transport-layer validation beyond passthrough      │
├─────────────────────────────────────────────────────────────────┤
│ biz:       IDGenerator.GenerateString() → default out_trade_no  │
│            CreatePayment: idempotent on (order_id, pay_channel) │
│            validateOutTradeNo: charset + length                  │
├─────────────────────────────────────────────────────────────────┤
│ data:      payments.out_trade_no NOT NULL                       │
│            UNIQUE INDEX (out_trade_no, pay_channel)             │
│              WHERE status IN ('pending','success')              │
│            UNIQUE INDEX (third_party_tx_id, pay_channel)        │
│              WHERE third_party_tx_id IS NOT NULL                │
│            sqlc: CreatePaymentWithOutTradeNo                    │
│            sqlc: GetActivePaymentByOrderChannel                 │
├─────────────────────────────────────────────────────────────────┤
│ river:     dedup hash computed over full CheckPayArgs           │
│            (limitation documented; not subset in this plan)     │
└─────────────────────────────────────────────────────────────────┘
```

## Critical Gaps Closed by This Plan

| Gap | Where today | Impact |
|---|---|---|
| `out_trade_no` is never stored in `payments` | `payments` table | Retries of `CreatePayment` for the same order create a second row. |
| `out_trade_no` ↔ `payment_id` is **not** unique | `CheckPayArgs` (River dedup is whole-struct) | Two River jobs for the same payment with different `out_trade_no` both run. |
| No uniqueness on `(order_id, pay_channel, status='pending')` in DB | `payments` | A user can call `CreatePayment` twice for the same order. |
| `third_party_tx_id` uniqueness is channel-blind | `idx_payments_third_party_tx_id` | A Wechat `transaction_id` and an Alipay `trade_no` could in theory collide. |
| `out_trade_no` collisions on the *gateway* side are not detected locally | `Prepay` call | No way to know it's a duplicate before the gateway rejects. |
| Alipay vs Wechat `out_trade_no` share the same client-supplied string | proto + biz | No `pay_channel` prefix; cross-channel collision is possible. |

## Out of Scope

- Refund-flow `out_refund_no` uniqueness (separate feature, see `refund-feature-plan-and-progress.md`).
- `GetPaymentByOutTradeNo` RPC — `out_trade_no` is already returned in `PaymentInfo`; clients can lookup by id or order_id.
- Subsetting River's `ByArgs` dedup hash — left as a `// TODO` comment in `payment.go`; the data-layer uniqueness now catches the bad outcome even if a duplicate job runs.
- Backfill of legacy rows with `out_trade_no` values.

---

## Phase A — Schema + sqlc (foundation, no behavior change)

### A1. Migration `app/mall/db/migrations/000007_payments_out_trade_no.up.sql`

```sql
-- 000007_payments_out_trade_no.up.sql
-- 为 payments 表添加 out_trade_no 列，并在非终态上建立唯一索引。
-- 失败/退款的支付可以重新生成 out_trade_no 后重试。

ALTER TABLE payments
  ADD COLUMN out_trade_no VARCHAR(64);

-- 活跃态唯一性: (out_trade_no, pay_channel) 在 pending/success 状态下唯一。
-- 一个商户订单号在同一渠道上被"占用"直到成功或被显式关闭。
CREATE UNIQUE INDEX idx_payments_active_out_trade_no_channel
  ON payments(out_trade_no, pay_channel)
  WHERE status IN ('pending','success') AND out_trade_no IS NOT NULL;

-- 普通索引: 用于按 out_trade_no 查询 (notify / sync 路径)。
CREATE INDEX idx_payments_out_trade_no
  ON payments(out_trade_no)
  WHERE out_trade_no IS NOT NULL;

-- 替换原有的渠道盲唯一索引。Wechat transaction_id 和 Alipay trade_no
-- 共享同一列，必须按 pay_channel 分别约束。
DROP INDEX IF EXISTS idx_payments_third_party_tx_id;
CREATE UNIQUE INDEX idx_payments_third_party_tx_id_channel
  ON payments(third_party_tx_id, pay_channel)
  WHERE third_party_tx_id IS NOT NULL;
```

`down.sql` reverses all three changes. Pure reversible migration, no data loss.

### A2. sqlc query `app/mall/db/query/payments.sql` — append

```sql
-- name: CreatePaymentWithOutTradeNo :one
INSERT INTO payments (
  order_id, user_id, merchant_id, amount, status, pay_channel, out_trade_no
) VALUES (
  $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: GetActivePaymentByOrderChannel :one
SELECT * FROM payments
WHERE order_id = $1 AND pay_channel = $2 AND status IN ('pending','success')
LIMIT 1;
```

Then `sqlc generate`. This produces:
- `db.CreatePaymentWithOutTradeNoParams` + `Querier.CreatePaymentWithOutTradeNo`
- `db.GetActivePaymentByOrderChannelParams` + `Querier.GetActivePaymentByOrderChannel`
- Updated `models.go`: `Payment.OutTradeNo` field added.

### A3. Mock regeneration
`make mock` (or `go generate ./...`) to refresh `internal/data/db/mock/querier_mock.go` so the new methods are mockable.

### A4. Verify Phase A
- `go build ./...`
- `go test ./...` (no behavior change yet, should all pass)
- `psql` smoke: insert two pending payments with same `out_trade_no`+`pay_channel` → expect 23505; flip one to `failed` → insert succeeds.

---

## Phase B — biz + data + service (idempotent create, server-generated out_trade_no)

### B1. biz layer (`internal/biz/payment.go`)

**Add field to `CreatePaymentArgs`:**
```go
type CreatePaymentArgs struct {
    OrderID    int64
    UserID     int64
    MerchantID int64
    Amount     int32
    PayChannel string
    OutTradeNo string // optional; server fills if empty
}
```

**Add field to `PaymentDO`:**
```go
type PaymentDO struct {
    ID             int64
    OrderID        int64
    UserID         int64
    MerchantID     int64
    Amount         int32
    Status         string
    PayChannel     string
    OutTradeNo     string // NEW
    ThirdPartyTxID string
    PaidAt         *time.Time
    CreatedAt      time.Time
    UpdatedAt      time.Time
}
```

**Inject `IDGenerator` into `paymentUsecase`:**
```go
type paymentUsecase struct {
    gateway     PaymentGateway
    paymentRepo PaymentRepo
    orderRepo   OrderRepo
    idGen       IDGenerator     // NEW
    log         *log.Helper
}

func NewPaymentUsecase(
    gateway PaymentGateway,
    paymentRepo PaymentRepo,
    orderRepo OrderRepo,
    idGen IDGenerator,            // NEW
    logger log.Logger,
) PaymentUsecase { ... }
```

**Rewrite `CreatePayment` to be idempotent and server-generate the id:**
```go
func (uc *paymentUsecase) CreatePayment(ctx context.Context, orderID, userID, merchantID int64, payChannel string) (*PaymentDO, error) {
    order, err := uc.orderRepo.GetOrder(ctx, orderID)
    if err != nil { return nil, err }
    channel := NormalizePayChannel(payChannel)
    if channel == "" { channel = string(Wechat) }

    // Idempotency: short-circuit on existing active payment.
    existing, err := uc.paymentRepo.GetActivePaymentByOrderChannel(ctx, orderID, channel)
    if err == nil && existing != nil {
        uc.log.WithContext(ctx).Infof("reusing existing active payment_id=%d order_id=%d channel=%s", existing.ID, orderID, channel)
        return existing, nil
    }
    if err != nil && !errors.Is(err, pgx.ErrNoRows) {
        return nil, err
    }

    outTradeNo, err := uc.resolveOutTradeNo(ctx, "")
    if err != nil { return nil, err }

    return uc.paymentRepo.CreatePayment(ctx, CreatePaymentArgs{
        OrderID:    orderID,
        UserID:     userID,
        MerchantID: merchantID,
        Amount:     order.TotalAmount,
        PayChannel: channel,
        OutTradeNo: outTradeNo,
    })
}

func (uc *paymentUsecase) resolveOutTradeNo(_ context.Context, supplied string) (string, error) {
    if supplied != "" {
        if err := validateOutTradeNo(supplied); err != nil {
            return "", err
        }
        return supplied, nil
    }
    return uc.idGen.GenerateString(), nil
}
```

**New validation helper (lives in `biz` per decision #10):**
```go
// validateOutTradeNo enforces: required, ≤ 64 chars, charset [A-Za-z0-9_-].
// Wechat allows 32 chars, Alipay 64; 64 is the upper bound across both.
// 字符集限制防止注入特殊字符到第三方支付系统的 URL/表单字段中。
func validateOutTradeNo(s string) error {
    if s == "" {
        return errors.BadRequest("OUT_TRADE_NO_REQUIRED", "out_trade_no is required")
    }
    if len(s) > 64 {
        return errors.BadRequest("OUT_TRADE_NO_TOO_LONG", "out_trade_no must be ≤ 64 chars")
    }
    for _, r := range s {
        switch {
        case r >= 'A' && r <= 'Z':
        case r >= 'a' && r <= 'z':
        case r >= '0' && r <= '9':
        case r == '_' || r == '-':
        default:
            return errors.BadRequest("OUT_TRADE_NO_INVALID_CHARSET", "out_trade_no must match [A-Za-z0-9_-]")
        }
    }
    return nil
}
```

**Add `ErrPaymentConflict` sentinel:**
```go
// ErrPaymentConflict is returned when a CreatePayment insert collides on
// idx_payments_active_out_trade_no_channel. Translated to ALREADY_EXISTS / 409.
var ErrPaymentConflict = errors.Conflict("PAYMENT_OUT_TRADE_NO_CONFLICT",
    "a payment with this out_trade_no already exists for this channel")
```

### B2. data layer (`internal/data/payment.go`)

**`PaymentRepo.CreatePayment` — switch to the new sqlc query and capture out_trade_no:**
```go
func (r *PaymentRepo) CreatePayment(ctx context.Context, args biz.CreatePaymentArgs) (*biz.PaymentDO, error) {
    amount := decimal.NewFromInt(int64(args.Amount))
    payment, err := querierFromContext(ctx, r.q).CreatePaymentWithOutTradeNo(ctx, db.CreatePaymentWithOutTradeNoParams{
        OrderID:    args.OrderID,
        UserID:     args.UserID,
        MerchantID: args.MerchantID,
        Amount:     amount,
        Status:     "pending",
        PayChannel: args.PayChannel,
        OutTradeNo: args.OutTradeNo,
    })
    if err != nil {
        // 23505 on idx_payments_active_out_trade_no_channel → concurrent
        // insert raced and won; surface as a domain conflict.
        var pgErr *pgconn.PgError
        if errors.As(err, &pgErr) && pgErr.Code == "23505" {
            return nil, biz.ErrPaymentConflict
        }
        return nil, err
    }
    return toBizPaymentDO(payment), nil
}
```

**`GetActivePaymentByOrderChannel`:**
```go
func (r *PaymentRepo) GetActivePaymentByOrderChannel(ctx context.Context, orderID int64, channel string) (*biz.PaymentDO, error) {
    payment, err := querierFromContext(ctx, r.q).GetActivePaymentByOrderChannel(ctx, db.GetActivePaymentByOrderChannelParams{
        OrderID:    orderID,
        PayChannel: channel,
    })
    if err != nil {
        return nil, err
    }
    return toBizPaymentDO(payment), nil
}
```

**`toBizPaymentDO` — copy `OutTradeNo`:**
```go
d := &biz.PaymentDO{
    ...
    OutTradeNo: p.OutTradeNo,
    ...
}
```

**`PaymentSyncRepo.applySuccess` — no change required.** The `out_trade_no` is set at create time and never mutated. The existing `UpdatePaymentSuccess` continues to set only `third_party_tx_id`.

### B3. wire regeneration

`NewPaymentUsecase` signature changes (added `idGen` arg). `wire ./cmd/mall` regenerates `wire_gen.go`. `NewSnowflakeIDGenerator` is already in `ProviderSet` (`data.go:30`) and the `biz.IDGenerator` interface is already bound to `*snowflakeGenerator` (`data.go:41`), so wire will auto-resolve.

### B4. service layer (`internal/service/payment.go`)

- `CreatePayment` proto signature unchanged for now (additive proto change in B5).
- `toProtoPaymentInfo` — add `OutTradeNo: p.OutTradeNo` to the proto response.
- No new error translation: `biz.ErrPaymentConflict` (built with `errors.Conflict`) already maps to gRPC `ALREADY_EXISTS` / HTTP 409.

### B5. proto change (`api/payment/v1/payment.proto`)

```diff
 message PaymentInfo {
   int64  id               = 1;
   int64  order_id         = 2;
   int64  user_id          = 3;
   int64  merchant_id      = 4;
   string amount           = 5;
   string status           = 6;
   string pay_channel      = 7;
   string third_party_tx_id = 8;
+  string out_trade_no     = 11;
   google.protobuf.Timestamp paid_at    = 9;
   google.protobuf.Timestamp created_at = 10;
 }

 message CreatePaymentRequest {
   int64  order_id    = 1;
   int64  user_id     = 2;
   int64  merchant_id = 3;
   string pay_channel = 4; // wechat/alipay
+  string out_trade_no = 5; // optional; server generates a snowflake id if empty
 }
```

`make api` (or `kratos proto client`) regenerates `payment.pb.go` and `payment_http.pb.go`.

### B6. Tests

| File | Test | Asserts |
|---|---|---|
| `internal/biz/payment_test.go` (new) | `TestCreatePayment_GeneratesOutTradeNo` | No existing payment; `idGen.GenerateString()` is called once and passed to `CreatePayment`. |
| | `TestCreatePayment_ReusesExistingActive` | Repo returns existing pending payment → usecase returns it, no `idGen` call, no second repo create. |
| | `TestCreatePayment_AllowsRetryAfterFailed` | Repo returns `ErrNoRows` for active lookup; new payment is created. |
| | `TestCreatePayment_HonoursClientOutTradeNo` | `CreatePaymentArgs.OutTradeNo = "client-123"` → repo receives that exact value. |
| | `TestValidateOutTradeNo_*` | Table-driven: empty / too long / bad chars / valid edge cases. |
| `internal/data/payment_test.go` (extend) | `TestCreatePayment_DBUniqueConflict` | Mock Querier returns `pgconn.PgError{Code:"23505"}` → method returns `biz.ErrPaymentConflict`. |

### B7. Verify Phase B

- `go build ./...`
- `go test ./...` — all green
- `go vet ./...` clean
- Manual (requires DB): `CreatePayment` twice for the same `(order_id, "wechat")` returns the same `id`; once for a different channel creates a new row.

---

## Phase C — River dedup (minimal, documented)

### C1. The problem, in code
`PaymentMQRepo.EnqueueCheckPay` (`data/payment.go:316-339`) uses:
```go
UniqueOpts: river.UniqueOpts{
    ByArgs:  true,
    ByQueue: true,
}
```
River hashes the **whole `CheckPayArgs` struct** (including `MaxPolls`, `PollIntervalSeconds`, `Source`). A re-enqueue with different polling settings is *not* deduped → duplicate jobs.

### C2. Decision
Per decision #4, keep `ByArgs: true` forever. River's `UniqueOpts` has no public surface to subset the hash. The data-layer uniqueness added in Phase A now catches the bad outcome: a duplicate job's `UpdatePaymentSuccess` will either be a no-op or hit 23505 on the new unique index. The job still runs, but its writes are rejected cleanly.

### C3. Action
Add a `// TODO` comment in `PaymentMQRepo.EnqueueCheckPay` documenting the limitation and pointing to the data-layer safety net. No code change. No tests required.

```go
// TODO: River's UniqueOpts.ByArgs hashes the entire CheckPayArgs struct,
// so re-enqueues with different MaxPolls/PollIntervalSeconds are NOT deduped.
// A duplicate job still runs, but its writes are now caught by
// idx_payments_active_out_trade_no_channel and idx_payments_third_party_tx_id_channel
// (added in migration 000007). For strict dedup across polling changes,
// switch to a narrower river.Job[CheckPayJobKey] type (Phase C2 — deferred).
```

### C4. Verify Phase C
- `go test ./...` — green
- No code changes; comment-only.

---

## Summary of Files Touched

**Phase A (schema + sqlc):**
- `app/mall/db/migrations/000007_payments_out_trade_no.up.sql` (new)
- `app/mall/db/migrations/000007_payments_out_trade_no.down.sql` (new)
- `app/mall/db/query/payments.sql` (append 2 queries)
- `app/mall/internal/data/db/*.sql.go` (regen)
- `app/mall/internal/data/db/models.go` (regen — `Payment.OutTradeNo` field)
- `app/mall/internal/data/db/mock/querier_mock.go` (regen)

**Phase B (idempotent create):**
- `app/mall/internal/biz/payment.go` — `CreatePaymentArgs.OutTradeNo`, `PaymentDO.OutTradeNo`, `IDGenerator` injection, `resolveOutTradeNo`, `validateOutTradeNo`, `ErrPaymentConflict`
- `app/mall/internal/biz/payment_test.go` (new) — 5 test cases
- `app/mall/internal/data/payment.go` — `CreatePayment` uses new sqlc query + 23505 mapping; new `GetActivePaymentByOrderChannel`; `toBizPaymentDO` copies `OutTradeNo`
- `app/mall/internal/data/payment_test.go` (extend) — 1 test
- `app/mall/internal/service/payment.go` — `toProtoPaymentInfo` adds `OutTradeNo`
- `app/mall/api/payment/v1/payment.proto` — add `out_trade_no` to `PaymentInfo` (field 11) and `CreatePaymentRequest` (field 5)
- `app/mall/api/payment/v1/payment.pb.go` (regen)
- `app/mall/cmd/mall/wire_gen.go` (regen)

**Phase C (River dedup — minimal):**
- `app/mall/internal/data/payment.go` — `// TODO` comment in `EnqueueCheckPay`

**Total: ~12 files** across 3 phases, additive proto changes only.

---

## Execution Order

1. **Phase A first.** Schema + sqlc + mock regen. No behavior change. Green build/test expected.
2. **Phase B second.** Biz + data + service + proto. Behavior change: idempotent create, server-generated `out_trade_no`, 23505 → `ErrPaymentConflict` translation, `out_trade_no` returned in `PaymentInfo`.
3. **Phase C last.** Comment-only. Marks the known River dedup limitation.

Each phase ends with `go build ./... && go test ./...` green before moving to the next.
