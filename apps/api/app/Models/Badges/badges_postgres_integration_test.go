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
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE badge_groups (
  id BIGSERIAL PRIMARY KEY,
  key TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  position INTEGER NOT NULL DEFAULT 0
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
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  position INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT badges_rule_check CHECK (
    (grant_mode = 'automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND threshold > 0)
    OR (grant_mode = 'manual' AND metric IS NULL AND threshold IS NULL)
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
-- 真实迁移里这些表字段多得多，这里只保留 Recompute 的 SQL 会读到的部分。
CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  author_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  author_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)
);

CREATE TABLE content_bookmarks (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL
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
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, enabled, position)
		VALUES ($1,$1,$2,'automatic',$3,$4,TRUE,$5) RETURNING id`,
		slug, tier, metric, threshold, position).Scan(&id); err != nil {
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
	granted, err := store.GrantFromRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if granted != int64(len(expectations)) {
		t.Fatalf("granted = %d, want %d (每个 metric 只应授予刚好达标的那个用户)", granted, len(expectations))
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
	if again != 0 {
		t.Fatalf("second run granted = %d, want 0 (手动/停用徽章不得被批量授予，且已授予幂等)", again)
	}
}
