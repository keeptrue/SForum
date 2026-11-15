# Decision: core 路由授权权威在 handler/service 层，Route Registry 的 core guard 目录不参与运行时裁决

## Status

Accepted（2026-11-15，RV-03 收口裁决）。与
`2026-11-15-ex2-05-guard-catalog-and-global-scope.md` §2 同源：那一轮把「补实现、不接
dispatcher」写成实施备注，本条把同一判断提成**显式架构边界**，并给出复位条件。

## Context

审计口径把 Route Registry 当成授权面，因此把「core 路由不经 dispatcher 裁决」记成
RV-03 的剩余面。簇 12 处理的是这条的**可用性**一半（37 个 core guard evaluator 全部
有实现，启动期覆盖闸门守住）；剩下这一半是**权威归属**：435 条 core 路由的
allow/deny 由谁决定。

现状是两套并存但不重叠的事实源：

- **运行期权威**：handler 与 domain service 里的策略检查（`Actor.Can` / 资源级复核 /
  目标绑定），失败即 401/403，且 core-owned handler 的 API 侧策略检查是权威口径
  （见 `../modules/backend.md` 的 Route registration rules）。
- **构建期声明**：`CoreRouteCatalog` 每条 core 路由带一个 `CoreGuardDescriptor`
  （kind + permissions + evaluatorId），来源是生成器里人手维护的策略表。

这两者今天只有一个交点是启动期覆盖闸门：目录声称的 contextual evaluator 必须有实现，
否则 boot 失败。目录本身不参与任何一次请求的裁决。

## Decision

**保留现状边界：core 路由的授权权威在 handler/service 层；Route Registry 的 core
guard 目录是声明式策略数据，不参与运行时裁决。不做 435 条 core 路由的全量接管。**

### 事实基线（文件:行号）

| # | 事实 | 证据 |
| --- | --- | --- |
| 1 | dispatcher 对 core step 不裁决 | `apps/api/app/Support/Routes/dispatcher.go:418`：`authorize` 遇到 `ProviderCore` 直接返回空 `routeInvocationAuthority`，不查 guard |
| 2 | core handler 在同一计划里被直接调用 | `apps/api/app/Support/Routes/dispatcher_dispatch.go:268`：handler 阶段 core step 走 `invokeCoreWithCommitEvidence`，即调用 Fiber handler 本体 |
| 3 | core-only 请求**根本不进** dispatcher | `apps/api/app/Support/Routes/dispatcher_dispatch.go:89`（注释在 `:90-91`）：`!dispatchPlanHasPluginStep(chain)` 时早退，整条留在 Fiber |
| 4 | 目录的六种 kind | `apps/api/app/Support/Routes/core_guard.go:11-16`：`public` / `login` / `guest` / `super_admin` / `permission_any` / `contextual` |
| 5 | 目录自述为构建期策略数据，运行期不得据其推导裁决 | `apps/api/app/Support/Routes/core_guard.go:19-20`（`CoreGuardDescriptor` 注释：*Runtime code must never derive authorization behavior from the human-readable catalog policy*） |
| 6 | 唯一的非测试运行期读取点 = 启动期覆盖闸门 | `apps/api/app/Support/Routes/core_guard_coverage.go:36` 读 `route.Guard.EvaluatorID`；其余命中全是 `PluginGuard.*`，与 core 目录无关（核对命令见下方代码块） |
| 7 | 闸门在 boot 阶段就 panic | `apps/api/app/Http/core_guard_authorizer.go:94`：`ValidateCoreGuardCatalogCoverage` 在 `NewProductionRouteGuardAuthorizerWithPolicies` 内执行 |
| 8 | 未绑定白名单为空，且只减不增 | `apps/api/app/Support/Routes/core_guard_coverage.go:17`（空 map）、`:62`（访问器注释：白名单供测试断言「只减不增」） |
| 9 | 10 条高风险声明被钉住 | `apps/api/app/Support/Routes/catalog_guard_declarations_test.go:20`：3 条分类/标签 `permission_any` + 4 条后台内容读 `contextual` + 3 条用户级上传策略 `contextual`，逐条断言 kind / permissions（有序）/ evaluatorId |
| 10 | 37 个 evaluator 绑定 + 全目录棘轮 | `apps/api/app/Http/core_guard_authorizer_test.go:127`（`len(bindings) != 37`）、`:2480`（`TestProductionCoreGuardCatalogCoverage`，含反向摘绑断言） |
| 11 | core 目录规模 | `apps/api/app/Support/Routes/core_catalog_gen.go` = **435** 条（计数命令见下方代码块） |

