# Knowledge Index

Project memory entry point for humans and AI sessions. Read this file, the one
hot handoff for the current workstream, and the relevant module note. Do not
load archived sessions or completed plans as current context.

## Active Workstreams

### 真实客户端 IP（web 层改信任入口头）

- Status: **landed（2026-10-28）**。2026-10-27 那版「web 层解析唯一 IP」在真实运行时
  从未生效：Nitro 的 `localFetch` 给 h3 的是 mock socket（`remoteAddress` 恒为空串），
  `resolveClientIP('')` 后 `applyClientIPHeaders` 把入口代理写好的四个 IP 头全删，
  API 只能回退 web 容器地址。现改为信任入口 `X-Real-IP`（缺失退 XFF 最右有效项）+
  单值化；web 不再接收 `TRUSTED_PROXIES`，端口必须只发布到 loopback。回归测试固定
  Nitro 的 mock socket 形状。
- Handoff：`sessions/2026-10-28-client-ip-web-edge-header.md`；
  计划：`plans/2026-10-27-client-real-ip-production.md`（2026-10-28 修订）。

### 安装弹窗收尾（信任入口按需出现 + 跳转前先卸载）

- Status: **landed（2026-10-28）**。成功态按钮改成「剩余动作」：已在跑
  （`enabled`/`upgraded`）不再渲染管理页按钮，只有信任确实待办时才在文案里提信任
  （新增 `hostAction.openPluginsEnable`）；`openManagement()` 改为 `reset()` →
  `nextTick()` → `navigate()`，并补 `dismissOnDeactivate()` 兜住 KeepAlive 停用；
  焦点归还加 `isConnected` 守卫。`bun test` 1808 pass。
- Handoff：`sessions/2026-10-28-install-dialog-handoff.md`。

### 后台新增用户（创建弹窗 + `POST /users`）

- Status: **landed（2026-10-27）**。`user.manage` 授权的创建端点在一个事务内
  写入用户、凭据、角色与审计；自设/随机两种初始密码，随机密码只在响应中出现
  一次。`/control-panel/users` 标题行新增按钮与创建弹窗，成功后回第 1 页并
  高亮新行；预览 inspector 抽成独立组件，`users.vue` baseline 降到 1138。
- Handoff：`sessions/2026-10-27-admin-create-user.md`。

### 邮件提供商默认与选择生命周期

- Status: **landed（2026-10-26）**。`mail_provider_selection` 不再是唯一
  事实源：无行时 Host 解析并持久化推荐默认（enabled `sforum.smtp`，否则唯一
  enabled provider）；disable/restart 保留偏好，uninstall/cleanup 清除；
  `mail.deliver` 区分 `skipped/provider_unconfigured` 与可重试的
  `provider_unavailable`。后台 Mail 页三态展示 ready/unavailable/unconfigured，
  reset 回到推荐默认。
- Handoff：`sessions/2026-10-26-mail-provider-default-selection.md`；
  决策：`decisions/2026-10-26-mail-provider-default-selection.md`。

### 宿主 coreVersion 暴露 + 清单兜底

- Status: **landed（2026-10-25）**。`version.Identity()` 是唯一权威
  （`coreVersion` 严格 semver / `buildVersion` 展示标签），经四条面下发：
  v2 握手 `host_build` → `Server.HostBuild()`、管理 bridge `host`
  （page-bootstrap 同包下发，零额外请求）、公开 L2 `host.coreVersion`、
  `GET /api/v1/health`。`manifest.sforumVersion` 第一次真正被求值：
  安装 422、启用/激活/升级/回滚/重启 409，统一 `extension.core_incompatible`；
  内置包豁免，非法 core 标签跳过并告警，预发布 core 按发行基线再求值一次。
  商城 `upstream.go` 的转发重开条件写在决策记录第 5 节。
  Handoff：`sessions/2026-10-25-core-version-surface-handoff.md`；
  决策：`decisions/2026-10-25-core-version-surface.md`。
- Status: **landed（2026-10-26，dev 派生）**。dev 构建的 `coreVersion` 不再恒为
  `1.0.0`：从最近可达 `v*` tag 派生严格 semver（`3.0.16`、`3.0.16+13.gSHA`、
  领先时带 `.dirty`），无 tag 才回落 `1.0.0`（migrator 要求可解析）。测试清单
  39 处 `^1.0.0` 一并订正为 `>=0.1.0`，生产代码里没有测试态分支。
  Handoff：`sessions/2026-10-26-dev-core-version-from-git-handoff.md`；
  决策：`decisions/2026-10-26-dev-core-version-from-git.md`。

### 扩展品牌资产（图标 / 社交卡）

- Status: **landed（2026-10-25）**。12 个扩展（8 内置插件 + 默认主题 + 3 个独立仓
  插件）与主仓产出同构品牌资产：`brand/icon.svg`、`brand/icon.png`（512）、
  `brand/social-preview.png`（1280×640 中文主卡），由 `scripts/brand/` 单一模板
  生成；门禁是 `sforum extension brand check` + 仓内 Go 测试；契约与模板参数见
  `../docs/extensions/brand-assets.md`，插件仓库标准见
  `../docs/zh-CN/development/plugin-standard.md`。
- **待人工**：4 个仓库的 GitHub Social preview 需手动上传（无公开 API）；重启 API 后
  8 个内置包会有一次同版本摘要漂移候选，需逐一点升级（或随 patch 版本自动晋升）。
- Handoff: `sessions/2026-10-25-extension-brand-assets-handoff.md`；决策：
  `decisions/2026-10-25-extension-brand-assets.md`。

### 应用商城 · SForum 侧（已落地）

- Status: **landed（2026-10-24；2026-10-26 扩成两个动作）**。P0（`381182cc7`）
  删除 Marketplace 死代码、`MARKETPLACE_ED25519_*` 配置与 `/extensions/store*`
  三页 + 占位货架，store UI 身份走 retired 墓碑账本；P1 新增管理页 host action：
  清单 `hostActions`（仅 `view=component`；`extension.install` 只暂存，
  `extension.install.enable` 装完由宿主启用/升级）+
  `bridge.installExtension` / `bridge.installAndEnableExtension` + 宿主确认框 +
  期望 ID/版本/类型校验（`extension.expected_mismatch`）+ 120 秒超时 +
  页面级单飞 + 详情页/启用弹窗披露；无 capability/表/worker。**残留**：真实公网
  扩展包经确认框 staged 只做到路由/服务层证明；商店侧层级 hack 与真机 QA 待收尾。
  Handoff：`sessions/2026-10-26-store-install-ux.md`（前序
  `sessions/2026-10-24-app-store-core-handoff.md`）；计划
  `plans/2026-10-24-app-store-core-install-primitive.md`。
- **发版就绪（2026-10-26，未打 tag）**：v3.0.17 的三个硬阻塞已核实 ——
  ① core HEAD `8ab9f246b` 与商城 pin `10c2b21e8` 都不认识 `extension.install.enable`
  （`git log --all -S` 为空，只有工作区有），必须先 commit + push；
  ② store `sforumVersion` 仍 `>=1.0.0`，抬到 `>=3.0.17` 是**运行期**语义诚实
  （`CheckSForumVersion` 唯一生产调用点 `Models/Extensions/lifecycle.go:720`），不是 CI 闸门；
  ③ 商城签名下载实测不稳定（同 token 一次 200 但截断、一次 503 `upstream_unavailable`）。
  清单：`sessions/2026-10-26-release-v3.0.17-readiness.md`（含门禁 G1–G6、两仓顺序、pin 形态、
  回滚点、两个开放问题的建议）。
- **真机取证标准（2026-10-26）**：已启用 / 已升级 / 已用 N 秒 / 有后端包影响面 四条各自的
  判定表（锚点选择器、文案、时间差、独立复核、反例、夹具路径）见
  `sessions/2026-10-26-install-evidence-acceptance.md`。当前目录 6 个商品无一可安装
  （3 个已装且兼容、3 个约束 `<2.0.0`），夹具按该文 §5 走 fetch-url 1.0.2 → 1.0.3。
- **本轮不做**：服务端 + 商城插件（`plans/2026-10-24-app-store-service.md`，
  新仓 `sforum-app-store`，仍是 draft）；不要创建该仓库或写插件。
- 已定：服务端独立；一个仓库两个产物；全量 GitHub App（无验证文件）；
  下载走商城签名短链代理；安装经管理页 host action（`bridge.installExtension`
  + 宿主确认框 + 期望校验），同步、只暂存、必须管理员当场确认，
  bridge 语义固定、将来可无痛升级为异步；登录走 `sforum-oauth2-provider`。
- 索引：`plans/README.md`；core 契约见 core 计划 §6.2。

### 发布 v3.0.16 正式版

- Status: **已发布 (2026-10-23T22:41:18Z)**。S3 存储插件 Cloudflare R2
  兼容（checksum when_required + 寻址三态 auto + 读流超时修复）；main
  CI/Security 全绿，release workflow 全绿，资产 10 项，GHCR
  `api:3.0.16` / `web:latest` / `migrate:3.0` 匿名拉取验证通过。
  无迁移、无手工升级步骤。
- Handoff: `sessions/2026-10-24-release-v3.0.16.md`；发布对象详见
  `sessions/2026-10-24-s3-r2-compatibility-handoff.md`。

### 多图发帖 500（附件引用唯一性作用域）

- Status: **landed (2026-10-24)**。根因是 `202607110015` 把槽位型唯一索引
  `(resource_type, resource_id, context)` 建到所有资源，topic/comment 第二张图
  必然违反约束并冒泡为 500 `internal_error`；修复收窄为槽位型部分索引 +
  论坛写入幂等化/锁序/重算计数 + 具名 422/409 错误映射。真机两图发帖 201、
  负例具名 422、浏览器渲染验证通过；`--fast` 三线全绿。
- Handoff: `sessions/2026-10-24-multi-image-attachment-500-fix.md`；
  决策：`decisions/2026-10-24-attachment-reference-uniqueness-scope.md`；
  模块：`modules/attachments.md`。

### API 连接弹窗误报治理

- Status: **landed (2026-10-23)**。连接失败先进 pending 缓冲并退避探测
  `/health`（1s→2s→4s 封顶，4s 超时容忍慢站点），跨 15s 宽限仍失败才弹
  模态（~15s+，慢响应下自然更晚）；恢复后 5s 内自动关闭并出 toast。GET 单次
  原地重试 + 默认超时 15s/30s（慢站点友好）；`/_nuxt/**` chunk / 动态 import
  失败分流为「新版本已发布」toast + 自动刷新（10s sessionStorage 冷却），
  不再误报为后端断线。
- 验证：web 1582 用例、typecheck、架构门禁通过；引擎全 fake timers 驱动。
- Handoff: `sessions/2026-10-23-api-connection-error-buffering.md`；
  模块：`modules/frontend.md`。

### 首页列表默认排序文案与兜底

- Status: **landed (2026-10-23)**。公开文案改为 最近活跃 / 最新发表 / 热门；
  默认仍是 `active`（最后活跃）。空/非法 `sort` 解析为站点默认，最终兜底
  `active`，不再落到 `latest`；后台默认排序把 `active` 标为推荐。
- 验证：web 1567 用例、typecheck、架构门禁；真实 API / 浏览器（1633×810、
  390×844）与后台设置页通过。
- Handoff: `sessions/2026-10-23-public-feed-sort-default-clarity.md`；
  决策：`decisions/2026-10-23-public-feed-sort-labels.md`；
  模块：`modules/forum.md`。

### AI 回复「成功却不回复」

- Status: **active（M1–M4 landed 2026-10-23；M5 剩浏览器 QA / eval 基线 / 保留策略）**。
  执行记录里的 `succeeded` 只说明模型调用成立；「回复是否出现」现在是独立的一行事实
  （`ai_reply_outcomes`，六种稳定 outcome）。空补全判失败且永不进结果缓存，缓存命中行
  在控制台带 `cache` 徽标与「0 ms / 0 token 是正确值」的解释。生成器按
  `(topic, trigger)` 幂等，写失败不回滚发布。
- 排查入口：`sforum ai:reply --topic-id N [--confirm]`（真实帖子 + 真实配置、禁用
  缓存、不发布）；控制台 `?tab=observability` 的「回复结果」区块。
- Handoff: `sessions/2026-10-23-ai-reply-silent-success-handoff.md`；计划：
  `plans/2026-10-23-ai-reply-silent-success-remediation.md`；决策：
  `decisions/2026-10-23-empty-completion-is-a-failure.md`、
  `decisions/2026-10-23-reply-outcome-ledger.md`。

### 编辑器链接续写与代码块粘贴修复

- Status: **landed (2026-10-23)**。`SForumLink` 固定 `inclusive:false`，修复粘贴/插入
  链接后继续输入仍被并入链接；工具栏空选区改为插入 URL 链接文字（不再写 stored
  mark）。代码块内多行粘贴改为字面文本（HTML-only 剪贴板也覆盖），纯代码 HTML
  合并为单个 fenced block；`SForumCodeBlock.toggleCodeBlock` 把顶层多段选区合并成
  一个代码块，不再每段一块。
- 验证：web 1565 用例、typecheck、架构门禁通过；真实浏览器 QA 覆盖链接续写、
  工具栏空选区链接、HTML-only 代码块粘贴、逐行 `<pre>` 合并、多段切换单块。
- Handoff: `sessions/2026-10-23-editor-link-codeblock-fix.md`；
  模块：`modules/frontend.md`（Editor And Content UI）。

### 内置插件自动更新

- Status: **landed (2026-10-23)，随 v3.0.13 正式版发布**。API 启动阶段新增 `BuiltinPluginAutoUpdater`：
  对已启用、`source=builtin` 且有更新 staged SemVer 的受保护插件，复用现有
  `Upgrade`/`Restart` 生命周期自动晋升；`builtin-auto` 无密码系统账号绑定
  `builtin_auto` 角色（仅 `extension.view` + `extension.plugin.manage`），
  authority=`builtin`，不写 trust grant。
- 安全闸：停用/installed 不处理；只升不降；同版本不同 digest 保持 staged；
  Safe Mode/recovery/无人类用户/已有 open operation 全部跳过；legacy 插件若
  正被选为 mail/storage/search/notification provider 则跳过，避免重置选择。
- 多节点：Postgres session advisory lock + `builtin-auto/<id>/<digest>` 幂等键；
  失败写 `builtin_auto_update_failed`，不阻断启动。
- 管理端：`extension.builtin_auto_update` 默认 `enabled`，插件页开关 + 内置
  badge；关闭后保留手工“全部升级”。
- 验证：Go 全量测试、隔离 Postgres 集成测试（含真实 legacy `Restart`）、
  web 1533 用例、typecheck、架构门禁；浏览器 QA 1633×810 / 390×844 通过。
- Handoff: `sessions/2026-10-23-builtin-plugin-auto-update.md`；
  决策：`decisions/2026-10-23-builtin-plugin-auto-update.md`。

### 发布 v3.0.13 正式版

- Status: **已发布 (2026-10-23)**。`ghcr.io/keeptrue/sforum-{api,migrate,web}:3.0.13`
  同时写入 `latest` 与 `3.0`（linux/amd64 + linux/arm64，带 attestation，匿名拉取
  验证，三个 tag 的 manifest digest 一致）；GitHub Release 正式版（10 个资产 +
  `SHA256SUMS`）；npm `@sforum/{admin-sdk,plugin-ui}@1.1.0` 命中 exact-artifact，
  按设计跳过发布。
- 发布前确认：main `8dad007fd` 的 CI 5/5 job 全绿、Security 全绿；本地
  `./scripts/test.sh --fast` 三线全 PASS（api 161s / web 48s / static 20s）。
- 范围：自上一稳定版 `v3.0.9` 起 160 个提交；Release 24 个 job 全 success
  （run `39890804744`）。发布后把自动 compare 链接修正为 `v3.0.9...v3.0.13`。
- Handoff: `sessions/2026-10-23-release-v3.0.13.md`

### 全新安装默认主题免手动激活（Host 批准）

- Status: **landed (2026-10-23)**。全新库首启不再停在 Core fallback：受保护内置默认
  主题由 Host 在启动修复/首次导入时批准核心页替换
  （`approval_source = 'host'`，无 actor），上传/第三方主题仍必须 super_admin 显式
  批准，内置主题新 digest 仍要人工激活一次。
- 根因：审批只读 `page_provider_bindings.approved_by`（FK → `users`），新库 0 用户
  时结构上不可能批准，于是 `SwitchThemeContributions` 只登记候选、不写绑定。
- 运行时证据：空库 → 单条 `startup_repair` 发布 `(approved=t, host, actor=NULL)`、
  34 条 host 绑定、`/pages/resolve?id=forum.home` → `provider=sforum.default-theme`、
  渲染三栏壳；重启后不重复发布、不降级。
- Handoff: `sessions/2026-10-23-host-approved-default-theme-handoff.md`；
  决策：`decisions/2026-10-23-host-approved-default-theme.md`。

### 全量测试提速（Go 分片并行 + 三线并行）

- Status: **landed (2026-10-23)**，本地全绿；CI 待 PR。`scripts/test.sh` 现在三条线
  并发（api / web / static，日志在 `tests/.logs/`），Go 段从「一个 database 上 `-p 1`」
  改成「多个一次性集群分片，片内 `-p 1`」（`SFORUM_TEST_SHARDS` 默认 min(nproc,4)）；
  compat 农场并入同一次调用（`--with-compat`）；新增 `--fast` 本地分层；typecheck 走
  vue-tsc 增量。
- 关键结论：任务书原定的「`-p` 提到 8」实测不安全——跑迁移链/写 public 共享行的包会
  互相踩出 `extension database physical resource conflict`、
  `lifecycle operation already in progress`、`could not open relation with OID`、
  `core database authority conflicts` 四类失败。所以并行度挪到集群级。
- 计时：21:31 → 7:24（冷）/ 6:14（热）/ 0:41（`--fast`），154 个包两轮冷跑零失败。
- Handoff: `sessions/2026-10-23-test-suite-speedup-handoff.md`；
  决策：`decisions/2026-10-23-test-gate-parallelism.md`。

### 公开 L2 恒开（移除 `SFORUM_V3_PUBLIC_L2`）

- Status: **landed (2026-10-23)**。删除配置字段、env 读取、`WithPublicL2` 与装配点，
  以及两份 Compose / 两份 env 模板 / 生产 env 生成脚本中的该变量；三处 gate 只保留
  Safe Mode 与 exact-artifact trust。`tests/validate-v3-production-trust.mjs` 反转为
  禁止再引入该变量；新增"信任门关掉时 L2 仍失败关闭"的测试。
- 隐含前提：`SFORUM_V3_TRUST_CHALLENGES=false` 会连带关闭公开 L2（没有信任门就没有可
  校验的授权），而 `.env.example` 与 `compose.yaml` 的非生产默认值正是 `false`；中英
  部署文档与 `authoring-guide` 已就地标注，避免新环境按默认配置只看到 SSR fallback。
- 行为变更：旧部署仍设 `SFORUM_V3_PUBLIC_L2=false` 会被静默忽略；内置
  `sforum-shortcodes` 的内容区样式从此默认生效。
- Handoff: `sessions/2026-10-23-public-l2-always-on-handoff.md`

### 发布 v3.0.13-alpha.1

- Status: **已发布 (2026-10-22)**。`ghcr.io/keeptrue/sforum-{api,migrate,web}:3.0.13-alpha.1`
  （amd64+arm64）、GitHub Pre-release（10 个资产）、npm `@sforum/{admin-sdk,plugin-ui}@1.1.0`。
- 途中修掉三处真实缺陷：Go stdlib CVE-2026-111943（toolchain → 1.26.10，26 个
  go.mod 同步）、migrate 缺绝对 `EXTENSION_ROOT`、内置插件源码契约漂移（8 个插件
  patch 版本 bump）；另发现 npm Trusted Publishing 从未验证过（旧版本一直走 skip），
  已由 operator 补齐授权。
- Handoff: `sessions/2026-10-22-release-v3.0.13-alpha.1.md`

### Dependabot 告警清账与依赖刷新

- Status: **landed (2026-10-22)**。45 条 open 告警里 5 条真实（vite 的 GHSA，8.1.6 起
  patched，本次直接升到 8.3.4 合并），40 条 composer 告警算的是 2023 年 PHP 时代、
  `main` 全历史都不存在的 lock，按 `not_used` dismiss。
- Next：在 Settings 关/开一次 Dependency graph 清陈旧快照，确认
  `dependencyGraphManifests` 不再含 composer 条目。
