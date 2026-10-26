package aitools_test

import (
	"context"
	"strings"
	"testing"

	aitools "github.com/keeptrue/sforum/apps/api/app/Models/AITools"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 九个内置工具必须都能回答控制台的两个问题：「这个工具是干什么的」与「它的英文
// 说明有多长」。前者靠 DisplayDescriptions，后者受 MaxToolDescriptionBytes 约束——
// 展示文案是设置文档的一部分，失控的字段会把 GET /admin/ai/settings 撑大。
func TestBuiltinToolsDeclareConsoleDisplayDescriptions(t *testing.T) {
	registry, err := aitools.NewBuiltinRegistry(aitools.Deps{
		Search:      &fakeSearcher{},
		Topics:      &fakeTopics{},
		Comments:    &fakeComments{},
		Lister:      &fakeLister{},
		Profiles:    &fakeProfiles{},
		Users:       &fakeProfiles{},
		Replies:     &fakeThreadReplies{},
		Attachments: &fakeAttachmentReader{},
		BotHistory:  &fakeBotHistory{},
		Actors:      &fakeActors{},
		SiteURL:     testLinks().SiteURL,
	})
	if err != nil {
		t.Fatalf("registry failed: %v", err)
	}

	want := []string{
		"forum-search",
		"forum-topic-read",
		"forum-topic-list",
		"forum-comment-thread",
		"forum-attachment-read",
		"ai-self-history",
		"user-profile-read",
		"user-search",
		"time-now",
	}
	tools := registry.Tools()
	if len(tools) != len(want) {
		t.Fatalf("registered %d tools, want %d", len(tools), len(want))
	}
	byName := make(map[string]supportai.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Definition().Name] = tool
	}

	for _, name := range want {
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		provider, ok := tool.(supportai.ToolDisplayDescriptionProvider)
		if !ok {
			t.Fatalf("%s must implement ToolDisplayDescriptionProvider", name)
		}
		descriptions := provider.DisplayDescriptions()
		if len(descriptions) != 1 {
			t.Fatalf("%s declares locales %v, want exactly en-US", name, descriptions)
		}
		english := strings.TrimSpace(descriptions["en-US"])
		if english == "" {
			t.Fatalf("%s has an empty en-US display description", name)
		}
		if len(english) > supportai.MaxToolDescriptionBytes {
			t.Fatalf("%s en-US display description is %d bytes, cap is %d",
				name, len(english), supportai.MaxToolDescriptionBytes)
		}
		// 模型可见的那份说明仍然必须存在：展示文案不能顶替它。
		if strings.TrimSpace(tool.Definition().Description) == "" {
			t.Fatalf("%s has no model-facing description", name)
		}
		if tool.Definition().Description == english {
			t.Fatalf("%s model description must not be the console display text", name)
		}
	}
}

// plainTool 是一个只满足 Tool 契约、不声明展示文案的工具。
type plainTool struct{}

func (plainTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{Name: "plain-tool", Description: "A tool without console display text."}
}

func (plainTool) Invoke(_ context.Context, _ supportai.ToolCall, _ supportai.ToolContext) (supportai.ToolResult, error) {
	return supportai.ToolResult{}, nil
}

// 展示文案契约是可选的：没有实现它的工具仍然是合法工具，控制台只是少一行英文。
// 这条默认行为必须保持，否则第三方旧插件会被迫改代码才能继续登记。
func TestToolsWithoutDisplayDescriptionsKeepOptionalDefault(t *testing.T) {
	registry := supportai.NewToolRegistry()
	if err := registry.Register(plainTool{}); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if _, ok := any(plainTool{}).(supportai.ToolDisplayDescriptionProvider); ok {
		t.Fatal("plainTool must not satisfy the optional display contract")
	}
	available := registry.Available()
	if len(available) != 1 {
		t.Fatalf("available = %+v", available)
	}
	if available[0].DescriptionI18n != nil {
		t.Fatalf("a tool without display text must leave DescriptionI18n empty: %+v", available[0].DescriptionI18n)
	}
}
