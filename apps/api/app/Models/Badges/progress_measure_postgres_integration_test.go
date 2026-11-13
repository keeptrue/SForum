package badges

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/app/Models/Badges/conditions"
	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 规则进度的真库集成测试。
//
// 单测能证明「Service 把 Measure 交给测量器」，但证明不了两件事：
//  1. 测量真的读规则条件对应的表（而不是 legacy metric/threshold 列）；
//  2. 多枚徽章共享**一次** Prepare（每类条件一条 SQL），不是按徽章 N+1。
//
// 第 2 条用 pgx 的 QueryTracer 直接数 SQL：删掉 Prepare、改成逐徽章 Resolve
// 时，activity.count 的查询次数会从 1 变成 2，测试立刻红。

const (
	ruleActivityTopicGTE3 = `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}}`
	ruleActivityTopicLTE5 = `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"lte","value":5}}}`
	ruleActivityAllGroup  = `{"v":1,"root":{"op":"all","children":[
		{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}},
		{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}
	]}}`
	ruleEmailVerified = `{"v":1,"root":{"type":"identity.email_verified"}}`
)

// insertRuleOnlyBadge 插入一枚「只有 rule，legacy 两列为 NULL」的自动徽章。
//
// 这正是 202611120002 之后新规则徽章的真实形态：读 metric/threshold 只会得到 NULL。
func insertRuleOnlyBadge(t *testing.T, fixture *badgesPGFixture, slug, rule string, position int) int64 {
	t.Helper()
	var id int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
		VALUES ($1,$1,'bronze','automatic',NULL,NULL,$2::jsonb,TRUE,$3) RETURNING id`,
		slug, rule, position).Scan(&id); err != nil {
		t.Fatalf("insert rule-only badge %s: %v", slug, err)
	}
	return id
}

func flattenUserBadges(sections []BadgeGroupSection) map[string]UserBadge {
	items := make(map[string]UserBadge)
	for _, section := range sections {
		for _, item := range section.Badges {
			items[item.Badge.Slug] = item
		}
	}
	return items
}

// 规则徽章（legacy 两列为 NULL）必须有值级进度；不可测形状必须没有。
func TestMeasureProgressReadsRuleOnlyBadges(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := context.Background()
	userID := fixture.user(t, "erin", "active")
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_activity (user_id, topic_count, comment_count) VALUES ($1, 2, 1)`, userID); err != nil {
		t.Fatal(err)
	}
	topicBadgeID := insertRuleOnlyBadge(t, fixture, "ten-topics", ruleActivityTopicGTE3, 1)
	insertRuleOnlyBadge(t, fixture, "lte-badge", ruleActivityTopicLTE5, 2)
	insertRuleOnlyBadge(t, fixture, "all-badge", ruleActivityAllGroup, 3)
	insertRuleOnlyBadge(t, fixture, "email-badge", ruleEmailVerified, 4)

	store := NewPostgresStore(fixture.pool)
	service := NewService(store, nil)
	service.SetProgressMeasurer(store)

	sections, err := service.ListUserBadges(ctx, userID)
	if err != nil {
		t.Fatalf("ListUserBadges: %v", err)
	}
	items := flattenUserBadges(sections)
	topic := items["ten-topics"]
	if topic.Progress.Current != 2 || topic.Progress.Threshold != 3 {
		t.Fatalf("ten-topics progress = %+v, want 2/3 (legacy 两列为 NULL 也必须可测)", topic.Progress)
	}
	for _, slug := range []string{"lte-badge", "all-badge", "email-badge"} {
		if got := items[slug].Progress; got != (BadgeProgress{}) {
			t.Fatalf("%s progress = %+v, want zero (不可测形状不给进度)", slug, got)
		}
	}

	// 目录进度只含「未获得 + 可测」的那一枚。
	progress := service.ViewerCatalogProgress(ctx, userID)
	if len(progress) != 1 {
		t.Fatalf("catalog progress = %+v, want only ten-topics", progress)
	}
	if got := progress["ten-topics"]; got.Current != 2 || got.Threshold != 3 {
		t.Fatalf("catalog ten-topics = %+v, want 2/3", got)
	}

	// 已获得的行不再有目录进度（DTO 也不输出这两个键）。
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_badges (user_id, badge_id) VALUES ($1,$2)`, userID, topicBadgeID); err != nil {
		t.Fatal(err)
	}
	progress = service.ViewerCatalogProgress(ctx, userID)
	if _, present := progress["ten-topics"]; present {
		t.Fatalf("earned badge must not carry catalog progress: %+v", progress)
	}
}

// activityQueryTracer 只数「读 user_activity」的 SQL。
type activityQueryTracer struct {
	mu    sync.Mutex
	count int
}

func (t *activityQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "user_activity") {
		t.mu.Lock()
		t.count++
		t.mu.Unlock()
	}
	return ctx
}

func (t *activityQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *activityQueryTracer) queries() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// 两枚同类型徽章必须共享一次 Prepare：activity.count 只查一次 user_activity。
//
// 改成「按徽章逐条 Resolve」时这里是 2，测试即红——那正是目录页最贵的 N+1。
func TestMeasureProgressPreparesOncePerConditionType(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := context.Background()
	userID := fixture.user(t, "frank", "active")
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_activity (user_id, topic_count, comment_count) VALUES ($1, 1, 2)`, userID); err != nil {
		t.Fatal(err)
	}

	config := fixture.pool.Config()
	tracer := &activityQueryTracer{}
	config.ConnConfig.Tracer = tracer
	tracedPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)

	leaf := func(metric string) ProgressMeasure {
		return ProgressMeasure{
			Leaf: ruleengine.Leaf{
				Type: conditions.KeyActivityCount,
				Params: map[string]any{
					"metric": metric,
					"window": map[string]any{"kind": ruleengine.WindowKindAll},
				},
			},
			Threshold: 5,
		}
	}
	progress := NewPostgresStore(tracedPool).MeasureProgress(ctx, userID, map[string]ProgressMeasure{
		"topics":   leaf("topic_count"),
		"comments": leaf("comment_count"),
	})

	if got := progress["topics"]; got.Current != 1 || got.Threshold != 5 {
		t.Fatalf("topics progress = %+v, want 1/5", got)
	}
	if got := progress["comments"]; got.Current != 2 || got.Threshold != 5 {
		t.Fatalf("comments progress = %+v, want 2/5", got)
	}
	if queries := tracer.queries(); queries != 1 {
		t.Fatalf("activity.count queries = %d, want exactly 1 (同类型徽章必须共享一次 Prepare)", queries)
	}
}

