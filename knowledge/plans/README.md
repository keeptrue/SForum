# Plans

Only draft, active, ready, blocked, and long-lived blueprint task books belong
in this directory. Completed, cancelled, and superseded plans live under
`archive/YYYY-MM/` and are not default session context.

## Status Vocabulary

| Status | Meaning |
| --- | --- |
| `draft` | Written for design review; **do not implement** until it becomes `ready` |
| `active` | Work is in progress; read before implementing |
| `ready` | Approved checklist; work may start |
| `blocked` | Do not continue until its named dependency clears |
| `blueprint` | Long-lived product guidance, not a single sprint |
| `completed` | Done; move to archive |
| `cancelled` | Explicitly stopped; move to archive |
| `superseded` | Replaced by another source; move to archive |

## Current Plans

| Plan | Status |
| --- | --- |
| `2026-10-26-uninstall-identity-purge.md` | **draft（分析完成，实施未批准）** -- 卸载 `identity purge` 与 append-only 发布账本的矛盾：唯一的生产 `DELETE FROM extensions`（`bootstrap/extension_lifecycle_cleanup_postgres.go:182-191`）→ 级联删 `extension_versions` → 被 `plugin_runtime_publication_members` 的 `ON DELETE RESTRICT` 挡住，任何曾 enable/upgrade 过的插件都不可达。判定：**修正 uninstall 终态定义，不推翻 2026-10-17**。澄清「30/34 能 finalized」= cleanup record id 30/34（不是比例），其扩展在 append-only members 表里历史 0 行 ⇒ 从未进过账本，是负证据。推荐 **A**（identity 保留为账本锚点 + `status='uninstalled'` + 清 active/staged + tombstone 增 `identity_disposition`/`superseded`）；B（归档发布历史）要拆四张表的不可变证据链，不推荐；C 会把重装退化成 staged 并留幽灵行。703 走 `superseded`；1154 修好 actor 链后仍撞同一面墙 |
| `2026-10-26-op926-job-publication.md` | **draft（分析完成，实施未批准）** -- op 926 的 job publication 停在 `target/pending`：`target 为空` = 设计（target 版本 20827 的 `manifest->'jobs'` 是 null，空期望集合法；本库 623 行历史快照全空，非空路径只有测试证据）；`按 operation_id 隔离` = 表结构（无 `extension_id` 列，唯一键三元组，部分索引 `_reconcile_idx` 已建但无查询使用）；真正缺陷在读取侧——`marker=true` 且未对账的行没有启动期重放（state 有修复、registries 有恢复、jobs 没有），`pending` 把「待重放」与「已作废」混成一种。本机 40 行未对账全部 `marker=false`（4 条 target / 36 条 source），River 无 `extension.plugin_job` 行 → 当前零用户可见后果。推荐 **B**（并入 `LifecycleRecovery` 启动快照，S1 只读盘点先行），A/C 对比、顺序陷阱与最小步骤见文 |
| `2026-10-26-store-package-delivery-risk.md` | **draft（决策建议，待拍板）** -- 商城 6.77MB 包贴 90s 下载硬超时：core `DefaultTimeout` 被生产装配写死成字面量（`api_assembly_domains.go:454`，不读 config），商城侧另有 `AbortSignal.timeout(90_000)`，实测 6.7–34 KB/s → 90s 内临界 77–79 KB/s，失败是必然而非边缘；超时被包成 `extension.remote_fetch_failed`，前端文案「对方没有返回可用的压缩包」与事实不符。推荐 A（三段超时收敛 + 可配，默认 180s，须与商城侧同批）→ C（官方镜像源）→ B（断点续传，长期正解但跨两仓） |
| `2026-10-24-app-store-core-install-primitive.md` | **landed（2026-10-24）** -- P0（`381182cc7`）删除 Marketplace 死代码 + `MARKETPLACE_ED25519_*` + `/extensions/store*` + 占位货架，store UI 身份转 retired 墓碑；P1 新增管理页 host action `installExtension`（清单 `hostActions` + 宿主确认框 + 期望 ID/版本/类型校验 + 120s 超时 + 页面级单飞 + 详情页/启用弹窗披露）。无 capability/表/worker；只暂存。残留：真实公网包 staged 只做到路由/服务层证明。见 `sessions/2026-10-24-app-store-core-handoff.md` |
| `2026-10-24-app-store-service.md` | **draft（讨论中，未批准实施）** -- 新仓 `sforum-app-store`：Next.js + Bun + PostgreSQL 服务端（SForum OAuth2 登录、GitHub App、自动检测 release 平台/摘要、签名短链代理、公开前台/开发者门户/审核）与 SForum 插件 `sforum.store`（目录/详情/安装交接）。消费 core 的 `installExtension` 契约 |
| `2026-10-26-extension-platform-gate.md` | **landed（2026-10-26）** -- 插件/主题架构兼容：Core 新增 `extensionpackage.DetectTarget`（fat Mach-O → buildinfo → ELF → Mach-O → PE，未知即放行），安装 422 / 启用·激活·升级·回滚·重启 409 `extension.platform_incompatible`（`fields=required/host/entry`），安装阶段在落盘前拒绝；商城新增 `latestPlatforms` 与 `platform=strict\|all`（`all` 只放宽可见性、版本轴仍生效），管理页加「只看支持当前架构」开关 + 卡片徽标 + 弹窗默认选最新可运行版本 + 平台下拉只留宿主/any。**残留**：① 运行阶段 409 只有 Go 单测，未做真机（替换已安装二进制会先撞摘要校验）；② 旧服务端无 `latestPlatforms` 的回落只做了响应注入验证，fixture 组合未真机；③ Core 前端插值文案未真机验证——用户 dev server 的 i18n 产物早于 locale 编辑 6.5 小时，未重启其进程；④ 全仓 `./scripts/test.sh --fast` 两条红属**其它未提交批次**：`validate-admin-framework.ts:371` 与 admin 侧栏新契约冲突、15 个 DB 集成测试硬编码 `>=1.0.0 <2.0.0` 而 dev core 已从 git 推导为 3.0.16。见 `sessions/2026-10-26-extension-platform-gate-handoff.md`、`decisions/2026-10-26-extension-executable-platform-gate.md` |
| `2026-10-23-ai-reply-silent-success-remediation.md` | **active（M1–M4 已落地，M5 剩浏览器 QA / eval 基线 / 保留策略）** -- 「succeeded + 0 ms + 0/0 token 却没有回复」：空补全改为失败且永不入缓存、缓存命中行加徽标与解释、回复结果独立成账（`ai_reply_outcomes` + `GET /admin/ai/reply-outcomes`）、生成器按触发幂等、新增 `sforum ai:reply` 排查命令。见 `sessions/2026-10-23-ai-reply-silent-success-handoff.md` |
| `2026-10-23-test-suite-speedup.md` | **active（本地全绿，CI 待 PR）** -- 全量测试提速：Go 并行从「包级 `-p`」改成「集群级分片」（`SFORUM_TEST_SHARDS` 默认 min(nproc,4)，片内 `-p 1`）——`-p>1` 在单个 database 上实测四类跨包竞态；两次隔离运行合并成 `--with-compat`；test.sh 三线并行（日志 `tests/.logs/`）；新增 `--fast` 本地分层；typecheck 增量缓存。实测 21:31 → 7:24（冷）/ 6:14（热）/ 0:41（`--fast`），154 个包两轮冷缓存真跑零失败。见 `sessions/2026-10-23-test-suite-speedup-handoff.md` |
| `2026-10-20-rich-content-fences.md` | **landed（有 residual）** -- 富内容围栏（mermaid / chart / 公式）：不扩短代码身份，改用代码围栏 + `$…$` 承载；客户端渐进增强接在 `v-highlight`，KaTeX 走 MathML，图表走窄 schema + Chart.js；AI 提示词增加宿主拥有的富内容段（`forum-reply@10`）。浏览器实测出图/回落/颜色模式重渲染/390 无溢出，并修掉 mermaid `click` 的 `<a>` 绕过站外中转。未做：eval baseline、工具栏插入菜单 |
| `2026-10-19-ai-tool-call-visibility.md` | **completed（待归档）** -- 两半：W1 让工具进入提示词（宿主渲染「本次可用工具」段 + `forum-reply@9`，修「模型拿到 web-search 却一次没调」）；W2 把 `ToolTrace` 落进新表 `ai_tool_calls` 并公开读（增量挂在 `GET /comments/:id/citations` 的 `toolCalls` 字段，不新增路由）。证据：run `8681e832…` 只有 1 行 `ai_executions` = 零工具调用；轨迹此前只进 slog |
| `2026-10-19-editor-tables.md` | **landed** -- 修复"编辑含 GFM 表格的帖子即整表丢失"；四层打通（编辑器节点/UI、Host Schema+规范化、Host 渲染+Markdown 投影、goldmark 短代码兼容路径）；v1 不做合并单元格与列宽持久化（见 `decisions/2026-10-19-editor-tables.md`） |
| `archive/2026-10/2026-10-19-ai-observability-and-capabilities.md` | **completed** -- A 批（ToolTrace / run correlation id / 轨迹回放）+ G（`sforum ai:eval` 基线）+ B 批（预算、3 个新只读工具、2 条派生用途、证据链溯源、会话状态）十项全部落地；六轨道并行 + 写作用域矩阵 + 进度账本 + 24 条偏离登记（D1–D24，无重号）；验证矩阵 L1–L5 通过，L6/L7 有运行时/浏览器证据；同日把仓库门禁从 HEAD 多处红推到 `./scripts/test.sh` 全绿（架构闸门 9→0） |
| `2026-10-18-core-attachment-extension-api.md` | **landed** -- 扩展认领 Core 附件：`sforum.attachments.extension.claim/release/list@1` 三条 Host 命令 + 公开读谓词拆分 + 卸载自动释放 + `attachment.extension_reference_limit` 已落地（实现放在 `Models/Attachments/ExtensionReference`，含 advisory 锁与按引用行重算计数）；插件侧接线在 `sforum-oauth2-provider` 仓库 |
| `2026-10-11-ai-plugin-host-api-m1.md` | **landed** -- 插件 Host AI 命令（`sforum.ai.complete` + `ai.invoke`）、事件发布命令（`sforum.events.emit`）与 `ai.tool` 贡献路径（`aiTools[]`、保留 slot、装配登记、SDK 路由）已落地并端到端验证；M4 `ai.purpose` registry 也已落地（见 `sessions/2026-10-12-ai-purpose-registry-m4-handoff.md`）；剩余 M3/M5、按用途覆盖的控制台 UI 与缓存开关 |
| `2026-10-11-ai-control-panel-remediation.md` | **completed** -- AI 控制台修复 M0-M6 完成（fail-closed/重置语义/策略 UI/nil 语义/诊断对齐/观测打磨）；仅渲染 QA 待管理员凭据 |
| `2026-08-22-builtin-shortcodes.md` | **active / blocked** -- M10B parser conformance and focused gates pass; exact immutable active-artifact promotion and final lifecycle/browser leakage gate remain |
| `2026-10-16-plugin-frontend-experience.md` | **active** -- M0 contract frozen, M1 host icon service, M2 region single source, M3 region-density batches, M4 cancelled (descriptor points are not content surfaces), M5 `layout: site` delivered by the authoring-speed plan, M7 `view: schema` done, M9 closed (`--schema-page`, public page runtime surface catalog, bilingual docs; `--public-page` and `extension dev` came from the authoring-speed plan), and **M6 `view: component` closed (2026-10-16)**: declaration, shared CLI/runtime preflight, resolve pass-through, reviewed body exit, catalog identity, the descriptor endpoint that used to 404 now 200, component mount in a real browser session, and — after fixing a settle defect in `tests/browser-public-390x844.mjs` that had measured before hydration — a **measured 390x844 mount** (`l2=mounted`, own content rendered, exactly one header/footer, no overflow, no console errors). Only M8 remains and it delegates public navigation to the navigation platform plan M7 |
| `2026-10-03-ai-read-only-chat-tools.md` | **active** -- read-only chat tools for the reply bot landed (contract, adapters, loop, five built-in tools, admin settings); rendered QA and plugin-facing tool surface remain |
| `2026-10-02-ai-assist-platform.md` | **ready** -- Host AI gateway contracts and the plugin/provider split are approved; M0 kernel work may start |
| `2026-10-12-ai-forward-ideas.md` | **blueprint** -- AI 前瞻想法池：两批共 35 条（首批 6 + T1-T4 15 + 第二批 14）；embedding/多模态相关项标注需重开 2026-10-02 排除决定 |
| `2026-07-30-image-sticker-platform.md` | **active** -- Forum Canvas base editor verified; sticker M0 contract and remaining picker decisions are next |
| `2026-07-13-trusted-plugin-theme-platform-v3.md` | **active** -- Protocol migration complete; theme-loader LTS residual and production-rewire honesty remain |
| `2026-07-13-trusted-plugin-theme-platform-v3-progress.md` | **active** -- durable residual ledger; do not claim 100% |
| `2026-07-22-v3-production-rewire-honesty-remediation.md` | **active** -- P0s, SystemTier, local gates repaired; real rollout/consumers and remaining honesty residuals open |
| `2026-07-22-current-head-regression-remediation.md` | **completed** -- M0-M7 closed; archive after the focused 404 handoff consumes it |
| `2026-07-22-theme-consistent-public-resource-404.md` | **completed** -- M0-M6 selected-theme public 404 closed; retained temporarily as the broader error-page precursor |
| `2026-07-22-theme-defined-system-error-pages.md` | **completed** -- 403/404/429/5xx virtual system pages use selected-theme L0/L1 with Host-owned truth/fallback |
| `2026-07-27-github-social-login-builtin-plugin.md` | **active** -- R1-R7 remediation complete; independent re-review requested, do not declare closure |
| `2026-07-27-external-auth-core-plugin-review-remediation.md` | **active** -- R1-R7 remediation complete; independent re-review requested |
| `2026-07-27-configurable-public-navigation-platform.md` | **active** -- M0-M6 complete; M7 lifecycle, recovery, documentation, and final release gate is next |
| `2026-07-12-iteration-a-engagement-loop.md` | **ready** -- remaining engagement product work |
| `2026-07-12-admin-settings-richness.md` | **blueprint** -- later settings waves |
| `2026-07-12-development-directions.md` | **blueprint** -- strategy context; verify against newer V3 decisions |
| `2026-07-12-extension-surface-density.md` | **active (partial)** -- remaining product surface density |

