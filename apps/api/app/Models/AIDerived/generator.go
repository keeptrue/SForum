package aiderived

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	aidomain "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 读取面。它们都是窄接口，且与 AITools 取数用的是同一条公开读取路径：
// 「机器人为蒸馏读到的内容」必须与「访客打开帖子看到的」完全一致，因此这里
// 不新增任何可见性谓词，只声明需要哪些既有方法。
type (
	// TopicReader 读取公开主题详情。未找到或不可见由实现按既有语义返回错误。
	TopicReader interface {
		GetTopic(ctx context.Context, topicID int64) (aidomain.TopicDetail, error)
	}
	// CommentReader 读取公开评论列表。
	CommentReader interface {
		ListComments(ctx context.Context, input aidomain.CommentListInput) (aidomain.CommentList, error)
	}
	// Executor 执行一次补全。它是网关照权限的最窄面：成本等级、配额、trace、
	// 缓存、出站守卫全部在网关里，本包不接触供应商，也不知道协议差异。
	//
	// 方法名与 *supportai.Gateway.Execute 逐字一致，因此网关直接满足它，不需要
	// 适配层——多一层适配就等于多一个能绕过网关的入口（任务书硬约束 5）。
	Executor interface {
		Execute(ctx context.Context, request supportai.CompletionRequest) (supportai.CompletionResult, error)
	}
)

// 评论拉取上限：一次派生只看这一栋楼的公开评论。
//
// 它远大于提示词预算（digestCommentLimit=60），因为「先取到再截断」才能让
// 提示词里的「讨论更长」这句话是真的；取回条数就等于预算时，那句话永远不会出现。
const derivedCommentFetchLimit = 200

// GenerateInput 是一次派生生成的入参。
type GenerateInput struct {
	// Purpose 是 forum.digest 或 forum.tour。
	Purpose string
	// TopicID 是派生对象。
	TopicID int64
	// RequestedBy 是发起这次派生的运营者，写进产物的 created_by_user_id。
	//
	// 它不参与任何权限判定：触发面的鉴权在上层（CLI 侧的操作者、将来的
	// ai.manage 端点）。把它当作授权凭据会让「谁触发的」变成「谁有权限」。
	RequestedBy int64
}

// Generator 生成一份派生草稿并落库。
//
// 它只做三件事：读公开上下文 → 走网关补全 → 把非空产物写成一行草稿。它不发布、
// 不写评论、不发通知、不触发任何后续 AI 调用。
type Generator struct {
	topics    TopicReader
	comments  CommentReader
	gateway   Executor
	artifacts ArtifactStore
	// parameters 提供用途的生效参数（成本等级 / 工具白名单 / 提示词版本）。
	// 可为 nil：那时使用用途声明的默认值，与 aireply 的降级口径一致。
	parameters ParameterSource
}

// ParameterSource 提供派生用途当前生效的参数。
//
// 它与 aireply.ReplyParameterSource 分开声明而不是复用，因为两者的输入面不同：
// 派生用途没有「系统指令覆盖」这一项（蒸馏提示词是用途的一部分，不是运营者可
// 改的文案），共享接口会逼出一个本用途永远用不到的字段。
type ParameterSource interface {
	DerivedPurposeParameters(ctx context.Context, purpose string) (PurposeParameters, error)
}

// PurposeParameters 是一个派生用途的生效参数。
type PurposeParameters struct {
	CostClass     string
	ToolsAllowed  []string
	ToolsOverride bool
	PromptVersion string
	// MaxTokens 是输出预算，取自该成本等级绑定 profile 的 Defaults.MaxTokens。
	// 0 表示解析不出 profile，由本包回落到构造上限。
	MaxTokens int
}

// derivedMaxTokens 是解析不出 profile 时的输出预算下限。
//
// 派生产物是一份结构化清单（结论 + 若干要点），比一次回复长得多；给回复用的
// 512 会让蒸馏稳定地被截断在「关键要点」第一条。profile 解析不出来时用这个值
// 兜底，真正的预算仍以 profile 为准。
const derivedMaxTokens = 1_024

func NewGenerator(topics TopicReader, comments CommentReader, gateway Executor, artifacts ArtifactStore, parameters ParameterSource) *Generator {
	return &Generator{topics: topics, comments: comments, gateway: gateway, artifacts: artifacts, parameters: parameters}
}

