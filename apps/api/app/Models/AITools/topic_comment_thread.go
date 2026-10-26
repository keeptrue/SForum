package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

const (
	// commentThreadDefaultLimit / commentThreadMaxLimit 约束一次读取的回复条数。
	// 上限比列表类工具低：楼中楼是用来补细节的，不是浏览入口，一次搬 30 条已经
	// 足够回答「这条追问有没有人回过」。
	commentThreadDefaultLimit = 10
	commentThreadMaxLimit     = 30
	// commentThreadMaxDepth 是允许展开的层数。depth=1 只读直接回复，depth=2 再
	// 多读一层。再深就不再是「谁回了他」，而是把整棵树搬进上下文。
	commentThreadMaxDepth = 2
)

// CommentThreadTool 读取一条评论下的回复楼：直接子回复，可选再展开一层。
//
// forum-topic-read 只能分页读平铺评论，回答「这条追问到底有没有人回过」要翻很多
// 页，翻漏了模型就会凭印象编。这个工具把「围绕某一楼的对话」作为一个整体交付。
//
// 可见性与公开详情页完全同源：ListCommentRepliesForViewer 已经内建了「评论不
// 存在或所属主题不可见，统一答不存在」的兜底，工具不再自己写一遍判定。
type CommentThreadTool struct {
	replies CommentThreadReader
	actors  identity.ActorStore
	links   SiteLinks
}

func NewCommentThreadTool(replies CommentThreadReader, actors identity.ActorStore, links SiteLinks) *CommentThreadTool {
	return &CommentThreadTool{replies: replies, actors: actors, links: links}
}

func (t *CommentThreadTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "forum-comment-thread",
		Description: "读取某条评论下的回复楼（直接回复，可选再展开一层）。回答「这条追问有没有人回过」" +
			"「后面那串讨论说了什么」时使用；读整页平铺评论用 forum-topic-read。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "commentId": {"type": "integer", "minimum": 1, "description": "要展开的评论 id（发言标签里的 #数字）"},
    "depth": {"type": "integer", "minimum": 1, "maximum": 2, "description": "展开层数：1 只读直接回复（默认），2 再多读一层"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 30, "description": "最多返回的回复条数，默认 10"}
  },
  "required": ["commentId"]
}`),
	}
}

type commentThreadArguments struct {
	CommentID int64 `json:"commentId"`
	Depth     int   `json:"depth"`
	Limit     int   `json:"limit"`
}

// threadEntry 是渲染前的展开结果。depth 是相对被展开评论的层数，不是库里的
// comments.depth：工具只承诺「一层 / 两层」，不承诺复刻存储的绝对深度。
type threadEntry struct {
	comment forum.Comment
	depth   int
}

func (t *CommentThreadTool) Invoke(ctx context.Context, call supportai.ToolCall, env supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.replies == nil {
		return supportai.ToolResult{Content: "回复楼读取在当前部署不可用。", IsError: true}, nil
	}
	var args commentThreadArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	if args.CommentID <= 0 {
		return supportai.ToolResult{Content: "commentId 必须是正整数。", IsError: true}, nil
	}
	depth := args.Depth
	if depth <= 0 {
		depth = 1
	}
	if depth > commentThreadMaxDepth {
		depth = commentThreadMaxDepth
	}
	limit := args.Limit
	if limit <= 0 {
		limit = commentThreadDefaultLimit
	}
	if limit > commentThreadMaxLimit {
		limit = commentThreadMaxLimit
	}
	viewer, err := viewerActor(ctx, t.actors, env)
	if err != nil {
		return supportai.ToolResult{}, err
	}
	direct, err := t.replies.ListCommentRepliesForViewer(ctx, args.CommentID, viewer)
	if err != nil {
		if errors.Is(err, forum.ErrCommentNotFound) || errors.Is(err, forum.ErrTopicNotFound) {
			return supportai.ToolResult{Content: "评论不存在或不可见。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}
	if len(direct) == 0 {
		return supportai.ToolResult{Content: fmt.Sprintf("评论 #%d 下还没有回复。", args.CommentID)}, nil
	}
	// 主题 id 从子回复上取：它们都挂在与父评论同一个主题下，而父评论本身不经这
	// 条路径返回。取不到就退化成无锚点的行，而不是编一个链接。
	topicID := direct[0].TopicID
	entries, truncated, err := t.collect(ctx, direct, viewer, depth, limit)
	if err != nil {
		return supportai.ToolResult{}, err
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "评论 #%d 的回复（%d 条%s）：\n", args.CommentID, len(entries), commentThreadScope(depth))
	for _, entry := range entries {
		builder.WriteString(t.renderEntry(ctx, entry, topicID))
	}
	if truncated {
		builder.WriteString("（还有更多回复未列出：本次已到 limit 上限）")
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}

// collect 按广度展开回复并受 limit 总条数约束。
//
// 只有直接回复数大于零的节点才会触发一次额外查询（ReplyCount 来自存储层），
// 因此最多调用 limit 次，最坏情况也在一个工具预算之内。
func (t *CommentThreadTool) collect(ctx context.Context, direct []forum.Comment, viewer identity.Actor, depth, limit int) ([]threadEntry, bool, error) {
	entries := make([]threadEntry, 0, limit)
	truncated := false
	for _, reply := range direct {
		if len(entries) >= limit {
			truncated = true
			break
		}
		entries = append(entries, threadEntry{comment: reply, depth: 1})
		if depth < commentThreadMaxDepth || reply.ReplyCount <= 0 {
			continue
		}
		children, err := t.replies.ListCommentRepliesForViewer(ctx, reply.ID, viewer)
		if err != nil {
			// 展开子层时的「不存在」只说明这一楼在本次读取过程中消失或被隐藏，
			// 不该让整次工具调用失败；内部故障仍然向上抛，由编排层记账。
			if errors.Is(err, forum.ErrCommentNotFound) || errors.Is(err, forum.ErrTopicNotFound) {
				continue
			}
			return nil, false, err
		}
		for _, child := range children {
			if len(entries) >= limit {
				truncated = true
				break
			}
			entries = append(entries, threadEntry{comment: child, depth: 2})
		}
	}
	return entries, truncated, nil
}

func (t *CommentThreadTool) renderEntry(ctx context.Context, entry threadEntry, topicID int64) string {
	name := "匿名"
	if entry.comment.Author != nil {
		name = authorName(entry.comment.Author.Username, entry.comment.Author.DisplayName)
	}
	prefix := "-"
	if entry.depth >= 2 {
		prefix = "  ↳"
	}
	line := fmt.Sprintf("%s %s（#%d）：%s", prefix, name, entry.comment.ID,
		clip(entry.comment.Content.PlainText, topicCommentRunes))
	if link := t.links.Comment(ctx, topicID, entry.comment.ID); link != "" {
		line += " ｜ 链接：" + link
	}
	return line + "\n"
}

func commentThreadScope(depth int) string {
	if depth >= commentThreadMaxDepth {
		return "，展开两层"
	}
	return "，仅直接回复"
}
