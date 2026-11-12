# 徽章规则引擎：12 个内置条件的参考 SQL（M1.3）

Status: **read-only recon**（只读核实，未改 `apps/api/` 任何文件）
基线：`d502f19a3` + 工作区未跟踪的 `apps/api/app/Support/RuleEngine/`、`database/migrations/202611120002_badge_rules.sql`
条件目录来源：`knowledge/plans/2026-11-12-badge-rule-engine.md` §5.2（12 行）
EXPLAIN 环境：dev PostgreSQL 容器 `sforum-postgres`（127.0.0.1:15432），实测行数 topics=1305 / comments=5464 / topic_tags=2389 / users=161 / user_roles=134 / content_reactions=62 / content_bookmarks=17 / user_visits=6

> **关于 EXPLAIN 的诚实边界**：以下「实测」都是在上面这组行数上跑出来的真实计划；`content_reactions`(62)、`content_bookmarks`(17)、`user_visits`(6)、`user_profiles`(161) 这几张表在 dev 里太小，planner 会选 Seq Scan —— 这是成本选择，不是索引缺失。每条我都注明「实测计划」与「数据量起来后的预期计划」，两者不混写。每条都给了可复现的 `EXPLAIN` 命令。

---

## 0. 三条全局口径（先定这个，再写 SQL）

### 0.1 `topic_count` / `comment_count` 是「做过什么」的历史累计，**窗口 SQL 不许过滤 status**

`apps/api/app/Models/Badges/activity_store.go:116-122`
```go
// topic_count 与 comment_count 不在这里重算：它们记的是「贡献已发生过」，
// 内容软删或被审核驳回都不回退（计划 D4）。用现存内容行数去覆盖它们，等于把
// 「你做过什么」改成「你现在还有什么」，与资料页的双源语义（D1）直接冲突。
```

`apps/api/app/Models/Forum/postgres_store_topic_mutations.go:114-115`
```go
	// 计划 D4：topic_count 记「你发过多少个主题」，创建即计数；软删不回退。
	if err := bumpUserActivityTx(ctx, tx, input.AuthorUserID, activityMetricTopicCount, 1); err != nil {
```
评论同构：`app/Models/Forum/postgres_store_comment_writes.go:119`。全仓无 `-1` 的 topic/comment 回退调用（`rg "bumpUserActivityTx\(ctx, tx, [^,]+, [^,]+, -1\)"` 无命中）。

**结论**：窗口版 `activity.count` 统计 `topics` / `comments` 行时**不加 `status` 过滤、不加 `deleted_at IS NULL`**。软删行还在表里，天然与计数器一致。加过滤就会与 `window.all`（走计数器）给出互相矛盾的两个答案 —— 同一个用户同一枚徽章，`all` 说满足、`recent_days` 说不满足。

### 0.2 `like_*` / `bookmark_count` 是「现存关系行」，可以重算，窗口 SQL 天然一致

`activity_store.go:141-168` 的 `Recompute` 明确只重算四类：`days_visited`、`like_given`、`like_received`、`bookmark_count`。取消点赞/取消收藏是**删行**（`app/Models/Forum/postgres_store_reactions.go:93-96` `DELETE FROM content_reactions`），所以计数器 = 现存行数，窗口版数行即一致。

### 0.3 `visited_on` 的日界 = **API 进程本地时区**，不是 `site.timezone`

这是第 3 项的核心结论，见 §3 的完整证据。**所有涉及「今天」的 SQL 都不能用 `CURRENT_DATE`**，必须由 Go 侧算好日期再作为 `$n::date` 传入。

---

## 1. `activity.count`（6 个 metric × 2 种 window）

### 1.1 `window.kind = "all"` —— 直接读反规范化计数器（**快路径，SQL 与今天完全一致**）

`$1 = user_id`；`%[1]s` 只能取 `activityMetricColumns` 里的列名（`app/Models/Badges/activity.go:99` 的注释：「`%[1]s` 只能是 activityMetricColumns 里的列名」，白名单在 `activity_hooks.go:38-39` / `activity.go:26` 同款）。

```sql
SELECT %[1]s FROM user_activity WHERE user_id = $1;
```
`%[1]s` ∈ `topic_count | comment_count | like_received_count | like_given_count | bookmark_count | days_visited`

