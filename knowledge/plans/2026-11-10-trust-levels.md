# 2026-11-10 任务书 — 信任等级（Discourse Trust Levels）

Status: **ready**（2026-11-10 立）
前置：`plans/2026-11-10-badges-and-user-activity.md`（统计层已 landed）
依赖输入：`user_activity` 六项计数 + `users.created_at`（**不含**新增表）

---

## 0. 目标与验收口径

给 SForum 在既有 RBAC 之上叠一层**动态、基于行为、自动升降**的能力层。

- **是**：按「累计行为」解锁/收紧**已有**能力；把现有「新人信任阶梯」（按注册时长两态）推广成 N 档。
- **不是**：第二套权限系统；不是徽章（徽章只展示，永不给能力）；不是审批流程。

验收口径（M8 逐条断言，不少于 70 条，0 FAIL）：

| 组 | 断言要点 |
| --- | --- |
| A 能力解析 | 静态授予 / TL 解锁 / deny 覆盖 / super_admin 四组合的 `Can()` 真值表 |
| B 边界隔离 | 已挣得集合与所有管理类权限键**零交集**（结构断言，不是抽查） |
| C 升降级 | 阈值上下各 1 的升降行为；可逆计数回退即降级；软删不降级 |
| D 限额收编 | 新人限额由 level 决定，`trust.new_user_days` 语义保持 |
| E 失败关闭 | `user_activity` 读失败 / 行缺失 → 已挣得集合为空 |
| F 开关 | `features.trust_levels` 关闭后行为**逐字节等价于本刀之前** |
| G 性能 | `LoadActor` 增加的开销实测（p95），不得让公开列表变慢。**基线是 4 次查询 → 5 次**（`created_at` 与 `kind` 合并成一次读，见 D29）；`LoadActor` 有 **45 个调用点**且单请求可能 ≥2 次调用，所以预算是 +N 不是 +1（N 须实测）。**测法：同一二进制翻 `features.trust_levels` 开关做 A/B**（D31），不用 worktree、不新增临时开关 |
| H 契约 | OpenAPI 引用、V3 目录计数、i18n 双语、架构基线 |

---

## 1. 核心判断：能力来源的单一权威（先读这一节，再读别的）

**问题**：每个 route guard、每个 policy helper 怎么知道「这个能力是静态授予还是 TL 解锁」？

**答案：它们不知道，也不需要知道。**

现状已核实：`apps/api` 非测试代码里有 **256 处 `.Can(` 调用点**（Forum 19+12、Moderation 14、AI 12、Identity 11、Jobs 9、Attachments 9+7、Extensions 7、UploadPolicy 7…）。复现命令：`rg -o "\.Can\(" --type go -g '!*_test.go' | wc -l` → 256（三种口径一致）。**writer 报的 226 是错的，验收断言别抄那个数。** 这 256 个点已经共享同一个权威 `identity.Actor.Can`。TL 必须是这个权威的**第四个授予来源**，不是并列的第二套检查：

```
Can(perm) = super_admin 绕过
          || 静态授予（角色 + 用户覆盖，deny 已在 DB 层生效）
          || 已挣得授予（TL 解锁，纯加性，永不否定）
```

**落地形态（D23/D13）：这条公式是概念式，实际在装载期求值一次**（`loadCurrentUserAccess` 里的 `applyTrustLevel`），结果写进 `Actor.Permissions` 有效集；`Can()` 本身一行未改。不要把它理解成「`Can` 内部逐次求值」——那会撞 `policy.go:29` 的 nil 早退，且违反 T3。

于是 route guard / policy helper / 领域服务**零改动**，仍然只问 `Can(perm)`。

**需要区分来源的只有两类消费者**，它们读一个**只读派生视图** `Actor.CapabilitySource(perm) → static | override | earned | none`：

1. 前端解释「为什么我不能发帖」（提示「再获 3 个赞可解锁」）；
2. 后台显示「这个能力谁能给、从哪来」。

派生视图不是第二权威：它只能解释 `Can()` 的结论，不能反过来影响它。

### 被否的三个方案（都是血泪）

| 方案 | 为什么否 |
| --- | --- |
| 在每个 guard 上挂 `RequireTrustLevel(n)` | 256 个点漏一个就是静默越权；且「能发帖但要 TL2」和「角色给了 `topic.create`」立刻互相打架 |
| 新开 `Actor.CanEarned()` 让调用方自己选 | 调用方必须懂两套语义 —— 这正是「两套权限互相打架」的定义 |
| 把 TL 解锁物化成真实权限行写进库 | 派生值变成第二份副本，需要写库 + 失效 + 对账，直接复制 `top_badge_id` 的三个失真窗口 |

**一句话纪律**：`Can()` 是唯一裁决点；来源只在装载时算一次；任何需要「知道来源」的地方读派生视图，不新增裁决点。

---

## 2. 现状核实（Lead 已在真实代码上核对；实施方仍须复核一遍再动手）

| 事实 | 证据 |
| --- | --- |
| 权限裁决在领域层，路由目录声明**不是**运行时守卫 | `app/Http/route_ingress.go:43` — `/api/v1` 直接 `c.Next()`，只做 planning |
| `Can` 的既有语义 | `app/Models/Identity/policy.go:22` — active 判定 → super_admin 绕过 → `hasExpandedPermission`（父权限继承子权限，`permission_compat.go:57`） |
| deny 覆盖已在 DB 层生效 | `database/queries/identity.sql:46-76` 的 `denied_permission_keys` CTE（经 `ExpandEffectivePermissions`，`postgres_store_roles.go:263`）。**原写 `postgres_store_permissions.go:49` 是错的**——那是 `ListActiveUsersWithPermission`（通知收件人查询），不是 actor 装载路径 |
| deny 的副作用形状 | deny 在**装载期**把键从 `Permissions` 里删掉，`Actor` 上**不保留**「哪些键被 deny」→ 这是 D14 的存在理由 |
| 默认 `member` 基线已含全部 7 个非管理内容键 | `seeds.go:153-161` `SeedMemberPermissions` → **TL 解锁现有键对默认安装是 no-op**（见 D17） |
| `LoadActor` 目前发 **4** 次查询（原写 2 次，已由 writer 实测更正） | `postgres_store.go:103` 主查询 → `loadCurrentUserAccess` 的 `ListUserRoleKeys` + `ListUserPermissions`（`postgres_store_roles.go:246-251`）→ `SELECT created_at, kind`（`postgres_store.go:380`） |
| **已存在一套「新人信任阶梯」** | `app/Models/Forum/service_ops.go:513 trustForActor`、`service_comment_limits.go`、`WithTrustPolicy`（`service.go:90`）、装配 `app/Providers/forum.go` |
| 该阶梯的配置面 | `app/Models/Options/types.go:115-121` 七个键：`trust.new_user_days`、`.topic_cooldown_seconds`、`.comment_cooldown_seconds`、`.daily_topic_limit`、`.daily_comment_limit`、`.forbid_outbound_links`、`.forbid_attachments` |
| 该阶梯的判据 | **只有账号年龄**（`actor.CreatedAt`），两态（新人/非新人），只产出限额与两个 forbid 位，**不产出权限** |
| 权限键落库有奇偶校验测试 | `database/migrations/permission_seed_parity_test.go`（`TestEverySeededPermissionReachesTheDatabase`） |
| 统计层就绪 | `app/Models/Badges/`（`activity_store.go` / `visits.go` 是读 `user_activity` / `user_visits` 的现成入口） |

