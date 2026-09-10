# 商品浏览记录、图文帖子与评论功能实施计划

> 状态：本轮已补齐缺失接入，并通过本地回归及独立 PostgreSQL / Redis / River / MinIO 集成验证（2026-09-09），具体证据和边界见第 13 节；尚未部署生产或运行远端 CI。
> 图片契约为可选的 0～9 张。下文历史阶段记录不等于生产验收，当前验证以第 13 节为准。存储接口采用 provider-agnostic 设计，当前适配 MinIO/S3。详见 [调用与部署说明](community-history-api.md)。

## 1. 目标与边界

在现有 `app/mall` 应用内新增以下能力，不拆分微服务：

1. **商品浏览记录**：登录用户记录商品足迹、查看最近浏览、删除单条和清空记录。
2. **图文帖子**：用户发布、查看、编辑和删除自己的帖子，支持有序图片列表。
3. **帖子点赞**：点赞、取消点赞、点赞数量及当前用户是否点赞。
4. **帖子评论**：一级评论和一级楼中楼；可以回复特定回复，但展示和存储结构始终只有两层。
5. **图片存储扩展**：建立媒体资源归属校验和存储接口，再接入真实 OSS 上传、读取与清理。

### 1.1 首期范围

- 仅记录登录用户的商品浏览，不做匿名设备轨迹和登录后的历史合并。
- 浏览记录是“每个用户对每个商品的最近一次足迹”，不是逐次访问埋点流水，也不提供精确浏览次数。
- 帖子使用“纯文本正文 + 有序图片列表”；不接受 HTML，不实现正文内任意位置插图的富文本编辑器。
- 首期直接发布，不引入草稿、关注、推荐算法、热门榜、收藏、话题和商品关联。
- 点赞仅针对帖子，不默认扩展到评论点赞。
- 评论仅支持文字、创建、分页和删除；暂不支持编辑、图片评论或私信通知。
- 作者可编辑/删除自己的帖子；评论作者可删除自己的评论；管理员可删除违规帖子或评论。帖子作者不能删除其他人的评论。
- 延续当前鉴权：新接口全部要求登录，不顺带开放现有商品接口或新社区接口的匿名访问。
- 不改造订单、库存、支付或退款流程；仍遵守微信支付为桩、支付宝仅沙箱的项目限制。

## 2. 当前项目基础与接入点

| 已核对的位置 | 当前情况 | 本次接入方式 |
| --- | --- | --- |
| `api/user/v1/user.proto` | 用户、登录、刷新令牌、收货地址接口 | 增加当前用户浏览记录 RPC |
| `api/mall/v1/mall.proto` | 商品、分类、活动；定义了 `MediaInfo` | 复用商品标识和展示语义，不把社区接口塞入 Mall 服务 |
| `app/mall/internal/service/mall.go` | `GetProduct` 仅查询并返回商品 | 不在通用商品读取链路中隐式写入浏览记录 |
| `app/mall/db/query/products.sql` | 商品使用 `deleted_at` 软删除；`GetProduct` 不过滤上下架状态 | 浏览上报单独校验 `status = 1 AND deleted_at IS NULL` |
| `app/mall/db/migrations/000001_init_schema.*.sql` | 初始化脚本已有用户、商品、交易、活动表，没有本次所需表 | 按 `AGENTS.md` 直接扩充 init up/down，无历史数据回填 |
| `app/mall/sqlc.yaml`、`app/mall/Makefile` | SQL → pgx/v5 代码及 `Querier` mock | 先维护 SQL，再生成代码；不手改生成文件 |
| `app/mall/internal/biz/transaction.go`、`internal/data/transaction.go` | `TxManager.InTx`、事务 context 和 `afterCommit` | 复用事务抽象；事务内查询统一走 `Data.DB(ctx)` |
| `internal/service/authorization.go`、`internal/biz/authorization.go` | JWT claims、资源所有者、管理员校验 | 调用者身份取自 claims，不能信任请求传入的作者 ID |
| `internal/server/http.go`、`grpc.go` | 两种传输均已接入 JWT、黑名单、参数校验 | 注册新服务；保留现有白名单，不扩大匿名访问面 |
| `internal/data/data.go`、`internal/job/job.go`、`reaper.go` | River 已接入 PostgreSQL；现有队列为 `payments`、`orders` | 新增独立维护/媒体队列、Worker 和周期任务 |
| `internal/conf/conf.proto` | 当前没有对象存储配置 | 新增存储和本功能策略配置；不能把 `MediaInfo` 当成已经实现 OSS |
| `internal/data/user.go`、`db/query/users.sql` | 用户删除为物理删除，且现有 Repo 删除路径直接使用 `data.q` | 新表必须明确用户删除策略；涉及联合事务时调整为事务感知查询 |
| `.github/workflows/go-test.yml` | 当前运行 `go test -race ... ./...`，没有启用 integration 标签 | 增补真实 PostgreSQL/Redis 集成测试 job |

> 以下路径未特别注明时，`internal/`、`db/` 均位于 `app/mall/` 下。计划以当前源码为准，不把其他文档中的历史迁移描述当作现状。

## 3. 核心业务规则

### 3.1 浏览记录

**建议采用显式上报，不修改 `ProductRepo.GetProduct`：**

1. 客户端成功展示商品详情后，调用浏览记录上报接口。
2. 服务端从 claims 获取用户 ID，使用数据库查询确认商品存在、未删除且已上架。
3. 按 `(user_id, product_id)` UPSERT，只保留一条足迹，更新最近上报时间。
4. 商品详情请求与足迹写入分离；足迹失败返回明确错误，但不影响已经展示的商品。客户端可有限重试，不承诺每次页面打开都必达。

