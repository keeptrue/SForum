package options

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	crypto "github.com/keeptrue/sforum/apps/api/app/Support/Crypto"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	localization "github.com/keeptrue/sforum/apps/api/app/Support/Localization"
)

const defaultCacheTTL = 30 * time.Second
const RecommendedForumReadPolicyRefreshInterval = 5 * time.Second
const footerCopyrightMaxRunes = 200
const footerLinkLabelMaxRunes = 40
const seoTitleTemplateMaxRunes = 120
const seoDescriptionMaxRunes = 320
const seoKeywordsMaxRunes = 200
const seoVerificationMaxRunes = 120
const seoTwitterSiteMaxRunes = 80
const seoRobotsPathListMaxRunes = 1000
const altchaWidgetMinDurationMin = 0
const altchaWidgetMinDurationMax = 10000
const altchaWidgetWorkersMin = 1
const altchaWidgetWorkersMax = 16
const forumTagMaxPerTopicMin = 0
const forumTagMaxPerTopicMax = 10
const forumPaginationMin = 1
const forumPaginationMax = 100
const forumTitleMinRunesMin = 1
const forumTitleMinRunesMax = 200
const forumTitleMaxRunesMin = 1
const forumTitleMaxRunesMax = 200
const forumContentMinRunesMin = 0
const forumContentMinRunesMax = 200000
const forumContentMaxRunesMin = 1
const forumContentMaxRunesMax = 200000
const forumCommentMinRunesMin = 0
const forumCommentMinRunesMax = 50000
const forumCommentMaxRunesMin = 1
const forumCommentMaxRunesMax = 50000
const forumNestingMin = 0
const forumNestingMax = 20
const forumTreeDescendantsMin = 1
const forumTreeDescendantsMax = 100
const forumEditWindowMin = 0
const forumEditWindowMax = 10080
const forumCooldownMin = 0
const forumCooldownMax = 86400
const forumDailyLimitMin = 0
const forumDailyLimitMax = 10000
const forumExcerptMin = 40
const forumExcerptMax = 500
const passwordMinLengthMin = 8
const passwordMinLengthMax = 128
const passwordMaxLengthMin = 64
const passwordMaxLengthMax = 512

// 最大活跃设备数取值范围，与 identity.NormalizeMaxDevices 对齐。
const sessionsMaxDevicesMin = 1
const sessionsMaxDevicesMax = 20

// 历史会话保留天数取值范围（1-365）。
const sessionsKeepDaysMin = 1
const sessionsKeepDaysMax = 365

var builtInLocales = []string{localization.DefaultLocale, "en-US"}
var footerLinkKeys = []string{"terms", "privacy", "guidelines"}
var seoTwitterCards = []string{"summary", "summary_large_image"}
var altchaWidgetTypes = []string{"native", "checkbox", "switch"}
var altchaWidgetAutoModes = []string{"off", "onfocus", "onload", "onsubmit"}
var altchaWidgetDisplays = []string{"standard", "bar", "floating", "overlay", "invisible"}
var forumTagCreationModes = []string{"controlled", "review", "open"}
var forumSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type footerLinkLabels struct {
	ZHCN string `json:"zh-CN"`
	ENUS string `json:"en-US"`
}

type footerLinkOption struct {
	Key    string           `json:"key"`
	Labels footerLinkLabels `json:"labels"`
	URL    string           `json:"url"`
}

type Defaults struct {
	SiteName                  string
	SiteURL                   string
	DefaultLocale             string
	SupportedLocales          []string
	HumanVerificationProvider string
	AltchaSecret              string
	AltchaChallengeTTL        time.Duration
	AltchaCost                int
}

type optionDefinition struct {
	name             string
	public           bool
	secret           bool
	managePermission string
}

