# 信任等级 — 能力映射表定稿 + 验收标准（M0/M8 共同输入）

Source: `plans/2026-11-10-trust-levels.md`（**已更新，含 D13–D20 / T13–T19；以文件为准**）
Owner: pm（阿策）
Status: **可执行，已按 lead 裁决 D15–D21 校准**。§1 是结论，§2 是计划与代码的逐条对照（含已裁决项），§3–§5 是定稿表，§6 是 A–H 验收条目清单（85 条 + 增补 9 + JSON 形状 6），§7 是产品边界。
Audience: backend（M1/M2/M3 实现与 D6/D15 结构断言）、ops（写验收脚本）、reviewer（M8 逐条执行）。

本文件不是计划复述。**每一行描述都先在真实代码上核过**，核不到的一律标「未核实」，不编。

### 来源标注约定（2026-11-10 增补）

本刀已出现两次「幻影字段/幻影键」从转述层一路传播到实现：`account_age_days`（不存在于 `Requirements`）、`trustLevels.*`（不存在于 i18n）。两者都是**转述层新增、代码里没有**。因此：

| 标记 | 含义 | 处理 |
| --- | --- | --- |
| **读代码** | 由我直接读 `apps/api/**` 或 `apps/web/**` 得到，附文件:行号 | 可直接作为断言依据 |
| **转述** | 来自计划、lead 消息、设计规范、或别处文档，未在代码中复核 | **必须复核后才能写成断言**；复核不符时以代码为准并记入不一致清单 |
| **未核实** | 无法在仓库内验证（外部行为、未实现部分） | 不进验收清单 |

凡本文件写「字段名 / 键名 / JSON 形状」，一律显式标 `读代码` 或 `转述`。

---

## 1. 结论先说（四条，都可独立验收）

1. **能力映射默认 = 全档空集，但机制必须是活的（D17 / Z2）**：TL0–TL4 默认**不解锁任何权限键**，同时**后台必须有每档的 earnable 勾选入口**。原因不是保守，而是**在现有 catalog 里，所有非管理类权限键都已经被默认 `member` 角色静态持有**（§3 给了逐键证据）。把 `topic.create` / `attachment.upload` 写成「TL2 解锁」，对默认安装是**零行为变化的假功能**——用户会看到「TL2 解锁发帖」而他从注册第一天就能发帖。
2. **产品诚实表述（对外与后台文案统一口径）**：*TL 默认价值 = 分档限额 + 等级展示 + 进度提示；权限解锁是框架能力，默认空集，由运营开启。* 任何页面不得暗示「默认安装里 TL2 能解锁发帖」。
3. **限额分档在推荐默认下只有两档**：TL0 严格 / TL1–TL4 全部等于站点默认（冷却 0、日限额 0）。「TL3 免冷却 / TL4 日限额放宽」在默认安装下是假文案，已从计划删除，本文件不再出现。
4. **Z3（收窄 `member` 基线、改由 TL1 授予）不做**，另开一刀；它的前置是重写 F 组口径（关闭开关 = 人人按 TL1 处理），否则一关开关全站会员不能发帖。已登记 `Open Questions`。

---

## 2. 计划 vs 代码：逐条对照（含裁决结果）

### 2.1 §5 解锁列与默认 member 基线冲突 → **裁决 D17（Z2）**

| 项 | 内容 |
| --- | --- |
| **计划（旧）说** | §5：TL2 解锁 `topic.create`、`attachment.upload`；D3 说「TL 解锁集合必须是已存在的键（`topic.create`/`post.create`/`attachment.upload`/`topic.edit_own` 等）」 |
| **代码是** | `member` 是默认注册角色，**默认就持有**这 7 个键：`apps/api/app/Models/Identity/seeds.go:153-161`（`SeedMemberPermissions` = topic.create, topic.edit_own, topic.delete_own, post.create, post.edit_own, post.delete_own, attachment.upload）；落库证据：`database/migrations/202607040001_identity_rbac.sql:105-110`（topic.create/post.create/post.edit_own/post.delete_own）、`202607120001_fine_grained_permissions_phase1.sql:47-52`（topic.edit_own/topic.delete_own）、`202607050004_attachments.sql:61-65`（attachment.upload） |
| **推论** | 权限目录里**非管理类**的键只有 15 个，其中 8 个是「对他人内容动手」的员工权限（any/lock/pin/revision，§3.2 列出），剩 7 个**全部已在 member 基线内**。所以「TL 解锁一个现有键」在默认安装上恒等于 no-op。 |
| **裁决（D17 / Z2）** | 默认 earnable = ∅；**后台必须提供每档 earnable 勾选入口**（不给入口 = 死代码）；**Z3 不做**；产品诚实表述见 §1.2。 |
| **本文件据此定稿** | §3.1 表格第三列 = 默认 ∅，第四列 = 后台可勾选范围（7 键），§7 边界第 9/10 条写死「默认文案不得暗示有解锁」。 |

### 2.2 §5 限额列在推荐默认下无差异 → **裁决：删（按建议）**

| 项 | 内容 |
| --- | --- |
| **计划（旧）说** | §5：TL3「免冷却（日限额仍生效）」、TL4「免冷却、日限额放宽」 |
| **代码是** | 站点默认冷却与日限额**都是 0（= 不限制）**：`apps/api/app/Models/Forum/content_limits.go:37-46`（`RecommendedTopicCooldownSeconds=0`、`RecommendedDailyTopicLimit=0`、`RecommendedCommentCooldownSeconds=0`、`RecommendedDailyCommentLimit=0`），并在 `content_limits.go:66-73` 进 `defaultForumSettings()`。只有运营手工调大这两个值，TL3/TL4 的「放宽」才有对象。 |
| **裁决** | 删除 TL3/TL4 限额差异；计划 §5 已改为「TL1–TL4 默认限额 = 站点默认，不叠加额外限制」，并写明「默认安装只有 TL0 与 TL1+ 有差异」。**机制保留**（每档有限额列，运营可调），只是不宣称默认差异。 |
| **本文件据此定稿** | §3.1 第五列五档全部写「站点默认」；§5.4 给出运营调值后会发生什么。 |

### 2.3 `LoadActor` 读取形态 → **已裁决 D18（不再是候选）**

| 项 | 内容 |
| --- | --- |
| **计划（旧）说** | D4：「读取位置：`LoadActor` 的第 3 次查询（`user_activity` 单行 PK）」 |
| **代码是** | 现成读取入口在 `apps/api/app/Models/Badges`：`activity_store.go:17-22`（`ActivityStore.GetCounters`）、`activity_store.go:55-58`（Postgres 实现，无行返回全 0 不报错）。而 `Badges` 的**声明依赖方向是单向的**：`apps/api/app/Models/Badges/activity.go:1-5` —「Badges 可以依赖 Identity / Jobs」。`LoadActor` 在 Identity 包（`postgres_store.go:380`），Identity → Badges 与该方向相反。 |
| **裁决（D18）** | Identity 侧声明窄接口 `TrustLevelSource { Grants(ctx, userID) (level int, earned []string, err error) }`，**不 import 任何域包**；`TrustLevels` 实现，bootstrap 注入；`LoadActor` 在 `level > 0` 时调用；activity 复用 `Badges.GetCounters`（**不许写第二份 SQL**），由 bootstrap 适配器做 `Badges.Activity → TrustLevels.Activity` 类型转换（**编译期检查**，字段漂移即编译失败，比列集合断言更硬）。 |
| **本文件据此定稿** | §6 原 H13（两处 SQL 列集合一致）作废，替换为增补条目 H14/H15（见 §6.9）。§2.3 的两条候选不再讨论。 |

