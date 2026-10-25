package extensions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	assetregistry "github.com/zhuchunshu/sforum/apps/api/app/Support/AssetRegistry"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
	platformversion "github.com/zhuchunshu/sforum/apps/api/version"
)

func TestPublicFrontendRequiresLiveExactArtifactTrust(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	store := &memoryExecutableTrustStore{}
	exactTrust := NewExecutableTrustService(reader, store)
	service := newAdmittedPublicFrontendService(reader, exactTrust)

	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("untrusted L2 component must be unavailable, got %v", err)
	}
	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	descriptor, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.SchemaVersion != PublicFrontendSchemaV1 || descriptor.APIVersion != PublicFrontendAPIVersion ||
		descriptor.TrustNotice != PublicFrontendTrustNotice || descriptor.PackageDigest != extension.PackageDigest ||
		descriptor.Entry.Handle != extension.Manifest.Components[0].ID+publicL2EntrySuffix || !descriptor.Entry.Module ||
		descriptor.Entry.ContractVersion != descriptor.Entry.Handle+"@1" ||
		len(descriptor.Assets) != 1 || descriptor.Assets[0].Handle != extension.Manifest.Assets[0].Handle {
		t.Fatalf("unexpected public descriptor: %#v", descriptor)
	}
	if descriptor.Entry.Integrity == "" || descriptor.Assets[0].Integrity == "" ||
		!strings.Contains(descriptor.Entry.AssetPath, "/_sforum/assets/extensions/"+extension.ID+"/"+extension.PackageDigest+"/frontend/public/card.mjs") {
		t.Fatalf("descriptor is not immutable/integrity-bound: %#v", descriptor)
	}
	// 公开描述符是匿名可读的：只带 release 级 coreVersion。
	if descriptor.CoreVersion != platformversion.CoreCompatibilityVersion() {
		t.Fatalf("descriptor coreVersion = %q", descriptor.CoreVersion)
	}
}

func TestPublicL2EntryAssetContractTracksComponentMajor(t *testing.T) {
	component := ManifestComponent{
		ID:              "demo.public.component.card",
		ContractVersion: "demo.public.component.card@2",
	}
	if got, want := publicL2EntryContractVersion(component), "demo.public.component.card.l2.entry@2"; got != want {
		t.Fatalf("entry asset contract=%q want=%q", got, want)
	}
}

// 公开 L2 恒开，但 exact-artifact trust 仍是独立门：关掉信任挑战后 L2 必须失败关闭。
func TestPublicFrontendStaysClosedWithoutTrustChallengeGate(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}).
		WithExecutableTrust(trust, false).
		WithPublicComponentAdmission(allowPublicComponentAdmission{})
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)

	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("public L2 must fail closed when the trust challenge gate is off, got %v", err)
	}
}

func TestPublicFrontendReplaceAllIsIndependentOfFirstRequestAndCatalogOrder(t *testing.T) {
	owner := publicFrontendFixtureFor(t, "owner.assets", nil)
	ownerHandle := owner.Manifest.Assets[0].Handle
	consumer := publicFrontendFixtureFor(t, "consumer.assets", []string{ownerHandle})
	consumer.Manifest.Dependencies = []ManifestDependency{{
		ID: owner.ID, Version: "^1.0.0", Kind: "required",
	}}
	reader := &fakeFrontendExtensionReader{items: []Extension{consumer, owner}}
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(reader, trustStore)
	grantPublicFrontend(t, trust, owner)
	grantPublicFrontend(t, trust, consumer)

	first := newAdmittedPublicFrontendService(reader, trust)
	if err := first.RestorePublicAssetPublications(t.Context(), []Extension{consumer, owner}, false); err != nil {
		t.Fatal(err)
	}
	descriptor, err := first.PublicComponent(t.Context(), consumer.ID, consumer.Manifest.Components[0].ID)
	if err != nil || len(descriptor.Assets) != 2 || descriptor.Assets[0].Handle != ownerHandle {
		t.Fatalf("consumer-first dependency plan=%#v err=%v", descriptor.Assets, err)
	}
	firstSnapshot := first.publicAssets.Snapshot()

	reader.items = []Extension{owner, consumer}
	restarted := newAdmittedPublicFrontendService(reader, trust)
	if err := restarted.RestorePublicAssetPublications(t.Context(), []Extension{owner, consumer}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.PublicComponent(t.Context(), owner.ID, owner.Manifest.Components[0].ID); err != nil {
		t.Fatal(err)
	}
	if secondSnapshot := restarted.publicAssets.Snapshot(); !reflect.DeepEqual(firstSnapshot.Publications, secondSnapshot.Publications) ||
		!reflect.DeepEqual(firstSnapshot.Assets, secondSnapshot.Assets) || firstSnapshot.Digest != secondSnapshot.Digest {
		t.Fatalf("catalog order changed snapshot: first=%#v second=%#v", firstSnapshot, secondSnapshot)
	}
}

func TestPublicFrontendServesExactPackageLocalModuleCSSAndBinaryResources(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)

	tests := []struct {
		path        string
		contentType string
	}{
		{path: "frontend/public/card.mjs", contentType: "application/javascript; charset=utf-8"},
		{path: "frontend/public/chunk.mjs", contentType: "application/javascript; charset=utf-8"},
		{path: "frontend/public/card.css", contentType: "text/css; charset=utf-8"},
		{path: "frontend/public/nested.css", contentType: "text/css; charset=utf-8"},
		{path: "frontend/public/font.woff2", contentType: "font/woff2"},
	}
	for _, test := range tests {
		asset, err := service.PublicPackageAsset(t.Context(), extension.ID, extension.PackageDigest, test.path)
		if err != nil || asset.ContentType != test.contentType || asset.Digest == "" || asset.Integrity == "" {
			t.Fatalf("%s asset=%#v err=%v", test.path, asset, err)
		}
	}
	for _, rejected := range []string{
		"frontend/public/unsafe.html", "frontend/public/unsafe.svg", "frontend/public/missing.mjs", "../card.mjs",
	} {
		if _, err := service.PublicPackageAsset(t.Context(), extension.ID, extension.PackageDigest, rejected); !errors.Is(err, ErrPublicFrontendUnavailable) {
			t.Fatalf("unsafe package resource %q error=%v", rejected, err)
		}
	}
	if _, err := service.PublicPackageAsset(
		t.Context(), extension.ID, strings.Repeat("f", 64), "frontend/public/card.mjs",
	); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("stale package resource error=%v", err)
	}
}

