package main

import (
	"context"
	"strings"
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// fakeTaxonomyStore 只实现分类/标签相关方法；嵌入 nil 接口让其余 Store 方法
// 保持未实现（本测试不会调用到它们）。
type fakeTaxonomyStore struct {
	forum.Store

	groups     []forum.CategoryGroup
	tags       []forum.Tag
	nextID     int64
	categories []forum.CreateCategoryInput
	createdTag []forum.CreateTagInput
}

func (s *fakeTaxonomyStore) next() int64 {
	s.nextID++
	return s.nextID
}

func (s *fakeTaxonomyStore) ListCategoryGroups(context.Context, bool) ([]forum.CategoryGroup, error) {
	return s.groups, nil
}

func (s *fakeTaxonomyStore) ListTags(context.Context, bool) ([]forum.Tag, error) {
	return s.tags, nil
}

func (s *fakeTaxonomyStore) CreateCategoryGroup(_ context.Context, input forum.CreateCategoryGroupInput) (forum.CategoryGroup, error) {
	group := forum.CategoryGroup{
		ID:          s.next(),
		Slug:        input.Slug,
		Name:        input.Name,
		Description: input.Description,
		Visibility:  input.Visibility,
		Position:    input.Position,
	}
	s.groups = append(s.groups, group)
	return group, nil
}

func (s *fakeTaxonomyStore) CreateCategory(_ context.Context, input forum.CreateCategoryInput) (forum.Category, error) {
	s.categories = append(s.categories, input)
	return forum.Category{
		ID:        s.next(),
		GroupID:   input.GroupID,
		Slug:      input.Slug,
		Name:      input.Name,
		Icon:      input.Icon,
		IconColor: input.IconColor,
	}, nil
}

func (s *fakeTaxonomyStore) CreateTag(_ context.Context, input forum.CreateTagInput) (forum.Tag, error) {
	s.createdTag = append(s.createdTag, input)
	return forum.Tag{ID: s.next(), Slug: input.Slug, Name: input.Name, Status: input.Status}, nil
}

func seedTestStaffActor() identity.Actor {
	return identity.Actor{
		ID:       1,
		Status:   identity.UserStatusActive,
		RoleKeys: []string{identity.RoleSuperAdmin},
	}
}

// TestSeedCatalogPassesForumValidation 用真实 forum.Service 校验内置目录：
// slug / icon / 颜色只要不符合领域规则，CreateCategory 或 CreateTag 就会报错。
// 这样目录本身不必在测试里重复维护一份正则。
func TestSeedCatalogPassesForumValidation(t *testing.T) {
	store := &fakeTaxonomyStore{}
	svc := forum.NewService(forum.ServiceConfig{Store: store})
	plan := buildSeedTaxonomyPlan(seedOptions{
		Profile:       seedProfileSmall,
		CategoryCount: len(seedCatalogCategories(1000)),
		TagCount:      len(seedTagCatalog),
	})

	result, err := ensureSeedTaxonomy(context.Background(), svc, seedTestStaffActor(), plan, func(string, ...any) {})
	if err != nil {
		t.Fatalf("ensureSeedTaxonomy rejected catalog: %v", err)
	}
	if result.CategoriesCreated != len(plan.Categories) {
		t.Fatalf("created %d categories, want %d", result.CategoriesCreated, len(plan.Categories))
	}
	if result.TagsCreated != len(plan.Tags) {
		t.Fatalf("created %d tags, want %d", result.TagsCreated, len(plan.Tags))
	}
	if result.GroupsCreated != len(seedCategoryCatalog) {
		t.Fatalf("created %d groups, want %d", result.GroupsCreated, len(seedCategoryCatalog))
	}
	for _, created := range store.categories {
		if created.Visibility != "public" || created.DefaultSort != "latest" {
			t.Fatalf("category %q has unexpected posture: %+v", created.Slug, created)
		}
		if !strings.HasPrefix(created.Icon, "i-") {
			t.Fatalf("category %q icon %q is not an iconify name", created.Slug, created.Icon)
		}
	}
	for _, created := range store.createdTag {
		if created.Status != forum.TagStatusActive {
			t.Fatalf("tag %q should be active, got %q", created.Slug, created.Status)
		}
	}
}

// TestEnsureSeedTaxonomyIsIdempotent 覆盖「重复运行 seed:forum 不报错」这一核心性质：
// 目录项已存在时不再创建，也不会重复计数。
func TestEnsureSeedTaxonomyIsIdempotent(t *testing.T) {
	store := &fakeTaxonomyStore{}
	svc := forum.NewService(forum.ServiceConfig{Store: store})
	plan := buildSeedTaxonomyPlan(seedOptions{Profile: seedProfileSmall, CategoryCount: 3, TagCount: 2})
	staff := seedTestStaffActor()

	first, err := ensureSeedTaxonomy(context.Background(), svc, staff, plan, func(string, ...any) {})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.CategoriesCreated != 3 || first.TagsCreated != 2 {
		t.Fatalf("unexpected first-run counts: %+v", first)
	}

	// 第二次运行前把已建分类挂回各自分组，模拟真实 ListCategoryGroups 的返回。
	byGroup := map[int64][]forum.Category{}
	for i, input := range store.categories {
		byGroup[input.GroupID] = append(byGroup[input.GroupID], forum.Category{
			ID: int64(i + 1), GroupID: input.GroupID, Slug: input.Slug, Name: input.Name,
		})
	}
	for i := range store.groups {
		store.groups[i].Categories = byGroup[store.groups[i].ID]
	}
	for i, input := range store.createdTag {
		store.tags = append(store.tags, forum.Tag{ID: int64(i + 1), Slug: input.Slug, Name: input.Name, Status: input.Status})
	}

	second, err := ensureSeedTaxonomy(context.Background(), svc, staff, plan, func(string, ...any) {})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.GroupsCreated != 0 || second.CategoriesCreated != 0 || second.TagsCreated != 0 {
		t.Fatalf("second run should create nothing, got %+v", second)
	}
}

func TestSeedCatalogSelectionLimits(t *testing.T) {
	if got := seedCatalogCategories(-1); got != nil {
		t.Fatalf("negative limit should return nil, got %d items", len(got))
	}
	if got := len(seedCatalogCategories(2)); got != 2 {
		t.Fatalf("expected 2 categories, got %d", got)
	}
	first := seedCatalogCategories(1)
	if len(first) != 1 || first[0].Spec.Slug != seedAnnouncementCategorySlug {
		t.Fatalf("first catalog category should be %q, got %+v", seedAnnouncementCategorySlug, first)
	}
	if got := len(seedCatalogCategories(1000)); got != 11 {
		t.Fatalf("expected 11 catalog categories, got %d", got)
	}

	if got := seedCatalogTags(0); got != nil {
		t.Fatalf("zero limit should return nil, got %d tags", len(got))
	}
	if got := len(seedCatalogTags(1000)); got != len(seedTagCatalog) {
		t.Fatalf("over-limit should clamp to catalog size %d, got %d", len(seedTagCatalog), got)
	}
	if got := len(seedCatalogTags(5)); got != 5 {
		t.Fatalf("expected 5 tags, got %d", got)
	}
}

func TestResolveSeedTaxonomyMatchesPlan(t *testing.T) {
	opts := seedOptions{Profile: seedProfileSmall, CategoryCount: 4, TagCount: 6}
	taxonomy := resolveSeedTaxonomy(opts)
	plan := buildSeedTaxonomyPlan(opts)

	if len(taxonomy.Categories) != len(plan.Categories)+1 || taxonomy.Categories[0] != defaultSeedCategorySlug {
		t.Fatalf("taxonomy categories should be general + plan: %v", taxonomy.Categories)
	}
	for i, planned := range plan.Categories {
		if taxonomy.Categories[i+1] != planned.Spec.Slug {
			t.Fatalf("category %d mismatch: %q vs %q", i, taxonomy.Categories[i+1], planned.Spec.Slug)
		}
	}
	if len(taxonomy.Tags) != len(plan.Tags) {
		t.Fatalf("tag count mismatch: %d vs %d", len(taxonomy.Tags), len(plan.Tags))
	}

	// 显式 --category-slug：只发到该分类，不再铺开目录分类。
	pinned := resolveSeedTaxonomy(seedOptions{Profile: seedProfileSmall, CategorySlug: "general", CategoryCount: 4})
	if len(pinned.Categories) != 1 || pinned.Categories[0] != "general" {
		t.Fatalf("explicit slug should pin categories, got %v", pinned.Categories)
	}
	if len(pinned.Tags) != 0 {
		t.Fatalf("no tags expected when TagCount=0, got %v", pinned.Tags)
	}
}

func TestApplySmallProfileDefaults(t *testing.T) {
	opts := seedOptions{Profile: seedProfileSmall}
	applySmallProfileDefaults(&opts, false, false, false, false)
	if opts.CategoryCount != defaultSmallCategoryCount || opts.TagCount != defaultSmallTagCount ||
		opts.Pinned != defaultSmallPinned || opts.ViewsMax != defaultSmallViewsMax {
		t.Fatalf("unexpected small defaults: %+v", opts)
	}

	// 显式传 0 表示关闭，不被默认值覆盖。
	zero := seedOptions{Profile: seedProfileSmall}
	applySmallProfileDefaults(&zero, true, true, true, true)
	if zero.CategoryCount != 0 || zero.TagCount != 0 || zero.Pinned != 0 || zero.ViewsMax != 0 {
		t.Fatalf("explicit zero should be preserved: %+v", zero)
	}

	// perf 不套用 small 默认值。
	perf := seedOptions{Profile: seedProfilePerf1m}
	applySmallProfileDefaults(&perf, false, false, false, false)
	if perf.TagCount != 0 || perf.Pinned != 0 || perf.ViewsMax != 0 {
		t.Fatalf("perf profile should not receive small defaults: %+v", perf)
	}
}