// resolveParameters 取本次派生的生效参数。
//
// 读配置失败回退到用途声明的默认值（与 aireply.resolveParameters 同一纪律）：
// 配置存储抖动不该让一次手动触发变成「AI 不可用」，而声明本身就是可用的兜底。
func (g *Generator) resolveParameters(ctx context.Context, purpose string) (PurposeParameters, string) {
	declaration, ok := supportai.DefaultPurposeRegistry().Lookup(purpose)
	fallback := PurposeParameters{PromptVersion: declaration.PromptVersion}
	if ok {
		fallback.CostClass = declaration.CostClass
		fallback.ToolsAllowed = append([]string(nil), declaration.ToolAllowlist...)
	} else {
		fallback.CostClass = supportai.CostClassEconomy
	}
	if g == nil || g.parameters == nil {
		return fallback, ""
	}
	parameters, err := g.parameters.DerivedPurposeParameters(ctx, purpose)
	if err != nil {
		slog.WarnContext(ctx, "aiderived: load purpose parameters failed; using the declared default",
			"purpose", purpose, "err", err)
		return fallback, ""
	}
	if !supportai.ValidCostClass(parameters.CostClass) {
		parameters.CostClass = fallback.CostClass
	}
	if strings.TrimSpace(parameters.PromptVersion) == "" {
		parameters.PromptVersion = fallback.PromptVersion
	}
	reason := ""
	if strings.TrimSpace(parameters.PromptVersion) == "" {
		reason = "prompt_version_missing"
	}
	return parameters, reason
}

// Generate 生成一条草稿并返回落库后的产物。
//
// 失败语义（照抄 forum.reply 的姿态，不发明新的降级）：
//   - 话题不存在或不可见 → ErrTopicUnavailable，不写任何行；
//   - 网关不可用 → 原样返回网关错误（含 Posture），不写任何行；
//   - 模型产出空正文 → ErrEmptyArtifact，不写任何行。
//
// 三种失败都不留半成品：一张「内容为空」的草稿在预览面上与「没跑过」看起来
// 一样，却会让运营者以为已经生成过而不再重试。
func (g *Generator) Generate(ctx context.Context, input GenerateInput) (Artifact, error) {
	if g == nil || g.gateway == nil || g.artifacts == nil || g.topics == nil || g.comments == nil {
		return Artifact{}, ErrGeneratorUnavailable
	}
	purpose := strings.TrimSpace(input.Purpose)
	if !ValidArtifactPurpose(purpose) {
		return Artifact{}, fmt.Errorf("%w: unsupported purpose %q", ErrArtifactInvalid, purpose)
	}
	if input.TopicID < MinArtifactTopicID {
		return Artifact{}, fmt.Errorf("%w: topic id must be positive", ErrArtifactInvalid)
	}

	snapshot, err := g.loadSnapshot(ctx, input.TopicID)
	if err != nil {
		return Artifact{}, err
	}

	parameters, versionReason := g.resolveParameters(ctx, purpose)
	maxTokens := parameters.MaxTokens
	if maxTokens <= 0 {
		maxTokens = derivedMaxTokens
	}
	system := SystemPromptFor(purpose)
	if system == "" {
		// 用途声明存在但提示词正文缺失属于装配缺陷：失败关闭，不带着空指令
		// 去调模型（那会得到一份没有格式约束的自由文本）。
		return Artifact{}, fmt.Errorf("%w: no system prompt for purpose %q", ErrArtifactInvalid, purpose)
	}

	// 一次派生 = 一次「调用簇」：这里生成的 id 同时写进执行记录的归属
	// （Metadata）与产物的 run_id，运营者因此能把「这份草稿花了多少、什么时候
	// 调的模型」与 ai_executions 里的行对上。此前 run_id 永远为空，产物只能靠
	// purpose+topic+时间窗猜，是一次真实的可追溯性缺口。
	runID := newDerivedRunID()

	completion, err := g.gateway.Execute(ctx, supportai.CompletionRequest{
		Purpose:       purpose,
		CostClass:     parameters.CostClass,
		System:        system,
		Messages:      []supportai.Message{{Role: supportai.RoleUser, Parts: []supportai.Part{{Type: supportai.PartText, Text: BuildPrompt(purpose, snapshot)}}}},
		MaxTokens:     maxTokens,
		PromptVersion: parameters.PromptVersion,
		// 派生是运营者手动动作，不占用任何成员的每日额度：它花的是站点配额。
		// 0 是网关既有的「系统发起」口径，与 eval 命令同一条约定。
		SubjectUserID: 0,
		Metadata: supportai.Metadata{
			// ResourceType 仍是 topic（「这份产物属于哪栋楼」），ResourceID 换成
			// 本次派生的关联 id：话题归属已经落在 ai_derived_artifacts.topic_id，
			// 而按 topic 反查花费可以 join artifacts 得到，按 id 反查执行记录则
			// 只有这一条路。
			ResourceType: derivedResourceType,
			ResourceID:   runID,
		},
	})
	if err != nil {
		return Artifact{}, err
	}

	content := strings.TrimSpace(completion.Text)
	if content == "" {
		// 空产出同样是确定性失败：重试只会得到同样的空正文，却把账单乘以尝试
		// 次数。reasoning 只用来解释原因，绝不写进产物（那是模型的自言自语）。
		//
		// 错误文本必须能区分两种形态，否则运营者只有一个「空」字可查：
		//   - 推理模型把输出预算耗在思考上（stopReason=length + 有 reasoning）；
		//   - 模型真的什么都没产出（协议不兼容或内容被过滤）。
		// 处置完全不同：前者要抬该 profile 的 maxTokens 或换非推理模型，后者要
		// 查协议。这与 diagnose.go 的 diagnoseEmptyReplyHint 是同一套判据。
		reasoningOnly := strings.TrimSpace(completion.Reasoning) != ""
		slog.WarnContext(ctx, "aiderived: model returned no content",
			"purpose", purpose, "topicId", input.TopicID,
			"stopReason", completion.StopReason, "reasoningPresent", reasoningOnly,
			"maxTokens", maxTokens, "outputTokens", completion.Usage.OutputTokens,
			"promptVersionReason", versionReason)
		return Artifact{}, fmt.Errorf("%w: %s", ErrEmptyArtifact, emptyArtifactHint(completion, maxTokens))
	}

	artifact, err := g.artifacts.Insert(ctx, Artifact{
		Purpose:   purpose,
		TopicID:   input.TopicID,
		RunID:     runID,
		PromptVer: parameters.PromptVersion,
		Status:    StatusDraft,
		Content:   content,
		CreatedBy: input.RequestedBy,
	})
	if err != nil {
		return Artifact{}, err
	}
	// 产物落库成功才记 Info：日志里的「生成了」必须对应一条可被读回的行。
	slog.InfoContext(ctx, "aiderived: artifact drafted",
		"purpose", purpose, "topicId", input.TopicID, "artifactId", artifact.ID,
		"promptVersion", artifact.PromptVer, "contentRunes", len([]rune(artifact.Content)),
		"model", completion.Provider.Model, "stopReason", completion.StopReason,
		"inputTokens", completion.Usage.InputTokens, "outputTokens", completion.Usage.OutputTokens)
	return artifact, nil
}

