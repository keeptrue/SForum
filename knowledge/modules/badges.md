# Badges And User Activity Module

## Purpose

Owns badge definitions, badge groups, grants, and the per-user activity counters
that badges are computed from. The counters are deliberately a separate data
domain from badges: they are the substrate the next workstream (trust levels)
will read, and badges are just the first consumer.

## Current Status

**landed（2026-11-10）**。M8 独立验收：84 条后端断言 0 FAIL、16 条浏览器结构
断言全过（2 条 pend 是 dev 限流，前一轮 403 已交叉验证）；双门禁绿；迁移
up/down 由 `scripts/verify-migration-reversible.sh` 独立覆盖。

验收报告 `../reports/2026-11-10-badges-m8-acceptance-report.md`；验收执行清单
`../reports/2026-11-10-badges-acceptance-criteria.md`（含 §6 契约对照）；决策
`../decisions/2026-11-10-badges-and-user-activity.md`；计划
`../plans/2026-11-10-badges-and-user-activity.md`（§11 决断 D1–D11、§12 陷阱 T1–T12）。

## Data Model

五张表 + 一个反规范化列（迁移 `202611100002_badges_and_user_activity.sql`）：

| 对象 | 作用 |
| --- | --- |
| `user_activity` | 每用户一行六项计数 + 预留阅读字段；`user_id` 主键，懒创建（`INSERT ... ON CONFLICT DO UPDATE`） |
| `user_visits` | `(user_id, visited_on)` 主键，`days_visited` 的真值来源 |
| `badge_groups` | 分组（`activity` / `content` / `honor`），`position` 决定资料页分区顺序 |
| `badges` | 徽章定义 + 规则（`grant_mode` / `metric` / `threshold`），三个 CHECK 约束把规则形状写进库 |
| `user_badges` | 授予行，`UNIQUE (user_id, badge_id)`，`granted_by` 可空 |
| `users.top_badge_id` | 反规范化「最高级 1 个」，列表页据此单列 JOIN |

**为什么 `user_activity` 是独立表而不是往 `users` 加列**：`users` 是读路径最热的
JOIN 目标（主题列表、评论、资料页都要 JOIN 它）。把六项计数放进去，意味着每次
发帖/点赞都在更新一张被高频读取的表，`updated_at` 会被统计写反复 touch，
任何按 `users.updated_at` 做的缓存失效或同步判断都会被统计噪声污染。独立表把
「身份」与「成就」分开，也让将来加阅读指标时不必继续加宽 `users`。

**六项计数的可重算性不对称**——这是运维最容易误解的一点：

| 计数 | 真值位置 | 能否精确重算 |
| --- | --- | --- |
| `like_received_count` / `like_given_count` | `content_reactions` 关系行 | **能**：关系行删了就没了，重算即真值 |
| `bookmark_count` | `content_bookmarks`（只算 `visibility='public'`） | **能**：同上 |
| `days_visited` | `user_visits` 行 | **能**：`COUNT(*)` 即真值 |
| `topic_count` / `comment_count` | 内容行（软删后仍存在） | **不能精确**：软删的内容行还在，重算若按 `status<>'deleted'` 会算少，按全量会算多——两者都不是「他做过什么」 |

因此 `topic_count` 与 Profile 页的实时 `COUNT(*)` 在软删后会**故意不一致**
（见计划 D1/D4：前者是成就，后者是「我现在有多少帖」）。看到不一致不要当 bug 修。

预留列 `posts_read_count` / `read_time_seconds` v1 恒为 0，不采集、不进稳定视图。

## Dependency Direction

```
bootstrap ─→ Badges ─→ Identity（读 users / actor 校验）
                  └──→ Support/Jobs（入队评估）
Forum ─╳→ Badges      （Forum 不认识徽章域）
```

`Badges` **不 import `Forum`**。`Forum` 自己定义消费方接口
`Forum.BadgeReader`（`DecorateTopics` / `DecorateComments`），由 `Badges` 实现、
`bootstrap` 注入。

**唯一例外必须点名**：`app/Models/Badges/badge_reader.go` 为了满足
`Forum.BadgeReader` 的签名，会 import Forum 的 `TopicSummary` / `Comment` 类型。
这是「实现别人的接口」所必需的，**不是**允许 `Badges` 随意引用 Forum 领域逻辑的
先例。要新增对 Forum 类型的引用，先问「这是不是在实现一个 Forum 声明的接口」。

## Grant Sources — 三来源判别式

`user_badges` 里三种来源可精确区分，M8 用它筛证据，运维用它审计：