### 2.4 `trustForActor` 的五道判据，计划（旧）只写了年龄一道 → **T15**

`trustForActor` 今天有**五道**判据。**M3 已落地**：函数已改名为 `limitsForActor` 并**按架构门禁抽到新文件** `apps/api/app/Models/Forum/service_trust_limits.go:23`（同文件还有 `newUserLimits:63` / `levelLimits:83` / `resolveLimit:101`；`service_ops.go` 1208→1129 行，`:511` 现在是 `containsOutboundLink`）。五道判据必须逐条保留：

| # | 判据 | 代码位置 | 漏掉的后果 |
| --- | --- | --- | --- |
| 1 | `s.trust == nil` 或 `actor.CreatedAt.IsZero()` → 不限制 | `service_trust_limits.go:23` 起（`limitsForActor` 入口） | CLI / 测试 / 内部构造点突然被限流 |
| 2 | `actor.Kind.IsBot()` → 不限制 | `service_trust_limits.go:23` 起 | 「新人禁外链」让机器人无法引用站内内容（AI 平台 M3 既有决定） |
| 3 | `days <= 0` → 整条阶梯关闭 | `service_trust_limits.go:63`（`newUserLimits`） | `trust.new_user_days=0` 时全员被当新人 |
| 4 | `time.Since(CreatedAt) > days` → 不受限 | `service_trust_limits.go:63`（`newUserLimits`） | 老用户被限 |
| 5 | `Can(settings.manage) \|\| Can(topic.edit_any)` → 员工豁免 | `service_trust_limits.go:63`（`newUserLimits`） | **昨天注册的新版主被新人限流**——最容易被验收抓到 |

已进 D 组断言（§6 D6/D7/D8），并在 §6.9 增补 E7（五道判据的存在性测试）。

### 2.5 metric 与 `features.*` 开关的联动 → 已采纳为规则

`like_received_count` / `like_given_count` 来自点赞（`features.reactions`，`Options/feature_flags.go:27`），`bookmark_count` 来自收藏（`features.bookmarks`，`feature_flags.go:29`）。这两个开关**关闭时对应计数恒 0**，于是 TL2/TL3/TL4 的点赞/收藏阈值永远达不到 —— 一个把 reactions 关掉的站点会永远没有 TL2+。

**规则（进 M1 的 `LevelPolicy`）**：每个 metric 声明来源开关；**来源开关关闭时该 metric 的阈值视为 0**。断言见 §6 H11/H12。

### 2.6 `features.badges` 目录缺口 → **裁决 D16：本刀顺手修**

先例缺陷（可验证）：`features.badges` 有常量（`feature_flags.go:32`）、有 `optionDefinitions` 条目（`:59`）、有 `FeatureFlags.Badges` 字段与读取（`:119`、`:141`），但**不在** `FeatureFlagCatalog()`（`:63-81`，只有 8 条）也**不在** `featureFlagRecommendedDefaults()`（`:88-98`，只有 8 条）。后果：它不参与「恢复推荐默认」与 `coerceFeatureFlagOptions` 归一。

**裁决**：本刀补齐 badges 两处注册 + **全 catalog** 完整性测试（不是只验 `trust_levels`）。与 D20（`forbid_attachments` 死配置不接线）的区别：修目录注册**不改变运行时行为**，接线**会**。断言见 §6 F8/F9。

**已落地（读代码复核，2026-11-10）**：`features.badges` 已进 `FeatureFlagCatalog()`（`feature_flags.go:81`）与 `featureFlagRecommendedDefaults()`（`:96`）；`features.trust_levels` 五处齐全（常量 `:35`、`optionDefinitions` `:63`、Catalog `:82`、RecommendedDefaults `:97`、结构体 `:130` + 读取 `:153`）。本节描述的缺口**已修复**，F8 现在是回归断言而不是待办。

### 2.7 命名：TL1 的 slug `member` 与角色 key `member` 同名 → **裁决 D21（终版 D10 命名）**

`member` 同时是默认注册角色的 key（`database/migrations/202607040001_identity_rbac.sql:78`，显示名「普通会员」）。风险不在代码（i18n 命名空间不同），而在**同一屏并列**与**第三方误读**：后台「用户组」页与等级阈值页会同时出现两套近义词；API 里 `levelSlug == "member"` 与 `roleKey == "member"` 语义完全不同。

**裁决（D21，终版）**：
- JSON 字段统一 `levelSlug`，**禁止裸 `slug`**；
- **zh-CN 五档整组改名为「…用户」单调阶梯**：新用户 / 正式用户 / 常驻用户 / 资深用户 / 领袖（初稿「新成员/成员/常驻/资深/领袖」作废——「成员」与「普通会员」同词根近义）；
- en-US 不变：`Newcomer / Member / Resident / Veteran / Leader`；slug 不变（`newcomer/member/resident/veteran/leader`，已冻结，改 slug 会牵动 i18n 键表 36 个键）；
- i18n 断言两条：① **机器可断言**——等级 i18n 键族与 `admin.roleCatalog.*` 的 zh 值集合**交集为空**；② **人工兜底**——同词根近义（成员/会员、管理/运营）机器抓不到，每加一个档位名人工过一遍。
- **键族以代码为准（读代码，不是转述）**：真实存在的只有**两个**键族，从 `apps/web/i18n/locales/{zh-CN,en-US}.json` 逐条走出：
  - 公开侧 `profile.trustLevel.*`：`title` / `name.{newcomer,member,resident,veteran,leader}` / `code` / `nextHint` / `maxed` / `earned` / `notEarned` / `ariaLabel` / `progress.{ariaLabel,valueText,text}` / `metric.{topicCount,commentCount,likeReceived,likeGiven,bookmarkCount,daysVisited}`；
  - 后台侧 `admin.settings.trustLevels.*`：`title` / `description` / `tl0Hint` / `restoreDefaults` / `restoreHint` / `zeroUnlimited` / `resetChanges` / `level.{五档}` / `metric.*`。
- **两个幻影键族都已撤回**：`trustLevels.*`（来自 D21 转述）与 `trust.level.<slug>.*`（我曾据 `Level.NameKey()` 写进断言，但 backend 已删除该生成器）。实测 `rg -c "trust\.level\." apps/web/i18n/locales/*.json` → **0**。`NameKey()` / `DescriptionKey()` 在 `types.go` 里已不存在。
- **两个陷阱，写断言前必读**：
  1. i18n 的 metric 键是 **camelCase**（`topicCount` / `likeReceived` / `daysVisited`…），而 API JSON 的 `requirements` 字段是 **snake_case**（`topic_count` / `like_received_count` / `days_visited`）。写「i18n metric 键 == `Metrics`」的断言必红。
  2. **en-US 侧交集不为空**：`admin.roleCatalog.member.alias` 的 en 值是 `Member`，TL1 的 en 值也是 `Member` → en 集合交集 = `{Member}`。D21 的机器断言只对 **zh** 成立（zh 交集为空）。这条必须写进文档，否则下一个人会把它当 bug 去「修」，或加一条 en 断言把它跑红。

**已核实（2026-11-10）**：真实键族是 `admin.roleCatalog.*`（`apps/web/i18n/locales/zh-CN.json:3157`），`roles.*` **不存在**；其 alias 值集合为 {版主, 站点运营, 技术管理, 普通会员, 超级管理员}（`:3159/3163/3167/3171/3175`），与新档位名集合 {新用户, 正式用户, 常驻用户, 资深用户, 领袖} **精确交集为空**，机器断言可全覆盖。另注：产品文案里「角色」称**用户组**（`admin.nav.roles` =「用户组」），文案审查统一用「用户组」。

