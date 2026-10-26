package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

const protocolV2DatabaseMaximumSQLFileSize = 64 << 10

type protocolV2DatabaseCatalog struct {
	queries  []hostapi.ProtocolV2DatabaseQueryDefinition
	executes []hostapi.ProtocolV2DatabaseExecuteDefinition
}

type protocolV2DatabaseCatalogBinder interface {
	Bind(protocolV2DatabaseCatalog) error
	Publish(protocolV2DatabaseCatalog) error
}

type protocolV2DatabaseCatalogBinderFactory func(*hostapi.Gateway) protocolV2DatabaseCatalogBinder

type postgresProtocolV2DatabaseCatalogBinder struct {
	pool    *pgxpool.Pool
	gateway *hostapi.Gateway
	options []hostapi.ProtocolV2DatabaseRuntimeOption
}

func (b postgresProtocolV2DatabaseCatalogBinder) Bind(catalog protocolV2DatabaseCatalog) error {
	runtime, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(b.pool, catalog.queries, catalog.executes, b.options...)
	if err != nil {
		return fmt.Errorf("construct DatabaseService runtime: %w", err)
	}
	if err := b.gateway.BindProtocolV2DatabaseRuntime(runtime); err != nil {
		return fmt.Errorf("bind DatabaseService runtime: %w", err)
	}
	return nil
}

func (b postgresProtocolV2DatabaseCatalogBinder) Publish(catalog protocolV2DatabaseCatalog) error {
	runtime, err := hostapi.NewPostgresProtocolV2DatabaseRuntime(b.pool, catalog.queries, catalog.executes, b.options...)
	if err != nil {
		return fmt.Errorf("construct DatabaseService runtime: %w", err)
	}
	if err := b.gateway.PublishProtocolV2DatabaseRuntime(runtime); err != nil {
		return fmt.Errorf("publish DatabaseService runtime: %w", err)
	}
	return nil
}

func bindProductionProtocolV2DatabaseRuntime(
	ctx context.Context,
	binder protocolV2DatabaseCatalogBinder,
	store extensions.Store,
	safeMode bool,
) error {
	if store == nil {
		return fmt.Errorf("database catalog extension store is required")
	}
	items, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list database catalog extensions: %w", err)
	}
	return bindProtocolV2DatabaseRuntime(binder, items, safeMode)
}

func postgresProtocolV2DatabaseCatalogBinderFactory(
	pool *pgxpool.Pool,
	options ...hostapi.ProtocolV2DatabaseRuntimeOption,
) protocolV2DatabaseCatalogBinderFactory {
	return func(gateway *hostapi.Gateway) protocolV2DatabaseCatalogBinder {
		return postgresProtocolV2DatabaseCatalogBinder{pool: pool, gateway: gateway, options: options}
	}
}

func bindProtocolV2DatabaseRuntime(
	binder protocolV2DatabaseCatalogBinder,
	items []extensions.Extension,
	safeMode bool,
) error {
	if binder == nil {
		return fmt.Errorf("DatabaseService catalog binder is required")
	}
	catalog, err := loadProtocolV2DatabaseCatalog(items, safeMode)
	if err != nil {
		return err
	}
	return binder.Bind(catalog)
}

func protocolV2DatabaseStartPreparer(
	store extensions.Store,
	binder protocolV2DatabaseCatalogBinder,
	safeMode bool,
) func(context.Context, extensions.Extension) error {
	return func(ctx context.Context, starting extensions.Extension) error {
		if safeMode {
			return fmt.Errorf("DatabaseService catalog publication is unavailable in Safe Mode")
		}
		items, err := store.List(ctx)
		if err != nil {
			return fmt.Errorf("list database catalog extensions before runtime start: %w", err)
		}
		// Rollback/promotion may pass an exact artifact before a concurrent reader
		// observes the new active pointer. Keep both immutable artifacts available.
		items = append(items, starting)
		catalog, err := loadProtocolV2DatabaseCatalog(items, false)
		if err != nil {
			return err
		}
		return binder.Publish(catalog)
	}
}

