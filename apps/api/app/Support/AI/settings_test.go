package ai_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func TestRecommendedSettingsAreValidAndConservative(t *testing.T) {
	settings := ai.RecommendedSettings()
	if err := settings.Validate(); err != nil {
		t.Fatalf("recommended settings must validate: %v", err)
	}
	if settings.Enabled {
		t.Fatal("gateway must ship disabled")
	}
	if settings.AutoAction.Enabled {
		t.Fatal("automatic action must ship disabled")
	}
	if settings.Gates.ExtensionDailyQuota <= 0 || settings.Gates.MonthlyBudgetMicros <= 0 {
		t.Fatal("default gates must be active")
	}
}

func TestSettingsKeepUnfinishedDisabledProfileSavable(t *testing.T) {
	settings := ai.RecommendedSettings()
	// 自定义提供商的初始草稿：尚未填写 baseUrl 与 model。
	settings.Profiles = append(settings.Profiles, ai.Profile{
		ID:        "custom",
		Label:     "自定义",
		Protocol:  ai.ProtocolOpenAIChat,
		APIKeyRef: ai.SecretReferencePrefix + "core/ai.custom.api_key",
		CostClass: ai.CostClassStandard,
	})
	if err := settings.Validate(); err != nil {
		t.Fatalf("an unfinished draft must stay savable: %v", err)
	}
	// 启用同一份草稿时必须被拒绝：不完整但启用的配置会直接导致调用失败。
	settings.Profiles[len(settings.Profiles)-1].Enabled = true
	if err := settings.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("enabling an incomplete profile must be rejected, got %v", err)
	}
}

func TestSettingsRejectInvalidProfileIdentityEvenWhenDisabled(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.Profiles = append(settings.Profiles, ai.Profile{ID: "CUSTOM!"})
	if err := settings.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("id stays required even for drafts, got %v", err)
	}
}

func TestSettingsAllowEnabledGatewayWithoutEnabledProfile(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	// 先开总开关再配供应商是正常顺序：这里必须允许保存。
	if err := settings.Validate(); err != nil {
		t.Fatalf("enabling the gateway alone must stay savable: %v", err)
	}
	warnings := strings.Join(settings.Warnings(), ",")
	if !strings.Contains(warnings, "gateway_enabled_without_enabled_profile") {
		t.Fatalf("missing provider must be warned about, got %v", settings.Warnings())
	}
	settings.Profiles[0].Enabled = true
	if warnings := strings.Join(settings.Warnings(), ","); strings.Contains(warnings, "gateway_enabled_without_enabled_profile") {
		t.Fatalf("warning must clear once a provider is enabled: %v", settings.Warnings())
	}
}

func TestSettingsRejectContractViolations(t *testing.T) {
	cases := map[string]func(*ai.Settings){
		"duplicate profile": func(s *ai.Settings) { s.Profiles = append(s.Profiles, s.Profiles[0]) },
		"unknown binding":   func(s *ai.Settings) { s.CostClassProfiles[ai.CostClassEconomy] = "missing" },
		"unknown class key": func(s *ai.Settings) { s.CostClassProfiles["free"] = "deepseek" },
		"bad posture":       func(s *ai.Settings) { s.PurposeFailurePosture["moderation"] = "whatever" },
		"bad action":        func(s *ai.Settings) { s.AutoAction.AllowedActions = []string{"delete"} },
		"confidence > 1":    func(s *ai.Settings) { s.AutoAction.ConfidenceThreshold = 1.2 },
		"rate over cap":     func(s *ai.Settings) { s.Gates.RateLimitPerMinute = ai.MaxRateLimitPerMinute + 1 },
		"budget over cap":   func(s *ai.Settings) { s.Gates.MonthlyBudgetMicros = ai.MaxMonthlyBudgetMicros + 1 },
		"user quota over":   func(s *ai.Settings) { s.Gates.UserDailyQuota = ai.MaxUserDailyQuota + 1 },
	}
	for name, mutate := range cases {
		settings := ai.RecommendedSettings()
		mutate(&settings)
		if err := settings.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
			t.Fatalf("%s: expected ErrSettingsInvalid, got %v", name, err)
		}
	}
}

