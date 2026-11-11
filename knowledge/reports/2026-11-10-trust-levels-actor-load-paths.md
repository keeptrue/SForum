# 2026-11-10 信任等级 · 装载路径完整清单（可勾表）

审计人：researcher｜方式：只读 `rg` / 读文件 / `git diff` 只读；未改任何生产代码
**快照声明（必读）**：本表读取期间 backend 正在同一工作区并行实现，**表里已有两处在我写报告的过程中变了**（P4 的 D22 已落地、`Actor.earnedOnly` 改名 `Actor.Earned`）。勾表前必须用 §5 命令复验。原始状态：（`git status` 有 105 个未提交文件，其中 `Identity/{policy,postgres_store,postgres_store_roles,types,seeds}.go` 已被修改、`TrustLevels/` 与 `Identity/trust_levels.go` 为新增）。**符号名与行号会移动**，本表给出的是读取时刻的位置，勾选前请用 §5 的命令复验。已知在动的符号：`Actor.Earned`（曾名 `earnedOnly`）、`CurrentUser.{TrustLevel,trustLevelResolved,earned}`。

---

## 0. 一句话结论

装载路径**不是一个，是三条**（`LoadActor` / `LoadEffectiveActorTx` / 手写 map），加上两个装饰器（`ScopedActorStore` / `ProtocolV2RouteActor`）。截至本轮读取：第一条已接、**第二条 D22 在报告写作期间落地**、**PAT 收窄（D26）仍未落地**；**装配层还没有调用 `WithTrustLevelSource`，所以此刻全仓没有任何一条路径真的解析出等级**（§4 N1）。另外发现一条**会让 T4 失效的真 bug**：deny 集合是「候选集内」的，`earned ∖ denied` 并不是真正的减法（§4 N2）。

---

## 1. 总表（21 条，逐条可勾）

列含义：**读CA** = 是否读 `users.created_at`（账龄输入）；**展开** = 是否走 `ExpandEffectivePermissions` 父→子；**TLS** = 是否经 `TrustLevelSource`；**三态** = TL 生效 / 不生效 / 应恒为 0（未解析）。