var optionDefinitions = append([]optionDefinition{
	{name: NameSiteName, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameSiteURL, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameSiteDefaultLocale, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameSiteSupportedLocales, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameHumanVerificationProvider, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaSecret, secret: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaChallengeTTL, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaCost, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetType, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetAuto, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetDisplay, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetHideLogo, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetHideFooter, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetWorkers, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAltchaWidgetMinDuration, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameAppearanceTheme, public: true, managePermission: identity.PermissionSettingsAppearanceManage},
	{name: NameAppearanceLightBackground, public: true, managePermission: identity.PermissionSettingsAppearanceManage},
	{name: NameFooterCopyrightZHCN, public: true, managePermission: identity.PermissionSettingsAppearanceManage},
	{name: NameFooterCopyrightENUS, public: true, managePermission: identity.PermissionSettingsAppearanceManage},
	{name: NameFooterLinks, public: true, managePermission: identity.PermissionSettingsAppearanceManage},
	{name: NameIdentityPasswordMinLength, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameIdentityPasswordMaxLength, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameIdentityPasswordRequireLowercase, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameIdentityPasswordRequireUppercase, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameIdentityPasswordRequireNumber, public: true, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameIdentityPasswordRequireSymbol, public: true, managePermission: identity.PermissionSettingsSiteManage},
	// 开放注册开关：public，默认开启；首用户 bootstrap 不受此开关限制。
	{name: NameIdentityRegistrationEnabled, public: true, managePermission: identity.PermissionSettingsSiteManage},
	// 最大活跃设备数：非 public（仅后端登录时读取，不暴露给前端），admin 通过 settings.manage 调整。
	{name: NameIdentitySessionsMaxDevices, public: false, managePermission: identity.PermissionSettingsSiteManage},
	// 历史会话保留天数：非 public，periodic job 据此清理。
	{name: NameIdentitySessionsKeepDays, public: false, managePermission: identity.PermissionSettingsSiteManage},
	{name: NameForumDefaultCategorySlug, public: true, managePermission: identity.PermissionCategoryManage},
	{name: NameForumTagCreationMode, public: true, managePermission: identity.PermissionTagManage},
	{name: NameForumTagPublicPages, public: true, managePermission: identity.PermissionTagManage},
	{name: NameForumTagMinPerTopic, public: true, managePermission: identity.PermissionTagManage},
	{name: NameForumTagMaxPerTopic, public: true, managePermission: identity.PermissionTagManage},
	{name: NameForumTopicsPerPage, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentsPerPage, public: true, managePermission: identity.PermissionForumSettingsManage},
	// 发帖/评论限制对前端 composer 公开，便于实时校验；冷却/每日上限也公开以便提示。
	{name: NameForumTopicTitleMinRunes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumTopicTitleMaxRunes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumTopicContentMinRunes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumTopicContentMaxRunes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumTopicEditWindowMinutes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumTopicCooldownSeconds, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumDailyTopicLimit, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentMinRunes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentMaxRunes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentMaxNestingDepth, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentsTreeDescendantsPerRoot, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentEditWindowMinutes, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumCommentCooldownSeconds, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumDailyCommentLimit, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameForumExcerptRuneLimit, public: true, managePermission: identity.PermissionForumSettingsManage},
	{name: NameSEOMetaTitleTemplate, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOMetaDescription, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOMetaKeywords, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOOGImageURL, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOTwitterCard, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOTwitterSite, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOAllowIndexing, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOGoogleVerification, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOBingVerification, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOBaiduVerification, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOYandexVerification, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEORobotsExtraAllow, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEORobotsExtraDisallow, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEORobotsBlockAIBots, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEORobotsBlockNonSEOBots, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSitemapEnabled, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSitemapIncludeStaticPages, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSitemapIncludeForumContent, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSchemaOrgEnabled, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSchemaOrgSearchAction, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSchemaOrgDiscussion, public: true, managePermission: identity.PermissionSEOManage},
	{name: NameSEOSchemaOrgOrganizationLogo, public: true, managePermission: identity.PermissionSEOManage},
	// 帖子 URL 形态：public（前端 SSR 需读取以拼接链接），SEO 管理权限可改。
	{name: NameSEOTopicURLMode, public: true, managePermission: identity.PermissionSEOManage},
	// 头像：allow_upload/default_provider/gravatar_base_url/max_size_kb/allow_gif/compress_enabled 对前端公开，
	// 用于客户端预校验上传；default_static_url/max_dimension/target_dimension/compress_quality 仅供后台管理。
	{name: NameAvatarAllowUpload, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarDefaultProvider, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarGravatarBaseURL, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarGravatarHashAlgorithm, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarDefaultStaticURL, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarMaxSizeKB, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarMaxDimension, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarAllowGIF, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarCompressEnabled, public: true, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarTargetDimension, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameAvatarCompressQuality, managePermission: identity.PermissionSettingsAvatarManage},
	{name: NameNotificationReplyInApp, managePermission: identity.PermissionSettingsMailManage},
	{name: NameNotificationReplyEmail, managePermission: identity.PermissionSettingsMailManage},
	{name: NameNotificationMentionInApp, managePermission: identity.PermissionSettingsMailManage},
	{name: NameNotificationMentionEmail, managePermission: identity.PermissionSettingsMailManage},
	{name: NameNotificationModerationInApp, managePermission: identity.PermissionSettingsMailManage},
	{name: NameNotificationModerationEmail, managePermission: identity.PermissionSettingsMailManage},
}, attachmentOptionDefinitions()...)

type Service struct {
	store    Store
	cacheTTL time.Duration
	defaults map[string]string
	// cipher 加密敏感 option 值（云存储/SSH/FTP 凭证），nil/透明时为明文（开发环境）。
	cipher *crypto.OptionCipher
	// auditor 写入 audit_events（F1.4）；nil 时跳过，不阻断设置保存。
	auditor audit.Writer

	mu        sync.RWMutex
	cached    map[string]string
	expiresAt time.Time
	// siteURLOverride 保留后台原始覆盖值；cached 中始终是解析后的有效公开地址。
	siteURLOverride string

	forumReadPolicy         *forumReadPolicySnapshot
	forumReadPolicyRevision uint64
}

type forumReadPolicySnapshot struct {
	guestRead            string
	softDeleteVisibility string
	expiresAt            time.Time
	revision             uint64
}

func NewService(store Store) *Service {
	return NewServiceWithDefaultsAndCacheTTL(store, Defaults{}, defaultCacheTTL)
}

func NewServiceWithDefaults(store Store, defaults Defaults) *Service {
	return NewServiceWithDefaultsAndCacheTTL(store, defaults, defaultCacheTTL)
}

func NewServiceWithCacheTTL(store Store, cacheTTL time.Duration) *Service {
	return NewServiceWithDefaultsAndCacheTTL(store, Defaults{}, cacheTTL)
}

func NewServiceWithDefaultsAndCacheTTL(store Store, defaults Defaults, cacheTTL time.Duration) *Service {
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	return &Service{
		store:    store,
		cacheTTL: cacheTTL,
		defaults: normalizedDefaults(defaults),
	}
}

// WithCipher 注入敏感值加密器（H2a）。返回 Service 自身以便链式调用。
// 未调用时 cipher 为 nil，敏感值以明文存储（开发环境兼容）。
func (s *Service) WithCipher(c *crypto.OptionCipher) *Service {
	s.cipher = c
	return s
}

