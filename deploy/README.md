# Caddy + GitHub Actions 部署

## 拓扑与域名

部署使用四份配置，顺序不能改变：

```text
docker-compose.yml + docker-compose.test.yml + docker-compose.caddy.yml + docker-compose.registry.yml
```

`docker-compose.registry.yml` 把 PostgreSQL、Redis、Jaeger、OTel、Prometheus、Loki、Promtail、Grafana、Caddy 换成 ACR 中的镜像副本（`deploy/mirror-images.txt` 是唯一清单）。测试服务器无法访问 Docker Hub，CI 在部署前把这些镜像复制进 ACR，服务器只从 ACR 拉取。该 overlay 要求 `DEPENDENCY_REGISTRY=<ACR_REGISTRY>/<ACR_NAMESPACE>`，由 workflow 传入，不写进服务器 `.env`，也没有任何公网兜底默认值。

本栈只部署**后端、存储和监控**。前端由自己的发布流程独立部署，可以放在任意静态托管/CDN，与本栈不共享域名、容器或发布目录，因此这里没有前端站点、静态根目录或 `WEB_DOMAIN`。

| 环境变量 | 必填 | 路由 |
| --- | --- | --- |
| `API_DOMAIN` | 是，如 `api.example.com` | `mall:8000` |
| `MINIO_DOMAIN` | 是，如 `minio.example.com` | `minio:9000`，S3 API |
| `CONSOLE_DOMAIN` | 是，如 `console.example.com` | `minio:9001`，管理控制台 |

三个域名只有示例值，仓库里没有真实域名的兜底值：Compose 用 `${VAR:?}` 校验，缺失时拒绝启动；`deploy/Caddyfile` 也不设默认，空值会让 Caddy 加载失败，避免给错误的主机名签发证书。真实域名只写在服务器 `.env` 中。

为上述三个域名分别添加 A 记录，或添加覆盖它们的通配符 A 记录（例如 `*.example.com`），指向服务器公网 IP。Caddy 仍按三个具体域名自动管理证书，**不是申请通配符证书，不需要 DNS API 凭据**。环境变量不要填协议、路径或端口，三个域名必须不同。前端域名不需要指向本服务器，也不需要出现在本栈 DNS/证书中。

Caddy 自动签发/续期 HTTPS，公网只发布 80/tcp、443/tcp、443/udp（HTTP/3，可选）。数据库、Redis、后端 HTTP/gRPC、MinIO API/Console 均不发布宿主机端口。监控 UI 保留 loopback 绑定。SSH 端口限制管理员来源。

Caddy 的独立 `edge` 网络地址固定为 `172.30.50.10`，后端只信任这个 `/32` 代理地址。存储域名在该网络中是 Caddy 的别名，后端通过 HTTPS 访问 Caddy，再转发至本机 Docker 网络中的 MinIO；不依赖公网回环，不关闭证书校验。Caddyfile 刻意不配置全局 `trusted_proxies`：未配置时 Caddy 会把 `X-Forwarded-For` 覆写为本跳客户端地址，客户端伪造的链路不会传到后端。首次部署检查 `172.30.50.0/24` 与宿主机/VPN/其他 Docker 网络不冲突。若修改网段，同时修改 `ipv4_address` 和 `HTTP_TRUSTED_PROXY`。

Console 除 MinIO 自身登录外还受 **Caddy 来源 IP 白名单**限制。默认只允许 loopback，因此公网访问返回 403；必须将管理员公网 IP/CIDR 写入 `CONSOLE_ALLOWED_CIDRS`。不要设为 `0.0.0.0/0`，不要依赖可伪造的 X-Forwarded-For。当前设计要求 Caddy 直接面对公网，不适合未经调整就置于 CDN/其他代理后面。

### 跨域（CORS）

前后端分开部署后，浏览器来源在部署期未知。默认配置放开 CORS：

| 配置项 | 默认 | 作用 |
| --- | --- | --- |
| `HTTP_CORS_ALLOWED_ORIGINS` | `*` | 后端 API 的浏览器来源白名单；逗号分隔，`*` 表示允许一切来源 |
| `MINIO_API_CORS_ALLOW_ORIGIN` | `*` | MinIO S3 API 的浏览器来源；浏览器用预签名 URL 直传/读取 |

