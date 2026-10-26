# 环境变量与密钥

[← 生产部署](../deployment.md)

生产配置只有一份：安装目录下的 `.env.production`。它同时承担两件事，但
这两件事的边界经常被误解：

1. **Compose 插值**：`compose*.yaml` 里的 `${VAR}` 从 `--env-file` 读取。
2. **容器运行时环境**：只有服务 `environment:` 列表里写出的变量才会进入
   容器。`.env.production` 不是容器的 `env_file`。

所以“写进 `.env.production`”不等于“应用能读到”。默认 Compose 已经透传
常用变量；高级诊断变量需要 Compose override，见文末。

## 文件与权限

```sh
cp .env.production.example .env.production
chmod 600 .env.production
```

- 配置向导生成的文件也是 `0600`，且不会覆盖已有文件。
- `docker compose` 默认读取 `.env`，不是 `.env.production`；所有命令都要
  带 `--env-file .env.production`。
- 不要把 `.env.production` 提交到仓库，也不要打印到工单或聊天记录里。
- `APP_DOMAIN` 只给宿主机 Caddy 示例使用，不会进入应用容器。

## 最小必填清单

`deploy.sh` 在改动数据库前会按下面的规则校验；手动部署也应满足同一套
契约。

| 变量 | 规则 |
| --- | --- |
| `APP_ENV` | 必须是 `production` |
| `APP_URL` | `http://` 或 `https://`，无空格 |
| `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` | 以 `/` 开头的安全路径 |
| `POSTGRES_DB` / `POSTGRES_USER` | 只含字母、数字、`_`、`.`、`-` |
| `POSTGRES_PASSWORD` | 至少 32 字符，只含字母、数字、`.`、`_`、`~`、`-` |
| `DATABASE_URL` | 必须指向 `postgres:5432/<POSTGRES_DB>` |
| `REDIS_ADDR` | 必须是 `redis:6379` |
| `REDIS_PASSWORD` | 至少 32 字符 |
| `SESSION_HASH_SECRET` | 至少 32 字符 |
| `IDENTITY_SUBJECT_HMAC_SECRET` | 至少 32 字符 |
| `APP_OPTION_ENC_KEY` | 64 位 hex |
| `ALTCHA_SECRET` | 至少 32 字符 |
| `MARKETPLACE_ED25519_PUBLIC_KEY_HEX` | 64 位 hex |
| `NUXT_PUBLIC_API_BASE_URL` | 必须是 `/api/v1` |

生成密钥：

```sh
openssl rand -hex 32
```

每个秘密单独生成一次。不要复用开发环境的默认值，也不要把同一个值同时
用于数据库、Redis、会话和加密。

## 站点与语言

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `APP_NAME` | `SForum` | 站点名，管理端与邮件使用 |
| `APP_ENV` | `production` | 生产必须是 `production` |
| `APP_DOMAIN` | `forum.example.com` | 只给宿主机 Caddy 示例 |
| `APP_URL` | `https://forum.example.com` | 公开地址；CSRF 默认来源、链接与 SEO 都依赖它 |
| `APP_LOCALE` | `zh-CN` | 默认语言 |
| `SUPPORTED_LOCALES` | `zh-CN,en-US` | 可用语言列表 |
| `LOG_LEVEL` | `info` | 日志级别 |

## 端口与入口

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `HTTP_HOST` | `0.0.0.0` | 容器内 API 监听地址 |
| `HTTP_PORT` | `8080` | 容器内 API 端口 |
| `HTTP_BODY_LIMIT` | `67108864` | API 请求体硬上限，64 MiB |
| `WEB_PORT` | `3000` | 宿主机 loopback 的 Web 端口 |
| `API_PORT` | `18080` | 宿主机 loopback 的 API/WebSocket 端口 |

`WEB_PORT` 和 `API_PORT` 只绑定 `127.0.0.1`；公网入口由宿主机反代负责。

## PostgreSQL

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `POSTGRES_DB` | `sforum` | 数据库名 |
| `POSTGRES_USER` | `sforum` | 数据库用户 |
| `POSTGRES_PASSWORD` | 无安全默认 | 必须替换为高熵值 |
| `DATABASE_URL` | `postgres://sforum:<密码>@postgres:5432/sforum?sslmode=disable` | 容器内连接串 |
| `MIGRATE_ON_STARTUP` | `true` | `true` 时 API 启动迁移；向导与 `deploy.sh` 写 `false` 并显式迁移 |
| `DATABASE_MAX_CONNS` | `10` | API 与内嵌 Worker 共用的连接池上限 |

Compose 内部 PostgreSQL 网络不启用 TLS。外部数据库属于高级部署，需要按
提供商要求改成 `verify-full` 并配置受信任证书；配置向导不提供外部数据库
选项。

## Redis

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `REDIS_ADDR` | `redis:6379` | Compose 内置 Redis 地址 |
| `REDIS_PASSWORD` | 无安全默认 | Compose 对 Redis 启用 `--requirepass` |

