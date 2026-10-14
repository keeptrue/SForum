# Knowledge Index

Project memory entry point for humans and AI sessions. Read this file, the one
hot handoff for the current workstream, and the relevant module note. Do not
load archived sessions or completed plans as current context.

## Active Workstreams

### Extension Authoring Speed And Controlled Power

- Status: **ready**; the ordered M0-M10 task book covers selected-theme public
  page shells, page/middleware scaffolds, route-scoped custom authorization
  guards, plugin-namespaced session state, typed request conveniences, and a
  secure one-command development loop. Raw Fiber context, raw Core session
  mutation, actor minting, and pre-plugin middleware remain closed.
- Plan:
  `plans/2026-08-22-extension-authoring-speed-and-controlled-power.md`
- First step: M0 production call-chain inventory, ADR, threat model, and V3
  residual ownership reconciliation. No implementation has started.
- Modules: `modules/extensions.md`, `modules/frontend.md`,
  `modules/identity.md`

### Built-in Shortcodes

- Status: **active / blocked, M0-M9 plus M10A complete; M10B parser review complete but release gate open**; the frozen Host shortcode ref/block nodes,
  strict validation, Goldmark/Tiptap import/paste grammar, protected fallback,
  shared conformance fixture, and deterministic native hash/no-op behavior are
  joined by production exact-artifact Content Registry Protocol V2 dispatch,
  runtime leases, Host validation/sanitization/budgets, bounded batch execution,
  lifecycle recovery, and source-free traces. Seven sealed, bounded Query
  Registry projections now keep public visibility and actor decisions in Host
  SQL and give only the exact shortcode content runtime opaque one-use query
  delegations. Public source remains raw-free and actor-authorized edit-source
  APIs own editor reloads. M5 ships the protected built-in
  `sforum-shortcodes` with `user`/`category`/`friend-links` declarations,
  strict schemas, typed render segments, localized labels, real-subprocess
  Host-chain integration tests, staging build, and release-baseline updates.
  Independent M5 review patch-bumped the artifact to `1.0.1`, repaired
  renderer/URL/Unicode/error boundaries and malformed raw-string links, removed
  a stray package executable, and added exact Content Registry publication to
  legacy enable/disable/restart. M6 adds the production per-node dispatcher and
  only the `topic`/`comment` handlers, with resource-key recursion, depth/
  reference/batch/execution budgets, duplicate-ID batching, owning-topic
  visibility recheck, generation-tagged cache invalidation, and SSR-safe
  fallbacks. M7 adds the ordinary-composer visibility-filtered reference
  selector, one exact trusted L2 toolbar command, searchable five-reference
  dialog, stable NodeViews, edit/replace/delete, canonical paste/alias handling,
  i18n/Toast/focus/mobile states, and topic/comment re-edit restoration. The
  active protected artifact is `sforum-shortcodes 1.2.0` at immutable digest
  `70d43c0e...`; its L2 digest is `a3fe4c25...`. Topic 145 and comment 368 prove
  five-node publish, public render, update, reload, and continued editing.
  Focused/PostgreSQL/subprocess, fresh full Go, full Web, typecheck/build,
  architecture, extension/release/catalog, normal admin activation, disable/
  Safe Mode/L2 failure, source-secrecy, and exact 390x844 Browser checks pass.
  M8 adds the protected-content substrate: default-locale Host placeholders,
  actor-independent base reads followed by request-only authorization and
  composition, private/no-store cache policy, non-cacheable protected execution
  with Host delegation suppression, exact accepted-fragment boundaries, and
  moderation mention replay fencing. M9 activates Host-authoritative `login`,
  `reply`, and comment-only `only-author` policies and exact typed handlers;
  M10A then adds the legacy conversion fixture
  (`contracts/fixtures/shortcode-legacy-conversion-v1.json`: 35 classified
  text cases + 15 structured rejections), Go/TypeScript parity conformance
  tests, the deterministic conversion report, and the bilingual
  user/operator/author documentation. M10B reconciled the four parser findings
  and fixed TypeScript list-to-heading canonical export. Focused and race tests cover denied,
  allowed, locale, cache, fragment, and concurrent viewer behavior. Real
  PostgreSQL/Redis, browser DOM/hydration/SEO marker scans, full Web,
  typecheck/build, architecture/OpenAPI, compat, and extension/release checks
  pass. The final environment-loaded `./scripts/test.sh` run reached the Go
  suite but surfaced unrelated existing PostgreSQL fixture/schema conflicts,
  fake-provider configuration, migration isolation failures, and sandboxed
  `ps`/Go-cache restrictions; these remain residuals and are not M9 failures.
  All `/t/**` SSR responses are `private, no-store`. The development runtime's
  separately selected optional Meilisearch provider currently returns 500;
  Host PostgreSQL search and protected marker index/rebuild tests are green.
  Runtime catalogs still point to active `sforum-shortcodes@1.2.0` while
  SyncBuiltins stages `1.3.0`; normal super_admin activation is the current
  blocker. See `reports/2026-08-28-builtin-shortcodes-m10b-final-gate.md`.
- Plan: `plans/2026-08-22-builtin-shortcodes.md`
- Handoff: `sessions/2026-08-28-builtin-shortcodes-m10b-handoff.md`
- Decision:
  `decisions/2026-08-22-structured-shortcodes-public-source-secrecy.md`
- Report:
  `reports/2026-08-22-builtin-shortcodes-m0-threat-inventory.md`,
  `reports/2026-08-28-builtin-shortcodes-m10a-conversion-report.md`,
  `reports/2026-08-28-builtin-shortcodes-m10b-final-gate.md`
- Modules: `modules/forum.md`, `modules/extensions.md`, `modules/frontend.md`

### Custom Image Sticker Platform

- Status: **active design**; Core/plugin/storage/rendering architecture is
  approved, including the generated plugin catalog and immutable historical
  assets. The Forum Canvas base editor is implemented and Browser-verified;
  the sticker catalog, node, picker, and admin product are not implemented.
- Plan: `plans/2026-07-30-image-sticker-platform.md`
- Handoff: `sessions/2026-07-30-image-sticker-platform-design.md`
- Decision: `decisions/2026-07-30-image-sticker-catalog.md`
- Modules: `modules/forum.md`, `modules/extensions.md`,
  `modules/attachments.md`

### Configurable Public Navigation Platform

- Status: **active, M0-M6 complete**; all four public locations share the
  canonical actor-sensitive navigation authority, sidebar taxonomy expands
  through `core.dynamic.categories`, stored placements now match the admin
  document (footer navigation defaults empty), and exact active-theme capability
  is projected from immutable runtime state. M7 is the final lifecycle and
  release gate.
- Plan: `plans/2026-07-27-configurable-public-navigation-platform.md`
- Handoff:
  `sessions/2026-07-28-public-navigation-platform-handoff.md`
- Decision:
  `decisions/2026-07-27-operator-owned-public-navigation.md`
- Modules: `modules/options.md`, `modules/frontend.md`,
  `modules/extensions.md`

### V3 production rewire honesty remediation

- Status: **active**; the two release-blocking P0s, SystemTier start order,
  CompatFarm RPC failure handling, local full gate, and admin overview Browser
  QA are repaired. Real pre-publication rollout, Marketplace/Privacy consumers,
  remaining compatibility/commerce honesty work, and release-image smoke remain.
- Plan: `plans/2026-07-22-v3-production-rewire-honesty-remediation.md`
- Prior partial evidence: `sessions/2026-07-22-p11-p12-p13-production-rewire-handoff.md`
- Module: `modules/extensions.md`

### Built-in GitHub social login

- Status: **整改完成，等待独立复审** (2026-07-27). R1-R7 now have focused,
  isolated runtime, and Browser evidence; the generic Host/plugin architecture
  and protected built-in reference remain subject to independent acceptance.
- Plan: `plans/2026-07-27-github-social-login-builtin-plugin.md`
- Remediation:
  `plans/2026-07-27-external-auth-core-plugin-review-remediation.md`
- Handoff:
  `sessions/2026-07-28-github-plugin-restart-handoff.md`
- Prior remediation handoff:
  `sessions/2026-07-27-external-auth-review-remediation-handoff.md`
- Evidence matrix:
  `reports/2026-07-27-external-auth-r1-r7-requirements-evidence-matrix.md`
- Decision: `decisions/2026-07-27-github-social-login-builtin-v1.md`
- Module: `modules/identity.md`

### V3 production and P13 residuals

- Status: P0-P12 phase checklists are complete; Manifest V3 + Protocol V2 are
  now the only accepted extension contracts. The production-rewire remediation
  above remains open alongside the independent request-time theme-loader APILTS
  residual, so current public distributions remain prerelease scope.
- Do not remove `sforum.theme.l1.request-time-loader` before its RemoveAfter
  date plus zero-shim evidence.
- Plan: `plans/2026-07-13-trusted-plugin-theme-platform-v3.md`
- Progress ledger: `plans/2026-07-13-trusted-plugin-theme-platform-v3-progress.md`
- Handoff: `sessions/2026-07-29-manifest-v3-protocol-v2-only.md`
- Module: `modules/extensions.md`

### Content-addressed asset namespace

- Status: **implemented**; browser-facing theme and extension resources use
  Host-reserved `/_sforum` digest paths outside the JSON API namespace.
- Handoff: `sessions/2026-07-23-content-addressed-assets-handoff.md`.
- Decision: `decisions/2026-07-23-content-addressed-asset-namespace.md`.
- Residual: remove legacy `/api/v1/...assets...` compatibility paths only with
  APILTS/deprecation evidence.

### AI Assist Platform

- Status: **active**; the M0 gateway (neutral `ai.completion@1`, provider
  profiles, Secret Store credential references, three cost gates, execution
  trace), the M3 reply bot (mention/reply trigger, loop prevention, bot
  identity), and **read-only chat tools** (contract `tools[]`/`toolCalls[]`,
  both protocol adapters, Host-owned orchestration loop with a per-reply step
  budget, five Core built-in read-only tools, admin settings) are in production
  code. The plugin-facing surfaces landed with the AI plugin Host API work
  (`sforum.ai.complete@1`, `sforum.events.emit@1`, manifest `aiTools[]`);
  outbound redaction and the completion result cache are wired, and plugin jobs
  can declare the `ai` queue. **M4 purpose registry landed (2026-10-12)**: a
  purpose is now a declaration (id, bilingual label, cost class, failure
  posture, tool allowlist, resource type, blast radius, prompt version) held in
  one read-only registry, `forum.reply` is its first entry and replaced the
  `reply.*` special case, site configuration mirrors it as `purposes`, and an
  undeclared purpose is denied before any outbound call. Remaining: the
  per-purpose override UI (needs rendered QA after an API restart), M2
  moderation assistant, content purposes, the `ai.provider` slot (M3),
  completion hooks (M5), and an operator control for the result cache.
- Plan: `plans/2026-10-02-ai-assist-platform.md`,
  `plans/2026-10-03-ai-read-only-chat-tools.md`,
  `plans/2026-10-12-ai-forward-ideas.md`（blueprint：前瞻想法池，未排期）
- Decision: `decisions/2026-10-02-ai-provider-gateway.md`,
  `decisions/2026-10-03-ai-read-only-chat-tools.md`,
  `decisions/2026-10-12-ai-purpose-registry.md`,
  `decisions/2026-10-12-extension-database-grant-authority.md`,
  `decisions/2026-10-11-ai-credentials-fail-closed-without-encryption-key.md`,
  `decisions/2026-10-11-ai-reset-keeps-provider-profiles.md`,
  `decisions/2026-10-11-ai-model-list-probe.md`,
  `decisions/2026-10-11-ai-private-provider-endpoints.md`,
  `decisions/2026-10-12-ai-scope-mcp-skills-streaming.md`
- Handoff: `sessions/2026-10-11-ai-model-list-probe-handoff.md`
  (prior: `sessions/2026-10-03-ai-read-only-tools-handoff.md`)
- Modules: `modules/ai.md`, `modules/extensions.md`, `modules/moderation.md`,
  `modules/notifications.md`
