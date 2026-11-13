package badges

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 徽章领域的真库集成测试。
//
// 这里验的四件事都是**单测证明不了**的：
//   1. 并发评估的幂等（单测的 fake store 串行执行，测不出竞争）
//   2. 统计列在真实 UPDATE 下的精确性（含负 delta 的钳零）
//   3. user_visits 同日并发（复合主键 + ON CONFLICT 的真实行为）
//   4. top_badge_id 的 SQL 排序（fake 只复刻了逻辑，没验过 ORDER BY 三个关键字）
//
// schema 用与 migrations/202611100002 同形的 DDL 建在隔离 schema 里：
// 复用真实迁移链会让每个 fixture 争抢固定 schema（sforum_core_v1），
// 而这里要验的是这几张表的语义，不是迁移能否重放（那由 migrations 包的测试守）。

const badgesIntegrationSchemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  top_badge_id BIGINT,
  -- 202611130003：展示位是「当前展示的徽章」，manual 表示它是用户选的。
  -- fixture 必须跟着迁移走：RecomputeTopBadge / RecomputeAll 的 SQL 会读写它，
  -- 漏列会让整包测试以「column users.top_badge_manual does not exist」变红。
  top_badge_manual BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE badge_groups (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0,
  -- ListGroups 会扫这两列（公开资料页徽章墙要走分组），fixture 漏列会让
  -- Service.ListUserBadges 直接 500（column badge_groups.created_at does not exist）。
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE badges (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  group_id BIGINT REFERENCES badge_groups(id) ON DELETE SET NULL,
  tier TEXT NOT NULL CHECK (tier IN ('bronze','silver','gold')),
  icon TEXT NOT NULL DEFAULT 'i-lucide-award',
  grant_mode TEXT NOT NULL CHECK (grant_mode IN ('automatic','manual')),
  metric TEXT,
  threshold BIGINT,
  rule JSONB,
  requirement_summary_zh TEXT NOT NULL DEFAULT '',
  requirement_summary_en TEXT NOT NULL DEFAULT '',
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  position INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- 与 202611120002 之后的定义一致：自动徽章只认 rule，legacy 两列可空。
  CONSTRAINT badges_rule_check CHECK (
    (grant_mode = 'automatic' AND rule IS NOT NULL)
    OR (grant_mode = 'manual' AND rule IS NULL)
  )
);

CREATE TABLE user_badges (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  badge_id BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
  grant_reason TEXT NOT NULL DEFAULT '',
  CONSTRAINT user_badges_unique UNIQUE (user_id, badge_id)
);

CREATE TABLE user_activity (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  topic_count BIGINT NOT NULL DEFAULT 0 CHECK (topic_count >= 0),
  comment_count BIGINT NOT NULL DEFAULT 0 CHECK (comment_count >= 0),
  like_received_count BIGINT NOT NULL DEFAULT 0 CHECK (like_received_count >= 0),
  like_given_count BIGINT NOT NULL DEFAULT 0 CHECK (like_given_count >= 0),
  bookmark_count BIGINT NOT NULL DEFAULT 0 CHECK (bookmark_count >= 0),
  days_visited INTEGER NOT NULL DEFAULT 0 CHECK (days_visited >= 0),
  posts_read_count BIGINT NOT NULL DEFAULT 0,
  read_time_seconds BIGINT NOT NULL DEFAULT 0,
  last_visited_on DATE,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_visits (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  visited_on DATE NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, visited_on)
);

-- Recompute 需要的最小内容表：只要作者列与目标 id。
-- 真实迁移里这些表字段多得多，这里只保留 Recompute / conditions 的 SQL 会读到的部分。
CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  author_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- activity.count 的窗口分支按 created_at 数，且 Postgres 在解析期就会校验列，
  -- 即使本次只查 window.all 也必须有这一列。
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  author_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)
);

-- 事实表也要在 fixture schema 里：否则不带 schema 前缀的查询会落到 public 的真实表，
-- 而本 fixture 的 user 只存在于隔离 schema，外键会直接炸（也会污染真实表）。
-- rule_health / rule_store 会查这五张表：必须建在 fixture schema 里，否则会落到
-- 真实 public（search_path 带 public，见计划 §13 残留 14）。
-- objectRef 候选（RefCandidates）要读分类/标签/角色的「展示名 + 是否可见」：
-- 列补齐时全部给默认值，既有测试只插 slug/key 也照常成立；漏列会让查询落到
-- 真实 public 表（search_path 带 public），所以新增读取列必须同批补进 DDL。
CREATE TABLE categories (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  visibility TEXT NOT NULL DEFAULT 'public'
);
CREATE TABLE tags (
  id BIGSERIAL PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active'
);
CREATE TABLE roles (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  alias TEXT NOT NULL DEFAULT '',
  is_enabled BOOLEAN NOT NULL DEFAULT TRUE
);
CREATE TABLE identity_external_links (
  id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL, provider_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active'
);

