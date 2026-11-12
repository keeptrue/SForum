# Badges And User Activity Module

## Purpose

Owns badge definitions, tiers, groups, grants, and the per-user activity counters
that badges are computed from. Counters are deliberately a separate data domain
from badges: they are the substrate trust levels will read, and badges are just
the first consumer.

## Current Status

三刀已落地，全部通过独立验收：

| 刀 | 日期 | 结果 |
| --- | --- | --- |
| 徽章 + 用户活跃 | 2026-11-10 | 84 条后端断言 0 FAIL、16 条浏览器结构断言；双门禁绿 |
| 等级表（`badge_tiers`） | 2026-11-12 | 8 条门禁全绿；P1/P2 修复后复验通过 |
| **规则引擎（布尔条件树 + 插件事实层）** | **2026-11-12** | **M1 后端全片落地**（迁移/引擎/条件/存储/回扫/HTTP/契约）；**M2 全部完成**（编辑器 + 剩余五项：`requirementText` 接卡片、影响确认弹窗、`queued` toast、summary 字段提示、公开面/后台 390 QA）；**第一刀「回扫进度可见」已收口**（`badge_rule_backfill_runs` + `backfill-status` 端点 + 后台三态行，§13 残留 #15 关闭）；**M3 插件扩展已收口**（manifest 候选 + `badges.fact.record` + SDK 封装 + 夹具，见扩展面段） |

规则引擎把「单指标 ≥ 阈值」升级成 JSONB 布尔条件树：`badges.rule` 成为自动徽章的**唯一**判定来源，`metric`/`threshold` 降为 legacy 只读（只给 down 迁移还原用）。

代码提交：`4457106e2`（apps，规则引擎 + 插件事实层）、`082f98624`（web，规则编辑器 / 回扫进度行 / 信任披露）、`4e1876331`（contracts，路由 422 与生成物）。

相关文档：计划 `../plans/2026-11-12-badge-rule-engine.md`、验收矩阵 `../reports/2026-11-12-badge-rule-engine-acceptance.md`、口径 SQL `../reports/2026-11-12-badge-rule-engine-condition-sql.md`、M3 契约（冻结）`../reports/2026-11-12-badge-conditions-manifest-spec.md`、偏差审计 `../reports/2026-11-12-badge-rule-engine-m1-deviation-audit.md`、决策 `../decisions/2026-11-12-badge-rule-engine.md`。

## 规则引擎分层

七块协作方，**依赖方向是单向的**，加功能前先认领正确的层：

| 层 | 位置 | 职责 | 不许做 |
| --- | --- | --- | --- |
| 纯 AST | `Support/RuleEngine/`（`types`/`decode`/`validate`/`eval`/`catalog`） | 解码（未知字段报错）、校验（深度≤5、叶子≤32、节点≤64、`days`∈1..3650、`value`∈1..1e9）、**Kleene 三值求值** | import 任何 `Models/` 包、任何 SQL、任何 i18n |
| 条件取数 | `Models/Badges/conditions/`（`registry` + `batch` + 5 个 `resolve_*`） | 13 条条件元数据；`Resolver.Prepare` 按 `type` 分组**每类一条 SQL** 并 memo，`Resolve` 逐叶子读 memo | 自己判「树是否成立」（那是 eval 的事） |
| 规则存储 | `Models/Badges/rule_store.go` | 规则读写 + 保存校验（AST + 引用存在性 + `badge.owned` **环检测 DFS**） | 在 HTTP 里跑全量 |
| 求值接线 | `Models/Badges/rule_eval.go` | `EvaluateRule` 把 `Resolver` 接进 store，并把**已声明 factKey 集合**透传给 `NewResolver(pool, userID, factKeys...)` | 吞掉 resolver 的错误 |
| 健康检查 | `Models/Badges/rule_health.go` | 失效引用清单（保存校验共用同一份引用检查） | 把「零授予」当失效 |
| 影响估算 | `Models/Badges/rule_impact.go` | `{estimated, method}`，AND 取最严叶子 / OR 取和，**上界语义（≈）** | 把估算当承诺 |
| 事实写入 | `Models/Badges/facts_store.go` | 同事务 `INSERT fact` + 计数累加；`dedupe_key` 唯一索引兜幂等 | 在 Core 里判定插件语义 |

回扫：`app/Jobs/Badges/backfill_rules.go`（`{badgeId, cursorUserId, batch}`，游标可续，`ByArgs` 去重）。**保存规则只入队，绝不在 HTTP 内跑全量**（残留 7，旧编号 #17，不得扩大）。

`evaluate.go` 保留签名，内部双分支：**快路径**只认「单 `activity.count` + `window.all` + `gte`」直接读计数器（10 枚内置徽章零回归、零额外查询）；其余走引擎。**判 `Rule == nil` 而不是判 `Metric == nil`**——纯 rule 徽章的 `metric` 是 NULL，先过旧 `ruleSatisfied` 会让新徽章「保存成功、达标、永不发、零报错」（P-04）。

## 条件目录（13 条 = 12 内置 + `fact.count`）

元数据在 `conditions/registry.go`，文案（双语 label/describe/unit）**直接写 Go**，不进 locale JSON——条件目录是服务端下发的数据，走 `t()` 会撞 `adminBadgesI18n.test.ts` 的双向断言。

| key | 取值 | 参数 |
| --- | --- | --- |
| `activity.count` | 计数 gte/lte | `metric` enum(6) + `window` |
| `activity.streak` | 计数 | `minDays` int(1..3650) |
| `object.engagement` | 布尔 | `action` enum(like/bookmark/comment) + `topicId` **int**（不是 objectRef） |
| `object.authoring` | 计数 | `targetKind` enum + `targetSlug` objectRef(category) + `window` |
| `identity.account_age` | 计数 | **无参数**（值就是年龄，比较走 `cmp`；不要加 `days`） |
| `identity.email_verified` / `identity.avatar` | 布尔 | 无 |
| `identity.oauth_link` | 布尔 | `providerId` objectRef(provider) |
| `identity.role` | 布尔 | `roleKey` objectRef(role) |
| `content.pinned` / `content.hidden` | 计数 | 无 |
| `badge.owned` | 布尔 | `slug` objectRef(badge)，保存时做环检测 |
| **`fact.count`** | 计数 | `factKey` objectRef(**fact**) + `variant` enum(可选) + `window` |