- Handoff: `sessions/2026-10-22-dependabot-alert-triage.md`

### 发布前 CI / Dependabot 红色修复

- Status: **landed (2026-10-22)**。main 的 `CI / Contracts and static gates` 由过时
  的 `useAdminRoutes` 静态断言引起（`useI18n`/`defaultLocale` vs 现行
  `useLocalePath` 委托），已按现行契约改写；`Extensions` 集成 fixture 的
  `users_pkey` 冲突改为 23505 重试；`/apps/web` Dependabot 生态从 `npm` 改为 `bun`
  （原配置每周以 `misconfigured_tooling` 失败）。
- 验证：`contracts-gate.sh` 全流程 + 全量 `go test ./...` + actionlint 全绿；
  pkey 冲突在同一库状态下「旧代码红 / 新代码绿」对照复现。
- Handoff: `sessions/2026-10-22-ci-gates-red-fix.md`

### 登录强化人机验证触发条件

- Status: **landed (2026-10-22)**，浏览器 QA 通过。`login_risk` 从「失败后」扩成三个
  可叠加触发条件：总是验证 / 新设备（UA 指纹）/ 管理员账号，全部受 `login_risk`
  总闸约束；限流改为可配置（1–600，默认 60）。
- 取舍：登录同时签发 PAT 的请求豁免验证（已知可绕过面，已记录）；OAuth 登录本轮
  不接入；`post_risk` 仍是死开关。
- 验证：Go build + `app/...`/`bootstrap/...` 测试全绿；web 1508 用例 + typecheck；
  OpenAPI refs OK。
- Handoff: `sessions/2026-10-22-login-human-verification.md`
- Decision: `decisions/2026-10-22-login-human-verification-triggers.md`
- Modules: `modules/identity.md`、`modules/options.md`

### 首注册者注册后进入后台

- Status: **landed (2026-10-22)**。零用户站点首位注册者（初始 `super_admin`）注册
  成功后直接落到后台概览（`useAdminRoutes().path('/')`，默认 `/control-panel`），
  不再回到 referrer/首页；其他注册者维持常规认证回跳。邮箱验证开启时先验证，验证后
  落后台。
- 顺带修正：`useAdminRoutes().path()` 改为经 i18n `localePath` 生成。`no_prefix` 下
  手工拼 `/<locale><prefix>` 的客户端导航会落到 Page Registry catch-all；后台入口
  对 en 语言恢复正确。
- 验证：web 1508 用例（含新增 `adminRouteLocalization` 与首注册者验证落地）+
  typecheck 通过；dev 上 `/control-panel` 确认为真实路由。
- Handoff: `sessions/2026-10-22-first-admin-admin-landing.md`
- Decision: `decisions/2026-07-04-configurable-admin-control-panel.md`（2026-10-22 更新段）
- Modules: `modules/identity.md`、`modules/frontend.md`

### 公开资产跨源发布（QQ 内置浏览器 500）

- Status: **landed (2026-10-22)**，待部署后复测。QQ 内置浏览器（X5 云加速）换源后
  把 `/_nuxt/**`、`/_sforum/**` 判为跨源，缺 ACAO / `CORP: same-origin` 导致入口
  chunk 与扩展样式被拒，页面停在错误页且动作全为 JS 死链。
- 修复：公开只读资产由应用自己发 `ACAO: *` + `CORP: cross-origin`（`/_nuxt` 用
  Nitro routeRules，`/_sforum/assets` 在代理 `onResponse` 覆盖上游，图标路由同
  helper）；扩展样式去掉 `crossorigin` 保留 `integrity`；两个 ingress 补同名头作冗余；
  错误页动作改真链接（`SFButton` 支持 `href`）。
- 验证：web 1503 用例 + typecheck + 架构门禁、Go build/Extensions 测试通过；dev 实测
  两处公开资产头正确。
- Handoff: `sessions/2026-10-22-public-asset-cross-origin-handoff.md`
- Decision: `decisions/2026-10-22-public-asset-cross-origin.md`

### 昵称修改与昵称策略

- Status: **landed (2026-10-20)**。成员可在 `/settings/profile` 自助改昵称；后台在
  「站点设置 → 注册与用户名」控制开关、冷却（默认 7 天，分钟精度）、长度与曾用名
  是否公开。昵称**非唯一**：唯一性只属于 username；重名只提示，撞他人用户名才拒绝；
  保留词复用用户名列表。改名历史永久保留，默认仅后台可见。
- 后端：Identity 的 `NicknameService`（事务内行锁 + 数据库时钟判冷却）、
  `profile.nickname.*` 选项、`PUT /profile` 接受 `displayName`、
  `GET /profile/nickname/availability`；改名后同步 `search_documents` 作者昵称。
- 前端：`SFProfileNicknameField`（本地预检 + 防抖预检 + 分钟级倒计时）、后台策略
  区块、后台用户详情改名历史、公开主页曾用名。
- 验证：Go 全量 + web 1496 用例 + typecheck + 架构/OpenAPI/目录门禁；浏览器实测
  1633×810 与 390×844。QA 改名数据已从开发库清理。
- Handoff: `sessions/2026-10-20-nickname-policy.md`
- Decision: `decisions/2026-10-20-nickname-policy.md`
- Modules: `modules/profile.md`、`modules/options.md`

### 富内容围栏（mermaid / chart / 公式）

- Status: **landed (2026-10-20)**。AI 与人类都能在正文里画脑图/流程图/时序图/统计图/
  公式：**不扩短代码身份**，载体是代码围栏（`mermaid` / `chart`）与 `$…$` / `$$…$$`；
  客户端渐进增强接在 `v-highlight`，KaTeX 走 MathML、图表走窄 schema + Chart.js、
  脑图走 mermaid `mindmap`。AI 提示词新增宿主拥有的富内容段（`forum-reply@10`），
  顺序 base → 富内容 → 工具 → 安全尾注。
- 浏览器实测（发帖 / AI 评论 / 编辑器预览三条路径）：真实 mermaid / chart / MathML
  出图、超限回落、颜色模式重渲染、390 宽无溢出；水合前有 shimmer 骨架，图出现不顶动
  正文。期间发现并修掉四个真实缺陷：**mermaid `click` 的 `<a>` 绕过站外中转**、
  **提及链接化把 SVG `<style>` 里的 `@keyframes` 当成 @提及**（小写 tagName 漏过白名单）、
  **chart 在 pass cleanup 后几毫秒消失**（abort 监听留在成功路径）、**视口内图块被
  紧接着的 updated 收掉后停在骨架**。
- 未做：eval baseline（需真实网关 + 加密密钥）、工具栏「插入图块」菜单（需登录才能
  渲染 QA）、摘要里剔除图源码（刻意留作后续）。
- Plan: `plans/2026-10-20-rich-content-fences.md`
- Handoff: `sessions/2026-10-20-rich-content-fences-handoff.md`
- Decision: `decisions/2026-10-20-rich-content-fences.md`
- Modules: `modules/frontend.md`、`modules/ai.md`、`modules/forum.md`

### 测试门禁吞吐

- Status: **landed (2026-10-20)**。`scripts/test-api-isolated.sh` 用固定宿主端口
  让 Go 测试结果缓存对数据库包生效（随机端口让 45 个包每次重跑 662 秒），
  一次性集群关掉持久化，`go test -p` 可配置。基线 21m31s，其中 Go 阶段 14m45s。
- 同轮把 `TestCoreMigratorRejectsKernelAuthorityDrift` 的 21 个子用例并行化，
  73.8s → 14–16s；`database/migrator` 整包 174.0s → 114.7s。模板库克隆方案
  因 owner 角色由 `current_database()` 派生而否决。
- Handoff: `sessions/2026-10-20-test-gate-throughput.md`
- 下一步热点：`Models/Extensions` 87–114s、`Models/Identity` 72–90s；
  Nuxt build 内存充裕时约 36s，swap 打满时会以 EPIPE/ENOENT 假失败。

### AI 观测层与能力层（A 批 + G + B 批）

- Status: **completed (2026-10-19)**。十项任务全部落地；同日把仓库门禁从「HEAD 多处红」推到 `./scripts/test.sh` 全绿（架构闸门 9→0、typecheck 0 错、1321 web 用例全过、Go 与 compat farm 全绿、两个 CI gate 脚本 EXIT=0）。：ToolTrace / run correlation id /
  轨迹回放 / `sforum ai:eval` 基线 / 预算默认步数 3→5 / 三个新只读工具 / 两条派生用途 /
  证据链溯源 / 会话状态。验证矩阵 L1–L5 通过（L3 的 9 条是 HEAD 既有告警），L6/L7
  有运行时与浏览器证据。
- 残留（非门禁）：真实模型产出 `[[cite:]]` 是概率性观测（写入/渲染路径均已确定性覆盖）；
  `bun run dev` 需重启一次让新增 i18n 客户端文案生效（D22）；派生用途的控制台按钮
  需要时再加 HTTP 端点（D16）。
- Plan: `plans/archive/2026-10/2026-10-19-ai-observability-and-capabilities.md`（含 D1–D24）
- Handoff: `sessions/2026-10-19-ai-observability-and-capabilities.md`
- Modules: `modules/ai.md`
- Decision: `decisions/2026-10-19-ai-topic-state-invalidates-storage-not-just-injection.md`

### Control-panel AI 保存 422 与上限放宽

- Status: **landed (2026-10-19)**。422 根因（空串/小数数字字段、无上界钳制、
  「填了但不合法」的供应商、自动裁决空动作列表）全部修复；`Support/AI` 天花板按
  模型能力重定标（输出 128k、输入 12 MiB、工具步数 12），默认值不变；上限经
  `GET /admin/ai/settings.limits` 下发给控制台，422 改为字段级 `data.fields`。
- Plan: 无独立计划文件；设计与取舍见
  `decisions/2026-10-19-ai-context-and-budget-ceilings.md`
- Handoff: `sessions/2026-10-19-admin-ai-save-422-and-limits.md`
- Modules: `modules/ai.md`

### AI Web Search Plugin（插件工具 + 工具目录/上下文补强）

- Status: **Core 侧 landed (2026-10-18)**；插件侧在仓库外
  `~/Code/github/sforum-web-search`（`sforum.web-search`，四 provider 适配器）。
  Core 交付：`aiTools[].context`（`locale`/`topic`/`viewer`，缺省 `locale`）、
  `GET /admin/ai/settings` 的 `availableTools[]`、控制台用途页签的已注册工具选择器、
  停用插件不再登记工具、工具调用的 locale 来自站点默认语言。
- Plan: 无独立计划文件；设计与取舍见
  `decisions/2026-10-18-ai-tool-context-and-catalog.md`
- Handoff: `sessions/2026-10-18-ai-web-search-plugin-handoff.md`
- Modules: `modules/ai.md`

### Core Attachment Extension API（OAuth2 图标上传）

- Status: **landed (2026-10-18)**. Core 侧全部交付：三条 Host Command
  （`sforum.attachments.extension.claim/release/list@1`）、公开读谓词拆分、
  卸载自动释放、`attachment.extension_reference_limit` 选项。实现落在
  `app/Models/Attachments/ExtensionReference`（扩展级 advisory 锁 + 按引用行重算计数）。
  插件侧接线（manifest `host_commands`、client-write、上传控件）在
  `sforum-oauth2-provider` 仓库，未在本仓库内。
- Plan: `plans/2026-10-18-core-attachment-extension-api.md`
- Handoff: `sessions/2026-10-18-core-attachment-extension-api-handoff.md`
- 需求来源: `sforum-oauth2-provider` 应用图标「上传图片」
- Modules: `modules/attachments.md`, `modules/extensions.md`

### Extension Authoring Speed And Controlled Power

- Status: **completed and archived (2026-10-17)**. M0–M9 complete; **M10 all ten rows
  evidenced** — row 5 closed with per-clause evidence (compiled-fixture joined test,
  real-PostgreSQL erasure matrix, live `privacy:erase-user` erase) and the anonymous
  clause closed by decision D1 as unavailable by construction; row 9 closed on the
  real `v3.0.9` registry tag and the production Caddy/TLS layer; **row 7 closed on
  real two-node evidence** (two API nodes from the published `v3.0.9` images sharing
  one durable PostgreSQL + Redis: dual registration with live leases, identical
  revision acks, cross-node disable/enable converge within the poll window with
  byte-identical artifact paths, graceful node-depart/Return with per-node
  acknowledgements). Two measured cold-start races are mitigated in the runbook
  (first-migration deadlock under simultaneous boot; simultaneous first-boot
  sync-order race — sequential node boot). Raw Fiber context, raw Core session
  mutation, actor minting, and pre-plugin middleware remain closed by contract.
- Plan: archived at `plans/archive/2026-08/2026-08-22-extension-authoring-speed-and-controlled-power.md`
- Handoff: `sessions/2026-10-17-extension-authoring-closeout-handoff.md` (current,
  amended with Revision 5 row-7 closure)
- Decision: `decisions/2026-10-16-extension-authoring-contracts-freeze.md`
- Modules: `modules/extensions.md`, `modules/frontend.md`, `modules/identity.md`

### Dev Trust Sessions (Session-Level Dev Auto-Trust)

- Status: **complete (2026-10-17)**. Backend, CLI, routes/catalog, OpenAPI,
  docs, tests all landed; admin UI panel is follow-up work.
- Handoff: `sessions/2026-10-17-dev-trust-sessions-handoff.md`
- Decision: `decisions/2026-10-17-dev-trust-session.md`
- Module: `modules/extensions.md` (M8 dev loop entry)

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
- 2026-10-19 模型工具目录与统一配置（`tools` 页签）：三源说明（运营者 > 插件清单 >
  无）、逐用途暴露与自动收敛、未注册引用清理、三种可用性诊断。
  Handoff: `sessions/2026-10-19-ai-tools-tab-handoff.md`；决策：
  `decisions/2026-10-19-ai-tool-catalog-console.md`。
- 2026-10-23 AI 工具可见性收敛为单一编辑面：`purposes[id].tools.allowed` 只在
  `?tab=tools` 编辑；用途页签改为只读摘要 + 深链；手动点名与逐用途恢复声明搬进
  `SFAdminAIToolReferencesBlock` 与矩阵列头；前端 `{"tools":{}}` 判定与后端对齐。
  决策：`decisions/2026-10-23-ai-tool-visibility-single-owner.md`。

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

- **CI 转绿与 v3.0.20 发布（2026-10-27，最新）**：附件错误码本地化、
  核心路由计数 380、CLI `recovery` 文档覆盖、两个静态契约测试跟随真实归属；
  Release tooling 因共享插件运行时变更要求 8 个内置插件 patch 抬版；
  Web SDK 1.3.0/1.1.0 因仓库改名元数据漂移 patch 到 1.3.1/1.1.1；发布链路的
  GHCR `imagetools inspect` 加有限重试。`v3.0.20-beta.2` 发 prerelease 验证链路后，
  stable `v3.0.20`（提交 `04765138d`）已发布：镜像带 `v3.0.20`/`3.0.20`/`latest`，
  匿名拉取与 npm SDK 校验通过；发布说明的一键安装改用 README 同一行
  `releases/latest/download/sforum-install.sh`。见
  `knowledge/sessions/2026-10-27-ci-green-and-v3.0.20-beta.2.md`。
- **交互式安装目录（2026-10-27）**：一行 loader 先问安装目录（默认
  `./sforum`，提示与回显都是绝对路径；当前目录已是安装目录时默认当前目录），
  `-d`/`SFORUM_INSTALL_DIR` 跳过询问，新增 `SFORUM_NONINTERACTIVE=1` 全链路不问。
  `deploy.sh`/`upgrade.sh` 成功与 `status` 打印安装目录、配置文件、备份目录，
  `.deployrc` 记录 `install_dir=`。见
  `knowledge/sessions/2026-10-27-interactive-install-directory.md`；决策
  `knowledge/decisions/2026-10-27-interactive-install-directory.md`。
- **首页右栏问候语按时段生成（2026-10-27）**：`晚上好，{name}` 不再写死，
  `utils/forum/forumHomeGreeting.ts` 分五档（05 早 / 11 中 / 13 下 / 18 晚 / 23 深夜），
  `useForumHomeGreeting` 在 SSR 用站点时区、挂载后改用访客本地时区，`useState` 钉首帧
  避免水合不一致；`welcomeUserTitle` 降级为缺键兜底。见
  `knowledge/sessions/2026-10-27-home-right-rail-greeting.md`。
- **Version GC 钉住主题发布 source（2026-10-27）**：GC 判据补上
  `theme_runtime_publications` 的 source 三元组（与插件账本共用
  `publication_history`），dry-run/execute 同源 + 锁内复核；主题历史版本基本不再
  回收，是刻意 fail-closed。见
  `knowledge/sessions/2026-10-27-version-gc-theme-publication-pin.md`。
- **商城更新徽章：真实数据取证（2026-10-27）**：三条 `list_column` surface 在 dev 上
  返回非空 cells，三页徽章断言全通（`<a>`、不在 `button` 内、`arrow-up-right`、
  `title` 含「来自 sforum.store」、深链落 `/control-panel/extensions/sforum.store/pages/catalog?product=<slug>`
  并开弹层）；「只看有更新」A/B：plugins 11→12 / themes 1→2 / overview 12→14。
  真实商城对三个已装扩展只发布同版本商品（逐 `extensionIds=` 核实），因此徽章数据用本地
  fixture 目录服务取得并明确标注；真实上游作为「无更新」负向对照（200 + `cells {}`）。
  顺带定位**徽章非首屏就绪**：overview 从加载到 3 枚徽章齐备约 8.3s（surface 早于 resources
  就绪 → 空 resources → 空 cells），这是本轮最初「0 徽章」的直接来源。商城仓三步门禁全绿
  （go test / 826 pass / manifest:check），文案三处逐字一致；遗留 `surfaces_test.go` 未保护
  `Label`、站内商城页 `copy.ts` 仍是「可更新至 v」。见
  `knowledge/sessions/2026-10-27-update-badges-live-evidence.md`。
- **扩展管理「更新」三态词汇与两控件行（2026-10-27）**：三页不再渲染原始
  `stagedVersion` 徽章，改为每行派生 `可升级 v{staged}` / `重启后生效` / `激活后生效`
  （`updateState.ts`，semver 2.0.0 含 prerelease、非法值 fail-closed）；行收敛为主按钮 +
  `⋯` 菜单（`⋯` 永远渲染且至少含「管理」，Lead 覆盖了原「只剩重复项不渲染」规则），
  `全部升级（N）` 只收版本升级、`{m} 个内置更新待重启` 只数内置**插件**内容更新，
  `只看有更新` 为不持久化视图开关。审查发现 P2×2 + P3×4，frontend 修复后复核通过。
  终态门禁：`bun test` 1777 pass / typecheck / 架构边界 / staged 契约 /
  `v3-catalog --check` / `go build` 全绿；浏览器 QA 实测 22/20/2 行、筛选 9/8/1、
  内置提示 6、`⋯` 全行可达、390 无溢出；商城 `list_column` 本轮 0 条，`+商城` 未覆盖。
  见 `knowledge/sessions/2026-10-27-extension-update-vocabulary.md`。
- **扩展管理三页的「可更新」徽章（2026-10-27）**：Core 复用 `list_column`
  Admin Surface，结果新增可选 `links`（资源 ID → 同源后台路径，`safeAdminPageId`
  白名单，非法值降级为不可点击徽章）；三页（plugins / themes / overview）经
  `useAdminListSurfaces().badgesFor()` 挂徽章，身份簇抽成
  `SFAdminExtensionIdentity`，徽章在选择按钮之外；只读访问需 `admin.access` +
  `extension.view`（只带后者会被重定向）。是否有更新仍由 `sforum.store`
  插件判定，Core 不加商城目录/表/API。T5 验收：`bun test` 1767 pass，typecheck /
  架构边界 / `v3-catalog --check` / `go build` 全绿；浏览器证据 fixture 与真实
  分开记录。遗留 3 条 P2（503/504/502 文案不可区分、restart 后出站连接僵死，
  以及「只读 UI 可点管理动作」——该条为 pre-existing、非本功能引入，out of scope）
  与 3 条 P3。见
  `knowledge/sessions/2026-10-27-extension-update-badges-review.md`。