// WithAuditor 注入审计写入器（F1.4 设置变更审计）。
func (s *Service) WithAuditor(w audit.Writer) *Service {
	s.auditor = w
	return s
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	defaults := s.defaultValues()
	rows, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		// 旧版本会把启动时的 APP_URL 物化为 site.url。值仍与当前环境一致时
		// 可安全迁回“未覆盖”；真正不同的运营自定义值必须保留。
		if normalizeName(row.Name) == NameSiteURL &&
			strings.TrimSpace(row.Value) == defaults[NameSiteURL] {
			if _, err := s.store.Upsert(ctx, UpdateInput{Name: NameSiteURL, Value: ""}); err != nil {
				return err
			}
			break
		}
	}
	for _, name := range allOptionNames() {
		// site.url 是可选覆盖值。缺失时由 defaults 中的 APP_URL 动态兜底，
		// 不把环境值固化进数据库，否则后续修改 APP_URL 不会生效。
		if name == NameSiteURL {
			continue
		}
		if err := s.store.InsertMissing(ctx, UpdateInput{Name: name, Value: defaults[name]}); err != nil {
			return err
		}
	}
	s.Invalidate()
	return nil
}

func (s *Service) List(ctx context.Context) ([]Option, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return nil, err
	}

	options := make([]Option, 0, len(optionDefinitions))
	for _, definition := range optionDefinitions {
		if !definition.public {
			continue
		}
		options = append(options, Option{Name: definition.name, Value: values[definition.name]})
	}
	return options, nil
}

func (s *Service) ListAdmin(ctx context.Context, actor identity.Actor) ([]AdminOption, error) {
	if !actorCanManageAnyOption(actor) {
		return nil, identity.ErrPermissionDenied
	}

	values, err := s.loadMap(ctx)
	if err != nil {
		return nil, err
	}
	return s.adminOptions(values, actor), nil
}

func (s *Service) Get(ctx context.Context, name string) (Option, error) {
	name = normalizeName(name)
	if !isPublicOption(name) {
		return Option{}, ErrInvalidOption
	}

	values, err := s.loadMap(ctx)
	if err != nil {
		return Option{}, err
	}
	return Option{Name: name, Value: values[name]}, nil
}

func (s *Service) WebOption(ctx context.Context, name string) (string, error) {
	option, err := s.Get(ctx, name)
	if err != nil {
		return "", err
	}
	return option.Value, nil
}

func (s *Service) SiteName(ctx context.Context) (string, error) {
	return s.WebOption(ctx, NameSiteName)
}

// AdminEmail 返回运营配置的站点管理员邮箱（admin-only，可为空）。
// 供 mail-test 默认收件人等内部路径使用；不走 WebOption/Get，避免公开选项校验拦截。
func (s *Service) AdminEmail(ctx context.Context) (string, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return "", err
	}
	return values[NameSiteAdminEmail], nil
}

func (s *Service) RuntimeSettings(ctx context.Context) (RuntimeSettings, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return RuntimeSettings{}, err
	}
	return RuntimeSettings{
		SiteName:                  values[NameSiteName],
		SiteURL:                   values[NameSiteURL],
		DefaultLocale:             values[NameSiteDefaultLocale],
		SupportedLocales:          parseStoredLocales(values[NameSiteSupportedLocales]),
		HumanVerificationProvider: values[NameHumanVerificationProvider],
	}, nil
}

func (s *Service) PasswordPolicy(ctx context.Context) (identity.PasswordPolicy, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return identity.PasswordPolicy{}, err
	}

	minLength, _ := strictAtoi(values[NameIdentityPasswordMinLength])
	maxLength, _ := strictAtoi(values[NameIdentityPasswordMaxLength])
	return identity.PasswordPolicy{
		MinLength:        minLength,
		MaxLength:        maxLength,
		RequireLowercase: isEnabledOption(values[NameIdentityPasswordRequireLowercase]),
		RequireUppercase: isEnabledOption(values[NameIdentityPasswordRequireUppercase]),
		RequireNumber:    isEnabledOption(values[NameIdentityPasswordRequireNumber]),
		RequireSymbol:    isEnabledOption(values[NameIdentityPasswordRequireSymbol]),
	}.Normalized(), nil
}

// RegistrationEnabled 返回运营配置的开放注册意图（不含 bootstrap 覆盖）。
// 身份服务会在“尚无任何用户”时强制允许注册，避免自建站锁死。
// mode=closed / invite / approval 时当前实现均视为关闭开放自助注册（invite/approval 完整流在后续波次）。
func (s *Service) RegistrationEnabled(ctx context.Context) (bool, error) {
	values, err := s.loadMap(ctx)
	if err != nil {
		return true, err
	}
	if mode, ok := normalizeRegistrationMode(values[NameIdentityRegistrationMode]); ok {
		if mode != "open" {
			return false, nil
		}
	}
	if value, ok := normalizeEnabledOption(values[NameIdentityRegistrationEnabled]); ok {
		return isEnabledOption(value), nil
	}
	return true, nil
}

type registrationPolicyTxStore interface {
	RegistrationPolicyValuesTx(context.Context, pgx.Tx) (map[string]string, error)
}

