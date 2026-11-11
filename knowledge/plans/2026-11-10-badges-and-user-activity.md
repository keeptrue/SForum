# SForum 徽章 + 用户活动统计（第一刀）

Status: **landed（2026-11-10）** — M0–M8 全部落地。验收：84 条断言 0 FAIL、16 条浏览器结构断言全过、2 条 pend 为限流且有前一轮 403 交叉验证；双门禁绿（`generate.mjs --check` 18 files current、架构边界 2053 文件 / 206 超 500 行）。报告见 `reports/2026-11-10-badges-m8-acceptance-report.md`。
Date: 2026-11-10
Parent: `2026-07-12-iteration-a-engagement-loop.md`（Iteration A 收口后的下一刀）
前置阅读：`2026-10-28-content-reactions-likes.md`、`decisions/2026-11-10-content-bookmarks.md`

---

## 0. 目标与验收口径

**目标**：社区有了「累积感」。用户发帖、回复、获赞、给赞、访问站点会累积成可展示的徽章；运营者能在后台定义徽章、编辑自动授予规则、手动发放荣誉徽章；徽章在资料页和帖子旁可见。

**这一刀同时交付「用户活动统计」这层地基**，它是信任等级唯一的前置依赖。做完这一刀，信任等级只剩「把统计阈值映射到能力」一步。

**完成即验收**（每条都要有可复现证据）：

1. 用户发一个主题 → 资料页「主题」数 +1；发一条评论 → 「回复」数 +1；刷新后保持。
2. 别人给你的主题点赞 → 你的「获赞」+1，点赞者的「给赞」+1；取消点赞回退。
3. 同一天重复访问只算 1 天；跨天访问 +1；`user_visits` 每个 `(user_id, visited_on)` 至多一行。
4. 达到规则阈值的用户自动获得对应徽章，`user_badges` 恰好一行（重复触发不重复授予）。
5. 徽章在资料页按分组展示、按 tier 排序；帖子旁显示该作者的**最高级 1 个**徽章。
6. 管理员手动授予/收回徽章生效，授予记录可查；自动规则徽章不可手动收回（只能停用规则）。
7. `features.badges` 关闭后：资料页徽章 tab 消失、帖子旁徽章消失、徽章写端点 403 `forum.badges_disabled`；**统计数字照常显示**（统计不属于徽章产品面）。
8. 迁移 up/down 在一次性 PG 集群上实机可逆。
9. 门禁：`go build ./...`、相关包测试 + 隔离集群集成、`bun test`、`typecheck`、`node tests/validate-architecture-boundaries.mjs`、`ruby scripts/validate-openapi-refs.rb`、`node scripts/v3-catalog/generate.mjs --check`、`./scripts/test.sh --fast` 全绿。
10. 浏览器 QA（1633×810 + 390×844）：资料页 tab、帖子旁 chip、后台徽章页三处无横向溢出、无裁切、`aria` 合规。

---

## 1. 已锁定决策

| 项 | 结论 |
| --- | --- |
| 统计口径 | 发帖数 / 回复数 / 获赞数 / 给赞数 / 收藏数 / 访问天数；表结构预留 `posts_read_count` / `read_time_seconds`，v1 不采集 |
| 授予方式 | 混合：规则自动 + 管理员手动 |
| 分级与分组 | `bronze` / `silver` / `gold` 三档 + 分组（活跃 / 内容 / 荣誉） |
| 露出位置 | 资料页 tab + 帖子旁最高级 1 个 |
| 后台范围 | 徽章 CRUD + 分组 CRUD + 规则编辑 + 手动授予/收回 + 授予记录 + 全量重算 |
| 统计可见性 | 资料页对所有人公开 |
| 开关 | `features.badges`（推荐开）；关闭不影响统计采集与展示 |
| 权限键 | `badge.view`（读后台）、`badge.manage`（CRUD + 规则 + 授予）；不拆 `badge.grant`，授予归 manage |
| 本轮不做 | 关注 / 订阅、私信、信任等级、已读帖子追踪、`/badges` 公共目录页、徽章排行榜 |

---

## 2. 架构与依赖方向

### 2.1 包的归属

新建独立包 `app/Models/Badges/`，**不塞进 `Forum`**。理由：徽章是用户激励域，不是内容关系域；`Forum` 包已背着 `service.go` 1074 行、`postgres_store_ops.go` 1438 行、`service_ops.go` 1170 行三份 legacy 基线，且 `Forum.Service` 接收者方法上限 77 不可增。

依赖方向（单向，无环）：

```
bootstrap
  ├─→ Badges        （构造 + 注入）
  ├─→ Forum         （构造）
  └─→ Identity      （构造）

Badges ─→ Identity        （读 users / actor 校验）
Badges ─→ Support/Jobs    （enqueue 评估 job）

Forum  ─→ Badges？  不。Forum 只定义窄接口 BadgeReader，
                        Badges 实现它，bootstrap 注入。
```

`Forum` 定义（消费方定义接口，Go 惯例）：

```go
// app/Models/Forum/badge_reader.go
type BadgeReader interface {
    DecorateTopics(ctx context.Context, topics []TopicSummary) error
    DecorateComments(ctx context.Context, comments []Comment) error
}
```

**与 reactions / bookmarks 的关键差异**：徽章是 **actor 无关**的（作者的徽章对所有人一样）。所以徽章装饰**可以进 `CachedStore`**，不像 `likedByMe` / `bookmarkedByMe` 必须每请求重算。这简化了读路径，也意味着徽章更新最多延迟一个缓存 TTL（topicDetail 30s / comments 20s），可接受。

### 2.2 统计层的写入位置

统计**不异步**。全部在现有写事务内反规范化累加，与 `topics.like_count` 完全同一姿态（`reactions` 那一刀已经证明这条路可行）：

| 触发点 | 现有文件 | 累加字段 |
| --- | --- | --- |
| 创建主题 | `Forum` 写事务 | `topic_count` |
| 创建评论 | `Forum` 写事务 | `comment_count` |
| 点赞 / 取消 | `PostgresStore.ApplyReaction` 事务 | `like_received_count`（目标作者）、`like_given_count`（操作者） |
| 收藏 / 取消 | `PostgresStore` bookmark 事务 | `bookmark_count` |
| 每日首次请求 | Controller 详情 GET 后 | `user_visits` 一行 + `days_visited` |

统计列放**独立表 `user_activity`**（`user_id` 主键），不往 `users` 加列。理由：`users` 是读路径最热的 JOIN 目标，不该被统计更新反复 touch `updated_at`；且信任等级将来要加阅读指标，独立表不会挤爆 `users`。

