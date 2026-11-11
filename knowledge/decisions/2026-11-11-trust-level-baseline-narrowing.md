# 2026-11-11 信任等级：基线收窄按「减法门」落地（D34）

Status: **accepted**（YG 在本次会话裁决；本文件尚未提交，提交后以 `git log -1 --format=%cI` 为准）
计划：`../plans/2026-11-11-trust-levels-baseline-narrowing.md`（§1 死锁推导、§3 三候选、**§4-bis 裁决原文**、§4-ter 留档）
前置：`2026-11-10-trust-levels.md`（D1–D32）、`2026-11-11-trust-level-earnable.md`（D33）。
本文件是**追加**决策，不改写既有条目（D1–D32 冻结约定：新决策追加）。基线 `HEAD = 368434f25`。

## 问题

D33 走 A 路（earnable 编辑器）时把 B 路（收窄 `member` 基线，7 键改由 TL1 授予）记成「代价大、另开一刀」。
本刀验证的是：**B 路按原口径执行不是「代价大」，是「删掉默认安装里唯一真正生效的等级齿」**。

### 1. 静音推导（**首版写错，会话内自纠**）

先记错误：本计划 §1 首版把后果写成「**永久**锁死信任阶梯」。**这句是错的，会话内自纠**——
`LevelOf` 的账龄闸在 `NewUserDays` 期满后**至少落 TL1**（`level.go:19-31 @ 368434f25`，
sha256 `4a5ea093fee4…`，`:20-22` 账龄闸 / `:31` `return LevelMember`），
所以静音**是有界的**，默认安装下是 7 天（`community_policy_options.go:116 @ 368434f25`
`NameTrustNewUserDays: "7"`，sha256 `61367ca9e9f3…`），不是永远。方向不变、量级从「全站瘫痪」降到
「新用户 7 天不能发帖 + TL0 限额全成死代码」。留档理由：这是「期望算法漏输入」在本仓的**第四次同形**，
掩饰它等于让下一次重犯。

推导链（四条事实，逐条带取证方式）：

| # | 事实 | 位置 @ 368434f25 |
| --- | --- | --- |
| 1 | `Grants(level)` 对 `level <= LevelNewcomer` **直接返回 nil**：TL0 拿不到任何 earnable 键，与配置无关 | `TrustLevels/registry.go:56`（sha256 `b15add0f850f…`） |
| 2 | TL1 的默认门槛含 `CommentCount ≥ 5`（阈值数字只作判据，不进运营文档） | `TrustLevels/policy.go:11` 起 `DefaultPolicy`（sha256 `5ffa2c7b504a…`） |
| 3 | **回复走 `post.create`**：`CreateComment` 第一句就是 `if !actor.Can(identity.PermissionPostCreate)` | `Forum/service.go:953-956`（sha256 `01c41b146b45…`） |
| 4 | `features.trust_levels` 推荐默认 **enabled** | `Options/feature_flags.go:97`（sha256 `e79a403f215c…`） |

⇒ 收窄 `member` → TL0 没有 `post.create` → 不能回复 → `comment_count` 恒 0 → TL1 门槛永不满足 →
新人窗口内到不了 TL1。TL0 是每个新用户的起点。

两条后果：

1. **有界静音 ≠ 无害**：新注册用户 7 天内既不能开主题也不能回复，期满由账龄闸直接落 TL1。
   注册后第一帖发不出去，站点不会有人留下。
2. **TL0 的限额齿全部变成死代码**：`trust.new_user.topic_cooldown_seconds` /
   `daily_topic_limit` / `comment_cooldown_seconds` / `daily_comment_limit` / `forbid_outbound_links`
   的执行点都在 `limitsForActor`（`Forum/service_trust_limits.go:23`，sha256 `845fdb6cde7d…`）的
   `trust.active` 分支上，而**这些分支只在能发帖时才被走到**。TL0 发不了帖 ⇒ 五个配置键失去消费者，
   与 D20 的 `forbid_attachments` 死配置**同一失败类**，只是这次是我们自己造的。

