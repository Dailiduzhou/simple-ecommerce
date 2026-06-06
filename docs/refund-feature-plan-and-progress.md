# Refund Feature Plan and Current Progress

Date: 2026-06-06

## Context

This document records the current implementation plan and actual progress for adding refund support to the Go Kratos mall service.

The work was requested after checking whether the project already had refund functionality. The answer is: the project currently has only a minimal refund API stub, not a real refund flow.

## Current Findings

### Existing Refund-Related Code

The project already has a `RefundPayment` RPC in `api/payment/v1/payment.proto`:

```proto
rpc RefundPayment (RefundPaymentRequest) returns (RefundPaymentReply) {
  option (google.api.http) = {
    post: "/v1/payments/{id}/refund"
    body: "*"
  };
}
```

Current request and response messages are minimal:

```proto
message RefundPaymentRequest {
  int64 id = 1;
}
message RefundPaymentReply {}
```

The current service implementation is an empty stub:

```go
func (s *PaymentService) RefundPayment(ctx context.Context, req *pb.RefundPaymentRequest) (*pb.RefundPaymentReply, error) {
	return &pb.RefundPaymentReply{}, nil
}
```

The payment sync code can mark a payment as `refunded` when WeChat order query returns `TradeState_REFUND`, but this is not a refund-request workflow. It does not create a refund order, call the WeChat refund API, enqueue MQ jobs, or track refund status.

### Missing Pieces

The following pieces do not exist yet:

- No `order_refunds` table.
- No `OrderRefund` model in sqlc-generated DB models.
- No refund SQL query file.
- No refund repo interface in `biz`.
- No refund data repo implementation.
- No refund usecase.
- No WeChat refund methods on `WechatPayProvider`.
- No River worker for initiating refunds.
- No River worker for querying refund status.
- No DI registration for refund repo/usecase/workers.
- No service implementation for creating refund records and enqueueing refund jobs.
- No refund tests.

### Existing Related Infrastructure

The project already has a working River setup for WeChat payment status checks:

- `CheckWechatPayArgs`
- `PaymentMQRepo`
- `PaymentSyncRepo`
- `PaymentJobUsecase`
- `CheckWechatPayWorker`
- `RiverServer`
- `job.ProviderSet`
- River client setup in `data.NewRiverClient`

The refund flow should reuse this same layering and DI style.

## Current Actual Progress

Only the refund migration files have been created so far.

Created:

- `app/mall/db/migrations/000006_create_order_refunds.up.sql`
- `app/mall/db/migrations/000006_create_order_refunds.down.sql`

The current up migration creates:

```sql
CREATE TABLE order_refunds (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  out_refund_no VARCHAR(64) NOT NULL,
  total_amount INTEGER NOT NULL,
  refund_amount INTEGER NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  status VARCHAR(20) NOT NULL DEFAULT 'pending',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

  CONSTRAINT fk_order_refund_order
  FOREIGN KEY (order_id) REFERENCES orders (id),
  CONSTRAINT fk_order_refund_user
  FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE UNIQUE INDEX idx_order_refunds_out_refund_no ON order_refunds(out_refund_no);
CREATE INDEX idx_order_refunds_order_id ON order_refunds(order_id);
CREATE INDEX idx_order_refunds_user_id ON order_refunds(user_id);
CREATE INDEX idx_order_refunds_status ON order_refunds(status);
```

The current down migration is:

```sql
DROP TABLE IF EXISTS order_refunds;
```

No sqlc generation has been run after adding these migration files.

No refund Go code has been added yet.

No tests have been added or run for refund.

## Proposed Database Model

The refund table should map to the requested Go model:

```go
type OrderRefund struct {
	ID           int64
	OrderID      int64
	UserID       int64
	OutRefundNo  string
	TotalAmount  int32
	RefundAmount int32
	Reason       string
	Status       string
	CreatedAt    pgtype.Timestamptz
	UpdatedAt    pgtype.Timestamptz
}
```

Status values:

