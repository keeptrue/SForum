# Decision: Badges + user activity statistics — core data domain, synchronous counters, actor-agnostic cache

Date: 2026-11-10
Status: accepted (implemented in the same change)
Related: `plans/2026-11-10-badges-and-user-activity.md`（§11 决断 D1–D11、§12 陷阱 T1–T12）、
`decisions/2026-10-12-remove-gamification-badges-growth.md`（本决策**部分反转**它，
见 Context 第 2 段）、`decisions/2026-10-28-content-reactions.md`、
`decisions/2026-11-10-content-bookmarks.md`、`modules/badges.md`、
`reports/2026-11-10-badges-m8-acceptance-report.md`

## Context

**1. 需求不是「加个徽章」，是补一层统计地基。** 运营者的原话是社区需要「累积感」，
但把这件事拆开看，徽章只是第一个消费者：真正缺的是「每个用户发过什么、被认可过什么、
来过多少天」这层计数。信任等级（下一刀）只要把这层计数映射成能力即可，不需要再动数据层。
因此本刀把「统计」当成核心数据域交付，徽章建在它上面，而不是把统计塞进徽章表里。

**2. 这推翻了 2026-10-12 的去游戏化决定，但只推翻一半。** 那条决策删掉的是
**插件形态**的 `sforum.gamification`（徽章 + 成长值 + 等级头衔 + 每日扫描 job），
理由是「产品不提供徽章与成长值」。运营者现在明确要求徽章回归，但形态完全不同：

- 不做成长值、不做等级头衔、不做每日扫描 job、不做庆祝动效；
- 徽章是 **core 数据域**（不是插件），因为统计层要供信任等级复用；
- 旧的 7 个 UI 身份是**永久墓碑**，本轮全部用新 ID + 新 `contractVersion` 重建。

所以本决策记录的是「为什么这次可以做」，不是「上次删错了」。上次删是对的：一个
自包含的插件版游戏化系统在没有任何统计地基的情况下，只能靠 job 全量扫描维持，
既慢又无法被其它功能复用。

**3. 沿用收藏/点赞那一刀的形状。** 单表关系行、可逆 toggle、反规范化计数、
稳定视图 + HostQuery、`features.*` 开关、请求级 `...ByMe` 装饰——这些机制已经被
两刀验证过，徽章尽量复用，只在「统计写在哪」「徽章能不能进缓存」「重算怎么做」
三处做了不同的选择，下面逐条写被否方案。

## Decision

### 1. 独立包 `app/Models/Badges/`，不塞进 `Forum`

**否掉的方案**：放进 `app/Models/Forum/`，当作 Forum 的一个能力。

理由：`Forum` 已经背着三份 legacy 基线（`service.go` 1074 行、
`postgres_store_ops.go` 1438 行、`service_ops.go` 1170 行），且 `Forum.Service` 的
接收者方法上限是 77、棘轮只减不增。徽章是**用户激励域**，不是内容关系域：它读
`users` 和 `user_activity`，不读主题内容；它的写路径由内容事件驱动，但它自己不
拥有内容。塞进去的结果是又一个 God object 继续长大，而架构门禁会逼着先做一轮
「为了腾地方而搬家」的抽取——那笔成本应该花在真正的边界上，不是花在省一个包上。

代价是 `Forum` 需要徽章来装饰列表，而 `Badges` 不能反过来 import `Forum`（会成环）。
解法是 Go 惯例的消费方定义接口：`Forum` 定义 `BadgeReader`，`Badges` 实现，
`bootstrap` 注入。唯一允许的例外是 `badge_reader.go` 为满足该签名而引用 Forum 的
`TopicSummary` / `Comment` 类型——已在 `modules/badges.md` 点名，避免后来者当成
「可以随便引用 Forum」的先例。

### 2. 统计放独立表 `user_activity`，不给 `users` 加列

**否掉的方案**：六个计数直接加在 `users` 上。

理由：`users` 是读路径最热的 JOIN 目标（主题列表、评论列表、资料页都要 JOIN）。
往它上面挂高频更新的列，等于让每次发帖/点赞都去 touch 一张被高频读取的表：
`updated_at` 会被统计写反复改写，任何依赖它做缓存失效、同步判断、增量导出的逻辑
都会被统计噪声污染。独立表还留了扩展空间——信任等级将来要加阅读指标，加列只会让
`users` 越来越宽，而独立表加列不影响最热的 JOIN。

代价是多一次 JOIN 或一次额外查询；对「资料页展示」这种单用户场景可忽略。

### 3. 统计同步写在既有写事务内，不做事件异步累加

**否掉的方案**：发事件 → 异步消费者累加计数（与通知 fanout 同姿态）。

理由：异步累加会引入**漂移**，而漂移需要**对账**，对账需要**真值 SQL**，而真值
SQL 对 `topic_count` / `comment_count` 根本不存在（软删的内容行还在，见
`modules/badges.md` 的可重算性表）。也就是说这条路会造出一个**无法自动修复**的
不一致类别。同步写在同一个事务里，最坏情况是事务变长一点——量级与既有的
`topics.like_count` 更新完全相同，而那条路径已经被 reactions 一刀验证过。

M8 的并发不变量证明了这一点：10 个账号并发点赞，`content_reactions` 恰好 +10、
作者 `like_received_count` 恰好 +10、无重复行。异步方案要给同一条断言写「最终一致」
的放宽版本，那等于放弃这条证据。