// RegistrationEnabledTx reads the authoritative operator policy in the caller's
// PostgreSQL transaction. It bypasses the process cache because the caller is
// about to create an account, role, link and audit record in that same boundary.
func (s *Service) RegistrationEnabledTx(ctx context.Context, tx pgx.Tx) (bool, error) {
	store, ok := s.store.(registrationPolicyTxStore)
	if !ok {
		return false, fmt.Errorf("options store does not support transactional registration policy")
	}
	rows, err := store.RegistrationPolicyValuesTx(ctx, tx)
	if err != nil {
		return false, err
	}
	values := s.defaultValues()
	for name, rawValue := range rows {
		name = normalizeName(name)
		if !registrationPolicyOptionName(name) {
			continue
		}
		if value, ok := normalizeOptionValue(name, rawValue); ok {
			values[name] = value
		}
	}
	values = coerceValueSet(s, values)
	if mode, ok := normalizeRegistrationMode(values[NameIdentityRegistrationMode]); ok && mode != "open" {
		return false, nil
	}
	if value, ok := normalizeEnabledOption(values[NameIdentityRegistrationEnabled]); ok {
		return isEnabledOption(value), nil
	}
	return true, nil
}

func (s *Service) InternalValues(ctx context.Context) (map[string]string, error) {
	return s.loadMap(ctx)
}

func (s *Service) Update(ctx context.Context, actor identity.Actor, input UpdateInput) (Option, error) {
	updated, err := s.UpdateMany(ctx, actor, []UpdateInput{input})
	if err != nil {
		return Option{}, err
	}

	name := normalizeName(input.Name)
	for _, item := range updated {
		if item.Name == name {
			return Option{Name: item.Name, Value: item.Value}, nil
		}
	}
	return Option{}, ErrInvalidOption
}

func (s *Service) UpdateMany(ctx context.Context, actor identity.Actor, inputs []UpdateInput) ([]AdminOption, error) {
	if !actorCanManageAnyOption(actor) {
		return nil, identity.ErrPermissionDenied
	}

	current, err := s.loadMap(ctx)
	if err != nil {
		return nil, err
	}
	merged := copyValues(current)
	pending := map[string]string{}

	for _, input := range inputs {
		name := normalizeName(input.Name)
		// 宿主内部 revision：仅 BumpPublicSurfaceRevision 可写，拒绝运营手改。
		if name == NamePublicSurfaceRevision {
			return nil, ErrInvalidOption
		}
		definition, ok := optionDefinitionFor(name)
		if !ok {
			return nil, ErrInvalidOption
		}
		if !actor.Can(definition.managePermission) {
			return nil, identity.ErrPermissionDenied
		}
		if isSecretOption(name) && strings.TrimSpace(input.Value) == "" {
			continue
		}

		value, ok := normalizeOptionValue(name, input.Value)
		if !ok {
			return nil, ErrInvalidOption
		}
		if name == NameSiteURL && value == "" {
			merged[name] = s.defaults[NameSiteURL]
		} else {
			merged[name] = value
		}
		pending[name] = value
	}

	if !isValidValueSet(merged) {
		return nil, ErrInvalidOption
	}

	storedInputs := make([]UpdateInput, 0, len(pending))
	changedNames := make([]string, 0, len(pending))
	for _, name := range allOptionNames() {
		value, ok := pending[name]
		if !ok {
			continue
		}
		// H2a：敏感值在写入前加密（数据库只存密文）。
		if isSecretOption(name) {
			encrypted, err := s.encryptValue(value)
			if err != nil {
				return nil, err
			}
			value = encrypted
		}
		storedInputs = append(storedInputs, UpdateInput{Name: name, Value: value})
		changedNames = append(changedNames, name)
	}
	// Only stores that explicitly support batching receive this path. The
	// PostgreSQL implementation makes registration enabled/mode atomic while
	// preserving the existing behavior of ordinary option stores.
	if batchStore, ok := s.store.(BatchUpsertStore); ok {
		if _, err := batchStore.UpsertMany(ctx, storedInputs); err != nil {
			return nil, err
		}
	} else {
		for _, input := range storedInputs {
			if _, err := s.store.Upsert(ctx, input); err != nil {
				return nil, err
			}
		}
	}
	// F1.4：敏感设置变更写审计（不记录密钥明文，仅名称列表）。
	if s.auditor != nil && len(changedNames) > 0 {
		_ = s.auditor.Append(ctx, audit.Event{
			ActorUserID: actor.ID,
			Action:      audit.ActionSettingsUpdate,
			Metadata: map[string]any{
				"names": changedNames,
				"count": len(changedNames),
			},
		})
	}
	// 写入后让缓存失效，下次读取重新从 DB 解密加载。
	s.mu.Lock()
	s.cached = nil
	s.siteURLOverride = ""
	s.mu.Unlock()

	s.Invalidate()
	values, err := s.loadMap(ctx)
	if err != nil {
		return nil, err
	}
	return s.adminOptions(values, actor), nil
}

func (s *Service) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cached = nil
	s.expiresAt = time.Time{}
	s.siteURLOverride = ""
	s.forumReadPolicy = nil
}

// RefreshForumReadPolicy 在后台/启动路径刷新只读策略快照。HTTP Guard 只读取
// ForumReadPolicySnapshot，绝不从请求热路径调用本方法。
func (s *Service) RefreshForumReadPolicy(ctx context.Context) error {
	_, err := s.loadMapFresh(ctx)
	return err
}

// RunForumReadPolicyRefresh 定期刷新底层 Options 缓存。快照到期而刷新失败时
// Guard 会保守关闭动态论坛读取路由，不使用无限期陈旧策略。
func (s *Service) RunForumReadPolicyRefresh(ctx context.Context, interval time.Duration) {
	if s == nil || ctx == nil {
		return
	}
	if interval <= 0 {
		interval = RecommendedForumReadPolicyRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.RefreshForumReadPolicy(ctx)
		}
	}
}