func TestPublicFrontendServesOnlyDeclaredImmutableBytes(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	store := &memoryExecutableTrustStore{}
	exactTrust := NewExecutableTrustService(reader, store)
	service := newAdmittedPublicFrontendService(reader, exactTrust)
	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	descriptor, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	entry, err := service.PublicAsset(context.Background(), extension.ID, extension.PackageDigest, descriptor.Entry.Digest, descriptor.Entry.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ContentType != "application/javascript; charset=utf-8" || entry.ETag != `"`+descriptor.Entry.Digest+`"` ||
		entry.Integrity != descriptor.Entry.Integrity || !strings.Contains(string(entry.Body), "export async function mount") {
		t.Fatalf("unexpected entry asset: %#v", entry)
	}
	styleRef := descriptor.Assets[0]
	style, err := service.PublicAsset(context.Background(), extension.ID, extension.PackageDigest, styleRef.Digest, styleRef.Handle)
	if err != nil || style.ContentType != "text/css; charset=utf-8" || !strings.Contains(string(style.Body), ".demo-l2") {
		t.Fatalf("style asset=%#v err=%v", style, err)
	}
	if _, err := service.PublicAsset(context.Background(), extension.ID, strings.Repeat("f", 64), descriptor.Entry.Digest, descriptor.Entry.Handle); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("stale package digest must fail, got %v", err)
	}
	if _, err := service.PublicAsset(context.Background(), extension.ID, extension.PackageDigest, strings.Repeat("f", 64), descriptor.Entry.Handle); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("stale file digest must fail, got %v", err)
	}
	if _, err := service.PublicAsset(context.Background(), extension.ID, extension.PackageDigest, descriptor.Entry.Digest, "demo.public.undeclared"); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("undeclared handle must fail, got %v", err)
	}
}

func TestPublicFrontendRebuildsAssetSnapshotAfterHostRestart(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	store := &memoryExecutableTrustStore{}
	exactTrust := NewExecutableTrustService(reader, store)
	first := newAdmittedPublicFrontendService(reader, exactTrust)
	grantPublicFrontend(t, exactTrust, extension)
	if err := first.RestorePublicAssetPublications(context.Background(), []Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	descriptor, err := first.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	restarted := newAdmittedPublicFrontendService(reader, exactTrust)
	if err := restarted.RestorePublicAssetPublications(context.Background(), []Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	asset, err := restarted.PublicAsset(context.Background(), extension.ID, extension.PackageDigest, descriptor.Entry.Digest, descriptor.Entry.Handle)
	if err != nil || len(asset.Body) == 0 || restarted.publicAssets.Snapshot().Revision != 1 {
		t.Fatalf("restart did not rebuild immutable asset snapshot: asset=%#v revision=%d err=%v", asset, restarted.publicAssets.Snapshot().Revision, err)
	}
}

func TestRestorePublicAssetPublicationsIfRevisionRejectsStaleCaller(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)

	if err := service.RestorePublicAssetPublicationsIfRevision(
		t.Context(), 0, []Extension{extension}, false,
	); err != nil {
		t.Fatal(err)
	}
	before := service.publicAssets.Snapshot()
	if err := service.RestorePublicAssetPublicationsIfRevision(
		t.Context(), 0, nil, true,
	); !errors.Is(err, assetregistry.ErrRevisionConflict) {
		t.Fatalf("stale full replacement error=%v", err)
	}
	after := service.publicAssets.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("stale full replacement changed snapshot: before=%#v after=%#v", before, after)
	}
}