这样不会把下单校验、后台读取、列表预加载误记成用户浏览，也不会因商品缓存命中而漏记。时间含义为“服务端最后成功接收上报的时间”，不信任客户端时间。

其他规则：

- 重复上报不新增行；不增加 `view_count`，避免把网络重试当成真实浏览次数。
- 默认保存最近 **90 天**，查询时即过滤过期记录，River 分批物理清理；清理延迟不能导致过期记录重新展示。
- 按 `(last_viewed_at DESC, product_id DESC)` 游标分页，建议默认 20 条、最大 50 条。列表支持可选的半开时间窗口 `[start_time, end_time)`（含头不含尾）：前端按用户本地时区计算日界并携带偏移发送，服务器按瞬时值过滤；窗口与游标解耦，翻页时窗口随请求重传。`start_time >= end_time` 返回 400；窗口超出保留期返回空页；现有 `(user_id, last_viewed_at DESC, product_id DESC)` 索引即为范围扫描，无需新增索引。
- 浏览列表批量关联商品摘要，避免逐条调用商品 Repo；下架或软删除商品保留足迹并返回“商品不可用”，不提供购买入口。
- 删除单条使用当前用户 ID 与商品 ID 联合条件；清空只能清空自己。管理员也不通过这些接口查看他人足迹。
- 上报、单条删除、清空按用户维度使用同一短事务锁序列化，例如锁定对应用户行；清空完成后发生的新浏览可以重新出现。
- 首期不把写入交给 River，避免用户清空后，历史上报任务重试又把记录写回来。
- 足迹排序字段会随新浏览变化，跨页为实时列表而非冻结快照；客户端按商品 ID 去重，重新进入页面刷新首屏，不承诺跨页绝对无遗漏。

### 3.2 图文帖子

建议默认校验：标题去除首尾空白后 1～100 字；正文 1～5000 字；图片可选，为 0～9 张，单张不超过 10 MiB。文本长度以 Unicode 字符而非 Go 字节数计，并限制整体请求体大小。

- 输入是标题、纯文本正文、顺序明确的 `image_ids`；不接收可任意指定的 bucket、object key 或第三方图片 URL。
- 图片须属于当前用户、已经服务端验证完成且未被其他帖子占用；首期一份上传资源只绑定一篇帖子。
- 有图片时首图作为封面，图片顺序可随编辑调整；无图片时为纯文本帖子，不返回封面。
- 创建帖子和绑定图片在同一数据库事务完成；所有图片校验失败时整个发布失败。
- 创建时可省略 `image_ids` 或传空数组，发布纯文本帖子。编辑使用全量替换标题、正文、图片列表，省略 `image_ids` 或传空数组表示移除全部图片，旧图片仍进入延迟清理；并携带 `expected_version`，版本冲突返回 409，避免多个编辑页相互覆盖。
- 默认帖子列表按 `(created_at DESC, id DESC)` 游标分页，可按作者 ID 筛选。作者筛选是公开内容筛选，不获得该作者的私有信息或已删除内容。
- 删除帖子为软删除；详情、列表、点赞、评论及回复入口均检查帖子未删除。已有评论和点赞不必在删除请求中逐条物理删除。
- 删除后不可恢复；解绑的图片进入延迟清理流程，不在数据库事务内调用 OSS。
- 对外作者摘要单独定义为 `id + nickname`，不复用包含手机号、实名等字段的 `UserInfo`。

### 3.3 点赞

- 使用“设置为已点赞”和“设置为未点赞”两个幂等动作，不提供 toggle 接口。
- `(post_id, user_id)` 唯一约束是重复点赞的最终防线。
- 插入使用 `ON CONFLICT DO NOTHING`；取消使用条件 DELETE。重复请求均成功，不重复计数。
- 允许作者为自己的帖子点赞；只允许对未删除帖子操作。
- 首期不存储冗余 `like_count`：从点赞关系表按页面内帖子 ID 批量聚合，当前用户 `liked_by_me` 用批量查询或 `EXISTS` 获取。
- 点赞数、有效评论数、有效回复数均由 PostgreSQL 派生，避免引入计数漂移、取消点赞负数和用户删除后的计数修复。
- 禁止直接同时 JOIN 点赞和评论明细后 `COUNT(*)`，避免连接乘积放大数量；分别预聚合后再关联帖子。
- 数据规模确有需要时再加事务内计数器及校准任务，不首期引入 Redis 异步计数。

### 3.4 评论与单层楼中楼

每条评论存储 `root_comment_id` 和 `reply_to_comment_id`，**不使用可无限递归的 parent 树作为接口结构**。

| 类型 | `root_comment_id` | `reply_to_comment_id` |
| --- | --- | --- |
| 一级评论 A | NULL | NULL |
| 回复 A 的 B | A.id | A.id |
| 回复 B 的 C | A.id | B.id |

展示结构始终为：

```text
一级评论 A
  ├─ B：回复 A
  └─ C：回复 B
```

C 不成为 B 的子树。`reply_to_comment_id` 只表达“回复谁”，根评论决定唯一楼层归属。

创建规则：

1. 请求仅提交正文和可选的 `reply_to_comment_id`；根评论 ID、作者 ID 和被回复用户由服务端推导。
2. 不指定回复目标则创建一级评论；指定目标则查询目标：目标是一级评论时根为目标自身，否则沿用目标的根。
3. 帖子、回复目标及根评论必须存在、未删除；目标与根必须属于同一帖子，根本身必须是一级评论。
4. 正文去空白后建议 1～1000 字；不能回复自己这条尚不存在的新评论，也不能通过修改关系制造环。
5. 评论所属帖子、根和回复目标创建后不可修改，只有正文删除状态可以变化。