### 2.1 由此得出的第二条核心判断

**现有「新人信任阶梯」就是 Discourse 的 TL0。** 不许并存两套动态阶梯：

- 今天：`age < trust.new_user_days` → 新人限额（冷却更长、日限额更低、禁外链、禁附件）
- 本刀后：`level == 0` → 同一组限额，数值来自同一批 option key
- 年龄从「唯一判据」降级为「上限闸」：`trust.new_user_days` 表示「最多当多久新成员」，活动量足够可以提前升到 TL1，但**不能提前越过 TL1**。

被否：让两套阶梯并存（TL 管权限、旧阶梯管限额）。那会让「这个用户现在受不受限制」有两个答案，正是本仓库最大的 bug 类。

---

## 3. 已锁定决策 D1–D12

**D1 单一权威**（见 §1）。`Can()` 合并四来源，deny 与 super_admin 语义不变。

**D2 TL 吸收新人阶梯**（见 §2.1）。`trustForActor` 改为 `limitsForActor`，按 level 取限额；七个 option key 全部保留、不迁移数据。

**D3 只复用现有权限键，不新增能力词汇。** TL 解锁集合必须是已存在的键（`topic.create` / `post.create` / `attachment.upload` / `topic.edit_own` 等），由 PM 用真实 catalog 定稿。本刀新增的权限键**只有** `trust.manage`。
被否：新造 `trust.level2.post` 这类键 —— 会让 `roleTemplates.ts`、`adminModules.ts`、权限本地化、后台角色页全部要认识一套影子词汇。

**D4 TL 是纯函数派生值，不落列、不缓存。**
`Level(activity Activity, accountAge time.Duration, policy LevelPolicy) int` 是纯函数，无 DB、无时间副作用（`now` 由调用方传入）。
读取位置：`LoadActor` 的第 3 次查询（`user_activity` 单行 PK）。
被否：`users.trust_level` 反规范化 —— 需要迁移 + 三个失真窗口 + 一次重算 job，而 TL 是**每请求都要正确**的东西，缓存它只会制造「刚解锁但还不能用」的假故障。
代价必须实测：每个已认证请求 +1 次 PK 查询。**若 p95 增量 > 1ms，本决策复议**（见 M7-G）。

**D5 降级语义**：只有**可逆计数**下降才降级（赞/收藏/访问天数不会因内容软删下降）。内容软删不降级；审核驳回不降级；封禁不降级（展示侧按 `status='active'` 过滤）。降级不回溯已有内容 —— 已发出的帖子仍在。

**D6 已挣得集合与「管理类权限」硬不相交。** 这是一条安全不变量，由**结构测试**保证（遍历权限 catalog，断言 earnable 集合 ∩ admin/manage 集合 == ∅），不是抽查。
被否：靠 code review 保证 —— 下一刀就会有人加一个 `moderation.review` 到 TL4。

**D7 失败关闭**：`user_activity` 读失败或行缺失 → 已挣得集合为空（**不是**按最高级放行）；`Actor` 零值 `TrustLevel` → 无已挣得权限。零值语义保证既有测试、插件桥、CLI、bot 构造点全部不改行为。

**D8 权限与限额分离**：权限回答「能不能」（`Can`），限额回答「多频繁/多大」（`TrustPolicyResolver`）。限额不进 `Can`。

**D9 开关与权限键**：`features.trust_levels`（`public: true`，推荐开）；后台写权限 `trust.manage`。
**不做 `trust.view`** —— 上一刀的教训：`badge.view` 是死权限（可达性依赖 `admin.access`）。后台读页复用 `admin.access` + 只读展示，不再造第二个死键。
关闭 `features.trust_levels` 后：已挣得集合恒为空、限额回落到本刀之前的语义（仍读 `trust.new_user_*`，按年龄两态）。**这是 F 组断言的判据。**

**D10 五档命名**：zh-CN **新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖**；en-US `Newcomer / Member / Resident / Veteran / Leader`；slug `newcomer / member / resident / veteran / leader`。
**初稿写的是「新成员 / 成员 / 常驻 / 资深 / 领袖」，2026-11-10 改**：`member` 与角色 key 同串、且「成员」与角色显示名「普通会员」同词根近义，运营在并列表格里会混。改成全档带「用户」的单调阶梯，与角色词族（会员/版主/运营/管理员）**零字面重叠**。

**D11 不做提名/晋升审批**（Discourse TL3 的 promotion 流程）。本刀纯规则、纯派生。

**D12 一键恢复推荐默认**（AGENTS.md 强制项）：后台阈值页必须能恢复到内置推荐值，并在 UI 明说「恢复默认不会影响用户已获得的内容」。

**D13 已挣得授予在装载期合并，`Can()` 零改动。**
合并点只有一处：`loadCurrentUserAccess`（`postgres_store_roles.go:245`）——它是 `Actor.Permissions` 与 `CurrentUser.Permissions`（下发前端 `can()`）的**唯一物化点**。合并在这里，两个消费者自动一致，`Can` 一个字节都不用改。
被否：`Actor.CanEarned`（调用方要选两套语义）；在 `Can` 里查库（热路径 256 点）。

**D14 deny 必须压过已挣得，且不改动静态路径的既有语义。**
装载公式固定为：

```
effective = ExpandEffectivePermissions(static_effective) ∪ (earned ∖ denied)
```

- `deny` 是**装载期**的事实（`identity.sql:46-76` 的 CTE 把它从候选里删掉），`Actor` 上不留痕。不显式把 denied 集合读出来，`earned` 就会复活被 deny 的 `topic.create` —— 这正是 T4 要禁止的，而 D6 的结构不变量挡不住（两者不是一回事）。
- 因此 `ListUserPermissions` 必须把**候选集 + 是否生效**一起返回（一次查询、deny 逻辑仍然只有一处）；不许新写第二个 deny CTE 副本。
- **顺序是硬的**：先展开父→子，再并 earned，最后减 denied。这样 earned 永远不可能触发父权限展开。
- **已知既有怪癖，本刀不修**：今天 deny 一个「由父权限展开出来的子键」（如 deny `settings.mail.manage` 而持有 `settings.manage`）会在展开后被加回来。修它会改变既有静态语义，超出本刀授权。登记为 residual，并写进模块笔记。

**D15 earnable 的「勾选面」必须与 `legacyPermissionChildren` 的父键零交集。**
父键（`settings.manage` / `extension.manage` / `user.manage`）一旦进入 earnable，已挣得会隐式展开出一串子权限。
**断言三个面，不能只断言一个**（PM 指出：只测配置结果会漏掉 UI 白名单）：
1. 后台**可勾选白名单** ∩ 父键 == ∅（PM 的 B9 —— 这是 D15 真正要防的面，父键本来就在管理类里，D6 会顺带挡住配置结果，但挡不住白名单漏配）；
2. **已配置的** earnable ∩ 父键 == ∅；
3. earnable ∩ 管理类 == ∅（D6）。