### 4. 徽章进 `CachedStore`，`likedByMe` / `bookmarkedByMe` 不进

**否掉的方案**：照 reactions 的做法，把徽章装饰放在缓存之后逐请求计算。

关键区别是 **actor 相关性**：`likedByMe` / `bookmarkedByMe` 的答案取决于「谁在看」，
所以必须每请求重算、永不进缓存；徽章是**作者的属性**，对所有访客相同，所以可以随
主题/评论载荷一起缓存。

代价是授予后最多一个 TTL（topicDetail 30s / comments 20s）才在帖子旁可见。资料页
不走 Forum 缓存，≤10s 可见。这个延迟被接受，因为徽章不是时效性信息；但两个时限
不同，验收和运营文档都必须分开写，否则会出现「后台授予后看不到 = 坏了」的误判。

### 5. `users.top_badge_id` 反规范化，不在列表查询里 `DISTINCT ON`

**否掉的方案**：列表查询时对 `user_badges` 做 `DISTINCT ON (user_id) ... ORDER BY tier`
取每人的最高级。

理由：列表页一页 20 个作者，`DISTINCT ON` 要在 `user_badges` 上按作者分组排序，
数据量随「用户数 × 徽章数」增长，而列表页只需要**一个**徽章。反规范化成一列后，
列表查询退化成一次 `LEFT JOIN badges ON badges.id = users.top_badge_id`，与既有的
`users.avatar` 之类同姿态。

代价是三个失真窗口（缓存延迟、删除最高级后变 NULL、停用后需重算），已在
`modules/badges.md` 单列一节。排序规则收敛到 `topBadgeOrderSQL` 一处，避免资料页与
帖子旁显示不同徽章。

### 6. 访问天数：Redis SETNX 挡库 + 命中后同步写 `user_visits`

**否掉的方案 A**：每次详情请求都 `INSERT ... ON CONFLICT DO NOTHING` 写库。
**否掉的方案 B**：排队/刷盘 job 批量落库。

A 的问题是每天每用户至少一次无谓写，纯靠唯一约束兜底；B 的问题是又要维护一个 job、
一份内存缓冲和一条「Redis 挂了丢访问记录」的失败路径。选的是照 `view_counter.go`
的成熟模式：`SETNX visit:{userID}:{date} TTL=25h`，只有首次命中才同步写一行
`user_visits`（`ON CONFLICT DO NOTHING` 天然幂等）。写入量 = DAU，比 job 简单一个
数量级；Redis 不可用时降级为直接写库（多一次冲突写入，无害，页面不失败）。

`days_visited` 由 `user_visits` 的 `COUNT(*)` 反规范化，插入成功后 +1。

### 7. 自动徽章不可手动收回

**否掉的方案**：允许管理员收回任何授予。

理由：自动徽章的授予由规则驱动，收回后下一次评估（写路径触发的
`badges.evaluate_user` job）会立刻重新授予——收回只制造一个短暂的假象，还会让
审计记录看起来像「管理员与规则在互相覆盖」。要撤销效果的正确操作是**停用规则**
（停用后不再评估，已授予行保留但不展示）。因此自动徽章收回返回
`422 forum.badge_manual_only`，并把「停用」写进错误文案。

### 8. `Recompute` 用一条批量 SQL，不逐用户调 evaluator

**否掉的方案**：`Recompute` 遍历所有用户、每人调一次 `Evaluator.EvaluateUser`。

理由：10 万用户 × 12 条规则在 Go 里循环是不可接受的，在 PG 里只是一次 join。
`GrantFromRules` 用 `INSERT ... SELECT ... ON CONFLICT DO NOTHING` 一条语句完成
「按当前规则补授予」，`RecomputeAll` 再用一条 `UPDATE ... WHERE IS DISTINCT FROM`
修 `top_badge_id`。两个计数分开返回（`granted` / `refreshed`），因为它们是两件事。

这条决策是**后来补的**：初版 `Recompute` 只修 `top_badge_id`、不补授予，导致
「运营调低阈值后点重算」什么也不会发生——D5 承诺的追溯能力根本没有实现。M8 之前
的代码核查发现后补上 `GrantFromRules`，并加了表驱动测试 + 变异验证（故意改坏
`like_given_count` 那一行 CASE，确认测试会红），因为六行 CASE 的复制粘贴错误
结构合法、约束拦不住、跑起来不报错。

## Consequences

- 统计层可被信任等级直接复用，不需要再动数据模型。
- `topic_count` / Profile 实时 `COUNT(*)` 在软删后**故意不一致**，且这一条被写进
  了资料页的双源设计（D1）。任何「统一成一个数」的重构都会推翻这个语义。
- 徽章有三条可见延迟/失真窗口，运营文档与后台文案必须写清。
- 无自动对账 job（D5 明确不做 CLI），漂移靠 `modules/badges.md` 的两段 SQL 人工核对。
- 本刀修掉的两个 P0（首页 500、后台无权限）都不是徽章逻辑本身的问题，而是
  「同一份东西存在多个副本」：列清单抄了三份、权限声明只有目录没有守卫。这条教训
  写进了 handoff，不是写进本决策——它属于实施纪律，不属于架构选择。
