package extensionsruntime_test

import (
	"context"
	"encoding/json"
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
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

// TestReferenceQueryPluginJoinedGates exercises the real Protocol V2 subprocess
// path for Host-owned Query Registry: Schema, filter, pagination, permission,
// cost, disable, and Safe Mode. P7 Query rows stay uncredited until the full
// product gate set is reviewed against production lifecycle/bootstrap wiring.
func TestReferenceQueryPluginJoinedGates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping reference Query plugin subprocess build in short mode")
	}
	extension := buildReferenceQueryExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{
			TrustGrantID: "query-reference", ImpactDigest: extension.PackageDigest,
		}},
	})
	manager := extensionsruntime.NewManager(extensionsruntime.ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("start reference Query plugin: %v", err)
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
	publication, err := extensionsruntime.BuildLifecycleQueryPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: active.Identity.InstanceID,
	})
	if err != nil || publication == nil {
		t.Fatalf("lifecycle query publication: %#v %v", publication, err)
	}

	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(
		func(input queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
			maximum := 2_000
			if input.RequestedMaximum > 0 {
				maximum = input.RequestedMaximum
			}
			units := 10 + len(input.Fields) + input.Pagination.Limit
			return queryregistry.QueryCost{Units: units, Maximum: maximum}, nil
		},
	)))
	registry.WithPluginAdmission(func(artifact queryregistry.Artifact) bool {
		return artifact == publication.Artifact
	})
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
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
			context.Context, queryregistry.ResultSchemaClaim, queryregistry.QueryRow,
		) error {
			return queryregistry.ErrResultInvalid
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Schemas: schemas, ResultFilterSource: filterSource,
		Admission: queryregistry.ContextualExecutionAdmissionFunc(func(ctx context.Context, _ queryregistry.Artifact) (queryregistry.ExecutionAdmissionLease, error) {
			return queryregistry.ExecutionAdmissionLease{Context: ctx, Release: func() {}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Pagination: queryregistry.PaginationRequest{Limit: 2},
	})
	// defaultSort=id desc；FetchLimit=limit+1 时插件返回 3 行，Host 只释放 2 行。
	if err != nil || len(result.Rows) != 2 || !result.Page.HasMore || result.Page.NextOffset != 2 ||
		result.Rows[0]["id"] != "5" || result.Rows[1]["id"] != "4" ||
		result.Rows[0]["title"] != "item-5 | masked" ||
		result.Rows[0]["score"] != json.Number("9007199254740993") {
		t.Fatalf("happy path result=%#v err=%v", result, err)
	}

	page, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Pagination: queryregistry.PaginationRequest{Offset: 2, Limit: 2},
	})
	if err != nil || len(page.Rows) != 2 || page.Rows[0]["id"] != "3" || page.Rows[1]["id"] != "2" ||
		page.Rows[0]["title"] != "item-3 | masked" || !page.Page.HasMore {
		t.Fatalf("pagination result=%#v err=%v", page, err)
	}

	tail, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Pagination: queryregistry.PaginationRequest{Offset: 4, Limit: 2},
	})
	if err != nil || len(tail.Rows) != 1 || tail.Rows[0]["id"] != "1" || tail.Page.HasMore ||
		tail.Rows[0]["title"] != "item-1 | masked" {
		t.Fatalf("tail pagination result=%#v err=%v", tail, err)
	}

	ascending, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID: "sforum.query-reference.items", Fields: []string{"id", "title", "score"},
		Sorts: []queryregistry.SortValue{{Field: "id"}}, Pagination: queryregistry.PaginationRequest{Limit: 2},
	})
	if err != nil || len(ascending.Rows) != 2 || ascending.Rows[0]["id"] != "1" || ascending.Rows[1]["id"] != "2" {
		t.Fatalf("caller sort result=%#v err=%v", ascending, err)
	}

	if _, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID: "sforum.query-reference.private",
		Fields:  []string{"id", "title", "score"},
	}); !errors.Is(err, queryregistry.ErrDenied) {
		t.Fatalf("login policy without auth = %v", err)
	}
	authed, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID: "sforum.query-reference.private",
		Fields:  []string{"id", "title", "score"},
		Permission: queryregistry.PermissionInput{
			Authenticated: true, ActorFingerprint: "actor:1", PolicyFingerprint: "session:v1",
			Recheck: queryregistry.PermissionRecheckFunc(func(context.Context, queryregistry.PermissionClaim) error {
				return nil
			}),
		},
	})
	if err != nil || len(authed.Rows) != 1 {
		t.Fatalf("login policy authenticated = %#v err=%v", authed, err)
	}

	if _, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Pagination: queryregistry.PaginationRequest{Limit: 10},
		MaxCost:    1,
	}); !errors.Is(err, queryregistry.ErrCostExceeded) {
		t.Fatalf("cost fence = %v", err)
	}

	if _, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Filters:    []queryregistry.FilterValue{{Field: "state", Value: "bad-schema"}},
		Pagination: queryregistry.PaginationRequest{Limit: 1},
	}); !errors.Is(err, queryregistry.ErrResultInvalid) {
		t.Fatalf("schema fence = %v", err)
	}

	if _, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Filters:    []queryregistry.FilterValue{{Field: "state", Value: "fail"}},
		Pagination: queryregistry.PaginationRequest{Limit: 1},
	}); !errors.Is(err, queryregistry.ErrProviderFailed) {
		t.Fatalf("provider failure = %v", err)
	}

	if err := manager.Stop(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	stopped = true
	if _, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID:    "sforum.query-reference.items",
		Fields:     []string{"id", "title", "score"},
		Pagination: queryregistry.PaginationRequest{Limit: 1},
	}); !errors.Is(err, queryregistry.ErrArtifactUnavailable) &&
		!errors.Is(err, queryregistry.ErrProviderUnavailable) &&
		!errors.Is(err, queryregistry.ErrProviderFailed) {
		t.Fatalf("disabled runtime = %v", err)
	}

	if _, err := registry.ReplaceAll(nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Execute(t.Context(), queryregistry.PlanRequest{
		QueryID: "sforum.query-reference.items",
		Fields:  []string{"id", "title", "score"},
	}); !errors.Is(err, queryregistry.ErrNotFound) &&
		!errors.Is(err, queryregistry.ErrArtifactUnavailable) {
		t.Fatalf("safe mode = %v", err)
	}
}

