package bootstrap

import (
	"context"
	"errors"
	"testing"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

func TestBindProductionSEORegistryUsesSharedSnapshotAndHostPolicy(t *testing.T) {
	registry := seoregistry.New()
	runtime, err := bindProductionSEORegistry(
		registry,
		extensionsruntime.NewManager(extensionsruntime.ManagerConfig{}),
		seoregistry.HostFinalPolicyConfig{
			SiteURL: "https://forum.example", SupportedLocales: []string{"zh-CN", "en-US"},
			AllowIndexing: true, SitemapEnabled: true, StructuredDataEnabled: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := seoregistry.Document{
		CanonicalURL: "https://forum.example/topic/1",
		Robots: seoregistry.RobotsDirectives{
			Indexing: seoregistry.RobotsIndex, Following: seoregistry.RobotsFollow,
		},
	}
	result, err := runtime.Execution.Execute(context.Background(), seoregistry.ExecuteRequest{
		Scope: "core.page.topic", Base: base,
	})
	if err != nil || result.Revision != registry.Revision() || result.Digest != registry.Snapshot().Digest ||
		result.Document.CanonicalURL != base.CanonicalURL {
		t.Fatalf("production SEO result=%#v err=%v", result, err)
	}
	if traces := runtime.Trace.SEOExecutionTraces(1); len(traces) != 1 || traces[0].Outcome != seoregistry.TraceOutcomeApplied {
		t.Fatalf("production SEO traces=%#v", traces)
	}
}

func TestBindProductionSEORegistryRejectsMissingAuthority(t *testing.T) {
	validPolicy := seoregistry.HostFinalPolicyConfig{
		SiteURL: "https://forum.example", SupportedLocales: []string{"zh-CN"},
	}
	if _, err := bindProductionSEORegistry(nil, extensionsruntime.NewManager(extensionsruntime.ManagerConfig{}), validPolicy); err == nil {
		t.Fatal("nil shared Registry was accepted")
	}
	if _, err := bindProductionSEORegistry(seoregistry.New(), nil, validPolicy); err == nil {
		t.Fatal("nil exact runtime Manager was accepted")
	}
	if _, err := bindProductionSEORegistry(
		seoregistry.New(), extensionsruntime.NewManager(extensionsruntime.ManagerConfig{}),
		seoregistry.HostFinalPolicyConfig{SiteURL: "javascript:alert(1)", SupportedLocales: []string{"zh-CN"}},
	); !errors.Is(err, seoregistry.ErrHostPolicyInvalid) {
		t.Fatalf("invalid Host policy error=%v", err)
	}
}

func TestProductionSEOEnabledUsesExistingOptionEncoding(t *testing.T) {
	for _, value := range []string{"enabled", "true", "1", "yes", "on"} {
		if !productionSEOEnabled(value, false) {
			t.Fatalf("%q did not resolve enabled", value)
		}
	}
	for _, value := range []string{"disabled", "false", "0", "no", "off"} {
		if productionSEOEnabled(value, true) {
			t.Fatalf("%q did not resolve disabled", value)
		}
	}
	if !productionSEOEnabled("unknown", true) || productionSEOEnabled("unknown", false) {
		t.Fatal("unknown SEO option ignored its existing recommended fallback")
	}
}
