# GitHub Actions 部署测试环境

## 部署方式

默认分支通过单元测试和**隔离的 CI 集成测试**后：

1. 构建 `linux/amd64`、`linux/arm64` 业务镜像，推送到 `ghcr.io/<owner>/<repo>/mall:<commit SHA>`。
2. 通过校验服务器 host key 的 SSH 连接，将 Compose、配置文件和部署脚本上传到服务器的独立 release 目录。
3. 服务器按镜像 **digest** 拉取业务镜像并执行 Compose，等待 HTTP 启动检查和 MinIO API 就绪。
4. 成功后更新 `current` 链接。数据库、日志、MinIO 对象使用固定 Compose 项目 `ecommerce-test` 的持久卷，不执行 `down -v` 或镜像清理。

部署默认关闭；PR、非默认分支均不能部署。也可在 Actions 手动运行默认分支的工作流。部署串行执行且不在中途自动取消，开始前拒绝已落后于默认分支的旧 revision，避免较慢的旧测试覆盖新版本。

CI 的 MinIO 仍是一次性隔离容器，**不会连接测试服务器的 MinIO**。服务器端直接使用已有 MinIO 镜像，不重新构建、不自动拉取该镜像。仅有镜像不等于已有 bucket、账号或持久化数据。

## 1. 服务器准备

需要 Linux、Bash、Docker Engine、Docker Compose **>= 2.24.4**、`flock`（util-linux）、tar。SSH 用户须有 Docker 权限，并能写部署目录。Docker 权限等同于高权限，请使用专用部署账号。

服务器需要能拉取 GHCR 和 Compose 依赖镜像。若无法访问 GHCR，应先配置网络/镜像代理，或另行改用可访问的镜像仓库；SSH 只传部署文件，不传业务镜像 tar。

在本地仓库或服务器临时 checkout 中准备配置：

```bash
# 服务器上执行；目录所有者应为部署账号。
sudo install -d -m 700 -o "$USER" -g "$(id -gn)" /opt/simple-ecommerce-test
cp deploy/test.env.example /opt/simple-ecommerce-test/.env
chmod 600 /opt/simple-ecommerce-test/.env
# 用编辑器填写 /opt/simple-ecommerce-test/.env，不要提交真实配置。
docker image ls
```

必须填写 PostgreSQL、Redis、Grafana 密码及三个不同的随机认证密钥（每个至少 32 字节），并让 `DATABASE_SOURCE` 与 PostgreSQL 密码一致；DSN 中的特殊字符需要 URL 编码。配置中的 `sslmode=disable` 只适用于这个不发布数据库端口的隔离 Docker 网络。

### 方式 A：已有 MinIO 镜像，尚未启动服务

在服务器 `.env` 中设置：

- `COMPOSE_PROFILES=storage`。
- `MINIO_IMAGE` 为 `docker image ls` 中的**准确镜像名和标签**，也可用本地存在的 digest。镜像须支持 `/bin/sh` 和 `minio server`，与当前 Compose 启动方式兼容。
- 单独设置强随机的 `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD`，**不要作为应用凭据**。
- `STORAGE_ENDPOINT=minio:9000`。

为便于在首次业务部署前创建 bucket，可从临时 checkout 仅启动 MinIO（不会启动业务服务）：

```bash
MALL_IMAGE=bootstrap-only docker compose \
  --project-name ecommerce-test \
  --env-file /opt/simple-ecommerce-test/.env \
  -f docker-compose.yml -f docker-compose.test.yml \
  --profile storage up -d --no-deps minio
```

MinIO 数据保存在 `ecommerce-test_minio_data` 卷。API 默认只绑定服务器 `127.0.0.1:9002`，控制台绑定 `127.0.0.1:9003`。切勿执行 `docker compose down -v`。

### 方式 B：MinIO 容器/服务已经运行

- 将 `COMPOSE_PROFILES=` 留空，部署脚本不会启动、拉取、重建或管理既有 MinIO。
- 将 `STORAGE_ENDPOINT` 指向**业务容器可达**的现有 MinIO API，例如 `oss-test.example.com:443`，相应设置 `STORAGE_USE_TLS=true`。
- 不需要在应用部署配置中填 MinIO 管理员凭据。
- 既有 MinIO 的网络、端口和数据卷由原来的部署方式管理。容器内的 `localhost` 是业务容器自身，不能用它访问宿主机服务。

## 2. 配置 OSS 入口、bucket 和应用账号

`STORAGE_PUBLIC_ENDPOINT` 必须是浏览器可访问的 **S3 API** 地址，例如 `oss-test.example.com`，不带 `https://` 或路径。不要填 Docker 服务名、localhost 或 MinIO 控制台端口，否则预签名上传/读图会失败。

建议使用 HTTPS 反向代理转发到 `127.0.0.1:9002`，**保持原始 Host 和请求路径，不添加 /minio 等前缀**，避免破坏 S3 签名；同时配置前端实际需要的 CORS、上传体积和超时限制。若使用公网 HTTP，临时签名 URL 会明文传输，不建议这样测试。

