package extensionsruntime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	entityregistry "github.com/keeptrue/sforum/apps/api/app/Support/EntityRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLifecycleEntityMaterialFreezesEntityTaxonomyFieldAndDigestV11(t *testing.T) {
	extension := lifecycleEntityTestExtension(t, "1.0.0", strings.Repeat("a1", 32), 601)
	material := lifecycleRegistryMaterial{
		extension: extension,
		binding: extensions.LifecycleRuntimeBinding{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Entity: entityregistry.New()})
	if err := boundary.freezeEntityMaterial(&material); err != nil {
		t.Fatal(err)
	}
	publication := material.entityPublication
	if publication == nil || len(publication.Entities) != 3 {
		t.Fatalf("frozen entity publication = %#v", publication)
	}
	kinds := map[string]int{}
	for _, declaration := range publication.Entities {
		kinds[declaration.Kind]++
	}
	if kinds[entityregistry.KindEntity] != 1 || kinds[entityregistry.KindTaxonomy] != 1 ||
		kinds[entityregistry.KindField] != 1 {
		t.Fatalf("entity kinds = %#v", kinds)
	}
	v11, err := encodeLifecycleRegistryMaterialDigestV11(&material)
	if err != nil {
		t.Fatal(err)
	}
	if material.digest != v11 {
		t.Fatalf("entity primary digest = %s want %s", material.digest, v11)
	}
	// Mutating manifest after freeze must not rewrite frozen publication.
	extension.Manifest.Entities[0].Label = "mutated"
	if publication.Entities[0].Label == "mutated" {
		t.Fatal("manifest mutation rewrote frozen entity publication")
	}
}

func TestLifecycleEntityMaterialsAuthorityAndSafeMode(t *testing.T) {
	source := lifecycleEntityTestExtension(t, "1.0.0", strings.Repeat("b1", 32), 602)
	target := lifecycleEntityTestExtension(t, "2.0.0", strings.Repeat("c1", 32), 603)
	sourceMaterial := lifecycleEntityTestMaterial(t, source)
	targetMaterial := lifecycleEntityTestMaterial(t, target)
	authority := &staticAssetAuthority{
		restore:   map[string]string{source.ID: strings.Repeat("d1", 32)},
		operation: map[string]string{target.ID: strings.Repeat("e1", 32)},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Entity: entityregistry.New(), AssetAuthority: authority,
	})
	request := lifecycleRegistryRequest(source, target, sourceMaterial.binding, targetMaterial.binding, 1)
	if err := boundary.freezeEntityMaterials(context.Background(), request, &sourceMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if sourceMaterial.entityPublication == nil || targetMaterial.entityPublication == nil {
		t.Fatal("expected source and target entity publications")
	}
	if !reflect.DeepEqual(authority.calls, []string{"restore:" + source.ID, "operation:" + target.ID}) {
		t.Fatalf("authority calls = %#v", authority.calls)
	}

	registry := entityregistry.New()
	if _, err := registry.Publish(*sourceMaterial.entityPublication); err != nil {
		t.Fatal(err)
	}
	safeBoundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Entity: registry})
	if err := safeBoundary.restoreEntityPublications(context.Background(), []extensions.Extension{source}, true); err != nil {
		t.Fatal(err)
	}
	if err := safeBoundary.restoreEntityPublications(context.Background(), nil, true); err != nil {
		t.Fatal(err)
	}
	if len(registry.List(entityregistry.KindEntity)) != 0 {
		snapshot := registry.Snapshot()
		if !snapshot.SafeMode {
			t.Fatalf("expected safe mode or empty graph, got %#v", snapshot)
		}
	}
}

func TestLifecycleEntityUpgradeDisableAndCAS(t *testing.T) {
	ctx := context.Background()
	registry := entityregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Entity: registry})
	if boundary.EntityRegistry() != registry {
		t.Fatal("lifecycle boundary created a second Entity Registry")
	}
	source := lifecycleEntityTestExtension(t, "1.0.0", strings.Repeat("11", 32), 604)
	target := lifecycleEntityTestExtension(t, "2.0.0", strings.Repeat("22", 32), 605)
	sourceMaterial := lifecycleEntityTestMaterial(t, source)
	targetMaterial := lifecycleEntityTestMaterial(t, target)
	if _, err := registry.Publish(*sourceMaterial.entityPublication); err != nil {
		t.Fatal(err)
	}
	if err := boundary.reconcileEntity(ctx, source.ID, &sourceMaterial, &targetMaterial, &targetMaterial); err != nil {
		t.Fatal(err)
	}
	if len(registry.List(entityregistry.KindEntity)) != 1 {
		t.Fatalf("after upgrade entities = %#v", registry.List(entityregistry.KindEntity))
	}
	active, ok := registry.SnapshotPublication(source.ID)
	if !ok || active.Artifact.PackageDigest != target.PackageDigest {
		t.Fatalf("active publication = %#v ok=%v", active, ok)
	}
	// Disable desired=nil removes the extension publication without touching rows.
	if err := boundary.reconcileEntity(ctx, source.ID, &targetMaterial, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(registry.List("")) != 0 {
		t.Fatalf("after disable graph = %#v", registry.List(""))
	}
}

func lifecycleEntityTestMaterial(t *testing.T, extension extensions.Extension) lifecycleRegistryMaterial {
	t.Helper()
	material := lifecycleRegistryMaterial{
		extension: extension,
		binding: extensions.LifecycleRuntimeBinding{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		},
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Entity: entityregistry.New()})
	if err := boundary.freezeEntityMaterial(&material); err != nil {
		t.Fatalf("freeze entity material: %v", err)
	}
	return material
}

func lifecycleEntityTestExtension(t *testing.T, version, packageDigest string, versionID int64) extensions.Extension {
	t.Helper()
	id := "demo.catalog"
	return extensions.Extension{
		ID: id, Version: version, PackageDigest: packageDigest, ActiveVersionID: versionID,
		Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		Manifest: extensionmanifest.Manifest{
			ManifestVersion: extensionmanifest.ManifestVersionV3,
			ID:              id, Name: "Demo Catalog", Version: version, Type: extensions.TypePlugin,
			Entities: []extensionmanifest.ManifestEntity{
				{
					ID: id + ".entity.product", ContractVersion: id + ".entity.product@1",
					Kind: "entity", Label: "Product", StorageKey: id + ".product",
					PermissionCreate:   id + ".product.create",
					PermissionRead:     id + ".product.read",
					PermissionUpdate:   id + ".product.update",
					PermissionDelete:   id + ".product.delete",
					PermissionImport:   id + ".product.import",
					PermissionExport:   id + ".product.export",
					ImportExportPolicy: "allow",
					DeletionPolicy:     "soft",
					TaxonomyIDs:        []string{id + ".taxonomy.category"},
				},
				{
					ID: id + ".taxonomy.category", ContractVersion: id + ".taxonomy.category@1",
					Kind: "taxonomy", Label: "Category", StorageKey: id + ".category",
					Hierarchical:     true,
					EntityIDs:        []string{id + ".entity.product"},
					PermissionManage: id + ".category.manage",
					PermissionAssign: id + ".category.assign",
				},
				{
					ID: id + ".field.price", ContractVersion: id + ".field.price@1",
					Kind: "field", Label: "Price",
					EntityID:    id + ".entity.product",
					Schema:      id + ".price@1",
					UIComponent: "CurrencyInput",
					Required:    true, Indexed: true, IndexKind: "numeric",
					PermissionFieldRead:  id + ".field.price.read",
					PermissionFieldWrite: id + ".field.price.write",
					Order:                10,
				},
			},
		},
	}
}