读取与删除规则：

- 一级评论按 `(created_at DESC, id DESC)` 分页；楼中楼按 `(created_at ASC, id ASC)` 单独分页，二者都使用带 ID 的稳定排序。
- 一级评论列表首期返回有效回复数，不自动加载全部回复；需要预览时最多批量取固定数量，禁止每条评论额外查询一次。
- `comment_count` 定义为帖下全部未删除评论数，包含一级评论和回复；`reply_count` 为某根评论下未删除回复数。
- 删除评论使用 `deleted_at` 并清空正文；重复删除幂等。
- 一级评论删除后，有未删除回复时保留“评论已删除”占位，仍可读取原楼中楼；无有效回复时从一级列表隐藏。
- 已删除的回复可从楼中楼列表隐藏；其他回复仍引用其 ID，响应显示“被回复评论已删除”，不得返回原正文或原作者摘要。
- 根评论已删除后禁止继续给整个楼层追加回复；回复目标已删除也禁止追加。已经存在的其他回复不级联删除。
- 普通用户操作范围由所有权校验限制；跨帖子回复、伪造根、第三层根和回复不存在的目标均拒绝。

## 4. 数据库设计

### 4.1 表与字段

新增实体主键延续项目的 `BIGINT GENERATED ALWAYS AS IDENTITY`，时间使用 `TIMESTAMPTZ`。关联表优先复合主键，不为每张表额外引入雪花 ID。

| 表 | 主要字段 | 关键约束与用途 |
| --- | --- | --- |
| `product_browsing_history` | `user_id`、`product_id`、`first_viewed_at`、`last_viewed_at` | 主键 `(user_id, product_id)`；用户、商品外键；检查最后时间不早于首次时间 |
| `posts` | `id`、`author_id`、`title`、`content`、`version`、`created_at`、`updated_at`、`deleted_at` | 作者外键；版本为正数；非删除状态的标题、正文长度检查 |
| `media_assets` | `id`、`owner_id`、`provider`、`bucket_name`、`object_key`、`content_type`、`size_bytes`、`width`、`height`、`status`、`expires_at`、`created_at`、`updated_at` | `(provider, bucket_name, object_key)` 唯一；状态建议 `pending/ready/deleting/deleted`；不持久化短期签名 URL |
| `post_images` | `post_id`、`media_id`、`sort_order` | 主键 `(post_id, media_id)`；`UNIQUE(media_id)`；`UNIQUE(post_id, sort_order)`；顺序范围 0～8 |
| `post_likes` | `post_id`、`user_id`、`created_at` | 主键 `(post_id, user_id)`；外键；用关系本身表达点赞状态 |
| `post_comments` | `id`、`post_id`、`author_id`、`root_comment_id`、`reply_to_comment_id`、`content`、`created_at`、`deleted_at` | 帖子、作者外键；同帖子内的根和回复目标外键；存储单层楼中楼 |

图片张数、连续排序和媒体归属属于跨行规则，由事务内业务校验保障；删除内容后的空正文必须允许通过数据库约束。

### 4.2 评论约束

- 增加 `UNIQUE(post_id, id)`，供 `(post_id, root_comment_id)`、`(post_id, reply_to_comment_id)` 复合外键引用，数据库层防止跨帖关联。
- CHECK 保证根和回复目标同时为空或同时非空，并限制二者不能等于当前评论 ID。
- **CHECK 不能查询其他行**：不能仅靠 `root_comment_id IS NOT NULL` 宣称限制了层数。
- 计划在 init SQL 增加约束触发器：校验根行 `root_comment_id IS NULL`，且回复目标要么就是根，要么属于同一根；同时禁止已有评论修改帖子、根、回复目标。
- 触发器承担结构兜底，业务层仍需在事务内校验目标和根的可见性，返回可识别的领域错误。
- 单条评论不物理删除，避免 self-FK 破坏楼中楼；整帖物理清理留待后续独立维护方案。

### 4.3 索引

| 场景 | 索引建议 |
| --- | --- |
| 用户最近浏览 | `(user_id, last_viewed_at DESC, product_id DESC)` |
| 浏览过期清理 | `(last_viewed_at, user_id, product_id)` |
| 商品物理删除时查找足迹 | `product_browsing_history(product_id)` |
| 帖子信息流 | `(created_at DESC, id DESC) WHERE deleted_at IS NULL` |
| 某作者帖子 | `(author_id, created_at DESC, id DESC) WHERE deleted_at IS NULL` |
| 当前用户点赞、注销清理 | `post_likes(user_id, post_id)`，帖子点赞聚合复用主键前缀 |
| 一级评论含删除占位 | `(post_id, created_at DESC, id DESC) WHERE root_comment_id IS NULL`，不能简单排除已删除根 |
| 楼中楼查询及根引用 | `(post_id, root_comment_id, created_at ASC, id ASC)` |
| 回复目标引用 | `(post_id, reply_to_comment_id)` |
| 有效评论数 | `(post_id) WHERE deleted_at IS NULL` |
| 用户评论清理 | `post_comments(author_id)` |
| 媒体过期清理、所有权校验 | `media_assets(status, expires_at, id)`、`media_assets(owner_id)` |

用真实数据量执行 `EXPLAIN (ANALYZE, BUFFERS)` 验证分页、聚合、占位根筛选和清理查询，不把所有可能的索引都提前堆到表上。

### 4.4 删除与外键策略

