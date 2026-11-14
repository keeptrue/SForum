package badges

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
)

// 本文件验证 `(*PostgresStore).RefCandidates` 的四条硬约束（计划 §5.2.1）：
//   1. 五类恒齐、固定顺序；
//   2. flag 映射与「只标不过滤」；
//   3. provider 未注入 = unavailable、注入 = registry 快照；
//   4. 单类查询失败只让该 kind 变 unavailable，其余照常返回。

func refGroup(t *testing.T, set conditions.RefCandidateSet, kind string) conditions.RefCandidateGroup {
	t.Helper()
	for _, group := range set {
		if group.Kind == kind {
			return group
		}
	}
	t.Fatalf("集合里没有 %s 组：%+v", kind, set)
	return conditions.RefCandidateGroup{}
}

func refCandidateByValue(group conditions.RefCandidateGroup, value string) (conditions.RefCandidate, bool) {
	for _, candidate := range group.Candidates {
		if candidate.Value == value {
			return candidate, true
		}
	}
	return conditions.RefCandidate{}, false
}

func seedRefCandidateFixture(t *testing.T, fixture *badgesPGFixture) {
	t.Helper()
	ctx := fixture.ctx
	statements := []string{
		`INSERT INTO categories (slug, name, visibility) VALUES ('news','News','public'), ('archive','Archive','hidden')`,
		`INSERT INTO tags (slug, name, status) VALUES ('go','Go','active'), ('draft','Draft','pending')`,
		`INSERT INTO roles (key, alias, is_enabled) VALUES ('moderator','版主',TRUE), ('bot','Bot',FALSE)`,
		`INSERT INTO badges (slug, name, tier, grant_mode, enabled) VALUES
			('veteran','Veteran','gold','manual',TRUE),
			('legacy','Legacy','bronze','manual',FALSE)`,
	}
	for _, statement := range statements {
		if _, err := fixture.pool.Exec(ctx, statement); err != nil {
			t.Fatalf("seed ref candidates: %v\n%s", err, statement)
		}
	}
}

// TestPostgresStoreRefCandidatesMapsFlags 覆盖四类的值与 flag 映射。
func TestPostgresStoreRefCandidatesMapsFlags(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	seedRefCandidateFixture(t, fixture)
	store := NewPostgresStore(fixture.pool)

	set, err := store.RefCandidates(fixture.ctx)
	if err != nil {
		t.Fatalf("全部成功时不该返回错误：%v", err)
	}
	if len(set) != 5 {
		t.Fatalf("应恒为 5 项，得到 %d: %+v", len(set), set)
	}
	for index, kind := range conditions.RefKinds() {
		if set[index].Kind != kind {
			t.Fatalf("第 %d 项应为 %s，得到 %s", index, kind, set[index].Kind)
		}
	}

	// 分类：hidden 分类仍进候选，只带 hidden 标记（规则记录的是历史事实）。
	category := refGroup(t, set, conditions.RefKindCategory)
	if category.Unavailable || category.Truncated {
		t.Fatalf("category 不该 unavailable/truncated：%+v", category)
	}
	if got, ok := refCandidateByValue(category, "archive"); !ok || got.Label != "Archive" {
		t.Fatalf("隐藏分类必须仍出现在候选里且带名字，得到 %+v", category.Candidates)
	} else if len(got.Flags) != 1 || got.Flags[0] != conditions.RefFlagHidden {
		t.Fatalf("隐藏分类的 flags 应为 [hidden]，得到 %v", got.Flags)
	}
	if got, _ := refCandidateByValue(category, "news"); got.Flags != nil {
		t.Fatalf("公开分类不该带 flag，得到 %v", got.Flags)
	}

	// 标签：status<>'active' → hidden。
	tag := refGroup(t, set, conditions.RefKindTag)
	if got, ok := refCandidateByValue(tag, "draft"); !ok {
		t.Fatalf("待审标签必须仍出现在候选里：%+v", tag.Candidates)
	} else if len(got.Flags) != 1 || got.Flags[0] != conditions.RefFlagHidden {
		t.Fatalf("待审标签的 flags 应为 [hidden]，得到 %v", got.Flags)
	}
	if got, _ := refCandidateByValue(tag, "go"); got.Flags != nil {
		t.Fatalf("active 标签不该带 flag，得到 %v", got.Flags)
	}

	// 角色：值是 roles.key，展示名是 alias，is_enabled=false → disabled。
	role := refGroup(t, set, conditions.RefKindRole)
	if got, ok := refCandidateByValue(role, "moderator"); !ok || got.Label != "版主" {
		t.Fatalf("角色候选的值必须是 key、展示名必须是 alias，得到 %+v", role.Candidates)
	} else if got.Flags != nil {
		t.Fatalf("启用角色不该带 flag，得到 %v", got.Flags)
	}
	if got, ok := refCandidateByValue(role, "bot"); !ok {
		t.Fatalf("停用角色必须仍出现在候选里：%+v", role.Candidates)
	} else if len(got.Flags) != 1 || got.Flags[0] != conditions.RefFlagDisabled {
		t.Fatalf("停用角色的 flags 应为 [disabled]，得到 %v", got.Flags)
	}

	// 徽章：enabled=false → disabled。自引用过滤不在后端（前端按当前徽章过滤）。
	badge := refGroup(t, set, conditions.RefKindBadge)
	if got, ok := refCandidateByValue(badge, "legacy"); !ok {
		t.Fatalf("停用徽章必须仍出现在候选里：%+v", badge.Candidates)
	} else if len(got.Flags) != 1 || got.Flags[0] != conditions.RefFlagDisabled {
		t.Fatalf("停用徽章的 flags 应为 [disabled]，得到 %v", got.Flags)
	}
	if got, ok := refCandidateByValue(badge, "veteran"); !ok || got.Flags != nil {
		t.Fatalf("启用徽章不该带 flag，得到 %+v / %v", got, got.Flags)
	}

	// 未注入 provider 来源 = 取不到候选（不是「没有候选」）。
	provider := refGroup(t, set, conditions.RefKindProvider)
	if !provider.Unavailable {
		t.Fatalf("未注入 SetProviderNames 时 provider 应 unavailable：%+v", provider)
	}
	if provider.Candidates == nil {
		t.Fatalf("provider 的 Candidates 必须是空切片而不是 nil")
	}
}