**`fact.count` 是插件条件的唯一入口**，M3 的 `badgeConditions[]` 不新增条件类型，只给它注册 `factKey`/`variant` 候选。两个面必须分开：

- `conditions.Core()` = **校验与求值全集**（13 条，含 `fact.count`）。从这里删掉它 → M3 存下的规则保存时 `unknown_condition`。
- `PublishableDefinitions(factKeys)` = **展示子集**（后台 `GET /admin/badges/conditions`）。M1 传 nil → `fact.count` 不出现（空 options 的 enum 是陷阱）。控制器只调它，**绝不回退 `Definitions()`**。

`ParamSpec.RefKind` ∈ `category|tag|role|provider|badge|fact`；数字一律 `int`，**绝不混进 objectRef**。`variant` 的 `Options: nil` 表示「候选由事实目录运行时填」，不是空下拉。

## 两条相反的失效语义（本模块最容易被「统一」掉的地方）

`conditions/resolve_facts.go:10-17` 有原文，判定时**看返回策略，不看注释**：

| 情形 | 返回 | 理由 |
| --- | --- | --- |
| 引用对象不存在（主题/分类/标签/角色/provider/徽章 slug 没了） | **确定值 0/false** | 「没做过」，不是「判不了」；返 error 会让整棵树 unknown，徽章永远不发 |
| 条件本身失效（`factKey` 无任何已启用扩展声明） | **error**（引擎转 unknown） | 0 会被 `lte` 判满足、被 `not` 判成 true，两条都是 fail-open |

三值链路：`resolver` 报错 → `eval.leaf` 记 `CodeResolveFailed` 并返回 `triUnknown` → `not(unknown)=unknown` → 最终 `Matched=false, Incomplete=true`。**所以「失效条件」不会经 `not` 反转成授予**。M1 的 `EvaluateRule` 传空 factKeys，因此任何 `fact.count` 叶子在 M1 恒 unknown——这是正确行为，M3 只需把集合填上。

`rule_health` 的两个 kind 对应用户能做的两件事：

- `undecodable_rule`：`badges.rule` 解不开（手工改库 / 旧格式）→ 该徽章**永不授予**，需人工修数据。
- `invalid_rule_shape`：能解但校验不过（超上限、未知条件、参数不合法）→ 保存路径本应拦住，出现即说明绕过（导入工具/直改库）。

另有 `missing_{topic,category,tag,role,provider,badge_slug,fact_key}` 表示「引用对象/事实键不在了」。**判 provider 必须接受「identity registry 已声明但尚无用户绑定」**，否则新装插件一上线全站规则先红一轮；**零授予不算失效**。

## Data Model

九张表 + 一个反规范化列。基础五张来自 `202611100002`，`badge_tiers` 来自 `202611120001`，规则引擎三张来自 `202611120002`：

| 对象 | 作用 |
| --- | --- |
| `user_activity` | 每用户一行六项计数 + 预留阅读字段；`user_id` 主键，懒创建 |
| `user_visits` | `(user_id, visited_on)` 主键，`days_visited` 的真值来源 |
| `badge_groups` | 分组（`activity`/`content`/`honor`），`position` 决定资料页分区顺序 |
| `badges` | 定义 + `rule` JSONB（自动徽章唯一判定来源）+ `requirement_summary_{zh,en}`；`metric`/`threshold` 为 legacy 只读 |
| `badge_tiers` | 等级档位（`key` 主键 + 双语名 + 图标 + 颜色 + `position` + `enabled`） |
| `user_badges` | 授予行，`UNIQUE (user_id, badge_id)`，`granted_by` 可空 |
| **`badge_facts`** | 插件事实流水（append-only）；`delta <> 0`；`fact_key` 正则 CHECK；`dedupe_key` 部分唯一索引兜幂等 |
| **`user_fact_counters`** | 事实累计，PK `(user_id, fact_key, variant)`；`total` **允许为负**（扣减），读侧统一 `GREATEST(...,0)` |
| **`badge_rules_rollback`** | 迁移前 `metric`/`threshold` 快照，**只在 Down 段读** |
| `users.top_badge_id` | 反规范化「最高级 1 个」，列表页据此单列 JOIN |

`badges.tier` 外键 `ON UPDATE CASCADE ON DELETE RESTRICT`——删一档必须先给 `migrateTo`，不让数据库替运营决定徽章归到哪。

**为什么 `user_activity` 是独立表**：`users` 是读路径最热的 JOIN 目标，把计数放进去会让每次发帖/点赞都 touch 一张高频读表的 `updated_at`，污染任何按它做的缓存失效判断。

**六项计数的可重算性不对称**（运维最容易误解）：

| 计数 | 真值位置 | 能否精确重算 |
| --- | --- | --- |
| `like_received` / `like_given` | `content_reactions` 关系行 | **能**（关系行删了就没了） |
| `bookmark_count` | `content_bookmarks`（**数全部行，不按 visibility**） | **能** |
| `days_visited` | `user_visits` 行 | **能** |
| `topic_count` / `comment_count` | 内容行（软删后仍在） | **不能精确**：按 `status<>'deleted'` 会算少，按全量会算多 |

所以 `topic_count` 与资料页实时 `COUNT(*)` 在软删后**故意不一致**（前者是成就，后者是「我现在有多少帖」）。看到不一致不要当 bug 修。

## Dependency Direction

```
bootstrap ─→ Badges ─→ Identity（读 users / actor 校验）
                  ├──→ Support/Jobs（入队评估 / 回扫）
                  └──→ Support/RuleEngine（纯 AST，无反向依赖）
Forum ─╳→ Badges      （Forum 不认识徽章域）
```

