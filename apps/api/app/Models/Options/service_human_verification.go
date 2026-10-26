package options

import (
	"context"
	"strings"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// 人机验证限流取值范围与推荐默认（每场景、每 IP、每分钟）。
const humanVerificationRateLimitMin = 1
const humanVerificationRateLimitMax = 600
const humanVerificationRateLimitDefault = 60

type humanVerificationScenario struct {
	name           string
	purpose        humanverify.Purpose
	defaultEnabled bool
}

var humanVerificationScenarios = []humanVerificationScenario{
	{name: NameHumanVerificationRegister, purpose: humanverify.PurposeRegister, defaultEnabled: true},
	{name: NameHumanVerificationPasswordReset, purpose: humanverify.PurposePasswordReset, defaultEnabled: true},
	{name: NameHumanVerificationEmailVerification, purpose: humanverify.PurposeEmailVerification, defaultEnabled: true},
	{name: NameHumanVerificationLoginRisk, purpose: humanverify.PurposeLoginRisk},
	{name: NameHumanVerificationPostRisk, purpose: humanverify.PurposePostRisk},
}

// humanVerificationLoginTriggerOptions 是登录强化的三个触发条件选项名。
// 它们只在 human_verification.scenarios.login_risk 总闸开启时才可能生效。
var humanVerificationLoginTriggerOptions = []string{
	NameHumanVerificationLoginAlways,
	NameHumanVerificationLoginNewDevice,
	NameHumanVerificationLoginPrivileged,
}

func init() {
	for _, scenario := range humanVerificationScenarios {
		optionDefinitions = append(optionDefinitions, optionDefinition{
			name:             scenario.name,
			public:           true,
			managePermission: identity.PermissionSettingsSiteManage,
		})
	}
	optionDefinitions = append(optionDefinitions,
		optionDefinition{name: NameHumanVerificationLoginAlways, public: true, managePermission: identity.PermissionSettingsSiteManage},
		optionDefinition{name: NameHumanVerificationLoginNewDevice, managePermission: identity.PermissionSettingsSiteManage},
		optionDefinition{name: NameHumanVerificationLoginPrivileged, managePermission: identity.PermissionSettingsSiteManage},
		optionDefinition{name: NameHumanVerificationRateLimit, managePermission: identity.PermissionSettingsSiteManage},
	)
}

func (s *Service) HumanVerificationConfig(ctx context.Context) (humanverify.RuntimeConfig, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return humanverify.RuntimeConfig{}, err
	}

	ttl, _ := parsePositiveDuration(values[NameAltchaChallengeTTL])
	cost, _ := parsePositiveInt(values[NameAltchaCost])
	purposeEnabled := map[humanverify.Purpose]bool{}
	for _, scenario := range humanVerificationScenarios {
		purposeEnabled[scenario.purpose] = isEnabledOption(values[scenario.name])
	}
	rateLimit, ok := parseBoundedInt(values[NameHumanVerificationRateLimit], humanVerificationRateLimitMin, humanVerificationRateLimitMax)
	if !ok {
		rateLimit = humanVerificationRateLimitDefault
	}
	return humanverify.RuntimeConfig{
		Provider:        values[NameHumanVerificationProvider],
		AltchaSecret:    values[NameAltchaSecret],
		AltchaTTL:       ttl,
		AltchaCost:      cost,
		PurposeEnabled:  purposeEnabled,
		RateLimit:       rateLimit,
		RateLimitWindow: time.Minute,
	}, nil
}

// LoginVerificationPolicy 返回登录强化的触发策略。
// 读取失败由调用方按 fail open 处理（退化为仅保留失败锁定行为）。
func (s *Service) LoginVerificationPolicy(ctx context.Context) (identity.LoginVerificationPolicy, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return identity.LoginVerificationPolicy{}, err
	}
	return identity.LoginVerificationPolicy{
		Enabled:    isEnabledOption(values[NameHumanVerificationLoginRisk]),
		Always:     isEnabledOption(values[NameHumanVerificationLoginAlways]),
		NewDevice:  isEnabledOption(values[NameHumanVerificationLoginNewDevice]),
		Privileged: isEnabledOption(values[NameHumanVerificationLoginPrivileged]),
	}, nil
}