// TestPostgresStoreRefCandidatesProviderNames 覆盖注入后的 provider 候选：
// zh 优先、回落 en、空值再回落 id，且顺序稳定（map 遍历本身随机）。
func TestPostgresStoreRefCandidatesProviderNames(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	store.SetProviderNames(func() map[string][2]string {
		return map[string][2]string{
			"github": {"GitHub", "GitHub"},
			"steam":  {"", "Steam"},
			"blank":  {"", ""},
		}
	})

	set, err := store.RefCandidates(fixture.ctx)
	if err != nil {
		t.Fatalf("不该返回错误：%v", err)
	}
	provider := refGroup(t, set, conditions.RefKindProvider)
	if provider.Unavailable {
		t.Fatalf("注入后 provider 不该 unavailable：%+v", provider)
	}
	if len(provider.Candidates) != 3 {
		t.Fatalf("应有 3 个 provider 候选，得到 %+v", provider.Candidates)
	}
	// 展示名排序（大小写不敏感）：Blank < GitHub < Steam。
	wantValues := []string{"blank", "github", "steam"}
	for index, value := range wantValues {
		if provider.Candidates[index].Value != value {
			t.Fatalf("排序后第 %d 项应为 %q，得到 %+v", index, value, provider.Candidates)
		}
	}
	if got := provider.Candidates[0].Label; got != "blank" {
		t.Fatalf("双语都为空时应回落 id，得到 %q", got)
	}
	if got := provider.Candidates[2].Label; got != "Steam" {
		t.Fatalf("zh 为空时应回落 en，得到 %q", got)
	}
	if provider.Candidates[1].Flags != nil {
		t.Fatalf("provider 候选不该带 flag，得到 %v", provider.Candidates[1].Flags)
	}
}

// TestPostgresStoreRefCandidatesSingleKindFailure 断言「一类挂了不影响其余」。
//
// 构造方式：在 fixture schema 里删掉 tags.status（public.tags 仍有该列，但
// search_path 先命中 fixture schema），于是只有 tags 这条 SQL 报错。判据是
// **值级**的：categories 的候选仍在、tags 变 unavailable、首条错误可见。
func TestPostgresStoreRefCandidatesSingleKindFailure(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	seedRefCandidateFixture(t, fixture)
	store := NewPostgresStore(fixture.pool)

	if _, err := fixture.pool.Exec(fixture.ctx, `ALTER TABLE tags DROP COLUMN status`); err != nil {
		t.Fatalf("drop tags.status: %v", err)
	}

	set, err := store.RefCandidates(fixture.ctx)
	if err == nil {
		t.Fatalf("单类失败必须回传第一条错误（供调用方 warn）")
	}
	tag := refGroup(t, set, conditions.RefKindTag)
	if !tag.Unavailable {
		t.Fatalf("失败类必须标 unavailable：%+v", tag)
	}
	if len(tag.Candidates) != 0 {
		t.Fatalf("失败类不该带候选：%+v", tag.Candidates)
	}
	// 其余类照常。
	category := refGroup(t, set, conditions.RefKindCategory)
	if category.Unavailable {
		t.Fatalf("categories 成功时不该被连带标成 unavailable")
	}
	if _, ok := refCandidateByValue(category, "news"); !ok {
		t.Fatalf("categories 的候选必须照常返回：%+v", category.Candidates)
	}
	badge := refGroup(t, set, conditions.RefKindBadge)
	if badge.Unavailable || len(badge.Candidates) != 2 {
		t.Fatalf("badges 的候选必须照常返回：%+v", badge)
	}
}

// TestPostgresStoreRefCandidatesNilPool 断言没有连接池时五项全 unavailable 且无错误。
// 这是装配早期与单测的合法状态，不是故障。
func TestPostgresStoreRefCandidatesNilPool(t *testing.T) {
	store := NewPostgresStore(nil)
	set, err := store.RefCandidates(context.Background())
	if err != nil {
		t.Fatalf("nil pool 不该返回错误：%v", err)
	}
	if len(set) != 5 {
		t.Fatalf("应恒为 5 项，得到 %d", len(set))
	}
	for _, group := range set {
		if !group.Unavailable {
			t.Fatalf("%s 应 unavailable：%+v", group.Kind, group)
		}
	}
}
