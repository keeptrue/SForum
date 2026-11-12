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

**等级表改造（2026-11-12）：通过（初验有条件通过，P1 与 P2 均已修复并复验）。** `badges.tier` 从写死的
CHECK 枚举升级为可管理的 `badge_tiers` 表（双语名 / 图标 / 颜色 / `position` /
`enabled`），后台徽章页从三分区扩成四分区（`badges` / `groups` / `tiers` / `grants`），
授予弹窗改成「搜索 + 批量 + 逐人结果」。独立验收 8 条门禁全绿；初验发现的 P1
（停用最后一个启用档会清空全站 `top_badge_id`）与写路径 QA 发现的 P2（新建等级
`position` 落 0，与金牌并列最高档）**都已修复，并在 shell 恢复后重跑门禁复验通过**
（`go build`、`bun test` 2379 pass、`bun run typecheck`、Badges/Controllers 两包
`test-api-isolated`、架构边界、OpenAPI refs、v3-catalog --check）。
报告 `../reports/2026-11-12-badges-tiers-review.md`；handoff `../sessions/2026-11-12-badges-tiers.md`。

验收报告 `../reports/2026-11-10-badges-m8-acceptance-report.md`；验收执行清单
`../reports/2026-11-10-badges-acceptance-criteria.md`（含 §6 契约对照）；决策
`../decisions/2026-11-10-badges-and-user-activity.md`；计划
`../plans/2026-11-10-badges-and-user-activity.md`（§11 决断 D1–D11、§12 陷阱 T1–T12）。

## Data Model

六张表 + 一个反规范化列。基础五张来自 `202611100002_badges_and_user_activity.sql`，
等级表 `badge_tiers` 来自 `202611120001_badge_tiers.sql`（2026-11-12 等级表改造）：

| 对象 | 作用 |
| --- | --- |
| `user_activity` | 每用户一行六项计数 + 预留阅读字段；`user_id` 主键，懒创建（`INSERT ... ON CONFLICT DO UPDATE`） |
| `user_visits` | `(user_id, visited_on)` 主键，`days_visited` 的真值来源 |
| `badge_groups` | 分组（`activity` / `content` / `honor`），`position` 决定资料页分区顺序 |
| `badges` | 徽章定义 + 规则（`grant_mode` / `metric` / `threshold`），自动徽章的两个 CHECK 约束把规则形状写进库；`tier` 是 `badge_tiers.key` 的外键 |
| `badge_tiers` | 等级档位：`key` 主键（小写字母开头，2..32 位，同时是 URL 段与 `data-tier` 取值）+ `name_zh` / `name_en` / `icon` / `color` / `position` / `enabled`；`badgeCount` 由查询聚合，不落列 |
| `user_badges` | 授予行，`UNIQUE (user_id, badge_id)`，`granted_by` 可空 |
| `users.top_badge_id` | 反规范化「最高级 1 个」，列表页据此单列 JOIN |

`badges.tier` 的外键是 `ON UPDATE CASCADE ON DELETE RESTRICT`，旧的
`CHECK (tier IN ('bronze','silver','gold'))` 已在 `202611120001` 里删除。
**`RESTRICT` 是刻意的**：删一档必须先给 `migrateTo`，让做出删除的人显式决定「这些
徽章归到哪」，而不是让数据库级联降级或级联删徽章。`badges_tier_idx` 支撑按档筛徽章
与删除前的 `badgeCount`。三个内置档的 `position` 与旧 `Weight()` 等价
（gold 0 < silver 1 < bronze 2），所以升级后逐人的最高级徽章选择结果与升级前一致
——这也是迁移 down 能把非内置档徽章落回 bronze、再重新加回旧 CHECK 的前提。

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
`badge_tiers.position` 升序（0 = 最高档）→ `badges.position` 升序 →
`user_badges.granted_at` 降序。**不再**用 `CASE` 映射 gold/silver/bronze：等级是运营
可增删的数据，写死映射会让新档位永远排最后。资料页、帖子旁 chip、`top_badge_id`
维护必须共用这一段，否则同一个人会在两个地方显示不同徽章。

