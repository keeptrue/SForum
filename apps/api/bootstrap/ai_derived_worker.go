package bootstrap

import (
	"context"

	"github.com/riverqueue/river"

	aiderivedjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/AIDerived"
	aiderived "github.com/keeptrue/sforum/apps/api/app/Models/AIDerived"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// newAIDerivedGenerator 用 API 已装配的依赖构建派生生成器（B3）。
//
// 它与 newEmbeddedAIReplyGenerator 的取舍相同：任一依赖缺失时返回 nil，注册
// 逻辑据此整体跳过。AI 派生是可选能力，缺装配时应当不存在，而不是退化成一份
// 行为不一致的次品装配。
//
// 与回复生成器的两处刻意差异：
//   - 不需要 identity.ActorStore 与 poster：派生不发布任何评论，因此没有「以
//     bot 身份写」这一步；
//   - 用 *Gateway 直接作为 Completer（单次补全），不接 Orchestrator：派生是
//     「读一条楼再写一份摘要」，工具循环带来的步数预算与轨迹对它是纯开销，而
//     用途声明的 ToolAllowlist 在没有编排器时不会被用到（见下面注释）。
//
// ToolAllowlist 的去向：用途声明里为两条派生用途写了只读工具白名单，但本装配
// 不接编排器，因此这两份白名单当前不会进入任何请求。它们仍然必须声明——用途
// 声明是「这个用途允许看见什么」的权威档案，接上编排器（或在 worker 里改为
// 工具循环）时它就是生效值，而运营者在控制台看到的能力边界不该取决于某一次
// 装配的细节。
func newAIDerivedGenerator(core *apiCoreStack) *aiderived.Generator {
	if core == nil || core.aiProvider == nil || core.forumProvider == nil || core.pool == nil {
		return nil
	}
	forumService := core.forumProvider.Service()
	gateway := core.aiProvider.Gateway()
	if forumService == nil || gateway == nil {
		return nil
	}
	return aiderived.NewGenerator(
		derivedTopicReader{service: forumService},
		derivedCommentReader{service: forumService},
		gateway,
		aiderived.NewPostgresArtifactStore(core.pool),
		aiDerivedPurposeParameters{gateway: gateway},
	)
}

// derivedTopicReader 把 forum.Service 适配成 aiderived.TopicReader。
//
// 用适配器而不是让 aireply 那样的窄接口直接由 *forum.Service 满足：方法签名
// 一模一样时 Go 会自动满足接口，这里显式包一层是为了在签名漂移时得到一个
// 明确的编译点，而不是让「接口意外不再满足」表现为一个空实现。
type derivedTopicReader struct {
	service *forum.Service
}

func (r derivedTopicReader) GetTopic(ctx context.Context, topicID int64) (forum.TopicDetail, error) {
	return r.service.GetTopic(ctx, topicID)
}

// derivedCommentReader 把 forum.Service 适配成 aiderived.CommentReader。
type derivedCommentReader struct {
	service *forum.Service
}

func (r derivedCommentReader) ListComments(ctx context.Context, input forum.CommentListInput) (forum.CommentList, error) {
	return r.service.ListComments(ctx, input)
}

// registerAIDerivedWorker 注册派生生成 worker（B3）。
//
// 它放在独立文件里而不是 worker.go：那个文件已经 985 行、上限 1000，注册逻辑
// 留在里面会把闸门顶破（任务书 D16 的硬要求）。
//
// generator 为 nil 时直接返回：不注册 kind，队列里的历史任务会保持 pending 而
// 不是被当成未知 kind 消耗重试（与 registerAIReplyWorker 同一条纪律）。
func registerAIDerivedWorker(registry *supportjobs.Registry, generator *aiderived.Generator) {
	if registry == nil || generator == nil {
		return
	}
	// 两个 kind 各注册一个 worker。River 用 args 类型的零值决定 kind，所以
	// 必须两个类型分别注册——只注册 DeriveArgs 会让 ai.forum_tour 变成
	// 「没有 worker」的 job（discarded: job kind is not registered）。
	registry.Add(func(workers *river.Workers) error {
		return aiderivedjobs.AddWorkers(workers, generator)
	})
}

// aiDerivedPurposeParameters 提供派生用途的生效参数（成本等级 / 提示词版本 /
// 输出预算）。
//
// 它读的是**真实** ai_settings 与用途登记表，与回复走同一条解析路径：运营者在
// 控制台改成本等级绑定，派生用途的每一次生成都会跟着变。工具白名单也一并解析，
// 即使当前装配不接编排器——参数解析的口径不该因为装配差异而分叉。
type aiDerivedPurposeParameters struct {
	gateway *supportai.Gateway
}

func (p aiDerivedPurposeParameters) DerivedPurposeParameters(ctx context.Context, purpose string) (aiderived.PurposeParameters, error) {
	settings, err := p.gateway.Settings(ctx)
	if err != nil {
		return aiderived.PurposeParameters{}, err
	}
	declaration, ok := supportai.DefaultPurposeRegistry().Lookup(purpose)
	if !ok {
		return aiderived.PurposeParameters{}, supportai.ErrPurposeUnknown
	}
	allowed := declaration.ToolAllowlist
	overridden := false
	if configured, exists := settings.Purposes[declaration.ID]; exists {
		if names, over := configured.ToolAllowlist(); over {
			allowed, overridden = names, true
		}
	}
	return aiderived.PurposeParameters{
		CostClass:     declaration.CostClass,
		ToolsAllowed:  append([]string(nil), allowed...),
		ToolsOverride: overridden,
		PromptVersion: declaration.PromptVersion,
		MaxTokens:     derivedMaxTokens(settings, declaration.CostClass),
	}, nil
}

// derivedMaxTokens 取该成本等级绑定 profile 的输出预算。解析不出 profile 时返回
// 0，由 aiderived 回落到构造上限（与 replyMaxTokens 同一条口径）。
func derivedMaxTokens(settings supportai.Settings, costClass string) int {
	profile, err := settings.ResolveProfile(costClass, "")
	if err != nil {
		return 0
	}
	return profile.Defaults.MaxTokens
}