func TestPublicFrontendRevocationDisableSafeModeAndByteDriftFailClosed(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	store := &memoryExecutableTrustStore{}
	exactTrust := NewExecutableTrustService(reader, store)
	service := newAdmittedPublicFrontendService(reader, exactTrust)
	exactTrust.WithPublicAssetRegistry(service.publicAssets)
	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); err != nil {
		t.Fatal(err)
	}

	if err := exactTrust.RevokeAllForExtension(context.Background(), extension.ID, 1, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("revoked component must fail, got %v", err)
	}
	if len(service.publicAssets.Snapshot().Assets) != 0 {
		t.Fatal("revocation did not clear process-local assets")
	}

	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	reader.item.Status = StatusInstalled
	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("disabled component must fail, got %v", err)
	}
	reader.item.Status = StatusEnabled
	publishTrustedPublicAssets(t, service, extension)
	safe := newAdmittedPublicFrontendService(reader, exactTrust).
		WithPublicAssetRegistry(service.publicAssets).
		WithSafeMode(true)
	if err := safe.RestorePublicAssetPublications(context.Background(), []Extension{extension}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := safe.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("safe mode must close L2, got %v", err)
	}
	if len(service.publicAssets.Snapshot().Assets) != 0 {
		t.Fatal("safe mode restore retained third-party asset publications")
	}

	// 重新发布后制造字节漂移：请求必须失败关闭并隔离 captured artifact。
	publishTrustedPublicAssets(t, service, extension)
	entryPath := filepath.Join(extension.PackagePath, extension.Manifest.PackageFiles[0].Path)
	if err := os.WriteFile(entryPath, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	descriptor, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicAsset(
		context.Background(), extension.ID, extension.PackageDigest, descriptor.Entry.Digest, descriptor.Entry.Handle,
	); !errors.Is(err, ErrPublicFrontendUnavailable) || !errors.Is(err, ErrFrontendPackageChanged) {
		t.Fatalf("byte drift must invalidate exact public runtime, got %v", err)
	}
	if _, ok := service.publicAssets.SnapshotPublication(extension.ID); ok {
		t.Fatal("byte drift must quarantine exact publication")
	}
}

func TestPublicFrontendUnknownRevokeCommitQuarantinesBeforeFenceReturns(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	commitErr := errors.New("commit response lost")
	store := &memoryExecutableTrustStore{revokeAllErr: errors.Join(ErrTrustRevocationCommitUnknown, commitErr)}
	trust := NewExecutableTrustService(reader, store)
	service := newAdmittedPublicFrontendService(reader, trust)
	trust.WithPublicAssetRegistry(service.publicAssets)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)
	sink := &recordingExecutableTrustRevocationSink{afterDurable: func(err error) {
		if !errors.Is(err, ErrTrustRevocationCommitUnknown) {
			t.Fatalf("durable error=%v", err)
		}
		if _, found := service.publicAssets.SnapshotPublication(extension.ID); found {
			t.Fatal("unknown commit left public assets open inside runtime fence")
		}
	}}
	trust.WithRevocationSink(sink)
	err := trust.RevokeAllForExtension(t.Context(), extension.ID, 1, "unknown-commit")
	if !errors.Is(err, ErrTrustRevocationCommitUnknown) || !errors.Is(err, commitErr) {
		t.Fatalf("unknown revoke result=%v", err)
	}
	if _, found := service.publicAssets.SnapshotPublication(extension.ID); found {
		t.Fatal("unknown commit republished public assets")
	}
}

func TestPublicFrontendRequestPathDoesNotListOrRebuildRegistry(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &countingFrontendExtensionReader{item: extension}
	exactTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, exactTrust)
	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	// 改变未声明文件会改变整包 DigestTree；请求仍应只校验发布元组、live grant 和目标文件字节。
	if err := os.WriteFile(filepath.Join(extension.PackagePath, "request-path-drift.txt"), []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	revisionBefore := service.publicAssets.Revision()
	listsBefore := reader.listCalls
	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); err != nil {
		t.Fatal(err)
	}
	if reader.listCalls != listsBefore {
		t.Fatalf("PublicComponent called Store.List %d times", reader.listCalls-listsBefore)
	}
	descriptor, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	listsBefore = reader.listCalls
	if _, err := service.PublicAsset(
		context.Background(), extension.ID, extension.PackageDigest, descriptor.Entry.Digest, descriptor.Entry.Handle,
	); err != nil {
		t.Fatal(err)
	}
	if reader.listCalls != listsBefore {
		t.Fatalf("PublicAsset called Store.List %d times", reader.listCalls-listsBefore)
	}
	listsBefore = reader.listCalls
	if _, err := service.PublicPackageAsset(
		context.Background(), extension.ID, extension.PackageDigest, "frontend/public/card.mjs",
	); err != nil {
		t.Fatal(err)
	}
	if reader.listCalls != listsBefore {
		t.Fatalf("PublicPackageAsset called Store.List %d times", reader.listCalls-listsBefore)
	}
	if revisionAfter := service.publicAssets.Revision(); revisionAfter != revisionBefore {
		t.Fatalf("public requests changed registry revision: before=%d after=%d", revisionBefore, revisionAfter)
	}
}

func TestPublicFrontendSharedAssetRegistryInstance(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	exactTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	shared := NewFrontendService(reader, &fakeFrontendTrustStore{}).PublicAssetRegistry()
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}).
		WithExecutableTrust(exactTrust, true).
		WithPublicComponentAdmission(allowPublicComponentAdmission{}).
		WithPublicAssetRegistry(shared)
	if service.PublicAssetRegistry() != shared {
		t.Fatal("FrontendService did not retain the injected shared Asset Registry")
	}
	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	if len(shared.Snapshot().Assets) == 0 {
		t.Fatal("shared registry was not published through FrontendService")
	}
}

