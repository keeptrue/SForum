package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	aitools "github.com/keeptrue/sforum/apps/api/app/Models/AITools"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	cryptox "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	postgres "github.com/keeptrue/sforum/apps/api/app/Support/Postgres"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	"github.com/keeptrue/sforum/apps/api/config"
)

// evalOptions 是 `sforum ai:eval` 的入参。
type evalOptions struct {
	FixtureDir  string
	BaselineDir string
	Diff        bool
	Confirm     bool
	DatabaseURL string
	// Only 按 id 限定本次要跑的 fixture（逗号分隔）。它的唯一用途是补齐失败项：
	// 一次整轮跑里个别 fixture 可能因供应商瞬时故障（DNS、连接重置）失败，
	// 全量重跑会连带刷新那些本来已成功的快照，让一次网络抖动变成一整批 diff 噪声。
	Only string
}

// defaultEvalBaselineDir 与 fixture 目录同居：基线必须进仓库，diff 才能跨机器比较。
const defaultEvalBaselineDir = "cmd/sforum/testdata/ai-eval/baseline"

// newAIEvalCommand 构造 `sforum ai:eval`。
//
// 它是能力改动的门禁工具：改提示词、改工具白名单、改预算前后各跑一次，用 --diff
// 看产出变化。它刻意是一个 CLI 而不是 tests/*.mjs：eval 需要真实 DB 装配与真实
// 模型调用，放进静态源码检查的 tests/ 会破坏那条惯例，也会误入 CI。
func newAIEvalCommand() *cobra.Command {
	opts := evalOptions{}
	cmd := &cobra.Command{
		Use:   "ai:eval",
		Short: "在固定 fixture 上跑一次真实 AI 回复生成并产出可 diff 的快照",
		Long: `在固定 fixture 上跑一次真实 AI 回复生成，产出可 diff 的快照。

装配是真实的那一套：真实 Gateway（真过闸门、真出站、真写执行记录）+ 真实
Orchestrator（真实工具循环）+ 真实 aireply.Generator。只有两处替换：
  - 线程读取换成 fixture（结果不依赖本机开发库内容）；
  - 评论发布换成内存捕获（**不发布任何评论**，开发库不会留垃圾帖）。

因此它测的是「模型拿到这份上下文会产出什么」，而不是「本机库里的帖子长什么样」。

前置条件（缺一即失败，不静默降级）：
  DATABASE_URL 或 --database-url   用于读 ai_settings（网关开关、用途绑定、密钥引用）
  APP_OPTION_ENC_KEY              用于解密 Secret Store 里的 provider 密钥
  网关 Enabled=true 且 forum.reply 的成本等级能解析出可用 profile

成本与副作用：
  - 每条 fixture 至少一次真实模型调用（带工具的会更多），**会消耗站点配额与费用**。
  - 用户级配额按 fixture 里的 triggerUserID 计量。fixture 故意用 900001+ 这类
    合成 id（不对应任何真实成员），因此不会吃掉真实成员当天的额度。
  - 每次模型调用都会在 ai_executions 留下一条 forum.reply 记录——这是观测面，不是污染。
  - 结果缓存被禁用（否则第二次跑会命中 10 分钟缓存，--diff 全部报 unchanged）。
  - 不发布评论。

用法：
  set -a; . ../../.env; set +a
  sforum ai:eval --fixture-dir cmd/sforum/testdata/ai-eval            # 只预估，不调用
  sforum ai:eval --fixture-dir cmd/sforum/testdata/ai-eval --confirm  # 生成/刷新基线
  sforum ai:eval --fixture-dir cmd/sforum/testdata/ai-eval --diff     # 与基线比对`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAIEval(cmd.Context(), opts, cmd)
		},
	}
	cmd.Flags().StringVar(&opts.FixtureDir, "fixture-dir", "", "fixture 目录（必填），其中每个 *.json 是一条用例")
	cmd.Flags().StringVar(&opts.BaselineDir, "baseline-dir", defaultEvalBaselineDir, "快照输出/比对目录（默认 testdata 下的 baseline）")
	cmd.Flags().BoolVar(&opts.Diff, "diff", false, "与基线目录里已提交的快照逐字段比较")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "确认执行真实模型调用（不带时只打印预估并退出）")
	cmd.Flags().StringVar(&opts.DatabaseURL, "database-url", "", "覆盖 DATABASE_URL")
	cmd.Flags().StringVar(&opts.Only, "only", "", "只跑这些 fixture id（逗号分隔），用于补齐瞬时失败项")
	return cmd
}