- **一行安装 loader（2026-10-27）**：新增第 10 个 Release 资产
  `sforum-install.sh`，把 20 行部署命令压成
  `curl -fsSL .../releases/latest/download/sforum-install.sh | bash`。loader 只做
  解析标签、校验该标签的 `sforum-bootstrap.sh`（`SHA256SUMS` 精确条目 + 可选
  attestation + `SFORUM_INSTALL_SHA256` pin）、落盘、exec，不含安装逻辑；
  `sforum-bootstrap.sh` 仍然禁止管道执行，`validate-docs` 的规则从「禁止一切
  curl|bash」收窄为「只有 loader 可管道」。默认装到 `./sforum`，已装目录自动转
  upgrade，无终端时降级 `--yes`。首层未校验与 `get.docker.com` 同级，审计版命令
  保留在 `one-click.md`。见
  `knowledge/sessions/2026-10-27-one-line-install-loader.md`。
- **生产真实客户端 IP（2026-10-27）**：`user_sessions.ip_address` 记成
  `192.168.*` 的根因是生产默认零信任、`TrustPrivate` 把链上的内网客户端当代理剥掉、
  `TRUST_PROXY` 又不是总开关。现在 `clientip.Config` 带 `TrustProxy` 总开关（零值
  fail-closed），私网信任只作用于 TCP 对端，链条目只认显式代理与 loopback；
  `compose.yaml`、向导 `configure-production.sh` 与 `.env.production.example` 三处
  统一写 `TRUST_PROXY=true`、`TRUST_PROXY_PRIVATE=true`、
  `TRUST_PROXY_LOOPBACK=false`、空 `TRUSTED_PROXIES`（api 只发布 loopback，对端是
  `web` 容器）；Nuxt 在转发前解析唯一客户端 IP 并覆盖上行头，写限流键统一走
  `clientip.FromCtx`。`deploy/caddy/Caddyfile` 用 `{client_ip}` 重写 `X-Real-IP` 并删除
  `CF-Connecting-IP` / `True-Client-IP`（否则任意客户端自带这两个头就能指定落库 IP），
  CDN 段由 Caddy `servers.trusted_proxies` 处理、应用侧留空。部署文档删掉「同机 Caddy
  用 `127.0.0.1/32`」这个错误配方，换成每跳对端拓扑表。决策
  `decisions/2026-07-12-client-real-ip.md`（2026-10-27 修订）；handoff
  `sessions/2026-10-27-client-real-ip-production.md`；计划
  `plans/2026-10-27-client-real-ip-production.md`。

- **dev「Core 版本未知」不再等于「更低」（2026-10-27）**：shallow clone 让
  `git describe` 退化成裸 sha，dev 构建回落哨兵 `1.0.0`，再被 `validateCoreMigrationTarget`
  当成真实降级，API 起不来。现在 `version.CoreCompatibilityDeclared()` 把「版本」与
  「是否真实声明」成对暴露，字符串仍是唯一版本权威；未声明的 target 不参与任何**版本比较**
  （ledger 兼容性、运行时状态降级围栏、物理 kernel 兼容性），但 exactness/结构校验一条不减，
  改为打一条 `git fetch --unshallow` 的可操作告警。复核补了三处：物理 kernel 闸门改传
  `declared` 而不是空 target（空串会连 exactness 校验一起跳过）；未声明运行不再写
  `current_version`（只把 `target_version` 收敛回去）；**生产源码构建必须显式声明版本**——
  `runStartupMigrations`（`MIGRATE_ON_STARTUP` 早退之前）与 `cmd/migrate` 双闸门 fail-closed，
  `compose.yaml` 的 api 与 `compose.prod.yaml` 的 migrate 都补了 `SFORUM_VERSION` build-arg，
  且 `v` 前缀在兼容版本解析处归一（部署侧沿用 v-tag 语义的同一个变量）。决策
  `decisions/2026-10-27-dev-core-version-unknown-vs-lower.md`；handoff
  `sessions/2026-10-27-dev-core-version-unknown-vs-lower-handoff.md`。

- **个人资料页扩展 tab 支持 `l2Widget` 原地挂载（2026-10-26）**：只重开
  `forum.profile.tabs` 一个贡献点 —— 选中 tab 后由宿主在资料页内容列渲染面板，
  它是容器；actions / sidebar / badges / nav 仍是动作与标记，M4 对其结论继续有效。
  payload 新增 `type: l2Widget` + 必填 `componentId`（须指向本包 public L2 组件，
  组件自身不得带 `Permission`）与可选 `permission`（服务端按 `Actor.Can()` 过滤，
  `json:"-"` 永不下发；**是可见性过滤，不是安全边界**，数据访问仍由插件路由自查）。
  内置三项位置不变，widget tab 排在「关于」之后，单页上限 8 个、按贡献顺序截断。
  资料页 widget refs 已并入页面文档 CSP 聚合（`SFPageOutletResolver` 原生路径 /
  `SFThemeTemplate` 主题路径，各一次）。注意失败模式：不聚合**不会**拦挂载（无
  header 时同源 import/link/fetch 本就放行），代价只是没有 digest 样式预注入
  （主题路径）与 policy digest 证据。决策
  `decisions/2026-10-26-profile-tab-l2widget.md`；handoff
  `sessions/2026-10-26-profile-tab-l2widget-handoff.md`；模块
  `modules/profile.md`、`modules/extensions.md`。

- **卸载 tombstone 三层诊断：grant 层已修，identity purge 层未解（2026-10-26，最新）**：
  908/909 长期 `pending` 的根因分三层，本轮修到第二层。① 授予校验只按
  `extension_id + status='active'` 取首行（隐含「一个扩展一条 active grant」，与 schema
  相反）→ 改为 exact-artifact 三元组匹配（`ae3913ec6`）；② identity purge 与 append-only
  发布账本的 `ON DELETE RESTRICT` 冲突 → **需要新决策，归
  `plans/2026-10-26-uninstall-identity-purge.md`，不能写成「已修好」**；③ 703 是
  fail-closed 正确、缺的是 `superseded` 出口，不是 bug。`record id 30/34` 是 cleanup
  record 编号而非「30/34 条成功」—— 这两条能 finalized 恰恰因为它们从未进过发布账本，
  是**负证据**。另有 op 926 job publication 停在 `target/pending` 的读取侧缺线
  （`plans/2026-10-26-op926-job-publication.md`）。诊断
  `sessions/2026-10-26-uninstall-tombstone-diagnosis.md`、决策
  `decisions/2026-10-26-disposition-grant-exact-artifact.md`。**本轮 5 个提交全部未
  push、未打 tag**，待推送清单与三条易误读结论见
  `sessions/2026-10-26-store-install-release-handoff.md` 的 Next。商城 6–7MB 包交付风险
  （90s 硬超时，推荐 A 收敛版 + 错误码修正）见
  `plans/2026-10-26-store-package-delivery-risk.md`。

- **v3.0.18 补发：商城安装链路首次真正发布（2026-10-26，最新）**：`v3.0.17` 的
  tag 已推但**零发布** —— `release.yml` 的 `Publish versioned Web SDKs` 卡在
  `Vue scaffold must depend on "@sforum/admin-sdk": "^1.3.0"`，`promote` 及其下游
  全部 skipped，npm / GHCR `latest` / GitHub Release 三处都没动；按「不移动已推送
  tag」惯例保留该 tag，由 `v3.0.18` 补发（tag message 首行即写明补的是 v3.0.17）。
  补发 run 24 job 全绿：Release「SForum v3.0.18」11 个 asset、
  `@sforum/admin-sdk@1.3.0`、GHCR `latest` 与 `3.0.18` 同 digest。core HEAD
  `26d808403` = tag `v3.0.18`（`v3.0.16..v3.0.18` 30 个提交，含宿主动作二连
  `extension.install.enable` 三态 `enabled`/`upgraded`/`staged`、`CoreCompatibilityVersion`
  从 tag 派生、`LifecycleRecovery`、平台闸门、`LiveGrant` NULL 修复）；store HEAD
  `6b3885f`，pin 完整 40 位 `26d808403…`（`# v3.0.18`）、`sforumVersion >=3.0.17`、
  `backend.digest 82156831…`，CI 两 job 全绿。关键修复：把 Web SDK 校验从
  release-only 挂进主门禁 `ci.yml`（原先失败永远发生在发版最后一步）。**残留**：
  store `ci.yml:75` 注释文本仍写 v3.0.17（与 ref 的 `# v3.0.18` 不一致，纯注释）；
  真机取证 P0-2/P0-3 未过（取包截断 2.0MB、一次 503）；6–7MB 包贴 90s 宿主超时，
  见 `plans/2026-10-26-store-package-delivery-risk.md`。详见
  `sessions/2026-10-26-store-install-release-handoff.md`；安装交互与弹窗层级
  （宿主弹窗固定 `z-[80]`、商店侧 z-index hack 已移除）见
  `sessions/2026-10-26-store-install-ux.md` 与
  `decisions/2026-10-24-admin-page-host-action.md`（2026-10-26 修订）。

- **插件/主题架构兼容闸门（2026-10-26，最新）**：Core 新增
  `extensionpackage.DetectTarget`（fat Mach-O → buildinfo → ELF → Mach-O → PE，
  读文件头不信清单声明，探测不出即放行），安装阶段**落盘前**拒绝错架构插件
  （422 `extension.platform_incompatible`，`fields=required/host/entry`），
  启用/激活/升级/回滚/重启 409 同 reason；主题、纯前端插件、脚本后端不受影响，
  内置包不豁免。商城侧新增 `latestPlatforms` 与 `platform=strict|all`（`all` 只
  放宽可见性、版本轴仍生效），管理页加「只看支持当前架构」开关、卡片徽标、弹窗
  默认选最新可运行版本、平台下拉只留宿主/any。两仓门禁与本功能相关的全绿；
  真机 1633×810 + 390×844 通过（含 422 与对照包 201 安装/卸载清理）。
  **残留**：运行 409 与旧服务端回落无真机证据；Core 平台文案在用户 dev server 上
  显示 raw key（i18n 产物早于 locale 编辑，未重启其进程）；全仓 `test.sh --fast`
  两条红属另外两批未提交改动。详见
  `sessions/2026-10-26-extension-platform-gate-handoff.md` 与
  `decisions/2026-10-26-extension-executable-platform-gate.md`。

- **dev coreVersion 从 git tag 派生（2026-10-26）**：dev 构建报告它所在发行线的
  严格 semver（`3.0.16`、领先 tag 时 `3.0.16+13.gSHA`、脏树带 `.dirty`），无 tag 回落
  `1.0.0`；release 仍走 `-ldflags`、不探 git。随之把 `apps/api` 里 39 处
  `sforumVersion: ^1.0.0` 测试清单与 4 个自带包清单订正为 `>=0.1.0`（3 处真负例保留
  并注明），生产代码不留测试态分支。全量 `go test ./...` 绿。
  **运行中的 API 要重启才生效**；`wiki.harvester` 等 4 个包摘要变化，dev watcher 会
  重新 stage 并要求重新授权。dev 现有真实降级护栏，但只覆盖更早的发行线（tag）：
  切回更早 tag 会 `ErrCoreUpgradeIncompatible`，同一 tag 内的提交数/dirty 回退不受
  保护（build metadata 不参与 precedence）。详见
  `sessions/2026-10-26-dev-core-version-from-git-handoff.md` 与
  `decisions/2026-10-26-dev-core-version-from-git.md`。

- **宿主 coreVersion 暴露 + 清单兜底（2026-10-25）**：`version.Identity()`
  单一权威（`coreVersion` 严格 semver / `buildVersion` 展示），四面下发：v2 握手
  `host_build` → `Server.HostBuild()`、管理 bridge `host`（page-bootstrap 同包下发）、
  公开 L2 `host.coreVersion`、`GET /api/v1/health`。`manifest.sforumVersion` 首次真正
  求值：安装 422、启用/激活/升级/回滚/重启 409，统一 `extension.core_incompatible`；
  内置包豁免、非法 core 标签跳过并告警、预发布 core 按发行基线再求值。
  Go/Web/架构/OpenAPI/catalog/静态全绿；商城 `upstream.go` 转发重开条件见决策第 5 节。
  详见 `sessions/2026-10-25-core-version-surface-handoff.md` 与
  `decisions/2026-10-25-core-version-surface.md`。

- **扩展品牌资产（2026-10-25）**：12 个扩展 + 主仓的图标与社交卡落地，
  `scripts/brand/` 一条 spec 生成（确定输出），`sforum extension brand check`
  + 仓内 Go 测试做门禁；品牌进包内 `brand/`、不进 `packageFiles`、仍属包树摘要。
  文档：`../docs/extensions/brand-assets.md`、双语 `../docs/zh-CN/development/plugin-standard.md`。
  待人工：4 个仓库 Social preview 手动上传；8 个内置包一次摘要漂移升级。
  详见 `sessions/2026-10-25-extension-brand-assets-handoff.md`。

- **卸载物理清理自动收敛（2026-10-24）**：503 `extension.lifecycle_cleanup_failed`
  携带结构化 `data.details`（operationId/failedStep/retriable）；新增 River 周期任务
  `extensions.finalize_uninstall_cleanup`（5 分钟 + RunOnStart，仅 embed worker）自动重放
  pending tombstone；免键收尾端点 `POST /admin/extensions/:id/uninstall-cleanup/finalize`
  （super_admin，幂等）；前端卸载幂等键落 sessionStorage + 失败步骤一键重试 +
  「立即完成清理」按钮 + 列表 `pendingUninstallCleanup` 徽标。Go/Web/架构门禁/OpenAPI
  /catalog 全绿；两段式卸载保留，收敛安全全部来自既有幂等机制。详见
  `sessions/2026-10-24-uninstall-cleanup-convergence-handoff.md` 与
  `decisions/2026-10-24-uninstall-cleanup-convergence.md`。
- **应用商城 · SForum 侧（2026-10-24，2026-10-26 扩成两个宿主动作）**：P0 删除
  Marketplace 预留包（8 文件 1492 行）+ `MARKETPLACE_ED25519_*` +
  `/extensions/store*` 三页与占位货架，store UI 身份转 retired 并进墓碑账本；
  P1 给受信任管理页加宿主中介安装动作（`extension.install` 只暂存、
  `extension.install.enable` 装完由宿主启用/升级；期望 ID/版本/类型在清单解析后、
  类型权限 switch 前比对，120 秒超时 + 页面级单飞）。安装链路 Go/Web/架构/catalog/
  OpenAPI/`--fast` 全绿，`rg -i marketplace` 在 `apps/api`/`apps/web` 归零；浏览器
  QA 覆盖 1633×810 与 390×844、取消不发请求、真实公网 ZIP 下载+摘要校验+拒绝链路。
  详见 `sessions/2026-10-26-store-install-ux.md`（前序
  `sessions/2026-10-24-app-store-core-handoff.md`）。
- **内置 S3 存储插件 R2 兼容修复（2026-10-24）**：SDK 默认 CRC32 请求校验和被 R2 拒绝 + Virtual-host 寻址在 R2 上无 DNS，探测与上传必然失败；改为 checksum `when_required`、寻址三态 auto（非 AWS 端点自动 Path-style）、R2 空 region 自动 `auto`、读流不再被 60s 截断。插件 1.0.10，包校验/单测/架构门禁/fast 三线全绿；**真机 R2 已验证**：草稿探测 storage.ok（真实写/读/删往返）、实例激活为当前写入端、真实上传 201、/media 302 到 region=auto 的 R2 预签名 URL 且浏览器渲染成功、自定义域直读 200。详见
  `sessions/2026-10-24-s3-r2-compatibility-handoff.md` 与
  `decisions/2026-10-24-s3-compatible-auto-addressing.md`。
- **多图发帖 500 修复（2026-10-24）**：附件引用唯一索引收窄为槽位型，
  论坛侧写入幂等化 + 具名 422/409；两图发帖真机 201。详见
  `sessions/2026-10-24-multi-image-attachment-500-fix.md`。
- **API 连接弹窗误报治理（2026-10-23）**：失败缓冲 + `/health` 探测
  （跨 9s 宽限才弹、恢复自动关）、GET 原地重试与默认超时、chunk 失败分流
  「新版本已发布」自动刷新。详见
  `sessions/2026-10-23-api-connection-error-buffering.md`。
- **AI 回复「成功却不回复」修复（2026-10-23）**：空补全改为失败
  （`empty_completion`）且永不入缓存；缓存命中行在控制台带 `cache` 徽标与解释；
  回复结果独立成账（`ai_reply_outcomes` + `GET /admin/ai/reply-outcomes`）；
  生成器按触发幂等；新增 `sforum ai:reply --topic-id N [--confirm]`。详见
  `sessions/2026-10-23-ai-reply-silent-success-handoff.md` 与
  `decisions/2026-10-23-empty-completion-is-a-failure.md`、
  `decisions/2026-10-23-reply-outcome-ledger.md`。
- **编辑器链接续写与代码块粘贴修复（2026-10-23）**：`SForumLink` 固定
  `inclusive:false`；工具栏空选区插入 URL 链接文字；代码块内粘贴强制字面文本、
  纯代码 HTML 合并单块；`SForumCodeBlock` 多段选区合并单块。详见
  `sessions/2026-10-23-editor-link-codeblock-fix.md`。
- **AI 工具可见性单一编辑面（2026-10-23，最新）**：`purposes[id].tools.allowed`
  只在 `?tab=tools` 编辑；用途页签改为只读摘要 + 深链；未注册引用与手动点名抽到
  `SFAdminAIToolReferencesBlock`，矩阵列头提供逐用途恢复声明；前端
  `{"tools":{}}` 判定与后端对齐。详见
  `sessions/2026-10-23-ai-tool-visibility-single-owner-handoff.md` 与
  `decisions/2026-10-23-ai-tool-visibility-single-owner.md`。
- **公共信息流去掉页码（2026-10-23，最新）**：首页/搜索只保留自动加载，移除
  `<SFPagination>`、`totalPages`、`homePageTo` 与 core 的 `.sforum-home__pagination`
  隐藏规则；根因是主题 `display:flex` 覆盖 core `display:none` 让页码意外可见。
  分类/标签/评论/审核台/后台分页不变，`?page=N` 仍是深链锚点。详见
  `sessions/2026-10-23-public-feed-pager-removal.md` 与
  `decisions/2026-10-23-public-feed-loading-model.md`。
- **后台安装扩展页 + 链接安装（2026-10-23，最新）**：扩展管理新增
  `/extensions/install`，支持拖拽 ZIP 与 HTTPS 链接拉取；新增
  `POST /admin/extensions/install-from-url`（SSRF 安全、仅 https、50MB、90s、
  可选 SHA-256），安装仍为惰性制品，启用/激活/信任确认不变。详见
  `sessions/2026-10-23-admin-extension-install-page.md` 与
  `decisions/2026-10-23-admin-extension-url-install.md`。
- **全量测试提速（2026-10-23，最新）**：`./scripts/test.sh` 从 21:31 降到 7:24（冷）/
  6:14（热），`--fast` 0:41。Go 并行改成集群级分片（`SFORUM_TEST_SHARDS`，片内 `-p 1`），
  因为 `-p>1` 在单个 database 上实测四类跨包竞态；compat 农场并入 `--with-compat`；
  三线并行日志落 `tests/.logs/`；typecheck 增量。154 个包两轮冷缓存真跑零失败，
  CI 三个 gate job 待 PR 确认。详见 `sessions/2026-10-23-test-suite-speedup-handoff.md`。
- **公开 L2 恒开（2026-10-23，最新）**：`SFORUM_V3_PUBLIC_L2` 已彻底移除，公开 L2
  恒开，只有 `SFORUM_SAFE_MODE` 与 exact-artifact trust 能关闭它。改动覆盖 API config、
  `FrontendService`、装配、Compose、env 模板、生产 env 生成脚本、中英部署文档与
  governance gate 表；验证门禁改为"禁止再引入该变量"。详见
  `sessions/2026-10-23-public-l2-always-on-handoff.md` 与
  `decisions/2026-10-23-public-l2-always-on.md`。
- **严格更新入口（2026-10-23，最新）**：新增根目录 `sforum-latest.sh`，每次先解析
  GitHub 最新 Release，校验完整 `sforum-deploy.tar.gz` 后把 `deploy.sh`、`upgrade.sh`、
  compose、`deploy/scripts`、Caddy 示例与 `VERSION` 同步到该版本，再交给
  install/upgrade；本地旧工具链不参与决策。`.deployrc` 缺少有效版本记录时自动改走
  `deploy.sh --action deploy` 重建状态。交互运行可选择正式版 / 最新发布版 /
  指定标签，目标低于 `.deployrc` 记录时会在下载前拦下。文档提供一条 curl 入口
  （临时文件 + 原子替换 + 包装器 SHA-256 校验），README 与中英部署指南已同步。详见
  `sessions/2026-10-23-sforum-latest-entry.md`。

