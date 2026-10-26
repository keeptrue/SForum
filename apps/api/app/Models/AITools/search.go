package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// searchPageSize 是每次检索返回的条数。它固定而不是交给模型：一次工具调用的
// 成本必须可预测，模型能控制的是查询词与翻页。
const searchPageSize = 8

// searchExcerptRunes 是每条命中的摘要长度上限。
const searchExcerptRunes = 200

// SearchTool 用站内全文检索回答「站内有没有人聊过 X」。
//
// 它复用与公开搜索页完全相同的服务：同一套引擎、同一套可见性过滤（仅
// active/locked 的公开主题，且剔除引擎里的幽灵文档）。机器人因此不会检索到
// 访客搜不到的东西。
type SearchTool struct {
	searcher Searcher
	links    SiteLinks
}

func NewSearchTool(searcher Searcher, links SiteLinks) *SearchTool {
	return &SearchTool{searcher: searcher, links: links}
}

func (t *SearchTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "forum-search",
		Description: "在站内检索主题，返回标题、摘要与链接。需要站内事实、历史讨论或可引用的链接时使用；" +
			"不支持按关键词列出最新主题——那用 forum-topic-list。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "检索关键词，越具体越准"},
    "category": {"type": "string", "description": "可选：限定分类 slug"},
    "tag": {"type": "string", "description": "可选：限定标签 slug"},
    "page": {"type": "integer", "minimum": 1, "description": "页码，默认 1"}
  },
  "required": ["query"]
}`),
	}
}

type searchArguments struct {
	Query    string `json:"query"`
	Category string `json:"category"`
	Tag      string `json:"tag"`
	Page     int    `json:"page"`
}

func (t *SearchTool) Invoke(ctx context.Context, call supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.searcher == nil {
		return supportai.ToolResult{Content: "站内检索在当前部署不可用。", IsError: true}, nil
	}
	var args searchArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return supportai.ToolResult{Content: "query 不能为空。", IsError: true}, nil
	}
	page := args.Page
	if page < 1 {
		page = 1
	}
	result, err := t.searcher.Search(ctx, search.SearchInput{
		Query:        query,
		CategorySlug: strings.TrimSpace(args.Category),
		TagSlug:      strings.TrimSpace(args.Tag),
		Page:         page,
		PerPage:      searchPageSize,
	})
	if err != nil {
		if errors.Is(err, search.ErrEngineUnavailable) {
			return supportai.ToolResult{Content: "站内检索当前不可用，请基于已有信息回答。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}
	if len(result.Items) == 0 {
		return supportai.ToolResult{Content: "没有找到相关主题。可以换关键词，或直接回答。"}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "共 %d 条相关主题，本页 %d 条：\n", result.Total, len(result.Items))
	for _, item := range result.Items {
		fmt.Fprintf(&builder, "- 《%s》 链接：%s\n", clip(item.Title, 120), t.links.Topic(ctx, item.ID))
		meta := fmt.Sprintf("  分类：%s ｜ 评论 %d ｜ 浏览 %d", strings.TrimSpace(item.CategorySlug), item.CommentCount, item.ViewCount)
		builder.WriteString(meta + "\n")
		if excerpt := clip(item.Excerpt, searchExcerptRunes); excerpt != "" {
			builder.WriteString("  摘要：" + excerpt + "\n")
		}
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}