用这个常量的每条查询都**必须**自带
`JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled`（别名固定 `t`）：
用 JOIN 不用 LEFT JOIN，停用档位的徽章不参与竞争，等价于「这一档不存在」。
所以「停用一档」与「删掉一档」对最高级徽章的效果相同，区别只是前者保留历史授予行。

**改序 / 启停 / 删除等级之后必须重算**：这三个动作改变的是「谁该被选中」，不是单条
徽章的外观。入口仍是等级写路径内的 `RecomputeAll` 与后台「重算」按钮；
**改名、改图标、改颜色不重算**（不改变选择结果），代价是展示层一个缓存 TTL 的延迟
（见 Cache Semantics）。

## 等级与配色

**9 色 palette 只有一个来源**：后端 `app/Support/Palette`（`palette.Tokens()` /
`palette.IsTierColor()` / `palette.DefaultForRank()`）。`TrustLevels/tier_colors.go`
现在只是薄包装（同名函数转发，既有调用点零改动），`Badges` 也吃同一份——两边各存一份
色表的结果是「后台预览的色和前台看到的不是同一个」。前端唯一实现是
`apps/web/app/utils/theme/tierColor.ts`；token 的**明暗两套取值**住在
`assets/css/sforum-trust-colors.css` 的 `--sf-tl-<token>` 里，组件只引用
`var(--sf-tl-*)`，不判断当前是不是暗色。

**自定义 hex 没有第二组取值**，深色靠现算：
`color-mix(in srgb, <hex> 74%, white)`（`TIER_DARK_ACCENT_MIX = 0.74`）。这条公式同时
以 JS（`tierAccentDarkHex`，供对比度提示）与 CSS（`tierAccentStyle` / `tierAccentVars`）
两种形式存在，**改一处必须改另一处**；数字只在 `tierColor.ts` 里写一次。

**统一 chip 规则**：底色 `color-mix(in srgb, var(--sf-tier-hue) 12%, transparent)`、
环线 `34%`，比例只写在样式表里；`tierAccentVars` 只负责成对下发
`--sf-tier-hue-light` / `--sf-tier-hue-dark`，组件按主题挑一个。**bronze 的虚线边框
已移除**——档位色是识别色，虚线会和「未解锁」语义撞车。内置三档的 `color` 种子值刻意
是 token（`amber` / `slate`）而不是 hex，只有 bronze 是历史 hex `#b45309`；前端兜底
`BUILTIN_TIER_COLORS.bronze = 'rose'` 只在载荷缺字段时生效。

**别踩的坑**：色值先过 `normalizeTierColor`（不认识的 token 返回 `null`，按位次取默认，
不抛错）；写一个不存在的 `--sf-tl-*` 会让整条 `color-mix()` 失效、色条掉成透明。
后台「颜色候选」来自服务端 `colorChoices`（`GET /admin/badges/tiers`），前端**不再抄
第二份候选表**——抄了就会在改色板时静默分叉。`tierColor.ts` 顶部的注释仍写着「必须与
`TrustLevels/tier_colors.go` 逐字同序」，实际对照物已是 `Support/Palette`（注释待更新）。

## Cache Semantics

**先说结论，避免下面这句被读成「Badges 有 CachedStore 包装」：**

| 对象 | 有没有缓存 | 事实 |
| --- | --- | --- |
| `Badges` 的 store | **没有** | `bootstrap/api_assembly_domains.go:554` 是裸 `badges.NewPostgresStore(pool)`，**无任何装饰器**；`CachedStore` 全仓只存在于 `app/Models/Forum`（`cached_store.go:28` 定义、`:75` 构造函数 `NewCachedStore`） |
| 徽章在**读模型**里的形态 | **有** | 帖子/评论载荷里的 `BadgeRef`（`app/Models/Badges/types.go:158-163`）随主题/评论进 `CachedStore`。它带 `slug` / `name` / `tier` / `icon` 与**等级外观**（`tier_name_zh` / `tier_name_en` / `color` / `position`），**actor 无关**（同一个作者的徽章对所有访客一样） |
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

## 管理台徽章页（`/control-panel/badges`，四分区）