// coerceHumanVerificationOptions 把人机验证相关的脏数据/缺省值收敛到推荐默认。
// 与 optionDefinitions、限流边界同处一文件，避免 service.go 继续膨胀。
func coerceHumanVerificationOptions(coerced, defaults map[string]string) {
	if provider, ok := normalizeHumanVerificationProvider(coerced[NameHumanVerificationProvider]); ok {
		coerced[NameHumanVerificationProvider] = provider
	} else {
		coerced[NameHumanVerificationProvider] = defaults[NameHumanVerificationProvider]
	}
	if coerced[NameHumanVerificationProvider] == humanverify.ProviderAltcha && strings.TrimSpace(coerced[NameAltchaSecret]) == "" {
		coerced[NameHumanVerificationProvider] = humanverify.ProviderDisabled
	}
	for _, scenario := range humanVerificationScenarios {
		value, ok := normalizeEnabledOption(coerced[scenario.name])
		if !ok {
			coerced[scenario.name] = defaults[scenario.name]
			continue
		}
		coerced[scenario.name] = value
	}
	for _, name := range humanVerificationLoginTriggerOptions {
		value, ok := normalizeEnabledOption(coerced[name])
		if !ok {
			coerced[name] = defaults[name]
			continue
		}
		coerced[name] = value
	}
	if _, ok := parseBoundedInt(coerced[NameHumanVerificationRateLimit], humanVerificationRateLimitMin, humanVerificationRateLimitMax); !ok {
		coerced[NameHumanVerificationRateLimit] = defaults[NameHumanVerificationRateLimit]
	}
	if _, ok := parsePositiveDuration(coerced[NameAltchaChallengeTTL]); !ok {
		coerced[NameAltchaChallengeTTL] = defaults[NameAltchaChallengeTTL]
	}
	if _, ok := parsePositiveInt(coerced[NameAltchaCost]); !ok {
		coerced[NameAltchaCost] = defaults[NameAltchaCost]
	}
	if _, ok := normalizeAltchaWidgetType(coerced[NameAltchaWidgetType]); !ok {
		coerced[NameAltchaWidgetType] = defaults[NameAltchaWidgetType]
	}
	if _, ok := normalizeAltchaWidgetAuto(coerced[NameAltchaWidgetAuto]); !ok {
		coerced[NameAltchaWidgetAuto] = defaults[NameAltchaWidgetAuto]
	}
	if _, ok := normalizeAltchaWidgetDisplay(coerced[NameAltchaWidgetDisplay]); !ok {
		coerced[NameAltchaWidgetDisplay] = defaults[NameAltchaWidgetDisplay]
	}
	if value, ok := normalizeEnabledOption(coerced[NameAltchaWidgetHideLogo]); ok {
		coerced[NameAltchaWidgetHideLogo] = value
	} else {
		coerced[NameAltchaWidgetHideLogo] = defaults[NameAltchaWidgetHideLogo]
	}
	if value, ok := normalizeEnabledOption(coerced[NameAltchaWidgetHideFooter]); ok {
		coerced[NameAltchaWidgetHideFooter] = value
	} else {
		coerced[NameAltchaWidgetHideFooter] = defaults[NameAltchaWidgetHideFooter]
	}
	if _, ok := parseBoundedInt(coerced[NameAltchaWidgetWorkers], altchaWidgetWorkersMin, altchaWidgetWorkersMax); !ok {
		coerced[NameAltchaWidgetWorkers] = defaults[NameAltchaWidgetWorkers]
	}
	if _, ok := parseBoundedInt(coerced[NameAltchaWidgetMinDuration], altchaWidgetMinDurationMin, altchaWidgetMinDurationMax); !ok {
		coerced[NameAltchaWidgetMinDuration] = defaults[NameAltchaWidgetMinDuration]
	}
}
