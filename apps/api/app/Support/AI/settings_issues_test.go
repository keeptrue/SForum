package ai_test

import (
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// fieldOf 返回第一条命中前缀的问题代码，便于断言「是哪一项错了」。
func fieldOf(issues []ai.Issue, field string) string {
	for _, issue := range issues {
		if issue.Field == field {
			return issue.Code
		}
	}
	return ""
}

// Validate 只有一个笼统的 ErrSettingsInvalid；控制台的 422 需要知道具体字段，
// 这条链路就是 Issues。它必须与 Validate 同源：有 issue 就意味着 Validate 失败。
func TestSettingsIssuesLocateTheOffendingField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ai.Settings)
		field  string
		code   string
	}{
		{
			name:   "rate over cap",
			mutate: func(s *ai.Settings) { s.Gates.RateLimitPerMinute = ai.MaxRateLimitPerMinute + 1 },
			field:  "gates.rateLimitPerMinute",
			code:   ai.IssueCodeOutOfRange,
		},
		{
			name:   "budget over cap",
			mutate: func(s *ai.Settings) { s.Gates.MonthlyBudgetMicros = ai.MaxMonthlyBudgetMicros + 1 },
			field:  "gates.monthlyBudgetMicros",
			code:   ai.IssueCodeOutOfRange,
		},
		{
			name:   "negative user quota",
			mutate: func(s *ai.Settings) { s.Gates.UserDailyQuota = -1 },
			field:  "gates.userDailyQuota",
			code:   ai.IssueCodeOutOfRange,
		},
		{
			name:   "unknown cost class binding",
			mutate: func(s *ai.Settings) { s.CostClassProfiles[ai.CostClassEconomy] = "missing" },
			field:  "costClassProfiles." + ai.CostClassEconomy,
			code:   ai.IssueCodeInvalidValue,
		},
		{
			name:   "bad failure posture",
			mutate: func(s *ai.Settings) { s.PurposeFailurePosture["moderation"] = "whatever" },
			field:  "purposeFailurePosture.moderation",
			code:   ai.IssueCodeInvalidValue,
		},
		{
			name:   "confidence over one",
			mutate: func(s *ai.Settings) { s.AutoAction.ConfidenceThreshold = 1.2 },
			field:  "autoAction.confidenceThreshold",
			code:   ai.IssueCodeOutOfRange,
		},
		{
			name: "auto action enabled without actions",
			mutate: func(s *ai.Settings) {
				s.AutoAction.Enabled = true
				s.AutoAction.AllowedActions = nil
			},
			field: "autoAction.allowedActions",
			code:  ai.IssueCodeRequired,
		},
		{
			name: "purpose tool name invalid",
			mutate: func(s *ai.Settings) {
				s.Purposes = map[string]ai.PurposeSettings{
					"forum.reply": {Tools: &ai.PurposeToolSettings{Allowed: []string{"Vendor Tool"}}},
				}
			},
			field: "purposes.forum.reply.tools.allowed",
			code:  ai.IssueCodeInvalidValue,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			settings := ai.RecommendedSettings()
			// 让 profile 保持有效：这些用例只关心配置层的问题，不掺供应商错误。
			settings.Profiles[0].Enabled = true
			testCase.mutate(&settings)

			issues := settings.Issues()
			if got := fieldOf(issues, testCase.field); got != testCase.code {
				t.Fatalf("issues = %+v, want %s on %s", issues, testCase.code, testCase.field)
			}
			if settings.Validate() == nil {
				t.Fatal("Issues must imply Validate failure")
			}
		})
	}
}

// 合法配置必须没有 issue：否则控制台会对一份能保存的配置报字段错误。
func TestSettingsIssuesEmptyForRecommendedDocument(t *testing.T) {
	settings := ai.RecommendedSettings().Normalized()
	if issues := settings.Issues(); len(issues) != 0 {
		t.Fatalf("recommended settings must be issue-free, got %+v", issues)
	}
}

// 启用的供应商才逐项校验；草稿（enabled=false）允许不完整，这是既有语义。
func TestSettingsIssuesSkipDisabledProfiles(t *testing.T) {
	settings := ai.RecommendedSettings().Normalized()
	settings.Profiles = append(settings.Profiles, ai.Profile{ID: "draft", Label: "", BaseURL: "not-a-url"})
	if issues := settings.Issues(); len(issues) != 0 {
		t.Fatalf("disabled profiles must not block saving, got %+v", issues)
	}
	settings.Profiles[len(settings.Profiles)-1].Enabled = true
	issues := settings.Issues()
	if got := fieldOf(issues, "profiles[3].baseUrl"); got != ai.IssueCodeInvalidURL {
		t.Fatalf("enabled profile must be validated field by field, got %+v", issues)
	}
}

// 内嵌凭据与「不是 URL」是两种不同的原因：运营者要据此知道该删掉密钥还是补 scheme。
func TestProfileIssuesDistinguishCredentialFromMalformedURL(t *testing.T) {
	profile := ai.BuiltinProfiles()[0]
	profile.Enabled = true
	profile.BaseURL = "api.deepseek.com"
	if got := fieldOf(profile.Issues(0), "profiles[0].baseUrl"); got != ai.IssueCodeInvalidURL {
		t.Fatalf("missing scheme must be invalid_url, got %q", got)
	}
	profile.BaseURL = "https://user:key@api.deepseek.com"
	if got := fieldOf(profile.Issues(0), "profiles[0].baseUrl"); got != ai.IssueCodeCredentialInURL {
		t.Fatalf("embedded credential must be credential_in_url, got %q", got)
	}
	// 中文标签按字节计：22 个汉字 = 66 字节，超过 64 字节上限。
	profile.BaseURL = "https://api.deepseek.com"
	profile.Label = strings.Repeat("模", 22)
	if got := fieldOf(profile.Issues(0), "profiles[0].label"); got != ai.IssueCodeTooLong {
		t.Fatalf("oversized label must be reported by bytes, got %q", got)
	}
}
