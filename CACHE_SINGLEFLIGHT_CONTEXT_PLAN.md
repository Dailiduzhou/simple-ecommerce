# cacheAside / singleflight 共享查询取消隔离改造方案

> 状态：第一阶段已实施并合入代码；第 10 节的第二阶段（服务关闭生命周期、共享查询并发上限）尚未实施。
>
> 目标：解决首个请求取消或超时后，共享数据库查询被取消，进而影响其他 singleflight 等待者的问题。
>
> 本方案仅涉及内部实现和内部配置，不修改对外 API、OpenAPI、数据库 schema 或 Redis key 格式。按项目约定，文档放在项目根目录，不维护 `docs/*`。

## 1. 问题与设计决策

### 1.1 当前问题

`app/mall/internal/data/cache.go` 中，`cacheAside` 使用 `singleflight.Group.DoChan` 合并查询，但共享回调及 Repository 的 `load` 闭包捕获了首个请求的 `ctx`。

```text
A 创建 flight，数据库查询使用 A.ctx
B 加入相同 flight
A 取消或超时
数据库查询响应 A.ctx 的取消
B 即使尚未取消，也收到共享查询的取消错误
```

这是取消生命周期耦合，不是 `singleflight.Group` 内部的 Go 内存数据竞争。给 Group 加锁、复制查询结果都不能解决这个问题。

### 1.2 采用的方案

采用“请求独立等待、共享工作独立超时”的设计，保留现有 `singleflight.Group`：

```text
请求 ctx
├── generation 查询
├── 第一次缓存 GET
└── 当前请求等待结果 / 退出等待

共享 workCtx
├── 第二次缓存 GET
├── 全部数据库回源查询
└── 同步、尽力而为的缓存回填
```

共享 context 在实际执行的 singleflight 回调内部创建：

```go
workCtx, cancel := context.WithTimeout(
    context.WithoutCancel(requestCtx),
    sharedWorkTimeout,
)
defer cancel()
```

关键约束：

1. 不继承首个请求的取消和 deadline，但保留稳定的 context value。
2. 必须设置独立超时，不能只使用 `WithoutCancel` 或无超时的 `Background`。
3. 创建和释放工作 context 的责任属于共享回调，不属于任何等待者。
4. 首个请求和后续请求均只通过自己的 `ctx` 控制等待。
5. 所有等待者退出后，查询仍可在有限预算内完成并回填缓存；第一阶段不实现等待者引用计数取消。
6. 事务内、空 key、无 Redis 的直接回源保持原请求取消语义。
7. 继续保持共享结果只读、修改前复制的约定。

### 1.3 目标行为

| 场景 | 改造后行为 |
|---|---|
| 首个请求取消 | 该请求退出，其他等待者继续等待同一次查询 |
| 后续等待者取消 | 该等待者退出，不影响其他请求 |
| 首个请求 deadline 到期 | 不缩短共享工作的独立 deadline |
| 共享工作自身超时 | 回源响应取消，仍在等待的请求收到共享超时错误 |
| 全部等待者取消 | 允许查询继续并填充缓存，但有独立超时约束 |
| 调用时请求已经取消 | 尽早返回，不主动创建新 flight |
| 事务内读取 | 不共享，继续使用原事务 context |
| generation 获取失败 | 保持禁用缓存及 singleflight、直接回源的现有行为 |
| 数据库成功，缓存写入失败 | 返回数据库结果；缓存错误只记录日志和指标 |
| 不同 owner、分页条件或 generation | 保持不同 flight，不合并 |

示例时序：

```text
A 请求超时 100ms，B 请求超时 3s，共享工作预算 3s，数据库耗时 500ms

0ms    A 创建共享工作
50ms   B 加入共享工作
100ms  A 退出，返回自己的超时错误
500ms  查询成功并尝试回填缓存，B 收到成功结果
```

## 2. 范围与阶段划分

### 第一阶段：本次修复必需

- 调整 `cacheAside` 的 `load` 签名及全部调用链。
- 在共享回调内部创建独立且有界的工作 context。
- 将缓存回填与提交后缓存维护的取消策略分开。
- 为共享工作超时提供默认值和内部配置。
- 快照异步闭包使用的可变请求参数。
- 增加确定性的取消、超时、回填及回归测试。