- **昵称策略（2026-10-20，最新）**：昵称改成可自助编辑的展示标签，唯一性仍只属于
  username。非唯一 + 7 天冷却（分钟精度）+ 永久历史 + 撞用户名/保留词拦截；后台
  有总开关与「曾用名公开」开关。改名同步搜索文档作者名。Go 全量、web 1496 用例、
  typecheck、架构/OpenAPI/目录门禁与两档浏览器实测通过。详见
  `sessions/2026-10-20-nickname-policy.md`、
  `decisions/2026-10-20-nickname-policy.md`。

- **富内容围栏（2026-10-20，最新）**：短代码不扩身份，改用代码围栏 + 公式定界符承载
  脑图/流程图/统计图/公式。四层落地：契约 fixture → 客户端渲染层（mermaid / Chart.js /
  KaTeX-MathML，全部动态 import + 懒加载 + 每页配额 + fail-open 源码）→ `v-highlight`
  接线（覆盖 6 个正文面，颜色模式重渲染）→ AI 宿主提示词段（`forum-reply@10`）。
  浏览器实测出图与回落；安全侧修掉 mermaid `click` 的 `<a>` 绕过站外中转，
  `themeCSS`/`themeVariables` 经 `secure` 锁死。验证：web 1483 用例、typecheck、
  架构闸门、Go 两包、docs 校验全绿。详见
  `sessions/2026-10-20-rich-content-fences-handoff.md`、
  `decisions/2026-10-20-rich-content-fences.md`。

- **AI 工具调用可见性（2026-10-19，最新）**：机器人「拿到 web-search 却一次没调」
  拆成两半修。**W1**：system 末尾由宿主渲染「本次可调用的工具」段（生成者是
  `Orchestrator.ToolSection`，与 `Run` 同一套闸门，因此「提示词宣称有」= 「请求真的
  带上」），默认提示词补「站外事实」段，`forum-reply@9`。**W2**：`ToolTrace` 落新表
  `ai_tool_calls`（`UNIQUE (comment_id, seq)`，`seq` 是 trace 下标——并行调用共享
  `step`），公开投影增量挂在 `GET /comments/:id/citations` 的 `toolCalls` 字段
  （不新增路由），过滤四种「撞配置」结局、**永不投影 `result_digest`**；前端
  `SFCommentAIActivity.vue` 一个折叠块两个分区。
  **已真实端到端验证**（2026-10-20 00:23，主题 1236 评论 5015）：run `105b53c7…`
  3 次模型调用、`ai_tool_calls` 4 行（`web-search` ×3 + `fetch-url`，其中 1 条
  `step_exhausted` 被公开面过滤），页面「工具调用（3）」；模型在两次检索失败后
  **如实说无法核实版本号**，没有编造。排查结论记在 handoff：`agent2api` 曾有两个
  独立故障（收了 tools 不回 `tool_calls`；上游 token 过期 502），与本仓库无关；
  eval 基线已按 `@9` 重生成。
  详见 `sessions/2026-10-19-ai-tool-call-visibility.md`、
  `decisions/2026-10-19-ai-tool-call-visibility-and-prompt-tool-section.md`。
- **编辑器表格（2026-10-19，最新）**：`SFEditor` 现在能画、能渲染、能存表格。
  修复的真实缺陷是"编辑含 GFM 表格的帖子即整表丢失"——`@tiptap/markdown` 对无
  handler 的 `table` token 返回 null，而渲染端早就支持表格。四层同时打通：
  ① Web 用 `@tiptap/extension-table@3.31.3`（full 预设）+ 6×6 插入网格 +
  BubbleMenu 上下文菜单；② Host `CoreSchema` 增 `table/tableRow/tableHeader/
  tableCell`（attrs 仅 `colspan/rowspan/align`）并做结构修复（非法形状修复 + fallback，
  不整篇 400）；③ 渲染输出 `<thead>/<tbody>` 无 style/colgroup，Markdown 投影 GFM
  管道表；④ goldmark `*ast.Table` 兼容解析 + 保护块放行表格。对齐统一走 `align`
  属性（两端净化器都剥 style），legacy markdown 路径也切到 `align`。
  v1 不做合并单元格与列宽持久化（GFM 无法表达，草稿 Markdown 往返会静默损坏）。
  验证：Go 两包 + `./app/Support/...`、web 1349 用例、typecheck、架构闸门全绿；
  浏览器实测插入/对齐/预览/发布/重进编辑往返，1633 与 390 宽均无横向溢出。
  详见 `sessions/2026-10-19-editor-tables.md`、`decisions/2026-10-19-editor-tables.md`。
- **公开三栏独立滚动（2026-10-19，最新）**：桌面三栏壳的左/中/右列各自
  `position: sticky` + `height: calc(100vh - topbar)` + `overflow-y: auto` +
  `overscroll-behavior: contain`，滚动一栏不再带动另外两栏；document 在
  中列持页脚的页面彻底不可滚。左右栏 `scrollbar-width: none` +
  `::-webkit-scrollbar { display: none }` 隐藏滚动条（滚动能力保留），列宽与
  不溢出的栏一致；中列保留可见滚动条。三处几何（`sforum-theme.css` 兼容块与
  Host 壳、五个页面级 CSS、主题包 `hybrid-forum.css`）同值，并删除把左右栏
  打回 `static/visible` 的反向规则。改前实测 document 1545/810、左右栏 1487px
  `overflow: visible`；改后三列 752px 独立滚动、document 810。主题包已
  `extension build` 并 stage（最新 `16562`，active `16540`）。
  详见 `sessions/2026-10-19-three-column-independent-scroll-handoff.md`。
- **生产部署文档写透（2026-10-19）**：把中英文生产部署手册拆成「入口页 +
  4 个任务页」（一键安装与更新、手动 Compose、环境变量与密钥、故障排查），补全
  bootstrap/deploy.sh/upgrade.sh 执行链、`.deployrc` 状态、手动更新/回滚、
  env 契约与 `recovery_required` 处置。顺手修复文档与实现的两处偏差：
  `compose.yaml` 现在真正透传 `TRUST_PROXY*` 与 `SFORUM_SAFE_MODE`；pprof/
  `GOMEMLIMIT` 明确为 override-only。`validate-docs` 新增部署文档门禁（页面齐全、
  env key 覆盖、Compose 透传、操作命令标记、旧计数防回归）。验证：
  `validate-docs`、`validate-docs_test`、compose/deploy/upgrade/release-assets
  测试、`contracts-gate`（374 routes / 330 UI surfaces / 99 traceability rows）
  与架构闸门全绿；全量 `./scripts/test.sh` exit 0。详见
  `sessions/2026-10-19-deployment-docs-depth.md`。
- **错误页 i18n 崩溃修复（2026-10-19）**：`error.vue` 报
  `Need to install with 'app.use' function` 是二次故障而非根因——`@nuxtjs/i18n`
  把 `vueApp.use(i18n)` 放在插件 setup 最后一步，提前失败就会留下"没有 i18n 的
  app"，而错误页与 `app/components/errors/` 下 8 个组件全部无条件依赖
  `useI18n()`，于是错误页自身崩溃并掩盖原始错误。触发环境是 dev server 运行中
  被 `bun install` 换掉了 `node_modules`（`.pnpm` 与 `.bun` 双树并存）。修法：
  新增 `useErrorPageShell()` 双条件就绪判定（`$i18n` + `$localeHead`）＋零依赖
  兜底页 `SFSystemErrorBootstrapFallback`，主题错误页与 SEO/head 全部纳入
  `i18nReady` 门控；同时删除误提交的 `apps/web/pnpm-lock.yaml` /
  `pnpm-workspace.yaml`。详见
  `sessions/2026-10-19-error-page-i18n-resilience.md`、
  `decisions/2026-10-19-error-page-must-not-depend-on-plugins.md`。
- **Code Scanning #43/#44 修复（2026-10-19）**：两条 open 告警同属
  `js/incomplete-multi-character-sanitization`，都由提交 `1da2b5da0` 引入——同一提交
  用「剥离到不动点」修好了 #41、抽出 h1 标题函数修好了 #42，却把单次正则写法留在了
  抽出的新函数（#43）和测试辅助函数（#44）里。修法：#43 改成逐字符线性扫描
  （`stripMarkupToText`，任意中间值不含标签边界、无 O(n²) 迭代面）；#44 抽成
  `tests/helpers/sourceText.ts` 的 `stripHtmlComments`，迭代到不动点且**用空格替换**
  （空串删除会拼出 `v-`+`html` 这类被断言禁止的 token）。验证：web 1331 用例全绿、
  typecheck 0 错、架构门禁通过、40006 例随机/对抗输入零尖括号泄漏。详见
  `sessions/2026-10-19-code-scanning-43-44-remediation.md`。
- **AI 观测层与能力层（2026-10-19）**：让 AI 工具循环对运营者第一次可见，并在这个基础上扩容能力。A 批：`ToolTraceEntry`（参数脱敏 + 结果摘要 + 五种失败结局，工具明细只进日志不进 `ai_executions`）、run 级关联 id（`RunResult.RunID` 复合字面量一次写入，7 处 `g.record` 全透传，迁移 001）、`GET /admin/ai/runs/:runId` 回放（服务层判 `ai.manage`，缓存命中单条记录必须标注）、`CompletionOutput` 四项透出 + 结构化 `tool_trace` 日志。G：`sforum ai:eval` 基线门禁（10 条 fixture、真实装配、只跑不发布、`--diff`/`--only`、成本保护；两条纪律：`TriggerUserID=0` 防配额自噬、带 `error` 的快照必须补跑）。B 批：预算默认步数 3→5、三个新只读工具（`forum-comment-thread` / `forum-attachment-read` / `ai-self-history`，提示词版本 `forum-reply@8`）、两条派生用途 （`forum.digest`/`forum.tour`，落新表 `ai_derived_artifacts`，只 CLI 手动触发）、证据链（`[[cite:]]` 解析隔离 + 逐条校验 + 发布后落 `ai_citations` + 公开读端点）、会话状态（`ai_topic_state`，三重失效 + 四条防丢失）。验证：L1/L2/L4/L5 绿、L3 仍为 9 条既有告警、L6/L7 有运行时与浏览器证据。详见 `sessions/2026-10-19-ai-observability-and-capabilities.md`、`plans/archive/2026-10/2026-10-19-ai-observability-and-capabilities.md`（含 D1–D24 偏离登记）、`modules/ai.md`。
- **扩展设置保存提速（2026-10-19）**：保存设置不是纯写库（实测 `put_ms=5`），耗时全在「重启插件」；80% 以上的 CPU 花在对同一份不可变扩展包反复做整树 SHA-256。根因是 `bootstrap` 里每 5 秒一轮的 `GuardPolicyCatalog.RunRefresh`（16 个 uploaded 扩展 × 2 次可信身份 = 约 32 次整树哈希 / 5 秒，独占 27.39 s / 40 s 窗口）。修复为包校验指纹记忆化（`app/Support/ExtensionPackage/verification_cache.go`，只读元数据、共用同一文件集合、TTL 5 分钟、dev 构建显式失效）＋同一 gate 内共享 `ArtifactLoader` 消除重复 registry 校验。实测单次保存 **24.5–39 s → ≈3.0 s**，后台循环 **27.39 s → 0.18 s**。详见 `sessions/2026-10-19-extension-settings-save-latency.md`、`decisions/2026-10-19-package-verification-memoization.md`。
- **控制台 AI 保存 422 与上限放宽（2026-10-19）**：定位到六条触发路径（`v-model.number` 清空产生空串、闸门/预算无上界、供应商「非空但不合法」、自动裁决空动作、数值入口分散、错误不可诊断）。修复方向是「前端永不产出非法形状 + 服务端说清哪个字段」：新增 `Settings.Issues()` 与字段级 422（`ai.settings_malformed` / `ai.settings_invalid` + `data.fields`），控制台改走 `coerceInteger`/`profileIssue` 并在保存前拦截；同时把 `Support/AI` 天花板按模型能力重定标（输出 128k、输入总预算 12 MiB、工具步数 12、月度预算 1000 万元，默认值不变），经 `limits` 下发前端；同步 SDK 镜像常量、gRPC 16 MiB 与插件 `GOMEMLIMIT` 96 MiB。详见 `sessions/2026-10-19-admin-ai-save-422-and-limits.md`、`decisions/2026-10-19-ai-context-and-budget-ceilings.md`。
- **dev 制品自愈 + lifecycle 终态一致（2026-10-19）**：stage 幂等改为「记录路径 == 本次 DigestTree 校验过的快照根」，`ensureExtensionVersion` 冲突时只修 `package_path`，删目录/换根不再永久 missing；dev auto 在自动恢复前先判冻结制品可装载，并按摘要 + attempt 上限（3）放弃，不再把 operation 推到 attempt=98；lifecycle 汇总新增 `closed`（`completed_at` 唯一判据），CLI already-closed 报错带 `state/terminal/completedAt`。详见 `sessions/2026-10-19-dev-artifact-self-heal.md`。
- **站点时区保存失败修复（2026-10-18）**：容器镜像（alpine，无 `/usr/share/zoneinfo`，`CGO_ENABLED=0`，未内嵌 `time/tzdata`）里 `time.LoadLocation` 拒绝所有非 UTC 时区，后台改时区保存返回 422 `options.invalid`（发布镜像 v3.0.9 内已复现；本机 dev 正常）。新增 `app/Support/Timezone`（内嵌 tzdata + `Load`），替换 options / ExtensionManifest / Jobs 三处调用；`tests/validate-runtime-tzdata.mjs` 接入 `scripts/test.sh` 做回归门禁。修复后 CGO off 探针在 alpine:3.22 内解析非 UTC 时区全部成功。详见 `sessions/2026-10-18-site-timezone-save-fix.md`、`decisions/2026-10-18-embed-tzdata.md`。
- **同意式 email 读取面（2026-10-18）**：新增高风险能力 `users.email.read` 与
  `sforum.core.identity.self_email@1`（结果 schema `sforum.core.self_email@1`，
  字段 `email`/`email_verified`/`updated_at`）。定义只声明一次，执行出口是它的
  显式 Query Registry 投影 `core.query.identity.self_email`：宿主只在
  route/admin 调用、有活 actor、精确制品声明能力时签发单次 query 委派
  （`scope="route"`、`MaxCost=50`、`RequestContext.HostQueryDelegations`），
  actorless 稳定入口对 `ConsentGated` 定义一律拒绝。执行侧 capability +
  60/min/(actor,extension) 限流 + 每次成功读取一条
  `identity.email.consent.read` 审计（无值）。`sforum_core_v1` 与公开查询零改动。
  参考 fixture `sforum-consented-claim-reference` 通过参考包门禁 13/13。
  详见 `sessions/2026-10-18-consented-email-surface-handoff.md`、
  `decisions/2026-10-18-consented-email-surface.md`。
- **扩展复用 Core 附件（2026-10-18）**：计划 `2026-10-18-core-attachment-extension-api.md` 的 Core 侧落地——三条 Host Command（`sforum.attachments.extension.claim|release|list@1`）、公开读谓词拆分（匿名可读 vs 永久 URL）、卸载自动释放、`attachment.extension_reference_limit`（默认 500）。实现落在新包 `app/Models/Attachments/ExtensionReference`（扩展级 advisory 锁 + 按引用行重算计数，10 个真实 PostgreSQL 集成用例含并发不变式）。Go 全量测试通过；架构边界与改动前同样的 8 条既有失败（唯一新增越线是 `Models/Extensions/service.go` 1001→1003 行）。插件侧接线在 `sforum-oauth2-provider` 仓库，未在本仓库内。详见 `sessions/2026-10-18-core-attachment-extension-api-handoff.md`。
- **dev watcher 自触发重建循环与 lifecycle 恢复修复（2026-10-18）**：`BuildBackend`/`WriteDigest` 幂等写守卫（内容不变不重写）终结 ~15 秒稳态重建循环；`recoverLatestFailedUpgrade` 补认 `cancelled` 终态（与 coordinator/recovery 语义对齐）；外部 `sforum-fetch-url` 补 lifecycle V2 声明 + no-op `runLifecycle`（dev-auto 自动 promote 生效）；删除损坏的 `sforum.marketplace` dev 脚手架。详见 `sessions/2026-10-18-dev-watcher-rebuild-loop-handoff.md`。
- **插件数据面 E10 全量交付（2026-10-18，两段）**：第一段 = 政策 ADR + 五视图 + 稳定查询 4→9 + `int64_list` RPC 通道 + **授权解析器 V3 grants 修复**；第二段 = 管理端查询目录页 `/control-panel/extensions/query-catalog`（后端窄边界 + Nuxt 页 + 四个审核矩阵 + identities/生成目录 + OpenAPI + 计数 tripwire 372）+ 消费型参考 fixture `sforum-query-surface-reference`（`core_views` 授权、三查询聚合、参考包门禁 12/12）。Go 全量零失败；Web 1238/1239（唯一失败为 HEAD 既有的 CSP 文档策略测试，与本变更无关）。剩余：fixture 的 dev 链浏览器证据（操作者侧）。详见 `sessions/2026-10-18-plugin-data-surface-e10-handoff.md`。
- **插件数据面 E10（2026-10-18）**：数据面政策 ADR（view/query/command/event 四条路 + surface request + 版本化）落定并写入 AGENTS.md 同变更交付规则；迁移 `202610180001` 新增 `site_stats`/`public_categories`/`safe_user_avatars` 稳定视图（已应用 dev 库并抽查）；稳定查询目录 4→9（站点统计、公开评论列表、按用户名查用户、批量作者+头像、公开分类），`int64_list` 过滤接入 RPC 通道（规范 JSON 数组 ≤100）；**修复查询授权解析器只认 legacy `database.authority` 的缺口**（V3 grants 清单此前永远过不了解析器）。authoring-guide 新增稳定查询清单。剩余：管理端查询目录页 + 消费型参考 fixture。详见 `sessions/2026-10-18-plugin-data-surface-e10-handoff.md`、`decisions/2026-10-18-extension-data-surface-policy.md`、`reports/2026-10-18-host-query-inventory.md`。
- **部署脚本交互与健壮性（2026-10-17）**：`upgrade.sh`/`sforum-bootstrap.sh`
  交互模式新增发布渠道询问（stable 推荐 / 含 alpha/beta 预发布，仅本次生效）；
  GitHub 查询与 docker pull 带重试；`.deploy.lock` 锁自愈；目标迁移无法在线
  安全执行时交互确认后原地降级为停机升级（`--confirm-maintenance` 可无人值守
  授权）。详见
  `knowledge/sessions/2026-10-17-deploy-scripts-ux-robustness-handoff.md`。
- **dev 零命令自动激活（2026-10-17）**。操作者拍板 B 方案：watcher 挪进 host dev
  装配，彻底 0 命令——`./scripts/dev-plugin-loop.sh` 已删除，登录提示/凭据缓存
  代码全部移除；host 进程内 fsnotify 监听 EXTERNAL_EXTENSION_ROOTS，保存即
  go build → digest（SRI 重算）→ stage → 逐摘要 grant → 启用/升级，插件子进程
  自动重启；0 管理员空库可用（合成无凭据 `dev-auto` 系统账号作审计归因，
  禁用/删除即关闭通道）；生产与 Safe Mode 不装配（fail closed），HTTP 激活路由
  super_admin 门原样。实机证据：dev 库自动出现 dev-auto 用户（id 157）、
  oauth2-provider/marketplace 自动 upgraded、touch 源文件 6 秒内自动重建
  （digest 7cc3d26b…）。详见 `sessions/2026-10-17-dev-live-activation-chain-handoff.md`
  与 `decisions/2026-10-17-dev-zero-command.md`。


- **插件子进程内存上限（2026-10-17）**。Protocol V2 插件子进程固定注入
  `GOGC=50` + `GOMEMLIMIT=48MiB` 软上限，已部署实例零配置生效；
  可选覆盖键 `SFORUM_PLUGIN_GOMEMLIMIT`。env 构建段自 `protocol.go` 抽至
  `plugin_command_transport.go`，基线下调 1011→941。实测 19 个 dev 插件
  PSS 平均 ~15.2MiB（较 08-20 基线再降约 20%），Extensions 全套测试绿。
  详见 `sessions/2026-10-17-plugin-process-memory-limits-handoff.md`、
  `decisions/2026-10-17-plugin-process-memory-limits.md`。

