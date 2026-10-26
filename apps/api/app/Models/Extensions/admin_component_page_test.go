package extensions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestAdminComponentPageBootstrapAndAssetEnforcePagePermission(t *testing.T) {
	root := t.TempDir()
	entry := "frontend/admin/dist/dashboard.mjs"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, entry)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, entry), []byte("export const apiVersion = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	item := installedExtension("component.plugin", TypePlugin, ManifestBackend{})
	item.Status = StatusEnabled
	item.Source = SourceUploaded
	item.IsDeletable = true
	item.PackagePath = root
	item.Manifest.Admin.Pages = []ManifestAdminPage{{
		Path: "/dashboard", Label: "Dashboard", View: "component", Permission: "component.dashboard.view",
		Component: &AdminComponent{ID: "dashboard", APIVersion: 1, Entry: entry},
	}}
	item.AdminFrontendDigest, _ = ComputeAdminFrontendDigest(item.Manifest, root)
	item.PackageDigest, _ = extensionpackage.DigestTree(root)
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	denied := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionExtensionView: true,
	}}
	if _, err := service.AdminPageBootstrap(context.Background(), denied, item.ID, "/dashboard", "en-US"); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("page bootstrap without declared permission = %v", err)
	}

	allowed := identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionExtensionView: true, "component.dashboard.view": true,
	}}
	bootstrap, err := service.AdminPageBootstrap(context.Background(), allowed, item.ID, "/dashboard", "en-US")
	if err != nil || bootstrap.Page == nil || bootstrap.Page.Component == nil || bootstrap.Page.Component.ID != "dashboard" {
		t.Fatalf("allowed component bootstrap = %#v, %v", bootstrap, err)
	}

	trustStore := &memoryExecutableTrustStore{}
	impact, err := buildTrustImpact(item, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trustStore.EnsureLiveGrant(context.Background(), TrustEnsureGrantInput{
		Identity: trustIdentity(impact), ActorUserID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	exactTrust := NewExecutableTrustService(store, trustStore)
	frontend := NewFrontendService(&fakeFrontendExtensionReader{item: item}, &fakeFrontendTrustStore{}).
		WithExecutableTrust(exactTrust, true)
	if _, err := frontend.ComponentAsset(context.Background(), denied, item.ID, item.AdminFrontendDigest, "dashboard", "entry"); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("component asset without page permission = %v", err)
	}
	asset, err := frontend.ComponentAsset(context.Background(), allowed, item.ID, item.AdminFrontendDigest, "dashboard", "entry")
	if err != nil {
		t.Fatal(err)
	}
	if string(asset.Body) != "export const apiVersion = 1\n" || asset.ContentType != "application/javascript; charset=utf-8" || asset.ETag == "" {
		t.Fatalf("allowed component asset = %#v", asset)
	}
}
