package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
	pages "github.com/zhuchunshu/sforum/apps/api/app/Support/Pages"
	routes "github.com/zhuchunshu/sforum/apps/api/app/Support/Routes"
	themecompiler "github.com/zhuchunshu/sforum/apps/api/app/Support/ThemeCompiler"
)

func TestLifecyclePageRuntimePublishesExactPluginBusinessContractAndRemovesSource(t *testing.T) {
	ctx := context.Background()
	pageRegistry := pages.NewRegistry(nil)
	themeRuntime := pages.NewThemeRuntimeRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Pages: pageRegistry, ThemeRuntime: themeRuntime, PageSiteName: "SForum", PageLocales: []string{"zh-CN"},
	})
	source := lifecyclePluginBusinessMaterial(t, "1.0.0", "runtime-source", strings.Repeat("a", 64), "source")
	if err := boundary.reconcilePages(ctx, source.extension.ID, nil, nil, &source); err != nil {
		t.Fatal(err)
	}
	sourceArtifact := lifecyclePageArtifact(source.extension, source.binding)
	if _, ok := themeRuntime.Resolve(sourceArtifact, source.pages[0].ID, source.pages[0].ID); !ok {
		t.Fatal("source exact plugin page runtime was not published")
	}

	target := lifecyclePluginBusinessMaterial(t, "2.0.0", "runtime-target", strings.Repeat("b", 64), "target")
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				renderer, ok := themeRuntime.Resolve(sourceArtifact, source.pages[0].ID, source.pages[0].ID)
				if !ok {
					return
				}
				output, err := renderer.RenderPluginData(
					ctx, json.RawMessage(`{"title":"source"}`), themecompiler.PageSEOView{Title: "Article"}, source.pages[0].ID,
				)
				if err != nil || output.Source != pages.ThemeRenderSourcePlugin {
					t.Errorf("source render output=%#v err=%v", output, err)
					return
				}
			}
		}()
	}
	if err := boundary.reconcilePages(ctx, target.extension.ID, &source, &target, &target); err != nil {
		t.Fatal(err)
	}
	readers.Wait()
	if _, ok := themeRuntime.Resolve(sourceArtifact, source.pages[0].ID, source.pages[0].ID); ok {
		t.Fatal("superseded source plugin page runtime remained visible")
	}
	targetArtifact := lifecyclePageArtifact(target.extension, target.binding)
	renderer, ok := themeRuntime.Resolve(targetArtifact, target.pages[0].ID, target.pages[0].ID)
	if !ok {
		t.Fatal("target exact plugin page runtime was not published")
	}
	output, err := renderer.RenderPluginData(
		ctx, json.RawMessage(`{"title":"target"}`), themecompiler.PageSEOView{Title: "Article"}, target.pages[0].ID,
	)
	if err != nil || output.Source != pages.ThemeRenderSourcePlugin || !strings.Contains(renderer.LegacyHTML(output), "target: target") {
		t.Fatalf("target output=%#v html=%q err=%v", output, renderer.LegacyHTML(output), err)
	}
	registered, ok := pageRegistry.ResolveAddedPath(target.pages[0].Path)
	if !ok || registered.RuntimeInstanceID != target.binding.RuntimeInstanceID || registered.PackageDigest != target.extension.PackageDigest {
		t.Fatalf("target Page Registry contribution=%#v ok=%t", registered, ok)
	}
}