- **索引**：`user_activity_pkey`（PK `user_id`，`202611100002_badges_and_user_activity.sql:18`）
- **EXPLAIN（实测，user_id=3）**：`Seq Scan on user_activity ... Filter: (user_id = 3)` —— 表只有几十行；数据量起来后是 `Index Scan using user_activity_pkey`。无排序。
- **边界**：无行时 `GetCounters` 返回零值结构（`activity_store.go:73-76` 的 `pgx.ErrNoRows` 分支），**不要**用 `COUNT(*)` 兜底，那会改变既有 12 枚徽章的行为。
- **对 M1.3 的影响**：这就是计划 §5.3 的「快路径」。单叶子 `activity.count` + `window.all` 必须走这条，不要进引擎新路径。

### 1.2 `window.kind = "recent_days"`，`days = $2`

六个 metric 逐个给。**统一不加 status 过滤**（见 §0.1）。

**(a) `topic_count`**
```sql
SELECT count(*) FROM topics
WHERE author_user_id = $1
  AND created_at >= now() - make_interval(days => $2);
```
- 索引：`topics_author_created_idx (author_user_id, created_at DESC)` —— `202607060003_forum_topics_comments_posts.sql:57`
- EXPLAIN（实测，author=3，30 天）：`Bitmap Heap Scan on topics` → `Bitmap Index Scan on topics_author_created_idx`，`Index Cond: ((author_user_id = 3) AND (created_at >= ...))`。两列都进 Index Cond，**无排序**。
- 边界：`author_user_id` 是 `REFERENCES users(id) ON DELETE SET NULL`（同文件 `:38`），用户被硬删后主题归属变 NULL，这类行不再计入任何人的计数 —— 与计数器口径的偏差只在「用户被物理删除」时出现，可接受。

**(b) `comment_count`**
```sql
SELECT count(*) FROM comments
WHERE author_user_id = $1
  AND created_at >= now() - make_interval(days => $2);
```
- 索引：`comments_author_created_idx (author_user_id, created_at DESC)` —— 同文件 `:81`
- EXPLAIN（实测，author=485，30 天）：`Bitmap Heap Scan on comments` → `Bitmap Index Scan on comments_author_created_idx`，两列进 Index Cond，无排序。

**(c) `like_given_count`**
```sql
SELECT count(*) FROM content_reactions
WHERE user_id = $1 AND reaction = 'like'
  AND created_at >= now() - make_interval(days => $2);
```
- 索引：`content_reactions_user_idx (user_id, created_at DESC)` —— `202610280001_content_reactions.sql:27`
- EXPLAIN（实测）：`Seq Scan on content_reactions ... Filter: (...)`（表只有 62 行）。**数据量起来后预期 `Index Scan using content_reactions_user_idx`**，`reaction` 走 Filter（该索引不含 reaction 列）。
- 边界：`reaction` 目前只有 `'like'`（CHECK `content_reactions_reaction_check`，同文件 `:16`），但**必须显式写 `reaction = 'like'`**，否则将来加新 reaction 时口径会静默变宽。

**(d) `bookmark_count`**
```sql
SELECT count(*) FROM content_bookmarks
WHERE user_id = $1
  AND created_at >= now() - make_interval(days => $2);
```
- 索引：`content_bookmarks_user_idx (user_id, created_at DESC, id DESC)` —— `202611100001_content_bookmarks.sql:32`
- EXPLAIN（实测）：`Seq Scan on content_bookmarks`（17 行）。预期：`Index Scan using content_bookmarks_user_idx`。
- 边界：**不按 `visibility` 过滤** —— 私密收藏也是「收藏过」，与 `bookmark_count` 计数器（数全部行，`activity_store.go:153`）一致。

**(e) `like_received_count`** —— 作者侧，见 §4 的完整形状与 EXPLAIN
```sql
SELECT
  (SELECT count(*) FROM content_reactions r JOIN topics t ON t.id = r.target_id
    WHERE r.target_type = 'topic' AND r.reaction = 'like'
      AND t.author_user_id = $1 AND r.user_id <> $1
      AND r.created_at >= now() - make_interval(days => $2))
+ (SELECT count(*) FROM content_reactions r JOIN comments c ON c.id = r.target_id
    WHERE r.target_type = 'comment' AND r.reaction = 'like'
      AND c.author_user_id = $1 AND r.user_id <> $1
      AND r.created_at >= now() - make_interval(days => $2));
```

**(f) `days_visited`**
```sql
SELECT count(*) FROM user_visits
WHERE user_id = $1
  AND visited_on >= $3::date - ($2::int - 1);
```
（`$3` = Go 侧算好的「今天」，见 §3）
- 索引：`user_visits_pkey (user_id, visited_on)` —— `202611100002_badges_and_user_activity.sql:37`
- EXPLAIN（实测）：`Bitmap Heap Scan on user_visits` → `Bitmap Index Scan on user_visits_pkey`，`Index Cond: ((user_id = 3) AND (visited_on >= '2026-10-13'::date))`。无排序。
- 边界：`days_visited` 计数器 = `COUNT(*) FROM user_visits WHERE user_id=$1`（`activity_store.go:151`），窗口版是它的子集，口径天然一致。