`Badges` 不 import `Forum`；`Forum` 声明 `BadgeReader` 接口，由 `Badges` 实现、`bootstrap` 注入。**唯一例外**：`badge_reader.go` 为实现该接口会 import Forum 的 `TopicSummary`/`Comment` 类型——这是「实现别人的接口」所必需，不是先例。

## Grant Sources — 三来源判别式

| 来源 | `granted_by` | `grant_reason` | 触发路径 |
| --- | --- | --- | --- |
| 规则自动 | `NULL` | `''` | `badges.evaluate_user` job |
| 管理员手动 | 操作者 user_id | 操作者填的理由 | `POST /admin/badges/:id/grants` |
| 批量重算 | `NULL` | `'recompute'` | `POST /admin/badges/recompute` → `GrantFromRules` |

幂等靠 `ON CONFLICT (user_id, badge_id) DO NOTHING`，但**两条路径对外语义相反**：手动授予重复是 `409 forum.badge_already_granted`；手动收回不存在是 `200 {"revoked": false}`。自动徽章不可手动收回（`422 forum.badge_manual_only`）——规则下次评估就重发，收回只是短暂假象。

## `top_badge_id` 失真窗口

三个「现象与数据库不一致」的来源，全是设计内行为：

1. **授予后缓存延迟**：帖子旁 chip 最多 20–30s；资料页 ≤10s（不走 Forum 缓存）。两个时限不同，别用同一窗口判。
2. **删最高级徽章后变 NULL**：`ON DELETE SET NULL` 只置空，不会自动挑次高级，直到下一次评估/重算。
3. **停用徽章要触发重算**：`enabled`/`tier`/`position` 变化时对受影响用户重算；改名/换图标/改色**不重算**（不改变「谁被选中」）。

排序规则单源：`postgres_store.go` 的 `topBadgeOrderSQL`——`badge_tiers.position` 升序（0 最高）→ `badges.position` 升序 → `user_badges.granted_at` 降序。**不要**再用 `CASE` 映射 gold/silver/bronze（等级是运营可增删的数据）。每条用它的查询必须自带 `JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled`（别名固定 `t`，用 JOIN 不用 LEFT JOIN）：停用一档等价于「这一档不存在」。

## Cache Semantics

| 对象 | 有没有缓存 | 事实 |
| --- | --- | --- |
| `Badges` 的 store | **没有** | 装配是裸 `badges.NewPostgresStore(pool)`，无装饰器；`CachedStore` 全仓只在 `app/Models/Forum` |
| 读模型里的 `BadgeRef` | **有** | 随主题/评论进 Forum 的 `CachedStore`；带 `slug`/`name`/`tier`/`icon` + 等级外观，**actor 无关** |
| `GET /api/v1/badges`（公开目录） | **明确不缓存** | 响应含查看者自己的 `earned`，头 `private, no-store` |
| `GET /profiles/:username/badges`（资料页徽章墙） | **共享缓存 30s** | 实测 `Cache-Control: public, max-age=30` + `Vary: Cookie`。同一句话（`requirementText`）目录页秒更、资料页最长滞后 **30s**——不要按 10s 估算（旧笔记写的 ≤10s 是错的）。该组合对登录用户命中率≈0 且键无界增长，属已知残留 |

`BadgeRef` 可以进缓存是因为 actor 无关；这与 `likedByMe`/`bookmarkedByMe` **相反**（那两个必须逐请求装饰、永不进缓存）。照 reactions 抄成「每请求重算徽章」是错的——列表页每个作者会多一次查询。

**公开目录禁止复用 `setPublicCacheable`**（它写 `public, max-age=30`）：`/api/v1/badges` 对同一 URL 返回不同人的 `earned`，写 `public` 会把 A 的 earned 投给 B。也不靠 `Vary` 补救——控制器路由注册不设 `Vary`。

## Feature Flag And Permissions

- 开关 `features.badges`（`public: true`，推荐开）。关闭后**全部徽章端点** 403 `forum.badges_disabled`、资料页徽章 tab 与帖子旁 chip 消失，**统计采集与统计数字照常**（统计不属于徽章产品面）。
- 权限键 `badge.view`（读后台）/ `badge.manage`（全部写）。**校验在领域层**：`AdminService` 每个方法都收 `identity.Actor` 并自行校验；路由目录里的权限声明只是目录，不是运行时守卫。
- `badge.view` 单独存在是**死权限**：可达性前提是 `admin.access`。

## 管理台徽章页（`/control-panel/badges`，四分区）

四个固定分区（`badges`/`groups`/`tiers`/`grants`），tab 在 URL query `?tab=`，非法值回落 `badges`。几何按 `pages/admin/settings/index.vue` 契约（紧凑标题 + `UDashboardToolbar` + `SFAdminFixedTabNav` + 单内容列），不另起 tab 系统。

- 壳 `pages/admin/badges.vue` 只承担编排：tab/query 同步、三态告警、toolbar。`blocked` 非空时四 tab 一起禁用；**加载失败态不禁用**（留着重试）。
- **唯一数据层实例在壳里**：`useAdminBadges()` 只调一次，`store` 下发四分区。分区里再调一次会得到第二份列表。
- `KeepAlive` 保留的是**组件实例级局部状态**（grants 的选中徽章/页码、groups 的表单草稿、badges 的弹窗开关）；筛选与排序**不是**它的功劳——七个 query 键从 `route.query` 计算。
- `badges` 分区七键筛选/排序全部进 URL（`q`/`group`/`tier`/`grantMode`/`enabled`/`sort`/`dir`），**读侧有白名单归一**：`group` 只认 `/^\d+$/` 或 `'none'`，其余归 `''`。
- `tiers` 分区：CRUD + 调序；**删除必须选迁移目标**；「最后一档」的开关禁用（前后端各拦一道）。
- `grants` 分区：先选徽章再懒加载，来源列按三来源着色，收回只对手动来源行渲染。
- 授予弹窗三态（`granted`/`skipped`/`failed`）**不能合并成一个成功 toast**；`skipped` 是「已持有」不是错误；失败项才给「重试这一批」。
- 规则编辑器（2026-11-12 新增）：`components/admin/badges/rule/SFAdminBadgeRule{Editor,Group,Leaf,JsonPreview}.vue` + 纯函数 `utils/admin/badgeRule.ts` / `badgeRuleParams.ts`。编辑器只渲染 `PublishableDefinitions` 下发的目录。
- **回扫进度行（2026-11-12 第一刀）**：`components/admin/badges/SFAdminBadgeBackfillRow.vue` 渲染在 `badges` 分区**筛选区上方一行**（不新增卡片/页头），`data-testid="badge-backfill-progress"`。三态：进行中「已扫描到用户 #N」/ 已完成 / **失败于用户 #N**（失败必须带徽章名 + 用户 id）；刷新按钮 `aria-label`/`title` 齐全；`queued>0` 的 recompute 后自动刷一次、`queued==0`/`undefined` 不刷。端点失败 → `[]` + 可见提示（`admin.badges.backfill.unavailable`），筛选区与表格照常渲染。数据源是 `GET /admin/badges/backfill-status`。

