package providers

import (
	"github.com/jackc/pgx/v5/pgxpool"

	aireply "github.com/zhuchunshu/sforum/apps/api/app/Models/AIReply"
	forum "github.com/zhuchunshu/sforum/apps/api/app/Models/Forum"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	supportai "github.com/zhuchunshu/sforum/apps/api/app/Support/AI"
)

// NewAIReplyGenerator 组装回复生成器。API 与 worker 两条装配路径共用它，
// 避免两处对同一个生成器做出不同解释。orchestrator 可为 nil：那时回复退化为
// 单次补全（无工具），与工具能力引入前一致。imageReader 也可为 nil：那时
// 多模态输入整体关闭，回复保持纯文本。
func NewAIReplyGenerator(
	pool *pgxpool.Pool,
	users identity.ActorStore,
	forumService *forum.Service,
	gateway *supportai.Gateway,
	orchestrator *supportai.Orchestrator,
	parameters aireply.ReplyParameterSource,
	imageReader aireply.ImageReader,
) *aireply.Generator {
	return aireply.NewGenerator(
		aireply.NewPostgresReader(pool),
		aireply.NewGatewayCompleter(gateway, orchestrator).WithImages(imageReader),
		aireply.NewBotCommentPoster(users, forumService),
		parameters,
	)
}