// runAIEval 是先检查再执行的两段式入口。
//
// 先检查的理由是钱：一次误跑会把每条 fixture 都真调一遍。因此所有「必然失败」
// 的条件（缺密钥、缺库）都在任何出站调用之前判定完，并且不带 --confirm 时连
// 装配都不做，只打印预估。
func runAIEval(ctx context.Context, opts evalOptions, cmd *cobra.Command) error {
	fixtures, err := loadEvalFixtures(opts.FixtureDir)
	if err != nil {
		return err
	}
	fixtures, err = selectEvalFixtures(fixtures, opts.Only)
	if err != nil {
		return err
	}

	if !opts.Confirm {
		printEvalEstimate(cmd, fixtures)
		return nil
	}

	stack, err := assembleEvalStack(ctx, opts)
	if err != nil {
		return err
	}
	defer stack.close()

	current := make(map[string]evalSnapshot, len(fixtures))
	failures := 0
	for _, fixture := range fixtures {
		snapshot, runErr := stack.runFixture(ctx, fixture)
		if runErr != nil {
			// 单条失败不终止整轮：其余用例的结果仍然有价值，而失败本身也进快照
			// （Error 字段），diff 才能看见「上次能跑、这次不能跑」。
			failures++
			cmd.Printf("! %s: %v\n", fixture.ID, runErr)
		} else {
			cmd.Printf("  %s: %d chars, %d model calls, %d tool calls\n",
				fixture.ID, len([]rune(snapshot.Text)), snapshot.ModelCalls, len(snapshot.ToolTrace))
		}
		current[fixture.ID] = snapshot
	}

	if opts.Diff {
		baseline, err := loadEvalBaseline(opts.BaselineDir)
		if err != nil {
			return err
		}
		if len(baseline) == 0 {
			return fmt.Errorf("baseline dir %s is empty: run without --diff first to generate it", opts.BaselineDir)
		}
		printEvalDiff(cmd.OutOrStdout(), buildEvalReports(baseline, current))
	} else {
		for _, fixture := range fixtures {
			if err := writeEvalSnapshot(opts.BaselineDir, current[fixture.ID]); err != nil {
				return err
			}
		}
		cmd.Printf("wrote %d snapshots to %s\n", len(current), opts.BaselineDir)
	}

	if failures > 0 {
		// 失败退出码非零：把一次「有一半没跑成」的运行报成成功会让门禁失去意义。
		return fmt.Errorf("%d of %d fixtures failed; snapshots were still written for inspection", failures, len(fixtures))
	}
	return nil
}

// printEvalEstimate 是不带 --confirm 时的成本提示，并按「必然失败」的条件提前拦。
//
// 它同时校验前置条件：让「跑不起来」在花钱之前暴露，而不是让运营者在第 3 条
// fixture 上收到一个凭据错误。
func printEvalEstimate(cmd *cobra.Command, fixtures []evalFixture) {
	cmd.Printf("将执行 %d 次真实调用，约 %s tokens\n", len(fixtures), estimateEvalTokens(fixtures))
	cmd.Printf("（每条 fixture 至少一次模型调用；带工具的用例会按工具步数递增）\n")
	cmd.Printf("不发布任何评论；会消耗站点配额与费用，并在 ai_executions 留下 forum.reply 记录。\n")
	if problem := evalPreconditionHint(); problem != "" {
		cmd.Printf("\n前置条件不满足，加 --confirm 会失败：%s\n", problem)
	}
	cmd.Printf("\n确认无误后加 --confirm 执行。\n")
}

// estimateEvalTokens 给一个量级估计，不是精确计数。
//
// 它必须偏保守（宁高不低）：这是给运营者判断「值不值得跑」的数字，低估会让人
// 在账单上得到意外。口径是经验值（输入按 2 字节/token、每条固定开销 600、输出
// 按 400），只用于提示。
func estimateEvalTokens(fixtures []evalFixture) string {
	var promptBytes int
	for _, fixture := range fixtures {
		promptBytes += len(fixture.TopicTitle) + len(fixture.TopicBody)
		for _, comment := range fixture.Comments {
			promptBytes += len(comment.Content) + len(comment.Author.DisplayName)
		}
	}
	inputTokens := promptBytes/2 + len(fixtures)*600
	outputTokens := len(fixtures) * 400
	return fmt.Sprintf("%d/%d", inputTokens, outputTokens)
}

func evalPreconditionHint() string {
	cfg := config.Load()
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return "DATABASE_URL 未设置（或用 --database-url 指定）"
	}
	if strings.TrimSpace(cfg.OptionEncryptionKey) == "" {
		return "APP_OPTION_ENC_KEY 未设置：无法解密 Secret Store 里的 provider 密钥"
	}
	return ""
}

// evalStack 是一次 eval 运行所需的真实装配。
type evalStack struct {
	gateway *supportai.Gateway
	// topicIDs 把 fixture id 映射到假主题 id（与 fixtureThreadReader 的编号一致）。
	topicIDs map[string]int64
	close    func()
}