**D16 开关必须进 5 处，且完整性测试要覆盖全 catalog。**
`features.trust_levels` 要同时进：常量、optionDefinitions、结构体读取、`FeatureFlagCatalog()`、`featureFlagRecommendedDefaults()`。
**顺手修掉 `features.badges` 的既有缺口**（常量在、但不在后两处，`feature_flags.go:63-98`）——把完整性测试收窄到只验 trust_levels 就是「CASE 六分支只验一行」的复刻，上一刀已经吃过这个亏。

**D17 默认 earnable = ∅，但机制必须是活的（Z2）。**
PM 已核实：`SeedMemberPermissions`（`seeds.go:153-161`）已含全部 7 个非管理内容键，所以「TL2 解锁 `topic.create`」对默认安装是零行为变化的假功能。

裁决：
- **默认 earnable 为空集**，默认安装行为与本刀之前逐字节一致（F 组因此天然成立）。
- **后台必须提供 earnable 映射编辑入口**（每档可勾选哪些键）。不提供 = 机制是死代码，正是本仓库反复踩的「声明了却永不执行」。
- **Z3（收窄 member 基线、改由 TL1 授予）不做**，另开一刀。它的前置是重写 F 组口径（关闭开关 = 人人按 TL1 处理），否则一关开关全站会员不能发帖。写进 `Open Questions`。
- **产品诚实表述**：TL 默认价值 = 分档限额 + 等级展示 + 进度提示；权限解锁是框架能力，默认空集，由运营开启。

**D18 依赖方向：Identity 定义窄接口，TrustLevels 实现，bootstrap 装配。**
Identity **不许** import `TrustLevels`、`Badges`、`Options`（三者都 import identity）。形态：

```go
// identity 侧声明，不 import 任何域包
type TrustLevelSource interface {
    Grants(ctx context.Context, userID int64) (level int, earned []string, err error)
}
```

`LoadActor` 在 `level > 0` 时调用它；`TrustLevels` 侧实现里做「读 activity + 读 options 阈值 + 算 accountAge → `LevelOf()` → 映射 earnable」；bootstrap 注入。接口返回 error 时按 D7 fail-closed 到空集。

activity 的读：`Badges` 的 `GetCounters`（`activity_store.go:58`，无行返回全 0 不报错）**可直接复用**，不许写第二份 SQL。做法是 bootstrap 里的适配器把 `Badges.Activity` 转成 `TrustLevels.Activity`——**转换是编译期检查**，字段漂移会编译失败，比"列集合断言"更硬。

**D19 阈值策略用单个 JSON option 键 `trust.levels`，不用扁平多键。**
四档 × 七指标 + 每档限额 ≈ 44 个值。扁平键要过 options 的 5 道注册（types / definitions / defaults / normalize case / allOptionNames）≈ 220 处改动，且漏第 5 道会静默丢弃。
先例：`seo.footer_links` 就是 JSON 值 + `json.Unmarshal` 校验（`service_seo_footer_normalize.go:81`）。照抄这个形态：单键 + 严格解码 + schema 版本字段 + 未知字段拒绝。
被否：44 个扁平键（改动量与静默丢弃风险都不划算）。

**D20 `trust.new_user.forbid_attachments` 是死配置，本刀不接线。**
已核实：options 里有键、`TrustPolicy.ForbidAttachments` 有字段，但 `Forum.TrustPolicyResolver` 没有对应方法、`trustForActor` 从未设置、`Attachments/**` 零命中——**运营勾了完全无效**。
本刀不动它，理由：接线是**运行时行为变更**（会突然开始拦新用户附件），混进本刀会破坏 F 组基线。
与 D16 的区别要说清：修 `features.badges` 的目录注册**不改变运行时行为**；接 `forbid_attachments` **会**。前者顺手修，后者单开一刀。
登记为 Known Residual，并在模块笔记点名「这是第二个 `badge.view`」。

**D21 命名：`levelSlug`，不叫裸 `slug`；档位名与角色词族零字面重叠。**
TL1 的 slug 定为 `member`，与角色 key `member`（`202607040001:78`）是同一个字符串。代码不冲突（i18n 命名空间不同），但 API 里 `level.slug` 与 `roleKey` 语义完全不同。
- JSON 字段统一 `levelSlug`，**禁止裸 `slug`**；
- 档位显示名见 D10（已改成一整组「…用户」，与角色词族零重叠）；
- i18n 断言两条：① **机器可断言**——`trustLevels.*` 与 `admin.roleCatalog.*` 的 zh 值集合**交集为空**（脚本可跑）；② **人工必审**——同词根近义（成员/会员、管理/运营）机器抓不到，每加一个档位名人工过一遍。
  > 注意：`roles.*` 这个命名空间**不存在**（设计侧实测纠正）。真实键族是 `admin.roleCatalog.*`，且产品文案里「角色」叫**用户组**（`admin.nav.roles` =「用户组」）。

**D22 存在第二个 actor loader，必须在同一注入面上（P0，researcher 反向审计 S1）。**
`app/Models/Identity/actor_postgres_tx.go:112 LoadEffectiveActorTx` 是独立的 actor 构造路径：自己的 SQL，**不读 `created_at`/`kind`**，且**不调用 `ExpandEffectivePermissions`**（该文件内出现 0 次）。4 个非测试调用点，其中 `app/Support/HostAPI/v2_command_actor_postgres.go:96` 是插件命令授权（`:36` 用 `actor.Can(RequiredPermissions)` 校验），而**3 个命令要求 `attachment.upload`**（`v2_command_attachment_extensions.go:87/177/228`）。
→ 不接线，TL2 挣来的上传权会在插件命令路径 403、HTTP 路径放行 —— **两个答案**，而且藏在一个没有任何 earned 测试覆盖的 loader 里。
裁决：
- 该 loader 必须接收同一个 `TrustLevelSource` 并走同一套合并；4 个调用点全部传入（bootstrap 注入）。
- **不修**它「不展开父→子」的既有缺口（`extension.manage` 持有者在插件命令路径今天就被误拒）——那是独立缺陷，修它改变既有行为，另开一刀。
- 新测试：对同一用户，`LoadActor` 与 `LoadEffectiveActorTx` 的 **earned 键集合必须逐键相等**（只断言 earned 面；父→子继承的差异按上一条登记 residual，不混进这条断言）。

**D23 合并形状修订：`Actor.Permissions` 仍是有效集，来源靠 `Actor.Earned` 还原（P0，researcher S2）。**
D13 只说「在 `loadCurrentUserAccess` 合并」，但那个函数产出的是**扁平并集**，合并后就无法区分来源，`CapabilitySource` 会退化成「再查一次库」（违反 T3）。定型：

| 字段 | 内容 | 消费者 |
| --- | --- | --- |
| `Actor.Permissions` | **有效集** = `Expand(static) ∪ (earned ∖ denied)` | `Can()`（零改动） |
| `Actor.Earned` | **只含 earned 独占键**（`earned ∖ static ∖ denied`） | `CapabilitySource()` |
| `CurrentUser.Permissions` | **有效集**（同 `Actor.Permissions`） | 前端 `can()` 门控 |
| `CurrentUser.TrustLevel` | 等级数字 | 资料页等级卡 |

