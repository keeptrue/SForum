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
授予，收回只是短暂假象。要撤销效果就停用规则。所以后台「授予记录」tab **只给手动
来源行渲染收回按钮**（`grantRevocable` = `grantedBy` 非空），自动/重算行连入口都没有；
`revoked: false` 走单独 toast 分支（「本来未持有」），既不算成功也不算失败。

## `top_badge_id` 失真窗口

反规范化的代价是三个「运营看到的现象与数据库不一致」的来源，全部是设计内行为：

1. **授予后缓存延迟**：帖子旁 chip 最多 **20–30s** 后才出现（topicDetail 30s /
   comments 20s）。资料页不走 Forum 缓存，≤10s 可见。两个时限不同，别用同一窗口判。
2. **删除最高级徽章后变 NULL**：`ON DELETE SET NULL` 只置空，不会自动挑次高级，
   所以即使该用户还有其它徽章，帖子旁 chip 也会消失到下一次评估/重算。后台提示已落地：
   编辑弹窗两步确认的**第二步**追加失真说明（`admin.badges.confirmDeleteTopBadgeHint`），
   删除成功 toast 追加 `admin.badges.toast.deletedRecomputeHint`。文案只说「需重算才恢复」，
   **不做「该用户还有 N 个徽章」的计数**——弹窗手里只有这一枚徽章的 `grantedCount`，
   拿不到「某用户还剩几个」，硬算等于为一次删除多打一条按用户的查询。
3. **停用徽章要触发重算**：`enabled=false` 后已授予行保留但不再展示；`AdminService`
   在 `enabled` / `tier` / `position` 变化时对受影响用户重算 `top_badge_id`（改名
   换图标不重算，因为不影响「谁被选中」）。

排序规则只有一处定义：`postgres_store.go` 的 `topBadgeOrderSQL`——
`tier` 权重（gold > silver > bronze）→ `badges.position` 升序 →
`user_badges.granted_at` 降序。资料页、帖子旁 chip、`top_badge_id` 维护必须共用
这一段，否则同一个人会在两个地方显示不同徽章。

## Cache Semantics

**先说结论，避免下面这句被读成「Badges 有 CachedStore 包装」：**

| 对象 | 有没有缓存 | 事实 |
| --- | --- | --- |
| `Badges` 的 store | **没有** | `bootstrap/api_assembly_domains.go:554` 是裸 `badges.NewPostgresStore(pool)`，**无任何装饰器**；`CachedStore` 全仓只存在于 `app/Models/Forum`（`cached_store.go:28` 定义、`:75` 构造函数 `NewCachedStore`） |
| 徽章在**读模型**里的形态 | **有** | 帖子/评论载荷里的 `BadgeRef`（`app/Models/Badges/types.go:158-163`）随主题/评论进 `CachedStore`。它只有 `slug`/`name`/`tier`/`icon` 四个字段，**actor 无关**（同一个作者的徽章对所有访客一样） |
| 公开目录端点 `GET /api/v1/badges` | **明确不缓存** | 响应含**查看者自己的** `earned`，头是 `private, no-store`（`Badges/controller.go:723` 的 `setViewerScopedNoStore`） |

**`BadgeRef` 可以进缓存**，因为它 actor 无关；这与 `likedByMe` / `bookmarkedByMe`
**相反**：那两个是 viewer 相关的，必须在 `PublicReadService` 缓存之后逐请求装饰、永不进缓存。
照 reactions 的做法抄成「每请求重算徽章」是错的——那会让列表页每个作者都多一次查询。

代价是授予后最多一个 TTL 的延迟（见上一节）。可接受，因为徽章不是时效性信息。

### 为什么 `/api/v1/badges` **禁止**复用 `setPublicCacheable`

`setPublicCacheable`（`Badges/controller.go:713`）写 `public, max-age=30`，注释理由是
「徽章墙 actor 无关」——那对**资料页**端点成立（返回**被查看者**的 earned，同一 URL 对所有人相同）。
`/api/v1/badges` 返回的是**查看者自己**的 earned，同一 URL 对不同人内容不同：
一旦写 `public`，共享缓存会把 A 的 earned 投给 B。所以它必须走
`setViewerScopedNoStore`（`:723`，`private, no-store`）。