---

## 2. `activity.streak{minDays}` —— 时区是本条的全部风险

### SQL

```sql
WITH islands AS (
  SELECT visited_on,
         visited_on - (row_number() OVER (ORDER BY visited_on))::int AS grp
  FROM user_visits
  WHERE user_id = $1
),
runs AS (
  SELECT max(visited_on) AS last_day, count(*) AS len
  FROM islands GROUP BY grp
)
SELECT COALESCE(max(len) FILTER (WHERE last_day BETWEEN $2::date - 1 AND $2::date), 0)
FROM runs;
```
`$2` = 「今天」的 `DATE`，**由 Go 用与写入端同一个时钟算出后传入**。

- **索引**：`user_visits_pkey (user_id, visited_on)`
- **EXPLAIN（实测，8 行）**：
  - 默认：`Bitmap Heap Scan on user_visits` → `Bitmap Index Scan on user_visits_pkey`，然后 `Sort (Sort Key: visited_on)` 供窗口用，再 `GroupAggregate (Group Key: g.grp)`。
  - `SET enable_bitmapscan=off` 后：`Index Only Scan using user_visits_pkey`，**窗口那层 Sort 消失**，只剩 `Sort (Sort Key: g.grp)` 供 `GROUP BY` 用。
- **结论**：这条的 Sort 有两层，容易看错。数据量正常时（一个用户最多几千行）PG 走有序索引扫描，窗口 `ORDER BY` 不需要额外排序；剩下那个 `Sort Key: g.grp` 是分组用的，**不可避免也不需要避免**（分组行数 ≤ 用户访问天数）。dev 里 8 行时 planner 选 bitmap + 多一次 sort，是成本选择。
- 复现：
```sql
SET enable_bitmapscan=off;
EXPLAIN <上面的 SQL>;
```

### 时区结论（第 3 项重点）

**日界 = API 进程本地时区（`time.Now()` 的 Location），不是 `site.timezone`。** 三条证据：

1. 写入用 `time.Now`：`apps/api/app/Models/Badges/visits.go:86-90`
```go
	return &VisitRecorder{
		store:  store,
		dedupe: dedupe,
		now:    time.Now,
		ttl:    visitKeyTTL,
	}
```
   消费点 `visits.go:115` `day := r.now()`；构造处 `apps/api/bootstrap/api_assembly_domains.go:328` **没有**注入任何时钟覆盖。

2. Redis 去重键也按这个 date 的本地格式：`visits.go:21-23`
```go
func VisitDedupKey(userID int64, day time.Time) string {
	return fmt.Sprintf("%s%d:%s", visitKeyPrefix, userID, day.Format("2006-01-02"))
}
```
   写入列 `recordVisitSQL` 把同一个 `time.Time` 直接塞进 `DATE`（`activity.go:106-108`），pgx 按该 time 自身的 Location 取年月日。

3. 站点时区**存在但不被这段代码消费**：选项 `site.timezone`（`app/Models/Options/types.go:40`），默认 `"UTC"`（`app/Models/Options/site_datetime_options.go:13`），校验走 `Support/Timezone` 内嵌 tzdata（同文件 `:63-70`）。但全仓 `rg "site.timezone|NameSiteTimezone"` 在 `app/Models/Options` 之外**零命中** —— 没有任何消费方，访问采集更没读它。

**因此**：`CURRENT_DATE` / `now()::date` 在 SQL 里是错的 —— PostgreSQL 的 `CURRENT_DATE` 取的是**数据库会话 TimeZone**（容器里通常 UTC），与 API 进程的 `TZ` 不一定相同。API 容器若设了 `TZ=Asia/Shanghai` 而 PG 是 UTC，跨零点那 8 小时里「今天」两边差一天：写入记 `2026-11-13`，SQL 认为今天是 `2026-11-12`，`BETWEEN $2-1 AND $2` 就把刚记的今天排除掉 → **连续 7 天在零点后立刻误判为断签**。

**给老后的实现要求**：`activity.streak` 与 `activity.count{metric:days_visited, window:recent_days}` 的日期边界必须来自**注入的时钟**（`VisitRecorder.now` 同源），由 Go 算 `today := clock.Now().Format("2006-01-02")` 后作为 `$n::date` 传参。若将来要让 streak 跟 `site.timezone` 走，必须**同时改写入端**（`visits.go` 的 `now` 换成「站点时区的今天」），否则读写两个日界，比不改更糟。