落地断言见 §6 增补 H16（字段名）与 H17（五档双语 + 值集合交集为空）。

### 2.8 计划内部不一致：§4 的 `trust.level.*` vs D19 的 `trust.levels`

| 位置 | 写的是 |
| --- | --- |
| 计划 §4（`阈值配置存储` 段） | 「走既有 options 面（**`trust.level.*` 新键**），不新建配置表」 |
| 计划 D19 | 「阈值策略用**单个 JSON option 键 `trust.levels`**，不用扁平多键」 |
| 计划 T16 | options 嵌套键要过 5 道注册，漏第 5 道静默丢弃 → 这是 D19 选单键的直接原因 |

**以 D19 为准**（更具体、有 T16 依据）。§4 那句话是 D19 定稿前的残留，**已修（lead）**。补充一条更硬的结论：`trust.level.<n>.<metric>` 这个形状**在任何层面都不存在**——既不是 options 键（D19 用单 JSON `trust.levels`），也不是 i18n 键族（实测 locales 零命中），唯一产出过它的是 `ThresholdOptionKey()`，**该函数已被删除**（与 `NameKey()` / `DescriptionKey()` 一起，`rg` 全仓零命中，见 §2.9）。

### 2.9 三个已删除的「死生成器」：`NameKey()` / `DescriptionKey()` / `ThresholdOptionKey()`

这三个函数都在 `TrustLevels` 包里，都**只产出字符串、都没有消费者**——它们是「幻影键族」的生产线：

| 函数 | 状态（读代码，2026-11-10 实测） | 后果 |
| --- | --- | --- |
| `Level.NameKey()` | **已删除** | 我曾据它把 `trust.level.<slug>.name` 写进断言——这是第三个幻影键族的来源 |
| `Level.DescriptionKey()` | **已删除** | 同上 |
| `ThresholdOptionKey()` | **已删除**（`rg ThresholdOptionKey apps/api apps/web` 全仓零命中） | 它曾声称产出「后台表单字段名」，但从来没有消费者——真正的后台字段名来自前端 `useTrustLevelsThresholds.ts` 的 `TrustLevelsForm`。`types.go:87-88` 的 `Metrics` 注释也曾指着它说「与 options 键后缀一一对应」，同样在描述一个不存在的契约 |

**状态（2026-11-10 复核）**：三个函数**都已删除**，`types.go:87-88` 的注释也随之消失。本条派修**已关闭**，不需要 backend 再处理。

**留档的判据仍然有效**：判断「某个键族/字段是否存在」只看**消费者**，不看**生成器**——这三个函数曾经的存在本身就在暗示「有个键族长这样」，而它们从头到尾没有任何调用者。

**教训（与 §来源标注约定 同源）**：这三个函数的存在本身就在暗示「有个键族长这样」。**代码里有个生成器 ≠ 有个契约**——判据是「有没有消费者」，不是「有没有函数」。

---

## 3. 能力映射表定稿（D17 / Z2）

### 3.1 定稿表

| TL | 名称（zh / slug） | **默认解锁** | 后台可勾选范围（D17 编辑入口） | 默认限额 |
| --- | --- | --- | --- | --- |
| 0 | 新用户 / `newcomer` | **∅** | 7 个候选键（§3.2 #1–#7） | 沿用 `trust.new_user_*` 七键现值（冷却 300s/60s、日限额 3/30、禁外链 on、禁附件 off†） |
| 1 | 正式用户 / `member` | **∅** | 同上 | 站点默认（不叠加额外限制） |
| 2 | 常驻用户 / `resident` | **∅** | 同上 | 站点默认 |
| 3 | 资深用户 / `veteran` | **∅** | 同上 | 站点默认 |
| 4 | 领袖 / `leader` | **∅** | 同上 | 站点默认 |

† `trust.new_user.forbid_attachments` 是**死配置**（D20：options 有键、`TrustPolicy` 有字段，但 `Forum.TrustPolicyResolver` 无对应方法、`trustForActor` 从未设置、`Attachments/**` 零命中）。本刀不接线，运营勾了无效——后台 UI 必须标「暂未生效」或隐藏该开关，不能让运营以为它管用。

**可勾选 = 7 键；禁勾 = 44 键**（43 个管理/员工键 + `trust.manage` 自身）。总数 51 = 本刀后 catalog 规模，与 D16/H4 的计数一致。

### 3.2 为什么默认是空集：15 个非管理类键的逐键判定

权限目录的编译期权威是 `apps/api/app/Models/Identity/seeds.go`（`SeedPermissions`，50 条，行号如下）。**落库目录同样是 50 条**（已核实：`202607110010_moderation_workbenches.sql:31` 在 Up 段 `DELETE FROM permissions WHERE key = 'moderation.report_review'`，`:95` 的重新插入在 **Down 段**，正常迁移不会执行）。

| # | 键 | seeds.go 行 | 判定 | 依据 |
| --- | --- | --- | --- | --- |
| 1 | `topic.create` | 29 | 可勾选候选 → **但 member 已持有** | seeds.go:153-161；migration 202607040001:105-110 |
| 2 | `topic.edit_own` | 31 | 可勾选候选 → **member 已持有** | seeds.go:154；202607120001:47-52 |
| 3 | `topic.delete_own` | 34 | 可勾选候选 → **member 已持有** | seeds.go:155；202607120001:47-52 |
| 4 | `post.create` | 38 | 可勾选候选 → **member 已持有** | seeds.go:156；202607040001:105-110 |
| 5 | `post.edit_own` | 39 | 可勾选候选 → **member 已持有** | seeds.go:157；202607040001 |
| 6 | `post.delete_own` | 42 | 可勾选候选 → **member 已持有** | seeds.go:158；202607040001 |
| 7 | `attachment.upload` | 59 | 可勾选候选 → **member 已持有** | seeds.go:160；202607050004:61-65 |
| 8 | `topic.edit_any` | 32 | **禁勾**（员工） | 对他人内容动手 |
| 9 | `topic.delete_any` | 35 | **禁勾**（员工） | 同上 |
| 10 | `topic.lock` | 36 | **禁勾**（员工） | 版务 |
| 11 | `topic.pin` | 37 | **禁勾**（员工） | 版务 |
| 12 | `topic.revision.view_any` | 33 | **禁勾**（员工） | 读他人历史含已删内容 |
| 13 | `post.edit_any` | 40 | **禁勾**（员工） | 对他人内容动手 |
| 14 | `post.delete_any` | 43 | **禁勾**（员工） | 同上 |
| 15 | `post.revision.view_any` | 41 | **禁勾**（员工） | 读他人历史含已删内容 |

**结论**：可勾选候选 = 7 个，全部 ⊂ `SeedMemberPermissions` ⇒ 默认安装下勾选它们不产生任何可见变化 ⇒ 默认 earnable 定稿 ∅。

### 3.3 三条路线的最终状态（D17 已裁决，留档防回退）

| 路线 | 状态 | 说明 |
| --- | --- | --- |
| Z1（空集 + 不给入口） | **被否** | 机制建好却没有配置路径 = 死代码，正是本仓库反复踩的坑 |
| **Z2（空集 + 后台可勾选）** | **采纳（D17）** | 安全默认 + 运营可配；默认安装与本刀之前逐字节一致，F 组天然成立 |
| Z3（收窄 member 基线，改由 TL1 授予） | **不做，另开一刀** | 前置：重写 F 口径（关闭开关 = 人人按 TL1 处理），否则一关开关全站会员不能发帖。已进 `Open Questions` |

