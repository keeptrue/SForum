package themecompiler

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

var schemaVersionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
var viewModelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var routeParamNamePattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

type registeredPageViewModel struct {
	descriptor PageViewModelSchema
	modelType  reflect.Type
}

// PageViewModelRegistry is immutable after construction and contains only
// Host-reviewed core schemas. Plugin-defined business contracts need their own
// typed adapter; they cannot register an arbitrary Go type or map here.
type PageViewModelRegistry struct {
	schemas map[string]registeredPageViewModel
}

var (
	coreViewModelsOnce sync.Once
	coreViewModels     *PageViewModelRegistry
)

func CorePageViewModelRegistry() *PageViewModelRegistry {
	coreViewModelsOnce.Do(func() {
		registry, err := newCorePageViewModelRegistry()
		if err != nil {
			panic(err)
		}
		coreViewModels = registry
	})
	return coreViewModels
}

func ValidateCorePageViewModelCatalog() error {
	_, err := newCorePageViewModelRegistry()
	return err
}

func newCorePageViewModelRegistry() (*PageViewModelRegistry, error) {
	definitions := []registeredPageViewModel{
		coreViewModel("forum.home", "sforum.page.home@1", ViewModelHome, HomePageViewModel{}),
		coreViewModel("forum.search", "sforum.page.search@1", ViewModelHome, HomePageViewModel{}),
		coreViewModel("forum.category.index", "sforum.page.category_index@1", ViewModelList, CategoryIndexPageViewModel{}),
		coreViewModel("forum.category.show", "sforum.page.category_show@1", ViewModelList, CategoryShowPageViewModel{}),
		coreViewModel("forum.tag.index", "sforum.page.tag_index@1", ViewModelList, TagIndexPageViewModel{}),
		coreViewModel("forum.tag.show", "sforum.page.tag_show@1", ViewModelList, TagShowPageViewModel{}),
		coreViewModel("forum.badge.index", "sforum.page.badge_index@1", ViewModelList, BadgeIndexPageViewModel{}),
		coreViewModel("forum.topic.show", "sforum.page.topic_show@1", ViewModelDetail, TopicDetailPageViewModel{}),
		coreViewModel("forum.topic.create", "sforum.page.topic_create@1", ViewModelCreate, TopicCreatePageViewModel{}),
		coreViewModel("forum.topic.reply", "sforum.page.topic_reply@1", ViewModelCreate, TopicReplyPageViewModel{}),
		coreViewModel("forum.topic.edit", "sforum.page.topic_edit@1", ViewModelCreate, TopicEditPageViewModel{}),
		coreViewModel("forum.profile.show", "sforum.page.profile_show@1", ViewModelProfile, ProfilePageViewModel{}),
		coreViewModel("forum.settings.profile", "sforum.page.settings_profile@1", ViewModelSettings, ProfileSettingsPageViewModel{}),
		coreViewModel("forum.settings.appearance", "sforum.page.settings_appearance@1", ViewModelSettings, AppearanceSettingsPageViewModel{}),
		coreViewModel("forum.settings.login_methods", "sforum.page.settings_login_methods@1", ViewModelSettings, LoginMethodsSettingsPageViewModel{}),
		coreViewModel("forum.settings.password", "sforum.page.settings_password@1", ViewModelSettings, LocalPasswordSettingsPageViewModel{}),
		coreViewModel("forum.settings.security", "sforum.page.settings_security@1", ViewModelSettings, SecuritySettingsPageViewModel{}),
		coreViewModel("forum.settings.tokens", "sforum.page.settings_tokens@1", ViewModelSettings, PersonalAccessTokensPageViewModel{}),
		coreViewModel("forum.settings.notifications", "sforum.page.settings_notifications@1", ViewModelSettings, NotificationSettingsPageViewModel{}),
		coreViewModel("forum.notifications", "sforum.page.notifications@1", ViewModelNotifications, NotificationsPageViewModel{}),
		coreViewModel("forum.notification.show", "sforum.page.notification_show@1", ViewModelNotifications, NotificationsPageViewModel{}),
		coreViewModel("moderation.review", "sforum.page.moderation_review@1", ViewModelModeration, ModerationReviewPageViewModel{}),
		coreViewModel("auth.login", "sforum.page.login@1", ViewModelAuth, LoginPageViewModel{}),
		coreViewModel("auth.register", "sforum.page.register@1", ViewModelAuth, RegisterPageViewModel{}),
		coreViewModel("auth.external_continuation", "sforum.page.external_auth_continuation@1", ViewModelAuth, ExternalAuthContinuationPageViewModel{}),
		coreViewModel("auth.email_verification", "sforum.page.email_verification@1", ViewModelAuth, EmailVerificationPageViewModel{}),
		coreViewModel("auth.forgot_password", "sforum.page.forgot_password@1", ViewModelAuth, ForgotPasswordPageViewModel{}),
		coreViewModel("auth.reset_password", "sforum.page.reset_password@1", ViewModelAuth, ResetPasswordPageViewModel{}),
		coreViewModel("site.terms", "sforum.page.terms@1", ViewModelLegal, TermsPageViewModel{}),
		coreViewModel("site.privacy", "sforum.page.privacy@1", ViewModelLegal, PrivacyPageViewModel{}),
		coreViewModel("site.guidelines", "sforum.page.guidelines@1", ViewModelLegal, GuidelinesPageViewModel{}),
		coreViewModel("system.forbidden", "sforum.page.forbidden@1", ViewModelError, ErrorPageViewModel{}),
		coreViewModel("system.not_found", "sforum.page.not_found@1", ViewModelError, ErrorPageViewModel{}),
		coreViewModel("system.rate_limited", "sforum.page.rate_limited@1", ViewModelError, ErrorPageViewModel{}),
		coreViewModel("system.server_error", "sforum.page.server_error@1", ViewModelError, ErrorPageViewModel{}),
		coreViewModel("site.outbound", "sforum.page.outbound@1", ViewModelUtility, OutboundPageViewModel{}),
		coreViewModel("dev.components", "sforum.page.dev_components@1", ViewModelDevelopment, DevelopmentComponentsPageViewModel{}),
	}
	registry := &PageViewModelRegistry{schemas: make(map[string]registeredPageViewModel, len(definitions))}
	for _, item := range definitions {
		if !viewModelIDPattern.MatchString(item.descriptor.PageID) || !schemaVersionPattern.MatchString(item.descriptor.SchemaVersion) {
			return nil, fmt.Errorf("%w: invalid catalog identity", ErrViewModelSchema)
		}
		if item.descriptor.Kind == "" || item.modelType.Kind() != reflect.Struct {
			return nil, fmt.Errorf("%w: invalid model for %s", ErrViewModelSchema, item.descriptor.PageID)
		}
		if err := validateReviewedViewModelType(item.modelType, map[reflect.Type]bool{}); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrViewModelSchema, item.descriptor.PageID, err)
		}
		if _, exists := registry.schemas[item.descriptor.PageID]; exists {
			return nil, fmt.Errorf("%w: duplicate page %s", ErrViewModelSchema, item.descriptor.PageID)
		}
		registry.schemas[item.descriptor.PageID] = item
	}
	return registry, nil
}