## 回扫进度（`badge_rule_backfill_runs`）的四条硬口径

规则改严触发的回扫是「一页一个 job」的链（`Jobs/Badges/backfill_rules.go`），链断在半路时运营只拿到过入队回执。进度表把它变成可查询的一行——**四条换写法就埋雷**：

1. **只由 job 侧写，绝不从授予数反推**。入队建 `running` 行 → 每页推 `last_user_id` → 末页 `completed`+`finished_at` → 页错 `failed`+`failed_cursor_user_id`。错法：读端点 JOIN `user_badges` 算「已处理人数」——规则不满足时进度会**倒退**，而真实回扫在前进。验收里有一条反向证据：徽章**有授予行但没有 run 行**时 `items` 必须为空。
2. **`failed` 不是「结束」**：失败**不写** `finished_at`（river 还会重试），重试成功要把 `failed_cursor_user_id` 清 0 并翻回 `running`。错法：失败即写 `finished_at`，运营看到「已结束」而队列还在重试。
3. **`Enqueuer == nil` 也要可见**：这一支原来是 `return nil`（静默停在半路），现在落 `failed` + `error`。装配漏接的信号落在 `bootstrap/badges_wiring_test.go`（断言 `Runs != nil`），**不在**生产路径 panic。
4. **`recompute` 的响应形状不动**：`{granted, refreshed, queued}` 恰好三键，进度是**另一个面**。变异依据：把 `queued` 改名即有一条断言红。

**刻意不给 `badge_rule_backfill_runs` 建 `sforum_core_v1` 视图**（AGENTS.md 要求「刻意缺失的读面写明理由」）：这是运维态（谁触发、卡在第几个用户、为什么失败），插件没有任何正当消费面；把它暴露成插件可读的稳定面等于给「谁在回扫谁」发一个契约。插件要读的是**授予结果**（`forum_user_badges`）与**事实累计**（`forum_user_badge_facts`），不是 Core 内部 job 的游标。

## 六个新陷阱（规则引擎这一刀长出来的）

每条都「同环境测不出来」，夹具必须显式构造，dev 库现有数据不算证据。

1. **窗口获赞必须排除自赞**（`r.user_id <> $1`）。累计口径 `activityDeltaForReaction` 对自赞返 `0,0`；窗口版漏了这条，同一枚徽章的「累计」与「最近 30 天」会互相打脸。**P-11「自评论算」不适用于获赞。**
2. **按日取界必须 Go 传参**（`$n::date`），SQL 不许出现 `CURRENT_DATE` / `(now()-interval)::date`。`user_visits.visited_on` 按 **API 进程本地日**写入（`visits.go` 用 `time.Now`，装配无时钟注入），而 `CURRENT_DATE` 是 PG 会话时区。API `TZ=Asia/Shanghai` + PG UTC 时跨零点 8 小时会把刚记的今天排除 → 连续 7 天零点后立刻误判断签。**时间戳比较可以在 SQL 里取 `now()`，日界不行。**
3. **`bookmark_count` 数全部行，不按 visibility**。写路径按 `relationDelta` 计数、`Recompute` 数全部行；`content_bookmarks.visibility` 默认 `private`，只算 public 会让「收藏家」几乎不可达。`object.engagement{bookmark}` 同口径。
4. **`content.hidden` 只认 `topics.status='hidden'`，只统计主题**。`hidden` 是纯管理员动作（`topic.delete_any`；作者自删走 `deleted`）；`deleted`/`pending`/`rejected` 不计；评论被隐藏不计。文案写「被管理员隐藏」，**不许**写「违规/处罚」——库里没有处罚记录（`moderation_reports` 无 action 列、`audit_events` 无内容目标列）。
5. **`content.pinned` 是「当前仍被置顶」**，不是「曾经」。库里只有 `is_pinned` 布尔值，无 `pinned_at`、无审计列。
6. **`identity.role` 用 `roles.key` + `r.is_enabled`**。`key` 有 UNIQUE，`alias` 无 UNIQUE 且可改（改名后规则会静默失效）；`is_enabled` 与授权判定一致。

## M2 收口长出来的四个陷阱（2026-11-12）

跟前面六个一样：都是「测试看着对、运行时静默错」，夹具必须显式构造。

