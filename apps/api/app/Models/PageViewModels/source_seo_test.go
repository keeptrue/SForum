package pageviewmodels

import (
	"net/url"
	"testing"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestListViewModelCanonicalUsesAuthoritativePageAndExcludesFilters(t *testing.T) {
	source := newTestSource(&sourceForum{}, defaultSourceOptions("public"))
	request := pages.CorePageViewModelRequest{
		PageID: "forum.home", Locale: "en-US", Path: "/en",
		SEO: themecompiler.PageSEOView{Title: "forum.home"},
	}

	paginated, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: request, Query: url.Values{"page": {"2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if paginated.SEO.CanonicalURL != "https://forum.example/en?page=2" || paginated.SEO.Robots != "index,follow" {
		t.Fatalf("paginated SEO = %#v", paginated.SEO)
	}

	filtered, err := source.Populate(t.Context(), CorePageViewModelInput{
		Request: request, Query: url.Values{"page": {"2"}, "category": {"support"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.SEO.CanonicalURL != "https://forum.example/en?page=2" || filtered.SEO.Robots != "noindex,follow" {
		t.Fatalf("filtered SEO = %#v", filtered.SEO)
	}
}

func TestTopicViewModelCanonicalUsesURLModeAndPreservesLocale(t *testing.T) {
	for _, test := range []struct {
		mode string
		want string
	}{
		{mode: "id", want: "https://forum.example/en/t/42?page=2"},
		{mode: "slug", want: "https://forum.example/en/t/hello?page=2"},
		{mode: "id_slug", want: "https://forum.example/en/t/42/hello?page=2"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			configured := defaultSourceOptions("public")
			configured.values[options.NameSEOTopicURLMode] = test.mode
			source := newTestSource(&sourceForum{}, configured)
			populated, err := source.Populate(t.Context(), CorePageViewModelInput{
				Request: pages.CorePageViewModelRequest{
					PageID: "forum.topic.show", Locale: "en-US", Path: "/en/t/42/old-slug",
					RouteParams: map[string]string{"path": "42/old-slug"}, SEO: themecompiler.PageSEOView{Title: "forum.topic.show"},
				},
				Query: url.Values{"page": {"2"}, "edit": {"1"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if populated.SEO.CanonicalURL != test.want {
				t.Fatalf("topic canonical = %q", populated.SEO.CanonicalURL)
			}
			if populated.SEO.Robots != "noindex,follow" {
				t.Fatalf("topic robots = %q", populated.SEO.Robots)
			}
			if len(populated.SEO.StructuredData) != 1 || populated.SEO.StructuredData[0].URL != populated.SEO.CanonicalURL {
				t.Fatalf("topic structured data = %#v", populated.SEO.StructuredData)
			}
		})
	}
}
