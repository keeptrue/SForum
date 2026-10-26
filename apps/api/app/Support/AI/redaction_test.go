package ai_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func allRedactionCategories() ai.RedactionSettings {
	return ai.RedactionSettings{Enabled: true, RedactEmail: true, RedactPhone: true, RedactIP: true}
}

func TestRedactTextReplacesRecognisablePersonalData(t *testing.T) {
	cases := map[string]struct {
		input string
		want  string
	}{
		"email": {
			input: "write to Alice.Q+forum@example.co.uk please",
			want:  "write to " + ai.RedactedEmailPlaceholder + " please",
		},
		"chinese mobile": {
			input: "我的手机号是 13800138000，随时联系",
			want:  "我的手机号是 " + ai.RedactedPhonePlaceholder + "，随时联系",
		},
		"international number": {
			input: "reach me on +1 415 555 0132 tomorrow",
			want:  "reach me on " + ai.RedactedPhonePlaceholder + " tomorrow",
		},
		"area code number": {
			input: "office line (021) 5555-0132 works",
			want:  "office line " + ai.RedactedPhonePlaceholder + " works",
		},
		"ipv4": {
			input: "seen from 203.0.113.7 again",
			want:  "seen from " + ai.RedactedIPPlaceholder + " again",
		},
		"ipv6": {
			input: "origin 2001:db8:85a3:8d3:1319:8a2e:370:7348 logged",
			want:  "origin " + ai.RedactedIPPlaceholder + " logged",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ai.RedactText(allRedactionCategories(), testCase.input); got != testCase.want {
				t.Fatalf("redacted = %q, want %q", got, testCase.want)
			}
		})
	}
}

// 误伤比漏掉更难排查：论坛里最常见的「看起来像个人数据」的东西是提及、订单号、
// 版本号、时间戳与日期，它们必须原样保留。
func TestRedactTextKeepsOrdinaryForumTextIntact(t *testing.T) {
	untouched := []string{
		"@alice thanks for the reply, see post 12345",
		"version 1.2.3 shipped in build 20261011",
		"posted 2026-10-11 09:30, topic #4821, 10.0.0 partial address",
		"order ORD-2030-11-07-000123 was refunded",
		"热度: 1234, 回复: 56, 浏览: 789012",
	}
	settings := allRedactionCategories()
	for _, input := range untouched {
		if got := ai.RedactText(settings, input); got != input {
			t.Fatalf("ordinary text was rewritten: %q -> %q", input, got)
		}
	}
}

func TestRedactTextHonoursCategoryToggles(t *testing.T) {
	input := "mail a@b.com, call 13800138000, from 203.0.113.7"
	cases := map[string]ai.RedactionSettings{
		"disabled":      {Enabled: false, RedactEmail: true, RedactPhone: true, RedactIP: true},
		"email only":    {Enabled: true, RedactEmail: true},
		"phone only":    {Enabled: true, RedactPhone: true},
		"ip only":       {Enabled: true, RedactIP: true},
		"nothing armed": {Enabled: true},
	}
	for name, settings := range cases {
		t.Run(name, func(t *testing.T) {
			got := ai.RedactText(settings, input)
			// 总开关关闭时任何分类都不生效：开关是权威，分类只是「打开时脱敏哪些」。
			wantEmail := settings.Enabled && settings.RedactEmail
			wantPhone := settings.Enabled && settings.RedactPhone
			wantIP := settings.Enabled && settings.RedactIP
			if wantEmail != strings.Contains(got, ai.RedactedEmailPlaceholder) {
				t.Fatalf("email placeholder state drifted: %q", got)
			}
			if wantPhone != strings.Contains(got, ai.RedactedPhonePlaceholder) {
				t.Fatalf("phone placeholder state drifted: %q", got)
			}
			if wantIP != strings.Contains(got, ai.RedactedIPPlaceholder) {
				t.Fatalf("ip placeholder state drifted: %q", got)
			}
			if !settings.Enabled && got != input {
				t.Fatalf("disabled redaction rewrote text: %q", got)
			}
			if settings.Enabled && !settings.RedactEmail && !settings.RedactPhone && !settings.RedactIP &&
				got != input {
				t.Fatalf("armed but empty redaction rewrote text: %q", got)
			}
		})
	}
}