### 第二阶段：生产运行加固

- 将共享工作与服务关闭生命周期关联。
- 统计等待者取消、共享查询耗时及超时。
- 根据负载决定是否增加回源并发上限。

第二阶段未实施前，不能宣称共享工作已经能够随服务关闭主动取消并完成统一 drain。

### 本次不做

- 不重写 singleflight，不引入分布式锁。
- 不实现“最后一个等待者退出就取消查询”的自定义 flight 管理器。
- 不修改 generation 机制、负缓存策略或业务错误映射。
- 不扩大缓存覆盖范围，例如不将订单详情改为缓存读取。
- 不改变事务提交后的 generation 递增、outbox 补偿机制。
- 不将本次改造描述为数据库与 Redis 的强一致性方案。

## 3. 逐文件修改清单

以下路径均相对项目根目录。

| 文件 | 修改内容 |
|---|---|
| `app/mall/internal/data/cache.go` | 修改 `load` 签名；拆分请求与工作 context；新增遵守父 context 的回填 helper；删除已无调用方的 `writeJSONCache` |
| `app/mall/internal/data/data.go` | 增加共享工作超时字段，初始化默认值和配置值 |
| `app/mall/internal/conf/conf.proto` | 在内部 `Data` 配置中增加 `Cache.shared_work_timeout` |
| `app/mall/internal/conf/conf.pb.go` | 通过配置生成命令生成，不手工编辑 |
| `app/mall/configs/config.yaml` | 增加 `data.cache.shared_work_timeout` 示例配置 |
| `app/mall/internal/data/product.go` | 商品详情、全部列表、分类列表的回源使用 `loadCtx`；回填使用新 helper |
| `app/mall/internal/data/category.go` | 分类详情、子分类、顶层分类回源使用 `loadCtx`；回填使用新 helper |
| `app/mall/internal/data/event.go` | 活动详情和列表使用 `loadCtx`；快照 `status`；回填使用新 helper |
| `app/mall/internal/data/user.go` | 用户资料、地址详情和地址列表使用 `loadCtx`；回填使用新 helper |
| `app/mall/internal/data/order.go` | 修改 `listOrders` 包装签名；订单主查询与明细查询均传递 `loadCtx`；列表回填使用新 helper |
| `app/mall/internal/data/payment_repository.go` | 修改 `getPayment` 包装签名及所有相关调用；回源使用 `loadCtx` |
| `app/mall/internal/data/payment_cache.go` | 支付回填使用新 helper，保留 15 分钟正缓存 TTL |
| `app/mall/internal/data/cache_context_test.go`（新增） | 请求取消隔离、共享超时、上下文传递和回填测试 |
| `app/mall/internal/data/cache_config_test.go`（新增或合并现有测试） | 默认超时及配置覆盖测试 |
| `app/mall/internal/data/cache_repository_context_test.go`（新增） | 支付/订单包装链路与活动条件快照回归 |
| `app/mall/internal/observability/metrics.go`、`tracing.go` | 共享工作 span、活跃数与结果计数 |
| `app/mall/internal/observability/community_test.go` | 共享工作指标的低基数标签断言 |
| 相关现有 Repository 测试 | 增加多段 SQL、支付包装链路及参数快照回归覆盖 |

以下文件原则上不修改其行为，只运行回归测试：

- `app/mall/internal/data/cache_generation.go`
- `app/mall/internal/data/cache_invalidation.go`
- `app/mall/internal/data/cache_invalidation_repository.go`
- `app/mall/internal/data/transaction.go`
- `app/mall/internal/biz/user.go`
- `app/mall/internal/biz/order.go`
- `app/mall/internal/biz/order_shipping.go`

## 4. 超时配置设计

### 4.1 内部配置

在 `conf.proto` 的 `message Data` 中增加嵌套配置，使用当前空闲的字段编号：

```proto
message Cache {
  // Shared cache-miss work timeout. Unset/non-positive uses the default.
  google.protobuf.Duration shared_work_timeout = 1;
}

// Data 的现有 database = 1、redis = 2 保持不变。
Cache cache = 3;
```

