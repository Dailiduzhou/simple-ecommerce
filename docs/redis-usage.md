# Redis 使用与缓存一致性设计

更新时间：2026-07-16

本文梳理项目中 Redis 的全部生产用途，重点说明 `afterCommit`、`cacheGeneration`、`bumpCacheGeneration`、Cache-Aside、singleflight、键空间和跨业务缓存失效。

## 1. 总体定位

PostgreSQL 是项目的唯一事实来源，Redis 不承载不可恢复的业务状态，主要用于：

1. 缓存用户、地址、分类、活动、商品、订单和支付数据。
2. 缓存分页或条件列表。
3. 保存 JWT 黑名单。
4. 用 generation 版本号批量失效无法枚举的列表缓存。

River 任务队列使用 PostgreSQL，而不是 Redis。项目也没有使用 Redis 分布式锁、Lua、Pub/Sub、限流器或 Redis Transaction。

生产代码使用的 Redis 命令只有：

| 命令 | 用途 |
|---|---|
| `PING` | 服务启动时验证 Redis 连接 |
| `GET` | 读取 JSON 缓存或 generation |
| `SET` | 写 JSON 缓存、JWT 黑名单 |
| `EXISTS` | 判断 JWT 是否在黑名单中 |
| `INCR` | 原子推进 generation |
| `UNLINK` | 异步删除缓存键，避免 `DEL` 同步释放大对象 |
| `CLOSE` | 服务退出时关闭客户端 |

## 2. 客户端初始化与共享对象