CREATE TABLE user_fact_counters (
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fact_key TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '',
  total BIGINT NOT NULL DEFAULT 0,
  last_occurred_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, fact_key, variant)
);

CREATE TABLE badge_facts (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  fact_key TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '',
  delta BIGINT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  source_extension_id TEXT NOT NULL DEFAULT '',
  dedupe_key TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX badge_facts_dedupe_idx
  ON badge_facts (user_id, fact_key, variant, dedupe_key) WHERE dedupe_key <> '';

CREATE TABLE content_bookmarks (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

type badgesPGFixture struct {
	ctx  context.Context
	pool *pgxpool.Pool
}

func newBadgesPGFixture(t *testing.T) *badgesPGFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("badges_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	// 注意 `search_path` 带 public（全仓 86 处同款写法）：fixture 里漏建的表会静默
	// 解析到真实 public 表。本刀不重构这个模式（见计划 §13 残留 14），所以**每加一张
	// 被查询的表，必须同批加进上面的 DDL**，否则读的是脏数据、写的是真实库。
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, badgesIntegrationSchemaSQL); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("apply badges fixture DDL: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return &badgesPGFixture{ctx: ctx, pool: pool}
}

func (f *badgesPGFixture) user(t *testing.T, username string, status string) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO users (username, display_name, status) VALUES ($1,$1,$2) RETURNING id`,
		username, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *badgesPGFixture) badge(t *testing.T, slug, tier string, position int, metric string, threshold int64) int64 {
	t.Helper()
	var id int64
	// 自 202611120002 起自动徽章只认 rule；fixture 同时写 rule（迁移同款单叶子形状）
	// 与 legacy 的 metric/threshold，模拟迁移后的真实数据形态。
	rule := fmt.Sprintf(`{"v":1,"root":{"type":"activity.count","params":{"metric":%q,"window":{"kind":"all"}},"cmp":{"op":"gte","value":%d}}}`, metric, threshold)
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
		VALUES ($1,$1,$2,'automatic',$3,$4,$6::jsonb,TRUE,$5) RETURNING id`,
		slug, tier, metric, threshold, position, rule).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestConcurrentEvaluationGrantsExactlyOnce 断言并发评估只落一行。
//
// 这是 M1 那句「单测证明不了并发最终数值」的直接兑现：10 个 goroutine 同时
// 评估同一用户，靠 ON CONFLICT DO NOTHING 收敛到 1 行。
func TestConcurrentEvaluationGrantsExactlyOnce(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "alice", "active")
	fixture.badge(t, "first-topic", "bronze", 0, "topic_count", 1)

	store := NewPostgresStore(fixture.pool)
	evaluator := NewEvaluator(store, NewPostgresActivityStore(fixture.pool))
	counters := ActivityCounters{UserID: userID, TopicCount: 3}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := evaluator.EvaluateWithCounters(context.Background(), userID, counters); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent evaluate: %v", err)
	}

	var rows int
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT count(*) FROM user_badges WHERE user_id=$1`, userID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("user_badges rows = %d, want exactly 1", rows)
	}
}

// TestActivityCountersAreExactAndClamped 断言统计列的精确性与钳零。
func TestActivityCountersAreExactAndClamped(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "bob", "active")
	store := NewPostgresActivityStore(fixture.pool)
	ctx := context.Background()

	bump := func(metric ActivityMetric, delta int64) {
		t.Helper()
		tx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.BumpTx(ctx, tx, userID, metric, delta); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	bump(MetricTopicCount, 1)
	bump(MetricCommentCount, 1)
	bump(MetricCommentCount, 1)
	bump(MetricLikeReceivedCount, 1)
	bump(MetricLikeReceivedCount, -1) // 取消点赞回退
	bump(MetricLikeGivenCount, -1)    // 从未点过就回退：必须钳到 0
	bump(MetricBookmarkCount, 1)

	counters, err := store.GetCounters(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.TopicCount != 1 || counters.CommentCount != 2 {
		t.Fatalf("topic=%d comment=%d, want 1/2", counters.TopicCount, counters.CommentCount)
	}
	if counters.LikeReceivedCount != 0 {
		t.Fatalf("like_received = %d, want 0 after add+remove", counters.LikeReceivedCount)
	}
	if counters.LikeGivenCount != 0 {
		t.Fatalf("like_given = %d, want 0 (负 delta 必须钳零，不能回绕)", counters.LikeGivenCount)
	}
	if counters.BookmarkCount != 1 {
		t.Fatalf("bookmark = %d, want 1", counters.BookmarkCount)
	}
}

// TestConcurrentRecordVisitCountsOneDay 断言同日并发只记一天。
func TestConcurrentRecordVisitCountsOneDay(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "carol", "active")
	store := NewPostgresActivityStore(fixture.pool)
	day := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	var mu sync.Mutex
	inserted := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.RecordVisit(context.Background(), userID, day)
			if err != nil {
				t.Errorf("record visit: %v", err)
				return
			}
			if ok {
				mu.Lock()
				inserted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if inserted != 1 {
		t.Fatalf("inserted = %d, want exactly 1 (复合主键必须收敛并发)", inserted)
	}
	var visits int
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT count(*) FROM user_visits WHERE user_id=$1`, userID).Scan(&visits); err != nil {
		t.Fatal(err)
	}
	if visits != 1 {
		t.Fatalf("user_visits rows = %d, want 1", visits)
	}
	counters, err := store.GetCounters(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.DaysVisited != 1 {
		t.Fatalf("days_visited = %d, want 1", counters.DaysVisited)
	}

	// 跨天：第二天必须重新计数。
	nextDay := day.Add(24 * time.Hour)
	if ok, err := store.RecordVisit(context.Background(), userID, nextDay); err != nil || !ok {
		t.Fatalf("next day record = %v/%v, want true/nil", ok, err)
	}
	counters, err = store.GetCounters(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if counters.DaysVisited != 2 {
		t.Fatalf("days_visited = %d, want 2", counters.DaysVisited)
	}
}

// TestRecomputeTopBadgeUsesSQLOrdering 断言 top_badge_id 的 SQL 排序：
// gold > silver > bronze，同级 position 升序。
//
// fake store 复刻的是逻辑顺序；这里验的是真实 ORDER BY 三个关键字在 Postgres 下的行为。
func TestRecomputeTopBadgeUsesSQLOrdering(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "dave", "active")
	bronze := fixture.badge(t, "first-topic", "bronze", 0, "topic_count", 1)
	goldLate := fixture.badge(t, "widely-acclaimed", "gold", 5, "like_received_count", 500)
	goldEarly := fixture.badge(t, "veteran", "gold", 2, "days_visited", 180)

	for _, badgeID := range []int64{bronze, goldLate, goldEarly} {
		if _, err := fixture.pool.Exec(fixture.ctx,
			`INSERT INTO user_badges (user_id, badge_id) VALUES ($1,$2)`, userID, badgeID); err != nil {
			t.Fatal(err)
		}
	}

	store := NewPostgresStore(fixture.pool)
	if err := store.RecomputeTopBadge(fixture.ctx, userID); err != nil {
		t.Fatal(err)
	}
	var top *int64
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT top_badge_id FROM users WHERE id=$1`, userID).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top == nil || *top != goldEarly {
		t.Fatalf("top_badge_id = %v, want gold with smaller position (%d)", top, goldEarly)
	}

	// 停用 gold 徽章后，必须落到 silver/bronze 里最高的那个。
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE badges SET enabled = FALSE WHERE tier='gold'`); err != nil {
		t.Fatal(err)
	}
	if err := store.RecomputeTopBadge(fixture.ctx, userID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT top_badge_id FROM users WHERE id=$1`, userID).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top == nil || *top != bronze {
		t.Fatalf("top_badge_id = %v, want bronze after disabling gold", top)
	}
}

// TestRecomputeRepairsRelationCountersOnly 是 Recompute 口径的真库证明。
//
// 计划 D4/D1：重算修的是**关系行漂移**（点赞/收藏/访问天数），
// 不抹**事件累计历史**（topic_count / comment_count）——后者记的是「你做过什么」，
// 内容软删或审核驳回都不回退。
//
// 这条测试的关键是后半句：把 topic_count 也改成错值，断言它**没被改**。
// 只验「错值被修好」不够——那无法区分「只重算三列」与「全都重算」。
func TestRecomputeRepairsRelationCountersOnly(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := context.Background()

	author := fixture.user(t, "erin", "active")
	liker := fixture.user(t, "frank", "active")

	var topicID int64
	if err := fixture.pool.QueryRow(ctx,
		`INSERT INTO topics (author_user_id) VALUES ($1) RETURNING id`, author).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	var commentID int64
	if err := fixture.pool.QueryRow(ctx,
		`INSERT INTO comments (author_user_id) VALUES ($1) RETURNING id`, author).Scan(&commentID); err != nil {
		t.Fatal(err)
	}
	// liker 赞了作者的主题与评论各一次；外加一条自赞（必须不计入）。
	for _, row := range []struct {
		userID     int64
		targetType string
		targetID   int64
	}{
		{liker, "topic", topicID},
		{liker, "comment", commentID},
		{author, "topic", topicID}, // 自赞：Recompute 里显式排除
	} {
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO content_reactions (user_id, target_type, target_id, reaction)
			VALUES ($1,$2,$3,'like')`, row.userID, row.targetType, row.targetID); err != nil {
			t.Fatal(err)
		}
	}
	// liker 收藏了一条内容。
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO content_bookmarks (user_id, target_type, target_id)
		VALUES ($1,'topic',$2)`, liker, topicID); err != nil {
		t.Fatal(err)
	}
	// 访问两天。
	for _, day := range []string{"2026-11-09", "2026-11-10"} {
		if _, err := fixture.pool.Exec(ctx, `
			INSERT INTO user_visits (user_id, visited_on) VALUES ($1,$2::date)`, liker, day); err != nil {
			t.Fatal(err)
		}
	}

	// 手工制造漂移：关系行派生列写错，事件累计列也写错。
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
		                           like_given_count, bookmark_count, days_visited)
		VALUES ($1, 42, 43, 99, 98, 97, 96)`, liker); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresActivityStore(fixture.pool)
	counters, err := store.Recompute(ctx, liker)
	if err != nil {
		t.Fatal(err)
	}

	// 关系行派生：必须被拉回真值。liker 赞了主题与评论各一次。
	if counters.LikeGivenCount != 2 {
		t.Fatalf("like_given = %d, want 2", counters.LikeGivenCount)
	}
	if counters.LikeReceivedCount != 0 {
		t.Fatalf("like_received = %d, want 0 (没人赞 liker 的内容)", counters.LikeReceivedCount)
	}
	if counters.BookmarkCount != 1 {
		t.Fatalf("bookmark = %d, want 1", counters.BookmarkCount)
	}
	if counters.DaysVisited != 2 {
		t.Fatalf("days_visited = %d, want 2", counters.DaysVisited)
	}
	// 事件累计历史：必须原样保留（这条才是本测试的价值）。
	if counters.TopicCount != 42 {
		t.Fatalf("topic_count = %d, want 42 untouched (重算不得抹掉事件累计)", counters.TopicCount)
	}
	if counters.CommentCount != 43 {
		t.Fatalf("comment_count = %d, want 43 untouched", counters.CommentCount)
	}

	// 自赞排除：author 被 liker 赞了主题与评论各一次（=2），自己也赞了自己主题一次。
	// 正确结果是 2；如果自赞被计入会是 3。
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, like_received_count) VALUES ($1, 77)`, author); err != nil {
		t.Fatal(err)
	}
	authorCounters, err := store.Recompute(ctx, author)
	if err != nil {
		t.Fatal(err)
	}
	if authorCounters.LikeReceivedCount != 2 {
		t.Fatalf("author like_received = %d, want 2 (自赞若被计入会是 3)", authorCounters.LikeReceivedCount)
	}
}

// TestRecomputeGrantsMissingBadgesFromRules 是「阈值下调后点重算能追溯授予」的真库证明。
//
// 修前 Recompute 只刷 top_badge_id、不补授予，运营调低阈值后点按钮什么都不会发生。
// 这条同时钉住三件事：批量 SQL 真的插入了行、来源标记正确、以及统计历史不被抹。
func TestRecomputeGrantsMissingBadgesFromRules(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := context.Background()

	userID := fixture.user(t, "grace", "active")
	// 阈值 10，用户已有 10 个主题：达标但还没被授予过（模拟「运营刚调低阈值」）。
	badgeID := fixture.badge(t, "topic-author", "bronze", 0, "topic_count", 10)
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, topic_count) VALUES ($1, 10)`, userID); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(fixture.pool)
	admin := NewAdminService(store)
	actor := identity.Actor{ID: 1, Permissions: map[string]bool{PermissionBadgeView: true, PermissionBadgeManage: true}}

	result, err := admin.Recompute(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != 1 {
		t.Fatalf("granted = %d, want 1 (阈值已达标应补授予)", result.Granted)
	}

	var grantedBy *int64
	var reason string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT granted_by, grant_reason FROM user_badges WHERE user_id=$1 AND badge_id=$2`,
		userID, badgeID).Scan(&grantedBy, &reason); err != nil {
		t.Fatalf("expected a granted row: %v", err)
	}
	if grantedBy != nil {
		t.Fatalf("granted_by = %v, want NULL (批量重算没有具体操作者)", *grantedBy)
	}
	if reason != "recompute" {
		t.Fatalf("grant_reason = %q, want recompute", reason)
	}

	// 幂等：再跑一次不应重复插入。
	again, err := admin.Recompute(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if again.Granted != 0 {
		t.Fatalf("second run granted = %d, want 0", again.Granted)
	}

	// 重算不得抹掉事件累计历史。
	var topicCount int64
	if err := fixture.pool.QueryRow(ctx, `SELECT topic_count FROM user_activity WHERE user_id=$1`, userID).Scan(&topicCount); err != nil {
		t.Fatal(err)
	}
	if topicCount != 10 {
		t.Fatalf("topic_count = %d, want 10 untouched", topicCount)
	}
}

// TestGrantFromRulesCoversEveryMetricBranch 逐 metric 覆盖批量授予的 CASE 分支。
//
// 为什么必须逐条验：CASE 分支最容易犯的错不是结构错，而是复制粘贴时改漏列名
// （例如 like_given 那行写成 like_received）。那种错**结构合法、约束拦不住、
// 真库也不报错**，只是给错的人授予徽章。只有「这个指标达标、那个没达标」的
// 成对断言才能抓到它。
//
// 每个 metric 造两个用户：一个刚好等于阈值（应授予），一个差 1（不应授予）。
func TestGrantFromRulesCoversEveryMetricBranch(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := context.Background()

	// metric → user_activity 的列名（与迁移里的 badges_metric_check 白名单一致）。
	columns := map[string]string{
		"topic_count":         "topic_count",
		"comment_count":       "comment_count",
		"like_received_count": "like_received_count",
		"like_given_count":    "like_given_count",
		"bookmark_count":      "bookmark_count",
		"days_visited":        "days_visited",
	}

	const threshold = int64(5)
	type expectation struct {
		metric  string
		badgeID int64
		hit     int64 // 刚好达标
		miss    int64 // 差 1
	}
	expectations := make([]expectation, 0, len(columns))
	position := 0
	for metric := range columns {
		badgeID := fixture.badge(t, "rule-"+metric, "bronze", position, metric, threshold)
		position++
		hit := fixture.user(t, "hit-"+metric, "active")
		miss := fixture.user(t, "miss-"+metric, "active")
		// 只给该 metric 对应的列赋值，其余列保持 0——这样如果 SQL 里串了列名，
		// 命中/未命中的组合会立刻对不上。
		if _, err := fixture.pool.Exec(ctx, fmt.Sprintf(`
			INSERT INTO user_activity (user_id, %s) VALUES ($1, $2)`, columns[metric]),
			hit, threshold); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(ctx, fmt.Sprintf(`
			INSERT INTO user_activity (user_id, %s) VALUES ($1, $2)`, columns[metric]),
			miss, threshold-1); err != nil {
			t.Fatal(err)
		}
		expectations = append(expectations, expectation{metric: metric, badgeID: badgeID, hit: hit, miss: miss})
	}

	store := NewPostgresStore(fixture.pool)
	recompute, err := store.GrantFromRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recompute.ComplexBadgeIDs) != 0 {
		t.Fatalf("全部是快路径形状，不应有徽章转入异步评估：%v", recompute.ComplexBadgeIDs)
	}
	if recompute.Granted != int64(len(expectations)) {
		t.Fatalf("granted = %d, want %d (每个 metric 只应授予刚好达标的那个用户)", recompute.Granted, len(expectations))
	}
	if int64(len(recompute.Rows)) != recompute.Granted {
		t.Fatalf("收集到的行 %d 应等于授予行数 %d", len(recompute.Rows), recompute.Granted)
	}

	for _, item := range expectations {
		var hitRows int
		if err := fixture.pool.QueryRow(ctx,
			`SELECT count(*) FROM user_badges WHERE user_id=$1 AND badge_id=$2`, item.hit, item.badgeID).Scan(&hitRows); err != nil {
			t.Fatal(err)
		}
		if hitRows != 1 {
			t.Fatalf("metric %s: 达标用户未被授予", item.metric)
		}
		var missRows int
		if err := fixture.pool.QueryRow(ctx,
			`SELECT count(*) FROM user_badges WHERE user_id=$1 AND badge_id=$2`, item.miss, item.badgeID).Scan(&missRows); err != nil {
			t.Fatal(err)
		}
		if missRows != 0 {
			t.Fatalf("metric %s: 差 1 的用户被误授予（CASE 分支可能串了列名）", item.metric)
		}
	}

	// 未启用 / 手动徽章即使达标也不得被批量授予。
	manualID := int64(0)
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, enabled, position)
		VALUES ('manual-honor','手动荣誉','gold','manual',TRUE,99) RETURNING id`).Scan(&manualID); err != nil {
		t.Fatal(err)
	}
	disabledID := fixture.badge(t, "disabled-rule", "silver", 98, "topic_count", 1)
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET enabled=FALSE WHERE id=$1`, disabledID); err != nil {
		t.Fatal(err)
	}
	again, err := store.GrantFromRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Granted != 0 {
		t.Fatalf("second run granted = %d, want 0 (手动/停用徽章不得被批量授予，且已授予幂等)", again)
	}
}

// TestFastPathSQLSubsetOfGoFastPath 是 lead 要求的兜底断言：
// **SQL 选中的徽章集合必须是 Go `fastPathRule()` 选中集合的子集**。
//
// 为什么必须有这条：批量 SQL 是手写的 jsonb 谓词，Go 侧是结构化判断，两边各写一次
// 就一定会漂移。漂移的方向决定后果——SQL 多选（把 `lte 5` 当成 `>= 5`）是**批量级
// 语义错误**；SQL 漏选只是变慢（那些徽章会进 backfill 队列）。所以断言写成
// 「SQL 授予的集合 ⊆ Go 快路径集合」，并额外要求 12 枚内置形状一个都不漏。
func TestFastPathSQLSubsetOfGoFastPath(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "all-metrics", "active")
	// 所有指标都给一个大值，任何「gte 小阈值」的规则都会命中——于是「谁被授予」
	// 只由「这条规则有没有进 SQL」决定，与阈值无关。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count, like_received_count,
		                           like_given_count, bookmark_count, days_visited)
		VALUES ($1, 1000, 1000, 1000, 1000, 1000, 1000)`, userID); err != nil {
		t.Fatal(err)
	}

	type ruleCase struct {
		slug string
		rule string
		fast bool
	}
	builtin := []ruleCase{
		{"r-days", `{"v":1,"root":{"type":"activity.count","params":{"metric":"days_visited","window":{"kind":"all"}},"cmp":{"op":"gte","value":7}}}`, true},
		{"r-topic", `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`, true},
		{"r-comment", `{"v":1,"root":{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":25}}}`, true},
		{"r-like-rx", `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_received_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":100}}}`, true},
		{"r-like-gv", `{"v":1,"root":{"type":"activity.count","params":{"metric":"like_given_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}}}`, true},
		{"r-bookmark", `{"v":1,"root":{"type":"activity.count","params":{"metric":"bookmark_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":10}}}`, true},
	}
	nonFast := []ruleCase{
		// 语义反转风险最大的一条：lte 若被 SQL 当 gte，会批量授予全站。
		{"x-lte", `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"lte","value":5}}}`, false},
		{"x-window", `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"recent_days","days":30}},"cmp":{"op":"gte","value":1}}}`, false},
		{"x-group", `{"v":1,"root":{"op":"all","children":[{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}}`, false},
		{"x-two-leaves", `{"v":1,"root":{"op":"all","children":[{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}},{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}}`, false},
		{"x-boolean", `{"v":1,"root":{"type":"identity.email_verified"}}`, false},
		{"x-zero", `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":0}}}`, false},
	}

	expectFast := map[string]bool{}
	for _, tc := range append(append([]ruleCase{}, builtin...), nonFast...) {
		var id int64
		if err := fixture.pool.QueryRow(fixture.ctx, `
			INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
			VALUES ($1,$1,'bronze','automatic','topic_count',1,$2::jsonb,TRUE,0) RETURNING id`,
			tc.slug, tc.rule).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if tc.fast {
			expectFast[tc.slug] = true
		}
	}

	store := NewPostgresStore(fixture.pool)
	recompute, err := store.GrantFromRules(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	granted, queuedIDs := recompute.Granted, recompute.ComplexBadgeIDs

	rows, err := fixture.pool.Query(fixture.ctx, `
		SELECT badges.slug FROM user_badges
		JOIN badges ON badges.id = user_badges.badge_id
		WHERE user_badges.user_id = $1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	sqlSelected := map[string]bool{}
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			t.Fatal(err)
		}
		sqlSelected[slug] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if granted != int64(len(sqlSelected)) {
		t.Fatalf("granted=%d 与实际行数 %d 不符", granted, len(sqlSelected))
	}
	// 方向一：SQL 不许选中 Go 认为不是快路径的徽章（语义错）。
	for slug := range sqlSelected {
		if !expectFast[slug] {
			t.Fatalf("SQL 选中了非快路径形状 %q —— 谓词比 fastPathRule 宽，语义会反转", slug)
		}
	}
	// 方向二：内置形状不许漏（漏了只是变慢，但说明谓词写窄了）。
	for slug := range expectFast {
		if !sqlSelected[slug] {
			t.Fatalf("内置形状 %q 没被 SQL 选中（谓词过窄）", slug)
		}
	}
	// 非快路径徽章必须全部进队列，且数量按徽章算。
	if len(queuedIDs) != len(nonFast) {
		t.Fatalf("queued 徽章数 = %d，want %d", len(queuedIDs), len(nonFast))
	}
}

// TestEvaluateRulePassesFactKeysThrough 是 task-4 rev6 的「接通」断言：
// `PostgresStore.EvaluateRule` 必须把已声明的事实 key 透传给 resolver。
//
// M1 不注入 provider → `fact.count` 恒 unknown（fail-closed，正确）。
// 注入 `wiki.edit` 后同一条规则必须变成**可判定**：有事实行时按计数比较，
// 没有事实行时是确定的 0。不接通这条链路，M3 上线后所有插件事实条件都会
// 「规则存得下、永远不满足」，且零报错。
func TestEvaluateRulePassesFactKeysThrough(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	userID := fixture.user(t, "fact-user", "active")
	store := NewPostgresStore(fixture.pool)
	rule, err := ruleengine.Decode([]byte(`{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"all"}},"cmp":{"op":"gte","value":2}}}`))
	if err != nil {
		t.Fatal(err)
	}

	// 未注入：unknown（fail-closed）。
	result, err := store.EvaluateRule(fixture.ctx, userID, rule)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || !result.Incomplete {
		t.Fatalf("M1 未声明 factKey 时必须 unknown，得到 %+v", result)
	}

	// 注入后：事实行 2 → 满足。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_fact_counters (user_id, fact_key, variant, total) VALUES ($1,'wiki.edit','',2)`, userID); err != nil {
		t.Fatal(err)
	}
	store.SetFactKeyProvider(func() []string { return []string{"wiki.edit"} })
	result, err = store.EvaluateRule(fixture.ctx, userID, rule)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || result.Incomplete {
		t.Fatalf("声明 factKey 且有 2 次事实时应满足，得到 %+v", result)
	}

	// 已声明但没有事实行：确定的 0（不是 unknown）。
	otherRule, err := ruleengine.Decode([]byte(`{"v":1,"root":{"type":"fact.count","params":{"factKey":"wiki.edit","window":{"kind":"all"}},"cmp":{"op":"lte","value":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	store.SetFactKeyProvider(func() []string { return []string{"wiki.edit"} })
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM user_fact_counters WHERE user_id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	result, err = store.EvaluateRule(fixture.ctx, userID, otherRule)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || result.Incomplete {
		t.Fatalf("已声明但无事实行应为确定 0（满足 lte 1），得到 %+v", result)
	}
}

// TestEvaluatePureRuleBadgeWithoutLegacyMetric 是 M1.4-02：`metric`/`threshold` 全为
// NULL、只有 `rule` 的自动徽章必须能正常授予。
//
// 这条覆盖的是本刀最贵的失败模式：若求值写成「先看 metric，没有再看 rule」，
// 纯 rule 徽章会「保存成功、用户达标、永远不发、零报错」。既有夹具全带 legacy
// metric，所以那种写法在别的测试里照样全绿。
func TestEvaluatePureRuleBadgeWithoutLegacyMetric(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	userID := fixture.user(t, "pure-rule", "active")

	var badgeID int64
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
		VALUES ('pure-rule', 'Pure Rule', 'bronze', 'automatic', NULL, NULL,
		        '{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}}'::jsonb,
		        TRUE, 0) RETURNING id`).Scan(&badgeID); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(fixture.pool)
	evaluator := NewEvaluator(store, NewPostgresActivityStore(fixture.pool))

	// 未达标：不授予。
	if granted, err := evaluator.EvaluateWithCounters(ctx, userID, ActivityCounters{TopicCount: 2}); err != nil {
		t.Fatal(err)
	} else if len(granted) != 0 {
		t.Fatalf("未达标不应授予，得到 %d 枚", len(granted))
	}
	// 达标：授予。metric/threshold 为 NULL 不影响判定。
	granted, err := evaluator.EvaluateWithCounters(ctx, userID, ActivityCounters{TopicCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(granted) != 1 || granted[0].Slug != "pure-rule" {
		t.Fatalf("纯 rule 徽章达标必须授予，得到 %+v", granted)
	}
	var metric, threshold *string
	if err := fixture.pool.QueryRow(ctx, `SELECT metric, threshold::text FROM badges WHERE id = $1`, badgeID).Scan(&metric, &threshold); err != nil {
		t.Fatal(err)
	}
	if metric != nil || threshold != nil {
		t.Fatalf("夹具的 legacy 两列必须为 NULL（否则没验到纯 rule 路径）")
	}
	// 把规则改严：不授予。
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET rule = jsonb_set(rule, '{root,cmp,value}', '99'::jsonb) WHERE id = $1`, badgeID); err != nil {
		t.Fatal(err)
	}
	if granted, err := evaluator.EvaluateWithCounters(ctx, userID, ActivityCounters{TopicCount: 3}); err != nil {
		t.Fatal(err)
	} else if len(granted) != 0 {
		t.Fatalf("规则改严后不应再授予，得到 %d 枚", len(granted))
	}
}

// TestGrantSourcesAreDistinguishable 是 M1.4-04：三来源判别式必须逐条钉住。
//
// 只验回扫那一行会让另外两条失效而无人发现——运维审计靠的就是这三行互不混淆：
//   - 规则自动：granted_by IS NULL, grant_reason = ”
//   - 批量重算：granted_by IS NULL, grant_reason = 'recompute'
//   - 管理员手动：granted_by = 操作者 id, grant_reason = 操作者填的理由
func TestGrantSourcesAreDistinguishable(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)

	automaticID := fixture.badge(t, "auto", "bronze", 0, "topic_count", 1)
	recomputeID := fixture.badge(t, "recompute", "bronze", 1, "topic_count", 1)
	manualID := fixture.badge(t, "manual", "bronze", 2, "topic_count", 1)
	if _, err := fixture.pool.Exec(ctx, `UPDATE badges SET grant_mode='manual', metric=NULL, threshold=NULL, rule=NULL WHERE id=$1`, manualID); err != nil {
		t.Fatal(err)
	}

	type source struct {
		userID int64
		reason *string
		by     *int64
	}
	read := func(badgeID, userID int64) source {
		t.Helper()
		var out source
		if err := fixture.pool.QueryRow(ctx, `
			SELECT grant_reason, granted_by FROM user_badges WHERE badge_id=$1 AND user_id=$2`,
			badgeID, userID).Scan(&out.reason, &out.by); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// 1) 规则自动：走 GrantAutomatic。
	autoUser := fixture.user(t, "auto-user", "active")
	if inserted, err := store.GrantAutomatic(ctx, autoUser, automaticID); err != nil || !inserted {
		t.Fatalf("GrantAutomatic: inserted=%v err=%v", inserted, err)
	}
	if got := read(automaticID, autoUser); got.by != nil || got.reason == nil || *got.reason != "" {
		t.Fatalf("规则自动行必须是 NULL/''，得到 %+v", got)
	}

	// 2) 批量重算：走 GrantFromRules。
	recomputeUser := fixture.user(t, "recompute-user", "active")
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, topic_count) VALUES ($1, 5)`, recomputeUser); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GrantFromRules(ctx); err != nil {
		t.Fatal(err)
	}
	if got := read(recomputeID, recomputeUser); got.by != nil || got.reason == nil || *got.reason != "recompute" {
		t.Fatalf("重算行必须是 NULL/'recompute'，得到 %+v", got)
	}

	// 3) 管理员手动：走 GrantManual，带操作者与理由。
	operatorID := fixture.user(t, "operator", "active")
	manualUser := fixture.user(t, "manual-user", "active")
	reason := "社区贡献"
	if inserted, err := store.GrantManual(ctx, GrantInput{UserID: manualUser, BadgeID: manualID, GrantedBy: operatorID, GrantReason: reason}); err != nil || !inserted {
		t.Fatalf("GrantManual: inserted=%v err=%v", inserted, err)
	}
	if got := read(manualID, manualUser); got.by == nil || *got.by != operatorID || got.reason == nil || *got.reason != reason {
		t.Fatalf("手动行必须带操作者与理由，得到 %+v", got)
	}
}
