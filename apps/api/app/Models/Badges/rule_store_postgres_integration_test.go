package badges

import (
	"context"
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 环检测三组 + 菱形依赖的反向断言（M1.4-09 / 09e）。
//
// 只验「该拒的拒了」会把「拒得太多」当成通过——所以菱形依赖必须保存成功。
// 判环用「当前路径集合」：用「已访问集合」会把 A→B→D、A→C→D 误判成环。
func TestBadgeRuleCycleDetection(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)

	mk := func(slug, rule string) int64 {
		t.Helper()
		var id int64
		if err := fixture.pool.QueryRow(ctx, `
			INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
			VALUES ($1,$1,'bronze','automatic','topic_count',1,$2::jsonb,TRUE,0) RETURNING id`,
			slug, rule).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	owned := func(slugs ...string) string {
		children := ""
		for index, slug := range slugs {
			if index > 0 {
				children += ","
			}
			children += `{"type":"badge.owned","params":{"slug":"` + slug + `"}}`
		}
		if len(slugs) == 1 {
			return `{"v":1,"root":` + children + `}`
		}
		return `{"v":1,"root":{"op":"all","children":[` + children + `]}}`
	}
	decode := func(raw string) ruleengine.Node {
		t.Helper()
		node, err := ruleengine.Decode([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return node
	}

	// 自引用：A → A。
	selfID := mk("cycle-self", owned("cycle-self"))
	if err := store.ValidateBadgeRule(ctx, selfID, decode(owned("cycle-self"))); !errors.Is(err, ErrBadgeRuleCycle) {
		t.Fatalf("自引用必须判环，得到 %v", err)
	}

	// 两枚互指：A → B → A。
	aID := mk("cycle-a", owned("cycle-b"))
	mk("cycle-b", owned("cycle-a"))
	if err := store.ValidateBadgeRule(ctx, aID, decode(owned("cycle-b"))); !errors.Is(err, ErrBadgeRuleCycle) {
		t.Fatalf("两枚互指必须判环，得到 %v", err)
	}

	// 三枚成环：X → Y → Z → X。
	xID := mk("cycle-x", owned("cycle-y"))
	mk("cycle-y", owned("cycle-z"))
	mk("cycle-z", owned("cycle-x"))
	if err := store.ValidateBadgeRule(ctx, xID, decode(owned("cycle-y"))); !errors.Is(err, ErrBadgeRuleCycle) {
		t.Fatalf("三枚成环必须判环，得到 %v", err)
	}

	// 菱形依赖必须保存成功：R → B1、R → B2，B1 → D、B2 → D。
	mk("diamond-d", `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}}`)
	mk("diamond-b1", owned("diamond-d"))
	mk("diamond-b2", owned("diamond-d"))
	rID := mk("diamond-r", owned("diamond-b1", "diamond-b2"))
	if err := store.ValidateBadgeRule(ctx, rID, decode(owned("diamond-b1", "diamond-b2"))); err != nil {
		t.Fatalf("菱形依赖不是环，必须保存成功，得到 %v", err)
	}

	// 引用不存在：指向一个没有的徽章。
	if err := store.ValidateBadgeRule(ctx, rID, decode(owned("no-such-badge"))); !errors.Is(err, ErrBadgeRuleReferenceInvalid) {
		t.Fatalf("引用不存在必须报 reference_invalid，得到 %v", err)
	}
}

// TestRuleHealthReportsOnlyBrokenReferences 固定健康页形状与「不列零授予」。
//
// 三件事必须同时成立：
//  1. 引用失效按 kind 分类、path 与引擎坐标一致；
//  2. provider 判定接受「已声明但零绑定」（M1.4-16）；
//  3. 规则正确但零授予的徽章**不出现**（M1.4-17）——空 items 是 200 正常结果。
func TestRuleHealthReportsOnlyBrokenReferences(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)

	mk := func(slug, rule string) int64 {
		t.Helper()
		var id int64
		if err := fixture.pool.QueryRow(ctx, `
			INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
			VALUES ($1,$1,'bronze','automatic','topic_count',1,$2::jsonb,TRUE,0) RETURNING id`,
			slug, rule).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 健康但零授予：不该出现在报告里。
	mk("healthy-empty", `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":99}}}`)
	// 引用失效：分类 / 主题 / 角色 / provider / 徽章 slug。
	mk("broken-refs", `{"v":1,"root":{"op":"all","children":[
	  {"type":"object.authoring","params":{"targetKind":"category","targetSlug":"gone","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}},
	  {"type":"object.engagement","params":{"action":"like","topicId":4242}},
	  {"type":"identity.role","params":{"roleKey":"ghost"}},
	  {"type":"identity.oauth_link","params":{"providerId":"sforum.auth-gitlab"}},
	  {"type":"badge.owned","params":{"slug":"nope"}}
	]}}`)
	// 不可解码的脏规则。
	var dirtyID int64
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
		VALUES ('dirty','dirty','bronze','automatic','topic_count',1,'{"v":1,"root":{"type":"activity.count","cmp":{"op":"gte","value":"not-a-number"}}}'::jsonb,TRUE,0)
		RETURNING id`).Scan(&dirtyID); err != nil {
		t.Fatal(err)
	}

	// 能解码但校验不过：空 all 必须报 invalid_rule_shape（ref = 错误码、label 空）。
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, rule, enabled, position)
		VALUES ('bad-shape','bad-shape','bronze','automatic','topic_count',1,'{"v":1,"root":{"op":"all"}}'::jsonb,TRUE,0)`); err != nil {
		t.Fatal(err)
	}

	// 已声明但零绑定的 provider：必须判有效。
	store.SetDeclaredProviders(func(ctx context.Context) ([]string, error) {
		return []string{"sforum.auth-gitlab"}, nil
	})

	report, err := store.RuleHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bySlug := map[string]BadgeRuleHealth{}
	for _, item := range report {
		bySlug[item.Slug] = item
	}
	if _, ok := bySlug["healthy-empty"]; ok {
		t.Fatal("零授予但规则正确的徽章不应出现在健康页（M1.4-17）")
	}
	broken, ok := bySlug["broken-refs"]
	if !ok {
		t.Fatal("引用失效的徽章必须出现")
	}
	kinds := map[string]string{}
	for _, issue := range broken.Issues {
		kinds[issue.Kind] = issue.Path
	}
	for _, want := range []string{IssueMissingCategory, IssueMissingTopic, IssueMissingRole, IssueMissingBadgeSlug} {
		if _, ok := kinds[want]; !ok {
			t.Fatalf("缺少 %s，实际 %v", want, kinds)
		}
	}
	if _, ok := kinds[IssueMissingProvider]; ok {
		t.Fatal("已声明但零绑定的 provider 不应报 missing_provider（M1.4-16）")
	}
	if kinds[IssueMissingTopic] != "root.children[1].params.topicId" {
		t.Fatalf("path 必须与引擎坐标一致，得到 %q", kinds[IssueMissingTopic])
	}
	shape, ok := bySlug["bad-shape"]
	if !ok || len(shape.Issues) != 1 {
		t.Fatalf("空 all 必须报 invalid_rule_shape，得到 %+v", shape)
	}
	if shape.Issues[0].Kind != IssueInvalidRuleShape || shape.Issues[0].Ref != ruleengine.CodeShapeInvalid || shape.Issues[0].Label != "" {
		t.Fatalf("invalid_rule_shape 的形状不符（path/ref/label）：%+v", shape.Issues[0])
	}
	dirty, ok := bySlug["dirty"]
	if !ok || len(dirty.Issues) != 1 || dirty.Issues[0].Kind != IssueUndecodableRule {
		t.Fatalf("不可解码规则必须报 undecodable_rule，得到 %+v", dirty)
	}
}

// TestEstimateRuleImpactIsUpperBound 固定「估算 = 上界」的语义。
//
// AND 取最严叶子、OR 取和——两者对「同时满足」的人数都只是上界，不是精确值。
// 这条测试用两个**完全重叠**的叶子（同一批用户既发主题又评论）证明 OR 的结果是
// 相加（上界），而 AND 取最严（下界近似）。
func TestEstimateRuleImpactIsUpperBound(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)

	// 3 个用户：1 个只发主题、1 个只评论、1 个两者都做。
	var ids []int64
	for _, name := range []string{"impact-a", "impact-b", "impact-c"} {
		ids = append(ids, fixture.user(t, name, "active"))
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_activity (user_id, topic_count, comment_count) VALUES
		  ($1, 5, 0), ($2, 0, 5), ($3, 5, 5)`, ids[0], ids[1], ids[2]); err != nil {
		t.Fatal(err)
	}
	decode := func(raw string) ruleengine.Node {
		t.Helper()
		node, err := ruleengine.Decode([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	topic := `{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}`
	comment := `{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}`

	// 单叶子：精确（只有 1 个叶子，没有合成误差）。
	impact, err := store.EstimateRuleImpact(ctx, decode(`{"v":1,"root":`+topic+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Estimated == nil || *impact.Estimated != 2 || impact.Method != impactMethodSingleLeaf {
		t.Fatalf("单叶子 = 2 人，得到 %+v", impact)
	}

	// OR：求和（上界）——真实并集是 3 人，但两个叶子各 2 人，和为 4，按总人数封顶。
	impact, err = store.EstimateRuleImpact(ctx, decode(`{"v":1,"root":{"op":"any","children":[`+topic+`,`+comment+`]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Method != impactMethodSumOfLeaves {
		t.Fatalf("OR 的 method 应为 %s，得到 %+v", impactMethodSumOfLeaves, impact)
	}
	if impact.Estimated == nil || *impact.Estimated != 3 {
		t.Fatalf("OR 估算应封顶到总人数 3，得到 %+v", impact)
	}

	// AND：取最严叶子（2 人），不是交集（2 人也一样，但 method 必须说明是「最严」）。
	impact, err = store.EstimateRuleImpact(ctx, decode(`{"v":1,"root":{"op":"all","children":[`+topic+`,`+comment+`]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Method != impactMethodStrictestLeaf || impact.Estimated == nil || *impact.Estimated != 2 {
		t.Fatalf("AND 取最严叶子，得到 %+v", impact)
	}

	// 算不出来的条件：明确报「估不了」，不给假数字。
	impact, err = store.EstimateRuleImpact(ctx, decode(`{"v":1,"root":{"type":"identity.oauth_link","params":{"providerId":"x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Method != impactMethodUnsupported || impact.Estimated != nil {
		t.Fatalf("不可估的条件必须 estimated=null（不是 0），得到 %+v", impact)
	}
	if len(impact.UnsupportedConditions) != 1 || impact.UnsupportedConditions[0] != "identity.oauth_link" {
		t.Fatalf("必须列出不可估的条件 key，得到 %+v", impact.UnsupportedConditions)
	}

	// AND 里部分叶子不可估：仍给上界 + partial + 条件清单。
	impact, err = store.EstimateRuleImpact(ctx, decode(`{"v":1,"root":{"op":"all","children":[`+topic+`,{"type":"identity.oauth_link","params":{"providerId":"x"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Method != impactMethodStrictestLeafPartial || !impact.Partial || impact.Estimated == nil || *impact.Estimated != 2 {
		t.Fatalf("AND 部分不可估应给 strictest_leaf_partial + 上界 2，得到 %+v", impact)
	}
	if len(impact.UnsupportedConditions) != 1 {
		t.Fatalf("partial 时必须列不可估条件，得到 %+v", impact.UnsupportedConditions)
	}

	// OR 里部分叶子不可估：整体 null，不拿总人数凑上界。
	impact, err = store.EstimateRuleImpact(ctx, decode(`{"v":1,"root":{"op":"any","children":[`+topic+`,{"type":"identity.oauth_link","params":{"providerId":"x"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if impact.Estimated != nil || impact.Method != impactMethodUnsupported {
		t.Fatalf("OR 含不可估叶子必须 null，得到 %+v", impact)
	}
}

// TestRecordFactIdempotencyAndCounter 固定事实写入的三条语义：
//  1. dedupe_key 非空：重复调用返回 inserted=false，且**不再累加**计数；
//  2. dedupe_key 为空：两次调用就是两行事实（刻意不去重）；
//  3. 事实行与计数在同一事务里，计数等于 delta 之和。
func TestRecordFactIdempotencyAndCounter(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)
	userID := fixture.user(t, "fact-writer", "active")

	first, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "wiki.edit", Delta: 2, DedupeKey: "evt-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Inserted || first.Total != 2 {
		t.Fatalf("首次写入应为 inserted=true total=2，得到 %+v", first)
	}
	// 同一个 dedupe_key 再来一次：幂等命中，不是错误。
	again, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "wiki.edit", Delta: 2, DedupeKey: "evt-1"})
	if err != nil {
		t.Fatalf("重复 dedupe_key 不应报错: %v", err)
	}
	if again.Inserted || again.Total != 2 {
		t.Fatalf("重复 dedupe_key 应为 inserted=false total=2，得到 %+v", again)
	}
	var factRows int64
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM badge_facts WHERE user_id = $1`, userID).Scan(&factRows); err != nil {
		t.Fatal(err)
	}
	if factRows != 1 {
		t.Fatalf("重复 dedupe_key 只应有一行事实，得到 %d", factRows)
	}

	// 空 dedupe_key：不去重，两行事实、计数累加两次。
	if _, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "wiki.edit", Delta: 1}); err != nil {
		t.Fatal(err)
	}
	third, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "wiki.edit", Delta: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !third.Inserted || third.Total != 4 {
		t.Fatalf("空 dedupe_key 应每次累加（total=4），得到 %+v", third)
	}

	// 负 delta 允许（扣减），读侧钳零在解析器里做。
	fourth, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "wiki.edit", Delta: -1, DedupeKey: "evt-2"})
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Total != 3 {
		t.Fatalf("负 delta 应扣减（total=3），得到 %+v", fourth)
	}

	// 入参校验。
	if _, err := store.RecordFact(ctx, FactInput{UserID: userID, FactKey: "wiki.edit", Delta: 0}); !errors.Is(err, ErrBadgeFactInvalid) {
		t.Fatalf("delta=0 应报 ErrBadgeFactInvalid，得到 %v", err)
	}
}

// TestUpdateBadgeRuleTriState 是 DTO 三态的库级断言（A/B/C）。
//
// A：不带 rule 的更新 → `rule::text` **字节级不变**（同一列前后比较；不拿请求体比，
//
//	jsonb 规范化后必然不同）。
//
// B：RuleSet + RuleRaw=nil → 清空（NULL）。
// C：RuleSet + RuleRaw=对象 → 替换。
func TestUpdateBadgeRuleTriState(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)
	badgeID := fixture.badge(t, "tristate", "bronze", 0, "topic_count", 1)

	before, err := store.GetBadge(ctx, badgeID)
	if err != nil {
		t.Fatal(err)
	}
	if before.RuleRaw == nil {
		t.Fatal("夹具应已写入规则")
	}
	readRuleText := func() string {
		t.Helper()
		var text *string
		if err := fixture.pool.QueryRow(ctx, `SELECT rule::text FROM badges WHERE id = $1`, badgeID).Scan(&text); err != nil {
			t.Fatal(err)
		}
		if text == nil {
			return ""
		}
		return *text
	}
	baseline := readRuleText()

	// A：只改名字，rule 三态缺省。
	input := BadgeInput{
		Slug: before.Slug, Name: "改了名字", Description: before.Description,
		GroupID: before.GroupID, Tier: before.Tier, Icon: before.Icon,
		GrantMode: before.GrantMode, Metric: before.Metric, Threshold: before.Threshold,
		Enabled: before.Enabled, Position: before.Position,
	}
	if _, err := store.UpdateBadge(ctx, badgeID, input); err != nil {
		t.Fatal(err)
	}
	if got := readRuleText(); got != baseline {
		t.Fatalf("只改名字不得改动 rule：\nbefore=%s\nafter =%s", baseline, got)
	}

	// B：显式清空（manual 徽章才允许没有规则；这里同时把 grant_mode 改成 manual）。
	input.GrantMode = GrantManual
	input.Metric = nil
	input.Threshold = nil
	input.RuleSet = true
	input.RuleRaw = nil
	if _, err := store.UpdateBadge(ctx, badgeID, input); err != nil {
		t.Fatal(err)
	}
	if got := readRuleText(); got != "" {
		t.Fatalf("RuleSet+nil 应清空规则，得到 %q", got)
	}

	// C：替换成一条新规则。
	replacement := `{"v":1,"root":{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}}`
	input.GrantMode = GrantAutomatic
	// fixture 的 CHECK 还是旧定义（automatic 必须带 metric/threshold），
	// 生产库在 202611120002 之后只认 rule；这里把两列填上以满足夹具。
	input.Metric = metricPtr(MetricCommentCount)
	input.Threshold = ptrInt64(3)
	input.RuleSet = true
	input.RuleRaw = []byte(replacement)
	updated, err := store.UpdateBadge(ctx, badgeID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Rule == nil || updated.Rule.Cmp == nil || updated.Rule.Cmp.Value != 3 {
		t.Fatalf("规则应被替换，得到 %+v", updated.Rule)
	}

	// requirementSummary：整体替换（存在就两个都写）。
	input.RequirementSummarySet = true
	input.RequirementSummaryZh = "中文要求"
	input.RequirementSummaryEn = ""
	updated, err = store.UpdateBadge(ctx, badgeID, input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequirementSummaryZh != "中文要求" || updated.RequirementSummaryEn != "" {
		t.Fatalf("summary 应整体替换，得到 %q/%q", updated.RequirementSummaryZh, updated.RequirementSummaryEn)
	}
}

// TestPublicBadgeRequirementText 是断言 D 的库内一半：公开结构带 requirementText，
// 单条件徽章两语言非空；多条件徽章两语言为空串（不退化成本地结构文本）。
func TestPublicBadgeRequirementText(t *testing.T) {
	single := Badge{
		Rule: &ruleengine.Node{
			Type:   "activity.count",
			Params: map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}},
			Cmp:    &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 10},
		},
	}
	public := single.Public()
	if public.RequirementText.Zh == "" || public.RequirementText.En == "" {
		t.Fatalf("单条件徽章必须有双语 requirementText，得到 %+v", public.RequirementText)
	}

	multi := Badge{
		Rule: &ruleengine.Node{Op: ruleengine.OpAll, Children: []ruleengine.Node{
			{Type: "activity.count", Params: map[string]any{"metric": "topic_count", "window": map[string]any{"kind": "all"}}, Cmp: &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 1}},
			{Type: "activity.count", Params: map[string]any{"metric": "comment_count", "window": map[string]any{"kind": "all"}}, Cmp: &ruleengine.Compare{Op: ruleengine.CompareGTE, Value: 1}},
		}},
	}
	public = multi.Public()
	if public.RequirementText.Zh != "" || public.RequirementText.En != "" {
		t.Fatalf("多条件徽章不应生成 requirementText，得到 %+v", public.RequirementText)
	}
}

// TestUpdateBadgeRuleNullCombinations 是 M1.4-06 的服务层断言：`rule:null` 与
// grantMode 的三种组合。落库前必须给出 ErrBadgeRuleInvalid（HTTP 422），
// 而不是让 badges_rule_check 抛约束错误（那是 500）。
func TestUpdateBadgeRuleNullCombinations(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	ctx := fixture.ctx
	store := NewPostgresStore(fixture.pool)
	badgeID := fixture.badge(t, "null-combo", "bronze", 0, "topic_count", 1)
	admin := NewAdminService(store)

	previous, err := store.GetBadge(ctx, badgeID)
	if err != nil {
		t.Fatal(err)
	}
	base := BadgeInput{
		Slug: previous.Slug, Name: previous.Name, Description: previous.Description,
		GroupID: previous.GroupID, Tier: previous.Tier, Icon: previous.Icon,
		Metric: previous.Metric, Threshold: previous.Threshold,
		Enabled: previous.Enabled, Position: previous.Position,
	}

	// ① 只发 {"rule":null}（仍是 automatic）→ 422。
	solo := base
	solo.GrantMode = GrantAutomatic
	solo.RuleSet = true
	solo.RuleRaw = nil
	if _, err := admin.UpdateBadge(ctx, manageActor(), badgeID, solo); !errors.Is(err, ErrBadgeRuleInvalid) {
		t.Fatalf("automatic + rule:null 必须报 ErrBadgeRuleInvalid，得到 %v", err)
	}

	// ② {"rule":null,"grantMode":"manual"} → 200（转手动时清空是合法操作）。
	toManual := base
	toManual.GrantMode = GrantManual
	toManual.Metric = nil
	toManual.Threshold = nil
	toManual.RuleSet = true
	toManual.RuleRaw = nil
	if _, err := admin.UpdateBadge(ctx, manageActor(), badgeID, toManual); err != nil {
		t.Fatalf("转手动并清空规则必须成功，得到 %v", err)
	}

	// ③ {"rule":null,"grantMode":"automatic"} → 422（显式声明自动却没有规则）。
	toAutomatic := toManual
	toAutomatic.GrantMode = GrantAutomatic
	if _, err := admin.UpdateBadge(ctx, manageActor(), badgeID, toAutomatic); !errors.Is(err, ErrBadgeRuleInvalid) {
		t.Fatalf("automatic + rule:null 必须报 ErrBadgeRuleInvalid，得到 %v", err)
	}
}
