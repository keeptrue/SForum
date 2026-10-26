package pageviewmodels

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

var sourceTestNow = time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)

type sourceForum struct {
	lastTopicInput   forum.TopicListInput
	lastCommentInput forum.CommentListInput
	topicErr         error
	lastTopicViewer  identity.Actor
}

func (s *sourceForum) ListCategoryGroups(context.Context, bool) ([]forum.CategoryGroup, error) {
	return []forum.CategoryGroup{{
		ID: 1, Slug: "general", Name: "General", Visibility: "public",
		Categories: []forum.Category{{ID: 10, Slug: "support", Name: "Support", Description: "Help topics", Visibility: "public", TopicCount: 1}},
	}}, nil
}

func (s *sourceForum) ListTags(context.Context, bool) ([]forum.Tag, error) {
	return []forum.Tag{{ID: 20, Slug: "go", Name: "Go", Description: "Go language", Status: forum.TagStatusActive, TopicCount: 1}}, nil
}

func (s *sourceForum) ListTopics(_ context.Context, input forum.TopicListInput) (forum.TopicList, error) {
	s.lastTopicInput = input
	return forum.TopicList{Items: []forum.TopicSummary{sourceTopic().TopicSummary}, Total: 1, Page: max(input.Page, 1), PerPage: 20}, nil
}

func (s *sourceForum) GetTopic(context.Context, int64) (forum.TopicDetail, error) {
	if s.topicErr != nil {
		return forum.TopicDetail{}, s.topicErr
	}
	return sourceTopic(), nil
}

func (s *sourceForum) GetTopicBySlug(context.Context, string) (forum.TopicDetail, error) {
	if s.topicErr != nil {
		return forum.TopicDetail{}, s.topicErr
	}
	return sourceTopic(), nil
}

func (s *sourceForum) GetTopicForViewer(_ context.Context, _ int64, viewer identity.Actor) (forum.TopicDetail, error) {
	s.lastTopicViewer = viewer
	return s.GetTopic(context.Background(), 42)
}

func (s *sourceForum) GetTopicBySlugForViewer(_ context.Context, slug string, viewer identity.Actor) (forum.TopicDetail, error) {
	s.lastTopicViewer = viewer
	return s.GetTopicBySlug(context.Background(), slug)
}

func (s *sourceForum) ListComments(_ context.Context, input forum.CommentListInput) (forum.CommentList, error) {
	s.lastCommentInput = input
	return forum.CommentList{Items: []forum.Comment{{
		ID: 91, TopicID: 42, AuthorUserID: 8, Author: sourceUser(),
		Content: forum.PublicRenderedContent{HTMLContent: "<p>Reply</p>"}, CreatedAt: sourceTestNow, UpdatedAt: sourceTestNow,
	}}, Total: 1, Page: max(input.Page, 1), PerPage: 20, View: input.View}, nil
}

type sourceProfiles struct{}

func (sourceProfiles) GetPublicProfile(context.Context, string) (profile.PublicProfile, error) {
	return sourceProfile(), nil
}

func (sourceProfiles) GetMyProfile(context.Context, int64) (profile.PublicProfile, error) {
	return sourceProfile(), nil
}

type sourceNotifications struct{}

func (sourceNotifications) List(context.Context, notifications.ListInput) (notifications.Page, error) {
	return notifications.Page{Items: []notifications.Notification{{
		ID: 71, Type: notifications.TypeReply, TargetType: "topic", TargetID: 42,
		Payload: []byte(`{"topicId":42,"title":"New reply"}`), CreatedAt: sourceTestNow,
	}}}, nil
}

func (sourceNotifications) UnreadCount(context.Context, int64) (int64, error) { return 3, nil }

type sourceModeration struct{}

func (sourceModeration) ListPending(context.Context, identity.Actor, moderation.WorkbenchListInput) (moderation.PendingList, error) {
	return moderation.PendingList{Items: []moderation.PendingItem{{
		TargetType: moderation.TargetTypeTopic, TargetID: 42, Title: "Pending topic", CreatedAt: sourceTestNow,
	}}, Total: 1, Page: 1, PerPage: 20}, nil
}

type sourceOptions struct {
	guestRead string
	ready     bool
	values    map[string]string
	features  map[string]bool
}

