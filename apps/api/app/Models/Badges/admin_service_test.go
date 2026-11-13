package badges

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 三个 actor 夹具对应验收矩阵：member 无权限、只读管理员、可写管理员。
func memberActor() identity.Actor {
	return identity.Actor{ID: 1, Permissions: map[string]bool{}}
}

func viewActor() identity.Actor {
	return identity.Actor{ID: 2, Permissions: map[string]bool{PermissionBadgeView: true}}
}

func manageActor() identity.Actor {
	return identity.Actor{ID: 3, Permissions: map[string]bool{PermissionBadgeView: true, PermissionBadgeManage: true}}
}

func superAdminActor() identity.Actor {
	return identity.Actor{ID: 4, RoleKeys: []string{identity.RoleSuperAdmin}, Permissions: map[string]bool{}}
}

// TestAdminCreateBadgeRejectsInvalidRules 逐条覆盖规则校验的拒绝分支。
func TestAdminCreateBadgeRejectsInvalidRules(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	days := MetricDaysVisited

	cases := []struct {
		name  string
		input BadgeInput
	}{
		{"empty slug", BadgeInput{Name: "X", Tier: TierGold, GrantMode: GrantManual}},
		{"empty name", BadgeInput{Slug: "x", Tier: TierGold, GrantMode: GrantManual}},
		// 不存在的档位自 202611120001 起报 ErrBadgeTierInvalid（422 badge_tier_invalid），
		// 不再是 ErrBadgeRuleInvalid：等级合法性由等级表回答，不是规则问题。
		{"bad tier", BadgeInput{Slug: "x", Name: "X", Tier: "platinum", GrantMode: GrantManual}},
		{"bad grant mode", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: "auto"}},
		{"bad icon prefix", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantManual, Icon: "emoji-star"}},
		// 自 202611120002 起 automatic 必须有**规则**；`metric`/`threshold` 是 legacy
		// 只读列，缺它们是合法形状（见 TestAdminCreateAutomaticBadgeNeedsOnlyRule）。
		// 这里守的是另一条：既没规则、legacy 字段也残缺时仍要 422，
		// 而不是让 `badges_rule_check` 抛约束错变成 500。
		{"automatic without rule", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantAutomatic}},
		{"automatic with unknown metric", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantAutomatic, RuleSet: true, RuleRaw: json.RawMessage(validSingleLeafRule), Metric: ptrActivityMetric("nope")}},
		{"automatic zero threshold", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantAutomatic, Metric: &days, Threshold: ptrInt64(0)}},
		{"manual with metric", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantManual, Metric: &days}},
		{"manual with threshold", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantManual, Threshold: ptrInt64(3)}},
		{"manual with rule", BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantManual, RuleSet: true, RuleRaw: json.RawMessage(validSingleLeafRule)}},
	}
	for _, tc := range cases {
		want := ErrBadgeRuleInvalid
		switch tc.name {
		case "bad tier":
			want = ErrBadgeTierInvalid
		case "manual with rule":
			// 状态冲突有自己的码：它那句「请检查指标与阈值的组合」对这里完全指错方向。
			want = ErrBadgeRuleModeConflict
		}
		if _, err := admin.CreateBadge(context.Background(), manageActor(), tc.input); !errors.Is(err, want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, want)
		}
	}
	if len(store.badges) != 5 {
		t.Fatalf("badges = %d, want 5 (非法输入不得写入)", len(store.badges))
	}
}

// TestAdminCreateBadgeNormalizesSlugAndIcon 断言 slug 归一化与默认图标。
func TestAdminCreateBadgeNormalizesSlugAndIcon(t *testing.T) {
	store := newFakeStore()
	admin := NewAdminService(store)

	created, err := admin.CreateBadge(context.Background(), manageActor(), BadgeInput{
		Slug: "  Quality_Answer ", Name: " 优质解答 ", Tier: TierSilver, GrantMode: GrantManual, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Slug != "quality-answer" {
		t.Fatalf("slug = %q, want quality-answer", created.Slug)
	}
	if created.Icon != "i-lucide-award" {
		t.Fatalf("icon = %q, want default i-lucide-award", created.Icon)
	}
}

// TestAdminGrantRejectsAutomaticBadge 断言自动徽章不能手动授予。
func TestAdminGrantRejectsAutomaticBadge(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)

	err := admin.Grant(context.Background(), manageActor(), GrantInput{UserID: 7, BadgeID: 1, GrantedBy: 1})
	if !errors.Is(err, ErrBadgeManualOnly) {
		t.Fatalf("err = %v, want ErrBadgeManualOnly", err)
	}
	if len(store.grants[7]) != 0 {
		t.Fatal("rejected grant must not write a row")
	}
}

// TestAdminRevokeRejectsAutomaticBadge 断言自动徽章不能手动收回
// （规则下一次评估就会重新授予，收回只是短暂假象）。
func TestAdminRevokeRejectsAutomaticBadge(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	store.insertGrant(7, 1, 0, "")

	_, err := admin.Revoke(context.Background(), manageActor(), 7, 1)
	if !errors.Is(err, ErrBadgeManualOnly) {
		t.Fatalf("err = %v, want ErrBadgeManualOnly", err)
	}
	if _, ok := store.grants[7][1]; !ok {
		t.Fatal("rejected revoke must not delete the row")
	}
}

// TestAdminGrantManualUpdatesTopBadge 断言手动授予后重算最高级徽章。
func TestAdminGrantManualUpdatesTopBadge(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)

	if err := admin.Grant(context.Background(), manageActor(), GrantInput{
		UserID: 7, BadgeID: 3, GrantedBy: 1, GrantReason: "优质解答",
	}); err != nil {
		t.Fatal(err)
	}
	if got := store.top[7]; got != 3 {
		t.Fatalf("top badge = %d, want 3", got)
	}
	// 重复授予被拒。
	if err := admin.Grant(context.Background(), manageActor(), GrantInput{UserID: 7, BadgeID: 3, GrantedBy: 1}); !errors.Is(err, ErrBadgeAlreadyGranted) {
		t.Fatalf("second grant err = %v, want ErrBadgeAlreadyGranted", err)
	}
}