- API 的 CORS 由后端中间件执行，Caddy **不再**添加任何 CORS 头。不要在两处同时配置，重复的 `Access-Control-Allow-Origin` 会被浏览器拒绝。
- `*` 会回显请求的 `Origin` 并允许携带凭据，因此既支持 Bearer Token 也支持 Cookie 客户端。允许一切来源意味着任何网站都能在用户浏览器中调用 API；身份认证仍然生效，但不能把 CORS 当作访问控制。
- 前端 origin 固定后可改为显式列表，例如 `HTTP_CORS_ALLOWED_ORIGINS=https://web.example.com,https://admin.example.com`；MinIO 同样可收窄为 `https://web.example.com`。不要填路径或通配符模式。
- MinIO 的 bucket 必须保持私有。允许任意来源只影响浏览器跨域读取，不会让未签名的对象变成公开对象；预签名 URL 在 TTL 内等同于临时凭据。

## 1. 服务器准备

需要 Linux `x86_64`（发布镜像是 `linux/amd64`）、Bash、Docker Engine、Docker Compose **>= 2.24.4**、`flock`（util-linux）、tar、gzip。SSH 专用部署用户须有 Docker 权限并能写部署目录；Docker 权限相当于高权限。

为三个域名添加 A 记录。只有 IPv6 完全可用时才添加 AAAA。开放 80/443，确保没有 Nginx/其他服务占用。服务器须能访问公网 ACME 服务与阿里云 ACR；**不需要**能访问 Docker Hub，第三方镜像全部由 CI 镜像到 ACR。

```bash
sudo install -d -m 700 -o "$USER" -g "$(id -gn)" /opt/simple-ecommerce-test
cp deploy/test.env.example /opt/simple-ecommerce-test/.env
chmod 600 /opt/simple-ecommerce-test/.env
```

编辑 `.env`，填写：

- 把三个 `example.com` 示例域名换成真实域名；填写管理员来源 IP。域名留空时 Compose 直接拒绝启动。
- 前后端分离部署时保持 `HTTP_CORS_ALLOWED_ORIGINS=*`；前端 origin 固定后再收窄。
- 启用支付时同步修改 `PAYMENT_CALLBACK_BASE_URL` 与 `ALIPAY_NOTIFY_URL` / `WECHAT_NOTIFY_URL`，它们必须是公网 API 域名，不能写 Docker 服务名。
- PostgreSQL、Redis、Grafana 密码和三个不同的认证密钥，分别用 `openssl rand -hex 32` 生成。不要每次发布重新生成，尤其妥善保存 `AUTH_PHONE_SECRET`。
- `DATABASE_SOURCE` 与数据库密码一致；特殊字符需要 URL 编码。`sslmode=disable` 仅用于不发布端口的本机受信 Docker 网络。建议正式运行时分离运行账号和迁移账号。
- 独立的 MinIO 管理员与应用账号；不能把 root 凭据用于应用。
- 正常发布无需在 `.env` 固定 `MALL_IMAGE` / `MINIO_IMAGE`，workflow 会传入两个构建结果的 digest，覆盖旧值。服务器会自动拉取，不再要求预加载 MinIO 镜像。

从旧版 `.env` 升级时，`WEB_DOMAIN`、`WEB_ROOT` 和 `APP_IS_PRODUCTION` 已不再读取，可以删除；前端的 DNS、静态目录和发布流程均不在本栈内。新增的 `HTTP_CORS_ALLOWED_ORIGINS` / `MINIO_API_CORS_ALLOW_ORIGIN` 缺省即为 `*`，但建议显式写入 `.env` 便于审计。

后端和 MinIO 共享一个 ACR 仓库，使用不同标签，不能使用相同标签互相覆盖：

```text
<ACR_REGISTRY>/<ACR_NAMESPACE>/aiyoung:mall-<commit SHA 后 8 位>
<ACR_REGISTRY>/<ACR_NAMESPACE>/aiyoung:minio-<commit SHA 后 8 位>
```

注意是 SHA **末尾** 8 位，不是 `git rev-parse --short` 的前缀。短标签用于识别版本，部署和回退使用各自的完整 digest；短标签仍可能碰撞，不能代替不可变引用。MinIO 镜像由 `deploy/minio.Dockerfile` 构建，并缓存编译结果；评估并定期维护其固定源码版本，修改前备份对象数据并检查升级兼容性。

### 前端

