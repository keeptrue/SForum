package extensionsruntime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func TestReferenceAdminPluginInvokesEverySurfaceThroughRealProtocolV2(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference plugin subprocess build in short mode")
	}
	extension := buildReferenceAdminExtension(t)
	if len(extension.Manifest.PermissionDefinitions) != 1 ||
		extension.Manifest.PermissionDefinitions[0].AssignmentPolicy != "host" ||
		len(extension.Manifest.PermissionDefinitions[0].RecommendedRoles) == 0 {
		t.Fatalf("reference permission must remain Host-assigned: %#v", extension.Manifest.PermissionDefinitions)
	}
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "admin-reference", ImpactDigest: extension.PackageDigest,
		}},
		HostAPI: gateway,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start reference admin plugin: %v", err)
	}
	t.Cleanup(func() { _ = manager.Stop(context.Background(), extension) })

	wantKinds := map[string]bool{
		"navigation": false, "dashboard": false, "list_column": false, "list_filter": false,
		"row_action": false, "bulk_action": false, "form": false, "notice": false,
		"editor_panel": false, "detail_region": false, "importer": false, "exporter": false,
	}
	commandAuthorityChecked := false
	for _, declaration := range extension.Manifest.AdminSurfaces {
		contract, err := manager.ResolveAdminSurface(declaration.ID)
		if err != nil {
			t.Fatalf("resolve %s: %v", declaration.ID, err)
		}
		wantKinds[declaration.Kind] = true
		input := referenceAdminSurfaceInput(declaration.Kind)
		if declaration.Operation == extensions.AdminSurfaceOperationCommand && !commandAuthorityChecked {
			_, err := manager.InvokeAdminSurface(t.Context(), extensionsruntime.AdminSurfaceInvocation{
				ExpectedContract: contract, ContractVersion: declaration.ContractVersion, Input: input,
			})
			if !errors.Is(err, extensionsruntime.ErrProtocolV2ActorDelegationInvalid) {
				t.Fatalf("command without actor/idempotency = %v", err)
			}
			commandAuthorityChecked = true
		}
		key := "admin-reference-" + strings.TrimPrefix(declaration.ID, "sforum.admin-surface-reference.surface.")
		result, err := manager.InvokeAdminSurface(t.Context(), extensionsruntime.AdminSurfaceInvocation{
			ExpectedContract: contract,
			ContractVersion:  declaration.ContractVersion,
			Input:            input,
			Actor:            extensionsruntime.NewProtocolV2RouteActor(42, true, map[string]bool{"sforum.admin-surface-reference.manage": true}),
			IdempotencyKey:   key,
		})
		if err != nil {
			t.Fatalf("invoke %s: %v", declaration.ID, err)
		}
		if result.Contract.ID != declaration.ID || len(result.Output) == 0 {
			t.Fatalf("surface %s result = %#v", declaration.ID, result)
		}
		if declaration.Operation == extensions.AdminSurfaceOperationCommand && !referenceResultContains(result.Output, key) {
			t.Fatalf("surface %s did not receive the exact idempotency key: %#v", declaration.ID, result.Output)
		}
	}
	if !commandAuthorityChecked {
		t.Fatal("reference manifest did not exercise a command surface")
	}
	for kind, covered := range wantKinds {
		if !covered {
			t.Fatalf("reference plugin did not exercise %s", kind)
		}
	}
}

func buildReferenceAdminExtension(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := referenceAdminFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum.admin-surface-reference")
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy reference plugin: %v", err)
	}
	binaryPath := filepath.Join(packageRoot, "backend", "plugin")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binaryPath, ".")
	moduleRoot := filepath.Join(fixtureRoot, "backend")
	build.Dir = moduleRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK="+temporaryPluginWorkspace(
		t, filepath.Clean(filepath.Join(fixtureRoot, "../../../..")), moduleRoot,
	))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reference plugin: %v\n%s", err, output)
	}
	digests := map[string]string{
		"__BACKEND_DIGEST__": fileSHA256(t, binaryPath),
		"__PROPS_DIGEST__":   fileSHA256(t, filepath.Join(packageRoot, "schemas", "surface-props.json")),
		"__RESULT_DIGEST__":  fileSHA256(t, filepath.Join(packageRoot, "schemas", "surface-result.json")),
	}
	templatePath := filepath.Join(packageRoot, "sforum.extension.json.tmpl")
	templateBody, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := string(templateBody)
	for token, digest := range digests {
		manifestBody = strings.ReplaceAll(manifestBody, token, digest)
	}
	if strings.Contains(manifestBody, "__") {
		t.Fatal("reference manifest contains an unresolved digest token")
	}
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load exact reference package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}

func referenceAdminFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins/sforum-admin-surface-reference"))
}

func referenceAdminSurfaceInput(kind string) map[string]any {
	return map[string]any{
		"placementId":              "core.component.page.admin.users",
		"placementContractVersion": "sforum.component.page.admin.users@1",
		"kind":                     kind,
		"locale":                   "en-US",
		"route":                    map[string]any{"path": "/admin/users"},
		"context": map[string]any{
			"resourceType": "user",
			"resource":     map[string]any{"id": 1, "displayName": "Admin", "status": "active"},
		},
		"resources": []any{
			map[string]any{"id": "1", "attributes": map[string]any{"status": "active"}},
			map[string]any{"id": "2", "attributes": map[string]any{"status": "disabled"}},
		},
		"resourceIds":     []any{"1", "2"},
		"filters":         map[string]any{},
		"selection":       "active",
		"sourceSurfaceId": "sforum.admin-surface-reference.surface.form-view",
		"values":          map[string]any{"note": "Reviewed", "mode": "validate"},
	}
}

func referenceResultContains(output map[string]any, expected string) bool {
	items, _ := output["items"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["value"] == expected {
			return true
		}
	}
	return false
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