`CapabilitySource(perm)`：`Permissions[perm] && !Earned[perm]` → static；`Earned[perm]` → earned；否则 none。**不需要额外查询。**
两条硬约束：
- **`loadAdminUserAccess`（`postgres_store_roles.go:229`）永不合并 earned。** 后台详情必须显示「角色/覆盖授予了什么」，不能显示「他现在能做什么」。否则后台说「该用户没有 `topic.create`」而用户能发帖 —— 两个视图不一致。写进 ADR。
- `CurrentUser.Permissions` 必须是有效集（不是静态集），否则前端会隐藏 API 已经放行的控件。这是**故意**与后台详情不同。

**D24 earnable 是**正白名单**，危险键族永不进入（P0，researcher S6）。**
S6 是真的：`revisionOrigin`（`Forum/service.go:831-836`）**只比 ID** 判 self/staff，审计只在 staff 路径写；而「能否越过作者编辑窗口」看的是 `Can(post.edit_any)`（`service_ops.go:331-332`、`:556/560`）。两者不一致 ⇒ 一旦 `post.edit_any` 可挣得，就会出现**有特权、无审计**的编辑：绕过窗口、被当特权路径、但 `Origin` 仍是 `self` → 不写审计、不报错。
D6 原判据（`∩ admin/manage == ∅`）**抓不到** `edit_any`/`delete_any`（不含 manage 字样）。
裁决：
- earnable = **正白名单**，且只含内容创建类 7 键：`topic.create` / `post.create` / `attachment.upload` / `topic.edit_own` / `post.edit_own` / `topic.delete_own` / `post.delete_own`。**白名单之外一律不可挣得**，运营也勾不到。
- **危险键族显式清单**（不是 `strings.Contains` 判定）：`*_any`、`*_view_any`、`topic.lock`、`topic.pin`、`user.ban`、`user.manage`、`user.permission_override`、`moderation.*`、`settings.*`、`extension.*`、`admin.access`、`role.manage`、`badge.manage`、`trust.manage`。
- 断言：`白名单 ⊆ 正白名单` ∧ `正白名单 ∩ 危险键族 == ∅` ∧ `白名单 ∩ legacyPermissionChildren 父键 == ∅`（D15 三面）。

**D25 机器人不获得已挣得授予（researcher S7；2026-11-10 收窄）。**
> **收窄说明**：原写「机器人、系统账号」。实测 `builtin-auto` / `dev-auto` 的 `kind` 是 `human`（`202610020003_user_kind.sql:9` DEFAULT `'human'`），按 kind 挡不住它们；而给它们加 kind 需要迁移。裁决：**D25 只管 `kind != human`（bot）**。系统账号按「**没有 `user_activity` 行、没有 `user_visits` 行 ⇒ 构造性恒为 TL0**」保证——它们不发帖、不登录，挣不到等级。**这是构造性保证，不是断言保证**，写进 Known Residuals；若哪天有系统账号产生了活动行，那是新问题，不在本刀范围内。

bot 走 `LoadActor` ⇒ 会拿到 earned，而限额层有 `IsBot()` 旁路（`service_comment_limits.go:14-19`）⇒ **bot 会变成 TL4**。裁决：`TrustLevelSource.Grants` 对 `kind != human` 与系统账号（`builtin-auto` / `dev-auto` 等）直接返回 level 0 + 空集。加测试。

**D27 每档限额必须显式存储，代码里不许硬编码「TL3 冷却=0」。**
`trust.levels` JSON 的每档限额字段用**哨兵值表示「继承站点默认」**，默认全部继承；TL0 的数值来自既有 `trust.new_user_*`。
理由：代码把 TL3/TL4 冷却硬编码成 0，等于在代码里发明一条「高等级用户绕过站级冷却」的策略——运营在后台看不到、也关不掉。而计划 §5 明写「TL1–TL4 默认限额 = 站点默认，不叠加额外限制」。两处必须一致：**默认继承，要放宽由运营显式配置**。

**D28 deny 集合必须是全量，不是候选集内的（P0，researcher N2）。**
`database/queries/identity.sql:46-77` 只对 `candidate_permission_keys` 求 `effective`，`denied_permission_keys` 只出现在 `NOT EXISTS` 子查询里 ⇒ **「被 deny 但没有任何静态授予」的键根本不出现在结果里**，`applyTrustLevel` 的 `denied` 看不到它 ⇒ TL 挣得该键时 deny 被绕过。
而且 D22 落地的 `LoadEffectiveActorTx` 是**全量**收集 deny 的 ⇒ 同一用户同一键「HTTP 允许、插件命令拒绝」，方向在越权侧。
修法（一行 SQL）：`UNION SELECT permission_key, FALSE FROM denied_permission_keys`，或让 denied 直接来自现成的 `listPermissionOverrides`。
**T4 的测试必须用「deny 一个没有任何静态授予的键」构造**——用「角色授予 + deny」那种最自然的写法会**假绿**，因为该键在候选集里。

**D29 `Grants` 收 `kind`，`created_at` 与 `kind` 合并成一次读。**
`TrustLevelSource.Grants(ctx, userID)` 拿不到 `kind`，`CurrentUser` 也没有该字段，而 `applyTrustLevel` 在 `Kind` 读取之前执行 ⇒ D25 无处落地。定型：
- `Grants(ctx context.Context, userID int64, kind UserKind) (level int, earned []string, err error)`；
- `CurrentUser` 增 `Kind UserKind`，标 `json:"-"`（与既有 `CreatedAt` 同姿态）；
- `created_at` 与 `kind` **合并成一次查询**并在 `loadCurrentUserAccess` 之前完成 —— 顺带消灭 R7 的「同一请求读两次」，G 组基线维持 **4→5**。

**D30 命名去撞车：`GrantSource` / `PermissionOrigin`。**
`identity.CapabilitySource` 与**既有的** `hostapi.CapabilitySource`（`HostAPI/v1.go:95`，管插件能力集合与 secret 写门禁）同名。编译能拦，但 review 会持续误判。改名 `Actor.GrantSource()`，返回类型 `PermissionOrigin`。
顺带修 `capability_source.go:44-48` 的注释：注释写「同键双来源记 static」，代码 `return ...Earned`。因为 `Earned` 只存独占键，该分支不可命中——**行为对、注释错**，但将来「按注释修代码」就会造出第二个答案。

**D31 性能 A/B 用 `features.trust_levels` 开关，不用 worktree、不新增临时开关。**
开关必须在**调用 resolver 之前短路**（关闭时不发任何 TL 查询）。于是同一二进制翻开关即可得到 Δp95，且「关掉的功能不花查询」本身就是要的行为。
被否：worktree 起 HEAD 干净实例（旧二进制跑不动当前 dev 库的插件运行时，503 `plugin_runtime_convergence_failed`）；「绝对 p95 预算」（拿不到 Δ 就没法判 D4 复议）。

