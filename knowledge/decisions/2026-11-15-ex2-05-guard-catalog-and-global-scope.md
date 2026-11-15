# Decision: 恢复态不回显扩展清单、core 守卫目录必须可兑现、global middleware 作用域显式化

## Status

Accepted（2026-11-15，簇 12-C）

## Context

三条同属「可见面与裁决面」的边界问题：

1. **RV-05**：启动进入 recovery-only 时，`recoveryOnlyMiddleware` 把整个
   `health.RecoveryRequirement` 序列化进 503 body——包括 `artifacts[]`（每个已装插件的
   extensionId / version / package digest）与 `publicationRevision`。任何匿名请求打
   `/api/v1/anything` 都能拿到站点装了哪些扩展、装的是哪一版。
2. **RV-03**：`Support/Routes/core_catalog_gen.go` 是「已审阅的构建期策略数据」，
   其中 37 个 evaluatorId 有 3 个（`comment_edit` / `comment_revision_restore` /
   `topic_revision_restore`）没有实现。运行期取不到实现会报
   `ErrCoreGuardEvaluatorUnavailable`，而 core step 本来就不经 dispatcher 裁决，所以
   目录等于虚报了一份不存在的守卫能力。
3. **EX2-05**：插件 `global_middleware` 无条件进入每一次匹配的贡献列表
   （`registry.Match`）、且执行计划只要求它不声明 path/method/targetId。结果是
   Core 的每一条 `/api/v1` terminal 都会被任意插件的 global middleware 包住，core 侧
   唯一的隔离手段是「这是 core 路由」这个事实本身，没有任何声明、审计或测试约束。

## Decision

### 1. recovery 态只回人读三元组（RV-05）

`app/Http/server.go` 新增 `recoveryPublicView`（code / component / message），503 只
序列化它。`artifacts[]`、`publicationRevision` 只出现在 `/api/v1/ready` 的结构化就绪
报告里（运维探针），或经认证的管理面。

用「白名单结构体」而不是「从 requirement 里删字段」是有意的：以后给
`RecoveryRequirement` 加字段不会顺带扩大匿名可见面。

### 2. core 守卫目录必须可兑现（RV-03）

选**实现**而不是从目录移除，理由：目录里这三条声明的是服务层真实存在的 AND 语义
（`RestoreTopic` 要 `topic.revision.view_any` + `topic.edit_any`；`RestoreCommentRevision`
要 `post.revision.view_any` + `post.edit_any`；`comment_edit_source` 是
`post.edit_any` OR（作者 + `post.edit_own`））。删声明会让目录失去可追溯性，而实现
它们只需要复用现成机制：

- 两条 revision restore 绑定 `requireAllDeclaredCoreGuardPermissions`（AND，逐项要求
  描述符里的权限），与服务层逐字对齐；
- `comment_edit` 复用 `forumCommentWriteGuardEvaluator`（把
  `core.route.forum.comment_edit_source` 加进它的 route 分支），与
  `authorizeForumCommentResource` 的「全局编辑权 OR 作者 + 自编辑权」一致。

配套两条闸门：

- **启动期**：`routes.ValidateCoreGuardCatalogCoverage(CoreRouteCatalog(), bindings)`
  在 `NewProductionRouteGuardAuthorizerWithPolicies` 里执行，缺实现直接让 boot 失败
  （与同文件既有的 `MustNewCoreGuardEvaluatorRegistry` panic 风格一致）；
- **测试期棘轮**：`TestProductionCoreGuardCatalogCoverage` 扫全 catalog，并要求
  `unboundCatalogEvaluatorAllowlist` 保持为空；白名单只能「带理由」进入。

`core_guard_authorizer_test.go` 的绑定数断言从 34 更新为 37（这正是这次补的三条）。

**没有把 core step 接进 dispatcher 裁决**：dispatcher 只处理「链里至少有一个插件
step」的计划（core-only 请求整条留在 Fiber），如果在这里给 core step 加裁决，就会
出现「同一请求有插件修饰器时被裁决、没有时不裁决」的分裂边界——那比现状更危险。
core terminal 的运行时裁决仍然在 handler/服务层，这轮做的是「目录不再声称不存在的
能力」。

### 3. global middleware 的作用域必须显式声明（EX2-05）

三处判定（`registry.Match`、`BuildExecutionPlan` 的 global 分支、
`policy_snapshot.routePolicyContributionApplies`）改为共用一个谓词
`globalMiddlewareScopeApplies(terminal, contribution)`：

| 声明 | 作用域 |
| --- | --- |
| `targetId` 留空 | **只覆盖插件自己的 terminal**（ProviderPlugin） |
| `targetId` 写明 | 精确覆盖这一条 terminal（Core 路由靠它 opt-in） |

因此 Core 的 `/api/v1` terminal 默认落在插件 global middleware 之外；想覆盖某一条
Core 路由必须在 route 里写 `targetId` —— 一次显式、可审计、有测试的声明。
manifest 校验（`v3_validate.go`）与准备阶段（`prepare.go`）同步允许 global 携带
`targetId`（仍禁止 Path/Methods：global 不是前缀匹配），并保留 Host 保留路由
（health / ready / notifications.stream）不可指向的限制。

拒绝的替代方案：按 path 前缀（例如「`/api/v1` 默认排除」）判作用域。那正是审计指出
的现状问题——把安全边界寄托在字符串前缀上，一旦有 Core 路由不在该前缀下就重新破防。

## Consequences

- 匿名 recovery 响应不再泄露扩展清单；`/ready` 仍是运维可见的完整诊断入口。
- 目录里 37 个 evaluator 全部有实现；任何新增 catalog evaluator 而忘记绑定的改动会
  在启动期和测试期同时变红。
- 现有插件若声明了 global middleware 且意图覆盖 Core 路由，需要补 `targetId`；
  否则它只作用于该插件自己的 terminal（默认更窄，不会因为升级而扩大影响面）。
- 现有测试里靠「global middleware 自动覆盖 core terminal」构造的链，改成显式声明
  作用域（`execution_plan_test.go`、`route_matrix_test.go`、
  `provider_selection_admission_test.go`、`route_mutable_fields_test.go`、
  `route_action_v2_fiber_integration_test.go`、
  `route_dispatcher_invalid_target_modifier_fence_test.go`、`registry_test.go`）。

## References

- 审计项：RV-05、RV-03、EX2-05（簇 12-C）
- 既有守卫模型：`knowledge/decisions/2026-10-16-extension-authoring-contracts-freeze.md`
- 实现：`app/Http/server.go`、`app/Http/core_guard_authorizer.go`、
  `app/Support/Routes/{registry,execution_plan,policy_snapshot,prepare,global_middleware_scope,core_guard_coverage}.go`
