# 订单与支付正确性加固实施计划

> 历史实施计划。文中的拟议步骤和文件行号可能已过期；当前实现与限制请查阅 [订单与支付实现状态](payment-feature-implementation-status.md) 和 [业务时序](payment-business-flow.md)。

## 1. 文档状态

- 状态：待实施
- 适用服务：`app/mall`
- 技术栈：Go Kratos、PostgreSQL、sqlc、River、Redis
- 范围：创建订单、库存预占、创建支付、支付关闭、支付回调、主动查询、订单超时、状态流转和幂等

## 2. 锁定前提与决策

本计划基于以下已确认前提：

1. 当前没有需要保留的历史业务数据。
2. 当前没有需要兼容的前端或外部客户端。
3. 数据库结构直接修改 `app/mall/db/migrations/000001_init_schema.up.sql`，不新增兼容迁移，不做数据回填。
4. API 可以直接进行不兼容调整，生成代码和调用方测试同步更新。
5. 数据库约束是并发正确性的最终保障，Redis 锁和进程内锁不承担资金、库存一致性职责。
6. 第三方支付调用不能放进数据库事务；使用本地支付流水、租约和固定 `out_trade_no` 处理故障恢复。
7. 支付渠道状态和对账状态分开保存，不再用 `reconcile_required` 覆盖真实支付状态。
8. 同一订单任意时刻最多允许一笔活跃支付，不区分支付渠道或支付产品。

由于没有历史数据和兼容要求，实施时同时修改：

- `000001_init_schema.up.sql`
- sqlc 查询及生成代码
- Proto 及生成代码
- biz/data/service/job 分层
- Wire 依赖注入
- 单元测试和 PostgreSQL + River 集成测试

## 3. 必须长期成立的业务不变量

1. 同一个用户、同一个订单幂等键最多创建一个订单，重复请求不得重复扣库存。
2. 订单金额、币种和商品快照只从数据库读取，客户端不能指定可信支付金额。
3. 创建订单、写订单明细、扣库存和插入订单超时 Job 必须在同一 PostgreSQL 事务提交。
4. 同一订单最多存在一笔 `creating`、`pending` 或 `close_pending` 支付。
5. 所有支付终态处理统一按“先锁订单、再锁支付”的顺序执行。
6. 同一订单只能有一笔正常支付完成订单；晚到或重复的第二笔成功必须进入待退款/对账流程。
7. 关闭一笔支付前必须检查同订单的其他支付，不能因为任意一笔支付关闭就直接释放库存。
8. 只有订单从 `pending_payment` 成功 CAS 到 `cancelling` 后才能恢复库存。
9. 支付通知只有在通知记录和 River Job 同事务持久化成功后才能向渠道返回成功。
10. 支付渠道确认的事实状态不能被“需要对账”覆盖；对账使用独立字段表达。
11. River Job 必须至少一次安全：重复执行不能重复扣库存、重复回库存或重复推进终态。
12. 数据库事务中不得调用微信、支付宝或其他第三方网络接口。

## 4. 目标状态机

### 4.1 订单状态

移除当前未实际使用的 `creating` 订单状态，目标状态机为：

```text
pending_payment ──支付成功──> paid ──发货──> shipped ──确认完成──> completed
       │
       └──取消/超时且支付关闭──> cancelling ──恢复库存──> cancelled
```

约束：

- `paid`、`shipped`、`completed` 不允许回到待支付或取消状态。
- `cancelled` 不允许重新变成已支付；取消后的晚到成功进入支付对账/退款。
- `is_completed = true` 只允许用于 `completed` 和 `cancelled`。

### 4.2 支付事实状态

目标支付状态：

```text
creating ──预支付成功──> pending ──渠道成功──> success ──退款成功──> refunded
    │                      │
    └──确定失败──> failed  ├──关单请求──> close_pending ──关单成功──> closed
                           └──确定失败──> failed
```

状态含义：

- `creating`：已创建本地支付号，尚未可靠保存前端支付动作。
- `pending`：渠道预支付成功，等待用户支付或渠道最终结果。
- `failed`：渠道明确确认支付失败，允许订单重新创建支付。
- `close_pending`：已要求关闭，正在等待渠道确认。
- `closed`：渠道确认已关闭。
- `success`：渠道确认收款成功。
- `refunded`：渠道确认退款完成。