// loadProtocolV2DatabaseCatalog freezes active and staged package snapshots.
// Non-enabled artifacts remain inert because runtime identity and database
// authority are still checked transactionally on every DatabaseService call.
func loadProtocolV2DatabaseCatalog(items []extensions.Extension, safeMode bool) (protocolV2DatabaseCatalog, error) {
	catalog := protocolV2DatabaseCatalog{
		queries:  []hostapi.ProtocolV2DatabaseQueryDefinition{},
		executes: []hostapi.ProtocolV2DatabaseExecuteDefinition{},
	}
	if safeMode {
		return catalog, nil
	}

	artifacts := make([]extensions.Extension, 0, len(items)*2)
	for _, extension := range items {
		artifacts = append(artifacts, extension)
		if staged := extension.StagedVersion; staged != nil {
			stagedExtension := extension
			stagedExtension.Version = staged.Version
			stagedExtension.Manifest = staged.Manifest
			stagedExtension.PackageDigest = staged.PackageDigest
			stagedExtension.PackagePath = staged.PackagePath
			stagedExtension.StagedVersion = nil
			artifacts = append(artifacts, stagedExtension)
		}
	}
	ordered := artifacts
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		if ordered[i].Version != ordered[j].Version {
			return ordered[i].Version < ordered[j].Version
		}
		return ordered[i].PackageDigest < ordered[j].PackageDigest
	})

	type artifactKey struct{ id, version, digest string }
	seen := make(map[artifactKey]extensions.Extension, len(ordered))
	for _, extension := range ordered {
		if extension.Type != extensions.TypePlugin || extension.Manifest.Database == nil ||
			len(extension.Manifest.Database.Operations) == 0 {
			continue
		}
		key := artifactKey{id: extension.ID, version: extension.Version, digest: extension.PackageDigest}
		if previous, exists := seen[key]; exists {
			if !reflect.DeepEqual(extensionmanifest.Normalize(previous.Manifest), extensionmanifest.Normalize(extension.Manifest)) ||
				filepath.Clean(previous.PackagePath) != filepath.Clean(extension.PackagePath) {
				return protocolV2DatabaseCatalog{}, fmt.Errorf("database catalog exact artifact %s@%s has conflicting snapshots", extension.ID, extension.Version)
			}
			continue
		}
		seen[key] = extension
		if err := appendProtocolV2DatabaseArtifact(&catalog, extension); err != nil {
			return protocolV2DatabaseCatalog{}, err
		}
	}
	return catalog, nil
}

func appendProtocolV2DatabaseArtifact(catalog *protocolV2DatabaseCatalog, extension extensions.Extension) error {
	manifest := extensionmanifest.Normalize(extension.Manifest)
	if err := extensionmanifest.Validate(manifest); err != nil {
		return fmt.Errorf("database catalog %s manifest is invalid: %w", extension.ID, err)
	}
	if extensionmanifest.EffectiveManifestVersion(manifest) != extensionmanifest.ManifestVersionV3 ||
		manifest.ID != extension.ID || manifest.Version != extension.Version || manifest.Type != extension.Type {
		return fmt.Errorf("database catalog %s exact manifest identity is invalid", extension.ID)
	}
	if !extensionmanifest.HasDatabaseGrant(manifest.Database, extensionmanifest.DatabaseGrantOwnSchema) {
		return fmt.Errorf("database catalog %s grants must include own_schema", extension.ID)
	}

	packageFiles := make(map[string]extensionmanifest.ManifestPackageFile, len(manifest.PackageFiles))
	for _, file := range manifest.PackageFiles {
		packageFiles[file.Path] = file
	}
	for _, operation := range manifest.Database.Operations {
		file, declared := packageFiles[operation.Path]
		if !declared || file.Kind != "database_operation" || file.Digest != operation.Digest {
			return fmt.Errorf("database catalog %s operation %s has no exact package file", extension.ID, operation.ID)
		}
		sql, err := readExactDatabaseOperation(extension, operation.Path, operation.Digest)
		if err != nil {
			return fmt.Errorf("database catalog %s operation %s: %w", extension.ID, operation.ID, err)
		}
		parameters, err := databaseOperationParameters(operation.Parameters)
		if err != nil {
			return fmt.Errorf("database catalog %s operation %s: %w", extension.ID, operation.ID, err)
		}
		columns := databaseOperationColumns(operation.Columns)
		resultSchemaID, resultSchemaVersion, err := splitDatabaseSchemaRef(operation.ResultSchema)
		if err != nil {
			return fmt.Errorf("database catalog %s operation %s: %w", extension.ID, operation.ID, err)
		}
		timeout := time.Duration(operation.TimeoutMS) * time.Millisecond
		switch operation.Kind {
		case "query":
			catalog.queries = append(catalog.queries, hostapi.ProtocolV2DatabaseQueryDefinition{
				ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
				OperationID: operation.ID, StatementVersion: operation.StatementVersion,
				Scope: hostapi.ProtocolV2DatabaseOwnSchema, SQL: sql, Parameters: parameters,
				ResultSchemaID: resultSchemaID, ResultSchemaVersion: resultSchemaVersion,
				Columns: columns, MaxRows: operation.MaxRows, Timeout: timeout,
			})
		case "execute":
			catalog.executes = append(catalog.executes, hostapi.ProtocolV2DatabaseExecuteDefinition{
				ExtensionID: extension.ID, ExtensionVersion: extension.Version, PackageDigest: extension.PackageDigest,
				OperationID: operation.ID, StatementVersion: operation.StatementVersion,
				SQL: sql, Parameters: parameters,
				ResultSchemaID: resultSchemaID, ResultSchemaVersion: resultSchemaVersion,
				ReturningColumns: columns, MaxAffectedRows: operation.MaxAffectedRows,
				QueryInvalidationTags: append([]string(nil), operation.QueryInvalidationTags...), Timeout: timeout,
			})
		default:
			return fmt.Errorf("database catalog %s operation %s kind is invalid", extension.ID, operation.ID)
		}
	}
	return nil
}

