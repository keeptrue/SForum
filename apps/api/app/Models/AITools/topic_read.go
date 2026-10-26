package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

const (
	// topicBodyRunes 是主楼正文进入工具结果的长度上限。
	topicBodyRunes = 4_000
	// topicCommentRunes 是单条评论进入工具结果的长度上限。
	topicCommentRunes = 600
	// topicReadPerPage 是每次读取的评论条数上限。
	topicReadPerPage = 50
	// topicReadDefaultPerPage 是默认读取的评论条数。
	topicReadDefaultPerPage = 20
)

// TopicReadTool 读取一个公开主题的主楼正文与分页评论。
//
// 它复用与帖子详情页相同的读取路径：主题可见性（active/locked + 公开分类）由
// forum.Service.GetTopic 判定，评论列表复用同一可见性规则，因此「机器人读到的」
// 与「访客打开的」是同一份内容。不可见主题统一回答「不存在」，不泄露存在性。
type TopicReadTool struct {
	topics   TopicReader
	comments CommentReader
	links    SiteLinks
}

func NewTopicReadTool(topics TopicReader, comments CommentReader, links SiteLinks) *TopicReadTool {
	return &TopicReadTool{topics: topics, comments: comments, links: links}
}

func (t *TopicReadTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "forum-topic-read",
		Description: "读取一个主题的主楼正文与评论（分页）。需要了解帖子内容、翻看更早的讨论或核对细节时使用。" +
			"当前对话所在主题的主题 id 通常已在上下文中给出。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "topicId": {"type": "integer", "description": "主题 id"},
    "page": {"type": "integer", "minimum": 1, "description": "评论页码，默认 1"},
    "perPage": {"type": "integer", "minimum": 1, "maximum": 50, "description": "每页评论条数，默认 20"}
  },
  "required": ["topicId"]
}`),
	}
}

type topicReadArguments struct {
	TopicID int64 `json:"topicId"`
	Page    int   `json:"page"`
	PerPage int   `json:"perPage"`
}

func (t *TopicReadTool) Invoke(ctx context.Context, call supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.topics == nil || t.comments == nil {
		return supportai.ToolResult{Content: "主题读取在当前部署不可用。", IsError: true}, nil
	}
	var args topicReadArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if args.TopicID <= 0 {
		return supportai.ToolResult{Content: "topicId 必须是正整数。", IsError: true}, nil
	}
	topic, err := t.topics.GetTopic(ctx, args.TopicID)
	if err != nil {
		if errors.Is(err, forum.ErrTopicNotFound) {
			return supportai.ToolResult{Content: "主题不存在或不可见。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}
	page, perPage := pageDefaults(args.Page, args.PerPage, topicReadDefaultPerPage, topicReadPerPage)
	comments, err := t.comments.ListComments(ctx, forum.CommentListInput{
		TopicID: args.TopicID,
		View:    "flat",
		Page:    page,
		PerPage: perPage,
	})
	if err != nil {
		if errors.Is(err, forum.ErrTopicNotFound) {
			return supportai.ToolResult{Content: "主题不存在或不可见。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "主题 #%d《%s》\n", topic.ID, clip(topic.Title, 120))
	fmt.Fprintf(&builder, "分类：%s ｜ 状态：%s ｜ 评论 %d ｜ 浏览 %d ｜ 链接：%s\n",
		strings.TrimSpace(topic.CategorySlug), strings.TrimSpace(topic.Status), topic.CommentCount, topic.ViewCount, t.links.Topic(ctx, topic.ID))
	if topic.Author != nil {
		fmt.Fprintf(&builder, "作者：%s\n", authorName(topic.Author.Username, topic.Author.DisplayName))
	}
	if body := clip(topic.Content.PlainText, topicBodyRunes); body != "" {
		builder.WriteString("\n主楼正文：\n" + body + "\n")
	}
	if len(comments.Items) == 0 {
		builder.WriteString("\n还没有评论。")
		return supportai.ToolResult{Content: builder.String()}, nil
	}
	fmt.Fprintf(&builder, "\n评论（第 %d 页，共 %d 条%s）：\n", comments.Page, comments.Total, moreSuffix(comments.HasMore))
	for _, comment := range comments.Items {
		name := "匿名"
		if comment.Author != nil {
			name = authorName(comment.Author.Username, comment.Author.DisplayName)
		}
		fmt.Fprintf(&builder, "- %s（#%d）：%s\n", name, comment.ID, clip(comment.Content.PlainText, topicCommentRunes))
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}

func moreSuffix(hasMore bool) string {
	if hasMore {
		return "，还有更多"
	}
	return ""
}
