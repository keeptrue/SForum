package pages

import (
	"slices"
	"testing"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestBuildCorePageViewModelCopiesTypedProductData(t *testing.T) {
	homeData := &themecompiler.HomePageViewModel{Topics: []themecompiler.TopicSummaryView{{ID: 42, Title: "Production topic"}}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.home", Locale: "en-US", Path: "/", Data: CorePageViewModelData{Home: homeData},
	})
	if err != nil {
		t.Fatal(err)
	}
	home, ok := model.(themecompiler.HomePageViewModel)
	if !ok || len(home.Topics) != 1 || home.Topics[0].ID != 42 {
		t.Fatalf("typed product data lost: %#v", model)
	}
	if home.Base.PageID != "forum.home" || home.Base.SchemaVersion != "sforum.page.home@1" {
		t.Fatalf("Host base was not authoritative: %#v", home.Base)
	}
	if homeData.Base.PageID != "" {
		t.Fatal("factory mutated the source-owned payload")
	}
}

func TestBuildCorePageViewModelOverridesHostFormBoundary(t *testing.T) {
	input := &themecompiler.TopicCreatePageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.fake.form", ActionRouteIDs: []string{"plugin.fake.route"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.topic.create", Locale: "en-US", Path: "/topics/new",
		Data: CorePageViewModelData{TopicCreate: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := model.(themecompiler.TopicCreatePageViewModel)
	if created.Form.ComponentID != "forum.component.topic_composer" || len(created.Form.ActionRouteIDs) != 1 || created.Form.ActionRouteIDs[0] != "core.route.forum.create_topic" {
		t.Fatalf("untrusted form boundary survived: %#v", created.Form)
	}
}

func TestBuildExternalAuthContinuationViewModelFreezesHostBoundary(t *testing.T) {
	input := &themecompiler.ExternalAuthContinuationPageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.capture.continuation", ActionRouteIDs: []string{"plugin.capture.ticket"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "auth.external_continuation", Locale: "en-US", Path: "/auth/continue",
		Data: CorePageViewModelData{ExternalContinuation: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	continuation := model.(themecompiler.ExternalAuthContinuationPageViewModel)
	wantRoutes := []string{
		"core.route.identity.prepare_external_auth_continuation",
		"core.route.identity.link_external_auth_continuation",
	}
	if continuation.Form.ComponentID != "identity.component.external_auth_continuation" || !slices.Equal(continuation.Form.ActionRouteIDs, wantRoutes) {
		t.Fatalf("Host continuation boundary drifted: %#v", continuation.Form)
	}
}

func TestBuildNotificationSettingsViewModelFreezesHostBoundary(t *testing.T) {
	input := &themecompiler.NotificationSettingsPageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.capture.settings", ActionRouteIDs: []string{"plugin.capture.secrets"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.settings.notifications", Locale: "en-US", Path: "/settings/notifications",
		Data: CorePageViewModelData{NotificationSettings: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := model.(themecompiler.NotificationSettingsPageViewModel)
	wantRoutes := []string{
		"core.route.notifications.get_preferences", "core.route.notifications.update_preferences",
		"core.route.notifications.restore_preferences", "core.route.notifications.web_push_config",
		"core.route.notifications.list_web_push_subscriptions", "core.route.notifications.create_web_push_subscription",
		"core.route.notifications.revoke_web_push_subscription",
	}
	if settings.Form.ComponentID != "notifications.component.settings" || !slices.Equal(settings.Form.ActionRouteIDs, wantRoutes) {
		t.Fatalf("Host notification boundary drifted: %#v", settings.Form)
	}
	if input.Form.ComponentID != "plugin.capture.settings" {
		t.Fatal("factory mutated source-owned notification payload")
	}
}

func TestBuildLoginMethodsSettingsViewModelFreezesHostBoundary(t *testing.T) {
	input := &themecompiler.LoginMethodsSettingsPageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.capture.login_methods", ActionRouteIDs: []string{"plugin.capture.credentials"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.settings.login_methods", Locale: "en-US", Path: "/settings/security/login-methods",
		Data: CorePageViewModelData{LoginMethodsSettings: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := model.(themecompiler.LoginMethodsSettingsPageViewModel)
	wantRoutes := []string{
		"core.route.identity.auth_provider_start",
		"core.route.identity.external_identities",
		"core.route.identity.external_identity_unlink",
		"core.route.identity.list_auth_providers",
	}
	if settings.Form.ComponentID != "identity.component.login_methods_settings" || !slices.Equal(settings.Form.ActionRouteIDs, wantRoutes) {
		t.Fatalf("Host login methods boundary drifted: %#v", settings.Form)
	}
	if input.Form.ComponentID != "plugin.capture.login_methods" {
		t.Fatal("factory mutated source-owned login methods payload")
	}
}

func TestBuildAppearanceSettingsViewModelFreezesHostBoundary(t *testing.T) {
	input := &themecompiler.AppearanceSettingsPageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.capture.appearance", ActionRouteIDs: []string{"plugin.capture.preferences"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.settings.appearance", Locale: "en-US", Path: "/settings/appearance",
		Data: CorePageViewModelData{AppearanceSettings: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := model.(themecompiler.AppearanceSettingsPageViewModel)
	wantRoutes := []string{"core.route.identity.update_current_user_appearance", "core.route.identity.clear_current_user_appearance"}
	if settings.Form.ComponentID != "identity.component.appearance_settings" || !slices.Equal(settings.Form.ActionRouteIDs, wantRoutes) {
		t.Fatalf("Host appearance boundary drifted: %#v", settings.Form)
	}
	if input.Form.ComponentID != "plugin.capture.appearance" {
		t.Fatal("factory mutated source-owned appearance payload")
	}
}

func TestBuildLocalPasswordSettingsViewModelFreezesHostBoundary(t *testing.T) {
	input := &themecompiler.LocalPasswordSettingsPageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.capture.password", ActionRouteIDs: []string{"plugin.capture.password"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.settings.password", Locale: "en-US", Path: "/settings/security/password",
		Data: CorePageViewModelData{LocalPasswordSettings: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := model.(themecompiler.LocalPasswordSettingsPageViewModel)
	if settings.Form.ComponentID != "identity.component.local_password_settings" || !slices.Equal(settings.Form.ActionRouteIDs, []string{"core.route.identity.setup_password"}) {
		t.Fatalf("Host local password boundary drifted: %#v", settings.Form)
	}
	if input.Form.ComponentID != "plugin.capture.password" {
		t.Fatal("factory mutated source-owned local password payload")
	}
}

func TestBuildPersonalAccessTokensViewModelFreezesHostBoundary(t *testing.T) {
	input := &themecompiler.PersonalAccessTokensPageViewModel{Form: themecompiler.HostFormBoundary{
		ComponentID: "plugin.capture.tokens", ActionRouteIDs: []string{"plugin.capture.tokens"},
	}}
	model, err := BuildCorePageViewModel(CorePageViewModelRequest{
		PageID: "forum.settings.tokens", Locale: "en-US", Path: "/settings/tokens",
		Data: CorePageViewModelData{PersonalAccessTokens: input},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := model.(themecompiler.PersonalAccessTokensPageViewModel)
	wantRoutes := []string{
		"core.route.identity.create_apitoken",
		"core.route.identity.list_apitokens",
		"core.route.identity.revoke_apitoken",
		"core.route.identity.rotate_apitoken",
	}
	if settings.Form.ComponentID != "identity.component.personal_access_tokens" || !slices.Equal(settings.Form.ActionRouteIDs, wantRoutes) {
		t.Fatalf("Host personal access tokens boundary drifted: %#v", settings.Form)
	}
	if input.Form.ComponentID != "plugin.capture.tokens" {
		t.Fatal("factory mutated source-owned token payload")
	}
}
