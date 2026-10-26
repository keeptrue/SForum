package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// seedDeps 把 runSeed 依赖的 service/store 收拢在一起，方便命令装配。
// 分类/标签创建需要 super_admin（staffActors），资料与收尾阶段可缺省（nil 即跳过）。
type seedDeps struct {
	identityService identityService
	forumService    forumService
	actors          actorLoader
	taxonomy        seedTaxonomyService
	profiles        seedProfileService
	staffActors     staffActorLoader
	postPass        seedPostPass
}

// identityService 是 identity.Service 在 seed 场景下用到的方法子集。
// 抽成接口是为了将来能在测试里注入假实现，符合现有 controller 的接口隔离风格。
type identityService interface {
	Register(ctx context.Context, input identity.RegisterInput) (identity.CurrentUser, error)
}

// forumService 是 forum.Service 在 seed 场景下用到的方法子集。
type forumService interface {
	ListCategories(ctx context.Context, includeHidden bool) ([]forum.Category, error)
	CreateTopic(ctx context.Context, actor identity.Actor, input forum.CreateTopicInput) (forum.TopicDetail, error)
	CreateComment(ctx context.Context, actor identity.Actor, input forum.CreateCommentInput) (forum.Comment, error)
}

// actorLoader 从 userID 加载带权限的 Actor。对应 identity.Store.LoadActor。
type actorLoader interface {
	LoadActor(ctx context.Context, userID int64) (identity.Actor, error)
}

// seedResult 记录一次 seed 运行的产物统计。
type seedResult struct {
	UsersCreated      int
	ProfilesSeeded    int
	GroupsCreated     int
	CategoriesCreated int
	TagsCreated       int
	TopicsCreated     int
	CommentsCreated   int
	PinnedTopics      int
	Elapsed           time.Duration
}

func (r seedResult) String() string {
	return fmt.Sprintf(
		"%d users, %d profiles, %d groups, %d categories, %d tags, %d topics, %d comments, %d pinned in %s",
		r.UsersCreated, r.ProfilesSeeded, r.GroupsCreated, r.CategoriesCreated, r.TagsCreated,
		r.TopicsCreated, r.CommentsCreated, r.PinnedTopics, r.Elapsed.Round(time.Millisecond),
	)
}