**D32 前端父→子展开的第三份副本，本刀顺手对齐。**
`apps/web/app/composables/**/usePermissions.ts:44-59` 是 `legacyPermissionChildren` 的第三份实现，且少 `entity_meta.manage`（Go 有 7 个，TS 只列 6 个），今天靠 `adminModules.ts:160` 与 `entity-meta.vue:39` 的 OR 兜住。
裁决：**补上，并加一条前后端展开表一致性测试**（不是只补一行）——只补一行下次还会漂。
**测试必须是三条腿，不是两份手写列表对拍**（reviewer 的护栏，已采纳）：`前端集合 == 后端集合 == 从 legacyPermissionChildren / 权限目录**派生**出来的集合`，三方相等。只有「派生」那条腿能抓到「两边都漏了同一个键」——两份手写列表可以同源写错，那种测试只是把 bug 抄了两遍。
与「不修 `Permissions["*"]` 合成 super_admin」的区别：那条会改变谁能进后台（F 组风险）；这条只是让前端与后端对同一个键给同一个答案。

**D26 `RestrictActor` 必须同步过滤 `Earned`（researcher S8）。**
`app/Models/APITokens/service.go:164 RestrictActor` 会把 `Permissions` 裁成令牌 scope 子集。若只裁 `Permissions` 不裁 `Earned`，`CapabilitySource` 会报出一个实际不生效的 earned 键（漂移）。裁决：同一处按同一集合过滤两者，加测试。

---

## 4. 数据模型

**本刀不新增表、不新增列。** 唯一需要动 schema 的地方是：无。

`Level` 的输入：

| 输入 | 来源 | 可逆 |
| --- | --- | --- |
| `topic_count` / `comment_count` | `user_activity` | 否（软删不回退） |
| `like_received_count` / `like_given_count` | `user_activity` | 是 |
| `bookmark_count` | `user_activity` | 是 |
| `days_visited` | `user_activity`（真值在 `user_visits`） | 否 |
| `accountAge` | `users.created_at` | 否 |

阈值配置存储：**单个 JSON option 键 `trust.levels`**（D19），不新建配置表 —— 与 `trust.new_user_*` 同域、同后台页、同恢复默认机制。形态照抄 `seo.footer_links`（`service_seo_footer_normalize.go:81`：JSON 值 + 严格解码 + 未知字段拒绝 + schema 版本字段）。

> **不要**退回「`trust.level.*` 扁平多键」：四档 × 七指标 + 每档限额 ≈ 44 个值，扁平键要过 options 的 5 道注册 ≈ 220 处改动，且漏第 5 道（`allOptionNames` 派生）会**静默丢弃**（T16）。

---

## 5. 能力映射表（已按 PM 的 catalog 复核定稿，见 D17）

**默认 earnable = ∅。** PM 已核实 `SeedMemberPermissions`（`seeds.go:153-161`）含全部 7 个非管理内容键，其余 8 个非管理键是员工权限（any/lock/pin/revision），进 earnable 会撞 D6。所以「解锁现有键」在默认安装下没有可解锁的东西。

| TL | 名称（slug） | 默认解锁 | 默认限额 |
| --- | --- | --- | --- |
| 0 | 新用户 `newcomer` | ∅ | 沿用 `trust.new_user_*` 七键现值 |
| 1 | 正式用户 `member` | ∅ | 站点默认（不叠加额外限制） |
| 2 | 常驻用户 `resident` | ∅（运营可配，受 D24 白名单约束） | 站点默认 |
| 3 | 资深用户 `veteran` | ∅（运营可配，受 D24 白名单约束） | 站点默认 |
| 4 | 领袖 `leader` | ∅（运营可配，受 D24 白名单约束） | 站点默认 |

三条硬口径：

1. **TL1–TL4 的默认限额 = 站点默认，不叠加额外限制。** 站点默认冷却与日限额都是 0（`content_limits.go:37-46`），所以「TL3 免冷却 / TL4 日限额放宽」这类文案在本仓库是假的，**从计划与文档里删掉**。默认安装只有 TL0 与 TL1+ 有差异，这是事实，照实写。
2. **earnable 的编辑入口必须有**（D17），否则机制是死代码。默认空集 + 可配置 = 框架正确形态。
3. **阈值默认值**以 PM 的 `reports/2026-11-10-trust-levels-capability-map.md` 为准（五档七指标，含每档数值依据）。`like_*` 在 TL1 故意为 0：依赖他人行为的指标不能当第一道闸；`bookmark` 从 TL3 起要求：避免上线即把老用户挡在门外。

---

## 6. 里程碑

| M | 内容 | 主责 |
| --- | --- | --- |
| M0 | 决策记录 ADR + 能力注册表声明形状（纯声明，无执行） | backend + pm |
| M1 | `app/Models/TrustLevels/`：纯函数 `Level()`、`LevelPolicy`、earnable registry、`user_activity` 读适配 | backend |
| M2 | Identity 接线：`Actor.TrustLevel` + `LoadActor` 解析 + `Can()` 合并 + `CapabilitySource` + D6 结构测试 | backend |
| M3 | Forum 新人阶梯收编：`trustForActor` → `limitsForActor` | backend |
| M4 | HTTP + OpenAPI：公开等级查询、后台阈值读写、恢复默认 | backend |
| M5 | 前端：资料页等级卡 + 进度、后台阈值页（固定 tab）、i18n 双语 | frontend |
| M6 | 目录/基线/知识库：V3 catalog 计数、架构 baseline、`modules/trust-levels.md`、ADR、handoff | writer + backend |
| M7 | 验证：验收脚本改造、六账号矩阵、变异验证、性能实测（G 组） | ops |
| M8 | 独立验收：≥70 断言 0 FAIL + 浏览器结构断言 | reviewer |

**M0 不得单独上线**：声明了却永不执行的注册表，比没有更糟（本仓库已有先例）。

---

## 7. 边界与失败模式

| 场景 | 行为 |
| --- | --- |
| `user_activity` 行不存在（从未发帖） | TL0，已挣得集合为空，**不报错** |
| `user_activity` 查询超时/失败 | 已挣得集合为空 + 记一条 warn；请求继续（fail-closed 到「无已挣得」） |
| `features.trust_levels` 关闭 | 已挣得集合恒空；限额回落到年龄两态 |
| 用户被 ban | `Can()` 本就因 `IsActive()` 为假返回 false；TL 不参与 |
| super_admin | 绕过全部，TL 无意义（但仍可被读取用于展示） |
| 降级瞬间有进行中的请求 | 每请求装载一次，无跨请求状态；降级从下一次请求生效 |
| 插件桥 / CLI / bot 构造的 `Actor` | 零值 TL → 无已挣得权限（与今天完全一致） |

---

## 8. 测试矩阵（最小但完整）

1. **真值表**（A 组）：静态 × 已挣得 × deny × super_admin，4×4 全枚举，表驱动。
2. **结构不变量**（B 组）：遍历 catalog，earnable ∩ admin == ∅。**必须做变异验证**：往 earnable 集合塞一个 `user.ban`，确认它红。
3. **阈值边界**（C 组）：每个 metric 在 `threshold-1 / threshold / threshold+1` 三点断言。
4. **升降级**（C 组）：可逆计数回退 → 降级；软删 → 不降级。
5. **失败关闭**（E 组）：store 返回 error → 空集合。
6. **开关**（F 组）：关闭后逐条对比本刀之前的行为。
7. **收编**（D 组）：`trust.new_user_days` 边界两侧的限额归属。
8. **隔离库**（必跑）：`./scripts/test-api-isolated.sh`。

---

## 9. 明确不做

