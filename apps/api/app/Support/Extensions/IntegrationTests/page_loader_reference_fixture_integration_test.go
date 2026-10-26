package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/bootstrap"
)

// A1（M2 任务 5）：带 typed loader data 的可执行插件页。
//
// 这条测试跑的是**真实链**，不是接口桩：
//  1. 把 durable 夹具 `extensions/fixtures/plugins/sforum-page-loader-reference` 复制到临时目录，
//     现场构建它的 Protocol V2 后端，并把 manifest 的 backend/executable 摘要改成新二进制的摘要；
//  2. 用 `LoadPackage` 证明这份精确制品是合法包；
//  3. 启动真实 V2 子进程运行时（exact instance + admission）；
//  4. 用页面注册表登记夹具的 `theme.json` 页面贡献并解析路径，确认贡献声明了
//     `dataSource: plugin` + `dataRoute` + 冻结制品；
//  5. 走生产 Loader Gateway 取数，断言插件返回的**类型化文档**真的到达渲染层。
func TestReferencePageLoaderFixtureServesTypedDataThroughProductionChain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping protocol v2 subprocess build in short mode")
	}
	packageRoot := stagePageLoaderReferenceFixture(t)
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load staged reference package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	extension := extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: packageDigest, PackagePath: packageRoot,
		Manifest: manifest,
	}

	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "41", ImpactDigest: "impact-41",
		}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := manager.Start(ctx, extension); err != nil {
		t.Fatalf("start reference page loader plugin: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Identity.InstanceID == "" || active.Target.BaseURL != "" {
		t.Fatalf("expected an exact non-loopback instance: %#v", active)
	}

	// 页面贡献来自夹具自己的 theme.json，经真实注册表登记。
	registry := pages.NewRegistry(pages.NewMemoryStore())
	bridge := pages.NewExtensionBridge(registry)
	if err := bridge.RegisterPluginPackage(ctx, pages.ThemeExtension{
		ID: extension.ID, Version: extension.Version, PackagePath: packageRoot, PackageDigest: extension.PackageDigest,
	}); err != nil {
		t.Fatalf("register plugin page package: %v", err)
	}
	contrib, ok := registry.ResolveAddedPath("/loader-articles/hello")
	if !ok {
		t.Fatal("reference fixture page contribution was not registered")
	}
	if contrib.DataSource != "plugin" || strings.TrimSpace(contrib.DataRoute) != "/page-data/article" {
		t.Fatalf("page contribution data contract = %#v", contrib)
	}

	// 生产 Loader Gateway：无 loopback，走 Protocol V2 精确实例调用。
	loader := pages.NewLoaderGateway(
		pages.NewPageDataLoader(nil),
		bootstrap.NewPageRouteTargetSourceWithRoutes(manager, pageLoaderReferenceStore{item: extension}),
	)
	contrib.ExtensionID = extension.ID
	contrib.Version = extension.Version
	contrib.PackageDigest = extension.PackageDigest
	contrib.RuntimeInstanceID = active.Identity.InstanceID
	result := loader.LoadExactForContribution(ctx, contrib, map[string]string{"slug": "hello"}, "zh-CN", 0)
	if result.Fallback || result.Status != http.StatusOK || len(result.Data) == 0 {
		t.Fatalf("reference loader result = %#v", result)
	}
	var document map[string]any
	if err := json.Unmarshal(result.Data, &document); err != nil {
		t.Fatalf("decode loader document: %v", err)
	}
	// 断言的是插件运行时的内容真的到达渲染层（不是「接口 200」）。
	if document["title"] != "Loaded from the plugin runtime: hello" || document["state"] != "published" {
		t.Fatalf("loader document = %#v", document)
	}
	// 夹具声明的页面 schema 必须真的被用来校验。
	//
	// 注意（实测，勿臆断）：`validateAgainstSimpleSchema` 是**轻量**校验，只覆盖
	// `type` + `required`；`enum`/`format` 这类约束由编译后的 ThemeRuntimeSnapshot
	// 在模板执行前按包内精确 schema 校验（见 LoadExactForContribution 的注释）。
	// 因此这里断言 required 缺失被拒绝，而不是假装 enum 会被这里拦住。
	schemaJSON := string(mustReadFile(t, filepath.Join(packageRoot, "schemas/article.json")))
	strict := pages.NewPageDataLoader(nil)
	missingRequired := strict.FetchV2(ctx, pages.LoaderRequest{
		ExtensionID: extension.ID, Route: "/page-data/article", SchemaJSON: schemaJSON,
	}, func(context.Context, string, map[string]string, string, int64) ([]byte, int, error) {
		return []byte(`{"title":"x"}`), http.StatusOK, nil
	})
	if !missingRequired.Fallback || missingRequired.Status != http.StatusBadGateway {
		t.Fatalf("declared page schema required-fields check was not enforced: %#v", missingRequired)
	}
	// 同一份文档满足 required 时必须通过（证明上面不是因为别的原因失败）。
	complete := strict.FetchV2(ctx, pages.LoaderRequest{
		ExtensionID: extension.ID, Route: "/page-data/article", SchemaJSON: schemaJSON,
	}, func(context.Context, string, map[string]string, string, int64) ([]byte, int, error) {
		return []byte(`{"title":"x","state":"published"}`), http.StatusOK, nil
	})
	if complete.Fallback || complete.Status != http.StatusOK {
		t.Fatalf("schema-valid document was rejected: %#v", complete)
	}
}

type pageLoaderReferenceStore struct {
	item extensions.Extension
}

func (s pageLoaderReferenceStore) Get(context.Context, string) (extensions.Extension, error) {
	return s.item, nil
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// stagePageLoaderReferenceFixture 复制夹具、现场构建后端，并把 manifest 的二进制摘要
// 对齐到新构建产物（夹具在源码树里始终可被 `extension validate/test` 直接校验）。
func stagePageLoaderReferenceFixture(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	// 本文件位于 apps/api/app/Support/Extensions/IntegrationTests，需上溯 6 层到仓库根。
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../.."))
	fixtureRoot := filepath.Join(repositoryRoot, "extensions/fixtures/plugins/sforum-page-loader-reference")
	packageRoot := filepath.Join(t.TempDir(), "sforum.page-loader-reference", "1.0.0")
	if err := os.MkdirAll(packageRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy %s: %v", fixtureRoot, err)
	}
	// 临时副本里不携带构建产物，避免把宿主架构的二进制带进断言。
	_ = os.Remove(filepath.Join(packageRoot, "backend", "plugin"))

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
		t.Fatalf("build reference page loader backend: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	digest := hex.EncodeToString(sum[:])

	manifestPath := filepath.Join(packageRoot, extensionmanifest.ManifestFileName)
	var manifest map[string]any
	if err := json.Unmarshal(mustReadFile(t, manifestPath), &manifest); err != nil {
		t.Fatal(err)
	}
	backend, _ := manifest["backend"].(map[string]any)
	backend["digest"] = digest
	for _, raw := range manifest["packageFiles"].([]any) {
		file, _ := raw.(map[string]any)
		if file["path"] == "backend/plugin" {
			file["digest"] = digest
		}
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return packageRoot
}