### 4.3 对账状态

在 `payments` 中增加独立字段：

```text
none -> required -> processing -> resolved
```

典型 `reconciliation_reason`：

- `amount_mismatch`
- `currency_mismatch`
- `provider_mismatch`
- `duplicate_success`
- `late_success_after_cancel`
- `unknown_provider_state`
- `close_failed`
- `job_exhausted`

支付事实可以是 `success`，同时 `reconciliation_status = required`。这能准确表达“钱已收到，但需要退款或人工处理”。

## 5. 直接修改初始化数据库结构

修改：

- `app/mall/db/migrations/000001_init_schema.up.sql`
- `app/mall/db/migrations/000001_init_schema.down.sql`

`down.sql` 当前按表删除，通常只需确认新增表也按外键依赖顺序删除。

### 5.1 orders

目标字段：

```sql
CREATE TABLE orders (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id BIGINT NOT NULL,
  address_id BIGINT NOT NULL,
  total_amount_minor BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  status VARCHAR(20) NOT NULL DEFAULT 'pending_payment',
  is_completed BOOLEAN NOT NULL DEFAULT FALSE,
  out_trade_no VARCHAR(64) NOT NULL,
  idempotency_key VARCHAR(64) NOT NULL,
  request_hash VARCHAR(64) NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT orders_amount_check CHECK (total_amount_minor > 0),
  CONSTRAINT orders_status_check CHECK (
    status IN (
      'pending_payment', 'paid', 'shipped', 'completed',
      'cancelling', 'cancelled'
    )
  ),
  CONSTRAINT orders_completion_check CHECK (
    is_completed = (status IN ('completed', 'cancelled'))
  )
);
```

索引：

```sql
CREATE UNIQUE INDEX idx_orders_out_trade_no
  ON orders(out_trade_no);

CREATE UNIQUE INDEX idx_orders_user_idempotency
  ON orders(user_id, idempotency_key);

CREATE INDEX idx_orders_pending_expiry
  ON orders(expires_at)
  WHERE status = 'pending_payment';
```

同时给 `order_items` 增加数据库检查：

```sql
CONSTRAINT order_items_quantity_check CHECK (quantity > 0),
CONSTRAINT order_items_price_check CHECK (unit_price_minor >= 0)
```

### 5.2 payments

移除 `reconcile_required` 事实状态，增加失败、对账和预支付租约字段：

```sql
CREATE TABLE payments (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  merchant_id BIGINT NOT NULL,
  amount_minor BIGINT NOT NULL,
  currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
  status VARCHAR(20) NOT NULL DEFAULT 'creating',
  pay_channel VARCHAR(30) NOT NULL,
  third_party_tx_id VARCHAR(128),
  out_trade_no VARCHAR(64) NOT NULL,
  action_type VARCHAR(20),
  action_payload JSONB,
  paid_at TIMESTAMPTZ,
  reconciliation_status VARCHAR(20) NOT NULL DEFAULT 'none',
  reconciliation_reason VARCHAR(64),
  reconciliation_detail TEXT,
  prepay_lease_token VARCHAR(64),
  prepay_lease_until TIMESTAMPTZ,
  prepay_attempts INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT payments_amount_check CHECK (amount_minor > 0),
  CONSTRAINT payments_status_check CHECK (
    status IN (
      'creating', 'pending', 'failed', 'close_pending',
      'closed', 'success', 'refunded'
    )
  ),
  CONSTRAINT payments_reconciliation_status_check CHECK (
    reconciliation_status IN ('none', 'required', 'processing', 'resolved')
  )
);
```

索引：

```sql
CREATE UNIQUE INDEX idx_payments_out_trade_no
  ON payments(out_trade_no);

CREATE UNIQUE INDEX idx_payments_third_party_tx_id_channel
  ON payments(third_party_tx_id, pay_channel)
  WHERE third_party_tx_id IS NOT NULL;

CREATE UNIQUE INDEX idx_payments_one_active_per_order
  ON payments(order_id)
  WHERE status IN ('creating', 'pending', 'close_pending');

CREATE INDEX idx_payments_reconciliation
  ON payments(reconciliation_status, updated_at)
  WHERE reconciliation_status <> 'none';
```

不建立“每个订单只能有一条 success”的唯一索引。系统必须能够如实记录渠道实际发生的第二笔成功，再通过 `duplicate_success` 推进退款；不能因为唯一约束而丢失资金事实。

