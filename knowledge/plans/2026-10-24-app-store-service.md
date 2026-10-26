# 应用商城 · 服务端与插件（`sforum-app-store`）

Status: **draft（讨论中，未批准实施）**
Started: 2026-10-24
Companion:
`2026-10-24-app-store-core-install-primitive.md`（SForum 宿主侧：清算 + 安装原语）
Decision records（批准实施时创建）:
`decisions/2026-10-24-app-store-out-of-tree.md`、
`decisions/2026-10-24-app-store-identity-and-distribution.md`

## 1. 产品形态

应用商城 = **一个独立服务端 + 一个 SForum 插件**，放在同一个新仓库
`sforum-app-store`，但作为两个独立产物发布：

- `service/`：Next.js + Bun + PostgreSQL。公开目录 API、公开前台、开发者门户、审核后台、GitHub 同步、签名下载代理。
- `plugin/sforum.store`：SForum 扩展（Go 后端 + 预构建 Vue 管理页）。只负责站内浏览、解析与安装交接。
- `contracts/store-api.yaml`：两者共享的 OpenAPI 契约；生成 TS 类型与 Go client，避免跨产物漂移。

为什么服务端不是 SForum 插件：它服务多个论坛、拥有独立生命周期与故障域、要处理不可信开发者输入与 GitHub 出站流量；把它塞进某一个论坛实例会让目录可用性、权限面和发布节奏全部被单站点绑死。

## 2. 已确认约束（来自代码与既有插件）

1. **登录**：SForum 的 OAuth2 提供方是独立插件 `sforum-oauth2-provider`，端点形如 `/api/oauth/authorize|token|userinfo|revoke|introspect|csrf`；授权码 + PKCE、refresh 轮换、scope 三层约束。
2. **CSRF**：它的所有插件路由都在宿主 CSRF 中间件后。服务端换 token / refresh / revoke 必须：先 `GET /api/oauth/csrf` 拿 `csrf_` cookie → 再带 `X-Csrf-Token` + `Origin: <forum>` 发 POST。标准 OAuth 库直接接会失败。
3. **userinfo**：只认 `access_token` 查询参数（插件路由拿不到 `Authorization`）。
4. **无动态客户端注册**：`client_id/secret` 只能在论坛后台手工建；商城对身份提供方必须**配置驱动**。
5. **GitHub**：决定全量使用 GitHub App（不设验证文件）；私有 release 资产只能通过 installation token 读取。
6. **安装**：SForum 侧提供管理页 bridge 的 `installExtension`（页面级 host action，见 companion 计划 §6.2）。商城插件页面调用它；插件后端不参与安装；必须管理员在宿主确认框里当场确认。
7. **core 下载器**：只接受 public HTTPS，50 MiB / 90 s，支持 `sha256` 钉住；商城下载地址必须公网可达。

## 3. 关键决策

- **D1 独立服务端**：多论坛、独立发布、独立故障域、不可信输入隔离。
- **D2 一个仓库两个产物**：共享 OpenAPI 与 CI，但镜像与插件 ZIP 分开发布。
- **D3 全量 GitHub App**：安装 App 即证明仓库归属，同时解锁私有 release；不存 PAT，不用 OAuth App 的 `repo` 大权限。
- **D4 所有下载走商城签名短链代理**：统一一条信任链、隐藏上游地址、支持私有仓库与撤销；代价是带宽，CDN/公开直链留到后续。
- **D5 自动检测，不靠文件名**：ZIP 内 `sforum.extension.json` + `backend.entry` 的 `debug/buildinfo` + 流式 SHA-256；文件名/头部只做兜底并记录 `detection_source`。
- **D6 图片上传到商城**：PNG/JPEG/WebP、禁 SVG、魔数与尺寸校验、内容寻址；不引入 `sharp`（避免 Bun 原生依赖风险）。
- **D7 登录只走 SForum OAuth2**：商城不注册、不存密码；账号唯一键 `(issuer, sub)`；论坛 token 加密落库、refresh 轮换。
- **D8 审核**：首个产品/首个 release 人工，后续 release 自动同步；改名、换仓、新增高危能力重新进审。
- **D9 目录不签名（v1）**：HTTPS + 每资产 SHA-256 + 签名下载短链已覆盖；离线镜像/多镜像再引入签名快照。
- **D10 v1 不做支付/授权**：数据模型预留 `entitlements/licenses`，但不接支付。
- **D11 安装走页面级 host action**：不申请 `extensions.install` capability，不调 Host Command，不建意图/worker；插件后端只负责解析目录与资产，安装由页面调 `bridge.installExtension` 交给宿主。同步、最长 ~90 秒、离开页面会中止；bridge 的 promise 语义固定，后续可无痛升级为异步（core 计划 §6.9）。