func TestPublicFrontendStaleArtifactFenceRejectsOverwrite(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	exactTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, exactTrust)
	grantPublicFrontend(t, exactTrust, extension)
	publishTrustedPublicAssets(t, service, extension)
	current, ok := service.publicAssets.SnapshotPublication(extension.ID)
	if !ok {
		t.Fatal("expected current publication")
	}
	stale := current
	stale.Artifact.PackageDigest = strings.Repeat("b", 64)
	if _, err := service.publicAssets.Publish(stale); !errors.Is(err, assetregistry.ErrArtifactConflict) {
		t.Fatalf("stale publish overwrite error=%v", err)
	}
	if _, _, err := service.publicAssets.QuarantineExact(stale.Artifact); !errors.Is(err, assetregistry.ErrArtifactConflict) {
		t.Fatalf("stale quarantine error=%v", err)
	}
	if _, ok := service.publicAssets.SnapshotPublication(extension.ID); !ok {
		t.Fatal("current publication was removed by stale artifact action")
	}
}

func TestPublicFrontendGateDefaultsClosed(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	exactTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}).WithExecutableTrust(exactTrust, true)
	if _, err := service.PublicComponent(context.Background(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("public L2 migration gate must default closed, got %v", err)
	}
}

func TestPublicFrontendComponentAdmissionDefaultsClosed(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	exactTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	grantPublicFrontend(t, exactTrust, extension)
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}).
		WithExecutableTrust(exactTrust, true)
	if _, err := service.PublicComponent(t.Context(), extension.ID, extension.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("public L2 must require Component Registry admission, got %v", err)
	}
}

func TestPublicFrontendAssetOnlyProviderRequiresTrustAndSupportsConsumerPlan(t *testing.T) {
	owner := publicAssetOnlyFixture(t, "owner.assetonly")
	if !RequiresExecutableTrust(owner) {
		t.Fatal("asset-only uploaded provider must require executable trust")
	}
	ownerHandle := owner.Manifest.Assets[0].Handle
	consumer := publicFrontendFixtureFor(t, "consumer.assetonly", []string{ownerHandle})
	reader := &fakeFrontendExtensionReader{items: []Extension{owner, consumer}}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)

	if _, err := service.PublicPackageAsset(t.Context(), owner.ID, owner.PackageDigest, owner.Manifest.Assets[0].Path); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("untrusted asset-only package must be unavailable, got %v", err)
	}
	grantPublicFrontend(t, trust, owner)
	grantPublicFrontend(t, trust, consumer)
	if err := service.RestorePublicAssetPublications(t.Context(), []Extension{owner, consumer}, false); err != nil {
		t.Fatal(err)
	}
	descriptor, err := service.PublicComponent(t.Context(), consumer.ID, consumer.Manifest.Components[0].ID)
	if err != nil || len(descriptor.Assets) < 2 || descriptor.Assets[0].Handle != ownerHandle {
		t.Fatalf("consumer plan must include asset-only owner: assets=%#v err=%v", descriptor.Assets, err)
	}
	declared, err := service.PublicAsset(
		t.Context(), owner.ID, owner.PackageDigest, descriptor.Assets[0].Digest, descriptor.Assets[0].Handle,
	)
	if err != nil || declared.ContentType != "text/css; charset=utf-8" {
		t.Fatalf("asset-only owner declared asset=%#v err=%v", declared, err)
	}
	asset, err := service.PublicPackageAsset(t.Context(), owner.ID, owner.PackageDigest, owner.Manifest.Assets[0].Path)
	if err != nil || asset.ContentType != "text/css; charset=utf-8" {
		t.Fatalf("asset-only owner package asset=%#v err=%v", asset, err)
	}
}

func TestPublicFrontendAcceptsHostCoreAssetDependencyWithoutExtensionLookup(t *testing.T) {
	coreHandle := "core.asset.runtime"
	consumer := publicFrontendFixtureFor(t, "consumer.coreasset", []string{coreHandle})
	reader := &fakeFrontendExtensionReader{item: consumer}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, consumer)
	core := assetregistry.Publication{Artifact: assetregistry.Artifact{
		ExtensionID: "core.assets", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), ImpactDigest: strings.Repeat("b", 64),
		OwnerKind: assetregistry.OwnerKindCore, Core: true,
	}, Assets: []assetregistry.Declaration{{
		Handle: coreHandle, ContractVersion: "sforum.asset.runtime@1", Type: "script",
		Path: "runtime.mjs", Digest: strings.Repeat("c", 64), Module: true,
	}}}
	if _, err := service.publicAssets.Publish(core); err != nil {
		t.Fatal(err)
	}
	publishTrustedPublicAssets(t, service, consumer)
	descriptor, err := service.PublicComponent(t.Context(), consumer.ID, consumer.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	foundCore := false
	for _, asset := range descriptor.Assets {
		foundCore = foundCore || asset.Handle == coreHandle
	}
	if !foundCore {
		t.Fatalf("core dependency missing from descriptor: %#v", descriptor.Assets)
	}
}

