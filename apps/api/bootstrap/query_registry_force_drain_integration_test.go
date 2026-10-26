package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

const (
	productionQueryForceOwnerID  = "sforum.query-reference"
	productionQueryForceFilterID = "sforum.query-filter-reference"
	productionQueryForceQueryID  = "sforum.query-reference.cacheable"
)

func TestProductionQueryProtocolV2ForceDrainJoined(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production Query subprocess ForceDrain gate in short mode")
	}
	owner, filter := buildProductionQueryForcePackages(t)

	t.Run("cross plugin filter normal", func(t *testing.T) {
		fixture := newProductionQueryForceFixture(t, owner, filter)
		result, err := fixture.runtime.Execute(t.Context(), queryregistry.PlanRequest{
			QueryID:    productionQueryForceQueryID,
			Fields:     []string{"id", "title", "score"},
			Pagination: queryregistry.PaginationRequest{Limit: 1},
		})
		if err != nil || len(result.Rows) != 1 || result.Rows[0]["title"] != "item-5 | externally masked" {
			t.Fatalf("cross-plugin Query filter result=%#v err=%v", result, err)
		}
		fixture.waitReleased(t)
	})

	t.Run("owner provider", func(t *testing.T) {
		fixture := newProductionQueryForceFixture(t, owner, filter)
		result := fixture.executeBlocked(t, "timeout", fixture.ownerReady)
		cause := errors.New("production owner ForceDrain")
		forced, err := fixture.manager.ForceDrain(fixture.ownerRuntime.Identity, cause)
		if err != nil || !forced.Forced || forced.ActiveTotal != 1 {
			t.Fatalf("owner ForceDrain snapshot=%#v err=%v", forced, err)
		}
		assertProductionQueryForceResult(t, result, cause)
		fixture.waitReleased(t)
		ownerAfter, err := fixture.manager.InspectRuntimeInstance(fixture.ownerRuntime.Identity)
		if err != nil || !ownerAfter.Active || !ownerAfter.Admission.Forced {
			t.Fatalf("owner after ForceDrain=%#v err=%v", ownerAfter, err)
		}
		filterAfter, err := fixture.manager.InspectRuntimeInstance(fixture.filterRuntime.Identity)
		if err != nil || !filterAfter.Active || filterAfter.Admission.Forced ||
			!fixture.manager.RuntimeInstanceAvailable(filterAfter.Identity) {
			t.Fatalf("filter changed by owner ForceDrain=%#v err=%v", filterAfter, err)
		}
	})

	t.Run("cross plugin fail open filter", func(t *testing.T) {
		fixture := newProductionQueryForceFixture(t, owner, filter)
		result := fixture.executeBlocked(t, "filter-timeout", fixture.filterReady)
		cause := errors.New("production filter ForceDrain")
		forced, err := fixture.manager.ForceDrain(fixture.filterRuntime.Identity, cause)
		if err != nil || !forced.Forced || forced.ActiveTotal != 1 {
			t.Fatalf("filter ForceDrain snapshot=%#v err=%v", forced, err)
		}
		assertProductionQueryForceResult(t, result, cause)
		fixture.waitReleased(t)
		ownerAfter, err := fixture.manager.InspectRuntimeInstance(fixture.ownerRuntime.Identity)
		if err != nil || !ownerAfter.Active || ownerAfter.Admission.Forced ||
			!fixture.manager.RuntimeInstanceAvailable(ownerAfter.Identity) {
			t.Fatalf("owner changed by filter ForceDrain=%#v err=%v", ownerAfter, err)
		}
		filterAfter, err := fixture.manager.InspectRuntimeInstance(fixture.filterRuntime.Identity)
		if err != nil || !filterAfter.Active || !filterAfter.Admission.Forced {
			t.Fatalf("filter after ForceDrain=%#v err=%v", filterAfter, err)
		}
	})
}

type productionQueryForceFixture struct {
	manager       *extensionsruntime.Manager
	starter       *extensionsruntime.ProtocolStarter
	runtime       *queryregistry.ExecutionRuntime
	ownerRuntime  extensionsruntime.RuntimeInstanceSnapshot
	filterRuntime extensionsruntime.RuntimeInstanceSnapshot
	ownerReady    string
	filterReady   string
}