// runSeed 按 dataset 逐条把假数据写入数据库。
//
// 写入路径完全复用领域 Service：
//   - 用户走 identity.Register（自动分配 member 角色、走 argon2 密码哈希）
//   - 分类/标签走 forum.CreateCategoryGroup / CreateCategory / CreateTag（需要 super_admin）
//   - 资料走 profile.UpdateMyProfile
//   - 主题/评论走 forum.CreateTopic / CreateComment（自动渲染 Markdown、生成 slug、
//     维护 path_key/depth、更新各类计数器、做权限检查）
//
// 因此种子数据与真实用户产生的数据在完整性上没有差别。代价是速度：每条记录一个事务，
// 串行执行。这是开发/测试种子数据，简单可靠优先于吞吐。
//
// 顺序有硬约束：先注册用户（空库上第一个用户会成为 super_admin），再创建分类/标签，
// 最后才能写主题——forum.tags.creation_mode 默认 controlled，未登记标签会让话题写入失败。
func runSeed(
	ctx context.Context,
	opts seedOptions,
	dataset seedDataset,
	taxonomyPlan seedTaxonomyPlan,
	deps seedDeps,
	logf func(format string, args ...any),
) (seedResult, error) {
	start := time.Now()
	result := seedResult{}
	// 收尾阶段（资料语料抽取）使用的随机源；主题/评论计划已在 dataset 中固化。
	rng := newSeededRand()

	// 1. 显式 --category-slug 时先做存在性校验，避免写了一半才失败。
	// seed 关注「分类是否存在」，隐藏分类同样可投放，因此包含隐藏项。
	if explicitSlug := strings.TrimSpace(opts.CategorySlug); explicitSlug != "" {
		categories, err := deps.forumService.ListCategories(ctx, true)
		if err != nil {
			return result, fmt.Errorf("list categories: %w", err)
		}
		if !categoryExists(categories, explicitSlug) {
			return result, fmt.Errorf("category %q not found; available: %v", explicitSlug, categorySlugs(categories))
		}
	}

	// 2. 批量注册假用户。冲突时重生成后缀重试。
	userIDs := make([]int64, 0, len(dataset.Users))
	for _, u := range dataset.Users {
		current, err := registerSeedUser(ctx, deps.identityService, u)
		if err != nil {
			return result, fmt.Errorf("register seed user %q: %w", u.Username, err)
		}
		userIDs = append(userIDs, current.ID)
		result.UsersCreated++
		if result.UsersCreated%10 == 0 {
			logf("  registered %d/%d users\n", result.UsersCreated, len(dataset.Users))
		}
	}
	logf("registered %d seed users\n", result.UsersCreated)

	// 3. 创建缺失的分类分组/分类/标签（追加语义，可重复执行）。
	if !taxonomyPlan.empty() {
		staff, err := deps.staffActors.LoadStaffActor(ctx)
		if err != nil {
			return result, err
		}
		taxonomy, err := ensureSeedTaxonomy(ctx, deps.taxonomy, staff, taxonomyPlan, logf)
		if err != nil {
			return result, err
		}
		result.GroupsCreated = taxonomy.GroupsCreated
		result.CategoriesCreated = taxonomy.CategoriesCreated
		result.TagsCreated = taxonomy.TagsCreated
	}

	// 4. 校验目标分类存在（可能刚由第 3 步创建）。空则用 forum 默认 general。
	categorySlug := strings.TrimSpace(opts.CategorySlug)
	if categorySlug == "" {
		categorySlug = defaultSeedCategorySlug
	}
	categories, err := deps.forumService.ListCategories(ctx, true)
	if err != nil {
		return result, fmt.Errorf("list categories: %w", err)
	}
	if !categoryExists(categories, categorySlug) {
		available := categorySlugs(categories)
		return result, fmt.Errorf("category %q not found; available: %v", categorySlug, available)
	}

	// 5. 用户公开资料（简介/签名/所在地/个人站点）。
	profiles, err := applySeedProfiles(ctx, deps.profiles, deps.actors, userIDs, rng, logf)
	if err != nil {
		return result, err
	}
	result.ProfilesSeeded = profiles

	// 6. 逐个主题写入，并在主题下生成评论。
	presentations := make([]seedTopicPresentation, 0, len(dataset.Topics))
	for i, plan := range dataset.Topics {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		authorActor, err := deps.actors.LoadActor(ctx, userIDs[plan.Topic.AuthorIndex])
		if err != nil {
			return result, fmt.Errorf("load actor for topic %d: %w", i+1, err)
		}

		topicCategory := strings.TrimSpace(plan.Topic.CategorySlug)
		if topicCategory == "" {
			topicCategory = categorySlug
		}

		topic, err := deps.forumService.CreateTopic(ctx, authorActor, forum.CreateTopicInput{
			CategorySlug: topicCategory,
			Title:        plan.Topic.Title,
			TagSlugs:     plan.Topic.TagSlugs,
			Content: forum.ContentInput{
				RawContent:   plan.Topic.Body,
				SourceFormat: forum.SourceFormatMarkdown,
				EditorType:   forum.EditorTypeMarkdown,
			},
		})
		if err != nil {
			return result, fmt.Errorf("create topic %d %q: %w", i+1, plan.Topic.Title, err)
		}
		result.TopicsCreated++
		if plan.Topic.Pinned {
			result.PinnedTopics++
		}
		presentations = append(presentations, seedTopicPresentation{
			TopicID: topic.ID,
			Views:   plan.Topic.ViewCount,
			Pinned:  plan.Topic.Pinned,
		})

		// 在当前主题下生成评论。topicCommentIDs 按创建顺序保存已建评论 ID，
		// 用于把 seedComment.ParentOffset 映射成真实评论 ID。
		topicCommentIDs := make([]int64, 0, len(plan.Comments))
		for _, c := range plan.Comments {
			commenterActor, err := deps.actors.LoadActor(ctx, userIDs[c.AuthorIndex])
			if err != nil {
				return result, fmt.Errorf("load actor for comment: %w", err)
			}

			input := forum.CreateCommentInput{
				TopicID: topic.ID,
				Content: forum.ContentInput{
					RawContent:   c.Body,
					SourceFormat: forum.SourceFormatMarkdown,
					EditorType:   forum.EditorTypeMarkdown,
				},
			}
			if c.ParentOffset >= 0 && c.ParentOffset < len(topicCommentIDs) {
				// Service 层会自行加载 parent summary 并维护 path_key/depth/reply_count。
				parentID := topicCommentIDs[c.ParentOffset]
				input.ParentID = &parentID
			}

			created, err := deps.forumService.CreateComment(ctx, commenterActor, input)
			if err != nil {
				return result, fmt.Errorf("create comment on topic %d: %w", topic.ID, err)
			}
			topicCommentIDs = append(topicCommentIDs, created.ID)
			result.CommentsCreated++
		}

		if opts.Batch > 0 && result.TopicsCreated%opts.Batch == 0 {
			logf("  seeded %d/%d topics (%d comments so far)\n", result.TopicsCreated, len(dataset.Topics), result.CommentsCreated)
		}
	}

	// 7. 收尾：回填浏览数/置顶，并按真实行数刷新分类计数。
	if deps.postPass != nil {
		if err := deps.postPass.ApplyTopicPresentation(ctx, presentations); err != nil {
			return result, err
		}
		if err := deps.postPass.RefreshCounters(ctx); err != nil {
			return result, err
		}
	}

	result.Elapsed = time.Since(start)
	return result, nil
}

