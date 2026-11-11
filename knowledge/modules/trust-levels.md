# Trust Levels Module

## Purpose

在既有 RBAC 之上叠一层**按累计行为自动升降**的能力层。

- **是**：按「累计行为」解锁/收紧**已存在**的权限键；把现有「新人信任阶梯」（按注册
  时长两态）推广成 N 档。
- **不是**：第二套权限系统；不是徽章（徽章只展示，永不给能力）；不是审批流程。

唯一裁决点仍是 `identity.Actor.Can`。TL 是它的**第四个授予来源**，不是并列的第二套
检查——这一条决定了整个模块的形状，动它之前先读计划 §1。

## Current Status

**M1–M5 已落地（M5 = 资料页等级卡，2026-11-11 09:21 复核）；M6–M8 未完成；M9–M13 收尾进行中（M10 earnable 编辑器已落地，2026-11-11 09:40 实测组件在树）。**
（2026-11-10 全量对拍复核 + 2026-11-11 09:21 对拍 M5 收口，逐条在真实代码上验证。）

| 结论 | 证据 |
| --- | --- |
| M1 领域包 | `apps/api/app/Models/TrustLevels/`：`types.go` / `level.go` / `policy.go` / `registry.go` / `config.go` / `resolver.go` + 测试 |
| `Actor` 三字段 | `Identity/types.go`：`TrustLevel` / `TrustLevelResolved` / `Earned`（已导出，D23） |
| 窄接口（4 参） | `Identity/trust_levels.go:29`：`Grants(ctx, userID, kind, createdAt)`；`ErrTrustLevelsDisabled` 哨兵同文件 |
| D14 公式 | `Identity/postgres_store_roles.go` 的 `applyTrustLevel`：`Expand(static)` → 并 earned（跳过 denied 与已存在）→ 记 `Earned` |
| 派生视图已改名 | `Identity/permission_origin.go`：`GrantSource()` 返回 `PermissionOrigin`（D30），与 `Can()` 同构由 `TestGrantSourceMatchesCan` 守住 |
| **装配已落地** | `bootstrap/api_assembly_domains.go:321` `identityStore.WithTrustLevelSource(...)`；`api_assembly_extensions.go:476` 同款注入 user-field store；来源在 `api_assembly_infrastructure.go:59-60` + `bootstrap/trust_levels_wiring.go` |
| **开关五处已注册** | `Options/feature_flags.go:35`（常量）/ `:63`（definitions）/ `:82`（catalog）/ `:97`（推荐默认）/ `:130`（结构体字段）+ `:153`（读取）；归一化分支在 `service_normalize.go`；前端已消费（`useWebOptions.ts`） |
| **M3 已落地** | `Forum/service_trust_limits.go:23` `limitsForActor`（**不是** `service_ops.go:514`——那是调用点）、`:101` `resolveLimit`、`:63` `newUserLimits`、`:83` `levelLimits`；适配器 `Providers/forum.go:273` `TrustPolicyAdapter.LevelLimits` |
| 阈值配置 | `Options/trust_level_options.go`：单键 `trust.levels` + `DecodeConfig` 严格解码；`DefaultPolicy()` 已与 PM 定稿表逐值一致（复核见 `reports/2026-11-10-trust-levels-doc-audit.md` C1） |
| 后台页 | `pages/admin/settings/index.vue` 的 `trustLevels` tab + `SFAdminTrustLevelsTab.vue` + `useTrustLevelsThresholds.ts`；**earnable 编辑器 = `SFAdminTrustLevelEarnableMatrix.vue`（M10 已落地）**；白名单来自后台读面 `eligiblePermissions`（前端零硬编码），`?? []` 兜底不能改成 `?? null` |
| 公开面已落地（M4） | 4 条路由进 catalog（`core_catalog_gen.go` 计数 409 = 405 + 1 公开 + 3 后台）、OpenAPI `contracts/openapi/paths/trust_levels.yaml` 在、`Http/Controllers/TrustLevels/controller_test.go` 在。**公开响应不含 `limits`**（`dto.go`：`Limits` 带 `omitempty`，注释写明「只在后台读面出现」——公开「某档冷却 60 秒」等于泄露限流参数） |
| 资料页等级卡已接线（M5 收口） | `useTrustLevel.ts` 的 `loadViewer()` 真请求 `/trust-levels`；`SFProfileShowPage.vue:110` 传 `computed(() => profile.value?.trustLevel)`，`SFProfileRightRail.vue:74` 消费（2026-11-11 09:21 复核） |
| M6–M8 未完成 | 验收断言、性能实测、独立验收（M13 收尾中，见 `../sessions/2026-11-11-trust-levels-closing.md`） |

配套文档：计划 `../plans/2026-11-10-trust-levels.md`（§3 决断 D1–D32、§10 陷阱 T1–T19、
§13 修订记录）；统计地基 `../modules/badges.md`；handoff
`../sessions/2026-11-11-trust-levels-closing.md`（当前 hot）+
`../sessions/archive/2026-11/2026-11-10-trust-levels.md`（上一刀，已归档）；作废说法底账
`../reports/2026-11-10-trust-level-superseded-claims.md`。

**纪律**：本文件每写一条实现断言，要么带路径+行号，要么写 `TODO(核实)`。不要因为计划里写了
就抄；也不要因为「代码里有」就当成「运行时生效」——两者都踩过。**否定句（「没有 X」）必须
每次对拍重测**，它们是滞后漂移的高发区。

## Data Model

**本刀不新增表、不新增列。** `Level` 是纯函数派生值，不落列、不缓存（计划 D4）。

| 输入 | 来源 | 可逆（回退会降级） |
| --- | --- | --- |
| `topic_count` / `comment_count` | `user_activity` | 否（软删不回退） |
| `like_received_count` / `like_given_count` | `user_activity` | **是** |
| `bookmark_count` | `user_activity` | **是** |
| `days_visited` | `user_activity`（真值在 `user_visits`） | 否 |
| `accountAge` | `users.created_at` | 否 |