// TestAdminRevokeManualClearsTopBadge 断言收回后最高级徽章落空。
func TestAdminRevokeManualClearsTopBadge(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	if err := admin.Grant(context.Background(), manageActor(), GrantInput{UserID: 7, BadgeID: 3, GrantedBy: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Revoke(context.Background(), manageActor(), 7, 3); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.top[7]; ok {
		t.Fatal("top badge must be cleared after revoking the only badge")
	}
}

// TestAdminDisableBadgeRecomputesAffectedUsers 断言停用徽章会重算持有者的 top_badge_id。
//
// 这是 ON DELETE SET NULL 覆盖不到的场景：行还在，只是不再展示。
func TestAdminDisableBadgeRecomputesAffectedUsers(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	store.insertGrant(7, 3, 0, "")
	if err := store.RecomputeTopBadge(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if store.top[7] != 3 {
		t.Fatalf("precondition: top badge = %d, want 3", store.top[7])
	}

	disabled := BadgeInput{
		Slug: "quality-answer", Name: "优质解答", Tier: TierSilver,
		Icon: "i-lucide-circle-check-big", GrantMode: GrantManual, Enabled: false, Position: 1,
	}
	if _, err := admin.UpdateBadge(context.Background(), manageActor(), 3, disabled); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.top[7]; ok {
		t.Fatal("disabling a badge must clear it from affected users' top badge")
	}
}

// TestAdminRecomputeAllCountsChanges 断言全量重算只统计真正变化的行。
func TestAdminRecomputeAllCountsChanges(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	store.insertGrant(7, 3, 0, "")
	store.insertGrant(8, 1, 0, "")
	store.top[7] = 3 // 7 已正确
	// 8 缺 top，重算后应补上 → 1 行变化。

	result, err := admin.Recompute(context.Background(), manageActor())
	if err != nil {
		t.Fatal(err)
	}
	if result.Refreshed != 1 {
		t.Fatalf("refreshed = %d, want 1", result.Refreshed)
	}
	if store.top[8] != 1 {
		t.Fatalf("user 8 top badge = %d, want 1", store.top[8])
	}
}

// TestAdminServiceEnforcesPermissions 是这一刀的安全回归探测器。
//
// 背景（2026-11-10 P0）：13 条后台徽章路由一度没有任何权限校验，
// 纯 member 能 GET 200、只有 badge.view 的账号能 POST 201 建出真实徽章。
// 根因是「catalog 里声明了守卫」被误当成「守卫已执行」。
//
// 因此这里不测「返回码好看」，而是逐条证明：越权在**领域边界**被拒绝，
// 且拒绝原因是 ErrPermissionDenied（不是 badges_disabled）。
func TestAdminServiceEnforcesPermissions(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	// 1. 纯 member：读和写都必须拒绝。
	if _, err := admin.ListBadges(ctx, memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member ListBadges err = %v, want ErrPermissionDenied", err)
	}
	if _, err := admin.ListGroups(ctx, memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member ListGroups err = %v, want ErrPermissionDenied", err)
	}
	if _, err := admin.ListGrants(ctx, memberActor(), GrantListInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member ListGrants err = %v, want ErrPermissionDenied", err)
	}
	if _, err := admin.CreateBadge(ctx, memberActor(), BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantManual}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member CreateBadge err = %v, want ErrPermissionDenied", err)
	}
	if err := admin.DeleteBadge(ctx, memberActor(), 1); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member DeleteBadge err = %v, want ErrPermissionDenied", err)
	}
	if _, err := admin.Recompute(ctx, memberActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member Recompute err = %v, want ErrPermissionDenied", err)
	}
	// 拒绝必须发生在写入之前：徽章数不变。
	if len(store.badges) != 5 {
		t.Fatalf("badges = %d, want 5 (越权请求不得落库)", len(store.badges))
	}

	// 2. 只读管理员：读通过，写全部拒绝。
	if _, err := admin.ListBadges(ctx, viewActor()); err != nil {
		t.Fatalf("viewer ListBadges err = %v, want nil", err)
	}
	if _, err := admin.CreateBadge(ctx, viewActor(), BadgeInput{Slug: "x", Name: "X", Tier: TierGold, GrantMode: GrantManual}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer CreateBadge err = %v, want ErrPermissionDenied", err)
	}
	if err := admin.DeleteBadge(ctx, viewActor(), 1); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer DeleteBadge err = %v, want ErrPermissionDenied", err)
	}
	if err := admin.Grant(ctx, viewActor(), GrantInput{UserID: 7, BadgeID: 3}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer Grant err = %v, want ErrPermissionDenied", err)
	}
	if _, err := admin.Revoke(ctx, viewActor(), 7, 3); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer Revoke err = %v, want ErrPermissionDenied", err)
	}
	if _, err := admin.Recompute(ctx, viewActor()); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("viewer Recompute err = %v, want ErrPermissionDenied", err)
	}

	// 3. super_admin：角色旁路，读写都通过。
	if _, err := admin.ListBadges(ctx, superAdminActor()); err != nil {
		t.Fatalf("super_admin ListBadges err = %v, want nil", err)
	}
	if _, err := admin.CreateBadge(ctx, superAdminActor(), BadgeInput{Slug: "super-ok", Name: "OK", Tier: TierGold, GrantMode: GrantManual, Enabled: true}); err != nil {
		t.Fatalf("super_admin CreateBadge err = %v, want nil", err)
	}

	// 4. 匿名（ID=0）即使权限集合异常也不放行。
	if _, err := admin.ListBadges(ctx, identity.Actor{ID: 0, Permissions: map[string]bool{PermissionBadgeView: true}}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("anonymous err = %v, want ErrPermissionDenied", err)
	}
}