### 5.3 payment_notifications

保留现有通知唯一约束和状态机：

```text
received -> processing -> processed
                    \-> failed -> processing
```

继续保留：

- `(provider, provider_event_id)` 部分唯一索引
- `(provider, out_trade_no, payload_hash)` 唯一索引
- 通知记录与 River Job 同事务写入

### 5.4 payment_reconciliation_failures

保留失败明细表，但增加：

```sql
reason VARCHAR(64) NOT NULL
```

支付表保存当前对账状态，失败表保存每次发生的详细事件和 River Job 信息。

## 6. API 修改

### 6.1 创建订单

修改 `api/order/v1/order.proto`：

```protobuf
message CreateOrderRequest {
  int64 address_id = 1;
  repeated OrderItemInput items = 2;
  string idempotency_key = 3;
}
```

要求：

- `idempotency_key` 必填，长度 8～64。
- 同一次用户下单操作的所有重试必须使用相同 key。
- 不提供兼容默认值。

规范化请求摘要：

```text
user_id
address_id
按 product_id 升序排列后的 product_id + quantity
```

对规范化 JSON 计算 SHA-256，保存为 `request_hash`。同 key、同 hash 返回已有订单；同 key、不同 hash 返回 `IDEMPOTENCY_KEY_CONFLICT`。

### 6.2 创建支付

修改 `api/payment/v1/payment.proto`：

- 删除已废弃的客户端金额字段，不再保留兼容字段。
- 支付金额和币种始终由订单决定。
- 创建支付只允许在订单 `pending_payment` 时执行。
- 如果订单已有其他方式的活跃支付，返回 `ORDER_HAS_ACTIVE_PAYMENT`。
- 切换支付方式必须先调用现有关闭支付 API，等待旧支付进入 `closed`，再创建新支付。

建议创建支付响应同时返回：

```protobuf
int64 payment_id = 1;
string out_trade_no = 2;
string action_type = 3;
bytes payload = 4;
```

### 6.3 错误码

新增或统一：

```text
IDEMPOTENCY_KEY_REQUIRED
IDEMPOTENCY_KEY_INVALID
IDEMPOTENCY_KEY_CONFLICT
ORDER_HAS_ACTIVE_PAYMENT
ORDER_ALREADY_PAID
ORDER_EXPIRED
PAYMENT_PREPAY_IN_PROGRESS
PAYMENT_RECONCILIATION_REQUIRED
PAYMENT_STATE_CONFLICT
```

修改 Proto 后执行项目既有代码生成流程，提交生成的 Go、HTTP、gRPC 和 OpenAPI 文件。

## 7. 创建订单改造

涉及：

- `app/mall/internal/service/order.go`
- `app/mall/internal/biz/order.go`
- `app/mall/internal/data/order.go`
- `app/mall/db/query/orders.sql`
- `app/mall/db/query/products.sql`
- `app/mall/db/query/order_items.sql`

### 7.1 service

- 从认证 claims 获取 `user_id`。
- 校验 `idempotency_key` 格式。
- 不计算金额、不读取商品、不实现幂等逻辑。

### 7.2 biz

`CreateOrderReq` 增加 `IdempotencyKey`。

biz 层负责：

- 规范化商品列表；
- 拒绝重复商品；
- 按 `product_id` 升序排序，统一数据库加锁顺序；
- 计算 `request_hash`；
- 设置订单过期时间，例如当前时间后 30 分钟。

过期时长放入配置，不在业务代码散落常量。

### 7.3 data transaction

目标流程：

```text
开始事务
  -> 按 user_id + idempotency_key 查询订单
       -> hash 相同：返回已有订单
       -> hash 不同：返回 IDEMPOTENCY_KEY_CONFLICT
  -> 校验地址归属
  -> 按 product_id 升序 FOR UPDATE 锁商品
  -> 使用数据库价格计算订单金额
  -> INSERT orders
  -> 条件扣减库存
  -> INSERT order_items 快照
  -> River InsertTx(expire_order, scheduled_at = expires_at)
提交
  -> 失效订单和商品缓存
```

并发请求可能同时在事务开始时查不到订单，因此 `INSERT orders` 必须依赖命名唯一约束 `idx_orders_user_idempotency` 兜底。