func TestLifecyclePageRuntimeBootReplacesLegacyArtifactWithExactCompiledRuntime(t *testing.T) {
	ctx := context.Background()
	extension := lifecycleBootPluginBusinessExtension(t)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}

	pkg, err := pages.LoadThemePackage(extensions.PackageContentRoot(extension))
	if err != nil {
		t.Fatal(err)
	}
	legacyContributions := pages.ContributionsFromTheme(extension.ID, extension.Version, extension.PackageDigest, pkg)
	for index := range legacyContributions {
		legacyContributions[index].RuntimeInstanceID = "legacy-runtime"
	}
	legacyArtifact := pages.RuntimeArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: "legacy-runtime",
	}
	pageRegistry := pages.NewRegistry(nil)
	if _, err := pageRegistry.PublishExtensionIfRevision(legacyArtifact, legacyContributions, 0); err != nil {
		t.Fatal(err)
	}
	themeRuntime := pages.NewThemeRuntimeRegistry()
	legacyBinding := extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
		RuntimeInstanceID: legacyArtifact.RuntimeInstanceID, VersionID: extension.ActiveVersionID,
	}
	legacyRuntime, err := buildExactPluginPageRuntime(extension, legacyBinding, legacyContributions, "Legacy", []string{"en-US"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := themeRuntime.Stage(legacyRuntime); err != nil {
		t.Fatal(err)
	}

	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pageRegistry, ThemeRuntime: themeRuntime,
		PageSiteName: "SForum", PageLocales: []string{"zh-CN"},
		Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
	})
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	exactArtifact := pages.RuntimeArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: active.Identity.InstanceID,
	}
	contribution, ok := pageRegistry.ResolveAddedPath("/business-article")
	if !ok || contribution.RuntimeInstanceID != exactArtifact.RuntimeInstanceID || contribution.PackageDigest != exactArtifact.PackageDigest {
		t.Fatalf("restored exact contribution=%#v ok=%t", contribution, ok)
	}
	renderer, ok := themeRuntime.Resolve(exactArtifact, contribution.ID, contribution.ID)
	if !ok {
		t.Fatal("boot did not compile the exact plugin ThemeRuntime snapshot")
	}
	contract, ok := renderer.PluginDataContract(contribution.ID)
	if !ok || contract.ViewModelID != extension.ID+".template.article" || contract.SchemaVersion != extension.ID+".article.data@1" {
		t.Fatalf("restored plugin contract=%#v ok=%t", contract, ok)
	}
	output, err := renderer.RenderPluginData(
		ctx, json.RawMessage(`{"title":"restored"}`), themecompiler.PageSEOView{Title: "Article"}, contribution.ID,
	)
	if err != nil || output.Source != pages.ThemeRenderSourcePlugin || !strings.Contains(renderer.LegacyHTML(output), "boot: restored") {
		t.Fatalf("restored output=%#v html=%q err=%v", output, renderer.LegacyHTML(output), err)
	}
	if _, ok := themeRuntime.Resolve(legacyArtifact, contribution.ID, contribution.ID); ok {
		t.Fatal("legacy process-local plugin ThemeRuntime survived exact boot restoration")
	}
}

// 无页面贡献的后端插件即使 active runtime 已 drain，也不得在 page restore
// 上 fail-closed，且不得发布任何 page 贡献。
func TestLifecyclePageRestoreSkipsBackendPluginWithoutPageContributions(t *testing.T) {
	ctx := context.Background()
	extension := lifecycleBackendOnlyPluginExtension(t, "plugin.backend-only")
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatal("fresh active runtime must be available before drain")
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatal("drained runtime must be unavailable")
	}

	pageRegistry := pages.NewRegistry(nil)
	themeRuntime := pages.NewThemeRuntimeRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pageRegistry, ThemeRuntime: themeRuntime,
		PageSiteName: "SForum", PageLocales: []string{"zh-CN"},
	})
	if err := boundary.restoreExactPluginPagePublications(ctx, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("backend-only plugin must not fail page restore: %v", err)
	}
	if _, ok := pageRegistry.ResolveAddedPath("/business-article"); ok {
		t.Fatal("backend-only plugin published a page contribution")
	}
	if themeRuntime.Claims(extension.ID, "", "") {
		t.Fatal("backend-only plugin staged ThemeRuntime without pages")
	}
	// 确认包确实无页面贡献（回归守卫：fixture 本身不得带 theme.json pages）。
	pkg, err := pages.LoadThemePackage(extensions.PackageContentRoot(extension))
	if err != nil {
		t.Fatal(err)
	}
	if got := pages.ContributionsFromTheme(extension.ID, extension.Version, extension.PackageDigest, pkg); len(got) != 0 {
		t.Fatalf("fixture must have zero page contributions, got %#v", got)
	}
}