func (s sourceOptions) WebOption(_ context.Context, name string) (string, error) {
	value, ok := s.values[name]
	if !ok {
		return "", errors.New("missing option " + name)
	}
	return value, nil
}

func (s sourceOptions) IsFeatureEnabled(_ context.Context, name string) (bool, error) {
	if s.features == nil {
		return true, nil
	}
	return s.features[name], nil
}

func (s sourceOptions) ForumReadPolicySnapshot() (string, string, uint64, bool) {
	return s.guestRead, "author_and_staff", 1, s.ready
}

type sourceRegistration struct{}

func (sourceRegistration) RegistrationStatus(context.Context) (identity.RegistrationStatus, error) {
	return identity.RegistrationStatus{RegistrationEnabled: true}, nil
}

type sourceSessions struct{}

func (sourceSessions) ListUserSessions(context.Context, int64, string, bool, int, int) (identity.SessionListResult, error) {
	return identity.SessionListResult{Items: []identity.SessionRecord{{
		ID: "device-1", DeviceName: "Chrome on macOS", IsCurrent: true, LastSeenAt: sourceTestNow,
	}}, Total: 1, Page: 1, PerPage: 20}, nil
}

type sourceChrome struct{}

func (sourceChrome) ResolvePublicNavigation(context.Context, identity.Actor, string, []string) (sitechrome.ResolvedNavigation, error) {
	return sitechrome.ResolvedNavigation{SchemaVersion: sitechrome.NavigationDocumentSchemaVersion, Revision: 1, Locations: []sitechrome.ResolvedNavigationLocation{{
		Location: sitechrome.NavigationLocationTopbar, Supported: true, Items: []sitechrome.ResolvedNavigationItem{
			{SourceKey: "core.home", Label: "Home", Href: "/"},
			{SourceKey: "core.categories", Label: "Categories", Href: "/categories"},
			{SourceKey: "operator.docs", Label: "Docs", Href: "/docs"},
			{SourceKey: "extension.reference.nav.guide", Label: "Guide", Href: "/guide"},
		},
	}}}, nil
}

func (sourceChrome) ListPublicAnnouncements(context.Context) ([]sitechrome.Announcement, error) {
	return []sitechrome.Announcement{{ID: 1, TitleEnUS: "Notice", BodyEnUS: "Production data", Enabled: true}}, nil
}

type sourceSearch struct{}

func (sourceSearch) Search(context.Context, search.SearchInput) (search.SearchResult, error) {
	return search.SearchResult{Items: []search.TopicSearchDoc{{
		ID: 42, Title: "Search hit", Slug: "hello", CategoryID: 10, CategorySlug: "support", CategoryName: "Support",
		AuthorUserID: 8, AuthorUsername: "alice", AuthorDisplayName: "Alice", Status: forum.TopicStatusActive,
		CreatedAt: sourceTestNow, UpdatedAt: sourceTestNow,
	}}, Total: 1, Page: 1, PerPage: 20}, nil
}