func TestPublicFrontendRevokeWithRequiredConsumerQuarantinesClosure(t *testing.T) {
	owner := publicFrontendFixtureFor(t, "owner.closure", nil)
	ownerHandle := owner.Manifest.Assets[0].Handle
	consumer := publicFrontendFixtureFor(t, "consumer.closure", []string{ownerHandle})
	reader := &fakeFrontendExtensionReader{items: []Extension{owner, consumer}}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	trust.WithPublicAssetRegistry(service.publicAssets)
	grantPublicFrontend(t, trust, owner)
	grantPublicFrontend(t, trust, consumer)
	if err := service.RestorePublicAssetPublications(t.Context(), []Extension{owner, consumer}, false); err != nil {
		t.Fatal(err)
	}
	if len(service.publicAssets.Snapshot().Publications) != 2 {
		t.Fatalf("expected owner+consumer publications, got %#v", service.publicAssets.Snapshot().Publications)
	}
	if err := trust.RevokeAllForExtension(t.Context(), owner.ID, 1, "test-closure"); err != nil {
		t.Fatal(err)
	}
	snapshot := service.publicAssets.Snapshot()
	if len(snapshot.Publications) != 0 || len(snapshot.Assets) != 0 {
		t.Fatalf("owner revoke must quarantine dependent closure, snapshot=%#v", snapshot)
	}
	if _, err := service.PublicComponent(t.Context(), consumer.ID, consumer.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("consumer descriptor must not survive owner revoke, got %v", err)
	}
}

func TestPublicFrontendStaleCapturedRevokeCannotRemoveUpgrade(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	store := &revokeHookExecutableTrustStore{memoryExecutableTrustStore: &memoryExecutableTrustStore{}}
	trust := NewExecutableTrustService(reader, store)
	service := newAdmittedPublicFrontendService(reader, trust)
	trust.WithPublicAssetRegistry(service.publicAssets)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)
	current, ok := service.publicAssets.SnapshotPublication(extension.ID)
	if !ok {
		t.Fatal("expected live publication")
	}
	upgrade := current
	upgrade.Artifact.ExtensionVersion = "1.0.1"
	upgrade.Artifact.PackageDigest = strings.Repeat("c", 64)
	upgrade.Artifact.ImpactDigest = strings.Repeat("d", 64)
	store.beforeRevoke = func() error {
		_, err := service.publicAssets.PublishIfArtifact(current.Artifact, upgrade)
		return err
	}
	if err := trust.RevokeAllForExtension(t.Context(), extension.ID, 1, "stale-race"); !errors.Is(err, assetregistry.ErrArtifactConflict) {
		t.Fatalf("stale captured revoke must report conflict, got %v", err)
	}
	live, ok := service.publicAssets.SnapshotPublication(extension.ID)
	if !ok || live.Artifact != upgrade.Artifact {
		t.Fatalf("upgrade must remain after stale quarantine: %#v ok=%t", live, ok)
	}
}

func TestPublicFrontendDependencyOwnerLiveTrustBeforeDescriptor(t *testing.T) {
	owner := publicFrontendFixtureFor(t, "owner.deptrust", nil)
	ownerHandle := owner.Manifest.Assets[0].Handle
	consumer := publicFrontendFixtureFor(t, "consumer.deptrust", []string{ownerHandle})
	reader := &fakeFrontendExtensionReader{items: []Extension{owner, consumer}}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	trust.WithPublicAssetRegistry(service.publicAssets)
	grantPublicFrontend(t, trust, owner)
	grantPublicFrontend(t, trust, consumer)
	if err := service.RestorePublicAssetPublications(t.Context(), []Extension{owner, consumer}, false); err != nil {
		t.Fatal(err)
	}
	// 吊销 owner grant 但先不隔离 registry，模拟 live grant 失效。
	if err := trust.store.RevokeAll(t.Context(), owner.ID, 1, "owner-only"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicComponent(t.Context(), consumer.ID, consumer.Manifest.Components[0].ID); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("descriptor must require dependency owner live trust, got %v", err)
	}
	// 失败关闭后 owner 闭包应被隔离，不能继续暴露 CSP/依赖。
	if _, ok := service.publicAssets.SnapshotPublication(owner.ID); ok {
		t.Fatal("missing owner grant must quarantine owner publication")
	}
	if _, ok := service.publicAssets.SnapshotPublication(consumer.ID); ok {
		t.Fatal("missing owner grant must quarantine dependent consumer publication")
	}
}