**不要靠 `Vary` 补救**：该控制器的路由注册**不设 `Vary`**（`RegisterRoutes` 里零命中；
运行时响应里那条 `Vary: Cookie, Accept-Encoding` 来自中间件，不由控制器保证覆盖 earned 维度）。
选 `no-store` 而不是只写 `private`：目录是低频读，宁可每次回源，也不赌缓存键正确。

> 这条同时是**第六条「幻影」**的现场（前五条：`account_age_days`、`roles.*`、`regular`、
> `ThresholdOptionKey`、`SeedMemberPermissions`）：本节旧文案「徽章可以进 `CachedStore`」
> 字面为真（`BadgeRef` 确实进了），机制误导（**Badges store 从未被包装**）。
> 2026-11-11 的 `/badges` 公共目录那一刀查实并改写。

## Feature Flag And Permissions

- 开关 `features.badges`（`public: true`，推荐开）。关闭后：**14 条**徽章端点（12 后台 + 资料页墙 + 公开目录）全部
  `403 forum.badges_disabled`、资料页徽章 tab 与帖子旁 chip 消失，**统计采集与
  统计数字照常**——统计不属于徽章产品面（计划 D1）。
- 权限键 `badge.view`（读后台）/ `badge.manage`（全部写）。**校验在领域层**：
  `AdminService` 每个方法都收 `identity.Actor` 并自行校验。路由目录里的权限声明
  只是目录，不是运行时守卫——M8 首轮就是靠这条发现了「纯 member 能建徽章」的 P0。
- `badge.view` 单独存在是**死权限**：可达性前提是 `admin.access`。只给
  `badge.view` 不给后台入口权限，用户进不去后台。

## 管理台徽章页（`/control-panel/badges`，三分区）

页面是**三个固定分区**（`badges` / `groups` / `grants`），tab 选择在 URL query `?tab=`：
非法值回落 `badges`，`route.query.tab` 与 `activeTab` 双向同步。这是 Core 自有后台页，
几何按 `pages/admin/settings/index.vue` 的契约走（紧凑 `text-xl` 标题 + 一个
`UDashboardToolbar` + `SFAdminFixedTabNav` + 单个 `flex flex-col gap-4` 内容列），
不另起第二套 tab 系统。内容区由一张 `UCard` 承载（与 `pages/admin/permissions.vue`
同款，默认 body padding，不写 `#header`），卡片在 `KeepAlive` 外、tab nav 在卡外——
切 tab 只换卡内组件，卡片不重建。

- **壳 `pages/admin/badges.vue` 只承担编排**：tab 选择 / query 同步、三态顶部告警
  （`features.badges` 关闭 / 无权限 / 加载失败）、toolbar。行动作全部在分区组件里。
  `blocked` 非空时三个 tab 一起禁用（`disabled: blockedActive` → `SFAdminFixedTabNav`
  的 `:disabled="item.disabled"`）；**加载失败态不禁用**，留着刷新重试。
- **唯一数据层实例在壳里**：`useAdminBadges()` 只调一次，`store` 下发三个分区，任一
  分区写动作后 `load()` 刷新的是同一份状态。别在分区里再调一次 `useAdminBadges()`——
  那会得到第二份列表。
- **两套状态不要混成一套**：模板是 `<KeepAlive>` 包
  `<component :is="activeComponent" :key="activeTab">`，`KeepAlive` 保留的是**组件实例级
  局部状态**，**只举三个代表**：grants 的选中徽章与页码、groups 的表单草稿、badges 的
  编辑弹窗开关。这里**不列全量 ref 清单**——实例内 ref 是实现细节，改一行就过期。
  **筛选与排序不是 `KeepAlive` 的功劳**：七个 query 键都从 `route.query` 计算，切 tab
  回来靠 URL 还原，删掉 `KeepAlive` 它们也还在。前两条（grants 选中徽章、groups 表单
  草稿）**已浏览器实测保留**（`sessions/2026-11-12-badges-admin-selectitem-fix.md`）；
  页码保留因真实数据单徽章最多 12 条授予（`PER_PAGE = 20`，分页器不渲染）暂无法实测，
  它与选中徽章同属同一实例的 ref，按同一机制保留。
