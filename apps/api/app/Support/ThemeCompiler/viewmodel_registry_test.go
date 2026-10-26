package themecompiler

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	viewModelThemeDigest      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherViewModelThemeDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	viewModelBindingRevision  = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestCorePageViewModelCatalogCoversPublicPageFamilies(t *testing.T) {
	if err := ValidateCorePageViewModelCatalog(); err != nil {
		t.Fatal(err)
	}
	want := map[string]PageViewModelKind{
		"forum.home": ViewModelHome, "forum.search": ViewModelHome,
		"forum.category.index": ViewModelList, "forum.category.show": ViewModelList,
		"forum.tag.index": ViewModelList, "forum.tag.show": ViewModelList,
		"forum.topic.show": ViewModelDetail, "forum.topic.create": ViewModelCreate,
		"forum.topic.reply":      ViewModelCreate,
		"forum.topic.edit":       ViewModelCreate,
		"forum.profile.show":     ViewModelProfile,
		"forum.settings.profile": ViewModelSettings, "forum.settings.appearance": ViewModelSettings, "forum.settings.login_methods": ViewModelSettings, "forum.settings.password": ViewModelSettings, "forum.settings.security": ViewModelSettings,
		"forum.settings.tokens":        ViewModelSettings,
		"forum.settings.notifications": ViewModelSettings,
		"forum.notifications":          ViewModelNotifications, "forum.notification.show": ViewModelNotifications, "moderation.review": ViewModelModeration,
		"auth.login": ViewModelAuth, "auth.register": ViewModelAuth, "auth.external_continuation": ViewModelAuth,
		"auth.email_verification": ViewModelAuth, "auth.forgot_password": ViewModelAuth, "auth.reset_password": ViewModelAuth,
		"site.terms": ViewModelLegal, "site.privacy": ViewModelLegal, "site.guidelines": ViewModelLegal,
		"system.forbidden": ViewModelError, "system.not_found": ViewModelError,
		"system.rate_limited": ViewModelError, "system.server_error": ViewModelError,
		"site.outbound":  ViewModelUtility,
		"dev.components": ViewModelDevelopment,
	}
	for _, schema := range CorePageViewModelRegistry().Catalog() {
		kind, ok := want[schema.PageID]
		if !ok {
			t.Fatalf("unexpected schema %#v", schema)
		}
		if schema.Kind != kind || !schemaVersionPattern.MatchString(schema.SchemaVersion) {
			t.Fatalf("invalid schema %#v", schema)
		}
		delete(want, schema.PageID)
	}
	if len(want) != 0 {
		t.Fatalf("missing schema families: %#v", want)
	}
}

func TestHostFormViewModelsExposeOnlyReviewedIslandBoundaries(t *testing.T) {
	base := PageViewModelBase{
		PageID: "auth.login", SchemaVersion: "sforum.page.login@1", Locale: "en-US",
		Route: PageRouteView{Path: "/login"}, SEO: PageSEOView{Title: "Login"},
	}
	valid := LoginPageViewModel{
		Base: base,
		Form: HostFormBoundary{
			ComponentID: "identity.component.login_form", ActionRouteIDs: []string{"core.route.identity.login"},
		},
	}
	if _, err := CorePageViewModelRegistry().Bind("auth.login", "sforum.page.login@1", viewModelThemeDigest, valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Form.ComponentID = "plugin.form.capture_credentials"
	if _, err := CorePageViewModelRegistry().Bind("auth.login", "sforum.page.login@1", viewModelThemeDigest, invalid); !errors.Is(err, ErrInvalidViewModel) {
		t.Fatalf("unreviewed form error = %v", err)
	}
	for name, routes := range map[string][]string{
		"empty":        nil,
		"plugin route": {"plugin.route.capture"},
		"extra action": {"core.route.identity.login", "core.route.identity.register"},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.Form.ActionRouteIDs = routes
			if _, err := CorePageViewModelRegistry().Bind("auth.login", "sforum.page.login@1", viewModelThemeDigest, candidate); !errors.Is(err, ErrInvalidViewModel) {
				t.Fatalf("route drift error = %v", err)
			}
		})
	}
	if _, err := CorePageViewModelRegistry().Bind("auth.login", "sforum.page.login@1", viewModelThemeDigest, map[string]any{
		"csrf": "leak", "credentials": "leak",
	}); !errors.Is(err, ErrInvalidViewModel) {
		t.Fatalf("form map error = %v", err)
	}
}