本仓库没有前端源码，后端 workflow **不构建、不托管、不探测前端**。前端发布流程只需把 API base URL 配置为 `https://<API_DOMAIN>`，并把存储公网地址配置为 `https://<MINIO_DOMAIN>`。前端域名、静态文件目录和 CDN 均不由本栈管理；前端更新也不会触发或阻塞后端部署。

## 2. 首次启动入口和 MinIO

先完成第 4 节的 ACR 凭据配置。首次可以直接运行 workflow，启动全部服务后再按第 3 节创建 bucket/IAM；初始化完成前不要开放媒体业务。部署探测不要求 bucket 已存在。

如果已有发布的 MinIO 镜像，也可以从临时 checkout 单独启动入口和存储。将下面镜像引用替换为实际已发布的标签或 digest；始终使用固定项目名 `ecommerce-test` 复用已有数据卷。该命令虽然只启动 `caddy` 和 `minio`，但仍要从 ACR 拉取 `mirror-caddy`，因此需要先让 workflow 完成一次镜像同步，或手工把 `mirror-caddy` 推送到 ACR：

```bash
DEPENDENCY_REGISTRY='<ACR_REGISTRY>/<ACR_NAMESPACE>' \
MINIO_IMAGE='<ACR_REGISTRY>/<ACR_NAMESPACE>/aiyoung:minio-<SHA后8位>' \
MALL_IMAGE=bootstrap-only docker compose \
  --project-name ecommerce-test \
  --env-file /opt/simple-ecommerce-test/.env \
  -f docker-compose.yml -f docker-compose.test.yml -f docker-compose.caddy.yml \
  -f docker-compose.registry.yml \
  up -d --no-deps caddy minio
```

这里不启动后端或数据库。Caddy 会申请证书；API 暂时 502 是正常的。检查（`$MINIO_DOMAIN` 需先按 `.env` 中的实际值 export）：

```bash
curl --fail "https://$MINIO_DOMAIN/minio/health/ready"
```

若证书签发失败，检查 DNS、AAAA、80/443、安全组、端口占用、CAA 限制和 Caddy 日志。

Caddy 不会重写 S3 的 Host、路径或查询参数。上传上限为 20 MB，以容纳项目 10 MiB 图片和 multipart 开销。MinIO 的 CORS 默认允许任意前端来源（`MINIO_API_CORS_ALLOW_ORIGIN`），API 的 CORS 由后端 `HTTP_CORS_ALLOWED_ORIGINS` 控制，Caddy 不参与。CORS 不是身份验证，bucket 必须保持私有。

## 3. 初始化私有 bucket 和应用权限

使用独立安装的 MinIO Client `mc`。以下变量由管理员手动设置，避免密码写入共享 shell 历史；不要将管理员凭据传入 Actions。`.env` 不会被脚本 source，执行前请按其中实际值自行 `export MINIO_DOMAIN` / `CONSOLE_DOMAIN` 等变量。

```bash
mc alias set test-minio "https://$MINIO_DOMAIN" "$MINIO_ADMIN_USER" "$MINIO_ADMIN_PASSWORD"
mc mb --ignore-existing test-minio/ecommerce-test-images
mc anonymous set none test-minio/ecommerce-test-images

sed 's/ecommerce-images/ecommerce-test-images/g' \
  deploy/minio-app-policy.json > /tmp/ecommerce-test-policy.json
mc admin policy create test-minio ecommerce-test-app /tmp/ecommerce-test-policy.json
mc admin user add test-minio "$STORAGE_ACCESS_KEY" "$STORAGE_SECRET_KEY"
mc admin policy attach test-minio ecommerce-test-app --user "$STORAGE_ACCESS_KEY"
```

此处域名和 bucket 必须与实际配置一致（命令中的 `$MINIO_DOMAIN` 等于 `.env` 里的值）。将应用账号写入服务器 `.env`，而非 GitHub Secrets。后端不会自动建桶或创建 IAM；部署就绪检查也不能代替权限验证。

访问 Console：`https://<CONSOLE_DOMAIN>`，来源 IP 必须在白名单内，然后使用 MinIO 账号登录。Caddy 会自动代理 WebSocket，`MINIO_BROWSER_REDIRECT_URL` 已同步设置为 Console 域名。

## 4. GitHub Actions

保留 GitHub Environment **testing** 与已有 Secrets，无需重新命名：