在 `config.yaml` 的 `data` 下增加：

```yaml
cache:
  shared_work_timeout: 3s
```

约定：

- 未配置或非正数，沿用项目 `durationOrDefault` 风格，回退为 3 秒。
- 正数使用配置值。
- 3 秒是起始默认值，后续结合 SQL P99、连接池等待和 Redis 延迟调整。
- 不取首个请求的剩余时间作为共享预算，否则仍然存在首个请求支配其他等待者的问题。
- 第一阶段仅新增共享总预算；Redis 自身读写超时继续沿用已有配置。

### 4.2 Data 字段和默认值

在 `Data` 中增加：

```go
cacheWorkTimeout time.Duration
```

在 `NewData` 中读取配置，显式处理 `c == nil`。现有集成测试会调用 `NewData(nil, pool, rdb)`，该行为必须保持可用。

```go
const defaultCacheWorkTimeout = 3 * time.Second

// 在 NewData 中计算，然后赋给 Data.cacheWorkTimeout。
workTimeout := defaultCacheWorkTimeout
if c != nil && c.Cache != nil {
    workTimeout = durationOrDefault(
        c.Cache.SharedWorkTimeout,
        defaultCacheWorkTimeout,
    )
}
```

测试中存在直接构造 `&Data{...}` 的情况，因此读取预算时增加兜底：

```go
func (d *Data) sharedCacheTimeout() time.Duration {
    if d.cacheWorkTimeout <= 0 {
        return defaultCacheWorkTimeout
    }
    return d.cacheWorkTimeout
}
```

测试可以在启动 goroutine 前注入短超时，不要在并发请求过程中修改此字段。

## 5. cacheAside 核心改造

### 5.1 新签名

将：

```go
load func() (T, error)
```

改为：

```go
load func(context.Context) (T, error)
```

### 5.2 核心参考实现

以下实现依赖本方案中的 `Data.sharedCacheTimeout()`，且传入的 `write` 必须使用第 6 节的回填 helper。不能只替换此函数而不修改调用链。

```go
func cacheAside[T any](
    ctx context.Context,
    d *Data,
    logger *log.Helper,
    key string,
    read func(context.Context, string) (T, error),
    write func(context.Context, string, T),
    load func(context.Context) (T, error),
) (T, error) {
    var zero T

    if err := ctx.Err(); err != nil {
        return zero, err
    }

    // 保留事务与禁用缓存时的原请求取消语义。
    if key == "" || inTransaction(ctx) || d.rdb == nil {
        return load(ctx)
    }

    get := func(readCtx context.Context) (T, error) {
        value, err := read(readCtx, key)
        if err != nil && !errors.Is(err, redis.Nil) {
            cacheFailure(readCtx, logger, "get", key, err)
        }
        return value, err
    }

    // 第一次 GET 属于请求自身。
    if value, err := get(ctx); err == nil {
        if err := ctx.Err(); err != nil {
            return zero, err
        }
        return value, nil
    }

    // 第一次 GET 期间可能已经取消，尽早阻止创建新工作。
    if err := ctx.Err(); err != nil {
        return zero, err
    }

    resultCh := d.sg.DoChan("sf:"+key, func() (any, error) {
        // 创建与释放都必须由实际执行回调的 goroutine 负责。
        workCtx, cancel := context.WithTimeout(
            context.WithoutCancel(ctx),
            d.sharedCacheTimeout(),
        )
        defer cancel()

        if value, err := get(workCtx); err == nil {
            return value, nil
        }

        // 二次 GET 已耗尽预算时，不再发起 SQL。
        if err := workCtx.Err(); err != nil {
            return zero, err
        }

        value, err := load(workCtx)
        if err != nil {
            return zero, err
        }
        if err := workCtx.Err(); err != nil {
            return zero, err
        }

        // 缓存写入失败不覆盖成功的数据库结果。
        write(workCtx, key, value)
        return value, nil
    })

    select {
    case <-ctx.Done():
        return zero, ctx.Err()
    case result := <-resultCh:
        // 两个分支可能同时就绪，优先处理已经观察到的请求取消。
        if err := ctx.Err(); err != nil {
            return zero, err
        }
        if result.Err != nil {
            return zero, result.Err
        }
        return result.Val.(T), nil
    }
}
```