> `accountAge` **不是** `Requirements` 字段，它不进 `trust.levels` 的阈值配置，而是独立
> 「账龄闸」的输入（`level.go`：账龄 < `NewUserDays` → 最高 TL1，否则至少 TL1）。

六项计数与 `user_visits` 已由上一刀落地，见 `../modules/badges.md` 的数据模型节。
**可逆性不对称是降级语义的全部依据**（计划 D5）：只有可逆计数下降才降级；内容软删、
审核驳回、封禁都不降级。

阈值配置存储：**单个 JSON option 键 `trust.levels`**（D19），不新建配置表——与
`trust.new_user*` 同域、同后台页、同恢复默认机制。形态照抄 `seo.footer_links`
（`service_seo_footer_normalize.go:81`：JSON 值 + 严格解码 + 未知字段拒绝 + schema 版本
字段）。

> **不要退回「`trust.level.*` 扁平多键」**：四档 × **六指标** + 4×4 限额 = **40** 个值，扁平键要过
> options 的 5 道注册（types / definitions / defaults / normalize case / `allOptionNames`
> 派生），且漏第 5 道会**静默丢弃**（T16）。指标是 **6 个不是 7 个**——账龄只保留
> `trust.new_user_days` 一个上限闸（`Policy.NewUserDays`），没有 `account_age_days` 字段。
> 这一条已实测过枚举点：`app/Models/Options/service_normalize.go`。

**阈值默认值的唯一来源是 `TrustLevels/policy.go` 的 `DefaultPolicy()`**，运营可在后台覆盖
并一键恢复；TL0 不参与判定，由 `trust.new_user*` 七键决定。

> **不要在文档里抄这组数字。** 当前 `DefaultPolicy()` 与 PM 定稿表
> （`reports/2026-11-10-trust-levels-capability-map.md`）有大量不一致，正在对齐（P0-2）。
> 对齐完成、且 backend 报过之后，再从代码里读一遍写进文档。**只印代码里能读到的数，
> 不印「应该会是这样」的数**——数字与代码不一致比没有数字更糟。

## Dependency Direction

```
Identity（声明窄接口 TrustLevelSource，不 import 任何域包）
   ↑ 实现                      ↑ 装配（**已做**：bootstrap/api_assembly_domains.go:321）
TrustLevels ──→ Badges（复用 GetCounters 读 activity，不写第二份 SQL）
   └──→ Options（读 trust.levels 阈值）
bootstrap ─→ 把 TrustLevels 的实现注入 Identity
Forum ─╳→ TrustLevels      （Forum 不认识等级域）
```

四条方向约束（D18）：

1. **Identity 不许 import `TrustLevels` / `Badges` / `Options`** —— 三者都 import
   identity，反向就是循环依赖。Identity 只声明窄接口
   `TrustLevelSource { Grants(ctx, userID) (level int, earned []string, err error) }`，
   `LoadActor` 在 `level > 0` 时调用它；接口返回 error 时按 D7 fail-closed 到空集。
2. **TrustLevels 实现该接口**（4 参：`ctx, userID, kind, createdAt`）：读 activity + 读
   options 阈值 → `LevelOf()` → 映射 earnable。`createdAt` 由装载方传入，resolver 不再自己
   读库（D29 修订）。装配在 `bootstrap`。
3. **activity 只读一处**：复用 `Badges.GetCounters`（`activity_store.go:58`，无行返回
   全 0 不报错），**不许写第二份 SQL**。bootstrap 里的适配器把 `Badges.Activity` 转成
   `TrustLevels.Activity`——转换是编译期检查，字段漂移会直接编译失败，比「列集合断言」
   更硬。
4. **Forum 不许 import TrustLevels。** M3 已落地：Forum 侧声明 `TrustPolicyResolver`
   （`Forum/service_trust_limits.go`），实现由 `Providers/TrustPolicyAdapter` 提供
   （`Providers/forum.go:273` 的 `LevelLimits`），`bootstrap` 注入。与 `Badges` 通过
   `Forum.BadgeReader` 反向注入是同一个套路。**注意函数位置**：`limitsForActor` 在
   `Forum/service_trust_limits.go:23`，`service_ops.go:514` 附近是 `containsOutboundLink`，
   不是它（PM 对拍报告此处指错，已记入不一致清单）。

## Capability Resolution — 四来源真值表

### 现状（已核实，**没有**已挣得来源）

`app/Models/Identity/policy.go:22` 的 `Can` 只有三条分支：

```
Can(perm) = IsActive()
         && ( IsSuperAdmin()                    // 绕过一切，含 deny
           || hasExpandedPermission(perm) )     // 角色授予 + 用户覆盖，含父→子继承
```

- `hasExpandedPermission`：`app/Models/Identity/permission_compat.go:57`。
- deny 在**装载时**由 SQL 排除：`database/queries/identity.sql:46-76`
  （`ListUserPermissions` 的 `denied_permission_keys` CTE），再经
  `ExpandEffectivePermissions`（`app/Models/Identity/postgres_store_roles.go:263`）。
- `super_admin` 绕过 deny 是既有设计，`policy.go` 有注释说明（L6）。

### 目标形状（D13 + D14，**已实现于装载期**）

`Can()` **一个字节都不改**（D13）。已挣得在**装载期**合并：`loadCurrentUserAccess`
（`app/Models/Identity/postgres_store_roles.go:245`）是 `Actor.Permissions` 与下发前端
`can()` 的 `CurrentUser.Permissions` 的**唯一物化点**，合并在这里两个消费者自动一致。

「在 `Can()` 里加分支」这条路还有第二个结构性理由走不通：`policy.go:29` 有
`Permissions == nil → false` 的早退，任何「在 `Can` 里额外查一个字段」的实现会在到达
它之前就返回 false。

装载公式（D14，顺序是硬的）：

```
effective = ExpandEffectivePermissions(static_effective) ∪ (earned ∖ denied)
```

先展开父→子，再并 earned，最后减 denied——这样 earned 永远不可能触发父权限展开。
`ListUserPermissions` 必须把**候选集 + 是否生效**一起返回（一次查询、deny 逻辑仍只有
一处），**不许新写第二个 deny CTE 副本**。