## 4. 仓库布局

```text
sforum-app-store/
  service/                         # Next.js + Bun + Drizzle
    src/app/…                      # 公开前台 / dashboard / moderation
    src/app/api/v1/…              # 公开 API + 插件 API
    src/lib/…                      # oauth client, github app, detection, storage
    src/worker.ts                  # pg-boss worker（release 同步 / 资产检测）
    drizzle/                       # schema + migrations
  plugin/                          # 仓库根即 EXTERNAL_EXTENSION_ROOTS 源根
    plugins/sforum.store/
      backend/                     # Go module（replace 到 SForum 检出）
      frontend/admin/              # Vue 3 + Vite，预构建 dist
      manifest/                    # admin / settings / langs
      schemas/                     # 命令与组件 schema
      openapi/                     # 插件路由契约
      sforum.extension.json
  contracts/store-api.yaml
  docs/                            # 部署、开发者接入、GitHub App 注册
  .github/workflows/{ci,release}.yml
```

- 插件后端的 `go.mod` 沿用 `sforum-oauth2-provider` 的做法：`require github.com/keeptrue/sforum/apps/api v0.0.0` + 本地 `replace`，CI 用 `go mod edit -replace` 指向检出目录。
- 根 `package.json` 使用 Bun workspace：`service` + `plugin/plugins/sforum.store/frontend/admin`。

## 5. 服务端

### 5.1 栈与运行时

- Next.js（App Router）+ TypeScript + Tailwind；Bun 作为包管理器与运行时。
- 脚本：`dev: bun --bun next dev`、`build: bun --bun next build`、`start: bun --bun next start`、`worker: bun run src/worker.ts`。
- **Bun 运行时闸门**：CI 必须跑一次 `bun --bun next build` + 启动冒烟（`/api/health` + 一页 SSR）。若原生依赖失败，只允许 `next start` 回退 Node，其余保持 Bun——由 CI 证据决定，不靠猜。
- PostgreSQL 16 + Drizzle ORM + drizzle-kit；`pg-boss` 作为 Postgres 队列（独立 worker 进程，同镜像）。
- 图片：`file-type` 验魔数 + `image-size` 读尺寸；内容寻址存储（本地卷，预留 S3 适配器）；不引入 `sharp`。

### 5.2 身份与会话

- 配置：`STORE_OAUTH_ISSUERS`（每项含 `issuer/client_id/client_secret/authorize/token/userinfo/revoke/csrf`）。官方实例绑官方论坛；自部署绑自己的论坛。
- 登录：
  1. `GET /api/auth/sforum/start?issuer=…` 生成 `state` + PKCE，写短时签名 Cookie。
  2. 跳 `${issuer}/api/oauth/authorize?client_id&redirect_uri&response_type=code&scope=profile avatar&state&code_challenge&code_challenge_method=S256`。
  3. 回调 `GET /api/auth/sforum/callback?code&state`：验 state → CSRF 握手 + `POST /api/oauth/token` → `userinfo?access_token=`。
  4. 按 `(issuer, sub)` upsert 账号；建商城会话；论坛 token 用 AES-256-GCM 加密落库。
- 刷新：`grant_type=refresh_token`，每次轮换并写回；失败标 `reauth_required`，页面提示重登，不静默登出开发者。
- 登出：best-effort `POST /api/oauth/revoke`（同样先 CSRF 握手）+ 删除会话。
- 商城会话：`__Host-store_session`（HttpOnly/Secure/SameSite=Lax），PG sessions，支持“撤销其它设备”。
- 角色：`store_roles(account_id, admin|moderator)`，由 `STORE_BOOTSTRAP_ADMINS`（`issuer#sub`）播种；**不信任论坛用户组**。
- scope：默认 `profile avatar`；`email` 仅在开发者主动选择公开联系邮箱时申请，并在 UI 说明用途。

### 5.3 数据模型