---

## 4. 管理类键完整清单（backend D6/D15 结构断言输入）

「管理类」的判定不是看 module 字段（`topic.lock` 的 module 是 `forum`，但它是版务权），而是看**它是否让他人/站点状态被改变**。定稿两列：

### 4.1 禁勾（43 键 + `trust.manage` = 44）

**A. 后台/设置/扩展/审核类（35 键，seeds.go 行号）**

```
admin.access(14)                role.manage(15)                 user.view(17)
user.manage(19)                 user.permission_override(21)     user.ban(22)
category.manage(23)             tag.manage(24)                   badge.view(26)
badge.manage(28)                moderation.manage(44)            moderation.review(45)
moderation.view_ip(47)          settings.manage(51)              settings.site.manage(52)
settings.mail.manage(53)        settings.avatar.manage(54)       settings.appearance.manage(55)
settings.notifications.manage(56) forum.settings.manage(57)      seo.manage(58)
attachment.manage(60)           attachment.settings.manage(61)   attachment.upload_policy.manage(62)
extension.manage(64)            extension.view(65)               extension.plugin.manage(66)
extension.theme.manage(67)      database.manage(68)              search.manage(69)
jobs.view(70)                   jobs.manage(71)                  entity_meta.manage(73)
identity.provider.manage(77)    ai.manage(81)
```

**B. 员工内容权限（8 键，module 是 forum 但属版务）**

```
topic.edit_any(32)  topic.delete_any(35)  topic.lock(36)  topic.pin(37)
topic.revision.view_any(33)  post.edit_any(40)  post.delete_any(43)  post.revision.view_any(41)
```

**C. 本刀新增（1 键）**

```
trust.manage（seeds.go 新增；D9 规定后台阈值写权限；自身禁勾）
```

> `badge.view` 归 A 列：它的可达性依赖 `admin.access`（D9 对 `badge.view` 死权限的判定），不是公开能力。

### 4.2 可勾选候选（7 键，默认全部已在 member 基线 → 默认不用）

```
topic.create(29)  topic.edit_own(31)  topic.delete_own(34)
post.create(38)   post.edit_own(39)   post.delete_own(42)
attachment.upload(59)
```

### 4.3 给 backend 的三条实现纪律（都来自真实陷阱）

1. **结构断言的 catalog 来源必须是 Go 常量（`SeedPermissions` / 显式键表），不许扫描迁移 SQL。** 迁移 `202607110010` 的 **Down 段**会重新 `INSERT moderation.report_review`，正则扫描全文件会得到 51 个键并误报「seeds 少一个」。这是本刀最容易写错的测试。
2. **断言用「两列集合交集为空」，不要写成 `strings.Contains(key, "manage")`。** 反例就在 catalog 里：`topic.lock`、`topic.pin`、`user.ban` 都不含 manage/settings/extension/moderation 字样。白名单式判定才能过 T9 变异（塞 `user.ban` 要红）。
3. **D15 的父键断言是独立的一条，不是 D6 的子集检查。** `legacyPermissionChildren`（`permission_compat.go:8-29`）的三个父键 `settings.manage` / `extension.manage` / `user.manage` 一旦进入 earnable，`ExpandEffectivePermissions` 会隐式展开出一串子权限（含 `settings.site.manage`）。D6 只查「earnable ∩ 管理类 == ∅」，父键本来就在管理类里所以会被 D6 顺带挡住；但 D15 的价值是**在 earnable 被运营配置后仍然成立**（运营只能勾 7 个候选，结构上就不可能勾到父键）——所以要断言「可勾选集合 ∩ 父键 == ∅」，而不是「earnable ∩ 父键 == ∅」（后者在运营配置后为真但测不出 UI 白名单漏配）。见 §6.9 B9。

---

## 5. 默认阈值表（五档，含保守性标注与调值后果）

### 5.1 定稿表

> **使用边界（2026-11-10 裁决）：本表是代码事实，不进运营页。** 运营页/使用文档**不印任何默认阈值数字**——阈值可配，文档印一份必然分叉，而分叉后没有编译报错会提醒任何人。运营页的正确形态是「以站点设置 → 信任等级页显示的当前值为准，该页有『恢复推荐默认』」。
>
> 本表存在的唯一目的：给实现者对照 `TrustLevels/policy.go` 的 `DefaultPolicy()`，以及给验收做 F6 的逐值比对。**它不是给运营抄的。**

`Level = 满足全部阈值要求的最高档`；阈值随档位单调不减（AND 累积），所以「从 TL0 直接跳到 TL2」在结构上不可能。

**「定位」列的含义**：`推荐值` = 有依据的常规默认，建议运营不要动；`保守值` = **故意压低或置 0** 以免误伤特定人群（改动前必须读 §5.4）；`最保守` = 上线初期最可能需要运营复核的一项。

| metric | TL0 | TL1 | TL2 | TL3 | TL4 | 定位 | 运营可调 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `topic_count` | 0 | 0 | 2 | 8 | 25 | TL1=**保守值**（保护潜水读者）；其余推荐值 | 是 |
| `comment_count` | 0 | 5 | 20 | 80 | 250 | 全部**推荐值** | 是 |
| `like_received_count` | 0 | **0** | 5 | 30 | 100 | TL1=**保守值**（故意为 0）；其余推荐值 | 是 |
| `like_given_count` | 0 | **0** | 3 | 20 | 80 | TL1=**保守值**（故意为 0）；其余推荐值 | 是 |
| `bookmark_count` | 0 | 0 | **0** | 5 | 20 | TL2=**保守值**（存量无数据）；TL3=**最保守**；TL4 推荐值 | 是 |
| `days_visited` | 0 | 3 | 15 | 45 | 100 | 全部**推荐值**（但最可能需要运营调整，见 §5.4 #1） | 是 |

> TL0 行的 0 表示「该档不设下限」，TL0 是**兜底档**：不满足 TL1 即落 TL0。

### 5.2 每档为什么是这个数

**TL1（3 天 / 5 条回复 / 点赞与收藏都不要求）**
- `days_visited ≥ 3`：`days_visited` 的真值来源是 `user_visits`（`202611100002_badges_and_user_activity.sql:34-40`，PK `(user_id, visited_on)` 天然幂等），**刷不出来**。3 个不同日期 = 「回来了至少两次」，是「不是一次性注册」的最低成本证据。
- `comment_count ≥ 5`：新人日限额是 30（`community_policy_options.go:118`），5 条只是它的 1/6；配上「跨 3 天」就变成日均 <2，不构成灌水门槛。
- `topic_count = 0`：**故意不要求（保守值）**。新人限制的目的是防灌水，不是防潜水；要求发主题会把「只回复的读者」永久锁在 TL0。
- `like_received = 0`、`like_given = 0`：**故意不要求（保守值）**。点赞依赖**别人**的行为，小站点上「没人点赞」会让所有用户永远升不了级（这是 Discourse 信任等级最常被吐槽的点）。点赞从 TL2 起才作为「常驻用户」的证据。
- **账龄不是任何一档的阈值**（已核实 `TrustLevels/types.go:78-85`：`Requirements` 只有 6 个字段，没有 `account_age_days`）。它是一个**独立闸**：`accountAge < trust.new_user_days` 时**最高只到 TL1**，`≥` 时**至少 TL1**（`level.go:23-40`）。所以它不出现在阈值表里，只出现在 §6 C17/C18/C19 的行为断言里。

