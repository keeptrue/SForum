package seoregistry

import (
	"strings"

	themecompiler "github.com/keeptrue/sforum/apps/api/app/Support/ThemeCompiler"
)

// ToPageSEOView maps a Host SEO Document into the theme compiler page SEO view.
// This is the production bridge from SEO Registry execution to SSR page chrome.
func ToPageSEOView(doc Document) themecompiler.PageSEOView {
	view := themecompiler.PageSEOView{
		Title:        strings.TrimSpace(doc.Title),
		CanonicalURL: strings.TrimSpace(doc.CanonicalURL),
		Robots:       formatRobots(doc.Robots),
	}
	for _, meta := range doc.Meta {
		if strings.EqualFold(meta.Attribute, "name") && strings.EqualFold(meta.Key, "description") {
			view.Description = meta.Content
			break
		}
	}
	if len(doc.Hreflang) > 0 {
		view.AlternateLinks = make([]themecompiler.AlternateLink, 0, len(doc.Hreflang))
		for _, link := range doc.Hreflang {
			view.AlternateLinks = append(view.AlternateLinks, themecompiler.AlternateLink{
				Locale: link.Locale, URL: link.URL,
			})
		}
	}
	if len(doc.JSONLD) > 0 {
		view.StructuredData = make([]themecompiler.StructuredDataView, 0, len(doc.JSONLD))
		for _, node := range doc.JSONLD {
			view.StructuredData = append(view.StructuredData, themecompiler.StructuredDataView{
				Kind:        node.Type,
				ID:          node.ID,
				Name:        firstNonEmpty(node.Name, node.Headline),
				URL:         node.URL,
				Description: node.Description,
				DateCreated: node.DatePublished,
				DateUpdated: node.DateModified,
			})
		}
	}
	return view
}

// MergeBaseDocument merges page-owned base SEO into a Document for Execute.
func MergeBaseDocument(base themecompiler.PageSEOView) Document {
	doc := Document{
		Title:        strings.TrimSpace(base.Title),
		CanonicalURL: strings.TrimSpace(base.CanonicalURL),
	}
	if desc := strings.TrimSpace(base.Description); desc != "" {
		doc.Meta = append(doc.Meta, MetaTag{Attribute: "name", Key: "description", Content: desc})
	}
	if robots := strings.TrimSpace(base.Robots); robots != "" {
		doc.Robots = parseRobots(robots)
	}
	for _, link := range base.AlternateLinks {
		doc.Hreflang = append(doc.Hreflang, HreflangLink{Locale: link.Locale, URL: link.URL})
	}
	for _, node := range base.StructuredData {
		doc.JSONLD = append(doc.JSONLD, JSONLDDocument{
			Context: "https://schema.org", Type: node.Kind, ID: node.ID,
			Name: node.Name, URL: node.URL, Description: node.Description,
			DatePublished: node.DateCreated, DateModified: node.DateUpdated,
		})
	}
	return doc
}

func formatRobots(robots RobotsDirectives) string {
	parts := make([]string, 0, 6)
	if robots.Indexing != "" {
		parts = append(parts, robots.Indexing)
	}
	if robots.Following != "" {
		parts = append(parts, robots.Following)
	}
	if robots.NoArchive {
		parts = append(parts, "noarchive")
	}
	if robots.NoImageIndex {
		parts = append(parts, "noimageindex")
	}
	if robots.NoSnippet {
		parts = append(parts, "nosnippet")
	}
	return strings.Join(parts, ", ")
}

func parseRobots(raw string) RobotsDirectives {
	var out RobotsDirectives
	for _, part := range strings.Split(raw, ",") {
		token := strings.ToLower(strings.TrimSpace(part))
		switch token {
		case "index", "noindex":
			out.Indexing = token
		case "follow", "nofollow":
			out.Following = token
		case "noarchive":
			out.NoArchive = true
		case "noimageindex":
			out.NoImageIndex = true
		case "nosnippet":
			out.NoSnippet = true
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