- `accounts`：`(issuer, sub)` 唯一；username/display_name/avatar_url/email/email_verified。
- `sessions`、`oauth_token_sets`（加密 token、过期、scope）。
- `oauth_issuers`：配置来源 + 最近校验时间。
- `products`：`extension_id` 全局唯一、`owner_account_id`、`kind(plugin|theme)`、`status(draft|pending|published|yanked|disconnected)`、`repo_id`、`slug`、分类/标签、`default_locale`。
- `product_translations`：`(product_id, locale)` → name/summary/description；回退默认语言。
- `assets`：sha256/mime/width/height/bytes/storage_key；`product_screenshots` 排序。
- `releases`：`product_id`、`version`（取 manifest，不用 tag）、`tag`、`channel(stable|beta)`、`published_at`、`notes_md`、`yanked`。
- `release_assets`：`release_id`、`os`、`arch`、`filename`、`size`、`sha256`、`upstream_asset_id`、`detection_source`、manifest 元数据（`extension_id/version/type/sforum_version/capabilities`）。
- `github_installations`、`github_repos`（installation_id/repo_id/full_name/private/archived/default_branch）。
- `sync_state`：ETag、last_checked、last_error、next_check_at。
- `moderation_events`、`audit_log`。
- v2 预留：`entitlements`、`licenses`、`download_nonces`。

### 5.4 GitHub App

- 注册（一次性）：`Contents: read` + `Metadata: read`；订阅 `release`、`installation`、`installation_repositories`；回调 `${store}/api/github/setup`；webhook `${store}/api/github/webhook`。
- 安装：`GET /api/github/install/start` → `https://github.com/apps/<slug>/installations/new?state=…` → setup 回调存 `installation_id` → `GET /installation/repositories` 让开发者选仓库。
- 令牌：App 私钥签 10 分钟 JWT → `POST /app/installations/{id}/access_tokens` → 1 小时 token；缓存到过期前 60 s。
- Webhook：验 `X-Hub-Signature-256`；`release` 入队增量同步；`installation*` 更新可见性；卸载即标记产品 `disconnected`，下载立即失效。
- 自部署矩阵：官方实例用官方 App；自部署必须注册自己的 App（部署文档给 10 分钟步骤），缺配置启动即失败。

### 5.5 同步与自动检测

对每个 release 的非源码 `.zip` 资产：

1. 下载到临时文件（≤50 MiB、90 s），校验 ZIP。
2. 读根目录（或单层子目录）`sforum.extension.json`，按仓内 vendored `manifest-v3.schema.json` 校验；取 `id/version/type/sforumVersion/capabilities`。
3. 有 `backend.entry`：读该二进制，`debug/buildinfo` 取 `GOOS/GOARCH`（含 `GOARM/GOAMD64`）；无 backend（主题）→ `any/any`。
4. 流式计算 ZIP SHA-256；若 release 带 `checksums.txt`/`SHA256SUMS`，交叉校验，不一致即失败。
5. 同一 release 内所有资产必须同 `extension.id/version/type`；同 `(os,arch)` 两个候选 → `needs_attention`，要求 `sforum-release.json` 覆盖，**不猜**。
6. 兜底顺序：buildinfo → ELF/Mach-O 头 → 文件名约定；每次兜底写 `detection_source`，审核可见。
7. 临时文件用后即删；不留包体、不写对象存储。

同步触发：webhook 即时 + 每小时兜底轮询（ETag）；产品页提供“手动刷新”，限流。

### 5.6 下载代理

- 目录/详情只返回 `downloadUrl`（5 分钟签名 token：asset/product/exp/nonce）+ `sha256` + `size`，不暴露 GitHub 原始地址。
- `GET /api/v1/download/{token}`：验签 → 产品/release 未 yank → 用 installation token 流式转发。
- 响应头：`Content-Type: application/zip`、`Content-Length`、`Content-Disposition: attachment`、`nosniff`、`Cache-Control: private, no-store`。
- 公开仓库同样走代理（统一一条信任链）；带宽压力大时再加 `directPublic` 快路径，不改协议。

### 5.7 API（契约优先）

`contracts/store-api.yaml` → `openapi-typescript`（TS）+ `oapi-codegen`（Go client）。`/api/v1`：

- `GET /catalog?channel=&sforum=&os=&arch=&q=&category=&cursor=`（ETag）
- `GET /products/{slug}`
- `GET /products/{slug}/releases`
- `GET /products/{slug}/releases/{version}`
- `GET /products/{slug}/download/{os}/{arch}` → 302 到签名 URL
- `GET /health`

错误体 `{code, message, details}`；`Cache-Control: public, max-age=60, stale-while-revalidate=300`；分页 cursor。

### 5.8 页面