func coreViewModel(pageID, schemaVersion string, kind PageViewModelKind, sample any) registeredPageViewModel {
	return registeredPageViewModel{
		descriptor: PageViewModelSchema{PageID: pageID, SchemaVersion: schemaVersion, Kind: kind},
		modelType:  reflect.TypeOf(sample),
	}
}

func (r *PageViewModelRegistry) Catalog() []PageViewModelSchema {
	if r == nil {
		return nil
	}
	result := make([]PageViewModelSchema, 0, len(r.schemas))
	for _, item := range r.schemas {
		result = append(result, item.descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PageID < result[j].PageID })
	return result
}

// Bind validates the exact schema type and seals it to the theme artifact that
// will render it. The digest is checked again by Snapshot.Render.
func (r *PageViewModelRegistry) Bind(
	pageID, schemaVersion, themePackageDigest string,
	value any,
) (BoundPageViewModel, error) {
	if r == nil || !canonicalDigestPattern.MatchString(themePackageDigest) {
		return BoundPageViewModel{}, ErrViewModelTheme
	}
	item, ok := r.schemas[strings.TrimSpace(pageID)]
	if !ok || item.descriptor.SchemaVersion != strings.TrimSpace(schemaVersion) {
		return BoundPageViewModel{}, ErrViewModelSchema
	}
	if reflect.TypeOf(value) != item.modelType {
		return BoundPageViewModel{}, fmt.Errorf("%w: %s requires %s, got %T", ErrInvalidViewModel, pageID, item.modelType, value)
	}
	if err := validateCorePageViewModel(item.descriptor, value); err != nil {
		return BoundPageViewModel{}, err
	}
	sealed, err := cloneReviewedViewModel(reflect.ValueOf(value))
	if err != nil {
		return BoundPageViewModel{}, err
	}
	sealedValue := sealed.Interface()
	base, ok := pageViewModelBase(sealedValue)
	if !ok {
		return BoundPageViewModel{}, ErrInvalidViewModel
	}
	return BoundPageViewModel{
		pageID: item.descriptor.PageID, schemaVersion: item.descriptor.SchemaVersion,
		themePackageDigest: themePackageDigest, value: sealedValue, seo: base.SEO,
	}, nil
}

