package forum

import (
	"context"
	"errors"
)

// Citation 是 AI 产出评论的一条依据。它与 aireply.Citation 是同一份数据的不同
// 消费面：aireply 侧负责「校验并写入」，Forum 侧负责「公开读取」。
//
// 为什么在 Forum 里再定义一次而不是 import aireply：Forum 是更底层的领域包，
// 让论坛读路径依赖 AI 回复包会把依赖方向倒过来（AI 已经依赖 Forum）。两个结构
// 的 JSON 字段名必须保持一致——那是同一个对前端（T6）的契约：
// topicId / commentId / excerpt，全部 omitempty。
type Citation struct {
	TopicID   int64  `json:"topicId,omitempty"`
	CommentID int64  `json:"commentId,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
}

// CitationList 是一条评论作为 AI 产物时留下的全部活动记录。
//
// 名字与路径仍是 citations（`GET /comments/{id}/citations` 是已发布的契约），
// 但它现在同时承载工具调用：一个端点的两个分区，而不是两条路由。理由是一页
// 20 条评论已经要发 20 个请求（前端有 4 并发限流），再加一条端点会翻倍；而
// 「这条回复依据了什么、调过什么」在读者眼里本来就是同一个问题。
//
// ToolCalls 用 omitempty：零工具调用时字段不出现，与「这个能力没装配」对读者
// 是同一个结论，前端也据此不渲染「工具调用」分区。
type CitationList struct {
	Items     []Citation      `json:"items"`
	ToolCalls []ToolCallEntry `json:"toolCalls,omitempty"`
}

// CitationReader 读取某条评论作为 AI 产出时留下的依据。
//
// 它由 aireply 的引用存储提供（PostgresCitationStore），经装配注入。未装配时
// 端点返回空列表而不是错误：引用是增强面，缺它不该让一个公开读端点 500。
type CitationReader interface {
	ListBySourceComment(ctx context.Context, sourceCommentID int64) ([]Citation, error)
}

// CitationService 是 AI 依据读取的聚焦协作者。
//
// 它刻意**不是** *Service 上的接收者方法集：Forum 的 Service 已经在
// receiverMethodCaps 上限（77），而 AGENTS.md 要求「一个 Service 跨多个独立能力
// 时必须变成聚焦协作者的兼容门面」。本文件是那个协作者，形状与
// TaxonomyService / EditableSourceService / ReferenceSelector 一致：
// 持有 *Service、只通过 s.service.store 取数、不往 Service 上加方法或字段。
type CitationService struct {
	service *Service
	// reader 可选：nil 时该方法仍会过可见性判定（不成为枚举豁免口），然后
	// 返回空列表。
	reader CitationReader
	// toolCalls 可选：nil 时「工具调用」分区整体缺席，与「这次没调工具」在
	// 公开载荷上是同一个形状（字段不出现）。
	toolCalls ToolCallReader
}

func NewCitationService(service *Service) *CitationService {
	return &CitationService{service: service}
}

// WithReader 注入引用读取能力。返回同一协作者以便链式装配。
func (s *CitationService) WithReader(reader CitationReader) *CitationService {
	if s == nil {
		return s
	}
	s.reader = reader
	return s
}

// WithToolCalls 注入工具调用读取能力。返回同一协作者以便链式装配。
//
// 两个 reader 必须挂在同一个服务上：它们共用同一次可见性判定。分成两个服务会
// 让「依据列表看得到、工具列表看不到」这种自相矛盾成为可能。
func (s *CitationService) WithToolCalls(reader ToolCallReader) *CitationService {
	if s == nil {
		return s
	}
	s.toolCalls = reader
	return s
}

// CommentCitations 返回一条评论的依据列表。
//
// 可见性判定**完全复用评论本身那套**（与 ListCommentRepliesForViewer 同一个
// 顺序）：先经评论摘要追溯到所属主题，再走 GetTopic 的公开可见性规则。任何
// 一步不可见都返回 ErrCommentNotFound——**不区分「不存在」与「不可见」**，
// 因为这条区分会让 commentID 枚举成为探测隐藏主题的手段。
//
// 关于 viewer：本方法的可见性**只由主题级公开判定决定**，与 viewer 的登录态
// 无关——`store.GetTopic` 的谓词是 `status IN (active,locked) AND deleted_at IS
// NULL AND categories.visibility='public'`，它在匿名与登录两种情形下返回同一
// 结论。因此本方法不接收 viewer 参数（接收却不用是代码味道，读代码的人会以为
// 这里本该有身份判断）。身份只在控制器层用于 `requireGuestRead` 与
// `ReadActor`（与 replies 同族），不参与引用可见性。
//
// 若将来出现「登录成员能看到 AI 在私密板块的依据」这种需求，那必须给
// 主题级判定本身加 actor 维度（与公开读同一条改造），而不是在这里补一个分支。
//
// 工具调用与依据共用这一次可见性判定：两者都是「这条 AI 产物的附属事实」，
// 分开判会让两个分区对同一条评论给出不同结论。
func (s *CitationService) CommentCitations(ctx context.Context, commentID int64) (CitationList, error) {
	if commentID <= 0 {
		return CitationList{}, ErrCommentNotFound
	}
	if s == nil || s.service == nil {
		return CitationList{}, ErrTopicNotFound
	}
	// 可见性先于读取：即使没有 reader，也必须过这一关，否则这个端点会成为
	// 「用 commentID 探测隐藏主题」的绕道。
	if _, err := s.visibleCommentTopic(ctx, commentID); err != nil {
		return CitationList{}, err
	}
	// 工具调用先读：它记录「这次回答做了什么」，依据记录「它引用了什么」。两者
	// 读失败的处理刻意不同——工具调用读失败只丢那个分区（它是增强面），依据读
	// 失败照旧冒泡（它一直是这个端点的主体）。
	toolCalls := s.commentToolCalls(ctx, commentID)
	if s.reader == nil {
		return CitationList{Items: []Citation{}, ToolCalls: toolCalls}, nil
	}
	items, err := s.reader.ListBySourceComment(ctx, commentID)
	if err != nil {
		return CitationList{}, err
	}
	if items == nil {
		return CitationList{Items: []Citation{}, ToolCalls: toolCalls}, nil
	}
	return CitationList{Items: items, ToolCalls: toolCalls}, nil
}

// commentToolCalls 读取并过滤公开可投影的工具调用。
//
// 过滤发生在服务层而不是存储层：同一条记录对运营者是「模型撞了白名单」的证据，
// 对读者只是噪声。两个投影共用一张表，边界画在服务层才说得清。
func (s *CitationService) commentToolCalls(ctx context.Context, commentID int64) []ToolCallEntry {
	if s == nil || s.toolCalls == nil {
		return nil
	}
	items, err := s.toolCalls.ListBySourceComment(ctx, commentID)
	if err != nil {
		return nil
	}
	return publicToolCalls(items)
}

// visibleCommentTopic 是引用读取的唯一可见性入口：评论存在、其所属主题对访客
// 公开可见。
//
// 它与 ListCommentRepliesForViewer 的前两段同形——两套判定必然分叉成「回复列表
// 看不到、依据列表看得到」这种自相矛盾，所以刻意复用同一条 store 路径。
func (s *CitationService) visibleCommentTopic(ctx context.Context, commentID int64) (CommentSummary, error) {
	summary, err := s.service.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		// 归一为评论不存在：GetCommentSummary 对不存在的行返回 ErrCommentNotFound，
		// 对真实故障返回别的错误。前者归一，后者照旧冒泡（那是 500，不是 404）。
		if errors.Is(err, ErrCommentNotFound) {
			return CommentSummary{}, ErrCommentNotFound
		}
		return CommentSummary{}, err
	}
	if _, err := s.service.store.GetTopic(ctx, summary.TopicID); err != nil {
		if errors.Is(err, ErrTopicNotFound) {
			// 主题不可见对调用方的结论必须是「这条评论不存在」：两种错误文本不同
			// 会让枚举者据此区分「评论存在但主题隐藏」，而 API 响应体里的 message
			// 就是这个错误字符串。这正是 replies 端点要防的那条泄漏。
			return CommentSummary{}, ErrCommentNotFound
		}
		return CommentSummary{}, err
	}
	return summary, nil
}