使用外部无密码 Redis 时，需要把 `REDIS_PASSWORD` 置空，并同步调整
`compose.yaml` 里的 `--requirepass`；不要只改一边。

## 队列与 Worker

API 始终内嵌 River Worker，没有独立 Worker 开关。

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `WORKER_SHUTDOWN_TIMEOUT` | `30s` | 关闭时等待任务收尾 |
| `JOB_QUEUE_CRITICAL_WORKERS` | `4` | 关键队列并发 |
| `JOB_QUEUE_DEFAULT_WORKERS` | `8` | 默认队列并发 |
| `JOB_QUEUE_SEARCH_WORKERS` | `6` | 搜索索引并发 |
| `JOB_QUEUE_MAIL_WORKERS` | `4` | 邮件并发 |
| `JOB_QUEUE_NOTIFICATIONS_WORKERS` | `6` | 通知并发 |
| `JOB_QUEUE_MAINTENANCE_WORKERS` | `2` | 维护任务并发 |

队列持续繁忙时，优先提高 `DATABASE_MAX_CONNS` 并观察 PostgreSQL，或降低
对应队列并发；不要无边界提高 River 并发。

## 人机验证

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `HUMAN_VERIFICATION_PROVIDER` | `disabled` | 验证提供方；关闭时不拦截 |
| `ALTCHA_SECRET` | 无安全默认 | ALTCHA 签名密钥 |
| `ALTCHA_CHALLENGE_TTL` | `10m` | 挑战有效期 |
| `ALTCHA_COST` | `1000` | 工作量证明成本 |

## 扩展与信任

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `EXTENSION_ROOT` | `/var/lib/sforum/extensions` | 已安装扩展的持久化目录 |
| `BUILTIN_EXTENSION_ROOT` | `/app/extensions/builtin` | 内置扩展目录 |
| `EXTERNAL_EXTENSION_ROOTS` | 空 | 可选，逗号分隔的容器内路径；需配套只读 volume |
| `SFORUM_V3_TRUST_CHALLENGES` | `true` | 启用精确制品信任确认 |
| `SFORUM_V3_TRUST_CHALLENGE_TTL` | `5m` | 信任确认有效期 |
| `SFORUM_SAFE_MODE` | 空（等价 `false`） | 设为 `true` 并重启 API 可跳过插件启动；恢复后改回 |

`EXTERNAL_EXTENSION_ROOTS` 必须是容器内路径，不能写宿主机路径。

公开 L2（`components[].l2Component` 与内容区样式）**没有独立开关**，恒为启用：
只要扩展已启用并通过精确制品信任确认，其预构建 ESM/CSS 就会进入浏览器。
旧版本中的 `SFORUM_V3_PUBLIC_L2` 已移除，继续设置它不会产生任何效果。

它建立在精确制品信任之上：`SFORUM_V3_TRUST_CHALLENGES=false` 会让公开 L2 整体关闭
（`.env.example` 与 `compose.yaml` 的非生产默认值就是 `false`，生产默认 `true`）。
只想关闭某个扩展时，禁用该扩展或撤销其精确制品信任；需要整体关闭时使用
`SFORUM_SAFE_MODE=true`。

## 会话、身份与加密

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `SESSION_HASH_SECRET` | 无安全默认 | 服务端会话签名；轮换会让现有会话全部失效 |
| `IDENTITY_SUBJECT_HMAC_SECRET` | 无安全默认 | 外部主体 digest 的 HMAC；轮换会改变 digest |
| `APP_OPTION_ENC_KEY` | 无安全默认，64 位 hex | option 敏感值 AES-256-GCM 加密；轮换后旧值不可读 |
| `MARKETPLACE_ED25519_PUBLIC_KEY_HEX` | 无安全默认，64 位 hex | Marketplace 签名索引验签公钥 |
| `MARKETPLACE_ED25519_KEY_ID` | `marketplace-primary` | 公钥标识 |

向导会生成一个 `deployment-local-untrusted` 用途的本地 Marketplace 公钥，
让未知索引保持锁定。接入官方 Marketplace 时必须替换为官方公钥和 key ID；
私钥不要进入仓库或容器。

## 前端

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `NUXT_PUBLIC_API_BASE_URL` | `/api/v1` | 浏览器同域 API 前缀，必须是 `/api/v1` |
| `NUXT_API_INTERNAL_BASE_URL` | `http://api:8080/api/v1` | Nuxt SSR 到 API 的内部地址 |
| `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` | `/control-panel` | 管理后台路径前缀 |

## CSRF 与反向代理

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `CSRF_TRUSTED_ORIGINS` | `https://forum.example.com` | 允许的公开 origin；留空时从 `APP_URL` 派生 |
| `TRUST_PROXY` | 空（生产等价 `false`） | 是否采信转发头 |
| `TRUSTED_PROXIES` | 空 | 反代地址或网段，逗号分隔 |
| `TRUST_PROXY_PRIVATE` | 空（生产等价 `false`） | 是否信任私网来源 |
| `TRUST_PROXY_LOOPBACK` | 空（生产等价 `false`） | 是否信任 loopback 来源 |

