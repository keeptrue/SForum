package options

import appearance "github.com/keeptrue/sforum/apps/api/app/Support/Appearance"

func normalizeAppearanceTheme(value string) (string, bool) {
	return appearance.NormalizeTheme(value)
}

func normalizeAppearanceLightBackground(value string) (string, bool) {
	return appearance.NormalizeLightBackground(value)
}

func coerceAppearanceOptions(values map[string]string, defaults map[string]string) {
	if value, ok := normalizeAppearanceTheme(values[NameAppearanceTheme]); ok {
		values[NameAppearanceTheme] = value
	} else {
		values[NameAppearanceTheme] = defaults[NameAppearanceTheme]
	}
	if value, ok := normalizeAppearanceLightBackground(values[NameAppearanceLightBackground]); ok {
		values[NameAppearanceLightBackground] = value
	} else {
		values[NameAppearanceLightBackground] = defaults[NameAppearanceLightBackground]
	}
}

func validateAppearanceOptions(values map[string]string) bool {
	if _, ok := normalizeAppearanceTheme(values[NameAppearanceTheme]); !ok {
		return false
	}
	_, ok := normalizeAppearanceLightBackground(values[NameAppearanceLightBackground])
	return ok
}