func TestRedactTextIsIdempotent(t *testing.T) {
	settings := allRedactionCategories()
	input := "a@b.com / 13800138000 / 203.0.113.7"
	once := ai.RedactText(settings, input)
	twice := ai.RedactText(settings, once)
	if once != twice {
		t.Fatalf("double redaction drifted: %q -> %q", once, twice)
	}
}

func TestApplyRedactionCoversSystemPartsAndToolArguments(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"query":    "user a@b.com",
		"page":     3,
		"nested":   []any{"203.0.113.7", 7},
		"flag":     true,
		"nullable": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ai.CompletionRequest{
		Purpose: "fixture.redaction", CostClass: ai.CostClassEconomy, MaxTokens: 64,
		System: "reply rules for a@b.com",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Parts: []ai.Part{{Type: ai.PartText, Text: "call 13800138000"}}},
			{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "c1", Name: "lookup", Arguments: string(arguments)}}},
			{Role: ai.RoleTool, ToolCallID: "c1", Parts: []ai.Part{{Type: ai.PartText, Text: "from 203.0.113.7"}}},
		},
	}
	redacted := ai.ApplyRedaction(allRedactionCategories(), request)

	if !strings.Contains(redacted.System, ai.RedactedEmailPlaceholder) ||
		!strings.Contains(redacted.Messages[0].Parts[0].Text, ai.RedactedPhonePlaceholder) ||
		!strings.Contains(redacted.Messages[2].Parts[0].Text, ai.RedactedIPPlaceholder) {
		t.Fatalf("redaction missed a text carrier: %#v", redacted)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(redacted.Messages[1].ToolCalls[0].Arguments), &decoded); err != nil {
		t.Fatalf("redacted tool arguments are not valid JSON: %v", err)
	}
	if decoded["query"] != "user "+ai.RedactedEmailPlaceholder || decoded["page"] != float64(3) {
		t.Fatalf("tool arguments drifted: %#v", decoded)
	}
	if decoded["flag"] != true || decoded["nullable"] != nil {
		t.Fatalf("non-string tool arguments must survive redaction: %#v", decoded)
	}
	nested, ok := decoded["nested"].([]any)
	if !ok || len(nested) != 2 || nested[0] != ai.RedactedIPPlaceholder || nested[1] != float64(7) {
		t.Fatalf("nested tool arguments drifted: %#v", decoded["nested"])
	}

	// 调用方手里的原文必须完好：网关只保证出站的那一份已脱敏。
	if !strings.Contains(request.System, "a@b.com") ||
		!strings.Contains(request.Messages[0].Parts[0].Text, "13800138000") ||
		!strings.Contains(string(arguments), "a@b.com") ||
		!strings.Contains(request.Messages[1].ToolCalls[0].Arguments, "a@b.com") {
		t.Fatalf("caller request was mutated: %#v", request)
	}
}

// 脱敏关闭时必须零改写，包括工具参数里的 JSON 排版：重排一次 JSON 会让缓存键与
// 历史条目全部错位，而运营者只是把开关关掉而已。
func TestApplyRedactionIsANoOpWhenDisabled(t *testing.T) {
	request := ai.CompletionRequest{
		Purpose: "fixture.redaction", CostClass: ai.CostClassEconomy, MaxTokens: 32,
		System:   "keep a@b.com",
		Messages: []ai.Message{{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "c1", Name: "x", Arguments: `{"b":1,"a":"a@b.com"}`}}}},
	}
	settings := ai.RedactionSettings{Enabled: false, RedactEmail: true}
	got := ai.ApplyRedaction(settings, request)
	if got.System != request.System ||
		got.Messages[0].ToolCalls[0].Arguments != request.Messages[0].ToolCalls[0].Arguments {
		t.Fatalf("disabled redaction rewrote the request: %#v", got)
	}
}

