package badges

import (
	"context"
	"fmt"
	"testing"
)

// 公开目录的**真库**集成测试。
//
// 这里验的三件事单测证明不了，因为 fake store 是自己排序、自己截断的：
//   1. ORDER BY 的四个关键字（含 badges.id tiebreaker）真的稳定；
//   2. LIMIT limit+1 的截断语义与 truncated 标志；
//   3. WHERE badges.enabled 在 SQL 里，停用徽章不吃 LIMIT 名额。
//
// 造数直接用 SQL（fixture 的 helper 只覆盖了徽章本体），分组位置与同 position
// 冲突是这两条断言的核心，必须能精确控制。

func (f *badgesPGFixture) catalogGroup(t *testing.T, key string, position int) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx,
		`INSERT INTO badge_groups (key, name, description, position) VALUES ($1,$1,'',$2) RETURNING id`,
		key, position).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *badgesPGFixture) catalogBadge(t *testing.T, slug string, groupID *int64, position int, enabled bool) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, enabled, position, group_id)
		VALUES ($1,$1,'bronze','manual',NULL,NULL,$2,$3,$4) RETURNING id`,
		slug, enabled, position, groupID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// B-04：排序稳定。两个分组 position 相同、组内两条徽章 position 也相同。
//
// 只跑一次查询的测试抓不到这条：同 position 行的顺序由 SQL 的 tiebreaker 决定，
// 没有 badges.id 时 planner 给出的顺序在不同执行间可能不同。这里连查 3 次比对
// 逐条相等，并断言同 position 的两行按 id 升序（与插入顺序相反）。
func TestPublicCatalogOrderIsStable(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)

	// 两个分组同为 position=10：组间靠 badge_groups.id NULLS LAST 之后的位置
	// 由 group.id 决定（先建的组在前）。
	firstGroup := fixture.catalogGroup(t, "alpha", 10)
	secondGroup := fixture.catalogGroup(t, "beta", 10)

	// 故意让 slug 顺序与 id 顺序相反：先插 zeta 再插 omega，两者 position 都是 5。
	fixture.catalogBadge(t, "zeta", &firstGroup, 5, true)
	fixture.catalogBadge(t, "omega", &firstGroup, 5, true)
	// 第二组一条，用来证明组间也稳定。
	fixture.catalogBadge(t, "gamma", &secondGroup, 1, true)

	var baseline []string
	for attempt := 0; attempt < 3; attempt++ {
		entries, _, err := store.ListPublicCatalog(fixture.ctx, PublicCatalogLimit)
		if err != nil {
			t.Fatalf("attempt %d: ListPublicCatalog: %v", attempt, err)
		}
		slugs := make([]string, 0, len(entries))
		for _, entry := range entries {
			slugs = append(slugs, entry.Slug)
		}
		if attempt == 0 {
			baseline = slugs
			continue
		}
		if fmt.Sprint(slugs) != fmt.Sprint(baseline) {
			t.Fatalf("order drifted between calls: %v vs %v", baseline, slugs)
		}
	}
	want := []string{"zeta", "omega", "gamma"}
	if fmt.Sprint(baseline) != fmt.Sprint(want) {
		t.Fatalf("want id-ascending order inside a same-position group %v, got %v", want, baseline)
	}
}

// B-05：硬上限 500 + truncated。
//
// 501 条启用徽章 → 500 条 + truncated=true；停用一条到 500 条 → truncated=false。
// 同时证明停用徽章不吃 LIMIT 名额（它们被 WHERE 挡在 LIMIT 之前）。
func TestPublicCatalogTruncatesAt500(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)

	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, enabled, position)
		SELECT 'bulk-' || i, 'bulk-' || i, 'bronze', 'manual', NULL, NULL, TRUE, i
		FROM generate_series(1, 501) AS i`); err != nil {
		t.Fatalf("seed 501 badges: %v", err)
	}
	// 三条停用徽章：它们不该挤掉任何启用徽章的名额。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO badges (slug, name, tier, grant_mode, metric, threshold, enabled, position)
		SELECT 'off-' || i, 'off-' || i, 'bronze', 'manual', NULL, NULL, FALSE, i
		FROM generate_series(1, 3) AS i`); err != nil {
		t.Fatalf("seed disabled badges: %v", err)
	}

	entries, truncated, err := store.ListPublicCatalog(fixture.ctx, PublicCatalogLimit)
	if err != nil {
		t.Fatalf("ListPublicCatalog: %v", err)
	}
	if len(entries) != 500 {
		t.Fatalf("want 500 items, got %d", len(entries))
	}
	if !truncated {
		t.Fatalf("want truncated=true with 501 enabled badges, got false")
	}
	for _, entry := range entries {
		if entry.Slug == "off-1" {
			t.Fatal("disabled badge must not appear in the public catalog")
		}
	}

	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE badges SET enabled = FALSE WHERE slug = 'bulk-501'`); err != nil {
		t.Fatalf("disable one badge: %v", err)
	}
	entries, truncated, err = store.ListPublicCatalog(fixture.ctx, PublicCatalogLimit)
	if err != nil {
		t.Fatalf("ListPublicCatalog after disable: %v", err)
	}
	if len(entries) != 500 {
		t.Fatalf("want 500 items at the boundary, got %d", len(entries))
	}
	if truncated {
		t.Fatalf("want truncated=false with exactly 500 enabled badges, got true")
	}
}