**TL2（15 天 / 2 主题 / 20 回复 / 收 5 给 3）**
- `days_visited ≥ 15`：半个月的持续出现，是「常驻用户」的字面含义，也是本表唯一能证明「不是集中刷」的指标。
- `topic_count ≥ 2`：证明「不只回复」；2 而不是 3，因为小站点的正常用户一年可能只发 2 个主题。
- `comment_count ≥ 20`：约为新人日限额的 2/3，跨 15 天即日均 1.3，是「日常参与者」而非「爆发一次」。
- `like_received ≥ 5`：从这一档开始要求社区认可，但取 5（很低），避免新人被「别人的沉默」卡住。
- `like_given ≥ 3`：与 `like_received` 配对，防「只收不给」；3 只证明「会读别人的内容」。
- `bookmark_count = 0`：**保守值**。收藏是较新能力，存量用户没有历史数据；TL2 就要求会让老用户卡在 TL1。

**TL3（45 天 / 8 主题 / 80 回复 / 收 30 给 20 / 收藏 5）**
- `days_visited ≥ 45`：一个半月的持续出现。
- `topic_count ≥ 8`、`comment_count ≥ 80`：约为 TL2 的 4 倍，保证「资深用户」不是「常驻用户」的同义词。
- `like_received ≥ 30`：需要持续产出被认可的内容，单次爆款不足以达到。
- `bookmark_count ≥ 5`：**本表最保守的一项**。收藏是弱信号，只在 TL3 起要求，且取 5。若 `features.bookmarks` 关闭，该阈值自动降为 0（§2.5）。

**TL4（100 天 / 25 主题 / 250 回复 / 收 100 给 80 / 收藏 20）**
- 全部约为 TL3 的 2.5–3 倍，`days_visited ≥ 100`（百日）是「领袖」的直觉门槛。
- 这一档**不新增任何能力**（§3），只影响展示，所以取高值不会伤害任何人。

### 5.3 「保守值 / 推荐值」总表（给运营页与后台 UI 的文案素材）

| 档 | 保守值项 | 推荐值项 | 一句话文案 |
| --- | --- | --- | --- |
| TL0 | — | — | 「新用户的限制来自 `trust.new_user_*` 七个设置，不在这里」 |
| TL1 | `topic_count=0`、`like_received=0`、`like_given=0` | `days_visited=3`、`comment_count=5` | 「只看『是否回来过』，不要求发主题，也不依赖别人给你点赞」 |
| TL2 | `bookmark_count=0` | `days_visited=15`、`topic_count=2`、`comment_count=20`、`like_received=5`、`like_given=3` | 「常驻用户 = 半个月里持续出现，且有内容被认可」 |
| TL3 | `bookmark_count=5`（最保守） | `days_visited=45`、`topic_count=8`、`comment_count=80`、`like_received=30`、`like_given=20` | 「资深用户 = TL2 的数倍，且会用收藏」 |
| TL4 | — | 全部 | 「领袖 = 百日以上的持续贡献」 |

### 5.4 运营调值后会发生什么（后台 UI 提示文案的唯一来源）

| # | 调整 | 立即发生什么 | 建议 UI 提示 |
| --- | --- | --- | --- |
| 1 | **调高** TL1 `days_visited`（3 → 7） | 只靠 TL1 阈值维持等级的用户**上线即降级到 TL0**，立刻被套新人限额（冷却 300s/60s、日限额 3/30、禁外链）。这批人通常是轻度参与的老用户，**最容易被误伤且最不会反馈**。TL2+ 不受影响（他们满足 15 天）。 | 「调高后，部分老用户会重新受到新人发言限制」 |
| 2 | **调高** TL2 `days_visited`（15 → 30） | 一批 TL2 降 TL1。因为 **TL1 与 TL2 的限额完全相同**，用户**感知不到任何行为变化**，只有等级牌变化。 | 「只影响等级显示，不改变发言限制」 |
| 3 | **调高** TL3 `bookmark_count`（5 → 20） | TL3 人数骤降；TL3 既无能力也无限额差异，纯展示影响。 | 「只影响等级显示」 |
| 4 | **调高** TL4 任一指标到极高 | TL4 变空档，「领袖」成为死档。 | 「当前可能无人能达到最高档」 |
| 5 | **调高** TL1 `like_received`（0 → 3） | **最危险的一次调整**：等级开始依赖他人行为，小站点可能**永远无人升级**。 | 「此指标依赖其他用户点赞，活跃度低的站点不建议设置」 |
| 7 | **整体调低**（全部 → 0） | 所有人同一天升到 TL4。TL4 无能力、限额等于站点默认 ⇒ **无行为影响**，但等级失去意义。 | 「全部置 0 会让所有人立即达到最高档」 |
| 8 | 关闭 `features.reactions` 后仍把 `like_*` 设为非 0 | **无效**：规则是「来源开关关闭时阈值视为 0」（§2.5）。 | 后台应**禁用**这两个输入框并注明原因 |
| 9 | 关闭 `features.bookmarks` 后仍把 `bookmark_count` 设为非 0 | 同上（TL3/TL4 仍可达）。 | 同上 |
| 10 | 任意调值 + 「恢复推荐默认」（D12） | 等级按新阈值重算，**用户已获得的内容、已发出的帖子、已有徽章全部不变**。 | 「恢复默认不会影响用户已获得的内容」（D12 原话） |
| 11 | 调低阈值 | 用户瞬时升级；**不补发任何内容或历史权限**。 | 「升级只影响之后的发言限制与等级显示」 |
| 12 | 调值导致可逆计数回退（赞/收藏被取消） | 按 D5 降级；软删内容、审核驳回、封禁**不降级**。 | 「取消点赞可能让等级下降；删除帖子不会」 |

---

## 6. 验收标准（A–H，85 条 + 增补 9 + JSON 形状 6）

格式：**ID / 怎么测 / 期望 / 判 FAIL**。公共前置：`./scripts/api-dev.sh` 起的 API + 隔离库；每条断言必须能在**干净重置的库**上复现。

### A 能力解析（14 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| A1 | 构造 `Actor{Status: active, Permissions: {topic.create: true}}`，`Can("topic.create")` | true | false |
| A2 | 同上但 `Permissions` 为空 map，earned ∅ | false | true |
| A3 | 测试注入 earnable={topic.create}，level=2，静态无该键 | true | false |
| A4 | 静态有 + earned 有同一键 | true（不重复授予、不报错） | 报错 / false |
| A5 | DB 层写入 `user_permission_overrides(effect='deny', key=topic.create)`，静态角色有该键 | false | true |
| A6 | 同上 deny + earned 有该键（T4/T14） | **false** | true（earned 复活了 deny） |
| A7 | `RoleKeys=[super_admin]`，静态空、earned 空 | true | false |
| A8 | super_admin + DB 有 deny 覆盖（`policy.go:22-28` L6 注释） | true（super_admin 绕过 deny） | false |
| A9 | `Status != active` + 静态有 + earned 有 | false | true |
| A10 | `Actor{}` 零值，遍历全 catalog | 全 false；`CapabilitySource` 全 `none` | 任一 true |
| A11 | `CapabilitySource` 四值：静态角色 / 用户覆盖 / earned / 无 | 分别返回 `static`/`override`/`earned`/`none` | 值错位 |
| A12 | 同一 actor 遍历 51 键：`Can(k)` 与 `Source(k) != none` 必须同真值 | 完全等价 | 出现一个反例 |
| A13 | 纯加性：earned 有 X 时，任何原本 true 的键仍为 true | 无键从 true 变 false | 出现降级 |
| A14 | **默认安装**（earnable ∅）下，对本刀前的 50 键做 `Can` 快照，与上一 commit 的快照逐键对比（本刀新增的 `trust.manage` 单独断言为 false for 非管理员） | 50 键完全一致 | 任一键变化 |