func TestCorePageViewModelSourcePopulatesEveryCatalogContract(t *testing.T) {
	forumReader := &sourceForum{}
	source := newTestSource(forumReader, sourceOptions{
		guestRead: "public", ready: true,
		values: map[string]string{
			options.NameSiteName: "SForum", options.NameSiteURL: "https://forum.example",
			options.NameSEOTopicURLMode: "id_slug", options.NameForumTagPublicPages: "enabled",
			options.NameLegalTermsBodyENUS:      "## Terms\n\nReal terms.",
			options.NameLegalPrivacyBodyENUS:    "## Privacy\n\nReal privacy policy.",
			options.NameLegalGuidelinesBodyENUS: "## Guidelines\n\nReal guidelines.",
		},
	})
	actor := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}
	viewer := themecompiler.PageViewerState{Authenticated: true, UserID: 8, Username: "alice", DisplayName: "Alice", Permissions: []string{identity.PermissionModerationReview}}

	requests := map[string]struct {
		path   string
		params map[string]string
		query  url.Values
		typeOf reflect.Type
	}{
		"forum.home":                   {"/", nil, nil, reflect.TypeOf(themecompiler.HomePageViewModel{})},
		"forum.search":                 {"/search", nil, url.Values{"q": {"production"}}, reflect.TypeOf(themecompiler.HomePageViewModel{})},
		"forum.category.index":         {"/categories", nil, nil, reflect.TypeOf(themecompiler.CategoryIndexPageViewModel{})},
		"forum.category.show":          {"/c/support", map[string]string{"categorySlug": "support"}, url.Values{"page": {"1"}}, reflect.TypeOf(themecompiler.CategoryShowPageViewModel{})},
		"forum.tag.index":              {"/tags", nil, nil, reflect.TypeOf(themecompiler.TagIndexPageViewModel{})},
		"forum.tag.show":               {"/tags/go", map[string]string{"tagSlug": "go"}, nil, reflect.TypeOf(themecompiler.TagShowPageViewModel{})},
		"forum.topic.show":             {"/t/42/hello", map[string]string{"path": "42/hello"}, url.Values{"page": {"1"}}, reflect.TypeOf(themecompiler.TopicDetailPageViewModel{})},
		"forum.topic.create":           {"/topics/new", nil, nil, reflect.TypeOf(themecompiler.TopicCreatePageViewModel{})},
		"forum.topic.reply":            {"/topics/reply", nil, url.Values{"topic": {"not-a-topic"}}, reflect.TypeOf(themecompiler.TopicReplyPageViewModel{})},
		"forum.topic.edit":             {"/topics/42/edit", map[string]string{"topicId": "42"}, nil, reflect.TypeOf(themecompiler.TopicEditPageViewModel{})},
		"forum.profile.show":           {"/u/alice", map[string]string{"username": "alice"}, nil, reflect.TypeOf(themecompiler.ProfilePageViewModel{})},
		"forum.settings.profile":       {"/settings/profile", nil, nil, reflect.TypeOf(themecompiler.ProfileSettingsPageViewModel{})},
		"forum.settings.appearance":    {"/settings/appearance", nil, nil, reflect.TypeOf(themecompiler.AppearanceSettingsPageViewModel{})},
		"forum.settings.login_methods": {"/settings/security/login-methods", nil, nil, reflect.TypeOf(themecompiler.LoginMethodsSettingsPageViewModel{})},
		"forum.settings.password":      {"/settings/security/password", nil, nil, reflect.TypeOf(themecompiler.LocalPasswordSettingsPageViewModel{})},
		"forum.settings.security":      {"/settings/security", nil, nil, reflect.TypeOf(themecompiler.SecuritySettingsPageViewModel{})},
		"forum.settings.tokens":        {"/settings/tokens", nil, nil, reflect.TypeOf(themecompiler.PersonalAccessTokensPageViewModel{})},
		"forum.settings.notifications": {"/settings/notifications", nil, nil, reflect.TypeOf(themecompiler.NotificationSettingsPageViewModel{})},
		"forum.notifications":          {"/notifications", nil, nil, reflect.TypeOf(themecompiler.NotificationsPageViewModel{})},
		"forum.notification.show":      {"/notifications/58", map[string]string{"notificationId": "58"}, nil, reflect.TypeOf(themecompiler.NotificationsPageViewModel{})},
		"moderation.review":            {"/moderation", nil, nil, reflect.TypeOf(themecompiler.ModerationReviewPageViewModel{})},
		"auth.login":                   {"/login", nil, nil, reflect.TypeOf(themecompiler.LoginPageViewModel{})},
		"auth.register":                {"/register", nil, nil, reflect.TypeOf(themecompiler.RegisterPageViewModel{})},
		"auth.external_continuation":   {"/auth/continue", nil, url.Values{"ticket": {"opaque"}}, reflect.TypeOf(themecompiler.ExternalAuthContinuationPageViewModel{})},
		"auth.email_verification":      {"/email-verification", nil, nil, reflect.TypeOf(themecompiler.EmailVerificationPageViewModel{})},
		"auth.forgot_password":         {"/forgot-password", nil, nil, reflect.TypeOf(themecompiler.ForgotPasswordPageViewModel{})},
		"auth.reset_password":          {"/reset-password", nil, url.Values{"token": {"exact-token"}}, reflect.TypeOf(themecompiler.ResetPasswordPageViewModel{})},
		"site.terms":                   {"/terms", nil, nil, reflect.TypeOf(themecompiler.TermsPageViewModel{})},
		"site.privacy":                 {"/privacy", nil, nil, reflect.TypeOf(themecompiler.PrivacyPageViewModel{})},
		"site.guidelines":              {"/guidelines", nil, nil, reflect.TypeOf(themecompiler.GuidelinesPageViewModel{})},
		"system.forbidden":             {"/forbidden", nil, nil, reflect.TypeOf(themecompiler.ErrorPageViewModel{})},
		"system.not_found":             {"/missing", nil, nil, reflect.TypeOf(themecompiler.ErrorPageViewModel{})},
		"system.rate_limited":          {"/rate-limited", nil, nil, reflect.TypeOf(themecompiler.ErrorPageViewModel{})},
		"system.server_error":          {"/server-error", nil, nil, reflect.TypeOf(themecompiler.ErrorPageViewModel{})},
		"site.outbound":                {"/outbound", nil, nil, reflect.TypeOf(themecompiler.OutboundPageViewModel{})},
		"dev.components":               {"/components", nil, nil, reflect.TypeOf(themecompiler.DevelopmentComponentsPageViewModel{})},
	}

	if len(requests) != len(pages.Catalog()) {
		t.Fatalf("test catalog drift: requests=%d catalog=%d", len(requests), len(pages.Catalog()))
	}
	for _, definition := range pages.Catalog() {
		t.Run(definition.ID, func(t *testing.T) {
			test := requests[definition.ID]
			populated, err := source.Populate(t.Context(), CorePageViewModelInput{
				Request: pages.CorePageViewModelRequest{
					PageID: definition.ID, Locale: "en-US", Path: test.path, RouteParams: test.params,
					Viewer: viewer, SEO: themecompiler.PageSEOView{Title: definition.ID},
				},
				Actor: actor, CurrentSessionID: "device-1", Query: test.query,
			})
			if err != nil {
				t.Fatalf("Populate: %v", err)
			}
			model, err := pages.BuildCorePageViewModel(populated)
			if err != nil {
				t.Fatalf("BuildCorePageViewModel: %v", err)
			}
			if reflect.TypeOf(model) != test.typeOf {
				t.Fatalf("type %T, want %v", model, test.typeOf)
			}
			if definition.ID == "forum.topic.show" {
				payload, err := json.Marshal(populated)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"rawContent", "contentHash", "sourceFormat", "editorType", "editorVersion", "M1_PAGE_SOURCE_SECRET"} {
					if strings.Contains(string(payload), forbidden) {
						t.Fatalf("public Page Registry payload contains %q: %s", forbidden, payload)
					}
				}
			}
			if _, err := themecompiler.CorePageViewModelRegistry().Bind(definition.ID, definition.ContractVersion, string(make([]byte, 0)), model); !errors.Is(err, themecompiler.ErrViewModelTheme) {
				t.Fatalf("expected only invalid test digest before schema validation, got %v", err)
			}
			if _, err := themecompiler.CorePageViewModelRegistry().Bind(definition.ID, definition.ContractVersion, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", model); err != nil {
				t.Fatalf("bind exact ViewModel: %v", err)
			}
		})
	}

	if forumReader.lastCommentInput.Viewer.ID != actor.ID {
		t.Fatalf("comment visibility lost authoritative actor: %#v", forumReader.lastCommentInput.Viewer)
	}
	if forumReader.lastTopicViewer.ID != actor.ID {
		t.Fatalf("topic visibility lost authoritative actor: %#v", forumReader.lastTopicViewer)
	}
}