> 本刀最反直觉的一条：默认安装里「信任等级有牙」**今天已经是真的**，牙长在 TL0 的限额上
> （冷却 / 日限额 / 禁外链），不在 earnable 权限上。B 路原样执行是把唯一真正生效的那副牙
> 连根拔掉，换上一副名义更彻底、实际空转的牙。
> 判据（沿用上一刀附录 A 第四句）：只要 `CreateComment` 仍用 `post.create` 鉴权、且 TL1 仍要求
> 回复量，任何「TL0 无 `post.create`」的实现都锁死——两个前提都是本刀不改的既有事实。

### 2. 第二发现：`SeedMemberPermissions` 是测试专用常量，不是落库真相

D33 与 capability-map 反复引用「`SeedMemberPermissions` 已含 7 键」——字面为真、机制误导：

| 对象 | 实际角色 |
| --- | --- |
| `Identity/seeds.go:157` 的 `SeedMemberPermissions` | **生产代码零消费者**，只被 `TestSeedMemberPermissionsStayNarrow` / `TestGrantSourceIsStaticForDefaultInstallWhitelistKeys` 读（sha256 `39b537c10e15…`） |
| 真实落库的 member 权限行 | 三条迁移：`202607040001_identity_rbac.sql`、`202607120001_fine_grained_permissions_phase1.sql`、`202607050004_attachments.sql` |
| 守护它的守卫 | `TestSeedMemberPermissionsStayNarrow` 名字叫 narrow，实际**硬要求 7 键全在**——它是「宽」守卫 |

⇒ 这是第五条「幻影」（前四条：`account_age_days`、`roles.*`、`regular`、`ThresholdOptionKey`）：
一个看起来是真相的常量，实际只有测试在读。任何「收窄基线 = 改一个常量」的估算都偏低。

## 决定 D34

D34 分两层：**产品口径**按 YG 原话落，**实施机制**改了一层并在此披露。

### D34-a 产品口径（YG 本次会话裁决，原文口径）

**默认安装下 TL0 不能开新主题、不能传附件；能回帖、能改/删自己的回复；TL1 起两者都有。**

### D34-b 实施机制：减法门（Z3-b″）

不采用「收窄角色行 + 由 TL1 授予」（加法版），改为**装载期减法门**：角色行、选项、迁移全部不动，
只在装载 actor 时对「等级已解析且低于 TL1」的会员**扣除**两键。

**机制与 YG 字面表述的偏差（必须明说）**：YG 说的是「基线收窄、由 TL1 授予」，字面指向加法版；
落地的是「基线不动、对 TL0 做减法」。**产品结果逐条相同**（TL0 少两键、TL1+ 有全部七键），
偏差只在机制层与默认 `earnable` 的表达（加法版会把两键写进 TL1 的默认 earnable，减法门不写）。

三条理由：

| # | 理由 | 具体后果 |
| --- | --- | --- |
| 1 | **失败方向**：论坛发言路径上 fail-open 才是对的 | 加法版漏改一条 loader ⇒ 该路径会员**发不了帖**（fail-closed）；减法门漏改 ⇒ TL0 **多**一个键（fail-open）。这是 P0 决策记录「新增一条没有防线的写路径」的镜像教训：宁可少收一个键，不可多挡一个人 |
| 2 | **零迁移** | 加法版要删 `role_permissions` 行 + 给存量安装做 `trust.levels` 选项迁移；减法门**角色行、选项、迁移全部不动** |
| 3 | **契约与 F 组不动** | 加法版让 `eligiblePermissions` 语义从 7 键变 5 键（J-01/J-02 重写）、F 组「关闭 → 已挣得恒空」必须重写；减法门下白名单仍 7 键、J-01/J-02 原样、F 组原样成立（关闭 = 不做减法） |

### D34-b 的五条口径（实施以这五条为准）

1. **基线常量**：`TrustLevels.BaselineGrants()` 返回 `{"topic.create","attachment.upload"}`
   ——**代码常量**，**不进** `DefaultPolicy().Earnable`（D17 的「默认空集」不动）。