### B 边界隔离（结构不变量，8 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| B1 | 遍历 `SeedPermissions`，断言 earnable ∩ 管理类 == ∅ | 空交集 | 非空 |
| B2 | earnable ∩ {topic.edit_any, topic.delete_any, topic.lock, topic.pin, topic.revision.view_any, post.edit_any, post.delete_any, post.revision.view_any} == ∅ | 空交集 | 非空 |
| B3 | **变异**：往 earnable 塞 `user.ban`，跑 B1 | B1 红 | 仍绿（守卫无效，T10） |
| B4 | **变异**：塞 `moderation.review`，跑 B1 | B1 红 | 仍绿 |
| B5 | earnable ⊆ seeds.go 的 50 键 | 是 | 出现新词汇（违反 D3） |
| B6 | 本刀新增权限键计数 | 恰好 1 个（`trust.manage`） | 0 或 ≥2 |
| B7 | 检查 B1 的 catalog 来源 | 来自 Go 常量/显式键表 | 来自正则扫描迁移 SQL（Down 段会带回 `moderation.report_review`，误报 51 键） |
| B8 | 默认安装读取 earnable 注册表 | 长度 0 | 非 0 |

### C 升降级（20 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| C1 | `days_visited` = 2 / 3 / 4，其余满足 TL1 | TL0 / TL1 / TL1 | 边界偏移 |
| C2 | `comment_count` = 4 / 5 / 6，其余满足 TL1 | TL0 / TL1 / TL1 | 同上 |
| C3 | `topic_count` = 1 / 2 / 3，其余满足 TL2 | TL1 / TL2 / TL2 | 同上 |
| C4 | `like_received` = 4 / 5 / 6，其余满足 TL2 | TL1 / TL2 / TL2 | 同上 |
| C5 | `like_given` = 2 / 3 / 4，其余满足 TL2 | TL1 / TL2 / TL2 | 同上 |
| C6 | `bookmark_count` = 4 / 5 / 6，其余满足 TL3 | TL2 / TL3 / TL3 | 同上 |
| C7 | 账龄闸上限：`accountAge` = 6d（< `trust.new_user_days`=7）且**满足 TL2 全部阈值** | **TL1**（活动够也不能越过 TL1，`level.go:23-30`） | TL2 |
| C8 | TL4 的每个 metric 各取 threshold-1（6 个 metric 分别） | 停在 TL3 | 升到 TL4 |
| C9 | 六个 metric 全满足 TL3 但 `like_received` = 29 | TL2 | TL3（AND 失效） |
| C10 | TL3 全满足，`days_visited` 回退 44 | TL2 | 不降级 |
| C11 | TL2 全满足，`like_received` 从 5 减到 4（可逆计数） | TL1 | 不降级 |
| C12 | TL2 全满足，`bookmark_count` 从 5 减到 4 | TL2→TL2（TL2 不要求收藏） | 误降 |
| C13 | TL2 全满足，软删一个 topic（`status='deleted'`），`topic_count` 不变 | 仍 TL2 | 降级（违反 D5） |
| C14 | 审核驳回一条 comment（`status='rejected'`），`comment_count` 不变 | 等级不变 | 降级 |
| C15 | 用户被 ban（`status='banned'`），读取等级 | 等级值不变 | 被改成 0 |
| C16 | `days_visited` 同一天访问 5 次 | 计 1（`user_visits` PK 保证），等级不变 | 计 5 |
| C17 | `age = trust.new_user_days`，活动全 0 | TL1（年龄闸） | TL0 |
| C18 | `age < trust.new_user_days`，活动满足 TL1 | TL1（提前升级） | TL0 |
| C19 | 非单调配置：把 TL2 阈值配得比 TL1 **低**，构造只满足 TL2 的用户 | TL2（取「被满足的最高档」，低档空洞不卡人，`level.go:34-39`） | TL1（被低档空洞卡住） |
| C20 | 降级后检查已发内容 | 内容全部仍在 | 内容被撤 |

> C7 取代了初稿的 `accountAge` 逐档断言：初稿假设账龄是 TL2/TL3 的阈值，实际实现是**闸**不是阈值（`Requirements` 无该字段）。「不能提前越过 TL1」由账龄闸实现，不需要新列，也不需要「首次达到 TL1 的时间」。

### D 限额收编（10 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| D1 | TL0 用户发主题两次（间隔 < 300s） | 第二次 `forum.topic_cooldown` 429 | 通过 |
| D2 | TL0 用户当日第 4 个主题 | 第 4 个 429 `forum.daily_topic_limit` | 通过 |
| D3 | TL0 用户发含 `http://` 的内容 | 被拒（`forbid_outbound_links` 默认 on） | 通过 |
| D4 | TL1 用户发主题两次（间隔 1s） | 通过（站点默认冷却 0） | 429 |
| D5 | `trust.new_user_days=0` | 全员 TL≥1，无新人限额 | 有人被限 |
| D6 | 新建 moderator（age=1d，持 `topic.edit_any`）发主题两次间隔 1s | 通过（判据 5，§2.4） | 429 |
| D7 | bot 账号（`kind='bot'`）发含外链内容 | 通过（判据 2） | 被拒 |
| D8 | 构造 `Actor{CreatedAt: zero}` 调用限额路径 | 不套 TL0 限额（判据 1，等价今天） | 被限 |
| D9 | TL0 用户 `Can("topic.create")` | **true**（限额不进 `Can`，D8 决策） | false |
| D10 | `features.trust_levels=off`，7 个 option 键逐项对比本刀之前的行为 | 逐项一致（age 两态） | 任一项不同 |

### E 失败关闭（6 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| E1 | fake store `GetCounters` 返回 error | earned ∅；记一条 warn；请求 200 继续 | 500 / 按 TL4 放行 |
| E2 | `user_activity` 无该用户行 | earned ∅，不报错 | 报错 |
| E3 | `GetCounters` 超时（context deadline） | 同 E1 | 阻塞请求 |
| E4 | `Actor{}` 零值 `TrustLevel` | earned ∅ | 非空 |
| E5 | 用 fake store 计数：`Level()` 调用一次 | DB 调用 0 次（纯函数，D4） | >0 |
| E6 | E1 场景下断言 earned 集合长度 | 显式 `== 0`（不是「level 被降为 0 但集合有值」） | 集合非空 |

### F 开关（9 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| F1 | `features.trust_levels=off`，遍历 51 键 `Can` | 与本刀之前快照完全一致 | 任一键变化 |
| F2 | off 时读 earnable | 长度 0 | 非 0 |
| F3 | off 时请求公开等级端点 | 404 或 403（按 M4 契约定稿） | 200 返回等级 |
| F4 | off 时打开资料页 | 不渲染等级卡 | 渲染 |
| F5 | off 时请求后台阈值读写 | 403 | 200 |
| F6 | on 时读默认阈值 | 等于 §5.1 表 | 任一值不同 |
| F7 | 读 `features.trust_levels` 目录定义 | `public: true`、推荐默认 = 开 | 缺 public 或默认关 |
| F8 | 检查 5 处接线：常量 / `optionDefinitions` / `FeatureFlagCatalog()` / `featureFlagRecommendedDefaults()` / `FeatureFlags` 结构体+读取 | 5 处齐全 | 缺任一（参考 `features.badges` 现缺 2 处，`feature_flags.go:63-98`） |
| F9 | 目录完整性测试：feature 常量数 == `FeatureFlagCatalog()` 条目数 | 相等（含 badges 补齐后） | 不等 |

