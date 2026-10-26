package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

const (
	// topicListDefaultPerPage / topicListMaxPerPage 约束榜单类回答的体积。
	topicListDefaultPerPage = 10
	topicListMaxPerPage     = 20
)

// TopicListTool 按「最新 / 活跃 / 热门」列出公开主题，可限定分类或标签。
//
// 关键词检索刻意不在这里：主题列表存储层不接受 ILIKE 全表扫描，站内搜索是唯一
// 的关键词入口（forum-search）。工具描述里写清楚这一点，模型才不会走错门。
type TopicListTool struct {
	lister TopicLister
	links  SiteLinks
}

func NewTopicListTool(lister TopicLister, links SiteLinks) *TopicListTool {
	return &TopicListTool{lister: lister, links: links}
}

func (t *TopicListTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "forum-topic-list",
		Description: "按排序列出站内公开主题（最新 / 活跃 / 热门），可限定分类或标签。" +
			"回答「最近有什么讨论」这类问题时使用；按关键词找内容用 forum-search。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "sort": {"type": "string", "enum": ["latest", "active", "hot"], "description": "排序方式，默认站点配置"},
    "category": {"type": "string", "description": "可选：分类 slug"},
    "tag": {"type": "string", "description": "可选：标签 slug"},
    "page": {"type": "integer", "minimum": 1, "description": "页码，默认 1"},
    "perPage": {"type": "integer", "minimum": 1, "maximum": 20, "description": "每页条数，默认 10"}
  }
}`),
	}
}

type topicListArguments struct {
	Sort     string `json:"sort"`
	Category string `json:"category"`
	Tag      string `json:"tag"`
	Page     int    `json:"page"`
	PerPage  int    `json:"perPage"`
}

func (t *TopicListTool) Invoke(ctx context.Context, call supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.lister == nil {
		return supportai.ToolResult{Content: "主题列表在当前部署不可用。", IsError: true}, nil
	}
	var args topicListArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	page, perPage := pageDefaults(args.Page, args.PerPage, topicListDefaultPerPage, topicListMaxPerPage)
	list, err := t.lister.ListTopics(ctx, forum.TopicListInput{
		Page:         page,
		PerPage:      perPage,
		CategorySlug: strings.TrimSpace(args.Category),
		TagSlug:      strings.TrimSpace(args.Tag),
		Sort:         strings.TrimSpace(args.Sort),
	})
	if err != nil {
		return supportai.ToolResult{}, err
	}
	if len(list.Items) == 0 {
		return supportai.ToolResult{Content: "这个条件下没有公开主题。"}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "共 %d 个主题，本页 %d 条（排序：%s）：\n", list.Total, len(list.Items), topicSortLabel(args.Sort))
	for _, item := range list.Items {
		flags := make([]string, 0, 2)
		if item.IsPinned {
			flags = append(flags, "置顶")
		}
		if strings.TrimSpace(item.Status) == "locked" {
			flags = append(flags, "已锁定")
		}
		line := fmt.Sprintf("- 《%s》 链接：%s ｜ 分类：%s ｜ 评论 %d ｜ 最后活动：%s",
			clip(item.Title, 120), t.links.Topic(ctx, item.ID), strings.TrimSpace(item.CategorySlug),
			item.CommentCount, item.LastActivityAt.UTC().Format("2006-01-02"))
		if len(flags) > 0 {
			line += " ｜ " + strings.Join(flags, "、")
		}
		builder.WriteString(line + "\n")
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}

func topicSortLabel(sort string) string {
	switch strings.TrimSpace(sort) {
	case "latest":
		return "最新"
	case "active":
		return "活跃"
	case "hot":
		return "热门"
	default:
		return "站点默认"
	}
}
