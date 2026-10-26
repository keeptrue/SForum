package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

const (
	// selfHistoryDefaultLimit / selfHistoryMaxLimit 约束一次回看的条数。上限 10
	// 与提示词窗口（RecentCommentWindow）同量级：这个工具是用来补窗口的，不是
	// 用来把整段历史搬进上下文的。
	selfHistoryDefaultLimit = 5
	selfHistoryMaxLimit     = 10
	// botHistoryPageSize 是适配器每页取多少条。它只决定「一次查多少行」，与工具
	// 返回条数无关。
	botHistoryPageSize = 50
	// botHistoryMaxPages 是适配器最多向前回溯的页数。末页由 Total 直接算出，
	// 这个上限只兜 Total 陈旧（flat 的总数来自 topics.comment_count，可短暂偏
	// 大）以及服务端深翻上限，绝不等于「扫全表」。
	botHistoryMaxPages = 5
)

// SelfHistoryTool 读取机器人自己在当前话题里已经发布过的回复。
//
// 提示词只带最近 RecentCommentWindow 条发言，超窗即丢，于是机器人会重复回答
// 同一件事、或者推翻自己刚给出的结论。这个工具让它按需回看「我说过什么」。
//
// 授权面只有一条：话题本身必须公开可见（ListBotComments 走公开评论列表，同一条
// GetTopic 谓词）。机器人身份（ToolContext.BotUserID）不参与可见性判定，只用来
// 在结果里认出「这一行是我说的」。
type SelfHistoryTool struct {
	history BotHistoryReader
	links   SiteLinks
}

func NewSelfHistoryTool(history BotHistoryReader, links SiteLinks) *SelfHistoryTool {
	return &SelfHistoryTool{history: history, links: links}
}