### G 性能（5 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| G1 | 未认证请求公开主题列表 p95，与本刀之前对比 | 增量 ≈ 0（不触发 `LoadActor`） | >0.2ms |
| G2 | 已认证请求公开主题列表 p95 | 增量 ≤ 1ms（D4 复议线） | >1ms（触发 D4 复议，不是直接 FAIL 实现） |
| G3 | 已认证单请求的 SQL 计数 | `LoadActor` 2 → 3 次，其中 `user_activity` 恰好 1 次 | ≥4 次 / 0 次 |
| G4 | 检查新增索引 | 无新增（`user_activity` PK 已够） | 加了索引 |
| G5 | 一次列表请求（20 条主题）的 `user_activity` 读取次数 | 1 | 按主题数增长（N+1） |

### H 契约与门禁（13 条）

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| H1 | `ruby scripts/validate-openapi-refs.rb` | 退出码 0 | 非 0 |
| H2 | 公开等级查询响应字段与 `apps/web` 消费类型 | 字段名/类型一致 | 缺字段或类型不符 |
| H3 | 后台阈值写接口权限 | `trust.manage` | 用 `admin.access` 兜底 |
| H4 | `trust.manage` 四处齐全：seeds.go / 迁移 SQL / i18n 双语 / `apps/web/app/config/roleTemplates.ts`（若进模板） | 四处齐全 | 缺任一 |
| H5 | `TestEverySeededPermissionReachesTheDatabase` | 绿 | 红 |
| H6 | 五档名称 i18n：zh-CN + en-US 各 5 条（D10） | 各 5 条 | 缺语言/缺档 |
| H7 | `receiverMethodCaps`：Identity / Forum / Options 当前方法数 vs baseline | 不超上限（T8） | 超限 |
| H8 | `node tests/validate-architecture-boundaries.mjs` | 绿 | 红 |
| H9 | V3 目录 `--check` + `validate-v3-p0-catalogs` | 绿 | 红（新增后台页会改计数，T12） |
| H10 | `scripts/verify-migration-reversible.sh` + 迁移目录 diff | 无新增迁移 | 出现新迁移 |
| H11 | `features.reactions=off`，构造满足 TL2 其他阈值的用户 | 点赞阈值视为 0，可到 TL2 | 卡在 TL1 |
| H12 | `features.bookmarks=off`，构造满足 TL3 其他阈值的用户 | 收藏阈值视为 0，可到 TL3 | 卡在 TL2 |
| H13 | ~~两处 SQL 列集合一致~~ → **已由 D18 作废**，替换为增补 H14/H15 | — | — |

### 6.9 增补（lead 裁决后新增，9 条，不改变原 85 条）

| ID | 来源 | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- | --- |
| B9 | **D15 面 1** | 断言「后台**可勾选白名单** ∩ `legacyPermissionChildren` 三个父键（`settings.manage`/`extension.manage`/`user.manage`，`permission_compat.go:8`）== ∅」 | 空交集 | 非空（运营能勾到父键 = 隐式展开一串子权限） |
| B9b | **D15 面 2** | 断言「**已配置的** earnable（读 `trust.levels` JSON + 后台保存后的结果）∩ 三个父键 == ∅」 | 空交集 | 非空（配置被写脏或导入旧数据） |
| B10 | **D17** | 断言后台 earnable 白名单恰好 = §4.2 的 7 键（逐键枚举），且 `trust.manage` 不在其中 | 7 键完全一致 | 多一个或少一个 |
| E7 | **T15** | 五道判据的存在性测试：分别构造 ①`CreatedAt` 零值 ②bot ③`days<=0` ④age > days ⑤`Can(topic.edit_any)` 的 actor，各发两次主题 | 五次全部通过（不触发 TL0 限额） | 任一次 429 |
| H14 | **D18** | 检查 `app/Models/Identity` 的 import 列表 | 不含 `TrustLevels` / `Badges` / `Options` | 出现任一（循环依赖） |
| H15 | **D18** | 检查 activity 适配器：把 `Badges.Activity` 字段改名（变异）后 `go build ./...` | 编译失败（证明是编译期检查） | 仍能编译（说明用了反射或复制字段） |
| G6 | **T17** | 统计单请求内 `LoadActor` 调用次数（45 个调用点，单请求可能 ≥2 次） | 按 +N 计，不按 +1 计；给出实测 N | 预算按 +1 写（性能结论失效） |
| H16 | **D21** | 检查公开等级查询与后台阈值的 JSON 响应/请求 schema | 字段名是 `levelSlug`；全文无裸 `slug` | 出现裸 `slug` |
| H17 | **D21** | 读两个真实键族的五档显示名并取集合（**读代码**：`zh-CN.json` / `en-US.json`）：① 公开侧 `profile.trustLevel.name.<slug>`；② 后台侧 `admin.settings.trustLevels.level.<slug>` | 两侧 zh 值都 = {新用户, 正式用户, 常驻用户, 资深用户, 领袖}；en 值都 = {Newcomer, Member, Resident, Veteran, Leader}；**且 zh 值集合 ∩ `admin.roleCatalog.*` 的 zh 值集合 == ∅**（en 交集 = `{Member}`，**已知且接受**，见 §2.7） | 任一档值不符 / 两个键族不一致 / **zh** 交集非空 / 出现「成员」「会员」同词根近义 / 键名写成 `trustLevels.*` 或 `trust.level.*` |

### 6.10 JSON 形状断言（对拍新增，6 条，全部**读代码**）

`trust.levels` 是本刀最容易写错的一处：**同一个 JSON 里两种命名风格**（`requirements` 用 snake_case、`limits` 用 camelCase），且 `null` 与 `0` 语义不同。以下 6 条全部来自 `app/Models/TrustLevels/{types,config}.go`，不来自任何转述。

| ID | 怎么测 | 期望 | 判 FAIL |
| --- | --- | --- | --- |
| H18 | 解码 `{"schemaVersion":1,"levels":[...]}`，检查顶层与档位字段集（**读代码** `config.go:24-40`） | 顶层只有 `schemaVersion` + `levels`；`levels` 恰好 4 条（TL1–TL4）；**没有 `newUserDays` 字段**（新人窗口只来自 `trust.new_user_days`）；TL0 出现在 `levels` 里必须被拒绝 | 接受 TL0 / 出现 `newUserDays` / 允许 3 条或 5 条 |
| H19 | 往 `requirements` 塞 `"account_age_days": 3`（**幻影字段回归测试**） | `ErrInvalidConfig`（`DisallowUnknownFields` 拦截）；同时 `requirements` 的六个键必须与 `Metrics`（`types.go:93-100`）逐字一致 | 解码成功（幻影字段被静默接受） |
| H20 | 检查 `limits` 的四个键名（**读代码** `types.go:154-160`） | **camelCase**：`topicCooldownSeconds` / `commentCooldownSeconds` / `dailyTopicLimit` / `dailyCommentLimit`（与 `requirements` 的 snake_case 不同风格） | 写成 snake_case（`topic_cooldown_seconds`） |
| H21 | 分别用 `null` 与显式 `0` 配 `topicCooldownSeconds`，读回策略（**读代码** `types.go:154-160` 指针语义） | `null` → 沿用站点默认；`0` → 免冷却（**不是**「沿用默认」）；`EncodeConfig → DecodeConfig` 往返保真 | `0` 被当成「没配」/ 往返丢字段 |
| H22 | 给 `earnable` 分别塞：不存在的键 / `user.ban` / `settings.manage`（父键） | 三种都 `ErrInvalidConfig`（`Earnable.Validate` 的存在性 + 管理类禁令 + 父键禁令）；空数组 = 无解锁，合法 | 任一被接受 |
| H23 | 严格性：未知顶层字段 / JSON 后有尾随内容 / `schemaVersion: 2` / 负阈值 / 空串 | 前四种 → `ErrInvalidConfig`（**拒绝，不静默丢弃**）；空串 → `DefaultPolicy()` | 静默丢弃 / 负值通过 / 空串报错 |

