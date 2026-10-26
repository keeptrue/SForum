package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 本文件提供 seed:forum 的内置分类/标签目录，以及「保证存在」的追加写入逻辑。
//
// 目录只服务于本地假数据：它不是产品默认值，站点出厂状态仍然只有 general 分类。
// 运营可以在后台重命名、移动或删除这些分类与标签。
//
// 两条硬约束决定了这里的写入方式：
//   - 话题写入前分类/标签必须已存在。forum.tags.creation_mode 默认 controlled，
//     未登记标签会让 CreateTopic 直接失败（见 forum.resolveTopicTags）。
//   - CreateCategory/CreateTag 需要 category.manage / tag.manage，
//     因此必须先解析出一个 super_admin 参与者。

const (
	// defaultSeedCategorySlug 与 forum.default_category_slug 迁移默认值一致。
	defaultSeedCategorySlug = "general"
	// seedChatCategorySlug / seedAnnouncementCategorySlug 在生成时享受特殊权重与文案。
	seedChatCategorySlug         = "chat"
	seedAnnouncementCategorySlug = "announcement"
	// seedTagsPerTopicMax 对齐 forum.tags.max_per_topic 的默认值 5；这里更保守。
	seedTagsPerTopicMax = 3
)

// seedCategorySpec 描述一个内置分类。
type seedCategorySpec struct {
	Slug        string
	Name        string
	Description string
	Icon        string
	IconColor   string
	Position    int
}

// seedCategoryGroupSpec 描述一个内置分类分组。Position 是后台展示顺序，
// 与目录顺序（创建优先级）无关。
type seedCategoryGroupSpec struct {
	Slug        string
	Name        string
	Description string
	Position    int
	Categories  []seedCategorySpec
}

// seedTagSpec 描述一个内置标签。
type seedTagSpec struct {
	Slug        string
	Name        string
	Description string
	Icon        string
	IconColor   string
}

// seedCategoryCatalog 按创建优先级排列：站务类在前，保证默认规模下存在公告分类。
// 展示顺序由各分组的 Position 决定（技术 → 生活 → 站务）。
var seedCategoryCatalog = []seedCategoryGroupSpec{
	{
		Slug:        "site",
		Name:        "站务管理",
		Description: "站点公告、规则与运营反馈。",
		Position:    2,
		Categories: []seedCategorySpec{
			{Slug: "announcement", Name: "站点公告", Description: "官方公告、规则更新与维护通知。", Icon: "i-tabler-flag", IconColor: "#ef4444", Position: 0},
			{Slug: "feedback", Name: "反馈与建议", Description: "功能建议、缺陷反馈与体验讨论。", Icon: "i-tabler-help", IconColor: "#0ea5e9", Position: 1},
		},
	},
	{
		Slug:        "tech",
		Name:        "技术交流",
		Description: "编程语言、工程实践与技术方案讨论。",
		Position:    0,
		Categories: []seedCategorySpec{
			{Slug: "backend", Name: "后端开发", Description: "服务端语言、框架、并发与架构。", Icon: "i-tabler-server", IconColor: "#2563eb", Position: 0},
			{Slug: "frontend", Name: "前端开发", Description: "浏览器、框架、构建与交互实现。", Icon: "i-tabler-device-desktop", IconColor: "#0ea5e9", Position: 1},
			{Slug: "database", Name: "数据库", Description: "建模、索引、查询优化与运维。", Icon: "i-tabler-database", IconColor: "#14b8a6", Position: 2},
			{Slug: "devops", Name: "运维与部署", Description: "容器、CI/CD、监控与线上排障。", Icon: "i-tabler-cloud", IconColor: "#f97316", Position: 3},
			{Slug: "ai", Name: "人工智能", Description: "模型应用、提示工程与工程化落地。", Icon: "i-tabler-cpu", IconColor: "#8b5cf6", Position: 4},
		},
	},
	{
		Slug:        "life",
		Name:        "生活杂谈",
		Description: "工作之外的生活、兴趣与成长。",
		Position:    1,
		Categories: []seedCategorySpec{
			{Slug: seedChatCategorySlug, Name: "闲聊灌水", Description: "随便聊聊，轻松一点。", Icon: "i-tabler-messages", IconColor: "#ec4899", Position: 0},
			{Slug: "reading", Name: "读书与写作", Description: "书单、读书笔记与写作练习。", Icon: "i-tabler-book", IconColor: "#a855f7", Position: 1},
			{Slug: "career", Name: "职场与成长", Description: "求职、晋升与长期能力建设。", Icon: "i-tabler-briefcase", IconColor: "#eab308", Position: 2},
			{Slug: "showcase", Name: "作品分享", Description: "晒一晒自己的项目与作品。", Icon: "i-tabler-sparkles", IconColor: "#22c55e", Position: 3},
		},
	},
}