页面是**四个固定分区**（`badges` / `groups` / `tiers` / `grants`），tab 选择在 URL query `?tab=`：
非法值回落 `badges`，`route.query.tab` 与 `activeTab` 双向同步。这是 Core 自有后台页，
几何按 `pages/admin/settings/index.vue` 的契约走（紧凑 `text-xl` 标题 + 一个
`UDashboardToolbar` + `SFAdminFixedTabNav` + 单个 `flex flex-col gap-4` 内容列），
不另起第二套 tab 系统。内容区由一张 `UCard` 承载（与 `pages/admin/permissions.vue`
同款，默认 body padding，不写 `#header`），卡片在 `KeepAlive` 外、tab nav 在卡外——
切 tab 只换卡内组件，卡片不重建。

- **壳 `pages/admin/badges.vue` 只承担编排**：tab 选择 / query 同步、三态顶部告警
  （`features.badges` 关闭 / 无权限 / 加载失败）、toolbar。行动作全部在分区组件里。
  `blocked` 非空时四个 tab 一起禁用（`disabled: blockedActive` → `SFAdminFixedTabNav`
  的 `:disabled="item.disabled"`）；**加载失败态不禁用**，留着刷新重试。
- **唯一数据层实例在壳里**：`useAdminBadges()` 只调一次，`store` 下发四个分区，任一
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
- **`tiers` 分区（2026-11-12 新增）**：等级表 CRUD（`GET` / `POST /admin/badges/tiers`、
  `PATCH` / `DELETE /admin/badges/tiers/:tierKey`、`POST /admin/badges/tiers/reorder`）。
  每行有双语名、图标（共享 `SFIconPicker`，存 `i-lucide-*` / `i-tabler-*` 全名）、颜色
  （9 token ∪ 自定义 `#rrggbb`）、`position` 与启用开关；列表带 `badgeCount`，删除前就能
  看到「这一档压着多少枚徽章」。**删除必须选迁移目标**（`migrateTo`：须存在、非自身、
  启用），不提供「静默降级」出口。**「最后一档」的开关禁用并给 `title` 说明**：停用最后
  一个启用档等于把全站 `top_badge_id` 清空，前端拦一道、后端
  `ErrBadgeTierInUse`（409 `forum.badge_tier_in_use`）再拦一道。**`key` 不可变**：它是
  URL 段与 `data-tier` 的取值，改名只能改 `name_zh` / `name_en`。调序是「按 keys 顺序重写
  `position`，未列出的档位按原序追加在后」——后台一次只带看得见的那几档，被隐藏的档位
  不该阻止可见档位的排序。
- **授予弹窗（`SFAdminBadgeGrantDialog` + `SFAdminBadgeUserPicker`）**：搜索候选
  （`GET /admin/badges/:id/grant-candidates`，权限 `badge.view`，只匹配 username /
  display_name，**绝不匹配 email**，单次最多 20 条）→ 多选 → 一次提交 → 弹窗内三态结果
  （`granted` / `skipped` / `failed`）。三态**不能合并成一个成功 toast**：`skipped` 是
  「已持有」（幂等命中，不是错误），`failed` 逐条给用户名与原因，且只对失败项提供
  「重试这一批」（重试时把两次结果累加，否则上一轮的 `granted` 会被冲掉）。body 是唯一
  滚动区、footer 吸底——失败名单可能很长，按钮不能被滚出视口（**这条未做浏览器取证**）。
  自动徽章在弹窗里直接禁用（后端 422 `forum.badge_manual_only`）。已持有的人在选择器里
  置灰；账号状态（disabled / banned）只提示、不过滤——「给已封禁用户补发历史活动徽章」
  是合法运营动作，服务端不替运营决定。
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
    **现状（2026-11-11 后台三分区改造后；2026-11-12 扩为四分区，`tiers.*` 与
    `tiers.lastEnabledHint` 同样受这条双向断言约束）**：后台 `admin.badges.*` 已由
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

16. **「最后一档」保护只防误点，不防对抗性并发**：`UpdateTier` 用事务 +
    `FOR UPDATE` 锁住被改的那一行，再数「除自己以外还有几个启用档」；两个请求**各停
    不同档**仍有窄窗口（各自都读到「还有别的启用档」）。`DeleteTier` 锁了被删行与目标行，
    比 `UpdateTier` 严一档。这是刻意的取舍，不是待修 bug（见 `tiers_store.go` 注释）。