捕获该约束的 `23505` 后，在事务外重新查询：

- hash 相同：返回已有订单；
- hash 不同：返回幂等冲突。

创建订单必须发生在库存扣减之前。这样唯一约束冲突会在扣库存前结束失败事务。

## 8. 单订单单活跃支付

涉及：

- `app/mall/internal/biz/payment.go`
- `app/mall/internal/data/payment.go`
- `app/mall/db/query/payments.sql`

创建支付事务：

```text
锁订单
  -> 非 pending_payment：拒绝
锁该订单全部支付记录
  -> 存在 success/refunded：ORDER_ALREADY_PAID
  -> 存在 reconciliation_status=required：PAYMENT_RECONCILIATION_REQUIRED
  -> 存在 creating/pending/close_pending：
       -> 同支付方式：返回该支付
       -> 不同支付方式：ORDER_HAS_ACTIVE_PAYMENT
  -> 只有 failed/closed：创建新的 creating payment
提交
```

`idx_payments_one_active_per_order` 是最终并发保障。不能继续只按 `(order_id, pay_channel)` 防重。

## 9. 预支付租约

### 9.1 抢占

新增 sqlc 查询：

```sql
-- name: ClaimPaymentPrepay :one
UPDATE payments
SET prepay_lease_token = $2,
    prepay_lease_until = CURRENT_TIMESTAMP + make_interval(secs => $3),
    prepay_attempts = prepay_attempts + 1,
    last_error = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE id = $1
  AND status = 'creating'
  AND (
    prepay_lease_until IS NULL
    OR prepay_lease_until < CURRENT_TIMESTAMP
  )
RETURNING *;
```

只有抢占成功者调用 `gateway.Prepay`。

状态处理：

```text
creating + 抢占成功：调用渠道
creating + lease 有效：PAYMENT_PREPAY_IN_PROGRESS
pending + action 非空：返回数据库 action
failed/close_pending/closed/success/refunded：调用渠道前拒绝
```

### 9.2 完成

渠道返回成功后，在一个短事务中：

```text
校验 payment_id + lease_token + status=creating
  -> creating -> pending
  -> 保存 action
  -> 清空 lease
  -> 如果需要轮询，River InsertTx(check_pay)
提交
```

返回给调用方的 action 必须从成功落库后的 payment 读取，不能直接返回可能输掉 CAS 的本次渠道响应。

### 9.3 失败恢复

- 渠道明确失败：记录 `last_error`，清理 lease，支付进入 `failed`。
- 技术超时或进程崩溃：lease 到期后允许使用相同 `out_trade_no` 重试。
- adapter 必须保证重试使用同一商户支付号，并验证渠道对同一商户支付号的幂等语义。

## 10. 支付最终状态事务

统一锁顺序：

```text
GetOrderForUpdate
-> ListPaymentsByOrderForUpdate
-> 使用已锁定的目标 payment
```

禁止其他路径先锁 payment 再锁 order，避免死锁。

### 10.1 支付成功

正常路径：

```text
订单 pending_payment
目标支付 pending
无其他 success/refunded
  -> payment pending -> success
  -> order pending_payment -> paid
  -> notification -> processed
同一事务提交
```

幂等路径：

```text
当前 payment 已 success
transaction_id 相同
订单为 paid/shipped/completed
  -> 保持不变
  -> notification -> processed
```

重复支付：

```text
订单已由另一 payment 支付
渠道又确认当前 payment success
  -> 如实记录当前 payment success
  -> reconciliation_status = required
  -> reconciliation_reason = duplicate_success
  -> 创建 reconciliation failure
  -> 不重复修改订单
```

取消后晚到成功：

```text
订单 cancelling/cancelled
渠道确认 payment success
  -> 如实记录 payment success
  -> reconciliation_status = required
  -> reconciliation_reason = late_success_after_cancel
  -> 进入自动退款或人工退款
  -> 不重新扣库存，不把订单改回 paid
```

### 10.2 关闭支付

渠道确认当前支付关闭后：

```text
当前 payment -> closed
检查同订单其他支付
  -> 有 success/refunded：订单保持 paid
  -> 有 creating/pending/close_pending：订单保持 pending_payment
  -> 有 required 对账：不释放库存
  -> 没有其他有效支付且订单 pending_payment：
       pending_payment -> cancelling
       恢复库存
       cancelling -> cancelled
```