### 5.3 行为边界

- `ctx.Err()` 检查与 flight 登记不是原子操作；若请求恰好在最后一次检查后取消，仍可能启动工作。这种工作按独立预算完成，是允许的行为。
- 共享超时从实际回调开始计算，不包括请求此前的 generation 查询和第一次 GET。
- 晚加入的等待者使用当前 flight 的剩余预算，不为它重新延长 deadline。
- 缓存回填保持同步，不另起持有业务对象的无界写缓存 goroutine。
- 数据库成功且完成预算检查后，回填失败或在回填期间超时，仍返回数据库结果；请求自己的 context 若已取消，则请求返回取消错误。
- 不给请求取消分支增加 `Forget`，也不自动重试已经超时的 flight。
- context 是协作式取消，不能强制终止不遵守 context 的函数、CPU 运算或 JSON 序列化，不能承诺绝对的 goroutine 墙钟时间上限。

## 6. 分离读取回填和提交后缓存维护

### 6.1 为什么需要分离

现有 `writeJSONCache` 内部使用：

```go
context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
```

直接把 `workCtx` 传给它，会再次去掉共享 deadline，导致回填不受共享剩余预算约束。将来增加服务关闭取消时，这种写法也会绕过关闭信号。

但是不能全局删除所有 `WithoutCancel`：数据库提交后的失效维护仍需在客户端断开后尽力完成。

### 6.2 新增读取回填 helper

建议新增 `fillJSONCache`，供 `cacheAside` 的 `setCache` / `setListCache` 使用：

```go
func fillJSONCache(
    ctx context.Context,
    d *Data,
    logger *log.Helper,
    key string,
    value any,
    ttl time.Duration,
) {
    if key == "" || d.rdb == nil || inTransaction(ctx) {
        return
    }
    if ctx.Err() != nil {
        return
    }

    encoded, err := json.Marshal(value)
    if err != nil {
        cacheFailure(ctx, logger, "marshal", key, err)
        return
    }
    if bytes.Equal(encoded, []byte("null")) {
        ttl = negativeCacheTTL
    }

    // 不使用 WithoutCancel，不重置父 deadline。
    cacheCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
    defer cancel()
    if err := d.rdb.Set(cacheCtx, key, encoded, ttl).Err(); err != nil {
        cacheFailure(cacheCtx, logger, "set", key, err)
    }
}
```

规则：

- 仅用于事务外读取回填，事务内直接返回，不登记 afterCommit。
- 正缓存 TTL 保持现有各 Repository 策略；JSON `null` 保持 30 秒。
- `[]` 仍然按普通列表缓存，不改变为空时的业务返回行为。
- JSON 序列化在发布 singleflight 结果之前完成。
- 2 秒是局部写操作上限；父工作 context 剩余时间更短时，以父 deadline 为准。
- 可以抽取公共 JSON 编码函数复用序列化逻辑，但不能合并两种不同取消策略。

### 6.3 原有维护逻辑的处理

- 将本次 `cacheAside` 路径的所有 `setCache` / `setListCache` 改为调用 `fillJSONCache`。
- 原 `writeJSONCache` 在本次改造后已无任何调用方（提交后发布路径统一走 `scheduleCacheInvalidation`），因此直接删除；JSON 序列化与负缓存 TTL 判定抽为 `encodeCacheValue`，写入上限抽为 `cacheWriteTimeout` 常量。注意：将来若需要“提交后写入正缓存”，必须重新引入带 `WithoutCancel` 的提交后 helper，不能复用 `fillJSONCache`。
- `scheduleCacheInvalidation`、`afterCommit`、generation bump 和 outbox 的独立超时语义保持不变。
- 不因为读取回填遵守 `workCtx`，就让已经提交的业务写入失去取消后的失效维护能力。

## 7. Repository 调用链改造细节

### 7.1 直接回源：以商品详情为例

