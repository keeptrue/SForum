package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	"github.com/keeptrue/sforum/apps/api/config"
)

// replyProbeOptions 是 `sforum ai:reply` 的入参。
type replyProbeOptions struct {
	TopicID          int64
	ParentCommentID  int64
	TriggerCommentID int64
	BotUserID        int64
	TriggerUserID    int64
	DatabaseURL      string
	// Confirm 是「真的花一次钱」的显式同意。没有它时只打印解析结果与成本提示，
	// 与 ai:eval 的同一约定保持一致。
	Confirm bool
}

// 回复探测默认值：BotUserID 取「站内 id 最小的机器人」不可靠（CLI 没有身份层
// 装配），因此默认 0 只是「工具上下文里不知道机器人是谁」，不影响出站。
const defaultReplyProbeBotUserID = 0

// newAIReplyCommand 构造 `sforum ai:reply`。
//
// 它回答的是控制台答不出的那个问题：「这份配置对这条帖子到底会不会产出一条回复」。
// 用法上刻意与 ai:eval 分工：
//   - ai:eval 在固定 fixture 上跑，用于改提示词/工具前后 diff；
//   - ai:reply 拿**真实**帖子与真实 ai_settings 跑，用于排查「控制台一片成功、
//     社区没有回复」。它不发布评论、不写回复结果表、不经过结果缓存。
func newAIReplyCommand() *cobra.Command {
	opts := replyProbeOptions{}
	cmd := &cobra.Command{
		Use:   "ai:reply",
		Short: "用真实帖子与真实配置跑一次回复生成，但不发布评论（排查「为什么没回」）",
		Long: `用一条真实帖子跑一次完整的回复生成，并打印这次生成的每一项决定事实。

它不复用结果缓存、不发布评论、不写 ai_reply_outcomes：一次排查不应该在社区留下
痕足迹，也不该让第二次排查命中第一次的答案。每次运行都会真实调用一次模型并计入
站点用量。

打印内容：
  - 生效参数：成本等级、解析到的 profile / 模型、输出预算、工具白名单、提示词版本
  - 请求指纹：system + user 与 maxTokens 的摘要（文本会因采样抖动，指纹不会）
  - 结果：停止原因、输入/输出 token、正文长度与开头预览、错误
  - 裁决：会发布 / 空回复 / 网关拒绝

用法：
  set -a; . ../../.env; set +a
  sforum ai:reply --topic-id 1237                          # 只解析配置与预算，不出站
  sforum ai:reply --topic-id 1237 --confirm                # 真实调用一次
  sforum ai:reply --topic-id 1237 --trigger-comment-id 501 --confirm`,
		Args: cobra.NoArgs,
		// 运行期失败（话题不可见、密钥缺失）不是用法错误：打印 help 只会把结论
		// 埋进一屏参数说明里。
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.TopicID <= 0 {
				return errors.New("--topic-id is required")
			}
			return runAIReplyProbe(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().Int64Var(&opts.TopicID, "topic-id", 0, "要排查的主题 id（必填）")
	cmd.Flags().Int64Var(&opts.ParentCommentID, "parent-comment-id", 0, "回复挂靠的评论 id；缺省等于 trigger-comment-id")
	cmd.Flags().Int64Var(&opts.TriggerCommentID, "trigger-comment-id", 0, "唤起回复的评论 id；缺省表示「发帖时 @ 了机器人」")
	cmd.Flags().Int64Var(&opts.BotUserID, "bot-user-id", defaultReplyProbeBotUserID, "机器人账号 id（只用于工具上下文，缺省 0）")
	cmd.Flags().Int64Var(&opts.TriggerUserID, "trigger-user-id", 0, "提问者 id（工具可见性视角）；缺省 0，即匿名口径")
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "确认发起一次真实模型调用（会计入站点用量与费用）")
	return cmd
}

func runAIReplyProbe(ctx context.Context, opts replyProbeOptions, cmd *cobra.Command) error {
	cfg := config.Load()
	databaseURL := strings.TrimSpace(opts.DatabaseURL)
	if databaseURL == "" {
		databaseURL = cfg.DatabaseURL
	}
	if databaseURL == "" {
		return errors.New("database url is empty: set DATABASE_URL or pass --database-url")
	}

	pool, err := postgres.NewPool(ctx, databaseURL, 4)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	store := supportai.NewPostgresStore(pool)
	settings, err := store.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("load ai settings: %w", err)
	}

	// 先用真实配置解析参数：这一步不花钱，却是最常见的失败点（开关、绑定、预算）。
	parameters := evalParameterSource{gateway: supportai.NewGateway(supportai.GatewayConfig{Settings: store})}
	resolved, err := parameters.ReplyPurposeParameters(ctx)
	if err != nil {
		return fmt.Errorf("resolve reply parameters: %w", err)
	}
	profile, profileErr := settings.ResolveProfile(resolved.CostClass, "")
	cmd.Printf("网关：enabled=%t revision=%d 触发模式=%s\n", settings.Enabled, settings.Revision, settings.Reply.NormalizedTriggerMode())
	if profileErr != nil {
		cmd.Printf("profile：解析失败（成本等级 %s）：%v\n", resolved.CostClass, profileErr)
	} else {
		cmd.Printf("profile：%s / %s / max_tokens=%d / timeout=%dms / tools=%t\n",
			profile.ID, profile.Model, profile.Defaults.MaxTokens, profile.Defaults.TimeoutMS, profile.ToolsSupported())
	}
	cmd.Printf("用途：%s 提示词=%s 成本等级=%s 工具=%v\n",
		aireply.ReplyPurpose, resolved.PromptVersion, resolved.CostClass, resolved.ToolsAllowed)
	if !opts.Confirm {
		cmd.Printf("\n以上为解析结果（未出站）。加上 --confirm 会真实调用一次模型。\n")
		return nil
	}

	// 密钥与出站守卫只在真正调用时装配：缺失时给出与 ai:eval 同一句硬失败。
	if strings.TrimSpace(cfg.OptionEncryptionKey) == "" {
		return errors.New("APP_OPTION_ENC_KEY is required to decrypt the provider credential")
	}
	cipher, err := cryptox.NewOptionCipher(cfg.OptionEncryptionKey)
	if err != nil {
		return fmt.Errorf("init option cipher: %w", err)
	}
	secretStore, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		return fmt.Errorf("create secret store: %w", err)
	}
	secretAudit, err := secretstore.NewPostgresAuditStore(pool)
	if err != nil {
		return fmt.Errorf("create secret audit store: %w", err)
	}
	secrets, err := secretstore.NewWithOptions(secretstore.Options{Store: secretStore, Audit: secretAudit, Cipher: cipher})
	if err != nil {
		return fmt.Errorf("create secret store service: %w", err)
	}

	gateway := supportai.NewGateway(supportai.GatewayConfig{
		Settings: store,
		Usage:    store,
		Traces:   store,
		Creds:    supportai.NewSecretStoreResolver(secrets),
		Invoker: supportai.NewHTTPInvoker(supportai.HTTPInvokerOptions{
			AllowHTTP:    !strings.EqualFold(cfg.AppEnv, "production"),
			AllowPrivate: config.AllowPrivateAIEndpoint(cfg),
		}),
		Purposes: supportai.DefaultPurposeRegistry(),
		// 关键：禁用结果缓存。否则一次空回复会被 10 分钟内的下一次排查读回来，
		// 让人以为「每次都一样」，而真相是第二次根本没有出站。
		Cache: nil,
	})

	parentID := opts.ParentCommentID
	triggerID := opts.TriggerCommentID
	if parentID == 0 {
		parentID = triggerID
	}
	completer := &recordingCompleter{
		inner: aireply.NewGatewayCompleter(gateway, nil),
	}
	poster := &capturePoster{}
	generator := aireply.NewGenerator(
		aireply.NewPostgresReader(pool),
		completer,
		poster,
		evalParameterSource{gateway: gateway},
	)

	generateErr := generator.Generate(ctx, aireply.ReplyJobInput{
		TopicID:          opts.TopicID,
		ParentCommentID:  parentID,
		TriggerCommentID: triggerID,
		TriggerUserID:    opts.TriggerUserID,
		BotUserID:        opts.BotUserID,
	})

	if completer.calls == 0 {
		cmd.Printf("\n结果：生成器没有走到模型调用。错误：%v\n", orErr(generateErr, errors.New("unknown")))
		return orErr(generateErr, errors.New("generator did not call the model"))
	}
	output := completer.output
	cmd.Printf("请求指纹：%s\n", completer.promptDigest())
	cmd.Printf("系统提示词：%d 字（含富内容段与安全尾注）\n", len([]rune(completer.request.System)))
	cmd.Printf("用户消息：%d 字\n", len([]rune(completer.request.User)))
	if strings.TrimSpace(completer.request.User) == "" {
		cmd.Printf("警告：用户消息为空——提示词没有带上帖子内容，模型无从回答。\n")
	}
	cmd.Printf("停止原因：%s 输入/输出 token：%d/%d\n", orDash(output.StopReason), output.Usage.InputTokens, output.Usage.OutputTokens)
	cmd.Printf("正文：%d 字", len([]rune(strings.TrimSpace(output.Text))))
	if preview := previewRunes(output.Text, 80); preview != "" {
		cmd.Printf("；开头：%s", preview)
	}
	cmd.Printf("\n")

	switch {
	case generateErr != nil:
		cmd.Printf("裁决：未发布（%v）\n", generateErr)
		return nil
	case poster.called:
		cmd.Printf("裁决：会发布（本次是 dry-run，未写入社区）\n")
	default:
		cmd.Printf("裁决：未发布（生成器没有调用发布器）\n")
	}
	return nil
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// previewRunes 截取开头若干字符用于终端阅读。多行文本压成一行：终端里换行会把
// 一次输出拆成几段，反而看不出这是一段正文。
func previewRunes(value string, limit int) string {
	flat := strings.Join(strings.Fields(value), " ")
	runes := []rune(flat)
	if len(runes) <= limit {
		return flat
	}
	return string(runes[:limit]) + "…"
}
