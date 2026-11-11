# 2026-11-11 信任等级下一刀：B 路（Z3）基线收窄

Status: **accepted（D34 = Z3-b′ 产品口径 + Z3-b″ 减法门机制；YG 在本次会话裁决，见 §4-bis）**。实施中。
基线：`HEAD = 368434f25`（采集时点 2026-11-11T11:11:29+08:00，复算 `git rev-parse HEAD`），工作树 0 改动。
前序：`2026-11-11-trust-levels-closing.md`（hot handoff）、`../decisions/2026-11-11-trust-level-earnable.md`（D33）、
`../decisions/2026-11-11-host-command-authority-gate-p0.md`（P0）、
`../reports/2026-11-11-trust-levels-acceptance-matrix-v3.md`（唯一编号源，150 条）。

## 0. 修订记录（先读这节）

| 时点 | 改动 |
| --- | --- |
| — | 本节时点是**会话内近似值**（本文件尚未提交）；提交后以 `git log -1 --format=%cI -- knowledge/plans/2026-11-11-trust-levels-baseline-narrowing.md` 为准（R15：会移动的事实要带复算方式）。 |
| 11:17 | **自纠**：§1 首版写「永久锁死」，错。`LevelOf` 的账龄闸**过了 `NewUserDays` 至少落 TL1**（`level.go:26-31`），所以锁死是**有界的**：默认安装下每个新用户被静音 7 天，不是永远。结论方向不变（B 路仍不可原样执行），但量级从「全站瘫痪」降为「新用户 7 天不能发帖 + TL0 限额全成死代码」。这正是「期望算法漏输入」的第四次同形。 |
| 11:14 | 首版。**§1 推翻 D33 §「被否：B 路」的执行口径**：那一节说 B 路只是「代价大」，本文件证明它按原口径执行会**删掉默认安装里唯一真正生效的等级齿**。 |
| 11:14 | §2 新增：`SeedMemberPermissions` 不是落库真相，它是**测试专用常量**，真身是三条 SQL 迁移。D33 与本文件早先引用的「member 基线」都指错了对象。 |

本文件一旦与 `2026-11-10-trust-levels.md`（D1–D32）或 D33 冲突，以本文件为准，并在裁决后落成
`decisions/2026-11-11-trust-level-baseline-narrowing.md`（D34）。

## 1. 阻塞发现：B 路按原口径执行会静音每个新用户一整个新人窗口

「B 路 = 收窄 `member` 基线，7 键改由 TL1 授予」这条路上有一个**死锁**，不是成本问题：

| # | 事实 | 采集方式（2026-11-11T11:1x） |
| --- | --- | --- |
| 1 | `Grants(level)` 对 `level <= LevelNewcomer` **直接返回 nil**，TL0 拿不到任何 earnable 键 | `git show 368434f25:apps/api/app/Models/TrustLevels/registry.go \| sed -n '55,75p'`（sha256 `b15add0f…810a0f`） |
| 2 | TL1 的默认门槛 = `DaysVisited ≥ 3` **且** `CommentCount ≥ 5` | 同仓 `TrustLevels/policy.go` `DefaultPolicy().Levels[LevelMember]`（sha256 `5ffa2c7b…bb03661`） |
| 3 | **回复（comment）走 `post.create`**：`CreateComment` 第一句就是 `if !actor.Can(PermissionPostCreate) { return ErrPermissionDenied }` | `git show 368434f25:apps/api/app/Models/Forum/service.go \| sed -n '952,958p'`（sha256 `01c41b14…49edbf9e8`） |
| 4 | `features.trust_levels` **默认 enabled** | `Options/feature_flags.go` `featureFlagRecommendedDefaults()`（sha256 `e79a403f…12bb5bd`） |

推导链：收窄 `member` → TL0 没有 `post.create` → **不能回复** → `comment_count` 恒为 0 →
`CommentCount ≥ 5` 永不满足 → 在新人窗口内**到不了 TL1**。TL0 是兜底档，每个新用户都从这里开始。
后果两条：

