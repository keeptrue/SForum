package ai_test

import (
	"errors"
	"strings"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

func TestBuiltinProfilesAreValidAndDisabledByDefault(t *testing.T) {
	profiles := ai.BuiltinProfiles()
	if len(profiles) == 0 {
		t.Fatal("expected builtin profiles")
	}
	protocols := map[string]bool{}
	for _, profile := range profiles {
		if err := profile.Validate(); err != nil {
			t.Fatalf("builtin profile %s invalid: %v", profile.ID, err)
		}
		if profile.Enabled {
			t.Fatalf("builtin profile %s must ship disabled", profile.ID)
		}
		if !strings.HasPrefix(profile.APIKeyRef, ai.SecretReferencePrefix) {
			t.Fatalf("builtin profile %s must reference the secret store", profile.ID)
		}
		protocols[profile.Protocol] = true
	}
	if !protocols[ai.ProtocolOpenAIChat] || !protocols[ai.ProtocolAnthropicMessages] {
		t.Fatalf("builtin profiles must cover both protocols, got %v", protocols)
	}
}

func baseProfile() ai.Profile {
	return ai.Profile{
		ID:        "custom",
		Label:     "Custom",
		Protocol:  ai.ProtocolOpenAIChat,
		BaseURL:   "https://example.invalid/v1",
		APIKeyRef: ai.SecretReferencePrefix + "core/ai.custom.api_key",
		Model:     "some-model",
		CostClass: ai.CostClassStandard,
		Defaults:  ai.ProfileDefaults{MaxTokens: 512, TimeoutMS: 10_000},
	}
}

func TestProfileRejectsCredentialBearingBaseURL(t *testing.T) {
	profile := baseProfile()
	profile.BaseURL = "https://user:pass@example.invalid/v1"
	if !errors.Is(profile.Validate(), ai.ErrProfileInvalid) {
		t.Fatal("expected credential-bearing base url to be rejected")
	}
}

func TestProfileRejectsContractViolations(t *testing.T) {
	cases := map[string]func(*ai.Profile){
		"blank id":         func(p *ai.Profile) { p.ID = "" },
		"uppercase id":     func(p *ai.Profile) { p.ID = "Custom" },
		"blank label":      func(p *ai.Profile) { p.Label = " " },
		"unknown protocol": func(p *ai.Profile) { p.Protocol = "grpc" },
		"missing host":     func(p *ai.Profile) { p.BaseURL = "https:///v1" },
		"bad scheme":       func(p *ai.Profile) { p.BaseURL = "ftp://example.invalid" },
		"plain secret":     func(p *ai.Profile) { p.APIKeyRef = "sk-live-plaintext" },
		"blank model":      func(p *ai.Profile) { p.Model = " " },
		"unknown class":    func(p *ai.Profile) { p.CostClass = "free" },
		"tokens over cap":  func(p *ai.Profile) { p.Defaults.MaxTokens = ai.MaxCompletionTokens + 1 },
		"timeout over cap": func(p *ai.Profile) { p.Defaults.TimeoutMS = ai.MaxTimeoutMS + 1 },
		"timeout under":    func(p *ai.Profile) { p.Defaults.TimeoutMS = ai.MinTimeoutMS - 1 },
		"negative price":   func(p *ai.Profile) { p.Price.InputPerMillionMicros = -1 },
	}
	for name, mutate := range cases {
		profile := baseProfile()
		mutate(&profile)
		if err := profile.Validate(); !errors.Is(err, ai.ErrProfileInvalid) {
			t.Fatalf("%s: expected ErrProfileInvalid, got %v", name, err)
		}
	}
}

func TestProfileNormalizedFillsStructuralDefaults(t *testing.T) {
	profile := ai.Profile{ID: "  custom  ", BaseURL: "https://example.invalid/v1/"}
	normalized := profile.Normalized()
	if normalized.ID != "custom" {
		t.Fatalf("id = %q", normalized.ID)
	}
	if strings.HasSuffix(normalized.BaseURL, "/") {
		t.Fatalf("base url should drop the trailing slash: %q", normalized.BaseURL)
	}
	if normalized.Defaults.TimeoutMS != ai.DefaultTimeoutMS || normalized.Defaults.MaxTokens != ai.DefaultMaxCompletionTokens {
		t.Fatalf("defaults not filled: %+v", normalized.Defaults)
	}
}

func TestEstimateSpendMicrosUsesPerMillionPricing(t *testing.T) {
	price := ai.Price{InputPerMillionMicros: 2 * ai.MicroPerUnit, OutputPerMillionMicros: 8 * ai.MicroPerUnit}
	usage := ai.Usage{InputTokens: 1_000_000, OutputTokens: 500_000}
	if got, want := ai.EstimateSpendMicros(price, usage), int64(6*ai.MicroPerUnit); got != want {
		t.Fatalf("spend = %d micro, want %d", got, want)
	}
	if got := ai.EstimateSpendMicros(ai.Price{}, usage); got != 0 {
		t.Fatalf("unpriced profile should cost 0, got %d", got)
	}
}