- 用户、商品物理删除时，浏览记录 `ON DELETE CASCADE`；商品软删除则保留记录并展示不可用。
- 用户删除时，点赞 `ON DELETE CASCADE`；派生计数自然更新。
- 帖子、评论作者及媒体所有者允许 NULL，用户外键 `ON DELETE SET NULL`，用于保留结构或待清理记录，不代表允许匿名发布。
- 用户删除流程须在同一事务内：隐藏本人帖子、清空本人评论正文并标记删除、解绑本人图片并登记清理任务，再执行现有用户删除。
- 删除根评论时仍保留其他用户的回复；注销后的作者显示为已注销，不泄露个人资料。
- 现有订单等外键若阻止用户物理删除，整笔清理事务一起回滚，不能先删除社区内容再返回注销失败。本次不擅自改变交易数据保留政策。
- 为此调整 `UserRepo.DeleteUser` 使用事务 context，并将受影响的用户缓存失效放到提交后；补充成功与回滚测试。
- `post_images`、`post_likes` 等依赖帖子的关系设置明确的删除策略；媒体本身不随关系行级联物理删除，因为 OSS 清理需要可重试记录。

### 4.5 初始化迁移

- 直接修改 `000001_init_schema.up.sql`，先创建依赖实体再创建关联表及触发器。
- 同步 `000001_init_schema.down.sql`，按反依赖顺序删除新增表、触发器函数，再删除现有商品/用户表。
- 增补 `db/query/browsing_history.sql`、`posts.sql`、`post_images.sql`、`post_likes.sql`、`post_comments.sql`、`media_assets.sql`。
- **修改 init 不会让已经标记版本 1 的数据库重新执行建表**。本地与测试需显式重建可丢弃的专用数据库；先确认数据可丢弃，不提供或自动运行清空共享数据库的操作。
- River 内部表仍由现有 `rivermigrate` 管理，不手写到业务 init SQL。

## 5. API 设计草案

浏览记录扩展现有 User 服务；新增 `api/community/v1` 的 Community 服务；媒体阶段新增 `api/media/v1` 的 Media 服务。每个新模块同步定义 error proto、HTTP/gRPC 生成代码及 OpenAPI。

### 5.1 浏览记录

| RPC | HTTP | 说明 |
| --- | --- | --- |
| `RecordProductView` | `POST /v1/users/me/browsing-history` | 请求含 `product_id`；返回已持久化的最后浏览时间 |
| `ListBrowsingHistory` | `GET /v1/users/me/browsing-history` | 当前用户游标列表，含商品摘要和可用状态 |
| `DeleteBrowsingHistoryItem` | `DELETE /v1/users/me/browsing-history/{product_id}` | 删除自己的一条足迹，不存在也成功 |
| `ClearBrowsingHistory` | `DELETE /v1/users/me/browsing-history` | 清空自己的足迹 |

`me` 路由需验证不会被既有 `/v1/users/{id}` 路由错误匹配；HTTP 路由测试与 gRPC 测试都必须覆盖。新请求不提供 `user_id`，由 claims 注入。

### 5.2 帖子与点赞

| RPC | HTTP | 说明 |
| --- | --- | --- |
| `CreatePost` | `POST /v1/posts` | 标题、正文、`image_ids`；作者来自 claims |
| `GetPost` | `GET /v1/posts/{id}` | 内容、图片、作者摘要、版本、点赞数、评论数、`liked_by_me` |
| `ListPosts` | `GET /v1/posts` | 游标分页，可选 `author_id` 筛选 |
| `UpdatePost` | `PUT /v1/posts/{id}` | 作者全量修改，携带 `expected_version` |
| `DeletePost` | `DELETE /v1/posts/{id}` | 作者或管理员软删除 |
| `LikePost` | `PUT /v1/posts/{id}/like` | 设置为已点赞 |
| `UnlikePost` | `DELETE /v1/posts/{id}/like` | 设置为未点赞 |

首期不新增点赞用户列表，避免不必要的社交关系暴露。帖子和评论创建不是幂等动作，客户端不得盲目自动重试；如确需提交重试保障，在 P0 确认后增加用户范围的 `client_request_id` 唯一键和请求摘要校验。

### 5.3 评论

| RPC | HTTP | 说明 |
| --- | --- | --- |
| `CreateComment` | `POST /v1/posts/{post_id}/comments` | 正文、可选 `reply_to_comment_id` |
| `ListComments` | `GET /v1/posts/{post_id}/comments` | 仅一级评论及必要占位、有效回复数 |
| `ListCommentReplies` | `GET /v1/posts/{post_id}/comments/{root_comment_id}/replies` | 一层平铺回复，携带回复目标摘要和删除状态 |
| `DeleteComment` | `DELETE /v1/posts/{post_id}/comments/{id}` | 作者或管理员删除；同时约束帖子归属 |

### 5.4 共用约定

- 游标包含排序键、过滤范围及版本；严格校验长度、格式和作用域，不能把 A 帖游标用于 B 帖回复列表。游标不承担鉴权。
- 列表返回 `items + next_cursor`，统一默认 20、最大 50；不为所有列表强制计算全量 total。
- ID 延续 proto `int64`；前端按 protobuf JSON 的字符串形式处理，避免 JavaScript 精度丢失。
- 使用 `buf.validate` 约束与现有 `ProtoValidate` 中间件，业务层重复检查核心规则，不能只依赖 HTTP 校验。
- 错误语义：参数非法 400、未登录 401、已确认资源的越权写 403、不可见/不存在资源 404、版本冲突或媒体状态冲突 409、限流 429；gRPC 使用对应状态映射。
- 新增错误原因建议包含：`PRODUCT_NOT_AVAILABLE`、`POST_NOT_FOUND`、`COMMENT_NOT_FOUND`、`INVALID_REPLY_TARGET`、`COMMENT_THREAD_CLOSED`、`MEDIA_NOT_READY`、`POST_VERSION_CONFLICT`。

## 6. 分层实现与一致性

### 6.1 职责划分