1. **静音期**：默认 `trust.new_user_days = 7`（`Options/community_policy_options.go` 推荐默认，
   sha256 `61367ca9…578d246f`），所以**每个新注册用户 7 天内既不能开主题也不能回复**，
   期满由账龄闸直接落 TL1 恢复（`level.go:26-31` 的 `return LevelMember`）。
   有界 ≠ 无害：注册后第一帖发不出去，站点不会有人留下。
2. **TL0 的限额齿全部变成死代码**：`trust.new_user.topic_cooldown_seconds`(300) /
   `daily_topic_limit`(3) / `comment_cooldown_seconds`(60) / `daily_comment_limit`(30) /
   `forbid_outbound_links`(true) 的执行点都在 `limitsForActor`（`Forum/service_trust_limits.go:23`）
   的 `trust.active` 分支上，而**这些分支只在能发帖时才被走到**。TL0 发不了帖 ⇒ 五个配置键
   全部失去消费者——与 `trust.new_user.forbid_attachments`（D20 死配置）**同一失败类**，
   只是这次是我们自己造的。

> **这是本刀最反直觉的一条**：默认安装里「信任等级有牙」**今天已经是真的**，只不过牙长在
> TL0 的限额上（300 秒冷却 / 每日 3 主题 30 回复 / 禁外链），不在 earnable 权限上。
> B 路原样执行会把**唯一真正生效的那副牙**连根拔掉，换上一副名义上更彻底、实际空转的牙。

> 判据（沿用上一刀附录 A 第四句）：这条推导在什么实现下会红？——只要 `CreateComment` 仍用
> `post.create` 鉴权、且 TL1 仍要求 `comment_count ≥ 5`，任何「TL0 无 `post.create`」的实现都锁死。
> 两个前提都是**本刀不打算改**的既有事实，所以结论不依赖具体改法。

**结论**：B 路不能原样执行。「7 键整体上移」与「TL0 能发帖」互斥，而「TL0 能发帖」是
TL0 限额齿存在的前提。要保留任何一副牙，`post.create` 必须留在基线里；
一旦它留下，7 键就不能整体上移。

## 2. 第二发现：`SeedMemberPermissions` 是测试专用常量，不是落库真相

D33 与 capability-map 反复写「`SeedMemberPermissions` 已含 7 键」——那句话**在字面上为真、
在机制上误导**：

| 对象 | 实际角色 | 证据 |
| --- | --- | --- |
| `Identity/seeds.go` 的 `SeedMemberPermissions` | **生产代码零消费者**；只被两个测试读（`TestSeedMemberPermissionsStayNarrow`、`TestGrantSourceIsStaticForDefaultInstallWhitelistKeys`） | `rg -uuu -n "SeedMemberPermissions" apps/api`（2026-11-11T11:16）：非测试命中只有 `seeds.go:157` 声明与 `registry.go:13`/`policy.go:5` 两处注释 |
| 真实落库的 member 权限行 | 三条迁移：`202607040001_identity_rbac.sql`（`topic.create`/`post.create`/`post.edit_own`/`post.delete_own`）、`202607120001_fine_grained_permissions_phase1.sql`（`topic.edit_own`/`topic.delete_own`）、`202607050004_attachments.sql`（`attachment.upload`） | `rg -n "topic.create\|attachment.upload" apps/api/database/migrations/*.sql` |
| 守护它的守卫 | `TestSeedMemberPermissionsStayNarrow` 名字叫 narrow，实际**硬要求 7 键全在**——它是「宽」守卫 | `sed -n '165,190p' apps/api/app/Models/Identity/seeds_test.go` |

⇒ 收窄基线要动的是**迁移 + 常量 + 那个反向守卫**三处，而它们本来就不同步（第四类「两份」）。
`SeedMemberPermissions` 目前是**第五条幻影**（前四条：`account_age_days`、`roles.*`、`regular`、
`ThresholdOptionKey`）：一个「看起来是真相」的常量，实际只有测试在读它。

## 3. 三个候选（D34 待裁决）