func buildReferenceQueryExtension(t *testing.T) extensions.Extension {
	t.Helper()
	fixtureRoot := referenceQueryFixtureRoot(t)
	packageRoot := filepath.Join(t.TempDir(), "sforum.query-reference")
	if err := os.CopyFS(packageRoot, os.DirFS(fixtureRoot)); err != nil {
		t.Fatalf("copy reference Query plugin: %v", err)
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
		t.Fatalf("build reference Query plugin: %v\n%s", err, output)
	}
	schemaPath := filepath.Join(packageRoot, "schemas", "items.json")
	templateBody, err := os.ReadFile(filepath.Join(packageRoot, "sforum.extension.json.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	manifestBody := strings.ReplaceAll(string(templateBody), "__BACKEND_DIGEST__", fileSHA256(t, binaryPath))
	manifestBody = strings.ReplaceAll(manifestBody, "__SCHEMA_DIGEST__", fileSHA256(t, schemaPath))
	if err := os.WriteFile(filepath.Join(packageRoot, extensionmanifest.ManifestFileName), []byte(manifestBody), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := extensionmanifest.LoadPackage(packageRoot)
	if err != nil {
		t.Fatalf("load exact Query reference package: %v", err)
	}
	packageDigest, err := extensionpackage.DigestTree(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackagePath: packageRoot, PackageDigest: packageDigest, Manifest: manifest, ActiveVersionID: 701,
		CapabilityGrants: []extensions.CapabilityGrant{{Key: capabilities.HostAPI, Risk: capabilities.RiskLow}},
	}
}

func referenceQueryFixtureRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../../extensions/fixtures/plugins/sforum-query-reference"))
}
