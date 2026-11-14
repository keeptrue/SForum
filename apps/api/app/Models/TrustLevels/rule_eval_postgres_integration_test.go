package trustlevels_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// 真库性能判据（M2 核心验收）。
//
// 这里**不重测条件语义**（那是 `Conditions` 包的职责），只测「判定链路会不会把规则
// 变成查询风暴」：
//   - §6.1 无规则站点零新增查询；
//   - §6.2 8 档各 32 叶 → Prepare 的 SQL 次数 = 出现的条件类型数（与档数无关）；
//   - §6.3 D19-c 探针：某档数值不过 → 该档**独有**的条件类型零 SQL；
//   - §6.4 10k 夹具 P50/P95（`SFORUM_TRUST_PERF=1` 时才跑，见 `TestRuleEvaluationLatency`）。
//
// 计数用 pgx 的 QueryTracer，不靠「感觉变快了」：查询次数是被判定的性质，延迟不是。

// schemaSQL 与 `Conditions` 包的集成测试同形（同一套列名），只保留本文件会碰到的表。
const schemaSQL = `
CREATE TABLE users (
  id BIGSERIAL PRIMARY KEY,
  username TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'active',
  email_verified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE user_activity (
  user_id BIGINT PRIMARY KEY,
  topic_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  like_received_count BIGINT NOT NULL DEFAULT 0,
  like_given_count BIGINT NOT NULL DEFAULT 0,
  bookmark_count BIGINT NOT NULL DEFAULT 0,
  days_visited INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE user_visits (user_id BIGINT NOT NULL, visited_on DATE NOT NULL, PRIMARY KEY (user_id, visited_on));
CREATE TABLE categories (id BIGSERIAL PRIMARY KEY, slug TEXT NOT NULL UNIQUE);
CREATE TABLE tags (id BIGSERIAL PRIMARY KEY, slug TEXT NOT NULL UNIQUE);
CREATE TABLE topics (
  id BIGSERIAL PRIMARY KEY,
  category_id BIGINT NOT NULL,
  author_user_id BIGINT,
  status TEXT NOT NULL DEFAULT 'active',
  is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE topic_tags (topic_id BIGINT NOT NULL, tag_id BIGINT NOT NULL, PRIMARY KEY (topic_id, tag_id));
CREATE TABLE comments (
  id BIGSERIAL PRIMARY KEY,
  topic_id BIGINT NOT NULL,
  author_user_id BIGINT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE content_bookmarks (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'private',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE identity_external_links (
  id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL, provider_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active'
);
CREATE TABLE roles (id BIGSERIAL PRIMARY KEY, key TEXT NOT NULL UNIQUE, is_enabled BOOLEAN NOT NULL DEFAULT TRUE);
CREATE TABLE user_roles (user_id BIGINT NOT NULL, role_id BIGINT NOT NULL, PRIMARY KEY (user_id, role_id));
CREATE TABLE user_profiles (user_id BIGINT PRIMARY KEY, avatar_attachment_id BIGINT);
CREATE TABLE badges (id BIGSERIAL PRIMARY KEY, slug TEXT NOT NULL UNIQUE);
CREATE TABLE user_badges (user_id BIGINT NOT NULL, badge_id BIGINT NOT NULL, PRIMARY KEY (user_id, badge_id));
CREATE TABLE user_fact_counters (
  user_id BIGINT NOT NULL, fact_key TEXT NOT NULL, variant TEXT NOT NULL DEFAULT '',
  total BIGINT NOT NULL DEFAULT 0, PRIMARY KEY (user_id, fact_key, variant)
);
CREATE TABLE badge_facts (
  id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL, fact_key TEXT NOT NULL,
  variant TEXT NOT NULL DEFAULT '', delta BIGINT NOT NULL, occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// queryTracer 记录每一次 SQL（按开始顺序）。
type queryTracer struct {
	mu      sync.Mutex
	queries []string
}

func (q *queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	q.mu.Lock()
	q.queries = append(q.queries, data.SQL)
	q.mu.Unlock()
	return ctx
}

func (q *queryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (q *queryTracer) reset() {
	q.mu.Lock()
	q.queries = nil
	q.mu.Unlock()
}

func (q *queryTracer) snapshot() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.queries))
	copy(out, q.queries)
	return out
}

// perfFixture 是一套隔离 schema + 带 tracer 的连接池。
type perfFixture struct {
	ctx    context.Context
	pool   *pgxpool.Pool
	tracer *queryTracer
}

func newPerfFixture(t *testing.T) *perfFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("trust_perf_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	tracer := &queryTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		admin.Close()
		t.Fatalf("apply fixture DDL: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return &perfFixture{ctx: ctx, pool: pool, tracer: tracer}
}

// seedUser 造一个 active 用户并返回 id。
func (f *perfFixture) seedUser(t *testing.T, username string) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO users (username, email_verified_at, created_at) VALUES ($1, now(), now() - interval '400 days') RETURNING id`,
		username).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// resolverFor 用给定策略装配 resolver（求值器走真实条件解析器 + 本 fixture 的连接池）。
