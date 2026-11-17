# 2026-07-09 安全审阅修复决策

## 背景

`docs/security-review-2026-07-09.md` 报告了 6 个问题（2 P1 + 3 P2 + 1 P3）。本批修复处理 4 项，订正 P3，CSRF 单独延后一期。决策依据见报告核验结论与 AGENTS.md 工作原则。

## 修复项与决策

### 1. P1 评论/回复绕过主题与分类可见性

- **决策**：在 service 层复用 `GetTopic` 的可见性判定（`status IN ('active','locked') AND categories.visibility='public'`），而非改 `commentSelectSQL` 加 JOIN。
- **理由**：
  - 现有 Forum 测试全是 mock-based、无 DB harness；service 层修复可直接回归测试，SQL JOIN 修复无法在现有套件内测。
  - `GetTopic` 是公开可见性的唯一事实源，复用避免规则分裂。
  - `CachedStore` 不覆盖 `ListComments`/`ListCommentReplies`，会自动缓存新增的 `GetTopic` 查询，对冲多查开销。
- **实现**：`Service.ListComments` 调用前先 `GetTopic`；`ListCommentReplies` 先 `GetCommentSummary` 追溯 topic 再 `GetTopic`。不可见统一返回 `ErrTopicNotFound`/`ErrCommentNotFound` → 404。
- **代价**：ListComments 多 1 次查询、ListCommentReplies 多 2 次（GetTopic 可被 CachedStore 缓存）。公开读路径影响可接受。

### 2. P2 密码重置人机验证启用后失效

- **决策**：`passwordResetRequest` 镜像 `register` 的写法，直接读首次绑定的 `req.HumanVerification` 调 verifier，删除有缺陷的 `verifyHumanVerification` 二次绑定辅助。
- **理由**：根因是 `verifyHumanVerification` 对同一 body 二次绑定到顶层 `humanVerificationRequest`，嵌套 token 被丢弃。register 的写法（读嵌套字段）本就是正确范式。`verifyHumanVerification` 修复后无调用方，删除避免死代码与 wrapper 滥用（AGENTS.md）。
- **前端**：`forgot-password.vue` 接入 ALTCHA，challenge URL 用 `purpose=password_reset`，body 携带嵌套 `humanVerification`，与 register.vue 一致。

### 3. P2 生产配置样例不一致

- **决策**：
  - compose Redis 启用 `--requirepass`（默认开发密码 `sforum-dev-redis`），healthcheck 带密码。
  - api/worker 补齐 `REDIS_PASSWORD`/`SESSION_HASH_SECRET`/`HUMAN_VERIFICATION_*`/`ALTCHA_*` 环境变量透传。
  - `.env.production.example` 与 docs 把 `SESSION_SECRET` 改为实际生效的 `SESSION_HASH_SECRET`，删除 `CSRF_SECRET`（CSRF 落地前不暴露无效键）。
- **理由**：让"照着样例部署"能真正工作，并消除会让运维误以为安全配置已生效的幽灵变量。

### 4. P2 附件主动内容风险（denylist + nosniff/强制下载 双层）

- **决策**：配置层硬封禁主动内容 MIME 入库 + 响应层 nosniff 与强制下载兜底，两层都做。
- **实现**：
  - `Options/attachment_options.go`：`attachmentActiveContentDenylist`（text/html、image/svg+xml、application/javascript 等），`normalizeAttachmentMIMETypes` 校验；通配符配置（`image/*`）仍接受，靠响应层兜底。
  - `Attachments/controller.go`：统一 `X-Content-Type-Options: nosniff`，主动内容类型强制 `Content-Disposition: attachment`。
- **理由**：配置层阻止危险类型入库是首选；但通配符配置或 DB 篡改时，响应层强制下载作为纵深防线，符合"安全边界 + 纵深防御"。

### 5. P3 头像附件 ID（经核验不成立）

