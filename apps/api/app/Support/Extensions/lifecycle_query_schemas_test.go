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
	"syscall"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	"github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleExecutableQueryBindsExactResultSchema(t *testing.T) {
	extension := lifecycleExecutableQuerySchemaExtension(t, "registry.demo.items.result@1",
		`{"type":"object","required":["ID","title"],"properties":{"ID":{"type":"integer"},"title":{"type":"string"}},"additionalProperties":false}`,
	)
	publication, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-schema-runtime"))
	if err != nil || publication == nil {
		t.Fatalf("build executable query publication: %#v %v", publication, err)
	}
	query := publication.Queries[0]
	if query.ResultSchemaDigest == "" || query.ResultSchemaDigest != extension.Manifest.PackageFiles[len(extension.Manifest.PackageFiles)-1].Digest {
		t.Fatalf("bound result Schema metadata = %#v", query)
	}
	registry := queryregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
	}
	claim := queryregistry.ResultSchemaClaim{
		QueryID: query.ID, ContractVersion: query.ContractVersion, PlanVersion: query.PlanVersion,
		ResultSchema: query.ResultSchema, Artifact: publication.Artifact, Fields: []string{"ID", "title"},
	}
	if err := registry.ValidateQueryResult(context.Background(), claim, queryregistry.QueryRow{"ID": json.Number("9007199254740993"), "title": "exact"}); err != nil {
		t.Fatalf("valid exact Schema row: %v", err)
	}
	if err := registry.ValidateQueryResult(context.Background(), claim, queryregistry.QueryRow{"ID": "1", "title": "wrong"}); !errors.Is(err, queryregistry.ErrResultInvalid) {
		t.Fatalf("invalid exact Schema row: %v", err)
	}

	// 发布后不再读取包文件；磁盘漂移不能改写不可变 Registry revision。
	if err := os.WriteFile(filepath.Join(extension.PackagePath, "schemas", "query-items.json"), []byte(`{"type":"null"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.ValidateQueryResult(context.Background(), claim, queryregistry.QueryRow{"ID": json.Number("1"), "title": "frozen"}); err != nil {
		t.Fatalf("published Schema changed after package mutation: %v", err)
	}
}

func TestLifecycleExecutableQueryAcceptsExactPathSchemaReference(t *testing.T) {
	extension := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json", `{"type":"object"}`)
	publication, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-path-schema-runtime"))
	if err != nil || publication == nil || publication.Queries[0].ResultSchemaDigest == "" {
		t.Fatalf("path result Schema publication = %#v, %v", publication, err)
	}
}

func TestLifecycleExecutableQueryAcceptsIDVersionSchemaReference(t *testing.T) {
	extension := lifecycleExecutableQuerySchemaExtension(t, "registry.demo.items.result@1", `{"type":"object"}`)
	// lifecycleExecutableQuerySchemaExtension 写入 ID 为 extension.ID+".items.result"、Version "1"。
	// 将 ResultSchema 改为 ID@version 形式后应仍能命中 exact packageFiles 条目。
	file := extension.Manifest.PackageFiles[len(extension.Manifest.PackageFiles)-1]
	extension.Manifest.Queries[0].ResultSchema = file.ID + "@" + file.Version
	publication, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-id-version-schema"))
	if err != nil || publication == nil || publication.Queries[0].ResultSchemaDigest != strings.ToLower(file.Digest) {
		t.Fatalf("ID@version Schema publication = %#v, %v", publication, err)
	}
}

func TestLifecycleHandlerlessQueryDoesNotRequirePackageSchema(t *testing.T) {
	extension := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("9", 64), 109)
	if extension.Manifest.Queries[0].Handler != "" {
		t.Fatal("fixture unexpectedly set a handler")
	}
	publication, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-handlerless"))
	if err != nil || publication == nil || publication.Queries[0].ResultSchemaDigest != "" {
		t.Fatalf("handlerless publication = %#v, %v", publication, err)
	}
	if publication.Queries[0].Handler != "" || len(publication.Queries[0].IdentityFields) != 0 ||
		len(publication.Queries[0].DefaultSort) != 0 || len(publication.ResultFilters) != 0 {
		t.Fatalf("handlerless gained executable metadata: %#v", publication)
	}
	registry := queryregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatalf("handlerless publish: %v", err)
	}
}

func TestLifecycleExecutableQueryPublishesHandlerIdentityDefaultSortAndResultFilters(t *testing.T) {
	extension := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json", `{"type":"object"}`)
	// executable fixture 默认无 relations 会阻塞第三方 plan，但 publication 仍应保留声明。
	query := extension.Manifest.Queries[0]
	extension.Manifest.QueryResultFilters = []extensions.ManifestQueryResultFilter{{
		ID: extension.ID + ".items.redact", ContractVersion: extension.ID + ".items.redact@1",
		QueryID: query.ID, QueryContractVersion: query.ContractVersion,
		QueryPlanVersion: query.PlanVersion, Handler: extension.ID + ".query.items.redact",
		Priority: 10, FailurePolicy: "fail_closed", TimeoutMS: 1000,
	}}
	publication, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-exec-meta"))
	if err != nil || publication == nil {
		t.Fatalf("executable metadata publication: %#v %v", publication, err)
	}
	got := publication.Queries[0]
	if got.Handler != query.Handler || len(got.IdentityFields) != 1 || got.IdentityFields[0] != "ID" ||
		len(got.DefaultSort) != 2 || got.DefaultSort[0].Field != "created_at" || !got.DefaultSort[0].Descending ||
		got.DefaultSort[1].Field != "ID" || got.ProviderDigest != "" {
		t.Fatalf("executable query metadata = %#v", got)
	}
	if len(publication.ResultFilters) != 1 ||
		publication.ResultFilters[0].ID != extension.ID+".items.redact" ||
		publication.ResultFilters[0].Handler != extension.ID+".query.items.redact" ||
		publication.ResultFilters[0].FilterDigest != "" ||
		len(publication.ResultFilters[0].IdentityFields) != 1 ||
		publication.ResultFilters[0].IdentityFields[0] != "ID" {
		t.Fatalf("result filter metadata = %#v", publication.ResultFilters)
	}
	// 无私有 callable 时仍可发布：inspect/plan 路径保持开放。
	registry := queryregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatalf("publish executable metadata without private material: %v", err)
	}
	resolved, err := registry.Resolve(got.ID)
	if err != nil || resolved.Handler != got.Handler || resolved.ProviderDigest != "" ||
		resolved.ResultSchemaDigest == "" {
		t.Fatalf("resolved executable metadata = %#v err=%v", resolved, err)
	}
}

func TestLifecycleExecutableQueryRejectsSchemaDigestReferenceAndSymlinkDrift(t *testing.T) {
	t.Run("digest", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "registry.demo.items.result@1", `{"type":"object"}`)
		if err := os.WriteFile(filepath.Join(extension.PackagePath, "schemas", "query-items.json"), []byte(`{"type":"array"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-digest-drift")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("digest drift = %v", err)
		}
	})

	t.Run("external ref", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "registry.demo.items.result@1", `{"$ref":"https://example.com/schema.json"}`)
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-external-ref")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("external Schema ref = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "registry.demo.items.result@1", `{"type":"object"}`)
		path := filepath.Join(extension.PackagePath, "schemas", "query-items.json")
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{"type":"object"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-symlink-drift")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("symlink drift = %v", err)
		}
	})

	t.Run("duplicate package entry", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json", `{"type":"object"}`)
		file := extension.Manifest.PackageFiles[len(extension.Manifest.PackageFiles)-1]
		// 同 path 的第二条 schema 条目使 exact 匹配歧义。
		extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
			ID: file.ID + ".dup", Kind: "schema", Path: file.Path, Digest: file.Digest, Version: "2",
		})
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-duplicate-schema")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("duplicate Schema entry = %v", err)
		}
	})

	t.Run("missing package entry", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "registry.demo.items.result@1", `{"type":"object"}`)
		extension.Manifest.Queries[0].ResultSchema = "missing.result@1"
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-missing-schema")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("missing Schema entry = %v", err)
		}
	})
}