1. **资料页与目录页是两条取数路径，`requirementText` 只在目录页算过**（已修）。`public_catalog` 走 `Badge.Public()`（`types.go:121` 是 `BuildRequirementText` 的唯一调用点），而资料页的 `ListUserBadges` 曾经自己 Scan 一份 `PublicBadge` → 文案恒 `{"zh":"","en":""}`。**键在值空**，任何「键存在 / 类型是 object」的断言都抓不到。判据必须是值级（zh/en 长度 > 0），且单条件与多条件两个方向都验。
2. **建徽章路径（`CreateBadge`）与改徽章路径不是同一套写逻辑**（已修）。`UpdateBadge` 有三态 `CASE WHEN`，`CreateBadge` 当时只 INSERT 老列 → `rule` 与 `requirement_summary_*` 全丢：automatic 撞 `badges_rule_check` 变 **500**、manual 的 summary 静默丢。**加列加字段时先问「创建路径也有这条吗」**，别只改 UPDATE。
3. **UTable 自己有滚动容器，外面再包一层就是两个 scroller**（已修）。`@nuxt/ui` 的 `slots.root` 默认 `relative overflow-auto`；外层再 `overflow-x-auto` 后，外层 `scrollW == clientW`（滚不动）、内层才是真 scroller，而 `tabindex` 落在外层 → **键盘可达性是假的**。正确做法：`<UTable tabindex="0" role="region" :aria-label>`（它 `v-bind="$attrs"` 到 root），保证全页只有一个可聚焦滚动容器。判据要写成「聚焦后 `scrollLeft` 能变」，不能只判 `document.activeElement`。
4. **`queued` 与 `granted`/`refreshed` 是三个不同单位**（已修）。`queued` = **转入异步评估的徽章数**，`granted` = 新插入授予行数，`refreshed` = `top_badge_id` 变化的用户数。前端曾经把它们相加当「影响量」——那会造出一个不存在的数字。文案必须分别说，且 `queued>0` 不能被摘要吃掉。

**影响估算的呈现纪律**（`SFAdminBadgeRuleImpactDialog.vue`）：端点估的是**已保存**规则（`EstimateRuleImpact(ctx, actor, badgeID)`），所以弹窗必须写「按当前已保存规则估算，不是本次编辑的草稿」；`estimated: 0`（读数确实是零人）与 `estimated: null`（无法估算）**不许互相顶替**；`partial: true` 必须追加「只算了部分条件，是上界」；不可估条件要逐个列出（后端 `unsupportedConditions` 只回条件 key），不能只报个数。

## 已知陷阱：`SelectItem` 不接受空字符串 value

**症状**：打开后台徽章页控制台抛 `A <SelectItem /> must have a value prop that is not an empty string.`，四个筛选下拉全坏——**一个 item 抛错会污染整个 `SelectContent` 的 update**。

**根因**：Reka 的 `SelectItem` 在 setup 里 `if (props.value === '') throw`（空串保留给「清空/placeholder」语义）。与 Nuxt UI 无关。

**修法**：哨兵值只活在组件里（`ALL_FILTER = 'all'`，写回前翻成 `''`）。**URL query 契约不变**，也不要在 `filterBadges` 里加 `'all'` 分支——那会把 UI 哨兵泄漏进数据契约。回归锚点：`apps/web/tests/admin/adminBadgesTabs.test.ts` 断言源码不含 `value: ''`。

## 假绿清单（本刀长出来的七种形态）

验收时逐条问一遍；命中任何一条，「全绿」都不能推出「可用」。

| 形态 | 本刀的真实例子 | 怎么防 |
| --- | --- | --- |
| **写死字面量** | M1.1-03 首版写死 `{root,children,0,...}`，在真库上误报 10/10 不等价 | 断言从**数据库当前值**构造，不抄计划示例 |
| **写死实现细节** | 断言依赖「`root` 是组节点」而迁移生成的是叶子；两种都合法，断言只认一种 | 断言语义（生效叶子），不认形状 |
| **只验单点不验多面** | 只验累计获赞会漏掉窗口版的错 | 同一口径的每个变体各一条 |
| **验证对象为空** | 迁移前基线若夹具为空，`should_have_but_missing = 0` 是空断言 | 先断言「迁移前必须非空」，再断言迁移后归零 |
| **断言集覆盖不到横切面** | 徽章那刀 84 条 0 FAIL，但 i18n 键完整性/路由计数/错误码本地化三样都不在断言集里 | 固定横切面清单：i18n 键存在性、目录计数 tripwire、错误码本地化、迁移可逆、开关关闭态、空数据与失败路径 |
| **测试借了环境** | dev 库已迁移，迁移前快照补不回来（M1.1-11/12 长期 pend）；dev 429 限流 | 需要「迁移前」的证据只能在一次性集群重放；429 记 pend 不判 FAIL |
| **只验「该拒的拒了」** | `Validate` 拒绝空组，但 `Eval` 对空 `all` 返回 `matched=true`（P-12）→ 绕过保存校验的脏规则会给全库发徽章 | 拒绝路径与放行路径都要验；求值层要独立兜底，不依赖「调用方应该先 Validate」 |

> 共同注脚：这七种的共同点是**证据看起来像证据**。判据要写成「删掉实现 → 断言必须红」，而不是「跑过了 → 通过」。

## Known Residuals（不要当 bug 修）

1. `Recompute` 的 `granted`（新插入行数）与 `refreshed`（`top_badge_id` 实际变化人数）相互独立，`{0,0}` 是正常幂等结果。
2. `revoked: false` 表示本来就没持有，不是失败。
3. 删当前最高级徽章后 chip 消失直到重算（见失真窗口 2）。
4. `badge.view` 是死权限（见权限节）。
5. 缓存延迟 20–30s（见缓存节）。
6. dev 的 admin 写端点有 IP 限流，连续跑验收脚本会撞 429：**退避重试 → 仍 429 记 pend**，不判 FAIL。
7. `RecomputeAll` 在 HTTP 请求内同步执行（改序/启停/删档三处触发）。规模上来后应改投 River job。**规则触发的回扫已走 job**，不要再叠同步路径。
8. 无自动对账周期 job；统计漂移靠下面两段 SQL 人工核对。
9. 阅读字段 `posts_read_count` / `read_time_seconds` 预留未采集，不进稳定视图。
10. **规则改严不自动收回**已授予（徽章是历史成就，收回只能手动）。UI 与文案都要写明。
11. **影响人数是估算**（AND 取最严叶子、OR 取和），弹窗文案必须写「≈」，`estimated: null` 表示**无法估算**而非 0 人。
    **估算对象是已保存规则**：`GET /admin/badges/:id/rule-impact` 不接受草稿，改规则时弹窗显示的是旧规则的上界（文案已明写）；新建徽章没有 id → 不弹、不编数字。若将来要估草稿，得新增端点（动路由计数 421→422 + OpenAPI + identities 四步走），这是**已评估后暂不采用**的方案。