即使数据库唯一索引理论上禁止多个活跃支付，这一防御检查仍需保留，用于处理重复 Job、异常中断和未来代码回归。

### 10.3 PAYERROR

渠道明确返回 `PAYERROR`：

```text
payment pending/creating -> failed
order 保持 pending_payment
允许用户创建新支付
订单最终由 expire_order 控制库存释放
```

未知或无法确认的渠道状态：

```text
保持支付事实状态
reconciliation_status -> required
reconciliation_reason -> unknown_provider_state
```

## 11. River Job 拆分

不要继续让一个 `check_pay` Job 同时承担查询、关单和订单过期。

### 11.1 expire_order

```go
type ExpireOrderArgs struct {
    OrderID int64 `json:"order_id" river:"unique"`
}
```

- 创建订单事务中通过 `InsertTx` 插入。
- `ScheduledAt = order.ExpiresAt`。
- 活跃状态按 River available/pending/running/retryable/scheduled 去重。

执行：

```text
锁订单和支付
  -> 非 pending_payment：完成
  -> 无支付或全部 failed/closed：取消订单并恢复库存
  -> 有 creating/pending：标记 close_pending，InsertTx(close_pay)
  -> 有 close_pending：确认 close_pay 存在
  -> 有 success/refunded：修正/保持订单 paid
  -> 有 required 对账：记录告警并 snooze，不释放库存
```

### 11.2 check_pay

只负责：

- 预支付后的主动轮询；
- 支付通知持久化后的权威查询；
- 将渠道结果交给统一状态事务。

唯一参数至少包括：

```go
PaymentID      int64  `river:"unique"`
Provider       string `river:"unique"`
NotificationID int64 `river:"unique"`
```

### 11.3 close_pay

独立 Job：

```go
type ClosePayArgs struct {
    PaymentID int64  `json:"payment_id" river:"unique"`
    Provider  string `json:"provider" river:"unique"`
    Reason    string `json:"reason"`
}
```

执行：

1. 查询渠道当前状态。
2. 已成功：进入成功/重复成功处理。
3. 已关闭：应用关闭事务。
4. 仍待支付：调用渠道 Close。
5. Close 成功后再次应用关闭事务。
6. 技术错误交给 River 重试。
7. 最终耗尽时设置 `reconciliation_status=required`，不盲目释放库存。

拆分 Job kind 后，主动关单不会再被活跃的预支付轮询 Job 错误去重。

## 12. Kratos 分层与文件改动

### api

- `api/order/v1/order.proto`
- `api/payment/v1/payment.proto`
- 对应生成文件和 OpenAPI

只定义协议、字段约束和错误契约。

### service

- `app/mall/internal/service/order.go`
- `app/mall/internal/service/payment.go`

只负责 claims、DTO/DO 转换和基础输入校验，不直接编排 SQL、River 或 provider 分支。

### biz

- `app/mall/internal/biz/order.go`
- `app/mall/internal/biz/payment.go`

负责：

- 请求规范化和 request hash；
- 状态机规则；
- 错误码；
- Repo、MQ Repo 和 Gateway 接口；
- 订单过期、支付关闭的业务决策。

### data

- `app/mall/internal/data/order.go`
- `app/mall/internal/data/payment.go`
- 新增或拆分 order/payment MQ repo
- `app/mall/db/query/*.sql`
- `app/mall/internal/data/db/*` 生成文件

负责：

- PostgreSQL 事务；
- 行锁和 CAS；
- 约束冲突映射；
- River `InsertTx`；
- 提交后缓存失效；
- provider adapter。

### job

- `app/mall/internal/job/river.go`
- 新增 `order_expiry.go`
- 新增 `close_payment.go`
- `app/mall/internal/job/job.go`

注册三个独立 worker：

- `expire_order`
- `check_pay`
- `close_pay`

### wiring/config

- `app/mall/internal/conf/conf.proto`
- `app/mall/internal/data/data.go`
- `app/mall/cmd/mall/wire.go`
- `wire_gen.go`

配置项至少包含：

```text
order_payment_timeout
payment_prepay_lease_duration
payment_poll_interval
payment_poll_max_count
```

## 13. 测试计划

### 13.1 订单幂等

