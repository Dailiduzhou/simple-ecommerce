# 浏览记录、社区与 MinIO 使用说明

## 已落地的契约

- 在 `app/mall` 内扩展 User、增加 Community / Media 服务；HTTP、gRPC 均要求 JWT 和黑名单校验，原有匿名白名单不变。
- 浏览记录显式上报，每用户/商品一条最近足迹；默认 90 天，读时过滤，后台分批删除。下架/软删除商品返回 `available=false`、价格 0，前端不得提供购买入口。列表支持可选的 `start_time`/`end_time` 时间窗口（半开区间 `[start, end)`，作用于 `last_viewed_at`）：按日查询由前端用用户本地时区算出当日边界，以带偏移的 RFC 3339 传入（如 `2026-09-09T00:00:00+08:00`）；服务器不感知时区。`start_time >= end_time` 或时间不可解析返回 400；窗口早于保留期时返回空页而不是错误；翻页时窗口参数与 `cursor` 一并重传，游标只携带 keyset 位置，与窗口解耦。
- 帖子标题/正文先 trim，再按 Unicode 字符限制 100/5000；评论上限 1000。拒绝 HTML（包括 `<` / `>`）、NUL、无效 UTF-8。帖子图片可选，允许 0～9 张已验证图片；创建时省略 `image_ids` 或传 `[]` 均表示纯文本帖子。请求体上限 64 KiB；图片不经应用请求体上传。
- 有图片时首图为封面，无图片时不返回封面。编辑是全量替换，省略 `image_ids` 或传 `[]` 表示移除全部图片（旧图片进入延迟清理），必须携带 `expected_version`；版本冲突返回 409。只有作者能编辑，作者/管理员能删除。管理员不能编辑别人的帖子，也不能指定别人为发布者。
- 点赞/取消幂等。数量由 PostgreSQL 关系表派生，非空帖子列表固定三次 SELECT（列表、统计、图片），加只读 REPEATABLE READ 事务的 BEGIN/COMMIT；正文、版本和图片来自同一快照，不混用点赞/评论明细 JOIN 的乘积计数。创建/编辑返回详情时复用原事务，不开启嵌套事务。
- 评论只提交 `content` 和可选 `reply_to_comment_id`；根由服务器推导。回复回复仍平铺在原根下，没有第三层。
- 删除根后有有效回复则保留 `deleted=true` 的空正文占位；没有有效回复则隐藏。已删除回复目标返回 `reply_to_deleted=true`，不返回其作者摘要。根/目标删除后不能继续回复。帖子作者不能删除其他用户的评论。
- 用户注销在一个事务内隐藏自己的帖子、清空自己的评论、解绑媒体、登记 River 删除任务，再物理删除用户。原订单等 FK 拒绝删除时全部回滚，缓存仅提交后失效。**这不是交易数据保留政策的改造。**

## 存储契约与 MinIO

`biz.ObjectStorage` 只暴露稳定定位、受限上传、对象读取、不可变写入、签名读取和删除，不引用 MinIO SDK。当前 `storage.provider=s3` 选择 `data.S3Storage`，兼容 MinIO/S3；其他厂商通过实现接口和扩展工厂接入，而不改 Service/Biz。S3 兼容服务必须实际支持条件创建（`If-None-Match: *`），不能忽略该请求头。

数据库保存 provider/bucket/key，不保存签名 URL。修改 bucket/provider 前必须迁移现有资源或保留旧适配器的路由，不能仅替换配置后丢弃旧资源。

### 本地部署

