package options

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	localization "github.com/keeptrue/sforum/apps/api/app/Support/Localization"
)

func isValidURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func normalizeLocaleList(values []string) []string {
	locales := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		locale, ok := normalizeLocaleChoice(value, builtInLocales)
		if !ok || seen[locale] {
			continue
		}
		seen[locale] = true
		locales = append(locales, locale)
	}
	return locales
}

func parseStoredLocales(value string) []string {
	parts := strings.Split(value, ",")
	locales := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		locale, ok := normalizeLocaleChoice(part, builtInLocales)
		if !ok || seen[locale] {
			continue
		}
		seen[locale] = true
		locales = append(locales, locale)
	}
	return locales
}

func normalizeLocaleChoice(value string, allowed []string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	candidate := localization.Normalize(value, nil)
	for _, locale := range allowed {
		if strings.EqualFold(candidate, locale) {
			return locale, true
		}
	}
	return "", false
}

func normalizeHumanVerificationProvider(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", humanverify.ProviderDisabled:
		return humanverify.ProviderDisabled, true
	case humanverify.ProviderAltcha:
		return humanverify.ProviderAltcha, true
	default:
		return "", false
	}
}

func normalizeEnabledOption(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "true", "1", "yes", "on":
		return enabledOptionValue(true), true
	case "disabled", "false", "0", "no", "off":
		return enabledOptionValue(false), true
	default:
		return "", false
	}
}

func enabledOptionValue(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

func isEnabledOption(value string) bool {
	normalized, ok := normalizeEnabledOption(value)
	return ok && normalized == enabledOptionValue(true)
}

func parsePositiveDuration(value string) (time.Duration, bool) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	return duration, err == nil && duration > 0
}

func parsePositiveInt(value string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	return parsed, err == nil && parsed > 0
}

func parseBoundedInt(value string, min int, max int) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	return parsed, err == nil && parsed >= min && parsed <= max
}

func normalizeAltchaWidgetType(value string) (string, bool) {
	return normalizeChoice(value, altchaWidgetTypes)
}

func normalizeAltchaWidgetAuto(value string) (string, bool) {
	return normalizeChoice(value, altchaWidgetAutoModes)
}

func normalizeAltchaWidgetDisplay(value string) (string, bool) {
	return normalizeChoice(value, altchaWidgetDisplays)
}

func normalizeChoice(value string, allowed []string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, item := range allowed {
		if value == item {
			return item, true
		}
	}
	return "", false
}

func normalizeStringChoice(value string, allowed []string) (string, bool) {
	value = strings.TrimSpace(value)
	for _, item := range allowed {
		if strings.EqualFold(value, item) {
			return item, true
		}
	}
	return "", false
}

func normalizeForumSlug(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	return value, forumSlugPattern.MatchString(value)
}

func normalizeForumTagCreationMode(value string) (string, bool) {
	return normalizeChoice(value, forumTagCreationModes)
}

func normalizeBoundedInt(value string, min int, max int) (string, bool) {
	parsed, ok := parseBoundedInt(value, min, max)
	if !ok {
		return "", false
	}
	return strconv.Itoa(parsed), true
}

func normalizeFooterCopyright(value string) (string, bool) {
	value = strings.TrimSpace(value)
	return value, len([]rune(value)) <= footerCopyrightMaxRunes
}

func normalizeSEOTitleTemplate(value string) (string, bool) {
	return normalizeBoundedText(value, seoTitleTemplateMaxRunes)
}

func normalizeBoundedText(value string, maxRunes int) (string, bool) {
	value = strings.TrimSpace(value)
	return value, len([]rune(value)) <= maxRunes
}

func normalizeOptionalURL(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if !isValidURL(value) {
		return "", false
	}
	return value, true
}

func normalizeSEOTwitterCard(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, card := range seoTwitterCards {
		if value == card {
			return card, true
		}
	}
	return "", false
}

func normalizeSEOTwitterSite(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if len([]rune(value)) > seoTwitterSiteMaxRunes {
		return "", false
	}
	if strings.HasPrefix(value, "@") {
		value = strings.TrimPrefix(value, "@")
	}
	if value == "" {
		return "", true
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return "", false
		}
	}
	return "@" + value, true
}

func normalizeSEOVerificationToken(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > seoVerificationMaxRunes {
		return "", false
	}
	if value == "" {
		return "", true
	}
	for _, char := range value {
		if char <= ' ' || char == '<' || char == '>' || char == '"' || char == '\'' {
			return "", false
		}
	}
	return value, true
}

func normalizeSEORobotsPathList(value string) (string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if strings.TrimSpace(value) == "" {
		return "", true
	}

	normalized := make([]string, 0, len(lines))
	seen := map[string]bool{}
	for _, line := range lines {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		if !isValidRobotsPath(path) || seen[path] {
			return "", false
		}
		seen[path] = true
		normalized = append(normalized, path)
	}

	result := strings.Join(normalized, "\n")
	return result, len([]rune(result)) <= seoRobotsPathListMaxRunes
}