- 公开：`/`（推荐/最新/最近更新）、`/products`（筛选/搜索）、`/products/[slug]`（图标、截图、介绍、版本历史、changelog、平台矩阵、仓库链接）、`/docs`（开发者接入）。
- 登录后：`/dashboard`（我的产品、连接仓库、发布、yank）、`/dashboard/products/[id]`。
- 审核：`/moderation`（首个产品/首个 release 人工；异常重新进审）、审计日志。
- i18n：UI `zh-CN` 默认 + `en-US`；产品文案走 `product_translations`。
- SEO：SSR + canonical + 每产品 OG 图。

### 5.9 安全

- SSRF 安全抓取（public HTTPS、重定向复检、大小上限）。
- changelog 服务端消毒后输出；产品描述同样消毒。
- 上传：魔数 + 尺寸 + 禁 SVG + 随机文件名 + 长缓存。
- 登录/提交/下载限流；webhook 验签；会话轮换；审计。
- 依赖与 GitHub Actions 固定版本；私钥只从环境/密钥管理读取。
- 开发者只能管理自己 installation 下的仓库与产品；越权按 404 处理。

### 5.10 部署矩阵

| 场景 | 身份提供方 | GitHub App | 下载 |
| --- | --- | --- | --- |
| 官方商城 | 官方论坛 OAuth2 client | 官方 App（公共安装） | 商城签名代理 |
| 自部署商城 | 自己论坛的 OAuth2 client | 自己注册的 App | 商城签名代理 |
| 纯内网/离线（后续） | 自己论坛 | 自己 App 或本地镜像 | 本地镜像 + 签名快照 |

## 6. 商城插件 `sforum.store`

### 6.1 清单

- `id: sforum.store`、`type: plugin`、`manifestVersion: 3`
- `sforumVersion` 设为包含 `hostActions` / `installExtension` 的最低 SForum 版本；旧宿主会在安装时被 core 拒绝，不会出现“装上了但按钮不可用”。
- `capabilities: [host.api, settings.own, net.outbound, extensions.read]`（不需要 `extensions.install`，也不调 Host Command）
- `database.grants`：按实际需要声明；安装链路不需要 `host_commands`
- `admin.pages: [{path:"/store", view:"component", menu:true, group:"应用商城", permission:"extension.view", hostActions:["extension.install"], component:{…}}]`
- 设置：`catalogBaseUrl`（默认官方）、`channel`、`locale`、`cacheTtl`、`allowPrerelease`；v2 预留 `licenseKey`（secret）。
- 前端 Vue 3 + Vite + `@sforum/admin-sdk`/`@sforum/plugin-ui`，预构建到 `frontend/admin/dist/*.mjs|*.css`。
- 页面必须 feature-detect `bridge.installExtension`：不存在时禁用安装并提示“宿主版本过旧，请先升级 SForum”；发布说明写明最低宿主版本。

### 6.2 后端路由（命名空间内）

- `GET /catalog`：带宿主 `os/arch`（`runtime.GOOS/GOARCH`）、channel、locale 取商城目录；合并 `extensions.read` 的已装清单。
- `GET /product/:slug`：详情 + 版本 + 当前平台资产。
- `POST /resolve`：点击安装时取一个新的签名下载地址（5 分钟 TTL），返回 `{url, sha256, expectedExtensionId, expectedVersion, expectedType, size}`；页面把它交给 `bridge.installExtension`。
- 签名地址只有 5 分钟；确认框停留过久导致 403/410 时，页面重新 `/resolve` 后再试，不要把过期地址缓存到下一次安装。
- `GET /updates`：已装版本 vs 最新兼容版本。
- 不再有后端 `/install` 或状态轮询路由——安装由宿主页面完成。

### 6.3 页面与交互

- 侧栏一级「应用商城」；插件/主题两个页签；搜索、分类、兼容性筛选、已装徽标。
- 详情：图标/截图/介绍/版本/changelog（纯文本或已消毒 HTML）/平台矩阵/仓库外链。
- 安装：按钮 → 后端 `POST /resolve` 取资产 → `bridge.installExtension({url, sha256, expected…})` → 宿主确认框 → 同步安装（最长 ~90 秒）→ 成功/失败。页面在安装期间提示“不要关闭页面；关闭会中止”，并把 `installExtension` 不存在的情况降级为“宿主版本过旧”。
- 无安装权限（非 super_admin、无对应 manage）时隐藏按钮，宿主/API 仍校验；错误按稳定错误码映射成可读文案。
- 不做自动启用、不做后台静默安装；安装成功后 `bridge.navigate('/extensions/plugins' | '/extensions/themes')` 去完成信任/启用。