func TestCorePageViewModelSourceUsesSearchAndBoundsUntrustedFilters(t *testing.T) {
	forumReader := &sourceForum{}
	source := newTestSource(forumReader, defaultSourceOptions("public"))
	request, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{PageID: "forum.home", Locale: "en-US", Path: "/", SEO: themecompiler.PageSEOView{Title: "forum.home"}},
		Query:   url.Values{"q": {"production"}, "page": {"999999"}, "category": {"support"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Data.Home == nil || request.Data.Home.Search == nil || len(request.Data.Home.Search.Results) != 1 {
		t.Fatalf("search projection missing: %#v", request.Data.Home)
	}
	if request.Pagination == nil || request.Pagination.Page != 1 {
		t.Fatalf("search service result must remain authoritative: %#v", request.Pagination)
	}
}

func TestCorePageViewModelSourceEnforcesGuestReadBeforeForumQueries(t *testing.T) {
	forumReader := &sourceForum{}
	source := newTestSource(forumReader, defaultSourceOptions("login_required"))
	_, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{PageID: "forum.home", Locale: "en-US", Path: "/", SEO: themecompiler.PageSEOView{Title: "forum.home"}},
	})
	if !errors.Is(err, ErrCorePageDataUnauthorized) {
		t.Fatalf("expected guest read denial, got %v", err)
	}
	if forumReader.lastTopicInput.Page != 0 {
		t.Fatal("forum data was queried before guest read authorization")
	}
}