// assembleEvalStack 装配 gateway 与它依赖的持久层。
//
// 刻意复用生产构造函数（NewGateway / NewOrchestrator / NewGenerator）而不是自己
// 拼一份简化版：eval 的意义是「生产配置下的产出」，自装配会把 eval 变成一个
// 只证明「我这份拼装能跑」的测试。
func assembleEvalStack(ctx context.Context, opts evalOptions) (*evalStack, error) {
	fixtures, err := loadEvalFixtures(opts.FixtureDir)
	if err != nil {
		return nil, err
	}

	cfg := config.Load()
	databaseURL := strings.TrimSpace(opts.DatabaseURL)
	if databaseURL == "" {
		databaseURL = cfg.DatabaseURL
	}
	if databaseURL == "" {
		return nil, errors.New("database url is empty: set DATABASE_URL or pass --database-url")
	}
	// APP_OPTION_ENC_KEY 缺失是硬失败，不降级：透明 cipher 读不出加密过的密钥，
	// 那会表现成「密钥缺失」，而真实原因是这把钥匙没带。两者的处置完全不同。
	if strings.TrimSpace(cfg.OptionEncryptionKey) == "" {
		return nil, errors.New("APP_OPTION_ENC_KEY is required: without it the Secret Store cannot decrypt provider credentials; export it (set -a; . ../../.env; set +a) and retry")
	}
	cipher, err := cryptox.NewOptionCipher(cfg.OptionEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("init option cipher: %w", err)
	}
	if !cipher.Enabled() {
		return nil, errors.New("APP_OPTION_ENC_KEY is not a 32-byte hex key: the cipher stayed disabled")
	}

	pool, err := postgres.NewPool(ctx, databaseURL, 4)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	secretStore, err := secretstore.NewPostgresStore(pool)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create secret store: %w", err)
	}
	secretAudit, err := secretstore.NewPostgresAuditStore(pool)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create secret audit store: %w", err)
	}
	secrets, err := secretstore.NewWithOptions(secretstore.Options{
		Store: secretStore, Audit: secretAudit, Cipher: cipher,
		// eval 用与部署同一个 cipher；AllowTransparent 保持关闭，避免「没配密钥
		// 也能跑」的假象。
		AllowTransparent: false,
	})
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create secret store service: %w", err)
	}

	store := supportai.NewPostgresStore(pool)
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
		// 结果缓存刻意关闭：命中缓存会让第二次跑报 unchanged，基线自证变成假绿。
		Cache: nil,
	})

	topicIDs := make(map[string]int64, len(fixtures))
	for index, fixture := range fixtures {
		topicIDs[fixture.ID] = int64(index + 1)
	}

	return &evalStack{gateway: gateway, topicIDs: topicIDs, close: func() { pool.Close() }}, nil
}

// evalParameterSource 提供回复用途当前生效的参数。
//
// 它读的是**真实** ai_settings：成本等级、工具白名单、提示词版本、MaxTokens 全部
// 来自站点配置与用途声明。于是 eval 会随运营者改配置而变化——这正是「改配置前后
// 能 diff」的前提，不是缺陷。
type evalParameterSource struct {
	gateway *supportai.Gateway
}

// ReplySystemPrompt 返回**基础**提示词（不含安全尾注与工具段）：组装由生成器
// 调用 supportai.ComposeReplySystemPrompt 完成，与生产路径逐字同源。
//
// 这里若返回已经组装好的 EffectiveSystemPrompt()，eval 的 system 就会缺掉
// 「本次可调用的工具」那一段——基线测的将不是生产实际发出去的东西。
func (s evalParameterSource) ReplySystemPrompt(context.Context) (string, error) {
	settings, err := s.gateway.Settings(context.Background())
	if err != nil {
		return "", err
	}
	return settings.Reply.BaseSystemPrompt(), nil
}

func (s evalParameterSource) ReplyPurposeParameters(context.Context) (aireply.ReplyPurposeParameters, error) {
	settings, err := s.gateway.Settings(context.Background())
	if err != nil {
		return aireply.ReplyPurposeParameters{}, err
	}
	declaration, ok := supportai.DefaultPurposeRegistry().Lookup(aireply.ReplyPurpose)
	if !ok {
		return aireply.ReplyPurposeParameters{}, supportai.ErrPurposeUnknown
	}
	allowed := declaration.ToolAllowlist
	overridden := false
	if configured, exists := settings.Purposes[declaration.ID]; exists {
		if names, over := configured.ToolAllowlist(); over {
			allowed, overridden = names, true
		}
	}
	return aireply.ReplyPurposeParameters{
		CostClass:     declaration.CostClass,
		ToolsAllowed:  append([]string(nil), allowed...),
		ToolsOverride: overridden,
		PromptVersion: declaration.PromptVersion,
		MaxTokens:     resolveEvalMaxTokens(settings, declaration.CostClass),
	}, nil
}