[`NewRedisClient`](../app/mall/internal/data/data.go#L76-L100) 创建 `go-redis` 客户端：

- 默认网络为 `tcp`。
- 当前密码固定为空，Redis DB 固定为 `0`。
- 默认连接超时为 5 秒，默认读写超时为 1 秒。
- 创建后立即执行 `PING`；失败会关闭客户端并阻止服务启动。

[`NewData`](../app/mall/internal/data/data.go#L60-L73) 将同一个 Redis 客户端和同一个 `singleflight.Group` 交给所有 Repo：

```go
return &Data{
    pool: pool,
    rdb:  rdb,
    q:    db.New(pool),
    sg:   &singleflight.Group{},
}, cleanup, nil
```

因此：

- 同一进程内的所有缓存操作共享连接池。
- 同一进程内的并发缓存回源可以通过 singleflight 合并。
- 不同服务实例之间不会共享 singleflight 状态。

Redis 键统一由 [`redisKey`](../app/mall/internal/data/redis_key.go#L8-L18) 生成，以 `:` 连接逻辑片段。

## 3. Cache-Aside 读取流程

活动、商品、分类、用户、地址、订单和支付基本采用相同的 Cache-Aside 流程：

```text
GET Redis
  ├─ 命中：JSON 反序列化并返回
  └─ 未命中或读取错误
       ↓
     singleflight.Do(sfKey)
       ↓
     再次 GET Redis
       ├─ 命中：返回
       └─ 未命中：查询 PostgreSQL → SET Redis → 返回
```

活动详情是一个完整示例，见 [`EventRepo.GetEvent`](../app/mall/internal/data/event.go#L69-L100)：

```go
e, err := r.getCache(ctx, cacheKey)
if err == nil {
    return e, nil
}

val, err, _ := r.data.sg.Do(sfKey, func() (any, error) {
    e, err := r.getCache(ctx, cacheKey) // double-check
    if err == nil {
        return e, nil
    }
    dbe, err := r.data.q.GetEvent(ctx, id)
    // ...
    r.setCache(ctx, cacheKey, &bizEvent)
    return &bizEvent, nil
})
```

第二次 `GET` 用于关闭并发窗口：当前请求等待 singleflight 时，其他请求可能已经完成回源并填充缓存。

当前策略的边界：

- singleflight 只在当前进程有效，不是 Redis 分布式锁。
- `sf:*` 是 singleflight 的逻辑键，不会写入 Redis。
- 项目没有负缓存；不存在的数据会重复回源 PostgreSQL。
- 除 JWT 黑名单外，Redis 读取错误通常按缓存未命中处理，业务继续访问数据库。
- JSON 反序列化失败也会触发数据库回源，成功后覆盖原缓存。

通用读取实现还可参考：

- 商品：[`GetProduct`](../app/mall/internal/data/product.go#L79-L111)、[`ListProducts`](../app/mall/internal/data/product.go#L115-L149)、[`ListProductsByCategory`](../app/mall/internal/data/product.go#L152-L187)
- 分类：[`GetCategory`](../app/mall/internal/data/category.go#L72-L104)、[`ListSubCategories`](../app/mall/internal/data/category.go#L106-L135)
- 订单：[`getOrder`](../app/mall/internal/data/order.go#L141-L169)、[`listOrders`](../app/mall/internal/data/order.go#L195-L222)
- 支付：[`getPayment`](../app/mall/internal/data/payment.go#L476-L500)
- 地址：[`ListShippingAddressesByUser`](../app/mall/internal/data/user.go#L323-L356)、[`GetShippingAddress`](../app/mall/internal/data/user.go#L359-L403)

## 4. `afterCommit`：数据库提交后再修改缓存

### 4.1 事务状态

[`transaction.InTx`](../app/mall/internal/data/transaction.go#L32-L63) 在 context 中注入数据库事务和一个回调列表：

```go
type txState struct {
    afterCommit []func()
}
```

事务结束时的顺序是：

```text
执行业务 SQL
  ↓
注册 Redis 回调
  ↓
PostgreSQL COMMIT
  ├─ 失败：返回错误，不执行 Redis 回调
  └─ 成功：顺序执行 Redis 回调
```

对应源码在 [`transaction.InTx`](../app/mall/internal/data/transaction.go#L39-L56)：

```go
if err != nil {
    _ = tx.Rollback(ctx)
    return
}
if cerr := tx.Commit(ctx); cerr != nil {
    err = cerr
    return
}
for _, cb := range state.afterCommit {
    cb()
}
```

### 4.2 注册规则

[`afterCommit`](../app/mall/internal/data/transaction.go#L76-L82) 的规则很简单：

```go
func afterCommit(ctx context.Context, fn func()) {
    if state, ok := ctx.Value(txStateKey{}).(*txState); ok && state != nil {
        state.afterCommit = append(state.afterCommit, fn)
        return
    }
    fn()
}
```

- context 属于 `InTx`：把 Redis 操作加入队列。
- context 不属于事务：立即执行。
- 数据库回滚或 `Commit` 失败：队列不会执行。
- Redis 回调没有错误返回值；Redis 失败不能回滚已经提交的数据库事务。

这保证了不会出现“Redis 已写入新数据，但 PostgreSQL 最终回滚”的明显不一致。

### 4.3 实际使用位置

以下缓存辅助函数使用 `afterCommit`：

- 活动缓存的 [`setCache`、`setListCache`、`deleteCache`](../app/mall/internal/data/event.go#L218-L248)
- 商品缓存的 [`setCache`、`setListCache`、`deleteCache`](../app/mall/internal/data/product.go#L282-L316)
- 地址缓存的 [`setCache`、`setListCache`、删除函数](../app/mall/internal/data/user.go#L250-L287)
- 订单缓存的 [`setCache`、`setListCache`、`deleteKey`](../app/mall/internal/data/order.go#L348-L380)
- 支付缓存的 [`setCache`、`deleteCache`](../app/mall/internal/data/payment.go#L713-L757)
- 所有 generation 推进操作

支付预下单是 `afterCommit` 最有价值的场景。业务层在同一事务中调用支付状态更新和 River 任务入队，见 [`PrepayForOrderWithCheckJob`](../app/mall/internal/biz/payment.go#L455-L470)。支付 Repo 在事务 context 中注册缓存变更，只有支付状态与任务记录都提交后才会真正修改 Redis。

有些调用是在 Repo 自己的 `InTx` 返回后才修改缓存，例如订单创建和地址创建。此时传入的是原始 context，`afterCommit` 会立即执行，但数据库事务已经成功提交，语义仍然正确。

分类和普通用户缓存目前没有包裹 `afterCommit`，它们在单条数据库操作成功后直接操作 Redis，见 [`CategoryRepo` 缓存函数](../app/mall/internal/data/category.go#L196-L247) 和 [`UserRepo` 缓存函数](../app/mall/internal/data/user.go#L154-L191)。

## 5. Generation：列表缓存的版本化失效

分页列表的所有具体缓存键很难枚举。项目没有使用 `SCAN` 删除所有分页键，而是把一个 generation 放入列表缓存键。

实现位于 [`cache_generation.go`](../app/mall/internal/data/cache_generation.go#L12-L34)。

### 5.1 `cacheGeneration`

```go
func cacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) int64 {
    generation, err := rdb.Get(ctx, key).Int64()
    if err == nil {
        return generation
    }
    // missing or failed: generation 0
    return 0
}
```

- generation 键存在：返回其整数值。
- Redis 返回 `redis.Nil`：视为初始版本 `0`。
- 其他 Redis 错误：记录指标和日志，然后降级为版本 `0`。

### 5.2 `bumpCacheGeneration`

```go
func bumpCacheGeneration(ctx context.Context, rdb *redis.Client, logger *log.Helper, key string) {
    afterCommit(ctx, func() {
        if err := rdb.Incr(ctx, key).Err(); err != nil {
            // metric + log
        }
    })
}
```

`INCR` 是 Redis 原子操作，多个服务实例可以安全地同时推进同一个 generation。因为它被 `afterCommit` 包裹，数据库事务回滚时不会错误地废弃列表缓存。

### 5.3 换代示例

假设当前存在：

```text
product:list:gen = 7
product:list:7:20:0 = [...]
```

商品变更提交后执行：

```text
INCR product:list:gen  // 7 → 8
```

下一次读取构造的新键是：

```text
product:list:8:20:0
```

旧的 `product:list:7:20:0` 不需要主动删除，已经无法通过当前 generation 访问，最终会因 TTL 自动过期。

generation 适用于：

- 活动全部列表和状态列表：[`event:list:gen`](../app/mall/internal/data/event.go#L103-L146)
- 商品总列表：[`product:list:gen`](../app/mall/internal/data/product.go#L115-L149)
- 分类商品列表：`product:category:<categoryId>:gen`
- 用户全部订单列表：`order:user:<userId>:gen`
- 用户进行中订单列表：`order:user:ongoing:<userId>:gen`

商品失效入口见 [`invalidateProductLists`](../app/mall/internal/data/product.go#L318-L331)，订单列表失效入口见 [`invalidateUserLists`](../app/mall/internal/data/order.go#L319-L322)。

这种策略的代价是：

- 旧版本值会占用内存直到 TTL 到期。
- generation 键本身没有 TTL，会长期保留。
- `INCR` 失败时，旧列表可能继续可见直到缓存过期。
- PostgreSQL 提交与 Redis `INCR` 之间仍有一个很短的最终一致窗口。

## 6. 键空间、值格式和 TTL

| 业务 | Redis 键 | 值 | 失效策略 | TTL | 主要源码 |
|---|---|---|---|---|---|
| JWT | `jwt:blacklist:<jti>` | 字符串 `1` | 到期自动删除 | Token 剩余有效期 | [`auth.go`](../app/mall/internal/data/auth.go#L23-L40) |
| 用户详情 | `user:<id>` | 用户公开字段 JSON | 精确 `UNLINK` 或覆盖 | 10–19 分钟 | [`user.go`](../app/mall/internal/data/user.go#L66-L191) |
| 用户手机号 | `user:phone:<hash>` | 当前没有写入 | 当前只有删除调用 | 无 | [`DeleteUser`](../app/mall/internal/data/user.go#L140-L152) |
| 地址详情 | `shipping_addr:user:<uid>:<addressId>` | 地址 JSON | 精确 `UNLINK` 或覆盖 | 10–19 分钟 | [`user.go`](../app/mall/internal/data/user.go#L226-L287) |
| 地址列表 | `shipping_addr:user:<uid>` | 地址数组 JSON | 精确 `UNLINK` | 10–19 分钟 | [`ListShippingAddressesByUser`](../app/mall/internal/data/user.go#L323-L356) |
| 分类详情 | `category:<id>` | 分类 JSON | 精确 `UNLINK` 或覆盖 | 10–19 分钟 | [`category.go`](../app/mall/internal/data/category.go#L72-L104) |
| 顶级分类 | `category:list:top` | 分类数组 JSON | 精确 `UNLINK` | 10–19 分钟 | [`categoryListCacheKey`](../app/mall/internal/data/category.go#L249-L258) |
| 子分类 | `category:list:<parentId>` | 分类数组 JSON | 精确 `UNLINK` | 10–19 分钟 | [`categoryListCacheKey`](../app/mall/internal/data/category.go#L249-L258) |
| 活动详情 | `event:<id>` | 活动 JSON | 精确 `UNLINK` 或覆盖 | 10–19 分钟 | [`event.go`](../app/mall/internal/data/event.go#L194-L228) |
| 活动列表 | `event:list:<gen>:all:<limit>:<offset>` | 活动数组 JSON | `event:list:gen` 换代 | 10–19 分钟 | [`eventListCacheKey`](../app/mall/internal/data/event.go#L258-L272) |
| 状态活动列表 | `event:list:<gen>:status:<status>:<limit>:<offset>` | 活动数组 JSON | `event:list:gen` 换代 | 10–19 分钟 | [`eventListCacheKey`](../app/mall/internal/data/event.go#L258-L272) |
| 商品详情 | `product:<id>` | 商品 JSON | 精确 `UNLINK` 或覆盖 | 10–19 分钟 | [`product.go`](../app/mall/internal/data/product.go#L258-L316) |
| 商品总列表 | `product:list:<gen>:<limit>:<offset>` | 商品数组 JSON | `product:list:gen` 换代 | 10–19 分钟 | [`ListProducts`](../app/mall/internal/data/product.go#L115-L149) |
| 分类商品列表 | `product:category:<categoryId>:<gen>:<limit>:<offset>` | 商品数组 JSON | 分类独立 generation | 10–19 分钟 | [`ListProductsByCategory`](../app/mall/internal/data/product.go#L152-L187) |
| 订单详情 | `order:<id>` | 订单 JSON | 精确 `UNLINK` 或覆盖 | 10–20 分钟 | [`GetOrder`](../app/mall/internal/data/order.go#L122-L127) |
| 订单号索引 | `order:no:<orderNo>` | 订单 JSON | 精确 `UNLINK` 或覆盖 | 10–20 分钟 | [`GetOrderByOrderNo`](../app/mall/internal/data/order.go#L129-L134) |
| 用户订单详情 | `order:user:<orderId>:<userId>` | 订单 JSON | 精确 `UNLINK` 或覆盖 | 10–20 分钟 | [`GetOrderByUser`](../app/mall/internal/data/order.go#L136-L140) |
| 用户订单列表 | `order:user:<userId>:<gen>:<limit>:<offset>` | 订单数组 JSON | 用户独立 generation | 10–20 分钟 | [`ListOrdersByUser`](../app/mall/internal/data/order.go#L185-L193) |
| 进行中订单 | `order:user:ongoing:<userId>:<gen>` | 订单数组 JSON | 用户独立 generation | 10–20 分钟 | [`ListOngoingOrdersByUser`](../app/mall/internal/data/order.go#L176-L183) |
| 支付详情 | `payment:<id>` | 支付 JSON | 精确 `UNLINK` 或覆盖 | 15 分钟 | [`GetPayment`](../app/mall/internal/data/payment.go#L448-L451) |
| 支付外部单号 | `payment:out_trade_no:<no>` | 支付 JSON | 精确 `UNLINK` 或覆盖 | 15 分钟 | [`GetPaymentByOutTradeNo`](../app/mall/internal/data/payment.go#L471-L475) |
| 订单最新支付 | `payment:order:<orderId>` | 支付 JSON | 精确 `UNLINK` 或覆盖 | 15 分钟 | [`GetLatestPaymentByOrder`](../app/mall/internal/data/payment.go#L461-L465) |
| 订单活跃支付 | `payment:order:<orderId>:active:<method>` | 支付 JSON | 精确 `UNLINK` 或覆盖 | 15 分钟 | [`GetActivePaymentByOrderMethod`](../app/mall/internal/data/payment.go#L466-L470) |

大多数缓存使用约 10–20 分钟的随机 TTL，目的是避免大量键在同一时间过期形成缓存雪崩。支付缓存使用固定 15 分钟 TTL。

## 7. 各业务的写入与失效策略

### 7.1 用户与 JWT

用户创建、更新后写入 `user:<id>`，删除用户时删除详情键。缓存值是一个裁剪后的 profile，只包含 ID、昵称、实名、角色和时间字段，不包含手机号、密码等字段，见 [`UserRepo.setCache`](../app/mall/internal/data/user.go#L166-L185)。

[`GetUserByPhoneHash`](../app/mall/internal/data/user.go#L101-L126) 当前只用 singleflight 合并数据库查询，查询成功后写 `user:<id>`，并不会读取或写入 `user:phone:<hash>`。

JWT 退出登录流程：

1. [`BlacklistToken`](../app/mall/internal/biz/user.go#L193-L200) 计算 Token 剩余有效期。
2. [`AuthRepo.SetBlacklist`](../app/mall/internal/data/auth.go#L23-L31) 写入 `jwt:blacklist:<jti> = 1`。
3. 鉴权通过 [`EXISTS`](../app/mall/internal/data/auth.go#L33-L40) 判断 Token 是否已注销。

JWT 黑名单与普通缓存不同：Redis 错误会向上传递，不会静默回源数据库。

### 7.2 地址

地址详情键包含 `userID` 和 `addressID`，避免不同用户之间出现同 ID 缓存串读。缓存命中后仍会检查 `sa.UserID == userID`，见 [`GetShippingAddress`](../app/mall/internal/data/user.go#L359-L380)。

创建、更新、删除或切换默认地址时会：

- 删除用户地址列表缓存。
- 删除发生变化的地址详情缓存。
- 必要时删除旧默认地址详情。
- 创建或更新后写回新的详情缓存。

默认地址切换使用 PostgreSQL 事务，见 [`SetDefaultShippingAddress`](../app/mall/internal/data/user.go#L430-L459)。

### 7.3 分类

分类列表没有分页，能够准确知道需要删除的父分类键，因此没有使用 generation：

- 创建：写详情，删除父分类列表。
- 更新：刷新详情，删除旧父级和新父级列表。
- 删除：删除自身和子分类详情，并删除相关父级列表。

源码见 [`CreateCategory`、`DeleteCategory`](../app/mall/internal/data/category.go#L31-L70) 和 [`UpdateCategory`](../app/mall/internal/data/category.go#L168-L194)。

### 7.4 活动

任何活动创建、更新、状态变化或删除都会：

- 精确失效对应 `event:<id>`。
- 执行 `INCR event:list:gen`，一次性废弃全部状态、limit、offset 组合。

源码见 [`EventRepo` 写方法](../app/mall/internal/data/event.go#L31-L67) 和 [`deleteListCaches`](../app/mall/internal/data/event.go#L250-L252)。

### 7.5 商品

商品变化时：

- 删除 `product:<id>`。
- 推进全局 `product:list:gen`。
- 推进相关 `product:category:<categoryId>:gen`。
- 商品换分类时同时推进旧分类和新分类。

统一入口是 [`invalidateProductLists`](../app/mall/internal/data/product.go#L318-L331)。订单创建和取消会改变库存，因此订单 Repo 也会跨领域删除商品详情并推进商品列表 generation，见 [`OrderRepo.CreateOrder`](../app/mall/internal/data/order.go#L102-L113) 和 [`CancelOrderByUser`](../app/mall/internal/data/order.go#L270-L287)。

### 7.6 订单

一个订单最多有三个详情索引：

```text
order:<orderId>
order:no:<outTradeNo>
order:user:<orderId>:<userId>
```

用户订单列表和进行中订单列表分别使用独立 generation。订单创建、取消或支付状态影响订单时，统一推进：

```text
order:user:<userId>:gen
order:user:ongoing:<userId>:gen
```

源码见 [`invalidateOrder`](../app/mall/internal/data/order.go#L310-L317) 和 [`invalidateUserLists`](../app/mall/internal/data/order.go#L319-L322)。

### 7.7 支付

[`cachePayment`](../app/mall/internal/data/payment.go#L724-L729) 将同一支付对象写入四个索引：

```text
payment:<paymentId>
payment:out_trade_no:<outTradeNo>
payment:order:<orderId>
payment:order:<orderId>:active:<method>
```

支付状态变化时，[`invalidatePayment`](../app/mall/internal/data/payment.go#L730-L737) 删除旧索引。支付成功、关闭或进入对账状态还会通过 [`invalidateOrder`](../app/mall/internal/data/payment.go#L738-L750) 删除相关订单详情，并推进用户订单 generation。

River 对账任务最终失败时，[`PaymentRiverErrorHandler.invalidatePaymentCaches`](../app/mall/internal/data/river_error_handler.go#L75-L99) 会在数据库事务提交后：

- 批量 `UNLINK` 支付和订单键。
- 推进用户订单和进行中订单 generation。

这里没有通过 `afterCommit` 注册回调，因为错误处理器已经手动完成 PostgreSQL `Commit`，随后才调用缓存失效。

## 8. 错误处理与一致性边界

### 8.1 Redis 是 best-effort 缓存

普通缓存的写入和删除错误通常只记录日志，不返回给业务层。数据库提交成功后，即使 Redis 操作失败，业务请求仍可能成功。

因此一致性保障是：

- PostgreSQL 始终正确。
- Redis 尽力在提交后更新。
- 失败时依靠日志、指标和 TTL 最终恢复。

generation 读取和推进失败会通过 [`observability.CacheFailure`](../app/mall/internal/observability/metrics.go#L54-L56) 上报指标。

### 8.2 最终一致窗口

`afterCommit` 保证 Redis 不会早于数据库提交，但 PostgreSQL `Commit` 和 Redis 操作不是同一个原子事务，两者之间仍存在短暂窗口：

```text
PostgreSQL 已提交
  ↓  短暂窗口
Redis UNLINK / SET / INCR
```

如果 Redis 操作失败，旧缓存可能继续存在直到 TTL 到期。这是当前设计接受的最终一致性取舍。

### 8.3 Redis 启动期与运行期策略不同

- 启动期：`PING` 失败会阻止服务启动，Redis 是强依赖。
- 运行期：普通数据缓存失败通常可以回源 PostgreSQL。
- JWT 黑名单：Redis 错误会返回调用方，不能回源数据库。

## 9. 当前实现中值得留意的点

以下是源码现状，不代表本文要求立即修改：

1. **手机号二级缓存未完成。** `user:phone:<hash>` 只有删除逻辑，`GetUserByPhoneHash` 不读取或写入该键。
2. **分类缓存错误处理较弱。** [`CategoryRepo.setCache`](../app/mall/internal/data/category.go#L220-L240) 没有检查 `SET` 返回错误，也没有使用 `afterCommit`。
3. **商品列表 singleflight 没有包含 generation。** [`ListProducts`](../app/mall/internal/data/product.go#L115-L149) 和分类列表使用的 `sfKey` 只包含查询参数。generation 切换期间，新旧版本请求可能被合并。
4. **地址详情 singleflight 没有包含 userID。** [`GetShippingAddress`](../app/mall/internal/data/user.go#L359-L403) 的 Redis 键和数据库条件都包含 userID，但 `sfKey` 只包含 address ID。
5. **没有负缓存。** 热点不存在 ID 可能持续访问 PostgreSQL。
6. **generation 读取错误降级为 0。** 如果 generation `GET` 临时失败而后续列表 `GET` 又成功，理论上可能访问旧的 generation 0 缓存。
7. **generation 键永久存在。** 列表值有 TTL，但 generation 键没有 TTL。

## 10. 相关测试

- Redis 键拼接：[`redis_key_test.go`](../app/mall/internal/data/redis_key_test.go#L5-L24)
- 商品全局与分类 generation：[`product_cache_test.go`](../app/mall/internal/data/product_cache_test.go#L14-L24)
- 订单与商品跨领域 generation：[`order_test.go`](../app/mall/internal/data/order_test.go#L14-L45)
- 活动缓存和 generation：[`event_test.go`](../app/mall/internal/data/event_test.go#L176-L231)
- 地址缓存所有者隔离：[`shipping_address_test.go`](../app/mall/internal/data/shipping_address_test.go#L24-L62)
- PostgreSQL/Redis/River 综合一致性：[`correctness_integration_test.go`](../app/mall/internal/data/correctness_integration_test.go#L270-L353)