func TestPageViewModelRejectsUnsafeOrAmbiguousSEOURLs(t *testing.T) {
	tests := []struct {
		name   string
		change func(*HomePageViewModel)
	}{
		{name: "javascript canonical", change: func(model *HomePageViewModel) { model.Base.SEO.CanonicalURL = "javascript:alert(1)" }},
		{name: "data alternate", change: func(model *HomePageViewModel) {
			model.Base.SEO.AlternateLinks = []AlternateLink{{Locale: "en-US", URL: "data:text/html,unsafe"}}
		}},
		{name: "file structured data", change: func(model *HomePageViewModel) {
			model.Base.SEO.StructuredData = []StructuredDataView{{Kind: "WebSite", URL: "file:///etc/passwd"}}
		}},
		{name: "duplicate locale", change: func(model *HomePageViewModel) {
			model.Base.SEO.AlternateLinks = []AlternateLink{
				{Locale: "en-US", URL: "https://example.com/en"},
				{Locale: "EN-us", URL: "https://example.com/en-duplicate"},
			}
		}},
		{name: "invalid locale", change: func(model *HomePageViewModel) {
			model.Base.SEO.AlternateLinks = []AlternateLink{{Locale: "../en", URL: "https://example.com/en"}}
		}},
		{name: "missing alternate URL", change: func(model *HomePageViewModel) {
			model.Base.SEO.AlternateLinks = []AlternateLink{{Locale: "en-US"}}
		}},
		{name: "URL credentials", change: func(model *HomePageViewModel) {
			model.Base.SEO.CanonicalURL = "https://user:credential@example.com/"
		}},
		{name: "relative canonical", change: func(model *HomePageViewModel) { model.Base.SEO.CanonicalURL = "/relative" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := validHomeViewModel()
			test.change(&model)
			if _, err := CorePageViewModelRegistry().Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, model); !errors.Is(err, ErrInvalidViewModel) {
				t.Fatalf("SEO error = %v", err)
			}
		})
	}
	valid := validHomeViewModel()
	valid.Base.SEO.AlternateLinks = []AlternateLink{{Locale: "x-default", URL: "https://example.com/"}}
	if _, err := CorePageViewModelRegistry().Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, valid); err != nil {
		t.Fatalf("x-default alternate rejected: %v", err)
	}
}

func TestSearchStateUsesHomeAndSearchContracts(t *testing.T) {
	registry := CorePageViewModelRegistry()
	home := validHomeViewModel()
	home.Search = &SearchStateView{
		Query: "compiler", Results: []SearchResultView{{Kind: "topic", Title: "Theme compiler", URL: "/t/compiler"}},
	}
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, home); err != nil {
		t.Fatal(err)
	}
	home.Base.PageID = "forum.search"
	home.Base.SchemaVersion = "sforum.page.search@1"
	home.Base.Route.Path = "/search"
	home.Base.SEO.CanonicalURL = "https://example.com/search"
	if _, err := registry.Bind("forum.search", "sforum.page.search@1", viewModelThemeDigest, home); err != nil {
		t.Fatal(err)
	}
}

func TestPageViewModelRegistryRejectsMapsActorsAndSecretShapedData(t *testing.T) {
	registry := CorePageViewModelRegistry()
	for name, value := range map[string]any{
		"arbitrary map":     map[string]any{"password": "leak"},
		"identity actor":    identity.Actor{ID: 7, Permissions: map[string]bool{"topic.create": true}},
		"custom secret DTO": struct{ SessionToken string }{SessionToken: "leak"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, value); !errors.Is(err, ErrInvalidViewModel) {
				t.Fatalf("Bind() error = %v", err)
			}
		})
	}
}

func TestPageViewModelRegistryValidatesViewerProjectionAndSchema(t *testing.T) {
	registry := CorePageViewModelRegistry()
	model := validHomeViewModel()
	model.Base.Viewer = PageViewerState{UserID: 7, Username: "hidden-actor"}
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, model); !errors.Is(err, ErrInvalidViewModel) {
		t.Fatalf("guest actor data error = %v", err)
	}

	model = validHomeViewModel()
	model.Base.Route.Params = []RouteParam{{Name: "sessionToken", Value: "leak"}}
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, model); !errors.Is(err, ErrInvalidViewModel) {
		t.Fatalf("secret route parameter error = %v", err)
	}

	model = validHomeViewModel()
	model.Base.Route.Params = []RouteParam{{Name: "categorySlug", Value: "support"}}
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, model); err != nil {
		t.Fatalf("catalog camelCase route parameter rejected: %v", err)
	}
	model.Base.Route.Params = []RouteParam{{Name: "category_slug", Value: "support"}}
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, model); !errors.Is(err, ErrInvalidViewModel) {
		t.Fatalf("non-catalog route parameter grammar error = %v", err)
	}

	model = validHomeViewModel()
	model.Base.SchemaVersion = "sforum.page.home@2"
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", viewModelThemeDigest, model); !errors.Is(err, ErrViewModelSchema) {
		t.Fatalf("embedded schema error = %v", err)
	}
	if _, err := registry.Bind("forum.home", "sforum.page.home@2", viewModelThemeDigest, model); !errors.Is(err, ErrViewModelSchema) {
		t.Fatalf("catalog schema error = %v", err)
	}
	if _, err := registry.Bind("forum.home", "sforum.page.home@1", "not-a-digest", validHomeViewModel()); !errors.Is(err, ErrViewModelTheme) {
		t.Fatalf("digest error = %v", err)
	}
}