实现位置：`Identity/postgres_store_roles.go` 的 `applyTrustLevel`（入参是
`[]store.ListUserPermissionsRow`，逐行看 `Effective`）。它同时填
`CurrentUser.trustLevelResolved` 与 `Actor.Earned`（只含 `earned ∖ static ∖ denied`，
D23），后者专供 `GrantSource()` 解释来源、**不参与裁决**。

四来源真值表（A 组断言就按这张表枚举，`earned` 与 `deny` 组合是重点）：

| static | earned | deny | super_admin | `Can()` |
| --- | --- | --- | --- | --- |
| 0 | 0 | 0 | 0 | false |
| 1 | 0 | 0 | 0 | true |
| 0 | 1 | 0 | 0 | true |
| 1 | 1 | 0 | 0 | true |
| 0 | 0 | 1 | 0 | false |
| 1 | 0 | 1 | 0 | false（deny 压 static） |
| 0 | 1 | 1 | 0 | **false**（earned 不得复活被 deny 的键，计划 T4） |
| 1 | 1 | 1 | 0 | **false** |
| 任意 | 任意 | 任意 | 1 | true（super_admin 绕过一切） |

> **D14 的存在理由（已裁决，勿再讨论）**：deny 在装载期就把键从 `Permissions` 删掉了
> （`identity.sql:46-76`），`Actor` 上**不留痕**。若把 earned 并进 `Can()`，可解锁且可
> 被 deny 的键（`topic.create`）会被复活，直接违反 T4；D6 的结构不变量只保证
> earnable ∩ 管理类 = ∅，**挡不住**这种情况。
> 候选方向里 `Actor.Denied` 集合**已被否**：它会让 `Actor.Permissions` 不再等于有效集，
> 出现两份「能不能」的表示。选定方案是「装载期 deny 之后合并」。

### 默认能力与限额（D15 / D16 / D17 定稿）

**默认 earnable = ∅。** PM 已核实 `SeedMemberPermissions`（`Identity/seeds.go:153-161`）
含全部 7 个非管理内容键，其余 8 个非管理键是员工权限（any / lock / pin / revision），
进 earnable 会撞 D6。**所以「TL2 解锁发帖」在默认安装里不存在**——权限解锁是运营在
后台开启的框架能力，不是开箱行为。

> **已被 D34 修正（2026-11-11，Z3-b″）**：上一段最后半句「不是开箱行为」在**权限集合**层面
> 已不成立——`topic.create` / `attachment.upload` 两个基线键由**装载期减法门**在 TL0 扣下、
> TL1 起恢复，默认安装即生效。`earnable` 默认空集这一半仍然为真（D17 不动）。
> 机制、四条不扣与已知缺口见文末「D34 增量」。

| TL | 名称（slug，D10 修订版） | 默认解锁 | 默认限额 |
| --- | --- | --- | --- |
| 0 | 新用户 `newcomer` | ∅ | 沿用 `trust.new_user*` 七键现值 |
| 1–4 | 正式用户 / 常驻用户 / 资深用户 / 领袖 | ∅（运营可配） | 站点默认，不叠加额外限制 |

> 本表的「默认解锁」列只描述 **earnable**。D34 之后，TL0 的**有效权限集合** = `member` 基线
> 7 键 − 基线门 2 键 = 5 键；TL1+ = 7 键。两者不要混为一谈。

> 计划 §5 的表仍写着初稿名（新成员 / 成员 / …），**以 D10 为准**（全档带「用户」、与
> 角色词族零字面重叠）。计划 §5 与 D10 的这处不一致已报给 lead，等计划侧回填。

站点默认冷却与日限额都是 `0`（`Options/service.go:980-988`：
`forum.topics.cooldown_seconds`、`forum.topics.daily_limit`、
`forum.comments.cooldown_seconds`、`forum.comments.daily_limit`），所以「TL3 免冷却 /
TL4 日限额放宽」这类文案在本仓库是假的，**已从计划与文档里删掉**。默认安装只有
TL0 与 TL1+ 之间有差异——这是事实，照实写。**D34 之后差异不止限额**：TL0 还少两个基线键
（见文末「D34 增量」）。

两条结构约束：D15 —— earnable 与 `legacyPermissionChildren` 的**父键**必须零交集
（否则 earned 会隐式展开出一串子权限）；D16 —— `features.trust_levels` 要进 5 处
（常量、optionDefinitions、结构体读取、`FeatureFlagCatalog()`、
`featureFlagRecommendedDefaults()`），并顺手补 `features.badges` 的既有缺口
（`feature_flags.go:63-98`）。

### 派生视图（不是第二权威）

`Actor.GrantSource(perm) → super_admin | static | earned | none` 只读、只在装载时
算一次，**只能解释 `Can()` 的结论，不能反过来影响它**。消费者只有两个：前端解释
「为什么我不能发帖」、后台展示「这个能力谁能给、从哪来」。

**已实现且已定调**（`Identity/permission_origin.go`）：`static` 刻意**不区分**「角色授予」
与「用户 allow 覆盖」——DB 层已把两者合并成同一份列表并排除了 deny，要区分必须多一次
查询，不值得放在每个已认证请求的热路径上。所以派生视图只有四值
`none / super_admin / static / earned`，没有 `override`。与 `Can()` 的同构关系由
`policy_test.go` 的表驱动断言守住。

### 改动范围

`apps/api` 非测试代码现有 **256 处 `.Can(` 调用点**（全仓含测试 280）。数这个数只认
命令，不认估算——本文件与计划都在这三个数上错过，T19 已把它定为纪律：

```bash
rg -o "\.Can\(" -g '*.go' -g '!*_test.go' | wc -l   # 256
rg -o "\.Can\(" -g '*.go' | wc -l                    # 280（含测试）
```

这 256 个点**一行都不用改**——它们继续只问 `Can()`。

## 新人阶梯与 TL0（M3 已收编）

**`trustForActor` 已改名为 `limitsForActor`（`Forum/service_ops.go:514`）**，限额改由等级决定，
七个 option 键全部保留、数据未迁移。限额与权限仍然分离（D8）：限额回答「多频繁」，`Can()`
回答「能不能」，限额不进 `Can()`。

