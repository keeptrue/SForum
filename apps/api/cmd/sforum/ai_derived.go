package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	aiderivedjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/AIDerived"
	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	"github.com/keeptrue/sforum/apps/api/config"
)

// `sforum ai:derived` 是 B3 的手动触发面：运营者指定一条话题与一个用途，入队一次
// 派生任务，产出一份**草稿**（ai_derived_artifacts），不发布、不写评论。
//
// 为什么是 CLI 而不是定时任务或事件钩子：任务书 B3 明确「触发面只做运营者手动」，
// 定时与事件驱动会重新引入「自主触发」的争议面（AI 产物再触发 AI 是硬约束 3 的
// 禁区）。CLI 是最小的手动面，控制台按钮将来复用同一服务层。
//
// 为什么不需要网关与密钥：真正执行在 worker 里（那边已有完整装配），CLI 只入队
// 并读回自己的产物表。要求 CLI 也持有一份 provider 密钥，会让「触发」与「执行」
// 各自持有一套凭据，而这套凭据的轮换本来只有一处。
//
// 为什么是一个父命令加两个子命令而不是两个顶层命令：根命令的注册行是与 T2 共享
// 的文件（command.go），一个父命令让本次改动在那里只占一行。

type aiDerivedOptions struct {
	DatabaseURL string
	Purpose     string
	TopicID     int64
	ActorUserID int64
	Wait        bool
	WaitTimeout time.Duration
	Limit       int
	Status      string
}

func newAIDerivedCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai:derived",
		Short: "AI 派生内容（话题蒸馏 / 长楼导览）的手动触发与预览",
		Long: `AI 派生内容的手动触发与预览。

派生用途有两条：
  forum.digest  话题蒸馏：把讨论收敛成「一句话结论 + 关键要点 + 未解决问题」
  forum.tour    长楼导览：「主线 + 争论点 + 可跳过楼层」的阅读路线

产物**不会**发布：它只写成一行 status=draft 的草稿（ai_derived_artifacts），
等运营者预览后另行处置。任何派生任务都不写 comments / topics，也不进审核台
——AI 草稿与待审内容必须是两回事。

权限：--actor-user-id 指向的账号必须持有 ai.manage（与轨迹回放同一个权限键；
手动触发是管理动作，不新开权限键）。`,
	}
	cmd.AddCommand(newAIDerivedRunCommand(), newAIDerivedListCommand())
	return cmd
}

