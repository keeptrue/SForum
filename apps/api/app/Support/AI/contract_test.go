package ai_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func validRequest() ai.CompletionRequest {
	return ai.CompletionRequest{
		Purpose:   "forum.reply",
		CostClass: ai.CostClassEconomy,
		System:    "你是社区审核助手。",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "请判断这条评论是否违规。"}}},
		},
		MaxTokens: 256,
	}
}

func TestCompletionRequestAcceptsMinimalValidRequest(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
}

func TestCompletionRequestRejectsContractViolations(t *testing.T) {
	temperature := func(v float64) *float64 { return &v }
	cases := map[string]func(*ai.CompletionRequest){
		"blank purpose":      func(r *ai.CompletionRequest) { r.Purpose = "   " },
		"unknown cost class": func(r *ai.CompletionRequest) { r.CostClass = "free" },
		"unknown format":     func(r *ai.CompletionRequest) { r.ResponseFormat = "xml" },
		"no messages":        func(r *ai.CompletionRequest) { r.Messages = nil },
		"too many messages": func(r *ai.CompletionRequest) {
			items := make([]ai.Message, 0, ai.MaxMessages+1)
			for i := 0; i <= ai.MaxMessages; i++ {
				items = append(items, ai.Message{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "x"}}})
			}
			r.Messages = items
		},
		"unknown role":        func(r *ai.CompletionRequest) { r.Messages[0].Role = ai.Role("system") },
		"empty parts":         func(r *ai.CompletionRequest) { r.Messages[0].Parts = nil },
		"non text part":       func(r *ai.CompletionRequest) { r.Messages[0].Parts[0].Type = ai.PartType("image") },
		"blank text":          func(r *ai.CompletionRequest) { r.Messages[0].Parts[0].Text = "  " },
		"tokens over ceiling": func(r *ai.CompletionRequest) { r.MaxTokens = ai.MaxCompletionTokens + 1 },
		"zero tokens":         func(r *ai.CompletionRequest) { r.MaxTokens = 0 },
		"temperature too hot": func(r *ai.CompletionRequest) { r.Temperature = temperature(2.5) },
		"system over ceiling": func(r *ai.CompletionRequest) { r.System = strings.Repeat("x", ai.MaxSystemBytes+1) },
	}
	for name, mutate := range cases {
		request := validRequest()
		mutate(&request)
		if err := request.Validate(); err == nil {
			t.Fatalf("%s: expected rejection, got nil", name)
		}
	}
}

func TestCompletionRequestNormalizedTrimsIdentifiers(t *testing.T) {
	request := validRequest()
	request.Purpose = "  forum.reply  "
	request.Metadata.ResourceID = " 42 "
	normalized := request.Normalized()
	if normalized.Purpose != "forum.reply" || normalized.Metadata.ResourceID != "42" {
		t.Fatalf("unexpected normalization: %+v", normalized)
	}
}

func TestCompletionRequestInputBytesCountsSystemAndParts(t *testing.T) {
	request := validRequest()
	expected := len(request.System) + len("请判断这条评论是否违规。")
	if got := request.InputBytes(); got != expected {
		t.Fatalf("input bytes = %d, want %d", got, expected)
	}
}

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	if got := ai.TruncateBytes("中文中文", 5); got != "中" || !utf8.ValidString(got) {
		t.Fatalf("truncate mid-rune = %q", got)
	}
	if got := ai.TruncateBytes("abc", 10); got != "abc" {
		t.Fatalf("truncate under limit = %q", got)
	}
	if got := ai.TruncateBytes("abc", 0); got != "abc" {
		t.Fatalf("truncate with zero limit should pass through, got %q", got)
	}
}
