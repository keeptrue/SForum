// Package aitools 实现 Core 内置的只读 AI 工具。
//
// 这些工具只做一件事：把「访客本来就能看到的站内内容」按需交给模型，并带上
// 可点击的链接。它们不写任何数据、不触碰审核状态、也不引入新的可见性规则——
// 每个工具都复用对应领域服务的公开读取路径，因此「机器人能看到什么」与
// 「访客能看到什么」由同一套代码决定。
//
// 工具的返回值会进入提示词并可能被复述，因此两条纪律贯穿全包：
//   - 只返回公开可见的内容，越权内容回答「不存在」而不是「无权限」；
//   - 输出按 rune 截断，单个结果保持在几千字节内，成本可预测。
package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// Searcher 是站内检索的最小面，由 Support/Search 的服务实现。
type Searcher interface {
	Search(ctx context.Context, input search.SearchInput) (search.SearchResult, error)
}

// TopicReader 读取公开主题详情，由 forum.Service 实现。
type TopicReader interface {
	GetTopic(ctx context.Context, topicID int64) (forum.TopicDetail, error)
}

// CommentReader 读取公开评论列表，由 forum.Service 实现。
type CommentReader interface {
	ListComments(ctx context.Context, input forum.CommentListInput) (forum.CommentList, error)
}

// CommentThreadReader 读取某条评论的直接子回复，由 forum.Service 实现。
//
// 它刻意复用既有的 ListCommentRepliesForViewer，而不是在工具侧另写一套「按
// parent_comment_id 过滤」：回复的可见性兜底（评论不存在、所属主题不可见，两者
// 统一答「不存在」）已经在那里实现过一次，第二份就是第二套可见性判定。该方法
// 返回的正是直接子回复（WHERE comments.parent_comment_id = $1），多层展开由
// forum-comment-thread 自己递归，工具因此不会把「深度」的语义交给存储层。
type CommentThreadReader interface {
	ListCommentRepliesForViewer(ctx context.Context, commentID int64, viewer identity.Actor) ([]forum.Comment, error)
}

// AttachmentReader 以某个身份打开附件内容，由 attachments.Service 实现。
//
// 签名直接引用 attachments 的类型，而不是在工具侧另造一个「附件」结构：读附件
// 必须走与公开下载完全相同的授权链（引用快照 + 主题可见性 + 访客登录策略），
// 在工具里重新描述一遍只会得到一份会走偏的副本。
type AttachmentReader interface {
	OpenContent(ctx context.Context, actor identity.Actor, publicID string) (attachments.Attachment, io.ReadCloser, error)
}

// BotHistoryReader 读取某个机器人账号在指定话题里的历史发言。
//
// 它回答的是「我在这个话题里已经说过什么」：提示词只带最近
// RecentCommentWindow 条发言，超窗即丢，机器人因此会重复回答、甚至推翻自己
// 刚给出的结论。
type BotHistoryReader interface {
	ListBotComments(ctx context.Context, topicID, botUserID int64, limit int) ([]forum.Comment, error)
}

// TopicLister 读取公开主题列表，由 forum.Service 实现。
type TopicLister interface {
	ListTopics(ctx context.Context, input forum.TopicListInput) (forum.TopicList, error)
}

// ProfileReader 读取公开个人资料，由 profile.Service 实现。
type ProfileReader interface {
	GetPublicProfile(ctx context.Context, username string) (profile.PublicProfile, error)
}

// UserSearcher 按关键词检索可公开访问的活跃用户，由 profile.Service 实现。
type UserSearcher interface {
	SearchPublicUsers(ctx context.Context, input profile.SearchUsersInput) (profile.UserSearchResult, error)
}

// SiteLinks 把站内路径拼成链接。SiteURL 为空时退回相对路径：站内评论里的相对
// 链接同样可点，绝对地址则更适合被复制到站外。
type SiteLinks struct {
	SiteURL func(ctx context.Context) string
}

func (l SiteLinks) Topic(ctx context.Context, topicID int64) string {
	return l.base(ctx) + "/t/" + strconv.FormatInt(topicID, 10)
}

func (l SiteLinks) User(ctx context.Context, username string) string {
	name := strings.TrimSpace(username)
	if name == "" {
		return ""
	}
	return l.base(ctx) + "/u/" + url.PathEscape(name)
}

// Comment 把评论锚点拼到所属主题的链接后面。锚点格式与公开详情页一致
// （`#comment-{id}`），模型复述出来的链接因此能直接落到那一楼；拿不到主题 id
// 时返回空串，而不是拼一个指向 `/t/0` 的假链接。
func (l SiteLinks) Comment(ctx context.Context, topicID, commentID int64) string {
	if topicID <= 0 || commentID <= 0 {
		return ""
	}
	return l.Topic(ctx, topicID) + "#comment-" + strconv.FormatInt(commentID, 10)
}