Generated V3 traceability is documentation, not a task book:
`../../docs/extensions/v3/catalogs/traceability.md`.

## Archived Plans

| `archive/2026-10/2026-10-18-route-stream-login-redirect.md` | **completed (2026-10-18)** -- 浏览器（`GET`/`HEAD` + `Accept: text/html`）访问受 `core.guard.login` 保护的 stream 路由时，宿主改回 `302 /login?redirect=`（目标只由 `c.Path()` + 原始查询串拼成，非法目标退化为 `/login`，响应带 `Cache-Control: no-store`），其余情形（API 客户端、SSE、非 stream、非安全方法）保持 401 JSON。改动仅 `apps/api/app/Http/route_dispatcher.go` + 新增 `route_dispatcher_stream_login_redirect_test.go`（9 个用例全绿）；dev 实机 before=401 JSON / after=302 已取证（注意 dev API 在 8080，3000 是 Nuxt），并补做了已登录会话复核：同一 URL 带会话返回 200 HTML 授权确认页、无会话才 302，确认→回跳带 code 链路不受影响 |
| `archive/2026-10/2026-10-17-extension-version-gc.md` | **completed (2026-10-17)** -- 扩展版本垃圾回收全部交付（含原定拆分的阶段 5 管理端入口，同日补齐）：阶段 0 腾位（flat 96→92，基线 94→92）、store 判据/执行（先盘后账、锁内复核、锚点+审计）、决策记录修正方案 A（发布账本 append-only，被引用版本永久钉住）、CLI `sforum extension gc`（dry-run 默认 + 一次性 confirm-token）、管理端三入口 + `SFAdminVersionGcDialog.vue` 三阶段对话框 + OpenAPI + 四张审核矩阵。隔离库测试绿、dev 实机 dry-run 225 行取证、桌面+390x844 渲染 QA 通过。见 `sessions/2026-10-17-extension-version-gc-handoff.md` |
| `2026-08-22-extension-authoring-speed-and-controlled-power.md` | **active, closed except row 7 (2026-10-17, matrix Revision 4)** -- M0-M8 complete; **M9 complete** (the last item, the session-state workflow reference package, landed as `extensions/fixtures/plugins/sforum-session-checkout-reference/`; reference gate 10 → 11 green); **M6 complete** (S1-S9 landed; this round closed the last production gap: the Gateway now implements the S9 issuer `ProtocolStarter` type-asserts — before the fix every scoped Host-initiated invocation failed at mint); **M10 rows 1-6, 8-10 evidenced, row 5 closed** with per-clause evidence (compiled-fixture-binary joined test, real-PostgreSQL Identity erasure matrix, live `privacy:erase-user` erase of a planted plugin-session key; the anonymous clause closed by decision **D1** as unavailable-by-construction, not satisfied), **row 9 fully closed** (real `v3.0.9` ghcr tag with `pull_policy: always`; production `deploy/caddy/Caddyfile` reverse-proxy/TLS layer verified: 308 redirect, H4 headers, health through the proxy, WebSocket Upgrade reaching the Go API); **row 7 (true multi-node) recorded as operator-owned environment work — the only open item**. Decisions D1/D2 recorded in Revision 4 of `decisions/2026-10-16-extension-authoring-contracts-freeze.md`; matrix at `sessions/2026-10-16-m10-joined-release-gate-matrix.md`; handoff at `sessions/2026-10-17-extension-authoring-closeout-handoff.md` |

Completed and superseded July task books are indexed at
`archive/2026-07/README.md`. Do not read them unless recovering historical
evidence.

Update this table when a current plan changes status. Move closed plans in the
same change instead of leaving them in the default directory.