核对命令（仓库根，三条都返回本文引用的数字）：

```bash
rg -n 'Guard\.(Kind|EvaluatorID|Permissions)' apps/api --glob '!*_test.go'
rg -c '^\t\{ID: "core\.route' apps/api/app/Support/Routes/core_catalog_gen.go
rg -ln 'RequiresPermission|PermissionDenied|denied' apps/api/app/Http/Controllers --glob '*_test.go' | wc -l
```

### 为什么不做全量接管

1. **会引入第二套权威，而不是消灭权威。** 目录能表达的只有粗门
   （`permission_any` 的键集合、contextual 的 AND/OR 形状）；handler/service 里真正
   决定放行的是资源级事实——目标用户是否被保护、topic 是否关闭、comment 作者与
   `post.edit_own` 的组合、附件 owner。dispatcher 也判一次之后，粗门与细门冲突时没有
   稳定优先级；按「谁更严」取值的写法会让同一条路由的拒绝理由随实现顺序漂移。
2. **行为变更面覆盖传输层，不止权限层。** core-only 计划不进 dispatcher 是**有意的**：
   dispatcher 会把下载、流、协议升级变成 buffer（`dispatcher_dispatch.go:90-91` 的
   注释）。接管 435 条等于同时改权限语义与传输语义，无法靠现有测试证明等价。
3. **收益只是「消除人工正确性依赖」。** 而该依赖今天已有门：服务层 fail-closed 语义
   加上逐路由 allowed/denied 测试（见下节）。用一个跨两层的改动去替换一个已有测试
   覆盖的人工环节，风险收益不成比例。
4. **分裂边界比现状更危险。** 一旦 dispatcher 开始裁决 core step，就会出现「同一路径
   在有插件修饰器时被裁决、没有时不裁决」——这正是 EX2-05 决策明确拒绝的形态。本条
   沿用同一判据：宁可只有一套权威，也不要两套按计划形状切换的权威。

## 替代保障（互补，各挡一面）

| 机制 | 位置 | 兜住什么 | 不兜住什么 |
| --- | --- | --- | --- |
| 启动期 evaluator 覆盖闸门 | `core_guard_coverage.go:25` → `app/Http/core_guard_authorizer.go:94` | 目录声称的 contextual evaluator 必须存在实现；缺一个 boot 失败（历史上 3 条正是这样漏进来的） | 声明**写错**但实现存在的情形 |
| 声明测试（10 条） | `catalog_guard_declarations_test.go:20` | 高风险 10 条的 kind / permissions 顺序 / evaluatorId 不许被改松（含生成器侧改错） | 其余 425 条 |
| evaluator 行为矩阵 | `app/Http/core_guard_*_test.go`（13 个文件，含 postgres 集成） | contextual 语义本身：allowed / denied / fail-closed / 伪造状态拒绝；`core_guard_forum_resource_test.go:334` 还按目录分区断言覆盖 | 目录与 handler 是否描述同一件事 |
| 路由级 permission 测试 | `apps/api/app/Http/Controllers/**/*_test.go`，含 denied 断言的 **39** 个文件（计数见上文「核对命令」第三条）；典型 `TrustLevels/controller_test.go:123` | 每条已测路由的粗门：缺权限 403、关闭态 404 可区分 | 一条从来没写测试的路由 |
| 后台 coarse-gate 逐条裁决（RV-46） | `2026-11-15-admin-coarse-gate.md` + `AdminOverview/coarse_gate_adjudication_test.go:92` | 「宽门只能当**下界**」：门之后再判数据级权限；读面同门、读宽写窄 | 门的**位置**（本条管的正是这一维） |