```go
return cacheAside(
    ctx, r.data, r.log, key, r.getCache, r.setCache,
    func(loadCtx context.Context) (*biz.Product, error) {
        row, err := r.data.DB(loadCtx).GetProduct(loadCtx, id)
        if stderrors.Is(err, pgx.ErrNoRows) {
            return nil, nil
        }
        if err != nil {
            return nil, err
        }
        value := toBizProduct(row)
        return &value, nil
    },
)
```

必须同时调整：

- `r.data.DB(loadCtx)` 或 `querierFromContext(loadCtx, r.data.q)`。
- SQL 方法的第一个参数 `loadCtx`。
- 回调内所有后续 SQL，不只是第一条查询。

商品、分类、用户资料、收货地址、活动的所有 `cacheAside` 闭包按此方式修改。维持原有 not-found 映射，尤其不要借此改动支付的错误返回和负缓存语义。

### 7.2 支付包装链路

将 `PaymentRepo.getPayment` 中的参数改为：

```go
load func(context.Context) (db.Payment, error)
```

其内部传给 `cacheAside` 的回调改为：

```go
func(loadCtx context.Context) (*biz.PaymentDO, error) {
    row, err := load(loadCtx)
    // 后续错误转换和实体映射保持原逻辑。
    // ...
}
```

继续修改调用方：

- `GetPayment`
- `GetLatestPaymentByOrder`
- `GetActivePaymentByOrderMethod`
- `GetPaymentByOutTradeNo`

例如 `GetPayment` 传给包装函数的数据库闭包必须是：

```go
func(loadCtx context.Context) (db.Payment, error) {
    return querierFromContext(loadCtx, r.data.q).GetPayment(loadCtx, id)
}
```

`GetPaymentForJob`、直接读取的 `getActivePaymentByOrder` 等不经过 `cacheAside` 的方法，不做无必要改造。`GetPaymentByUser` 保持取结果后的归属校验。

### 7.3 订单列表包装链路

将 `OrderRepo.listOrders` 中的参数改为：

```go
load func(context.Context) ([]db.Order, error)
```

需要覆盖：

1. `ListOrdersByUser` 传入的主查询闭包。
2. `ListOngoingOrdersByUser` 传入的主查询闭包。
3. `listOrders` 内部调用 `load(loadCtx)`。
4. 同一回调内的 `ListOrderItemsByOrderIDs(loadCtx, ids)`。

不要出现主查询已脱离请求、订单明细查询仍然使用外层 `ctx` 的半修复。

订单详情 `getOrder` 当前直接查询数据库，不参与这次共享查询改造。

### 7.4 活动条件和其他可变输入快照

`EventRepo.ListEvents` 当前接收 `status *int32`。在构造 key 和创建回调前，复制其值：

```go
hasStatus := status != nil
var statusValue int32
if hasStatus {
    statusValue = *status
}
```

参数校验、key 构造和 SQL 参数都基于同一份快照。若现有 key helper 仍要求指针，传入只由当前函数拥有且不再修改的局部副本。

原则：

- 闭包优先捕获 ID、分页参数、字符串、不可变条件快照。
- 不捕获请求体、会被复用的字节缓冲区、调用方可能继续修改的 map/slice。
- `WithoutCancel` 保留 context value，但不深拷贝其中的对象。共享工作不能依赖请求结束即失效的对象。
- 不把 trace/request ID 加入 singleflight key；会影响查询结果的 owner/tenant/filter 必须被 key 或授权逻辑正确隔离。

## 8. 测试设计

### 8.1 新增核心测试

建议集中放在 `app/mall/internal/data/cache_context_test.go`。