11b. **`recompute` 的三个数不许相加**：`{granted, refreshed, queued}` 单位不同（授予行 / 用户 / 徽章）。`queued>0` 时 toast 必须说「N 枚徽章已转入后台评估」，全 0 时返回 `undefined`（正常幂等，不编假成功）。
11c. **后台表在 390 下靠单一滚动容器 + 可聚焦 region**：`scrollW 508 / clientW 326`、聚焦后 `scrollLeft` 能到 182px，操作列才够得到。别再在外面包第二层 `overflow-x-auto`（外层滚不动，键盘白给）。
11d. **建徽章路径必须与改徽章路径同口径**：`CreateBadge` 的 INSERT 要写 `rule` / `requirement_summary_zh` / `requirement_summary_en`；「automatic 必须有规则」的 422 判定在服务层（`normalizeNewBadge`），不要退回 `validateBadgeInput` 里靠 legacy `metric`/`threshold` 兜。
12. **`site.timezone` 全仓除 Options 包外零消费**：`visited_on` 的日界是 API 进程本地时区。站点级缺陷，影响所有按日统计的功能，**本刀不修**，应单开一刀。
13. 插件 RPC 判定器（A 路径）未做；`fail-closed` 目前只覆盖「事实查询失败」。
14. 查看类 / 采纳精华热榜 / 举报类条件未做（分别缺 `user_topic_reads`、Forum 域字段、审核数据不进公开面）。
15. trust levels 未共用引擎（引擎已独立成包，可平移）。
16. 公开面自然语言是双列（zh/en）；只填一种时另一种回落规则自动生成句。
17. ~~`PublishableDefinitions(factKeys []string)` **不携带 variants**~~ —— **M3 已闭**：签名落成 `PublishableDefinitions(candidates []FactCandidate)`，候选自带 `Variants`（见扩展面段）。保留原条目以便对照当时的 M2 约束。
18. 等级改名/改色的缓存延迟：帖子 chip 20–30s、资料页 ≤10s。
19. 批量授予单次上限 50 人（`admin.badges.grant.search.max`）；候选搜索上限 20 条是另一回事。
20. 等级 `key` 不可变（`PATCH` 只改展示字段）。
21. 「最后一档」保护只防误点，不防对抗性并发（跨档窄窗口，见 `tiers_store.go` 注释）。
22. 新建等级缺省 `position` 走 `TierPositionAppend`（-1）由 store 解析成末尾——**不要把「没传」当 0**，0 是最高位次。
23. `SFComment.vue` 仍在 500 行以上（本模块只抽了 `SFCommentAuthorRow`）。
24. **`rule_health` 的 `label` 是整屏中文硬编码**：`RuleIssue` 只有 `Kind`/`Path`/`Ref`/`Label`，
   没有 locale 维度，英文界面下用户看到中文（靠 `kind`/`path`/`ref` 定位）。修法要给
   `RuleIssue` 加 `labelKey` + 前端 i18n 文案表，属跨前后端契约变更 → 独立开单，别在本刀顺手加字段。
25. **`badgeConditions` 的 `rule #1`（非 plugin 类型）不可达**：theme 带 backend 会先被主题校验拒，
   走不到这条分支。按纵深防御保留，测试只能断言「Go 拒绝」，**拿不到 `rule #1` 编号**
   （spec §10 Errata 4）。
26. **`badgeConditions.key` 在 Go 侧比 schema 更严**：Go 复用 `manifestIDPattern` → 上限 **81** 字符，
   而 schema 与 `badge_facts.fact_key` 的 CHECK 都是 **121**。82–121 字符能过 schema 与 DB，但会被
   manifest 拒 —— 安全侧偏差，已在 `v3_badge_conditions.go` 的注释写明，不要为「对齐」而放宽 Go 侧。

## Reconcile SQL（对账靠这两段）

```sql
-- 1) 关系行三项 + 访问天数：可精确对账，任何差异都是漂移
--    注意列名是 topics.author_user_id / comments.author_user_id（不是 author_id）
WITH truth AS (
  SELECT u.id AS user_id,
         COALESCE((SELECT count(*) FROM content_reactions r
                     JOIN topics t ON t.id = r.target_id
                    WHERE r.target_type = 'topic' AND r.reaction = 'like'
                      AND t.author_user_id = u.id), 0)
       + COALESCE((SELECT count(*) FROM content_reactions r
                     JOIN comments c ON c.id = r.target_id
                    WHERE r.target_type = 'comment' AND r.reaction = 'like'
                      AND c.author_user_id = u.id), 0) AS like_received,
         COALESCE((SELECT count(*) FROM content_reactions r
                    WHERE r.user_id = u.id AND r.reaction = 'like'), 0) AS like_given,
         COALESCE((SELECT count(*) FROM content_bookmarks cb
                    WHERE cb.user_id = u.id), 0) AS bookmarks,
         COALESCE((SELECT count(*) FROM user_visits v WHERE v.user_id = u.id), 0) AS days
  FROM users u
)
SELECT ua.user_id,
       ua.like_received_count, truth.like_received,
       ua.like_given_count,    truth.like_given,
       ua.bookmark_count,      truth.bookmarks,
       ua.days_visited,        truth.days
FROM user_activity ua JOIN truth ON truth.user_id = ua.user_id
WHERE ua.like_received_count <> truth.like_received
   OR ua.like_given_count    <> truth.like_given
   OR ua.bookmark_count      <> truth.bookmarks
   OR ua.days_visited        <> truth.days;

-- 2) 授予 vs 规则：找出「按当前规则该有却没有」的用户（重算会补的就是这些）
--    2026-11-12 起 rule 是唯一判定来源；纯 SQL 只能覆盖「单 activity.count + window.all + gte」
--    这一种形状，复杂规则必须走 Go 侧求值（这条 SQL 仅作快路径对账）。
SELECT b.slug, ua.user_id
FROM badges b
JOIN user_activity ua
  ON b.enabled AND b.grant_mode = 'automatic'
 AND b.rule->'root'->>'type' = 'activity.count'
 AND b.rule->'root'->'params'->'window'->>'kind' = 'all'
 AND b.rule->'root'->'cmp'->>'op' = 'gte'
 AND CASE b.rule->'root'->'params'->>'metric'
       WHEN 'topic_count'         THEN ua.topic_count
       WHEN 'comment_count'       THEN ua.comment_count
       WHEN 'like_received_count' THEN ua.like_received_count
       WHEN 'like_given_count'    THEN ua.like_given_count
       WHEN 'bookmark_count'      THEN ua.bookmark_count
       WHEN 'days_visited'        THEN ua.days_visited::bigint
     END >= (b.rule->'root'->'cmp'->>'value')::bigint
WHERE NOT EXISTS (
  SELECT 1 FROM user_badges ub WHERE ub.user_id = ua.user_id AND ub.badge_id = b.id
);
```

