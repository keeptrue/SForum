package aicontroller

import (
	"encoding/json"
	"strings"
	"testing"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 控制台的真实使用路径是「GET 默认配置 → 只改一个开关 → PUT 整份文档」。
// 这个往返必须始终可保存：一旦它被拒绝，用户看到的就是一个没有任何解释的 422。
func TestUpdateSettingsRequestRoundTripStaysSavable(t *testing.T) {
	settings := supportai.RecommendedSettings().Normalized()
	settings.Enabled = true

	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	var request updateSettingsRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	assembled := settingsFromRequest(request)
	if !assembled.Enabled {
		t.Fatal("enabled flag did not survive the round trip")
	}
	if len(assembled.Profiles) != len(settings.Profiles) {
		t.Fatalf("profiles lost in round trip: %d vs %d", len(assembled.Profiles), len(settings.Profiles))
	}
	if err := assembled.Validate(); err != nil {
		t.Fatalf("enabling the gateway alone must stay savable: %v", err)
	}
	warnings := assembled.Warnings()
	if len(warnings) == 0 {
		t.Fatal("saving an enabled gateway without providers must warn")
	}
}

// 控制台是单选语义：选中的提供商必须真正被解析到，否则界面在撒谎。
func TestSelectedProviderIsTheOneResolved(t *testing.T) {
	for _, preset := range supportai.BuiltinProfiles() {
		settings := supportai.RecommendedSettings()
		// 模拟控制台提交：只启用选中的那个，并把三个成本等级都指向它。
		for index := range settings.Profiles {
			settings.Profiles[index].Enabled = settings.Profiles[index].ID == preset.ID
		}
		for _, class := range []string{supportai.CostClassEconomy, supportai.CostClassStandard, supportai.CostClassPremium} {
			settings.CostClassProfiles[class] = preset.ID
		}
		request := updateSettingsRequest{
			Enabled:               true,
			Profiles:              settings.Profiles,
			CostClassProfiles:     settings.CostClassProfiles,
			PurposeFailurePosture: settings.PurposeFailurePosture,
			AutoAction:            settings.AutoAction,
			Gates:                 settings.Gates,
			Redaction:             settings.Redaction,
		}
		assembled := settingsFromRequest(request)
		if err := assembled.Validate(); err != nil {
			t.Fatalf("preset %s: %v", preset.ID, err)
		}
		for _, class := range []string{supportai.CostClassEconomy, supportai.CostClassStandard, supportai.CostClassPremium} {
			resolved, err := assembled.ResolveProfile(class, "")
			if err != nil {
				t.Fatalf("preset %s class %s: %v", preset.ID, class, err)
			}
			if resolved.ID != preset.ID {
				t.Fatalf("selected %s but class %s resolves to %s", preset.ID, class, resolved.ID)
			}
		}
	}
}

// 每个内置预设单独启用后都必须可用：它们是运营者最先会打开的选项。
func TestBuiltinProfilesRemainIndividuallyEnableable(t *testing.T) {
	for _, preset := range supportai.BuiltinProfiles() {
		settings := supportai.RecommendedSettings().Normalized()
		settings.Enabled = true
		for index := range settings.Profiles {
			settings.Profiles[index].Enabled = settings.Profiles[index].ID == preset.ID
		}
		if err := settings.Validate(); err != nil {
			t.Fatalf("preset %s must be enableable on its own: %v", preset.ID, err)
		}
		warnings := settings.Warnings()
		for _, warning := range warnings {
			if warning == "gateway_enabled_without_enabled_profile:no_call_will_succeed" {
				t.Fatalf("preset %s enabled but the missing-provider warning still fired", preset.ID)
			}
		}
	}
}

// 前端一直在 PUT 文档里带 `reply`（提示词与工具白名单），而 DTO 曾漏掉这个字段：
// 保存设置会静默清空运营者写过的提示词，并让白名单配置根本无法落库。这条断言把
// 「往返不丢回复相关字段」钉在控制台边界上。
func TestUpdateSettingsRequestKeepsReplyOverrides(t *testing.T) {
	settings := supportai.RecommendedSettings().Normalized()
	settings.Reply.SystemPrompt = "只回答与本站相关的问题。"
	settings.Reply.Tools = &supportai.ReplyToolSettings{Allowed: []string{"forum-search", "vendor-plugin-tool"}}

	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var request updateSettingsRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	assembled := settingsFromRequest(request)
	if assembled.Reply.SystemPrompt != settings.Reply.SystemPrompt {
		t.Fatalf("reply prompt lost in round trip: %q", assembled.Reply.SystemPrompt)
	}
	names, configured := assembled.Reply.ToolAllowlist()
	if !configured || len(names) != 2 || names[1] != "vendor-plugin-tool" {
		t.Fatalf("reply tool allowlist lost in round trip: %#v configured=%v", names, configured)
	}
	if err := assembled.Validate(); err != nil {
		t.Fatalf("well-formed reply overrides must stay savable: %v", err)
	}

	// 白名单形状校验必须走同一条保存路径：非法名字在保存时就该被拒。
	settings.Reply.Tools = &supportai.ReplyToolSettings{Allowed: []string{"Vendor Tool"}}
	raw, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var invalid updateSettingsRequest
	if err := json.Unmarshal(raw, &invalid); err != nil {
		t.Fatal(err)
	}
	if err := settingsFromRequest(invalid).Validate(); err == nil {
		t.Fatal("an invalid tool name must be rejected before saving")
	}
}

// 控制台把数字输入框清空时，载荷里的数字字段会变成空串；填入小数时是 12.5。
// 两种形状都无法解码成 Go 的 int。它们必须被单独识别为「形状不对」并指出字段，
// 而不是折叠成一个没有任何指向的 ai.settings_invalid。
func TestDecodeSettingsRequestReportsMalformedNumericFields(t *testing.T) {
	cases := map[string]string{
		"empty string": `{"enabled":true,"profiles":[{"id":"deepseek","label":"DeepSeek","protocol":"openai-chat","baseUrl":"https://api.deepseek.com","apiKeyRef":"sforum.secret://core/ai.deepseek.api_key","model":"deepseek-chat","costClass":"economy","enabled":true,"defaults":{"maxTokens":"","timeoutMs":30000}}]}`,
		"decimal":      `{"enabled":true,"profiles":[{"id":"deepseek","label":"DeepSeek","protocol":"openai-chat","baseUrl":"https://api.deepseek.com","apiKeyRef":"sforum.secret://core/ai.deepseek.api_key","model":"deepseek-chat","costClass":"economy","enabled":true,"defaults":{"maxTokens":12.5,"timeoutMs":30000}}]}`,
	}
	for name, body := range cases {
		_, fields, reason := decodeSettingsRequest([]byte(body))
		if reason != "ai.settings_malformed" {
			t.Fatalf("%s: reason = %q", name, reason)
		}
		found := false
		for field := range fields {
			if strings.HasSuffix(field, "maxTokens") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: fields must point at maxTokens, got %#v", name, fields)
		}
		// 只暴露字段路径：载荷里可能有密钥，原值不得回显。
		for _, messages := range fields {
			for _, message := range messages {
				if strings.Contains(message, "12.5") || strings.Contains(message, "cannot unmarshal") {
					t.Fatalf("%s: message leaks request details: %q", name, message)
				}
			}
		}
	}
}

// 取值越界走另一条路径：字段级消息按载荷路径给出，控制台据此把文案挂到输入框。
func TestDecodeSettingsRequestReportsFieldLevelIssues(t *testing.T) {
	_, fields, reason := decodeSettingsRequest([]byte(`{"gates":{"rateLimitPerMinute":999999}}`))
	if reason != "ai.settings_invalid" {
		t.Fatalf("reason = %q", reason)
	}
	messages, ok := fields["gates.rateLimitPerMinute"]
	if !ok || len(messages) == 0 {
		t.Fatalf("fields = %#v", fields)
	}
	if messages[0] != "ai.field."+supportai.IssueCodeOutOfRange {
		t.Fatalf("message key = %q", messages[0])
	}
}

// 一份推荐配置必须原样通过：放宽上限后仍然要有一条「不做任何修改也能保存」的基线。
func TestDecodeSettingsRequestAcceptsRecommendedDocument(t *testing.T) {
	settings := supportai.RecommendedSettings().Normalized()
	settings.Enabled = true
	for index := range settings.Profiles {
		settings.Profiles[index].Enabled = settings.Profiles[index].ID == "deepseek"
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	decoded, fields, reason := decodeSettingsRequest(raw)
	if reason != "" {
		t.Fatalf("recommended document rejected: reason=%q fields=%#v", reason, fields)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded document must validate: %v", err)
	}
}

// toolDisplayDescriptions 是整份文档 PUT 的一部分。DTO 漏字段会重演 reply 的事故：
// 运营者保存任何其它设置时，写过的界面译文被静默抹掉，而界面看起来一切正常。
// 这条断言把「提交 → 组装 → 落库形状 → 再读回」整条链钉住。
func TestUpdateSettingsRequestKeepsToolDisplayDescriptions(t *testing.T) {
	base := supportai.RecommendedSettings().Normalized()
	request := updateSettingsRequest{
		Enabled:               base.Enabled,
		Profiles:              base.Profiles,
		CostClassProfiles:     base.CostClassProfiles,
		PurposeFailurePosture: base.PurposeFailurePosture,
		Purposes:              base.Purposes,
		AutoAction:            base.AutoAction,
		Gates:                 base.Gates,
		Redaction:             base.Redaction,
		Multimodal:            base.Multimodal,
		ToolDisplayDescriptions: map[string]map[string]string{
			"forum-search":         {"en-US": "Search posts and topics", "zh-CN": "搜索帖子与主题"},
			" vendor-plugin-tool ": {" en-US ": "  Vendor search  "},
		},
	}
	assembled := settingsFromRequest(request)
	if err := assembled.Validate(); err != nil {
		t.Fatalf("well-formed display descriptions must stay savable: %v", err)
	}

	raw, err := json.Marshal(assembled)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	var stored supportai.Settings
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if len(stored.ToolDisplayDescriptions) != 2 {
		t.Fatalf("display descriptions lost in round trip: %#v", stored.ToolDisplayDescriptions)
	}
	if stored.ToolDisplayDescriptions["forum-search"]["zh-CN"] != "搜索帖子与主题" {
		t.Fatalf("zh-CN text lost: %#v", stored.ToolDisplayDescriptions["forum-search"])
	}
	// 键与值在组装时已被归一：落库的形状必须是干净的，控制台不必再清洗一遍。
	if stored.ToolDisplayDescriptions["vendor-plugin-tool"]["en-US"] != "Vendor search" {
		t.Fatalf("trimmed plugin text lost: %#v", stored.ToolDisplayDescriptions)
	}

	// 形状校验必须走同一条保存路径：非法工具名在保存时就该被拒，而不是存进去。
	request.ToolDisplayDescriptions = map[string]map[string]string{"Vendor Tool": {"en-US": "text"}}
	if err := settingsFromRequest(request).Validate(); err == nil {
		t.Fatal("an invalid tool name must be rejected before saving")
	}
}