func (l SiteLinks) base(ctx context.Context) string {
	if l.SiteURL == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(l.SiteURL(ctx)), "/")
}

// Deps 汇总内置工具的依赖。任一依赖缺失时对应工具不注册：工具不存在比
// 「工具存在但永远失败」更诚实，模型不会把一次调用浪费在必然失败的路径上。
type Deps struct {
	Search   Searcher
	Topics   TopicReader
	Comments CommentReader
	Lister   TopicLister
	Profiles ProfileReader
	Users    UserSearcher
	// Replies 读取评论的直接子回复（forum-comment-thread）。
	Replies CommentThreadReader
	// Attachments 打开附件内容（forum-attachment-read）。
	Attachments AttachmentReader
	// BotHistory 读取机器人自己的历史发言（ai-self-history）。
	BotHistory BotHistoryReader
	// Actors 把提问者 id 换成 Actor。为 nil 时按匿名身份取数——匿名是比任何
	// 登录用户都窄的一档，因此缺它只会少给内容，不会多给。
	Actors  identity.ActorStore
	SiteURL func(ctx context.Context) string
	Clock   func() time.Time
}

// NewBuiltinRegistry 注册 Core 内置的九个只读工具。
func NewBuiltinRegistry(deps Deps) (*supportai.ToolRegistry, error) {
	registry := supportai.NewToolRegistry()
	links := SiteLinks{SiteURL: deps.SiteURL}
	register := func(tool supportai.Tool, enabled bool) error {
		if !enabled {
			return nil
		}
		return registry.Register(tool)
	}
	if err := register(NewSearchTool(deps.Search, links), deps.Search != nil); err != nil {
		return nil, err
	}
	if err := register(NewTopicReadTool(deps.Topics, deps.Comments, links), deps.Topics != nil && deps.Comments != nil); err != nil {
		return nil, err
	}
	if err := register(NewTopicListTool(deps.Lister, links), deps.Lister != nil); err != nil {
		return nil, err
	}
	if err := register(NewUserProfileTool(deps.Profiles, links), deps.Profiles != nil); err != nil {
		return nil, err
	}
	if err := register(NewUserSearchTool(deps.Users, links), deps.Users != nil); err != nil {
		return nil, err
	}
	if err := register(NewCommentThreadTool(deps.Replies, deps.Actors, links), deps.Replies != nil); err != nil {
		return nil, err
	}
	if err := register(NewAttachmentReadTool(deps.Attachments, deps.Actors), deps.Attachments != nil); err != nil {
		return nil, err
	}
	if err := register(NewSelfHistoryTool(deps.BotHistory, links), deps.BotHistory != nil); err != nil {
		return nil, err
	}
	if err := register(NewTimeTool(deps.Clock), true); err != nil {
		return nil, err
	}
	return registry, nil
}

// decodeArguments 解析模型给出的参数串。参数不是合法 JSON 对象时返回给模型看的
// 说明，而不是让整次回复失败。
func decodeArguments(raw string, target any) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), target); err != nil {
		return errors.New("参数无法解析：需要 JSON 对象，且字段类型要匹配")
	}
	return nil
}

// clip 按 rune 截断，避免在多字节字符中间切断。
func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// authorName 取公开显示名，缺省回落到用户名。
func authorName(username, displayName string) string {
	if name := strings.TrimSpace(displayName); name != "" {
		return name
	}
	return strings.TrimSpace(username)
}

// viewerActor 把「谁在提问」换成领域层认的 Actor。
//
// 可见性判定只认 ToolContext.ViewerUserID（硬约束 4）：机器人是系统账号，用它的
// 身份取数等于给机器人开一份比提问者更宽的视野。ViewerUserID 为零表示匿名，与
// 公开读走同一档；账号已被删除同样回落到匿名（比任何登录身份都窄），而其它加载
// 失败一律向上抛——静默降级会把一次基础设施故障变成一次「看起来读到空」的回复。
func viewerActor(ctx context.Context, actors identity.ActorStore, env supportai.ToolContext) (identity.Actor, error) {
	if env.ViewerUserID <= 0 || actors == nil {
		return identity.Actor{}, nil
	}
	actor, err := actors.LoadActor(ctx, env.ViewerUserID)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return identity.Actor{}, nil
		}
		return identity.Actor{}, err
	}
	return actor, nil
}

// pageDefaults 归一化页码与每页条数：页码至少 1，每页条数落在 1..max 内。
func pageDefaults(page, perPage, fallback, max int) (int, int) {
	if page < 1 {
		page = 1
	}
	if perPage <= 0 {
		perPage = fallback
	}
	if perPage > max {
		perPage = max
	}
	return page, perPage
}
