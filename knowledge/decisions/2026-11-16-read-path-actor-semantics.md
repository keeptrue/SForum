# Decision: 读路径主体语义 A–E 三分法（依赖故障 ≠ 身份问题）

## Status

Accepted（2026-11-16，审计后备池 B1 收口裁决）。本条同时给出 RV-05b（匿名 `/ready`
收面）的运维取数口径，以及「B 不可观测」这一实现边界的三态化后续项与复位条件。

## Context

同一个读端点里的「拿不到登录主体」在改造前有 **五种** 答案，根因是
`apphttp.OptionalActor` 返回的单个 `error` 里混着三类完全不同的原因：

| 改造前行为 | 代表调用点 | 问题 |
| --- | --- | --- |
| 任何 error 一律 401 | `Controllers/EntityMeta/controller.go` 的 `readActor` | Redis 抖动被答成「你的登录无效」（与 OPS2-01 自相矛盾） |
| `slog.Warn` + 匿名 200 | `Controllers/Profile/controller.go`(publicProfile)、`bookmarks.go`(publicBookmarks) | 唯一确认的 fail-open：依赖故障被翻译成「你没登录」，探针与调用方都看不到 |
| 原样 `return err` | `Forum`（topic / topicBySlug / comments / replies、citations）、`Profile.bookmarks` 之外、`SiteChrome`、`Attachments`、`Extensions` | 全局 handler 变 500 |
| 刻意匿名 | `Badges.viewer`（公开目录/徽章墙） | 依赖故障与「无主体」被合并 |
| 跳过 viewer 分支 | `Pages`（Page Registry 渲染） | 设计如此（见下节） |

OPS2-01 已经立下规则「依赖故障 = 503 `service.not_ready`，不是 401、不是 500」，
但该规则此前只覆盖限流器/CSRF 用的 storage 层；身份与会话两条读路径没有任何可判别信号。

## Decision

### 1. A–E 三分法（写进实现，不是文档约定）

| 类 | 含义 | 判定信号（类型化，无字符串匹配） | 公开读答案 |
| --- | --- | --- | --- |
| A 无凭据 | 本次请求没有带凭据 | `authsession.CurrentUserID` 返回 `(0,false,nil)` | 零值 Actor + 200 匿名（**一字不变**） |
| B 凭据存在但不可解析/失效 | 坏 token、过期、已撤销、版本失配、续期被拒 | 同样被会话层折成 `(0,false,nil)` → **不可观测**（见 §2） | 等同 A |
| C 主体不可用 | 凭据指向的用户不存在 / disabled / banned | `errors.Is(err, identity.ErrUserNotFound)`；或 `LoadActor` 成功但 `Status` 是 disabled/banned | 401 `auth.required` |
| D 依赖故障 | 会话存储、身份存储、超时、连接中断 | `errors.Is(err, authsession.ErrStoreUnavailable)` / `identity.ErrStoreUnavailable` | 503 `service.not_ready` + `Retry-After: 1` |
| E 未识别 | 其它 | 兜底 | 503（fail-closed；**不放宽成匿名**） |

实现入口：`app/Http/auth_read.go`
`ReadActor`(:76) / `RequireActor`(:99) / `classifyActorFailure`(:127) /
`unusableSubjectStatus`(:142)；渲染在 `errors.go:23` 分支 → `storageUnavailable`
（复用既有错误码，零新增 locale 键）。

### 2. 为什么必须新增 typed 哨兵，而不是在 HTTP 层猜

分类器此前不存在：两条依赖读都把原始错误原样上抛，HTTP 层没有任何可判别信号。

| 哨兵 | 位置 | 覆盖的失败面 | 依据 |
| --- | --- | --- | --- |
| `authsession.ErrStoreUnavailable` | `app/Support/AuthSession/request_state.go:26` | 会话存储 `m.store.Get`（:149-153） | Fiber session store 对「cookie 缺失/提取失败/会话不存在」续造匿名会话**不报错**（fiber v3.4.0 `middleware/session/store.go:136-158`，`getSessionID` 把提取失败折成空 id），所以能走到这里的 error 只剩存储族 |
| `authsession.ErrStoreUnavailable` | `app/Support/AuthSession/manager.go:341`、`:356` | 令牌版本来源读失败、撤销目录查询失败 | 两者都是依赖读；原实现 `return 0,false,nil` 会把 PG/Redis 抖动变成「全体已登录用户静默登出」 |
| `identity.ErrStoreUnavailable` | `app/Models/Identity/postgres_store.go:25` | `GetCurrentUser` 的查询/扫描（:146）与 `loadCurrentUserAccess`（:151） | 与「用户不存在」严格分开 |
| `identity.ErrUserNotFound` | 既有哨兵（`types.go`）；`GetCurrentUser` 现在把 `pgx.ErrNoRows` 显式翻成它（`postgres_store.go:143-144`） | 行不存在 | 此前直接透传驱动错误，上层无从判别 |