func TestSettingsEnableAutoActionRequiresAllowedActions(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.AutoAction.Enabled = true
	settings.AutoAction.AllowedActions = nil
	if !errors.Is(settings.Validate(), ai.ErrSettingsInvalid) {
		t.Fatal("enabled auto action without allowed actions must be rejected")
	}
}

func TestFailurePostureDefaultsToOpen(t *testing.T) {
	settings := ai.RecommendedSettings()
	if got := settings.FailurePosture("moderation.review"); got != ai.FailurePostureOpen {
		t.Fatalf("posture = %q", got)
	}
	settings.PurposeFailurePosture["moderation.review"] = ai.FailurePostureClosed
	if got := settings.FailurePosture("moderation.review"); got != ai.FailurePostureClosed {
		t.Fatalf("posture = %q", got)
	}
}

func TestResolveProfileHonoursCostClassBinding(t *testing.T) {
	settings := ai.RecommendedSettings()
	if _, err := settings.ResolveProfile(ai.CostClassEconomy, ""); !errors.Is(err, ai.ErrProfileUnavailable) {
		t.Fatalf("disabled profile must be unavailable, got %v", err)
	}
	settings.Profiles[0].Enabled = true
	profile, err := settings.ResolveProfile(ai.CostClassEconomy, "")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if profile.ID != "deepseek" {
		t.Fatalf("resolved %q", profile.ID)
	}
	if _, err := settings.ResolveProfile(ai.CostClassEconomy, "nope"); !errors.Is(err, ai.ErrProfileUnavailable) {
		t.Fatalf("unknown explicit profile must be unavailable, got %v", err)
	}
}

func TestWarningsStayQuietUntilGatewayIsEnabled(t *testing.T) {
	if warnings := ai.RecommendedSettings().Warnings(); len(warnings) != 0 {
		t.Fatalf("disabled gateway should not warn: %v", warnings)
	}
	settings := ai.RecommendedSettings()
	settings.Enabled = true
	settings.Profiles[0].Enabled = true
	warnings := settings.Warnings()
	joined := strings.Join(warnings, ",")
	if !strings.Contains(joined, "budget_gate_inactive") {
		t.Fatalf("expected unpriced warning, got %v", warnings)
	}
}

// 回复工具白名单的三态必须可区分：缺省（用用途声明）、显式空列表（不给任何工具）、
// 显式列表（以站点配置为准）。把「未配置」和「配置为空」混起来，等于让一次保存动作
// 意外关掉回复的全部工具能力。
func TestReplyToolAllowlistDistinguishesUnsetFromEmpty(t *testing.T) {
	unset := ai.ReplySettings{}
	if names, configured := unset.ToolAllowlist(); configured || names != nil {
		t.Fatalf("unset reply tools = %#v configured=%v", names, configured)
	}
	empty := ai.ReplySettings{Tools: &ai.ReplyToolSettings{}}
	names, configured := empty.ToolAllowlist()
	if !configured || len(names) != 0 {
		t.Fatalf("empty reply tools = %#v configured=%v", names, configured)
	}
	list := ai.ReplySettings{Tools: &ai.ReplyToolSettings{Allowed: []string{" forum-search ", "vendor-plugin-tool"}}}
	names, configured = list.ToolAllowlist()
	if !configured || len(names) != 2 || names[0] != "forum-search" || names[1] != "vendor-plugin-tool" {
		t.Fatalf("configured reply tools = %#v configured=%v", names, configured)
	}
}

func TestSettingsValidateReplyToolAllowlistShape(t *testing.T) {
	valid := ai.RecommendedSettings()
	valid.Reply.Tools = &ai.ReplyToolSettings{Allowed: []string{"forum-search", "vendor-plugin-tool"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("well-formed allowlist must validate: %v", err)
	}

	cases := map[string][]string{
		"invalid name charset": {"Forum Search"},
		"dotted name":          {"vendor.plugin.tool"},
		"duplicate":            {"forum-search", "forum-search"},
		"empty entry":          {"forum-search", "  "},
	}
	for name, allowed := range cases {
		t.Run(name, func(t *testing.T) {
			settings := ai.RecommendedSettings()
			settings.Reply.Tools = &ai.ReplyToolSettings{Allowed: allowed}
			if err := settings.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
				t.Fatalf("%s must be rejected, got %v", name, err)
			}
		})
	}

	// 一次请求最多带 MaxToolsPerRequest 个工具：比它更长的白名单无法被满足，保存时就拒绝。
	tooMany := ai.RecommendedSettings()
	allowed := make([]string, 0, ai.MaxToolsPerRequest+1)
	for index := 0; index <= ai.MaxToolsPerRequest; index++ {
		allowed = append(allowed, fmt.Sprintf("tool-%d", index))
	}
	tooMany.Reply.Tools = &ai.ReplyToolSettings{Allowed: allowed}
	if err := tooMany.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
		t.Fatalf("oversized allowlist must be rejected, got %v", err)
	}
}