- **Service**：claims 提取、协议转换、权限入口校验、领域错误映射；不直接访问 SQL 或 OSS。
- **Biz**：`BrowsingHistoryUsecase/Repo`、`PostUsecase/Repo`、`CommentUsecase/Repo`、`MediaUsecase/Repo` 与 `ObjectStorage` 接口；点赞可放在 Post 用例中，不另建复杂领域。
- **Data**：sqlc 查询、事务锁、存储适配器；写 SQL 明确所有权与软删除条件，不使用“先查拥有者、后无条件更新”。
- **Job**：只调用维护用例/Repo，负责历史清理、过期上传回收和对象删除重试，不接管首期浏览/点赞/评论主写入。

Biz 不依赖生成的 SQL 模型和具体 OSS SDK；新构造器更新各层 `ProviderSet`，由 Wire 重新生成组装代码。避免直接调用当前可能带缓存的商品 `GetProduct` 来作事务内可见性判断。

### 6.2 写入事务与并发

- 浏览 UPSERT 使用数据库时间，必要时 `GREATEST` 保证最后时间不会因并发覆盖倒退；保留第一次上报时间。
- 帖子编辑/删除、点赞/取消、评论创建/删除统一先锁帖子行，再检查未删除状态，避免“帖子删除成功后又创建评论或点赞”。
- 需要锁评论或媒体时，保持“帖子 → 评论（按 ID）→ 媒体（按 ID）”的统一顺序；用户注销批量操作按帖子 ID 排序并复核锁顺序，避免引入反向死锁。
- 帖子创建尚无已有帖子可锁时，按媒体 ID 升序锁定资源；所有附件绑定流程均校验媒体状态，避免并发发布抢占同一图片。
- 评论根/目标的删除与回复创建参与同一锁协议；不会出现校验时有效、插入时根已删除却仍成功追加的竞态。
- 对于足迹/点赞重复请求依靠唯一键和条件 SQL；删除用受影响行数判定是否首次变更，不重复排入媒体清理任务。
- 一个操作只有一个明确的事务入口；现有 `TxManager.InTx` 不提供嵌套事务语义，Repo 不在不知情的情况下再次开启事务。
- 必须可靠执行的异步副作用通过 `RiverInsertClient` 在同一 PostgreSQL 事务内 `InsertTx`；不能用提交后的临时 goroutine 保证任务不丢失。

首期按帖子串行化互动写入，逻辑清晰但会限制极热门帖写吞吐；压测确认瓶颈后再优化锁粒度，不提前牺牲正确性。

### 6.3 Redis 与 River

- 新功能首期以 PostgreSQL 为唯一事实来源，浏览列表、帖子正文和互动数据先不增加缓存，减少权限泄露、删除后残留与个性化缓存污染风险。
- 后续按测量结果加入缓存时复用 `redisKey`、`afterCommit` 和 generation；共享内容缓存不能混入 `liked_by_me` 或私人浏览列表。
- generation 只能使旧列表键失效，不能保证所有读写竞态下强一致；删除/权限敏感的可见性不得只依赖旧缓存。
- Redis 可用于新增写接口限流，但项目当前没有通用限流器，需要单独实现和测试。发布、评论、上传建议按用户/IP 限额，部署开放用户写入前完成。
- 缓存故障可数据库回源，不能据此绕过现有 JWT 黑名单校验。限流故障策略也需单独配置，默认对上传/发布保守拒绝，不无限制放行。
- 新增 `maintenance` 与 `media` 队列，配置独立并发数，避免清理任务占满支付队列。
- 历史清理按截止时间和有限批次执行，锁定待删行后再次检查过期条件；不能将已重新浏览的商品足迹误删。
- 清理 Worker 重复执行安全，单批可恢复；超过重试上限可观测并可补偿。新增非支付任务错误不能错误写入支付对账失败表，需检查现有 `PaymentRiverErrorHandler` 的分派逻辑。

## 7. OSS 分阶段接入

当前只有商品/活动媒体元信息，没有实际上传服务。**不能把存一个 URL 或测试桩当成图文上传已经交付。**

### 7.1 先建立可替换接口

- 定义 `ObjectStorage` 能力：创建受限上传凭证、检查对象、生成读取 URL、删除对象。
- 媒体 Repo 管理 `pending → ready → deleting → deleted` 状态；发布只接受 `ready` 资源。
- 单元测试使用 fake 存储；开发可使用本地测试适配器，但不得作为生产可用性的验收依据。
- 新增独立 Media API，不直接复用商品 API 中允许客户端传 bucket/key 的 `MediaInfo` 作为发布入参；返回 DTO 可保持相似展示字段。

### 7.2 完成真实上传闭环

| RPC | HTTP | 行为 |
| --- | --- | --- |
| `CreateImageUpload` | `POST /v1/media/images/uploads` | 验证类型/大小声明，创建 `pending` 资源，返回限定对象的短期上传凭证 |
| `CompleteImageUpload` | `POST /v1/media/images/{id}/complete` | 校验当前用户归属，实际检查对象并验证格式、大小、尺寸后置 `ready`；重复完成幂等 |

- 存储厂商在 P0 确认；使用私有 bucket，服务端生成随机且不复用的 object key。
- 仅支持 JPEG/PNG/WebP；校验真实内容与解码尺寸、总像素，不只信任扩展名、请求 MIME 或对象元数据；首期不支持 SVG/HTML。
- 上传凭证限制 key、有效期和大小；客户端不能列举 bucket、覆盖其他对象或删除对象。
- 完成校验后绑定不可变对象版本，或将已验证上传复制到只读最终 key，避免凭证未过期时覆盖已经校验/发布的图片。
- 数据库存储稳定资源定位信息；读取时根据未删除帖子或上传者权限生成短期签名 URL，不存放即将过期的签名链接。
- 不主动抓取客户端提供的远程 URL，避免 SSRF；凭据从安全配置注入，不写入日志或提交仓库。
- 帖子删除后停止签发新读取 URL；已签发 URL 可能在短 TTL 内继续有效，这一边界需在验收中说明。若要求立即撤销，需采用鉴权代理读取或存储侧失效能力。