- 2026-10-11 控制台修复已完成（M0-M6）：加密密钥配置、密钥保存 fail closed、
  重置保留供应商、autoAction/redaction 设置 UI、toolCalls nil 语义、诊断按实际
  绑定解析、观测页筛选与深链。计划：`plans/2026-10-11-ai-control-panel-remediation.md`
- 2026-10-11 连接配置支持一键获取模型列表（`POST
  /admin/ai/profiles/:profileId/models`，只读、不计费、不写执行记录；前端在下拉
  里选择并保留手动输入出口）。仍待渲染 QA：需要管理员凭据。
- Excluded by operator decision: embeddings/vector retrieval and multimodal
  input.

## Current Project State

- **Web:** Nuxt 4, Vue 3, Nuxt UI 4, Bun, SSR-first, `zh-CN` default and
  `en-US` secondary locale.
- **API:** Go Fiber v3, PostgreSQL, Redis, River, Goose, and sqlc.
- **Forum:** taxonomy, topics/comments, moderation lifecycle, configurable
  policy, million-scale read-path work, and content revisions V1 are shipped.
  The flat comment stream is live: `topics.comment_revision` (statement-level
  triggers) + `GET /topics/{id}/comments/revision` + SSE
  `GET /topics/{id}/comments/stream` drive no-refresh comment loading with REST
  reconciliation and polling fallback.
- **Identity:** Redis sessions, RBAC, permission overrides, first-user
  `super_admin`, and account-session management are shipped.
- **Search:** protected PostgreSQL site search is the default; Meilisearch is an
  optional external plugin; Host-ledger reconciliation repairs missing, stale,
  and obsolete documents automatically.
- **Extensions:** Manifest V3, exact-artifact trust, lifecycle, Host API v2,
  registries, Page Registry themes, theme-defined virtual system error pages,
  and buildless settings UI are present; extension-owned permission
  localization is present; Bootstrap ABI v1 is independent from Protocol V2,
  and failed initial plugin convergence leaves the API in Host recovery-only
  mode; production-rewire honesty findings remain open.
- **Notifications:** V2 is complete: transactional reply/mention/moderation
  fanout, layered policy and own-user preferences, exact-artifact plugin
  emission, per-user cross-tab durable-revision SSE leadership with REST/
  reconnect fallback, generic channel delivery, and the protected Web Push
  reference provider are shipped. The Navbar preview links to personal
  notification preferences, where Browser Notifications is a non-shrinking
  first-viewport device control.
- **AI:** the gateway, the reply bot, and read-only chat tools are shipped:
  `forum.reply` may call `forum.search`, `forum.topic.read`,
  `forum.topic.list`, `user.profile.read`, and `time.now` through a
  Host-owned loop (<= 3 tool calls per reply, per-step gates and traces, user
  quota counted once per reply). The topic body now reaches the prompt, tool
  results are treated as untrusted data, and citations carry links. See
  `sessions/2026-10-03-ai-read-only-tools-handoff.md`.
- **Dev:** Compose owns PostgreSQL, Redis, and Mailpit. The user owns the web
  dev server on port 3000; do not kill it.

## Latest Handoff

- AI 自主参与（autonomous triage）（2026-10-15）：AI 回复触发方式可配置为
  mention（默认）/ autonomous / off；autonomous 下每条新主题/评论先进
  `forum.reply.triage` 经济档判定（无工具、8 tokens、fail-closed），判 REPLY
  才入队正式生成，被 @ 仍直达。控制台 AI 设置新增触发方式三选块。
  详见 `sessions/2026-10-15-ai-autonomous-triage-handoff.md`。
- 移动端评论区几何修复（2026-10-15）：正文「拉满整行」不再硬编码 lead，改由
  `--sf-comment-avatar-size + --sf-comment-entry-gap` 算出，并补 `max-width: none`
  （同文件的 `max-width: 100%` 会把 `calc(100% + lead)` 夹回，正文右缘因此永远短一截）；
  默认主题新增「移动端评论几何」块（必须位于全端规则之后）并把评论行贴齐列表列。
  400×822 正文列 238px → 338px，桌面几何不变。**待办：** 后台「重新激活主题」激活
  暂存版本 `94aec837c69f…`（活动版本仍是 `493be898…`），否则页面仍加载旧主题制品。
  详见 `sessions/2026-10-15-mobile-comment-geometry-fix.md`。
- 徽章与成长值（gamification）整体移除（2026-10-12）：产品决定不再提供成就/徽章/成长值，
  内置插件 `sforum.gamification`（含 9 条路由、5 张插件表、种子徽章/等级、每日巡检、
  通知类型与公开徽章墙）连同其管理台页面、公开页面、i18n、集成测试、生成目录与
  Core 里为它加的三处临时探针、`cmd/sforum_valcheck_main.go`、build/dependabot 条目
  全部删除；`docs/extensions/v3` 里 7 个 UI 身份按 append-only 契约转为 retired 墓碑
  （禁止 ID 复用），生成目录与生成器输出逐字节一致。Core 框架契约（事件、`forum.profile.tabs`、
  own_schema 授权、admin surface、通知类型）保持不变——授权供给覆盖改由
  `extensions/fixtures/plugins/sforum-commerce-workflow` 等 fixture 承担。运行时侧
  `storage/` 制品与 web 构建产物已清理；扩展目录行由 `SyncBuiltins` 的
  `PruneMissingBuiltins` 在下次启动时自动停用。详见
  `sessions/2026-10-12-gamification-removal.md` 与
  `decisions/2026-10-12-remove-gamification-badges-growth.md`。

- AI 用途登记表 M4 + 两个阻塞清除（2026-10-12）：① `.github/dependabot.yml` 补上
  插件 backend 的 gomod 条目（`validate-docs.mjs` 唯一失败项消除；该条目随
  2026-10-12 gamification 移除一并删除）；② 扩展数据库精确授权
  的操作者绑定供给从此真的存在——`ProtocolStarter` 此前恒用 host 权威，导致任何
  `own_schema` 插件都无法通过正常启用流程跑起来；现在启动上下文携带
  `{actor, auditEventID}`（旧的 `ActivationCoordinator.Start` 写一条
  `extension.database_grant` 审计锚点并盖章，Lifecycle V2 host 从 durable ledger 取同一
  权威），缺锚点即退回 host 权威并失败关闭，撤销仍恒为 host 权威；顺带修掉
  `Support/AI` ↔ `Models/Extensions` 的**测试期 import 环**（AI 工具适配层移到
  `Support/AI/toolplugin`，`Models/Extensions` 测试包才重新可编译）。③ M4
  `ai.purpose` registry：用途成为声明（id/成本等级/失败姿态/工具白名单/资源类型/影响
  范围/提示词版本），`forum.reply` 是唯一条目并取代 `reply.*` 特例；站点配置新增
  `purposes`（旧 `reply.tools` 归一化时迁移）；未声明用途被网关拒为
  `purpose_unknown`（零出站 + denied trace），站点关闭仍只有一个答案
  `gateway_disabled`；回复生成器不再自带工具常量/成本等级/提示词版本。契约与 OpenAPI
  已同步补齐（含此前缺失的 `reply.tools`/`replyBot`/`defaultSystemPrompt`/
  `safetyAppendix`）。验证：构建、聚焦套件、架构门禁、OpenAPI/Host API/文档校验、隔离
  集群端到端全绿；Web 侧 `bun test` 1119 pass（2 个失败项属当时并行的
  gamification 工作流，该插件已于 2026-10-12 移除）、typecheck、生产构建（需
  `NODE_OPTIONS=--max-old-space-size=6144`）全绿；用途页签在真实浏览器完成端到端
  （含持久化/回显/恢复默认闭环），并修掉三处渲染缺陷（USelect 空值选项哨兵化、
  value-key/label-key 补齐、以及**所有 AI 页签共有的 hydration mismatch**——三个
  useAsyncData 从未被 await，SSR 按空兜底渲染、客户端按真实载荷水合）。
  **遗留：390x844 窄屏渲染 QA。** 详见
  `sessions/2026-10-12-ai-purpose-registry-m4-handoff.md`、
  `decisions/2026-10-12-ai-purpose-registry.md`、
  `decisions/2026-10-12-extension-database-grant-authority.md`。

- AI 插件 Host API（M1 命令 / M1.5 事件 / M2 工具）落地并接线，外加两项 M1 遗留
  收口（出站脱敏、结果缓存）与插件 job 队列声明（2026-10-11/12）：插件发起补全走
  `sforum.ai.complete@1`（`ai.invoke`），发布自身事件走 `sforum.events.emit@1`
  （`events.emit`），贡献工具走清单 `aiTools[]` + 保留 slot `sforum.ai.tool`（登记
  不等于暴露：不进任何 Core 用途白名单）；脱敏在 `Gateway.Execute` 于缓存键与出站
  之前执行；结果缓存接 Redis、命中零成本并回报 `cacheHit`；`jobs[].queue: "ai"` 让
  慢任务进入 AI 低并发通道。两处基线按棘轮调整（`manifest.go` 920→916 下调、
  `Support/Extensions` 扁平目录 145→147）。验证：`go build ./...`、聚焦 Go 套件、
  架构门禁、目录文档同步、Host API 文档校验全绿；隔离集群上跑通插件命令端到端
  （成功记账 / 配额用尽 / 站点关闭 / 能力未授予）、AI 工具子进程派发，以及事件发布
  端到端——后者当场抓到 `sforum.events.emit@1` 无法通过 broker 规划的缺陷（Preview
  缺 Policy、Prepare 只带 private），已修并加单测钉住。插件工具的暴露也定案并落地：
  回复用途的白名单来自站点配置 `reply.tools.allowed`（缺省＝用途声明的 Core 列表，
  插件工具必须被点名才对模型可见），同时修掉了「设置 DTO 丢 reply 字段导致保存清空
  提示词」的既有缺陷。详见
  `sessions/2026-10-11-ai-plugin-host-api-m2-handoff.md`、
  `plans/2026-10-11-ai-plugin-host-api-m1.md` 与
  `decisions/2026-10-11-ai-tool-contribution-m2.md`、
  `decisions/2026-10-11-ai-outbound-redaction.md`、
  `decisions/2026-10-11-ai-result-cache-wiring.md`、
  `decisions/2026-10-12-plugin-job-queue-declaration.md`。
  注：本工作流在 2026-10-12 00:28 遭遇一次工作树回滚（tracked 改动被恢复到 HEAD），
  恢复清单见 `sessions/2026-10-12-ai-host-api-recovery-note.md`。
- 插件运行时全量恢复与测试隔离（2026-10-12）：集成测试污染开发库导致
  `sforum.gamification` 精确数据库授权丢失、全站 503。已通过正式生命周期恢复
  （rev 29 applied）、身份注册表 tip active、公开接口全部 200；新增
  `scripts/test-api-isolated.sh` 独立测试集群 + 包级连接守卫 + AGENTS.md Agent
  约束；修复插件响应契约（TypedDocument 字符串）、OpenAPI 片段声明与并行
  M1.5 会话的编译断点。遗留：并行会话两处架构基线残留（当时记录的成就公开路由
  422 随 2026-10-12 gamification 整体移除而失效）。详见
  `sessions/2026-10-12-plugin-runtime-recovery-and-test-isolation.md`。

- AI 回复的身份标识与成员检索（2026-10-11）：提示词里的每条发言从「只有昵称」改为
  `显示名（@用户名）`（`AuthorRef`，主楼作者也补上了），并新增第六个内置只读工具
  `user-search`（昵称 → @用户名 + 主页链接），使 `user-profile-read` 真正可用；
  `profile.Service.SearchPublicUsers` 是这条路径的公开读取面——空查询直接拒绝
  （否则 `%%` 会列出全站成员）、LIKE 元字符转义、只匹配 active、只投影
  id/username/displayName。提示词版本 `forum-reply@4`。详见
  `sessions/2026-10-11-ai-reply-user-identity-handoff.md` 与
  `decisions/2026-10-11-ai-reply-endpoint-identity-and-user-search.md`。