HTTP 层只 `errors.Is/As`，不 import pgx/redis。

### 3. B 不可观测：实现边界与后续项（复位条件）

`authsession.currentUserID` 把四类情况折成 `(0,false,nil)`（确定结论，不是故障）：
续期被 Host 门拒绝 `manager.go:317-318`、会话无主体负载 `:330-332`、令牌版本失配
`:348-349`、设备目录标记已撤销 `:363`。因此 **B 与 A 在 HTTP 层不可区分**，本轮
「B→匿名 200 / B→401」的区别只存在于文档；代码里只有 C 才产生 401。

要真正区分 B，需要把 `CurrentUserID` 换成三态返回（`(userID, credentialPresent,
credentialUsable, err)` 或等价的 typed 结果），并逐个复核 40+ 处调用方。
**复位条件（满足任一条就该捡回来）**：
1. 出现「必须对坏凭据答 401、而不是静默匿名」的产品需求（例如客户端要据此清 cookie）；
2. 需要区分「匿名访客」与「失效会话访客」的缓存/审计/限流策略；
3. 引入无状态签名 cookie 或第二种凭据载体，使 B 可以被可靠观察。

本轮不作三态化：它改的是所有调用方的契约面，属独立一轮。

### 4. 读路径 `ReadActor` 与写路径 `RequireActor` 的不对称（有意）

- `ReadActor`：`err==nil` 后再判 `disabled/banned` → 401。读路径没有域层兜底，
  「降级成匿名」就是放宽可见集合，必须在门上判。
- `RequireActor`：只做「必须登录」（无凭据 → 401）与错误分类（C→401、D/E→503），
  **不**判账号状态；写路径的失效主体由域层答 403 `permission.denied`
  （既有契约，`EntityMeta` 的 permission 测试钉住），在门上改答 401 等于替换一个
  已经声明过的状态码。
- 未识别的账号状态（零值 `Status`）不判不可用：真实存储只写 active/disabled/banned，
  零值只出现在测试缝里。判成 401 会让每一处假 store 都变成「账号被封」。

### 5. `Pages`（Page Registry 渲染）判「不改」

`Controllers/Pages/controller.go` 的 `optionalActor` 包装仍返回原始 error，三个消费点
（`optionalActorID`、`resolve` 的 viewer 分支、页面 access 的 guest 判定）继续「拿不到
viewer 就按 guest 渲染」：

- 降级方向是**收窄**（guest 可见集合更小），不是 fail-open；
- 该路径的契约是「页面永远有 fallback 可渲染」，改成 503 会让 Page Registry 在
  Redis/PG 抖动时**全站白屏**（比资料页/徽章页下降级严重得多）；
- 但静默降级不能同时静默失明：日志落在 `apphttp.OptionalActor` 自身
  （`auth_user.go:70-74` 调 `LogActorDependency`，进程级限频 30s，`auth_read.go:187`）。
  为什么不在 Pages 包装里：`Pages/controller.go` 已顶到 975 行棘轮，任何净增都会被
  架构门打回；而 OptionalActor 正是「会把 error 吞成 guest」的唯一入口，收紧在这里
  既覆盖 Pages，也不漏掉将来的同类调用方。该接线由
  `TestOptionalActorLogsDependencyFailures` 钉住（摘掉即红，变异 M20）。

残余：渲染期个性化仍会静默消失（只有日志与 `/ready` 组件可查），`pageViewer`
（`:762`）目前无调用方，属死代码，本轮不动。

### 6. RV-05b：匿名 `/ready` 只回摘要

`app/Http/server.go`：`readyPublicView`(:280) + `publicReadyView`(:288)，handler 在
`:248` 用它替代直接序列化 `health.ReadyReport`。恢复态仍回 503 + `ready:false` +
`recovery{code,component,message}`；**不再出现** `artifacts` 与 `publicationRevision`
（断言 key 不存在，不是断言空值）。`ReadyRecovery` 契约同步收窄并加
`additionalProperties: false`。