// 控制台展示文案的归一化语义：键去空白、空白值丢弃、空条目整体消失、最终为空
// 置 nil。它与 normalizePurposes 同一条规则——空覆盖等于没有，否则控制台会显示
// 一条空文案和一条假的「已自定义」。
func TestToolDisplayDescriptionsNormalizePrunesEmptyOverrides(t *testing.T) {
	settings := ai.Settings{
		ToolDisplayDescriptions: map[string]map[string]string{
			" forum-search ": {
				" en-US ": "  Search posts  ",
				"zh-CN":   "   ",
				"":        "orphan locale",
			},
			"vendor-tool": {},
			"   ":         {"en-US": "nameless tool"},
		},
	}
	normalized := settings.Normalized().ToolDisplayDescriptions
	if len(normalized) != 1 {
		t.Fatalf("only the tool with a real value may survive, got %#v", normalized)
	}
	entry, ok := normalized["forum-search"]
	if !ok {
		t.Fatalf("tool name must be trimmed, got %#v", normalized)
	}
	if len(entry) != 1 || entry["en-US"] != "Search posts" {
		t.Fatalf("locale key and value must be trimmed, got %#v", entry)
	}
}

func TestToolDisplayDescriptionsEmptyDocumentBecomesNil(t *testing.T) {
	empty := ai.Settings{ToolDisplayDescriptions: map[string]map[string]string{}}
	if got := empty.Normalized().ToolDisplayDescriptions; got != nil {
		t.Fatalf("empty map must normalize to nil, got %#v", got)
	}
	blank := ai.Settings{ToolDisplayDescriptions: map[string]map[string]string{
		"forum-search": {"en-US": "  "},
		"vendor-tool":  {},
	}}
	normalized := blank.Normalized()
	if normalized.ToolDisplayDescriptions != nil {
		t.Fatalf("blank overrides must normalize to nil, got %#v", normalized.ToolDisplayDescriptions)
	}
	// 「空覆盖等于没有」也必须是序列化事实：文档里不该留下一个空对象让控制台
	// 显示一个假的已配置状态。
	raw, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if strings.Contains(string(raw), "toolDisplayDescriptions") {
		t.Fatalf("an empty override must not be serialized: %s", raw)
	}
}

func TestSettingsAcceptWellFormedToolDisplayDescriptions(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.ToolDisplayDescriptions = map[string]map[string]string{
		"forum-search":       {"en-US": "Search posts and topics", "zh-CN": "搜索帖子与主题"},
		"vendor-plugin-tool": {"en-US": "Vendor search"},
	}
	if err := settings.Validate(); err != nil {
		t.Fatalf("well-formed display descriptions must validate: %v", err)
	}
	// 边界值本身是合法的：64 个工具、每个 8 条 locale。
	boundary := ai.RecommendedSettings()
	boundary.ToolDisplayDescriptions = make(map[string]map[string]string, ai.MaxToolDisplayTools)
	for tool := 0; tool < ai.MaxToolDisplayTools; tool++ {
		locales := make(map[string]string, ai.MaxToolDisplayLocales)
		for index := 0; index < ai.MaxToolDisplayLocales; index++ {
			locales[fmt.Sprintf("en-%c%c", 'A'+index, 'A'+index)] = "text"
		}
		boundary.ToolDisplayDescriptions[fmt.Sprintf("tool-%d", tool)] = locales
	}
	if err := boundary.Validate(); err != nil {
		t.Fatalf("the documented boundaries must stay savable: %v", err)
	}
}