func readExactDatabaseOperation(extension extensions.Extension, manifestPath, expectedDigest string) (string, error) {
	target, ok := extensions.InstalledFilePathForRuntime(extension, manifestPath)
	if !ok {
		return "", fmt.Errorf("package path is invalid")
	}
	root, err := filepath.EvalSymlinks(extensions.PackageContentRoot(extension))
	if err != nil {
		return "", fmt.Errorf("resolve package root: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("resolve package file: %w", err)
	}
	if resolvedTarget == root || !strings.HasPrefix(resolvedTarget, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("package file escapes its content root")
	}

	file, err := os.Open(resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("open package file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat package file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > protocolV2DatabaseMaximumSQLFileSize {
		return "", fmt.Errorf("package file must be a regular SQL file no larger than %d bytes", protocolV2DatabaseMaximumSQLFileSize)
	}
	body, err := io.ReadAll(io.LimitReader(file, protocolV2DatabaseMaximumSQLFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read package file: %w", err)
	}
	if len(body) > protocolV2DatabaseMaximumSQLFileSize {
		return "", fmt.Errorf("package file exceeds %d bytes", protocolV2DatabaseMaximumSQLFileSize)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return "", fmt.Errorf("package file digest does not match the exact manifest")
	}
	return strings.TrimSpace(string(body)), nil
}

func databaseOperationParameters(values []extensionmanifest.ManifestDatabaseParameter) ([]hostapi.ProtocolV2DatabaseParameter, error) {
	result := make([]hostapi.ProtocolV2DatabaseParameter, 0, len(values))
	for _, value := range values {
		schemaID, schemaVersion, err := splitDatabaseSchemaRef(value.Schema)
		if err != nil {
			return nil, err
		}
		result = append(result, hostapi.ProtocolV2DatabaseParameter{
			SchemaID: schemaID, SchemaVersion: schemaVersion, Field: value.Field,
			Kind: value.Kind, Nullable: value.Nullable, MaxBytes: value.MaxBytes,
		})
	}
	return result, nil
}

func databaseOperationColumns(values []extensionmanifest.ManifestDatabaseColumn) []hostapi.ProtocolV2DatabaseColumn {
	result := make([]hostapi.ProtocolV2DatabaseColumn, 0, len(values))
	for _, value := range values {
		result = append(result, hostapi.ProtocolV2DatabaseColumn{Name: value.Name, Nullable: value.Nullable})
	}
	return result
}

func splitDatabaseSchemaRef(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	index := strings.LastIndex(value, "@")
	if index <= 0 || index == len(value)-1 {
		return "", "", fmt.Errorf("schema reference %q must use id@version", value)
	}
	version := value[index+1:]
	if version[0] == '0' || strings.Trim(version, "0123456789") != "" {
		return "", "", fmt.Errorf("schema reference %q must use a positive integer version", value)
	}
	return value[:index], version, nil
}