今天仍在生效的五道判据（`service_trust_limits.go:23-57`，收编时逐条保留，少一条就是
「昨天注册的新版主被新人限流」）：

| 判据 | 条件 | 行为 |
| --- | --- | --- |
| 1 | 策略未注入或 `CreatedAt` 为零值 | 不限制 |
| 2 | `actor.Kind.IsBot()` | 不限制（AI 配额与审核闸门另有约束） |
| 3 | `settings.manage` 或 `topic.edit_any` | 不限制（员工不进新人限制） |
| 4 | `TrustLevelResolved` | TL0 → 新人七键；TL1+ → 站点默认 + 分档覆盖 |
| 5 | 未解析（开关关闭 / 统计读失败） | 回落年龄两态：`days <= 0` 或已过窗口 → 不限制，否则新人七键 |

`resolveLimit`（`:101`）区分「新人只收紧 / 分档可显式覆盖」：TL1–TL4 的限额默认全部继承
站点默认（D27，JSON `null` = 继承），放宽必须由运营显式配置。

七个 option 键的**字面量**（注意第一项是下划线、其余是点分，别照计划抄错）：

| 键 | 后台默认值（`SFAdminSiteNewcomersTab.vue`） |
| --- | --- |
| `trust.new_user_days` | 7（0–365） |
| `trust.new_user.topic_cooldown_seconds` | 300 |
| `trust.new_user.comment_cooldown_seconds` | 60 |
| `trust.new_user.daily_topic_limit` | 3 |
| `trust.new_user.daily_comment_limit` | 30 |
| `trust.new_user.forbid_outbound_links` | 开 |
| `trust.new_user.forbid_attachments` | 关（死配置，见残留 3） |

定义在 `apps/api/app/Models/Options/types.go:115-121`；后台入口是
`apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`
（i18n 键 `admin.settings…newcomers`，中文标签「新人限制」）。

年龄从「唯一判据」降级为「上限闸」：`Policy.NewUserDays` 表示「最多当多久新成员」，活动量
足够可提前到 TL1，但**不能提前越过 TL1**（`level.go` 的两段式判定）。

## Feature Flag And Permissions

**开关 `features.trust_levels` 已五处注册**（D16）：常量 `Options/feature_flags.go:35`、
`optionDefinitions` `:63`、`FeatureFlagCatalog()` `:82`、`featureFlagRecommendedDefaults()`
`:97`（推荐开）、结构体字段 `:130` + 读取 `:153`；归一化分支在 `service_normalize.go`。
前端已消费（`composables/useWebOptions.ts`）。**F 组断言现在可以执行。**

关闭后的目标行为（F 组判据）：已挣得集合恒空、限额回落到年龄两态。实现路径是
`TrustLevels/resolver.go` 的 `ErrDisabled` → identity 侧 `ErrTrustLevelsDisabled` →
**静默**保持「未解析」，不写 warn（关闭不是故障）。**开关必须在调用 resolver 之前短路**
（D31）：关掉的功能不花查询，同时让「同一二进制翻开关」成为性能 A/B 的唯一手段。

**后台写权限 `trust.manage` 已闭合**：常量与目录 `Identity/seeds.go:84` / `:144`、
迁移 `database/migrations/202611100004_trust_manage_permission.sql`（插键 + 授
super_admin + Down 回收）、前端 `apps/web/app/config/roleTemplates.ts:69`。
奇偶校验 `TestEverySeededPermissionReachesTheDatabase` 覆盖「seeds 有、迁移没有」。

**不做 `trust.view`**（D9）：后台读面复用 `admin.access` + 只读展示。论证**不是**
「`badge.view` 是死权限」——那条前提已被 2026-11-11 实测推翻（见下），现行论证是：
`trust.manage` 已经有**四道门**（页面 `admin.access` / 侧栏 `settings.site.manage` /
读 `admin.access` / 写 `trust.manage`，证据 `core_catalog_gen.go:231-233` +
`adminModules.ts:111-116`），再补一个 `trust.view` 只会造出**第五个权限位**，而读面本来
就被 `admin.access` 覆盖；新增一个权限位的成本（seeds + 迁移 + 权限目录 + 前端登记点 +
可达性审计）大于收益。

> **订正（2026-11-11 09:30）**：父计划 §6 用「`badge.view` 是死权限」来论证
> 「权限可达性有前提」，该前提**不成立**：`badge.view` 在 `core_catalog_gen.go:44` 有守卫、
> 在 `adminModules.ts:229` 被侧栏消费（本文件作者 09:28 复核同款结论）。
> **结论保留**（不做 `trust.view`），论证链按上一段重写。父计划作为历史证据**不改**；
> 详见 researcher 审计 §6。顺带提醒：`badge.view` 是活权限，别在别处再当反例用。

## Extension Surface

`TODO(核实)`：计划未定等级域的稳定视图与 HostQuery，这里**不编**。M4/M5 落地后必须回答：

1. 等级要不要进 `sforum_core_v1`？派生值不落库，视图无法直接暴露；若暴露，只能在
   HostQuery 里现算——那就得决定「按谁算」（请求者？目标用户？）以及是否接受
   每次查询多读一行 `user_activity`。
2. 公开资料页要展示等级时，走视图还是走独立只读接口。
3. 插件是否需要「读取某用户等级」的稳定契约；若需要，等级名与阈值是否也进契约
   （否则插件无法解释自己的判定）。

明确**不做**（计划 §9）：等级专属徽章联动、等级影响搜索权重/排序、
`trust_level` 反规范化列与后台重算按钮、阅读类指标、提名/晋升审批。

## 登记点：新增后台 tab 或公开组件是 **9 处**，不是 6 处

V3 目录的生成器 `scripts/v3-catalog/generate.mjs` 要求每个新增组件在
`docs/extensions/v3/catalog-identities.json` 有 reviewed identity，**缺失时直接抛错**
（不是静默失败）。所以完整登记点是：