| # | 路径（位置） | 读CA | 展开 | TLS | 实现状态 | TL 三态 | 归谁 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| P1 | `PostgresStore.LoadActor` — `app/Models/Identity/postgres_store.go:395`（CA 读在 `:412`，组装 `:418-428`） | ✅ | ✅（经 P2 的 `applyTrustLevel`） | ✅ `s.trustLevels` | **已实现** | **生效** | backend 已完 |
| P2 | `PostgresStore.GetCurrentUser` → `loadCurrentUserAccess` — `postgres_store.go:103` → `postgres_store_roles.go:216/262` | ❌（resolver 自读） | ✅（`applyTrustLevel:292` 内先展开） | ✅ | **已实现** | **生效**（会话/登录/改偏好共用） | backend 已完 |
| P3 | `postgresTxStore.LoadCurrentUserAccess` — `postgres_store_roles.go:225` | ❌ | ✅ | ❌ **刻意传 nil**（`:225-229` 注释） | **已实现** | **应恒为未解析**（注册事务） | backend 已完 |
| P4 | `LoadEffectiveActorTx` — `app/Models/Identity/actor_postgres_tx.go:113`（签名已收 `trust TrustLevelSource`） | ❌（仍不读） | ❌（**刻意不修**，`:214-218` 注释自述是既有缺口） | ✅（`:219 resolveTrustGrants`） | **D22 已落地**，4 个调用点均传 trust：`v2_command_actor_postgres.go:96`、`actor_postgres_tx.go:72`、`user_fields_postgres_authority.go:231`、`session_policy_selection_postgres_authority.go:205` | **生效（earned 面）**；完整有效集仍与 P1 不等（父→子缺口） | backend 已完 |
| P5 | `RestrictActor` — `app/Models/APITokens/service.go:164-205` | 不读（继承入参） | 不适用 | ❌（继承 inner） | **未接（D26）** | 应**继承** `TrustLevel/TrustLevelResolved` 并**同步过滤 `Earned`** | backend |
| P6 | `ScopedActorStore.LoadActor` — `app/Models/APITokens/scoped_store.go:14-24` | 继承 | 继承 | 继承 inner（P1） | 已实现（随 P1） | **生效**，但被 P5 削窄 | backend |
| P7 | 插件桥合成 actor — `app/Http/core_guard_authorizer.go:210-217` | ❌ | 继承（`Request.Permissions` 已展开） | ❌ | 不修（lead 已裁定登记） | **应恒为未解析**；但 `Permissions` 含 earned 而 `Earned=nil` → P21 会答 `static`（见 N5） | lead 裁定 |
| P8 | 附件系统 actor — `app/Models/Attachments/service.go:591-595` | ❌ | ❌ | ❌ | 不修 | **应恒为未解析**（手写 `attachment.settings.manage`） | backend 保持 |
| P9 | `recoverySuperAdminActor` — `cmd/sforum/reset_password.go:311-317` | ❌ | ❌ | ❌ | 不修 | **应恒为未解析**（`RoleKeys=[super_admin]` 走旁路） | backend 保持 |
| P10 | `systemactor` — `app/Models/Extensions/systemactor/system_actor.go:46/53/87/97/102` | 经 P2/P1 | ✅ | ✅（若装配注入） | 已实现 | **生效** — **与 D25 冲突**（要求系统账号恒 0，见 N3） | backend |
| P11 | bot — `app/Models/AIReply/poster.go:52`、`app/Models/AITools/aitools.go:231` | 经 P1 | ✅ | ✅ | 已实现 | **生效** — **与 D25 冲突**（bot 活动量最大 → 会到 TL4，见 N3） | backend |
| P12 | CLI — `cmd/sforum/{ai_derived.go:177/280, seed_run.go:163/200, seed_profiles.go:99, seed_taxonomy.go:270, privacy_erase_user.go:102/120}` | 经 P1 | ✅ | ❌（CLI 装配不注入） | 已实现 | **应恒为未解析** | backend |
| P13 | 测试 fake — `app/Models/Identity/service_test.go:1032`、`component_permissions_test.go:16` | ❌ | ❌ | ❌ | 已实现 | **应恒为未解析** | qa/backend |
| P14 | 零值 actor（66 处非测试，如 `app/Http/auth_user.go:39/42/45/49`、`route_dispatcher.go:303`） | ❌ | ❌ | ❌ | 不修 | **应恒为未解析**（`IsActive()` false → `Can()` 全 false） | backend 保持 |
| P15 | 死包装 — `app/Models/Forum/service_ops.go:471`、`:509`（全仓零调用） | ❌ | ❌ | ❌ | 建议删 | **应恒为未解析**（`Actor{ID}` → `CreatedAt` 零 → 限额全跳过） | backend |
| P16 | `identity.Service.LoadActor` — `app/Models/Identity/service.go:569` | 透传 P1 | ✅ | ✅ | 已实现 | **生效** | backend |
| P17 | 其余 `LoadActor` 调用点（`Attachments/UploadPolicy/service.go:156/177/200`、`Forum/protected_shortcode_policy.go:39`、`Identity/component_permissions.go:64`、`bootstrap/{query_registry.go:91, privacy_registry.go:61, notification_adapter.go:37, app.go:144}`、`Extensions/dev_auto_runtime.go:329`、`Providers/ai_reply_image_reader.go:31`、`APITokens/service.go:148`） | 经 P1 | ✅ | ✅ | 已实现 | **生效** | backend |
| P18 | `ProtocolV2RouteActor` — `app/Support/Extensions/protocol_v2_routes.go:124`（构造自 `Providers/extensions.go:358`） | ❌（只搬 `permissions map`） | 继承 | ❌ | 不修 | **生效但丢来源**：权限集合含 earned，`Earned` 不随行 → 插件侧无法解释来源 | backend 保持 |
| P19 | protocolv2 wire actor（`protocol_v2_actor_delegation.go:68`、`guards.go:141`、`identity_runtime.go:148`、`v2_command_contract.go:122`） | 不适用 | 不适用 | 不适用 | 不修 | 不适用（不带权限，Host 侧用 **P4** 解析） | backend |
| P20 | `bootstrap/{query_registry.go:70, privacy_registry.go:66, notification_adapter.go:41}` 的裁决 | 经 P1 | ✅ | ✅ | 已实现 | **生效**（`privacy_registry.go:66` 还有 `IsSuperAdmin()` 短路） | backend |
| P21 | `Actor.CapabilitySource` — `app/Models/Identity/capability_source.go:30` | 读 `Actor.Earned` | 不适用 | 派生视图 | **已实现** | 派生；对 P7/P8/P18 会答 `static`（见 N5） | backend |

---

## 2. 三态判定汇总

**TL 生效（8 条）**：P1 P2 P6 P10 P11 P16 P17 P20 —— 共同点：都经 `LoadActor` 或 `GetCurrentUser`，即都过 `applyTrustLevel`。