- 提名/晋升审批、等级公告、等级历史表
- 阅读类指标（`posts_read_count` 仍恒 0，不参与 Level）
- 等级专属徽章联动（徽章永不给能力，反向也不做）
- 等级影响搜索权重 / 排序
- `trust_level` 反规范化列与后台重算按钮（派生值不需要重算）

---

## 10. 陷阱 T1–T12（动手前必读）

**T1 现有 `trust.*` 是第二套阶梯。** 改任何 TL 行为前先 `rg -n "trust\.new_user" apps/api apps/web` 全链走一遍（options types / normalize / 公开可见性 / Forum resolver / 后台 UI / i18n / 测试）。漏一处就是两个答案。

**T2 `Actor` 的构造点不止 `LoadActor`。** 测试、插件桥、CLI、bot、`component_permissions_test.go` 的 fake 都直接构造 `Actor{}`。零值 TL 必须等价于「无已挣得权限」，否则你会同时改掉几十个测试的语义。

**T3 `Can` 在热路径上（256 个调用点）。** 不许在 `Can` 里查库、读 options、拿锁。TL 只在装载时算一次。

**T4 deny 覆盖在 DB 层已生效**（`postgres_store_permissions.go:49`）。`Can` 里的已挣得合并**不能**让被 deny 的键复活。写测试证明这条。

**T5 新增权限键要过奇偶校验**：`TestEverySeededPermissionReachesTheDatabase`（`database/migrations/permission_seed_parity_test.go`）会抓「seeds 里有、迁移里没有」。`trust.manage` 要同时进 seeds、迁移、权限本地化、`roleTemplates.ts`、后台角色页。

**T6 fixture base schema 是第二份 schema。** 本刀理论上不动表，但一旦你的实现顺手加了列，`revisionLedgerFixtureBaseSchemaSQL` 会静默落后，症状是隔离测试红在毫不相关的地方（上一刀 T6/T7 原样重演）。

**T7 动了 SQL 或写事务，`go test` 绿不算绿。** 必须跑 `./scripts/test-api-isolated.sh`。

**T8 接收者上限**：给 `Identity.Service`（47）、`Forum.Service`（77）、`Options.Service`（48）加方法前，先 `grep` 当前方法数并与 `tests/architecture-boundaries-baseline.json` 的 `receiverMethodCaps` 对一遍。上一刀为此返工过一次。

**T9 变异验证是交付条件。** 至少两处：① B 组结构断言（塞一个管理键，看它红）；② `Level()` 的某个阈值分支（把 `like_received_count` 改成读 `like_given_count`，看它红）。改回再全绿。

**T10 守卫测试要先证明会误报。** 一个从没红过的守卫测试不能证明它能抓到问题。

**T11 前端 `defineEmits` 的类型化写法在这套 vue-tsc 下会吃掉载荷类型**（上一刀 T11）。需要传原生事件时用运行时声明。

**T12 新增后台页会改 V3 目录计数**（routes / UI surfaces / traceability）。生成目录 `--check` 与 `validate-v3-p0-catalogs` 会红，别当成回归。
**本刀已按 D9 决定不新开后台页**（进既有 `?tab=trustLevels`），所以这条只在你改主意时才适用。

### 审计补充（researcher 实测，计划原先没写）

**T13 `Can()` 里有个 `Permissions == nil → false` 的早退**（`policy.go:29`）。如果把 earned 实现成「`Can` 里额外查一个字段」，nil 分支会在到达它之前就返回 false。按 D13 在装载期合并就不存在这个问题——这是 D13 而非「`Can` 里加分支」的又一个理由。

**T14 deny 在 `Actor` 里不留痕**（同 D14）。`ListUserPermissions` 只返回幸存的键，denied 集合被丢掉。不显式读出来，earned 会复活被 deny 的键，直接违反 T4。**D6 的结构不变量挡不住这个**，两者不是一回事。

**T15 `trustForActor` 有 5 道判据，不是 1 道**（`service_ops.go:515/520/530`）：`CreatedAt` 零值跳过、bot 跳过、`days<=0` 关闭整条阶梯、年龄比较、`Can(settings.manage)||Can(topic.edit_any)` 豁免（员工不进新人限制）。M3 收编必须**逐条保留**——少一条就是「昨天注册的新版主被新人限流」。

**T16 options 嵌套键不支持通配**：每个键都要过 5 道（types / definitions / defaults / normalize case / allOptionNames 派生），漏第 5 道会**静默丢弃**。这是 D19 选单 JSON 键的直接原因。

**T17 `LoadActor` 有 45 个调用点**，单请求可能调 ≥2 次。G 组性能预算按 +N 算，别按 +1 算。

**T18 `Actor` 字面量构造点极多**（`Actor{` 664 处，非测试 98；`identity.Actor{}` 150，非测试 66）。带字段名的非测试字面量只有 3 处 → **只加字段 + 保持 nil-map 分支原样，零值 TL 语义可以不改任何既有测试**。这条要在 M2 交付时实测确认，不要假设。

**T19 数字纪律**：本文件里出现过三个被反复算错的数，一律以命令为准，不要互相转述：
- `.Can(` 非测试调用点 = **256**（`rg -o "\.Can\(" -g '*.go' -g '!*_test.go' | wc -l`；全仓含测试 280）。researcher 报的 226、writer 报的 226 都是错的。
- `LoadActor` 查询数 = **4**（不是 2）。
- deny 排除位置 = `database/queries/identity.sql:60-76`（不是 `postgres_store_permissions.go:49`）。

---

## 11. 派工与文件域（避免写冲突）

| 角色 | 文件域 |
| --- | --- |
| backend | `apps/api/app/Models/TrustLevels/`、`app/Models/Identity/{policy,postgres_store,types,seeds}.go`、`app/Models/Forum/service_{ops,comment_limits}.go`、`app/Http/Controllers/TrustLevels/`、`bootstrap/`、`database/migrations/` |
| frontend | `apps/web/app/pages/**`、`components/profile|admin/trust*/**`、`composables/admin/trust/**`、`i18n/locales/*.json` |
| pm | `knowledge/reports/2026-11-10-trust-levels-capability-map.md`、验收标准 |
| designer | `knowledge/reports/2026-11-10-trust-level-visual-spec.md` |
| ops | `scripts/verify-trust-levels-acceptance.sh`、`storage/qa/*` |
| writer | `knowledge/modules/trust-levels.md`、`knowledge/sessions/*`、`docs/` 双语 |
| reviewer | 只读 + 验收报告 |

**流程纪律（强制）**：本仓库已出现四次「计划、契约、实现三者不一致」，每次都是靠有人去读真实代码才发现。**每位员工在改任何描述前，先核实现，再改描述。** 计划里任何一行与实际代码冲突，以代码为准，并把冲突写回任务板 —— 不要照抄本文件。

---

## 12. 验收证据清单（M8 必须逐条给出）

1. `./scripts/test-api-isolated.sh` 全绿（不是 `go test`）
2. 变异验证两次的「红 → 改回 → 绿」记录
3. 真值表 4×4 的测试输出
4. D6 结构不变量测试（含变异）
5. `features.trust_levels` 关闭前后的行为对照（逐条）
6. `LoadActor` 性能实测（p95 增量，G 组）
7. 六账号矩阵（含 429 退避）与浏览器结构断言
8. `node tests/validate-architecture-boundaries.mjs` 与 `ruby scripts/validate-openapi-refs.rb` 全绿
9. 迁移可逆性：本刀无迁移 → 用 `scripts/verify-migration-reversible.sh` 证明**没有新增迁移**