### 7.3 回收与补偿

- 未完成/未使用上传建议 24 小时后进入回收；解绑图片先标记 `deleting`，再以资源 ID 作为幂等标识登记 River 任务。
- 清理先锁资源并确认没有 `post_images` 引用，再提交不可重新绑定的 `deleting` 状态；外部删除发生在事务外。
- “检查无引用”和“阻止新绑定”必须原子完成，不能查完数据库直接删除 OSS，导致图片刚被绑定就被误删。
- 删除对象成功后标记 `deleted`；对象已不存在也视为成功。失败保留记录交给 River 重试，终态失败告警。
- 周期扫描兜底查找过期资源和滞留 `deleting` 资源，保证进程崩溃后可以恢复；不得复用旧 key 或删除仍被引用的对象。
- 补充 `internal/conf/conf.proto`、示例配置、部署文档；确认 `cmd/mall/main.go` 配置读取与 Wire 注入能拿到新增配置。

## 8. 文件改动清单

| 范围 | 计划新增/修改 |
| --- | --- |
| API | 修改 `api/user/v1/user.proto` 及错误定义；新增 `api/community/v1/community.proto`、`community_error.proto`；媒体阶段新增 `api/media/v1/media.proto`、`media_error.proto` |
| 数据库源文件 | init up/down；六份新 query 文件；必要的用户注销查询 |
| 生成产物 | API `*.pb.go`、HTTP/gRPC/errors 代码、`internal/data/db/*.go`、`db/mock/querier_mock.go`、`cmd/mall/wire_gen.go`、配置 pb、OpenAPI |
| Biz | 新增 `browsing_history.go`、`post.go`、`comment.go`、`media.go`、`storage.go`；调整用户注销编排及 `biz.go` |
| Data | 新增对应 Repo、`storage_oss.go`；修改 `data.go` provider/队列、事务感知的用户删除与缓存失效 |
| Service/Server | 扩展 `service/user.go`；新增 `service/community.go`、`service/media.go`；更新 provider、HTTP/gRPC 注册和构造器测试 |
| 后台任务 | 新增 `job/browsing_history_cleanup.go`、`job/media_cleanup.go`；扩展 `job.go`、周期任务组装及错误分派 |
| 配置与工程 | `conf.proto`、`configs/config.yaml`、`.env.example`、`cmd/mall/main.go`（按需）、`Makefile`、CI 集成测试 job |
| 测试与说明 | 各层 `_test.go`、新 integration 测试、上传/足迹客户端调用文档 |

注意：当前 Makefile 的 API/config 目标使用相对 `api/`、`third_party/`、`internal/`，但仓库目录不完全位于同一层；且 `make init` 没有安装 `protoc-gen-go-errors`、sqlc、mockgen。实施时先修正为明确的仓库根/应用目录路径并固定工具版本，不能假设 `make all` 当前即可成功。根目录和 `app/mall/` 都有 OpenAPI 文件，需确定唯一生成入口并明确镜像文件是否保留，避免两份规格漂移。

## 9. 实施阶段与交付物

各阶段已按顺序实施；生成文件、Wire 和共享迁移集中生成。以下勾选项对应本地实现和验收，不代表已部署生产。

### P0：确认契约与工具链

- [x] 确认 90 天足迹保留期、字数/图片限制、直接发布和删除策略。
- [x] 确认一层楼中楼语义、回复删除根的限制、是否需要创建请求幂等键。
- [x] 确认 OSS 厂商、开发适配器、签名读取 TTL、凭据注入和上线限流规则。
- [x] 确认用户注销与现有交易外键的边界，不将本功能扩展成完整注销系统重构。
- [x] 明确生成命令、固定依赖工具版本和 OpenAPI 输出路径。

**验收**：API 草案、领域规则和生成流程可执行，无“图片随意传 URL”“多层递归评论”等未决核心问题。

### P1：Schema、sqlc 与约束测试

- [x] 完成六张新表、索引、外键、评论结构触发器及 up/down。
- [x] 实现 UPSERT、游标分页、按页聚合、条件删除、媒体绑定和锁查询。
- [x] 生成 sqlc 与 mock，更新受影响测试编译。
- [x] 使用可丢弃测试数据库验证 up → down → up，直接 SQL 不能插入非法评论层级。

**验收**：数据模型独立于 HTTP 层也能守住唯一关系、跨帖引用和评论层级不变量。

### P2：浏览记录完整闭环

- [x] 完成 User RPC、Service、Biz、Data 与依赖注入。
- [x] 实现显式上报、可用商品校验、分页、删除、清空和过期过滤。
- [x] 接入 River 分批清理及维护队列。
- [x] 完成所有权、重复上报、分页移动、清空并发和清理重试测试。

**验收**：用户能看到自己的最近商品足迹，其他用户不能访问或删除；足迹失败不影响商品详情；清空后不会被旧后台任务恢复。

### P3：帖子、点赞与媒体抽象

- [x] 实现媒体抽象及测试适配器，不将其标记为真实 OSS 已完成。
- [x] 完成图文发布、图片顺序/归属、详情/列表、版本编辑及软删除。
- [x] 完成幂等点赞/取消和批量统计，避免 N+1 查询。
- [x] 接入用户注销时的社区清理事务与回滚测试。