**TL 不生效、必须修（1 条；P4 已在我写报告期间落地）**：
- **P5 `RestrictActor`** —— PAT 路径。除了 `Earned` 过滤，还要决定 `TrustLevel/TrustLevelResolved` 是否透传（不透传 → 前端对 PAT 会话显示"未解析"；透传 → 要保证 `Earned` 与 `Permissions` 一致，否则 P21 会说谎）。

**刻意恒为 0 / 未解析（9 条，需在 ADR 写明理由）**：
P3（注册事务）、P7（插件桥，lead 已裁定）、P8（附件系统 actor）、P9（CLI 恢复超管）、P12（CLI）、P13（测试 fake）、P14（零值）、P15（死包装，建议直接删）、P18/P19（插件线，不带来源）。

**冲突（2 条，D25 未落地）**：P10 系统账号、P11 bot —— 当前都**会**解析出等级，与 D25「bot 与系统账号 level 0 + 空集」相反。见 N3。

---

## 3. M8 可断言清单（每行一个断言，A1–A24）

| # | 断言 | 覆盖 |
| --- | --- | --- |
| A1 | `WithTrustLevelSource` 在 bootstrap 有且只有一处调用，且在 HTTP server 启动前 | 装配 |
| A2 | 未注入 source 时，`LoadActor` 返回 `TrustLevelResolved == false`、`Earned == nil`、`Permissions` 与注入前逐字节相同 | P1 失败姿态 |
| A3 | `features.trust_levels=disabled` 时，`loadCurrentUserAccess` 不写 warn、不设 `trustLevelResolved` | P2 / F 组 |
| A4 | `Grants` 返回 error 时，`loadCurrentUserAccess` 记一条 warn 且 `TrustLevelResolved=false` | P2 / E 组 |
| A5 | `effective = Expand(static) ∪ (earned ∖ denied)` 的顺序：earned 键**不触发**父→子展开 | P2 / S5 |
| A6 | 同一用户同一时刻，`Actor.Permissions` 与 `CurrentUser.Permissions` 集合相等 | P1↔P2 |
| A7 | `Actor.Earned` ⊆ `Actor.Permissions`，且 `Earned ∩ Expand(static) == ∅` | P1 |
| A8 | `CapabilitySource(perm) != none` ⟺ `Can(perm)`，对 `Permissions=nil`、super_admin、空串三种边界成立 | P21 |
| A9 | `postgresTxStore.LoadCurrentUserAccess` 产出的 `CurrentUser.Permissions` 不含 earned（注册事务内） | P3 |
| A10 | `loadAdminUserAccess` 产出的 `AdminUserDetail.Permissions` 不含 earned，且只含 `effective=true` 的行 | P2 后台分支 |
| A11 | `LoadEffectiveActorTx` 与 `LoadActor` 对同一用户的 `Earned` 集合**逐键相等**（D22 验收口径；父→子缺口另计，不许断言完整有效集相等） | P4 |
| A12 | `LoadEffectiveActorTx` 仍不读 `created_at`/`kind`（`Actor.CreatedAt.IsZero() && Kind==""`），且父→子不展开是**已知且被测试固定**的缺口，防止有人顺手修好而改变既有授权 | P4 |
| A12b | 同一用户同一键，HTTP 路径与插件命令路径对 deny 的结论一致（当前不一致，见 N2） | P1↔P4 |
| A13 | `RestrictActor` 之后 `Earned ⊆ Permissions`，且被 scope 削掉的键不在 `Earned` 里 | P5 / D26 |
| A14 | `RestrictActor` 保留 `TrustLevel`/`TrustLevelResolved`（或显式清零，二选一并断言） | P5 |
| A15 | `ScopedActorStore` 无 scopes 时原样返回 inner actor（不做任何 TL 相关改写） | P6 |
| A16 | bot 账号 `LoadActor` → `TrustLevelResolved=false` 或 `Earned=nil`（D25） | P11 |
| A17 | 系统账号（systemactor）同上（D25） | P10 |
| A18 | CLI 装配的 store 未注入 source：`seed:forum` 全流程 `TrustLevelResolved=false` | P12 |
| A19 | 测试 fake `LoadActor` 产出的 Actor `Earned == nil` | P13 |
| A20 | 零值 `Actor{}`：`Can()` 全 false、`CapabilitySource()` 全 none | P14 |
| A21 | `core_guard_authorizer` 合成 actor 的 `CapabilitySource` 不得答 `earned`（它没有 `Earned`）；若答 `static`，须在 ADR 里声明"该路径来源不可信" | P7 / N5 |
| A22 | `ProtocolV2RouteActor` 携带的权限集合与宿主 `Actor.Permissions` 相等（含 earned），但插件侧不存在 `Earned` 字段 | P18 |
| A23 | `enforceTopicCreateLimits`/`enforceCommentCreateLimits`（`service_ops.go:471/509`）已删除 | P15 |
| A24 | 全仓 `identity.Actor{` 带 `Permissions` 字面量的点，逐个在 ADR 的"刻意未解析"名单里 | P7/P8/P9 |