func TestLifecycleExecutableQueryRejectsOversizedDirectoryAndFIFOSchema(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json", `{"type":"object"}`)
		path := filepath.Join(extension.PackagePath, "schemas", "query-items.json")
		// 超过 1 MiB 边界；digest 故意保持旧值以先触发 size 拒绝。
		if err := os.WriteFile(path, []byte(strings.Repeat("x", int(lifecyclePackageSchemaMaximumBytes)+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-oversize-schema")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("oversized Schema = %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json", `{"type":"object"}`)
		path := filepath.Join(extension.PackagePath, "schemas", "query-items.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-directory-schema")); !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
			t.Fatalf("directory Schema = %v", err)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		extension := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json", `{"type":"object"}`)
		path := filepath.Join(extension.PackagePath, "schemas", "query-items.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := buildLifecycleQueryPublication(extension, lifecycleRegistryBinding(extension, "query-fifo-schema"))
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrLifecycleRegistryPublicationInvalid) {
				t.Fatalf("FIFO Schema = %v", err)
			}
		case <-time.After(500 * time.Millisecond):
			writer, openErr := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if openErr == nil {
				_ = writer.Close()
			}
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
			}
			t.Fatal("FIFO Schema read blocked")
		}
	})
}

func TestLifecycleExecutableQueryRestoreRollbackDisableAndSafeMode(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	queries := queryregistry.New()
	core := lifecycleCoreQueryPublication(t)
	if _, err := queries.Publish(core); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Routes: routes.NewRegistry(), RouteSchemas: lifecycleRouteSchemaPublication(t),
		Queries: queries,
	})
	source := lifecycleExecutableQuerySchemaExtension(t, "schemas/query-items.json",
		`{"type":"object","required":["ID"],"properties":{"ID":{"type":"integer"}},"additionalProperties":false}`)
	source.ActiveVersionID = 110
	source.Version = "1.0.0"
	source.Manifest.Version = "1.0.0"
	// 重新计算 package digest，因 fixture 已写入 Schema 文件。
	var err error
	source.PackageDigest, err = extensionpackage.DigestTree(source.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	source.Manifest.Queries[0].ID = source.ID + ".items"
	source.Manifest.Queries[0].ContractVersion = source.ID + ".items@1"
	source.Manifest.Queries[0].Entity = source.ID + ".item"
	source.Manifest.Queries[0].PlanVersion = source.ID + ".items.plan@1"
	source.Manifest.Queries[0].ResultSchema = "schemas/query-items.json"
	source.Manifest.Queries[0].Handler = source.ID + ".query.items.execute"
	source.Manifest.PackageFiles[len(source.Manifest.PackageFiles)-1].ID = source.ID + ".items.result"
	if err := rewriteLifecycleQueryManifest(source); err != nil {
		t.Fatal(err)
	}
	source.PackageDigest, err = extensionpackage.DigestTree(source.PackagePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := manager.Start(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, false); err != nil {
		t.Fatalf("restore executable query Schema: %v", err)
	}
	runtime, err := manager.ActiveRuntimeInstance(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected := queryregistry.Artifact{
		ExtensionID: source.ID, ExtensionVersion: source.Version,
		PackageDigest: source.PackageDigest, VersionID: source.ActiveVersionID,
		RuntimeInstanceID: runtime.Identity.InstanceID,
	}
	assertLifecycleQueryArtifact(t, queries, expected)
	snapshot := queries.Snapshot()
	var bound queryregistry.QueryContribution
	for _, item := range snapshot.Queries {
		if item.ID == source.ID+".items" {
			bound = item
		}
	}
	if bound.ResultSchemaDigest == "" {
		t.Fatalf("restored executable Schema digest missing: %#v", snapshot.Queries)
	}
	claim := queryregistry.ResultSchemaClaim{
		QueryID: bound.ID, ContractVersion: bound.ContractVersion, PlanVersion: bound.PlanVersion,
		ResultSchema: bound.ResultSchema, Artifact: expected,
	}
	if err := queries.ValidateQueryResult(ctx, claim, queryregistry.QueryRow{"ID": json.Number("1")}); err != nil {
		t.Fatalf("restored Schema validation: %v", err)
	}

	// disable
	sourceMaterial := lifecycleQueryTestMaterial(t, source, runtime.Identity.InstanceID)
	if err := boundary.reconcileQueries(source.ID, &sourceMaterial, nil, nil); err != nil {
		t.Fatalf("disable executable query: %v", err)
	}
	if _, found := queries.SnapshotPublication(source.ID); found {
		t.Fatal("disabled executable query publication remains active")
	}
	if err := queries.ValidateQueryResult(ctx, claim, queryregistry.QueryRow{"ID": json.Number("1")}); !errors.Is(err, queryregistry.ErrResultInvalid) {
		t.Fatalf("disabled Schema remained callable: %v", err)
	}

	// re-publish then Safe Mode restore
	if _, err := queries.Publish(*sourceMaterial.queryPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.RestoreRoutePublications(ctx, []extensions.Extension{source}, true); err != nil {
		t.Fatalf("Safe Mode restore: %v", err)
	}
	safe := queries.Snapshot()
	if !safe.SafeMode || len(safe.Publications) != 1 || safe.Publications[0].Artifact != core.Artifact {
		t.Fatalf("Safe Mode query snapshot = %#v", safe)
	}
	if err := queries.ValidateQueryResult(ctx, claim, queryregistry.QueryRow{"ID": json.Number("1")}); !errors.Is(err, queryregistry.ErrResultInvalid) {
		t.Fatalf("Safe Mode left third-party Schema callable: %v", err)
	}
}

func lifecycleExecutableQuerySchemaExtension(t *testing.T, reference, schema string) extensions.Extension {
	t.Helper()
	extension := lifecycleQueryTestExtension(t, "1.0.0", strings.Repeat("8", 64), 108)
	query := &extension.Manifest.Queries[0]
	query.ResultSchema = reference
	query.Handler = extension.ID + ".query.items.execute"
	query.Sort = append(query.Sort, "ID")
	query.IdentityFields = []string{"ID"}
	query.DefaultSort = []extensions.ManifestQuerySort{{Field: "created_at", Descending: true}, {Field: "ID"}}
	path := "schemas/query-items.json"
	body := []byte(schema)
	if err := os.MkdirAll(filepath.Join(extension.PackagePath, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension.PackagePath, path), body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, extensions.ManifestPackageFile{
		ID: extension.ID + ".items.result", Kind: "schema", Path: path,
		Digest: hex.EncodeToString(digest[:]), Version: "1",
	})
	if err := rewriteLifecycleQueryManifest(extension); err != nil {
		t.Fatal(err)
	}
	var err error
	extension.PackageDigest, err = extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	return extension
}

func rewriteLifecycleQueryManifest(extension extensions.Extension) error {
	manifestBody, err := json.Marshal(extension.Manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(extension.PackagePath, extensionmanifest.ManifestFileName), manifestBody, 0o600)
}