- **决策**：不修复（非缺陷），订正报告。
- **核验**：`Profile/postgres_store.go` 的 `validateAvatarAttachment` 在事务内已校验 owner==actor、status==active、`image/*`，越权场景返回 `ErrProfileInvalid`。报告原判断基于 service 层归一化只校验 `>0`，遗漏了 store 层校验。
- **保留**：作为可选纵深防御（service 层提前校验减少事务往返），优先级最低。

## 延后项

### CSRF 防护（P1）— 独立里程碑，已实施

- **决策**：采用 Fiber v3 自带 `middleware/csrf`，double-submit cookie（`csrf_`）+ `X-Csrf-Token` header，状态存共享 Redis storage，覆盖全部 unsafe 方法（含 login/register/password-reset，防登录型 CSRF）。作为独立里程碑在第一批修复之后落地。
- **理由**：工程量最大，独立交付降低审查面与回滚风险。`knowledge/decisions/2026-07-05-browser-session-jwt-strategy.md` 已记录 production-ready 前必须完成。复用框架自带中间件而非自研（AGENTS.md 库优先）。
- **实现**：
  - `app/Http/server.go`：CSRF 注册在 `/api/v1` group（GET 也经过，以便种 cookie）；`CSRFEnabled` 配置开关（默认 true，测试显式 false）。
  - `config/config.go`：`CSRFTrustedOrigins []string`（逗号分隔，默认从 APP_URL 派生），`envStringSlice` helper；字段名刻意避开 `CSRFSecret`（config_test.go 有守卫禁止幽灵字段）。
  - 自定义 `csrfErrorHandler` 把错误映射为统一 envelope reason（`csrf.invalid` / `csrf.origin_invalid`）。
  - 前端 `useApiClient.request` 自动注入 `X-Csrf-Token`（client 用 useCookie，SSR 从透传 cookie 头解析）；修复 `SFNavbar.vue` 绕过 request 的 logout。
  - 部署：compose/.env/docs 同步 `CSRF_TRUSTED_ORIGINS`；OpenAPI `info.description` 文档化 CSRF 要求。
- **关键架构约束**：API 在 Nuxt 代理后看到的 Host 是内部地址，Origin 是公开站点，二者不匹配会被默认拒绝——TrustedOrigins 必须含公开站点。非 HTTPS 无 Origin 时中间件跳过 Origin/Referer 校验但 token 校验仍生效（double-submit 是核心防线）。

## 测试覆盖

- Forum service：5 个新测试（隐藏/可见主题评论、评论不存在/隐藏主题回复、可见主题回复）。
- Identity controller：3 个新测试（HV 禁用放行、启用缺 token 拒绝、嵌套 token 通过）。
- Options：4 个新测试（denylist 拒绝、安全/通配接受、混合列表原子拒绝、IsAttachmentActiveContentType）。
- Attachments controller：3 个新测试（主动内容强制下载、安全内容 inline、文件名净化）。
- config：5 个测试（CSRF 字段守卫、CSRF_TRUSTED_ORIGINS 解析、APP_URL 派生、originsFromAppURL 无效输入、envStringSlice）。
- CSRF 中间件：7 个测试（GET 种 token、unsafe 无 token 拒绝、有效 token 通过、不匹配拒绝、envelope 映射、未授权 Origin 拒绝、授权 Origin 通过）。
- 现有 controller/server 测试通过 `CSRFEnabled: false` 显式 opt-out（业务测试不重复 CSRF 逻辑）。

## Open Questions

- compose Redis `--requirepass` 与 CSRF_TRUSTED_ORIGINS 改变了默认部署行为，是否需要在 release notes 显式提示升级注意？
- SSR 阶段后端种的 csrf_ cookie 透传回客户端浏览器的链路需人工 dev server 验证（Nitro 是否转发 set-cookie）。
- 是否要给 OpenAPI 每个 unsafe operation 显式加 X-Csrf-Token header 参数（目前仅文档化，避免契约膨胀）。

## 第二批：全栈安全审计修复（C/H/M/L）

承接 `decisions/2026-07-09-security-audit.md` 的全栈审计 backlog，本批处理剩余 23 项中的 20 项（3 项因需独立重构降级）。

