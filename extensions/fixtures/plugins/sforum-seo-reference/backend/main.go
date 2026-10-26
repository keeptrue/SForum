package main

import (
	"context"
	"errors"
	"log"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

// P13 SEO reference: multi-kind Host SEO Registry contributions.
// 每个 handler 只允许改动自己声明的 kind；失败策略为 fallback。

const (
	titleID     = "sforum.seo-reference.title"
	metaID      = "sforum.seo-reference.meta"
	canonicalID = "sforum.seo-reference.canonical"
	robotsID    = "sforum.seo-reference.robots"
	jsonldID    = "sforum.seo-reference.jsonld"
	sitemapID   = "sforum.seo-reference.sitemap"
	scopeTopic  = "core.page.topic"
)

func main() {
	registry, err := pluginv2.NewSEORegistry(
		pluginv2.SEODefinition{
			ID: titleID, ContractVersion: titleID + "@1",
			Scope: scopeTopic, Kind: "title", Action: "filter",
			Handler: titleID, FailurePolicy: "fallback", TimeoutMS: 500,
			Execute: applyTitle,
		},
		pluginv2.SEODefinition{
			ID: metaID, ContractVersion: metaID + "@1",
			Scope: scopeTopic, Kind: "meta", Action: "add",
			Handler: metaID, FailurePolicy: "fallback", TimeoutMS: 500,
			Execute: applyMeta,
		},
		pluginv2.SEODefinition{
			ID: canonicalID, ContractVersion: canonicalID + "@1",
			Scope: scopeTopic, Kind: "canonical", Action: "filter",
			Handler: canonicalID, FailurePolicy: "fallback", TimeoutMS: 500,
			Execute: applyCanonical,
		},
		pluginv2.SEODefinition{
			ID: robotsID, ContractVersion: robotsID + "@1",
			Scope: scopeTopic, Kind: "robots", Action: "filter",
			Handler: robotsID, FailurePolicy: "fallback", TimeoutMS: 500,
			Execute: applyRobots,
		},
		pluginv2.SEODefinition{
			ID: jsonldID, ContractVersion: jsonldID + "@1",
			Scope: scopeTopic, Kind: "jsonld", Action: "add",
			Handler: jsonldID, FailurePolicy: "fallback", TimeoutMS: 500,
			Execute: applyJSONLD,
		},
		pluginv2.SEODefinition{
			ID: sitemapID, ContractVersion: sitemapID + "@1",
			Scope: scopeTopic, Kind: "sitemap", Action: "add",
			Handler: sitemapID, FailurePolicy: "fallback", TimeoutMS: 500,
			Execute: applySitemap,
		},
	)
	if err != nil {
		log.Fatalf("configure SEO reference plugin: %v", err)
	}
	pluginv2.Serve(pluginv2.NewServer().WithSEORegistry(registry))
}

func failIfRequested(ctx context.Context, title string) error {
	switch title {
	case "reference:fail":
		return errors.New("reference SEO failure")
	case "reference:timeout":
		<-ctx.Done()
		return context.Cause(ctx)
	default:
		return nil
	}
}

func applyTitle(ctx context.Context, call *pluginv2.SEOCall) (pluginv2.SEODocument, error) {
	if err := failIfRequested(ctx, call.Current.Title); err != nil {
		return pluginv2.SEODocument{}, err
	}
	result := call.Current
	result.Title = strings.TrimSpace(result.Title) + " | SEO Reference"
	return result, nil
}

func applyMeta(ctx context.Context, call *pluginv2.SEOCall) (pluginv2.SEODocument, error) {
	if err := failIfRequested(ctx, call.Current.Title); err != nil {
		return pluginv2.SEODocument{}, err
	}
	result := call.Current
	result.Meta = append(append([]seoregistry.MetaTag(nil), result.Meta...), seoregistry.MetaTag{
		Attribute: "name", Key: "description",
		Content: "SEO Reference description for " + strings.TrimSpace(call.Current.Title),
	})
	return result, nil
}

func applyCanonical(ctx context.Context, call *pluginv2.SEOCall) (pluginv2.SEODocument, error) {
	if err := failIfRequested(ctx, call.Current.Title); err != nil {
		return pluginv2.SEODocument{}, err
	}
	result := call.Current
	// 仅规范化 trailing slash；必须保持同源，否则 Host final policy 拒绝。
	canonical := strings.TrimSpace(result.CanonicalURL)
	if canonical != "" && !strings.HasSuffix(canonical, "/") {
		result.CanonicalURL = canonical + "/"
	}
	return result, nil
}

func applyRobots(ctx context.Context, call *pluginv2.SEOCall) (pluginv2.SEODocument, error) {
	if err := failIfRequested(ctx, call.Current.Title); err != nil {
		return pluginv2.SEODocument{}, err
	}
	result := call.Current
	// 只能收紧：标记 noarchive，不放开 index/follow。
	result.Robots.NoArchive = true
	return result, nil
}

func applyJSONLD(ctx context.Context, call *pluginv2.SEOCall) (pluginv2.SEODocument, error) {
	if err := failIfRequested(ctx, call.Current.Title); err != nil {
		return pluginv2.SEODocument{}, err
	}
	result := call.Current
	result.JSONLD = append(append([]seoregistry.JSONLDDocument(nil), result.JSONLD...), seoregistry.JSONLDDocument{
		Context:  "https://schema.org",
		Type:     "DiscussionForumPosting",
		Headline: strings.TrimSpace(call.Current.Title),
		URL:      strings.TrimSpace(call.Current.CanonicalURL),
	})
	return result, nil
}

func applySitemap(ctx context.Context, call *pluginv2.SEOCall) (pluginv2.SEODocument, error) {
	if err := failIfRequested(ctx, call.Current.Title); err != nil {
		return pluginv2.SEODocument{}, err
	}
	result := call.Current
	url := strings.TrimSpace(call.Current.CanonicalURL)
	if url == "" {
		return result, nil
	}
	priority := 0.6
	result.Sitemap = append(append([]seoregistry.SitemapEntry(nil), result.Sitemap...), seoregistry.SitemapEntry{
		URL: url, ChangeFrequency: seoregistry.SitemapDaily, Priority: &priority,
	})
	return result, nil
}