| # | 位置 |
| --- | --- |
| 1 | 路由 shell 的 tab 联合类型（如 `pages/admin/settings/index.vue` 的 `SettingsTab`） |
| 2 | 同一文件的 `validTabs` 数组 |
| 3 | 同一文件的 tab 列表（label + icon） |
| 4 | 同一文件的 tab → 组件映射 |
| 5 | 组件文件本身（如 `components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue`） |
| 6 | 两份 locale 的 i18n 键（`zh-CN.json` / `en-US.json`） |
| 7 | `docs/extensions/v3/catalog-identities.json` 的 reviewed identity |
| 8 | 生成产物里的 catalog 条目（由生成器写入，`--check` 会红） |
| 9 | `tests/validate-admin-framework.ts` 的 `adminPagePathsById` 映射（**新增后台页**才需要，新增 tab 不需要）——漏了是门禁红 |

漏 #1–#4 的后果是**静默回落**（tab 不显示或回落到 `basic`）；漏 #7 的后果是**生成器
抛错**，比静默温和，但同样挡提交；漏 #9 的后果是静态门禁直接红
（`Admin module registry points to an unknown page id`）。别按「6 处」记。

**#1–#4 与 #9 按「页面形态」二选一，不是全都要：**

| 形态 | 需要 | 不需要 | 例子 |
| --- | --- | --- | --- |
| **设置页内的 tab**（组件，没有独立 page id） | #5–#8 | #1–#4、#9 | 本刀的 `SFAdminTrustLevelsTab.vue` / `SFAdminTrustLevelEarnableMatrix.vue` |
| **独立后台页**（`adminModules.ts` 里有自己的 `id`，且有对应 `pages/admin/**` 文件） | #5–#9（#1–#4 不适用） | #1–#4 | 徽章的 `pages/admin/badges.vue` + `id: '/badges'` |

判据只有一条：`adminModules.ts` 里有没有它的 `id`。有 `id` 就必须同时补
`adminPagePathsById` 一行（否则门禁红），并把 `pages/admin/<id>.vue` 写进
`definePageMeta({ layout: 'admin' })` + `defineOptions({ name: '<componentName>' })`
+ `useAdminPage('<id>')`。**没有 `id` 的 tab 不要往那张表里加**——加了一张没有对应
registry 条目的映射是死行。

## Known Residuals（不要当 bug 修）

1. **M1–M5 已落地（含公开面与资料页等级卡），M6–M8 未完成。** 别再按「未接线 / 端点不存在」
   理解现状：等级、限额、公开等级接口、资料页等级卡都已生效。
2. **父键展开出的子键被 deny 后会被加回**（既有怪癖，本刀不修）。deny 一个由父权限展开出来
   的子键（持有父键时），`ListUserPermissions` 把它标成不生效，但
   `ExpandEffectivePermissions` 随后按父键重新加回。修它会改变既有静态语义，另开一刀。
3. **`trust.new_user.forbid_attachments` 是死配置，运营勾了无效。** 值会存、后台会显示、
   `Options.TrustPolicy.ForbidAttachments` 也读出来，但**没有消费者**：`limitsForActor` 只读
   `forbid_links`。D20 裁决本刀不接线（接线是运行时行为变更，会破坏 F 组基线）。
   别在文档里写成生效的开关（「存下来但没人读」是这类配置的共同形状）。
4. **默认 earnable = ∅**，所以「TL2 解锁发帖」在默认安装里不存在。权限解锁是运营开启的框架
   能力（D17 要求必须有编辑入口，否则机制是死代码）。裁决见
   `../decisions/2026-11-11-trust-level-earnable.md`（D33：走 A 路补编辑器，默认空集不变）。
   **D34 补充**：「TL1 解锁发帖」这件事在默认安装里**存在**，但它走基线门、不走进 earnable——
   所以别把本条的「不存在」推广成「等级默认不影响权限」。
5. **TL1–TL4 默认限额 = 站点默认**（D27：`null` = 继承，全档默认继承）。站点默认冷却与日限额
   都是 `0`，所以默认安装只有 TL0 与 TL1+ 之间有差异。
6. **`LoadActor` 的查询基线是 4 次**（主查询 + 角色 + 权限 + `created_at`/`kind` 合并读），
   信任等级再加 1 次 activity 读 → **4→5**（R7 的「同请求读两次」已收口，别再按 4→6 算）。
   按 T17：`LoadActor` 有 45 个调用点、单请求可能调 ≥2 次，性能预算按 +N 算。
7. **deny 的真实位置**是 `database/queries/identity.sql`（`denied_permission_keys` CTE），
   不是 `postgres_store_permissions.go:49`（那是收件人查询）。
8. **派生视图只有四值，没有 `override`**：`GrantSource()` 返回 `PermissionOrigin`
   （`none` / `super_admin` / `static` / `earned`），`static` 刻意不区分「角色授予」与「用户
   allow 覆盖」——多一次查询不值得放在热路径。这是已定调的设计（D30），不是待补的 TODO。
9. **命名与 i18n 有三条硬约定**：
   - 真实键族只有两个：公开侧 `profile.trustLevel.*`、后台侧 `admin.settings.trustLevels.*`。
     `trust.level.*` 与 `trustLevels.*` 都**不是** i18n 键族（前者已零命中，`NameKey()` /
     `DescriptionKey()` / `ThresholdOptionKey()` 三个死生成器都已删除）。
   - **「档位名 ∩ 角色名 == ∅」只对 zh 成立**：`admin.roleCatalog.member.alias` 的 **en 值也是
     `Member`**，与 TL1 en 值相同 ⇒ en 交集 = `{Member}`，**已知且接受**。机器断言只对 zh 生效，
     别写成双语。
   - **i18n metric 键是 camelCase**（`topicCount` / `daysVisited`），**API `requirements` 是
     snake_case**（`topic_count` / `days_visited`）。写「两者相等」的断言必红。
10. 文档侧：运营页**刻意不写端点路径**——`tests/validate-docs.mjs` 要求文档里每个
    `METHOD /api/v1/...` 同时存在于 OpenAPI 与 Go 路由目录，M4 未落地前写端点会直接把门禁
    打红。M4 落地后拿到端点清单再补。