1. 复制 `.env.example` 并填入 JWT、MinIO 管理员和独立应用服务账号凭据。
2. 启动 `docker compose -f docker-compose.dev.yml --profile storage up -d minio postgres redis`（也可用支持 Compose 的 Podman）。MinIO S3/控制台分别在本机 9002/9003 端口，避免与 mall gRPC 的 9000 冲突。
3. 使用控制台或 `mc` 创建 **私有** bucket `ecommerce-images`，禁止匿名访问；不要开启版本控制，或另外配置非当前版本的回收策略。应用的单对象删除不会自动清理 bucket 的历史版本。
4. 创建只可访问该 bucket 的服务账号。参考 `deploy/minio-app-policy.json`，若改 bucket 名须同步资源 ARN。应用不需要修改 bucket policy、创建 bucket、创建用户或管理服务器的权限。`mc` 示例（凭据从本机环境注入，不在仓库填写真实值）：

   ```bash
   mc alias set local http://localhost:9002 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD"
   mc mb --ignore-existing local/ecommerce-images
   mc anonymous set none local/ecommerce-images
   # 通过 MinIO 管理界面建立应用账号并绑定上述策略，设置 STORAGE_* 凭据。
   ```

5. 容器内 I/O 使用 `STORAGE_ENDPOINT=minio:9000`；客户端签名地址使用 `STORAGE_PUBLIC_ENDPOINT=localhost:9002`。应用不会向 public endpoint 主动抓取内容。生产应使用真实可达的 HTTPS 域名，并设置 `STORAGE_PUBLIC_USE_TLS=true`；不要在签名生成后改 URL host/path，否则签名失效。原生运行 mall 时，把内部 endpoint 改为本机可达的地址。
6. 浏览器直传需要 MinIO/反向代理允许前端 origin 的 CORS（POST/GET），不要将私有 bucket 改成公开来解决跨域。应用自身的跨域策略仍由部署入口配置。
7. 启动 mall；不要给应用配置 MinIO root 凭据。`provider=disabled` 可让二进制在没有存储时启动，但媒体操作明确失败，不能作为图文功能已验收的依据。

### 两阶段上传

1. `POST /v1/media/images/uploads`，JSON：`{"content_type":"image/png","size_bytes":"12345"}`。
2. 获得 `id`、`upload_url`、`form_fields`、`expires_at`。客户端对 `upload_url` 发 multipart POST：先原样加入所有 form_fields，再追加名为 `file` 的文件。不要将 Bearer token 发给对象存储。
3. 上传成功后，`POST /v1/media/images/{id}/complete`。
4. 服务端读取真实字节，验证 JPEG/PNG/WebP、声明大小、完整解码、宽高（各不超过 10000）和总像素（不超过 2000 万）；单图最大 10 MiB。不接受 SVG/HTML 或任意远程 URL。
5. 将验证过的字节写入独立随机最终 key，使用原子 `If-None-Match: *`，不能覆盖已经验证的对象。上传凭证只允许写暂存 key，重放它不会改变最终图片。重复 complete 幂等。
6. 帖子请求只引用 `image_ids`，图片必须为本人所有、ready、未过期或已绑定于当前帖子，不能被别的帖子复用。

浏览器示例：

```javascript
const ticket = await api('/v1/media/images/uploads', {
  method: 'POST', body: JSON.stringify({content_type: file.type, size_bytes: String(file.size)})
});
const form = new FormData();
for (const [key, value] of Object.entries(ticket.form_fields)) form.append(key, value);
form.append('file', file); // file 必须放在策略字段之后
const uploaded = await fetch(ticket.upload_url, {method: 'POST', body: form});
if (!uploaded.ok) throw new Error('upload failed');
await api(`/v1/media/images/${ticket.id}/complete`, {method: 'POST', body: '{}'});
await api('/v1/posts', {method: 'POST', body: JSON.stringify({
  title: '标题', content: '纯文本正文', image_ids: [ticket.id]
})});
```

`api` 是调用方自己的 Bearer/JSON 封装，不负责自动重试创建请求。

### 私有读取与回收