**验收**：用已验证测试资源可完成完整帖子流程；跨用户资源复用被拒绝；并发点赞不重复，版本冲突不覆盖新内容。

### P4：评论与楼中楼

- [x] 完成根评论推导、评论/回复独立分页和回复目标摘要。
- [x] 实现作者/管理员删除、根占位、正文清除和楼层关闭规则。
- [x] 校验创建/删除并发下的锁顺序、帖子可见性与计数口径。

**验收**：回复某条回复仍归属原根评论，响应没有第三层；删除根不误删其他人的回复，已删除正文不可读取。

### P5：真实 OSS、限流与上线验收

- [x] 完成受限上传、内容校验、不可变最终对象和鉴权读取。
- [x] 完成未绑定资源回收、解绑删除任务、失败重试及周期兜底。
- [x] 完成用户写接口限流、日志脱敏、关键指标与告警。
- [x] 补齐 HTTP/gRPC、数据库/River/Redis、真实 OSS 测试 bucket 的验收链路。
- [x] 更新配置/调用文档及 OpenAPI，运行全量回归，确认不影响现有订单支付链路。

**验收**：真实图片能上传、发布、读取、编辑解绑和安全清理。P3 的 fake 验收不能替代本阶段。

## 10. 测试与验证清单

### 10.1 单元与接口测试

| 模块 | 必须覆盖 |
| --- | --- |
| 浏览记录 | claims 来源、跨用户隔离、重复上报不增行、不存在/下架/已删商品、过期过滤、空列表、删除/清空幂等、同时间戳分页 |
| 帖子 | 空白/超长文本、Unicode 字数、图片 0/1/9/10 张、他人图片、未完成上传、顺序调整、版本冲突、作者与管理员权限、删除后不可见 |
| 点赞 | 同用户并发点赞仅一行、反复取消、多个用户聚合、当前用户标记不串用户、删除帖子后拒绝新增点赞 |
| 评论 | 一级、回复一级、回复回复仍两层、跨帖/跨根、伪造/不存在/已删目标、已删根关闭楼层、根占位、删除回复目标脱敏、计数口径 |
| 注销 | 浏览/点赞清理、本人帖子隐藏、评论清空、他人回复保留、媒体任务一致性；订单外键导致失败时整笔回滚 |
| 媒体 | 伪造 MIME、超大小/尺寸、SVG 拒绝、过期凭证、越权完成、校验后覆盖、重复完成、被引用资源不删除 |
| 传输与鉴权 | HTTP/gRPC 均未放宽白名单；无 token、黑名单、非作者修改、非管理员删除、`me` 路由、统一错误码 |

### 10.2 真实基础设施集成测试

沿用 `internal/data/correctness_integration_test.go` 的 `//go:build integration`、环境变量和测试数据库名称保护，不依赖 mock 证明事务锁正确。

- 并发浏览 UPSERT 与清空、过期清理与重新浏览。
- 同时编辑/删除帖子与点赞/发评论；并发删除根与回复根下目标。
- 直接 SQL 插入跨帖根、第三层根、跨根回复和修改关系，确认被数据库拒绝。
- 用户注销失败时事务、River 入队和缓存失效均不留下部分成功结果。
- 媒体绑定与清理竞态、OSS 删除成功但数据库状态回写失败后的重复执行。
- River 事务回滚不产生任务，Worker 重启/重试不重复删除有效对象。
- PostgreSQL 查询计划符合预期，列表查询次数不随返回条数线性增长。
- Redis 运行时故障不把数据库已成功的普通读取变成错误，但鉴权/限流按各自策略处理，不把整体服务依赖宣称为可无 Redis 运行。

现有集成 fixture 使用 Redis DB 15 并调用 `FlushDB`；新增测试必须使用独立 Redis 实例或串行执行，不能并行清空共享测试库。

### 10.3 计划执行的命令

以下验证入口已执行通过；真实集成环境为独立 PostgreSQL 16、Redis 7、MinIO 测试容器，不使用共享库：

```bash
# 仓库根目录：protoc 36.1，Go 工具版本由 Makefile 固定
make -C app/mall generate  # API/OpenAPI、配置、sqlc/mock、Wire 串行生成

# Wire：API/配置代码生成成功后执行
(cd app/mall/cmd/mall && go generate ./...)

# 全量回归与静态检查
go test ./...
go test -race ./...
go vet ./...

# 专用集成数据库名称必须包含 integration；仅使用可丢弃数据库和独立 Redis
export ECOMMERCE_INTEGRATION_EXCLUSIVE=true # 确认三个依赖为独占、可丢弃实例
export ECOMMERCE_INTEGRATION_POSTGRES_URL='<专用 integration 测试数据库 DSN>'
export ECOMMERCE_INTEGRATION_REDIS_ADDR='<独立测试 Redis 地址>'
export ECOMMERCE_INTEGRATION_S3_ENDPOINT='<MinIO 测试端点 host:port>'
export ECOMMERCE_INTEGRATION_S3_BUCKET='<名称包含 integration 的私有 bucket>'
# 安全注入 ECOMMERCE_INTEGRATION_S3_ACCESS_KEY 和 ECOMMERCE_INTEGRATION_S3_SECRET_KEY
# 三种依赖均必须是专用测试实例；不并行运行多份 suite。
bash .github/scripts/integration-test.sh # 检查全部必需环境变量后，串行运行上述包的 -race 集成测试

# 检查差异及生成产物是否出现非预期改动
git diff --check
```

API/config 生成已归入 `make -C app/mall generate`。根 `openapi.yaml` 是唯一生成入口，应用目录保存自动镜像；重复生成内容一致。CI 已增加 PostgreSQL/Redis/MinIO 独立集成任务并检查环境变量，避免全部 skip 被误认为通过。

