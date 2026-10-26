package extensionmanifest

import (
	"encoding/json"
	"fmt"
	"strings"

	regioncatalog "github.com/keeptrue/sforum/apps/api/app/Support/RegionCatalog"
)

func normalizeContribution(contribution ManifestContribution) ManifestContribution {
	contribution.Point = strings.TrimSpace(contribution.Point)
	contribution.ID = NormalizeID(contribution.ID)
	contribution.Icon = strings.TrimSpace(contribution.Icon)
	contribution.EnabledBySetting = strings.TrimSpace(contribution.EnabledBySetting)
	if contribution.Label != nil {
		labels := make(map[string]string, len(contribution.Label))
		for locale, value := range contribution.Label {
			locale = strings.TrimSpace(locale)
			value = strings.TrimSpace(value)
			if locale != "" && value != "" {
				labels[locale] = value
			}
		}
		contribution.Label = labels
	}
	// 主题/评论行动作共用 extensionRoute 载荷形态（含 requiresAuth）。
	if (contribution.Point == PointForumTopicActions || contribution.Point == PointForumCommentActions) && len(contribution.Payload) > 0 {
		var payload TopicActionContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err == nil {
			payload.Type = strings.TrimSpace(payload.Type)
			payload.Method = strings.ToUpper(strings.TrimSpace(payload.Method))
			payload.Path = strings.TrimSpace(strings.ReplaceAll(payload.Path, "\\", "/"))
			if !strings.Contains(payload.Path, "://") {
				payload.Path = NormalizeRoutePath(payload.Path)
			}
			if normalized, err := json.Marshal(payload); err == nil {
				contribution.Payload = normalized
			}
		}
	}
	return contribution
}

func validateContributions(manifest Manifest, definitions []ContributionPointDefinition) error {
	points := make(map[string]ContributionPointDefinition, len(definitions))
	for _, definition := range definitions {
		if definition.ID == "" || definition.Kind != ContributionPointKindDescriptor {
			return fmt.Errorf("%w: contribution rule #1", ErrInvalidManifest)
		}
		if _, duplicate := points[definition.ID]; duplicate {
			return fmt.Errorf("%w: contribution rule #2", ErrInvalidManifest)
		}
		points[definition.ID] = definition
	}

	settingTypes := map[string]string{}
	for _, setting := range manifest.Settings {
		settingTypes[setting.Key] = setting.Type
	}

	seen := map[string]bool{}
	for _, contribution := range manifest.Contributions {
		definition, known := points[contribution.Point]
		if contribution.Point == "" || contribution.ID == "" || !known {
			return fmt.Errorf("%w: contribution rule #3", ErrInvalidManifest)
		}
		key := contribution.Point + ":" + contribution.ID
		if seen[key] {
			return fmt.Errorf("%w: contribution rule #4", ErrInvalidManifest)
		}
		seen[key] = true
		if contribution.Order < 0 {
			return fmt.Errorf("%w: contribution rule #5", ErrInvalidManifest)
		}
		if contribution.Icon != "" && !allowedContributionIcon(contribution.Icon) {
			return fmt.Errorf("%w: contribution rule #6", ErrInvalidManifest)
		}
		if len(contribution.Label) == 0 {
			return fmt.Errorf("%w: contribution rule #7", ErrInvalidManifest)
		}
		for locale, label := range contribution.Label {
			if strings.TrimSpace(locale) == "" || strings.TrimSpace(label) == "" {
				return fmt.Errorf("%w: contribution rule #8", ErrInvalidManifest)
			}
		}
		// enabledBySetting 必须指向本扩展声明的 boolean 设置键。
		if gate := strings.TrimSpace(contribution.EnabledBySetting); gate != "" {
			settingType, ok := settingTypes[gate]
			if !ok || settingType != "boolean" {
				return fmt.Errorf("%w: contribution rule #9", ErrInvalidManifest)
			}
		}
		if err := validateDescriptorContributionPayload(manifest, definition.PayloadType, contribution.Payload); err != nil {
			return err
		}
	}
	return nil
}

// ContributionEnabledBySetting 解析「贡献是否因设置门控而生效」。
// key 为空时始终生效；否则用已存值，缺省回落到 settings schema 的 default。
// 未声明的 key 或空默认按 false 处理（fail closed）。
func ContributionEnabledBySetting(manifest Manifest, stored map[string]string, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}
	value := ""
	if stored != nil {
		if raw, ok := stored[key]; ok {
			value = strings.TrimSpace(raw)
		}
	}
	if value == "" {
		for _, setting := range manifest.Settings {
			if setting.Key == key {
				value = strings.TrimSpace(setting.Default)
				break
			}
		}
	}
	return settingTruthy(value)
}