// 单类型失败只让该类徽章没有进度：`fact.count` 引用未声明的事实时，Prepare
// 只把该类型记成 groupErr，activity.count 的徽章仍必须测到值。
//
// 这是「fail-soft，绝不上抛 500」的直接判据：签名没有 error 可抛，而同一批里
// 健康类型的结果不能被坏类型拖掉。
func TestMeasureProgressFailsSoftPerConditionType(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := context.Background()
	userID := fixture.user(t, "grace", "active")
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_activity (user_id, topic_count) VALUES ($1, 2)`, userID); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(fixture.pool) // 不注入 factKeys：事实条件必然无法判定
	progress := store.MeasureProgress(ctx, userID, map[string]ProgressMeasure{
		"topics": {
			Leaf: ruleengine.Leaf{
				Type: conditions.KeyActivityCount,
				Params: map[string]any{
					"metric": "topic_count",
					"window": map[string]any{"kind": ruleengine.WindowKindAll},
				},
			},
			Threshold: 5,
		},
		"plugin-fact": {
			Leaf: ruleengine.Leaf{
				Type: conditions.KeyFactCount,
				Params: map[string]any{
					"factKey": "acme.unknown",
					"window":  map[string]any{"kind": ruleengine.WindowKindAll},
				},
			},
			Threshold: 3,
		},
	})

	if got := progress["topics"]; got.Current != 2 || got.Threshold != 5 {
		t.Fatalf("healthy condition lost its progress: %+v (all=%+v)", got, progress)
	}
	if _, present := progress["plugin-fact"]; present {
		t.Fatalf("undeclared fact must not produce progress: %+v", progress)
	}
}
