package ai_test

import (
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 有些网关不把思考放进 reasoning_content，而是把它和回答拼在同一个 content 里，
// 用标签分隔。正文原样发布出去就是同一个故障的第二种入口。
func TestParseOpenAIResponseStripsInlineThinkingBlock(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"<think>先看题目，再想一下</think>答案是 42。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":20}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "答案是 42。" {
		t.Fatalf("text = %q", response.Text)
	}
}

func TestParseOpenAIResponseStripsRepeatedInlineThinkingBlocks(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"<thinking>一</thinking>\n<reasoning>二</reasoning>\n回答。"},"finish_reason":"stop"}]}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "回答。" {
		t.Fatalf("text = %q", response.Text)
	}
}

// 输出预算被思考耗尽时标签永远等不到闭合。那一段整体都是思考，不是回答。
func TestParseOpenAIResponseDropsUnterminatedInlineThinking(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"<think>用户在问端口占用，我先看看"},"finish_reason":"length"}]}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "" {
		t.Fatalf("text = %q；未闭合的思考块必须整体丢弃", response.Text)
	}
}

// 标签只被当作「正文开头」的标记：出现在中间的可能是成员提问里的字面内容，
// 剥掉它等于篡改模型的回答。
func TestParseOpenAIResponseKeepsInlineTagInsideAnswer(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"可以这样写：<think>内容</think>，编辑器会把它当普通文本。"},"finish_reason":"stop"}]}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "可以这样写：<think>内容</think>，编辑器会把它当普通文本。" {
		t.Fatalf("text = %q", response.Text)
	}
}

func TestParseOpenAIResponseStripsInlineThinkingCaseInsensitively(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"<THINK>想一下</THINK>答案。"},"finish_reason":"stop"}]}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "答案。" {
		t.Fatalf("text = %q", response.Text)
	}
}

// anthropic 侧的 text block 同样可能内联思考标签（兼容层会把 thinking 拼进 text）。
func TestParseAnthropicResponseStripsInlineThinkingBlock(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"<thinking>先算一遍</thinking>结果是 3。"}],"stop_reason":"end_turn"}`)
	response, err := ai.ParseWireResponse(claudeProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "结果是 3。" {
		t.Fatalf("text = %q", response.Text)
	}
}