- **账号设置侧栏整合：账号安全三页合并（2026-10-17）**。登录方式 / 登录密码 /
  账号安全收进单一「账号安全」侧栏入口与 `/settings/security` 子路由
  （index = 设备与会话默认 tab / login-methods / password），共享
  `SFSettingsSecurityTabNav` 路由 tab；三个 Page Registry 页面 ID 与主题模板绑定
  零变更（默认主题制品无需重激活），Go 只改 catalog 两行 PathPattern +
  external_auth 两处回调 redirect；旧路径 Nitro 302 兼容；identity 台账显式记录
  移动并登记新 tab 组件。focused Go 与静态门禁全绿；桌面与 390x844 渲染 QA 均已通过
  （含旧路径 302 与两个 QA 过程缺陷修复）。详见
  `sessions/2026-10-17-account-security-consolidation-handoff.md`。

- **dev 循环「保存即生效」激活链（2026-10-17）**。外部插件仓库
  （`EXTERNAL_EXTENSION_ROOTS`，如 `sforum-oauth2-provider`）不再需要拷进主程序目录：
  CLI dev 循环/dev-trust create 接受外部根；`GrantDevArtifact` 授权前先重扫外部源，
  新摘要无需重启 API 即暂存；`--auto-upgrade`（隐含 --auto-trust）在授权后自动走既有
  upgrade 路由——保存 → 构建/摘要/校验/测试 → 暂存 → 信任 → 升级全自动，插件子进程
  按新摘要重启。`digest --write` 顺带重算已声明资产的 SRI。同时修复 OpenAPI
  `{extensionID}` vs Go 目录 `:id` 的命名漂移（docs 端点校验 HEAD 既有红）
  与 dev-trust 文档端点口径。详见
  `sessions/2026-10-17-dev-live-activation-chain-handoff.md`、
  `decisions/2026-10-17-dev-live-activation-chain.md`。

- **路由注册表 Route Catalog（2026-10-17）**。新只读管理页
  `/control-panel/extensions/route-catalog`：按提供者来源分组展示 Route
  Registry 快照全量路由（实测 397 条 / 核心 371 + 3 制品组）+ 每条运行时准入
  状态（`routeAdmitted`）+ 冲突标记，补上「manifest 声明 vs registry 生效」
  的观测缺口。`routes.Catalog` 与 inspector 共享同一 registry；新路由进入
  guard 审核矩阵（`core.guard.extensions.read` 门）+ identities + OpenAPI，
  计数 tripwire 370→371。桌面/390/暗色 QA 通过。详见
  `sessions/2026-10-17-route-catalog-handoff.md`。

- **扩展版本垃圾回收 Version GC（2026-10-17）**。`sforum extension gc`
  交付：默认 dry-run（判据清单 + 保留/阻塞原因 + 一次性 confirm-token，绑定
  精确计划摘要，15 分钟 TTL）+ `--confirm-token` 执行（先删盘后删台账、锁内
  复核、`extension.version_gc` 审计 + `extension_version_gc_removals` 锚点、
  幂等）。**管理端入口同日交付**：`GET version-gc/candidates` +
  `POST confirmations/execute`（读守卫粗门 / super_admin 守卫 + 服务层权威
  super_admin 边界）+ `SFAdminVersionGcDialog.vue` 三阶段对话框（计划→确认→
  结果）+ OpenAPI + 四张审核矩阵；桌面与 390x844 渲染 QA 通过。**运行时冲突
  修正**：发布账本是 append-only（`202607160027` 触发器拒绝一切删除），任务书
  方案 A 的"删除过期发布修订"废止——被发布历史引用的版本行永久钉住
  （`publication_history`），曾启用的插件版本永不回收；实际回收面 = 主题
  历史版本 + 从未进入发布的插件版本。实机 dev 库 dry-run：225 历史行 →
  139 可回收 / 63 发布钉住 / 13 窗口内 / 10 盘缺。架构腾位：
  `Models/Extensions` flat 96→92（六次同族合并），基线 94→92。任务书
  completed 并归档 `plans/archive/2026-10/`。决策：
  `decisions/2026-10-17-version-gc-publication-restrict-cooperation.md`。
  详见 `sessions/2026-10-17-extension-version-gc-handoff.md`。

- **孤儿 lifecycle operation 自动回收 + 带外关闭 CLI（2026-10-17）**。op 115 卡死
  API 全接口 503 的事故闭环：仓库层 `ReclaimOrphanedLifecycleOperations`
  （fail-closed：只回收所有 step lease 均已过期的开放操作，CAS 关闭到
  `cancelled` + 审计）、bootstrap genesis 重试钩子、coordinator fence pending
  先回收再判 pending、带外 `sforum extension recovery lifecycle-close`。
  实测端到端：种入模拟孤儿 op 238 后，live coordinator 一个 poll 窗口内自动
  回收、ready 恒 true；事故 op 115 已由手工 SQL 关闭并重启恢复。架构腾位：
  `Support/StableFile` 包抽出、`Models/Extensions` flat 基线 95→94。任务书
  completed 并归档 `plans/archive/2026-10/`。详见
  `sessions/2026-10-17-lifecycle-orphan-reclaim.md`。

- **main CI 门禁修复：fixture 二进制摘要 + catch-all 静态契约（2026-10-17）**。`6ab053f42` 推送后 CI 双红：① catch-all 断言仍绑在单文件，适配到 `useRegistryPageResolve` + `SFRegistryPageBody/Shell` 拼接校验；② 参考插件 fixture 的 `backend/plugin` 摘要不可复现，新增 `app/Support/ExtensionPackage/referencefixture.StageBackendFixture`（暂存副本现场构建+摘要对齐）；③ `privacy:erase-user` 用法契约测试固定 `DATABASE_URL`；④ 重生成 `public-page-surfaces` 目录并把 validator 计数 tripwire bump 到 routes 363 / UI 321；⑤ 清掉意外入库的 55MB `apps/api/sforum` 二进制并 ignore。详见 `sessions/2026-10-17-ci-gate-repair-fixture-serif.md`。

- **公开列表壳窄屏画布修复：移动端首页背景与 PC 不一致（2026-10-17）**。窄屏（≤980px）首页/分类/标签/搜索列表落在 `--sf-public-bg` 画布色上，而 PC（≥981px）是 `--sf-public-surface`——原因是 `assets/css/sforum-home.css` 的 `.sforum-home__layout` 表面色被 `@media (min-width: 981px)` 独占，透明话题行（G1 白底扁平列表）失去承托。已把该声明上提到基规则（所有宽度生效，深色/各 appearance 预设随 token 自动对齐），桌面逐像素不变，主题不可变制品无需重打包。详见 `sessions/2026-10-17-public-shell-narrow-canvas-handoff.md`。

- **row 7 关闭、工作流整体归档（2026-10-17，更正同日 Revision 5）**。在真两节点拓扑（同一宿主的两个 API 容器，镜像为**已发布的 `v3.0.9`**，共享一次性 PostgreSQL+Redis）上跑完 row 7 的逐探针验收：双节点注册（45s 租约/10s 心跳）、同不可变修订的双侧 `applied` ack、管理端在 A 上的 disable/enable 在 5s 轮询窗口内于 B 收敛（B 的 web-push 子进程消失/以**逐字节相同制品路径**复活）、节点 stop→租约 45s 内失效且 A 不受影响、start→新 boot_id 立即 ack 最近修订且运行时复活。两条**真冷启动运维发现**（同时首启竞态：①首迁移互死锁：A 的 idle 连接持有 campaign goose 会话锁+CIC 等 B 的先开事务；②builtins 同步顺序竞态：`smtp not synchronized`）已在 runbook 写下缓解（顺序启动/先 migrate/`MIGRATE_ON_STARTUP=false`/勿漏 BUILTIN_EXTENSION_ROOT）。矩阵在 Revision 5 里宣布**十行全部取证、工作流关闭**，主计划已移入 `plans/archive/2026-08/`。细节：`sessions/2026-10-17-extension-authoring-closeout-handoff.md`（同日补记 row 7 节）、矩阵 Revision 5。

- **扩展授权计划收尾与闭环交付（2026-10-16，第二轮评分）**：M10 矩阵按现有证据**重新评分（Revision 2）**——行 **1/2/3/4/6/8/9/10 已取证**，只剩**行 5（插件 session state：仓库不存在会话契约，需人工决定后再做 M6）**与**行 7（真多节点：需运维提供第二节点）**；更正表逐条写明每个行"为什么改口"（行 1 按 **R5** 改为「现有内置主题」；行 4 按 **R4** 删掉不存在的 `challenge`；**行 9 是更正——发布镜像 smoke 早已跑过**，上一轮误报为「需外部条件」；行 2 由 **R2** 解锁，活跃实例 `resolve-path` 已返回 `fallback=false` + 插件类型化 `loaderData` + 已绑定运行实例，主题覆盖半由真实 HTTP 测试 `TestResolvePathUsesActiveThemeOverrideForPluginBusinessPage` 覆盖，含 `themeOverrideKey` 漂移时 soft-skip 回插件模板且业务数据不变）。**修掉一个验收支架缺陷**：`tests/browser-public-390x844.mjs` 的"稳定"判据（`body.innerText.length` 连续两次不变）在**水合之前就成立**，于是 ~1 982 ms 就退出测量，而 `view: component` 页面的客户端挂载在 **~5 427 ms**（同一载体/页面/宽度），描述符请求也在那时才发出——因此上一轮记录的"headless 下组件不挂载、连描述符请求都没有"是**测量假象**，不是客户端缺口（也解释了为何把 settle 提到 25s 无效：退出条件是「2 次稳定」而非预算）。支架改为：等真实水合（`#__nuxt.__vue_app__`）→ 对 `SFORUM_L2_PATHS` 要求 L2 终态（`mounted|fallback|quarantined`，绝不在 `idle|loading` 时判稳定）→ 可显式断言挂载，水合失败即 FAIL；负控制（`SFORUM_L2_PATHS=/probe-site`）实测确实 FAIL。**由此关闭相邻任务书 M6 最后一项并交付 T7**：组件页在真实 Chrome 390x844 实测 `l2=mounted/kids=1 trustNotice=true`、header/footer 各一、无溢出/裁切/控制台错误，同包 `view: template` 对照页仍 `l2=null`。**主计划 M6 记录为人工决定项**（Manifest V3 的 `session` 只存在于 `identityProviders[].kind/operations/sessionPolicy` 这一宿主调用的身份提供方策略钩子；Protocol V2 的 `Actor.session_id` 被投影拒绝写入；Host API v2 无会话状态服务），**M9 改为「除该项外完成」**且不造假夹具（新增会话契约属契约变更，须先留 ADR 修订）。本轮复跑命令与输出见
  `sessions/2026-10-16-extension-authoring-closeout-handoff.md`。**T6 收尾（操作者已拍板）**：保留两个证据支架与 `acme.iconprobe`，台账行留待运维走「缺失制品清理」。同轮**更正任务说明的两处事实**：①「卡住的那条 DB 行现已不存在」不成立——实测 `sforum.page-loader-reference` 行仍在（`disabled`、1.0.0、`active_version_id=7267` 指向 digest `c17221c0…`，磁盘无该目录，这才是真正缺的那份）；②`storage/extensions/sforum.page-loader-reference/1.0.0/{be7d08e4…,e8f86047…}` 与 `1.0.1/90661e93…` **不是**「无害残留」，而是台账版本行 7194／7349／7434 的磁盘制品，因此**未删除**（批准所依据的前提被数据库推翻，删除不可逆，留一轮不损失任何选项）。另：本轮全量 `go test ./...` 期间 air 管理的 dev API 停止服务（非插件启动失败），已用 `./scripts/api-dev.sh` 重启（564 handlers），重启后活动主题摘要与四条证据页解析全部不变。**环境状态（未处理，如实记录）**：操作者自启的 web dev server（`:3000`）在本轮末尾 SSR worker OOM（`Worker terminated due to reaching memory limit: JS heap out of memory`）并持续 500，已按仓库规则**未杀未重启**；本轮所有运行时证据都是在它健康时取得的（最后一次全绿的重跑就在 API 重启之后），OOM 只影响一次可选的额外复跑，建议以更大堆重启（仓库生产构建已用 `NODE_OPTIONS=--max-old-space-size=6144`）。

- 相邻任务书 **M9 已闭环**（2026-10-16）：五项逐条落实——`--public-page`（主计划 M3 交付，本会话核验其 3 个测试）、`--schema-page`（本会话交付：四种 kind、提前拒绝、`extension validate` OK / `extension test` 0 errors）、`extension dev`（主计划 M8 早已交付，本会话核验 `dev_extension.go` + 12 个测试）、公开页运行时面目录（本会话交付：生成物 + 新校验接入 `scripts/ci/contracts-gate.sh`）、双语文档（`docs/zh-CN|en-US/development/cli.md` 覆盖全部四个 flag，迁移文档成对存在）。**归因如实**：五项中两项非本任务书所建，只做核验；本会话实建三项。相邻任务书仅剩 **M8**（委派给导航计划 M7）。
- 相邻任务书 M9 的**公开页运行时面目录**已交付（2026-10-16）：新增生成物 `docs/extensions/v3/catalogs/public-page-surfaces.{json,md}`（由 `scripts/v3-catalog/generate.mjs` 从运行时同一批手写来源生成：`Pages.coreCatalog`、`RegionCatalog.pageRegionMatrix`、`PageView`/`PageLayout` 词表、`allowedHostIslands`），实测 **36 页 / 12 区域 / 3 视图（template|component|schema）/ 2 layout（site|bare）/ 41 宿主岛**；新增校验 `tests/validate-public-page-surface-catalog.mjs`（钉住词表、页面集合与 Go 目录双向一致、区域存在、受审查正文岛在册、markdown 覆盖词表、目录新鲜度），已接入 `scripts/ci/contracts-gate.sh`；文档：`docs/extensions/v3/README.md` 与 authoring-guide 的 “Where to look before you declare a page”。M9 仅剩 `extension dev` 未开始。
- 相邻任务书 M9 的 `--schema-page` 已交付（2026-10-16）：`make:plugin`/`make:theme` 新增 `--schema-page <slug>` 与 `--schema-page-kind list|detail|form|notice`（默认 list），生成 `schemas/page-<slug>.json`（有界描述符）+ `schemas/page-<slug>-contract.json`，合并 `theme.json` 的 `view: schema` 声明，并在 `packageFiles[]` 以 **`kind: schema` + `version: "1"`** 声明两者（版本是 `<schemaFileId>@1` 精确引用可解析的前提）。提前拒绝：未知 kind、kind 缺 slug、非法 slug、与 `--public-page` 同 slug。实测：新脚手架直接 `extension validate` → OK、`extension test` → 0 errors、`PreflightExactTemplateRuntime` 对四种 kind 全通过（无需手改）；测试 `TestSchemaPageScaffoldPassesSharedPreflight`(4 kind)/`DeclaresBoundedDescriptorAndContract`/`RejectsUnsafeFlags`。双语文档已更新（`docs/zh-CN|en-US/development/cli.md` + authoring-guide 的 “What `--schema-page` generates”）。M9 其余项（`extension dev`、运行时 surface catalog）仍未开始。
- 相邻任务书 M7（`view: schema`）阶段 1 完成（2026-10-16）：`theme.json` 新增 `view: schema` 声明（`schema.descriptor` = 包内 `kind: schema` 描述符文件，`schema.contract` = 必须满足的精确 schema 引用）；结构校验在 `app/Support/Pages/theme_package.go`（与 template/component 互斥、未知 view fail closed），描述符/契约/有界规则校验在 `apps/api/sdk/plugin/schema_view.go`，经 `PreflightExactTemplateRuntime` 进入——**CLI（extension validate/test）与运行时激活同一入口**，不会重演「CLI 0 error、激活被拒」。有界规则：`kind` ∈ list|detail|form|notice、五种 tone、五种字段类型、24 字段/64 条目/64 选项/64 cells 上限。CLI 证据：合法包 `OK /tmp/svpkg`；把描述符改成 `{"kind":"dashboard"}` 后 `Error: invalid package … schema descriptor kind "dashboard" is not one of list|detail|form|notice`，`extension test` 报 1 error。**阶段 2 已完成**：`resolveResponse.schemaView` 透传描述符（装载实现唯一，绑定路径与 add 路径共用；读取失败回落到空状态）；两条解析路径的「无模板即运行时不可用」回落改为 `pageBodyProvidedByView`/`responseBodyProvidedByView`（同时覆盖 component 与 schema）；公开渲染器 `SFPublicSchemaView.vue` 渲染 list/detail/notice，form 只读并标 `data-schema-form-pending`（提交依赖仍被阻塞的 dataRoute 绑定，不伪造提交），测试禁止出现管理端标识（`commandSurfaceId`/`invokable`/`visibleResourceIds`/`SFAdminSurfaceOutlet`）；catalog identity 登记 `core.component.shared.sf_public_schema_view`（owners=public）；fixture `sforum-page-schema-reference`（无代码包）。**浏览器证据**：`/page-schema-reference/list` → shell=site/theme、可见 navbar 1 + footer 1、`data-schema-kind=list`、标题/条目/cells 均由描述符渲染、无溢出、0 裁切。声明命名（`schema.descriptor`/`schema.contract`）属**待确认解释**，已在计划正文记录。
- 相邻任务书 M6 阶段 3 取证（2026-10-16）：新增 fixture `extensions/fixtures/plugins/sforum-page-component-reference`（一个 `view: component` 公开页 + 同包模板页对照），
  digest/validate/test 全绿；**真实 HTTP 证据**：装入 dev 并启用后 `resolve-path` 返回
  `{"view":"component","component":"…component.body","fallback":false,"layout":"site"}`，且启用时**要求精确制品信任挑战**（该包无 Go 后端，但声明了公开 L2 浏览器代码，
  属可执行面，门禁正确）。**仍开放**：正文岛调用的描述符端点返回 `404 extension.frontend_trust_not_found`，而管理端前端信任流程
  （`POST …/frontend/confirmation` → `…/frontend/trust`）按构造只服务 `admin.entry`（`primaryAdminComponent`），因此无法为「只有公开 L2 组件」的包授权——
  **跟进更正**：那个 404 码是 `mapPublicFrontendError` 对 **`ErrPublicFrontendUnavailable`** 的折叠（`public_frontend.go:163-167`，刻意不向公开调用方区分 disabled/revoked/stale/changed/undeclared），
  因此它**不能证明**缺少前端信任授予；我上一轮把管理端前端信任流程当疑似门槛的措辞未被证实（且 `Challenge`/`Grant` 都要求 `primaryAdminComponent`，即 `admin.entry`，按构造与管理端专属）。
  **根因已定位（三次运行时检视）**：① dev 进程环境里 `SFORUM_V3_TRUST_CHALLENGES=true`、`SFORUM_V3_PUBLIC_L2=true`，`component-inspector` 报 `safeMode:false` → 不是开关或 Safe Mode；
  ② `asset-inspector` 在 fixture 启用期间显示**资产发布物确实创建了**，handle 正是 `…component.body.l2.entry`（type script）→ 不是发布物缺失；
  ③ 缺口在**组件注册表**：解析响应 `selectedRuntimeInstanceId: null`（该包无后端 → 无运行时实例），`component-inspector` 的 `contributionCount: 1` 且**没有提及该包的 trace**，
  而 `ComponentRegistry.AdmitPublicComponent`（component_registry.go:439-465）同时要求按实例 id 匹配的 `registrations[extension.ID]` 与已发布的 `contributionsByID[component.ID]`，
  否则返回 `ErrPublicFrontendUnavailable`（被公开处理器折叠为 404）。因此**目前无运行时实例（无可执行后端）的包无法让 `view: component` 公开页被准入**；
  `componentPackageRuntimeInstanceID`（:597-607）按 type+id+version+digest 派生**包级**实例 id → 已确认为**缺陷**而非设计门槛：
  `RestoreRuntimes`（:245-266）在 Host 启动时注册「已启用 + 有 components」的包，而**启用路径不收敛到同一状态**（实测：启用后 `contributionCount:1` 且无该包 trace、描述符折叠 404；
  重启一次后描述符 **200**、`contributionCount:2`）。**根因已定位**：`Service.Enable` 仅在 `usesLifecycleV2(extension)` 为真时走 Lifecycle V2，而该谓词要求 `Manifest.Backend.ProtocolVersion == 2` **且**声明 `manifest.lifecycle`
  （`service_lifecycle_v2.go:64-67`）；只有公开 L2 组件的包两者都不满足 → 走 legacy 启用路径，它发布资产发布物（实测 `.l2.entry` 在），但**从不 reconcile 组件注册表**（`reconcileComponents`/`validateComponentTransition` 都在 V2 发布内）。
  **已修复并验证（2026-10-16）**：Support 层新增 `PublishComponentPackageRuntime` / `RemoveComponentPackageRuntime`（实例 id 派生留在 Support，避免 Models→Support 环依赖与第二处真相）；
  legacy 启用/禁用路径调用**自由函数** `publishLegacyComponentRuntime` / `removeLegacyComponentRuntime`（首版写成 `serviceCore` 方法把接收者方法数从 79 推到 81，被架构门禁拦下），
  经窄接口 `componentPackageRuntimePublisher` 调用（既有 fake 不受影响），且调用发生在启用提交**之后**：发布失败退化为「下次启动生效」并记事件，不使 enable 失败。
  `service_lifecycle.go` 994→996 行（≤1000）。测试：Support 侧收敛/空操作/幂等移除，Models 侧调用点与失败吞咽。
  **运行时验证（无重启）**：安装→启用后描述符 **200**（修复前为折叠 404）、浏览器 `l2State=mounted` 且组件在外壳内、navbar/footer 各一、无溢出；disable+uninstall 后注册表回到 `contributionCount:1`、台账无行。
  **原修法设计（已按此实施）**：legacy 启用路径需收敛到与启动一致的状态（对「已启用且 `len(components)>0`」的 V3 包用 `componentPackageRuntimeInstanceID` 发布组件运行时，禁用时移除）；两处约束使它成为设计任务而非小补丁：
  **实测约束**：`app/Models/Extensions` 正好卡在 flat-directory 上限（95/95 非测试文件）、`app/Support/Extensions` 同样 148/148，因此**必须先抽取才能新增**；`service_lifecycle.go` 已 994 行（上限 1000），连三行调用点都需同轮小幅抽取；helper 可放有空间的既有文件 `component_registry.go`（`ReplaceRuntime` 已是公开方法，`service.go:63-64` 已持有 `componentRegistry`）。
  且该修复会让已批准的公开 L2 代码**无需重启即生效**（信任授予仍必需，属收敛而非放宽），值得一条显式决定。待补测试：禁用/启用/重启矩阵，断言启用后的组件注册表快照等于重启后的快照。第二个缺陷：公开 L2 模块契约要求**具名导出**（`isPublicFrontendModule` 校验模块命名空间，`assets.ts:219-224`），
  fixture 原来用 `export default` 被判定 contract mismatch（`data-l2-state="fallback"`），改为具名导出后挂载成功（`region-demo` fixture 仍是 `export default`，值得单独修/记）。
  **浏览器证据已取得**：`/page-component-reference/component` → `shell=site/shellSource=theme`、可见 navbar 1 + footer 1、`l2State=mounted`、组件内容与 bridge 注入正确、`insideShell=true`、无溢出、0 裁切控件。
  **更正（2026-10-16 跟进）**：同包 `view: template` 对照页**渲染正常**——上一轮「未渲染正文」是**测量错误**（探针找的 `[data-layout]` 属性 API 根本不产出）。
  实测：正文文本 "Site layout reference" / "layout: site — …"、`.sf-theme-template` 两块（主题外壳 + 正文）、无 widget、无空状态。附带发现（按设计）：API 的 `templateHtml` 会**丢弃 `data-*` 属性**
  （`themeTemplatePolicy()` 只允许 `class/id/title/role/aria-*` 等固定白名单，`app/Support/Pages/template.go:100-108`），插件页模板应改用 `class`/`id` 钩子。
  另：两个违规模块已改为具名导出（`region-demo/frontend/region-card.mjs`、`commerce-workflow/frontend/order-card.mjs`，后者原来是 Vue 风格 `{name, props}` 根本没有 mount），region-demo 已刷新摘要并通过 validate/test。实测页面仍为外壳 `site`/`theme`、可见 navbar 1 + footer 1、无溢出、`componentMounted=false`，**不声称浏览器证据完成**。