11. **存在第二个 actor loader：`LoadEffectiveActorTx`（`Identity/actor_postgres_tx.go:112`）**，
    已按 D22 接入 `TrustLevelSource`，但它**历史上不展开父→子权限继承**（该文件内
    `ExpandEffectivePermissions` 出现 0 次）。后果：`extension.manage` 持有者在**插件命令
    路径**今天就被误拒。本刀**不修**这个缺口（另开一刀）。**看到「插件命令 403 而 HTTP 放行」
    先查这条，不要当成 TL 的 bug。**
12. **`EligiblePermissions()` 无生产消费者，但不能删。** 唯一调用点是 `registry_test.go`，它
    断言的是**字面白名单集合**（与实现走不同路径），且守着一段真实事故史（旧实现是负面过滤 +
    `.override` 后缀表）。判据：**无生产消费者 ≠ 可删**，要看测试是否走了不同路径、是否守着
    事故史——与「自证式断言不算断言」互补。
13. **系统账号「恒 TL0」是构造性保证，不是断言保证**（D25 收窄后）：机器人（`kind != human`）
    由 `Grants` 直接不授予；系统账号（`builtin-auto` / `dev-auto`）`kind` 仍是 `human`，靠
    「没有 `user_activity` / `user_visits` 行 ⇒ 计数全 0 ⇒ 构造性停在 TL0」。**边界**：只保证
    「挣不到等级」，**不保证「不吃 TL0 限额」**——若走 forum 写路径会被新人限额拦（冷却 300s、
    日限额 3）。
14. **仓库已不能安全跑 `sqlc generate`**：本刀只手工同步了 `ListUserPermissions` 一个查询
    （小审逐字比对：形状正确，唯一差异是 sqlc 剥掉的 `ORDER BY 1` 分号）。当前 sqlc 版本会
    把 `database/sqlc/models.go` 从 84 行推到约 2481 行、重命名 param、留下一批未跟踪文件。
    要修单开一刀。
15. **deny 全量已修（D28）**：`identity.sql` 现在 `UNION SELECT permission_key, FALSE FROM
    denied_permission_keys`，所以「被 deny 但无静态授予」的键也进结果集。**纪律保留**：T4 的
    测试必须用「deny 一个没有任何静态授予的键」构造，用「角色授予 + deny」会假绿。
16. **限额已改为显式存储（D27）**：哨兵是 JSON `null`（`*int` + nil），**不是 0**——0 在限额里
    有语义（免冷却）。编码/解码两侧都要有 null 往返测试，否则后台表单会把 null 丢成 0，那是
    一次静默策略变更。
17. ~~资料页等级卡的数据源恒 `null`（M5 待办）~~ **已收口**：2026-11-11 09:21 实测
    `useTrustLevel.ts` 的 `loadViewer()` 真请求 `/trust-levels`，`SFProfileShowPage.vue:110`
    已传 `profile.value?.trustLevel`。**编号保留**，避免下文与计划 §5-T20 的「残留 17」引用断链。
18. **`GrantSource` / `PermissionOrigin` 是改名后的名字**（D30）：旧名 `CapabilitySource` 与
    `hostapi.CapabilitySource`（管插件能力集合与 secret 写门禁）撞名，编译能拦但 review 会
    持续误判。看到旧名先确认是不是在说 hostapi 那个。

## 已作废结论（读旧版规范或旧讨论前先看这里）

这刀计划改了 32 条决策、视觉规范冻了 4 次。**下一个会话最需要的不是终稿，而是「哪些说法
已经作废」**——否则会照着旧版规范重新实现一遍已被推翻的东西。

终稿规范：`../reports/2026-11-10-trust-level-visual-spec.md`（第四次冻结：39 键 / 6 指标 /
8 处登记点）。本节的底账来自 `../reports/2026-11-10-trust-level-superseded-claims.md`
（designer 整理，20 条）。**清单与终稿规范冲突时以规范为准。**

### A. 设计侧自纠（不要照旧版规范实现）

1. 「`runRestore` 会弹既有确认框」—— 错：它直接 `apply()` + success Toast，没有确认步骤。
   所以 `UAlert` 里那句「恢复默认不会影响用户已获得的等级」是 D12 的**唯一**披露点，
   禁止改写成 Toast。证据：`composables/settings/useSettingsSection.ts:60-68`。
2. `__maxed` 的 `margin:0` —— 错：与档位名零间距会被读成同一段文本。改 `margin:10px 0 0`
   （与进度块同高，替换时不跳动）。证据：`components/profile/SFTrustLevelCard.vue:270-272`。
3. 「窄屏 2 列 + 末项 `span 2`」—— 在 6 个指标下错：6 是偶数，硬跨 2 会造出半格空洞。
   规则改为**指标数为奇数时才启用 `span 2`**。
4. 「settings 子 tab 不进 V3 目录」—— 错：新增**组件**必须有 reviewed identity，否则
   `scripts/v3-catalog/generate.mjs` 直接抛错；登记点由 6 处改为 **8 处**（+2 条 catalog
   identity）。保留原结论的另一半：`SFAdminFixedTabNav` 本身不需要注册。
5. 「一指标一键 `trust.level.{level}.{metric}`」—— 错：实际是单个 JSON 选项键 `trust.levels`，
   值为 `{ schemaVersion, levels: [{ levelSlug, requirements, limits, earnable }] }`。
   证据：`TrustLevels/config.go`、`composables/admin/settings/useTrustLevelsThresholds.ts:12`。
6. 指标表里的 `account_age_days` —— **该字段不存在**；`Requirements` 只有 6 个指标，账龄只
   保留 `trust.new_user_days` 一个上限闸。证据：`TrustLevels/types.go` 的 `Requirements`。
7. 键数 36 → 41 → 39 —— 中间值作废。终稿 39 = 原 36 + 新增 5（`progress.text`、
   `likeGiven`×2、`bookmarkCount`×2）− 删除 2（`metric.accountAge`×2）。