| 建议测试名 | 核心断言 |
|---|---|
| `TestCacheAside_LeaderCancellationDoesNotCancelSharedLoad` | A 取消，B 成功；数据库 load 恰好一次；工作 context 未随 A 取消 |
| `TestCacheAside_WaiterCancellationDoesNotCancelSharedLoad` | 后加入的 B 取消，不影响 A 和其他等待者 |
| `TestCacheAside_LeaderDeadlineIsNotInherited` | 首个请求短 deadline 到期后，共享查询仍可成功 |
| `TestCacheAside_SharedWorkTimeout` | 工作 context 有独立 deadline；到期后回源和等待者收到超时；不缓存失败结果 |
| `TestCacheAside_AllWaitersCancelStillFillsCache` | 全部等待者退出，解除受控查询阻塞后仍能回填；后续请求命中缓存 |
| `TestCacheAside_CanceledRequestDoesNotStartLoad` | 已取消请求不执行 load、不新建共享工作 |
| `TestCacheAside_TransactionUsesRequestContext` | 事务内绕过读写缓存和共享工作；原 context 取消会终止事务查询 |
| `TestCacheAside_DisabledCacheUsesRequestContext` | 空 key / nil Redis 分支保持请求取消语义 |
| `TestCacheAside_SecondReadUsesWorkContext` | 第二次 GET 不再使用首个请求 context；第二次命中时也可正确返回 |
| `TestCacheAside_SecondReadExhaustsBudget` | 第二次 GET 耗尽共享预算后，不再发起数据库查询 |
| `TestCacheAside_PreservesContextValues` | 稳定的测试 context value 能到达回源函数 |
| `TestFillJSONCache_RespectsParentDeadline` | SET context 不超过共享工作的 deadline，不脱离父取消 |
| `TestFillJSONCache_FailureDoesNotFailSuccessfulLoad` | 数据库成功，缓存写入失败或超时，仍返回数据库结果 |
| `TestFillJSONCache_TransactionDoesNotPublish` | 事务快照不写 Redis，也不登记回填回调 |
| `TestFillJSONCache_NegativeTTL` | typed nil 使用 30 秒负缓存，空列表保持普通 TTL |

此外增加配置测试：nil 配置、无 cache 配置、非正数默认值、正数覆盖值、直接构造 Data 的兜底行为。

### 8.2 确定性并发测试

不使用固定 `time.Sleep` 猜测等待者已经加入 flight。

推荐采用两层测试：

1. helper 级测试使用内存版 `read`/`write`/`load` 回调、channel 屏障和可控时钟。
2. Repository 级测试使用已有 mock Querier 和 miniredis，验证包装链路和实际缓存行为。

项目 Go 版本允许使用 `testing/synctest`。纯内存测试可在 `synctest.Test` 内运行，以 `synctest.Wait` 确认 goroutine 已进入阻塞状态；这类测试不要混入真实网络或 miniredis I/O。

首个请求取消测试应保证：

```text
A 的 load 已启动且被屏障阻塞
B 已完成初次 GET 并进入同一 flight 等待
取消 A
验证 A 已退出、工作 context 仍有效
解除 load 屏障
验证 B 成功、load 调用次数为 1
```

使用 channel 返回 goroutine 的结果，在测试主 goroutine 中断言；共享计数使用 atomic 或互斥锁。确保测试结束前所有后台工作退出。

### 8.3 调用链回归

必须补充或加强 Repository 测试：

- 支付：验证 `getPayment` 两层闭包都收到工作 context，首个请求取消后仍可返回给等待者。
- 订单：在主查询之后取消首个请求，确认后续 `ListOrderItemsByOrderIDs` 仍使用有效工作 context。
- 活动：查询条件快照用于 key 和 SQL；若测试修改原始 `status`，必须用同步屏障确保修改发生在快照完成之后，避免测试自身产生 data race。
- 配置：短共享超时测试不要依赖修改运行中的全局变量。

### 8.4 必须继续通过的既有测试

- `TestUserRepo_GetUserByID_Singleflight`
- `TestProductSingleflightSeparatesGenerations`
- `TestShippingAddressSingleflightSeparatesOwners`
- `TestUpdateCannotBeUndoneByInflightLoad`
- `TestCacheMutationsWaitForCommitAndSnapshotValues`
- `TestGenerationCommitAndFailureSemantics`
- `TestGenerationFailureNeverReadsOrWritesGenerationZero`
- `TestDetailNegativeCachesExpire`
- `TestCacheFallbackAndDatabaseErrors`
- `TestShippingAddressDecryptionDoesNotMutateSharedResults`
- `TestOrderUsecase_ListOrdersOwnsShippingPage`

收货地址和订单解密的复制行为必须保留。取消隔离不会让 singleflight 返回值变成独占对象。

## 9. 实施与验证步骤

### 9.1 建议提交顺序