| 候选 | 做法 | 新用户能否发帖 | 牙齿 | 改动面 |
| --- | --- | --- | --- | --- |
| **Z3-a** B 路原样 | 7 键全部上移到 TL1 | **否，静音 7 天**（§1） | 名义上有；同时删掉 TL0 的限额齿 | 迁移 + 常量 + 守卫 + F 组 + 所有 loader |
| **Z3-b′** 拆分门（推荐） | `member` 基线只收走 `topic.create` + `attachment.upload`；`post.create`/`*_edit_own`/`*_delete_own` 留基线。TL1 earnable 默认 = 这 2 键 | 是（能回帖） | **两副都有**：TL0 限额齿保留 + 新增「开主题/传附件」权限齿 | 迁移 + 常量 + 守卫 + F 组 + loader 补齐 |
| **Z3-c** 放弃权限齿，只做限额齿 | 不动基线；把「等级有牙」落在限额上（TL1 比 TL2+ 更紧的默认，或接线 `forbid_attachments`） | 是 | 只有限额齿 | 无迁移、无 F 组重写 |

**推荐 Z3-b′**，理由三条：

1. **它不删牙、只加牙**：TL0 的限额齿原样保留（`post.create` 留在基线，`limitsForActor` 的执行点仍可达），
   同时第一次给出权限齿。Z3-a 是**用一副空转的牙换掉一副真牙**。
2. **它兑现了一个已经存在的意图**：`trust.new_user.forbid_attachments` 是死配置（D20），
   TL1 才给 `attachment.upload` 正是那条死配置想表达的行为，但用等级实现，可审计、可关。
3. **它让「解锁」第一次可观测**：默认安装里 TL0 与 TL1 的**权限**差异不再为空集，
   而 D33 的 J-13/J-14 只证明了 `Grants` 语义，从没证明过「默认安装里有人真的少一个键」。

Z3-b′ 的代价必须写在前面：**默认安装升级后，存量新用户（TL0）会突然不能开新主题、不能传附件。**
这是**有意变更**，要进 release note；不接受这一点就只能选 Z3-c。

三个候选**都不包含**的一件事：把 `topic.edit_own`/`topic.delete_own` 从 TL0 拿走。
能发帖不能改错字，只会制造「发完发现错字只能重发」的双帖噪音——那不是牙齿，是自伤。

## 4-bis. 裁决（本次会话，YG）：Z3-b′ 采纳

产品口径按 Z3-b′ 落：**默认安装下 TL0 不能开新主题、不能传附件；能回帖、能改/删自己的回复；
TL1 起两者都有。**

**实施机制我改了一层，并在此披露**：不采用「收窄角色行 + 由 TL1 授予」（加法版），
改用 **减法门**（Z3-b″）。产品结果与 Z3-b′ 逐条相同，理由是**失败方向**：

| | 加法版（收窄 role + earned 授予） | 减法门（Z3-b″） |
| --- | --- | --- |
| 少改一条 loader 的后果 | 该路径上的会员**发不了帖**（fail-closed） | TL0 **多**一个键（fail-open） |
| 需要的迁移 | 删 `role_permissions` 行 + 存量安装的 `trust.levels` 选项迁移 | **零迁移**（角色行、选项都不动） |
| 契约/UI 影响 | `eligiblePermissions` 语义变（7→5），J-01/J-02 重写 | **不变**（白名单仍 7 键，J-01/J-02 原样） |
| F 组「关闭 → 已挣得恒空」 | 必须重写 | **原样成立**（关闭 = 不做减法） |
| 机器人账号 | 会丢 `post.create`（`Grants` 对非人类返回空） | 减法跳过 bot，无影响 |

论坛的发言路径上，**fail-open 才是正确的失败方向**——这正是 P0 决策记录里
「新增一条没有防线的写路径」的镜像教训：宁可少收一个键，不可多挡一个人。

Z3-b″ 的具体口径（实施以本节为准）：

1. **基线常量**：`TrustLevels.BaselineGrants()` 返回 `{"topic.create","attachment.upload"}`
   （代码常量，**不进** `DefaultPolicy().Earnable`，D17 的「默认空集」不动）。