// 真实页面贡献者在 runtime draining/unavailable 时仍必须在任何 page/runtime
// 发布之前 fail-closed。
func TestLifecyclePageRestoreFailClosedWhenPageContributorRuntimeUnavailable(t *testing.T) {
	ctx := context.Background()
	extension := lifecycleBootPluginBusinessExtension(t)
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeInstanceAvailable(active.Identity) {
		t.Fatal("drained page contributor runtime must be unavailable")
	}

	pageRegistry := pages.NewRegistry(nil)
	themeRuntime := pages.NewThemeRuntimeRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Pages: pageRegistry, ThemeRuntime: themeRuntime,
		PageSiteName: "SForum", PageLocales: []string{"zh-CN"},
	})
	err = boundary.restoreExactPluginPagePublications(ctx, []extensions.Extension{extension}, false)
	if !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("page contributor unavailable error=%v", err)
	}
	if !strings.Contains(err.Error(), "startup page runtime") {
		t.Fatalf("error must name page runtime fence: %v", err)
	}
	if _, ok := pageRegistry.ResolveAddedPath("/business-article"); ok {
		t.Fatal("unavailable page contributor published a page")
	}
	exactArtifact := pages.RuntimeArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: active.Identity.InstanceID,
	}
	if _, ok := themeRuntime.Resolve(exactArtifact, "plugin.business-page.article", "plugin.business-page.article"); ok {
		t.Fatal("unavailable page contributor staged ThemeRuntime before fail-closed")
	}
}

func TestLifecyclePageRuntimeFailedPublicationRemovesStagedTarget(t *testing.T) {
	ctx := context.Background()
	pageRegistry := pages.NewRegistry(nil)
	themeRuntime := pages.NewThemeRuntimeRegistry()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Pages: pageRegistry, ThemeRuntime: themeRuntime,
	})
	target := lifecyclePluginBusinessMaterial(t, "2.0.0", "runtime-target", strings.Repeat("b", 64), "target")
	foreignArtifact := pages.RuntimeArtifact{
		ExtensionID: target.extension.ID, ExtensionVersion: "9.9.9",
		PackageDigest: strings.Repeat("f", 64), RuntimeInstanceID: "foreign-runtime",
	}
	if _, err := pageRegistry.PublishExtensionIfRevision(foreignArtifact, target.pages, 0); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcilePages(ctx, target.extension.ID, nil, &target, &target); !errors.Is(err, ErrLifecycleRegistryPublicationConflict) {
		t.Fatalf("foreign page artifact error=%v", err)
	}
	targetArtifact := lifecyclePageArtifact(target.extension, target.binding)
	if _, ok := themeRuntime.Resolve(targetArtifact, target.pages[0].ID, target.pages[0].ID); ok ||
		themeRuntime.Claims(target.extension.ID, target.pages[0].ID, target.pages[0].ID) {
		t.Fatal("failed Page Registry publication leaked its staged ThemeRuntime")
	}
}