---

## 3. `object.engagement{action, target}`

`target` 形状按计划是 `{kind:"topic", id}`，`$1 = user_id`、`$2 = topic_id`。

**`action = "like"`**
```sql
SELECT count(*) FROM content_reactions
WHERE user_id = $1 AND target_type = 'topic' AND target_id = $2 AND reaction = 'like';
```
- 索引：唯一约束 `content_reactions_unique (user_id, target_type, target_id, reaction)`（`202610280001_content_reactions.sql:19`）就是完美匹配的索引；另有 `content_reactions_roster_idx (target_type, target_id, reaction, created_at DESC, id DESC)` 可用于目标侧。
- EXPLAIN（实测）：`Seq Scan on content_reactions ... Filter: (...)`（62 行）。预期：`Index Scan using content_reactions_unique`。
- 边界：**自赞算数**。自赞会真的写行（`postgres_store_reactions.go:34-37` 注释：「自赞返回 0,0，两个 like 计数都不动（内容行 like_count 已照常更新，那是热度）」—— 行在，只有计数器跳过）。`object.engagement` 问的是「关系存在吗」，所以不加 `user_id <> author`。

**`action = "bookmark"`**
```sql
SELECT count(*) FROM content_bookmarks
WHERE user_id = $1 AND target_type = 'topic' AND target_id = $2;
```
- 索引：`content_bookmarks_unique (user_id, target_type, target_id)`（`202611100001_content_bookmarks.sql:23`）
- EXPLAIN（实测）：`Seq Scan on content_bookmarks`（17 行）。预期：唯一索引扫描。
- 边界：私密/公开都算（`visibility` 不参与）。

**`action = "comment"`**
```sql
SELECT count(*) FROM comments
WHERE topic_id = $2 AND author_user_id = $1;
```
- 索引：`comments_topic_root_idx (topic_id, root_comment_id, path_key) WHERE status='active'`（`202607060003_...sql:78-79`）**不能**直接服务这条（部分索引不含 author）；实际会走 `comments_author_created_idx` + Filter `topic_id`，或 `comments_topic_path_idx (topic_id, path_key)` 后 Filter author。数据量下建议用 `EXISTS` 形态让 planner 自选。
- 边界：**自评论算数**（计划 P-11 裁决），所以不加 `author_user_id <> 主题作者`；也**不加 status 过滤** —— 与 `comment_count` 含软删一致。若老后想排除 `pending/rejected`，必须同时改 `comment_count` 口径，否则两处矛盾。

---

## 4. 窗口「获赞」的作者侧 SQL（第 4 项重点）

### 权威形状：直接抄 `Recompute`，不要自创

`apps/api/app/Models/Badges/activity_store.go:156-170`（现存唯一权威口径，含自赞排除）：
```sql
(
	SELECT COUNT(*)
	FROM content_reactions AS reactions
	JOIN topics ON topics.id = reactions.target_id
	WHERE reactions.target_type = 'topic'
	  AND reactions.reaction = 'like'
	  AND topics.author_user_id = $1
	  AND reactions.user_id <> $1
) + (
	SELECT COUNT(*)
	FROM content_reactions AS reactions
	JOIN comments ON comments.id = reactions.target_id
	WHERE reactions.target_type = 'comment'
	  AND reactions.reaction = 'like'
	  AND comments.author_user_id = $1
	  AND reactions.user_id <> $1
)
```

### 建议写法（窗口版）

```sql
SELECT
  (SELECT count(*) FROM content_reactions r JOIN topics t ON t.id = r.target_id
     WHERE r.target_type = 'topic' AND r.reaction = 'like'
       AND t.author_user_id = $1 AND r.user_id <> $1
       AND r.created_at >= now() - make_interval(days => $2))
+ (SELECT count(*) FROM content_reactions r JOIN comments c ON c.id = r.target_id
     WHERE r.target_type = 'comment' AND r.reaction = 'like'
       AND c.author_user_id = $1 AND r.user_id <> $1
       AND c.created_at >= now() - make_interval(days => $2));
```

**`r.user_id <> $1` 不是可选项**：写路径 `activityDeltaForReaction(actor, recipient, add)` 在自赞时返回 `(0,0)`（`postgres_store_reactions.go:41-45` 注释 + 调用点 `:44-49`），`Recompute` 也显式排除。漏了它，窗口版会比计数器多算自赞。

### 命中的索引