---

## 4. 本表暴露的新问题（N1–N6，按严重度）

### N1（P0，装配）`WithTrustLevelSource` 目前零调用者 → 全仓 TL 都是死的

`grep -rn "WithTrustLevelSource" apps/api` 只命中定义（`postgres_store.go:73`）与注释；`trustlevels.NewResolver(` 除自身定义外**无调用者**。也就是说当前工作区里 `s.trustLevels == nil` 恒成立，P1/P2 全部走"未解析"分支。

**这不是 bug，是还没接线**，但它意味着：**M8 的任何"TL 生效"断言在接线前都会假绿/假红**。验收清单第一项必须是 A1。

### N2（P0，安全，T4 失效）deny 集合是「候选集内」的，`earned ∖ denied` 不是真减法

- SQL：`database/queries/identity.sql:46-77` 返回 `candidate_permission_keys`（角色 ∪ allow）**加一列 `effective`**；`denied_permission_keys` 只出现在 `NOT EXISTS` 子查询里。
- 于是 `applyTrustLevel`（`postgres_store_roles.go:292-300`）构造的 `denied` 只包含**候选集里出现过的**键。
- **后果**：若某用户被 deny 了 `topic.create`，但该键**不在**他的候选集里（例如站点的 `member` 角色被运营改成不含 `topic.create`，或 deny 是防御性预置的），`denied` 里就没有它 → TL2 挣得 `topic.create` 时 `applyTrustLevel:334` 的 `denied[key]` 判空通过 → **deny 被绕过**。
- 计划 T4 写的"写测试证明被 deny 的键不会复活"，如果测试用"角色授予 + deny"构造（最自然的写法），**会绿**，而上面这条路仍然漏。
- **修法**（一行 SQL）：把 deny 行也返回，例如
  `SELECT key, effective FROM (candidate…) UNION SELECT permission_key, FALSE FROM denied_permission_keys`；或让 `denied` 单独来自一次 `listPermissionOverrides`（`postgres_store_roles.go` 已有现成方法，`loadAdminUserAccess:239` 在用）。
- **升级（跨 loader 分叉）**：D22 落地后的 `LoadEffectiveActorTx` 是**全量**收集 deny 的（`actor_postgres_tx.go:195-208` 遍历 `user_permission_overrides` 每一行，`denied[permission]=true`），而 HTTP 路径只看得到候选集内的 deny。于是同一个用户、同一个键会出现：**HTTP 路径 earned 复活 → 允许；插件命令路径 → 拒绝**。这正是计划要消灭的两个答案，且方向是「HTTP 更宽松」，属越权侧。
- 附带：`loadAdminUserAccess:236-241` 的 `decision.Effective` 过滤是对的，但它同样看不到"非候选 deny"——后台详情会把"被 deny 但本来也没有"的键显示为"无"，这与今天的表现一致，不算回归。

### N3（P1，D25 不可实现）`Grants(ctx, userID)` 签名拿不到 `Kind`，bot/系统账号无法恒 0

- `identity.TrustLevelSource`（`app/Models/Identity/trust_levels.go:22-24`）只有 `Grants(ctx, userID)`；
- `trustlevels.Resolver.Grants`（`app/Models/TrustLevels/resolver.go:79-91`）只读 activity/createdAt/policy；
- `CurrentUser` **没有 `Kind` 字段**（`types.go:239+`），`applyTrustLevel` 在 `loadCurrentUserAccess` 里跑，而 `Kind` 是 `LoadActor` **之后**才单独查的（`postgres_store.go:412`）——**顺序上 `applyTrustLevel` 拿不到 `Kind`**；
- `TrustLevels` 包内 `grep -i "bot\|kind"` 零命中。

所以 D25 现在**无处落地**。三个可行落点（选一个写进 D25）：
1. `TrustLevelSource` 加参数：`Grants(ctx, userID int64, kind UserKind)`（identity 已有 `Kind` 类型，不新增依赖）；
2. 装配层 `ActivityReader` 对 bot 返回零值 Activity（但 level 仍可能因账龄闸给出非 0）；
3. `applyTrustLevel` 之后再在 `LoadActor` 里清空（`Kind` 已知时）——最省事但会让 `CurrentUser.TrustLevel`（前端等级卡）与 `Actor` 不一致，**不建议**。