---

## 13. 修订记录（读计划前先看这里）

本文件在 2026-11-10 一轮内被改了多次，**读之前先确认自己读的是哪一版**。以下按「谁促使改的」分组，便于核对旧口径：

| 修订 | 触发 | 内容 |
| --- | --- | --- |
| D13–D16 | writer 报 deny 复活缺口 | 装载期合并、deny 压过 earned、earnable 父键隔离、开关 5 处注册 |
| D17–D21 | PM 推翻能力表 / 设计侧命名核实 | 默认 earnable = ∅、依赖方向、单 JSON 键、死配置不接线、`levelSlug` |
| D22–D26 | researcher 反向审计（S1/S2/S6/S7/S8） | 第二个 loader、合并形状、正白名单、bot 恒 0、`RestrictActor` |
| D27–D32 | ops 的「计划≠代码」+ researcher 第二轮（N1–N5） | 限额显式存储、deny 全量、`Grants` 收 kind、命名去撞车、开关做性能 A/B、前端第三份副本 |
| §1 措辞 | ops 报「代码走装载期合并」 | 公式改成概念式 + 明确落地在装载期 |
| §5 表 | writer 报档位名未回填 | 五档名统一到 D10 修订版 |
| D29 签名 | reviewer 对码扫描 | 实现扩为 **4 参**：`Grants(ctx, userID, kind UserKind, createdAt time.Time)`。代码那版更好——`createdAt` 由装载方传入，resolver 不再自己读库。H14 的接口形态断言按 4 参写 |
| D29 措辞作废 | reviewer | D29 原写「顺带消灭 R7 的读两次」**是不实陈述**：`postgres_store_roles.go:234` 与 `postgres_store.go:412` 各读一次（= 收口项 ③）。修掉之前，任何「已消灭」的说法都不成立 |
| D27 哨兵值 | reviewer | 哨兵是 **JSON `null`**（`*int` + nil），**不是 0**。限额里 `0` 有语义（免冷却），`null` 是「继承站点默认」。后端表单把 null 当缺省丢掉再写回 0，就是一次静默策略变更 —— 编码/解码两侧都要有测试钉住 null 的往返 |
| D30 残留分支 | reviewer | `Identity/permission_origin.go:44-46` 的 `if a.Earned[permission] { return PermissionOriginEarned }` 在 `Earned` 只存独占键时**不可命中**。行为对，但它是「按注释修代码」就能造出的第二个答案 —— 删掉内层 `if`，或加一条不可达断言 |
| D21 的 en 例外 | PM 实测 | 「档位名与 `admin.roleCatalog.*` 交集为空」**只对 zh 成立**：`admin.roleCatalog.member.alias` 的 en 值是 `Member`，与 TL1 的 en 值 `Member` 相同 ⇒ **en 交集 = {Member}，已知且接受**。机器断言只对 zh 生效，判 FAIL 条件别写成双语 |
| i18n metric 键 vs API 字段 | PM 实测 | i18n 的 metric 键是 **camelCase**（`topicCount`/`likeReceived`/`daysVisited`），API `requirements` 是 **snake_case**（`topic_count`/`like_received_count`/`days_visited`）。写「i18n 键 == `Metrics`」的断言必红 |
| 三个死生成器 | PM/reviewer/designer | `NameKey()` / `DescriptionKey()` / `ThresholdOptionKey()` —— **三个都已删除**（`ThresholdOptionKey` 在 `policy.go:65` 曾存在，按 D30 尾项删除后全仓零命中）。判据：**有生成器 ≠ 有契约，看消费者不看函数**；且**存在性断言必须先确认函数存在，再确认谁在调用它** |
| `EligiblePermissions()` 无生产消费者 | designer | **但它不能删**：唯一调用点 `registry_test.go:44` 断言的是**字面白名单集合**（与实现走不同路径），且守着一段真实事故史（旧实现是负面过滤 + `.override` 后缀表）。判据：**无生产消费者 ≠ 可删**，要看「测试是否走了不同路径」与「是否守着事故史」——与「自证式断言不算断言」互补 |
| D25 边界 | reviewer | 「构造性恒 TL0」保证的是**挣不到等级**，**不保证不吃 TL0 限额**。系统账号若哪天走 forum 写路径，会被新人限额拦（冷却 300s / 日限额 3）。M3 收编时别把「TL0 限额」理解成「只影响真人」 |

**冻结约定**：backend 报 M2 落地后，D1–D32 冻结；此后新增决策一律追加到本节（带日期与理由），**不再原地改写既有条目**——否则每个读到一半的人都会拿旧口径验新代码。

**已被本文件自己推翻的说法**（看到请当过期）：
- 「TL 是 `Can()` 的第四个授予来源」的**逐次求值**读法（错，见 §1 落地形态）
- 「TL2 解锁 `topic.create` / `attachment.upload`」（错，默认 earnable = ∅，见 D17）
- 「TL3 免冷却 / TL4 日限额放宽」（错，默认不叠加，见 §5 + D27）
- 「阈值走 `trust.level.*` 扁平键」（错，见 D19）
- 「`LoadActor` 2 次查询」「`.Can(` 256 处以外的任何数字」「deny 在 `postgres_store_permissions.go:49`」（见 T19）

---

## 14. 证据纪律（2026-11-10 两次踩坑后定，适用于全队）

这一刀里「已落地 + 全绿」这句话错了两次，都是同一个形状：**编译/测试的绿与红的边界没分清**。

| 命令 | 覆盖 `_test.go` | 能证明什么 | 不能证明什么 |
| --- | --- | --- | --- |
| `go build ./...` | **否** | 非测试代码能编译 | 测试代码能不能编译 |
| `go vet ./pkg/` | **是** | 测试文件也能编译（无 arity/undefined 错） | 断言是否成立 |
| `go test -count=1 ./pkg/` | 是 | 测试真的跑了 | —— |

三条硬规则：