func TestPublicFrontendSymlinkAndByteDriftFailClosed(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)
	descriptor, err := service.PublicComponent(t.Context(), extension.ID, extension.Manifest.Components[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	entryPath := filepath.Join(extension.PackagePath, extension.Manifest.PackageFiles[0].Path)
	outside := filepath.Join(t.TempDir(), "evil.mjs")
	if err := os.WriteFile(outside, []byte("export const evil = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(entryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, entryPath); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicAsset(
		t.Context(), extension.ID, extension.PackageDigest, descriptor.Entry.Digest, descriptor.Entry.Handle,
	); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("symlink asset must fail closed, got %v", err)
	}

	// 恢复常规文件后写入漂移内容；grant/registry 仍绑定旧 digest，fd 读取必须失败关闭。
	if err := os.Remove(entryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entryPath, []byte("export const swapped = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicPackageAsset(
		t.Context(), extension.ID, extension.PackageDigest, "frontend/public/card.mjs",
	); !errors.Is(err, ErrPublicFrontendUnavailable) || !errors.Is(err, ErrFrontendPackageChanged) {
		t.Fatalf("swapped package bytes must fail closed, got %v", err)
	}
}

func TestPublicFrontendRejectsIntermediateSymlinkEscape(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := newAdmittedPublicFrontendService(reader, trust)
	grantPublicFrontend(t, trust, extension)
	publishTrustedPublicAssets(t, service, extension)

	publicDir := filepath.Join(extension.PackagePath, "frontend", "public")
	outsideDir := filepath.Join(t.TempDir(), "public")
	// 整体移动保留完全相同的 bytes/digest，确保测试验证的是 root containment。
	if err := os.Rename(publicDir, outsideDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, publicDir); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublicPackageAsset(
		t.Context(), extension.ID, extension.PackageDigest, "frontend/public/card.mjs",
	); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("intermediate symlink escape error=%v", err)
	}
}

func TestReadOpenedStableRegularFileRejectsPathSwap(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "asset.mjs")
	replacement := filepath.Join(root, "replacement.mjs")
	body := []byte("export const exact = true\n")
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.WriteFile(replacement, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, target); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenedStableRegularFile(file, target, int64(len(body)), true); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("path swap error=%v", err)
	}
}

func TestReadStableRegularFileRejectsNonRegularAndOversize(t *testing.T) {
	root := t.TempDir()
	if _, err := readStableRegularFile(root, 16, false); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("directory read error=%v", err)
	}
	target := filepath.Join(root, "large.css")
	if err := os.WriteFile(target, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readStableRegularFile(target, 4, false); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("oversize read error=%v", err)
	}
}

func TestRequiresExecutableTrustIncludesAssetRegistryDeclarations(t *testing.T) {
	extension := publicAssetOnlyFixture(t, "trust.asset.registry")
	if !RequiresExecutableTrust(extension) {
		t.Fatal("uploaded asset registry script/style must require trust without L2")
	}
	extension.Manifest.Assets = nil
	if RequiresExecutableTrust(extension) {
		t.Fatal("asset-only fixture without assets must not require trust")
	}
}

func TestBuildPublicAssetPublicationUsesExtensionTypeOwnerKind(t *testing.T) {
	extension := publicAssetOnlyFixture(t, "owner.kind")
	for _, test := range []struct {
		typ  string
		want string
	}{{typ: TypePlugin, want: assetregistry.OwnerKindPlugin}, {typ: TypeTheme, want: assetregistry.OwnerKindTheme}} {
		extension.Type = test.typ
		publication, err := BuildPublicAssetPublication(extension, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		if publication.Artifact.OwnerKind != test.want || publication.Artifact.Core {
			t.Fatalf("type=%s artifact=%#v", test.typ, publication.Artifact)
		}
	}
}

func TestBuildPublicAssetPublicationRejectsDeclarationsOverLimit(t *testing.T) {
	extension := publicAssetOnlyFixture(t, "asset.limit")
	asset := extension.Manifest.Assets[0]
	extension.Manifest.Assets = make([]ManifestAsset, maxPublicL2Assets+1)
	for index := range extension.Manifest.Assets {
		extension.Manifest.Assets[index] = asset
	}

	if _, err := BuildPublicAssetPublication(extension, strings.Repeat("a", 64)); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("expected declarations over the publication limit to fail closed, got %v", err)
	}
}

func TestValidatePublishedIdentityRejectsTupleMismatchWithoutDigestTree(t *testing.T) {
	extension := publicFrontendFixture(t)
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	grantPublicFrontend(t, trust, extension)
	identity, err := trust.RuntimeIdentity(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	ownerKind, ok := publicAssetOwnerKind(extension)
	if !ok {
		t.Fatal("expected plugin owner kind")
	}
	artifact := assetregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, ImpactDigest: identity.ImpactDigest,
		OwnerKind: ownerKind,
	}
	if err := trust.ValidatePublishedIdentity(t.Context(), extension, artifact); err != nil {
		t.Fatal(err)
	}
	builtin := extension
	builtin.Source = SourceBuiltin
	if err := trust.ValidatePublishedIdentity(t.Context(), builtin, artifact); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("source-only builtin identity error=%v", err)
	}
	builtin.IsSystem = true
	builtin.IsDeletable = false
	if err := trust.ValidatePublishedIdentity(t.Context(), builtin, artifact); err != nil {
		t.Fatalf("builtin published identity error=%v", err)
	}
	core := assetregistry.Artifact{
		ExtensionID: "core.assets", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("b", 64), ImpactDigest: strings.Repeat("c", 64),
		OwnerKind: assetregistry.OwnerKindCore, Core: true,
	}
	if err := trust.ValidatePublishedIdentity(t.Context(), Extension{}, core); err != nil {
		t.Fatalf("core published identity error=%v", err)
	}
	wrongKind := artifact
	wrongKind.OwnerKind = assetregistry.OwnerKindTheme
	if err := trust.ValidatePublishedIdentity(t.Context(), extension, wrongKind); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("owner kind mismatch error=%v", err)
	}
	stale := artifact
	stale.PackageDigest = strings.Repeat("a", 64)
	if err := trust.ValidatePublishedIdentity(t.Context(), extension, stale); !errors.Is(err, ErrPublicFrontendUnavailable) {
		t.Fatalf("tuple mismatch error=%v", err)
	}
	// 删除包路径后 RuntimeIdentity 会因 DigestTree 失败；ValidatePublishedIdentity 不得扫盘。
	if err := os.RemoveAll(extension.PackagePath); err != nil {
		t.Fatal(err)
	}
	if err := trust.ValidatePublishedIdentity(t.Context(), extension, artifact); err != nil {
		t.Fatalf("ValidatePublishedIdentity must not scan package bytes, got %v", err)
	}
}