2. **门控点**：装载期，`TrustLevelResolved == true` 且 `TrustLevel < LevelMember` 时，
   从有效权限里**扣除** `BaselineGrants()`。
3. **不扣的三种情况**：`features.trust_levels` 关闭 / 等级未解析 / bot（`Kind.IsBot()`）。
4. **显式 allow 覆盖优先**：`user_permission_overrides` 里 `effect='allow'` 的键不扣——
   否则运营「手动放开某人」这个动作会被静默推翻。
5. **合并点只有一个**：扣减与 `mergeEarnedPermissions` 走同一处（`Identity` 内的共享辅助），
   `LoadActor` 与 `LoadEffectiveActorTx` 都调它。**两处各写一份就是本仓第五次「两份」。**

## 4-ter. 若改回加法版，开关语义按这个口径改（留档，不执行）

现 D9：关闭 → `TrustLevelResolved == false` → **已挣得集合恒空** + 限额回落年龄两态。
收窄基线后，「恒空」等于「关开关就把 `topic.create`/`attachment.upload` 从所有人身上拿走」，
所以加法版必须改成：

| 状态 | 权限 | 限额 | `TrustLevelResolved` |
| --- | --- | --- | --- |
| 开（enabled） | 基线 ∪ 该档 earnable（TL0 只有基线） | 按档（TL0 用 `trust.new_user_*`，TL1+ 站点默认 + 覆盖） | true |
| 关（disabled） | **基线 ∪ TL1 earnable**（= 收窄前的 7 键，逐字节等价） | **不变**，仍回落年龄两态 | false（F 组断言保留） |

选「限额不变」而不是「限额也按 TL1」：关开关的正确含义是「没这功能」，
而今天的关态**本来就有**年龄两态限额。把限额也改成 TL1 会顺手删掉一项既有行为，
那不是「关闭」，是「另一个功能」。

这条重写落在 `resolveTrustGrants`（`Identity/trust_levels.go`）与 `limitsForActor`
（`Forum/service_trust_limits.go:23`）两个点上——**两处都要改，且都要有「关态 = 收窄前」的证据**。
Z3-b″ 下**不需要**这段重写；留档是为了将来若有人改回加法版，不必重新推一遍。

## 5. 变体无关、裁决前就开的工作

| 工作 | 归属 | 产出 | 为什么与变体无关 |
| --- | --- | --- | --- |
| §1 死锁的**机械证明**（红态证据，不是推导） | backend | `storage/qa/trust-levels-z3/D34-01-deadlock-red.txt` | 三个候选都要靠它排除 Z3-a |
| 装载路径清单：哪些 loader 合并 earned、哪些不合并 | researcher | `storage/qa/trust-levels-z3/inventory-loaders.md` | 任何收窄都要保证每条 loader 都拿到基线 |
| 7 键的生产消费者清单（`Can()` 调用点） | researcher | 同上 | 决定哪个键可以门控 |
| F 组断言清单（哪些矩阵条目断言「关 → earned 恒空」） | researcher | `storage/qa/trust-levels-z3/inventory-f-group.md` | 任何变体都要重写 F 组 |
| dev 库现状快照（member 权限行、开关、`trust.new_user_*` 现值） | ops | `storage/qa/trust-levels-z3/db-snapshot.txt` | 迁移的 before 侧 |

## 6. 判据（沿用上一刀，不新发明）

- **R16**：矩阵 `0 FAIL` 不得单独作为可提交依据，验收必须顺带跑 `./scripts/test.sh --fast` 三线。
- **R10**：守卫必须附「它红过的样子」，且红在点名那句上（行号 + 点名文字）。
- **R13**：证据文件必须写前置校验（开关状态 / session 可读 / 现场已清理）+ 采集时间 + 实现文件 sha256。
- **R14**：文档里每个路径先 `ls`、每个计数先 `wc -l`；通配符不是路径。
- **R15**：会移动的事实（行号 / `HEAD` / 工作树 / 开关 / 账号档位）必须带时点 + 复算方式。
- **「全绿」必须写清是哪几包**：上一刀「八包全绿」漏了 `app/Support/HostAPI`、`bootstrap`、
  `app/Http/Controllers/Extensions`，那三个包各带红，里面藏着 P0。