17. **`RecomputeAll` 在 HTTP 请求内同步执行**：改序 / 启停 / 删除等级三处都会触发。它是一
    条 SQL（按「有授予行或 `top_badge_id` 非空」全量 UPDATE，只写真正变化的行），但在
    上万 `user_badges` 的库上仍是长事务 + 长请求。规模上来后应改投 River job，并把
    「已排队」回执给运营，而不是让 PATCH 等它跑完。
18. **等级改名 / 改色的缓存延迟**：帖子 chip 20–30s、资料页 ≤10s（与授予同一窗口），因为
    `BadgeRef` 带 `tier` 与等级外观进了 `CachedStore`。看到「后台改了色、前台还是旧色」
    先等一个 TTL，别当渲染 bug 查。
19. **批量授予单次上限 50 人**（前端选择器到上限即拦，文案键
    `admin.badges.grant.search.max`）；**候选搜索上限 20 条**是另一回事
    （`MaxGrantCandidates`），两个数字别混用。候选搜索不返回邮箱，也不按 `user.view`
    授权——它只回答「谁可以被授予这枚徽章」。
20. **等级 `key` 不可变**（`PATCH` 只改展示字段）。迁移 down 会把所有非内置档的徽章落回
    bronze，且**不动 `updated_at`**（`202611120001_badge_tiers.sql:72`）；回滚丢的是运营
    可重建的等级配置，不是用户数据。
21. **写路径浏览器证据已补齐（2026-11-12 下午）**：等级新建 / 改色 / 改图标 / 调序 / 停用 /
    删除（含迁移目标，下拉只列启用档）、批量授予（已授予 1 / 已持有 1 / 失败 0，`skipped`
    不是错误）、授予结果区 390×500 下 footer 吸底、编辑弹窗等级下拉只列启用档——全部真实
    浏览器实测，脚本与 43 张截图在 `storage/qa/`。**仍未取证**：`features.badges` 关闭态
    403 与后台三态告警、帖子列表 chip 与资料页徽章 tab（库内没有持徽章用户，基线 0）。
22. **跨档并发窄窗口**：两个请求各停用不同档时各锁各自的行，理论上仍可能同时通过
    「至少保留一档」检查。防的是运营误点，不是对抗性并发；真要根治用 advisory lock。
23. **新建等级缺省 `position` 走 `TierPositionAppend`（-1）**，由 store 在
    `INSERT ... SELECT CASE WHEN $6 < 0 THEN max(position)+1 ELSE $6 END` 里解析成末尾。
    不要把「没传」当成 0：0 是最高位次，新档会与金牌并列，挂在上面的徽章会和金牌徽章
    争同一个名次。

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
- 等级表与配色：`apps/api/app/Models/Badges/tiers_store.go`（等级读写 + 最后一档保护）、
  `tiers.go`（`resolveBadgeTier`：切档必须存在且启用，保持原值放行）、
  `grant_candidates.go`（候选搜索）、`apps/api/app/Support/Palette`（9 色唯一来源）、
  迁移 `apps/api/database/migrations/202611120001_badge_tiers.sql`；前端
  `apps/web/app/utils/theme/tierColor.ts`（取值域 / 归一化 / 两条渲染路径）+
  `apps/web/app/assets/css/sforum-trust-colors.css`（`--sf-tl-*` 明暗两套）
- 前端（后台徽章页）：壳 `pages/admin/badges.vue`；四个分区
  `components/admin/badges/tabs/SFAdminBadgesBadgesTab.vue` /
  `SFAdminBadgesGroupsTab.vue` / `SFAdminBadgesTiersTab.vue` /
  `SFAdminBadgesGrantsTab.vue`；共享
  `components/admin/badges/SFAdminBadge{Table,EditDialog,GrantDialog,UserPicker}.vue`；
  数据层 `composables/admin/badges/useAdminBadges.ts` + 纯函数
  `utils/admin/adminBadges.ts`（`grantSourceOf` / `grantRevocable` / `filterBadges` /
  `sortBadges` / `validateBadgeGroupForm`）
- 契约：`contracts/openapi/paths/badges.yaml`、`contracts/openapi/schemas/badges.yaml`