| 分支 | 驱动侧 | 内侧 |
| --- | --- | --- |
| topic | `topics_author_created_idx (author_user_id, created_at DESC)` | `content_reactions_roster_idx (target_type, target_id, reaction, created_at DESC, id DESC)` |
| comment | `comments_author_created_idx (author_user_id, created_at DESC)` | 同上 |

`roster_idx` 的前三列 `(target_type, target_id, reaction)` 正好是内侧的等值条件，`created_at DESC` 正好是窗口条件 —— 这个索引就是为这条查询长的。

### EXPLAIN（实测，两条都跑了）

1. `UNION ALL` 形态（两个标量子查询相加）：
```
InitPlan 1 -> Aggregate -> Nested Loop
   -> Seq Scan on content_reactions r  (Filter: target_type='topic' AND reaction='like' AND created_at>=...)
   -> Memoize -> Index Scan using topics_pkey on topics t (Index Cond: id = r.target_id; Filter: author_user_id = 3)
InitPlan 2 -> ... Index Scan using comments_pkey on comments c
```
2. `EXISTS` 形态（`FROM topics t WHERE t.author_user_id=$1 AND EXISTS(...)`）：`Hash Join`，外侧 `Bitmap Heap Scan on topics`（`topics_author_created_idx`），内侧 `HashAggregate` + `Seq Scan on content_reactions`。

**结论（诚实版）**：62 行的 `content_reactions` 上，planner 认为「扫全表再 join」比「从作者内容出发逐条探索引」便宜，两种写法都变成 `Seq Scan on content_reactions`。**这不代表索引无效** —— 数据量起来后内侧会切到 `roster_idx` 的 `Index Scan`。成本随 **该作者的内容条数 × 每个目标的点赞行数** 增长，不随全站 reactions 总量增长（前提是 planner 选 nested loop；若它坚持 hash join，成本就退化为全站 reactions 扫一遍）。

**验证命令**（老后落地时必须在有真实数据量的库上跑）：
```sql
EXPLAIN (ANALYZE, BUFFERS) <上面的窗口 SQL>;
-- 若出现 Seq Scan on content_reactions 且 reactions 行数已上万：
SET enable_hashjoin = off; SET enable_seqscan = off;
EXPLAIN <同上>;   -- 确认 roster_idx 能承接，再决定是否用 LATERAL 强制连接顺序
```

---

## 5. 其余 6 条

### 5.1 `object.authoring{target:{kind:"category"|"tag", slug}, window}`

**category**
```sql
SELECT count(*) FROM topics t
JOIN categories c ON c.id = t.category_id
WHERE t.author_user_id = $1 AND c.slug = $2
  AND t.created_at >= now() - make_interval(days => $3);
```
- 索引：`topics_author_created_idx`（驱动侧）+ `categories_slug_key`（UNIQUE，`202607070003_forum_taxonomy.sql:2-4` 的 `slug TEXT NOT NULL UNIQUE`）
- EXPLAIN（实测）：`Nested Loop` → `Seq Scan on categories`（2 行，slug 走 Filter）+ `Bitmap Heap Scan on topics`（`topics_author_created_idx`）。数据量起来后 categories 侧会切到 `categories_slug_key`。

**tag** —— 见第 2 项重点，表名确认存在
```sql
SELECT count(*) FROM topics t
JOIN topic_tags tt ON tt.topic_id = t.id
JOIN tags g ON g.id = tt.tag_id
WHERE t.author_user_id = $1 AND g.slug = $2
  AND t.created_at >= now() - make_interval(days => $3);
```
- EXPLAIN（实测，tag_id=23 有 117 行）：`Hash Join`，`topic_tags` 侧是 **`Index Only Scan Backward using topic_tags_tag_topic_idx`**（`Index Cond: tag_id = 23`），`topics` 侧 `Bitmap Heap Scan`（`topics_author_created_idx`）。无排序。
- **边界（需要拍板）**：`tags.status` 有 `active / pending / disabled`（`forum_taxonomy.sql:26`）。我的建议是**不按 `tags.status` 过滤** —— 关联事实（这篇主题打过这个标签）在标签被下架后依然成立；若过滤，运营下架一个标签会让已发徽章的依据凭空消失，且徽章不会回收（只影响后续评估），造成「同一条规则两次评估结论不同」。若 lead 要「下架标签不算」，必须同时决定**已发徽章是否回收**。

### 5.2 `identity.account_age{days}`
```sql
SELECT count(*) FROM users
WHERE id = $1 AND created_at <= now() - make_interval(days => $2);
```
- 索引：`users_pkey`
- EXPLAIN：PK 单行查找（未单独跑，与 `user_activity` 同形）。无排序。
- 边界：用 `created_at`（注册时刻），不是 `email_verified_at`。`now()` 在这里只用于「多久以前」，不涉及日界，安全。