### N4（P1，命名）`CapabilitySource` 已被占用，且占用方是安全门禁

- 新的：`identity.CapabilitySource`（类型）+ `Actor.CapabilitySource(perm)`（`app/Models/Identity/capability_source.go:30`）。
- 已存在的：`hostapi.CapabilitySource`（接口，`app/Support/HostAPI/v1.go:95-100`），由 `BindCapabilitySource` 注入（`bootstrap/api_assembly_extensions.go:455/457/460`、`worker.go:429`、`cmd/sforum/plugin_command_runtime.go:74`、`v2_command.go:241`、`platform_resources_protocol_v2.go:44`），**管的是插件能力集合与 secret 写门禁**。
- 风险：两处同名、都在 `app/` 下、都会被 `grep CapabilitySource` 命中；将来有人把 `Actor.CapabilitySource` 的返回值喂给 `BindCapabilitySource`，是类型不兼容（`CapabilitySource` vs `interface`）所以编译会拦——但**阅读和 review 会持续误判**。
- 建议改名（在 M8 之前，改名成本最低）：`Actor.GrantSource(perm)` 或 `Actor.PermissionOrigin(perm)`，类型 `PermissionOrigin`。

### N5（P2，文档/代码矛盾）`Actor.CapabilitySource` 的注释与实现相反

`capability_source.go:44-48`：

```go
// 同一个键既被静态授予又可由等级解锁时，来源记 static：没有等级也能用。
if a.Earned[permission] {
    return CapabilitySourceEarned
}
return CapabilitySourceStatic
```

注释说"记 static"，代码返回 `Earned`。因为 `Earned` 的定义是 `earned ∖ static ∖ denied`（`types.go:227`），这个分支**永远不会**在"静态也有"时命中，所以代码行为是对的、注释是错的。但注释描述的是一种真实需求（同键双来源时该报 static），将来有人"按注释修正代码"就会把 `CapabilitySource` 变成另一个答案。**要么改注释，要么在 `Earned` 里保留全量 earned 并在此处判 `hasExpandedPermission` 优先**——二选一，别留矛盾。

另：P7/P8/P18 这类"手写 `Permissions` 但没有 `Earned`"的 actor，`CapabilitySource` 会把 earned 来的键答成 `static`。这些路径今天没有 `CapabilitySource` 消费者，但 A21 要把它固定住。

### N6（P2，契约）`ListUserPermissions` 的返回形状变了，注意生成物与视图

- `database/queries/identity.sql:46-77` 与 `database/sqlc/identity.sql.go` 都已改（新增 `Effective` 列）。
- 该查询同时被 `loadCurrentUserAccess`（P2）与 `loadAdminUserAccess`（后台）使用，两处都已适配。
- 需要确认：`sforum_core_v1` 稳定视图 / HostQuery 是否引用了这个查询的旧形状（本刀不该改插件数据面契约）。命令见 §5。

---

## 5. 复验命令（勾表用）

```bash
cd apps/api
# A1 装配
grep -rn "WithTrustLevelSource\|NewResolver(" --include=*.go . | grep -v _test

# P1/P2/P3 合并点
sed -n '209,232p' app/Models/Identity/types.go              # Actor 字段（Earned/TrustLevel*）
sed -n '216,230p' app/Models/Identity/postgres_store_roles.go   # tx 路径传 nil
sed -n '262,350p' app/Models/Identity/postgres_store_roles.go   # loadCurrentUserAccess + applyTrustLevel
sed -n '395,430p' app/Models/Identity/postgres_store.go         # LoadActor 组装

# P4/D22
grep -c "ExpandEffectivePermissions" app/Models/Identity/actor_postgres_tx.go   # 期望 0（现状）
grep -rn "LoadEffectiveActorTx(" --include=*.go . | grep -v "func "

# P5/D26
sed -n '164,205p' app/Models/APITokens/service.go

# N2 deny 语义
sed -n '46,77p' database/queries/identity.sql

# N3 bot
grep -rn "Grants(ctx" app/Models/Identity/trust_levels.go app/Models/TrustLevels/resolver.go
grep -rn -i "bot\|Kind" app/Models/TrustLevels/ | grep -v _test   # 期望空

# N4 命名冲突
grep -rn "CapabilitySource" --include=*.go . | grep -v _test

# N6 契约
grep -rn "ListUserPermissions" --include=*.go --include=*.sql . | grep -v _test
```

本次审计未执行测试、未连数据库、未修改任何生产文件（除本报告）。