| Secret | 含义 |
| --- | --- |
| `TEST_SSH_HOST` | 服务器 IP/域名，不带协议 |
| `TEST_SSH_USER` | 专用部署用户 |
| `TEST_SSH_PRIVATE_KEY` | 非交互部署私钥 |
| `TEST_SSH_KNOWN_HOSTS` | 已通过可信渠道核验的服务器 host key；非 22 端口需 `[host]:port` |
| `ACR_USERNAME` | ACR Docker 登录用户名，具备目标仓库推送权限 |
| `ACR_PASSWORD` | 对应的 ACR 访问密码，不是直接填写 AccessKey Secret |

Variables（`ACR_REGISTRY`、`ACR_NAMESPACE` 无仓库默认值，必须显式设置）：

| Variable | 默认值 |
| --- | --- |
| `ACR_REGISTRY` | **必填**，控制台提供的公网 Registry 域名，不带协议/路径 |
| `ACR_NAMESPACE` / `NAMESPACE` | **必填**（二选一，`ACR_NAMESPACE` 优先）；没有 vendor 默认值，缺失时 `image-metadata.sh` 直接失败。GitHub 变量名不区分大小写 |
| `TEST_SSH_PORT` | `22` |
| `TEST_DEPLOY_PATH` | `/opt/simple-ecommerce-test` |

提前在 ACR 创建私有仓库 `<ACR_NAMESPACE>/aiyoung`，仓库名固定为 `aiyoung`。GitHub 托管 runner 需要可达的公网 Registry；服务器当前使用同一入口，不自动替换为 VPC 地址。

最后将**仓库级** Variable `TEST_DEPLOY_ENABLED=true`。不要只放在 Environment 内，因为 job 条件在进入 Environment 前求值。建议为 testing 配置默认分支限制和人工审批。

以相同 SSH 部署用户在服务器预先登录 ACR，优先使用独立的只读拉取账号：

```bash
ACR_REGISTRY='控制台提供的Registry域名'
ACR_USERNAME='服务器拉取账号'
read -rs ACR_PASSWORD
printf '%s' "$ACR_PASSWORD" | docker login "$ACR_REGISTRY" -u "$ACR_USERNAME" --password-stdin
unset ACR_PASSWORD
```

CI 使用 GitHub Secrets 中的 ACR 推送凭据；不向服务器传递推送密码。`GITHUB_TOKEN` 不再用于镜像发布，已移除 `packages: write`。ACR 密码不能与 MinIO 管理员或应用密码混用。若服务器使用临时 ACR 凭据，需另行配置续期。

服务器拉取账号必须能读取同一命名空间下的 `mirror-*` 仓库（与 `aiyoung` 同级）。首次发布时 CI 会自动创建这些仓库并推送镜像，因此 ACR 需要允许推送时自动建仓库；若关闭了该选项，请预先手工创建 `mirror-postgres`、`mirror-redis`、`mirror-jaeger`、`mirror-otel-collector`、`mirror-prometheus`、`mirror-loki`、`mirror-promtail`、`mirror-grafana`、`mirror-caddy`。

后端、MinIO 以及全部第三方依赖都走 ACR。workflow 在构建镜像前先执行 `.github/scripts/mirror-images.sh`：按 `deploy/mirror-images.txt` 把 Docker Hub 镜像复制到 `<ACR_REGISTRY>/<ACR_NAMESPACE>/mirror-*`，只复制 `linux/amd64` 清单，已存在的目标标签直接跳过。因此首次发布需要 runner 能访问 Docker Hub，之后即使 Docker Hub 不可用也不影响部署。

镜像标签不会自动跟随上游浮动标签（例如 `postgres:16-alpine` 在 ACR 中固定为首次复制的内容）。升级依赖时同时修改 Compose 和 `deploy/mirror-images.txt` 中的版本，再删除或改用新的 ACR 标签；`deploy` 包的测试会校验三份 Compose 的字面镜像与清单、overlay 保持一致。

运行顺序：