| 来源 | `granted_by` | `grant_reason` | 触发路径 |
| --- | --- | --- | --- |
| 规则自动 | `NULL` | `''` | `badges.evaluate_user` job（写路径入队） |
| 管理员手动 | 操作者 user_id | 操作者填的理由 | `POST /admin/badges/:id/grants` |
| 批量重算 | `NULL` | `'recompute'` | `POST /admin/badges/recompute` → `GrantFromRules` |

```sql
-- 某个用户的授予都从哪来
SELECT b.slug, ub.granted_by, ub.grant_reason, ub.granted_at
FROM user_badges ub JOIN badges b ON b.id = ub.badge_id
WHERE ub.user_id = $1 ORDER BY ub.granted_at DESC;
```

幂等由 `ON CONFLICT (user_id, badge_id) DO NOTHING` 保证：重复评估、重复重算都
不会产生第二行。**但两条路径的对外语义相反**：手动授予重复是 `409`
`forum.badge_already_granted`（错误，不是静默成功）；手动收回不存在的授予是
`200 {"revoked": false}`（幂等，`revoked` 表示「确实删了一行」而非「请求成功」）。

自动徽章不可手动收回（`422 forum.badge_manual_only`）：规则下一次评估就会重新
授予，收回只是短暂假象。要撤销效果就停用规则。

## `top_badge_id` 失真窗口

反规范化的代价是三个「运营看到的现象与数据库不一致」的来源，全部是设计内行为：

1. **授予后缓存延迟**：帖子旁 chip 最多 **20–30s** 后才出现（topicDetail 30s /
   comments 20s）。资料页不走 Forum 缓存，≤10s 可见。两个时限不同，别用同一窗口判。
2. **删除最高级徽章后变 NULL**：`ON DELETE SET NULL` 只置空，不会自动挑次高级，
   所以即使该用户还有其它徽章，帖子旁 chip 也会消失到下一次评估/重算。后台删除前
   应提示「该用户还有 N 个徽章，删除后需重算才恢复显示」。
3. **停用徽章要触发重算**：`enabled=false` 后已授予行保留但不再展示；`AdminService`
   在 `enabled` / `tier` / `position` 变化时对受影响用户重算 `top_badge_id`（改名
   换图标不重算，因为不影响「谁被选中」）。

排序规则只有一处定义：`postgres_store.go` 的 `topBadgeOrderSQL`——
`tier` 权重（gold > silver > bronze）→ `badges.position` 升序 →
`user_badges.granted_at` 降序。资料页、帖子旁 chip、`top_badge_id` 维护必须共用
这一段，否则同一个人会在两个地方显示不同徽章。

## Cache Semantics

徽章是 **actor 无关**的（同一个作者的徽章对所有访客一样），所以它可以进
`CachedStore`，与主题/评论载荷一起缓存。

这与 `likedByMe` / `bookmarkedByMe` **相反**：那两个是 viewer 相关的，必须在
`PublicReadService` 缓存之后逐请求装饰、永不进缓存。照 reactions 的做法抄成
「每请求重算徽章」是错的——那会让列表页每个作者都多一次查询。

代价是授予后最多一个 TTL 的延迟（见上一节）。可接受，因为徽章不是时效性信息。

## Feature Flag And Permissions

- 开关 `features.badges`（`public: true`，推荐开）。关闭后：13 条徽章端点全部
  `403 forum.badges_disabled`、资料页徽章 tab 与帖子旁 chip 消失，**统计采集与
  统计数字照常**——统计不属于徽章产品面（计划 D1）。
- 权限键 `badge.view`（读后台）/ `badge.manage`（全部写）。**校验在领域层**：
  `AdminService` 每个方法都收 `identity.Actor` 并自行校验。路由目录里的权限声明
  只是目录，不是运行时守卫——M8 首轮就是靠这条发现了「纯 member 能建徽章」的 P0。
- `badge.view` 单独存在是**死权限**：可达性前提是 `admin.access`。只给
  `badge.view` 不给后台入口权限，用户进不去后台。

## Extension Surface

| 视图 | HostQuery | 结果 schema |
| --- | --- | --- |
| `sforum_core_v1.forum_badges` | `sforum.core.public_badges.list` | `sforum.core.public_badge` |
| `sforum_core_v1.forum_user_badges` | `sforum.core.public_user_badges.list` | `sforum.core.public_user_badge` |
| `sforum_core_v1.forum_user_activity` | `sforum.core.public_user_activity.list` | `sforum.core.public_user_activity` |

三个视图都 `security_barrier` + 按 `users.status='active'` 过滤，都不输出
`granted_by` / 阅读字段 / 邮箱。`forum_badges` 用 `WHERE badges.enabled` 过滤，
因此公开面不需要再判 `enabled`。

