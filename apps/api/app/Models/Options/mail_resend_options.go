package options

import (
	"context"
	"strconv"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	mailResendCooldownMin = 0
	mailResendCooldownMax = 3600
	mailResendWindowMin   = 1
	mailResendWindowMax   = 1440
	mailResendTargetMin   = 1
	mailResendTargetMax   = 100
	mailResendIPMin       = 1
	mailResendIPMax       = 1000
)

func init() {
	permission := identity.PermissionSettingsSiteManage
	optionDefinitions = append(optionDefinitions,
		optionDefinition{name: NameIdentityMailResendCooldownSeconds, managePermission: permission},
		optionDefinition{name: NameIdentityMailResendWindowMinutes, managePermission: permission},
		optionDefinition{name: NameIdentityMailResendMaxPerTarget, managePermission: permission},
		optionDefinition{name: NameIdentityMailResendMaxPerIP, managePermission: permission},
	)
}

func mailResendRecommendedDefaults() map[string]string {
	return map[string]string{
		NameIdentityMailResendCooldownSeconds: strconv.Itoa(int(identity.RecommendedMailResendCooldown / time.Second)),
		NameIdentityMailResendWindowMinutes:   strconv.Itoa(int(identity.RecommendedMailResendWindow / time.Minute)),
		NameIdentityMailResendMaxPerTarget:    strconv.Itoa(identity.RecommendedMailResendMaxPerTarget),
		NameIdentityMailResendMaxPerIP:        strconv.Itoa(identity.RecommendedMailResendMaxPerIP),
	}
}

func mergeMailResendDefaults(values map[string]string) {
	for name, value := range mailResendRecommendedDefaults() {
		values[name] = value
	}
}

func normalizeMailResendOption(name, value string) (string, bool) {
	switch name {
	case NameIdentityMailResendCooldownSeconds:
		return normalizeBoundedInt(value, mailResendCooldownMin, mailResendCooldownMax)
	case NameIdentityMailResendWindowMinutes:
		return normalizeBoundedInt(value, mailResendWindowMin, mailResendWindowMax)
	case NameIdentityMailResendMaxPerTarget:
		return normalizeBoundedInt(value, mailResendTargetMin, mailResendTargetMax)
	case NameIdentityMailResendMaxPerIP:
		return normalizeBoundedInt(value, mailResendIPMin, mailResendIPMax)
	default:
		return "", false
	}
}

func coerceMailResendOptions(values, defaults map[string]string) {
	for _, name := range []string{
		NameIdentityMailResendCooldownSeconds,
		NameIdentityMailResendWindowMinutes,
		NameIdentityMailResendMaxPerTarget,
		NameIdentityMailResendMaxPerIP,
	} {
		if value, ok := normalizeMailResendOption(name, values[name]); ok {
			values[name] = value
		} else {
			values[name] = defaults[name]
		}
	}
}

func isValidMailResendOptions(values map[string]string) bool {
	for _, name := range []string{
		NameIdentityMailResendCooldownSeconds,
		NameIdentityMailResendWindowMinutes,
		NameIdentityMailResendMaxPerTarget,
		NameIdentityMailResendMaxPerIP,
	} {
		if _, ok := normalizeMailResendOption(name, values[name]); !ok {
			return false
		}
	}
	return true
}

type MailResendPolicyResolver struct {
	service *Service
}

func NewMailResendPolicyResolver(service *Service) *MailResendPolicyResolver {
	return &MailResendPolicyResolver{service: service}
}

func (r *MailResendPolicyResolver) MailResendPolicy(ctx context.Context) (identity.MailResendPolicy, error) {
	values, err := r.service.loadMap(ctx)
	if err != nil {
		return identity.MailResendPolicy{}, err
	}
	cooldownSeconds, _ := strictAtoi(values[NameIdentityMailResendCooldownSeconds])
	windowMinutes, _ := strictAtoi(values[NameIdentityMailResendWindowMinutes])
	maxPerTarget, _ := strictAtoi(values[NameIdentityMailResendMaxPerTarget])
	maxPerIP, _ := strictAtoi(values[NameIdentityMailResendMaxPerIP])
	return identity.MailResendPolicy{
		Cooldown:     time.Duration(cooldownSeconds) * time.Second,
		Window:       time.Duration(windowMinutes) * time.Minute,
		MaxPerTarget: maxPerTarget,
		MaxPerIP:     maxPerIP,
	}, nil
}