- `GET /v1/media/images/{id}`：上传者可读未过期的 ready 未绑定资源；登录用户可读未删除帖子关联的 ready 资源。帖子详情/列表返回图片短期 URL。
- 默认上传凭证 10 分钟、读取 URL 5 分钟、未使用资源 24 小时。帖子删除后停止签发新 URL；已签发 URL 可在原 TTL 内继续有效，不提供即时撤销保证。
- 解绑先原子标记 deleting 并在同一 SQL 事务 `InsertTx` 入队；OSS 调用在事务外。删除至少等到上传凭证过期加一分钟宽限，防止凭证重放复活暂存对象。最终和暂存对象都删除；对象已不存在视作成功。
- Complete/对象删除用专用 PostgreSQL **session advisory lock** 串行化；跨 OSS I/O 不持有 SQL 事务。防止验证尚在写最终对象时，清理先删除、稍后 PUT 又把对象复活。绑定/回收检查另有媒体行锁和不可重新绑定的 deleting 状态。
- `maintenance` 和 `media` 默认各 2 个 worker，不占用支付/订单队列。每分钟清理一批足迹（默认 100，最大 1000）；媒体的新过期资源与滞留 deleting 资源各有独立的同等批次预算，单次最多为 `cleanup_batch_size × 2`。deleting 重入队后至少间隔 10 分钟才再次补偿；失败删除不会占用新过期资源的处理名额。
- 暂存对象会保留到整个媒体资源回收；若额外设置 bucket 生命周期清理 uploads 前缀，必须保证不早于未完成资源有效期。
- 已验证的是正常请求、凭证过期后的新请求、应用内完成/删除串行及重试。上传跨越凭证到期、客户端取消后存储服务仍完成写入等边界依赖存储实现，当前测试不证明其绝对安全；生产应限制上传耗时并验证存储端取消语义，辅以暂存对象生命周期和孤儿对象巡检。
- 失败记录保留，River 重试并通过周期扫描补偿。错误处理不会把媒体任务写入支付对账表。

## 接口速查

所有 ID 按 protobuf JSON 的字符串处理，不转 JavaScript Number。

| 功能 | HTTP |
| --- | --- |
| 上报/列表 | `POST/GET /v1/users/me/browsing-history`（列表 query：`cursor`、`page_size`、可选 `start_time`/`end_time`，后两个为含头不含尾的 RFC 3339 瞬时值） |
| 单条删除/清空 | `DELETE /v1/users/me/browsing-history/{product_id}` / `DELETE /v1/users/me/browsing-history` |
| 发布/列表 | `POST/GET /v1/posts` |
| 详情/编辑/删除 | `GET/PUT/DELETE /v1/posts/{id}` |
| 点赞/取消 | `PUT/DELETE /v1/posts/{id}/like` |
| 发评论/一级列表 | `POST/GET /v1/posts/{post_id}/comments` |
| 平铺回复 | `GET /v1/posts/{post_id}/comments/{root_comment_id}/replies` |
| 删除评论 | `DELETE /v1/posts/{post_id}/comments/{id}` |
| 上传凭证/完成/读取 | `POST /v1/media/images/uploads` / `POST /v1/media/images/{id}/complete` / `GET /v1/media/images/{id}` |

列表：`page_size` 默认 20、最大 50；`cursor` 为服务端返回值，不自行拼装。帖子可用 `author_id` 筛选。游标绑定版本和范围；用户/帖子/根/作者过滤不同不能复用。浏览列表排序会随新上报移动，客户端跨页按商品 ID 去重，进入页面重刷首屏，不保证冻结快照。

示例：`POST /v1/users/me/browsing-history {"product_id":"123"}`，应在成功展示商品后调用；不因预加载、下单校验或后台读取自动产生足迹。重试上报、删除和点赞安全。**发布帖子、创建评论没有创建幂等键，不得盲目自动重试。**

错误：400 参数，401 鉴权，403 已确认资源的越权写，404 不可见资源，409 版本/媒体冲突，429 限流，503 存储/限流依赖不可用。gRPC 有相应状态映射。

## 限流、监控和数据安全