## 7. 本刀不做

- 不改 D24 的白名单封闭性（`*_any`/置顶/锁定/封禁/审核永不进 earnable）。
- 不动 `LoadEffectiveActorTx` 的父→子继承缺口（residual 3）——但它与本刀**相交**：
  收窄后插件命令路径也必须拿到基线，这条要在 §5 的 loader 清单里判死。
- 不做 `/badges` 公共目录页（独立一刀）。
- 不改 `sqlc generate`（不可安全运行，residual 4）。

## 8. 实施中新增的教训（本刀现场产出，下一刀直接用）

### 8.1 「红在别处」与「我没弄红它」是两个断言

整仓门禁第一次跑：`PASS web / PASS static / FAIL api`，红的是
`session_policy_selection_postgres_effect_integration_test.go` 一整族（31 条）。
那一刻有三种可能：本刀引入 / 基线既有 / 并发污染。**三者都不能靠读代码定**。

判据（本刀实测有效的顺序）：

1. 干净副本 `git worktree add /tmp/x 368434f25` 跑**同一条命令** → 基线绿；
2. 工作树跑同一条 → 红；
3. 此时才看红在**哪一行**，而不是看红在哪一族。

本刀结果：`Identity/identity_persistence_postgres_fixture_test.go` 手写的 `users` 表
**没有 `created_at` 列**，而本刀让 `LoadEffectiveActorTx` 读它 ⇒ 查询失败 ⇒ effect 失败
⇒ selection 行不合法 ⇒ 哨兵 `ErrIdentitySessionPolicyStoreUnavailable`。
**红是本刀引入的，但不是生产逻辑错，是测试夹具模型残缺**——所以处置是「补夹具」，
不是「回退生产代码」。这个区分必须写进结论，否则下一个人会去回退一个正确的修复。

### 8.2 错误文案指向 A，真实原因常在 B

`identity: session policy store is unavailable` 听起来是 store 没装配，
实际是「列不存在」。**哨兵文案的语义只覆盖它自己那一层**，跨层根因不会写在里面。
看到哨兵先去 `rg` 它的**全部**返回点（本例 `rows.go:73-75` 的行合法性校验），
不要顺着文案去查那一层。

### 8.3 手写夹具 schema 是第五类「两份」

本仓已抓到四类「同一判断两份实现」（fixture base schema、`mapOptionsError`、
密封 Query 目录、证据目录树）。本刀是第五类：**`CREATE TABLE users` 在 `_test.go` 里
有 34 个文件 / 35 个 DDL 块**（`rg -uuu -l "CREATE TABLE users" --glob '*_test.go' apps/api`）。

生产查询**加一列**就会静默拖红一批无关测试，而且失败信息不会提到「夹具」。
判据（本刀用过的）：按**生产调用方**定范围，不是按「红过的包」——
`LoadEffectiveActorTx` 的非测试调用方只有 `app/Models/Identity` 与 `app/Support/HostAPI`
两个包，所以只补这两处的夹具（2 份），其余 29 份缺 `status`/`kind`，结构上跑不到 loader。

**正向守卫**比清单更耐久：`Identity/identity_persistence_actor_columns_guard_test.go`
直接跑一次 `LoadEffectiveActorTx` 并断言 `CreatedAt` 非零。
「列名清单」会腐坏，「跑一次真实查询」不会。

### 8.4 并发窗口里的红绿分布不可当因果证据——但也不能因此不做 A/B

本刀有 5 次运行落在三方并发起隔离集群的窗口里，红绿分布**看起来**与时间强相关，
差点被定性成「并发污染」。正确做法是**串行化 + 干净副本 A/B**，
而不是「因为有并发，所以不下结论」。上一刀的纪律 4（共享资源需要串行化）又踩了一次：
说明它不是一次性教训，要变成流程——**谁要跑隔离库，先在群里说一声**。