// seedTagCatalog 按创建优先级排列的前 N 个标签会被 --tags 选中。
var seedTagCatalog = []seedTagSpec{
	{Slug: "golang", Name: "Go", Description: "Go 语言与生态。", Icon: "i-tabler-brand-golang", IconColor: "#00add8"},
	{Slug: "rust", Name: "Rust", Description: "Rust 语言与生态。", Icon: "i-tabler-brand-rust", IconColor: "#dea584"},
	{Slug: "typescript", Name: "TypeScript", Description: "类型系统与工程实践。", Icon: "i-tabler-brand-typescript", IconColor: "#3178c6"},
	{Slug: "vue", Name: "Vue", Description: "Vue 3 与组件设计。", Icon: "i-tabler-brand-vue", IconColor: "#42b883"},
	{Slug: "nuxt", Name: "Nuxt", Description: "Nuxt 全栈与服务端渲染。", Icon: "i-tabler-brand-nuxt", IconColor: "#00dc82"},
	{Slug: "postgresql", Name: "PostgreSQL", Description: "关系型数据库使用与调优。", Icon: "i-tabler-database", IconColor: "#336791"},
	{Slug: "redis", Name: "Redis", Description: "缓存、队列与数据结构。", Icon: "i-tabler-database-cog", IconColor: "#dc382d"},
	{Slug: "docker", Name: "Docker", Description: "镜像、Compose 与本地环境。", Icon: "i-tabler-brand-docker", IconColor: "#2496ed"},
	{Slug: "linux", Name: "Linux", Description: "命令行、发行版与系统管理。", Icon: "i-tabler-terminal-2", IconColor: "#fcc624"},
	{Slug: "performance", Name: "性能优化", Description: "压测、剖析与容量规划。", Icon: "i-tabler-bolt", IconColor: "#f59e0b"},
	{Slug: "open-source", Name: "开源", Description: "开源项目与社区协作。", Icon: "i-tabler-brand-github", IconColor: "#24292f"},
	{Slug: "career", Name: "职场", Description: "求职、面试与职业选择。", Icon: "i-tabler-briefcase", IconColor: "#eab308"},
	{Slug: "interview", Name: "面试", Description: "面试题、复盘与准备方法。", Icon: "i-tabler-checklist", IconColor: "#0ea5e9"},
	{Slug: "tooling", Name: "效率工具", Description: "编辑器、脚本与工作流。", Icon: "i-tabler-tool", IconColor: "#64748b"},
	{Slug: "reading", Name: "阅读", Description: "书单与读书笔记。", Icon: "i-tabler-book", IconColor: "#a855f7"},
	{Slug: "life", Name: "生活记录", Description: "日常、旅行与兴趣。", Icon: "i-tabler-coffee", IconColor: "#d97706"},
	{Slug: "security", Name: "安全", Description: "Web 安全与安全实践。", Icon: "i-tabler-shield-check", IconColor: "#16a34a"},
	{Slug: "deploy", Name: "部署", Description: "上线、回滚与环境管理。", Icon: "i-tabler-rocket", IconColor: "#f97316"},
	{Slug: "debug", Name: "排错", Description: "报错定位与疑难问题。", Icon: "i-tabler-bug", IconColor: "#ef4444"},
	{Slug: "design", Name: "设计", Description: "界面、交互与视觉细节。", Icon: "i-tabler-palette", IconColor: "#8b5cf6"},
	{Slug: "ai-tools", Name: "AI 工具", Description: "模型工具链与使用心得。", Icon: "i-tabler-sparkles", IconColor: "#0ea5e9"},
	{Slug: "frontend-engineering", Name: "前端工程化", Description: "构建、质量与团队协作。", Icon: "i-tabler-layout-grid", IconColor: "#14b8a6"},
	{Slug: "database-design", Name: "数据库设计", Description: "表结构、范式与演进。", Icon: "i-tabler-database-search", IconColor: "#336791"},
	{Slug: "writing", Name: "写作", Description: "技术写作与内容表达。", Icon: "i-tabler-pencil", IconColor: "#6366f1"},
}

// seedPlannedCategory 把分类与其所属分组绑在一起，写入时不必再查目录。
type seedPlannedCategory struct {
	Group seedCategoryGroupSpec
	Spec  seedCategorySpec
}