- 通知深链定位修复（2026-10-11）：通知点击进帖子定位失败/加载不出新回复的
  两处根因——深链不带评论页码（跨页目标 SSR 永远先渲染第 1 页）、同页新回复
  不触发评论缓存重取。后端 `ResolveNotificationTarget` 输出
  `/t/{id}/page/{N}#comment-{id}`，话题页规范化重定向保留 page 段，
  `useTopicCommentAnchor` 对"目标应在当前页但不在旧列表"强制刷新一次。
  当时阻塞浏览器端到端复验的 gamification 插件路由目录问题，已随 2026-10-12
  该插件整体移除而消失。详见
  `sessions/2026-10-11-notification-comment-deeplink-page-handoff.md`。
- AI 控制台连接配置支持一键获取模型列表（2026-10-11）：新增只读探测路由
  `POST /admin/ai/profiles/:profileId/models`，用表单里的地址与密钥在保存前就能拿到
  供应商模型列表，前端以下拉选择呈现并保留手动输入出口；探测不经过网关、不计费、
  不写执行记录。回复提示词同时搬到独立页签（providers / replyPrompt / diagnose /
  observability），可编辑草稿与保存操作条上移到页面壳，任一页签都能就地保存。
  详见 `sessions/2026-10-11-ai-model-list-probe-handoff.md`。
- 受保护短代码插入修复（2026-10-10）：校验失败的插入不再改动文档，五种失败
  原因拆分为可操作性文案；阶段二把包裹目标固定为顶层块并为引用插入加容器前置
  校验；阶段三空行可直接插入空脚手架（块内占位、发布门拦截、空块不进
  Markdown）；详见
  `sessions/2026-10-10-shortcode-protected-insert-fix-handoff.md`。
- 论坛编辑器 @提及自动补全（2026-10-10）：`SFEditor` 新增 `enableMentions`，
  复用 `/composer/references?kind=user` 做候选、纯文本 `@username ` 插入 +
  装饰高亮；详见 `sessions/2026-10-10-editor-mention-autocomplete-handoff.md`。
- 插件候选升级入口（2026-08-29）：后台扩展总览与插件管理已把模糊的“候选”改为
  “待升级”，并明确当前版本仍继续运行；每个 staged 插件新增“升级”，工具栏新增
  “全部升级（N）”。批量流程逐个执行，V2→V2 使用原生 upgrade ledger，legacy 或
  可恢复停用来源使用既有 exact staged restart bridge；精确信任或能力审阅会暂停队列，
  确认后继续，未新增绕过 Host 安全门的批量后端。完整 Web 947 项、typecheck、生产
  build、架构与 staged contract 均通过；桌面及 390×844 Browser QA 覆盖 GitHub、
  Meilisearch、紧凑工具栏、操作换行和无页面横向溢出。证据见：
  `sessions/2026-08-29-extension-candidate-upgrade-ui.md`

- Built-in Shortcodes editor preview（2026-08-29）：正式 NodeView 已采用确认的
  B 方案——悬浮类型标签、安静的编辑器内预览主体和选中描边；节点内不再放置更换、
  定位、解除保护或删除按钮。用户引用显示安全头像视图，分类引用显示已有 icon 与
  六位十六进制颜色；选择对话框、缓存、Go API 和 OpenAPI 同步扩展，原始 email 不
  会返回浏览器。短代码公开态、保护/fallback、编辑器 NodeView 与选择器 CSS 已全部
  移出 Core，由 `sforum-shortcodes@1.3.3` exact style asset 持有；Core 仅提供通用、
  失败关闭的 `core.surface.forum-content` 目录与 SRI 加载器。`SFEditor` 同时修复了直接
  输入 `[` 被自身 Markdown 回写重新导入成 `&#91;` 的问题。发布态另修复了错误 DOM
  选择器和保护 fallback 双层大卡片，现与编辑器共享悬浮标签、圆角、描边、渐变与
  密度但保持只读。完整 Web、typecheck、production build、focused Go、架构和 OpenAPI
  校验均通过。当前运行态仍是 v1.3.1，需要正常 super_admin 确认激活候选 v1.3.3 后
  才会看到修正。证据见：
  `sessions/2026-08-29-shortcode-editor-preview-handoff.md`

- Built-in Shortcodes presentation hooks（2026-08-28）：公开短代码输出在 Host
  消毒后由固定 `sf-shortcode` wrapper 提供主题可用的语义挂点；保护块的授权结果和
  fallback 同样包装，写入时的 `sf-editor-fallback` 也补齐共享中性卡片样式。插件
  仍不获得任意 class/style/data 属性，短代码声明、授权、缓存和源码投影均未改变。
  Go focused、完整 Web 单测和架构边界检查通过。证据见：
  `sessions/2026-08-28-shortcode-presentation-hooks.md`

- Built-in Shortcodes M10A（2026-08-28）：新增
  `contracts/fixtures/shortcode-legacy-conversion-v1.json`（8 个冻结声明表、
  唯一 `topic-tag`→`category` 别名、`password` 明确 deferred、29 个文本用例按
  converted(13)/literal(3)/invalid(10)/unsupported(1)/over-limit(1) 分类，
  15 个结构化拒绝用例按 `authority: both|host` 标注）；Go 与 TypeScript 用同一
  fixture 验证分类、canonical 输出与 fallback code 完全一致；用户/运营中英文文档
  （`docs/{zh-CN,en-US}/usage/shortcodes.md`）与扩展作者 Reference 6 已交付。
  未改任何运行时字节；记录 4 个既有 Go/TS parser 一致性发现，列为 M10B 启动前
  必须解决项。证据：
  `sessions/2026-08-28-builtin-shortcodes-m10a-handoff.md`、
  `reports/2026-08-28-builtin-shortcodes-m10a-conversion-report.md`

- Built-in Shortcodes M9（2026-08-28）：在 M8 protected substrate 基础上完成
  Host-authoritative `login`、`reply`、comment-only `only-author` 策略、M4
  projection 接线、严格 Protocol V2 typed handlers、编辑器创建/包裹/编辑/删除/
  解包/预览/发布/重新编辑流程和 `sforum-shortcodes@1.3.0` exact artifact。
  失败路径保持 closed fallback，protected 响应为 `private, no-store`，且无
  source/secret-marker 泄漏。Focused/race Go、full Web、typecheck/build、
  extension/release/catalog、PG/Redis 和 desktop/390x844 Browser 证据已通过。
  `M10A/M10B` 未开始。证据见：
  `sessions/2026-08-28-builtin-shortcodes-m9-handoff.md`

- Web SDK Release 重试修复（2026-08-22）：Node 24.19.0/npm 11.17.0 会把与
  Node 23 首发包完全相同的文件集编码为不同 `.tgz` 字节，导致
  `v3.0.11-alpha.2` 的 SDK 发布门禁误报同版本内容冲突。发布器现先比较
  tarball SHA-512，字节不同时再下载 registry artifact，按路径、权限、大小与
  文件内容计算规范化 SHA-512；规范内容相同安全跳过，真实内容差异仍硬失败。
  精确 CI 工具链端到端复现及相关门禁通过；旧标签不可移动，需由新提交和新
  prerelease tag 验证完整 Release：
  `sessions/2026-08-22-web-sdk-release-retry-fix.md`
=======
- 评论区免刷新加载（2026-10-03）：flat 时间流新增「修订号 + SSE 信号 + REST 对账」
  三层：`topics.comment_revision` 由 `comments` 语句级触发器维护并按主题
  `pg_notify`，`GET /topics/{id}/comments/revision` 是不过缓存的事实来源，
  `GET /topics/{id}/comments/stream` 只推修订号（10s 心跳 / 15s 对账 / 60s 寿命 /
  进程级连接预算，超限 429 退化轮询）。前端抽出通用 revisionStream 运行时（通知流
  同步收敛到它），新增 `useTopicCommentLive` 与 `SFCommentStreamNotice`：在最后一页
  底部静默追加，上滑/跨页/编辑器打开只出胶囊，数量不变时静默 diff 后才提示。
  Go 包测试 + 迁移触发器集成测试（真实 Postgres）+ Web 966 测试 + 架构/目录门禁通过；
  浏览器 QA 已做（桌面 + 390x844 移动端：底部静默追加 / 上滑 sticky 胶囊 / 点击胶囊追平 /
  多标签协同），并修掉三个只有浏览器才能暴露的缺陷（内层滚动容器下的阅读位置判定、SSE 游标
  与对账版本混用同一个 ref、移动端底栏盖住胶囊）：
  `sessions/2026-10-03-topic-comment-live-stream.md`,
  `decisions/2026-10-03-topic-comment-live-stream.md`
- 论坛 @提及高亮与资料卡（2026-10-03）：正文里的 `@用户名` 现在在客户端被装饰成
  `a.sf-mention`（跳过链接与代码块），点击弹出与「点评论头像」同一张公开资料卡；
  后端 HTML 与存量内容不变（SSR 仍输出纯文本）。语法与通知口径对齐：
  token 支持 `-`、上限 50 → 64，editor-document 存档先还原 Markdown 再解析提及
  （代码块里的 @ 不再触发通知）。Web 943 测试、Go 相关包、typecheck、架构门禁通过；
  渲染态 Browser QA 未做（本会话无 BrowserSkill CLI、桌面工具不可用）：
  `sessions/2026-10-03-forum-mention-linkify-preview.md`，
  `decisions/2026-10-03-forum-mention-rendering.md`
- 深链评论资料卡被裁剪修复（2026-10-03）：`#comment-{id}` 命中的评论只要留在 URL 上
  就一直带 `:target`，而扫光高亮规则给它加了 `overflow: hidden`（为了裁掉越界光带），
  于是评论内绝对定位的 @作者资料卡在评论下沿被整块切断。现在光带改为盒内
  `background-position` 动画（`-66.7% → 166.7%`），定位评论保持 `overflow: visible`，
  动画时长与观感不变；headless Chrome 实测扫光仍逐帧推进、卡片 95px 溢出评论下沿也
  完整绘制（桌面 1440×900 与 390×844 均验证），Web 939 测试、架构门禁通过。契约记录在
  `modules/forum.md`。
- 评论流时间流排序（2026-10-03）：flat 评论列表原本按 `path_key` 树序展开，回复被
  插在被回复评论下面，且楼层号（列表序号）会因他人回复整体漂移。现在 flat 统一
  `created_at ASC, id ASC`：新回复恒在最底楼、楼层稳定；位置口径（keyset 游标、
  `CountCommentsBefore`、深链页码反查、个人主页 `commentPage`）全部对齐同一排序键，
  新增 `comments_topic_created_idx (topic_id, created_at, id)` 迁移（本机已应用）；
  回复关系继续由 `replyTo` 引用卡表达，并在发布成功后由
  `useTopicCommentAnchor.focusCreatedComment` 把视口带到新楼层。Go 全量、真库集成
  测试（含变异验证）、Web 923 测试、typecheck、架构与 OpenAPI 门禁通过；渲染态
  Browser QA 未做（本会话无 BrowserSkill CLI、桌面工具不可用）：
  `sessions/2026-10-03-comment-stream-chronological-order.md`，
  `decisions/2026-10-03-comment-stream-chronological-order.md`

- 公开列表排序（2026-10-03）：首页排序按钮此前只在主题皮肤里定义且被
  `.sf-theme--default` 限定，Core fallback 路径（本实例全部公开页）下退化成纯文字。
  新增 Core 样式表 `assets/css/sforum-sort-control.css`（首页 / 分类目录 / 标签页
  统一分段控件，双渲染路径都覆盖），并把首页排序从「前端对已加载页重排」改为
  服务端 `sort=active|latest|hot` + URL 状态（换 sort 自动重置分页与 cursor）。
  公开文案：最新（最后活跃，默认）/ 最新帖子（发布时间）/ 热门；推荐默认排序由
  `latest` 改为 `active`（对齐公开活动索引）。Web 922 测试、typecheck、Go 全量、
  架构门禁通过；CDP 真实点击验证 URL / 请求 / 顺序一致：
  `sessions/2026-10-03-public-feed-sort-control.md`，
  `decisions/2026-10-03-public-list-default-sort-active.md`

