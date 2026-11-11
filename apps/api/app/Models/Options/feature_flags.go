package options

import (
	"context"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// F4.5：站点级产品开关，与 RBAC 权限正交。
// 仅控制「产品面是否开启」，不授予任何操作权限。

const (
	// NameFeatureSearch 公开搜索入口与搜索 API 产品开关（引擎由 search.provider 提供）。
	NameFeatureSearch = "features.search"
	// NameFeatureRegistration 开放注册产品面（与 identity.registration.* 策略配合）。
	NameFeatureRegistration = "features.registration"
	// NameFeatureAttachments 附件上传产品面。
	NameFeatureAttachments = "features.attachments"
	// NameFeatureMentions @ 提及产品面。
	NameFeatureMentions = "features.mentions"
	// NameFeaturePublicProfiles 公开用户资料页。
	NameFeaturePublicProfiles = "features.public_profiles"
	// NameFeatureWebhooks 出站 Webhooks 产品面（管理端仍受权限约束）。
	NameFeatureWebhooks = "features.webhooks"
	// NameFeatureReactions 点赞（主题/评论）产品面；关闭后写端点 403、前端隐藏控件。
	NameFeatureReactions = "features.reactions"
	// NameFeatureBookmarks 收藏（主题/评论）产品面；关闭后写端点与名单端点 403。
	NameFeatureBookmarks = "features.bookmarks"
	// NameFeatureBadges 徽章产品面；关闭后徽章端点 403、前端隐藏徽章 UI，
	// 但活动统计的采集与资料页统计数字不受影响（统计与徽章是两层）。
	NameFeatureBadges = "features.badges"
	// NameFeatureTrustLevels 信任等级产品面；关闭后已挣得集合恒为空、限额回落到
	// 本刀之前的年龄两态（D9 的 F 组判据）。
	NameFeatureTrustLevels = "features.trust_levels"
)

// FeatureFlagDefinition 描述宿主目录中的一个产品开关。
type FeatureFlagDefinition struct {
	Name        string `json:"name"`
	Public      bool   `json:"public"`
	Default     string `json:"recommendedDefault"`
	Description string `json:"description"`
}

func init() {
	optionDefinitions = append(optionDefinitions, featureFlagOptionDefinitions()...)
}

func featureFlagOptionDefinitions() []optionDefinition {
	site := identity.PermissionSettingsSiteManage
	return []optionDefinition{
		{name: NameFeatureSearch, public: true, managePermission: site},
		{name: NameFeatureRegistration, public: true, managePermission: site},
		{name: NameFeatureAttachments, public: true, managePermission: site},
		{name: NameFeatureMentions, public: true, managePermission: site},
		{name: NameFeaturePublicProfiles, public: true, managePermission: site},
		// webhooks 对访客无意义，仅 admin 列表可见。
		{name: NameFeatureWebhooks, public: false, managePermission: site},
		{name: NameFeatureReactions, public: true, managePermission: site},
		{name: NameFeatureBookmarks, public: true, managePermission: site},
		{name: NameFeatureBadges, public: true, managePermission: site},
		{name: NameFeatureTrustLevels, public: true, managePermission: site},
	}
}

// FeatureFlagCatalog 返回宿主拥有的全部 features.* 定义（稳定顺序）。
func FeatureFlagCatalog() []FeatureFlagDefinition {
	defaults := featureFlagRecommendedDefaults()
	return []FeatureFlagDefinition{
		{Name: NameFeatureSearch, Public: true, Default: defaults[NameFeatureSearch], Description: "Public search product surface."},
		{Name: NameFeatureRegistration, Public: true, Default: defaults[NameFeatureRegistration], Description: "Open registration product surface."},
		{Name: NameFeatureAttachments, Public: true, Default: defaults[NameFeatureAttachments], Description: "Attachment upload product surface."},
		{Name: NameFeatureMentions, Public: true, Default: defaults[NameFeatureMentions], Description: "Mention product surface."},
		{Name: NameFeaturePublicProfiles, Public: true, Default: defaults[NameFeaturePublicProfiles], Description: "Public profile pages."},
		{Name: NameFeatureWebhooks, Public: false, Default: defaults[NameFeatureWebhooks], Description: "Outbound webhooks product surface."},
		{Name: NameFeatureReactions, Public: true, Default: defaults[NameFeatureReactions], Description: "Like reactions on topics and comments."},
		{Name: NameFeatureBookmarks, Public: true, Default: defaults[NameFeatureBookmarks], Description: "Bookmarks on topics and comments."},
		// features.badges 之前漏了这条（常量在、目录里没有），D16 顺手补齐：
		// 补齐不改变运行时行为，只是让后台开关目录与实现一致。
		{Name: NameFeatureBadges, Public: true, Default: defaults[NameFeatureBadges], Description: "Badges, grants, and profile badge walls."},
		{Name: NameFeatureTrustLevels, Public: true, Default: defaults[NameFeatureTrustLevels], Description: "Trust levels: derived levels, per-level limits, and earned capability mappings."},
	}
}

func featureFlagRecommendedDefaults() map[string]string {
	return map[string]string{
		NameFeatureSearch:         enabledOptionValue(true),
		NameFeatureRegistration:   enabledOptionValue(true),
		NameFeatureAttachments:    enabledOptionValue(true),
		NameFeatureMentions:       enabledOptionValue(true),
		NameFeaturePublicProfiles: enabledOptionValue(true),
		NameFeatureWebhooks:       enabledOptionValue(true),
		NameFeatureReactions:      enabledOptionValue(true),
		NameFeatureBookmarks:      enabledOptionValue(true),
		NameFeatureBadges:         enabledOptionValue(true),
		NameFeatureTrustLevels:    enabledOptionValue(true),
	}
}

func mergeFeatureFlagDefaults(values map[string]string) {
	for name, value := range featureFlagRecommendedDefaults() {
		if _, exists := values[name]; !exists {
			values[name] = value
		}
	}
}

func coerceFeatureFlagOptions(coerced, defaults map[string]string) {
	for _, def := range FeatureFlagCatalog() {
		if value, ok := normalizeEnabledOption(coerced[def.Name]); ok {
			coerced[def.Name] = value
		} else {
			coerced[def.Name] = defaults[def.Name]
		}
	}
}

// FeatureFlags 是运行时开关快照。
type FeatureFlags struct {
	Search         bool `json:"search"`
	Registration   bool `json:"registration"`
	Attachments    bool `json:"attachments"`
	Mentions       bool `json:"mentions"`
	PublicProfiles bool `json:"publicProfiles"`
	Webhooks       bool `json:"webhooks"`
	Reactions      bool `json:"reactions"`
	Bookmarks      bool `json:"bookmarks"`
	Badges         bool `json:"badges"`
	TrustLevels    bool `json:"trustLevels"`
}

// FeatureFlags 读取全部 features.*（含非 public）。
func (s *Service) FeatureFlags(ctx context.Context) (FeatureFlags, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return FeatureFlags{}, err
	}
	return featureFlagsFromValues(values), nil
}

