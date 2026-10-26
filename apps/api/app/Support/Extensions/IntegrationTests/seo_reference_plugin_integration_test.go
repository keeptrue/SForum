package extensionsruntime_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

func TestReferenceSEOPluginUsesRealProtocolV2AndCoreFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference SEO plugin subprocess build in short mode")
	}
	extension := buildReferenceSEOExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "seo-reference", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start reference SEO plugin: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = manager.Stop(context.Background(), extension)
		}
	})
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(extension.Manifest.SEO) < 6 {
		t.Fatalf("SEO reference must declare multi-kind contributions, got %d", len(extension.Manifest.SEO))
	}
	scope := extension.Manifest.SEO[0].Scope
	contributions := make([]seoregistry.Declaration, 0, len(extension.Manifest.SEO))
	for _, declaration := range extension.Manifest.SEO {
		contributions = append(contributions, seoregistry.Declaration{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion,
			Scope: declaration.Scope, Kind: declaration.Kind, Action: declaration.Action,
			Handler: declaration.Handler, Priority: declaration.Priority,
			FailurePolicy: declaration.FailurePolicy, Timeout: time.Duration(declaration.TimeoutMS) * time.Millisecond,
		})
	}
	registry := seoregistry.New()
	artifact := seoregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: extension.PackageDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: active.Identity.InstanceID,
	}
	if _, err := registry.Publish(seoregistry.Publication{
		Artifact: artifact, Contributions: contributions,
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := seoregistry.NewHostFinalPolicy(seoregistry.HostFinalPolicyConfig{
		SiteURL: "https://forum.example", SupportedLocales: []string{"zh-CN", "en-US"},
		AllowIndexing: true, SitemapEnabled: true, StructuredDataEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	trace := seoregistry.NewExecutionTraceRing(8)
	execution, err := seoregistry.NewExecutionRuntime(seoregistry.ExecutionConfig{
		Registry: registry, Resolver: extensionsruntime.NewProtocolV2SEOProviderResolver(manager),
		Admission: extensionsruntime.NewSEOExecutionAdmission(manager), FinalPolicy: policy, Trace: trace,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := referenceSEOBase("Core topic")
	result, err := execution.Execute(t.Context(), seoregistry.ExecuteRequest{Scope: scope, Base: base})
	if err != nil {
		t.Fatalf("reference SEO execute: %v", err)
	}
	if result.Document.Title != "Core topic | SEO Reference" {
		t.Fatalf("title=%q", result.Document.Title)
	}
	if len(result.Document.Meta) != 1 || result.Document.Meta[0].Key != "description" {
		t.Fatalf("meta=%#v", result.Document.Meta)
	}
	if result.Document.CanonicalURL != "https://forum.example/topic/1/" {
		t.Fatalf("canonical=%q", result.Document.CanonicalURL)
	}
	if !result.Document.Robots.NoArchive || result.Document.Robots.Indexing != seoregistry.RobotsIndex {
		t.Fatalf("robots=%#v", result.Document.Robots)
	}
	if len(result.Document.JSONLD) != 1 || result.Document.JSONLD[0].Type != "DiscussionForumPosting" {
		t.Fatalf("jsonld=%#v", result.Document.JSONLD)
	}
	if len(result.Document.Sitemap) != 1 || result.Document.Sitemap[0].URL == "" {
		t.Fatalf("sitemap=%#v", result.Document.Sitemap)
	}
	if len(result.Applied) != len(contributions) {
		t.Fatalf("applied=%d want %d result=%#v", len(result.Applied), len(contributions), result)
	}

	failedBase := referenceSEOBase("reference:fail")
	result, err = execution.Execute(t.Context(), seoregistry.ExecuteRequest{Scope: scope, Base: failedBase})
	if err != nil || result.Document.Title != failedBase.Title || len(result.Fallbacks) == 0 {
		t.Fatalf("reference failure fallback=%#v err=%v", result, err)
	}

	// --- 重启：Stop + Start 后以 exact-artifact CAS 换绑 RuntimeInstanceID ---
	previousArtifact := artifact
	if err := manager.Stop(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("restart SEO plugin: %v", err)
	}
	active, err = manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity.InstanceID == "" {
		t.Fatalf("restart runtime: %#v err=%v", active, err)
	}
	artifact = seoregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: extension.PackageDigest,
		VersionID: extension.ActiveVersionID, RuntimeInstanceID: active.Identity.InstanceID,
	}
	if _, err := registry.PublishIfArtifact(previousArtifact, seoregistry.Publication{
		Artifact: artifact, Contributions: contributions,
	}); err != nil {
		t.Fatalf("republish after restart (CAS): %v", err)
	}
	afterRestart, err := execution.Execute(t.Context(), seoregistry.ExecuteRequest{Scope: scope, Base: base})
	if err != nil || afterRestart.Document.Title != "Core topic | SEO Reference" {
		t.Fatalf("after restart execute=%#v err=%v", afterRestart, err)
	}
	t.Log("coverage.seo.restart=protocol-v2-cas-republish")

	// --- Safe Mode：清空第三方 publication，只保留 Host base ---
	if _, err := registry.ReplaceAll(nil, true); err != nil {
		t.Fatalf("seo safe mode: %v", err)
	}
	safeResult, err := execution.Execute(t.Context(), seoregistry.ExecuteRequest{Scope: scope, Base: base})
	if err != nil || safeResult.Document.Title != base.Title {
		t.Fatalf("safe mode must use Host base only: %#v err=%v", safeResult, err)
	}
	// Safe Mode 下第三方不得再 Publish（管理员拒绝隐式恢复）。
	if _, err := registry.Publish(seoregistry.Publication{
		Artifact: artifact, Contributions: contributions,
	}); err == nil {
		t.Fatal("safe mode must reject third-party SEO republish without explicit exit")
	}
	t.Log("coverage.seo.safe_mode=host-base-only+deny-third-party")

	// --- 管理员允许/拒绝 + 无隐式授权 ---
	if len(extension.CapabilityGrants) == 0 {
		t.Fatal("SEO reference must declare capability grants for operator trust UI")
	}
	for _, grant := range extension.CapabilityGrants {
		if grant.Key == "" {
			t.Fatalf("empty capability grant: %#v", grant)
		}
	}
	for _, def := range extension.Manifest.PermissionDefinitions {
		if def.AssignmentPolicy != "" && def.AssignmentPolicy != "host" {
			t.Fatalf("SEO must not self-assign permissions: %#v", def)
		}
	}
	// 无 trust 的 Starter 拒绝启动（管理员拒绝路径）。
	deniedManager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{
		Starter: extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
			Trust: staticRuntimeTrust{}, // empty trust → deny
		}),
	})
	if err := deniedManager.Start(t.Context(), extension); err == nil {
		_ = deniedManager.Stop(context.Background(), extension)
		t.Fatal("SEO Start without trust grant must be denied")
	}
	t.Log("coverage.seo.admin_deny=missing-trust-grant")

	// --- 退出 Safe Mode 后重新绑定，再 disable 验证 Host fallback ---
	if _, err := registry.ReplaceAll(nil, false); err != nil {
		t.Fatalf("exit seo safe mode: %v", err)
	}
	if _, err := registry.Publish(seoregistry.Publication{
		Artifact: artifact, Contributions: contributions,
	}); err != nil {
		t.Fatalf("republish after safe mode exit: %v", err)
	}
	if err := manager.Stop(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	stopped = true
	// 进程已停：publication 仍在时必须 fallback，证明无隐式授权继续改写 SEO。
	result, err = execution.Execute(t.Context(), seoregistry.ExecuteRequest{Scope: scope, Base: base})
	if err != nil || result.Document.Title != base.Title || len(result.Fallbacks) == 0 {
		t.Fatalf("reference disable fallback=%#v err=%v", result, err)
	}
	if _, removed, err := registry.Remove(artifact); err != nil || !removed {
		t.Fatalf("remove SEO publication after uninstall: removed=%v err=%v", removed, err)
	}
	if snap := registry.Snapshot(); len(snap.Contributions) != 0 {
		t.Fatalf("uninstall must remove SEO publication, got %#v", snap.Contributions)
	}
	t.Log("coverage.seo.uninstall=publication-removed")
}

func referenceSEOBase(title string) seoregistry.Document {
	return seoregistry.Document{
		Title: title, CanonicalURL: "https://forum.example/topic/1",
		Robots: seoregistry.RobotsDirectives{Indexing: seoregistry.RobotsIndex, Following: seoregistry.RobotsFollow},
	}
}

func buildReferenceSEOExtension(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := referenceSEOFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum.seo-reference")
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy reference SEO plugin: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(fixtureRoot, "../../../.."))
	goModPath := filepath.Join(packageRoot, "backend", "go.mod")
	goMod, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goMod = []byte(strings.ReplaceAll(string(goMod), "../../../../../apps/api", filepath.Join(repositoryRoot, "apps/api")))
	if err := os.WriteFile(goModPath, goMod, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-mod=mod", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	build.Dir = filepath.Join(packageRoot, "backend")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reference SEO plugin: %v\n%s", err, output)
	}
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := strings.ReplaceAll(string(templateBody), "__BACKEND_DIGEST__", fileSHA256(t, binaryPath))
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load exact SEO reference package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: 501,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}

func referenceSEOFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins/sforum-seo-reference"))
}
