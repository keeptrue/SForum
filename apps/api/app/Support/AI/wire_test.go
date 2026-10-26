package ai_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func deepseekProfile() ai.Profile {
	for _, profile := range ai.BuiltinProfiles() {
		if profile.ID == "deepseek" {
			return profile
		}
	}
	panic("deepseek preset missing")
}

func claudeProfile() ai.Profile {
	for _, profile := range ai.BuiltinProfiles() {
		if profile.Protocol == ai.ProtocolAnthropicMessages {
			return profile
		}
	}
	panic("anthropic preset missing")
}

func TestBuildWireRequestRequiresCredential(t *testing.T) {
	request := validRequest()
	if _, err := ai.BuildWireRequest(deepseekProfile(), "  ", request); !errors.Is(err, ai.ErrCredentialMissing) {
		t.Fatalf("expected ErrCredentialMissing, got %v", err)
	}
}

func TestBuildWireRequestPropagatesRequestViolations(t *testing.T) {
	request := validRequest()
	request.MaxTokens = ai.MaxCompletionTokens + 1
	if _, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", request); !errors.Is(err, ai.ErrRequestInvalid) {
		t.Fatalf("expected ErrRequestInvalid, got %v", err)
	}
}

func TestOpenAIChatRequestPlacesSystemFirstAndUsesBearerAuth(t *testing.T) {
	wire, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", validRequest())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if wire.Method != http.MethodPost {
		t.Fatalf("method = %s", wire.Method)
	}
	if wire.URL != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("url = %s", wire.URL)
	}
	if wire.Headers["Authorization"] != "Bearer sk-test" {
		t.Fatalf("authorization header = %q", wire.Headers["Authorization"])
	}
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v", payload.Messages)
	}
	if payload.Messages[1].Role != "user" || payload.MaxTokens != 256 {
		t.Fatalf("messages = %+v maxTokens=%d", payload.Messages, payload.MaxTokens)
	}
}

func TestOpenAICompatibleBaseURLKeepsVersionSegment(t *testing.T) {
	profile := deepseekProfile()
	profile.BaseURL = "https://api.openai.com/v1"
	wire, err := ai.BuildWireRequest(profile, "sk-test", validRequest())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if wire.URL != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("url = %s", wire.URL)
	}
}