- **toolbar 右槽窄屏收成图标**：两个 `UButton` 的可见文字包在
  `<span class="hidden sm:inline">` 里，`aria-label` / `title` 恒在（重算按钮的两态
  「重算授予 / 确认重算」都要有），这是 admin settings 契约对窄屏命令的原文要求。
  左槽统计保持 `whitespace-nowrap` 并允许换行，390 宽下不溢出。
- **toolbar 左槽是统计**（总数 / 启用 / 自动 / 累计授予），四项全部从列表响应推导，
  **不接新路由、不加统计端点**；右槽是刷新与重算。重算 toast 的 `{count}` 是
  `granted + refreshed`（新插入行数 + `top_badge_id` 实际变化人数），不是 `granted` 单值。
- **`badges` 分区**：目录表 + 筛选（`q` / `group` / `tier` / `grantMode` / `enabled`）+
  排序（`name` / `tier` / `grantedCount`），筛选与排序**全部进 URL query，共七键**：
  `q` / `group` / `tier` / `grantMode` / `enabled` / `sort` / `dir`（七个键由
  `SFAdminBadgesBadgesTab.vue` 的 `FILTER_KEYS` 常量单源约束，页面级还有一个 `tab`），
  刷新与分享都能还原。`group=none` 表示未分组。**读侧有白名单归一**：`group` 只认
  `/^\d+$/` 或 `'none'`，`tier` / `grantMode` / `enabled` 各自只认枚举值，其余一律归
  `''`（不筛选）——URL 是能手改的输入面，`?group=all` 这种写侧永不产生的值不能被当成
  分组 id 去比对，否则一条不剩落 noMatch、`badgeFiltersActive` 还会误报有筛选。空态分两种：筛出 0 条走
  「无匹配 + 清除筛选」，本来就没有徽章才显示 `admin.badges.intro`——**intro 不再是
  页头说明**，页头只有标题。
- **`groups` 分区**：分组 CRUD（`POST` / `PATCH` / `DELETE /admin/badges/groups[/:id]`）。
  删除是行内两步确认，`badgeCount > 0` 的组在确认文案里带数量；删除不拒绝有徽章的组，
  组内徽章由外键 `SET NULL` 落成未分组。
- **`grants` 分区**：先选徽章再懒加载 `GET /admin/badges/:id/grants`（`perPage` 20 分页，
  返回 `{ items, total }`），来源列用三来源判别式着色；收回是行内两步确认（不弹窗），
  只对手动来源行渲染（见上一节的收回语义）。
- **`top_badge_id` 失真提示在删除弹窗里**（见失真窗口 2），不在页面上做常驻横幅。

## 已知陷阱：`SelectItem` 不接受空字符串 value

**症状**：打开 `/control-panel/badges` 控制台报
`A <SelectItem /> must have a value prop that is not an empty string.`，紧跟
`[Vue warn] Unhandled error during execution of component update at <SelectContent>`。
四个筛选下拉一渲染就抛，且**一个 item 抛错会污染整个 `SelectContent` 的 update**，
所以现象看起来是「下拉全坏」，不是「某一项坏」。

**根因**：`badges` 分区四个筛选 `USelect` 的 items 首项曾写成 `value: ''`
（「全部 / 全部分组」）。Reka 的 `SelectItem` 在 setup 里直接
`if (props.value === '') throw`（reka-ui 2.9.10 `dist/Select/SelectItem.js:89`）——
空串是**保留给「清空 / placeholder」的 model-value 语义**，不能当 item 值。这与
Nuxt UI 的 `USelect` 无关，是 Reka 的硬约束。

**修法**：哨兵值只活在组件里。`SFAdminBadgesBadgesTab.vue` 内
`const ALL_FILTER = 'all'`；`filterSelectModel(value) = value || ALL_FILTER` 供
`:model-value`，`onFilterSelect(key, event)` 在写回前把 `ALL_FILTER` 翻成 `''`。
**URL query 契约完全不变**：空值仍是「不筛选」，`applyQuery` 继续按空值 delete key，
所以 URL 里永远不会出现 `group=all` 这种垃圾参数；`group=none`（未分组）是真实
筛选值，不被哨兵吃掉。