- `pending`: local refund record was created, but the WeChat refund API has not been called yet.
- `processing`: WeChat accepted the refund request and is processing it asynchronously.
- `success`: refund succeeded.
- `failed`: refund failed, was closed, or became abnormal.

## Planned API Shape

The existing `RefundPaymentRequest` only has `id`. The current route is:

```http
POST /v1/payments/{id}/refund
```

The recommended interpretation is:

- `id` means `payment_id`.
- If no refund amount is provided, default to full refundable amount.
- Add request fields for actual refund creation:

```proto
message RefundPaymentRequest {
  int64 id = 1;                  // payment_id, preserved for existing route
  int32 refund_amount = 2;        // cents; if 0, full refundable amount
  string reason = 3;              // refund reason
}
```

Recommended response:

```proto
message RefundPaymentReply {
  int64 refund_id = 1;
  int64 job_id = 2;
  string out_refund_no = 3;
  string status = 4;
}
```

This requires regenerating:

- `api/payment/v1/payment.pb.go`
- `api/payment/v1/payment_grpc.pb.go`
- `api/payment/v1/payment_http.pb.go`
- `openapi.yaml`

## Planned Biz Layer

Add WeChat refund DTOs to `app/mall/internal/biz/payment.go`:

```go
type WechatRefundReq struct {
	OutTradeNo   string
	OutRefundNo  string
	Reason       string
	TotalAmount  int32
	RefundAmount int32
}

type WechatRefundDTO struct {
	OutRefundNo string
	RefundID    string
	Status      string
}
```

Extend `WechatPayProvider`:

```go
type WechatPayProvider interface {
	PrepayJSAPI(ctx context.Context, req *pb.PrepayJSAPIRequest) (*pb.PrepayJSAPIReply, error)
	QueryOrder(ctx context.Context, outTradeNo string) (*pb.QueryOrderReply, error)
	CloseOrder(ctx context.Context, outTradeNo string) (*pb.CloseOrderReply, error)
	Refund(ctx context.Context, req WechatRefundReq) (*WechatRefundDTO, error)
	QueryRefund(ctx context.Context, outRefundNo string) (*WechatRefundDTO, error)
}
```

Add refund repo/usecase concepts:

```go
type RefundRepo interface {
	CreateRefund(ctx context.Context, paymentID int64, refundAmount int32, reason string) (*OrderRefund, error)
	GetRefund(ctx context.Context, refundID int64) (*OrderRefund, error)
	ListRefundsByOrder(ctx context.Context, orderID int64) ([]OrderRefund, error)
	UpdateRefundStatus(ctx context.Context, refundID int64, status string) error
	ApplyRefundResult(ctx context.Context, refundID int64, result *WechatRefundDTO) error
}
```

Add refund MQ methods to the existing MQ repo or create a dedicated refund MQ repo:

```go
type RefundMQRepo interface {
	EnqueueProcessRefund(ctx context.Context, args ProcessRefundArgs, scheduledAt time.Time) (*MQJob, error)
	EnqueueCheckRefund(ctx context.Context, args CheckRefundArgs, scheduledAt time.Time) (*MQJob, error)
}
```

Recommended usecase:

```go
type RefundUsecase struct {
	refundRepo RefundRepo
	mqRepo     RefundMQRepo
	log        *log.Helper
}
```

Expected service-level flow:

1. Validate `payment_id`.
2. Validate refund amount if provided.
3. Create local `order_refunds` record in `pending` state.
4. Enqueue `process_refund` River job.
5. Return refund info and job id.

## Planned Data Layer

Add `app/mall/db/query/refunds.sql`.

Expected sqlc queries:

```sql
-- name: CreateOrderRefund :one
INSERT INTO order_refunds (
  order_id,
  user_id,
  out_refund_no,
  total_amount,
  refund_amount,
  reason,
  status
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetOrderRefund :one
SELECT * FROM order_refunds WHERE id = $1;

-- name: GetOrderRefundByOutRefundNo :one
SELECT * FROM order_refunds WHERE out_refund_no = $1;

-- name: ListOrderRefundsByOrder :many
SELECT * FROM order_refunds WHERE order_id = $1 ORDER BY id DESC;

-- name: SumSuccessfulRefundAmountByOrder :one
SELECT COALESCE(SUM(refund_amount), 0)::int
FROM order_refunds
WHERE order_id = $1 AND status = 'success';

-- name: UpdateOrderRefundStatus :exec
UPDATE order_refunds
SET status = $2, updated_at = CURRENT_TIMESTAMP
WHERE id = $1;
```