**合计 100 条**（原 85 + 增补 9 + JSON 形状 6），满足 §0 的「不少于 70 条」。

---

## 7. 产品边界（写死，防止下一刀加回来）

| # | 边界 | 判据（有人违反时怎么看出来） |
| --- | --- | --- |
| 1 | **徽章永不给能力** | 任何 `user_badges` 行不得出现在 `Can()` 的输入路径里；`top_badge_id` 只进展示。反向也不做（等级不自动发徽章）。 |
| 2 | **TL 不做审批流程**（D11） | 没有 `trust_level_applications` 表、没有「申请晋升」端点、没有 `trust.approve` 权限。等级只能由纯函数算出。 |
| 3 | **不做等级专属排序/搜索权重**（§9） | 搜索与列表排序 SQL 里不得出现 level 条件；`HostQuery`/`search` 排序参数不接受 level。 |
| 4 | **不做等级专属徽章联动**（§9） | 徽章规则不新增 `trust_level` metric。 |
| 5 | **不落 `users.trust_level` 列、不做重算按钮**（D4） | 迁移里没有该列；后台没有「重算等级」按钮（派生值不需要重算）。 |
| 6 | **不新增第二套权限词汇**（D3） | catalog 里除 `trust.manage` 外不出现 `trust.*` 权限键；不出现 `trust.level2.post` 这类影子键。 |
| 7 | **不把限额塞进 `Can()`**（D8） | `Can()` 的签名与返回仍是 bool，不出现 `CanWithQuota` / `CanAndLimit`。 |
| 8 | **阅读类指标不参与等级**（§9） | `posts_read_count` / `read_time_seconds` 不进 `LevelPolicy`；资料页不展示「已读 N 帖」作为等级进度。 |
| 9 | **earnable 必须有编辑入口，且默认空集**（D17） | 后台存在每档勾选 UI；`FeatureFlagCatalog` 之外的默认值里 earnable 长度必须为 0。两个条件缺一即违反：**没有入口 = 死代码；默认非空 = 假功能**。 |
| 10 | **默认安装不得出现「解锁了但没有变化」的文案** | 资料页/后台不得写「TL2 解锁发帖」这类与默认基线矛盾的说明；文案只能描述**限额**（「再获 3 个赞可解除发帖冷却」）或**已由运营开启的解锁**（必须与后台配置联动，不能写死）。 |
| 11 | **限额差异不得宣称默认存在** | 任何页面不得写「TL3 免冷却 / TL4 放宽」；只能写「TL1 以上使用站点默认限额」。 |
| 12 | **`forbid_attachments` 不得悄悄接线**（D20） | 本刀内 `Attachments/**` 不得出现对该键的读取；后台该开关必须标注「暂未生效」。 |
| 13 | **默认阈值数字不得进运营页/使用文档** | 任何面向运营的页面/文档里不得出现「TL2 = 15 天 / 20 回复」这类具体数字；只能写「以设置页当前值为准」+「恢复推荐默认」。判据：`rg "days_visited\": *(15|45|100)" docs/` 零命中。数字的唯一副本在 `DefaultPolicy()`。 |

---

## 8. 拍板结果（已裁决，留档）

| # | 议题 | 裁决 | 落地位置 |
| --- | --- | --- | --- |
| ① | 解锁列 Z1 / Z2 / Z3 | **Z2**：默认 ∅ + 后台 earnable 编辑入口；Z3 另开一刀 | D17；本文件 §3.1/§3.3/§7-9 |
| ② | TL3/TL4 限额差异 | **删**：TL1–TL4 默认 = 站点默认，机制保留可调 | 计划 §5 三条硬口径；本文件 §3.1/§5.4 |
| ③ | `features.badges` 缺口 | **本刀顺手修** + 全 catalog 完整性测试 | D16；本文件 §6 F8/F9 |
| ④ | `trustForActor` 旁路 | 采纳，实际 **5 道判据** | T15；本文件 §2.4/§6 D6-D8/E7 |
| ⑤ | metric 与 `features.*` 联动 | 采纳：来源开关关闭 → 阈值视为 0 | 本文件 §2.5/§6 H11/H12 |
| ⑥ | D6 结构断言写法纪律 | 采纳，已转 backend | 本文件 §4.3 |
| ⑦ | `LoadActor` 读取形态 | **D18 定型**：Identity 窄接口 + TrustLevels 实现 + bootstrap 注入；activity 复用 `Badges.GetCounters` + 编译期类型转换 | 本文件 §2.3/§6 H14/H15 |
| ⑧ | D15 断言面 | **三面**（可勾选白名单 ∩ 父键 / 已配置 earnable ∩ 父键 / earnable ∩ 管理类） | D15；本文件 §6.9 B9/B9b + §6 B1 |
| ⑨ | 命名 | **D21 + 终版 D10**：JSON 字段 `levelSlug`（禁裸 `slug`）；zh-CN 五档 = 新用户/正式用户/常驻用户/资深用户/领袖；en-US 不变；slug 冻结不改；断言两个真实键族 `profile.trustLevel.name.*` / `admin.settings.trustLevels.level.*`（**读代码**），zh 值集合 ∩ `admin.roleCatalog.*` == ∅（en 交集 `{Member}` 已知） | 本文件 §2.7/§6.9 H16/H17 |

---

## 9. 回写建议（谁去改哪份文档）

| 文件 | 改什么 | 主责 |
| --- | --- | --- |
| `plans/2026-11-10-trust-levels.md` §4「阈值配置存储」 | ~~内部不一致：仍写 `trust.level.*`~~ **已修（lead）**：现为单 JSON 键 `trust.levels` + 照抄 `seo.footer_links` + 「不要退回扁平多键」。本文件 §2.8 保留为决策记录 | — |
| 同文件 §5 / D15 / D21 | ~~建议补 D15 三面、D21 命名~~ **已修（lead）**：D15 三面、D21 命名、§5 三条硬口径均已在计划中。本文件 §2.7/§4.3/§6.9 与之对齐 | — |
| 同文件 §2.1 | 补「5 道判据」（已由 T15 覆盖，§2.1 正文可加一句指向 T15） | writer |
| 同文件 §3 D4 | 标注「读取形态已由 D18 定型」，避免后来者按旧文再讨论候选 | writer |
| 同文件 §4 输入表 | 补「metric 来源开关联动」规则（§2.5） | writer |
| 同文件 §5 | 三条硬口径已写；建议补一句「阈值默认值的定位标注与调值后果见 PM 报告 §5.3/§5.4」 | writer |
| 本文件 | lead 后续裁决时同步 §3.1 / §5 / §8 | pm |
| 后台运营页文案 | §5.3（保守/推荐）与 §5.4（调值后果）是**唯一文案来源**，前端不得自行改写 | frontend |