func TestSnapshotRenderRequiresExactSchemaAndThemeDigest(t *testing.T) {
	snapshot := compilePageViewModelSnapshot(t, PageTemplateBinding{
		PageID: "forum.home", SchemaVersion: "sforum.page.home@1",
	})
	model := validHomeViewModel()
	bound, err := CorePageViewModelRegistry().Bind(
		"forum.home", "sforum.page.home@1", viewModelThemeDigest, model,
	)
	if err != nil {
		t.Fatal(err)
	}
	// Binding seals slices: a caller cannot mutate reviewed data before render.
	model.Topics[0].Title = "mutated after bind"
	output, err := snapshot.Render(context.Background(), "templates/home.html", bound)
	if err != nil {
		t.Fatal(err)
	}
	segments := output.HTMLSegments()
	if len(segments) != 1 {
		t.Fatalf("output = %#v", segments)
	}
	if segments[0].String() != "forum.home:Original topic" {
		t.Fatalf("output = %#v", segments)
	}

	wrongTheme, err := CorePageViewModelRegistry().Bind(
		"forum.home", "sforum.page.home@1", otherViewModelThemeDigest, validHomeViewModel(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Render(context.Background(), "templates/home.html", wrongTheme); !errors.Is(err, ErrViewModelTheme) {
		t.Fatalf("theme mismatch error = %v", err)
	}

	wrongSchemaSnapshot := compilePageViewModelSnapshot(t, PageTemplateBinding{
		PageID: "forum.home", SchemaVersion: "sforum.page.home@2",
	})
	if _, err := wrongSchemaSnapshot.Render(context.Background(), "templates/home.html", bound); !errors.Is(err, ErrViewModelSchema) {
		t.Fatalf("schema mismatch error = %v", err)
	}
}

func TestCompilerRejectsInvalidPageViewModelBindings(t *testing.T) {
	source := fstest.MapFS{
		"templates/home.html": &fstest.MapFile{Data: []byte(`home`)},
	}
	compiler := NewCompiler(Limits{})
	for name, bindings := range map[string]map[string]PageTemplateBinding{
		"missing binding":  {},
		"unknown template": {"templates/missing.html": {PageID: "forum.home", SchemaVersion: "sforum.page.home@1"}},
		"invalid schema":   {"templates/home.html": {PageID: "forum.home", SchemaVersion: "home-latest"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compiler.CompileFS(source, viewModelThemeDigest, Bindings{
				BindingRevision: viewModelBindingRevision, PageViewModels: bindings,
			})
			if !errors.Is(err, ErrViewModelSchema) {
				t.Fatalf("CompileFS() error = %v", err)
			}
		})
	}
}

func compilePageViewModelSnapshot(t *testing.T, binding PageTemplateBinding) *Snapshot {
	t.Helper()
	snapshot, err := NewCompiler(Limits{}).CompileFS(fstest.MapFS{
		"templates/home.html": &fstest.MapFile{Data: []byte(`{{.Base.PageID}}:{{(index .Topics 0).Title}}`)},
	}, viewModelThemeDigest, Bindings{
		BindingRevision: viewModelBindingRevision,
		PageViewModels:  map[string]PageTemplateBinding{"templates/home.html": binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func validHomeViewModel() HomePageViewModel {
	return HomePageViewModel{
		Base: PageViewModelBase{
			PageID: "forum.home", SchemaVersion: "sforum.page.home@1", Locale: "en-US",
			Route: PageRouteView{Path: "/"}, SEO: PageSEOView{Title: "Home"},
		},
		Topics: []TopicSummaryView{{ID: 1, Title: "Original topic", URL: "/t/original"}},
	}
}