8. 断点算式按 5 项、再按 7 项算过两版 —— 均作废。按 6 项重算：`6` / `3+3` / `2+2+2`。
9. TL1 显示名两度变更：「成员」→「正式成员」→ **「正式用户」**（终稿整组带「用户」）；
   slug 始终是 `member`，未动。

### B. 指令与实现被实测纠正

10. 「指标 7 个」—— 错，实际 **6 个**（多算了不存在的 `account_age_days`）。
11. 「i18n 审查断言看 `roles.*`」—— 该命名空间**不存在**。实际是 `admin.roleCatalog.*`，
    且产品文案把「角色」叫**用户组**（`admin.nav.roles` =「用户组」）。已进计划 D21。
12. 后台桌面网格「5 列 → 7 列」—— 两版都作废，终稿 **6 列**。核查时实现仍是 `repeat(5, …)`，
    与规范、与真实契约（6 个指标）三边不一致；唯一会说话的是 `DisallowUnknownFields`
    （保存被拒）。已转 frontend 改 6 列。**2026-11-10 复核：现为 `repeat(6, …)`，已对齐。**
13. `adminSurfaceCatalog.test.ts` 计数 48→49 曾疑似「新功能多算一条」—— 不是。那 +1 是徽章页；
    信任等级 tab 是**设置页内组件**不是 page，正确做法就是不进 page 级 placements，只进
    `ui-surfaces`。证据：`node scripts/v3-catalog/generate.mjs --check`。

### C. Go 侧注释/契约漂移 —— **backend 已修完，本条可删**

14. ~~后端把 TL2 的 slug 写成另一个词~~ —— 已改为 `resident`（复核 `types.go` 的 `nameSlugs`），
    `config.go` 注释同步。旧词已不在仓库里，按 `resident` 检索即可。
15. ~~`types.go` 注释称 options 键后缀为 `trust.level.<n>.<metric>`~~ —— 已改：注释明写该方案
    作废；`ThresholdOptionKey` 只用于**表单字段名与契约字段名**，不是 options 键。
16. ~~`types.go` 的 `NameKey()` 是无人消费的死生成器~~ —— 已删，**三个死生成器全部清掉**：
    `NameKey()` / `DescriptionKey()` / `ThresholdOptionKey()`。
    `ThresholdOptionKey` 的准确说法（提交级证据，`f63ad2cf9` 之后可断言）：
    **它从未进入任何提交的 Go 代码，仅存在于本刀的工作树（`policy.go:65`）并被删除；当前
    仓库中该字符串只出现在 `knowledge/` 的记录里。**
    验证命令：`git grep -c "ThresholdOptionKey" HEAD -- '*.go'` → 零命中；
    `git log -S"ThresholdOptionKey"` → 只有 `f63ad2cf9` 这一次提交，且命中文件全在 `knowledge/`。
    **纪律：自证式断言不算断言**（计划 §14 第 4 条）——断言自己的前缀证明不了任何事；
    判据是**有生成器 ≠ 有契约，看消费者不看函数**。

### D. 明确不做（是边界裁决，**不是待修项，别当 TODO 捡起来**）

17. 等级卡放进徽章 tab —— 否。等级是身份元数据，徽章只展示不给能力，混放会误导；
    落右栏（每个 tab 可见）。
18. 后台新开独立页 —— 否。需要第二套页头/工具栏，还改 V3 目录计数，收益为零；它是设置页内
    的固定 tab。
19. 后台画能力解锁勾选矩阵 —— 否。M4 只冻结阈值读写；能力映射未定稿前画矩阵就是第二套真相。
20. 在「`L<4` 且下一档全阈值 0」时写「已达最高等级」或「继续活跃即可升级」—— 否。前者是错的，
    后者是编的；等 M4 暴露「被上限闸压住」的信号再写 `nextHint`。

## 验收与证据（M8）

**原始证据在 `storage/qa/trust-levels/`**，该目录被 `.gitignore:118`（`storage/`）忽略，
**提交后不存在**——这是刻意的：提交的是**可复现的仪器**，不是几十兆日志。重跑即得，命令见下。

**最终数字有两个口径，两个都要知道**（否则下一个人会以为其中一份错了）：

| 来源 | 数字 | 口径 |
| --- | --- | --- |
| `../reports/2026-11-10-trust-levels-m8-final-status.md`（reviewer） | **124 行：ok 75 / 未实现 49 / FAIL 0** | 合并口径：reviewer 独立证据优先（浏览器实测 / 变异红绿 / 代码级 / 静态链路），其次运维 `_summary.txt`，两边都无则「未实现」+ 理由 |
| 运维 `_summary.txt` | ok 67 / 未实现 57 | 纯自动化基线：只算脚本实际跑到的矩阵 ID |

**重跑命令**（按序执行；证据落回 `storage/qa/trust-levels/`）：

```bash
# 1) 全量测试日志（A-3 的输入）
#    **-v 必须有**：不带它 go test 不输出 === RUN，A-3 的硬断言会判「测试没跑」并 FAIL。
./scripts/test-api-isolated.sh -v ./... > /tmp/iso.log 2>&1

# 2) 验收 + 摘要（生成 _summary.txt 与 _unimplemented.tsv）
SFORUM_TL_TEST_LOG=/tmp/iso.log ./scripts/verify-trust-levels-acceptance.sh

# 3) F-2 夹具与开关证据
./storage/qa/fixture-trust-earned.sh --apply --flag-off-evidence

# 4) D-8 阈值下探/恢复证据
./storage/qa/seed-trust-levels-matrix.sh --thresholds-only --lower-thresholds

# 5) G 组性能（D31：同一二进制翻开关做 A/B，不用 worktree、不加临时开关）
./storage/qa/trust-levels-perf.sh --capture-baseline && ./storage/qa/trust-levels-perf.sh --compare

# 6) 变异验证（6 条必须全 RED，改回再全绿）
./scripts/verify-trust-levels-acceptance.sh --mutations
```