func TestCorePageViewModelSourceRejectsGuestNotificationSettings(t *testing.T) {
	source := newTestSource(&sourceForum{}, defaultSourceOptions("public"))
	_, err := source.Populate(t.Context(), CorePageViewModelInput{Request: pages.CorePageViewModelRequest{
		PageID: "forum.settings.notifications", Locale: "en-US", Path: "/settings/notifications",
		SEO: themecompiler.PageSEOView{Title: "Notification settings"},
	}})
	if !errors.Is(err, ErrCorePageDataUnauthorized) {
		t.Fatalf("guest notification settings error = %v", err)
	}
}

func TestCorePageViewModelSourceDoesNotBypassDisabledSearch(t *testing.T) {
	forumReader := &sourceForum{}
	configured := defaultSourceOptions("public")
	configured.features = map[string]bool{options.NameFeatureSearch: false}
	source := newTestSource(forumReader, configured)
	_, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{PageID: "forum.home", Locale: "en-US", Path: "/", SEO: themecompiler.PageSEOView{Title: "forum.home"}},
		Query:   url.Values{"q": {"must-not-run"}},
	})
	if !errors.Is(err, ErrCorePageDataUnavailable) {
		t.Fatalf("disabled search must fail to core fallback, got %v", err)
	}
	if forumReader.lastTopicInput.Page != 0 {
		t.Fatal("disabled search fell through to an alternate topic query")
	}
}

func TestCorePageViewModelSourceUsesActorAwareResolvedTopbar(t *testing.T) {
	chrome := &recordingSourceChrome{resolved: sitechrome.ResolvedNavigation{
		SchemaVersion: sitechrome.NavigationDocumentSchemaVersion,
		Revision:      9,
		Locations: []sitechrome.ResolvedNavigationLocation{{
			Location:  sitechrome.NavigationLocationTopbar,
			Supported: true,
			Items:     []sitechrome.ResolvedNavigationItem{{SourceKey: "operator.members", Label: "Members", Href: "/members"}},
		}},
	}}
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{
		Options: defaultSourceOptions("public"), Registration: sourceRegistration{}, SiteChrome: chrome,
	})
	actor := identity.Actor{ID: 17, Status: identity.UserStatusActive, Permissions: map[string]bool{"forum.members": true}}
	request, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{PageID: "auth.login", Locale: "en-US", Path: "/members", SEO: themecompiler.PageSEOView{Title: "Login"}},
		Actor:   actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if chrome.actor.ID != actor.ID || chrome.locale != "en-US" || !reflect.DeepEqual(chrome.locations, []string{sitechrome.NavigationLocationTopbar}) {
		t.Fatalf("resolver input actor=%#v locale=%q locations=%#v", chrome.actor, chrome.locale, chrome.locations)
	}
	if len(request.Navigation) != 1 || request.Navigation[0].ID != "operator.members" || !request.Navigation[0].Active {
		t.Fatalf("resolved navigation projection=%#v", request.Navigation)
	}
}

func TestCorePageViewModelSourceFailsClosedWhenNavigationResolutionFails(t *testing.T) {
	source := NewCorePageViewModelSource(CorePageViewModelDependencies{
		Options:      defaultSourceOptions("public"),
		Registration: sourceRegistration{},
		SiteChrome:   &recordingSourceChrome{err: errors.New("registry unavailable")},
	})
	_, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: pages.CorePageViewModelRequest{PageID: "auth.login", Locale: "en-US", Path: "/login", SEO: themecompiler.PageSEOView{Title: "Login"}},
	})
	if !errors.Is(err, ErrCorePageDataUnavailable) {
		t.Fatalf("navigation resolver error=%v", err)
	}
}

type recordingSourceChrome struct {
	resolved  sitechrome.ResolvedNavigation
	err       error
	actor     identity.Actor
	locale    string
	locations []string
}

func (s *recordingSourceChrome) ResolvePublicNavigation(_ context.Context, actor identity.Actor, locale string, locations []string) (sitechrome.ResolvedNavigation, error) {
	s.actor = actor
	s.locale = locale
	s.locations = append([]string(nil), locations...)
	return s.resolved, s.err
}