## 11. 最终完成标准

- [x] 所有接口均从可信 claims 获取操作者身份，不泄露足迹、手机号或实名。
- [x] 足迹按用户和商品去重，可删除/清空，保留期明确且查询与清理一致。
- [x] 图文帖子真实上传链路可用，媒体归属、不可变对象和失败清理均经过验证。
- [x] 点赞/取消幂等，点赞及评论数量与关系数据一致，没有 Redis 独有状态。
- [x] 评论结构始终最多两层，可回复指定评论，数据库和业务层均阻止跨帖/跨根及非法深度。
- [x] 删除、注销、并发回复与资源回收不存在部分成功或越权复活。
- [x] 迁移、sqlc、mock、Proto、Wire、OpenAPI、配置与测试同步交付。
- [x] 支付、订单及现有用户商品功能回归通过；新增队列不抢占支付任务资源。


## 12. 实施记录（2026-09-08）

- **P0 决策**：采用文档默认限制和删除语义；创建帖子/评论不增加幂等键，客户端不得盲目重试。存储按用户确认采用厂商无关接口，当前 MinIO/S3，上传/读取 TTL 为 10/5 分钟。
- **关键实现**：六张表与评论结构触发器；当前用户浏览记录；帖子/点赞/两层评论；私有两阶段上传、内容校验与条件不可变写入；事务内媒体任务、周期补偿；用户注销联动、Redis 原子限流。
- **并发协议**：用户行 → 按 ID 排序的帖子 → 评论 → 按 ID 排序的媒体。媒体完成与对象删除另持 session advisory lock，但 OSS 调用不在 SQL 事务内；清理等待上传凭证过期及宽限。
- **真实测试**：历史清空/重新浏览与过期清理；跨帖/跨根/第三层直接 SQL；删除根占位与目标脱敏；注销失败时内容、River、缓存全部回滚；媒体验证与删除竞态、外部删除后 DB 回写失败；真实 River 自动重试；真实 MinIO 上传、发布、读取、编辑换图、解绑及清理。
- **工程验证**：`make generate` 全流程及重复生成哈希一致；`go test ./...`、`go test -race ./...`、`go vet ./...`、编译和真实集成测试通过。HTTP/gRPC 均覆盖未登录、黑名单、权限和错误码；示例配置读取与实际应用启动通过。
- **查询验证**：3000 条帖子与足迹数据执行 `EXPLAIN (ANALYZE, BUFFERS)`，分页、统计、根占位和清理使用相应索引；返回 1/20/50 帖均为三次 SELECT；本轮增加只读快照事务后另有 BEGIN/COMMIT，总计五次数据库调用。未把此项宣称为生产规模压测。
- **原有测试修正**：新启用真实集成 CI 暴露两处旧断言：共享 fixture 的库存应按调用前库存减 2；支付缓存应使用现有 generation key 构造函数。只更新测试断言，未改变订单、支付或退款业务代码。
- **上线边界**：私有 bucket 和应用专用账号需部署方建立；生产 TLS/CORS、代理 IP 限流及容量压测另按环境配置。已签 URL 在 TTL 内可能继续有效；版本化 bucket 需独立配置历史版本回收策略。初始化迁移不会自动作用于已标记版本 1 的数据库，不自动清空共享数据库。

## 13. 本轮修复与复验（2026-09-09）

- **缺失接入**：补齐 init up/down、评论同帖外键与不可变两层关系触发器、配置 Proto、User 足迹 RPC/errors、sqlc/models/Querier/mock、ProviderSet/Wire、HTTP/gRPC 注册及限流/请求体中间件。生产 UserRepo 绑定为社区注销包装器，在同一事务内清理内容、入队并删除用户，缓存提交后失效。
- **并发修复**：帖子 Get/List 使用只读 REPEATABLE READ，写后读取复用原事务；以确定性屏障覆盖 Get/List 与替换、重排、移除图片及删帖并发。媒体新过期与滞留 deleting 各占独立预算；超过十批失败资源仍不能饿死新资源，已有回归测试。
- **后台与工程**：maintenance/media 队列、worker、周期补偿、终态失败观测和 Prometheus 规则已接通。生成入口固定工具版本、明确仓库/应用路径并串行执行；`make -j8 -C app/mall generate` 重复生成哈希一致，根和应用 OpenAPI 完全一致。
- **实际命令结果**：`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build`、`sqlc compile`、`git diff --check` 通过；独占测试实例上的 `.github/scripts/integration-test.sh` 通过，含真实 PostgreSQL/Redis/River/MinIO。CI YAML/Compose 解析及集成入口缺环境变量时拒绝运行的检查通过；未声称 GitHub Actions 已远端执行。
- **应用启动**：实际编译并运行完整 Wire 应用，通过 HTTP 验证注册/登录、无图创建/编辑、超过九图拒绝、点赞、两层评论/根占位、足迹路由及注销后帖子不可见；不只依赖模拟仓库的传输测试。
- **旧测试夹具修正**：库存断言改为相对调用前库存；支付缓存断言使用现有 generation key；并发幂等订单测试只共享幂等键，每次生成不同订单号，与现有 Biz 行为一致（避免同时触发两个唯一约束）。该旧测试额外连续运行五次通过，未修改订单/支付生产逻辑。
- **保留边界**：已有版本 1 数据库不会自动重跑 init，不自动重建共享库。私有 bucket、专用账号、TLS/CORS、代理限流和容量规划仍由部署方落实。短期签名 URL 不能即时撤销；存储端超时/取消后仍提交以及跨过期的在途上传不在本轮已验证保证内，需配合存储边界验证及生命周期/孤儿对象巡检。参见调用说明。