func TestGatewayRedactsOutboundWireRequestWithoutTouchingTheCaller(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	profile := settings.Profiles[0]
	profile.Enabled = true
	settings.Profiles[0] = profile
	invoker := &fakeInvoker{response: ai.WireResponse{Text: "ok", StopReason: ai.StopReasonEnd}}
	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: &fakeSettings{settings: settings},
		Usage:    &fakeUsage{},
		Traces:   &fakeTraces{},
		Creds:    &fakeCreds{value: "sk-test"},
		Invoker:  invoker,
	})
	request := ai.CompletionRequest{
		Purpose: "fixture.redaction", CostClass: ai.CostClassEconomy, MaxTokens: 64,
		System: "Follow the rules for alice@example.com.",
		Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{
			Type: ai.PartText, Text: "call me on +8613800138000 from 203.0.113.7",
		}}}},
		Metadata: ai.Metadata{CallerExtensionID: "fixture.ai"},
	}
	if _, err := gateway.Execute(context.Background(), request); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if invoker.calls != 1 {
		t.Fatalf("invoker calls = %d", invoker.calls)
	}
	body := string(invoker.lastWire.Body)
	for _, raw := range []string{"alice@example.com", "8613800138000", "203.0.113.7"} {
		if strings.Contains(body, raw) {
			t.Fatalf("outbound body leaked %q: %s", raw, body)
		}
	}
	for _, placeholder := range []string{
		ai.RedactedEmailPlaceholder, ai.RedactedPhonePlaceholder, ai.RedactedIPPlaceholder,
	} {
		if !strings.Contains(body, placeholder) {
			t.Fatalf("outbound body is missing %s: %s", placeholder, body)
		}
	}
	if !strings.Contains(request.System, "alice@example.com") ||
		!strings.Contains(request.Messages[0].Parts[0].Text, "+8613800138000") {
		t.Fatal("gateway mutated the caller's request")
	}
}

// 脱敏在缓存键之前完成：两个只有个人数据不同的请求在脱敏后正文相同，因此共享一次
// 出站。这条同时证明「供应商看到的正文」才是缓存语义的依据。
func TestGatewayCacheKeyUsesTheRedactedBody(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	profile := settings.Profiles[0]
	profile.Enabled = true
	settings.Profiles[0] = profile
	invoker := &fakeInvoker{response: ai.WireResponse{Text: "cached answer", StopReason: ai.StopReasonEnd}}
	cache := &fakeCache{items: map[string]ai.CompletionResult{}}
	traces := &fakeTraces{}
	gateway := ai.NewGateway(ai.GatewayConfig{
		Settings: &fakeSettings{settings: settings},
		Usage:    &fakeUsage{}, Traces: traces,
		Creds:   &fakeCreds{value: "sk-test"},
		Invoker: invoker, Cache: cache,
	})
	calls := 0
	execute := func(email string) ai.CompletionResult {
		calls++
		result, err := gateway.Execute(context.Background(), ai.CompletionRequest{
			Purpose: "fixture.redaction", CostClass: ai.CostClassEconomy, MaxTokens: 64,
			Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{
				Type: ai.PartText, Text: "summarise the report for " + email,
			}}}},
			Metadata: ai.Metadata{CallerExtensionID: "fixture.ai"},
		})
		if err != nil {
			t.Fatalf("execute #%d: %v", calls, err)
		}
		return result
	}
	execute("alice@example.com")
	second := execute("bob@example.com")
	if invoker.calls != 1 {
		t.Fatalf("redacted bodies should share one outbound call, got %d", invoker.calls)
	}
	if second.Text != "cached answer" || len(cache.items) != 1 {
		t.Fatalf("cache state drifted: text=%q entries=%d", second.Text, len(cache.items))
	}
	hit := false
	for _, record := range traces.records {
		hit = hit || record.CacheHit
	}
	if !hit {
		t.Fatal("cache hit was not recorded in the execution trace")
	}
}