### 已修复（20 项）

- **C1 扩展 Version 路径穿越**：`manifest.go` 新增 `manifestVersionPattern` 校验 `Version`，`service.go` `extractArchiveFiles` 常规文件统一 `0644` 掩码丢弃执行位。测试覆盖危险值拒绝/合法版本接受。
- **C2 生产密钥强制校验**：`config.go` `validateProductionSecrets` 在 `AppEnv=="production"` 时校验 `SessionHashSecret`/`AltchaSecret`/`MeiliMasterKey`/`APP_OPTION_ENC_KEY` 非空且非占位词，失败 panic 拒绝启动。
- **H1 防自我提权**：Identity service `ReplaceUserRoles`/`ReplaceUserPermissionOverrides` 加 `actor.ID==targetUserID` 拒绝（`ErrSelfRoleChange`）+ 授予 `super_admin` 要求 actor 本身是 `super_admin`（`ErrSuperAdminGrantRestricted`）。
- **H2a option 敏感值 AES-GCM 加密**：新增 `Support/Crypto` 包（`OptionCipher`），Options service 写入加密/读取解密，bootstrap 注入 `APP_OPTION_ENC_KEY`。透明模式兼容开发环境，明文历史值兼容解密。
- **H2b SFTP 强制主机密钥**：空指纹不再回退 `InsecureIgnoreHostKey`，改为拒绝连接。
- **H3 前端 DOMPurify**：新增 `dompurify` 依赖 + `utils/sfSanitize.ts`，`SFComment`/`SFEditor`/topic 页 `v-html` 前净化。（原文「SSR 信任服务端 bluemonday」已于 2026-11-17 更正：SSR 分支自己用 parse5 白名单净化，且与客户端同口径，见 `2026-11-17-sanitize-single-whitelist.md`。）
- **H4 安全响应头**：`Caddyfile` 加 HSTS/`X-Content-Type-Options`/`X-Frame-Options`/`Referrer-Policy`。
- **H6 容器降权**：api/worker/migrate/web Dockerfile 加非 root `sforum` 用户 + `chown` + `USER`。
- **M4 registration-status 不泄漏**：恒定返回 `NextUserIsInitialSuperAdmin=false`，消除首用户劫持信息面。
- **M5 DB sslmode**：默认与生产模板改为 `sslmode=require`。
- **M6+L4 LIKE 转义 + page cap**：Identity/Attachments/Database 的 LIKE 加 `escapeLike` + `ESCAPE '\'`；`normalizePage` 加 `maxPage=200`。
- **M7 websiteUrl scheme 兜底**：前端 `utils/sfUrl.ts` `safeUrl` 仅允许 `http`/`https`/`mailto`。
- **M8 token 版本号会话失效**：migration 加 `users.current_token_version`，AuthSession Manager 注入 `TokenVersionSource`，session 存版本号、`CurrentUserID` 比对，密码重置递增版本号使旧会话失效。
- **M9 session Secure**：bootstrap `shouldUseSecureCookie` 检测 `APP_URL` https scheme（生产强制 true）。
- **M10 前端依赖固定**：`package.json` 的 `latest` 改为 caret 精确版本（nuxt `^4.4.8` 等）。
- **L1 登录时序对齐**：用户不存在时跑 dummy argon2 对齐时序。
- **L3 paramInt 注释**说明安全兜底。
- **L5 FTP/SFTP Exists 返回真实错误**区分不存在与故障。
- **L6 policy.go 注释**文档化 `super_admin` deny 不生效（设计决策）。

### 降级待办（3 项，需独立重构）

- **L2 通用附件 EXIF 清洗**：avatar 已重编码去 EXIF；通用附件需重构上传字节流，降级。
- **M2 端点级限流**：需 Redis 失败计数器基础设施，独立功能工程；当前靠全局 `LIMITER_WRITE_MAX` 缓解。
- **M3 ALTCHA 两阶段验证**：需重构 `HumanVerify` 接口，影响所有调用方；非安全漏洞（仅配额浪费）。