func newProductionQueryForceFixture(
	t *testing.T,
	owner extensions.Extension,
	filter extensions.Extension,
) *productionQueryForceFixture {
	t.Helper()
	ownerReady := filepath.Join(t.TempDir(), "owner.ready")
	filterReady := filepath.Join(t.TempDir(), "filter.ready")
	settings := productionQueryForceSettings{
		owner.ID:  ownerReady,
		filter.ID: filterReady,
	}
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: productionQueryForceTrust{}, Settings: settings,
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), owner); err != nil {
		t.Fatalf("start Query owner: %v", err)
	}
	if err := manager.Start(t.Context(), filter); err != nil {
		_ = manager.Stop(context.Background(), owner)
		t.Fatalf("start Query filter: %v", err)
	}
	ownerRuntime, err := manager.ActiveRuntimeInstance(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	filterRuntime, err := manager.ActiveRuntimeInstance(filter.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, identity := range []extensionsruntime.RuntimeInstanceIdentity{
			filterRuntime.Identity, ownerRuntime.Identity,
		} {
			if err := manager.StopRuntimeInstance(ctx, identity); err != nil {
				_ = starter.StopInstance(ctx, identity)
			}
		}
	})

	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(
		func(input queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
			return queryregistry.QueryCost{Units: 10 + len(input.Fields) + input.Pagination.Limit, Maximum: 2_000}, nil
		},
	)))
	// 安装生产 lifecycle admission 回调，而不是测试自己的 artifact predicate。
	_ = extensionsruntime.NewPostgresLifecycleBoundaryRegistries(
		extensionsruntime.LifecycleRegistryBoundaryConfig{Manager: manager, Queries: registry},
	)
	for _, item := range []struct {
		extension extensions.Extension
		runtime   extensionsruntime.RuntimeInstanceSnapshot
	}{{owner, ownerRuntime}, {filter, filterRuntime}} {
		publication, err := extensionsruntime.BuildLifecycleQueryPublication(item.extension, extensions.LifecycleRuntimeBinding{
			ExtensionID: item.extension.ID, ExtensionVersion: item.extension.Version,
			PackageDigest: item.extension.PackageDigest, VersionID: item.extension.ActiveVersionID,
			RuntimeInstanceID: item.runtime.Identity.InstanceID,
		})
		if err != nil || publication == nil {
			t.Fatalf("build %s Query publication=%#v err=%v", item.extension.ID, publication, err)
		}
		if _, err := registry.Publish(*publication); err != nil {
			t.Fatalf("publish %s Query surface: %v", item.extension.ID, err)
		}
	}
	coreProviders, err := queryregistry.NewStaticProviderResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := extensionsruntime.NewCompositeQueryProviderResolver(coreProviders, manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	filterSource, err := extensionsruntime.NewProtocolV2QueryResultFilterSource(manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := queryregistry.NewCompositeResultSchemaValidator(
		registry,
		queryregistry.ResultSchemaValidatorFunc(func(
			context.Context,
			queryregistry.ResultSchemaClaim,
			queryregistry.QueryRow,
		) error {
			return queryregistry.ErrResultInvalid
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Schemas: schemas,
		ResultFilterSource: filterSource, Admission: newProductionQueryRuntimeAdmission(manager),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &productionQueryForceFixture{
		manager: manager, starter: starter, runtime: execution,
		ownerRuntime: ownerRuntime, filterRuntime: filterRuntime,
		ownerReady: ownerReady, filterReady: filterReady,
	}
}

func (f *productionQueryForceFixture) executeBlocked(
	t *testing.T,
	state string,
	readyPath string,
) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := f.runtime.Execute(t.Context(), queryregistry.PlanRequest{
			QueryID:    productionQueryForceQueryID,
			Fields:     []string{"id", "title", "score"},
			Filters:    []queryregistry.FilterValue{{Field: "state", Value: state}},
			Pagination: queryregistry.PaginationRequest{Limit: 1},
		})
		result <- err
	}()
	waitProductionQueryForceReady(t, readyPath, result)
	owner, ownerErr := f.manager.InspectRuntimeInstance(f.ownerRuntime.Identity)
	filter, filterErr := f.manager.InspectRuntimeInstance(f.filterRuntime.Identity)
	if ownerErr != nil || filterErr != nil || owner.Admission.ActiveTotal != 1 || filter.Admission.ActiveTotal != 1 {
		t.Fatalf("in-flight leases owner=%#v/%v filter=%#v/%v", owner, ownerErr, filter, filterErr)
	}
	return result
}

func (f *productionQueryForceFixture) waitReleased(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for _, identity := range []extensionsruntime.RuntimeInstanceIdentity{
		f.ownerRuntime.Identity, f.filterRuntime.Identity,
	} {
		if err := f.manager.WaitDrain(ctx, identity); err != nil {
			t.Fatalf("wait exact Query lease release for %#v: %v", identity, err)
		}
	}
}

func assertProductionQueryForceResult(t *testing.T, result <-chan error, cause error) {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, queryregistry.ErrArtifactUnavailable) ||
			!errors.Is(err, extensionsruntime.ErrRuntimeAdmissionForced) || !errors.Is(err, cause) {
			t.Fatalf("production Query ForceDrain error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("production Query execution did not stop after ForceDrain")
	}
}

func waitProductionQueryForceReady(t *testing.T, path string, result <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect Query ready marker: %v", err)
		}
		select {
		case err := <-result:
			t.Fatalf("Query execution stopped before ready marker: %v", err)
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for Query ready marker %s", path)
		}
	}
}

