package badges

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// 建徽章写路径的**真库**测试。
//
// fake store 证明不了这几条：INSERT 的列是不是齐、`badges_rule_check` 会不会把
// 「automatic 没规则」顶成约束错（500 而不是 422）、双语要求是不是真的落进了列。
// fake 自己赋值自己回读，缺列也会绿——所以这组用例必须走 PostgresStore。

const createBadgeSingleLeafRule = `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}}`

func newBadgesPGStore(t *testing.T) *PostgresStore {
	t.Helper()
	return NewPostgresStore(newBadgesPGFixture(t).pool)
}

func storedBadgeColumn(t *testing.T, store *PostgresStore, slug, column string) any {
	t.Helper()
	var value any
	if err := store.pool.QueryRow(context.Background(),
		`SELECT `+column+` FROM badges WHERE slug = $1`, slug).Scan(&value); err != nil {
		t.Fatalf("read %s for %s: %v", column, slug, err)
	}
	return value
}

// 手动徽章 + 双语要求：INSERT 必须把 summary 落库，响应也必须原样回显。
func TestCreateBadgePersistsRequirementSummary(t *testing.T) {
	store := newBadgesPGStore(t)

	created, err := store.CreateBadge(context.Background(), BadgeInput{
		Slug: "manual-summary", Name: "手动条件", Tier: TierBronze,
		GrantMode: GrantManual, Enabled: true,
		RequirementSummarySet: true,
		RequirementSummaryZh:  "手动条件",
		RequirementSummaryEn:  "manual condition",
	})
	if err != nil {
		t.Fatalf("CreateBadge: %v", err)
	}
	if created.RequirementSummaryZh != "手动条件" || created.RequirementSummaryEn != "manual condition" {
		t.Fatalf("response must echo submitted summary, got %q / %q",
			created.RequirementSummaryZh, created.RequirementSummaryEn)
	}
	// 回读列值：响应回显可能来自内存入参，落库与否只有列能证明。
	if got := storedBadgeColumn(t, store, "manual-summary", "requirement_summary_zh"); got != "手动条件" {
		t.Fatalf("requirement_summary_zh = %v, want 手动条件", got)
	}
	if got := storedBadgeColumn(t, store, "manual-summary", "requirement_summary_en"); got != "manual condition" {
		t.Fatalf("requirement_summary_en = %v, want manual condition", got)
	}
}

// 自动徽章只给 rule（不给 legacy metric/threshold）：必须建得出来且 rule 非空。
func TestCreateBadgePersistsRuleWithoutLegacyMetric(t *testing.T) {
	store := newBadgesPGStore(t)

	created, err := store.CreateBadge(context.Background(), BadgeInput{
		Slug: "rule-only", Name: "纯规则", Tier: TierGold,
		GrantMode: GrantAutomatic, Enabled: true,
		RuleSet: true, RuleRaw: json.RawMessage(createBadgeSingleLeafRule),
	})
	if err != nil {
		t.Fatalf("CreateBadge: %v", err)
	}
	if len(created.RuleRaw) == 0 {
		t.Fatal("rule must be read back from the rule column")
	}
	var envelope struct {
		V    int             `json:"v"`
		Root json.RawMessage `json:"root"`
	}
	if err := json.Unmarshal(created.RuleRaw, &envelope); err != nil {
		t.Fatalf("stored rule is not valid JSON: %v", err)
	}
	if envelope.V != 1 || len(envelope.Root) == 0 || string(envelope.Root) == "{}" {
		t.Fatalf("rule envelope must keep v=1 and a non-empty root, got %s", created.RuleRaw)
	}
	if created.Metric != nil || created.Threshold != nil {
		t.Fatalf("legacy columns must stay NULL, got %v / %v", created.Metric, created.Threshold)
	}
	if got := storedBadgeColumn(t, store, "rule-only", "metric"); got != nil {
		t.Fatalf("metric column = %v, want NULL", got)
	}
}

// 自动徽章不给规则：领域必须先拦下（422 的来源），不能漏到 CHECK 约束变成 500。
func TestCreateAutomaticBadgeWithoutRuleFailsBeforeInsert(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	admin := NewAdminService(NewPostgresStore(fixture.pool))

	_, err := admin.CreateBadge(context.Background(), manageActor(), BadgeInput{
		Slug: "no-rule", Name: "无规则", Tier: TierGold, GrantMode: GrantAutomatic, Enabled: true,
	})
	if err == nil {
		t.Fatal("automatic badge without rule must be rejected")
	}
	if !errors.Is(err, ErrBadgeRuleInvalid) {
		t.Fatalf("err = %v, want ErrBadgeRuleInvalid", err)
	}
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT COUNT(*) FROM badges WHERE slug = 'no-rule'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected create must not write a row, got %d", count)
	}
}