### 2.3 访问天数的采集

照 `view_counter.go` 的成熟模式（Redis 去重挡库）：

```
请求 → VisitRecorder.Record(userID)
         └→ Redis SETNX  key=visit:{userID}:{YYYY-MM-DD}  TTL=25h
               └→ 首次命中才同步写 user_visits（ON CONFLICT DO NOTHING）
```

**选择：Redis SETNX 命中后同步写 `user_visits`。** 不排队。理由：一天一次、一次一行，写入量 = DAU，比刷盘 job 简单一个数量级，且 `ON CONFLICT` 天然幂等。Redis 不可用时降级为「直接写库」（多一次冲突写入，无害）。

`days_visited` 由 `user_visits` 的 `COUNT(*)` 反规范化到 `user_activity`，在插入成功后 `+1`。

---

## 3. 数据模型与迁移

新迁移 `apps/api/database/migrations/202611100002_badges_and_user_activity.sql`，声明 `-- +sforum OnlineSafe`（全部是新增表 / 新增可空列）。

### 3.1 表

```sql
-- 用户活动统计（新核心数据域）
CREATE TABLE user_activity (
  user_id              BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  topic_count          BIGINT  NOT NULL DEFAULT 0 CHECK (topic_count >= 0),
  comment_count        BIGINT  NOT NULL DEFAULT 0 CHECK (comment_count >= 0),
  like_received_count  BIGINT  NOT NULL DEFAULT 0 CHECK (like_received_count >= 0),
  like_given_count     BIGINT  NOT NULL DEFAULT 0 CHECK (like_given_count >= 0),
  bookmark_count       BIGINT  NOT NULL DEFAULT 0 CHECK (bookmark_count >= 0),
  days_visited         INTEGER NOT NULL DEFAULT 0 CHECK (days_visited >= 0),
  -- 预留：信任等级需要的阅读指标，v1 不采集，恒为 0
  posts_read_count     BIGINT  NOT NULL DEFAULT 0 CHECK (posts_read_count >= 0),
  read_time_seconds    BIGINT  NOT NULL DEFAULT 0 CHECK (read_time_seconds >= 0),
  last_visited_on      DATE,
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 日粒度访问记录（days_visited 的真值来源）
CREATE TABLE user_visits (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  visited_on DATE   NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, visited_on)
);
CREATE INDEX user_visits_recent_idx ON user_visits (visited_on DESC);

-- 徽章分组
CREATE TABLE badge_groups (
  id          BIGSERIAL PRIMARY KEY,
  key         TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 徽章定义
CREATE TABLE badges (
  id          BIGSERIAL PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  group_id    BIGINT REFERENCES badge_groups(id) ON DELETE SET NULL,
  tier        TEXT NOT NULL CHECK (tier IN ('bronze','silver','gold')),
  icon        TEXT NOT NULL DEFAULT 'i-lucide-award',
  grant_mode  TEXT NOT NULL CHECK (grant_mode IN ('automatic','manual')),
  metric      TEXT,
  threshold   BIGINT,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT badges_rule_check CHECK (
    (grant_mode = 'automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND threshold > 0)
    OR (grant_mode = 'manual' AND metric IS NULL AND threshold IS NULL)
  ),
  CONSTRAINT badges_metric_check CHECK (
    metric IS NULL OR metric IN (
      'topic_count','comment_count','like_received_count',
      'like_given_count','bookmark_count','days_visited'
    )
  )
);
CREATE INDEX badges_enabled_auto_idx ON badges (metric) WHERE enabled AND grant_mode = 'automatic';

-- 授予记录
CREATE TABLE user_badges (
  id           BIGSERIAL PRIMARY KEY,
  user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  badge_id     BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
  grant_reason TEXT NOT NULL DEFAULT '',
  CONSTRAINT user_badges_unique UNIQUE (user_id, badge_id)
);
CREATE INDEX user_badges_user_idx ON user_badges (user_id, granted_at DESC);
CREATE INDEX user_badges_badge_idx ON user_badges (badge_id, granted_at DESC);

-- 帖子旁只显示最高级 1 个：反规范化，避免列表查询做 DISTINCT ON
ALTER TABLE users ADD COLUMN IF NOT EXISTS top_badge_id BIGINT REFERENCES badges(id) ON DELETE SET NULL;
```

`user_activity` 行**懒创建**：写路径用 `INSERT ... ON CONFLICT (user_id) DO UPDATE SET x = user_activity.x + 1`，不需要回填。

### 3.2 稳定读视图（同变更强制交付）

```sql
CREATE OR REPLACE VIEW sforum_core_v1.forum_badges
WITH (security_barrier = true, security_invoker = false) AS
SELECT badges.id, badges.slug, badges.name, badges.description,
       badge_groups.key AS group_key, badges.tier, badges.icon, badges.position
FROM public.badges
LEFT JOIN public.badge_groups ON badge_groups.id = badges.group_id
WHERE badges.enabled
OFFSET 0;

CREATE OR REPLACE VIEW sforum_core_v1.forum_user_badges
WITH (security_barrier = true, security_invoker = false) AS
SELECT user_badges.id, user_badges.user_id, user_badges.badge_id, user_badges.granted_at,
       users.username, users.display_name, badges.slug, badges.tier
FROM public.user_badges
JOIN public.users  ON users.id  = user_badges.user_id AND users.status = 'active'
JOIN public.badges ON badges.id = user_badges.badge_id AND badges.enabled
OFFSET 0;

CREATE OR REPLACE VIEW sforum_core_v1.forum_user_activity
WITH (security_barrier = true, security_invoker = false) AS
SELECT user_activity.user_id, users.username, users.display_name,
       user_activity.topic_count, user_activity.comment_count,
       user_activity.like_received_count, user_activity.like_given_count,
       user_activity.bookmark_count, user_activity.days_visited
FROM public.user_activity
JOIN public.users ON users.id = user_activity.user_id AND users.status = 'active'
OFFSET 0;
```

用 `OR REPLACE`（理由同 bookmarks：schema 隔离的测试库重放迁移链会命中同一对象）。视图**不输出** `posts_read_count` / `read_time_seconds`（未采集，输出 0 会误导）。

`stable_core_views_test.go` 补三处断言：`security_barrier` / `security_invoker`、谓词、禁止列（email / `granted_by` / 阅读字段）。

### 3.3 通知类型