type allowPublicComponentAdmission struct{}

func (allowPublicComponentAdmission) AdmitPublicComponent(Extension, ManifestComponent) bool {
	return true
}

func newAdmittedPublicFrontendService(
	reader FrontendExtensionReader,
	trust *ExecutableTrustService,
) *FrontendService {
	return NewFrontendService(reader, &fakeFrontendTrustStore{}).
		WithExecutableTrust(trust, true).
		WithPublicComponentAdmission(allowPublicComponentAdmission{})
}

type countingFrontendExtensionReader struct {
	item      Extension
	listCalls int
}

type revokeHookExecutableTrustStore struct {
	*memoryExecutableTrustStore
	beforeRevoke func() error
}

func (s *revokeHookExecutableTrustStore) RevokeAll(
	ctx context.Context,
	extensionID string,
	actorUserID int64,
	reason string,
) error {
	if s.beforeRevoke != nil {
		if err := s.beforeRevoke(); err != nil {
			return err
		}
		s.beforeRevoke = nil
	}
	return s.memoryExecutableTrustStore.RevokeAll(ctx, extensionID, actorUserID, reason)
}

func (r *countingFrontendExtensionReader) Get(_ context.Context, id string) (Extension, error) {
	if r.item.ID == id {
		return r.item, nil
	}
	return Extension{}, ErrExtensionNotFound
}

func (r *countingFrontendExtensionReader) List(context.Context) ([]Extension, error) {
	r.listCalls++
	return []Extension{r.item}, nil
}