- 相邻任务书 M6 阶段 1-2 已实现（2026-10-16）：公开页新增 `view: template|component` 与 `component.id`；校验落在**唯一读取点**
  `LoadThemePackage`，CLI 与运行时激活共用同一裁决（未知 view / component+template / component 缺 id / 模板页带 component 全部拒绝）；
  `PageContribution`→`ResolvedPage`→解析响应全程透传 `view`/`component`，且组件页**豁免**「无模板即运行时不可用」的回退判定；
  web 侧正文出口新增组件分支，经既有 `SFExtensionWidget`（描述符端点 → `loadPublicFrontendRelease` → `mountPublicFrontendModule`，带隔离与租约）
  在受审查出口内部挂载，页面仍只有一个 chrome 拥有者。测试：Go 7 用例契约测试 + 贡献透传测试；web 分支顺序/无 chrome/三处透传测试。
  **架构棘轮**：两条基线**下调**而非抬高（`Support/Pages/registry.go` 1017→1008，快照助手移入 `registry_snapshot.go`；
  `Http/Controllers/Pages/controller.go` 1027→1023，回退 provider/action 归一化移入 `externalProvider`）。
  **阶段 3 待做**：尚无包 fixture 声明 `view: component` 公开页（demo fixture 只有模板、无 components/assets），
  因此**还没有端到端证据**（真实解析响应 + 浏览器落地 + 目录身份）。
- 相邻任务书 M6 解锁 + 取证清单（2026-10-16）：M5（公开 `layout: site|bare` 外壳）已由主计划交付，M6（公开插件 UI 入口 + 公开 `view: component` 页）
  因此**解锁但尚未开工**。已在任务书里写入**基于代码核对的现状清单**（已存在：`runtime/public-extensions` 挂载机制、受审查正文出口 `SFRegistryPageBody.vue`、
  shell/island 管道、`resolvePath` 契约；缺口：清单只有 `admin` 段没有公开前端入口、公开页 `ThemePageDecl` 无 `view` 字段、`resolvePath` 不返回组件描述符）
  与**分阶段实施计划**（阶段 1 契约+校验、阶段 2 解析+渲染、阶段 3 证据）。**同轮更正**（读运行时后推翻我自己的初稿）：公开 L2 组件机制其实**已存在**——
  组件在 `manifest.components[]` 声明（`l2Component`）、由 `publicManifestComponent` 准入、经
  `GET /extensions/runtime/{id}/components/{componentId}` 以摘要寻址描述符提供（含发布物快照、资产计划、可执行信任校验、准入），
  web 侧由 `SFExtensionWidget.vue` 经 `loadPublicFrontendRelease` + `mountPublicFrontendModule` 挂载并带隔离与租约；
  fixture `sforum-region-demo` 已在用。真正缺的只有**页面级绑定**：`ThemePageDecl` 无 `view`、`ResolvedPage` 无组件字段、
  `SFRegistryPageBody.vue` 无组件分支。并明确沿用冻结契约的两条约束：
  ①**阶段 1 不得单独上线**（「声明了却永不渲染」比没有更糟，见 M4 被取消的先例）；②L2 仍是精确制品批准后的完全可信代码，不做沙箱、不在运行时跑包构建脚本。
- **相邻任务书 M6 阶段 3 大部分已取证（2026-10-16）**：夹具装好并启用（真实管理端流程，含精确制品信任挑战）；`resolve-path` → `view=component`、`component=…body`、`fallback=false`；**此前卡住整个阶段 3 的描述符端点从 404 变为 200**（`trustNotice=fully_trusted_browser_code`）——计划里那条「无运行时实例的纯组件包无法准入」的开放问题**已修复**。**真实浏览器会话（桌面 1238px）里组件确实在受审查正文出口内挂载**，渲染自己的内容 + 宿主信任提示，且恰好 1 header / 1 footer。**未完成的一项（如实标注、不冒充）**：在 390x844 的 headless CDP 上下文里组件内容不出现——**完全没有发起描述符请求**（网络日志只有主题/扩展 CSS 200，无 FAILED，`consoleErrors=0`），在 390/500/900 三种宽度、25s settle、带或不带会话 cookie 下都可复现；同一页面在常规浏览器会话里能挂载。因此该上下文要么是 headless 客户端挂载的假象、要么是真实客户端缺口，**未证明**，故不声称 390x844 的组件挂载证据。
- **缺口 ② 深层修复已落地（2026-10-16）**：CLI 现在也构建路由 schema 目录——`loadValidatedExtensionPackage`（`validate`/`test` 共用）新增 `preflightRouteSchemaCatalog`，对声明了 `openapi[]` 或 `routes[]` 的包跑 `BuildRouteSchemaCatalog`。**双向验证**：路由的 `responseSchema` 无法解析时 `extension validate` 报出与 API 启动失败**同一条**错误（`route schema catalog: … route schema is missing`）；同一包补上可解析片段后通过。由 `TestExtensionValidateBuildsRouteSchemaCatalog`（拒绝+允许）钉住。顺带发现并修掉一个潜在雷：`sforum-contract-events` 夹具声明了路由 schema 却**完全没有 OpenAPI 片段**（一旦被启用就会让启动失败），现已补上完整片段。
- **M9 出口门禁已跑通（2026-10-16）**：新增 `scripts/ci/reference-packages-gate.sh`，把 `extensions/fixtures/plugins/` 下**10 个参考包**在临时暂存副本上跑 `digest --write` + `validate` + `test`（带后端的现场构建，并如实注明「这是仓内暂存副本、不是 clean-room；真正 clean-room 需要已发布 SDK 制品」）——**10/10 全过**。门禁立刻抓到并修掉**第三个 CLI 与作者预期不一致**：没有 `packageFiles` 的包（只声明 routes/events 契约）跑 `digest --write` 会被一句 `Manifest V3 packageFiles must be inline for digest refresh` 拦下，而同一包 `validate`/`test` 是绿的；现改为显式 no-op（校验后打印 `no package files declared; … validated`），「存在但非内联」仍报错，并由 `TestExtensionDigestCommandAcceptsPackageWithoutPackageFiles` 钉住。
- **M2 已收口（2026-10-16）**：A1（typed loader data 插件页）、A2（运行时缺失/插件禁用，按引用既有测试关闭）、A3（**真正的 390x844**：真实 Chrome 154 over CDP，`tests/browser-public-390x844.mjs`——shell 页恰好 1 header + 1 footer、`bare` 页 0、无溢出、无裁切控件、无 console 错误，截图在 `storage/qa/2026-10-16-public-390x844-*.png`）、A4（外层制品身份：`/site/active-theme/skin` 与插件页 shell 身份都指向 `sforum.default-theme` / `85ddda6a…`，插件正文制品单独归因；DOM 标记按页面种类如实说明，未伪造 `data-template`）全部完成。遗留两项后续：启用期 runtime instance 绑定（已修）与 CLI 不构建路由 schema 目录（已用构建期测试钉住参考包，深层修复待做）。
- **启用期 runtime instance 绑定缺口已修（2026-10-16）**：此前只有启动恢复路径把 `RuntimeInstanceID` 写进插件页面贡献，启用路径不写 → 「刚启用、未重启 API」时插件页数据恒为 `pages: exact plugin runtime unavailable`。现在 `PageRegistryAdapter` 增加 `PageRuntimeInstanceSource` 缝（`WithRuntimeInstances`），在编译快照与注册**之前**绑定精确实例；bootstrap 注入的解析器只在版本+摘要**精确匹配**时返回 id（否则保持为空、不伪造身份）。`TestPageRegistryAdapterBindsPluginRuntimeInstanceAtEnable` 钉住三侧（精确匹配绑定 / 制品不匹配不绑 / 未接来源不绑）。**运行时验证**：dev 实例上 disable → enable（带信任 token 的真实管理端流程）后**不重启**直接解析页面，`fallback=false`、`loaderError=null`、`loaderData={"state":"published","title":"Loaded from the plugin runtime: hello"}`、`selectedRuntimeInstanceId=1141cb0d…`（重启用产生的新实例）。
- **A2 已按「引用既有测试」关闭（2026-10-16）**：M2 任务 6 剩余项（运行时缺失 / 插件禁用）已被既有测试覆盖，故**不造同义测试**，改为复跑取证：`TestEnableDisableSyncsPageRegistry`（禁用后页面立即消失、重启用立即恢复）、`TestLifecyclePagePublicationUsesExactRevisionAndRuntimeAdmission`（运行时未准入时页面不可见）、`TestResolvePathReportsLayoutRegardlessOfBodyRender`（正文渲染失败不影响 layout/chrome 归属）——三条本轮均 PASS。
- **R2 已完成（2026-10-16）**：侦察结论——宿主→插件的 Protocol V2 **路由调用传输已存在**（`RuntimeInvoker.InvokeRouteInstance`，按精确进程分发，`route_dispatcher_v2.go:62` 已在用），所以 R2 **不需要动冻结的线协议**，而是「让 Page Loader Gateway 用它，而不是要求 loopback BaseURL」。已全部落地：`validateLoaderDocument` 抽出共享响应后校验（两条传输同一安全边界）、`PageDataLoader.FetchV2` + `LoaderDocumentFunc`（`Pages` 仍不依赖 `Support/Extensions`）、gateway 在有 `Invoke` 时优先 V2（HTTP 路径保留，两者皆无则 503）、bootstrap 适配器在无 loopback 时产出 `Invoke` 目标（绑定精确实例 + admission）、`pageDataRouteAdapter` 从扩展记录读声明路由（**只接受 `core.guard.public` 的 GET 路由**，重复声明拒绝）、拒绝与传输失败分离（`pages.ErrLoaderRouteRefused` → 503，真实调用失败 504）、生产装配切到 `NewPageRouteTargetSourceWithRoutes`。被钉住的 `page_loader_transport_integration_test.go` **已重写**：允许侧用真实 V2 子进程取回插件类型化文档（断言 title/slug/locale/actorHint，重复调用证明 lease 释放），拒绝侧四条（未声明路由/非公开守卫/制品摘要漂移/未接路由来源）全部 fail closed 503 且无数据；子进程侧断言 loader 调用必须是 GET + handler 阶段 + 过滤授权 + 无 actor。顺带修掉 `ValidateDataRoute` 的真实缺口（`/api/` 尾斜杠导致 `/api/anything` 漏过）。`go test ./...`（apps/api）**零失败**。**A1 因此解锁**：Web 侧 `loaderData` 链路（resolve → SFPageOutletRender → SFRegistryPageBody/SFThemeTemplate）本就完整，只差一个真插件夹具 + dev 安装 + 浏览器证据。
- **A1 进行中（2026-10-16）**：参考包 `extensions/fixtures/plugins/sforum-page-loader-reference/` 已建成——真实 Protocol V2 后端（一条公开 GET 数据路由）+ 页面贡献 + 模板 + schema；源码包过作者命令（`digest --write`、`validate` OK、`test` 0 errors / 6 checks 含 `template.runtime_exact`）。`TestReferencePageLoaderFixtureServesTypedDataThroughProductionChain` 跑完整链路：临时目录暂存→现场构建后端→对齐摘要→`LoadPackage`→真实 V2 子进程→真实注册表登记页面并解析路径→生产 Loader Gateway 取数，断言插件类型化文档到达渲染层。三个实测发现：①**`theme.json` 的数据声明是嵌套 `data:{source,route,schema}`**，旧的 `sforum-plugin-page-business-e2e` 用扁平键（被静默忽略，所以它其实从来没有数据接线）；②**`TypedDocument.SchemaId` 不带 `@version`**（版本单独在 `SchemaVersion`），写错会报 `protocol v2 route response is invalid`；③**loader 自身的 schema 校验是轻量的**（只覆盖 `type`+`required`），`enum`/`format` 由编译后的 `ThemeRuntimeSnapshot` 在模板执行前校验。`.gitignore` 补了 `extensions/fixtures/plugins/*/backend/plugin`。**A1 已闭环（2026-10-16）**：参考包经真实管理端流程装入 dev（install 201 → 精确制品信任挑战 → enable 200），
  运行时证据：`/pages/resolve-path` 返回 `fallback=false`、`loaderData={"state":"published","title":"Loaded from the plugin runtime: hello"}`、
  `selectedRuntimeInstanceId` 存在；SSR HTML 里就有渲染后的数据；浏览器实测 `shell=site / shellSource=theme`、可见 header 1 / footer 1、无横向溢出（1238==1238）。
  两个**新发现的生产缺口**（待修）：①**启用路径不把 runtime instance id 写进页面贡献**（只有启动恢复路径写），所以启用后不重启 API 时页面数据取不到（`pages: exact plugin runtime unavailable`）；
  ②**CLI 与运行时对路由 schema 的判断不一致**——路由声明 `responseSchema` 但 OpenAPI 片段不完整时，`validate`/`test` 仍报 0 errors，而 API **启动失败**（已用 `TestReferencePageLoaderRouteSchemasResolve` 在构建期钉住该契约）。
  dev 实例备注：规范 id 的行因早先卸载留下悬挂版本行，处于 `disabled` 且无法经管理端清理（`extension.artifact_missing` / `missing_artifact_cleanup_invalid`），需运维在「缺失制品清理」里处理；上述证据用临时改名副本 `sforum.page-loader-reference-live` 取得。
- **R1 已实施（2026-10-16）**：`core.guard.raw_request` 在**清单校验阶段被拒绝**（错误消息给出替代方案），四处运行时映射全部 fail closed（`Routes/request_authority.go`、`Routes/prepare.go`、`Routes/core_guard_authorizer.go`、`Extensions/protocol_v2_request_authority.go`；`route_dispatcher.go` 本就拒绝）；刻意保留（不授权、仅披露）：常量本身、`guard_risk.go` 的 raw 风险标签、`trust_service.go` 的 `requestsRawRequest`——旧制品带着该 id 时仍会被**披露**为高风险。注释与 5 份文档已更正（含破坏性变更说明：raw 权限必须由包内显式声明的守卫提供；已安装的旧包会在下次启用/恢复时 fail closed）。fixture `sforum-commerce-workflow` 已声明自己的 `kind: raw_request` 守卫（允许侧）。**CLI 双向证据**：允许侧 `/tmp/cwprobe` → `OK` + `test` 0 errors；把该路由改回 `core.guard.raw_request` 的 `/tmp/cwdeny` → 报错并 1 error。测试波及**七个包**（ExtensionManifest / Routes / Extensions / Extensions/IntegrationTests / Http / ExtensionOpenAPI / Models/Extensions），断言已按新契约翻转或改用声明守卫，允许侧仍被覆盖。
  两个额外发现：①**一条测试原本在空转**——`TestRouteInvocationAuthorityRejectsExactBindingDrift` 把已退役守卫放在响应阶段步骤上，拒绝被 `committedAfterFailure` 吸收，13 项漂移矩阵从未执行；现改用声明守卫并断言调用器确实被触达。
  ②**R1 暴露一个真实发布缺陷**：`buildLifecycleRegistryMaterial` 没有把 `Manifest.Guards` 带进路由集（启动路径有），R1 之后这会让所有使用声明守卫的路由在启用期 fail closed；两条路径现已一致。该修复使 `lifecycle_registry_publication.go` 超过 1036 行上限，故把共用的材料摘要助手抽到 `lifecycle_registry_publication_components.go` 并把基线**下调到 1018**（该包 flat-directory 上限不允许新增文件）。
  全量 `go test ./...` 只剩两个**负载敏感的 flake**（`TestRevisionStreamReconcilesMissedWakeAndHeartbeats`、`TestAIPluginCommandReferenceEntersHostGatesAndAccounting`），单跑各 2-3 次全绿，与本轮改动无关。
