package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

func TestLoadProtocolV2DatabaseCatalogMapsExactPackageStatements(t *testing.T) {
	extension := databaseCatalogFixture(t)
	catalog, err := loadProtocolV2DatabaseCatalog([]extensions.Extension{extension}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.queries) != 1 || len(catalog.executes) != 1 {
		t.Fatalf("catalog queries=%d executes=%d", len(catalog.queries), len(catalog.executes))
	}
	query := catalog.queries[0]
	if query.ExtensionID != extension.ID || query.ExtensionVersion != extension.Version ||
		query.PackageDigest != extension.PackageDigest || query.OperationID != "demo.catalog.database.items.query" ||
		query.StatementVersion != "2" || query.Scope != hostapi.ProtocolV2DatabaseOwnSchema ||
		query.SQL != "SELECT id, name FROM items WHERE id = $1" || query.MaxRows != 50 || query.Timeout != 1500*time.Millisecond {
		t.Fatalf("query definition = %#v", query)
	}
	if len(query.Parameters) != 1 || query.Parameters[0].SchemaID != "demo.catalog.database.item-id" ||
		query.Parameters[0].SchemaVersion != "1" || query.Parameters[0].Field != "id" ||
		query.Parameters[0].Kind != hostapi.ProtocolV2DatabaseInt64 || query.Parameters[0].MaxBytes != 8 {
		t.Fatalf("query parameters = %#v", query.Parameters)
	}
	if query.ResultSchemaID != "demo.catalog.database.item" || query.ResultSchemaVersion != "3" ||
		len(query.Columns) != 2 || !query.Columns[1].Nullable {
		t.Fatalf("query result = %#v", query)
	}
	execute := catalog.executes[0]
	if execute.SQL != "INSERT INTO items (name) VALUES ($1) RETURNING id, name" ||
		execute.ResultSchemaID != "demo.catalog.database.item" || execute.ResultSchemaVersion != "3" ||
		execute.MaxAffectedRows != 1 || len(execute.ReturningColumns) != 2 ||
		!slices.Equal(execute.QueryInvalidationTags, []string{"demo.catalog.items"}) {
		t.Fatalf("execute definition = %#v", execute)
	}
	binder := &recordingDatabaseCatalogBinder{}
	if err := bindProtocolV2DatabaseRuntime(binder, []extensions.Extension{extension}, false); err != nil {
		t.Fatalf("bind exact catalog: %v", err)
	}
	if len(binder.bound.queries) != 1 || len(binder.bound.executes) != 1 {
		t.Fatalf("bound catalog = %#v", binder.bound)
	}
}