生产默认不信任任何网段。只有部署在宿主机反代之后，才开启 `TRUST_PROXY`
并把 `TRUSTED_PROXIES` 设为反代地址，例如同机 Caddy 用 `127.0.0.1/32`。
不要写 `0.0.0.0/0`；无脑信任会让伪造 `X-Forwarded-For` 绕过限流与审计。

这四个变量已经由 `compose.yaml` 透传到 `api` 容器。`PROXY_HEADER` 默认是
`X-Forwarded-For`，需要改时通过 override 注入。

### AI 供应商出站

生产环境只允许 `https`，并拒绝解析到私网/回环/保留地址的端点。
`SFORUM_AI_ALLOW_PRIVATE_ENDPOINT=true` 只用于开发环境直连本机模型
（Ollama、vLLM 等），生产恒为 `false`。生产要接内网模型服务，请在前端放
一个提供 HTTPS 的反向代理。

## 部署脚本变量

这些变量由宿主机上的脚本读取，不是应用容器配置。

| 变量 | 默认/示例 | 说明 |
| --- | --- | --- |
| `SFORUM_VERSION` | 无 | 目标不可变标签；`compose.release.yaml` 必需 |
| `SFORUM_REGISTRY` | `ghcr.io/keeptrue` | 镜像仓库前缀 |
| `COMPOSE_PROJECT_NAME` | `sforum` | Compose 项目名；影响容器与卷命名 |
| `SFORUM_BACKUP_DIR` | `./backups` | 备份目录 |
| `SFORUM_DEPLOY_HEALTH_TIMEOUT_SECONDS` | `120` | `deploy.sh` 健康检查超时 |
| `SFORUM_DEPLOY_STABILITY_SECONDS` | `3` | 服务稳定观察时间 |
| `SFORUM_UPGRADE_HEALTH_TIMEOUT_SECONDS` | `120` | 蓝绿槽健康检查超时 |
| `SFORUM_CONFIRM_RESTORE` | 空 | 设为 `RESTORE` 才允许恢复 |
| `SFORUM_SKIP_ATTESTATION` | `false` | bootstrap 跳过 `gh attestation verify` |
| `SFORUM_RELEASE_REPOSITORY` | `keeptrue/SForum` | bootstrap 使用的 GitHub 仓库 |
| `SFORUM_RELEASE_DOWNLOAD_BASE_URL` | GitHub Releases 下载前缀 | 镜像或离线发布源 |
| `SFORUM_LATEST_RELEASE_API_URL` | GitHub `releases/latest` | 稳定通道解析地址 |
| `SFORUM_RELEASES_API_URL` | GitHub Releases 列表 | 预发布通道解析地址 |

## 需要 override 的高级变量

以下变量不在默认 Compose 的 `environment:` 列表中，直接写进
`.env.production` 不会进入容器。需要时新增一个 override 文件，例如
`compose.local.yaml`，并在所有 Compose 命令里追加 `-f compose.local.yaml`。

| 变量 | 用途 |
| --- | --- |
| `PPROF_ENABLED` / `PPROF_ADDR` | API pprof，默认关闭；地址必须是 loopback |
| `WORKER_PPROF_ENABLED` / `WORKER_PPROF_ADDR` | 独立 Worker pprof（当前拓扑不使用独立 Worker） |
| `GOMEMLIMIT` | Go runtime 软堆上限，例如 `512MiB` |
| `HTTP_READ_TIMEOUT` / `HTTP_WRITE_TIMEOUT` / `HTTP_IDLE_TIMEOUT` | HTTP 超时 |
| `SESSION_IDLE_TIMEOUT` / `SESSION_ABSOLUTE_TIMEOUT` / `SESSION_RENEWAL_INTERVAL` | 会话生命周期 |
| `LIMITER_WINDOW` | 写请求限流窗口 |
| `DATABASE_CONNECT_TIMEOUT` / `DATABASE_MAX_CONN_IDLE_TIME` / `DATABASE_MAX_CONN_LIFETIME` | 连接池细节 |
| `REDIS_DIAL_TIMEOUT` / `REDIS_READ_TIMEOUT` / `REDIS_WRITE_TIMEOUT` | Redis 超时 |
| `SFORUM_AI_ALLOW_PRIVATE_ENDPOINT` | 仅开发环境直连本机模型；生产恒为 `false` |

override 示例：

```yaml
services:
  api:
    environment:
      GOMEMLIMIT: "512MiB"
      PPROF_ENABLED: "true"
      PPROF_ADDR: "127.0.0.1:6060"
```

pprof 只监听容器 loopback，不能直接通过 `ports:` 暴露。采集方式见
[故障排查](./troubleshooting.md)。

## 验证

```sh
docker compose --env-file .env.production \
  -f compose.yaml -f compose.prod.yaml -f compose.release.yaml config --quiet

./deploy.sh --action status
```

`config --quiet` 只验证 Compose 展开，不验证应用是否健康；健康检查见
[手动 Compose 部署](./manual.md)。