2. **门控点**：装载期，`TrustLevelResolved == true` 且 `TrustLevel < LevelMember` 时，
   从有效权限里**扣除** `BaselineGrants()`。
3. **不扣的三种情况**：`features.trust_levels` 关闭 / 等级未解析 / bot（`Kind.IsBot()`）。
4. **显式 allow 覆盖优先**：`user_permission_overrides` 里 `effect='allow'` 的键不扣——
   否则运营「手动放开某人」会被静默推翻。
5. **合并点只有一个**：扣减与 `mergeEarnedPermissions`（`Identity/postgres_store_roles.go:343`，
   调用点 `:256`）走**同一处**共享辅助；`LoadActor`（`app/Http/auth_user.go:36`）与
   `LoadEffectiveActorTx`（`Identity/actor_postgres_tx.go:113`）都调它。
   **两处各写一份就是本仓第五次「两份」。**

## 被否方案与理由

| 方案 | 为什么否 |
| --- | --- |
| **Z3-a** B 路原样（7 键全部上移 TL1） | §1 死锁：新用户有界静音（默认 7 天）+ TL0 五条限额齿变死代码。**用一副空转的牙换掉一副真牙** |
| **加法版 Z3-b′**（收窄角色行，两键由 TL1 earnable 授予） | 产品口径对，机制选错：失败方向 fail-closed、需要迁移、要重写 `eligiblePermissions` 语义与 F 组。三条理由见 D34-b 表 |
| **Z3-c** 放弃权限齿，只做限额齿 | 产品上放弃「TL1 解锁能力」这件事本身；且要接线 `trust.new_user.forbid_attachments` 才能补上附件行为，等于绕远路还少一件事 |

三个候选**都不包含**：把 `topic.edit_own`/`topic.delete_own` 从 TL0 拿走。能发帖不能改错字
只会制造「发完发现错字只能重发」的双帖噪音——那不是牙齿，是自伤。

## 与 D17 / D24 / D9 / D33 的关系

| 既有决策 | 本刀后 | 说明 |
| --- | --- | --- |
| **D17**（默认 `earnable = ∅`） | **不变** | `BaselineGrants()` 是代码常量，不进 `DefaultPolicy().Earnable`；后台读面 `recommendedConfig` 的 earnable 仍是空集（`types.go:173-174` 注释不失效） |
| **D24**（earnable 正白名单封闭） | **不变** | 白名单仍 7 键（`registry.go:26-34`）；`BaselineGrants()` ⊂ 白名单；`*_any` / 置顶 / 锁定 / 封禁 / 审核仍永不进 earnable；`eligiblePermissions` 形状不动 |
| **D9**（关闭语义） | **字面不变、语义补强** | 关闭仍 ⇒ `TrustLevelResolved == false` ⇒ 已挣得集合恒空 + 限额回落年龄两态。补强的是：关闭 = **不做减法** ⇒ 七键全在。D33 说「B 路会把可选开关变成全站发言总闸」——在减法门下**不成立**，因为基线行没动 |
| **D33**（A 路：earnable 编辑器） | **保留**，但有两处被推翻 | 保留：编辑器、默认空集、累积语义披露、前端零硬编码白名单、`null`/`[]`/省略三态。**推翻 ①**：D33「被否：B 路」整节的**理由**——那一节说 B 路只是「代价大」，实际是 §1 死锁（见下方标注）。**推翻 ②**：D33「后果与残留」第 2 条「默认安装的等级差异仍然只有 TL0 与 TL1+ 之间的限额差异」——本刀后还有**权限**差异（少两键） |

**对 D33 的标注（按冻结约定追加，不原地改写原文）**：
`2026-11-11-trust-level-earnable.md` §「被否：B 路（Z3，收窄 `member` 基线…）」**已被 D34 取代**；
**取代理由是 §1 的死锁（机制不可执行），不是成本**。D33 该节列的「开关语义整体重写 + 数据迁移 +
混淆故障定位」三条成本判断，在减法门方案下都不再是必需项。

## 后果与残留