func (f *perfFixture) resolverFor(t *testing.T, policy trustlevels.Policy, activity trustlevels.Activity) *trustlevels.Resolver {
	t.Helper()
	return trustlevels.NewResolver(
		stubActivityReader{activity: activity},
		stubPolicySource{policy: policy},
	).WithRuleEvaluators(func(ctx context.Context, userID int64) (trustlevels.RuleEvaluator, error) {
		conditionResolver, err := conditions.NewResolver(f.pool, userID)
		if err != nil {
			return nil, err
		}
		return trustlevels.NewRuleEvaluator(conditionResolver), nil
	})
}

// TestNoRuleSiteIssuesNoConditionQueries 是 §6.1：无规则站点零新增查询。
//
// 「新增」在这里的含义是全链路：判定过程中**一条条件 SQL 都没有**。数值口径（activity
// 读）由 stub 承担、不计入——本测试要证明的正是「规则求值没有悄悄多查一次库」。
func TestNoRuleSiteIssuesNoConditionQueries(t *testing.T) {
	fixture := newPerfFixture(t)
	userID := fixture.seedUser(t, "no-rule")
	resolver := fixture.resolverFor(t, trustlevels.DefaultPolicy(), trustlevels.Activity{DaysVisited: 100})

	fixture.tracer.reset()
	if _, err := resolver.Evaluate(fixture.ctx, userID, identity.UserKindHuman, time.Now().Add(-400*24*time.Hour)); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if queries := fixture.tracer.snapshot(); len(queries) != 0 {
		t.Fatalf("无规则站点产生了 %d 条条件查询（应为 0）：%v", len(queries), queries)
	}
}

// TestPrepareIssuesOneQueryPerConditionType 是 §6.2：8 档各 32 叶，SQL 次数 = type 数。
//
// 同时钉住 D19-b 的另一半：**8×32 的合成根不会因为「合计超过 32 叶」而被拒**——
// 每档单独合法，合计不设上限（合成根只 Prepare、不 Validate）。
func TestPrepareIssuesOneQueryPerConditionType(t *testing.T) {
	fixture := newPerfFixture(t)
	userID := fixture.seedUser(t, "many-leaves")

	// 4 种条件类型 × 8 叶 = 32 叶/档；8 档 → 合计 256 叶。
	leaves := []string{
		`{"type":"identity.email_verified"}`,
		`{"type":"identity.account_age","cmp":{"op":"gte","value":1}}`,
		`{"type":"identity.role","params":{"roleKey":"moderator"}}`,
		`{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}`,
	}
	children := make([]string, 0, 32)
	for repeat := 0; repeat < 8; repeat++ {
		children = append(children, leaves...)
	}
	tree := ruleDoc(`{"op":"all","children":[` + strings.Join(children, ",") + `]}`)
	tiers := make([]string, 0, 8)
	for index := 0; index < 8; index++ {
		tiers = append(tiers, rulesTierJSON(`{}`, tree))
	}
	policy := mustPolicy(t, tiersJSON(tiers...))
	resolver := fixture.resolverFor(t, policy, trustlevels.Activity{})

	fixture.tracer.reset()
	evaluation, err := resolver.Evaluate(fixture.ctx, userID, identity.UserKindHuman, time.Now().Add(-400*24*time.Hour))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	queries := fixture.tracer.snapshot()
	// 短路：高档成立就停，所以实际只会求值 TL8 一棵树——但 Prepare 已经把**全部 8 档**
	// 的树合过一次，类型数仍是 4。这正是「查询数 = type 数，与档数无关」。
	if len(queries) != len(leaves) {
		t.Fatalf("SQL 次数 = %d（期望 = 条件类型数 %d）。查询：\n%s", len(queries), len(leaves), strings.Join(queries, "\n---\n"))
	}
	if evaluation.Incomplete {
		t.Fatalf("不该 incomplete：%v", evaluation.Errors)
	}
}

// TestNumericFailureSkipsUniqueConditionType 是 §6.3 的 D19-c 探针。
//
// 用**只在该档出现**的 `object.authoring`（会查 categories/topic_tags）做探针：
// 它的数值预筛不过 → 合成根里没有它 → 与对象相关的 SQL 一条都不该出现。
// 「总查询数变少」这种间接口径会被「别的档恰好也用了同 type」掩盖。
func TestNumericFailureSkipsUniqueConditionType(t *testing.T) {
	fixture := newPerfFixture(t)
	userID := fixture.seedUser(t, "probe")

	authoringRule := ruleDoc(`{"op":"all","children":[{"type":"object.authoring","params":{"targetKind":"category","targetSlug":"general","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}`)
	verifiedRule := ruleDoc(`{"op":"all","children":[{"type":"identity.email_verified"}]}`)
	policy := mustPolicy(t, tiersJSON(
		// TL1：数值无门槛，规则只碰 users。
		rulesTierJSON(`{}`, verifiedRule),
		// TL2：数值门槛永不满足（topic_count 999），规则独有 object.authoring。
		rulesTierJSON(`{"topic_count":999}`, authoringRule),
	))
	// activity.topic_count = 0 → TL2 预筛必不过；TL1 无门槛。
	resolver := fixture.resolverFor(t, policy, trustlevels.Activity{})

	fixture.tracer.reset()
	if _, err := resolver.Evaluate(fixture.ctx, userID, identity.UserKindHuman, time.Now().Add(-400*24*time.Hour)); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	queries := fixture.tracer.snapshot()
	for _, query := range queries {
		if strings.Contains(query, "topic_tags") || strings.Contains(query, "FROM categories") {
			t.Fatalf("TL2 数值不过，却查了它独有的对象条件：\n%s", query)
		}
	}
	if len(queries) != 1 {
		t.Fatalf("只该有 TL1 的一条查询（users），得到 %d 条：\n%s", len(queries), strings.Join(queries, "\n---\n"))
	}
}