func lifecyclePluginBusinessMaterial(
	t *testing.T,
	version, runtimeID, packageDigest, marker string,
) lifecycleRegistryMaterial {
	t.Helper()
	const extensionID = "plugin.business-page"
	root := t.TempDir()
	templateBody := `<article>` + marker + `: {{.title}}</article>`
	schemaBody := `{"type":"object","required":["title"],"additionalProperties":false,"properties":{"title":{"type":"string"}}}`
	writeLifecyclePageRuntimeFile(t, root, "theme.json", `{"pages":[{
  "id":"plugin.business-page.article","action":"add","path":"/business-article",
  "template":"templates/article.html","contract":"plugin.business-page.page.article@1","access":"public",
  "data":{"source":"plugin","route":"/page-data/article","schema":"schemas/article.json"}
}]}`)
	writeLifecyclePageRuntimeFile(t, root, "templates/article.html", templateBody)
	writeLifecyclePageRuntimeFile(t, root, "schemas/article.json", schemaBody)
	templateDigest := sha256.Sum256([]byte(templateBody))
	schemaDigest := sha256.Sum256([]byte(schemaBody))
	extension := extensions.Extension{
		ID: extensionID, Version: version, Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: packageDigest, PackagePath: root, ActiveVersionID: 1,
		Manifest: extensions.Manifest{
			ManifestVersion: extensionmanifest.ManifestVersionV3,
			ID:              extensionID, Version: version, Type: extensions.TypePlugin, SForumVersion: ">=0.1.0",
			Templates: []extensions.ManifestTemplate{{
				ID: extensionID + ".template.article", ContractVersion: extensionID + ".template.article@1",
				Action: "add", Path: "templates/article.html", Digest: hex.EncodeToString(templateDigest[:]),
				ViewModelSchema: extensionID + ".article.data@1", ThemeOverrideKey: extensionID + ".article",
			}},
			PackageFiles: []extensions.ManifestPackageFile{
				{ID: extensionID + ".file.template", Kind: "template", Path: "templates/article.html", Digest: hex.EncodeToString(templateDigest[:])},
				{ID: extensionID + ".article.data", Kind: "schema", Version: "1", Path: "schemas/article.json", Digest: hex.EncodeToString(schemaDigest[:])},
			},
		},
	}
	binding := extensions.LifecycleRuntimeBinding{
		ExtensionID: extensionID, ExtensionVersion: version, PackageDigest: packageDigest,
		RuntimeInstanceID: runtimeID, VersionID: 1,
	}
	contribution := pages.PageContribution{
		ID: extensionID + ".article", Action: pages.ActionAdd, Path: "/business-article", Template: "templates/article.html",
		Contract: extensionID + ".page.article@1", Access: pages.AccessPublic,
		DataSource: "plugin", DataRoute: "/page-data/article", DataSchema: "schemas/article.json",
		ExtensionID: extensionID, Version: version, PackageDigest: packageDigest, RuntimeInstanceID: runtimeID,
	}
	return lifecycleRegistryMaterial{extension: extension, binding: binding, pages: []pages.PageContribution{contribution}}
}

func lifecycleBootPluginBusinessExtension(t *testing.T) extensions.Extension {
	t.Helper()
	material := lifecyclePluginBusinessMaterial(t, "1.0.0", "unused", strings.Repeat("a", 64), "boot")
	extension := material.extension
	backendBody := []byte("business-page-runtime")
	backendDigest := sha256.Sum256(backendBody)
	backendDigestValue := hex.EncodeToString(backendDigest[:])
	extension.Manifest.Backend = extensions.ManifestBackend{
		Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
		Digest: backendDigestValue, HostAPIVersion: "sforum.host@2",
	}
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: extension.ID + ".file.backend", Kind: "executable", Path: "bin/plugin", Digest: backendDigestValue,
	})
	writeLifecyclePageRuntimeFile(t, extension.PackagePath, "bin/plugin", string(backendBody))
	manifestBody, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeLifecyclePageRuntimeFile(t, extension.PackagePath, extensionmanifest.ManifestFileName, string(manifestBody))
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

// lifecycleBackendOnlyPluginExtension 构造有 Protocol V2 backend、无 theme.json
// 页面贡献的启用插件，模拟 sforum.content-policy 一类纯后端包。
func lifecycleBackendOnlyPluginExtension(t *testing.T, extensionID string) extensions.Extension {
	t.Helper()
	root := t.TempDir()
	backendBody := []byte("backend-only-runtime")
	backendDigest := sha256.Sum256(backendBody)
	backendDigestValue := hex.EncodeToString(backendDigest[:])
	writeLifecyclePageRuntimeFile(t, root, "bin/plugin", string(backendBody))
	extension := extensions.Extension{
		ID: extensionID, Version: "1.1.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackagePath: root, ActiveVersionID: 1, Name: "Backend Only",
		Manifest: extensions.Manifest{
			ManifestVersion: extensionmanifest.ManifestVersionV3,
			ID:              extensionID, Name: "Backend Only", Version: "1.1.0",
			Type: extensions.TypePlugin, SForumVersion: ">=0.1.0",
			Backend: extensions.ManifestBackend{
				Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
				Digest: backendDigestValue, HostAPIVersion: "sforum.host@2",
			},
			PackageFiles: []extensions.ManifestPackageFile{{
				ID: extensionID + ".file.backend", Kind: "executable", Path: "bin/plugin", Digest: backendDigestValue,
			}},
		},
	}
	manifestBody, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeLifecyclePageRuntimeFile(t, root, extensionmanifest.ManifestFileName, string(manifestBody))
	extension.PackageDigest, err = extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func writeLifecyclePageRuntimeFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
