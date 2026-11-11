package badges

import (
	"context"
	"errors"
	"testing"
)

// 公开目录读面的领域测试。
//
// 这里验的是**形状与语义**：包含未获得、省略空组、失败上抛、匿名不碰 user_badges。
// 排序稳定性（B-04）与 500 上限（B-05）证明不了——假实现自己排序、自己截断，
// 等于拿被测逻辑证明它自己，所以那两条在 public_catalog_postgres_integration_test.go。

func flattenCatalog(catalog PublicCatalog) []PublicCatalogEntry {
	entries := make([]PublicCatalogEntry, 0, 8)
	for _, group := range catalog.Groups {
		entries = append(entries, group.Badges...)
	}
	return entries
}

func seedCatalogStore(t *testing.T) (*fakeStore, int64) {
	t.Helper()
	ctx := context.Background()
	store := newFakeStore()
	group, err := store.CreateGroup(ctx, BadgeGroupInput{Key: "starter", Name: "起步", Position: 1})
	if err != nil {
		t.Fatal(err)
	}
	earned, err := store.CreateBadge(ctx, BadgeInput{
		Slug: "first-post", Name: "首帖", GroupID: &group.ID,
		Tier: TierBronze, GrantMode: GrantManual, Enabled: true, Position: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBadge(ctx, BadgeInput{
		Slug: "ten-posts", Name: "十帖", GroupID: &group.ID,
		Tier: TierSilver, GrantMode: GrantManual, Enabled: true, Position: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBadge(ctx, BadgeInput{
		Slug: "hundred-posts", Name: "百帖", GroupID: &group.ID,
		Tier: TierGold, GrantMode: GrantManual, Enabled: true, Position: 3,
	}); err != nil {
		t.Fatal(err)
	}
	// 停用徽章必须完全不出现：它不在目录里，标记 earned 只会让前端找不到条目。
	if _, err := store.CreateBadge(ctx, BadgeInput{
		Slug: "retired-badge", Name: "已下架", Tier: TierGold,
		GrantMode: GrantManual, Enabled: false, Position: 3,
	}); err != nil {
		t.Fatal(err)
	}
	userID := int64(42)
	store.insertGrant(userID, earned.ID, 0, "seed")
	return store, userID
}

// B-02：目录含全部启用徽章（含未获得），停用徽章不出现。
func TestPublicCatalogIncludesUngrantedAndSkipsDisabled(t *testing.T) {
	ctx := context.Background()
	store, userID := seedCatalogStore(t)
	service := NewService(store, newFakeActivity())

	catalog, err := service.PublicCatalog(ctx)
	if err != nil {
		t.Fatalf("PublicCatalog: %v", err)
	}
	entries := flattenCatalog(catalog)
	if len(entries) != 3 {
		t.Fatalf("want 3 badges (1 earned, 2 unearned), got %d: %+v", len(entries), entries)
	}

	earned, err := service.EarnedBadgeSlugs(ctx, userID)
	if err != nil {
		t.Fatalf("EarnedBadgeSlugs: %v", err)
	}
	if len(earned) != 1 {
		t.Fatalf("want exactly 1 earned badge, got %d: %+v", len(earned), earned)
	}
	if _, ok := earned["first-post"]; !ok {
		t.Fatalf("want earned=first-post, got %+v", earned)
	}
	for _, entry := range entries {
		if entry.Slug == "retired-badge" {
			t.Fatal("disabled badge must not appear in the public catalog")
		}
	}
}

// B-06：只输出含至少一个启用徽章的分组；空组省略。
func TestPublicCatalogOmitsEmptyGroups(t *testing.T) {
	ctx := context.Background()
	store, _ := seedCatalogStore(t)
	empty, err := store.CreateGroup(ctx, BadgeGroupInput{Key: "empty", Name: "空组", Position: 2})
	if err != nil {
		t.Fatal(err)
	}
	// 这个组里的徽章全部停用 → 该组必须整体消失。
	if _, err := store.CreateBadge(ctx, BadgeInput{
		Slug: "hidden", Name: "隐藏", GroupID: &empty.ID,
		Tier: TierBronze, GrantMode: GrantManual, Enabled: false, Position: 1,
	}); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, newFakeActivity())

	catalog, err := service.PublicCatalog(ctx)
	if err != nil {
		t.Fatalf("PublicCatalog: %v", err)
	}
	if len(catalog.Groups) != 1 {
		t.Fatalf("want 1 group, got %d: %+v", len(catalog.Groups), catalog.Groups)
	}
	if catalog.Groups[0].Key != "starter" {
		t.Fatalf("want group starter, got %q", catalog.Groups[0].Key)
	}
}

// B-11：读库失败必须上抛，不能当空目录返回（fail-closed）。
func TestPublicCatalogFailsClosedOnStoreError(t *testing.T) {
	store := newFakeStore()
	store.catalogErr = errors.New("db is down")
	service := NewService(store, newFakeActivity())

	catalog, err := service.PublicCatalog(context.Background())
	if err == nil {
		t.Fatalf("want error on store failure, got nil (catalog=%+v)", catalog)
	}
	if len(catalog.Groups) != 0 {
		t.Fatalf("failed read must not return partial groups: %+v", catalog.Groups)
	}
}

// 「earned 不进缓存」的机械判据之一：目录读根本不碰 user_badges。
//
// 若有人把 earned 合并进 PublicCatalog（让目录读变成 actor 相关），这条会红在
// grantReads 上——而那种改动正是共享缓存泄露 A 的 earned 给 B 的起点。
func TestPublicCatalogNeverReadsViewerGrants(t *testing.T) {
	ctx := context.Background()
	store, _ := seedCatalogStore(t)
	service := NewService(store, newFakeActivity())

	if _, err := service.PublicCatalog(ctx); err != nil {
		t.Fatalf("PublicCatalog: %v", err)
	}
	if store.catalogReads != 1 {
		t.Fatalf("want 1 catalog read, got %d", store.catalogReads)
	}
	if store.grantReads != 0 {
		t.Fatalf("actor-independent catalog read must not touch user_badges, got %d grant reads", store.grantReads)
	}

	// 匿名（userID<=0）连一次 user_badges 查询都不该发。
	if _, err := service.EarnedBadgeSlugs(ctx, 0); err != nil {
		t.Fatalf("EarnedBadgeSlugs(0): %v", err)
	}
	if store.grantReads != 0 {
		t.Fatalf("anonymous viewer must not query user_badges, got %d grant reads", store.grantReads)
	}
}

// 目录为空时必须是空切片而不是 nil：nil 会被 DTO 层原样序列化成 null，
// 前端 .map 直接炸（B-10 的领域侧一半）。
func TestPublicCatalogEmptyResultHasNonNilGroups(t *testing.T) {
	service := NewService(newFakeStore(), newFakeActivity())
	catalog, err := service.PublicCatalog(context.Background())
	if err != nil {
		t.Fatalf("PublicCatalog: %v", err)
	}
	if catalog.Groups == nil {
		t.Fatal("want empty groups slice, got nil")
	}
	if len(catalog.Groups) != 0 {
		t.Fatalf("want 0 groups, got %d", len(catalog.Groups))
	}
	if catalog.Truncated {
		t.Fatal("empty catalog must not be truncated")
	}
}