func (t *SelfHistoryTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "ai-self-history",
		Description: "读取你自己在当前话题里已经发布过的回复（新的在前）。要确认「这件事我是不是已经答过」" +
			"「我上一轮给的是什么结论」时使用，避免重复回答或自相矛盾。默认读当前话题。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "topicId": {"type": "integer", "minimum": 1, "description": "话题 id，默认当前话题"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 10, "description": "最多返回的条数，默认 5"}
  }
}`),
	}
}

type selfHistoryArguments struct {
	TopicID int64 `json:"topicId"`
	Limit   int   `json:"limit"`
}

func (t *SelfHistoryTool) Invoke(ctx context.Context, call supportai.ToolCall, env supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.history == nil {
		return supportai.ToolResult{Content: "历史发言读取在当前部署不可用。", IsError: true}, nil
	}
	var args selfHistoryArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	topicID := args.TopicID
	if topicID <= 0 {
		topicID = env.TopicID
	}
	if topicID <= 0 {
		return supportai.ToolResult{Content: "topicId 不能为空：当前不在具体话题里，请显式给出话题 id。", IsError: true}, nil
	}
	// 身份来自 ToolContext，不来自参数：模型不能通过参数声称「我是另一个机器人」。
	botUserID := env.BotUserID
	if botUserID <= 0 {
		return supportai.ToolResult{Content: "当前部署没有提供机器人身份，无法读取历史发言。", IsError: true}, nil
	}
	limit := args.Limit
	if limit <= 0 {
		limit = selfHistoryDefaultLimit
	}
	if limit > selfHistoryMaxLimit {
		limit = selfHistoryMaxLimit
	}
	comments, err := t.history.ListBotComments(ctx, topicID, botUserID, limit)
	if err != nil {
		if errors.Is(err, forum.ErrTopicNotFound) || errors.Is(err, forum.ErrCommentNotFound) {
			return supportai.ToolResult{Content: "话题不存在或不可见。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}
	if len(comments) == 0 {
		return supportai.ToolResult{Content: "我在这个话题里还没有发言记录。"}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "我在话题 #%d 的历史发言（%d 条，新的在前）：\n", topicID, len(comments))
	// 适配器按时间升序返回，这里倒过来：模型判断「我是不是已经答过」，要看的
	// 是最近那几条，不是这个话题最早的那几条。
	for index := len(comments) - 1; index >= 0; index-- {
		comment := comments[index]
		fmt.Fprintf(&builder, "- #%d（%s）：%s", comment.ID,
			comment.CreatedAt.UTC().Format("2006-01-02 15:04"),
			clip(comment.Content.PlainText, topicCommentRunes))
		if link := t.links.Comment(ctx, topicID, comment.ID); link != "" {
			builder.WriteString(" ｜ 链接：" + link)
		}
		builder.WriteString("\n")
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}

// CommentBotHistory 用公开评论列表（flat 视图）实现 BotHistoryReader。
//
// flat 视图按 created_at ASC, id ASC 排序，所以「最近」在末页：先用 Total 算出
// 末页，再向前回溯，绝不拉全表。
//
// 它只经 ListComments 读——与访客可见的是同一份内容。viewer 传零值 Actor（匿名），
// 这是比任何登录身份都**窄**的一档：软删墓碑按匿名策略被隐藏，所以这个适配器只
// 可能少给、不会多给。机器人 id 只用来在结果里挑出「我自己的发言」，不参与任何
// 可见性判定（硬约束 4）。
type CommentBotHistory struct {
	comments CommentReader
}

// NewBotHistoryReader 用评论读取器构造历史发言适配器。comments 为 nil 时返回
// 接口的 nil（不是 typed-nil），登记表据此不注册 ai-self-history。
func NewBotHistoryReader(comments CommentReader) BotHistoryReader {
	if comments == nil {
		return nil
	}
	return CommentBotHistory{comments: comments}
}

func (h CommentBotHistory) ListBotComments(ctx context.Context, topicID, botUserID int64, limit int) ([]forum.Comment, error) {
	if topicID <= 0 || botUserID <= 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = selfHistoryDefaultLimit
	}
	if limit > selfHistoryMaxLimit {
		limit = selfHistoryMaxLimit
	}
	first, err := h.comments.ListComments(ctx, forum.CommentListInput{
		TopicID: topicID,
		View:    "flat",
		Page:    1,
		PerPage: botHistoryPageSize,
	})
	if err != nil {
		return nil, err
	}
	if first.Total <= 0 && len(first.Items) == 0 {
		return nil, nil
	}
	lastPage := 1
	if first.Total > 0 {
		lastPage = int((first.Total + int64(botHistoryPageSize) - 1) / int64(botHistoryPageSize))
	}
	if lastPage < 1 {
		lastPage = 1
	}
	// 服务端对深翻页有硬上限（超过即按末页处理）。这里先按同一条边界收敛，否则
	// 第 5000 页会被服务端当第 200 页返回，而我们以为自己在读更后面的页。
	if lastPage > forum.MaxTopicPage {
		lastPage = forum.MaxTopicPage
	}
	mine := make([]forum.Comment, 0, limit)
	pagesWalked := 0
	for page := lastPage; page >= 1 && len(mine) < limit && pagesWalked < botHistoryMaxPages; page-- {
		items := first.Items
		if page != 1 {
			listed, err := h.comments.ListComments(ctx, forum.CommentListInput{
				TopicID: topicID,
				View:    "flat",
				Page:    page,
				PerPage: botHistoryPageSize,
			})
			if err != nil {
				return nil, err
			}
			items = listed.Items
		}
		pagesWalked++
		for _, item := range items {
			if item.AuthorUserID == botUserID {
				mine = append(mine, item)
			}
		}
	}
	sort.SliceStable(mine, func(i, j int) bool {
		if mine[i].CreatedAt.Equal(mine[j].CreatedAt) {
			return mine[i].ID < mine[j].ID
		}
		return mine[i].CreatedAt.Before(mine[j].CreatedAt)
	})
	if len(mine) > limit {
		mine = mine[len(mine)-limit:]
	}
	return mine, nil
}