func (*recordingSourceChrome) ListPublicAnnouncements(context.Context) ([]sitechrome.Announcement, error) {
	return nil, nil
}

func TestCorePageViewModelSourceRendersRealProductDataThroughThemeCompiler(t *testing.T) {
	source := newTestSource(&sourceForum{}, defaultSourceOptions("public"))
	request, err := source.Populate(t.Context(), CorePageViewModelInput{Request: pages.CorePageViewModelRequest{
		PageID: "forum.home", Locale: "en-US", Path: "/", SEO: themecompiler.PageSEOView{Title: "forum.home"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	model, err := pages.BuildCorePageViewModel(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	snapshot, err := themecompiler.NewCompiler(themecompiler.Limits{}).CompileFS(fstest.MapFS{
		"templates/home.html": &fstest.MapFile{Data: []byte(`{{range .Topics}}<a href="{{.URL}}">{{.Title}}</a>{{end}}{{range .Categories}}<span>{{.Name}}:{{.Count}}</span>{{end}}`)},
	}, digest, themecompiler.Bindings{
		BindingRevision: strings.Repeat("b", 64),
		PageViewModels: map[string]themecompiler.PageTemplateBinding{
			"templates/home.html": {PageID: "forum.home", SchemaVersion: "sforum.page.home@1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := themecompiler.CorePageViewModelRegistry().Bind("forum.home", "sforum.page.home@1", digest, model)
	if err != nil {
		t.Fatal(err)
	}
	output, err := snapshot.Render(t.Context(), "templates/home.html", bound)
	if err != nil {
		t.Fatal(err)
	}
	segments := output.HTMLSegments()
	var html strings.Builder
	for _, segment := range segments {
		html.WriteString(segment.String())
	}
	if !strings.Contains(html.String(), `href="/t/42/hello"`) || !strings.Contains(html.String(), "Hello") || !strings.Contains(html.String(), "Support:1") {
		t.Fatalf("production ViewModel did not reach L1 output: %#v", segments)
	}
}

func newTestSource(forumReader *sourceForum, optionReader sourceOptions) *CorePageViewModelSource {
	return NewCorePageViewModelSource(CorePageViewModelDependencies{
		Forum: forumReader, Profiles: sourceProfiles{}, Notifications: sourceNotifications{}, Moderation: sourceModeration{},
		Options: optionReader, Registration: sourceRegistration{}, Sessions: sourceSessions{}, SiteChrome: sourceChrome{}, Search: sourceSearch{},
	})
}

func defaultSourceOptions(guestRead string) sourceOptions {
	return sourceOptions{guestRead: guestRead, ready: true, values: map[string]string{
		options.NameSiteName: "SForum", options.NameSiteURL: "https://forum.example",
		options.NameSEOTopicURLMode: "id_slug", options.NameForumTagPublicPages: "enabled",
	}}
}

func sourceTopic() forum.TopicDetail {
	return forum.TopicDetail{TopicSummary: forum.TopicSummary{
		ID: 42, CategoryID: 10, CategorySlug: "support", CategoryName: "Support",
		AuthorUserID: 8, Author: sourceUser(), Title: "Hello", Slug: "hello", Status: forum.TopicStatusActive,
		CommentCount: 1, Tags: []forum.TopicTagSummary{{ID: 20, Slug: "go", Name: "Go", Status: forum.TagStatusActive}},
		Excerpt: "Hello excerpt", CreatedAt: sourceTestNow, UpdatedAt: sourceTestNow,
	}, Content: forum.PublicRenderedContent{HTMLContent: "<p>Hello body</p>", Excerpt: "Hello excerpt"}}
}

func sourceUser() *forum.UserSummary {
	return &forum.UserSummary{ID: 8, Username: "alice", DisplayName: "Alice"}
}

func sourceProfile() profile.PublicProfile {
	return profile.PublicProfile{
		UserID: 8, Username: "alice", DisplayName: "Alice",
		Profile:    profile.Profile{UserID: 8, Bio: "<b>plain bio</b>"},
		TopicCount: 1, CommentCount: 2, RecentTopics: []forum.TopicSummary{sourceTopic().TopicSummary}, JoinedAt: sourceTestNow,
	}
}