- **六项待决事项已拍板**（2026-10-16，记录为契约记录 Revision 1）：R1 空绑定 `core.guard.raw_request` 转发凭证**判定为漏洞**，收紧为「显式声明才放行」，并同轮更正相反注释与文档；R2 插件页数据走 **Protocol V2 新增取数方法**（不开 loopback HTTP、也不砍能力），解锁 M2 任务 5/A1、M10 行 2 与 `view: schema` 表单提交；R3 单次守卫拒绝**保持不写审计**（判断落定）；R4 M10 行 4 的 `challenge` 子句**删除**；R5 M10 行 1 验收改为**现有**内置主题；R6 `view: schema` 保留 `schema.descriptor` + `schema.contract` 命名。实施顺序：R1（安全收紧 + 双向测试 + 更正注释文档）→ R2（协议方法 + 重写被钉住的传输测试）。
- 主任务书 **M8 已闭环**（2026-10-16）：出口门禁四项逐条落实——①一条命令完成 源码编辑→构建/摘要/校验/契约测试→暂存，激活步骤在 ADR 内由 `tests/admin-extension-activation.sh` 自动化（循环本身不得绕过信任）；②失败保留最后健康制品（`TestExtensionDevLoopKeepsLastHealthyDigestOnFailure` + 场景脚本）；③**生产态冒烟补齐两侧**：命令拒绝非开发环境（`TestExtensionDevRefusesNonDevelopmentEnvironments`）+ **生产配置在 Load 期拒绝扫描开发脚手架树**（新增 `config.DevelopmentScaffoldRoot` 与 `TestProductionRejectsDevelopmentScaffoldExternalRoots`，落实 ADR 6.2.6 / M8 任务 7「在解析/bootstrap 期拒绝，而不是只藏起命令」；此前 `EXTERNAL_EXTENSION_ROOTS` 指向 `extensions/dev/...` 在生产只是约定、没有强制）；按 ADR 6.2.2 不存在可被激活的 dev 信任授予；④退出后无 watcher/构建子进程残留（进程组回收 + SIGINT）。
- M8 收口（2026-10-16）：① **管理端激活自动化**在 ADR 内交付——新增 `tests/admin-extension-activation.sh` 走真实 HTTP 流程
  （安装惰性 201 → 精确制品信任挑战 → 带 token 启用 200；可执行包无 token 必须失败；非可执行包接受服务端 `trust_not_required` 判定），
  并支持 `SFORUM_UNINSTALL=1` 收尾；对 dev API 实测全绿且台账无残留。② **重命名**场景补进 `tests/dev-loop-failure-scenarios.sh`
  （delete+create 合并为一次成功事务）。③ **运行时崩溃**按边界如实记录：**构建子进程**崩溃保留最后健康制品（场景脚本），
  **插件运行时**崩溃属 API 职责，由 typed `crash` 失败族 fail-closed 到 `ErrCoreGuardEvaluatorUnavailable` 且不泄漏 secret
  （`app/Http/plugin_guard_runtime_test.go`）。
- M10 行 4 / 行 8 收口（2026-10-16）：行 4 的 uninstall 与 restart 子项由既有测试覆盖（本轮实跑全绿，逐个点名记录，不造同义测试），
  行 4 仅剩 **challenge** 未满足（契约里不存在该结果）；行 8 最后的 **JS 禁用渲染**已实测：
  `SFORUM_DISABLE_JS=1 node tests/browser-registry-page-shell-390x844.mjs` 通过 CDP `Emulation.setScriptExecutionDisabled`
  关闭脚本执行，仍测得 site/default 各一个 navbar 与 footer、bare 为 0、外壳标记正确、无溢出、SSR 正文存在 → chrome 与正文来自服务端渲染。
  **环境变更**：`:3000` 的 Nuxt dev server 此前已停（前两轮请求过重启未果），本轮用仓库文档命令 `cd apps/web && bun run dev` 启动它
  并重跑了运行时证据与两组 390x844（含 JS 禁用），日志在 `/tmp/web-dev-relaunch.log`；用户可随时停止。
- M10 行 3 关闭（2026-10-16）：新增 `TestExecutionPlanOrdersModifierPhasesForSafeAndUnsafeMethods`
  （`app/Support/Routes/execution_plan_method_phases_test.go`）补齐「不安全方法」一半：POST 计划被标记 unsafe 且与 GET 计划同序
  （global → before → filter → wrap → handler → after，恰好一个 handler）；`global_middleware` 因不参与方法匹配而**两个计划都出现**；
  方法专用修饰器只出现在自己的计划里。写这条测试时发现一条真实规则：修饰器的 target 必须与自身方法一致，否则注册表直接拒绝输入。
  行 4 的 CSRF 子项由既有测试覆盖（安全请求铸 token、不安全请求无 token 被拒、有 token 通过、P6 合并矩阵、任意管理路径不得绕过
  CSRF/权限），因此不造同义测试。
- 证据脚本自检修复（2026-10-16）：web 服务下线时 `browser-registry-page-shell-390x844.mjs` 同时打印 `SKIP` 与
  `all 390x844 acceptance assertions passed`（exit 0）——**假阳性**，因为 `finish()` 只在失败列表为空时就宣布成功。三个 harness
  （公开页 390x844、管理端 390x844、运行时证据）现引入 `asserted` 标记：只有真正测量过才宣布通过，否则打印
  `skipped: no assertion ran`；`SFORUM_EVIDENCE_REQUIRE_LIVE=1` 时离线仍 exit 1。已实测验证。
- M10 行 6 关闭（2026-10-16）：新增可复现场景脚本 `tests/dev-loop-failure-scenarios.sh`（真机 `bun`、真 digest/validate/test、
  真文件事件、真重启时序），7 条断言全绿：启动对账、包失败（清单声明文件缺失）报 `failed stage=build` 且保留 `lastHealthyDigest`、
  恢复后重建、前端**编译失败**（`bun run build` → exit status 7）同样保留健康摘要、循环两次都存活、循环中途 **API 重启**
  （文档化注释探针，`catalog.go` 事后逐字节一致）后下一次编辑仍能重建、`SIGINT` 干净退出并报 `stopped`。脚本自带 trap 清理
  （探针包/二进制/临时文件）且无残留进程。「崩溃」按**构建**崩溃覆盖；插件**运行时**崩溃不在循环职责内（循环只监视源码、不监督运行时），
  这一区分已记录而非含糊带过。
- M5 任务 8 关闭（2026-10-16）：管理端 390 宽验收已用真实浏览器完成——`tests/browser-admin-390x844.mjs` 走 CDP，
  `Emulation.setDeviceMetricsOverride(390x844, dpr 3)` + 通过 `Network.setCookie` 注入操作员会话（httpOnly cookie 用
  `document.cookie` 读不到，CDP 可以；值从环境变量传入，不写进仓库）。实测：route-inspector 页面与 incidents 面板均存在、
  无横向溢出、无裁切控件、无控制台错误；截图 `storage/qa/2026-10-16-admin-390x844-control-panel-extensions-route-inspector.png`。
  因此「载体阻塞」从来不是仓库属性，只是 ego 侧栏载体的属性——公开页与管理端两组 390x844 现在都能按需跑。
- A2 判定关闭（2026-10-16）：按清单要求先判定是否已覆盖——四项行为均已被既有测试固定：①layout 与正文渲染无关
  `TestResolvePathReportsLayoutRegardlessOfBodyRender`；②运行时缺失 `TestLoaderUnavailableWhenPluginRuntimeMissing`
  （页面仍解析、layout 保留、响应带 `loaderError`；且对 Protocol V2 插件这是**永远**的路径，由 loader 传输集成测试与
  `bootstrap/app_test.go` 固定）；③插件禁用/贡献被移除 `TestLifecyclePagePublicationRetainsExactEmptySet` 与
  `…UsesExactRevisionAndRuntimeAdmission`（精确空发布清空页面、移除按精确制品+运行时实例栅栏、被排空的运行时页面在准入前不可见）；
  ④属主不再贡献的页面按 `TestResolvePath404` 解析为 not-found。**未单独断言**：属主在两次 resolve 调用之间被禁用的单次 HTTP 往返
  （转换本身已由 ③ 在注册表层覆盖，转换后的 HTTP 结果与 ④ 同路径）。因此不造同义测试。
- 主题激活预检真相（2026-10-16）：**更正**——C1/C2/C3 由同日更早的一段会话关闭
  （`apps/api/cmd/sforum/theme_scaffold_activation_test.go`）：覆盖率假设被推翻
  （`TestActiveThemeDoesNotRequireFullPageCoverage`），真实原因是脚手架曾声明 `components[]`、其 `propsSchema` 指向宿主核心页契约，
  CLI 接受而运行时组件注册表拒绝；脚手架修复已落地，CLI↔运行时一致性由 `TestThemeComponentSchemaDeclarationIsEnforcedByCliAndRuntime` 固定。
  本段新增：**冲突控制** `TestThemePreflightRejectsSamePathAddOwnedByAnotherExtension`（同路径 add 被他扩展拥有会被拒、换空闲路径则通过），
  以及两层**状态相关**预检的分析（切换栅栏外的主题运行时 → `ErrComponentRegistryConflict`；资源发布既不匹配旧主题也不匹配新主题 →
  `errAssetPublicationConflict`），并把 `extension.build_failed` 双语文案改为同时点名两族原因、说明错误文本会指明失败的预检层。
  **两条状态相关路径已升级为「观测到」**：`TestComponentRegistryThemeTransitionRejectsThirdThemeOutsideTheFence`
  （栅栏外第三个主题运行时被拒，清理后同一切换通过）与新增子测试
  `TestThemeAssetTransitionRejectsStaleDriftAndMultipleThemes/stale_third_theme_publication_blocks_every_switch`
  （陈旧第三方主题发布挡住一切切换，清理后同一预检通过）。诊断流程落成 runbook
  `runbooks/2026-10-16-theme-activation-rejection-diagnosis.md`（按失败层分族、给补救方式与持久性说明：组件注册表是进程内视图、
  资源发布是持久状态，后者单靠重启不会清除）。改进后的文案已补断言测试 `TestThemeActivationFailureMessageNamesBothCauses`
  （双语都必须点名声明族、残留状态族、事件日志与失败预检层）。**未完成**：未在 dev 实例上种下实时陈旧状态做端到端复现。
  决策记录：`decisions/2026-10-16-theme-activation-preflight-truth.md`。
- Route A 第 6 层收口（2026-10-16，第十段）：B2 已由既有 fail-open 测试覆盖（不造同义测试）；B1 落成**运行时**常驻检查
  `tests/registry-page-shell-runtime-evidence.mjs`（断言 site 页 shell=theme 且恰好一个 navbar 拥有者 + 一个 footer、
  bare 页 shell=none 零 chrome、`data-registry-shell` 承载 layout 而 `-source` 承载来源、shellProvider/shellPackageDigest
  与 `/site/active-theme/skin` 一致；服务未起时 SKIP，`SFORUM_EVIDENCE_REQUIRE_LIVE=1` 时失败）；
  B3 落成可复现 runbook `knowledge/runbooks/2026-10-16-theme-switch-shell-verification.md`。组件级渲染测试**已放弃**：
  `bun test` 无 SFC 编译步骤（既有组件测试都读源码字符串），渲染层证据只能走运行时。
- M10 联合发布门（2026-10-16，第九段）：**无法诚实关闭，改为产出诚实的验收矩阵**
  `knowledge/sessions/2026-10-16-m10-joined-release-gate-matrix.md`——逐行给出本轮采集的运行时证据、缺失证据与阻塞原因。
  已取证：兼容农场 `./scripts/test-api-isolated.sh --compat` → `ok: true`（两个 required cell 通过）；APILTS 契约状态
  （host.v2 / manifest.v3 / protocol.v2 current，已弃用的 L1 契约带 shim 与替代契约）；`site`/`bare`/默认布局的 SSR chrome 计数；
  载体 500 宽下的**可见** chrome 计数与无横向溢出；`/pages/resolve-path` 的 selectedProvider/digest 与 shellProvider/digest
  与 `/site/active-theme/skin` 一致。未闭合：行 1/3/4/6/7/8 需更多证据或决策；行 2（插件正文数据）被 loader 传输决策阻塞；
  行 5（会话状态）因契约不存在而阻塞；行 9（发布镜像）**已取证**：api 与 migrate 两个候选镜像构建成功（620MB / 29.9MB），在**隔离库** `sforum_smoke`（事后已删除，未触碰 dev 库）
上迁移（145 表 + 7 条 River 迁移），`APP_ENV=production` 启动后 `GET /api/v1/health` → 200 `{"status":"ok","environment":"production"}`；
smoke 还依次展示了三道生产 fail-closed 门（占位密钥 panic、`EXTENSION_ROOT` 缺失、`MARKETPLACE_ED25519_PUBLIC_KEY_HEX` 缺失），
其中 `EXTENSION_ROOT` 缺失原本只报 `mkdir /storage: permission denied`（看不出缺哪个变量），已修为 `config: EXTENSION_ROOT must be an absolute path in production`。
**环境提示（本段末尾实测）**：web :3000 无响应（用户自启的 Nuxt dev，本会话未停也未重启）；:8080 无响应但 air 进程仍在
  （air 7h09m + sforum-api 子进程 2h57m，env 正确，air 日志满是 `exit status 1`，8080 无监听）；用手工 env 直接跑已构建二进制**可以正常绑定 8080
  并注册 564 个 handler** → 代码与配置健康，异常在 air 循环/进程。因此依赖服务器的门禁本轮不声称通过（运行时证据脚本报 SKIP）。
  **三镜像端到端 smoke 已完成**：web 镜像（261MB）与 api/migrate 一起在隔离库上运行——
`GET /` 经 web 容器返回 200、81,120 字节 SSR HTML、标题 SForum、恰好一个桌面 navbar + 一个移动 navbar（同一 navbar 拥有者的响应式一对）、
含 API 派生的论坛内容标记；**对照实验**：停掉 api 容器后同一请求返回 **000**（无响应），证明 web 镜像是真实 SSR 而非静态壳。
**并已升级为「已播种数据库」的 smoke**：用宿主 migrate/seed CLI 准备隔离库（50 用户，user#1 为 super_admin；2 组、6 分类、12 标签、8 主题、17 评论），
三个候选镜像经 web SSR 提供**真实内容**——`/` 200（127,220 字节，桌面+移动 navbar 各一，8 条种子主题链接、4 条种子分类链接）、
`/c/general` 200、`/t/1/topic-ngkxas` 200（含种子主题内容）。容器/库/镜像均已清理。
**发布镜像的扩展面也已取证**：镜像启动后把受保护内置同步进库（8 插件 + 1 主题；shortcodes/content-policy/search-site/smtp/web-push 与默认主题 enabled，
auth-github/storage-fs/storage-s3 installed），容器内以**不可变摘要路径**跑起 5 个插件后端子进程，且已启用的 search 插件对种子主题做了真实索引
（`indexed topic topicId=1..4`）。**上传包的安装+启用也已在发布镜像里端到端验证**：非交互重置管理员密码 → 登录（csrf_ + X-CSRF-Token + Origin）→
multipart 安装（201 / `installed` / `source: uploaded`）→ enable（200 / `enabled`）→ 宿主 CLI 台账确认 → 该包公开页经 web 镜像渲染，
layout 契约成立（`bare` 0/0 且 `shellSource=none`；`site`/`default` 1/1 且 `shellSource=theme`）。
**更正**：启用路径只在包**真的携带可执行制品**时才要求精确制品信任确认；本次两个 fixture 都声明 `backend:{entry:"",rpc:""}` 且不含后端文件，
因此 `extension.trust_not_required` 是正确判定（且 smoke 里 `APP_ENV=production` → `SFORUM_V3_TRUST_CHALLENGES` 默认为开，门禁并未被关闭）。
**可执行信任门禁也已在发布镜像里两侧验证**：把 `sforum-shortcodes` 后端以新 id `acme.trustprobe` 复制、重写全部含 id 字段、把模块 replace 指向仓库、
`CGO_ENABLED=0 GOOS=linux` 编译出 `backend/plugin`（16.1MB，26 文件包）；安装 201（`backend.entry=backend/plugin`、protocol 2）→
challenge 200（64 字符 token、5 分钟 TTL、`sforum.trust-impact@2` 影响面）→ 无 token 启用 **409** `extension.trust_challenge_required` →
带 token 启用 200 → 容器内 6 个插件后端进程（含 `/var/lib/sforum/extensions/acme.trustprobe/1.3.4/9b1e202816de…/backend/plugin`）。
另测得：disable 后不带新 token 再启用返回 200（精确制品信任已持久化，属设计行为，不是重放）；`DELETE …/trust` 后 `trustRequired=true/trusted=false`，
再用已消费 token 启用 → **409** `extension.trust_challenge_replayed`（一次性语义成立）。
**release compose 栈也已跑过**（用本地候选镜像代替 registry tag）：不设 `SFORUM_VERSION` 时逐服务报错并拒绝启动（fail-closed）；
设好后 postgres/redis healthy、api 自跑迁移、`/api/v1/health` 200（`environment: production`）、web `/` 200（navbar 1 + footer）、`/c/general` 200，
播种 + 重启 API 后首页含 3 条种子主题链接；宿主 CLI 读到镜像内置台账 5 enabled / 3 installed / 1 theme。
三条实测运维结论已写进 runbook（`knowledge/runbooks/2026-10-16-release-image-stack-verification.md`）：**别把开发 `.env` 带进发布栈**
（相对 `EXTENSION_ROOT` 现在会 fail-closed、`NUXT_API_INTERNAL_BASE_URL=127.0.0.1` 会让容器内 SSR 502）、`down` 也必须带同一组 `-f` 与 `SFORUM_VERSION`、
播种在启动之后时需要重启一次 API 让搜索索引对账。
残留：真正从 registry 拉取已发布 tag、`compose.prod.yaml` 的反代/TLS 层。**两处任务书与仓库冲突**（只有一个受保护内置主题；
  guard 没有 challenge 结果）如实记录。**更正**：任务书基线摘要 `4691a60541836c57` 已过期，运行时活动主题制品是
  `85ddda6a…`（两个身份端点一致）。
- M9 参考包（2026-10-16，第八段）：新增聚焦参考包 `extensions/fixtures/plugins/sforum-page-layout-reference`
  ——一个包里用三种方式声明同一公开页（显式 bare / 显式 site / 不声明），通过 `digest --write`、`validate`、
  `test`（0 error / 0 warning / 4 checks，含 `template.runtime_exact`）。两条夹具级测试固定作者面行为：
  真实激活预检保留声明原样、`prepareContributions` 把未声明归一为平台默认 `site`；以及 AGENTS.md 硬规则 4
  的三方身份（每个 theme.json 模板路径恰好一条 templates[] 声明 + 一条 kind=template 的 packageFiles[]，
  摘要与文件一致）。顺带更正一处过时注释：`ThemePageDecl.Layout` 原写「空值等价 bare」，与 `NormalizeLayout`
  （空值 → site）及 M2 运行时证据冲突，已改为真实规则。仍缺：会话状态工作流参考包（无会话契约，不可构建）。
- M9 文档半程（2026-10-16，第七段）：新增权威英文迁移页 `docs/extensions/migrating-from-sforum-v2.md`
  （SForum-old → V3 对照表：Controller→`routes[]`、中间件→`action: before|after` + `core.guard.inherit` +
  显式可变字段、Blade→Page Registry 三方身份模板、Itf 钩子→`hooks[]`/events/queries/providers、
  插件辅助库→Protocol V2 SDK；Session 明确写为**不可用**）、有意关闭操作清单（原始 Fiber、核心会话改写、
  任意 Nuxt Layer、插件前置中间件、铸造身份、客户端 IP 判定），以及**在仓库外干净临时目录实测通过**的快速开始；
  配套 zh-CN / en-US 手册与索引链接。CLI 文档门禁现在也覆盖 `extension dev` / `extension builtins`（双语）。
  两个契约冲突如实记录：**不存在会话契约**（M6 仍被其 ADR 阻塞）与 **guard 没有 challenge 结果**
  （只有 allow/deny + denied/crash/timeout/protocol/canceled/unavailable）。参考包盘点：多数能力已有聚焦夹具，
  缺「bare 公开页」与「会话状态工作流」；并记录仓库夹具是 CI 夹具而非干净目录作者包（后端 go.mod 用相对
  replace、且不携带 `backend/plugin` 二进制，所以直接复制到别处跑 `digest --write` 会失败）。
- M8 `extension dev` 循环（2026-10-16，第六段）：`sforum extension dev <package-root>` 按 M0 ADR 6.2 全部冻结决定实现——
  与 DevHygiene reaper 同一门禁（仅 development，入口拒绝，测试覆盖 production/staging/test/空值）、
  **不做信任绕过**（每轮打印新摘要 + `reload_required` 指向管理端精确制品确认）、`fsnotify` 成为**直接**模块依赖、
  不建第二条构建管线（直接调用 `runExtensionBuild` 后读 `DigestTree`）、只监视包内源输入、去抖合并 + 单事务、
  `--json` 供编辑器集成、`--once` 供 CI、SIGINT 干净退出、失败保留最后健康制品。**真机 e2e 暴露并修掉两个真实缺陷**：
  ①循环自触发（每轮摘要刷新重写清单 → 13k 行输出；改为每次事务后重取内容戳快照）；②预检拒绝了「摘要过期」这个
  它本该修复的状态（改为只要求清单存在，并在启动时对账一次）。**未完成**：管理端激活自动化（ADR 禁止信任绕过）、
  运行时崩溃/API 重启反应。前端构建路径与子进程回收已实测：`extension dev --once --skip-install` 走完
  `bun run build` → dist 产物 → 摘要刷新 → 校验 → 契约测试；并发现真实缺陷——SIGINT 只杀掉直接子进程，
  构建脚本派生的孙进程会存活，现改为子进程独占进程组 + 取消时杀整组（平台分文件 + 两个测试）。
  **ADR 6.2.5 已收口**：新增 `sforum extension builtins`（从清单树发现 id/相对路径/后端入口/
  是否含 Go 后端，含主题）并让 `scripts/build-builtin-plugins.sh` 消费它——脚本里不再出现任何内置 id 或目录名；
  发现走清单元数据而非 `LoadPackage`（受保护源树没有后端二进制，完整校验必然因摘要不匹配失败）。真机运行证据：
  探针 staging 根下 built=8 skipped=1、8 个契约测试 PASS、源树未被修改、staged 清单摘要与二进制一致。