func grantPublicFrontend(t *testing.T, trust *ExecutableTrustService, extension Extension) {
	t.Helper()
	actor := frontendSuperAdmin()
	challenge, err := trust.Challenge(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := trust.ConfirmEnable(context.Background(), actor, extension, challenge.Token); err != nil {
		t.Fatal(err)
	}
}

func publishTrustedPublicAssets(t *testing.T, service *FrontendService, extension Extension) {
	t.Helper()
	identity, err := service.executableTrust.RuntimeIdentity(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := BuildPublicAssetPublication(extension, identity.ImpactDigest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.publicAssets.Publish(publication); err != nil {
		t.Fatal(err)
	}
}

func publicFrontendFixture(t *testing.T) Extension {
	return publicFrontendFixtureFor(t, "demo.public", nil)
}

func publicAssetOnlyFixture(t *testing.T, id string) Extension {
	t.Helper()
	root := t.TempDir()
	stylePath := "frontend/public/shared.css"
	styleBody := []byte(".asset-only { color: var(--sf-accent); }\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, stylePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, stylePath), styleBody, 0o644); err != nil {
		t.Fatal(err)
	}
	styleDigest := bytesDigest(styleBody)
	manifest := Manifest{
		ManifestVersion: 3, ID: id, Name: "Asset Only", Version: "1.0.0", Type: TypePlugin,
		PackageFiles: []ManifestPackageFile{
			{ID: id + ".file.style", Kind: "asset", Path: stylePath, Digest: styleDigest},
		},
		Assets: []ManifestAsset{{
			Handle: id + ".asset.style", ContractVersion: id + ".asset.style@1",
			Type: "style", Path: stylePath, Digest: styleDigest, Loading: "blocking",
			CSP: []string{"style-src 'self'"},
		}},
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: StatusEnabled, Source: SourceUploaded, IsDeletable: true, Manifest: manifest,
		PackagePath: root, PackageDigest: packageDigest,
	}
}

func TestEditorOnlyL2UsesExactPublicPackageAssetBoundary(t *testing.T) {
	root := t.TempDir()
	modulePath := "frontend/editor/shortcodes.mjs"
	moduleBody := []byte("export const apiVersion = 1\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, modulePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, modulePath), moduleBody, 0o644); err != nil {
		t.Fatal(err)
	}
	moduleDigest := bytesDigest(moduleBody)
	extension := Extension{
		ID: "demo.editor", Version: "1.0.0", Type: TypePlugin, Status: StatusEnabled,
		Source: SourceBuiltin, IsSystem: true, IsDeletable: false, PackagePath: root,
		Manifest: Manifest{
			ManifestVersion: 3, ID: "demo.editor", Name: "Editor", Version: "1.0.0", Type: TypePlugin,
			PackageFiles: []ManifestPackageFile{{
				ID: "demo.editor.file.module", Kind: "frontend", Path: modulePath, Digest: moduleDigest,
			}},
			Editor: []extensionmanifest.ManifestEditor{
				{ID: "demo.editor.node.reference", ContractVersion: "demo.editor.node.reference@1", Kind: "node", L2Module: modulePath, L2Digest: moduleDigest},
				{ID: "demo.editor.command.reference", ContractVersion: "demo.editor.command.reference@1", Kind: "command", L2Module: modulePath, L2Digest: moduleDigest},
			},
		},
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest = packageDigest
	reader := &fakeFrontendExtensionReader{item: extension}
	trust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}).
		WithExecutableTrust(trust, true)
	identity, err := trust.RuntimeIdentity(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := BuildPublicAssetPublication(extension, identity.ImpactDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(publication.Assets) != 1 || publication.Assets[0].Path != modulePath ||
		len(publication.Assets[0].Scope) != 1 {
		t.Fatalf("editor asset publication=%+v", publication.Assets)
	}
	if _, err := service.publicAssets.Publish(publication); err != nil {
		t.Fatal(err)
	}
	asset, err := service.PublicPackageAsset(t.Context(), extension.ID, extension.PackageDigest, modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(asset.Body, moduleBody) || asset.Digest != moduleDigest {
		t.Fatalf("editor package asset=%+v body=%q", asset, asset.Body)
	}
	plan, err := service.publicAssets.Plan(assetregistry.PlanRequest{IncludeGlobal: true})
	if err != nil || len(plan) != 0 {
		t.Fatalf("editor module leaked into global asset plan: plan=%+v err=%v", plan, err)
	}
}

func publicFrontendFixtureFor(t *testing.T, id string, assetDependencies []string) Extension {
	t.Helper()
	root := t.TempDir()
	entryPath := "frontend/public/card.mjs"
	stylePath := "frontend/public/card.css"
	files := map[string][]byte{
		entryPath:                     []byte("import { marker } from './chunk.mjs'\nexport const apiVersion = 1\nexport const moduleURL = import.meta.url\nexport async function mount(target) { target.dataset.mounted = marker; return () => target.replaceChildren() }\n"),
		stylePath:                     []byte("@import './nested.css';\n.demo-l2 { font-family: demo; src: url('./font.woff2'); }\n"),
		"frontend/public/chunk.mjs":   []byte("export const marker = '1'\n"),
		"frontend/public/nested.css":  []byte(".nested { color: var(--sf-accent); }\n"),
		"frontend/public/font.woff2":  []byte("test-font"),
		"frontend/public/unsafe.html": []byte("<script>top.location='https://example.invalid'</script>"),
		"frontend/public/unsafe.svg":  []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"),
		"schemas/card-props.json":     []byte("{\"type\":\"object\",\"properties\":{\"title\":{\"type\":\"string\"}}}\n"),
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entryBody := files[entryPath]
	styleBody := files[stylePath]
	entryDigest := bytesDigest(entryBody)
	styleDigest := bytesDigest(styleBody)
	manifest := Manifest{
		ManifestVersion: 3, ID: id, Name: "Public L2", Version: "1.0.0", Type: TypePlugin,
		PackageFiles: []ManifestPackageFile{
			{ID: id + ".file.card", Kind: "frontend", Path: entryPath, Digest: entryDigest},
			{ID: id + ".file.style", Kind: "asset", Path: stylePath, Digest: styleDigest},
			{ID: id + ".file.chunk", Kind: "frontend", Path: "frontend/public/chunk.mjs", Digest: bytesDigest(files["frontend/public/chunk.mjs"])},
			{ID: id + ".file.nested", Kind: "asset", Path: "frontend/public/nested.css", Digest: bytesDigest(files["frontend/public/nested.css"])},
			{ID: id + ".file.font", Kind: "asset", Path: "frontend/public/font.woff2", Digest: bytesDigest(files["frontend/public/font.woff2"])},
			{ID: id + ".file.html", Kind: "asset", Path: "frontend/public/unsafe.html", Digest: bytesDigest(files["frontend/public/unsafe.html"])},
			{ID: id + ".file.svg", Kind: "asset", Path: "frontend/public/unsafe.svg", Digest: bytesDigest(files["frontend/public/unsafe.svg"])},
			// 组件 props/result schema 必须有同包精确声明（与 provider 同一规则）。
			{ID: id + ".component.card.props", Kind: "schema", Path: "schemas/card-props.json", Digest: bytesDigest(files["schemas/card-props.json"]), Version: "1"},
		},
		Assets: []ManifestAsset{{
			Handle: id + ".asset.style", ContractVersion: id + ".asset.style@1",
			Type: "style", Path: stylePath, Digest: styleDigest,
			Dependencies: append([]string(nil), assetDependencies...),
			Scope:        []string{id + ".component.card"}, Loading: "blocking", CSP: []string{"connect-src 'self'"},
		}},
		Components: []ManifestComponent{{
			ID: id + ".component.card", ContractVersion: id + ".component.card@1",
			Action: "add", L2Component: id + ".file.card", PropsSchema: id + ".component.card.props@1",
		}},
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: StatusEnabled, Source: SourceUploaded, IsDeletable: true, Manifest: manifest,
		PackagePath: root, PackageDigest: packageDigest,
	}
}

func bytesDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