func TestAnthropicRequestHoistsSystemAndUsesVersionHeader(t *testing.T) {
	wire, err := ai.BuildWireRequest(claudeProfile(), "sk-ant", validRequest())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if wire.URL != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("url = %s", wire.URL)
	}
	if wire.Headers["x-api-key"] != "sk-ant" || wire.Headers["anthropic-version"] != ai.AnthropicAPIVersion {
		t.Fatalf("headers = %+v", wire.Headers)
	}
	var payload struct {
		System   string `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(wire.Body, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.System != validRequest().System {
		t.Fatalf("system = %q", payload.System)
	}
	if len(payload.Messages) != 1 || payload.Messages[0].Content[0].Type != "text" {
		t.Fatalf("messages = %+v", payload.Messages)
	}
}

func TestSafeHeadersDropsCredentials(t *testing.T) {
	wire, err := ai.BuildWireRequest(claudeProfile(), "sk-ant", validRequest())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	safe := wire.SafeHeaders()
	if _, ok := safe["x-api-key"]; ok {
		t.Fatal("x-api-key must not survive SafeHeaders")
	}
	if safe["anthropic-version"] != ai.AnthropicAPIVersion {
		t.Fatalf("safe headers = %+v", safe)
	}
	openaiWire, err := ai.BuildWireRequest(deepseekProfile(), "sk-test", validRequest())
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if _, ok := openaiWire.SafeHeaders()["Authorization"]; ok {
		t.Fatal("Authorization must not survive SafeHeaders")
	}
}

func TestParseOpenAIChatResponseMapsUsageAndStopReason(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"length"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":5}}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "ok" || response.StopReason != ai.StopReasonLength {
		t.Fatalf("response = %+v", response)
	}
	if response.Usage.InputTokens != 12 || response.Usage.OutputTokens != 3 || response.Usage.CachedTokens != 5 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestParseAnthropicResponseConcatenatesTextBlocks(t *testing.T) {
	body := []byte(`{"content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"stop_reason":"max_tokens","usage":{"input_tokens":7,"output_tokens":2,"cache_read_input_tokens":1}}`)
	response, err := ai.ParseWireResponse(claudeProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "ab" || response.StopReason != ai.StopReasonLength {
		t.Fatalf("response = %+v", response)
	}
	if response.Usage.CachedTokens != 1 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestParseWireResponseSurfacesProviderError(t *testing.T) {
	body := []byte(`{"error":{"message":"invalid api key","type":"authentication_error"}}`)
	_, err := ai.ParseWireResponse(deepseekProfile(), 401, body)
	if !errors.Is(err, ai.ErrWire) {
		t.Fatalf("expected ErrWire, got %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should carry the status: %v", err)
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Fatalf("error must not leak credentials: %v", err)
	}
}

func TestParseWireResponseRejectsEmptyBody(t *testing.T) {
	if _, err := ai.ParseWireResponse(deepseekProfile(), 200, nil); !errors.Is(err, ai.ErrWire) {
		t.Fatalf("expected ErrWire, got %v", err)
	}
}

func TestUnknownFinishReasonDegradesToEnd(t *testing.T) {
	if got := ai.MapOpenAIFinishReason("something_new"); got != ai.StopReasonEnd {
		t.Fatalf("openai mapping = %q", got)
	}
	if got := ai.MapAnthropicStopReason("something_new"); got != ai.StopReasonEnd {
		t.Fatalf("anthropic mapping = %q", got)
	}
}

// 推理类模型会把答案写在 reasoning_content 而不是 content。那是「模型思考了但
// 没有作答」，不是「思维链就是答案」：正文必须保持为空，思考单独回传，否则调用方
// 会把思维链当成回答发布到社区。
func TestParseOpenAIResponseKeepsReasoningOutOfText(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"","reasoning_content":"收到"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "" {
		t.Fatalf("text = %q；思考正文不得成为回答", response.Text)
	}
	if response.Reasoning != "收到" {
		t.Fatalf("reasoning = %q", response.Reasoning)
	}
}

func TestParseOpenAIResponseKeepsReasoningAliasOutOfText(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"reasoning":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Reasoning != "ok" {
		t.Fatalf("reasoning = %q", response.Reasoning)
	}
}

// 两个字段都有内容时正文优先：推理过程不是给用户看的答案。
func TestParseOpenAIResponsePrefersContentOverReasoning(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"answer","reasoning_content":"thinking"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`)
	response, err := ai.ParseWireResponse(deepseekProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "answer" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Reasoning != "thinking" {
		t.Fatalf("reasoning = %q", response.Reasoning)
	}
}

// anthropic-messages 的 thinking block 同理：正文为空而 thinking 非空，表示模型
// 只思考没有作答。
func TestParseAnthropicResponseKeepsThinkingOutOfText(t *testing.T) {
	body := []byte(`{"content":[{"type":"thinking","text":"收到"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":2}}`)
	response, err := ai.ParseWireResponse(claudeProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "" {
		t.Fatalf("text = %q；思考 block 不得成为回答", response.Text)
	}
	if response.Reasoning != "收到" {
		t.Fatalf("reasoning = %q", response.Reasoning)
	}
}

func TestParseAnthropicResponseCollectsThinkingAlongsideText(t *testing.T) {
	body := []byte(`{"content":[{"type":"thinking","text":"先查一下"},{"type":"text","text":"答案是 42"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":9}}`)
	response, err := ai.ParseWireResponse(claudeProfile(), 200, body)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if response.Text != "答案是 42" {
		t.Fatalf("text = %q", response.Text)
	}
	if response.Reasoning != "先查一下" {
		t.Fatalf("reasoning = %q", response.Reasoning)
	}
}