1. 增加内部超时配置、Data 默认值和配置测试。
2. 在同一可编译变更中修改 `cacheAside`、全部 Repository 闭包及支付/订单包装签名。
3. 增加 `fillJSONCache`，切换所有读回填 setter，保留提交后失效语义。
4. 完成可变参数快照和确定性并发测试。
5. 运行全量相关包的 race 测试，审查生成文件与差异范围。
6. 第二阶段再接入服务关闭和运行指标，不把两阶段能力混淆。

步骤 2 与 3 应在同一次完整验证前完成，不能发布仅脱离数据库查询、却仍让回填重置工作 deadline 的中间状态。

### 9.2 生成内部配置

项目 Makefile 要求 `protoc 36.1`。环境满足要求后执行：

```bash
make -C app/mall config
```

不要执行 `make api` 或 `make generate` 来完成这次内部配置改动，避免无关地重新生成对外 OpenAPI、SQL 或 Wire 文件。

### 9.3 自动化验证

在实际修改并格式化 Go 文件后运行：

```bash
# 核心取消、超时和回填用例重复执行。
go test -race ./app/mall/internal/data \
  -run 'TestCacheAside_|TestFillJSONCache_|TestCacheWorkTimeout_' \
  -count=20

# 数据层、业务层、可观测性全量普通测试与 race 检查。
go test -race ./app/mall/internal/data ./app/mall/internal/biz ./app/mall/internal/observability -count=1

# 配置和服务层回归。
go test ./app/mall/internal/conf ./app/mall/internal/service ./app/mall/internal/server

# 编译应用，验证内部配置和依赖注入链路。
go build ./app/mall/cmd/mall

# 差异和格式检查。
git diff --check
git status --short
```

`go build ./app/mall/cmd/mall` 会在工作目录生成可执行文件；若不希望产生该文件，使用 `go build -o <临时目录>/mall ./app/mall/cmd/mall`。

真实 PostgreSQL/Redis 集成测试需要 `integration` build tag 和专用测试环境；有条件时补充，但不能把普通单元测试通过表述为真实基础设施集成测试已通过。

### 9.4 静态审查重点

```bash
rg -n 'cacheAside\(|func \(.*(getPayment|listOrders)' app/mall/internal/data
rg -n 'fillJSONCache\(|WithoutCancel|CacheSharedWork' app/mall/internal
```

人工检查：

- 每个新的 `func(loadCtx context.Context)` 内是否仍有 `DB(ctx)` 或 SQL 使用外层 `ctx`。
- 所有读取回填 setter 是否已切换到遵守父 context 的 helper。
- 是否误改了 transaction、generation、失效批次或 outbox 的取消策略。
- 是否引入无界 goroutine、请求取消时的 `Forget` 或非必要重试。
- 共享工作是否使用 `observability.StartSharedCacheWork`，而不是继续挂在已结束的请求 span 上。
- `openapi.yaml`、`api/`、`db/migrations/`、`docs/` 是否保持未修改。

## 10. 第二阶段：服务关闭与可观测性

### 10.1 关闭生命周期

第一阶段的工作 context 脱离请求且有超时，但没有自动关联服务关闭。第二阶段可引入服务级工作管理器，使共享查询在下列任一条件满足时取消：

```text
共享预算到期 OR 服务主动关闭
```

关闭顺序：

```text
停止接收新请求 / 新共享工作
→ 原子地关闭工作登记入口
→ 取消或等待已登记共享工作
→ 确认共享工作退出
→ 关闭 Redis 和 PostgreSQL 资源
```

实现约束：

- 工作登记、关闭 admission 和活动计数必须协调，不能在开始 Wait 后又无保护地登记新工作。
- 不只在回调中裸用 `WaitGroup.Add`，却让 cleanup 可以抢先观察到零活动工作并关闭资源。
- 可在共用互斥锁下检查 closing 并登记工作；进入 closing 后的回调直接拒绝执行。
- 若使用 `context.AfterFunc` 将服务停止信号桥接到工作 cancel，退出时注销回调，并测试服务已取消时的登记竞态。
- 如不用 `WithoutCancel(requestCtx)` 保留值，可从服务 context 派生，再显式复制稳定的 trace/baggage；不要依赖请求级可变对象。
- 需要单独验证关闭期间不再访问已关闭资源，以及迟到的 flight 不会被遗漏。

