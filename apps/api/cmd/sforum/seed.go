package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	"github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	"github.com/keeptrue/sforum/apps/api/config"
)

// small（默认）profile 的规模默认值：显式传 0 表示关闭该类数据。
const (
	defaultSmallCategoryCount = 6
	defaultSmallTagCount      = 12
	defaultSmallPinned        = 3
	defaultSmallViewsMax      = 500
)

// newSeedCommand 构造 `sforum seed:forum` 子命令。
//
// 它复用 config.Load() 解析数据库连接（只读环境变量，不读 .env，与 cmd/migrate 一致），
// 用 postgres.NewPool 建池，再用 identity/forum 的 PostgresStore + Service 写入数据。
//
// profile=small（默认）走领域 Service 逐条写入；profile=perf-1m 走 bulk COPY，
// 面向百万级读路径基线，禁止误写入日常开发库（需 --confirm-perf-db）。
func newSeedCommand() *cobra.Command {
	opts := seedOptions{Profile: seedProfileSmall}
	cmd := &cobra.Command{
		Use:   "seed:forum",
		Short: "批量生成假论坛数据（用户、分类、标签、主题、评论）",
		Long: `批量生成假论坛数据用于本地开发和测试。

profiles:
  small    默认。复用领域 Service 写入：用户 + 公开资料、分类分组/分类、
           标签、主题（带标签、置顶、浏览数）与嵌套评论。适合百～千级主题。
  perf-1m  百万级读路径基线。bulk INSERT/COPY，默认约 1e6 主题、多分类、
           ≥1 帖 5e4 评论。追加生成、不触发 domain events。

small 规模默认值（显式传 0 可关闭单项）:
  --categories  ` + fmt.Sprint(defaultSmallCategoryCount) + `   内置分类目录前 N 个（含分组；公告/反馈优先）
  --tags        ` + fmt.Sprint(defaultSmallTagCount) + `  内置标签目录前 N 个
  --pinned      ` + fmt.Sprint(defaultSmallPinned) + `   置顶主题数（落在公告分类，使用公告文案）
  --views-max   ` + fmt.Sprint(defaultSmallViewsMax) + ` 普通主题浏览数上限（置顶主题取 [max, 2*max)）

磁盘/时间量级（perf-1m 全量）:
  ` + perfDiskOrderOfMagnitude + `
  ` + perfTimeOrderOfMagnitude + `

隔离警告:
  切勿把 perf-1m 全量写入日常共享开发库。请使用专用 DATABASE_URL / Docker volume，
  并显式传入 --confirm-perf-db。

由于 config.Load() 只读环境变量、不读 .env，运行前需先把 .env 导入环境：

  set -a; . ./.env; set +a
  go run ./cmd/sforum seed:forum --count=300 --users=60 --comments-max=8
  go run ./cmd/sforum seed:forum --tags=0 --categories=0 --pinned=0   # 只要用户/主题/评论
  go run ./cmd/sforum seed:forum --profile=perf-1m --dry-run
  go run ./cmd/sforum seed:forum --profile=perf-1m --confirm-perf-db --database-url=...

注意：分类/标签需要 super_admin 参与者；空库首次运行时第一个种子用户会成为
super_admin，因此无需手工建管理员。

或用 --database-url 显式覆盖连接串。

注意：此命令面向开发/测试环境，请勿在生产数据库运行。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 记录哪些规模相关 flag 被显式设置，避免默认值覆盖用户意图。
			opts.countExplicit = cmd.Flags().Changed("count")
			opts.usersExplicit = cmd.Flags().Changed("users")
			opts.commentsMaxExplicit = cmd.Flags().Changed("comments-max")
			opts.categoriesExplicit = cmd.Flags().Changed("categories")
			opts.tagsExplicit = cmd.Flags().Changed("tags")
			opts.pinnedExplicit = cmd.Flags().Changed("pinned")
			opts.viewsExplicit = cmd.Flags().Changed("views-max")
			return runSeedCommand(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().StringVar(&opts.Profile, "profile", seedProfileSmall, "种子规模：small | perf-1m")
	cmd.Flags().IntVar(&opts.Count, "count", 1000, "生成的主题数量（perf-1m 默认 1000000，可覆盖为 proof seed）")
	cmd.Flags().IntVar(&opts.Users, "users", 50, "预先创建的假用户数量（perf-1m 默认 200）")
	cmd.Flags().IntVar(&opts.CommentsMax, "comments-max", 5, "每个普通主题最多评论数（0 表示不生成；perf-1m 默认 0）")
	cmd.Flags().IntVar(&opts.CategoryCount, "categories", 0, "small：创建的内置分类数（默认 6）；perf-1m：分类数（默认 20）")
	cmd.Flags().IntVar(&opts.TagCount, "tags", 0, "small：创建的内置标签数（默认 12；0 表示不建标签）")
	cmd.Flags().IntVar(&opts.Pinned, "pinned", 0, "small：置顶主题数（默认 3；0 表示不置顶）")
	cmd.Flags().IntVar(&opts.ViewsMax, "views-max", 0, "small：普通主题浏览数上限（默认 500；0 表示不写浏览数）")
	cmd.Flags().IntVar(&opts.HotComments, "hot-comments", 0, "perf-1m 热帖评论数（默认 50000）")
	cmd.Flags().StringVar(&opts.HotSlug, "hot-slug", "", "perf-1m 热帖固定 slug（默认 perf-hot-thread）")
	cmd.Flags().StringVar(&opts.CategorySlug, "category-slug", "", "small 模式：主题固定发布到的分类 slug（默认按目录分布）")
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL；空则用环境变量")
	cmd.Flags().IntVar(&opts.Batch, "batch", 20, "进度日志/批大小（small：每 N 条打日志；perf-1m：主题批大小，默认 5000）")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只打印计划，不写数据库")
	cmd.Flags().BoolVar(&opts.ConfirmPerfDB, "confirm-perf-db", false, "确认目标库为专用 perf 库（perf-1m 非 dry-run 必填）")
	return cmd
}

// newSeedPerfCommand 是 seed:forum --profile=perf-1m 的便捷入口（D4 允许 seed:perf 别名）。
func newSeedPerfCommand() *cobra.Command {
	opts := seedOptions{Profile: seedProfilePerf1m}
	cmd := &cobra.Command{
		Use:   "seed:perf",
		Short: "百万级读路径种子（seed:forum --profile=perf-1m 别名）",
		Long: `seed:forum --profile=perf-1m 的别名。

默认约 1e6 主题、多分类、≥1 帖 5e4 评论；bulk 写入；需 --confirm-perf-db。
详见 seed:forum --help。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Profile = seedProfilePerf1m
			opts.countExplicit = cmd.Flags().Changed("count")
			opts.usersExplicit = cmd.Flags().Changed("users")
			opts.commentsMaxExplicit = cmd.Flags().Changed("comments-max")
			return runSeedCommand(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().IntVar(&opts.Count, "count", defaultPerfTopicCount, "主题数量（默认 1000000）")
	cmd.Flags().IntVar(&opts.Users, "users", defaultPerfUsers, "假用户数量")
	cmd.Flags().IntVar(&opts.CommentsMax, "comments-max", defaultPerfRegularComments, "普通主题最多评论数")
	cmd.Flags().IntVar(&opts.CategoryCount, "categories", defaultPerfCategoryCount, "分类数")
	cmd.Flags().IntVar(&opts.HotComments, "hot-comments", defaultPerfHotComments, "热帖评论数")
	cmd.Flags().StringVar(&opts.HotSlug, "hot-slug", defaultPerfHotSlug, "热帖固定 slug")
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL")
	cmd.Flags().IntVar(&opts.Batch, "batch", 5000, "主题批大小")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只打印计划，不写数据库")
	cmd.Flags().BoolVar(&opts.ConfirmPerfDB, "confirm-perf-db", false, "确认目标库为专用 perf 库")
	return cmd
}

func runSeedCommand(ctx context.Context, opts seedOptions, cmd *cobra.Command) error {
	opts.Profile = strings.TrimSpace(opts.Profile)
	if opts.Profile == "" {
		opts.Profile = seedProfileSmall
	}
	applyPerfProfileDefaults(&opts, opts.countExplicit, opts.usersExplicit, opts.commentsMaxExplicit)
	applySmallProfileDefaults(&opts, opts.categoriesExplicit, opts.tagsExplicit, opts.pinnedExplicit, opts.viewsExplicit)

	if err := validateSeedOptions(&opts); err != nil {
		return err
	}

	rng := newSeededRand()

	// perf-1m：纯计划路径（不物化百万正文）。
	if opts.Profile == seedProfilePerf1m {
		plan, err := buildPerfSeedPlan(opts, rng)
		if err != nil {
			return fmt.Errorf("build perf plan: %w", err)
		}
		if opts.DryRun {
			printPerfDryRun(cmd, opts, plan)
			return nil
		}
		if !opts.ConfirmPerfDB {
			return fmt.Errorf("refusing perf-1m write without --confirm-perf-db (use a dedicated DATABASE_URL; see seed:forum --help)")
		}
		return runPerfSeedCommand(ctx, opts, plan, cmd)
	}

	// small：内存 dataset + Service 写入。
	taxonomy := resolveSeedTaxonomy(opts)
	dataset, err := generateSeedDataset(opts, rng, taxonomy)
	if err != nil {
		return fmt.Errorf("generate seed dataset: %w", err)
	}
	if opts.DryRun {
		printDryRun(cmd, opts, dataset, taxonomy)
		return nil
	}

	databaseURL := opts.DatabaseURL
	if databaseURL == "" {
		databaseURL = config.Load().DatabaseURL
	}
	if databaseURL == "" {
		return fmt.Errorf("database url is empty: set DATABASE_URL or pass --database-url")
	}

	pool, err := postgres.NewPool(ctx, databaseURL, 10)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	identityStore := identity.NewPostgresStore(pool)
	forumService := forum.NewService(forum.ServiceConfig{Store: forum.NewPostgresStore(pool)})
	deps := seedDeps{
		identityService: identity.NewService(identityStore),
		forumService:    forumService,
		actors:          identityStore,
		taxonomy:        forumService,
		profiles:        profile.NewService(profile.NewPostgresStore(pool)),
		staffActors:     newSeedStaffActorLoader(pool, identityStore),
		postPass:        newSeedPostPass(pool),
	}

	cmd.Printf("seeding forum: %d topics, %d users, up to %d comments/topic, %d categories, %d tags, %d pinned\n",
		opts.Count, opts.Users, opts.CommentsMax, opts.CategoryCount, opts.TagCount, opts.Pinned)
	result, err := runSeed(ctx, opts, dataset, buildSeedTaxonomyPlan(opts), deps, cmd.Printf)
	if err != nil {
		return err
	}
	cmd.Printf("done: %s\n", result.String())
	return nil
}

func runPerfSeedCommand(ctx context.Context, opts seedOptions, plan perfSeedPlan, cmd *cobra.Command) error {
	databaseURL := opts.DatabaseURL
	if databaseURL == "" {
		databaseURL = config.Load().DatabaseURL
	}
	if databaseURL == "" {
		return fmt.Errorf("database url is empty: set DATABASE_URL or pass --database-url")
	}

	// bulk 需要更大连接与更长语句；池大小略增。
	pool, err := postgres.NewPool(ctx, databaseURL, 16)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	identityStore := identity.NewPostgresStore(pool)
	deps := seedBulkDeps{
		pool:            pool,
		identityService: identity.NewService(identityStore),
		actors:          identityStore,
	}

	cmd.Printf("seeding forum perf-1m: %s\n", plan.String())
	cmd.Printf("  disk note: %s\n", plan.DiskNote)
	cmd.Printf("  time note: %s\n", plan.TimeNote)
	cmd.Printf("  database:  %s\n", redactDatabaseURL(databaseURL))
	result, err := runPerfBulkSeed(ctx, opts, plan, deps, cmd.Printf)
	if err != nil {
		return err
	}
	cmd.Printf("done: %s\n", result.String())
	return nil
}

// applySmallProfileDefaults 在 profile=small 时填入默认规模。
// 只有「用户没显式设置」的 flag 才会填默认值；显式传 0 表示关闭该项。
func applySmallProfileDefaults(opts *seedOptions, categoriesExplicit, tagsExplicit, pinnedExplicit, viewsExplicit bool) {
	if opts == nil || opts.Profile != seedProfileSmall {
		return
	}
	if !categoriesExplicit {
		opts.CategoryCount = defaultSmallCategoryCount
	}
	if !tagsExplicit {
		opts.TagCount = defaultSmallTagCount
	}
	if !pinnedExplicit {
		opts.Pinned = defaultSmallPinned
	}
	if !viewsExplicit {
		opts.ViewsMax = defaultSmallViewsMax
	}
}

// validateSeedOptions 规整并校验 flag，给出可读的边界错误。
func validateSeedOptions(opts *seedOptions) error {
	switch opts.Profile {
	case seedProfileSmall, seedProfilePerf1m:
	default:
		return fmt.Errorf("--profile must be %q or %q", seedProfileSmall, seedProfilePerf1m)
	}
	if opts.Count <= 0 {
		return fmt.Errorf("--count must be positive")
	}
	if opts.Users <= 0 {
		return fmt.Errorf("--users must be positive")
	}
	if opts.CommentsMax < 0 {
		return fmt.Errorf("--comments-max must be >= 0")
	}
	if opts.Batch < 0 {
		return fmt.Errorf("--batch must be >= 0")
	}
	if opts.CategoryCount < 0 {
		return fmt.Errorf("--categories must be >= 0")
	}
	if opts.TagCount < 0 {
		return fmt.Errorf("--tags must be >= 0")
	}
	if opts.Pinned < 0 {
		return fmt.Errorf("--pinned must be >= 0")
	}
	if opts.ViewsMax < 0 {
		return fmt.Errorf("--views-max must be >= 0")
	}
	if opts.Profile == seedProfilePerf1m && opts.HotComments < 0 {
		return fmt.Errorf("--hot-comments must be >= 0")
	}
	return nil
}

// printDryRun 打印 small 模式内存计划的摘要，不连库也不写任何数据。
func printDryRun(cmd *cobra.Command, opts seedOptions, dataset seedDataset, taxonomy seedTaxonomy) {
	totalComments := 0
	pinnedTopics := 0
	taggedTopics := 0
	for _, p := range dataset.Topics {
		totalComments += len(p.Comments)
		if p.Topic.Pinned {
			pinnedTopics++
		}
		if len(p.Topic.TagSlugs) > 0 {
			taggedTopics++
		}
	}
	cmd.Printf("dry-run plan (no database writes)\n")
	cmd.Printf("  profile:       %s\n", opts.Profile)
	cmd.Printf("  users:         %d (+公开资料)\n", len(dataset.Users))
	cmd.Printf("  categories:    %d 个可发布分类（新建目录 %d 个）\n", len(taxonomy.Categories), opts.CategoryCount)
	if len(taxonomy.Categories) > 0 {
		cmd.Printf("    - %s\n", strings.Join(taxonomy.Categories, ", "))
	}
	cmd.Printf("  tags:          %d 个可用标签（新建目录 %d 个）\n", len(taxonomy.Tags), opts.TagCount)
	if len(taxonomy.Tags) > 0 {
		cmd.Printf("    - %s\n", strings.Join(taxonomy.Tags, ", "))
	}
	cmd.Printf("  topics:        %d（%d 带标签）\n", len(dataset.Topics), taggedTopics)
	cmd.Printf("  comments:      %d (max %d/topic)\n", totalComments, opts.CommentsMax)
	cmd.Printf("  pinned:        %d（浏览数 %d~%d）\n", pinnedTopics, opts.ViewsMax, opts.ViewsMax*2)
	if len(dataset.Users) > 0 {
		cmd.Printf("  sample user:   %s <%s>\n", dataset.Users[0].Username, dataset.Users[0].Email)
	}
	if len(dataset.Topics) > 0 {
		t := dataset.Topics[0].Topic
		cmd.Printf("  sample topic:  [user#%d] [%s] %s (%d body chars, tags=%v, %d comments, %d views)\n",
			t.AuthorIndex, t.CategorySlug, t.Title, len(t.Body), t.TagSlugs, len(dataset.Topics[0].Comments), t.ViewCount)
	}
	if len(dataset.Topics) > 1 {
		t := dataset.Topics[len(dataset.Topics)-1].Topic
		cmd.Printf("  sample (tail): [user#%d] [%s] %s (tags=%v, %d views)\n",
			t.AuthorIndex, t.CategorySlug, t.Title, t.TagSlugs, t.ViewCount)
	}
}

// printPerfDryRun 打印 perf-1m 规模计划（不物化正文）。
func printPerfDryRun(cmd *cobra.Command, opts seedOptions, plan perfSeedPlan) {
	cmd.Printf("dry-run plan (no database writes)\n")
	cmd.Printf("  profile:              %s\n", plan.Profile)
	cmd.Printf("  users:                %d\n", plan.Users)
	cmd.Printf("  topics:               %d\n", plan.TopicCount)
	cmd.Printf("  categories:           %d (%s)\n", plan.CategoryCount, strings.Join(plan.CategorySlugs, ", "))
	for i, n := range plan.TopicsPerCategory {
		if i < 5 || i == plan.HotTopicCategoryIndex {
			cmd.Printf("    - %s: %d topics\n", plan.CategorySlugs[i], n)
		}
	}
	if plan.CategoryCount > 5 {
		cmd.Printf("    - … (%d more categories)\n", plan.CategoryCount-5)
	}
	cmd.Printf("  regular comments max/topic: %d (upper bound total %d)\n", plan.RegularCommentsMax, plan.ExpectedRegularCommentsMax)
	cmd.Printf("  hot topic index:      %d (category %s)\n", plan.HotTopicIndex, plan.CategorySlugs[plan.HotTopicCategoryIndex])
	cmd.Printf("  hot comments:         %d\n", plan.HotComments)
	cmd.Printf("  hot slug:             %s\n", plan.HotSlug)
	cmd.Printf("  est comments max:     %d\n", plan.EstimatedTotalCommentsMax)
	cmd.Printf("  disk note:            %s\n", plan.DiskNote)
	cmd.Printf("  time note:            %s\n", plan.TimeNote)
	cmd.Printf("  write requires:       --confirm-perf-db on a dedicated DATABASE_URL\n")
	_ = opts
}

// redactDatabaseURL 隐藏密码，仅用于日志。
func redactDatabaseURL(raw string) string {
	// postgres://user:pass@host/db → postgres://user:***@host/db
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			userinfo := rest[:at]
			hostpart := rest[at:]
			if colon := strings.Index(userinfo, ":"); colon >= 0 {
				return raw[:i+3] + userinfo[:colon] + ":***" + hostpart
			}
		}
	}
	return raw
}