// seedTaxonomyPlan 是本轮 seed 计划保证存在的分类与标签。
type seedTaxonomyPlan struct {
	Categories []seedPlannedCategory
	Tags       []seedTagSpec
}

func (p seedTaxonomyPlan) empty() bool {
	return len(p.Categories) == 0 && len(p.Tags) == 0
}

// seedTaxonomy 是生成阶段可用的分类/标签 slug 集合（写入前由目录推导，不做数据库查询）。
type seedTaxonomy struct {
	Categories []string
	Tags       []string
}

// buildSeedTaxonomyPlan 依据 flags 推导要创建的分类与标签。
// 显式 --category-slug 表示话题集中在单一分类，此时不再铺开目录分类。
func buildSeedTaxonomyPlan(opts seedOptions) seedTaxonomyPlan {
	plan := seedTaxonomyPlan{}
	if strings.TrimSpace(opts.CategorySlug) == "" && opts.CategoryCount > 0 {
		plan.Categories = seedCatalogCategories(opts.CategoryCount)
	}
	if opts.TagCount > 0 {
		plan.Tags = seedCatalogTags(opts.TagCount)
	}
	return plan
}

// resolveSeedTaxonomy 推导生成阶段使用的 slug 列表。它必须与 buildSeedTaxonomyPlan
// 选择同一批目录项，保证「dry-run 描述」与「真实写入」一致。
func resolveSeedTaxonomy(opts seedOptions) seedTaxonomy {
	out := seedTaxonomy{}
	if slug := strings.TrimSpace(opts.CategorySlug); slug != "" {
		out.Categories = []string{slug}
	} else {
		out.Categories = append(out.Categories, defaultSeedCategorySlug)
		for _, planned := range seedCatalogCategories(opts.CategoryCount) {
			out.Categories = append(out.Categories, planned.Spec.Slug)
		}
	}
	for _, spec := range seedCatalogTags(opts.TagCount) {
		out.Tags = append(out.Tags, spec.Slug)
	}
	return out
}

// seedCatalogCategories 按目录优先级取前 limit 个分类。
func seedCatalogCategories(limit int) []seedPlannedCategory {
	if limit <= 0 {
		return nil
	}
	out := make([]seedPlannedCategory, 0, limit)
	for _, group := range seedCategoryCatalog {
		for _, category := range group.Categories {
			if len(out) == limit {
				return out
			}
			out = append(out, seedPlannedCategory{Group: group, Spec: category})
		}
	}
	return out
}

// seedCatalogTags 按目录优先级取前 limit 个标签。
func seedCatalogTags(limit int) []seedTagSpec {
	if limit <= 0 {
		return nil
	}
	if limit > len(seedTagCatalog) {
		limit = len(seedTagCatalog)
	}
	return seedTagCatalog[:limit]
}

// seedTaxonomyResult 记录本轮新建的分类体系数量（已存在的项不计入）。
type seedTaxonomyResult struct {
	GroupsCreated     int
	CategoriesCreated int
	TagsCreated       int
}

// seedTaxonomyService 是 seed 场景用到的分类/标签读写子集，由 forum.Service 实现。
type seedTaxonomyService interface {
	ListCategoryGroups(ctx context.Context, includeHidden bool) ([]forum.CategoryGroup, error)
	ListTags(ctx context.Context, includePending bool) ([]forum.Tag, error)
	CreateCategoryGroup(ctx context.Context, actor identity.Actor, input forum.CreateCategoryGroupInput) (forum.CategoryGroup, error)
	CreateCategory(ctx context.Context, actor identity.Actor, input forum.CreateCategoryInput) (forum.Category, error)
	CreateTag(ctx context.Context, actor identity.Actor, input forum.CreateTagInput) (forum.Tag, error)
}

// staffActorLoader 解析一个具备分类/标签管理权限的参与者。
type staffActorLoader interface {
	LoadStaffActor(ctx context.Context) (identity.Actor, error)
}

// seedStaffActorLoader 用一条最小 SQL 找出第一个 active 的 super_admin，再走 identity
// store 加载完整 Actor（角色 + 权限），避免在 seed 里重写权限判定。
type seedStaffActorLoader struct {
	pool  *pgxpool.Pool
	store actorLoader
}

func newSeedStaffActorLoader(pool *pgxpool.Pool, store actorLoader) seedStaffActorLoader {
	return seedStaffActorLoader{pool: pool, store: store}
}