// loadSnapshot 读取派生所需的公开上下文。
//
// 话题读不到时统一归一为 ErrTopicUnavailable：「不存在」与「不可见」必须给出
// 同一个答案，否则这条路径就成了一个话题存在性探测器（与 TopicReadTool 同一
// 纪律）。评论读取失败按同一条规则处理：主题可见但评论读不出来时，产出一份
// 「没有讨论」的摘要比直接失败更糟——那会让运营者以为楼里真的没人说话。
func (g *Generator) loadSnapshot(ctx context.Context, topicID int64) (TopicSnapshot, error) {
	topic, err := g.topics.GetTopic(ctx, topicID)
	if err != nil {
		if errors.Is(err, aidomain.ErrTopicNotFound) {
			return TopicSnapshot{}, ErrTopicUnavailable
		}
		return TopicSnapshot{}, fmt.Errorf("aiderived: read topic: %w", err)
	}
	comments, err := g.comments.ListComments(ctx, aidomain.CommentListInput{
		TopicID: topicID,
		View:    "flat",
		Page:    1,
		PerPage: derivedCommentFetchLimit,
	})
	if err != nil {
		if errors.Is(err, aidomain.ErrTopicNotFound) {
			return TopicSnapshot{}, ErrTopicUnavailable
		}
		return TopicSnapshot{}, fmt.Errorf("aiderived: read comments: %w", err)
	}
	return TopicSnapshot{Topic: topic, Comments: comments.Items}, nil
}

// derivedResourceType 是派生用途执行记录的归属类型。它与回复的 `ai.run` 分开：
// 两者都是「一次生成」，但派生的产物与回复的产物落在不同的表、由不同的界面读取。
const derivedResourceType = "ai.derived"

// newDerivedRunID 生成一次派生的关联 id（16 字节随机 hex，与 A3 的 newRunID 同规格）。
//
// 随机源不可用时退回时间戳：可追溯性是观测能力，不该让一次派生失败。
func newDerivedRunID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}

// emptyArtifactHint 把一次空产出翻译成一句可执行的建议。
//
// 判据与 diagnose.go 的 diagnoseEmptyReplyHint 一致，但文案面向「派生草稿」这个
// 场景：派生的输出预算比回复大得多（构造上限 1_024），所以「预算被思考吃光」在
// 这里更常见，也是第一个该被想到的原因。
func emptyArtifactHint(completion supportai.CompletionResult, maxTokens int) string {
	if strings.TrimSpace(completion.Reasoning) != "" {
		return fmt.Sprintf(
			"模型只产出了思考内容（reasoning），没有产出正文：推理类模型会先把 %d 个输出 token 消耗在思考上（本次 output=%d）。请提高该成本等级绑定 profile 的 maxTokens，或为它绑定一个非推理模型。",
			maxTokens, completion.Usage.OutputTokens)
	}
	switch completion.StopReason {
	case supportai.StopReasonLength:
		return fmt.Sprintf(
			"模型在输出预算内结束（finish_reason=length，%d 个 token）且没有正文：推理类模型通常先把预算消耗在思考上。请提高该 profile 的 maxTokens 后重试。",
			maxTokens)
	case supportai.StopReasonFiltered:
		return "模型输出被内容过滤拦下（finish_reason=content_filter），没有产生正文。"
	default:
		return fmt.Sprintf(
			"供应商返回了一次成功的调用，但没有正文（stopReason=%q，output=%d）。可能是该模型不兼容当前协议解析，或返回了空 completion。",
			completion.StopReason, completion.Usage.OutputTokens)
	}
}