> 第 1 步的 `-v` 不是排版偏好，而是 A-3 判据的**输入前提**：那条硬断言守的不是「断言是否
> 正确」，是「**断言是否真的执行过**」。本刀它当场抓到过一次——`-v` 被当成包选择传入，
> `=== RUN` 计数 0，看起来全绿实则一条没跑。

**证据升级（首次提交后必须做，`knowledge/README.md` 有同款约定）**：提交前的存在性结论是
**观察级**（`grep` 只证明「此刻没有」）；首次提交后必须回填**提交级**
（`git log -S` + `git grep … HEAD`），因为只有版本历史能区分三种情况：
**从未存在** / **曾经存在且已删** / **只存在于未提交的工作树**。
本刀的 `ThresholdOptionKey` 正是第三种——提交前说「零调用者、已删」是观察级，说「从未存在」
是错的，提交后才能给出唯一正确的说法（见「已作废结论」C 组 16）。

**方法学收获（比上面任何一条都耐用）**：验收矩阵与验收脚本之间也需要一条契约。本刀出现过
「矩阵 30 个测试名里 29 个不存在」，而 `go test -run` 匹配不到时**退出码是 0**，脚本会把它们
全记成 PASS。判据：**矩阵是唯一编号源，脚本按矩阵 ID 落盘，矩阵有而脚本没有的显式标「未实现」**
——省略会让人以为覆盖了。与「自证式断言不算断言」是同一条链：**没有执行证据的绿，不是绿。**

## D34 增量（2026-11-11，基线收窄 Z3-b″）

裁决：`../decisions/2026-11-11-trust-level-baseline-narrowing.md`；计划
`../plans/2026-11-11-trust-levels-baseline-narrowing.md`（§1 死锁、§4-bis 裁决）；
验收 `../reports/2026-11-11-trust-levels-baseline-narrowing-matrix.md`（N 组 16 条）。
代码提交 `185be5701`，文档提交 `69f455a42`。

**机制**（三句话）：

1. `TrustLevels/baseline_grants.go:23` 的 `BaselineGrants()` 是**代码常量**（两个键：
   `topic.create` / `attachment.upload`），**不进** `DefaultPolicy().Earnable`，后台不可配。
2. 装载期 `Identity/trust_level_grants.go` 的 `applyTrustGrants`（`:35`）→ `deductBaselineGrants`
   （`:58`）在等级已解析且低于 TL1 时从 `Permissions` 扣除；`mergeEarnedKeys`（`:78`）仍在同处合并 earned。
   **合并点只有一个**：`LoadActor` 与 `LoadEffectiveActorTx` 都走它。
3. **四条不扣**：开关关闭 / 等级未解析 / bot（`Kind.IsBot()`）/ 该键在
   `user_permission_overrides` 里是 `effect='allow'`（运营手动放开不被静默推翻）。
   第四条在事务装载里另有一份 `allowed` 集合（同批 override 行，不额外查库）。

**读面字段**：`GET /admin/trust-levels` 新增 `baselinePermissions`（`Http/Controllers/TrustLevels/dto.go:75`，
字段 `Baseline`）。它与 `eligiblePermissions` 同级、语义相反：后者是「允许被等级授予的白名单」（仍 7 键），
前者是「TL0 会被扣掉的基线键」。字面集合由 `TestAdminBaselinePermissionsLiteral` 钉住。
前端据此把这两列渲染成**静态门控标记**（无勾选框，不可取消），其余 5 键才是可勾选。

**双 loader 一致性修复**：`Identity/actor_postgres_tx.go` 的 `LoadEffectiveActorTx` 原先只
`SELECT status, kind`，`actor.CreatedAt` 全程未赋值 → 账龄被当成无限长 → 插件命令路径**永远不低于 TL1**。
本刀补读 `created_at` 并 `Scan(&actor.CreatedAt)`。修复前同一 TL0 用户在 HTTP 路径 5 键、事务路径 7 键。

**已知缺口（照实登记，别当成已修）**：

1. **后台用户详情仍把被扣的键算作生效**：详情页展示角色与用户级覆盖，不反映装载期减法，
   所以 TL0 用户详情仍显示「能发主题」而实际请求被拒。以实际请求为准。
2. **L5 / L8 两条路径不合并 earned**（`postgresTxStore.LoadCurrentUserAccess` 注释自认；CLI 无
   `WithTrustLevelSource`），本刀下表现为 fail-open（TL0 多键），不是挡人。清单见
   `storage/qa/trust-levels-z3/inventory-loaders.md`。
3. **`trust.new_user.forbid_attachments` 仍是死配置**（D20 未修）：减法门实现了它想表达的**行为**，
   但那条 option 本身依旧无消费者。别把本刀记成 D20 的修复。
4. **父→子继承缺口（residual 3）不变**：事务装载不做父→子展开，本刀只补了 `CreatedAt`。

## Important Paths

- 计划：`knowledge/plans/2026-11-10-trust-levels.md`
- 本刀：`knowledge/plans/2026-11-11-trust-levels-baseline-narrowing.md`、
  `knowledge/decisions/2026-11-11-trust-level-baseline-narrowing.md`
- 基线门：`apps/api/app/Models/TrustLevels/baseline_grants.go`、
  `apps/api/app/Models/Identity/trust_level_grants.go`
- 统计地基（已 landed）：`apps/api/app/Models/Badges/`、`../modules/badges.md`
- 裁决点：`apps/api/app/Models/Identity/policy.go`（`Can`）、
  `permission_compat.go`（父→子继承）
- 装载：`apps/api/app/Models/Identity/postgres_store.go`（`LoadActor`）、
  `postgres_store_roles.go`（`loadCurrentUserAccess`）、
  `database/queries/identity.sql`（`ListUserPermissions`，deny 的真身）
- 类型：`apps/api/app/Models/Identity/types.go`（`Actor`，M2 在此加字段）
- 待收编：`apps/api/app/Models/Forum/service_ops.go`（`trustForActor`）
- 配置：`apps/api/app/Models/Options/types.go`（`trust.new_user*` 七键）、
  `app/Models/Options/service_normalize.go`
- 后台 UI：`apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`
- 权限奇偶校验：`apps/api/database/migrations/permission_seed_parity_test.go`