- 原生推送设备注册（2026-10-02）：Core 新增 `push_devices` 与自服务端点
  `GET/POST /api/v1/push/devices`、`DELETE /api/v1/push/devices/{deviceId}`；令牌按
  `(platform, token)` 唯一、重复注册改绑并撤销同 deviceId 旧令牌，落库只存 SHA-256
  指纹 + Core 密钥密文，响应永不回显。FCM/APNs 传输仍属 provider 插件，本轮未声明
  `native_push` 通道键（通道 + provider 槽 + 参考插件一起落地）。迁移
  `202610020004` 已在本机开发库应用（原 `202610020001` 与并发工作流重号）。
  为让生成器可运行，代并发 AI 工作流按约定补了三条 UI 身份目录。
  Go 全量、真库集成测试、目录 `--check`、架构/文档/OpenAPI 门禁通过：
  `sessions/2026-10-02-native-push-device-registry.md`，
  `decisions/2026-10-02-native-push-device-registry.md`

- 客户端版本门禁（2026-10-02）：新增三个 public 选项 `client.minimum_version` /
  `client.recommended_version` / `client.update_notice`（默认全空 = 不限制，
  `settings.site.manage` 管理），App 冷启动读 `GET /web-options` 自行判定是否强制
  升级；后台新增「站点设置 → 客户端」固定标签页并提供一键恢复推荐默认。服务端不代
  判定升级、不记录客户端版本，也未引入 Deprecation/Sunset 头（无真实消费者）。
  Go 选项测试、Web 920 测试、typecheck、生产构建、架构与文档门禁通过；为守住
  `Models/Options/service.go` 的遗留大文件基线，把 Defaults 覆盖块抽到
  `defaults_overrides.go` 并把该 cap 从 1119 下调到 1090。
  **后台标签的渲染态 Browser QA 未做**（本会话无 BrowserSkill CLI，桌面工具不可用）：
  `sessions/2026-10-02-client-version-policy.md`，
  `decisions/2026-10-02-client-version-policy-via-public-options.md`

- App / 机器客户端登录换令牌（2026-10-02）：`POST /auth/login` 新增可选
  `issueApiToken`，一次密码登录同时签发 PAT（规则与 `POST /auth/tokens` 一致：
  scopes 显式子集、明文只返回一次；未携带时响应仍是 `CurrentUser`），原生 App
  从此不需要 cookie jar、CSRF double-submit 与两步换取；令牌管理端点继续只
  接受 cookie 会话，App 登出仍只能清除本地令牌。契约新增
  `ApiResponseLogin` / `LoginIssuedToken`，双语 API 文档补「登录即换令牌」章节，
  Go 全量测试、OpenAPI 引用、文档与架构边界门禁通过：
  `sessions/2026-10-02-app-client-login-token.md`，
  `decisions/2026-10-02-app-client-login-token.md`

- seed:forum 全量假数据（2026-10-02）：small profile 现生成用户公开资料、分类
  分组/分类（站务 → 技术 → 生活目录）、标签、带标签/置顶/浏览数的主题与嵌套
  评论；新增 `--tags/--pinned/--views-max`，`--categories` 在 small 生效，
  显式 0 关闭单项。分类/标签走 `forum.Service`（super_admin），收尾批量回填
  计数。开发库实跑 400 主题/1552 评论/16 标签，二次运行 taxonomy `+0/+0/+0`；
  `go test ./...`、架构边界与文档门禁通过：
  `sessions/2026-10-02-seed-forum-taxonomy-profiles-handoff.md`

- AI 辅助平台 M0 内核（2026-10-02）：`app/Support/AI` 基础设施（中立补全契约、
  provider profile、三层配置、三道闸门、双协议适配器、受控出站、Secret Store
  凭证解析、Postgres 存储、网关编排）、`app/Models/AI` 权限感知服务层、
  `Http/Controllers/AI` 五个管理端点、Provider 与 bootstrap 装配、OpenAPI 契约、
  `ai.manage` 权限与本地化，以及两个已应用的迁移。`go test ./...` 121 包全绿、
  Web 915 pass、目录与文档门禁通过，运行时探测确认新端点返回 401 而非 404，
  并已用真实 DeepSeek key 打通端到端调用（含生产 SSRF 守卫）。执行路径为 Core
  协议翻译 + Host 受控出站，`ai.provider` 槽位保留给非标准协议。
  Admin 控制台（供应商与闸门、用量与执行记录，含密钥录入端点）亦已落地并通过
  类型检查与生产构建。**剩余：渲染态浏览器 QA；M2 审核助手（purpose/middleware
  扩展点据其提炼）**：
  `sessions/2026-10-02-ai-assist-platform-design.md`，
  `plans/2026-10-02-ai-assist-platform.md`，
  `decisions/2026-10-02-ai-provider-gateway.md`

- Dependabot 告警收敛（2026-10-02）：94 条 open 告警拆解为 3 个 grpc advisory
  × 18 个 Go module（54 条）与 4 个废弃 PHP 分支遗留 `composer.lock`（40 条）。
  18 个 module 的 grpc 统一升至 `v1.83.2`，删除 `v2`/`dev`/`php82`/
  `zhuchunshu-patch-1` 并以 `archive/php-*-final` tag 归档，关闭 14 个被取代的
  依赖 PR；Go 全量构建、`apps/api` 全量测试（ok=119, fail=0）与架构边界门禁
  通过，Dependabot open 告警已归零（94 → 0，其中 40 条 composer 由分支清理
  触发自动修复），7 个 builtin 插件按共享 SDK 规则递增补丁版本：
  `sessions/2026-10-02-dependabot-alert-triage-handoff.md`

- 容器镜像扫描修复（2026-10-02）：`sforum-web:ci` 镜像 Trivy 报出 6 个 HIGH。
  `@tiptap/*` 12 个包 3.27.1 → 3.31.3，`devalue` 5.8.1 → 5.9.4、`sharp`
  0.35.3 → 0.35.5 经 `overrides` 强制；同时收敛 `prosemirror-model`
  1.25.12 与 `prosemirror-view` 1.42.6，消除 Tiptap 升级引起的双版本类型
  不兼容。`bun run typecheck` 通过，`bun test` 904 pass / 1 fail（失败项
  `pluginRouteProxy` retry-read 在升级前依赖上同样失败，属既有环境问题），
  CI 五作业全绿：`sessions/2026-10-02-dependabot-alert-triage-handoff.md`

- Topbar 头像菜单后台入口（2026-10-02）：`usePublicUserMenu` 新增 `admin`
  条目，桌面下拉与移动右抽屉同源；准入复用 API 权威键 `admin.access`
  （`super_admin` 角色绕过 + moderator/operator/tech_admin 模板，member 不含），
  路径经 `useAdminRoutes` 跟随 `adminRoutePrefix`。新增身份矩阵回归与门控测试，
  Web 全量 915 测试、类型检查、架构与身份/管理框架门禁通过：
  `sessions/2026-10-02-topbar-admin-entry.md`

- 首注册超管提示改到注册成功后（2026-10-02）：注册页删除公开 bootstrap
  预告分支与 `auth.firstUserAdminNotice`，改为在 `POST /auth/register` 响应
  报告 `isInitialSuperAdmin` 时提示 `auth.initialSuperAdminGranted`；公开
  `registration-status` 仍恒定 `false`。聚焦回归、Web 全量 905 测试、类型
  检查与架构门禁通过，渲染态 Browser QA 未做：
  `sessions/2026-10-02-first-registration-super-admin-notice.md`

- Code scanning 与 CI 整改（2026-08-20）：修复 CodeQL #24 的切片容量加法
  溢出与 #25 的 `int64` 到 `int` 未界定转换；Web Docker 构建补齐
  `plugin-ui` workspace，SDK 离线消费者所需 Vite 工具改为直接依赖，并补齐
  component asset contextual-guard 目录契约。7 个内置插件按共享 SDK 规则递增
  补丁版本并刷新发布基线；Go 全套、Web prod 容器、staging extension test、
  兼容性、类型检查和其余门禁通过。单次 `test.sh` 仅受本地沙箱禁止 `/bin/ps`
  限制，失败前后步骤已分别验证：
  `sessions/2026-08-20-code-scanning-ci-remediation.md`

- 插件运行时内存优化（2026-08-20）：Protocol V2 插件侧传输已从旧 Host runtime
  解耦，SDK 非标准依赖从 396 降至 164；SMTP 从 397 降至 165，Linux stripped
  二进制从 23,011,490 降至 15,618,210 bytes。插件子进程固定注入
  `GODEBUG=disablethp=1`，隔离 Linux 对照中 SMTP PSS 从 27,360 降至
  19,284 KiB。7 个模块测试、Linux release 构建、staging digest/extension test、
  架构与发布基线校验通过。第二阶段已将 FS/S3 解耦到轻量存储 SDK，同条件
  Linux PSS 再减少约 12.1 MiB；预计生产插件总 PSS 从约 205 MiB 降至
  138-148 MiB。后台资源接口现已提供 60 秒 PSS 中位数和 `AnonHugePages`，
  待下个 release 部署后复测：
  `sessions/2026-08-20-plugin-runtime-memory.md`，
  `decisions/2026-08-20-plugin-runtime-memory.md`

- Plugin UI SDK 分发与一键构建（2026-08-20）：`@sforum/admin-sdk@1` 和
  `@sforum/plugin-ui@1` 已具备 npm 精确打包、版本/API 一致性、离线消费构建及
  Release OIDC 发布门禁；`extension build` 会在作者侧完成 Bun 构建、摘要刷新、
  完整校验和契约测试，生产仍不执行源码或 package scripts。两个 npm 包的
  `1.0.0` 已通过 `sforum` 账号 2FA 首次发布，registry 下载与审核 tarball
  SHA-256 完全一致；两者均已绑定 `zhuchunshu/SForum` 的 `release.yml`
  Trusted Publisher，仅允许 `npm publish`，并已切换为必须 2FA、禁止 bypass
  token 的严格发布策略。下一次 SDK 版本变化时再验证 OIDC provenance；已有
  `1.0.0` 不会被重复发布：
  `sessions/2026-08-20-plugin-sdk-distribution.md`，
  `decisions/2026-08-20-plugin-sdk-distribution.md`

- 审核通知与发布可见性修复（2026-08-20）：新建或重新入队的待审核主题/评论
  现在会在原写事务内通知所有当前有效 `moderation.review` 用户（含
  `super_admin`、遵循用户 allow/deny、排除提交者），通知目标在读取时重新鉴权并
  直达审核工作台。预发布审核通过提交后会同步失效 Forum Redis 公开读模型；
  两个审核前端同时清除 Nuxt 首页 async-data/useState，首页整页改为
  `no-store`，同一浏览器返回首页即可读取新发布内容。Go/Web 全套、类型检查、
  OpenAPI 引用和架构门禁通过：
  `sessions/2026-08-20-moderation-notification-publication-visibility.md`

- Topic Webhook 业务快照（2026-08-19）：全部 `topic.*` observe 事件保留旧
  ID/slug 字段并新增 `path/url/topic/author/category/tags`；URL 跟随实时
  `site.url` 与 `seo.topic_url_mode`，作者/分类/标签发送公开名称，不发送邮箱、
  正文或编辑理由。出站 Webhook 通过既有事件桥直接获得同一 payload，补充读取
  fail-open，不改变主题写入成功条件。Go 全套、事件文档同步与架构门禁通过：
  `sessions/2026-08-19-topic-webhook-payloads.md`

- 移动端左右抽屉全视口修复（2026-08-19）：根因是 2026-08-01 仅把 Core
  回退样式改为 `inset/top: 0`，默认运行时主题仍用旧的 topbar 偏移并在不可变
  skin 加载后覆盖 Core。现已同步默认主题、增加 Core/主题成对回归，完成
  digest/validate/test、SyncBuiltins 与 super_admin 正常重新激活；活动摘要
  `0020c49a…`。Chrome `402x849` 实测左右抽屉与遮罩均从视口顶部开始、覆盖完整
  高度，无横向溢出或控制台错误：
  `sessions/2026-08-19-mobile-drawer-viewport-fix.md`

- 已验证 Release bootstrap（2026-08-18）：新增 `sforum-bootstrap.sh` 作为安装/
  更新唯一推荐联网入口；每次先解析不可变目标标签、校验并自刷新 bootstrap，
  再校验同标签完整部署包后交给 deploy/upgrade 状态机。老实例一次接管即可，
  `.env.production`、`.deployrc`、路由 runtime、数据与卷不变，受管工具先备份到
  `.sforum/tooling-backups/`；Release 资产、SHA256SUMS/attestation、README、
  中英文部署文档、Release Notes 和 CI 门禁已同步：
  `sessions/2026-08-18-verified-release-bootstrap.md`，
  `decisions/2026-08-18-verified-release-bootstrap.md`