这五条的关系：RV-46 决定「门该多宽」，本条决定「门在哪一层」，覆盖闸门决定「目录
不许虚报」，声明测试与行为矩阵决定「已声明的语义不许漂移」，路由级 denied 测试决定
「服务层真的在判」。没有一条能替代另一条。

## Consequences

- **记账口径（RV-03 从此不计入待修池）。** RV-03 由「部分完成」改为**「设计如此，
  摘出分母」**，与 RV-46 同法：它不是被修掉的代码条目，也不是已修条目，而是被判定为
  正确的架构边界。`knowledge/index.md` 的台账随之变化：审计总 **113** 不变；摘出分母
  由 12 条变 **13** 条（§7 噪声 9 + F-D2 + FE2-02 + RV-46 + **RV-03**）；**有效待修池
  101 → 100**；已修 **99** 不变；剩余 **2 → 1 = P2 1 / P3 0**（仅 EX2-01）。
  簇 12 补的三条 evaluator 已在该轮计入，本次不重复计。
- **新 core 路由的纪律不变。** 每条非公开的 core 路由仍必须在 handler/service 层实现
  授权；目录声明是审阅材料与覆盖闸门的输入，**不是放行凭证**。「目录写了 `public`
  所以不用判」是对本条的误读。
- **插件路由不受影响。** 插件 step 的 guard（`core.permission` / `core.inherit` /
  `custom` / `raw_request`）仍由 dispatcher 裁决（`HostRouteGuardAuthorizer` /
  `ProductionRouteGuardAuthorizer`）。本条只界定 core terminal。
- **目录仍然有用。** 它是「这条路由按什么语义放行」的唯一集中描述，是审计、审阅、
  以及未来想统一观察点时的起点；没有它，435 条路由的授权现状只有读代码才能知道。

## 残余风险

1. **声明与实现会漂移。** kind 与 handler 里的检查是两份手写事实。漂移的后果不是漏洞
   （handler 才是权威），而是**目录说谎**：覆盖闸门只保证「声明的 evaluator 存在」，
   不保证「kind 与 handler 一致」。既有缓解只有 10 条声明测试；新增 contextual 声明
   必须同时进那张表。
2. **人工正确性依赖仍在。** 一条新 core 路由忘记写权限检查，没有任何运行时机制会
   发现。`route_matrix_test.go` 一类只钉 dispatcher/registry 行为，不检查 handler 内
   授权；唯一的门是 review 与 denied 测试。
3. **集中观察点缺失。** 想在 dispatcher 统一回答「这条 core 路由被谁、按什么策略
   放行」，今天拿不到：core 请求的裁决证据在 service 层，没有 dispatcher trace。这是
   本裁决的已知代价，也是复位条件 3 的来源。

## 复位条件（出现任一条，RV-03 回池重做，而不是继续按本条解释）

1. **出现 core 路由漏鉴权的实证**：某条 handler/service 缺门，或某个 actor 能读到
   不该读的数据面。此时问题不在目录，先修路由，再重审本条是否仍然成立。
2. **裁决分裂被利用**：同一路径在「有/无插件修饰器」两种形态下得到不同授权结果，且
   能构造越权。分裂边界从「理论风险」变成「可利用」。
3. **需要在 dispatcher 统一审计 core 路由裁决**（合规留痕、集中授权日志、多租户
   配额）：说明观察点必须与裁决点同层，声明式目录不再够用。
4. **目录需要表达 handler 无法用 kind 表达的语义**（目标绑定、字段级、状态机），且已
   有批量路由需要它。那时应扩展契约，而不是继续靠人工与测试。
5. **core-only 请求开始进入 dispatcher**（传输语义改造落地）：`dispatcher_dispatch.go:89`
   这个前提消失，边界需要重算。

## 参考

- `2026-11-15-ex2-05-guard-catalog-and-global-scope.md` §2（core 守卫目录可兑现，
  含「不接进 dispatcher」的原始判据）
- `2026-11-15-admin-coarse-gate.md`（RV-46：同一种「判设计如此、摘出分母」记账）
- `../sessions/2026-11-15-audit-cluster12-trust-chain.md`（簇 12 证据：37 个绑定、
  启动期闸门、反向摘绑断言）