1. 16 个并发相同 key、相同请求，只创建一个订单。
2. 上述场景库存只扣一次，River 只生成一个过期 Job。
3. 相同 key、不同商品返回 `IDEMPOTENCY_KEY_CONFLICT`。
4. 第一次事务失败后，相同 key 可以重新成功创建。
5. 两个商品顺序相反的订单按统一顺序加锁，不产生应用层不确定行为。

### 13.2 单活跃支付

1. 不同支付方式并发创建，只产生一笔活跃支付。
2. 同方式重复创建返回同一 payment。
3. 已有 `pending` 时切换方式返回 `ORDER_HAS_ACTIVE_PAYMENT`。
4. 原支付 `closed/failed` 后允许创建新支付。
5. 存在 required 对账时禁止创建新支付。

### 13.3 预支付租约

1. 多个并发请求只调用一次 adapter `Prepay`。
2. lease 有效时返回 `PAYMENT_PREPAY_IN_PROGRESS`。
3. lease 到期后使用同一个 `out_trade_no` 恢复。
4. 输掉 finalize CAS 的请求不返回自己的未落库 action。
5. `pending/close_pending/success/closed` 不会再次调用 Prepay。

### 13.4 支付终态

1. 支付成功与关单并发时只有一个正常终态获胜。
2. 同一支付成功通知重复执行保持幂等。
3. 两笔支付均被渠道确认成功时，第二笔进入 `duplicate_success`。
4. 已取消订单晚到成功进入 `late_success_after_cancel`，库存不重新扣减。
5. 金额、币种、provider、out_trade_no 不匹配时订单不进入 paid。
6. 关闭一笔支付时存在另一活跃支付，不取消订单、不恢复库存。

### 13.5 订单超时

1. 无支付的超时订单取消并只恢复一次库存。
2. 重复执行 expire Job 不重复恢复库存。
3. 已支付订单的 expire Job 直接完成。
4. 有 pending 支付时插入 close Job，不立即恢复库存。
5. close Job 成功后订单取消并恢复库存。
6. close Job 耗尽时进入对账，不释放库存。

### 13.6 通知和 River

1. 相同 event ID 重复通知只产生一个通知记录和一个活跃 Job。
2. 相同 payload 重复通知幂等。
3. 不同通知可以独立触发权威查询。
4. 通知落库成功但 Job 插入失败时整个事务回滚，并向渠道返回失败。
5. `check_pay`、`close_pay`、`expire_order` 不会相互去重。
6. River 最终失败会写失败明细和对账状态。

## 14. 实施顺序

由于没有历史数据和客户端，不需要兼容发布，按以下顺序在一个功能分支完成：

1. 直接修改 `000001_init_schema.up.sql` 和 `down.sql`。
2. 修改订单、支付和配置 Proto。
3. 修改 sqlc query，重新生成 db 代码和 mocks。
4. 调整 biz 状态、接口、错误码和请求 hash。
5. 实现订单幂等和单活跃支付事务。
6. 实现预支付 lease。
7. 拆分并注册 `expire_order`、`check_pay`、`close_pay`。
8. 重写支付最终状态事务和对账状态。
9. 更新缓存失效、指标和结构化日志。
10. 完成单元测试和 PostgreSQL + River 集成测试。
11. 重新创建本地数据库，从 `000001` 全新初始化验证。
12. 执行完整生成、格式化、静态检查和测试。

## 15. 验收命令

按项目现有 Makefile 和工具链选择等价命令，至少完成：

```bash
make api
make config
sqlc generate
go generate ./...
gofmt -w ./app ./api
go vet ./...
go test ./...
```

数据库验收必须从空库执行 `000001_init_schema.up.sql`，不能只在已有开发库上执行测试。

## 16. 完成标准

满足以下全部条件后才算完成：

- 相同订单请求任意重试只创建一个订单、只扣一次库存。
- 订单创建与超时 Job 原子提交。
- 待支付订单最终能自动关闭或明确进入对账，不永久占用库存。
- 同一订单不能并发创建不同支付方式的活跃支付。
- 第二笔渠道成功不会被当作普通幂等成功。
- 任意支付关闭不会在仍有有效支付时错误释放库存。
- 支付事实状态和对账状态已完全分离。
- 预支付并发调用有持久化租约保护。
- 三种 River Job 职责和唯一键互不冲突。
- 所有状态更新都有行锁或条件更新保护。
- 全量生成、静态检查、单元测试和集成测试通过。