```sql
INSERT INTO notification_type_descriptors (
  type, contract_version, payload_version, category, active, required,
  presentation, target_contract
) VALUES (
  'badge_granted', 1, 1, 'engagement', TRUE, FALSE,
  '{"icon":"i-lucide-award"}'::jsonb,
  '{"kind":"entity","id":"core.entity.notification_target"}'::jsonb
) ON CONFLICT (type) DO NOTHING;

INSERT INTO notification_type_policies (type, channel, enabled, recommended_enabled, user_configurable, required) VALUES
  ('badge_granted','in_app', TRUE,  TRUE,  TRUE, FALSE),
  ('badge_granted','email',  FALSE, FALSE, TRUE, FALSE),
  ('badge_granted','web_push', FALSE, FALSE, TRUE, FALSE)
ON CONFLICT (type, channel) DO NOTHING;
```

邮件默认关（与 reaction 同姿态）；`Jobs/Notifications/render.go` 仍必须补 `forum.badge_granted` 中英模板，否则用户打开邮件通道会无模板投递。

### 3.4 内置默认数据（同迁移 INSERT）

三个分组：`activity`（活跃）/ `content`（内容）/ `honor`（荣誉）。
12 个内置徽章：

| 分组 | 徽章 | tier | 规则 |
| --- | --- | --- | --- |
| 活跃 | 常客 | bronze | `days_visited >= 7` |
| 活跃 | 老住户 | silver | `days_visited >= 30` |
| 活跃 | 元老 | gold | `days_visited >= 180` |
| 内容 | 初次发帖 | bronze | `topic_count >= 1` |
| 内容 | 主题作者 | bronze | `topic_count >= 10` |
| 内容 | 讨论者 | bronze | `comment_count >= 25` |
| 内容 | 小有名气 | bronze | `like_received_count >= 10` |
| 内容 | 广受好评 | silver | `like_received_count >= 100` |
| 内容 | 众望所归 | gold | `like_received_count >= 500` |
| 内容 | 收藏家 | bronze | `bookmark_count >= 10` |
| 荣誉 | 社区贡献 | gold | manual |
| 荣誉 | 优质解答 | silver | manual |

`ON CONFLICT (slug) DO NOTHING`，运营者可删可改。

### 3.5 Down

逆序：删三个视图 → 删 `users.top_badge_id` → 删 `user_badges` / `badges` / `badge_groups` / `user_visits` / `user_activity` → 删通知策略/描述符 → bump `notification_policy_revisions`。

---

## 4. 里程碑

### M0 — 迁移与契约骨架

- 上述迁移文件。
- `database/migrations/badges_migration_test.go`：Up/Down 结构断言（照 `content_reactions_test.go` / `content_bookmarks_test.go` 的 `strings.Fields` 规范化风格）。
- `database/migrations/stable_core_views_test.go`：补三个视图的断言。

### M1 — 统计层（后端写路径）

| 文件 | 内容 |
| --- | --- |
| `app/Models/Badges/activity.go`（新） | `ActivityCounters`、`ActivityMetric` 枚举、`bumpActivitySQL` 常量 |
| `app/Models/Badges/activity_store.go`（新） | `ActivityStore` 窄接口 + `PostgresActivityStore`（`BumpTx` / `GetCounters` / `RecordVisit` / `Recompute`） |
| `app/Models/Badges/visits.go`（新） | `VisitRecorder`：Redis SETNX 日 key + `user_visits` 写入 |
| `app/Models/Forum/activity_hooks.go`（新） | `bumpUserActivityTx(ctx, tx, userID, metric, delta)` 薄封装，供 Forum 事务调用 |
| `app/Models/Forum/postgres_store_ops.go`（改） | 创建主题/评论事务内各加一次 `bumpUserActivityTx` |
| `app/Models/Forum/postgres_store_reactions.go`（改） | `applyReactionCount` 之后加 `like_received` / `like_given` |
| `app/Models/Forum/postgres_store_bookmarks.go`（改） | 收藏切换后加 `bookmark_count` |
| `app/Http/Controllers/Forum/controller.go`（改） | 详情 GET 成功后调 `VisitRecorder.Record(actorID)`（匿名跳过） |

**约束**：`postgres_store_ops.go` 当前 1438 行，是 legacy 基线（cap 1438）。加调用必须**净零或净减**：先在同文件内把即将改动的函数体抽到新文件，腾出空间再挂 hook，并同轮下调基线。

### M2 — 徽章领域

| 文件 | 内容 |
| --- | --- |
| `app/Models/Badges/types.go` | `Badge`、`BadgeGroup`、`BadgeTier`、`GrantMode`、`UserBadge`、`BadgeRef`、错误值 |
| `app/Models/Badges/errors.go` | `ErrBadgesDisabled`、`ErrBadgeNotFound`、`ErrBadgeSlugTaken`、`ErrBadgeRuleInvalid`、`ErrBadgeManualOnly`、`ErrBadgeAlreadyGranted` |
| `app/Models/Badges/service.go` | `Service`：`ListBadges` / `ListUserBadges` / `ListGroups` / `TopBadgesFor`（批量） |
| `app/Models/Badges/admin_service.go` | `AdminService`：徽章 CRUD、分组 CRUD、规则校验、手动授予/收回、授予记录、`RecomputeAll` |
| `app/Models/Badges/evaluate.go` | 规则评估：读 `user_activity` 单行 → 匹配所有启用规则 → 差集 → 授予 |
| `app/Models/Badges/store.go` | `Store` 窄接口 |
| `app/Models/Badges/postgres_store.go` | 实现（含 `INSERT ... ON CONFLICT DO NOTHING RETURNING`、`top_badge_id` 维护、事务内通知投影） |
| `app/Models/Badges/badge_reader.go` | 实现 `Forum.BadgeReader`：批量查 `users.top_badge_id` + `badges`，填充 `TopBadge` |

**规则校验**（`ErrBadgeRuleInvalid`）：`automatic` 必须有 metric + threshold > 0；`manual` 必须两者皆空；`tier` 必须在枚举内；`icon` 必须匹配 `i-lucide-*` 或 `i-tabler-*` 前缀。

**授予幂等**：`INSERT INTO user_badges (user_id, badge_id, ...) VALUES (...) ON CONFLICT (user_id, badge_id) DO NOTHING RETURNING id`。插入成功才写通知、才更新 `top_badge_id`。

**`top_badge_id` 维护**：授予后重算该用户的最高级徽章（tier 权重 gold > silver > bronze，同级按 `badges.position` 升序，再按 `granted_at` 降序），写回 `users.top_badge_id`。收回后同样重算（可能变成 NULL）。

### M3 — 读模型与 HTTP

**类型扩展**（`app/Models/Forum/types.go` 实测 921 行，低于 1000 硬线，且不在 `legacyLargeFiles` 基线里；评论类型历史上已抽到 `types_comments.go`，**无需前置抽取**）：
- `TopicSummary` / `TopicDetail` / `Comment` 各加 `TopBadge *BadgeRef`。