type productionQueryForceSettings map[string]string

func (s productionQueryForceSettings) ListSettings(_ context.Context, extensionID string) (map[string]string, error) {
	path := strings.TrimSpace(s[extensionID])
	if path == "" {
		return nil, errors.New("missing production Query ready marker")
	}
	return map[string]string{"ready_file": path}, nil
}

type productionQueryForceTrust struct{}

func (productionQueryForceTrust) RuntimeIdentity(
	_ context.Context,
	extension extensions.Extension,
) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{
		TrustGrantID: "query-force:" + extension.ID, ImpactDigest: extension.PackageDigest,
	}, nil
}

func buildProductionQueryForcePackages(t *testing.T) (extensions.Extension, extensions.Extension) {
	t.Helper()
	owner := buildProductionQueryForceOwner(t)
	filterRoot := filepath.Join(t.TempDir(), productionQueryForceFilterID)
	if err := os.CopyFS(filterRoot, os.DirFS(owner.PackagePath)); err != nil {
		t.Fatalf("copy cross-plugin Query filter package: %v", err)
	}
	manifest := owner.Manifest
	manifest.ID = productionQueryForceFilterID
	manifest.Name = "SForum Query Filter Reference"
	manifest.Description = "Protocol V2 cross-plugin Query result-filter fixture."
	manifest.Queries = nil
	manifest.Dependencies = []extensions.ManifestDependency{{
		ID: productionQueryForceOwnerID, Version: "^1.0.0", Kind: "required",
	}}
	manifest.QueryResultFilters = []extensions.ManifestQueryResultFilter{{
		ID:                   productionQueryForceFilterID + ".items.mask",
		ContractVersion:      productionQueryForceFilterID + ".items.mask@1",
		QueryID:              productionQueryForceQueryID,
		QueryContractVersion: productionQueryForceQueryID + "@1",
		QueryPlanVersion:     productionQueryForceQueryID + ".plan@1",
		Handler:              productionQueryForceFilterID + ".items.mask",
		Priority:             100, FailurePolicy: "fail_open", TimeoutMS: 5_000,
		Dependency: &extensions.ManifestQueryResultFilterDependency{
			ExtensionID: productionQueryForceOwnerID, VersionConstraint: "^1.0.0",
		},
	}}
	manifest.PackageFiles = []extensions.ManifestPackageFile{{
		ID: productionQueryForceFilterID + ".file.backend", Kind: "executable",
		Path: manifest.Backend.Entry, Digest: manifest.Backend.Digest,
	}}
	if err := extensionmanifest.Validate(manifest); err != nil {
		t.Fatalf("validate cross-plugin Query filter manifest: %v", err)
	}
	writeProductionQueryForceManifest(t, filterRoot, manifest)
	digest, err := extensionpackage.DigestTree(filterRoot)
	if err != nil {
		t.Fatal(err)
	}
	filter := extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: filterRoot, PackageDigest: digest, Manifest: manifest, ActiveVersionID: 702,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
	return owner, filter
}

func buildProductionQueryForceOwner(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := productionQueryForceFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), productionQueryForceOwnerID)
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy Query owner reference package: %v", err)
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
		t.Fatalf("build Query owner reference plugin: %v\n%s", err, output)
	}
	schemaPath := filepath.Join(packageRoot, "schemas", "items.json")
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := strings.ReplaceAll(string(templateBody), "__BACKEND_DIGEST__", productionQueryForceFileSHA256(t, binaryPath))
	manifestBody = strings.ReplaceAll(manifestBody, "__SCHEMA_DIGEST__", productionQueryForceFileSHA256(t, schemaPath))
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load Query owner reference package: %v", err)
	}
	digest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: digest, Manifest: manifest, ActiveVersionID: 701,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}

func writeProductionQueryForceManifest(t *testing.T, root string, manifest extensions.Manifest) {
	t.Helper()
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, extensionmanifest.ManifestFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func productionQueryForceFileSHA256(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func productionQueryForceFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(
		filepath.Dir(file), "../../../extensions/fixtures/plugins/sforum-query-reference",
	))
}