func featureFlagsFromValues(values map[string]string) FeatureFlags {
	return FeatureFlags{
		Search:         isEnabledOption(values[NameFeatureSearch]),
		Registration:   isEnabledOption(values[NameFeatureRegistration]),
		Attachments:    isEnabledOption(values[NameFeatureAttachments]),
		Mentions:       isEnabledOption(values[NameFeatureMentions]),
		PublicProfiles: isEnabledOption(values[NameFeaturePublicProfiles]),
		Webhooks:       isEnabledOption(values[NameFeatureWebhooks]),
		Reactions:      isEnabledOption(values[NameFeatureReactions]),
		Bookmarks:      isEnabledOption(values[NameFeatureBookmarks]),
		Badges:         isEnabledOption(values[NameFeatureBadges]),
		TrustLevels:    isEnabledOption(values[NameFeatureTrustLevels]),
	}
}

// IsFeatureEnabled 判断目录内开关是否开启；未知 key 返回 false。
func (s *Service) IsFeatureEnabled(ctx context.Context, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if !isKnownFeatureFlag(name) {
		return false, nil
	}
	values, err := s.loadMap(ctx)
	if err != nil {
		return false, err
	}
	return isEnabledOption(values[name]), nil
}

// MissingRequiredFeatures 返回 requiresFeatures 中当前关闭的 key。
func (s *Service) MissingRequiredFeatures(ctx context.Context, required []string) ([]string, error) {
	if len(required) == 0 {
		return nil, nil
	}
	values, err := s.loadMap(ctx)
	if err != nil {
		return nil, err
	}
	var missing []string
	seen := map[string]bool{}
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if !isKnownFeatureFlag(name) || !isEnabledOption(values[name]) {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

// ListFeatureFlagsAdmin 管理端目录 + 当前值。
func (s *Service) ListFeatureFlagsAdmin(ctx context.Context, actor identity.Actor) ([]AdminOption, error) {
	if !actor.Can(identity.PermissionSettingsSiteManage) && !actor.Can(identity.PermissionSettingsManage) {
		return nil, identity.ErrPermissionDenied
	}
	values, err := s.loadMap(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AdminOption, 0, len(FeatureFlagCatalog()))
	for _, def := range FeatureFlagCatalog() {
		value := values[def.Name]
		if value == "" {
			value = def.Default
		}
		out = append(out, AdminOption{
			Name:   def.Name,
			Value:  value,
			Public: def.Public,
			Secret: false,
		})
	}
	return out, nil
}

// RestoreFeatureFlagDefaults 一键恢复全部 features.* 推荐默认。
func (s *Service) RestoreFeatureFlagDefaults(ctx context.Context, actor identity.Actor) ([]AdminOption, error) {
	if !actor.Can(identity.PermissionSettingsSiteManage) && !actor.Can(identity.PermissionSettingsManage) {
		return nil, identity.ErrPermissionDenied
	}
	defaults := featureFlagRecommendedDefaults()
	inputs := make([]UpdateInput, 0, len(defaults))
	for _, def := range FeatureFlagCatalog() {
		inputs = append(inputs, UpdateInput{Name: def.Name, Value: defaults[def.Name]})
	}
	if _, err := s.UpdateMany(ctx, actor, inputs); err != nil {
		return nil, err
	}
	return s.ListFeatureFlagsAdmin(ctx, actor)
}

func isKnownFeatureFlag(name string) bool {
	for _, def := range FeatureFlagCatalog() {
		if def.Name == name {
			return true
		}
	}
	return false
}