`topic_count` / `comment_count` 的差异要按「含软删」口径解释，不要直接判为漂移。

## Extension Surface

| 视图 | HostQuery | 结果 schema |
| --- | --- | --- |
| `sforum_core_v1.forum_badges` | `sforum.core.public_badges.list` | `sforum.core.public_badge` |
| `sforum_core_v1.forum_user_badges` | `sforum.core.public_user_badges.list` | `sforum.core.public_user_badge` |
| `sforum_core_v1.forum_user_activity` | `sforum.core.public_user_activity.list` | `sforum.core.public_user_activity` |
| `sforum_core_v1.forum_user_badge_facts` | `sforum.core.public_user_badge_facts.list` | `sforum.core.public_user_badge_fact` |

四个视图都 `security_barrier` + 按 `users.status='active'` 过滤。`forum_badges` 用 `WHERE badges.enabled` 过滤，并输出 `rule` + `requirement_summary_{zh,en}`（`metric`/`threshold` 保留一版）。事实视图**只输出累计值**：不输出 delta 行、不输出 `dedupe_key`、不输出 `source_extension_id`。

### 插件事实条件（M3）

`badgeConditions[]` **不是新条件类型**：它是给 Core 已有的 `fact.count` 注册合法 `factKey`
与 `variant` 候选。规则叶子恒为 `{"type":"fact.count","params":{"factKey","variant","window"}}`，
引擎白名单仍是 13 条，不随插件数量增长。

| 层 | 落点 | 行为 |
| --- | --- | --- |
| 清单校验 | `Support/ExtensionManifest/v3_badge_conditions.go` | 仅 `type:plugin` + Protocol V2 backend；`≤32` 条、`variants≤16`；`compare` 非空且 ⊆ 五 op（无 `ne`）；`contractVersion == key@N`；`label`（给了 `unit` 时同）双语必填 |
| 候选模型 | `Models/Badges/conditions/facts.go` | `FactCandidate` / `FactVariant` / `FactDeclaration` / `MergeFactDeclarations` / `FactConflict`；输出按 `Key` 排序（下拉顺序要稳定） |
| 合并规则 | 同上 `MergeFactDeclarations` | 只取**已启用**插件（`StatusEnabled` + `TypePlugin`）；三条拒绝：与 Core 条件重名、缺 `<id>.` 前缀、重复 key（保留第一条，其余进 `FactConflict`） |
| 候选来源 | `bootstrap/badges_fact_catalog.go` | 5s TTL 快照；读失败**回退旧快照**（一次 DB 抖动不该让全站插件条件变 unknown）；冲突只 `slog.Warn`，不覆盖 |
| 发布面 | `conditions.Registry.PublishableDefinitions(candidates)` | 有候选才发布 `fact.count`（12 条内置恒在）；HTTP `items` + **additive** `factCandidates`，两个面不混 |
| 保存期 | `rule_store.go` `checkRuleReferences` | `factKey` 无候选**允许保存**（规则是运营资产）；`variant` 拼错 → `forum.badge_rule_reference_invalid`（`conditions.VariantDeclared`） |

`variant` 是**引擎开放集 + 域层兜底**：`fact.count` 的 `variant` 声明为 `ParamEnum` 且
`Options == nil`，引擎只查形态，成员资格由 `Models/Badges` 按当前候选判。`RuleEngine`
里 `nil` 与 `[]string{}` 语义不同（开放集 vs 封闭空集），不要混。

**Host 命令 `badges.fact.record`**（`Support/HostAPI/v2_command_badges.go`）：`ID` 不带 `@N`、
`Version` 是独立字段 `1`、schema ref `sforum.badges.fact.record.{input,result}@1`、
PolicyId `sforum.badges.fact.record@1`、`RequiredCapability = badges.fact.write`、actorless service。

| 项 | 口径 |
| --- | --- |
| 输入六字段 | `userId` / `factKey` / `variant` / `delta` / `occurredAt` / `dedupeKey`；`userId` 与 `delta` 都是**十进制字符串**（BIGINT 与 64 位整数不过 protobuf `Struct` 的 `double`） |
| 六个 reason（逐字） | `capability_denied`、`fact_key_not_declared`、`invalid_user_id`、`invalid_delta`、`invalid_occurred_at`、`fact_write_failed` |
| 审计分层 | `capability_denied` 由共享能力闸门在事务**前**返回，**不落审计**；其余五个域层拒绝落 `extension.badge_fact_record.denied`（best effort，审计失败不改返回码） |
| 幂等 | `dedupeKey` 非空才幂等：命中部分唯一索引 → `inserted:false`（正常输出，不是错误）；**空串两次都插** |
| 计数 | 回执 `total` clamp 到 0；存储 `user_fact_counters.total` **保留真实负值**（扣减没被吞） |