func validateCorePageViewModel(schema PageViewModelSchema, value any) error {
	base, ok := pageViewModelBase(value)
	if !ok {
		return ErrInvalidViewModel
	}
	if base.PageID != schema.PageID || base.SchemaVersion != schema.SchemaVersion {
		return ErrViewModelSchema
	}
	if strings.TrimSpace(base.Locale) == "" || !strings.HasPrefix(base.Route.Path, "/") {
		return fmt.Errorf("%w: locale and absolute route path are required", ErrInvalidViewModel)
	}
	if err := validateViewerState(base.Viewer); err != nil {
		return err
	}
	if err := validateRouteParams(base.Route.Params); err != nil {
		return err
	}
	if err := validateSEOView(base.SEO); err != nil {
		return err
	}
	if err := validatePageSpecificBoundaries(value); err != nil {
		return err
	}
	return validatePassiveViewModel(value)
}

func pageViewModelBase(value any) (PageViewModelBase, bool) {
	switch model := value.(type) {
	case HomePageViewModel:
		return model.Base, true
	case CategoryIndexPageViewModel:
		return model.Base, true
	case CategoryShowPageViewModel:
		return model.Base, true
	case TagIndexPageViewModel:
		return model.Base, true
	case TagShowPageViewModel:
		return model.Base, true
	case BadgeIndexPageViewModel:
		return model.Base, true
	case TopicDetailPageViewModel:
		return model.Base, true
	case ProfilePageViewModel:
		return model.Base, true
	case TopicCreatePageViewModel:
		return model.Base, true
	case TopicReplyPageViewModel:
		return model.Base, true
	case TopicEditPageViewModel:
		return model.Base, true
	case ProfileSettingsPageViewModel:
		return model.Base, true
	case AppearanceSettingsPageViewModel:
		return model.Base, true
	case LoginMethodsSettingsPageViewModel:
		return model.Base, true
	case LocalPasswordSettingsPageViewModel:
		return model.Base, true
	case SecuritySettingsPageViewModel:
		return model.Base, true
	case PersonalAccessTokensPageViewModel:
		return model.Base, true
	case NotificationSettingsPageViewModel:
		return model.Base, true
	case NotificationsPageViewModel:
		return model.Base, true
	case ModerationReviewPageViewModel:
		return model.Base, true
	case LoginPageViewModel:
		return model.Base, true
	case RegisterPageViewModel:
		return model.Base, true
	case ExternalAuthContinuationPageViewModel:
		return model.Base, true
	case EmailVerificationPageViewModel:
		return model.Base, true
	case ForgotPasswordPageViewModel:
		return model.Base, true
	case ResetPasswordPageViewModel:
		return model.Base, true
	case TermsPageViewModel:
		return model.Base, true
	case PrivacyPageViewModel:
		return model.Base, true
	case GuidelinesPageViewModel:
		return model.Base, true
	case ErrorPageViewModel:
		return model.Base, true
	case DevelopmentComponentsPageViewModel:
		return model.Base, true
	case OutboundPageViewModel:
		return model.Base, true
	default:
		return PageViewModelBase{}, false
	}
}