1. **升级后的存量 TL0 会突然不能开新主题、不能传附件**（有意变更）。必须进 release note；
   不接受这一点就只能选 Z3-c（放弃权限齿）。**已知有意副作用**：头像上传与附件共用
   `attachment.upload`（`Profile/service.go:497 @ 368434f25`），所以 TL0 也**不能上传头像**——
   这是同一把钥匙，不是第二个 bug；release note 要一并写。
2. **默认安装第一次出现权限层级的差异**：TL0 与 TL1 的权限集合不再相等。这是 D33 说的
   「等级解锁能力默认空操作」在产品层的修正——但**只修正到两键**，其余五键仍在基线。
3. **D20 的 `forbid_attachments` 仍是死配置**：减法门实现了它想表达的行为（附件要到 TL1），
   但那条 option 本身依旧无消费者。**不要把本刀记成 D20 的修复。**
4. **父→子继承缺口（residual 3）与本刀相交**：收窄后插件命令路径也必须拿到基线（否则插件侧
   对 TL0 的判断与 HTTP 侧不一致）。这条只能靠「合并点唯一」+ 装载路径清单判死，
   见 N-05（范围以 researcher 的 `storage/qa/trust-levels-z3/inventory-loaders.md` 为准，
   该文件落盘前 N-05 记 `pend`）。
5. **风险集中在「少扣」而不在「多扣」**：漏掉一个 loader 的后果是 TL0 多一个键（fail-open），
   不会挡人；但会让「TL0 少两键」在个别路径上不成立。验收必须按真实 `Forum.Service` 调用
   路径断言（N-02/N-03），并配 N-01/N-04 对照组——没有对照组，「一律扣除」也会让 N-02/N-03 绿，
   那是假守卫。
6. **验收侧返工面比「F 组」三个字大**：researcher 的
   `storage/qa/trust-levels-z3/inventory-f-group.md` 判定 F 组只重写 1 条（F2）+ 新增 1 条，
   但同一语义簇在 D 组有 5 条连带（D9 重写、D1/D2/D3 前提消失、D10 失去对照物）——
   合计 **7 条断言 + 1 份证据文件**（`storage/qa/trust-levels-flag-off-evidence.txt` 的 `earned=empty`
   是由 403 反推的，基线复原后必失效）+ **脚本三处同 ID 实现**（`scripts/verify-trust-levels-acceptance.sh`
   的 `:1132-1150` F-2、`:1152-1154` 假 F-3、`:2038` 真 F3）。**D 组那 5 条不是本刀弄坏的回归，
   是旧判据的地基（甲 = TL0 能发主题）没了。** 条目见矩阵 N-07..N-12。
7. **不做**：不改 D24 白名单封闭性；不做 `/badges` 公共目录页；不跑 `sqlc generate`（residual 4）。

## 实施要点（给下一位）

- 基线常量只放一处：`TrustLevels.BaselineGrants()`。**禁止**在 `Identity`、`Forum`、前端各抄一份
  ——那是第六份「两份」。
- 减法与 `mergeEarnedPermissions` 共用同一辅助函数；`LoadActor` 与 `LoadEffectiveActorTx` 都调它。
  守卫形态参照 `TestOptionsErrorMappingHasSingleOwner`：**加回第二份实现必须红**。
- bot 判定用既有 `Kind.IsBot()`，不要新造「是不是机器人」的第二个判据。
- allow 覆盖的优先级必须显式测：给 TL0 加 `user_permission_overrides(topic.create, allow)` 后能开主题。
- 关开关 = 不做减法：`features.trust_levels=disabled` 时 TL0 仍能开主题。这条是 F 组的延伸断言，
  **不是**对 D9 的改写。
- 证据要按 R10 落红态：把扣除条件改坏（去掉 `TrustLevel < LevelMember`）→ N-02/N-03 红在点名句；
  删掉「不扣 allow 覆盖」→ allow 条目红在点名句。
- 文档侧沿用 D33 最后一条：**不印阈值数字、不印端点路径以外的东西**，以站点设置页显示的当前值为准。