- 每用户每分钟：发布/编辑 10，评论 30，上传/完成 20，足迹/点赞/删除互动 120；同一来源 IP 限额为各值的 5 倍。窗口计数/首次 TTL 用 Redis Lua 原子执行。
- 不信任 `X-Forwarded-For`；直接连接 IP 用于应用限流，反向代理还应配置真实客户端 IP 限流。IP 在 Redis key 中摘要化，不记录正文、凭证或签名 URL。
- 默认 Redis 限流故障拒绝写请求；`rate_limit_fail_open` 是显式放行策略开关，不改变 JWT 黑名单失败即拒绝的规则。新领域读取不依赖缓存，但不能宣称整个服务无需 Redis。
- 指标：`community_operation_total`（接口/清理/限流结果），`river_job_discarded_total`（按 kind）。告警在 `deploy/community-alerts.yaml`；与支付告警一起接入 Prometheus。
- 排障只记录 job ID、kind、错误原因和计数；不要粘贴上传 form_fields、签名 URL、token 或凭据到日志/工单。

## 生成、迁移与验证

```bash
# 仓库根目录调用（其他目录使用应用目录的绝对路径）；protoc 36.1 为系统依赖，Go 工具版本已固定。
make -C app/mall init
make -C app/mall generate  # 串行 api -> config -> sqlc/mock -> Wire
# 根 openapi.yaml 是唯一生成入口，app/mall/openapi.yaml 是自动同步镜像。
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

init 已直接扩充；**版本 1 已执行的数据库不会自动得到新表**。只能在确认可丢弃后重建专用本地数据库，禁止对共享库自动 down/reset。River 内部表仍由 rivermigrate 管理。

真实集成测试使用独立 PostgreSQL、独立 Redis 和私有 MinIO 测试 bucket：

```bash
export ECOMMERCE_INTEGRATION_POSTGRES_URL='postgres://.../ecommerce_integration?sslmode=disable'
export ECOMMERCE_INTEGRATION_REDIS_ADDR='127.0.0.1:16379'
export ECOMMERCE_INTEGRATION_S3_ENDPOINT='127.0.0.1:19000'
export ECOMMERCE_INTEGRATION_S3_BUCKET='ecommerce-integration'
# 通过本机安全环境注入 ECOMMERCE_INTEGRATION_S3_ACCESS_KEY / _SECRET_KEY
# 凭据只能访问上述专用测试 bucket；测试可在其不存在时创建它。
export ECOMMERCE_INTEGRATION_EXCLUSIVE=true # 确认三个依赖都是独占、可丢弃测试实例
bash .github/scripts/integration-test.sh # 先检查全部必需环境变量，再串行运行带 -race 的集成测试
```

测试会使用 Redis DB 15 并 FlushDB，不能连接共享 Redis，也不能并行启动多份集成套件。数据库名和 bucket 名均要求包含 `integration`。CI 单独提供三个真实服务并显式检查环境变量，不把缺配置后的 skip 当验收。

已验证：迁移 up/down/up、直接 SQL 层级约束、浏览并发/过期、帖子版本/权限、点赞和评论计数、根删除占位、注销回滚、媒体 I/O 与删除竞态、删除成功但 DB 回写失败的重试、真实 River 重试、MinIO 上传限制/私有读取/不可变对象/删除闭环。3000 条帖子及足迹的 EXPLAIN 覆盖分页、统计、占位根和清理索引；非空列表 1/20/50 条均为三次 SELECT 加 BEGIN/COMMIT（五次数据库调用），不会随条数增加查询次数。这是功能与代表性查询验证，不是生产容量压测。

### 配置启动验证补充

`cmd/mall/config_test.go` 使用实际示例 YAML 验证 env → Bootstrap：只将已知布尔字段的字符串转换为 bool，保持数字形式的 App ID/密钥为字符串，避免全局自动转换破坏凭据。示例鉴权密钥使用环境占位符，订单支付超时仍为 30 分钟，改用 protobuf Duration 可读取的 `1800s` 表示。已实际启动组装后的 mall（HTTP/gRPC/River + MinIO 配置），确认私有足迹路由无 token 返回 401。