- API 独占 Worker（2026-08-19）：生产 SMTP 连接测试成功但真实投递仍报 535，
  根因是 API 读取 SettingsLifecycle/SecretStore，而可选独立 Worker 可退回旧
  `extension_settings`。现已删除 Worker 所有权开关以及开发、普通生产、蓝绿
  Compose 的独立 Worker 服务；API 始终承载 River 和同一扩展 runtime。
  deploy/upgrade 会按精确 Compose 标签清理旧 `worker*` 容器，遗留
  `EMBED_WORKER_IN_API=false` 不再生效：
  `sessions/2026-08-19-api-owned-worker-only.md`，
  `decisions/2026-08-19-api-owned-worker-only.md`

- 编辑器正文渲染一致性修复（2026-08-17）：撰写、客户端预览与正式
  `.sf-prose` 现共享正文语义 CSS，显式恢复被 Tailwind Preflight 清除的
  `ul/ol` marker，并统一段落、H2/H3、列表/嵌套、引用、代码、链接和分隔线；
  DOMPurify 白名单测试与 Tiptap 浏览器结构测试解耦。后端
  `orderedList.start` 统一为整数规范，保留零/负数，省略默认 1，拒绝小数和
  字符串，并只在 `ol[start]` 放行带符号十进制整数。Web 全套测试、类型检查、
  Go 全套测试、架构门禁通过；Browser 在桌面及 390x844 下确认编辑/预览/正式
  列表 marker、26px 列表缩进、6px 列表项内缩与 H2/H3 层级一致，无横向溢出、
  无控制台错误：
  `sessions/2026-08-17-editor-content-parity.md`

