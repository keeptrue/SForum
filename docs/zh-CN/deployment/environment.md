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
| `TRUST_PROXY` | `true` | API 总开关：`false` 时任何转发头都不读，只记录 TCP 对端 |
| `TRUSTED_PROXIES` | 空 | **只作用于 api**：额外信任的代理 IP 或网段，逗号分隔；标准 Compose 拓扑下恒为空（CDN 段与 compose 网段都不写） |
| `TRUST_PROXY_PRIVATE` | `true` | 是否信任私网（RFC1918 / ULA）**对端** |
| `TRUST_PROXY_LOOPBACK` | `false` | 是否信任 loopback（`127.0.0.0/8`、`::1`）**对端** |

`TRUST_PROXY` 是唯一总开关：置 `false` 时其余三项都不再生效，API 只记录 TCP
对端地址。需要临时退回「不采信任何转发头」时，改这一个变量重启即可。

「信任私网」只作用于**对端地址**，不会把 `X-Forwarded-For` 链条上的私网条目当成
代理剥掉——链上的私网地址更可能是内网客户端本身，剥掉它反而会退回对端地址。
链条目只有 `TRUSTED_PROXIES` 里的显式代理（以及打开 `TRUST_PROXY_LOOPBACK`
后的 loopback 条目）才参与剥离。

### web 层如何传客户端 IP

链路是「浏览器 → Caddy → `web`(Nuxt) → `api`」。Nuxt 的请求管线（dev 与生产
`bun` preset 相同）经 `localFetch` 进入 h3，**拿不到 TCP 对端**（mock socket 的
`remoteAddress` 恒为空串），所以 web 层不做任何「对端是否可信」的判断：

1. 读 Caddy 写入的 `X-Real-IP`（`header_up X-Real-IP {client_ip}` 覆盖，权威值）；
2. 没有时退 `X-Forwarded-For` 的最右有效项（自定义反代兜底）；
3. 规范化成单值后同时写回 `X-Real-IP` 与 `X-Forwarded-For`，并删除
   `CF-Connecting-IP` / `True-Client-IP`；
4. 两个头都缺失或非法时删除全部四个头（fail-closed），API 回退到 TCP 对端。

**因此 `web` 端口必须只发布到 loopback/内网**（`compose.prod.yaml` 绑
`127.0.0.1:${WEB_PORT:-3000}`）。一旦 web 端口公网可达，任何客户端都能自带
`X-Real-IP` 指定自己的审计 IP；这是 web 层信任入口头的前提。直连 `:3000` 而不经过
任何代理时（例如本地 `bun run dev`），没有头可转发，记录的就是 API 看到的回环地址。

每一跳的 TCP 对端不同，API 侧配置也就不同：

| 拓扑 | API 看到的 TCP 对端 | 该配什么 |
| --- | --- | --- |
| 宿主机 Caddy → `web` → `api`（标准 Compose） | compose 网络里的 `web` 容器（私网） | 默认值即可 |
| `router` 容器 → `web` → `api`（零停机栈） | 同上 | 默认值即可 |
| Caddy 把 WebSocket Upgrade 直送 `127.0.0.1:18080` | `127.0.0.1`（loopback） | 默认只记录 `127.0.0.1`；WS 不写会话 IP，要精确值再打开 `TRUST_PROXY_LOOPBACK=true` |
| 裸机 Nuxt + API，同机回环 | `127.0.0.1` | `TRUST_PROXY=true` + `TRUST_PROXY_LOOPBACK=true` |
| 裸机 Nuxt + API，API 监听 LAN 地址 | 宿主机 LAN 地址（私网） | `TRUST_PROXY=true` + `TRUST_PROXY_PRIVATE=true`（`config` 内置默认在 production 是关闭） |
| API 直接暴露公网，没有 `web` 层 | 公网地址 | 保持不信任：`TRUST_PROXY=false` |
| CDN 在 Caddy 之前 | 仍是 `web` 容器 | 在 **Caddy** 的全局 `servers` 块写 `trusted_proxies static <CDN 出口段>`（Cloudflare 另加 `client_ip_headers CF-Connecting-IP`），应用侧 `TRUSTED_PROXIES` 留空 |

CDN 场景不要动应用的 `TRUSTED_PROXIES`：Caddy 用 `{client_ip}` 重算真实客户端，并删除
`CF-Connecting-IP` / `True-Client-IP` 之后才转发，web 再把这条唯一值收敛成单值上行。
`trusted_proxies` 必须写在 `servers` 块内（Caddy 2.10 不认顶层同名选项），可直接抄
`deploy/caddy/Caddyfile` 里的示例。

**标准 Compose 拓扑下应用侧 `TRUSTED_PROXIES` 应当恒为空**，不只是 CDN 段不要写：
**compose 网段（`172.x`、`10.x` 之类）同样不要写**。它只服务于一种特殊链路——API 的
对端本身就是显式代理，且应用层确实需要采信 `CF-Connecting-IP` / `True-Client-IP`
这类 CDN 单值头。把容器网段填进去后，只要 API 的对端落在该网段，它就会被判成「显式
代理」，重新采信链路上的 CDN 单值头；此时链路上若还有没删这两个头的代理（旧
Caddyfile、Nginx 或其它中间件），伪造值就能直接落库。

不要写 `0.0.0.0/0`；无脑信任会让伪造 `X-Forwarded-For` 绕过限流与审计。`api` 只发布
在 `127.0.0.1:${API_PORT:-18080}`，所以默认值的信任边界就是宿主机与 compose 网络；
一旦把 API 端口发布到公网，这条边界不再成立，必须关掉 `TRUST_PROXY`。

上表里的「默认值」指 `compose.yaml` 与向导写出的值。不用向导、手写环境变量时注意：
`config` 在 `APP_ENV=production` 下这四个变量的内置默认值仍是关闭，裸机部署要自己
写 `TRUST_PROXY=true` 与对应的一项。

这四个变量只透传给 `api`，`web` 不接收 `TRUSTED_PROXIES`——web 层不解析对端，也就
没有「显式代理」这层语义。`PROXY_HEADER` 默认是 `X-Forwarded-For`，需要改时通过
override 注入。

### 症状：会话/审计里记成容器地址

`user_sessions.ip_address`、登录审计 `metadata.ipAddress`、`moderation.view_ip`
与写限流键都来自同一条解析链。如果它们出现 `172.x` / `192.168.x` 的容器地址而不是
访问者地址：

1. 看 API 启动日志的 `client_ip policy:` 一行，确认 `trust` / `private` 生效；
2. 从真实客户端登录一次，查 `select ip_address from user_sessions order by created_at desc limit 1;`；
3. 值仍是容器地址，说明 web 没收到入口头：检查 Caddy 的 `header_up X-Real-IP {client_ip}`
   是否被自定义配置覆盖，以及 web 端口前面是否还挂着一层不写头的代理。

历史行不会回溯修正（原值已丢失）。

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
| `SFORUM_INSTALL_DIR` | `./sforum` | 一行 loader 的安装目录；等同 `-d DIR` |
| `SFORUM_INSTALL_SHA256` | 空 | loader 额外要求的 `sforum-bootstrap.sh` 精确 SHA-256 |
| `SFORUM_SKIP_ATTESTATION` | `false` | loader 与 bootstrap 跳过 `gh attestation verify` |
| `SFORUM_RELEASE_REPOSITORY` | `keeptrue/SForum` | loader 与 bootstrap 使用的 GitHub 仓库 |
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
