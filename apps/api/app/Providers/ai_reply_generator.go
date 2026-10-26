package providers

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	aireply "github.com/keeptrue/sforum/apps/api/app/Models/AIReply"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// NewAIReplyGenerator 组装回复生成器。API 与 worker 两条装配路径共用它，
// 避免两处对同一个生成器做出不同解释。orchestrator 可为 nil：那时回复退化为
// 单次补全（无工具），与工具能力引入前一致。imageReader 也可为 nil：那时
// 多模态输入整体关闭，回复保持纯文本。
//
// pool 同时承载 B4 的引用存储与 B5 的会话状态（两者都只需要连接池）：它们与
// 线程读取共用同一份连接，因为引用校验的可见性谓词必须与线程读取完全同源
// （见 PostgresCitationStore.topicVisiblePredicate 的注释）。
// pool 为 nil 时引用与状态整体关闭，回复退化为「无依据、无记忆」——这是既有的
// 可选能力纪律，不是降级失败。
func NewAIReplyGenerator(
	pool *pgxpool.Pool,
	users identity.ActorStore,
	forumService *forum.Service,
	gateway *supportai.Gateway,
	orchestrator *supportai.Orchestrator,
	parameters aireply.ReplyParameterSource,
	imageReader aireply.ImageReader,
) *aireply.Generator {
	generator := aireply.NewGenerator(
		aireply.NewPostgresReader(pool),
		aireply.NewGatewayCompleter(gateway, orchestrator).WithImages(imageReader),
		aireply.NewBotCommentPoster(users, forumService),
		parameters,
	)
	// 顺序固定：先引用、后状态。WithTopicState 在缺引用时不装配（状态里的事实
	// 只能来自 B4 校验过的引用），所以顺序颠倒会让状态静默失效。
	if citations := NewAIReplyCitationStore(pool); citations != nil {
		generator = generator.WithCitations(citations).
			WithTopicState(aireply.NewPostgresTopicStateStore(pool))
	}
	// 工具轨迹与工具段互相独立：前者只写（发布之后落库），后者只读（组装提示词）。
	// 两者都缺时行为与引入前完全一致——回复照常，只是看不到调用、模型也不知道
	// 自己有哪些工具。
	if pool != nil {
		generator = generator.WithToolCalls(aireply.NewPostgresToolCallStore(pool))
	}
	if orchestrator != nil {
		generator = generator.WithToolSectioner(orchestrator)
	}
	// 回复结果与幂等判据来自同一处存储：写入的那一行就是「这条触发回过没有」的
	// 唯一答案。pool 为 nil 时整体关闭（与引用、状态同一条可选能力纪律）。
	if outcomes := NewAIReplyOutcomeStore(pool); outcomes != nil {
		generator = generator.WithOutcomes(outcomes)
	}
	return generator
}

// NewAIReplyOutcomeStore 构造回复结果存储。pool 为 nil 时返回 nil 接口值，
// 生成器据此整体关闭结果记录与幂等判定。
//
// 写面（生成器）与读面（控制台）共用同一个实现，但走两条权限路径：写入没有
// actor（由队列执行），读取必须经过 Models/AI 服务层的 ai.manage 判定。
func NewAIReplyOutcomeStore(pool *pgxpool.Pool) aireply.OutcomeRecorder {
	if pool == nil {
		return nil
	}
	return supportai.NewPostgresStore(pool)
}

// NewForumToolCallReader 把工具轨迹存储适配成 Forum 侧的读取面。
//
// 与 NewForumCitationReader 同一形状：两侧各有自己的结构（Forum 不 import
// aireply，反向依赖会把方向倒过来），转换处就是那份「字段必须逐字一致」契约的
// 唯一执行点。
//
// 注意公开投影在这里就已经做减法：ResultDigest 不转换（它是工具结果片段，可能
// 包含提问者可见而读者不可见的内容），Arguments 保留（写入侧已脱敏并截断）。
func NewForumToolCallReader(pool *pgxpool.Pool) forum.ToolCallReader {
	if pool == nil {
		return nil
	}
	return aireplyToolCallReader{store: aireply.NewPostgresToolCallStore(pool)}
}

type aireplyToolCallReader struct {
	store *aireply.PostgresToolCallStore
}

func (r aireplyToolCallReader) ListBySourceComment(ctx context.Context, sourceCommentID int64) ([]forum.ToolCallEntry, error) {
	items, err := r.store.ListBySourceComment(ctx, sourceCommentID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	converted := make([]forum.ToolCallEntry, 0, len(items))
	for _, item := range items {
		converted = append(converted, forum.ToolCallEntry{
			Tool:      item.Tool,
			Arguments: item.Arguments,
			IsError:   item.IsError,
			Failure:   item.Failure,
			LatencyMS: item.LatencyMS,
		})
	}
	return converted, nil
}

// NewAIReplyCitationStore 构造 B4/B5 共用的引用存储。pool 为 nil 时返回 nil
// （接口类型的 nil，不是 typed-nil）：生成器据此整体关闭引用能力。
//
// 它同时被 Forum 的依据读取端复用（forum.CitationReader），因此「写入时按哪套
// 可见性校验」与「公开读取时按哪套判定」不会分叉。
func NewAIReplyCitationStore(pool *pgxpool.Pool) aireply.CitationStore {
	if pool == nil {
		return nil
	}
	return aireply.NewPostgresCitationStore(pool)
}

// NewForumCitationReader 把引用存储适配成 Forum 侧的读取面。
//
// 两侧各有一个 Citation 结构（Forum 不 import aireply：Forum 更底层，反向依赖会
// 把方向倒过来），所以这里做一次显式转换而不是类型别名——转换处就是那份「字段
// 必须逐字一致」契约的唯一执行点。
func NewForumCitationReader(pool *pgxpool.Pool) forum.CitationReader {
	if pool == nil {
		return nil
	}
	// 直接持具体类型而不是 CitationStore 接口：读取（ListBySourceComment）是
	// 消费面需要的、生成面不需要的能力，把它塞进 CitationStore 会让生成器
	// 依赖一个它从不调用的方法。
	return aireplyCitationReader{store: aireply.NewPostgresCitationStore(pool)}
}

type aireplyCitationReader struct {
	store *aireply.PostgresCitationStore
}

func (r aireplyCitationReader) ListBySourceComment(ctx context.Context, sourceCommentID int64) ([]forum.Citation, error) {
	items, err := r.store.ListBySourceComment(ctx, sourceCommentID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	converted := make([]forum.Citation, 0, len(items))
	for _, item := range items {
		converted = append(converted, forum.Citation{
			TopicID:   item.TopicID,
			CommentID: item.CommentID,
			Excerpt:   item.Excerpt,
		})
	}
	return converted, nil
}
