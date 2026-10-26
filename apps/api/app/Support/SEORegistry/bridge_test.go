package seoregistry

import (
	"testing"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestToPageSEOViewAndMergeRoundTrip(t *testing.T) {
	doc := Document{
		Title: "主题标题", CanonicalURL: "https://example.com/t/1",
		Meta:     []MetaTag{{Attribute: "name", Key: "description", Content: "描述"}},
		Robots:   RobotsDirectives{Indexing: "index", Following: "follow", NoArchive: true},
		Hreflang: []HreflangLink{{Locale: "en-US", URL: "https://example.com/en/t/1"}},
		JSONLD: []JSONLDDocument{{
			Context: "https://schema.org", Type: "DiscussionForumPosting",
			Name: "主题标题", Description: "描述", URL: "https://example.com/t/1",
		}},
	}
	view := ToPageSEOView(doc)
	if view.Title != "主题标题" || view.Description != "描述" || view.CanonicalURL == "" {
		t.Fatalf("view = %#v", view)
	}
	if view.Robots == "" || len(view.AlternateLinks) != 1 || len(view.StructuredData) != 1 {
		t.Fatalf("view links/data = %#v", view)
	}
	// Merge base back to document preserves core fields.
	merged := MergeBaseDocument(themecompiler.PageSEOView{
		Title: "Base", Description: "Base desc", CanonicalURL: "https://example.com/",
		Robots: "noindex, nofollow",
	})
	if merged.Title != "Base" || len(merged.Meta) != 1 || merged.Robots.Indexing != "noindex" {
		t.Fatalf("merged = %#v", merged)
	}
}