Then run sqlc generation from `app/mall`:

```bash
sqlc generate
```

This should update:

- `app/mall/internal/data/db/models.go`
- `app/mall/internal/data/db/querier.go`
- a new generated `app/mall/internal/data/db/refunds.sql.go`

The existing mock file may also need regeneration if tests use `db.Querier` mocks:

- `app/mall/internal/data/db/mock/querier_mock.go`

## Planned River Job Args

Add two refund job args, probably in `app/mall/internal/biz/payment.go` or a dedicated refund biz file so the args can be shared by data and job layers:

```go
const (
	ProcessRefundJobKind = "process_refund"
	CheckRefundJobKind   = "check_refund_status"
)

type ProcessRefundArgs struct {
	RefundID int64 `json:"refund_id" river:"unique"`
}

func (ProcessRefundArgs) Kind() string {
	return ProcessRefundJobKind
}

type CheckRefundArgs struct {
	RefundID int64 `json:"refund_id" river:"unique"`
}

func (CheckRefundArgs) Kind() string {
	return CheckRefundJobKind
}
```

Note: the user-provided example placed these args under `package job`, but the current project already defines shared River args in `biz` for payment checks. Keeping refund args in `biz` is more consistent with the current codebase.

## Planned Workers

### ProcessRefundWorker

Purpose:

- Load local refund.
- Skip if it is not `pending`.
- Call WeChat refund API.
- Update local refund status based on WeChat response.
- If status is `processing`, enqueue delayed `check_refund_status`.

Expected status mapping:

- WeChat `SUCCESS` -> local `success`
- WeChat `PROCESSING` -> local `processing`
- WeChat `ABNORMAL` -> local `failed`
- WeChat `CLOSED` -> local `failed`
- Unknown status -> local `processing`

Expected delayed fallback:

```go
ScheduledAt: time.Now().Add(5 * time.Minute)
```

### CheckRefundWorker

Purpose:

- Load local refund.
- Skip if already terminal: `success` or `failed`.
- Call WeChat refund query API by `out_refund_no`.
- Update local refund status from query result.
- If still `processing`, return an error or use River retry scheduling so River will check again later.

Recommended retry interval:

- 5 minutes for refund status checks.

## Planned Service Layer

Extend `PaymentService` constructor:

```go
func NewPaymentService(
	wechatPay biz.WechatPayProvider,
	paymentJobs *biz.PaymentJobUsecase,
	refunds *biz.RefundUsecase,
) *PaymentService
```

Add a `refunds` field:

```go
refunds *biz.RefundUsecase
```

Implement `RefundPayment`:

1. Return `503 PAYMENT_REFUND_NOT_CONFIGURED` if the refund usecase is nil.
2. Validate `req.Id > 0`.
3. Call `refunds.CreateRefund(ctx, req.Id, req.RefundAmount, req.Reason)`.
4. Convert result to `RefundPaymentReply`.

## Planned DI Changes

Update provider sets:

### Biz

Add:

- `NewRefundUsecase`

### Data

Add:

- `NewRefundRepo`
- bind `biz.RefundRepo`
- possibly bind `biz.RefundMQRepo` if it is separate from `PaymentMQRepo`

### Job

Add:

- `NewProcessRefundWorker`
- `NewCheckRefundWorker`
- register both workers in `NewWorkers`

### Wire

Regenerate Wire after changing provider sets:

```bash
env GOCACHE=/tmp/simple-ecommerce-gocache go run -mod=mod github.com/google/wire/cmd/wire ./app/mall/cmd/mall
```

Expected generated changes:

