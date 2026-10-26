package pages

import (
	"fmt"
	"sort"
	"strings"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

// CorePageViewModelRequest contains only Host-produced presentation state.
// Product data can be added through the typed fields as each page migrates;
// themes never receive request/session objects or arbitrary maps.
type CorePageViewModelRequest struct {
	PageID      string
	Locale      string
	Path        string
	RouteParams map[string]string
	Viewer      themecompiler.PageViewerState
	Pagination  *themecompiler.PaginationView
	SEO         themecompiler.PageSEOView
	Navigation  []themecompiler.NavigationItem
	Breadcrumbs []themecompiler.BreadcrumbItem
	Regions     []themecompiler.PageRegion
	Data        CorePageViewModelData
}

func BuildCorePageViewModel(request CorePageViewModelRequest) (any, error) {
	page, ok := Find(strings.TrimSpace(request.PageID))
	if !ok {
		return nil, ErrUnknownPage
	}
	path := strings.TrimSpace(request.Path)
	if path == "" {
		path = page.PathPattern
	}
	if path == "" {
		path = "/"
	}
	seo := request.SEO
	if strings.TrimSpace(seo.Title) == "" {
		seo.Title = page.ID
	}
	base := themecompiler.PageViewModelBase{
		PageID: page.ID, SchemaVersion: page.ContractVersion,
		Locale: strings.TrimSpace(request.Locale),
		Route:  themecompiler.PageRouteView{Path: path, Params: sortedRouteParams(request.RouteParams)},
		Viewer: request.Viewer, Pagination: clonePagination(request.Pagination), SEO: seo,
		Navigation:  append([]themecompiler.NavigationItem(nil), request.Navigation...),
		Breadcrumbs: append([]themecompiler.BreadcrumbItem(nil), request.Breadcrumbs...),
		Regions:     append([]themecompiler.PageRegion(nil), request.Regions...),
	}
	if base.Locale == "" {
		base.Locale = "zh-CN"
	}

	switch page.ID {
	case "forum.home":
		model := valueOrZero(request.Data.Home)
		model.Base = base
		return model, nil
	case "forum.search":
		model := valueOrZero(request.Data.Search)
		model.Base = base
		return model, nil
	case "forum.category.index":
		model := valueOrZero(request.Data.CategoryIndex)
		model.Base = base
		return model, nil
	case "forum.category.show":
		model := valueOrZero(request.Data.CategoryShow)
		model.Base = base
		return model, nil
	case "forum.tag.index":
		model := valueOrZero(request.Data.TagIndex)
		model.Base = base
		return model, nil
	case "forum.tag.show":
		model := valueOrZero(request.Data.TagShow)
		model.Base = base
		return model, nil
	case "forum.topic.show":
		model := valueOrZero(request.Data.TopicDetail)
		model.Base = base
		return model, nil
	case "forum.topic.create":
		model := valueOrZero(request.Data.TopicCreate)
		model.Base = base
		model.Form = hostForm("forum.component.topic_composer", "core.route.forum.create_topic")
		return model, nil
	case "forum.topic.reply":
		model := valueOrZero(request.Data.TopicReply)
		model.Base = base
		model.Form = hostForm("forum.component.topic_reply", "core.route.forum.create_comment")
		return model, nil
	case "forum.topic.edit":
		model := valueOrZero(request.Data.TopicEdit)
		model.Base = base
		model.Form = hostForm("forum.component.topic_editor", "core.route.forum.update_topic")
		return model, nil
	case "forum.profile.show":
		model := valueOrZero(request.Data.Profile)
		model.Base = base
		return model, nil
	case "forum.settings.profile":
		model := valueOrZero(request.Data.ProfileSettings)
		model.Base = base
		model.Form = hostForm("profile.component.settings_form", "core.route.profile.update_my_profile")
		return model, nil
	case "forum.settings.appearance":
		model := valueOrZero(request.Data.AppearanceSettings)
		model.Base = base
		model.Form = hostForm(
			"identity.component.appearance_settings",
			"core.route.identity.update_current_user_appearance",
			"core.route.identity.clear_current_user_appearance",
		)
		return model, nil
	case "forum.settings.login_methods":
		model := valueOrZero(request.Data.LoginMethodsSettings)
		model.Base = base
		model.Form = hostForm(
			"identity.component.login_methods_settings",
			"core.route.identity.auth_provider_start",
			"core.route.identity.external_identities",
			"core.route.identity.external_identity_unlink",
			"core.route.identity.list_auth_providers",
		)
		return model, nil
	case "forum.settings.password":
		model := valueOrZero(request.Data.LocalPasswordSettings)
		model.Base = base
		model.Form = hostForm("identity.component.local_password_settings", "core.route.identity.setup_password")
		return model, nil
	case "forum.settings.security":
		model := valueOrZero(request.Data.SecuritySettings)
		model.Base = base
		model.Form = hostForm("identity.component.security_settings",
			"core.route.identity.list_sessions", "core.route.identity.revoke_other_sessions",
			"core.route.identity.revoke_session")
		return model, nil
	case "forum.settings.tokens":
		model := valueOrZero(request.Data.PersonalAccessTokens)
		model.Base = base
		model.Form = hostForm(
			"identity.component.personal_access_tokens",
			"core.route.identity.create_apitoken", "core.route.identity.list_apitokens",
			"core.route.identity.revoke_apitoken", "core.route.identity.rotate_apitoken",
		)
		return model, nil
	case "forum.settings.notifications":
		model := valueOrZero(request.Data.NotificationSettings)
		model.Base = base
		model.Form = hostForm(
			"notifications.component.settings",
			"core.route.notifications.get_preferences",
			"core.route.notifications.update_preferences",
			"core.route.notifications.restore_preferences",
			"core.route.notifications.web_push_config",
			"core.route.notifications.list_web_push_subscriptions",
			"core.route.notifications.create_web_push_subscription",
			"core.route.notifications.revoke_web_push_subscription",
		)
		return model, nil
	case "forum.notifications":
		model := valueOrZero(request.Data.Notifications)
		model.Base = base
		return model, nil
	case "forum.notification.show":
		model := valueOrZero(request.Data.Notifications)
		model.Base = base
		return model, nil
	case "moderation.review":
		model := valueOrZero(request.Data.ModerationReview)
		model.Base = base
		return model, nil
	case "auth.login":
		model := valueOrZero(request.Data.Login)
		model.Base = base
		model.Form = hostForm("identity.component.login_form", "core.route.identity.login")
		return model, nil
	case "auth.register":
		model := valueOrZero(request.Data.Register)
		model.Base = base
		model.Form = hostForm("identity.component.register_form", "core.route.identity.register")
		return model, nil
	case "auth.external_continuation":
		model := valueOrZero(request.Data.ExternalContinuation)
		model.Base = base
		model.Form = hostForm(
			"identity.component.external_auth_continuation",
			"core.route.identity.prepare_external_auth_continuation",
			"core.route.identity.link_external_auth_continuation",
		)
		return model, nil
	case "auth.email_verification":
		model := valueOrZero(request.Data.EmailVerification)
		model.Base = base
		model.Form = hostForm("identity.component.email_verification", "core.route.identity.email_verification_request")
		return model, nil
	case "auth.forgot_password":
		model := valueOrZero(request.Data.ForgotPassword)
		model.Base = base
		model.Form = hostForm("identity.component.recovery_request_form", "core.route.identity.password_reset_request")
		return model, nil
	case "auth.reset_password":
		model := valueOrZero(request.Data.ResetPassword)
		model.Base = base
		model.Form = hostForm("identity.component.recovery_confirm_form", "core.route.identity.password_reset_confirm")
		return model, nil
	case "site.terms":
		model := valueOrZero(request.Data.Terms)
		model.Base = base
		return model, nil
	case "site.privacy":
		model := valueOrZero(request.Data.Privacy)
		model.Base = base
		return model, nil
	case "site.guidelines":
		model := valueOrZero(request.Data.Guidelines)
		model.Base = base
		return model, nil
	case "system.forbidden":
		model := valueOrZero(request.Data.Forbidden)
		model.Base = base
		if model.StatusCode == 0 {
			model.StatusCode = 403
		}
		if strings.TrimSpace(model.Title) == "" {
			model.Title = seo.Title
		}
		return model, nil
	case "system.not_found":
		model := valueOrZero(request.Data.NotFound)
		model.Base = base
		if model.StatusCode == 0 {
			model.StatusCode = 404
		}
		if strings.TrimSpace(model.Title) == "" {
			model.Title = seo.Title
		}
		return model, nil
	case "system.rate_limited":
		model := valueOrZero(request.Data.RateLimited)
		model.Base = base
		if model.StatusCode == 0 {
			model.StatusCode = 429
		}
		if strings.TrimSpace(model.Title) == "" {
			model.Title = seo.Title
		}
		return model, nil
	case "system.server_error":
		model := valueOrZero(request.Data.ServerError)
		model.Base = base
		if model.StatusCode == 0 {
			model.StatusCode = 500
		}
		if strings.TrimSpace(model.Title) == "" {
			model.Title = seo.Title
		}
		return model, nil
	case "site.outbound":
		model := valueOrZero(request.Data.Outbound)
		model.Base = base
		return model, nil
	case "dev.components":
		model := valueOrZero(request.Data.DevelopmentComponents)
		model.Base = base
		return model, nil
	default:
		return nil, fmt.Errorf("%w: no Host ViewModel factory for %s", ErrUnknownPage, page.ID)
	}
}

func valueOrZero[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

func sortedRouteParams(input map[string]string) []themecompiler.RouteParam {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]themecompiler.RouteParam, 0, len(keys))
	for _, key := range keys {
		result = append(result, themecompiler.RouteParam{Name: key, Value: input[key]})
	}
	return result
}

func clonePagination(input *themecompiler.PaginationView) *themecompiler.PaginationView {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func hostForm(componentID string, routeIDs ...string) themecompiler.HostFormBoundary {
	return themecompiler.HostFormBoundary{ComponentID: componentID, ActionRouteIDs: append([]string(nil), routeIDs...)}
}