### 5.3 `identity.email_verified`
```sql
SELECT count(*) FROM users WHERE id = $1 AND email_verified_at IS NOT NULL;
```
- 列由 `202608010001_identity_email_verification.sql:6` `ALTER TABLE users ADD COLUMN email_verified_at TIMESTAMPTZ` 引入，**无索引**（按 PK 单行读，不需要）。
- 边界：该迁移第 10 行把存量用户回填为 `created_at`（`UPDATE users SET email_verified_at = created_at WHERE email_verified_at IS NULL`）—— 所以「老用户全部已验邮箱」，条件对他们是恒真。这是历史事实，不是 bug。

### 5.4 `identity.oauth_link{providerId}`
```sql
SELECT count(*) FROM identity_external_links
WHERE user_id = $1 AND status = 'active' AND provider_id = $2;
```
- 索引：`identity_external_links_user_status_provider_idx (user_id, status, provider_id)` —— `202607190037_identity_external_links.sql:90-91`，三列与三个谓词**逐列对齐**。
- EXPLAIN（实测）：planner 选了 `Index Scan using identity_external_links_active_provider_digest_uidx`（`provider_id` 进 Index Cond，`user_id` 走 Filter）—— 因为表小、且那个部分索引更窄。**两个索引都能服务；数据量起来后上面那个三列索引才是首选**，不要因为实测看到另一个名字就以为写错了。
- 边界：`status` 有 `active / unlinked / erased`（同文件 `:45`）。**只认 `active`**：`unlinked` 是用户主动解绑，`erased` 是隐私擦除。

### 5.5 `identity.role{roleKey}` —— 第 5 项重点
```sql
SELECT count(*) FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1 AND r.key = $2 AND r.is_enabled;
```
**用 `roles.key`，不要用 `roles.alias`。** 证据：

- `roles` 表：`key TEXT NOT NULL UNIQUE`、`alias TEXT NOT NULL`（**没有 UNIQUE**）—— `202607040001_identity_rbac.sql:30-31`。`alias` 允许重名，做标识会撞车。
- `alias` 明确可改，且改 alias 是常规运营操作：`app/Models/Identity/roles_test.go:9` `func TestMemberAliasCanChangeButRoleCannotBeDeleted`，用例里 `service.UpdateRole(ctx, admin, RoleMember, RoleInput{Alias: "注册用户", ...})` 之后断言 `role.Key != RoleMember` 会失败（即 key 不变）。
- `key` 是代码里的常量入口：`RoleMember` / `RoleSuperAdmin`（同测试 `:15`、`:655-656`）。
- 授权判定也用 key 且过滤停用角色：`app/Models/Identity/postgres_store_permissions.go:26-29`
```sql
		      FROM user_roles
		      JOIN roles ON roles.id = user_roles.role_id
		      WHERE user_roles.user_id = users.id
		        AND roles.is_enabled = TRUE
		        AND roles.key = 'super_admin'
```
- EXPLAIN（实测）：`Nested Loop` → `Index Scan using roles_key_key`（`Index Cond: key = 'member'`，`Filter: is_enabled`）+ `Seq Scan on user_roles`（134 行）。数据量起来后 `user_roles` 侧走 PK `(user_id, role_id)` 前缀。
- **`r.is_enabled` 必须加**：与授权语义一致；停用的角色不应让徽章条件为真。这一条要写进条件说明文案，否则运营停用角色后徽章「不发了」会变成谜。

### 5.6 `identity.avatar`
```sql
SELECT count(*) FROM user_profiles WHERE user_id = $1 AND avatar_attachment_id IS NOT NULL;
```
- 索引：`user_profiles_pkey (user_id)`；另有 `user_profiles_avatar_idx (avatar_attachment_id) WHERE avatar_attachment_id IS NOT NULL`（`202607070004_user_profiles.sql:15-16`），但那是反查用，本条按 PK 读。
- EXPLAIN（实测）：`Seq Scan on user_profiles`（161 行）。预期 PK 单行读。
- 边界：`avatar_attachment_id` 是 `ON DELETE SET NULL`（同文件 `:9`）—— 附件被清后自动回落为「无头像」。用户资料行是懒创建（同文件 `:14` 注释），**没有行 = 无头像**，所以 `count(*) = 0` 即 false，不要写 `NOT EXISTS` 反查。