func (l seedStaffActorLoader) LoadStaffActor(ctx context.Context) (identity.Actor, error) {
	if l.pool == nil || l.store == nil {
		return identity.Actor{}, errors.New("staff actor loader is not configured")
	}
	var userID int64
	err := l.pool.QueryRow(ctx, `
		SELECT users.id
		FROM users
		JOIN user_roles ON user_roles.user_id = users.id
		JOIN roles ON roles.id = user_roles.role_id
		WHERE roles.key = $1 AND users.status = 'active'
		ORDER BY users.id ASC
		LIMIT 1
	`, identity.RoleSuperAdmin).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.Actor{}, fmt.Errorf(
			"no active %s found to create seed categories/tags; seed some users first or pass --categories=0 --tags=0",
			identity.RoleSuperAdmin,
		)
	}
	if err != nil {
		return identity.Actor{}, fmt.Errorf("query staff actor: %w", err)
	}
	actor, err := l.store.LoadActor(ctx, userID)
	if err != nil {
		return identity.Actor{}, fmt.Errorf("load staff actor %d: %w", userID, err)
	}
	return actor, nil
}

// ensureSeedTaxonomy 追加创建缺失的分组/分类/标签；已存在的 slug 一律跳过，
// 因此命令可以重复执行而不会冲突或重复计数。
func ensureSeedTaxonomy(
	ctx context.Context,
	svc seedTaxonomyService,
	staff identity.Actor,
	plan seedTaxonomyPlan,
	logf func(format string, args ...any),
) (seedTaxonomyResult, error) {
	result := seedTaxonomyResult{}
	if svc == nil || plan.empty() {
		return result, nil
	}

	// 去重需要看到隐藏分组/隐藏分类，否则会重复创建同名 slug。
	groups, err := svc.ListCategoryGroups(ctx, true)
	if err != nil {
		return result, fmt.Errorf("list category groups: %w", err)
	}
	groupIDs := make(map[string]int64, len(groups))
	existingCategories := make(map[string]bool)
	for _, group := range groups {
		groupIDs[group.Slug] = group.ID
		for _, category := range group.Categories {
			existingCategories[category.Slug] = true
		}
	}

	for _, planned := range plan.Categories {
		groupID, ok := groupIDs[planned.Group.Slug]
		if !ok {
			created, err := svc.CreateCategoryGroup(ctx, staff, forum.CreateCategoryGroupInput{
				Slug:        planned.Group.Slug,
				Name:        planned.Group.Name,
				Description: planned.Group.Description,
				Visibility:  "public",
				Position:    planned.Group.Position,
			})
			if err != nil {
				return result, fmt.Errorf("create category group %q: %w", planned.Group.Slug, err)
			}
			groupIDs[created.Slug] = created.ID
			groupID = created.ID
			result.GroupsCreated++
		}
		if existingCategories[planned.Spec.Slug] {
			continue
		}
		if _, err := svc.CreateCategory(ctx, staff, forum.CreateCategoryInput{
			GroupID:     groupID,
			Slug:        planned.Spec.Slug,
			Name:        planned.Spec.Name,
			Description: planned.Spec.Description,
			Icon:        planned.Spec.Icon,
			IconColor:   planned.Spec.IconColor,
			Visibility:  "public",
			Position:    planned.Spec.Position,
			DefaultSort: "latest",
		}); err != nil {
			return result, fmt.Errorf("create category %q: %w", planned.Spec.Slug, err)
		}
		existingCategories[planned.Spec.Slug] = true
		result.CategoriesCreated++
	}

	if len(plan.Tags) > 0 {
		tags, err := svc.ListTags(ctx, true)
		if err != nil {
			return result, fmt.Errorf("list tags: %w", err)
		}
		existingTags := make(map[string]bool, len(tags))
		for _, tag := range tags {
			existingTags[tag.Slug] = true
		}
		for _, spec := range plan.Tags {
			if existingTags[spec.Slug] {
				continue
			}
			if _, err := svc.CreateTag(ctx, staff, forum.CreateTagInput{
				Slug:        spec.Slug,
				Name:        spec.Name,
				Description: spec.Description,
				Icon:        spec.Icon,
				IconColor:   spec.IconColor,
				Status:      forum.TagStatusActive,
			}); err != nil {
				return result, fmt.Errorf("create tag %q: %w", spec.Slug, err)
			}
			existingTags[spec.Slug] = true
			result.TagsCreated++
		}
	}

	logf("taxonomy ready: +%d groups, +%d categories, +%d tags (actor user#%d)\n",
		result.GroupsCreated, result.CategoriesCreated, result.TagsCreated, staff.ID)
	return result, nil
}
