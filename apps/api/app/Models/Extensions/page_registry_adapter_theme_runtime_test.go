package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestPageRegistryAdapterThemeRuntimeSwitchAndRollback(t *testing.T) {
	registry := pages.NewRegistry(pages.NewMemoryStore())
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	source := themeRuntimeExtensionFixture(t, DefaultThemeID, strings.Repeat("1", 64), "/shared", false)
	target := themeRuntimeExtensionFixture(t, "target.theme", strings.Repeat("2", 64), "/shared", false)

	if err := adapter.RegisterThemePackage(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	assertActiveThemeRuntime(t, runtimeRegistry, source.ID)
	if err := adapter.RegisterThemePackageReplacing(context.Background(), target, source.ID); err != nil {
		t.Fatal(err)
	}
	assertActiveThemeRuntime(t, runtimeRegistry, target.ID)
	if _, ok := runtimeRegistry.Resolve(pages.RuntimeArtifact{
		ExtensionID: source.ID, ExtensionVersion: source.Version, PackageDigest: source.PackageDigest,
	}, "forum.home", source.ID+".home"); !ok {
		t.Fatal("default theme fallback was removed after custom theme activation")
	}
	if err := adapter.RegisterThemePackageReplacing(context.Background(), source, target.ID); err != nil {
		t.Fatal(err)
	}
	assertActiveThemeRuntime(t, runtimeRegistry, source.ID)
}

func TestPageRegistryAdapterFailedSwitchKeepsSourceRuntime(t *testing.T) {
	registry := pages.NewRegistry(pages.NewMemoryStore())
	if err := registry.RegisterContributions("plugin.owner", []pages.PageContribution{{
		ID: "plugin.page", Action: pages.ActionAdd, Path: "/occupied",
		ExtensionID: "plugin.owner", Version: "1.0.0", PackageDigest: "plugin-digest",
	}}); err != nil {
		t.Fatal(err)
	}
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	source := themeRuntimeExtensionFixture(t, "source.theme", strings.Repeat("3", 64), "/source", false)
	conflicting := themeRuntimeExtensionFixture(t, "target.theme", strings.Repeat("4", 64), "/occupied", true)
	if err := adapter.RegisterThemePackage(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RegisterThemePackageReplacing(context.Background(), conflicting, source.ID); err == nil {
		t.Fatal("expected add-route conflict")
	}
	assertActiveThemeRuntime(t, runtimeRegistry, source.ID)
	if _, ok := runtimeRegistry.Resolve(pages.RuntimeArtifact{
		ExtensionID: conflicting.ID, ExtensionVersion: conflicting.Version, PackageDigest: conflicting.PackageDigest,
	}, "forum.home", conflicting.ID+".home"); ok {
		t.Fatal("failed target snapshot remained staged")
	}
}

func TestThemeActivationRuntimeFailureRestoresExactApprovalAcrossServiceAndRestart(t *testing.T) {
	ctx := context.Background()
	store := pages.NewMemoryStore()
	registry := pages.NewRegistry(store)
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	previous := exactThemeRuntimeExtensionFixture(t, "previous.theme", "/previous")
	target := exactThemeRuntimeExtensionFixture(t, "target.theme", "/target")
	previous.Status = StatusEnabled
	target.Status = StatusInstalled

	if err := adapter.RegisterThemePackage(ctx, previous); err != nil {
		t.Fatal(err)
	}
	previousSnapshot, ok := registry.ExtensionSnapshot(previous.ID)
	if !ok || len(previousSnapshot.Contributions) != 1 {
		t.Fatalf("previous publication = %#v", previousSnapshot)
	}
	previousContribution := previousSnapshot.Contributions[0]
	if err := registry.ApproveReplace(ctx, pages.ProviderBinding{
		PageID: "forum.home", ExtensionID: previous.ID, ContributionID: previousContribution.ID,
		Version: previous.Version, PackageDigest: previous.PackageDigest,
		ContractVersion: previousContribution.Contract, ApprovedBy: 42,
	}); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("target activation health check failed")
	runtimeRegistry.WithActivationCheck(func(artifact pages.RuntimeArtifact) error {
		if artifact.ExtensionID == target.ID {
			return injected
		}
		return nil
	})
	extensionStore := newFakeExtensionStore(map[string]Extension{previous.ID: previous, target.ID: target})
	extensionStore.activeThemeID = previous.ID
	extensionStore.themeApprovalBy = map[string]int64{previous.ID: 42}
	service := NewServiceWithOptions(extensionStore, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(adapter))
	targetActor := extensionManager()
	targetActor.ID = 99
	_, err := service.ActivateThemeFromPreview(ctx, targetActor, target.ID, ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: previous.ID, CurrentThemeVersion: previous.Version, CurrentThemeDigest: previous.PackageDigest,
		ApproveCoreReplacements: true,
	})
	if !errors.Is(err, ErrBuildFailed) || extensionStore.activeThemeID != previous.ID {
		t.Fatalf("activation error=%v active=%q", err, extensionStore.activeThemeID)
	}
	compensation := extensionStore.latestThemePublication
	if compensation.Reason != ThemeRuntimePublicationCompensation || compensation.ThemeID != previous.ID ||
		!compensation.CoreReplacementsApproved || compensation.ActorUserID != 42 ||
		!compensation.SourceCoreReplacementsApproved || compensation.SourceActorUserID != 99 {
		t.Fatalf("compensation publication = %#v", compensation)
	}
	assertActiveThemeRuntime(t, runtimeRegistry, previous.ID)
	resolved, err := registry.Resolve(ctx, "forum.home")
	if err != nil || resolved.Provider != previous.ID {
		t.Fatalf("restored provider=%#v err=%v", resolved, err)
	}
	binding, ok, err := store.GetBinding(ctx, "forum.home")
	if err != nil || !ok || binding.ExtensionID != previous.ID || binding.ApprovedBy != 42 ||
		binding.PackageDigest != previous.PackageDigest {
		t.Fatalf("restored binding=%#v ok=%t err=%v", binding, ok, err)
	}
	if _, ok := registry.ExtensionSnapshot(target.ID); ok {
		t.Fatal("failed target Page publication remained visible")
	}
	if _, ok := runtimeRegistry.Resolve(pages.RuntimeArtifact{
		ExtensionID: target.ID, ExtensionVersion: target.Version, PackageDigest: target.PackageDigest,
	}, "forum.home", target.ID+".home"); ok {
		t.Fatal("failed target runtime remained staged")
	}

	fresh := pages.NewRegistry(store)
	if err := fresh.RegisterContributions(previous.ID, previousSnapshot.Contributions); err != nil {
		t.Fatal(err)
	}
	if err := fresh.RestoreBindings(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := fresh.Resolve(ctx, "forum.home")
	if err != nil || restarted.Provider != previous.ID {
		t.Fatalf("restart provider=%#v err=%v", restarted, err)
	}
}

func TestPageRegistryAdapterUnsafeTemplateFailsBeforeRuntimeSwitch(t *testing.T) {
	registry := pages.NewRegistry(pages.NewMemoryStore())
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	source := themeRuntimeExtensionFixture(t, "source.theme", strings.Repeat("5", 64), "/source", false)
	target := themeRuntimeExtensionFixture(t, "unsafe.theme", strings.Repeat("6", 64), "/unsafe", false)
	// Legacy page validation treats this as inert text; the V3 compiler must
	// reject the unapproved html/template helper before staging the snapshot.
	writeThemeRuntimeExtensionFile(t, target.PackagePath, "templates/home.html", `{{printf "%s" .Base.SEO.Title}}<sf-home-page></sf-home-page>`)

	if err := adapter.RegisterThemePackage(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RegisterThemePackageReplacing(context.Background(), target, source.ID); !errors.Is(err, pages.ErrThemeRuntimeInvalid) {
		t.Fatalf("unsafe activation error = %v", err)
	}
	assertActiveThemeRuntime(t, runtimeRegistry, source.ID)
	if _, ok := runtimeRegistry.Resolve(pages.RuntimeArtifact{
		ExtensionID: target.ID, ExtensionVersion: target.Version, PackageDigest: target.PackageDigest,
	}, "forum.home", target.ID+".home"); ok {
		t.Fatal("unsafe target snapshot was published")
	}
}

func TestPageRegistryAdapterPublishesPluginOnlyAfterCompiledSnapshot(t *testing.T) {
	registry := pages.NewRegistry(pages.NewMemoryStore())
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	plugin := themeRuntimeExtensionFixture(t, "plugin.demo", strings.Repeat("7", 64), "/plugin", false)
	plugin.Type = TypePlugin

	if err := adapter.RegisterPluginPackage(context.Background(), plugin); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtimeRegistry.Resolve(pages.RuntimeArtifact{
		ExtensionID: plugin.ID, ExtensionVersion: plugin.Version, PackageDigest: plugin.PackageDigest,
	}, "forum.home", plugin.ID+".home"); !ok {
		t.Fatal("plugin page became visible without an exact compiled renderer")
	}

	unsafe := themeRuntimeExtensionFixture(t, "plugin.unsafe", strings.Repeat("8", 64), "/unsafe", false)
	unsafe.Type = TypePlugin
	writeThemeRuntimeExtensionFile(t, unsafe.PackagePath, "templates/home.html", `{{printf "%s" .Base.SEO.Title}}<sf-home-page></sf-home-page>`)
	if err := adapter.RegisterPluginPackage(context.Background(), unsafe); !errors.Is(err, pages.ErrThemeRuntimeInvalid) {
		t.Fatalf("unsafe plugin error=%v", err)
	}
	if _, ok := registry.ExtensionSnapshot(unsafe.ID); ok {
		t.Fatal("plugin contribution was published before compiler success")
	}
}

func TestPageRegistryAdapterClearsPluginWithoutPageContributions(t *testing.T) {
	registry := pages.NewRegistry(pages.NewMemoryStore())
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	plugin := themeRuntimeExtensionFixture(t, "plugin.backend-only", strings.Repeat("a", 64), "/stale-plugin-page", false)
	plugin.Type = TypePlugin
	if err := adapter.RegisterPluginPackage(t.Context(), plugin); err != nil {
		t.Fatal(err)
	}
	if !runtimeRegistry.Claims(plugin.ID, "forum.home", plugin.ID+".home") {
		t.Fatal("plugin runtime fixture was not registered")
	}
	if err := os.Remove(filepath.Join(plugin.PackagePath, "theme.json")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RegisterPluginPackage(t.Context(), plugin); err != nil {
		t.Fatal(err)
	}
	if snapshot, exists := registry.ExtensionSnapshot(plugin.ID); exists {
		t.Fatalf("backend-only plugin retained Page Registry artifact: %#v", snapshot)
	}
	if runtimeRegistry.Claims(plugin.ID, "forum.home", plugin.ID+".home") {
		t.Fatal("backend-only plugin retained ThemeRuntime state")
	}
}

func TestPageRegistryAdapterRestoresDefaultFallbackBeforeCustomTheme(t *testing.T) {
	registry := pages.NewRegistry(pages.NewMemoryStore())
	runtimeRegistry := pages.NewThemeRuntimeRegistry()
	adapter := NewPageRegistryAdapter(registry).WithThemeRuntime(runtimeRegistry, "SForum", []string{"zh-CN"})
	defaultTheme := themeRuntimeExtensionFixture(t, DefaultThemeID, strings.Repeat("9", 64), "/default", false)
	custom := themeRuntimeExtensionFixture(t, "custom.theme", strings.Repeat("a", 64), "/custom", false)
	writeThemeRuntimeExtensionFile(t, custom.PackagePath, "templates/home.html", `<main>{{asset "missing"}}</main><sf-home-page></sf-home-page>`)

	if err := adapter.RegisterDefaultThemeFallback(context.Background(), defaultTheme); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RegisterThemePackage(context.Background(), custom); err != nil {
		t.Fatal(err)
	}
	renderer, ok := runtimeRegistry.Resolve(pages.RuntimeArtifact{
		ExtensionID: custom.ID, ExtensionVersion: custom.Version, PackageDigest: custom.PackageDigest,
	}, "forum.home", custom.ID+".home")
	if !ok {
		t.Fatal("custom theme runtime did not resolve")
	}
	output, err := renderer.Render(context.Background(), pages.CorePageViewModelRequest{
		PageID: "forum.home", Locale: "zh-CN", Path: "/",
	}, custom.ID+".home")
	if err != nil || output.Source != pages.ThemeRenderSourceDefaultTheme || !output.Fallback {
		t.Fatalf("output=%#v err=%v", output, err)
	}
}

func themeRuntimeExtensionFixture(t *testing.T, id, digest, addPath string, includeAdd bool) Extension {
	t.Helper()
	root := t.TempDir()
	pagesJSON := `[{"id":"` + id + `.home","action":"replace","target":"forum.home","template":"templates/home.html","contract":"sforum.page.home@1"}`
	if includeAdd {
		pagesJSON += `,{"id":"` + id + `.add","action":"add","path":"` + addPath + `","template":"templates/add.html","contract":"` + id + `.page@1"}`
	}
	pagesJSON += `]`
	writeThemeRuntimeExtensionFile(t, root, "theme.json", `{"pages":`+pagesJSON+`}`)
	writeThemeRuntimeExtensionFile(t, root, "templates/home.html", `<main>home</main><sf-home-page></sf-home-page>`)
	writeThemeRuntimeExtensionFile(t, root, "templates/add.html", `<main>add</main>`)
	return Extension{ID: id, Type: TypeTheme, Status: StatusEnabled, Version: "1.0.0", PackageDigest: digest, PackagePath: root}
}

func exactThemeRuntimeExtensionFixture(t *testing.T, id, addPath string) Extension {
	t.Helper()
	item := themeRuntimeExtensionFixture(t, id, "", addPath, false)
	if err := os.WriteFile(filepath.Join(item.PackagePath, ManifestFileName), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(item.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	item.PackageDigest = digest
	return item
}

func writeThemeRuntimeExtensionFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertActiveThemeRuntime(t *testing.T, registry *pages.ThemeRuntimeRegistry, extensionID string) {
	t.Helper()
	snapshot, _, ok := registry.Active()
	if !ok || snapshot.Artifact().ExtensionID != extensionID {
		t.Fatalf("active runtime=%#v", snapshot)
	}
}