func validatePageSpecificBoundaries(value any) error {
	var form HostFormBoundary
	var expectedComponent string
	var expectedRoutes []string
	switch model := value.(type) {
	case TopicCreatePageViewModel:
		form, expectedComponent = model.Form, "forum.component.topic_composer"
		expectedRoutes = []string{"core.route.forum.create_topic"}
	case TopicReplyPageViewModel:
		form, expectedComponent = model.Form, "forum.component.topic_reply"
		expectedRoutes = []string{"core.route.forum.create_comment"}
	case TopicEditPageViewModel:
		form, expectedComponent = model.Form, "forum.component.topic_editor"
		expectedRoutes = []string{"core.route.forum.update_topic"}
	case ProfileSettingsPageViewModel:
		form, expectedComponent = model.Form, "profile.component.settings_form"
		expectedRoutes = []string{"core.route.profile.update_my_profile"}
	case AppearanceSettingsPageViewModel:
		form, expectedComponent = model.Form, "identity.component.appearance_settings"
		expectedRoutes = []string{
			"core.route.identity.update_current_user_appearance",
			"core.route.identity.clear_current_user_appearance",
		}
	case LoginMethodsSettingsPageViewModel:
		form, expectedComponent = model.Form, "identity.component.login_methods_settings"
		expectedRoutes = []string{
			"core.route.identity.auth_provider_start",
			"core.route.identity.external_identities",
			"core.route.identity.external_identity_unlink",
			"core.route.identity.list_auth_providers",
		}
	case LocalPasswordSettingsPageViewModel:
		form, expectedComponent = model.Form, "identity.component.local_password_settings"
		expectedRoutes = []string{"core.route.identity.setup_password"}
	case SecuritySettingsPageViewModel:
		form, expectedComponent = model.Form, "identity.component.security_settings"
		expectedRoutes = []string{
			"core.route.identity.list_sessions",
			"core.route.identity.revoke_other_sessions",
			"core.route.identity.revoke_session",
		}
	case PersonalAccessTokensPageViewModel:
		form, expectedComponent = model.Form, "identity.component.personal_access_tokens"
		expectedRoutes = []string{
			"core.route.identity.create_apitoken",
			"core.route.identity.list_apitokens",
			"core.route.identity.revoke_apitoken",
			"core.route.identity.rotate_apitoken",
		}
	case NotificationSettingsPageViewModel:
		form, expectedComponent = model.Form, "notifications.component.settings"
		expectedRoutes = []string{
			"core.route.notifications.get_preferences",
			"core.route.notifications.update_preferences",
			"core.route.notifications.restore_preferences",
			"core.route.notifications.web_push_config",
			"core.route.notifications.list_web_push_subscriptions",
			"core.route.notifications.create_web_push_subscription",
			"core.route.notifications.revoke_web_push_subscription",
		}
	case LoginPageViewModel:
		form, expectedComponent = model.Form, "identity.component.login_form"
		expectedRoutes = []string{"core.route.identity.login"}
	case RegisterPageViewModel:
		form, expectedComponent = model.Form, "identity.component.register_form"
		expectedRoutes = []string{"core.route.identity.register"}
	case ExternalAuthContinuationPageViewModel:
		form, expectedComponent = model.Form, "identity.component.external_auth_continuation"
		expectedRoutes = []string{
			"core.route.identity.prepare_external_auth_continuation",
			"core.route.identity.link_external_auth_continuation",
		}
	case EmailVerificationPageViewModel:
		form, expectedComponent = model.Form, "identity.component.email_verification"
		expectedRoutes = []string{"core.route.identity.email_verification_request"}
	case ForgotPasswordPageViewModel:
		form, expectedComponent = model.Form, "identity.component.recovery_request_form"
		expectedRoutes = []string{"core.route.identity.password_reset_request"}
	case ResetPasswordPageViewModel:
		form, expectedComponent = model.Form, "identity.component.recovery_confirm_form"
		expectedRoutes = []string{"core.route.identity.password_reset_confirm"}
	default:
		return nil
	}
	if form.ComponentID != expectedComponent || !slices.Equal(form.ActionRouteIDs, expectedRoutes) {
		return fmt.Errorf("%w: invalid Host form boundary", ErrInvalidViewModel)
	}
	return nil
}

func validateSEOView(seo PageSEOView) error {
	if err := validateSEOURL("canonical", seo.CanonicalURL); err != nil {
		return err
	}
	seenLocales := make(map[string]struct{}, len(seo.AlternateLinks))
	for _, alternate := range seo.AlternateLinks {
		locale := strings.TrimSpace(alternate.Locale)
		localeKey := strings.ToLower(locale)
		if !localePattern.MatchString(locale) && !strings.EqualFold(locale, "x-default") {
			return fmt.Errorf("%w: invalid SEO alternate locale", ErrInvalidViewModel)
		}
		if _, exists := seenLocales[localeKey]; exists {
			return fmt.Errorf("%w: duplicate SEO alternate locale", ErrInvalidViewModel)
		}
		seenLocales[localeKey] = struct{}{}
		if alternate.URL == "" {
			return fmt.Errorf("%w: SEO alternate URL is required", ErrInvalidViewModel)
		}
		if err := validateSEOURL("alternate", alternate.URL); err != nil {
			return err
		}
	}
	for _, document := range seo.StructuredData {
		if err := validateSEOURL("structured data", document.URL); err != nil {
			return err
		}
	}
	return nil
}

