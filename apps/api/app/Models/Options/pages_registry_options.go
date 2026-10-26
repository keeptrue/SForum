package options

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// Page Registry / runtime theme 开关，与 features.* 产品面开关正交。
const (
	NamePagesRegistryEnabled   = "pages.registry_enabled"
	NameThemesRuntimeL0Enabled = "themes.runtime_l0_enabled"
	NameThemesRuntimeL1Enabled = "themes.runtime_l1_enabled"
)

func init() {
	optionDefinitions = append(optionDefinitions, pagesRegistryOptionDefinitions()...)
}

func pagesRegistryOptionDefinitions() []optionDefinition {
	// 主题管理权限可读改这些运维开关；public 供前台 outlet 与布局决定是否走 registry。
	theme := identity.PermissionExtensionThemeManage
	return []optionDefinition{
		{name: NamePagesRegistryEnabled, public: true, managePermission: theme},
		{name: NameThemesRuntimeL0Enabled, public: true, managePermission: theme},
		{name: NameThemesRuntimeL1Enabled, public: true, managePermission: theme},
	}
}

func mergePagesRegistryDefaults(values map[string]string) {
	for name, value := range pagesRegistryRecommendedDefaults() {
		if _, exists := values[name]; !exists {
			values[name] = value
		}
	}
}

func pagesRegistryRecommendedDefaults() map[string]string {
	return map[string]string{
		NamePagesRegistryEnabled:   enabledOptionValue(true),
		NameThemesRuntimeL0Enabled: enabledOptionValue(true),
		NameThemesRuntimeL1Enabled: enabledOptionValue(true),
	}
}

func coercePagesRegistryOptions(coerced, defaults map[string]string) {
	for _, name := range []string{
		NamePagesRegistryEnabled,
		NameThemesRuntimeL0Enabled,
		NameThemesRuntimeL1Enabled,
	} {
		if value, ok := normalizeEnabledOption(coerced[name]); ok {
			coerced[name] = value
		} else {
			coerced[name] = defaults[name]
		}
	}
}

// PagesRegistryEnabled 前台/管理是否启用 Page Registry。
func (s *Service) PagesRegistryEnabled(ctx context.Context) (bool, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return false, err
	}
	return isEnabledOption(values[NamePagesRegistryEnabled]), nil
}

// ThemesRuntimeL0Enabled L0 皮肤无重建路径。
func (s *Service) ThemesRuntimeL0Enabled(ctx context.Context) (bool, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return false, err
	}
	return isEnabledOption(values[NameThemesRuntimeL0Enabled]), nil
}

// ThemesRuntimeL1Enabled L1 模板替换/新增。
func (s *Service) ThemesRuntimeL1Enabled(ctx context.Context) (bool, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return false, err
	}
	return isEnabledOption(values[NameThemesRuntimeL1Enabled]), nil
}