func TestLoadProtocolV2DatabaseCatalogFailsClosedOnTampering(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *extensions.Extension)
	}{
		{name: "changed SQL bytes", change: func(t *testing.T, extension *extensions.Extension) {
			t.Helper()
			path, _ := extensions.InstalledFilePathForRuntime(*extension, extension.Manifest.Database.Operations[0].Path)
			if err := os.WriteFile(path, []byte("SELECT id, name FROM items WHERE id = $2\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing SQL file", change: func(t *testing.T, extension *extensions.Extension) {
			t.Helper()
			path, _ := extensions.InstalledFilePathForRuntime(*extension, extension.Manifest.Database.Operations[0].Path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "wrong package kind", change: func(_ *testing.T, extension *extensions.Extension) {
			extension.Manifest.PackageFiles[0].Kind = "asset"
		}},
		{name: "stale manifest version", change: func(_ *testing.T, extension *extensions.Extension) {
			extension.Manifest.Version = "2.0.0"
		}},
		{name: "invalid schema reference", change: func(_ *testing.T, extension *extensions.Extension) {
			extension.Manifest.Database.Operations[0].ResultSchema = "demo.catalog.database.item@0"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := databaseCatalogFixture(t)
			test.change(t, &extension)
			if _, err := loadProtocolV2DatabaseCatalog([]extensions.Extension{extension}, false); err == nil {
				t.Fatal("tampered database catalog must fail closed")
			}
		})
	}
}

func TestBindProtocolV2DatabaseRuntimeRejectsInvalidSQL(t *testing.T) {
	extension := databaseCatalogFixture(t)
	operation := &extension.Manifest.Database.Operations[0]
	invalidSQL := []byte("SELECT id FROM items; DROP TABLE items\n")
	digest := sha256.Sum256(invalidSQL)
	operation.Digest = hex.EncodeToString(digest[:])
	extension.Manifest.PackageFiles[0].Digest = operation.Digest
	path, _ := extensions.InstalledFilePathForRuntime(extension, operation.Path)
	if err := os.WriteFile(path, invalidSQL, 0o600); err != nil {
		t.Fatal(err)
	}
	binder := validatingDatabaseCatalogBinder{}
	if err := bindProtocolV2DatabaseRuntime(binder, []extensions.Extension{extension}, false); err == nil {
		t.Fatal("invalid SQL must prevent DatabaseService binding")
	}
}

func TestProtocolV2DatabaseCatalogEmptyInstalledAndSafeModeAreNoOps(t *testing.T) {
	for _, test := range []struct {
		name     string
		items    []extensions.Extension
		safeMode bool
	}{
		{name: "fresh boot"},
		{name: "installed plugin", items: func() []extensions.Extension {
			extension := databaseCatalogFixture(t)
			extension.Status = extensions.StatusInstalled
			return []extensions.Extension{extension}
		}()},
		{name: "safe mode", items: func() []extensions.Extension {
			extension := databaseCatalogFixture(t)
			extension.Manifest.Database.Operations[0].Digest = strings.Repeat("f", 64)
			return []extensions.Extension{extension}
		}(), safeMode: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, err := loadProtocolV2DatabaseCatalog(test.items, test.safeMode)
			if err != nil {
				t.Fatal(err)
			}
			wantOperations := 0
			if test.name == "installed plugin" {
				wantOperations = 2
			}
			if len(catalog.queries)+len(catalog.executes) != wantOperations {
				t.Fatalf("catalog = %#v", catalog)
			}
			if err := bindProtocolV2DatabaseRuntime(&recordingDatabaseCatalogBinder{}, test.items, test.safeMode); err != nil {
				t.Fatalf("bind explicit empty catalog: %v", err)
			}
		})
	}
}

func TestProtocolV2DatabaseStartPreparerPublishesNewExactArtifact(t *testing.T) {
	active := databaseCatalogFixture(t)
	active.Status = extensions.StatusDisabled
	store := &bootstrapExtensionSettingsStore{item: active}
	binder := &recordingDatabaseCatalogBinder{}
	prepare := protocolV2DatabaseStartPreparer(store, binder, false)
	if err := prepare(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	if binder.publishCalls != 1 || len(binder.published.queries) != 1 || len(binder.published.executes) != 1 {
		t.Fatalf("duplicate active artifact was not deduplicated: %#v", binder.published)
	}

	upgraded := databaseCatalogFixture(t)
	upgraded.ID = active.ID
	upgraded.Manifest.ID = active.ID
	upgraded.Version = "2.0.0"
	upgraded.Manifest.Version = upgraded.Version
	upgraded.PackageDigest = strings.Repeat("b", 64)
	if err := prepare(t.Context(), upgraded); err != nil {
		t.Fatal(err)
	}
	if binder.publishCalls != 2 || len(binder.published.queries) != 2 || len(binder.published.executes) != 2 {
		t.Fatalf("published catalog = %#v calls=%d", binder.published, binder.publishCalls)
	}
	seen := map[string]bool{}
	for _, query := range binder.published.queries {
		seen[query.ExtensionVersion+"@"+query.PackageDigest] = true
	}
	if !seen[active.Version+"@"+active.PackageDigest] || !seen[upgraded.Version+"@"+upgraded.PackageDigest] {
		t.Fatalf("published exact artifacts = %#v", seen)
	}
}

func TestLoadProtocolV2DatabaseCatalogRejectsConflictingExactArtifactSnapshots(t *testing.T) {
	first := databaseCatalogFixture(t)
	conflict := first
	conflict.Manifest.Name = "Conflicting manifest"
	if _, err := loadProtocolV2DatabaseCatalog([]extensions.Extension{first, conflict}, false); err == nil {
		t.Fatal("same exact artifact identity with different manifest must fail closed")
	}
}

type recordingDatabaseCatalogBinder struct {
	bound        protocolV2DatabaseCatalog
	published    protocolV2DatabaseCatalog
	bindCalls    int
	publishCalls int
}

func (b *recordingDatabaseCatalogBinder) Bind(catalog protocolV2DatabaseCatalog) error {
	b.bindCalls++
	b.bound = catalog
	return nil
}

func (b *recordingDatabaseCatalogBinder) Publish(catalog protocolV2DatabaseCatalog) error {
	b.publishCalls++
	b.published = catalog
	return nil
}

type validatingDatabaseCatalogBinder struct{}

func (validatingDatabaseCatalogBinder) Bind(catalog protocolV2DatabaseCatalog) error {
	_, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(new(pgxpool.Pool), catalog.queries, catalog.executes)
	return err
}

func (validatingDatabaseCatalogBinder) Publish(catalog protocolV2DatabaseCatalog) error {
	return validatingDatabaseCatalogBinder{}.Bind(catalog)
}

func databaseCatalogFixture(t *testing.T) extensions.Extension {
	t.Helper()
	root := t.TempDir()
	query := []byte("SELECT id, name FROM items WHERE id = $1\n")
	execute := []byte("INSERT INTO items (name) VALUES ($1) RETURNING id, name\n")
	queryDigest := sha256.Sum256(query)
	executeDigest := sha256.Sum256(execute)
	queryHex := hex.EncodeToString(queryDigest[:])
	executeHex := hex.EncodeToString(executeDigest[:])
	for name, body := range map[string][]byte{
		"database/items-query.sql":  query,
		"database/items-insert.sql": execute,
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := extensionmanifest.Manifest{
		ManifestVersion: extensionmanifest.ManifestVersionV3,
		ID:              "demo.catalog", Name: "Database Catalog", Description: "Database catalog fixture.",
		URL: "https://example.com/database-catalog", Author: extensionmanifest.ManifestAuthor{Name: "SForum"},
		Version: "1.0.0", Type: extensionmanifest.TypePlugin, SForumVersion: ">=0.1.0",
		Database: &extensionmanifest.ManifestDatabase{
			ContractVersion: "demo.catalog.database@1",
			Grants:          []string{extensionmanifest.DatabaseGrantOwnSchema, extensionmanifest.DatabaseGrantHostCommands},
			Schema:          "demo_catalog", Role: "demo_catalog",
			Retention: extensionmanifest.ManifestRetention{OnDisable: "retain", OnUninstall: "retain"},
			Operations: []extensionmanifest.ManifestDatabaseOperation{
				{
					ID: "demo.catalog.database.items.query", StatementVersion: "2", Kind: "query",
					Path: "database/items-query.sql", Digest: queryHex,
					Parameters:   []extensionmanifest.ManifestDatabaseParameter{{Schema: "demo.catalog.database.item-id@1", Field: "id", Kind: "int64", MaxBytes: 8}},
					ResultSchema: "demo.catalog.database.item@3", Columns: []extensionmanifest.ManifestDatabaseColumn{{Name: "id"}, {Name: "name", Nullable: true}},
					MaxRows: 50, TimeoutMS: 1500,
				},
				{
					ID: "demo.catalog.database.items.insert", StatementVersion: "1", Kind: "execute",
					Path: "database/items-insert.sql", Digest: executeHex,
					Parameters:   []extensionmanifest.ManifestDatabaseParameter{{Schema: "demo.catalog.database.item-name@1", Field: "name", Kind: "string", MaxBytes: 1024}},
					ResultSchema: "demo.catalog.database.item@3", Columns: []extensionmanifest.ManifestDatabaseColumn{{Name: "id"}, {Name: "name"}},
					MaxAffectedRows: 1, QueryInvalidationTags: []string{"demo.catalog.items"}, TimeoutMS: 2000,
				},
			},
		},
		PackageFiles: []extensionmanifest.ManifestPackageFile{
			{ID: "demo.catalog.file.database.query", Kind: "database_operation", Path: "database/items-query.sql", Digest: queryHex},
			{ID: "demo.catalog.file.database.execute", Kind: "database_operation", Path: "database/items-insert.sql", Digest: executeHex},
		},
	}
	return extensions.Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded, Manifest: manifest,
		PackageDigest: strings.Repeat("a", 64), PackagePath: root,
	}
}