// 分组顺序与空组：SQL 给出的行序决定组序，空组天然不出现。
func TestPublicCatalogGroupsFollowSQLOrder(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)

	late := fixture.catalogGroup(t, "late", 20)
	early := fixture.catalogGroup(t, "early", 5)
	fixture.catalogGroup(t, "empty", 10) // 无徽章 → 必须整体消失
	fixture.catalogBadge(t, "late-badge", &late, 1, true)
	fixture.catalogBadge(t, "early-badge", &early, 1, true)
	fixture.catalogBadge(t, "ungrouped", nil, 1, true)

	entries, _, err := store.ListPublicCatalog(fixture.ctx, PublicCatalogLimit)
	if err != nil {
		t.Fatalf("ListPublicCatalog: %v", err)
	}
	catalog := PublicCatalog{Groups: groupCatalogEntries(entries)}
	keys := make([]string, 0, len(catalog.Groups))
	for _, group := range catalog.Groups {
		keys = append(keys, group.Key)
	}
	want := []string{"early", "late", ""}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("want group order %v (position asc, ungrouped last), got %v", want, keys)
	}
}

// earned 只给该用户、只算启用徽章。
func TestListEarnedBadgeSlugsScopesToUserAndEnabled(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	viewer := fixture.user(t, "viewer", "active")
	other := fixture.user(t, "other", "active")
	kept := fixture.catalogBadge(t, "kept", nil, 1, true)
	off := fixture.catalogBadge(t, "off", nil, 2, false)
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_badges (user_id, badge_id) VALUES ($1,$2),($1,$3),($4,$2)`,
		viewer, kept, off, other); err != nil {
		t.Fatalf("seed grants: %v", err)
	}

	earned, err := store.ListEarnedBadgeSlugs(ctx, viewer)
	if err != nil {
		t.Fatalf("ListEarnedBadgeSlugs: %v", err)
	}
	if len(earned) != 1 {
		t.Fatalf("want exactly 1 earned slug (disabled excluded), got %v", earned)
	}
	if _, ok := earned["kept"]; !ok {
		t.Fatalf("want kept in earned set, got %v", earned)
	}
	// 匿名不查库：空集合且不报错。
	anonymous, err := store.ListEarnedBadgeSlugs(ctx, 0)
	if err != nil || len(anonymous) != 0 {
		t.Fatalf("anonymous earned set = %v, %v; want empty, nil", anonymous, err)
	}
}

// B-02 的真库侧：目录含**未获得**的启用徽章，停用徽章不出现。
//
// 与领域测试重复是刻意的：那边用 fake store，证明不了 SQL 没把 user_badges
// 内连接进来。若有人把查询改成只返回已获得，501 条造数那条也会红（0 条），
// 但红在计数上；这条红在语义上（want 3 badges, got 1）。
func TestPublicCatalogIncludesUngrantedOnPostgres(t *testing.T) {
	fixture := newBadgesPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	ctx := context.Background()

	viewer := fixture.user(t, "viewer", "active")
	granted := fixture.catalogBadge(t, "granted", nil, 1, true)
	fixture.catalogBadge(t, "ungranted-a", nil, 2, true)
	fixture.catalogBadge(t, "ungranted-b", nil, 3, true)
	fixture.catalogBadge(t, "disabled", nil, 4, false)
	if _, err := fixture.pool.Exec(ctx,
		`INSERT INTO user_badges (user_id, badge_id) VALUES ($1,$2)`, viewer, granted); err != nil {
		t.Fatalf("seed grant: %v", err)
	}

	entries, _, err := store.ListPublicCatalog(ctx, PublicCatalogLimit)
	if err != nil {
		t.Fatalf("ListPublicCatalog: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 badges (1 earned, 2 unearned), got %d", len(entries))
	}
	earned, err := store.ListEarnedBadgeSlugs(ctx, viewer)
	if err != nil {
		t.Fatalf("ListEarnedBadgeSlugs: %v", err)
	}
	if len(earned) != 1 {
		t.Fatalf("want exactly 1 earned badge, got %v", earned)
	}
	for _, entry := range entries {
		if entry.Slug == "disabled" {
			t.Fatal("disabled badge must not appear in the public catalog")
		}
	}
}
