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

// userProfileRecentTopics 是资料里带出的近期主题条数上限。
const userProfileRecentTopics = 5

// UserProfileTool 读取公开个人资料：加入时间、主题/评论数与近期主题。
//
// 它只走公开资料服务（profile.Service.GetPublicProfile），因此邮箱、登录方式、
// 管理标记这些非公开字段不会经由机器人泄露；不存在的用户回答「没有这个用户」，
// 不区分「不存在」与「不可见」。
type UserProfileTool struct {
	profiles ProfileReader
	links    SiteLinks
}

func NewUserProfileTool(profiles ProfileReader, links SiteLinks) *UserProfileTool {
	return &UserProfileTool{profiles: profiles, links: links}
}

func (t *UserProfileTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{
		Name: "user-profile-read",
		Description: "读取站内用户的公开资料：加入时间、主题与评论数量、近期主题。回答「这位用户是谁」" +
			"「他写过什么」这类问题时使用。username 用发言标签里的 @用户名；只有昵称时先用 user-search 换到用户名。",
		Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "username": {"type": "string", "description": "用户名（@ 后面的部分）"}
  },
  "required": ["username"]
}`),
	}
}

type userProfileArguments struct {
	Username string `json:"username"`
}

func (t *UserProfileTool) Invoke(ctx context.Context, call supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	if t == nil || t.profiles == nil {
		return supportai.ToolResult{Content: "用户资料在当前部署不可用。", IsError: true}, nil
	}
	var args userProfileArguments
	if err := decodeArguments(call.Arguments, &args); err != nil {
		return supportai.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	username := strings.TrimPrefix(strings.TrimSpace(args.Username), "@")
	if username == "" {
		return supportai.ToolResult{Content: "username 不能为空。", IsError: true}, nil
	}
	public, err := t.profiles.GetPublicProfile(ctx, username)
	if err != nil {
		if errors.Is(err, profile.ErrProfileNotFound) {
			return supportai.ToolResult{Content: "没有这个用户。", IsError: true}, nil
		}
		return supportai.ToolResult{}, err
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s（@%s）\n", authorName(public.Username, public.DisplayName), public.Username)
	fmt.Fprintf(&builder, "加入时间：%s ｜ 主题 %d ｜ 评论 %d\n", public.JoinedAt.UTC().Format("2006-01-02"), public.TopicCount, public.CommentCount)
	if link := t.links.User(ctx, public.Username); link != "" {
		builder.WriteString("主页：" + link + "\n")
	}
	if len(public.RecentTopics) > 0 {
		builder.WriteString("近期主题：\n")
		for index, topic := range public.RecentTopics {
			if index >= userProfileRecentTopics {
				break
			}
			fmt.Fprintf(&builder, "- 《%s》 链接：%s\n", clip(topic.Title, 120), t.links.Topic(ctx, topic.ID))
		}
	}
	return supportai.ToolResult{Content: strings.TrimRight(builder.String(), "\n")}, nil
}
