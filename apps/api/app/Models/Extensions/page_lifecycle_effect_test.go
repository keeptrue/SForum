package extensions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

func fixturePageRegistryDemo(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../"))
	return filepath.Join(root, "extensions/fixtures/plugins/page-registry-demo")
}

// TestEnableDisableSyncsPageRegistry 证明同步生命周期会立即更新页面，无需 API 重启。
func TestEnableDisableSyncsPageRegistry(t *testing.T) {
	root := fixturePageRegistryDemo(t)
	if _, err := os.Stat(filepath.Join(root, "theme.json")); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	reg := pages.NewRegistry(pages.NewMemoryStore())
	adapter := NewPageRegistryAdapter(reg)
	ext := Extension{
		ID: "sforum.page-registry-demo", Type: TypePlugin, Status: StatusEnabled,
		Version: "1.0.0", PackageDigest: "digest-v1", PackagePath: root,
	}
	// enable effect → RegisterPluginPackage
	if err := adapter.RegisterPluginPackage(context.Background(), ext); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := reg.ResolveAddedPath("/demo-docs/hello"); !ok {
		t.Fatal("page should appear immediately after enable")
	}
	// disable effect → ClearExtension
	adapter.ClearExtension(ext.ID)
	if _, ok := reg.ResolveAddedPath("/demo-docs/hello"); ok {
		t.Fatal("page should disappear immediately after disable")
	}
	// re-enable without restart
	if err := adapter.RegisterPluginPackage(context.Background(), ext); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.ResolveAddedPath("/demo-docs/hello"); !ok {
		t.Fatal("page should reappear after re-enable without API restart")
	}
}