示例配置仅在隔离 Docker 网络内部使用 HTTP，因此显式设置 `STORAGE_USE_TLS=false`、`STORAGE_ALLOW_INSECURE=true`，但公网必须保持 `STORAGE_PUBLIC_USE_TLS=true`。如两条链路均支持 TLS，改为两项 TLS 都为 `true`、`STORAGE_ALLOW_INSECURE=false`。

使用独立安装的 MinIO Client（`mc`）和管理员身份执行一次初始化。以下变量由管理员手动设置，不要将管理员凭据写入 Actions：

```bash
mc alias set test-minio https://oss-test.example.com "$MINIO_ADMIN_USER" "$MINIO_ADMIN_PASSWORD"
mc mb --ignore-existing test-minio/ecommerce-test-images
mc anonymous set none test-minio/ecommerce-test-images

# 仓库策略模板的默认 bucket 为 ecommerce-images，测试环境替换为独立 bucket。
sed 's/ecommerce-images/ecommerce-test-images/g' \
  deploy/minio-app-policy.json > /tmp/ecommerce-test-policy.json
mc admin policy create test-minio ecommerce-test-app /tmp/ecommerce-test-policy.json
mc admin user add test-minio "$STORAGE_ACCESS_KEY" "$STORAGE_SECRET_KEY"
mc admin policy attach test-minio ecommerce-test-app --user "$STORAGE_ACCESS_KEY"
```

将该应用账号的 Access Key / Secret Key 填入服务器 `.env`。bucket 保持私有，应用使用短期预签名 URL；**业务程序不会自动创建 bucket 或 IAM 策略**。如果自定义 bucket 名称，策略和 `STORAGE_BUCKET` 也必须同步修改。首次部署后应实际测试一次申请上传、上传、完成校验和读图；启动检查不能代替 bucket/IAM 验证。

## 3. 配置镜像拉取权限

Actions 使用自带的 `GITHUB_TOKEN` 发布镜像，无需额外推送密码。

如果 GHCR 包是私有的，先以**相同 SSH 部署账号**在服务器登录一次，使用有该包读取权限的 `read:packages` token：

```bash
read -rs GHCR_READ_TOKEN
printf '%s' "$GHCR_READ_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USER --password-stdin
unset GHCR_READ_TOKEN
```

不要将 Actions 的写入 token 持久化到服务器。公开镜像一般无需登录拉取。

## 4. 配置 GitHub

创建 GitHub Environment **testing**，建议限制为默认分支并按需启用人工审批。

在该 Environment 中设置 Secrets：

| Secret | 含义 |
| --- | --- |
| `TEST_SSH_HOST` | 测试服务器 IP 或域名，不带 ssh:// |
| `TEST_SSH_USER` | 专用部署账号 |
| `TEST_SSH_PRIVATE_KEY` | 该账号对应的部署私钥，须可非交互使用 |
| `TEST_SSH_KNOWN_HOSTS` | 经过核验的服务器 known_hosts 内容；非 22 端口需包含 [host]:port |

Host key 应通过云控制台等可信途径核对指纹；不要盲目信任网络中临时取得的 `ssh-keyscan` 结果。工作流不会关闭主机校验。

可选 Variables（Environment 或仓库级）：

| Variable | 默认值 |
| --- | --- |
| `TEST_SSH_PORT` | `22` |
| `TEST_DEPLOY_PATH` | `/opt/simple-ecommerce-test`，仅允许字母、数字、/、_、- |

最后，在**仓库级 Variables** 设置 `TEST_DEPLOY_ENABLED=true` 才会启用部署（不要仅放在 Environment 中，因为 job 条件在进入 Environment 前求值）。

## 5. 发布、检查与手动回退

推送默认分支，或手动运行 `Test and Deploy`。实际布局：

```text
/opt/simple-ecommerce-test/
  .env                      # 仅服务器保存
  current -> releases/...   # 最近一次成功部署
  releases/<sha>-<run>-<attempt>/
    image                   # 成功部署的不可变镜像引用
    docker-compose.yml
    docker-compose.test.yml
    deploy/
    app/mall/configs/
```

HTTP 默认只发布在服务器 `127.0.0.1:8000`，通过 HTTPS 反向代理或 SSH 隧道访问。数据库、Redis 和 gRPC 不对外发布，监控端口也仅绑定 loopback。默认保留生产模式的 CORS 限制，前端建议通过同域反向代理访问 API。

应用启动时执行迁移；启动检查失败会让 Actions 失败，但**不会自动回滚已经修改的数据库或容器**。`current` 只记录最近成功版本，不保证失败后运行中的服务仍是该版本。修复配置后可重跑。

确认数据库 schema 兼容后，手动回退到某个已成功部署的 release：

```bash
release=/opt/simple-ecommerce-test/releases/CHOSEN_RELEASE
bash "$release/deploy/deploy-test.sh" \
  /opt/simple-ecommerce-test "$(head -n 1 "$release/image")"
```

回退复用当前服务器 `.env`，不恢复旧配置或数据。不要自动删除持久卷、旧 release 或镜像；按测试服务器容量制定保留和备份策略。