1. 单元/竞态测试和一次性 PostgreSQL、Redis、MinIO 集成测试。
2. 独立 deployment-config job 检查脚本、四份 Compose 合并结果（含 ACR overlay），并用实际部署版本的 Caddy 验证配置、执行回环 HTTP 路由/CORS 透传/Console ACL 测试。只使用临时假凭据，不申请证书。
3. 仅默认分支、非 PR、显式启用后，先把 `deploy/mirror-images.txt` 列出的依赖镜像同步到 ACR（已存在则跳过），再分别构建 `linux/amd64` 后端及 MinIO 镜像（测试服务器只有 amd64；不启用 QEMU、不做多架构），推送到同一 ACR 仓库的 `mall-<末8位>` / `minio-<末8位>` 标签。镜像全部就绪后才部署。服务器若是 arm64，需同时修改 workflow 的 `platforms`、镜像同步的平台选择，并接受 MinIO 源码构建的耗时。
4. 校验 SSH host key，仅打包 Git 中已跟踪的部署文件，上传到独立 release 目录；服务器密钥、前端产物不打包。
5. 通过 SSH 传入两个独立 digest 和 `DEPENDENCY_REGISTRY`，按 digest 拉取后端、MinIO，并从 ACR 拉取 Caddy 及其余依赖镜像；检查 Caddy 配置后替换服务。任一镜像拉取失败都不会进入服务替换阶段。
6. 等待容器就绪，先验证 Caddy 对三个域名的 HTTPS 与 Console 上游（首次签发证书最多等约 2 分钟，失败时打印 Caddy 日志），再验证后端能否经 Caddy 访问存储 API。Console 允许预期的 403；全部通过后更新 `current`。

CI 的 MinIO 不连接服务器，不删除服务器对象；部署过程不建桶、不写入测试对象、不清理镜像或数据卷。workflow 与服务器 `flock` 都串行化部署。

## 5. 发布与验证

推送默认分支或手动运行 `Test and Deploy`。部署目录：

```text
/opt/simple-ecommerce-test/
  .env
  current -> releases/...
  releases/<sha>-<run>-<attempt>/
    image                    # 最近成功的后端 digest
    minio-image              # 同一次成功发布的 MinIO digest
    docker-compose.yml
    docker-compose.test.yml
    docker-compose.caddy.yml
    docker-compose.registry.yml
    deploy/
    app/mall/configs/
```

首次部署后从真实前端来源验证：登录 → 申请上传 → 用返回的 POST 表单直传 MinIO → 通知完成 → 验证签名图片读取。不能把返回的 POST 上传凭据当成 PUT URL。确认匿名直接读取对象被拒绝、预检请求返回预期的 CORS 头、Console 非白名单来源返回 403。前端 origin 变化不需要重新部署后端，因为默认允许所有来源；若已收窄为显式列表，则需同步更新 `.env` 并重新部署。

`/` 返回 404 只证明后端 HTTP 启动完成，不是深度健康检查。部署检查不能证明所有数据库权限、bucket/IAM 或业务流程正常。

### 资源与持久化

原栈含 MinIO 的内存上限合计 3072 MiB，Caddy 增加 128 MiB，总计 3200 MiB；4 GiB 服务器仅剩约 896 MiB 给系统、dockerd 和突发，比原部署更紧。建议 8 GiB，或在实际测量后精简监控。不要在小服务器同时构建镜像。内存上限和 Go 软限制不能保证任意负载下不 OOM。

PostgreSQL 允许使用少量宿主机已有 swap；Redis 保持 `noeviction`，满载会影响登录/限流等状态写入，需监控。Jaeger 内存存储重启丢失追踪；Loki 14 天、Prometheus 15 天保留期仍生效。

保留并异机备份 PostgreSQL、MinIO 对象、服务器密钥、部署配置；Caddy `/data` 保存证书与 ACME 账号。持久卷不是备份。**不要执行 `docker compose down -v`。** 另行配置 Docker 日志轮转和磁盘报警。

### 失败与回退

启动默认执行数据库/River 迁移，失败不会自动回滚数据库或容器。`current` 只代表最近一次成功发布，不保证失败后运行中的容器仍然是旧版本。

确认数据库 schema 和 MinIO 数据格式均兼容后，可回退到已成功部署的 release。MinIO 降级不一定安全，不兼容时应保留当前兼容的 MinIO digest，仅回退后端：

```bash
release=/opt/simple-ecommerce-test/releases/CHOSEN_RELEASE
bash "$release/deploy/deploy-test.sh" \
  /opt/simple-ecommerce-test \
  "$(head -n 1 "$release/image")" \
  "$(head -n 1 "$release/minio-image")" \
  '<ACR_REGISTRY>/<ACR_NAMESPACE>'
```

回退复用当前服务器 `.env`，不恢复旧配置或数据。前端由独立流程发布和回退。上线支付仍受项目约束：微信是桩实现，支付宝仅支持沙箱。