- M7 窄化类型化请求便利接口（2026-10-16，第五段）：结论是**不新增字段**——候选清单里有真实消费方的能力
  （过滤后的请求头投影、locale/trace/deadline/idempotency、宿主校验的响应头、无损 query 值）**已经实现**；
  其余六个候选全部带证据否决：客户端 IP/代理分类（无消费方，且代理信任归 `config.TrustProxy`）、
  UA/设备摘要（现有契约明确排除，测试是反向断言）、插件自有响应 Cookie（凭据面保持关闭）、
  重定向提示（身份提供方返回 typed redirect 文档、由宿主执行跳转）、缓存提示（CacheRegistry 拥有）、
  限速提示（已审核的路由执行策略拥有）。因此**不改 proto**（M7 任务 4 允许在 ADR 更新后如此）。
  新增安全测试：CRLF/空格/冒号头名与 CRLF/NUL 头值在投影处拒绝且**不触达插件**；代理/客户端身份头
  的伪造边界被固定（`x-forwarded-for`/`x-real-ip`/`forwarded`/`user-agent` 按客户端自身声明透传，
  宿主不为其中任何一个背书）。决策记录：
  `decisions/2026-10-16-m7-narrow-typed-request-conveniences-contract.md`。超过传输上限的请求文档也已补测：
  在进程内装配与生产相同的消息上限（服务端 `MaxRecvMsgSize` + 客户端 call options），断言超限文档在 RPC
  边界失败且不触达插件。
- M5 隔离可见性（2026-10-16，第三段）：崩溃循环隔离此前**没有任何管理端读取路径**（事故表只写不读），
  现在有 `GET /api/v1/admin/extensions/route-incidents`（有界 200 行、已脱敏、含聚合摘要）与 Route Inspector
  的「崩溃循环隔离」面板，六个状态齐备：loading / error / stale（刷新失败保留上一次成功账本并告警）/
  empty / quarantined（红警 + 逐行结果徽章）/ recovery（安全模式与 CLI 恢复指引）。真实浏览器 1313x930
  渲染证据（`storage/qa/2026-10-16-route-incidents-quarantined-1313x930.png`）；stale/error 由 web 契约测试
  覆盖而非截图。新增读取路由必须同时进入四个已审核矩阵（身份台账 / `requireExtensionsReadAuthority`
  可证策略清单 / 读守卫分区测试 / 目录路由计数），门禁全部命中——其中第二个是真实运行时接线要求。
  事故表为 append-only（`resolve_once`/`no_truncate` 触发器），删除夹具行须在单事务内临时禁用触发器并复原。
- M5 授权归属披露（2026-10-16，`sessions/2026-10-16-m5-guard-authorization-disclosure-handoff.md`）：
  `ExecutableTrustStatus.guardAuthorization` 与 `InspectorStep.guardRisk/guardRequiresTrust` 让操作员
  在一处看到「哪条路由的授权由宿主、插件代码还是原始请求权威拥有」，含守卫类型、可执行摘要、
  失败策略与已声明可变字段；确认识别对话框新增「授权归属」区与原始请求红警。该信息是**派生视图**，
  不进入影响摘要（摘要即授权本身，改动它会吊销全部已发授信——见
  `decisions/2026-10-16-trust-impact-digest-is-authority-view-is-derivation.md`）。已取得 HTTP 与真实
  浏览器证据（1313x930 桌面 + 载体的真实 500 窄屏，**不是** 390）。二轮按证据复核：任务 2（actor 绑定
  + 5 分钟 TTL + 仅 super_admin）、任务 4（审计词汇已覆盖全部要求动作，隔离经
  `routes.committed_after_failure` + 事件行留痕）、任务 5（守卫调用持 `RuntimeCallGuard` 租约，
  排空/隔离/等待已接线并有集成测试）均**核实通过**；任务 6 的真实缺口（路由策略测试从不覆盖守卫）
  已由 `lifecycle_route_guard_policy_test.go` 补上——升级无残留、回滚逐字段恢复先前守卫绑定、
  权威空集与安全模式零守卫授权。剩余：任务 7（六种 UI 状态）与「是否审计单次守卫拒绝」待人工决定；
  任务 8 的 390 宽管理端 QA 仍受载体限制。
- 扩展作者计划收口一轮 + 390x844 实机 QA（2026-10-16，Route A 第 6 层 + Loader 传输缺口 + 主题脚手架对齐）。
  用真实 Chrome（CDP）取得**真正的 390x844**（侧栏载体 500 下限不再是障碍）：插件页恰好 1 个可见
  navbar + 1 个可见 footer、无横向溢出、无裁切控件；并修掉这一轮暴露出的页脚归属缺陷
  （宿主 `sforum-theme.css` 对默认主题 fullwidth-3col 外壳隐藏页面级页脚，而 Page Registry add 页没有
  内容列 → 补 `:not(:has(.sforum-content-footer))` 规则；活动主题制品 4691a605… → 85ddda6a…，
  已重建 + 管理员重激活 + 双端点核验）。
  Route A 第 6 层补上自动化断言（组合外壳计数、几何 class、bare 隔离、解析期
  fail-open）；`make:theme` 脚手架与运行时激活校验器的不一致已定位并收口（根因是脚手架
  在核心页 schema 上声明 `components[]`，manifest 校验现在与组件注册表用同一条精确规则，
  误导文案与文档同步改正）；M4 的自定义 guard 生产绑定已核实并把四处「后续 wave / 已关闭」
  的过时披露改正。**发现一处生产缺口**：Protocol V2 运行时不暴露 loopback 传输，Loader
  Gateway 因此拿不到数据，`dataSource: plugin` 的页面贡献必然回退（真实子进程测试固定）。
  同时**修正两条我自己的旧结论**：主题不需要页面覆盖率；外壳的 `data-theme-region` 等
  data 标记会被旧 L1 净化路径剥掉（class 几何保留）。除上述阻塞项外全绿。详见
  `sessions/2026-10-16-route-a-layer6-and-loader-transport-handoff.md`。
- M3 页面/中间件脚手架交付（2026-10-16，第四轮）。`make:plugin` / `make:theme` 增加
  `--public-page`、`--public-page-layout site|bare`、`--route-middleware` 三个可组合 flag，
  不新增第二条生成分支（admin 页复用 `--vue-admin-page`）。全新脚手架**无需手工编辑**即可通过
  `extension validate` / `extension test`（0 error）/ `extension build --allow-scaffold` 与精确模板
  运行时预检；主题变体额外通过运行时激活用的 `PreflightThemePackage`（全新安装与替换活动主题两种
  状态）。生成的中间件只继承目标路由 guard、只声明自己列出的可变字段、不声明
  access/permission；插件页数据源**不生成**（仍被 Loader 传输缺口阻塞）。双语 CLI 文档与
  authoring guide 已更新，退出码/拒绝语义有 go 测试固定。**运行时渲染证据**：把生成的主题脚手架
  打包→安装→走真实管理员流程激活，用真实 Chrome（CDP）在 390x844 实测：生成的 `layout: bare`
  页零 chrome，主题自带首页恰好 1 个可见 navbar + 1 个可见 footer；该实测同时暴露并修掉两个
  脚手架缺陷（主题首页模板原先不含任何 chrome；模板 HTML 注释里写岛标签会让精确模板预检失败，
  因为岛扫描器会读注释）。实验后已恢复：活动主题回到 `sforum.default-theme` `85ddda6a…`，
  两个临时主题已卸载。
- 站内 AI 身份注入移除（2026-10-16）。出站 system 只由调用方决定：`Gateway.Execute`
  删除 2026-10-15 引入的身份声明注入（供应商标识 / 模型名 / 协议随成员一句
  「你是哪个模型」进入会话），配置面、控制台开关、OpenAPI、双语文案与组件身份整体
  撤下，提示词版本递增为 `forum-reply@6` / `forum-reply-triage@3` / `diagnose@3`。
  Go/Web 守卫测试反转，V3 目录账本零漂移（`generate.mjs --check` current）。
  已知既有红状态：`tests/validate-v3-p0-catalogs.mjs` 在这条含 130+ 文件未提交改动的
  工作区（以及干脆净的 HEAD）本来就不通过，非本次引入。详见
  `sessions/2026-10-16-remove-ai-identity-injection-handoff.md` 与
  `decisions/2026-10-16-remove-ai-identity-injection.md`。
- 扩展作者契约冻结（M0）+ 公开页外壳（M1 首个增量）（2026-10-16）。M0 把外壳/guard/
  版本回滚/会话/dev 五组契约从生产代码里冻结下来，过程中**推翻了两处前提**：
  (1) 自定义 guard 运行时**是活的**——文档说「later wave / closed」，
  但 `bootstrap/api_assembly_domains.go:618` 注入了活的评估器、
  `app/Http/plugin_guard_runtime.go:32` 真在请求路径上调用插件代码；
  (2) `core.guard.raw_request` **不运行任何守卫代码却把原始凭据交给 handler**
  （空绑定即 allow，而该绑定正是 `core.guard.raw_request` 所要求的条件）。
  第 (2) 点我原先在 ADR 里写反了——采信了紧贴在短路代码上方、且与行为相反的注释，
  经独立追踪指出后重新核验并就地更正。同轮还取消了 M4：「让 l2Widget 进每个
  descriptor 点」——那些点全是链接/动作/徽章，接受 widget 会造出校验通过、
  文档承诺、运行时静默无输出的能力。M1 已实现 `layout: site|bare`（默认 `bare`
  以保持既有精确制品行为），并以探针插件同摘要下的两个页面取得运行时证据：
  `/probe-bare` 零 navbar 零 footer 且 `max-w-4xl` 类名逐字节不变，`/probe-site`
  恰好一个 navbar 与一个 footer 且正文渲染在共享外壳内。架构门禁要求的容量通过
  同包抽取腾出，并把两个基线**下调**（registry.go 1055→1017、
  controller.go 1113→1073 级）而非抬高。详见
  `sessions/2026-10-16-m0-freeze-and-m1-shell-handoff.md`、`decisions/2026-10-16-extension-authoring-contracts-freeze.md`
  与 `sessions/2026-10-16-m0-threat-and-inventory-report.md`。
- 帖子/评论链接策略与站外中转页（2026-10-16）。正文链接的不变量（一律
  `target="_blank"` + 规范 `rel`）与站外改写都在净化接缝内完成——新增
  `apps/api/app/Support/ContentLinks` 并被 `Forum/renderer.go`、
  `EditorDocument.Accept`、`ContentRegistry` 渲染段、`Markdown.RenderSafe`
  共用；关键前提是 `target` 必须进入各净化策略的 allowlist，否则下一次净化会
  静默抹掉新标签页语义，且 `ContentRegistry` 的 SSR 固定点校验会直接失败。
  绝对 `http(s)` 链接一律改写成 `/outbound?u=<base64url>`（存储态契约），
  「是否启用中转 / 免中转白名单 / 是否本站」全部在**点击时**判定，因此运营改
  配置不需要重渲染任何历史内容。`site.outbound` 是非替换页（⇒ 非主题化），
  主题与插件无法摘掉目的地提示，但呈现仍走 `SFHostPublicChrome`。前端
  `v-outbound` 只作为历史内容与插件 SSR 片段的第二层补齐。设计备选见
  `tmp/demos/outbound-interstitial/`（五种方向，选定前默认「警示条 + 双栏对比」）。
  细节：`decisions/2026-10-16-outbound-link-policy.md`。

- 插件页面区域：单一来源 + 生成式投影 + 双向渲染契约（2026-10-16，插件前端体验程序
  M2/M3）。原计划让 `NavigationRegistry` 成为所有放置的唯一权威，但读运行时后否掉了
  这个前提：`RegionCatalog`/`forum.page.regions` 往**页面区域**放内容，
  `NavigationRegistry` 组装的是**导航 chrome**，两者是不同的问题、不同的运行时，
  合并只会带来信任相邻的大重构而作者一无所获。实际落地的是这个里程碑真正要消灭的漂移：
  `RegionCatalog.pageRegionMatrix` 成为唯一手写来源（14 个区域 / 11 个页面），
  `scripts/v3-catalog/generate.mjs` 由它生成
  `apps/web/app/config/pageRegionCatalog.gen.ts`，替掉 `usePageRegions.ts` 里手工同步的
  白名单；`apps/web/tests/framework/pageRegions.test.ts` 用生成的矩阵**双向**核对宿主组件
  里的 `<SFRegionOutlet>`，因此「先声明、后补渲染」不再可能。该检查揪出一个既有的死声明
  （`forum.topic.reply` 的组件只是跳转占位，一个 outlet 都没有），并否掉了 `hero` 与
  `composer_after`（与既有区域相邻、不构成独立插槽）。`MAX_REGIONS` 改为由生成目录推导
  （原来写死 8，会把 9 个区域的帖子页静默判成非法）。渲染证据：探针插件向
  `detail_before`/`comments_before`/`sidebar_top` 放置卡片，上传→信任→升级后三张卡片
  都出现在正确位置。批次二继续给首页/搜索/分类/标签六个页面加上
  `list_before`/`list_after`（分类详情的 `detail_*` 因为与 `list_before` 重复而被否掉），
  并抽出 `SFTopicMobileInfoDrawer.vue` 以满足 `SFTopicShowPage.vue` 的行数上限
  （基线随之下调到 1137）。批次二已端到端验证：`/` 与 `/c/announcement` 的
  `list_before` 位于列表容器之上、`/tags/tooling` 的 `list_after` 位于列表与分页之间，
  均在 SSR HTML 中按**位置**（而非仅存在）核对，并在浏览器 DOM 用
  `compareDocumentPosition` 复核、卡片文案可见。期间排查掉两个环境问题（`:3000`
  dev server 已死亡并按其原命令重启；`air` 因并发工作流频繁改 Go 文件而反复重启 API，
  造成间歇性错误页，已用 `PerformanceNavigationTiming.responseStatus=200` 与
  零 JS 异常/零失败请求证明与本次改动无关）。详见 `sessions/2026-10-16-plugin-page-regions-m2-m3-handoff.md` 与
  `decisions/2026-10-16-plugin-frontend-surface-contract.md`。
- `ai_tool_probe` 设置动作 + 首个外部 AI 工具插件（2026-10-16）：插件贡献的 AI 工具
  （`aiTools[]`）此前只有模型能调用，运营者无法在设置页验证配置——`provider_probe`
  要求 provider slot，纯工具插件用不了。现新增设置动作 kind `ai_tool_probe`：宿主用
  草稿值起一个短命 runtime，按 `tool: <aiTools[].id>` 派发到保留槽
  `sforum.ai.tool/invoke`。顺带在实测中发现并修掉一个老缺陷：探测 starter 原先不继承信任来源，
  而 `protocolV2ClientConfig` 对「无信任来源且非内置」直接拒绝，导致**所有上传型插件**
  的探测都静默失败（仓内带设置动作的插件都是内置，从未暴露）。配套的真实子进程集成测试
  与端到端证据、外部插件 `sforum.fetch-url`（白名单 + 开关 + 代理的 AI 抓取工具）见
  `sessions/2026-10-16-ai-tool-probe-fetch-url-plugin.md` 与
  `decisions/2026-10-15-ai-tool-probe-settings-action.md`。
- 插件宿主图标服务（2026-10-16，插件前端体验程序 M1）：插件此前完全用不了宿主图标——
  Nuxt Icon 的 `server` provider 只在 Nuxt 组件树内可用，两个 bridge 都没有图标能力，
  `@sforum/plugin-ui` 也没有任何图标组件。现新增版本化平台面
  `GET /_sforum/icons?names=…`（≤32 名称的批量 JSON）与
  `GET /_sforum/icons/{lucide|tabler}/{name}.svg`，直接从已内置的 `@iconify-json`
  集合生成并清洗 SVG（白名单重建元素/属性、片段 id 按图标命名空间化），插件只提供
  名称、永不提供标记。`AdminPageBridgeV1.icons` / `PublicFrontendBridgeV1.icons` 为
  可选增量字段（`@sforum/admin-sdk@1.1.0`）；`@sforum/plugin-ui@1.1.0` 新增
  `SPluginIcon`、若干组件 `icon` prop 与 `definePluginPage`（由 SDK 注入解析器，
  手写 `createApp` 不再会静默丢图标）。8399 个图标（含 alias）全部无损通过清洗。
  详见 `sessions/2026-10-16-plugin-host-icons-m1-handoff.md` 与
  `decisions/2026-10-16-plugin-frontend-surface-contract.md`。
- AI 思考内容外泄与模型身份修复（2026-10-15）：两个独立缺陷同源——协议适配器在
  正文为空时把 `reasoning_content` / `thinking` 提升成回答正文（于是回复机器人把
  思维链发成评论、控制台把它当测试回复并报「连接正常」、结果缓存存 10 分钟），
  而模型身份从未进入请求（只在 `Gateway.Execute` 解析 profile 后存在，提示词作者
  写不出它）。现 `Reasoning` 与 `Text` 严格分离（内联思考标签在协议层剥离、
  `resultCacheVersion` 1 → 2），回复输出预算改为跟随 profile 的 `Defaults.MaxTokens`
  （此前写死 512 且该配置从未生效），确定性空回复以 `river.JobCancel` 收尾。详见
  `sessions/archive/2026-10/2026-10-15-ai-reasoning-leak-and-identity-handoff.md` 与
  `decisions/2026-10-15-ai-reasoning-separation-and-model-identity.md`（第 2、3 条已被
  下一条取代）。
- 站内 AI 身份注入移除（2026-10-16）：`Gateway.Execute` 不再向 system 追加任何宿主
  文本——2026-10-15 引入的身份声明（`identity@1`，含 `settings.identity.discloseModel`
  开关）把 profile 的供应商标识、模型名与协议送进了会话，成员问「你是哪个模型」时
  助手会按后台配置回答。现 `Settings.Identity`、REST DTO、OpenAPI `AIIdentitySettings`、
  控制台 `SFAdminAIIdentityBlock` 与双语键整体删除，组件身份转入退役账本；配置文档残留
  的 `identity` 字段被普通 `json.Unmarshal` 忽略，无需迁移。出站 system 只由调用方
  决定，模型对自身身份的回答只能来自模型自身。提示词版本 `forum-reply@6` /
  `forum-reply-triage@3` / `diagnose@3`。详见
  `sessions/2026-10-16-remove-ai-identity-injection-handoff.md` 与
  `decisions/2026-10-16-remove-ai-identity-injection.md`。
- Manifest V3 放行插件声明个人设置入口（2026-10-15）：
  `navigation[].kind` 新增 `account-settings`，并补上可选 `visibility` /
  `ownerResource`，三处白名单（JSON Schema、Go 平台校验器、OpenAPI 镜像）已对齐。
  宿主侧解析器与 `/site/account-navigation` 投影此前已就绪，缺的只是契约；
  `public` / 省略 visibility 仍由生命周期发布抬升为 `authenticated`，`href` 仍
  只放行 `/settings/*` 与 `/extensions/*`。详见
  `sessions/2026-10-15-account-settings-navigation-manifest.md`。
- 管理端扩展页首屏信任闪屏修复（2026-10-15）：`useAdminFrontendTrust` 的
  `status` 初值为 `null`，而 `trusted` 由它算得 `false`，于是
  `v-else-if="!trusted"` 把「尚未读到状态」画成「未授权」；管理端是 SSR + 禁缓存，
  警告条因此被直接写进服务端 HTML，随后才被真实组件替换。现改为三态
  （未决 / 读取失败 / 已授权 / 未授权），未决时渲染中性骨架；
  `page-bootstrap` 为 component/settings 视图附带可空 `frontend` 信任结论
  （handler 迁出 991 行的 `controller.go` 至 `page_bootstrap.go`），
  `/trust` 移出关键路径。详见
  `sessions/2026-10-15-admin-extension-page-trust-flash.md`。
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
  SHA-256 完全一致；两者均已绑定 `keeptrue/SForum` 的 `release.yml`
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