// 自动徽章的合法规则夹具：单 activity.count 叶子 + window.all + gte。
const validSingleLeafRule = `{"v":1,"root":{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":3}}}`

func ptrActivityMetric(value string) *ActivityMetric {
	metric := ActivityMetric(value)
	return &metric
}

// TestAdminCreateAutomaticBadgeNeedsOnlyRule 断言「纯 rule 徽章」建得出来。
//
// M1 之后 `rule` 是唯一判定来源，`metric`/`threshold` 是 legacy 只读列（可为 NULL）。
// 旧校验要求 automatic 必须带这两个字段，导致后台建不出任何只给规则的新徽章——
// 这条用例就是那个回归的守门人。
func TestAdminCreateAutomaticBadgeNeedsOnlyRule(t *testing.T) {
	store := newFakeStore()
	admin := NewAdminService(store)

	created, err := admin.CreateBadge(context.Background(), manageActor(), BadgeInput{
		Slug: "rule-only", Name: "纯规则", Tier: TierGold, GrantMode: GrantAutomatic, Enabled: true,
		RuleSet: true, RuleRaw: json.RawMessage(validSingleLeafRule),
	})
	if err != nil {
		t.Fatalf("CreateBadge: %v", err)
	}
	if created.RuleRaw == nil || len(created.RuleRaw) == 0 {
		t.Fatalf("rule must reach the store, got %q", created.RuleRaw)
	}
	if created.Metric != nil || created.Threshold != nil {
		t.Fatalf("legacy metric/threshold must stay nil, got %v / %v", created.Metric, created.Threshold)
	}
}

// TestAdminCreateBadgeKeepsRequirementSummary 断言创建时提交的双语要求逐字落到 store。
//
// 只断言响应回显会被「DTO 里写死空串 / store 里丢字段」同时骗过：
// 所以把同一份 input 传下去后，再从 store 读回来比对。
func TestAdminCreateBadgeKeepsRequirementSummary(t *testing.T) {
	store := newFakeStore()
	admin := NewAdminService(store)

	created, err := admin.CreateBadge(context.Background(), manageActor(), BadgeInput{
		Slug: "manual-summary", Name: "手动条件", Tier: TierBronze, GrantMode: GrantManual, Enabled: true,
		RequirementSummarySet: true,
		RequirementSummaryZh:  "手动条件",
		RequirementSummaryEn:  "manual condition",
	})
	if err != nil {
		t.Fatalf("CreateBadge: %v", err)
	}
	stored, err := store.GetBadge(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetBadge: %v", err)
	}
	if stored.RequirementSummaryZh != "手动条件" || stored.RequirementSummaryEn != "manual condition" {
		t.Fatalf("summary must reach the store verbatim, got %q / %q",
			stored.RequirementSummaryZh, stored.RequirementSummaryEn)
	}
}