// resolveEvalMaxTokens 取该成本等级绑定 profile 的输出预算。
//
// 解析不出 profile 时返回 0，由生成器回落到 ReplyMaxTokens——与生产同一条路径
// （aireply.resolveMaxTokens）。这里不做第二份兜底逻辑。
func resolveEvalMaxTokens(settings supportai.Settings, costClass string) int {
	profile, err := settings.ResolveProfile(costClass, "")
	if err != nil {
		return 0
	}
	return profile.Defaults.MaxTokens
}

// runFixture 对一条 fixture 跑一次完整生成，并收敛成快照。
//
// 工具登记表按 fixture 重建：语料是每条用例自带的，「站内有什么」在不同用例之间
// 本来就不同。工具实现本身仍是 aitools 的真实内置工具。
func (s *evalStack) runFixture(ctx context.Context, fixture evalFixture) (evalSnapshot, error) {
	topicID := s.topicIDs[fixture.ID]
	// fixture 只有一条，因此 reader 的编号是 1；这里把它重绑到该 fixture 的假主题
	// id，保证「线程读取」与「工具读取当前这栋楼」看到的是同一个主题。
	reader := newFixtureThreadReader([]evalFixture{fixture})
	thread, ok := reader.threads[1]
	if !ok {
		return evalSnapshot{ID: fixture.ID}, errors.New("fixture thread was not built")
	}
	reader.threads = map[int64]aireply.ReplyContext{topicID: thread}
	corpus := newEvalCorpusReader(fixture)
	corpus.self.ID = topicID

	registry, err := aitools.NewBuiltinRegistry(aitools.Deps{
		Search:   corpus,
		Topics:   corpus,
		Comments: corpus,
		Lister:   corpus,
		Profiles: corpus,
		Users:    corpus,
		SiteURL:  func(context.Context) string { return "https://eval.invalid" },
		// 固定时钟：time-now 的产出若随运行时间变化，基线永远无法稳定。
		Clock: evalFixedClock,
	})
	if err != nil {
		return evalSnapshot{ID: fixture.ID}, fmt.Errorf("build tool registry: %w", err)
	}
	orchestrator := supportai.NewOrchestrator(supportai.OrchestratorConfig{
		Gateway:  s.gateway,
		Registry: registry,
	})
	poster := &capturePoster{}
	completer := &recordingCompleter{
		inner: aireply.NewGatewayCompleter(s.gateway, orchestrator).WithImages(evalImageReader{}),
	}
	generator := aireply.NewGenerator(reader, completer, poster, evalParameterSource{gateway: s.gateway}).
		// 与生产同源：工具段由编排器按「本次真的会带上的工具」渲染。
		WithToolSectioner(orchestrator)

	// 评论触发时 parent 与 trigger 是同一条评论（生产里「回复机器人」就是这样）。
	parentID := int64(0)
	if fixture.TriggerCommentIndex >= 0 {
		parentID = topicID
	}
	// TriggerUserID 固定为 0（系统口径），**不**用 fixture 里的 triggerUserID：
	//
	// 生成器把 TriggerUserID 同时当作「用户配额主体」和「工具可见性的提问者身份」
	// （见 Generator.Generate → CompletionInput.SubjectUserID / ToolContext.ViewerUserID）。
	// 一个 eval 门禁必须能反复跑，而 per-user 日额度是 20 次：用 fixture 的用户 id
	// 跑十几轮之后，那几条用例会永久变成 quota_exceeded，直到次日窗口滚动——工具
	// 自己把自己跑坏，且失败原因看起来像「模型不可用」。
	//
	// 匿名身份对 eval 是正确的口径：fixture 全部是公开内容，可见性结果与登录成员
	// 相同；站点级/分钟级计数照常增长（帮助里已写明），费用与观测记录不受影响。
	// fixture 里的 triggerUserID 因此只作为「这是谁在问」的人物设定保留，不参与计量。
	generateErr := generator.Generate(ctx, aireply.ReplyJobInput{
		TopicID:          topicID,
		ParentCommentID:  parentID,
		TriggerCommentID: parentID,
		TriggerUserID:    0,
		BotUserID:        fixture.BotUserID,
	})
	if completer.calls == 0 {
		return newEvalSnapshot(fixture.ID, aireply.CompletionOutput{}, "", false, "", generateErr),
			fmt.Errorf("generator did not reach the completer: %w", orErr(generateErr, errors.New("unknown reason")))
	}
	return newEvalSnapshot(fixture.ID, completer.output, poster.content, poster.called, completer.promptDigest(), generateErr), generateErr
}

func orErr(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