// registerSeedUser 注册一个种子用户。用户名/邮箱冲突时换后缀重试最多 3 次，
// 以支持重复运行 seed:forum 而不报错（追加生成语义）。
func registerSeedUser(ctx context.Context, svc identityService, base seedUser) (identity.CurrentUser, error) {
	// 首次直接用预生成的随机后缀尝试；冲突再重抽。
	current, err := svc.Register(ctx, identity.RegisterInput{
		Username:    base.Username,
		Email:       base.Email,
		Password:    base.Password,
		DisplayName: base.DisplayName,
	})
	if err == nil {
		return current, nil
	}
	// 只对参数冲突（用户名/邮箱已存在）重试，其它错误直接上抛。
	var invalid *identity.RegisterInvalidError
	if !errors.As(err, &invalid) {
		return identity.CurrentUser{}, err
	}

	for attempt := 0; attempt < 3; attempt++ {
		retry, regenErr := generateSeedUser(0)
		if regenErr != nil {
			return identity.CurrentUser{}, regenErr
		}
		current, err = svc.Register(ctx, identity.RegisterInput{
			Username:    retry.Username,
			Email:       retry.Email,
			Password:    retry.Password,
			DisplayName: base.DisplayName, // 保留原序号显示名
		})
		if err == nil {
			return current, nil
		}
		if !errors.As(err, &invalid) {
			return identity.CurrentUser{}, err
		}
	}
	return identity.CurrentUser{}, fmt.Errorf("seed user registration kept conflicting after retries: %w", err)
}

func categoryExists(categories []forum.Category, slug string) bool {
	for _, c := range categories {
		if c.Slug == slug {
			return true
		}
	}
	return false
}

func categorySlugs(categories []forum.Category) []string {
	out := make([]string, 0, len(categories))
	for _, c := range categories {
		out = append(out, c.Slug)
	}
	return out
}

// newSeededRand 构造一个带随机种子的 *rand.Rand，用于生成可复现的 dataset。
// 使用 math/rand/v2 的 PCG 实现，足够种子场景使用。
func newSeededRand() *rand.Rand {
	return rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(time.Now().UnixNano()<<1)))
}