### 6.4 缓存与降级

- 商城目录缓存在 Host `PluginFiles`（`kind=private`），带 ETag + `fetchedAt`；TTL 10 分钟，stale 兜底 24 小时并显式标注「数据更新于 X」。
- 商城不可达：展示缓存、禁用安装并提示；不伪造版本。

### 6.5 与 core 的安装交接

1. 页面从后端取到签名资产后，调用 `bridge.installExtension`；宿主确认框是唯一的安装入口。
2. `bridge` resolve 时已经 `staged`（或失败）；页面显示扩展 ID、版本、摘要。
3. 管理员在 core 完成 trust challenge 与启用；商城不代劳。
4. 如果目标是已启用插件的新版本：core 只暂存候选，页面文案必须写“去完成升级”，不能写“已更新”。
5. 管理员离开页面会中止安装；页面必须明确提示，并在重进后允许重试。

## 7. 阶段与验收

| 阶段 | 交付 | 验收 |
| --- | --- | --- |
| S0 | 仓库骨架、OpenAPI 契约、CI（Bun 冒烟 + Go 插件构建） | 空壳可构建；契约生成通过 |
| S1 | OAuth2 登录（CSRF 握手、refresh 轮换、会话） | 真实论坛登录/刷新/撤销通过 |
| S2 | GitHub App 安装 + 仓库选择 + release 同步 + 自动检测 | 公开/私有 fixture 各一条 release 被正确解析 |
| S3 | 产品 CRUD + 图片上传 + 公开 API + 公开前台 | 产品页 SSR、筛选、分页、ETag |
| S4 | 开发者门户 + 审核后台 | 首个产品人工审核；后续 release 自动 |
| S5 | 插件 MVP（目录/详情/安装/更新/缓存） | 浏览器 1633×810 + 390×844；端到端安装到暂存 |
| S6 | 加固与文档（yank、限流、审计、自部署矩阵） | 端到端演练 + 回滚演练 |

## 8. 风险与未决

- **Bun + Next 生产运行时**：以 CI 冒烟为准；失败仅回退 `next start`。
- **GitHub App 全量**：公开仓库开发者也需安装；自部署需自建 App。文档必须写清失败提示。
- **OAuth provider 的 CSRF/cookie 约束**：自定义 client，集成测试需打到真实 provider 或高保真 fake。
- **清单 schema 漂移**：service vendored schema + CI 与 SForum tag 对比，漂移即失败。
- **扩展 ID 抢注**：`products.extension_id` 全局唯一 + App 安装即归属；`sforum.*` 预留给官方。
- **带宽**：全量代理是刻意选择；CDN/直链快路径留到后续。
- **同步安装的已知限制**：离开页面会中止、无进度/重试/批量；页面必须明确提示。若真实使用中不可接受，按 core 计划 §6.9 升级为“意图 + worker”，插件页面不需要改。
- **宿主 bridge 版本**：旧宿主没有 `installExtension`，也没有 `hostActions` 字段（`additionalProperties: false` 会拒收清单）；插件必须 feature-detect，发布说明写明最低宿主版本。
- **反代超时**：宿主侧这次 POST 要显式 120 秒；部署侧 Caddy/Nginx 的读超时也要 ≥120 秒，否则同步安装会被反代截断。
- **未决**：
  - 官方域名、GitHub App slug、OAuth client 注册归属。
  - 是否在 v1 支持多 issuer（配置驱动可支持，但 UI 是否暴露“选择论坛”）。
  - 是否支持 Windows 包。
  - 分类法（taxonomy）与推荐排序规则。
  - 下载统计与隐私口径。
  - yank 后客户端行为（隐藏/标红/禁止安装）。
  - 多扩展 ID 的 monorepo 如何建模（一产品一 ID，还是允许子产品）。
  - 产品翻译粒度（name/summary/description 全量还是仅 name/summary）。

## 9. 假设

- 论坛已安装并启用 `sforum-oauth2-provider`，商城 client 已在论坛后台注册。
- 商城以 public HTTPS 暴露，能被论坛 API 访问。
- SForum 插件后端为 Go；非 Go 后端走头部/文件名兜底并标记检测来源。
- 首版不含支付/授权；数据模型预留。
- PostgreSQL 16+；不引入 Redis。