通知类型 `badge_granted`（category `engagement`，站内默认开、邮件/推送默认关），
邮件模板键 `forum.badge_granted`。管理员给自己授予**会**发通知——徽章是公开荣誉，
不是互动噪音（与自赞跳过的口径不同）。

## Reconcile SQL（D5：本刀不做 CLI，对账靠这两段）

统计列漂移时按下面两段核对；**`topic_count` / `comment_count` 的差异要按
「含软删」口径解释**，不要直接判为漂移。

```sql
-- 1) 关系行三项：可精确对账，任何差异都是漂移
WITH truth AS (
  SELECT u.id AS user_id,
         COALESCE((SELECT count(*) FROM content_reactions r
                    JOIN topics t ON t.id = r.target_id
                   WHERE r.target_type = 'topic' AND r.reaction = 'like'
                     AND t.author_id = u.id), 0)
       + COALESCE((SELECT count(*) FROM content_reactions r
                    JOIN comments c ON c.id = r.target_id
                   WHERE r.target_type = 'comment' AND r.reaction = 'like'
                     AND c.author_id = u.id), 0) AS like_received,
         COALESCE((SELECT count(*) FROM content_reactions r
                   WHERE r.user_id = u.id AND r.reaction = 'like'), 0) AS like_given,
         COALESCE((SELECT count(*) FROM content_bookmarks cb
                   WHERE cb.user_id = u.id AND cb.visibility = 'public'), 0) AS bookmarks,
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
SELECT b.slug, ua.user_id
FROM badges b
JOIN user_activity ua
  ON b.enabled AND b.grant_mode = 'automatic'
 AND b.metric IS NOT NULL AND b.threshold IS NOT NULL AND b.threshold > 0
 AND CASE b.metric
       WHEN 'topic_count'         THEN ua.topic_count
       WHEN 'comment_count'       THEN ua.comment_count
       WHEN 'like_received_count' THEN ua.like_received_count
       WHEN 'like_given_count'    THEN ua.like_given_count
       WHEN 'bookmark_count'      THEN ua.bookmark_count
       WHEN 'days_visited'        THEN ua.days_visited::bigint
     END >= b.threshold
WHERE NOT EXISTS (
  SELECT 1 FROM user_badges ub WHERE ub.user_id = ua.user_id AND ub.badge_id = b.id
);
```

第 2 段的结果集大小 ≈ 下一次 `POST /admin/badges/recompute` 的 `granted`。
`topic_count` / `comment_count` 的漂移没有真值可对——只能靠代码审查确认写路径都在
同一事务内，或在已知软删量下人工比对。

## Known Residuals（不要当 bug 修）

1. `Recompute` 的 `granted`（新插入行数）与 `refreshed`（`top_badge_id` 实际变化
   人数）相互独立，`{0,0}` 是正常幂等结果。
2. `revoked: false` 表示本来就没持有，不是失败；DELETE 两种结果都是 200。
3. 删除当前最高级徽章后 chip 消失直到重算（见失真窗口 2）。
4. `badge.view` 是死权限（见权限节）。
5. 缓存延迟 20–30s（见缓存节）。
6. dev 的 admin 写端点有 IP 限流，连续跑验收脚本会撞 429；按「退避重试 → 仍 429
   记 pend」处理，不判 FAIL。
7. `SFComment.vue` 仍有 537 行（在 500 以上），本刀只抽了 `SFCommentAuthorRow`，
   未继续拆；下次动它之前先看 `tests/architecture-boundaries-baseline.json` 的 cap。
8. 无自动对账周期 job（D5 明确不做 CLI），统计漂移靠上面两段 SQL 人工核对。
9. `/badges` 公共目录页未做（要 Page Registry 全套：catalog、route shell、
   ThemeCompiler ViewModel、Host island、主题模板）。
10. 阅读字段 `posts_read_count` / `read_time_seconds` 预留未采集。
11. **本刀的 i18n 完整性没有被断言覆盖，HEAD 的 `badges` 键整块缺失。**
    84 条断言 0 FAIL，但 **i18n 完整性不在断言覆盖范围内**——HEAD 的 `zh-CN.json` /
    `en-US.json` 里**根本没有 `badges` 键**（不是空对象，是整块不存在），而徽章组件与测试
    引用了 15 个键（`tier.gold|silver|bronze`、`chip.ariaLabel|ariaLabelLink`、
    `card.ariaLabel|ariaLabelUngranted|ariaLabelNotEarned|earnedOn|notEarned|progress`、
    `group.count`、`empty`、`loadFailed`、`retry`）加 `profile.tabs.badges`。这些键是
    信任等级那一刀才补上的（两份 locale 各 +87/−0，零删除）。
    **教训：验收断言不覆盖 i18n 完整性时，「全绿」不能推出「可用」。**
    补键来源有两类：徽章视觉规范原文（tier 名、chip/card 的 aria 模板、`group.count`），
    以及徽章测试里钉死的期望字符串（`empty` / `loadFailed` / `retry`）——**后者不是规范
    原文**，看到别当成设计定稿。
