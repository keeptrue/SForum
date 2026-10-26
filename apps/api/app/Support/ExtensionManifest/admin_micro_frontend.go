package extensionmanifest

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	AdminMicroFrontendAPIVersion = 1

	ContributionPointKindDescriptor = "descriptor"

	// AdminHostActionExtensionInstall 是宿主为受信任管理页提供的安装动作。
	// 动作由宿主执行并需要管理员当场确认，不是插件进程能力；它只暂存包，
	// 不启用、不信任。语义自首版起保持不变。
	AdminHostActionExtensionInstall = "extension.install"

	// AdminHostActionExtensionInstallEnable 在安装动作之上追加「装完由宿主按自身
	// 规则启用」：宿主确认后先完成安装，再由宿主既有的启用流程接管。它不是插件
	// 进程能力，也不跳过任何宿主门禁——可执行后端仍要 exact-artifact 的
	// super_admin 信任确认，其余判断沿用服务层策略。
	AdminHostActionExtensionInstallEnable = "extension.install.enable"

	// adminPageHostActionsMax 与清单 schema 的 maxItems 保持一致。
	adminPageHostActionsMax = 8
)

// EffectiveAdminPages 解析出真正生效的管理页列表：优先 admin.pages，
// 其次兼容旧字段 adminPages。归一化在返回前完成，调用方拿到的就是规范值。
func EffectiveAdminPages(manifest Manifest) []ManifestAdminPage {
	manifest = Normalize(manifest)
	if len(manifest.Admin.Pages) > 0 {
		return manifest.Admin.Pages
	}
	return manifest.AdminPages
}

// MenuAdminPages 只返回进入侧栏的页面。
func MenuAdminPages(manifest Manifest) []ManifestAdminPage {
	pages := EffectiveAdminPages(manifest)
	menuPages := make([]ManifestAdminPage, 0, len(pages))
	for _, page := range pages {
		if page.Menu {
			menuPages = append(menuPages, page)
		}
	}
	return menuPages
}

// AdminManagePath 是「管理此扩展」的落地路径。
func AdminManagePath(manifest Manifest) string {
	manifest = Normalize(manifest)
	pages := EffectiveAdminPages(manifest)
	if manifest.Admin.Entry != "" {
		return manifest.Admin.Entry
	}
	for _, page := range pages {
		if page.Path == "/settings" {
			return page.Path
		}
	}
	if len(pages) > 0 {
		return pages[0].Path
	}
	return "/about"
}

// AdminHostActions 返回宿主支持的全部页面级动作 ID，用于清单校验与披露。
// 顺序稳定且旧动作在前：披露与快照按此顺序渲染，调用方可以安全修改返回值。
func AdminHostActions() []string {
	return []string{AdminHostActionExtensionInstall, AdminHostActionExtensionInstallEnable}
}

// knownAdminHostAction 是动作词表的唯一判定入口；新增动作必须同时登记在这里
// 与 AdminHostActions，admin_host_actions_test.go 会校验两者一致。
func knownAdminHostAction(value string) bool {
	switch value {
	case AdminHostActionExtensionInstall, AdminHostActionExtensionInstallEnable:
		return true
	default:
		return false
	}
}

// validateAdminHostActions 只允许 component 页面声明动作，且动作 ID 必须已知。
// 动作是宿主提供的能力，声明在非组件页上没有任何承载方，因此直接拒绝清单。
func validateAdminHostActions(page ManifestAdminPage) error {
	if len(page.HostActions) == 0 {
		return nil
	}
	if page.View != "component" {
		return fmt.Errorf("%w: admin host actions require view=component (%s)", ErrInvalidManifest, page.Path)
	}
	if len(page.HostActions) > adminPageHostActionsMax {
		return fmt.Errorf("%w: admin host actions exceed %d entries (%s)", ErrInvalidManifest, adminPageHostActionsMax, page.Path)
	}
	for _, action := range page.HostActions {
		if !knownAdminHostAction(action) {
			return fmt.Errorf("%w: unknown admin host action %q (%s)", ErrInvalidManifest, action, page.Path)
		}
	}
	return nil
}

// normalizeAdminHostActions 去空白、转小写并去重，同时保留作者顺序。
func normalizeAdminHostActions(actions []string) []string {
	if len(actions) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(actions))
	seen := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		value := NormalizeID(action)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

var adminComponentIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,80}$`)

// AdminComponent is one author-prebuilt browser module mounted inside a
// Host-owned admin surface. Settings and ordinary admin pages share this exact
// artifact contract, but expose different bridge capabilities at runtime.
type AdminComponent struct {
	ID         string `json:"id"`
	APIVersion int    `json:"apiVersion"`
	Entry      string `json:"entry,omitempty"`
	CSS        string `json:"css,omitempty"`
}

type AdminComponentBinding struct {
	Surface   string
	PagePath  string
	Component AdminComponent
}

// DeclaredAdminComponents returns every executable admin browser surface in a
// deterministic order for trust impact, digest, and package validation.
func DeclaredAdminComponents(manifest Manifest) []AdminComponentBinding {
	bindings := make([]AdminComponentBinding, 0, len(manifest.Admin.Pages))
	if component := manifest.SettingsDocument.UI.Component; component != nil && component.Entry != "" {
		bindings = append(bindings, AdminComponentBinding{Surface: "settings", Component: *component})
	}
	for _, page := range EffectiveAdminPages(manifest) {
		if page.View != "component" || page.Component == nil || page.Component.Entry == "" {
			continue
		}
		bindings = append(bindings, AdminComponentBinding{
			Surface: "page:" + page.Path, PagePath: page.Path, Component: *page.Component,
		})
	}
	sort.Slice(bindings, func(left, right int) bool {
		if bindings[left].Surface == bindings[right].Surface {
			return bindings[left].Component.ID < bindings[right].Component.ID
		}
		return bindings[left].Surface < bindings[right].Surface
	})
	return bindings
}

func normalizeAdminComponent(component *AdminComponent) {
	if component == nil {
		return
	}
	component.ID = NormalizeID(component.ID)
	component.Entry = normalizeAdminRelativePath(component.Entry)
	component.CSS = normalizeAdminRelativePath(component.CSS)
}

func validAdminComponent(component *AdminComponent) bool {
	if component == nil || !adminComponentIDPattern.MatchString(component.ID) ||
		component.APIVersion != AdminMicroFrontendAPIVersion || component.Entry == "" {
		return false
	}
	if !safeAdminRelativePath(component.Entry) || path.Ext(component.Entry) != ".mjs" ||
		!strings.HasPrefix(component.Entry, "frontend/admin/dist/") {
		return false
	}
	return component.CSS == "" || (safeAdminRelativePath(component.CSS) &&
		path.Ext(component.CSS) == ".css" && strings.HasPrefix(component.CSS, "frontend/admin/dist/"))
}

func normalizeAdminPageSlice(pages []ManifestAdminPage) {
	for index := range pages {
		pages[index].Path = NormalizeRoutePath(pages[index].Path)
		pages[index].Label = strings.TrimSpace(pages[index].Label)
		pages[index].Description = strings.TrimSpace(pages[index].Description)
		pages[index].Icon = strings.TrimSpace(pages[index].Icon)
		pages[index].View = strings.ToLower(strings.TrimSpace(pages[index].View))
		if pages[index].View == "" {
			pages[index].View = "about"
		}
		pages[index].Permission = strings.TrimSpace(pages[index].Permission)
		pages[index].HostActions = normalizeAdminHostActions(pages[index].HostActions)
		normalizeAdminComponent(pages[index].Component)
	}
}

func validateAdminDeclaration(manifest Manifest) error {
	pages := EffectiveAdminPages(manifest)
	componentIDs := map[string]struct{}{}
	if component := manifest.SettingsDocument.UI.Component; component != nil && component.Entry != "" {
		componentIDs[component.ID] = struct{}{}
	}
	for _, page := range pages {
		if page.Path == "" || !strings.HasPrefix(page.Path, "/") || strings.Contains(page.Path, "..") || page.Label == "" {
			return fmt.Errorf("%w: admin declaration rule #1 (line ~3285)", ErrInvalidManifest)
		}
		if page.View != "" && page.View != "about" && page.View != "settings" && page.View != "component" {
			return fmt.Errorf("%w: admin declaration rule #2 (line ~3288)", ErrInvalidManifest)
		}
		if page.View == "component" {
			if !validAdminComponent(page.Component) {
				return fmt.Errorf("%w: admin declaration rule #3 (line ~3292)", ErrInvalidManifest)
			}
			if _, exists := componentIDs[page.Component.ID]; exists {
				return fmt.Errorf("%w: admin declaration rule #4 (line ~3295)", ErrInvalidManifest)
			}
			componentIDs[page.Component.ID] = struct{}{}
		} else if page.Component != nil {
			return fmt.Errorf("%w: admin declaration rule #5 (line ~3299)", ErrInvalidManifest)
		}
		if page.Order < 0 {
			return fmt.Errorf("%w: admin declaration rule #6 (line ~3302)", ErrInvalidManifest)
		}
		if err := validateAdminHostActions(page); err != nil {
			return err
		}
	}
	if manifest.Admin.Entry == "" {
		return nil
	}
	if strings.Contains(manifest.Admin.Entry, "://") || !strings.HasPrefix(manifest.Admin.Entry, "/") || strings.Contains(manifest.Admin.Entry, "..") {
		return fmt.Errorf("%w: admin declaration rule #7 (line ~3309)", ErrInvalidManifest)
	}
	if manifest.Admin.Entry == "/about" {
		return nil
	}
	for _, page := range pages {
		if page.Path == manifest.Admin.Entry {
			return nil
		}
	}
	return fmt.Errorf("%w: admin declaration rule #8 (line ~3319)", ErrInvalidManifest)
}

// ValidateWithContributionPoints validates a manifest against the host-owned descriptor catalog.
func ValidateWithContributionPoints(manifest Manifest, points []ContributionPointDefinition) error {
	return validateManifest(manifest, points)
}

func normalizeAdminRelativePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	return path.Clean(value)
}

func safeAdminRelativePath(value string) bool {
	if value == "" || value == "." || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return false
	}
	if len(value) >= 2 && value[1] == ':' && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}