**查询**：`topicSummarySQL` / `commentSelectSQL` 加 `LEFT JOIN badges top_badge ON top_badge.id = users.top_badge_id`，逐处补 scan。徽章字段进缓存（actor 无关）。

**公开路由**（`Controllers/Badges/routes.go`，新建 controller 包）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/profiles/:username/badges` | 某人的徽章（按分组 + tier 排序，含进度：`current` / `threshold`） |

**后台路由**（同 controller，`/api/v1/admin/badges` 组）：

| 方法 | 路径 |
| --- | --- |
| GET | `/api/v1/admin/badges` |
| POST | `/api/v1/admin/badges` |
| PATCH | `/api/v1/admin/badges/:badgeID` |
| DELETE | `/api/v1/admin/badges/:badgeID` |
| GET | `/api/v1/admin/badges/groups` |
| POST | `/api/v1/admin/badges/groups` |
| PATCH | `/api/v1/admin/badges/groups/:groupID` |
| DELETE | `/api/v1/admin/badges/groups/:groupID` |
| GET | `/api/v1/admin/badges/:badgeID/grants` |
| POST | `/api/v1/admin/badges/:badgeID/grants` |
| DELETE | `/api/v1/admin/badges/:badgeID/grants/:userID` |
| POST | `/api/v1/admin/badges/recompute` |

**错误映射**：`forum.badges_disabled`（403）、`forum.badge_not_found`（404）、`forum.badge_slug_taken`（409）、`forum.badge_rule_invalid`（422）、`forum.badge_manual_only`（422，自动徽章不可手动收回）。

**功能开关**：`Models/Options/feature_flags.go` 增 `NameFeatureBadges`（`public: true`、推荐开、`settings.site.manage`），加入 `service_normalize.go` 已知键分支与 `FeatureFlagCatalog`。

**顺带修复**：`features.bookmarks` 的 `admin.features.flags` / `flagsHelp` 文案在 zh-CN 与 en-US **都缺失**（收藏那一刀漏了），本轮补齐。

### M4 — 通知

- `Notifications/types.go`：`TypeBadgeGranted = "badge_granted"`；`categoryForType` 归 `engagement`。
- `Notifications/registry.go`：`coreTypeDescriptors()` 增 `{TypeBadgeGranted, "engagement", "i-lucide-award"}`。
- `Notifications/fanout.go`：`NotifyBadgeGrantedTx(ctx, tx, BadgeGrantEvent)`，`DedupeKey = badge_granted:{userBadgeID}`；管理员给自己授予**要发**（与自赞不同：徽章是公开荣誉）。
- `bootstrap/notification_adapter.go`：接上 `Badges` 侧窄接口 `BadgeNotifier`。
- `Jobs/Notifications/render.go`：新增 `case "forum.badge_granted"` 中英邮件文案。
- Web：`utils/notifications/notificationsPresentation.ts` 增图标与 `badge_granted` 过滤项；zh-CN / en-US 文案。

### M5 — 前端

**资料页**

| 文件 | 改动 |
| --- | --- |
| `components/profile/SFProfileBadgeTab.vue`（新） | 按分组分区，每组内 tier 降序；每个徽章显示图标 + 名称 + tier 色 + 获得日期；未获得的自动徽章显示进度条（`current/threshold`） |
| `components/profile/SFProfileShowPage.vue`（改） | `profileTabs` 加第 5 项（`badges`），受 `features.badges` 控制；激活时取数，切走保留 |
| `composables/profile/useProfileBadges.ts`（新） | 取数 + 缓存 + 加载态 |

**帖子旁**

| 文件 | 改动 |
| --- | --- |
| `components/forum/SFBadgeChip.vue`（新） | 单个徽章 chip：图标 + tier 色环 + `title`/`aria-label` 完整文案；点击跳资料页徽章 tab |
| `components/forum/SFComment.vue`（改） | 作者名旁挂 `SFBadgeChip`（`v-if="comment.topBadge"`） |
| `components/forum/SFTopicAuthorBlock.vue`（新） | 从 `SFTopicShowPage.vue` **抽取**现有主题作者块整段，抽取中挂 `SFBadgeChip` |
| `components/forum/SFTopicShowPage.vue`（改） | 作者块替换为 `<SFTopicAuthorBlock>`，**净减行数**，同轮下调基线（1127 → 实际值） |

**后台**

| 文件 | 改动 |
| --- | --- |
| `pages/admin/badges.vue`（新） | 路由壳：标题 + 工具栏 + 列表（照 `pages/admin/users.vue` 206 行形状） |
| `components/admin/badges/SFAdminBadgeTable.vue`（新） | 列表：图标 / 名称 / 分组 / tier / 授予方式 / 规则 / 已授予人数 / 启用 |
| `components/admin/badges/SFAdminBadgeEditDialog.vue`（新） | 新建/编辑：slug、名称、描述、分组、tier、图标选择、授予方式、metric + threshold |
| `components/admin/badges/SFAdminBadgeGrantDialog.vue`（新） | 手动授予：用户搜索 + 理由 |
| `composables/admin/badges/useAdminBadges.ts`（新） | 列表 / CRUD / 授予 / 重算 |
| 后台导航配置 | 侧栏加「徽章」入口（`badge.view`） |

**i18n**：`zh-CN.json` / `en-US.json` 增 `profile.tabs.badges`、`badge.*`、`admin.badges.*`、`notifications.types.badge_granted`、`notifications.body.badge_granted`、`admin.features.flags.features.badges`、`flagsHelp.features.badges`（顺带补 `features.bookmarks`）。

**交互细节**：tier 色用现有 appearance token（不新增调色板）；`aria-label` 带完整语义（「金牌徽章：众望所归」）；Toast 成功提示；错误 Toast 不自动关闭；390 宽不新增横向溢出。

### M6 — 扩展数据面（AGENTS.md 强制项）

三个新核心数据域，同变更交付视图 + HostQuery：

| 视图 | Query ID | 结果 schema |
| --- | --- | --- |
| `sforum_core_v1.forum_badges` | `sforum.core.public_badges.list` | `sforum.core.public_badge@1` |
| `sforum_core_v1.forum_user_badges` | `sforum.core.public_user_badges.list` | `sforum.core.public_user_badge@1` |
| `sforum_core_v1.forum_user_activity` | `sforum.core.public_user_activity.list` | `sforum.core.public_user_activity@1` |

改动点（照 bookmarks 先例）：
- `Support/HostAPI/v2_query_runtime.go`：三个 QueryID 常量 + 三个 SchemaID，加入 `isStableProtocolV2QueryID`。
- `Support/HostAPI/v2_query_postgres.go`：三段声明式计划。
- `Support/HostAPI/query_registry_core.go`：三个显式映射（`PaginationOffset`、`PermissionPolicyPublic`）。
- 测试：允许（公开数据返回行）+ 拒绝（停用徽章 / 非 active 用户不返回）。

### M7 — 目录、基线与知识库

- `node scripts/v3-catalog/generate.mjs`：核心路由数 +13；新 UI 身份（`public` 的 `SFBadgeChip` / `SFProfileBadgeTab`；`admin` 的四个组件）。
- `tests/architecture-boundaries-baseline.json`：
  - `SFTopicShowPage.vue` 1127 → 实际值（必须下调）。
  - `postgres_store_ops.go` 1438 → 实际值（若做了抽取则下调；若净零则持平）。
- `contracts/openapi/paths/badges.yaml` + `schemas/badges.yaml` + `contracts/openapi.yaml` 索引；跑 `ruby scripts/validate-openapi-refs.rb`。
- 知识库：`modules/forum.md`、`modules/notifications.md` 更新；新建 `modules/badges.md`；决策 `decisions/2026-11-10-badges-and-user-activity.md`；session handoff；`plans/README.md` 增行。

### M8 — 验证

见第 7 节。

---

## 5. 公开 API 与 Schema 变更

**新增路由 13 条**（1 公开 + 12 后台），全部在 `contracts/openapi/paths/badges.yaml` 以独立 path item 暴露。

**新增 schema**：`Badge`、`BadgeGroup`、`BadgeRef`、`BadgeProgress`、`UserBadgePage`、`BadgeGrantRecord`、`ApiResponse*` 若干。

**既有 schema 增量字段**：`TopicSummary.topBadge`、`TopicDetail.topBadge`、`Comment.topBadge`（全部 optional，向后兼容）。

**新稳定查询 3 条**（见 M6）。

**新错误码**：`forum.badges_disabled`（403）、`forum.badge_not_found`（404）、`forum.badge_slug_taken`（409）、`forum.badge_rule_invalid`（422）、`forum.badge_manual_only`（422）、`forum.badge_already_granted`（409）。

**新权限键 2 个**：`badge.view`、`badge.manage`（module `forum`；`moderator` 模板给 `badge.view`，`operator` / `super_admin` 给两个）。

**新功能开关 1 个**：`features.badges`。

---

## 6. 边界与失败模式

| 场景 | 行为 |
| --- | --- |
| 匿名访问资料页徽章 | 200，正常返回公开徽章 |
| `features.badges` 关闭 | 徽章端点 403 `forum.badges_disabled`；tab 与 chip 隐藏；**统计数字照常** |
| 用户被 ban / disabled | 徽章行保留（`ON DELETE CASCADE` 只在硬删时清）；稳定视图按 `users.status='active'` 过滤 |
| 徽章被停用（`enabled=false`） | 已授予行保留但不再展示（视图与读模型都按 `enabled` 过滤）；`top_badge_id` 需重算（停用时触发受影响用户重算） |
| 徽章被删除 | `user_badges` 级联删除；`users.top_badge_id` 置 NULL（`ON DELETE SET NULL`） |
| 自动徽章手动收回 | 422 `forum.badge_manual_only`（规则会立刻重新授予，收回无意义） |
| 手动徽章重复授予 | 409 `forum.badge_already_granted` |
| 规则阈值改成比现有用户低 | 不自动追溯。需点「重算」按钮或跑 CLI |
| Redis 不可用（访问采集） | 降级直接写库；`ON CONFLICT DO NOTHING` 保证幂等；页面不失败 |
| 统计列漂移（并发/失败） | 提供 `sforum badge:reconcile` CLI + 后台「重算」按钮；文档化对账 SQL |
| 用户硬删 | `user_activity` / `user_visits` / `user_badges` 全部 CASCADE |
| 列表查询 | 徽章走 `users.top_badge_id` 单列 join，不做 `DISTINCT ON`；一次查询覆盖整页，无 N+1 |
| 缓存 | 徽章进 `CachedStore`（actor 无关），授予后最多一个 TTL（20–30s）延迟可见 |

---

## 7. 测试矩阵（最小但完整）

**Go 单测**
- `Badges/service_test.go`：徽章列表分组排序、tier 权重、`top_badge_id` 重算（gold > silver > bronze，同级按 position）。
- `Badges/evaluate_test.go`：规则命中 / 未命中 / 差集；重复评估不重复授予；停用徽章不评估。
- `Badges/admin_service_test.go`：CRUD 校验（slug 冲突、规则非法、tier 非法、icon 前缀非法）；手动授予 / 收回；自动徽章收回被拒。
- `Badges/visits_test.go`：同日重复只算 1 天；跨天 +1；Redis 不可用降级路径。
- `Forum/badge_reader_test.go`：批量装饰、无徽章时字段为 nil。
- `Forum/activity_hooks_test.go`：写路径 bump 的 metric 与 delta 正确。

**Go 集成（真实 PostgreSQL，一次性集群）**
- `Badges/badges_postgres_integration_test.go`：并发 10 次评估同一用户 → `user_badges` 恰好 1 行；发帖 / 评论 / 点赞 / 收藏后 `user_activity` 精确 +1；取消点赞后 `like_received_count` 回退且不为负；`user_visits` 同日并发插入只 1 行、`days_visited` 不重复加；迁移 up/down 实机可逆。
- `Support/HostAPI/v2_query_postgres_badges_integration_test.go`：三个查询的允许 / 拒绝两侧。
- `database/migrations/stable_core_views_test.go`：三视图谓词与禁止列。

**Go 契约与 HTTP**
- `Http/Controllers/Badges/badges_http_test.go`：403（开关关）/ 404 / 409 / 422 全码路径；后台权限 `badge.view` 与 `badge.manage` 的允许 / 拒绝两侧。
- `badge_cache_scope_test.go`：公开徽章端点缓存头；后台端点 `private, no-store`。

**Web**
- `apps/web/tests/profile/profileBadgeTab.test.ts`：分组渲染、tier 排序、进度计算、开关关闭时 tab 不存在。
- `apps/web/tests/forum/badgeChip.test.ts`：`aria-label` 完整文案、无徽章不渲染、点击目标正确。
- `apps/web/tests/admin/badges.test.ts`：规则表单校验、授予对话框用户搜索、重算按钮状态。
- i18n 键齐全性（zh-CN 与 en-US 键集合一致）。

**浏览器 QA**（真实会话，1633×810 + 390×844）
- 资料页第 5 tab 出现 / 开关关闭后消失；分组与进度渲染正确。
- 帖子旁 chip 出现且只显示最高级 1 个；390 无横向溢出、英文不裁切。
- 后台徽章页：列表、新建、编辑规则、手动授予、重算；标题/工具栏对齐、无横向滚动。

**门禁**
`go build ./...`、`./scripts/test-api-isolated.sh`、`bun test`、`bun run typecheck`、`node tests/validate-architecture-boundaries.mjs`、`ruby scripts/validate-openapi-refs.rb`、`node scripts/v3-catalog/generate.mjs --check`、`./scripts/test.sh --fast`。

---

## 8. 明确不做（第一刀）

- 信任等级（下一刀，站在本刀的统计层上）。
- 关注 / 订阅（watch / track）、私信。
- 已读帖子追踪（`posts_read_count` / `read_time_seconds` 只预留列，不采集）。
- `/badges` 公共目录页（要 Page Registry 全套：catalog、route shell、ThemeCompiler ViewModel、Host island、主题模板，成本远超本刀）。
- 徽章排行榜、按徽章筛选用户。
- 多表情反应、反应汇总条。
- 徽章图标上传（只用图标库名字，不引入附件管道）。
- 自动对账周期 job（改为手动重算 + CLI）。
- 主题制品改动。

---

## 9. 假设与风险

1. **`postgres_store_ops.go` 必须先抽取再新增**：1438 → 1265（M1 已抽 `CreateComment` 到 `postgres_store_comment_writes.go`），贴顶且零余量，靠 `TestActivityHooksStayOutOfLegacyBaselineFile` 反向守护。`types.go` **不需要抽取**——实测 921 行、不在基线里（初稿写「998 行、硬上限 1000、必须先抽取」是错的，已由实施方当场纠正并核实）。
2. **`SFTopicShowPage.vue` 是 1127 行 cap**。主题作者旁挂徽章必须先抽 `SFTopicAuthorBlock.vue`，抽取后净减并同轮下调基线；否则门禁红。若抽取发现作者块与详情页状态耦合过紧，退路是**只在评论旁挂 chip，主题作者徽章只出现在资料页**——这会损失一部分可见性，需要在实施时确认。
3. **统计同步写在事务内**会略微拉长写事务（一次 `INSERT ... ON CONFLICT DO UPDATE`）。量级与现有的 `like_count` 更新相同，可接受；若实测写延迟上升，退路是改成事件 + 异步累加（但要接受漂移和对账负担）。
4. **`top_badge_id` 是反规范化**。徽章停用 / 删除 / 规则重算都会让它短暂失真。视图与读模型都按 `badges.enabled` 过滤，所以最坏情况是「显示了一个刚停用的徽章」，不会泄露数据。
5. **徽章进缓存**意味着授予后最多 20–30s 才在帖子旁可见。用户点完「重算」看不到即时变化是正常的，后台文案要说明。
6. **`features.bookmarks` 文案缺失**是既有缺陷，本刀顺带修；修完要确认后台开关页正常渲染。
7. **后台 12 条路由是本刀体量最大的部分**。若时间紧张，可先砍「分组 CRUD」（用固定三个分组，只允许在徽章编辑里选），路由降到 8 条；这不会影响产品闭环。
8. **12 个内置徽章的阈值是默认值**，不是共识。运营者按社区规模改；改完要提醒点「重算」才会追溯授予。

---

## 10. 实施顺序与依赖

```
M0 迁移 ──→ M1 统计层 ──→ M2 徽章领域 ──→ M3 读模型/HTTP ──→ M5 前端
                │                │                │
                └────────────────┴──→ M4 通知 ───┘
                                          │