**不要做的事**：不要为了省事把 `ALL_FILTER` 写进 query，也不要在
`utils/admin/adminBadges.ts` 的 `filterBadges` 里加 `'all'` 分支——那会把 UI 哨兵
泄漏进数据契约，深链和外部调用都会带上无意义值。

**回归锚点**：`apps/web/tests/admin/adminBadgesTabs.test.ts` 的
`never feeds an empty string to a filter SelectItem` 断言源码不含 `value: ''`，
并钉住四个 `filterSelectModel(...)` / `onFilterSelect(...)` 绑定——防止有人加回空串。

**同类核查（结论：其余两处合法，不要动）**：`SFAdminBadgeEditDialog.vue` 的分组下拉用
`value: null`（「—」= 无分组）——Reka 只拦空串，`null` 走 `valueComparator` 的
`isEqual` 分支；浏览器实测「新建徽章 → 分组选『—』→ 保存」落成未分组（分组列显示
`—`），回写正常，无需 sentinel 映射。**这依赖 reka 的实现细节（`props.value === ''`
的严格比较 + `isEqual` 兜底），升级 reka-ui / Nuxt UI 时把「null 分组回写」这条回归
一次**；若哪天它开始拒 `null`，再换成显式 sentinel（如 `'none' ↔ null` 映射）。`SFAdminBadgesGrantsTab.vue` 用 `undefined`
作为 `:model-value` 是**未选择 → 显示 placeholder** 的正当用法，与 item 值无关。

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
9. ~~`/badges` 公共目录页未做~~ —— **已做（2026-11-11）**：page ID `forum.badge.index`、
   contract `sforum.page.badge_index@1`、`CoreComponent pages/badges/index`、
   Host 岛 `sf-badge-index-page`（ComponentID `forum.component.badge_index`）、
   端点 `GET /api/v1/badges`、路由 `core.route.badges.public_catalog`、
   内置主题模板 `templates/badges.html`。剩余只有视觉 QA（B-26）未取证。
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
    **现状（2026-11-11 后台三分区改造后）**：后台 `admin.badges.*` 已由
    `apps/web/tests/admin/adminBadgesI18n.test.ts` **双向**断言兜住——引用键必须都在两份
    locale 里，locale 里的键也必须都被引用（`missing` 与 `extra` 同时必须为空，外加 zh/en
    键集合相等）。动态拼接的前缀走 `DYNAMIC_ENUMERATIONS`（`grantMode.` / `metrics.` /
    `columns.`），所以新增 `tabs.*` / `stats.*` / `filters.*` / `sort.*` / `grants.*` /
    `groups.*` 时它会先红，不必人工核对。
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
15. `SFProfileBadgeTab.vue` 现在 **514 行**（跨过 500 复核线，未进
    `architecture-boundaries-baseline.json`），一个组件承载**双 variant 单 DOM**
    （`profile` / `catalog`）。**第三个消费者出现时必须把卡片抽成
    `apps/web/app/components/forum/badges/SFBadgeCard.vue`**；现在不抽是为了不制造第二份，
    不是为了省事。

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
- 前端（论坛面）：`components/profile/SFProfileBadgeTab.vue`、`components/forum/SFBadgeChip.vue`
- 前端（后台徽章页）：壳 `pages/admin/badges.vue`；三个分区
  `components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue` /
  `SFAdminBadgesGroupsTab.vue` / `SFAdminBadgesGrantsTab.vue`；共享
  `components/admin/badges/SFAdminBadge{Table,EditDialog,GrantDialog}.vue`；
  数据层 `composables/admin/badges/useAdminBadges.ts` + 纯函数
  `utils/admin/adminBadges.ts`（`grantSourceOf` / `grantRevocable` / `filterBadges` /
  `sortBadges` / `validateBadgeGroupForm`）
- 契约：`contracts/openapi/paths/badges.yaml`、`contracts/openapi/schemas/badges.yaml`