// TestRuleEvaluationLatency 是 §6.4 的 10k 夹具实测（默认不跑）。
//
// 门槛是**每个已认证请求**的装载期增量，所以这里量的是「一次 Evaluate」的耗时：
//   - baseline：无规则策略（今天的 5 条固定查询口径的规则侧增量 = 0）；
//   - rules：1 档带规则（2 条条件 SQL：users + user_activity 之外再加条件查询）。
//
// 用 `SFORUM_TRUST_PERF=1` 开启，`SFORUM_TRUST_PERF_USERS`（默认 10000）控制夹具规模。
// 之所以默认不跑：它要插入上万行并跑上千次判定，是「结项前跑一次」的动作，不是日常循环。
func TestRuleEvaluationLatency(t *testing.T) {
	if os.Getenv("SFORUM_TRUST_PERF") != "1" {
		t.Skip("set SFORUM_TRUST_PERF=1 to run the 10k latency fixture")
	}
	users := 10000
	if raw := os.Getenv("SFORUM_TRUST_PERF_USERS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("SFORUM_TRUST_PERF_USERS 非法：%q", raw)
		}
		users = parsed
	}
	fixture := newPerfFixture(t)
	// 批量造夹具：users + user_activity 各 N 行（`generate_series` 一次搞定）。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO users (username, email_verified_at, created_at)
		SELECT 'perf_' || i, now(), now() - interval '400 days' FROM generate_series(1, $1) AS i`, users); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count, days_visited)
		SELECT id, (id % 50), (id % 200), (id % 60) FROM users`); err != nil {
		t.Fatalf("seed activity: %v", err)
	}
	var ids []int64
	rows, err := fixture.pool.Query(fixture.ctx, `SELECT id FROM users ORDER BY id LIMIT $1`, min(users, 500))
	if err != nil {
		t.Fatalf("load ids: %v", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	rule := ruleDoc(`{"op":"all","children":[{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}`)
	withRules := mustPolicy(t, tiersJSON(rulesTierJSON(`{"days_visited":1}`, rule), rulesTierJSON(`{"topic_count":5}`, rule)))
	baseline := trustlevels.DefaultPolicy()
	createdAt := time.Now().Add(-400 * 24 * time.Hour)

	// 数值预筛必须**通过**，否则规则根本不会被求值（那测的是「什么都没做」有多快）。
	activity := trustlevels.Activity{DaysVisited: 100, TopicCount: 10, CommentCount: 100}
	measure := func(policy trustlevels.Policy) []time.Duration {
		resolver := fixture.resolverFor(t, policy, activity)
		// 暖连接：第一次调用会把池里没建好的连接补上，那是夹具成本不是判定成本。
		if _, err := resolver.Evaluate(fixture.ctx, ids[0], identity.UserKindHuman, createdAt); err != nil {
			t.Fatalf("warm up: %v", err)
		}
		samples := make([]time.Duration, 0, len(ids)*2)
		for repeat := 0; repeat < 2; repeat++ {
			for _, id := range ids {
				start := time.Now()
				if _, err := resolver.Evaluate(fixture.ctx, id, identity.UserKindHuman, createdAt); err != nil {
					t.Fatalf("evaluate: %v", err)
				}
				samples = append(samples, time.Since(start))
			}
		}
		return samples
	}

	baseSamples := measure(baseline)
	ruleSamples := measure(withRules)
	baseP50, baseP95 := percentiles(baseSamples)
	ruleP50, ruleP95 := percentiles(ruleSamples)
	t.Logf("fixture users=%d samples=%d", users, len(ruleSamples))
	t.Logf("baseline(无规则)  P50=%s P95=%s", baseP50, baseP95)
	t.Logf("with-rules      P50=%s P95=%s", ruleP50, ruleP95)
	t.Logf("增量            P50=%s P95=%s（预算 P95 ≤ 5ms）", ruleP50-baseP50, ruleP95-baseP95)
	if ruleP95-baseP95 > 5*time.Millisecond {
		t.Fatalf("P95 增量 %s 超过预算 5ms：先查缺失索引，不要加缓存绕过", ruleP95-baseP95)
	}
}

func percentiles(samples []time.Duration) (p50, p95 time.Duration) {
	if len(samples) == 0 {
		return 0, 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)*50/100], sorted[min(len(sorted)-1, len(sorted)*95/100)]
}