**事件转事实**：插件在 `events[]` 声明 observer，在 handler 里调 SDK
`Host.RecordBadgeFact`（`BadgeFactInput{UserID int64, FactKey, Variant, Delta int64, OccurredAt, DedupeKey}`，
wire 上 `userId` 是字符串）。**不新增 core 事件订阅总线** —— 这是计划里 C 路径的等价实现。
observe 至少投递一次，幂等真值位置是 `dedupeKey`（用业务标识派生，不要用时间戳）。

**卸载语义**：规则保留 → `fact.count` resolver 返 error（`resolve_facts.go`）→ 引擎 `unknown`
（`Matched=false, Incomplete=true`）→ `rule_health` 出 `missing_fact_key` → 同 key 重装**自动恢复**
（候选实时合并，不需要回填 job）。**不静默删/改规则**：禁用/卸载链路上没有任何写 `badges` 的代码，
`rule_health` 只读；失败语义的证据是 `reports/2026-11-12-badge-m3-manifest-acceptance.md` 的
D-01（未声明 → unknown 成对）与 D-02（variant 三态，含「卸载后仍可存」）。

通知类型 `badge_granted`（category `engagement`，站内默认开、邮件/推送默认关），邮件模板键 `forum.badge_granted`。管理员给自己授予**会**发通知——徽章是公开荣誉，不是互动噪音（与自赞跳过的口径不同）。

## Important Paths

- 规则引擎：`apps/api/app/Support/RuleEngine/`（`types` / `decode` / `validate` / `eval` / `catalog`）
- 条件与规则：`apps/api/app/Models/Badges/conditions/`（`registry` / `batch` / `resolve_{activity,object,identity,content,facts}`）、`rule_store.go`、`rule_eval.go`、`rule_health.go`、`rule_impact.go`、`facts_store.go`
- 领域：`apps/api/app/Models/Badges/`（`types.go` / `service.go` / `admin_service.go` / `evaluate.go` / `postgres_store.go` / `activity_store.go` / `visits.go` / `badge_reader.go` / `tiers_store.go` / `tiers.go` / `grant_candidates.go`）
- 回扫 job：`apps/api/app/Jobs/Badges/backfill_rules.go`
- HTTP：`apps/api/app/Http/Controllers/Badges/`（`controller.go` 路由 + `dto.go` wire 形状唯一来源 + `rule_controller.go`）
- 装配：`apps/api/bootstrap/badges_wiring.go`、`apps/api/app/Providers/badges.go`
- 前端（论坛面）：`components/profile/SFProfileBadgeTab.vue`、`components/forum/badges/SFBadgeCard.vue`、`components/forum/SFBadgeChip.vue`
- 前端（后台）：壳 `pages/admin/badges.vue`；四分区 `components/admin/badges/tabs/SFAdminBadges{Badges,Groups,Tiers,Grants}Tab.vue`；共享 `SFAdminBadge{Table,EditDialog,GrantDialog,UserPicker}.vue`；规则区 `SFAdminBadgeRuleSection.vue`（从编辑弹窗抽出）；规则编辑器与影响弹窗 `components/admin/badges/rule/{Editor,Group,Leaf,JsonPreview,ImpactDialog}.vue`；数据层 `composables/admin/badges/{useAdminBadges,useBadgeRuleDraft,useAdminBadgeConditions,useAdminBadgeRuleHealth,useAdminBadgeRuleImpact}.ts` + 纯函数 `utils/admin/{adminBadges,badgeRule,badgeRuleParams}.ts`
- 前端（公开面）：`components/forum/badges/{SFBadgeCard,SFBadgeCatalogPage}.vue`、`components/profile/SFProfileBadgeTab.vue`、`utils/forum/forumBadge.ts`（`forumBadgeRequirementText` 是公开面唯一句子解析入口，只选语言、不生成句子）
- 验收与证据：`scripts/verify-badge-rules-acceptance.sh`（值级双向，`requirementText` 按规则形状取样）、`knowledge/reports/2026-11-12-badge-m2-remaining-acceptance.md`、`knowledge/reports/2026-11-12-badge-m2-visual-qa.md` + `qa-2026-11-12-badge-m2/`
- 配色：`apps/api/app/Support/Palette`（9 色唯一来源）+ `apps/web/app/utils/theme/tierColor.ts` + `assets/css/sforum-trust-colors.css`
- 契约：`contracts/openapi/paths/badges.yaml`、`contracts/openapi/schemas/badges.yaml`

## Open Questions / Next

- **M2 已结项（2026-11-12）**：五项落实，两处契约变更（后台 wire 增 `rule`/`requirementSummary`、UI 身份 4→6）见计划 §15.2。
- **M3 插件扩展已收口（2026-11-12）**：契约冻结在 `../reports/2026-11-12-badge-conditions-manifest-spec.md`
  （含 Errata 1–4），实现与扩展面见本文件「插件事实条件（M3）」节，文档面向作者的是
  `../../docs/extensions/badge-plugin-facts.md`。三处硬判据仍然成立：`key` pattern 与
  `badge_facts.fact_key` 的 CHECK 逐字一致、`contractVersion == key@N`、未声明 factKey 返 error 而非 0。
- ~~**`M1-EXIT-PERF` 仍未取证**~~ —— **已闭（2026-11-12）**：10k 规模（1,999,800 行）已真跑，`PERF-01` 走 `Index Scan using content_reactions_roster_idx`（0.189ms）、`PERF-03` p50 2.19ms / p95 3.47ms（预算 50ms）；见计划 §15「收口遗留」。
- **`site.timezone` 零消费**（残留 12）是站点级缺陷，影响所有按日统计的功能，应单开一刀，
  不要塞进徽章刀。
- ~~**`PublishableDefinitions` 的 variants 签名**（残留 17）留到 M3~~ —— **已闭**：
  落成 `PublishableDefinitions(candidates []FactCandidate)`，候选自带 `Variants` 与双语 label/unit。
- 条件目录若要扩（查看类 / 采纳精华热榜 / 举报类），先确认数据面是否存在——
  三者分别缺 `user_topic_reads`、Forum 域字段、以及「审核数据不进公开面」的既有裁决。
