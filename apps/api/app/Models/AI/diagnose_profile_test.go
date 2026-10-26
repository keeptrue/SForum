package ai

import (
	"testing"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// 诊断必须探测实际能承载流量的那份配置：只绑定 standard/premium 的站点若被
// 判成「没有可用提供商」，运营者会照着错误结论去改一份本来正确的配置。
func TestResolveDiagnoseProfileFallsBackToBoundCostClass(t *testing.T) {
	settings := supportai.RecommendedSettings()
	// 只留 standard 绑定，并把三个内置 profile 里对应那个启用。
	settings.CostClassProfiles = map[string]string{
		supportai.CostClassEconomy:  "",
		supportai.CostClassStandard: "openai",
		supportai.CostClassPremium:  "",
	}
	for index := range settings.Profiles {
		settings.Profiles[index].Enabled = settings.Profiles[index].ID == "openai"
	}

	profile, costClass, err := resolveDiagnoseProfile(settings)
	if err != nil {
		t.Fatalf("expected the bound standard profile to be diagnosed: %v", err)
	}
	if profile.ID != "openai" {
		t.Fatalf("diagnosed profile = %q", profile.ID)
	}
	if costClass != supportai.CostClassStandard {
		t.Fatalf("reported cost class = %q", costClass)
	}
}

// economy 优先：多数部署只绑定 economy，诊断就该测它。
func TestResolveDiagnoseProfilePrefersEconomy(t *testing.T) {
	settings := supportai.RecommendedSettings()
	settings.CostClassProfiles = map[string]string{
		supportai.CostClassEconomy:  "deepseek",
		supportai.CostClassStandard: "openai",
		supportai.CostClassPremium:  "anthropic",
	}
	for index := range settings.Profiles {
		settings.Profiles[index].Enabled = true
	}

	profile, costClass, err := resolveDiagnoseProfile(settings)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if profile.ID != "deepseek" || costClass != supportai.CostClassEconomy {
		t.Fatalf("got %q / %q", profile.ID, costClass)
	}
}

// 一个绑定都没有时仍然报「没有可用提供商」，与网关的失败姿态保持一致。
func TestResolveDiagnoseProfileReportsUnavailableWhenNothingBound(t *testing.T) {
	settings := supportai.RecommendedSettings()
	settings.CostClassProfiles = map[string]string{}
	if _, _, err := resolveDiagnoseProfile(settings); err == nil {
		t.Fatal("expected an error when no cost class is bound")
	}
}