func newAIDerivedRunCommand() *cobra.Command {
	opts := aiDerivedOptions{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "入队一次派生任务",
		Long: `入队一次派生任务，产物落成草稿。

前置条件（缺一即失败，不静默降级）：
  DATABASE_URL 或 --database-url   用于入队与读回产物
  worker 必须正在运行              任务在队列里执行，CLI 不发起任何模型调用

用法：
  sforum ai:derived run --topic-id 42 --purpose digest --actor-user-id 1
  sforum ai:derived run --topic-id 42 --purpose tour --actor-user-id 1 --wait`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAIDerivedRunCommand(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL")
	cmd.Flags().StringVar(&opts.Purpose, "purpose", aiderived.PurposeDigest, "用途：digest（forum.digest）| tour（forum.tour）")
	cmd.Flags().Int64Var(&opts.TopicID, "topic-id", 0, "派生对象的话题 id（必填）")
	cmd.Flags().Int64Var(&opts.ActorUserID, "actor-user-id", 0, "发起者账号 id，必须持有 ai.manage（写入产物 created_by_user_id）")
	cmd.Flags().BoolVar(&opts.Wait, "wait", false, "入队后轮询产物表，等到草稿出现并打印正文")
	cmd.Flags().DurationVar(&opts.WaitTimeout, "wait-timeout", 90*time.Second, "--wait 的最长等待时间")
	return cmd
}

func newAIDerivedListCommand() *cobra.Command {
	opts := aiDerivedOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列取已生成的草稿",
		Long: `列取已生成的派生草稿，创建时间倒序。

它是预览面：草稿的正文只在这里（以及将来的控制台）被读出。--status 取
draft / published / discarded；缺省不过滤。

用法：
  sforum ai:derived list --topic-id 42
  sforum ai:derived list --purpose tour --status draft --actor-user-id 1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAIDerivedListCommand(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL")
	cmd.Flags().StringVar(&opts.Purpose, "purpose", "", "只看某个用途：digest | tour（缺省不过滤）")
	cmd.Flags().Int64Var(&opts.TopicID, "topic-id", 0, "只看某条话题（缺省不过滤）")
	cmd.Flags().StringVar(&opts.Status, "status", "", "只看某个状态：draft | published | discarded")
	cmd.Flags().Int64Var(&opts.ActorUserID, "actor-user-id", 0, "读取者账号 id，必须持有 ai.manage")
	cmd.Flags().IntVar(&opts.Limit, "limit", aiderived.DefaultArtifactListLimit, "最多列出多少条")
	return cmd
}

// normalizeDerivePurpose 接受简写（digest / tour）与完整 id（forum.digest）两种写法。
//
// 两种都收是因为运营者在控制台与日志里看到的是完整 id，而手打时简写更自然。
// 除此之外不接受任何取值：拼错的用途必须在这里失败，而不是变成一条永远匹配不上
// 用途声明的孤儿产物。
func normalizeDerivePurpose(value string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "digest", aiderived.PurposeDigest:
		return aiderived.PurposeDigest, nil
	case "tour", aiderived.PurposeTour:
		return aiderived.PurposeTour, nil
	default:
		return "", fmt.Errorf("unsupported --purpose %q: use digest (forum.digest) or tour (forum.tour)", value)
	}
}

// resolveDeriveDatabaseURL 按「命令行 > 环境变量」取数据库地址。
//
// 走 config.Load() 而不是直接读 os.Getenv：config 是仓库唯一的配置入口，
// 绕过它会让 CLI 与其它命令对同一个变量产生两种解释。
func resolveDeriveDatabaseURL(value string) (string, error) {
	databaseURL := strings.TrimSpace(value)
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(config.Load().DatabaseURL)
	}
	if databaseURL == "" {
		return "", errors.New("database url is empty: set DATABASE_URL or pass --database-url")
	}
	return databaseURL, nil
}

func runAIDerivedRunCommand(ctx context.Context, opts aiDerivedOptions, cmd *cobra.Command) error {
	// 用法契约先于连接配置校验：参数错误不该先花一次连库的时间。
	if opts.ActorUserID <= 0 {
		return errors.New("ai:derived run requires --actor-user-id (the account holding ai.manage, recorded on the artifact)")
	}
	if opts.TopicID <= 0 {
		return errors.New("ai:derived run requires --topic-id")
	}
	purpose, err := normalizeDerivePurpose(opts.Purpose)
	if err != nil {
		return err
	}
	databaseURL, err := resolveDeriveDatabaseURL(opts.DatabaseURL)
	if err != nil {
		return err
	}

	pool, err := postgres.NewPool(ctx, databaseURL, 2)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	// 权限由真实 RBAC 把守：这里先给出「账号不存在 / 没有权限」的可读错误，
	// 服务层再判一次（它是权威）。两处判定不是重复，而是同一权威的两个入口。
	actor, err := identity.NewPostgresStore(pool).LoadActor(ctx, opts.ActorUserID)
	if err != nil {
		return fmt.Errorf("load actor %d: %w (the actor must be a real account holding ai.manage)", opts.ActorUserID, err)
	}

	// insert-only client：CLI 只入队，不注册执行器。注册执行器会让命令行进程在
	// 退出前把任务跑起来，那等于把「触发」与「执行」的时机混成一件不确定的事。
	jobClient, err := supportjobs.NewInsertOnlyClient(pool, supportjobs.Config{})
	if err != nil {
		return fmt.Errorf("create job client: %w", err)
	}
	service := aiderived.NewService(aiderived.ServiceConfig{
		Artifacts: aiderived.NewPostgresArtifactStore(pool),
		Enqueuer:  aiderivedjobs.NewEnqueuer(supportjobs.NewDispatcher(jobClient)),
	})

	startedAt := time.Now()
	requestID, err := service.Enqueue(ctx, actor, aiderived.EnqueueInput{
		Purpose:     purpose,
		TopicID:     opts.TopicID,
		RequestedBy: actor.ID,
	})
	if err != nil {
		return err
	}
	cmd.Printf("enqueued %s on topic %d (request=%s, queue=%s)\n", purpose, opts.TopicID, requestID, supportjobs.QueueAI)
	cmd.Printf("产物只写进 ai_derived_artifacts（status=draft）：不发布、不写评论、不进审核台。\n")

	if !opts.Wait {
		cmd.Printf("用 sforum ai:derived list --topic-id %d 查看结果。\n", opts.TopicID)
		return nil
	}
	return waitForDerivedArtifact(ctx, cmd, service, actor, opts, purpose, startedAt)
}

// waitForDerivedArtifact 轮询产物表，等本次触发落下的草稿出现。
//
// 判据是「created_at 不早于入队时刻」，不是「表里有这个 topic 的产物」：后者会把
// 上一次触发的旧草稿当成这一次的结果，运营者会以为新一轮已经跑完了。
//
// 超时只说「还没出现」，不说「失败」：任务可能仍在重试（MaxAttempts=3，供应商侧
// 超时是常见失败），两者的处置不同（再等 vs 排查配置），一句话说死会误导。
func waitForDerivedArtifact(
	ctx context.Context,
	cmd *cobra.Command,
	service *aiderived.Service,
	actor identity.Actor,
	opts aiDerivedOptions,
	purpose string,
	startedAt time.Time,
) error {
	deadline := time.Now().Add(opts.WaitTimeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		items, err := service.List(ctx, actor, aiderived.ArtifactFilter{
			Purpose: purpose,
			TopicID: opts.TopicID,
			Limit:   5,
		})
		if err != nil {
			return err
		}
		for _, item := range items {
			if !item.CreatedAt.Before(startedAt) {
				printDerivedArtifact(cmd, item)
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"no artifact appeared within %s; the job may still be retrying (MaxAttempts=3) — check worker logs for purpose %s on topic %d",
				opts.WaitTimeout, purpose, opts.TopicID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runAIDerivedListCommand(ctx context.Context, opts aiDerivedOptions, cmd *cobra.Command) error {
	if opts.ActorUserID <= 0 {
		return errors.New("ai:derived list requires --actor-user-id (the account holding ai.manage)")
	}
	databaseURL, err := resolveDeriveDatabaseURL(opts.DatabaseURL)
	if err != nil {
		return err
	}
	purpose := ""
	if strings.TrimSpace(opts.Purpose) != "" {
		if purpose, err = normalizeDerivePurpose(opts.Purpose); err != nil {
			return err
		}
	}

	pool, err := postgres.NewPool(ctx, databaseURL, 2)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	actor, err := identity.NewPostgresStore(pool).LoadActor(ctx, opts.ActorUserID)
	if err != nil {
		return fmt.Errorf("load actor %d: %w (the actor must be a real account holding ai.manage)", opts.ActorUserID, err)
	}
	service := aiderived.NewService(aiderived.ServiceConfig{Artifacts: aiderived.NewPostgresArtifactStore(pool)})

	items, err := service.List(ctx, actor, aiderived.ArtifactFilter{
		Purpose: purpose,
		TopicID: opts.TopicID,
		Status:  strings.TrimSpace(opts.Status),
		Limit:   opts.Limit,
	})
	if err != nil {
		return err
	}
	if len(items) == 0 {
		cmd.Println("no artifacts matched.")
		return nil
	}
	for _, item := range items {
		printDerivedArtifact(cmd, item)
	}
	return nil
}

func printDerivedArtifact(cmd *cobra.Command, artifact aiderived.Artifact) {
	cmd.Printf("\nartifact #%d  %s  topic=%d  status=%s  promptVersion=%s\n",
		artifact.ID, artifact.Purpose, artifact.TopicID, artifact.Status, artifact.PromptVer)
	if artifact.RunID != "" {
		cmd.Printf("run=%s\n", artifact.RunID)
	}
	cmd.Printf("createdAt=%s requestedBy=%d\n", artifact.CreatedAt.Format(time.RFC3339), artifact.CreatedBy)
	cmd.Printf("\n%s\n", artifact.Content)
}