- `app/mall/cmd/mall/wire_gen.go`

## Planned Tests

### Biz Tests

Add refund usecase tests:

- Reject missing payment id.
- Reject negative refund amount.
- Default refund amount to full refundable amount when request amount is zero.
- Create refund record and enqueue `process_refund`.
- Propagate repo errors.

### Data Tests

Add data repo tests if practical:

- Create refund from successful payment and paid order.
- Reject refund when payment does not exist.
- Reject refund when payment is not `success`.
- Reject refund amount greater than remaining refundable amount.
- Generate unique `out_refund_no`.
- Update refund status.

Given existing data tests use gomock around `db.Querier`, sqlc mock regeneration may be needed.

### Job Tests

Add worker tests similar to the existing `CheckWechatPayWorker` tests:

- `ProcessRefundWorker` skips non-pending refund.
- `ProcessRefundWorker` calls WeChat refund for pending refund.
- `ProcessRefundWorker` maps `SUCCESS` to local `success`.
- `ProcessRefundWorker` maps `PROCESSING` to local `processing` and schedules `check_refund_status`.
- `ProcessRefundWorker` returns error on WeChat API failure so River retries.
- `CheckRefundWorker` skips terminal refunds.
- `CheckRefundWorker` maps query results to local statuses.
- `CheckRefundWorker` retries or schedules next check when still processing.
- Both workers cancel invalid args with `river.JobCancel`.

### Service Tests

Extend `app/mall/internal/service/payment_test.go`:

- `RefundPayment` delegates to refund usecase.
- Missing refund usecase returns service unavailable.
- Invalid payment id returns bad request.
- Response contains refund id, job id, out refund no, and status.

### Full Verification

Run focused tests first:

```bash
env GOCACHE=/tmp/simple-ecommerce-gocache go test ./app/mall/internal/biz ./app/mall/internal/data ./app/mall/internal/job ./app/mall/internal/service
```

Run full tests after that:

```bash
env GOCACHE=/tmp/simple-ecommerce-gocache go test ./...
```

Note: `app/mall/internal/data` tests use `miniredis`, which needs local TCP sockets. In the managed sandbox, this can fail with:

```text
listen tcp 127.0.0.1:0: socket: operation not permitted
```

If that happens, rerun tests outside the sandbox with approval.

## Open Implementation Decisions

### Whether to Store WeChat Refund ID

The requested `OrderRefund` struct does not include a WeChat-side `refund_id`.

The provided DTO includes:

```go
RefundID string
```

Options:

1. Keep the database exactly as requested and do not persist WeChat refund ID.
2. Add an optional column such as `third_party_refund_id VARCHAR(128)` to `order_refunds`.

Option 1 follows the requested schema strictly.

Option 2 is more useful operationally because it preserves the WeChat refund identifier for reconciliation.

### Whether `RefundPaymentRequest.id` Means Payment ID or Order ID

Current route:

```http
POST /v1/payments/{id}/refund
```

Recommended interpretation:

- `id` is `payment_id`.

This matches the existing route path and service name.

### Whether Refund Should Update Payment and Order Immediately

Recommended behavior:

- When refund succeeds, update `order_refunds.status = 'success'`.
- If the successful refund amount reaches the full payment/order amount, update `payments.status = 'refunded'`.
- Do not automatically cancel an already completed order unless the product/business requirement says so.

The current project only has simple payment status values, so full-refund detection is enough for now.

## Suggested Next Step

Continue from the current two migration files with this order:

1. Add `app/mall/db/query/refunds.sql`.
2. Run `sqlc generate`.
3. Extend `WechatPayProvider`.
4. Add refund biz structs, interfaces, usecase, and River args.
5. Add refund data repo and MQ enqueue methods.
6. Add `ProcessRefundWorker` and `CheckRefundWorker`.
7. Register workers in `job.ProviderSet` and `NewWorkers`.
8. Update `PaymentService.RefundPayment`.
9. Update proto request/reply and regenerate proto code.
10. Regenerate Wire.
11. Add tests.
12. Run focused and full test suites.