运维怎么拿 artifacts（本轮**不**新增受保护端点，理由如下）：

1. `sforum extension recovery list --json` —— 只读 durable 行，输出
   `id/name/type/status/source/isSystem/version/packageDigest`，不启动主进程、不加载插件
   代码（`apps/api/cmd/sforum/recovery.go`）。
2. `sforum extension recovery recheck --reason "<why>"` —— 打印每一件与最新不可变发布
   不匹配的制品并非零退出（`docs/en-US/development/cli.md` §recovery）。
3. API 启动日志 `plugin runtime convergence failed; entering Host recovery-only mode`
   带 `publication_revision` 与失败原因（`bootstrap/api_assembly_extension_restore.go`）。

不加端点的理由：恢复态下除 `/api/v1/health`、`/api/v1/ready` 外**所有**路径被
`recoveryOnlyMiddleware`（`server.go:310`）拒绝，任何「受认证管理面」恰恰在需要它的
时刻不可达；要可用就得把新路径放进 allowlist 并依赖会话/PG 同时健康，等于在恢复态
再造一条认证链。**复位条件**：若运维评估 CLI 不可用（例如只有 HTTP 通道的托管环境），
再按「recovery allowlist + `admin.access` 只读端点 + 独立 schema」实施，并同时补
契约与豁免测试。

## 契约联动

本轮 **+34 条** operation 补 503（复用 `components/responses.yaml#/ServiceUnavailable`），
按最终 diff 逐文件复算（`grep -c "^      '503':"`，括号内为该文件 503 总数）：

| 文件 | 本轮新增 | 文件内总数 | 说明 |
| --- | --- | --- | --- |
| `paths/entity_meta.yaml` | **+6** | 6 | 值读写 2 条 + admin 4 条（同一门全覆盖） |
| `paths/forum.yaml` | **+5** | 14 | `getTopic` / `getTopicBySlug` / `listTopicComments` / `listCommentReplies` / `listCommentCitations` |
| `paths/profile.yaml` | **+0** | 4 | `getPublicProfile` / `listProfileBookmarks` 此前已声明 503 |
| `paths/site_chrome.yaml` | **+1** | 2 | `resolveSiteNavigation` |
| `paths/badges.yaml` | **+21** | 30 | `Controller.actor()`/`viewer()` 的全部消费点（公开 2 + admin 19） |
| `paths/attachments.yaml` | **+1** | 9 | `getAttachment`；content/variant 两条此前已声明 |

判定口径不是「公开读」，而是**「能否到达本轮改过的助手」**：`ReadActor` /
`RequireActor` / 包内 `actor()`→`RequireActor` / 包内 `viewer()`→`ReadActor` 的
可达 handler 一律补 503；仍走 `LoadActor` 的必登面不补（其失败仍是 500，见残余 1）。
`badges` 的 27 处 `Controller.actor()` 与 `viewer()` 两个助手全部覆盖，不留
「同一助手、一部分声明一部分不声明」的半途状态。
- `ServiceUnavailable` 的描述补一句：宿主依赖故障路径的 503 携带 `Retry-After: 1`。
  共享 response 不声明 headers —— 其它 503（如插件运行时不可用）并不发该头，
  声明了反而失真。**字段面（headers）本轮未进 parity 字段级比对，人工核对，记残余。**
- `extensions` 的插件代理是 `ALL /extensions/:extensionId/*` catch-all，契约静态无法
  对齐（既有 3 条通配路径同因），其 503 只在实现层，记残余。
- `pages` 无契约变化（§5 判不改）。
- B2 面：`ReadyRecovery` 删 `artifacts`/`publicationRevision` 并加
  `additionalProperties: false`；`Ready` 与 `ReadyComponent` 同步加
  `additionalProperties: false`，让「探针摘要」在 schema 层也是封闭集合
  （`paths/system.yaml` 的 200/503 两个 `$ref` 都指向同一份 `ApiResponseReady`，
  已一并核对）。

## 残余风险