func TestSettingsRejectInvalidToolDisplayDescriptions(t *testing.T) {
	tooManyTools := make(map[string]map[string]string, ai.MaxToolDisplayTools+1)
	for tool := 0; tool <= ai.MaxToolDisplayTools; tool++ {
		tooManyTools[fmt.Sprintf("tool-%d", tool)] = map[string]string{"en-US": "text"}
	}
	tooManyLocales := make(map[string]string, ai.MaxToolDisplayLocales+1)
	for index := 0; index <= ai.MaxToolDisplayLocales; index++ {
		tooManyLocales[fmt.Sprintf("en-%c%c", 'A'+index, 'A'+index)] = "text"
	}

	cases := map[string]struct {
		descriptions map[string]map[string]string
		field        string
		code         string
	}{
		"invalid tool name": {
			descriptions: map[string]map[string]string{"Vendor Tool": {"en-US": "text"}},
			field:        "toolDisplayDescriptions.Vendor Tool",
			code:         ai.IssueCodeInvalidValue,
		},
		"dotted tool name": {
			descriptions: map[string]map[string]string{"vendor.plugin.tool": {"en-US": "text"}},
			field:        "toolDisplayDescriptions.vendor.plugin.tool",
			code:         ai.IssueCodeInvalidValue,
		},
		"overlong tool name": {
			descriptions: map[string]map[string]string{strings.Repeat("a", ai.MaxToolNameLen+1): {"en-US": "text"}},
			field:        "toolDisplayDescriptions." + strings.Repeat("a", ai.MaxToolNameLen+1),
			code:         ai.IssueCodeInvalidValue,
		},
		"locale is a word": {
			descriptions: map[string]map[string]string{"forum-search": {"english": "text"}},
			field:        "toolDisplayDescriptions.forum-search.english",
			code:         ai.IssueCodeInvalidValue,
		},
		"locale with script subtag": {
			descriptions: map[string]map[string]string{"forum-search": {"zh-Hant": "text"}},
			field:        "toolDisplayDescriptions.forum-search.zh-Hant",
			code:         ai.IssueCodeInvalidValue,
		},
		"locale with three subtags": {
			descriptions: map[string]map[string]string{"forum-search": {"zh-Hant-CN": "text"}},
			field:        "toolDisplayDescriptions.forum-search.zh-Hant-CN",
			code:         ai.IssueCodeInvalidValue,
		},
		"empty locale key": {
			descriptions: map[string]map[string]string{"forum-search": {"  ": "text"}},
			field:        "toolDisplayDescriptions.forum-search",
			code:         ai.IssueCodeInvalidValue,
		},
		"overlong value": {
			descriptions: map[string]map[string]string{"forum-search": {"en-US": strings.Repeat("a", ai.MaxToolDescriptionBytes+1)}},
			field:        "toolDisplayDescriptions.forum-search.en-US",
			code:         ai.IssueCodeTooLong,
		},
		"too many locales": {
			descriptions: map[string]map[string]string{"forum-search": tooManyLocales},
			field:        "toolDisplayDescriptions.forum-search",
			code:         ai.IssueCodeTooMany,
		},
		"too many tools": {
			descriptions: tooManyTools,
			field:        "toolDisplayDescriptions",
			code:         ai.IssueCodeTooMany,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			settings := ai.RecommendedSettings()
			settings.ToolDisplayDescriptions = testCase.descriptions
			issues := settings.Issues()
			found := false
			for _, issue := range issues {
				if issue.Field == testCase.field && issue.Code == testCase.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected %s=%s, got %#v", testCase.field, testCase.code, issues)
			}
			if err := settings.Validate(); !errors.Is(err, ai.ErrSettingsInvalid) {
				t.Fatalf("invalid display descriptions must be rejected, got %v", err)
			}
		})
	}
}

// 展示文案是整份设置文档的一部分：它必须能穿过一次 JSON 往返，且不污染模型可见
// 的工具说明字段（ToolAvailability.Description）。
func TestToolDisplayDescriptionsSurviveJSONRoundTrip(t *testing.T) {
	settings := ai.RecommendedSettings()
	settings.ToolDisplayDescriptions = map[string]map[string]string{
		"forum-search": {"en-US": "Search posts"},
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ai.Settings
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ToolDisplayDescriptions["forum-search"]["en-US"] != "Search posts" {
		t.Fatalf("display descriptions lost in JSON round trip: %#v", decoded.ToolDisplayDescriptions)
	}
}