### 5.7 `content.pinned`
```sql
SELECT count(*) FROM topics WHERE author_user_id = $1 AND is_pinned;
```
- 索引：`topics_author_created_idx`；**没有 `is_pinned` 相关索引**，`is_pinned` 走 Filter。
- EXPLAIN（实测）：`Bitmap Heap Scan on topics`（`topics_author_created_idx`）→ `Filter: is_pinned`。无排序。
- 边界：`is_pinned` 是「被置顶过/当前置顶」的瞬时状态，取消置顶后计数会掉。若要「曾经被置顶过」的历史口径，**表里没有这个信息**（无 `pinned_at`/审计列）—— 未找到，需要 lead 决定是否改成瞬时口径并在文案写明。

### 5.8 `content.hidden` —— 第 1 项重点，语义有坑
```sql
SELECT count(*) FROM topics WHERE author_user_id = $1 AND status = 'hidden';
```

**结论：`topics.status='hidden'` 是纯员工动作，不包含作者自删；但它是「被处罚过」的代理指标，且 `comments` 有独立 hidden 状态，两个条件不可混用。**

证据链：

1. `hidden` 只能由**版主**设置。`app/Models/Forum/service_ops.go:1006-1009`
```go
// canManageTopicVisibility: hide/restore 需要 topic.delete_any。
func canManageTopicVisibility(actor identity.Actor) bool {
	return actor.Can(identity.PermissionTopicDeleteAny)
}
```
   调用点 `service.go:881-884`：
```go
	case TopicActionHide, TopicActionRestore:
		if !canManageTopicVisibility(actor) {
			return TopicLifecycleRecord{}, identity.ErrPermissionDenied
		}
```
   写入 `postgres_store_topic_lifecycle.go:53-55`：`case TopicActionHide: setStatus = TopicStatusHidden`。

2. **作者自删走的是 `deleted`，不是 `hidden`**。`service.go:854`（`DeleteTopic` 路径）：`topic.Status = TopicStatusDeleted`；软删列是 `deleted_at`（`202607060003_...sql:51`）。所以 `hidden` 里**不含**作者自删。

3. 第二条设置路径是举报裁决：`app/Models/Moderation/workbench_store.go:428-432`
```go
	if input.Action == ActionHideAndClose || input.Action == ActionDeleteAndClose {
		status := "hidden"
		if input.Action == ActionDeleteAndClose {
			status = "deleted"
		}
```
   即 `hide_and_close` 也写 `hidden`。

4. `comments` 确实有独立 hidden 状态，且 CHECK 与 topics 不同：`202607060003_...sql:70` `status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','hidden','deleted'))`，后来扩到 `('active','hidden','deleted','pending','rejected')`（`202607110010_moderation_workbenches.sql:37-39`）。评论的 hidden 写入点同样只有 moderation（`workbench_store.go:409`、`:519`）与 `pending`（`revision_writes.go:244`）。**所以「作者被处罚过」如果只看 topics，会漏掉「只被删评/隐评」的用户。**

5. 状态全集比计划写的宽：`202607110010_moderation_workbenches.sql:31-39` 把 topics 扩成 `('active','locked','hidden','deleted','pending','rejected')`、comments 扩成 `('active','hidden','deleted','pending','rejected')`。`pending`（待审）与 `rejected`（驳回）是**审核队列状态**，既不是 hidden 也不是 deleted。

**能不能精确到「有审核记录的那部分」？—— 不能，两条路都不通：**

- `moderation_reports` 不记录最终动作：列只有 `status ∈ (open,reviewing,resolved,rejected)` + `review_note`（`202607070006_moderation_reports.sql:3-16`），**没有 action 列**。所以「resolved 的举报」无法区分 hide 还是 delete 还是驳回。
- `audit_events` 没有内容目标列：`id, actor_user_id, target_user_id, action, metadata JSONB, created_at`（`202607040001_identity_rbac.sql:66-73`）—— 只有 `target_user_id`，主题/评论 id 最多在 `metadata` JSONB 里且无索引。

**给 lead 的选项**（我倾向 A，理由在下面）：

| 选项 | SQL | 代价 |
| --- | --- | --- |
| A. 现状：`status='hidden'` | 上面那条 | 纯员工动作代理；**不含**作者自删（这点是好的），但把「举报裁决 hide」和「版主直接 hide」混在一起；且完全看不到评论侧处罚 |
| B. 收窄到有举报记录 | 加 `AND EXISTS (SELECT 1 FROM moderation_reports mr WHERE mr.target_type='topic' AND mr.target_id=t.id AND mr.status='resolved')` | 会漏掉「版主主动隐藏、无人举报」的情况；且仍分不清 hide/delete 裁决 |
| C. 改名 + 扩到评论 | `topics.status='hidden'` UNION `comments.status='hidden'` | 语义更接近「被处罚过」，但 12 枚内置徽章里没有一条用它，改名不影响存量 |