12. **路由计数 tripwire 停在 392，而 catalog 已是 405。** 本刀加了 13 条徽章路由（1 公开 +
    12 后台），但 `apps/api/app/Support/Routes/registry_test.go` 的计数断言没跟着更新——392
    是收藏那刀的数字。信任等级那刀才把它改成 405（现在 409 = 405 + 四条信任等级路由）。
    **症状**：`go test ./app/Support/Routes` 红、报数字不符，且看起来与徽章毫无关系。
13. **六个错误码没有本地化文案。** `TestAPIErrorCodesHaveLocalizedMessages` 的失败清单
    **全是徽章的码**：`forum.badge_manual_only`、`forum.badge_already_granted`、
    `forum.badges_disabled`、`forum.badge_slug_taken`、`forum.badge_rule_invalid`、
    `forum.badge_not_found`（`trust_levels.*` 一个都不在）。**归属：徽章那刀遗留，由信任
    等级那刀补齐**——文案在 `apps/api/app/Support/Localization/messages.go`（zh 与 en 两份，
    注释已写明是 cross-domain fix）。看到这条红先别怀疑信任等级。

14. **资料页统计条两个键缺失**：`SFProfileFeedTab.vue` 用了
    `t('profile.likeReceivedCount')` 与 `t('profile.daysVisited')`，而这两个键在两份 locale
    里**都不存在**（HEAD 也没有），页面上直接显示裸键名。D1 规定资料页要显示获赞数与访问
    天数（数字来自 `user_activity`），所以这是「功能做了、文案没跟上」。已由前端补齐中英各
    两条。

> **11–14 四条的共同注脚**：徽章那一刀的验收是「**84 条断言 0 FAIL**」，而这四样东西
> **都不在那 84 条里**——i18n 键完整性、路由计数 tripwire、错误码本地化、资料页统计条文案。
> **不是那刀做得差，是断言集不完备时，「全绿」推不出「可用」。**
>
> 而且**四条里有三条是 i18n**（11、13、14）——这不是零星遗漏，是**断言集对 i18n 的结构性
> 盲区**。这条比列清单更有预测力：下次做任何带界面的功能，验收清单里必须有「所有新增
> `t()` 键在两份 locale 里存在」这一条，且它应当是**遍历型断言**（扫源码里的 `t('...')` 与
> locale 键集合求差），不是人工核对——人工核对永远会漏掉「组件里写了、locale 里没有」这种。
>
> 还有一条给下一位验收者：**这四个缺口全部是跨刀发现的**，徽章那刀自己验收时一个都没抓到。
> 所以「本刀验收」这件事本身有盲区——**下一刀的验收应该顺带扫上一刀**。这解释了为什么这类
> 问题总是晚一步才暴露：不是没人验，是每次只验自己刚碰过的地方。
>
> 通用横切面清单（验收前逐条问）：i18n 键存在性 / 目录计数 tripwire / 错误码本地化 /
> 迁移可逆 / 开关关闭态 / 空数据与失败路径。没覆盖的那些，全绿等于没验。

## Important Paths

- 领域：`apps/api/app/Models/Badges/`（`types.go` / `service.go` / `admin_service.go` /
  `evaluate.go` / `postgres_store.go` / `postgres_store_catalog.go` /
  `activity_store.go` / `visits.go` / `badge_reader.go`）
- HTTP：`apps/api/app/Http/Controllers/Badges/`（`controller.go` / `dto.go`；
  `dto.go` 是 wire 形状的唯一来源，改契约前先读它）
- 装配：`apps/api/bootstrap/badges_wiring.go`（`badges.evaluate_user` worker 注册）、
  `apps/api/app/Providers/badges.go`
- 前端：`components/profile/SFProfileBadgeTab.vue`、`components/forum/SFBadgeChip.vue`、
  `pages/admin/badges.vue` + `components/admin/badges/`、`composables/admin/badges/useAdminBadges.ts`
- 契约：`contracts/openapi/paths/badges.yaml`、`contracts/openapi/schemas/badges.yaml`