func settingTruthy(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on", "y":
		return true
	default:
		return false
	}
}

func allowedContributionIcon(icon string) bool {
	return strings.HasPrefix(icon, "i-lucide-") || strings.HasPrefix(icon, "i-tabler-")
}

// validateDescriptorContributionPayload 按 payloadType 校验宿主拥有的描述符（F4.3 / E2）。
// manifest 仅供需要交叉引用包内声明的 payload（如 regionPlacement 的 l2Widget）使用。
func validateDescriptorContributionPayload(manifest Manifest, payloadType string, raw json.RawMessage) error {
	switch payloadType {
	case PayloadTypeExtensionRoute:
		return validateTopicActionContributionPayload(raw)
	case PayloadTypeProfileSection:
		return validateProfileTabContributionPayload(raw)
	case PayloadTypeTopicSidebarCard:
		return validateTopicSidebarContributionPayload(raw)
	case PayloadTypeTopicBadge:
		return validateTopicBadgeContributionPayload(raw)
	case PayloadTypeNavItem:
		return validateNavItemContributionPayload(raw)
	case PayloadTypeDashboardLink:
		return validateDashboardWidgetContributionPayload(raw)
	case PayloadTypeHealthDescriptor:
		return validateHealthCheckContributionPayload(raw)
	case PayloadTypeRegionPlacement:
		return validateRegionPlacementContributionPayload(manifest, raw)
	default:
		return ErrInvalidManifest
	}
}

func validateRegionPlacementContributionPayload(manifest Manifest, raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload RegionPlacementContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	if len(payload.Pages) == 0 {
		return ErrInvalidManifest
	}
	region := strings.TrimSpace(payload.Region)
	seenPages := map[string]bool{}
	for _, page := range payload.Pages {
		page = strings.TrimSpace(page)
		if page == "" || seenPages[page] || !regioncatalog.Valid(page, region) {
			return ErrInvalidManifest
		}
		seenPages[page] = true
	}
	switch strings.TrimSpace(payload.Type) {
	case "hostLink":
		if payload.Method != "" || payload.Path != "" || payload.ComponentID != "" {
			return ErrInvalidManifest
		}
		if !safeHostLinkPath(payload.Href) {
			return ErrInvalidManifest
		}
		return nil
	case PayloadTypeExtensionRoute:
		if payload.Href != "" || payload.ComponentID != "" {
			return ErrInvalidManifest
		}
		switch strings.ToUpper(strings.TrimSpace(payload.Method)) {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return ErrInvalidManifest
		}
		if !safeContributionRoutePath(payload.Path) {
			return ErrInvalidManifest
		}
		return nil
	case PayloadRegionPlacementL2Widget:
		if payload.Method != "" || payload.Path != "" || payload.Href != "" {
			return ErrInvalidManifest
		}
		componentID := NormalizeID(payload.ComponentID)
		if componentID == "" {
			return ErrInvalidManifest
		}
		// 必须引用本 manifest 内的公开 L2 add 组件；放置声明不授予任何执行权，
		// 挂载时仍由公开 L2 运行时按信任授予权威裁决。
		for _, component := range manifest.Components {
			if NormalizeID(component.ID) != componentID {
				continue
			}
			if component.L2Component == "" || component.Action != ComponentActionAdd || component.Permission != "" {
				return ErrInvalidManifest
			}
			return nil
		}
		return ErrInvalidManifest
	default:
		return ErrInvalidManifest
	}
}

func validateTopicActionContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload TopicActionContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	if payload.Type != PayloadTypeExtensionRoute {
		return ErrInvalidManifest
	}
	// 主题动作与 composer 工具均允许安全写方法（GET 禁止，避免 CSRF 风格误触发）。
	switch strings.ToUpper(strings.TrimSpace(payload.Method)) {
	case "POST", "PUT", "PATCH", "DELETE":
	default:
		return ErrInvalidManifest
	}
	if !safeContributionRoutePath(payload.Path) {
		return ErrInvalidManifest
	}
	return nil
}

func validateProfileTabContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload ProfileTabContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	switch strings.TrimSpace(payload.Type) {
	case PayloadTypeExtensionRoute:
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		// 资料页 tab 导航以 GET 为主；仍允许写方法以便插件做「关注」类动作。
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return ErrInvalidManifest
		}
		if !safeContributionRoutePath(payload.Path) {
			return ErrInvalidManifest
		}
		return nil
	case "hostLink":
		if !safeHostLinkPath(payload.Href) {
			return ErrInvalidManifest
		}
		return nil
	default:
		return ErrInvalidManifest
	}
}

func validateTopicSidebarContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload TopicSidebarContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	// 与 profileSection 同形：卡片是导航/说明，默认 GET；写方法仍允许以便插件触发侧栏动作。
	switch strings.TrimSpace(payload.Type) {
	case PayloadTypeExtensionRoute:
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return ErrInvalidManifest
		}
		if !safeContributionRoutePath(payload.Path) {
			return ErrInvalidManifest
		}
		return nil
	case "hostLink":
		if !safeHostLinkPath(payload.Href) {
			return ErrInvalidManifest
		}
		return nil
	default:
		return ErrInvalidManifest
	}
}

func validateTopicBadgeContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload TopicBadgeContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	switch strings.TrimSpace(payload.Tone) {
	case "neutral", "info", "success", "warning", "danger":
	default:
		return ErrInvalidManifest
	}
	// 可选站内链接；空 href 表示纯展示徽章。
	if strings.TrimSpace(payload.Href) != "" && !safeHostLinkPath(payload.Href) {
		return ErrInvalidManifest
	}
	return nil
}

func validateNavItemContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload NavItemContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	switch strings.TrimSpace(payload.Type) {
	case PayloadTypeExtensionRoute:
		// 公开导航以 GET 打开扩展页；禁止写方法误作「导航」。
		if strings.ToUpper(strings.TrimSpace(payload.Method)) != "GET" {
			return ErrInvalidManifest
		}
		if !safeContributionRoutePath(payload.Path) {
			return ErrInvalidManifest
		}
		return nil
	case "hostLink":
		if !safePublicNavHostLink(payload.Href) {
			return ErrInvalidManifest
		}
		return nil
	default:
		return ErrInvalidManifest
	}
}

func validateDashboardWidgetContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload DashboardWidgetContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	if strings.TrimSpace(payload.Type) != "adminLink" {
		return ErrInvalidManifest
	}
	if !safeAdminDashboardRoute(payload.Route) {
		return ErrInvalidManifest
	}
	switch strings.TrimSpace(payload.Severity) {
	case "", "info", "success", "warning", "danger":
	default:
		return ErrInvalidManifest
	}
	return nil
}

func validateHealthCheckContributionPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return ErrInvalidManifest
	}
	var payload HealthCheckContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ErrInvalidManifest
	}
	switch strings.TrimSpace(payload.Type) {
	case "extensionRuntime", "static":
	default:
		return ErrInvalidManifest
	}
	component := strings.TrimSpace(payload.Component)
	// 组件名：小写字母数字与 ._-: ，避免与 core 名冲突时仍可读。
	if component == "" || len(component) > 80 {
		return ErrInvalidManifest
	}
	for _, r := range component {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == ':' {
			continue
		}
		return ErrInvalidManifest
	}
	return nil
}

func safeContributionRoutePath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || !strings.HasPrefix(value, "/") || value == "/" {
		return false
	}
	if strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	return value != "/api" && !strings.HasPrefix(value, "/api/")
}

// safeHostLinkPath 仅允许站内相对路径（公开页），禁止协议相对 // 与 /api。
func safeHostLinkPath(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return false
	}
	if strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	return value != "/api" && !strings.HasPrefix(value, "/api/")
}

// safePublicNavHostLink 公开顶栏导航：在 safeHostLinkPath 上再禁止 /admin。
func safePublicNavHostLink(value string) bool {
	if !safeHostLinkPath(value) {
		return false
	}
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	return value != "/admin" && !strings.HasPrefix(value, "/admin/")
}

// safeAdminDashboardRoute 管理端相对路由（admin shell 内 path）。
func safeAdminDashboardRoute(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return false
	}
	if strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	// 禁止跳出到公开 API 或绝对 URL。
	if value == "/api" || strings.HasPrefix(value, "/api/") {
		return false
	}
	return true
}