1. **`go build` 绿不等于 `go test` 绿**——`go build` 根本不编译 `_test.go`。改函数签名时**必须同批改调用点，包括测试里的调用点**。
2. **`go test` 一律带 `-count=1`**。缓存结果会让「上一版绿」冒充「这一版绿」。
5. **存在性断言必须带命令与时间戳，传播一次就衰减一次。** 本刀四个幻影（`account_age_days`、`roles.*`、`regular`、`ThresholdOptionKey`「是第三个死函数」）的成因完全相同：**某一刻为真的事实被当成永久事实转述**。`ThresholdOptionKey` 在我读到它时确实存在（`policy.go:65`），我据此下「零调用者、删掉」的指令；backend 执行删除后，设计侧复核时它已经不存在——两边的观察都对，错的是**没有人在转述时带时间**。判据：凡「X 存在 / X 不存在 / X 零调用者」，必须附命令与时间；消费方自己重跑一次。

   **命令给的是快照，不是历史**（设计侧的原话，比本节初稿更准）。`grep` 零命中只证明「此刻没有」，**不证明「从来没有」**：
   - 要断言「**已删除**」→ 必须同时给出删除前后两侧（`git log -S'<symbol>'` 或删除提交）；
   - 要断言「**从未存在**」→ 必须用 `git log -S` 扫全历史，而不是一次 grep；
   - 只给一次 grep 就写「根本不存在」，是用同一类错误去纠正前一个错误，而且更隐蔽——因为它有命令输出撑腰。
   正确写法：「截至 `<时间>`、用 `<命令>` 观察到零命中」。

   **本刀还有一个更硬的约束：整条工作流未提交，`git log -S` 无法裁决任何事。** `apps/api/app/Models/TrustLevels/` 目前是 `??`（全部未跟踪），`git log -S'ThresholdOptionKey'` 必然为空——**不管它曾经存不存在**。所以在这一刀里，唯一的历史是**文件 mtime** 与**各人带时间戳的观察记录**。
   推论：**观察必须在观察到的那一刻就记下时间**，事后无法补。`git log -S` 这套办法要等这 143 个文件提交之后才可用；在那之前，任何人写「从未存在」都是无证据的断言。

   **证据升级路径（设计侧提出，已采纳）**：首次提交后，凡「观察级」结论必须回填「提交级」证据——`git log -S"<symbol>" --oneline` 能指出引入提交与删除提交，证据强度从「某人某刻观察到零命中」升到「提交 X 引入、提交 Y 删除」。
   **不回填的后果**：再严谨的观察记录，半年后与传闻无异。所以这条不是可选项——**提交者负责回填**，且回填只补证据、不改结论。

   这条链的终点不是「找一个更强的命令」，而是：**任何断言都不该强于当时可用的证据形式**。
4. **自证式断言不算断言。** 这一刀出现过两次同形：`registry_test.go:31` 用 `IsAdminClassPermission` 验 `EligiblePermissions()`（而漏网原因正是同一个谓词），`registry_test.go:146` 断言 `NameKey()` 的前缀是 `trust.level.`（而那个键族在 locale 里根本不存在、无任何生产消费者）。判据：**测试必须与实现走不同路径**——用字面期望集合、用真实查询、用另一份派生来源。
3. **快照必须带时间戳，且在被消费前由消费者重新取一次**。Lead 转述的 sha256 只能当「某一刻存在过」，不能当「现在是这个」。**信号 = 消费者自己跑出来的绿 + 自己取到的 hash**，不是上游的报告。

违反这三条的后果已经实测过两次：一次是 19:57 报「M1 已落 + 全绿」而包内 11 处编译错误；一次是 20:09 报 checkpoint 后 3 分钟内 `Grants` 又改成 4 参、`resolver_test.go` 停在 3 参，而 Lead 拿 20:08 的 hash 当信号放行了审查。

### 追加 residual（第二轮审查后登记）

1. **系统账号的 TL0 是构造性的，不是断言的**（D25 收窄）。`builtin-auto` / `dev-auto` 的 `kind='human'`，靠「没有活动行」保证恒为 TL0。不要改成按 kind 挡——那要迁移。
2. **仓库已不能安全跑 `sqlc generate`**。v1.27 产出 `models.go` 84→2481 行、param 改名、一批未跟踪文件。本刀只手工同步了 `ListUserPermissions`（小审逐字比对：形状正确，唯一差异是 `.sql` 结尾 `ORDER BY 1;` vs 生成物 `ORDER BY 1`，属 sqlc 剥分号）。**单开一刀**：要么钉版本进 CI 只跑 `diff` 不改，要么明确声明生成物人工维护并删掉脚本。本刀不解决。
3. **`go build` 不编译 `_test.go`** —— 见 §14。

---

## 15. 实施期新增的硬规则（M4 收口时补）

1. **改 OpenAPI 描述后必须重跑 `scripts/v3-catalog/generate.mjs`。** 生成物 `routes.json` / `routes.md` 嵌了 OpenAPI 派生的摘要，改描述不重跑会让 `--check` 报 stale。这不是回归，是没重跑。
2. **权限判定必须在解码之前。** `Http/Controllers/TrustLevels` 初版把写权限留在 `options.Update` 里，于是「无权限 + 非法 body」会先返回 422、把 403 盖掉——**未授权用户能通过错误码差异探测配置格式**。已改为 `requireAdmin(c, permission)` 前置（`controller.go:151` 在 `DecodeConfig` `:161` 之前），顺序固定为：**产品开关（关闭 → 404）→ 权限（不足 → 403）→ 解码（非法 → 422）→ 业务**。
3. **`json.RawMessage` 优于指针，当需要区分「省略」与「显式 null」时。** `*LevelLimits` 的 nil 同时表示两者，做不到「省略 → 422、`null` → 继承」。定型为 `json.RawMessage`：`len(raw)==0` 判省略，显式 `null`/`{}`/`[]` 各自保留语义。
4. **反滥用面与等级阈值不是一类东西。** 等级阈值公开是为了让人知道怎么进步；限流参数（冷却秒数、日限额）公开等于告诉想绕的人该卡在哪。**能公开的是「你做过什么」，不该公开的是「系统怎么限制你」**（与 D1 不公开给赞数/收藏数同一判断）。

---

## 16. 收尾期发现的两条语义澄清（写进文档时照这个口径）

1. **「没有 `user_activity` 行 ⇒ TL0」是不精确的说法。** 真实语义是：没有行 ⇒ 六个计数全 0 ⇒ **由策略决定落在哪一档**。
   - 默认策略下 TL1 要求 `days_visited ≥ 3`，所以零活动账号是 TL0 —— 看起来像「fail-closed 到 TL0」，其实是阈值不满足。
   - **若运营把某档的全部阈值调成 0，零活动账号会直接落进那一档**（甚至 TL4），并拿到该档 earnable 的全部权限。
   这是构造上的必然，不是 bug，但必须让运营知道：**阈值全 0 = 「人人达标」**，不是「不启用」。
2. **D7 的 fail-closed 只管「解析失败」**（store 报错 → 已挣得集合为空），不管「零活动」。把两者混为一谈会让人以为零活动账号有特殊保护，而实际没有。

---

## 17. T6/T7 在本刀实际复发（2026-11-10）

计划 §12 的 T6/T7 写的是「fixture base schema 是第二份 schema 定义，主 schema 加列时会静默落后」。**本刀真的踩了一次**：

- D29 把 `LoadEffectiveActorTx` 的 `SELECT status` 改成 `SELECT status, kind`；
- `app/Support/HostAPI/v2_command_postgres_integration_test.go:444` 的 fixture 建的是 `users (id, status)`，**没有 `kind` 列**；
- 症状：4 个用例里 3 个炸在 `column kind does not exist`，另 1 个是既有红。

修法：fixture 补 `kind TEXT NOT NULL DEFAULT 'human'` 并留注释点名 T6/T7。**修前 4 FAIL → 修后 1 FAIL。**

**这条纪律因此升级**：改动任何**被测试 fixture 复制的查询**（不只是迁移）时，必须同时 `rg` 该查询的列名在 `_test.go` 里的 fixture 建表语句。**查询加列 = 可能有一份手写 schema 落后**，而它只会在跑隔离套件时才暴露。