1. ~~必登面仍直连 `LoadActor`~~ **已收口（W1，2026-11-16）**：18 个控制器包、19 处
   `apphttp.LoadActor` 直连全部换成 `RequireActor`——C（主体不存在）→ 401
   `auth.required`；D（会话/身份存储故障、超时）与 E（未识别）→ 503 `service.not_ready`
   + `Retry-After: 1`；无凭据仍是 401（必登面没有匿名视图）。`Forum.commentPage` 是
   公开读（`requireGuestRead` 放行、错误被丢弃），改走 `OptionalActor`：响应不变
   （降级成匿名是收窄），但依赖故障至少留一条限频日志。契约按 W2 可达性门逐条补
   **188 条 503**（`components/responses.yaml#/ServiceUnavailable`，其描述已写明该
   路径带 `Retry-After: 1`），门从 188 条 `gap-unregistered` 降到 **0**，`guard-removed`
   仍为 0。端点级证据：`app/Http/require_actor_endpoints_test.go`（C→401、D→503、
   E→503、A→401，各覆盖 admin overview / upload-policy / account-navigation /
   my profile / my bookmarks 五条端点）。
   **裁决不改**：`disabled`/`banned` 不在 `RequireActor` 门上判（§4 的不对称是有意的：
   必登面没有匿名回退，失效主体由域层答 403 `permission.denied`）；`Retry-After` 的
   **字段级声明**与 parity 比对属 W3 的 headers 项，本轮只在共享 response 的描述里
   承诺。**复位条件**：出现「失效主体在必登面也必须答 401」的产品需求时，先改 §4 的
   契约并同步 `EntityMeta` 已声明的 403，再回填本表。
2. `Pages` 渲染期降级（含 `resolvePath` 的 guest 分支）只留限频日志、不改变响应；
   `pageViewer`（`Pages/controller.go:762`）无调用方，属死代码，本轮不动。
3. 匿名 `/ready` 的 `components` 仍含已启用扩展的组件名（公开的 extensionId）。
   组件 `error` 字段此前会回显 `RuntimeStatus.LastError` 原文——那是插件进程启动失败的
   `err.Error()`（`Support/Extensions/manager.go:375`/`:512`、
   `manager_runtime_instances.go:491`/`:497`），可能带制品路径 / 版本 / digest / 信任
   拒绝细节，属 RV-05b 同类指纹。本轮已收敛为固定摘要
   `"extension runtime reported a failure"`（`Support/Health/extension_checks.go:91`），
   原始文本仍可从认证管理面（扩展运行时状态）与日志取得；由
   `TestExtensionRuntimeHealthDoesNotEchoRawRuntimeError` 钉住（注入含 digest 与
   `/app/storage/extensions` 的 LastError，断言序列化后的报告零命中）。
4. 契约 headers（`Retry-After`）与 503 的字段级一致性依赖人工核对；parity 门仍只比
   状态码集合。
5. 12 条公开读 401 棘轮（`tests/contract-public-read-401.json`）本轮**未改动、
   无需同步**：它的 `原因` 引用的是处理函数行（`controller.go:340` 是 `func
   categories`，其 `requireGuestRead` 在下一行），HEAD 与当前逐行相同；那 12 条是
   「无凭据 + guest read 收紧」场景，与 B 无关。
6. **parity 门看不见本轮新增的两条门**：`tests/validate-contract-status-parity.mjs`
   的 `GUARD_HELPERS` 只有 `requireGuestRead` / `requireAdmin`，`ReadActor` /
   `RequireActor` 不在其中，于是可达这两条门的路由落在「状态码集合为空」的
   194/435 退化区——这正是 P1（`entity_meta` 值读写漏 503）能溜过门的原因。本轮
   靠可达性扫描（路由 → 同包调用图 → 助手）人工补齐 `missing = 0`；把该扫描落成
   常设门属独立一轮（需同时处理跨包转调与 catch-all 路由）。

## 相关

- OPS2-01：`app/Http/storage_failure.go`（同一份 503 响应形状，另一条通道）。
- RV-05b 原记：`../sessions` 对应轮次的总表条目；本轮把「匿名可及」收为摘要。
- 测试：`app/Http/auth_read_test.go`（A–E 全表 + 限频）、
  `app/Http/read_path_endpoints_test.go`（端点级 503/401/匿名）、
  `app/Http/Controllers/EntityMeta/reader_actor_test.go`（读/写两条门）、
  `app/Http/server_test.go`（恢复态内容级断言 + ready/degraded 200）、
  `app/Support/AuthSession/manager_test.go`（撤销 vs 目录故障、令牌版本故障）。