- Release artifact 执行位修复（2026-08-17）：v3.0.9-alpha.2 的
  verify-release-assets job 失败，根因是 `upload-artifact` →
  `download-artifact` 跨 job 传输不保留 Unix 执行位（upgrade.sh 以 0644
  落地），finalizer 的 `[[ -x ]]` 检查失败。finalize-release-assets.sh 现
  在保留存在/非空/shebang 检查并新增 `bash -n`，内容校验通过后集中
  `chmod 0755` 再做 `-x` 后置检查，最后生成 SHA256SUMS（chmod 不进
  release.yml，finalizer 独立处理真实 artifact 输入）。release_assets_test.sh
  新增回归：构建后断言可执行 → `chmod 0644` 模拟 Artifact → finalize 必须
  成功 → 断言执行位恢复与 SHA256SUMS 唯一条目 → 断言压缩包内 deploy.sh/
  upgrade.sh/deploy/scripts/*.sh 权限仍由 tar 保存（变异验证确认测试能
  捕获原始缺陷）。bash -n、release/assets 测试、release workflow 校验、
  go test 全部通过；完整 test.sh 停在 compatibility farm（未配置
  DATABASE_URL，既有限制）。修复合并后需新建 prerelease tag 验证，不得
  移动旧标签：
  `sessions/2026-08-16-docs-version-governance-remediation.md`

- 扩展作者文档补齐（2026-08-16）：新增 `docs/extensions/routes.md`（插件声明式
  路由：manifest `routes[]` 语义、core guard、Protocol V2 InvokeRoute/流、
  入口 probe + Nuxt 代理、CSRF/PAT、限制与测试，锚定 sforum-custom-content）
  与 `docs/extensions/build-and-load.md`（backend go.mod replace 接线、前端
  prebuild、digest/validate/package、dev 加载与信任迭代循环）；**中英双语**：
  完整中文版在 `docs/zh-CN/extensions/`，`docs/en-US/extensions/` 为英文入口
  页（满足 zh/en 平行校验），zh/en README 与 authoring-guide 交叉链接；
  authoring-guide 新增 Reference 4 路由参考。
  **待办**：guard 执行运行时落地后同步 routes.md Guards 表。
  `sessions/2026-08-16-extension-authoring-docs-handoff.md`

- 文档与版本治理修复（2026-08-16，三轮整改）：deploy.sh/upgrade.sh 默认
  **stable** 通道；deploy.sh 区分**当前版本 vs 目标版本**（install/update 按
  channel 解析 latest，维护动作用已部署版本且不访问 GitHub，交互多动作不再
  污染后续目标版本）；下载/校验流程统一为 `set -eu` 子 shell 先存文件→按
  文件名字段唯一选择 SHA256SUMS 条目→checksum→可选 provenance→校验后解压；
  Release 固定名称部署资产 `sforum-deploy.tar.gz` + `upgrade.sh`（纳入
  SHA256SUMS/attestation）；upgrade.sh 刷新入口改为
  `releases/latest/download/upgrade.sh`（不再用 main raw）；文档修正
  POST /auth/password、外部登录全路径、CSRF 豁免、Webhook gateway skeleton
  等事实；`tests/validate-docs.mjs` 加强（未知或未解析 CLI 注册均失败、
  extensions README 链接、完整 prerelease token 不再豁免同前缀稳定版本、
  Dockerfile/Go 一致性、文档 HTTP method/path 同时对照 OpenAPI 与 Go Route
  Catalog、安装命令 fail-closed）并新增失败路径测试；Dependabot
  以 `docker-compose` 覆盖根 Compose，并覆盖 tools/proto、tests/compat，Compose
  基础镜像均固定明确版本。2026-08-17 又修复了可选扩展 README 被整目录忽略
  而仅在本地可见的 CI 断链，并让文档门禁拒绝指向 gitignored 目标的链接；
  失败路径夹具增至 14 个用例。
  **阻塞性冲突未变**：路线图要求发行保持
  prerelease，而 2026-08-01 记录 "v3.0.1 准备进入 release gate"，需产品决定：
  `sessions/2026-08-16-docs-version-governance-remediation.md`

- CI Quality gate Web 测试 DOM 竞态 + Web 运行镜像 OpenSSL 漏洞修复：新增 Bun
  `[test].preload`（`tests/helpers/setup-dom.ts` + `tests/helpers/dom.ts` +
  `apps/web/bunfig.toml`）在任一 vue/@tiptap/vue-3 导入前安装 happy-dom，消除
  `@vue/runtime-dom` 模块级 `doc` 被冻结为 null 的加载顺序竞态；批次挂载测试
  复用集中 helper，并新增 `editorDomLoadOrder.test.ts` 回归。prod Docker stage
  新增 `apk add --upgrade 'libcrypto3>=3.5.7-r0' 'libssl3>=3.5.7-r0'` 修复
  CVE-2026-45447（Trivy 0 漏洞、保持非 root 与 Bun 启动方式）：
  `sessions/2026-08-15-web-test-dom-race-openssl-fix.md`

- Built-in `sforum.storage-s3` release baseline drift fixed: the prior S3
  EventStream DoS commit wrote a wrong `sourceDigest` for 1.0.3; the Bun
  migration did not change plugin source. Patch-bumped storage-s3 to **1.0.4**
  and regenerated `tests/builtin-plugin-release-baseline.json` with the repo
  validator (digest `b843c0ea…`). Full repo gate passes; supersedes the earlier
  "1.0.3 baseline correctly refreshed" claim:
  `sessions/2026-08-14-builtin-s3-baseline-drift-fix.md`

- Web production runtime moved from Node to Bun: the `prod` Docker stage now
  uses `oven/bun:1.3.14-alpine` (no `apk add nodejs`), starts with
  `CMD ["bun", ".output/server/index.mjs"]`, and `nuxt.config.ts` sets
  `nitro.preset: 'bun'` because the `node-server` output cannot start under Bun
  (`srvx` exports `bun` condition resolves a tree-shaken adapter). Image build,
  non-root `sforum` runtime, HTTP 3000, entrypoint URL injection, and the full
  web test/typecheck gate all pass:
  `sessions/2026-08-14-web-production-bun-runtime.md`,
  `decisions/2026-08-14-web-production-bun-runtime.md`

- S3 EventStream DoS (GO-2026-5764) remediation: the protected
  `sforum.storage-s3` backend upgraded its AWS SDK v2 graph so
  `service/s3` is v1.97.3 and `eventstream` v1.7.8; plugin patch-bumped to
  1.0.3 with the release baseline refreshed. Local build/govulncheck/test
  gates pass; Security workflow confirmation pending push:
  `sessions/2026-08-14-s3-eventstream-dos-remediation.md`

- Mobile topic list G1 (white flat list) + mobile topbar search icon landed:
  rows show `author ← lastReply · lastActivity`, category pill, neutral reply
  badge, no mobile tags; search moved from an always-visible bar to a topbar
  icon with a slide-down panel, and the mobile navbar is a single 54px row:
  `sessions/2026-08-14-mobile-topic-list-g1.md`

- Public forum images at `/media/attachments/{publicId}` are anonymously
  readable again: the production attachment read guard now recognizes the
  display-variant route used by the Nuxt media alias while retaining the same
  resource visibility and `forum.guest.read` policy:
  `sessions/2026-08-14-public-attachment-variant-guard-fix.md`

- Mobile third-party OAuth callbacks now keep the secure browser binding cookie
  available across HTTPS redirect chains and always expose callback failures:
  auth routes use inline feedback, while arbitrary return pages use a root
  persistent alert. Focused API/Web tests, Web typecheck, Identity tests,
  architecture validation, and local Browser checks pass; real provider/mobile
  account-switch E2E still needs operator credentials:
  `sessions/2026-08-09-mobile-external-auth-callback-fix.md`

- Guarded online Core migrations now use target-image capability negotiation,
  explicit transactional compatibility declarations, bounded PostgreSQL lock
  and statement timeouts, backup-before-migrate ordering, and an exact
  post-check before blue/green traffic switches. River and undeclared changes
  remain maintenance-only; that fallback now recognizes and stops an existing
  blue/green topology instead of rejecting its managed ports:
  `sessions/2026-08-02-online-core-migrations.md`

- CI email-verification Page Registry fix: `/email-verification` now has a
  complete catalog, ViewModel, Host-island, default-theme template, and V3
  route/UI identity chain. The admin mutation guard remains Host-authoritative.
  A follow-up CI run exposed and repaired stale moderation ownership and V3
  route/UI count gates. The full Web suite/build, focused Go tests, theme
  validation/tests, catalogs, and architecture checks pass; the local full
  repository gate remains limited by unrelated stale shared-database state:
  `sessions/2026-08-02-ci-email-verification-page-registry.md`

- Manual password-recovery and email-verification sends now share configurable,
  server-authoritative cooldown/window/per-target/per-IP limits under Site
  Settings → Account security. Public countdowns consume success/429 retry
  metadata, password recovery remains non-enumerating, and SMTP test mail is
  excluded. Focused API/Web, OpenAPI, typecheck, and architecture checks pass;
  desktop/mobile Browser QA awaits an authenticated local admin session:
  `sessions/2026-08-02-configurable-mail-resend-limits.md`

- Browser notification subscriptions now hide revoked device history, require
  a matching live browser and Host subscription before showing enabled, and
  default untouched Core reply/mention/moderation Web Push policy to the
  enabled recommendation without overwriting operator choices. Same-user tabs
  now elect one SSE leader and broadcast revision signals, preventing the
  four-connection guard from returning 429 under ordinary multi-tab use. Full
  Web/API tests, typecheck, architecture validation, authenticated Chrome Web
  Push state, and a seven-tab single-upstream runtime check pass. The formal
  executable ZIP chain also runs in an isolated temporary database:
  `sessions/2026-08-02-browser-notification-subscription-fix.md`

- Mobile right-rail entry is reserved for authenticated users and uses their
  avatar; its drawer combines desktop user-menu actions with page-owned
  information. Guests render no synthetic avatar or right-rail trigger and get
  one login/registration action instead (login only when registration is
  closed). Full Web tests, typecheck, architecture validation, and guest mobile
  Browser navigation QA pass; the authenticated collapsed-section click loop
  remains pending after a prior DevTools pointer-control timeout:
  `sessions/2026-08-02-mobile-page-header-drawer-triggers.md`

- Email verification is implemented end to end: one-use hashed link tokens,
  localized outbox mail, registration-to-waiting-page navigation, confirm/resend
  APIs, OAuth assertion handling, authoritative gates for topics/comments/
  attachments, and audited admin verify/reset controls that invalidate old
  links. When enforcement is enabled, all authenticated unverified users are
  redirected from non-admin routes to the waiting page; admin routes remain
  accessible and remote resets take effect on the next public navigation. The
  waiting page never sends on entry: users explicitly send after completing
  the operator-configured ALTCHA challenge, and the API enforces that evidence.
  The waiting flow has no manual-code path. Full web/API tests, typecheck,
  OpenAPI validation, and authenticated Browser verification pass:
  `sessions/2026-08-01-email-verification.md`

- CI architecture gate repair: `SFNavbar.vue` dropped from 1132 to 948 lines by
  extracting focused mobile search and bottom-navigation components; the
  architecture gate, full 852-test Web suite, typecheck, V3 catalogs, complete
  repository gate, and desktop/mobile Browser regression all pass. Development
  rules now require an early architecture preflight for large production-file
  changes:
  `sessions/2026-08-01-ci-quality-gate-repair.md`

- Deleted comments now render a localized bordered notice instead of a blank
  body in the shared public comment component; focused presentation tests and
  Nuxt typecheck pass:
  `sessions/2026-08-01-deleted-comment-placeholder.md`

- OAuth Provider Core Host contracts are now in place: account-settings
  navigation, opaque identity delegation, and one-use consent bridge. The
  provider remains an external optional plugin; next step is Protocol V2
  exposure and independent plugin implementation:
  `sessions/2026-08-01-oauth-core-host-contracts.md`

- Mobile global style: the approved D soft-panel direction is implemented in
  the shared public navbar, home topic rows, and global mobile drawer baseline.
  The inline mobile topic author avatar is now 24px; its rendered follow-up is
  intentionally left for operator manual QA:
  `sessions/2026-08-01-mobile-global-style.md`

- CI legal SEO test fix: the failed GitHub Actions Quality gate all-web unit
  test was a stale legal route source guard expecting `useSeoMeta` after public
  SEO ownership moved to `useSForumSeo`; the test and development rules now
  enforce the centralized resolver, and full web unit tests pass:
  `sessions/2026-08-01-ci-legal-seo-test-fix.md`

- SEO title ownership is now centralized in `useSForumSeo`/`resolveSEO`; the
  root app only provides an empty-title fallback, preventing duplicate site
  names. Focused frontend tests, typecheck, and production build pass; the
  existing port-3000 runtime still needs a restart/redeploy:
  `sessions/2026-08-01-seo-title-ownership-fix.md`

- Moderation report creation and legacy status updates now return the committed
  enriched row instead of failing during response scanning; focused unit,
  architecture, and real PostgreSQL regression checks pass:
  `sessions/2026-08-01-moderation-report-submit-fix.md`

- Deployment console now asks for the admin route prefix and prints reverse
  proxy targets plus site/admin URLs after deploy or restart:
  `sessions/2026-08-01-deploy-addresses-and-admin-prefix.md`

- Runtime Site Config URL fix: deployment URLs now remain runtime values,
  homepage health probes use `/health`, and the next versioned Web image can be
  applied through the existing zero-downtime updater without host-side Caddy
  edits:
  `sessions/2026-08-01-runtime-site-url-fix.md`

- V3 stable release preparation: fresh deployments resolve the latest stable
  Release to an immutable tag, the bilingual operator guide includes clone-free
  installation and update commands, and `v3.0.1` is ready for the release gate:
  `sessions/2026-08-01-v3-release.md`

- System update discovery now refreshes at most every five minutes while the
  admin shell is open and presents a six-hour-cooled update modal:
  `sessions/2026-08-01-system-update-prompt.md`

- Alpha.13 production release: Linux `/proc` API/Worker/plugin metrics, Night
  Harbor removal, migration-guarded blue/green updates, curated release notes,
  and the Compose schema-command hotfix are published. Exact CI/Security and
  release gates pass; the alpha.10 validation install now runs alpha.13 on the
  green slot, with 2,400/0 continuous HTTP switch evidence:
  `sessions/2026-08-01-alpha13-release-validation.md`

- Release-blocker remediation: historical `enc::` secrets now migrate without
  loss, production Marketplace key inputs are explicit, SystemTier affects real
  start order, fake rollout acknowledgements and CompatFarm soft-passes are
  removed, database restore is fail-fast and atomically published, release
  creation requires anonymous image pulls, deploy waits for API/Web readiness,
  and the reviewed public UI regressions are repaired. Real rollout/consumer
  work and an immutable release-image smoke remain open; the clean-host deploy
  entrypoint is intentionally left for the planned rewrite:
  `sessions/2026-07-31-release-blockers-fixed.md`

- Runtime memory governance: API/Worker/plugin/total resource cards now use
  cached sampling and a 60-second median, embedded Worker accounting is
  explicit, plugin artifact hashing is streaming, and pprof is loopback-only
  opt-in. The CPU gauge now has a larger centered presentation; its focused SFC
  checks pass and manual rendered confirmation is pending. Earlier desktop and
  390px Browser QA passed for the resource workflow and sorted plugin details
  popover:
  `sessions/2026-07-31-runtime-memory-governance.md`

- PNG attachment compression: lossless display variants now use the strongest
  standard-library PNG encoder at the recommended strength and exact indexed
  color for images with at most 256 RGBA colors, including transparency. The
  Attachments package and related controller/job tests pass:
  `sessions/2026-07-31-png-compression-effectiveness.md`
- Editor image upload: the shared Tiptap editor now opens a click-or-drop
  upload modal from its toolbar, supports clipboard image uploads and
  exact-position drag uploads through the existing attachment policy, persists
  transactional attachment identity, and permits image-only topic/comment
  publication without fabricating plain text. Images now carry bounded
  dimensions and compact/standard/wide display modes, with per-surface
  PhotoSwipe viewing and an authorized original-media alias. API, focused
  frontend, and architecture tests pass; the advanced local static URL prefix
  remains optional and empty by default:
  `sessions/2026-07-31-editor-image-upload-modal-and-paste.md`
- Extension fixture audit: all 18 tracked fixture packages now satisfy the
  Manifest V3 / Protocol V2 baseline; three stale static manifests were fixed,
  a full static inventory gate was added, and the deleted V1 Host API reference
  was removed from current authoring guidance:
  `sessions/2026-07-31-extension-fixture-audit.md`
- Navbar notification preview: the public bell now opens all/reply/mention
  previews with at most the latest three rows per tab, a full-history hint,
  recipient-authorized excerpts, desktop popover and mobile bottom-sheet
  behavior. Typecheck, focused tests, architecture validation, and user
  interaction verification pass:
  `sessions/2026-07-31-navbar-notification-preview.md`
- Selection quote reply: eligible topic/comment text selections now show an
  absolute, content-anchored `引用并回复` action and feed a safe Markdown quote
  into the existing topic/comment composer and Notification V2 path. Focused
  tests, typecheck, and desktop Chrome interaction pass; the connected Chrome
  viewport override did not apply for the requested mobile-size replay:
  `sessions/2026-07-31-selection-quote-reply.md`
- Edit save validation feedback: topic and comment edit buttons now explain
  no-op saves and missing cross-author reasons while preserving native disable
  only for in-flight submissions. Focused tests, typecheck, and architecture
  validation pass; rendered topic-route Browser QA remains blocked by repeated
  in-app navigation timeouts:
  `sessions/2026-07-31-edit-save-validation-feedback.md`
- Mobile topic editor visibility: topic create/edit now align the editor body
  with their responsive canvas height and preserve page-specific bottom space
  after shared home padding, so the complete status row scrolls above the fixed
  action dock. Focused tests and Chrome QA at `402x905` plus `1280x720` pass:
  `sessions/2026-07-31-mobile-topic-editor-visibility.md`
- Admin user sorting: `/control-panel/users` now requests stable server-side
  ordering by joined/updated time, username, display name, email, or status,
  with selectable direction and page-1 reset. Automated gates pass; rendered
  desktop/mobile Browser QA remains pending after Chrome tab-claim timeouts:
  `sessions/2026-07-31-admin-user-sorting.md`
- Forum code highlighting: topic/comment rich content now uses the selected
  paper-line code block with language labels, sticky line numbers, copy Toasts,
  expanded grammars, and light/dark styling. `/t/59` now treats undeclared code
  as `纯文本 / TXT` and has no nested rounded border:
  `sessions/2026-07-31-forum-code-highlighting.md`
- Admin user-group tabs: `/control-panel/roles` now separates group management
  and extension permission reviews into query-synced fixed tabs, lazily loads
  review data, and preserves the exact approved-permission merge into dirty
  role drafts. Typecheck and focused source tests pass; rendered manual QA is
  pending:
  `sessions/2026-07-31-admin-role-tabs.md`
- Attachment upload policies: existing RBAC remains the upload-eligibility
  authority, while audited role/user policies resolve per-file limits under
  site and HTTP transport caps. The admin page exposes role and user controls,
  and oversize ordinary uploads return a specific 413 response:
  `sessions/2026-07-31-attachment-upload-policies.md`
- Flat comment separator rendering: removed the row bottom border that looked
  like an intermittent horizontal scrollbar while moving across comments;
  tree-mode branch separators and code-block scrolling remain unchanged:
  `sessions/2026-07-31-comment-separator-rendering.md`
- Responsive public sidebar: desktop and mobile now share
  `public.sidebar.primary`, one serializable drawer owner, and the same
  page-sidebar DOM; Navbar falls back to generic navigation only on pages
  without a desktop left rail, while V1 mobile data remains compatible:
  `sessions/2026-07-31-responsive-public-sidebar.md`
- Mobile comment actions now keep reply/permalink inline, move secondary
  actions into a floor-adjacent menu at narrow widths, and preserve the full
  desktop action strip; active-theme Browser QA passed at `402x905` and
  `1280x720`:
  `sessions/2026-07-31-mobile-comment-actions.md`
- Default-theme topic readability now uses a semantic 12/14/14/16px scale;
  body text, publication metadata, comments, composer guidance, and the right
  rail are larger and clearer. The mobile discussion heading now preserves the
  desktop horizontal geometry with a compact 44px row and 24px lead-in. Source
  tests and theme validation pass; the new immutable candidate awaits confirmed
  local reactivation and 402px Browser evidence:
  `sessions/2026-07-31-default-topic-readability.md`
- Release gate repair: `v3.0.0-alpha.3` stopped before publication because the
  main CI lacked a direct Vue SFC compiler test dependency and the release
  waiter misparsed GitHub's empty in-progress conclusion. Fresh Web installs
  now also run `nuxt prepare` before Bun resolves Nuxt aliases; the next
  immutable prerelease is `v3.0.0-alpha.4`:
  `sessions/2026-07-31-release-gate-repair.md`
- Release pipeline artifacts: the maintainer helper returns immediately by
  default; GitHub reuses the exact main CI, publishes four multi-platform
  images, six cross-platform CLI archives, two Linux backend bundles,
  checksums, and provenance after scan and smoke:
  `sessions/2026-07-30-release-pipeline-artifacts.md`
- Attachment image optimization: ordinary proxied JPEG/PNG uploads now produce
  durable `display` variants in the background, use them through the existing
  attachment URL with transparent original fallback, and expose policy,
  statistics, and explicit backfill controls in Attachment Configuration:
  `sessions/2026-07-30-attachment-image-compression.md`
- Personal appearance settings: logged-in users now have a dedicated
  `/settings/appearance` sidebar page with immediate unsaved preview,
  save-only persistence, and explicit restoration to live site inheritance;
  normal and system-error documents share the same user-first appearance
  resolution, including authenticated hard-refresh errors:
  `sessions/2026-07-30-personal-appearance-settings.md`
- Unlinked external login choice continuation: an unbound provider subject now
  enters `/auth/continue` and may either prove an existing local account then
  auto-bind, or use the existing registration flow then auto-bind. Both paths
  share the Host ticket/link authority, with browser binding, exact-artifact
  revalidation, and no email-based account matching:
  `sessions/2026-07-30-external-login-registration-continuation.md`
- Shared topic comment composer: quick reply remains inline; advanced reply,
  comment reply, and comment edit use one bottom drawer with pointer/keyboard
  height adjustment. Its toolbar/canvas/status layout now expands only the
  canvas and preserves the status row when compact heights require scrolling.
  The legacy advanced-reply page redirects into that drawer, and desktop/mobile
  Browser QA plus hydration checks pass:
  `sessions/2026-07-30-shared-comment-composer-drawer.md`
- Custom image sticker platform design: the Forum Canvas base is now live in
  `SFEditor` with quiet focus, responsive toolbar scrolling, and preserved
  write/preview plus trusted L2 contracts. Markdown source and native JSON
  inspection are no longer exposed, while their persistence payloads remain.
  The Unicode emoji picker is gone, while the real sticker command remains
  gated on the immutable catalog and `sforumSticker` node; size caps remain
  `128x128` desktop and `96x96` mobile:
  `sessions/2026-07-30-image-sticker-platform-design.md`
- Uploaded avatar media route repair: AvatarView now exposes stable
  `/media/avatars/{publicId}` URLs, the Nuxt Host proxies them to the existing
  attachment authority, and uploaded avatars bypass IPX so they render instead
  of falling back to initials:
  `sessions/2026-07-30-avatar-media-route-repair.md`
- API startup deadlock and theme replay repair: notification LISTEN lifecycle
  now closes before the shared PostgreSQL pool, so bootstrap errors remain
  visible; theme binding replay tolerates a deleted historical approver, and
  the live API is healthy and ready on port 8081:
  `sessions/2026-07-30-api-startup-theme-replay-repair.md`
- Login methods settings page: account login methods now live on independent
  `/settings/login-methods` with a dedicated sidebar entry, Page Registry
  surface, Host island, built-in theme templates, and activated default-theme
  runtime evidence:
  `sessions/2026-07-30-login-methods-settings-page.md`
- Local password settings page: local password setup/change now lives on
  independent `/settings/password` with a dedicated sidebar entry, Page Registry
  surface, Host island, built-in theme templates, and recent-auth-aware UI:
  `sessions/2026-07-30-local-password-settings-page.md`
- Personal access tokens settings: PAT management now lives on independent
  `/settings/tokens` with a dedicated sidebar entry, Page Registry surface,
  Host island, built-in theme templates, Create/Manage tabs, and preset +
  checkbox scope picker:
  `sessions/2026-07-30-personal-access-tokens-settings.md`
- Sidebar about link: the public left sidebar's bottom "About {siteName}"
  entry now has runtime `site.about_url` plus `site.about_open_in_new_tab`
  settings exposed from Site Settings; empty URL keeps the old inert text:
  `sessions/2026-07-30-sidebar-about-link.md`
- Forum cooldown recovery feedback: topic/comment cooldowns stay independently
  configured, while 429 responses now publish standard and structured recovery
  timing and all create surfaces show a server-authoritative countdown without
  locking drafts:
  `sessions/2026-07-30-forum-cooldown-recovery-feedback.md`
- SSR-stable chrome controls: anonymous SSR now renders guest actions
  immediately, while language, appearance, and authenticated-user ClientOnly
  surfaces keep visible, inert, geometry-stable fallbacks until hydration;
  focused tests, raw HTML checks, and desktop/mobile Browser QA pass:
  `sessions/2026-07-30-ssr-stable-chrome-controls.md`
- Shared public page headings: category, tag, home/search, notification,
  account-settings, and moderation list shells now use one Core header
  component with centralized page/section typography tokens; focused tests
  pass and operator visual verification remains:
  `sessions/2026-07-30-shared-public-page-headings.md`
- Daytime background admin follow: Core Personalization now offers 12 localized
  light palettes shared by public and admin surfaces; preset choices preview
  immediately in admin memory, persist only after save, and every background
  mapping remains outside `.dark`. Focused automation and operator interaction
  verification pass:
  `sessions/2026-07-30-daytime-background-admin-follow.md`
- Moderation theme presentation repair: complete and operator-verified; the
  plugin-closed workbench now renders through a constrained active-theme L1
  shell, Host fallback navbar geometry is scoped, and moderation/profile center
  columns use the correct foreground surface token:
  `sessions/2026-07-30-moderation-theme-presentation-repair.md`
- Built-in theme activation repair: the default theme binds all 27 Page
  Registry templates to exact Manifest V3 declarations; source and staged
  artifact validation pass. The former Nocturne/Night Harbor theme is no longer
  built in:
  `sessions/2026-07-30-builtin-theme-v3-template-declarations.md`
- Localized transactional mail: Core now produces paired HTML/text password
  reset, registration welcome, reply, mention, and moderation templates; mail
  language is snapshotted before queueing, and the new welcome-email Mail
  Settings control defaults off. Authenticated Browser QA is blocked by the
  current login-page `$setup.t is not a function` error:
  `sessions/2026-07-30-localized-transactional-mail.md`
- Profile settings runtime identity: `/settings/profile` now persists the
  private `users.locale` preference through `PUT /auth/locale` and displays its
  username path from normalized public `site.domain`; the domain defaults from
  the trusted `site.url` host, strips protocols/trailing slashes on save, and
  focused domain tests pass while operator UI verification remains:
  `sessions/2026-07-30-account-default-language.md`
- S3 storage provider Admin UX: multi-instance plugin roots can no longer be
  selected as writers, plugin configuration deep-links to the attachment
  instance editor, and new built-in storage providers require explicit enable:
  `sessions/2026-07-30-multi-instance-s3-storage-handoff.md`
- Attachment storage localization: storage-instance UI messages are now in the
  correct attachment namespace, and probe data messages follow request locale
  while retaining stable reasons and raw stored diagnostics; rendered Browser
  QA needs an authenticated administrator session:
  `sessions/2026-07-30-attachment-storage-i18n-fix.md`
- Registration settings state fix: a legacy disabled registration switch with
  no mode now renders as `closed` in Site Settings, while fresh defaults remain
  open; focused unit coverage passes and administrator-session Browser QA is
  still pending:
  `sessions/2026-07-30-registration-settings-state-fix.md`
- Extension black-box test split: all 28 public-contract and joined-runtime
  tests now live in a focused `IntegrationTests` package, the one necessary
  root subprocess helper is ratcheted, and ordinary plus race suites pass;
  the full gate remains blocked by concurrent out-of-scope
  `Models/Extensions` architecture growth:
  `sessions/2026-07-30-extension-black-box-tests-split.md`
- Authentication shared shell: login, registration, and password recovery now
  share runtime public branding and appearance tokens; both built-in themes
  mount the operator-owned footer on all four authentication templates.
  Password-recovery ALTCHA is now enabled by default once the operator enables
  and configures the provider. Manual desktop/mobile verification and
  active-artifact activation remain pending:
  `sessions/2026-07-30-password-recovery-frontend-handoff.md`
- Multi-instance S3-compatible attachment storage: Core now owns named
  instance identity, SecretStore references, exact historical routing, probe
  and one-click writer selection; protected `sforum.storage-s3` owns AWS
  S3/MinIO/R2 behavior, while FTP/SFTP protected built-ins were removed:
  `sessions/2026-07-30-multi-instance-s3-storage-handoff.md`,
  `decisions/2026-07-30-multi-instance-s3-storage.md`
- Comment user preview: comment avatars and author names now open the compact
  A profile card before canonical profile navigation; focused tests, build,
  and component-level desktop/mobile Browser QA pass, while real topic-theme
  QA remains pending API recovery:
  `sessions/2026-07-30-comment-user-preview-handoff.md`
- Plugin bootstrap startup recovery: restored the historical Bootstrap ABI v1
  cookie independently from Protocol V2, added cross-built compatibility and
  built-in version gates, kept the API alive in recovery-only mode on initial
  convergence failure, and added exact protected-artifact quarantine; manual
  operator verification remains:
  `sessions/2026-07-30-plugin-bootstrap-startup-recovery.md`
- Manifest V3 / Protocol V2 only: package installation and executable runtime
  now reject every older or missing contract; V1 runtime, SDK, fixtures,
  built-ins, rollback artifacts, and documentation were removed before public
  release:
  `sessions/2026-07-29-manifest-v3-protocol-v2-only.md`,
  `decisions/2026-07-29-manifest-v3-protocol-v2-only.md`
- Runtime site URL OAuth callbacks: external-auth display and execution now use
  the admin `site.url` first and inherit environment `APP_URL` only when empty;
  request Host remains untrusted, production stays HTTPS-only, and start-time
  callback transactions remain stable:
  `sessions/2026-07-29-runtime-site-url-oauth-callback.md`,
  `decisions/2026-07-29-runtime-site-url-oauth-callback.md`
- Lifecycle Identity stale recovery: exact failed-disable evidence can now
  repair the pre-fix `enabled + tombstone` state at startup, while new durable
  Identity changes commit atomically with the aggregate registry phase;
  disable, enable, restart, settings restart, and cold-start verification pass:
  `sessions/2026-07-29-lifecycle-identity-stale-recovery.md`
- CI quality gate repair: architecture growth was split into focused owners,
  stale fixtures and performance budgets now match current dependencies, and
  the PostgreSQL 17 Actions service is migrated on the required-test port while
  compatibility credentials remain isolated from opt-in suites:
  `sessions/2026-07-29-ci-quality-gate-fix.md`
- Lifecycle V2 settings restart: enabled plugins such as
  `sforum.auth-github` now preflight before persistence and restart the exact
  active artifact through Host lifecycle orchestration; stable recovery errors
  replace the prior generic 500, while operator verification remains:
  `sessions/2026-07-29-lifecycle-v2-settings-restart-fix.md`
- Stale extension permission suggestions: approval remains exact-artifact
  fail-closed, while authorized rejection can now close pending records after
  disable, uninstall, missing-artifact cleanup, or replacement:
  `sessions/2026-07-29-stale-extension-role-suggestion-rejection.md`
- Default site brand assets: the supplied V3 mark now ships at
  `/brand/sforum-logo.svg`; empty runtime Logo/favicon options resolve to it
  without storing the fallback, while operator URLs remain authoritative:
  `sessions/2026-07-29-default-site-brand-assets.md`
- Admin attachment navigation: Attachment Configuration and Attachment
  Management are independent permission-aware routes; Attachment Configuration
  contains Basic Configuration and Compression Configuration tabs; Basic
  Configuration now includes persistent field guidance and units, while the old
  URL redirects compatibly and the governance extension placement follows
  Attachment Management; the manager now provides server-backed button
  pagination with filter reset and stale-page recovery; operator verification
  remains:
  `sessions/2026-07-29-admin-attachment-submenus-handoff.md`
- Personalization brand assets: compact click/drag uploads now auto-fill URL
  plus attachment ID and show previews; brand SVG is safely rasterized to PNG
  without weakening the ordinary attachment denylist:
  `sessions/2026-07-29-personalization-brand-asset-upload.md`,
  `decisions/2026-07-29-brand-svg-rasterization.md`
- Category directory layout: removed the duplicate desktop top offset, moved
  group focus into the main toolbar, and reduced both side rails; immutable
  theme refresh and desktop/mobile manual verification remain:
  `sessions/2026-07-29-category-directory-layout-handoff.md`
- Forum content relative time: topic/comment publish and accepted-edit times
  now use a one-month relative window with site-timezone `Y-m-d H:i:s`
  fallback; automated and manual verification remain for the user:
  `sessions/2026-07-29-forum-content-relative-time-handoff.md`
- Canonical public search route: `/search?q=...` now owns a complete
  `forum.search` Page Registry/theme surface; old homepage search URLs redirect,
  and operator runtime verification remains:
  `sessions/2026-07-29-public-search-route-handoff.md`
- Announcement authoring: labeled bilingual create form, field-oriented
  SForum editor preset, time-window controls, local validation, and
  server-sanitized Markdown presentation; operator Browser QA remains:
  `sessions/2026-07-29-announcement-editor-handoff.md`
- Missing-artifact uninstall routing: per-row cleanup now targets only the
  selected missing extension, ordinary lifecycle DELETE fails closed, and
  immutable publication history no longer causes refresh-time reappearance:
  `sessions/2026-07-29-extension-missing-artifact-uninstall-routing.md`
- GitHub Actions and security remediation: reusable CI/security checks, four
  multi-platform GHCR images, version-pinned deployment, gRPC remediation, and
  Fiber `v3.4.0` across all 18 source Go modules with session failure cleanup;
  the Buf tool graph now uses patched CEL, text, and compression dependencies;
  redirect targets, database pool integers, and stored Argon2 verification now
  reject unsafe input before crossing their runtime boundaries; the unpatched
  imaging dependency is gone and JPEG/PNG transforms now explicitly reject
  TIFF; uploaded extension ZIP paths now have a CodeQL-visible strict source
  guard plus independent snapshot containment checks; external-auth runtime
  evidence now logs only credential-neutral primitive projections; release
  tags now inject one shared Core/Web/API/worker/migrator/CLI build identity,
  shown once beside the SForum admin brand, while the protected overview shows
  build and runtime diagnostics; the bilingual `scripts/release.sh` validates
  and pushes the annotated release tag without building locally; repository
  licensing is now explicitly MIT under Inkedus while separately licensed
  third-party material retains its own terms:
  `sessions/2026-07-29-github-actions-release-pipeline-handoff.md`,
  `decisions/2026-07-29-mit-project-license.md`
- Notification email opt-in defaults: admin per-type email control, Core
  transaction-scoped user preference enforcement, hard site gate, safe default
  migration, personal managed state, and desktop/mobile Browser QA:
  `sessions/2026-07-28-notification-email-opt-in-handoff.md`
- Notification stream lifecycle hotfix: downstream disconnect now destroys the
  Nuxt upstream SSE socket, API subscriptions have one-minute leases, and the
  client uses bounded backoff plus HMR cleanup instead of native retry storms:
  `sessions/2026-07-28-notification-stream-lifecycle-hotfix-handoff.md`
- Notification detail type navigation: list/detail now share the same type
  rail, loaded-scope counts, current-type highlight, and filter-state return;
  focused verification passed while rendered Browser QA remains to repeat
  after the Chrome control timeout:
  `sessions/2026-07-28-notification-detail-type-nav-handoff.md`
- Notification list source identity: reply/mention rows now use the actor's
  configured avatar, system rows use Tabler icons, hidden targets scrub actor
  summaries, and desktop/mobile Browser QA is complete:
  `sessions/2026-07-28-notification-source-avatar-handoff.md`
- Topic detail stale-comment repair: `/t/**` whole-page SWR removed, explicit
  anonymous/session cache-control policies, API generation caches retained,
  and direct anchor/runtime proof with all current comments:
  `sessions/2026-07-28-topic-page-cache-correctness-handoff.md`
- Unified Admin Mail and Notification settings: one permission-aware fixed-tab
  surface, compact shared settings geometry, legacy notification-route redirect,
  and desktop/mobile Browser QA:
  `sessions/2026-07-28-admin-mail-notification-settings-parity-handoff.md`
- Notification detail preview and realtime badge recovery: independent
  authenticated list/detail routes, recipient-authoritative preview, selected
  theme proof, single-owner public chrome, immediate auth subscription, and
  SSE error/reconnect/visible-page REST reconciliation:
  `sessions/2026-07-28-notification-detail-preview-handoff.md`
- Architecture boundary debt M0-M12 completed: fixed tabs, domain placement,
  backend splits, focused collaborators, stable extension contracts, import
  ratchets, ADR, and resumed final gate evidence:
  `sessions/2026-07-28-architecture-debt-m12-handoff.md`
- Architecture boundary debt M10: runtime `Manager` is now a 72-method
  compatibility facade over four focused collaborators with single owners for
  admission and event/provider state; M11 stable packages are next:
  `sessions/2026-07-28-architecture-debt-m10-handoff.md`
- GitHub built-in restart repair: dedicated Host restart orchestration,
  correct built-in trust preview semantics, exact staged-target recovery,
  legacy-to-Lifecycle-V2 bridge, and successful browser/database evidence
  without 409 or exact-fence conflict:
  `sessions/2026-07-28-github-plugin-restart-handoff.md`
- Architecture boundary guardrails: future feature work is now constrained by
  domain placement, fixed Core Tab components, Go package responsibility, and
  baseline-enforced non-growth for large files/flat packages/God objects:
  `sessions/2026-07-28-architecture-boundary-guardrails-handoff.md`
- Runtime public URL override: admin `site.url` may be cleared to inherit
  environment `APP_URL`; public consumers receive the resolved value while
  OAuth/CSRF/cookie security stays environment-owned:
  `sessions/2026-07-27-app-url-runtime-fallback-handoff.md`
- Extension artifact-presence reconciliation: DB-retained plugin/theme records
  expose `artifactState`; missing packages fail closed and super admins can
  explicitly batch-uninstall eligible records through an atomic, data-aware
  confirmation flow:
  `sessions/2026-07-27-extension-artifact-presence-handoff.md`
- Search automatic reconciliation: provider-neutral Host ledger, startup + 15m
  bounded repair schedule, and real-runtime cleanup of 92 historical ghosts:
  `sessions/2026-07-27-search-auto-reconciliation-handoff.md`
- Configurable public navigation M0-M6: revisioned four-location editor,
  recovery/backup, canonical topbar/sidebar/mobile/footer SSR authority,
  dynamic taxonomy, exact theme capabilities, and selected-theme
  desktop/mobile Browser QA are complete; M7 final gate is next:
  `sessions/2026-07-28-public-navigation-platform-handoff.md`
- Notification Platform V2 completed M0-M7 with exact-artifact Web Push,
  real PostgreSQL multi-node/fixture evidence, hidden-target scrubbing, and a
  green full repository gate; use this handoff only for independent review:
  `sessions/2026-07-27-notification-platform-plan-handoff.md`
- Built-in GitHub social login: R1-R7 remediation packet completed with
  isolated HTTP/Browser artifacts; independent review remains required:
  `sessions/2026-07-27-external-auth-review-remediation-handoff.md`
- Topic create/edit selected-theme and Core-fallback shell parity, exact
  artifact activation, CSS validator boundary fix, and editor Host Island
  allowlist:
  `sessions/2026-07-27-topic-composer-shell-parity-handoff.md`
- Topic side card **贡献者**: author+editors avatar group (max 5), public
  contribution timeline modal, `TopicDetail.contributors` +
  `GET /topics/{id}/contribution-timeline` (no body/reason; staff exposed):
  `sessions/2026-07-27-topic-contributors-handoff.md`
- Editor-document edit load fix: topic/comment editors restore native Tiptap
  JSON via `forumEditorInitialContent` → `SFEditor.initialContent`, never seed
  Markdown v-model with `rawContent`; save uses `forumContentFromEditorPayload`:
  `sessions/2026-07-27-editor-document-edit-load-fix.md`
- Comment stream visual refresh: deep-link highlight now light-sweep animation;
  header meta regrouped (time · floor right-aligned, OP badge, one-line reply
  quote): `sessions/2026-07-26-comment-stream-visual-refresh-handoff.md`
- `/settings/profile` + `/settings/security` three-column chrome extracted into
  shared `SFSettingsShell` (slots: main / rail / head-actions; supersedes the
  07-24 "no SettingsShell yet" decision):
  `sessions/2026-07-26-settings-shell-refactor-handoff.md`
- Profile Replies tab deep links now include `/page/N#comment-{id}` via API
  `commentPage` (SSR cannot see URL hash alone):
  `sessions/2026-07-24-profile-reply-comment-page-links.md`
- Comment cross-page anchor SSR resolve (topic page) + path-style `/page/N`:
  `sessions/2026-07-24-comment-cross-page-anchor-handoff.md`
- `/moderation` host chrome sidebar tokens aligned with default-theme hybrid:
  `sessions/2026-07-24-moderation-sidebar-token-parity-handoff.md`

## Recently Completed

- Tri-state color mode reliability: shared Automatic/Light/Dark authority and
  public/admin menus, resolved-only extension bridge, cache-neutral SSR, and
  safe local canonical origin completed. See
  `reports/2026-07-27-tristate-color-mode-reliability-final.md`.
- Settings chrome: shared `sforum-settings` CSS shell, `SFSettingsAccountNav`,
  profile preview rail, security summary rail; fullwidth theme templates;
  chrome now componentized as `SFSettingsShell` (2026-07-26).
- `/moderation` workbench sidebars: reuse `SFHomeNavigation`, notifications rail
  section language, shared mobile drawer keys; queue overview large count;
  decision rail restyle; typecheck + workbench unit tests pass.
- Default-theme `/categories` grouped directory: implemented against confirmed
  Demo 01 with real category-group API data, SSR internal API reads, focused
  tests, typecheck, production build, and browser screenshot QA.
- Default-theme public profile B1: `/u/{username}` now uses the shared
  three-column shell with real public activity grouped by date, real public
  stats/recent topics, self-only edit UI, mobile drawers, i18n copy, OpenAPI
  updates, and focused Go/Bun/browser validation.
- Default-theme notifications and topic composer now use the shared responsive
  three-column shell with real API flows, mobile drawers, persistent errors,
  appearance-aware feedback, and focused Browser QA.
- Theme-defined system error pages: completed 2026-07-23. The selected public
  theme can own L0/L1 presentation for 403, 404, 429, and 500/502/503/504
  virtual surfaces while Host preserves status, safe copy/actions, no-store,
  noindex/nofollow, and Core emergency fallback. See
  `plans/2026-07-22-theme-defined-system-error-pages.md`.
- Theme-consistent public resource 404: M0-M6 completed 2026-07-23. Ordinary
  semantic 404s retain the healthy selected theme and real private HTTP 404;
  Core is emergency-only. See
  `plans/2026-07-22-theme-consistent-public-resource-404.md`.

## Other Open Product Tracks

- Iteration A engagement: `plans/2026-07-12-iteration-a-engagement-loop.md`
- Admin settings blueprint: `plans/2026-07-12-admin-settings-richness.md`
- Extension surface density: `plans/2026-07-12-extension-surface-density.md`
- Strategic direction: `plans/2026-07-12-development-directions.md`

## Navigation

| Path | Role |
| --- | --- |
| `modules/` | Living feature state; prefer over dated prose |
| `sessions/` | One concise handoff per active workstream |
| `sessions/archive/YYYY-MM/` | Cold historical checkpoints; do not load by default |
| `plans/` | Active, ready, blocked, and blueprint task books |
| `plans/archive/YYYY-MM/` | Completed, cancelled, and superseded task books |
| `decisions/` | Architecture/product ADRs; keep and mark replacements in-file |
| `reports/` | Point-in-time performance and security evidence |
| `glossary.md` | Shared framework terms |

Historical references that require verification before use:

- `knowledge/archive/architecture-maturity-audit.md` -- 2026-07-12 pre-V3 scorecard
- `knowledge/archive/legacy-sforum-feature-gap.md` -- partially stale old-product comparison
- `knowledge/archive/research.md` -- early stack/library research; accepted ADRs supersede it

## Session Rules

1. Read root `AGENTS.md`, this index, one relevant module note, and only the
   active plan/handoff required for the task.
2. Do not read `sessions/archive/` or `plans/archive/` unless recovering
   historical evidence.
3. Update the living module note instead of appending completed work here.
4. Keep one hot handoff per active workstream and archive it when the track
   closes or a newer handoff supersedes it.
5. For V3 percentages and LTS residuals, trust the active progress ledger, not
   archived session prose.

## Open Questions

- **版本发布状态冲突（阻塞）**：路线图要求在生产重接线 M3/M5/M6/M7 关闭前
  发行保持 prerelease；2026-08-01 会话记录 "v3.0.1 已准备进入 release
  gate"。当前实际 GitHub Release 状态需人工核实；若已发布 stable，则与
  路线图诚实性承诺矛盾，需产品决定（见
  `sessions/2026-08-16-docs-version-governance-remediation.md`）。
- Production backup destination and retention policy for operator deployments.
- Whether `en-US` copy must be complete for the first public release.
- Category-scoped ACL timing relative to global RBAC.
- Timing of the next architecture maturity re-audit after V3 LTS cleanup.