**我的建议**：条件 key 保留 `content.hidden`，但**文案必须写成「主题被管理员隐藏的次数」**（不是「你被处罚过」），并在 `requirement_summary_zh/en` 里点明「不含作者自删、不含评论」。若 lead 想表达「被处罚过」，应新增一条 `moderation.hidden_content`（topics + comments 两表 UNION），把两者分开——否则将来一定会有人在后台问「我明明只是删了自己的帖，为什么徽章说被隐藏」。

---

## 6. 批量策略判断：12 条里有没有必须多查的？

**结论：没有一条「无法用一条 SQL 表达」。** 逐条核对：

| 条件 | 一条 SQL？ | 说明 |
| --- | --- | --- |
| activity.count × 6 metric | 是 | 6 个 metric 各自一条；`like_received` 是同一语句内的两个子查询，仍是一次往返 |
| activity.streak | 是 | 一个 CTE 链；但**必须传 Go 算的 `$2::date`** |
| object.engagement × 3 action | 是 | 三个 action 是三种不同表，各一条 |
| object.authoring × 2 target | 是 | category / tag 两种 FROM，各一条 |
| identity.account_age / email_verified / oauth_link / role / avatar | 是 | 都是单表或两表 PK/唯一索引查找 |
| content.pinned | 是 | 单表 + Filter |
| content.hidden | 是 | 单表（口径问题另说） |
| badge.owned | 是 | `user_badges JOIN badges` |

**但有三处建议合并往返**（不是「必须」，是「值得」）：

1. **同 type 多叶子参数化**：计划 §5.3 已定「每个 type 一次查询」。`activity.count` 有 6 个 metric、`object.engagement` 有 3 个 action —— 同一次评估里若出现多个，应把参数做成数组一次查完（例如 `metric = ANY($2::text[])` 走 `unnest` + `LEFT JOIN LATERAL`），否则一个 32 叶子的规则最坏会打出 32 次往返。
2. **`activity.count` 的 `window.all` 与 `recent_days` 混用**：`all` 走 `user_activity` 单行读，`recent_days` 走各业务表 —— 这是两种数据源，**无法合并成一条**，但可以合并成「一次 `GetCounters` + 最多 5 条窗口查询」，且窗口查询之间互不依赖，可并发。
3. **`badge.owned` + `object.engagement` 的组合**：**不需要多查**。它们是两个独立叶子，各自一条 SQL；`badge.owned` 的「保存时环检测」（计划 §5.2）是**规则保存期**的校验，不在评估期，与查询次数无关。

**唯一会变成「必须多查」的情况**：若 lead 把 `content.hidden` 改成 C 方案（topics + comments 两表），它仍是一条 SQL（`UNION ALL`）；只有当口径变成「必须结合审计/举报记录精确判定」时才需要多查 —— 而 §5.8 已证明那两张表都查不出精确答案，所以这条不会发生。

---

## 7. 未找到 / 需 lead 拍板

1. **`tags.status='disabled'` 的标签是否计入 `object.authoring{kind:"tag"}`** —— 表里有 `status`（`forum_taxonomy.sql:26`），但没有任何代码注释说明「下架标签的历史关联是否仍算数」。我建议不过滤（见 §5.1），需 lead 确认，并连带决定「已发徽章是否回收」。
2. **`is_pinned` 的历史痕迹** —— 表里只有当前布尔值，无 `pinned_at`、无审计列。「曾经被置顶过」这个口径**未找到**任何数据来源。
3. **`content.hidden` 的最终口径** —— 见 §5.8 的 A/B/C 三选项与建议；这需要 lead 决定，不是实现方能定的。
4. **`site.timezone` 与 `visited_on` 的对齐** —— 现状是「写入用进程本地时区，站点时区选项无人消费」。若产品预期是「站点时区」，那是一个**跨写入端与读取端**的改动，不属于 M1.3；若维持现状，必须在 streak 条件说明里写明「按服务器时区切日」，否则用户会在跨零点时看到「连续 7 天」时断时续。
5. **`identity.role` 是否要包含 `is_super_admin` 隐含权限** —— `super_admin` 走的是 `roles.key='super_admin'` 特判（`postgres_store_permissions.go:22-29`），`user_permission_overrides` 也能给权限（同文件 `:42-44`）。条件只问「有没有这个角色」，不问权限，所以不涉及；但若将来要做 `identity.permission`，那是另一条。