M6 扩展数据面（依赖 M0 视图）──────────────┴──→ M7 目录/基线/知识库 ──→ M8 验证
```

M1 与 M2 可并行（不同文件域）。M4 依赖 M2 的授予事件形状。M5 前端依赖 M3 的响应 schema 冻结。M6 只依赖 M0 的视图，可与 M2/M3 并行。

---

## 11. 实施决断（2026-11-10 补充，回应验收清单 A1–A13）

验收清单见 `reports/2026-11-10-badges-acceptance-criteria.md`。以下决断覆盖其中全部阻塞项与待定项，**以此节为准**，前文与之冲突处按本节执行。

### D1 统计数字的 UI 落点（回应 A2）

资料页新增两项数字，放在现有 `joinedOn` 同一行的资料摘要区：

| 数字 | 来源 | 是否新增 |
| --- | --- | --- |
| 主题数 | `Profile` 现有实时 `COUNT(*)` | 保持不动 |
| 回复数 | `Profile` 现有实时 `COUNT(*)` | 保持不动 |
| 分类数 | `Profile` 现有实时 `COUNT(*)` | 保持不动 |
| **获赞数** | `user_activity.like_received_count` | **新增** |
| **访问天数** | `user_activity.days_visited` | **新增** |

**共 5 项，`categoryCount` 保留。** 修订说明：初稿写成 4 项（漏看了现有统计条第三项是「参与分类数」）。删掉一个已上线的公开数字属于产品回归，超出本刀授权——本刀是「加徽章」，不是「重做统计条」。因此保留，总数 5。

布局：≥768 一行 5 格；<768 用 2 列、末项 `grid-column: span 2` 占满（避免现有固定 3 列 + 每格 border 造成的半格缺口）。具体 CSS 以视觉规范 §3.7 定稿为准。

**刻意保留双源**：主题/回复继续走实时 `COUNT(*)`（删除后立即正确，是用户对「我有多少帖」的直觉），获赞/访问天数走 `user_activity` 快照（是「累计成就」，删除内容不回退，见 D4）。两者语义不同，不强行统一；README 与模块笔记要写明这一点，避免后来者以为是 bug。

给赞数、收藏数**不在资料页展示**（它们是行为计数，公开展示价值低且增加窥探面），只用于徽章进度。因此 AC-7 的断言落点是：`features.badges` 关闭后，**获赞数与访问天数仍然显示**，只有徽章 tab 与 chip 消失。

### D2 自动授予的触发时机（回应 A3、A13）

**新增 River job `badges.evaluate_user`**（M2 漏项，必须补）：

- 触发点（全部 fail-open，入队失败只记日志，不影响请求）：
  - 主题/评论创建事务提交后
  - 点赞/取消事务提交后（目标作者与操作者各入队一次）
  - 收藏/取消事务提交后
  - `VisitRecorder` 记录成功且 `days_visited` 变化后
- 队列 `QueueDefault`，`MaxAttempts: 3`，去重键 `badges:evaluate:{userID}`（同一用户短时间多次触发合并为一次，用 River 的 unique opts 或入队前查近期 job）
- Job 体：读 `user_activity` 单行 → 匹配启用规则 → 差集 → 授予 → 写通知 → 重算 `top_badge_id`
- **最迟可见时间**：队列正常时 ≤ 5s；缓存 TTL 再叠 20–30s（帖子旁）。验收按「≤ 10s 出现在资料页徽章 tab」判 PASS（资料页不走 Forum 缓存）。
- 兜底：后台 `POST /admin/badges/recompute` 全量重算

### D3 自赞的统计口径（回应 A5）

**自赞不计入 `like_received_count` 与 `like_given_count`。**
内容的 `topics.like_count` / `comments.like_count` 照常 +1（保持既有行为不变）。

理由：内容计数是热度，个人统计是成就。允许自赞计入成就会让「给自己点 10 个赞」直接刷出徽章，这是明确的滥用路径。

### D4 统计回退语义（回应 A9）

按「关系行 vs 内容行」区分，语义是「你做过什么」，不是「你现在有什么」：

| 动作 | 回退？ | 理由 |
| --- | --- | --- |
| 取消点赞 | **回退** `like_given_count` / `like_received_count` | 关系行被删除，行为被撤销 |
| 取消收藏 | **回退** `bookmark_count` | 同上 |
| 删除主题（软删） | **不回退** `topic_count` | 内容行仍存在，只是不可见；贡献已发生 |
| 删除评论（软删） | **不回退** `comment_count` | 同上 |
| 主题/评论被审核驳回 | **不回退** | 审核是可见性判定，不是行为撤销 |
| 用户被 ban | **不回退**（数据保留，展示按 `users.status='active'` 过滤） | 可能解封 |

因此：`user_activity.topic_count` 与 Profile 的实时 `COUNT(*)` **在删除后会不一致**，这是设计而非缺陷（见 D1）。

### D5 `sforum badge:reconcile` CLI（回应 A8）

**本刀不做 CLI。** 只交付后台「重算」按钮（`POST /admin/badges/recompute`）+ 模块笔记里一段文档化对账 SQL（统计列与真值表对账、`user_badges` 与规则对账）。
计划 §6 第 10 行的「提供 `sforum badge:reconcile` CLI」作废，改为上述。理由：CLI 要接 `cmd/sforum` 的依赖装配，成本高于本刀收益；重算按钮已覆盖运营场景。

### D6 资料页 tab 顺序（回应 A10）

`topics / replies / bookmarks / badges / about`。徽章插在「收藏」之后、「关于」之前，内置四项位置不变，扩展 widget tab 仍在「关于」之后。

### D7 DOM 契约（回应 A6）

以 `reports/2026-11-10-badge-visual-spec.md` 为准，冻结为：

- chip 根元素 class：`sf-badge-chip`，`data-tier="bronze|silver|gold"`，`data-badge-slug="{slug}"`
- chip `aria-label` 模板：zh `{tierName}徽章：{name}`，en `{tierName} badge: {name}`（例：`金牌徽章：众望所归`）
- 资料页 tab value：`badges`；tab 容器 `role="tablist"`，每项 `role="tab"` + `aria-selected`
- 资料页徽章卡 class：`sf-profile-badge-card`，未获得态加 `data-earned="false"`
- 进度条 `role="progressbar"` + `aria-valuenow` / `aria-valuemax` / `aria-valuetext`

### D8 aria 判定标准（回应 A11）

采纳四条可断言规则：① 每个 chip 有非空 `aria-label` 且同时含徽章名与 tier 名；② tab 容器与 tab 项角色正确且 `aria-selected` 唯一；③ 进度条三个 aria 值齐全；④ 无不可聚焦的交互元素（可点元素必须是 button/a 或有 tabindex + role）。

### D9 后台 URL（回应 A12）

- 前端页面：`/control-panel/badges`（受 `NUXT_PUBLIC_ADMIN_ROUTE_PREFIX` 重写，实际值以 dev 环境为准，前端负责确认并在 QA 记录里写死）
- API：`/api/v1/admin/badges`（不带 admin 前缀变量）

### D10 并发口径（回应 A13）

两条都跑：① 并发 10 次 `POST /admin/badges/recompute`；② 并发 10 次发帖（触发写路径评估）。两条都要断言 `user_badges` 恰好 1 行、`user_activity` 计数精确。

### D11 测试账号（回应 A1）

验收前由运维准备并写入 `storage/qa/creds.txt`（单一口径，旧脚本里写死的过期密码必须一并改掉）：

| 代号 | 角色 | 用途 |
| --- | --- | --- |
| 甲 | member | 被统计者 / 徽章持有者 |
| 乙 | member | 给甲点赞、评论 |
| 丙 | member（零基线） | 验证「未获得」与首次授予 |
| 丁 | 持 `badge.view` 的管理员 | 验证只读权限不能写 |
| 戊 | 持 `badge.manage` 的管理员 | 验证 CRUD / 授予 / 重算 |

管理员账号优先用 dev 库现成的 `super_admin`；若密码未知，用 `sforum` CLI 重置（不要手工改库）。密码统一为 `QaPass!2026-devops`。

---

## 12. 实施陷阱记录（M3-C 核查所得，动手前必读）

以下由实施方在 M3 阶段做 SQL→扫描器配对核查时发现并核实，**不是推测**。写在这里是因为它们会静默产出脏数据，不写下来下一轮（或下一个会话）一定会再踩。

### T1 `topicSummarySQL()` 配的是 `scanTopicSummaryWithAvatar`，不是 `scanTopicSummary`

`apps/api/app/Models/Forum` 里有两个名字极像的扫描器：

| SQL | 正确的扫描器 | 误用后果 |
| --- | --- | --- |
| `topicSummarySQL()` | `scanTopicSummaryWithAvatar` | — |
| （另一套列布局） | `scanTopicSummary` | 它通过 `ScanTopicSummary` **导出给 Profile 域复用**，配的是不同的列布局 |

**按「名字最像」去改 `scanTopicSummary`，会让 Profile 的主题列表静默错列**——不报错，是把 excerpt 读成 status 这类脏数据。只改 `scanTopicSummaryWithAvatar`。

### T2 users 别名已核实为 `users`

`topicSummarySQL` / `topicDetailSQL` / `commentSelectSQL` 三处都是 `LEFT JOIN users ON users.id = ...`，没有 `u` / `authors` 别名。但**评论的 `users` 是评论作者，不是主题作者**，JOIN 条件要按各自的表写。

### T3 追加列只能加在列尾

`lastReplyAuthorSelectSQL()` 之后 / `posts.render_version` 之后 / `comments.updated_at` 之后。这样 scan 只需在末尾补 4 个 `sql.NullString`，**不用重排既有索引**——重排索引是这类改动最容易出错的地方。

### T4 五个扫描器都要补，且 NULL 路径必须有用例

`scanTopicSummaryWithAvatar` / `scanTopicDetail` / `scanTopicDetailWithAvatar` / `scanComment` / `scanCommentWithAvatar`。

`BadgeRef` 四个字段全部可能为 NULL（LEFT JOIN 没匹配），必须用 `sql.NullString` 接再转，**绝不直接扫进 string**。并且**必须补一个「用户没有徽章」的用例**——本地测试数据都有徽章的话，这条路径根本测不出来。

### T5 三个 SQL 的调用方全要跟着验

- `topicSummarySQL()`：GetTopicForComment、GetTopicForAction、`list_topics.go` hydrate、`postgres_store_bookmarks.go` 书签列表、`search_live.go` 搜索结果
- `topicDetailSQL()`：CreateTopic 回读、topic_queries 的 GetTopic / GetTopicBySlug、topic_mutations 的 update 回读
- `commentSelectSQL()`：`scanComment` 与 `scanCommentWithAvatar`，且评论列表有分页/树形两条路径

只跑新测试不够，必须跑全量 `./app/Models/Forum/...` 加 `./app/Http/Controllers/Forum/...`。

### T6 动了 SQL 或写事务，`go test` 绿不算绿

M1 与 M3 都只跑了无 DB 的 `go test` 自证，直到 M3 修 P0 时才跑 `./scripts/test-api-isolated.sh`，当场炸出两条：

1. `column users.top_badge_id does not exist` —— `revisionLedgerFixtureBaseSchemaSQL` 的 users 表没跟上迁移，而 C 的 JOIN 引用了它
2. `user_activity violates FK user_activity_user_id_fkey` —— fixture schema 里没有 `user_activity`，search_path 回落到 public 的真实表，而 fixture 用户只存在于隔离 schema

**纪律：凡改动涉及 SQL、写事务、或新增表引用，必须跑 `./scripts/test-api-isolated.sh`，不能只信 `go test`。** 无 DB 的单测证明的是「代码逻辑」，证明不了「SQL 与真实 schema 对得上」。

### T7 fixture base schema 是第二份 schema 定义，会静默落后

`revisionLedgerFixtureBaseSchemaSQL` 里的建表语句是真实迁移的**手工副本**。新增列/表后不补它，症状是隔离测试红在看起来毫不相关的地方（FK 违反、列不存在）。

本刀补的是：`users.top_badge_id` + `badges` / `user_activity` / `user_visits` 三个最小 stub（只保留读路径与 hook 用到的列，非负约束省略——fixture 只关心列存在与同事务写入）。**这条注释必须留在 fixture 里**，否则下一个人会以为约束是漏写的。

### T8 新写的守卫测试要先证明它会误报

为 ops 那个「权限键没落库」事故加的 `TestEverySeededPermissionReachesTheDatabase`（seeds.go ↔ 迁移奇偶校验）首次运行就红，报 `attachment.upload_policy.manage` 缺失——实际存在，是正则 `\(` 不允许换行，而那条迁移是多行 VALUES。

**一个从没红过的守卫测试，不能证明它能抓到问题。** 它先因为格式差异误报一次，才说明它的检查逻辑真的在跑。

### T9 给既有 Service 加方法前，先看它离接收者上限多远

M3 一开始把 `BadgesFeatureEnabled` 加在 `Options.Service` 上，架构门禁当场报 **49 > 48**（`receiverMethodCaps` 里 `Options.Service` 的上限是 48）。改法：挪到 provider 层做成 `badgesFeatureGate` 适配器，`Options.Service` 回到 48。

**纪律：新增功能开关、查询方法、辅助函数时，先 `grep` 目标类型当前接收者方法数，与 `tests/architecture-boundaries-baseline.json` 的 `receiverMethodCaps` 对一下。** 这一步只要 10 秒，省掉一次红门禁 + 一次返工。同理适用于 `Forum.Service`（77）、`Identity.Service`（47）、`Extensions.Service`（70）。

### T10 守卫测试的终极证据是「把它该抓的错造出来，看它红」

`GrantFromRules` 的六个 metric 分支只验 `topic_count` 时，其余五行靠「SQL 结构合法 + `badges_metric_check` 约束」保证——但 CASE 分支最容易犯的错是复制粘贴改漏列名，那种错结构合法、约束拦不住、跑起来不报错，只是给错的人授予徽章。

补齐表驱动测试后，实施方做了一次**变异验证**：把 `CASE` 里 `like_given_count` 那行故意改成读 `activity.like_received_count`，测试立刻红（`metric like_given_count: 达标用户未被授予`），改回后全绿。

**这比测试通过本身更有说服力**：它证明了六行 CASE 确实被逐条执行，而不是被 `topic_count` 一行覆盖后「看起来都过了」。

纪律：为「分支覆盖」写的守卫测试，交付前至少做一次变异——改坏一个分支，确认测试红，再改回。没做过变异的「全覆盖」只是行数统计。

### T11 前端：`defineEmits` 的类型化写法在这套 vue-tsc 下会吃掉载荷类型

抽 `SFCommentAuthorRow` 时，`defineEmits<{ previewCapture: [event: MouseEvent] }>()` 生成的 `onPreviewCapture` 被推断成 `() => any`，父层传 `(event: MouseEvent) => void` 直接 TS2322；换成调用签名写法同样失败。最终改用运行时声明 `defineEmits(['previewToggle', 'previewCapture'])` 才通过。

后果：**「capture 阶段把原生事件原样交给父层」这条链是靠运行时约定保住的，不是类型保住的**。改动这两个事件时必须手工确认载荷，编译器不会拦。

### T12 前端：外部点击判定用稳定 class，不要跨组件持有 DOM 节点

原本父层用 `authorPreviewTrigger.contains(target)` 判断外部点击；抽成子组件后父层拿不到子组件的 button。可选方案有三个：

| 方案 | 问题 |
| --- | --- |
| `defineExpose` 暴露 ref | 类型化 ref 会反过来影响模板里的 props/events 校验 |
| 跨层持有 DOM 节点 | 组件内部结构调整就失效 |
| **`target.closest('.sf-comment__author-trigger')`** | 依赖 class 名，而 class 是组件契约的一部分 |

选了第三个。**判定依据选「组件契约里稳定存在的东西」（class / data 属性），比「跨层持有节点引用」更耐改**——前者是公开约定，后者是内部实现细节。