### 10.2 运行指标

第一阶段已实现（标签均为 `entity` / `result` / `operation` 低基数字段）：

- `cache_shared_work_inflight{entity}`：正在执行的共享查询数。请求放弃后工作仍会继续，这个 gauge 是连接池/数据库被“无人等待的工作”占用的信号。
- `cache_shared_work_total{entity,result}`：`cache_hit`、`loaded`、`error`、`timeout`。
- `cache_operation_failure_total{operation,entity}`：共享预算耗尽时追加 `operation="work_timeout"`，每个 flight 只记一次。
- `cache.shared_work` span：带 `cache.entity` 和 `cache.work_budget_ms`，让脱离请求的工作有自己的生命周期，同时保留 trace 关联。

仍未实现：

- 请求等待取消次数。
- 共享查询耗时分布（可从 span 推导，但没有专门的 histogram）。
- 数据库连接池等待。
- 等待者数量：标准 singleflight 不暴露活跃等待者数，未实现专门跟踪前，不声称能精确统计“所有等待者均取消的 flight 数量”。

HTTP 状态码保持不变（500），只把 reason 改为 `CACHE_WORK_TIMEOUT`，因为公开 OpenAPI 未定义这些端点的网关超时响应。改为 504 需要同步更新 OpenAPI，属于对外契约变更。

## 11. 明确禁止的实现方式

| 错误做法 | 问题 |
|---|---|
| 只在 cacheAside 内创建 workCtx，load 仍为无参闭包 | Repository 仍捕获原请求 context，问题未解决 |
| 在等待者函数外层创建 workCtx 并 `defer cancel()` | 首个请求退出时仍会取消共享查询 |
| `WithTimeout(requestCtx, timeout)` | 仍受首个请求更短 deadline 和取消影响 |
| 只用 `WithoutCancel` / `Background`，不设置独立超时 | 可能产生长期存活的回源工作 |
| 请求取消时调用 `Forget` | 不取消旧查询，却允许新查询并发重复执行 |
| 读取回填再次使用 `WithoutCancel(workCtx)` | 绕过共享预算和未来的服务关闭取消 |
| 将事务查询也放到独立共享 context | 破坏事务生命周期、快照隔离和连接使用约束 |
| 为提前返回而直接异步序列化共享业务对象 | 可能与返回值的后续修改形成数据竞争 |
| 为本问题改成 Redis 分布式锁 | 没有解决本地 context 归属问题，增加复杂度 |

## 12. 验收清单

- [x] 首个请求取消不影响已经加入同一 flight 的正常等待者。
- [x] 后续等待者取消不影响共享工作。
- [x] 共享查询具有独立、可配置、默认有界的预算。
- [x] 缓存第二次 GET、全部回源 SQL 和回填均使用工作 context。
- [x] 支付和订单中间包装层没有残留捕获外层请求 ctx 的 SQL。
- [x] 事务、空 key、无 Redis 的分支保持原请求取消行为。
- [x] 所有等待者退出后的继续工作行为有明确测试覆盖。
- [x] 回填失败不使已成功的数据库读取失败。
- [x] 提交后失效、generation 隔离、负缓存、owner 隔离均未退化。
- [x] 收货地址和订单共享结果的复制行为未退化。
- [x] 输入条件在异步生命周期内稳定，context value 的使用边界明确。
- [x] 新增测试不依赖 Sleep 猜测并发顺序，重复 race 测试通过。
- [x] 内部配置已重新生成，nil 配置和测试直接构造 Data 的方式仍可用。
- [x] 未修改对外 API、OpenAPI、数据库 schema 或 Redis key 格式。
- [x] 第一阶段与第二阶段已实现的能力在提交说明中如实区分。
- [ ] 共享预算默认值（3s）尚未用真实 SQL p99 验证；它同时是 cache miss 读的延迟上限，低于 `server.http.timeout`。

最终原则：**请求拥有自己的等待生命周期；共享查询拥有独立的执行预算。任何单个请求都不应独占共享工作的取消权。**