// ForumReadPolicySnapshot 返回当前不可变策略，不执行 I/O。返回 ok=false 表示
// 快照不存在或已过期，调用方必须 fail closed。
func (s *Service) ForumReadPolicySnapshot() (guestRead string, softDeleteVisibility string, revision uint64, ok bool) {
	if s == nil {
		return "", "", 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := s.forumReadPolicy
	if snapshot == nil || !time.Now().Before(snapshot.expiresAt) {
		return "", "", 0, false
	}
	return snapshot.guestRead, snapshot.softDeleteVisibility, snapshot.revision, true
}

func (s *Service) loadMap(ctx context.Context) (map[string]string, error) {
	return s.loadMapWithFreshness(ctx, false)
}

// loadMapFresh 绕过通用 Options 缓存，供后台策略刷新使用。这样每次成功刷新
// 都会延长快照有效期，不会在通用缓存边界产生周期性 fail-closed 窗口。
func (s *Service) loadMapFresh(ctx context.Context) (map[string]string, error) {
	return s.loadMapWithFreshness(ctx, true)
}

func (s *Service) loadMapWithFreshness(ctx context.Context, forceFresh bool) (map[string]string, error) {
	now := time.Now()

	s.mu.RLock()
	if !forceFresh && s.cached != nil && now.Before(s.expiresAt) {
		cached := copyValues(s.cached)
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !forceFresh && s.cached != nil && now.Before(s.expiresAt) {
		return copyValues(s.cached), nil
	}

	rows, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}

	values := s.defaultValues()
	siteURLOverride := ""
	for _, row := range rows {
		name := normalizeName(row.Name)
		if !isKnownOption(name) {
			continue
		}
		// H2a：敏感值在读取时解密（DB 存密文，内存/缓存存明文），在 normalize 之前解密以校验明文长度。
		rawValue := row.Value
		if isSecretOption(name) {
			rawValue = s.decryptValue(rawValue)
		}
		value, ok := normalizeOptionValue(name, rawValue)
		if ok {
			values[name] = value
			if name == NameSiteURL {
				siteURLOverride = value
			}
		}
	}
	values = coerceValueSet(s, values)

	s.cached = values
	s.siteURLOverride = siteURLOverride
	s.expiresAt = now.Add(s.cacheTTL)
	s.publishForumReadPolicyLocked(values, s.expiresAt)
	return copyValues(values), nil
}

func (s *Service) publishForumReadPolicyLocked(values map[string]string, expiresAt time.Time) {
	guestRead := strings.TrimSpace(values[NameForumGuestRead])
	softDeleteVisibility := strings.TrimSpace(values[NameForumCommentsSoftDeleteVisibility])
	if (guestRead != "public" && guestRead != "login_required") ||
		(softDeleteVisibility != "hidden" && softDeleteVisibility != "staff_only" && softDeleteVisibility != "author_and_staff") ||
		!time.Now().Before(expiresAt) {
		s.forumReadPolicy = nil
		return
	}
	s.forumReadPolicyRevision++
	s.forumReadPolicy = &forumReadPolicySnapshot{
		guestRead: guestRead, softDeleteVisibility: softDeleteVisibility,
		expiresAt: expiresAt, revision: s.forumReadPolicyRevision,
	}
}

func (s *Service) adminOptions(values map[string]string, actor identity.Actor) []AdminOption {
	s.mu.RLock()
	siteURLOverride := s.siteURLOverride
	s.mu.RUnlock()

	items := make([]AdminOption, 0, len(optionDefinitions))
	for _, definition := range optionDefinitions {
		if !actor.Can(definition.managePermission) {
			continue
		}
		value := values[definition.name]
		if definition.secret {
			items = append(items, AdminOption{
				Name:      definition.name,
				Public:    definition.public,
				Secret:    true,
				SecretSet: strings.TrimSpace(value) != "",
			})
			continue
		}
		if definition.name == NameSiteURL {
			overrideValue := siteURLOverride
			items = append(items, AdminOption{
				Name:          definition.name,
				Value:         values[definition.name],
				Public:        definition.public,
				OverrideValue: &overrideValue,
				FallbackValue: s.defaults[definition.name],
				Inherited:     siteURLOverride == "",
			})
			continue
		}
		items = append(items, AdminOption{
			Name:   definition.name,
			Value:  value,
			Public: definition.public,
		})
	}
	return items
}

func (s *Service) defaultValues() map[string]string {
	return copyValues(s.defaults)
}

func coerceValueSet(s *Service, values map[string]string) map[string]string {
	defaults := s.defaultValues()
	coerced := copyValues(values)

	if strings.TrimSpace(coerced[NameSiteName]) == "" {
		coerced[NameSiteName] = defaults[NameSiteName]
	}
	if !isValidURL(coerced[NameSiteURL]) {
		coerced[NameSiteURL] = defaults[NameSiteURL]
	}

	supported := parseStoredLocales(coerced[NameSiteSupportedLocales])
	if len(supported) == 0 {
		supported = parseStoredLocales(defaults[NameSiteSupportedLocales])
	}
	coerced[NameSiteSupportedLocales] = strings.Join(supported, ",")

	if locale, ok := normalizeLocaleChoice(coerced[NameSiteDefaultLocale], supported); ok {
		coerced[NameSiteDefaultLocale] = locale
	} else {
		coerced[NameSiteDefaultLocale] = supported[0]
	}

	// 时区/日期时间展示：无效值回退到推荐默认，避免脏数据阻断启动。
	coerceSiteDateTimeOptions(coerced, defaults)
	// 副标题/管理邮箱：无效值回退空串默认。
	coerceSiteIdentityOptions(coerced, defaults)
	// Wave 2 品牌资源与法律正文：无效值回退推荐默认。
	coerceSiteBrandOptions(coerced, defaults)
	// Wave 1 社区策略：注册/新人/维护/论坛阅读与行为。
	coerceCommunityPolicyOptions(coerced, defaults)
	// 客户端版本与昵称策略：脏数据回退到推荐默认。
	coercePolicyOptions(coerced, defaults)
	coerceMailResendOptions(coerced, defaults)

	coerceHumanVerificationOptions(coerced, defaults)
	coerceAppearanceOptions(coerced, defaults)
	if _, ok := normalizeFooterCopyright(coerced[NameFooterCopyrightZHCN]); !ok {
		coerced[NameFooterCopyrightZHCN] = defaults[NameFooterCopyrightZHCN]
	}
	if _, ok := normalizeFooterCopyright(coerced[NameFooterCopyrightENUS]); !ok {
		coerced[NameFooterCopyrightENUS] = defaults[NameFooterCopyrightENUS]
	}
	if _, ok := normalizeFooterLinks(coerced[NameFooterLinks]); !ok {
		coerced[NameFooterLinks] = defaults[NameFooterLinks]
	}
	coercePasswordPolicyOptions(coerced, defaults)
	if _, ok := normalizeForumSlug(coerced[NameForumDefaultCategorySlug]); !ok {
		coerced[NameForumDefaultCategorySlug] = defaults[NameForumDefaultCategorySlug]
	}
	if _, ok := normalizeForumTagCreationMode(coerced[NameForumTagCreationMode]); !ok {
		coerced[NameForumTagCreationMode] = defaults[NameForumTagCreationMode]
	}
	if value, ok := normalizeEnabledOption(coerced[NameForumTagPublicPages]); ok {
		coerced[NameForumTagPublicPages] = value
	} else {
		coerced[NameForumTagPublicPages] = defaults[NameForumTagPublicPages]
	}
	if _, ok := parseBoundedInt(coerced[NameForumTagMinPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); !ok {
		coerced[NameForumTagMinPerTopic] = defaults[NameForumTagMinPerTopic]
	}
	if _, ok := parseBoundedInt(coerced[NameForumTagMaxPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); !ok {
		coerced[NameForumTagMaxPerTopic] = defaults[NameForumTagMaxPerTopic]
	}
	// min/max 成对约束：配置错误时回退推荐默认，避免运营误配导致发帖全失败。
	if minTags, okMin := parseBoundedInt(coerced[NameForumTagMinPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); okMin {
		if maxTags, okMax := parseBoundedInt(coerced[NameForumTagMaxPerTopic], forumTagMaxPerTopicMin, forumTagMaxPerTopicMax); okMax && minTags > maxTags {
			coerced[NameForumTagMinPerTopic] = defaults[NameForumTagMinPerTopic]
			coerced[NameForumTagMaxPerTopic] = defaults[NameForumTagMaxPerTopic]
		}
	}
	for _, name := range []string{NameForumTopicsPerPage, NameForumCommentsPerPage} {
		if _, ok := parseBoundedInt(coerced[name], forumPaginationMin, forumPaginationMax); !ok {
			coerced[name] = defaults[name]
		}
	}
	coerceForumContentLimitOptions(coerced, defaults)
	if _, ok := parseBoundedInt(coerced[NameIdentitySessionsMaxDevices], sessionsMaxDevicesMin, sessionsMaxDevicesMax); !ok {
		coerced[NameIdentitySessionsMaxDevices] = defaults[NameIdentitySessionsMaxDevices]
	}
	if _, ok := parseBoundedInt(coerced[NameIdentitySessionsKeepDays], sessionsKeepDaysMin, sessionsKeepDaysMax); !ok {
		coerced[NameIdentitySessionsKeepDays] = defaults[NameIdentitySessionsKeepDays]
	}
	for _, name := range seoEnabledOptionNames() {
		value, ok := normalizeEnabledOption(coerced[name])
		if !ok {
			coerced[name] = defaults[name]
			continue
		}
		coerced[name] = value
	}
	if _, ok := normalizeSEOTitleTemplate(coerced[NameSEOMetaTitleTemplate]); !ok {
		coerced[NameSEOMetaTitleTemplate] = defaults[NameSEOMetaTitleTemplate]
	}
	if _, ok := normalizeBoundedText(coerced[NameSEOMetaDescription], seoDescriptionMaxRunes); !ok {
		coerced[NameSEOMetaDescription] = defaults[NameSEOMetaDescription]
	}
	if _, ok := normalizeBoundedText(coerced[NameSEOMetaKeywords], seoKeywordsMaxRunes); !ok {
		coerced[NameSEOMetaKeywords] = defaults[NameSEOMetaKeywords]
	}
	if _, ok := normalizeOptionalURL(coerced[NameSEOOGImageURL]); !ok {
		coerced[NameSEOOGImageURL] = defaults[NameSEOOGImageURL]
	}
	if _, ok := normalizeSEOTwitterCard(coerced[NameSEOTwitterCard]); !ok {
		coerced[NameSEOTwitterCard] = defaults[NameSEOTwitterCard]
	}
	if _, ok := normalizeSEOTwitterSite(coerced[NameSEOTwitterSite]); !ok {
		coerced[NameSEOTwitterSite] = defaults[NameSEOTwitterSite]
	}
	for _, name := range seoVerificationOptionNames() {
		if _, ok := normalizeSEOVerificationToken(coerced[name]); !ok {
			coerced[name] = defaults[name]
		}
	}
	if _, ok := normalizeSEORobotsPathList(coerced[NameSEORobotsExtraAllow]); !ok {
		coerced[NameSEORobotsExtraAllow] = defaults[NameSEORobotsExtraAllow]
	}
	if _, ok := normalizeSEORobotsPathList(coerced[NameSEORobotsExtraDisallow]); !ok {
		coerced[NameSEORobotsExtraDisallow] = defaults[NameSEORobotsExtraDisallow]
	}
	if _, ok := normalizeOptionalURL(coerced[NameSEOSchemaOrgOrganizationLogo]); !ok {
		coerced[NameSEOSchemaOrgOrganizationLogo] = defaults[NameSEOSchemaOrgOrganizationLogo]
	}
	if _, ok := normalizeChoice(coerced[NameSEOTopicURLMode], []string{"id_slug", "id", "slug"}); !ok {
		coerced[NameSEOTopicURLMode] = defaults[NameSEOTopicURLMode]
	}
	coerceAttachmentOptions(coerced, defaults)
	coerceAvatarOptions(coerced, defaults)
	coerceFeatureFlagOptions(coerced, defaults)
	coercePagesRegistryOptions(coerced, defaults)
	coercePublicSurfaceRevisionOptions(coerced, defaults)
	return coerced
}

func coercePasswordPolicyOptions(coerced, defaults map[string]string) {
	if _, ok := parseBoundedInt(coerced[NameIdentityPasswordMinLength], passwordMinLengthMin, passwordMinLengthMax); !ok {
		coerced[NameIdentityPasswordMinLength] = defaults[NameIdentityPasswordMinLength]
	}
	if _, ok := parseBoundedInt(coerced[NameIdentityPasswordMaxLength], passwordMaxLengthMin, passwordMaxLengthMax); !ok {
		coerced[NameIdentityPasswordMaxLength] = defaults[NameIdentityPasswordMaxLength]
	}
	minLength, minOK := strictAtoi(coerced[NameIdentityPasswordMinLength])
	maxLength, maxOK := strictAtoi(coerced[NameIdentityPasswordMaxLength])
	if !minOK || !maxOK || maxLength < minLength {
		coerced[NameIdentityPasswordMinLength] = defaults[NameIdentityPasswordMinLength]
		coerced[NameIdentityPasswordMaxLength] = defaults[NameIdentityPasswordMaxLength]
	}
	for _, name := range passwordPolicyBooleanOptionNames() {
		if value, ok := normalizeEnabledOption(coerced[name]); ok {
			coerced[name] = value
		} else {
			coerced[name] = defaults[name]
		}
	}
	if value, ok := normalizeEnabledOption(coerced[NameIdentityRegistrationEnabled]); ok {
		coerced[NameIdentityRegistrationEnabled] = value
	} else {
		coerced[NameIdentityRegistrationEnabled] = defaults[NameIdentityRegistrationEnabled]
	}
}

// coerceMailOptions 确保邮件选项始终落在推荐默认值上，避免无效 provider/encryption。
func normalizedDefaults(defaults Defaults) map[string]string {
	values := map[string]string{
		NameSiteName:                            "SForum",
		NameSiteURL:                             "http://127.0.0.1:3000",
		NameSiteDomain:                          "127.0.0.1:3000",
		NameSiteAboutURL:                        "",
		NameSiteAboutOpenInNewTab:               enabledOptionValue(false),
		NameSiteTagline:                         "",
		NameSiteAdminEmail:                      "",
		NameSiteDefaultLocale:                   localization.DefaultLocale,
		NameSiteSupportedLocales:                "zh-CN,en-US",
		NameSiteTimezone:                        recommendedSiteTimezone,
		NameSiteDateFormat:                      recommendedSiteDateFormat,
		NameSiteTimeFormat:                      recommendedSiteTimeFormat,
		NameSiteStartOfWeek:                     strconv.Itoa(recommendedSiteStartOfWeek),
		NameSystemUpdatesGitHubMirrorURL:        "",
		NameExtensionBuiltinAutoUpdate:          enabledOptionValue(true),
		NameHumanVerificationProvider:           humanverify.ProviderDisabled,
		NameHumanVerificationRegister:           enabledOptionValue(true),
		NameHumanVerificationPasswordReset:      enabledOptionValue(true),
		NameHumanVerificationEmailVerification:  enabledOptionValue(true),
		NameHumanVerificationLoginRisk:          enabledOptionValue(false),
		NameHumanVerificationPostRisk:           enabledOptionValue(false),
		NameHumanVerificationLoginAlways:        enabledOptionValue(false),
		NameHumanVerificationLoginNewDevice:     enabledOptionValue(false),
		NameHumanVerificationLoginPrivileged:    enabledOptionValue(false),
		NameHumanVerificationRateLimit:          strconv.Itoa(humanVerificationRateLimitDefault),
		NameAltchaSecret:                        "",
		NameAltchaChallengeTTL:                  (10 * time.Minute).String(),
		NameAltchaCost:                          "1000",
		NameAltchaWidgetType:                    "checkbox",
		NameAltchaWidgetAuto:                    "off",
		NameAltchaWidgetDisplay:                 "standard",
		NameAltchaWidgetHideLogo:                enabledOptionValue(true),
		NameAltchaWidgetHideFooter:              enabledOptionValue(true),
		NameAltchaWidgetWorkers:                 "2",
		NameAltchaWidgetMinDuration:             "500",
		NameAppearanceTheme:                     "pine_teal",
		NameAppearanceLightBackground:           "pure_white",
		NameFooterCopyrightZHCN:                 "© {year} {siteName}。保留所有权利。",
		NameFooterCopyrightENUS:                 "© {year} {siteName}. All rights reserved.",
		NameFooterLinks:                         defaultFooterLinksValue(),
		NameIdentityPasswordMinLength:           strconv.Itoa(identity.RecommendedPasswordMinLength),
		NameIdentityPasswordMaxLength:           strconv.Itoa(identity.RecommendedPasswordMaxLength),
		NameIdentityPasswordRequireLowercase:    enabledOptionValue(false),
		NameIdentityPasswordRequireUppercase:    enabledOptionValue(false),
		NameIdentityPasswordRequireNumber:       enabledOptionValue(false),
		NameIdentityPasswordRequireSymbol:       enabledOptionValue(false),
		NameIdentityRegistrationEnabled:         enabledOptionValue(true),
		NameIdentitySessionsMaxDevices:          strconv.Itoa(identity.RecommendedMaxDevices),
		NameIdentitySessionsKeepDays:            strconv.Itoa(identity.RecommendedSessionsKeepDays),
		NameForumDefaultCategorySlug:            "general",
		NameForumTagCreationMode:                "controlled",
		NameForumTagPublicPages:                 enabledOptionValue(true),
		NameForumTagMinPerTopic:                 "0",
		NameForumTagMaxPerTopic:                 "5",
		NameForumTopicsPerPage:                  "20",
		NameForumCommentsPerPage:                "20",
		NameForumTopicTitleMinRunes:             "2",
		NameForumTopicTitleMaxRunes:             "100",
		NameForumTopicContentMinRunes:           "0",
		NameForumTopicContentMaxRunes:           "50000",
		NameForumTopicEditWindowMinutes:         "0",
		NameForumTopicCooldownSeconds:           "0",
		NameForumDailyTopicLimit:                "0",
		NameForumCommentMinRunes:                "1",
		NameForumCommentMaxRunes:                "10000",
		NameForumCommentMaxNestingDepth:         "20",
		NameForumCommentsTreeDescendantsPerRoot: "50",
		NameForumCommentEditWindowMinutes:       "0",
		NameForumCommentCooldownSeconds:         "0",
		NameForumDailyCommentLimit:              "0",
		NameForumExcerptRuneLimit:               "180",
		NameSEOMetaTitleTemplate:                "",
		NameSEOMetaDescription:                  "",
		NameSEOMetaKeywords:                     "",
		NameSEOOGImageURL:                       "",
		NameSEOTwitterCard:                      "summary_large_image",
		NameSEOTwitterSite:                      "",
		NameSEOAllowIndexing:                    enabledOptionValue(true),
		NameSEOGoogleVerification:               "",
		NameSEOBingVerification:                 "",
		NameSEOBaiduVerification:                "",
		NameSEOYandexVerification:               "",
		NameSEORobotsExtraAllow:                 "",
		NameSEORobotsExtraDisallow:              "",
		NameSEORobotsBlockAIBots:                enabledOptionValue(false),
		NameSEORobotsBlockNonSEOBots:            enabledOptionValue(false),
		NameSEOSitemapEnabled:                   enabledOptionValue(true),
		NameSEOSitemapIncludeStaticPages:        enabledOptionValue(true),
		NameSEOSitemapIncludeForumContent:       enabledOptionValue(false),
		NameSEOSchemaOrgEnabled:                 enabledOptionValue(true),
		NameSEOSchemaOrgSearchAction:            enabledOptionValue(true),
		NameSEOSchemaOrgDiscussion:              enabledOptionValue(true),
		NameSEOSchemaOrgOrganizationLogo:        "",
		NameSEOTopicURLMode:                     "id_slug",
		// 邮件：开发默认 dev_log，配合 Mailpit/控制台调试；生产未配置 SMTP 时回退 noop。
		// 头像：默认 identicon（离线可用，符合"开箱即用"原则）；上传与压缩默认开启。
		NameAvatarAllowUpload:           enabledOptionValue(true),
		NameAvatarDefaultProvider:       AvatarProviderInitials,
		NameAvatarGravatarBaseURL:       "https://gravatar.com/avatar/",
		NameAvatarGravatarHashAlgorithm: AvatarHashSHA256,
		NameAvatarDefaultStaticURL:      "",
		NameAvatarMaxSizeKB:             strconv.Itoa(avatarMaxSizeKBDefault),
		NameAvatarMaxDimension:          strconv.Itoa(avatarMaxDimensionDefault),
		NameAvatarAllowGIF:              enabledOptionValue(false),
		NameAvatarCompressEnabled:       enabledOptionValue(true),
		NameAvatarTargetDimension:       strconv.Itoa(avatarTargetDimensionDefault),
		NameAvatarCompressQuality:       strconv.Itoa(avatarCompressQualityDefault),
		NameNotificationReplyInApp:      enabledOptionValue(true),
		NameNotificationReplyEmail:      enabledOptionValue(true),
		NameNotificationMentionInApp:    enabledOptionValue(true),
		NameNotificationMentionEmail:    enabledOptionValue(true),
		NameNotificationModerationInApp: enabledOptionValue(true),
		NameNotificationModerationEmail: enabledOptionValue(true),
	}
	mergeAttachmentDefaults(values)
	mergeCommunityPolicyDefaults(values)
	mergeClientVersionDefaults(values)
	mergeMailResendDefaults(values)
	mergeSiteBrandDefaults(values)
	mergeFeatureFlagDefaults(values)
	mergePagesRegistryDefaults(values)
	mergePublicSurfaceRevisionDefaults(values)
	for name, value := range seoRecommendedDefaults() {
		values[name] = value
	}
	applyDefaultsOverrides(values, defaults)

	return values
}
