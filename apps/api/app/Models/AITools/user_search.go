package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// userSearchLimit 是每次检索返回的条数上限。理由与 forum-search 相同：一次工具
// 调用的成本必须可预测，模型能控制的是关键词，不是条数。
const userSearchLimit = 8

// UserSearchTool 按昵称或用户名查找站内成员。
//
// 它补的是一个具体断点：提示词里的发言只带显示名，而 user-profile-read 需要
// @用户名。没有这一步，模型即使知道「有人叫张三」，也无从把它变成一个能查的账号。
//
// 它只走 profile.Service 的公开检索（活跃用户、用户名与昵称匹配、结果里没有邮箱或
// 状态），并且拒绝空关键词——空查询等于「列出全站成员」，那是目录导出，不是检索。
type UserSearchTool struct {
	users UserSearcher
	links SiteLinks
}

func NewUserSearchTool(users UserSearcher, links SiteLinks) *UserSearchTool {
	return &UserSearchTool{users: users, links: links}
}

func (t *UserSearchTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "user-search",
		Description: "按昵称或用户名查找站内成员，返回 @用户名与主页链接。只知道昵称、" +
			"或要确认某位成员是谁时先用它拿到用户名，再用 user-profile-read 读资料。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "昵称或用户名的关键词"}
  },
  "required": ["query"]
}`),
	}
}

type userSearchArguments struct {
	Query string `json:"query"`
}

func (t *UserSearchTool) Invoke(ctx context.Context, call supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.users == nil {
		return supportai.ToolResult{Content: "成员检索在当前部署不可用。", IsError: true}, nil
	}
	var args userSearchArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return supportai.ToolResult{Content: "query 不能为空：请给出昵称或用户名里的关键词。", IsError: true}, nil
	}
	result, err := t.users.SearchPublicUsers(ctx, profile.SearchUsersInput{Query: query, Limit: userSearchLimit})
	if err != nil {
		// 关键词本身非法（为空或过长）由服务层判定，这里翻成给模型看的话。
		if errors.Is(err, profile.ErrProfileInvalid) {
			return supportai.ToolResult{Content: "关键词不可用：请给出昵称或用户名里的关键词。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}
	if len(result.Items) == 0 {
		return supportai.ToolResult{Content: "没有找到匹配的成员。可以换关键词，或直接回答。"}, nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "找到 %d 位成员：\n", len(result.Items))
	for _, item := range result.Items {
		fmt.Fprintf(&builder, "- %s（@%s） 主页：%s\n",
			authorName(item.Username, item.DisplayName), item.Username, t.links.User(ctx, item.Username))
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}