var localePattern = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)

func validateSEOURL(kind, value string) error {
	if value == "" {
		return nil
	}
	if strings.Contains(value, templateActionMarker) || inspectURLAttribute(value) != nil {
		return fmt.Errorf("%w: unsafe SEO %s URL", ErrInvalidViewModel, kind)
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%w: SEO %s URL must be absolute HTTP(S)", ErrInvalidViewModel, kind)
	}
	return nil
}

func validateViewerState(viewer PageViewerState) error {
	if !viewer.Authenticated {
		if viewer.UserID != 0 || viewer.Username != "" || viewer.DisplayName != "" || viewer.AvatarURL != "" || len(viewer.Permissions) != 0 {
			return fmt.Errorf("%w: guest viewer contains actor data", ErrInvalidViewModel)
		}
		return nil
	}
	if viewer.UserID <= 0 || strings.TrimSpace(viewer.Username) == "" {
		return fmt.Errorf("%w: authenticated viewer identity is incomplete", ErrInvalidViewModel)
	}
	seen := make(map[string]struct{}, len(viewer.Permissions))
	for _, permission := range viewer.Permissions {
		if !viewModelIDPattern.MatchString(permission) {
			return fmt.Errorf("%w: invalid permission projection", ErrInvalidViewModel)
		}
		if _, exists := seen[permission]; exists {
			return fmt.Errorf("%w: duplicate permission projection", ErrInvalidViewModel)
		}
		seen[permission] = struct{}{}
	}
	return nil
}

func validateRouteParams(params []RouteParam) error {
	seen := make(map[string]struct{}, len(params))
	for _, param := range params {
		// Catalog paths use camelCase parameters (categorySlug/tagSlug). Route
		// parameters have their own identifier grammar rather than component-id
		// syntax; sensitive names remain forbidden.
		if !routeParamNamePattern.MatchString(param.Name) || sensitiveViewModelName(param.Name) {
			return fmt.Errorf("%w: invalid route parameter", ErrInvalidViewModel)
		}
		if _, exists := seen[param.Name]; exists {
			return fmt.Errorf("%w: duplicate route parameter", ErrInvalidViewModel)
		}
		seen[param.Name] = struct{}{}
	}
	return nil
}

func validateReviewedViewModelType(value reflect.Type, visiting map[reflect.Type]bool) error {
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
		value = value.Elem()
	}
	if value == reflect.TypeOf(SafeHTML{}) {
		return nil
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Map, reflect.Interface, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return fmt.Errorf("unreviewed dynamic field type %s", value)
	case reflect.Struct:
		if visiting[value] {
			return nil
		}
		visiting[value] = true
		defer delete(visiting, value)
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			if field.PkgPath != "" {
				continue
			}
			if sensitiveViewModelName(field.Name) {
				return fmt.Errorf("forbidden sensitive field %s", field.Name)
			}
			if err := validateReviewedViewModelType(field.Type, visiting); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported field type %s", value)
	}
}

func sensitiveViewModelName(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(value, "_", ""), "-", ""))
	for _, fragment := range []string{"password", "secret", "token", "cookie", "authorization", "session"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

// cloneReviewedViewModel seals slices and pointers after validation so callers
// cannot mutate a previously approved ViewModel before template execution.
func cloneReviewedViewModel(value reflect.Value) (reflect.Value, error) {
	if !value.IsValid() {
		return value, nil
	}
	if value.Type() == reflect.TypeOf(SafeHTML{}) {
		return value, nil
	}
	switch value.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return value, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		item, err := cloneReviewedViewModel(value.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(item)
		return result, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			item, err := cloneReviewedViewModel(value.Index(index))
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(item)
		}
		return result, nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			item, err := cloneReviewedViewModel(value.Index(index))
			if err != nil {
				return reflect.Value{}, err
			}
			result.Index(index).Set(item)
		}
		return result, nil
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if field.PkgPath != "" {
				return reflect.Value{}, fmt.Errorf("%w: unreviewed private field %s", ErrInvalidViewModel, field.Name)
			}
			item, err := cloneReviewedViewModel(value.Field(index))
			if err != nil {
				return reflect.Value{}, err
			}
			result.Field(index).Set(item)
		}
		return result, nil
	default:
		return reflect.Value{}, fmt.Errorf("%w: unsupported sealed type %s", ErrInvalidViewModel, value.Type())
	}
}
