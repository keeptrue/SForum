package extensions

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestFrontendServiceTrustMutationsRequireSuperAdmin(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, &fakeFrontendTrustStore{})
	actor := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionExtensionManage: true}}

	if _, err := service.Challenge(context.Background(), actor, extension.ID); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("challenge: expected permission denied, got %v", err)
	}
	if _, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{Digest: extension.AdminFrontendDigest}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("grant: expected permission denied, got %v", err)
	}
	if _, err := service.Revoke(context.Background(), actor, extension.ID); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("revoke: expected permission denied, got %v", err)
	}
}

func TestFrontendServiceGrantIsConfirmationAndDigestBound(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	trust := &fakeFrontendTrustStore{}
	auditor := &recordingAuditor{}
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, trust).WithAuditor(auditor)
	actor := frontendSuperAdmin()

	if _, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{Digest: extension.AdminFrontendDigest}); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("missing confirmation must fail, got %v", err)
	}
	if _, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{Digest: "stale"}); !errors.Is(err, ErrFrontendPackageChanged) {
		t.Fatalf("stale digest must fail, got %v", err)
	}

	challenge, err := service.Challenge(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	component := *prebuiltSettingsComponent(extension)
	status, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{
		Digest: extension.AdminFrontendDigest,
		Confirmation: &FrontendTrustConfirmation{
			ChallengeID:  challenge.ChallengeID,
			Code:         challenge.Code,
			ExtensionID:  extension.ID,
			Version:      extension.Version,
			Digest:       extension.AdminFrontendDigest,
			APIVersion:   component.APIVersion,
			ComponentID:  component.ID,
			Phrase:       extension.ID,
			Acknowledged: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Kind != AdminFrontendKindPrebuiltComponent || status.TrustState != FrontendTrustTrusted {
		t.Fatalf("unexpected status: %#v", status)
	}
	if trust.created.PackageDigest != extension.PackageDigest || trust.created.AdminFrontendDigest != extension.AdminFrontendDigest ||
		trust.created.APIVersion != component.APIVersion || len(trust.created.ComponentIDs) != 1 || trust.created.ComponentIDs[0] != component.ID {
		t.Fatalf("grant identity mismatch: %#v", trust.created)
	}
	if !auditor.hasAction(audit.ActionExtensionFrontendGrant) {
		t.Fatal("grant must be audited")
	}
	if _, err := service.Grant(context.Background(), actor, extension.ID, GrantFrontendInput{
		Digest: extension.AdminFrontendDigest,
		Confirmation: &FrontendTrustConfirmation{
			ChallengeID:  challenge.ChallengeID,
			Code:         challenge.Code,
			ExtensionID:  extension.ID,
			Version:      extension.Version,
			Digest:       extension.AdminFrontendDigest,
			APIVersion:   component.APIVersion,
			ComponentID:  component.ID,
			Phrase:       extension.ID,
			Acknowledged: true,
		},
	}); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("challenge must be single-use, got %v", err)
	}
}

func TestFrontendServiceRevokeIsImmediate(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	trust := &fakeFrontendTrustStore{grant: frontendGrantForExtension(extension)}
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, trust)

	status, err := service.Revoke(context.Background(), frontendSuperAdmin(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustRevoked || trust.revoked.ExtensionID != extension.ID {
		t.Fatalf("revoke was not immediate: status=%#v input=%#v", status, trust.revoked)
	}
}

func TestFrontendServiceAssetRequiresExactGrantAndImmutableBytes(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	trust := &fakeFrontendTrustStore{grant: frontendGrantForExtension(extension)}
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, trust)

	asset, err := service.Asset(context.Background(), frontendSuperAdmin(), extension.ID, extension.AdminFrontendDigest, "entry")
	if err != nil {
		t.Fatal(err)
	}
	entryPath := extension.Manifest.SettingsDocument.UI.Component.Entry
	wantEntry, err := os.ReadFile(filepath.Join(extension.PackagePath, entryPath))
	if err != nil {
		t.Fatal(err)
	}
	if asset.ContentType != "application/javascript; charset=utf-8" || asset.ETag == "" ||
		!bytes.Equal(asset.Body, wantEntry) {
		t.Fatalf("unexpected asset: %#v", asset)
	}
	if _, err := service.Asset(context.Background(), frontendSuperAdmin(), extension.ID, extension.AdminFrontendDigest, "backend"); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("asset allowlist bypassed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extension.PackagePath, entryPath), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Asset(context.Background(), frontendSuperAdmin(), extension.ID, extension.AdminFrontendDigest, "entry"); !errors.Is(err, ErrFrontendPackageChanged) {
		t.Fatalf("changed bytes must invalidate grant: %v", err)
	}
}

func TestFrontendServiceV3UsesTheExactArtifactGrant(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	reader := &fakeFrontendExtensionReader{item: extension}
	exactStore := &memoryExecutableTrustStore{now: func() time.Time { return time.Now().UTC() }}
	exactTrust := NewExecutableTrustService(reader, exactStore)
	service := NewFrontendService(reader, &fakeFrontendTrustStore{}).WithExecutableTrust(exactTrust, true)
	actor := frontendSuperAdmin()

	status, err := service.Frontend(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustRequired {
		t.Fatalf("frontend must fall back before exact-artifact trust: %#v", status)
	}
	if _, err := service.Asset(context.Background(), actor, extension.ID, extension.AdminFrontendDigest, "entry"); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("asset must be closed before exact-artifact trust, got %v", err)
	}

	challenge, err := exactTrust.Challenge(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := exactTrust.ConfirmEnable(context.Background(), actor, extension, challenge.Token); err != nil {
		t.Fatal(err)
	}
	status, err = service.Frontend(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustTrusted {
		t.Fatalf("exact-artifact trust must cover the declared admin component: %#v", status)
	}
	if _, err := service.Asset(context.Background(), actor, extension.ID, extension.AdminFrontendDigest, "entry"); err != nil {
		t.Fatalf("exact-artifact trust should open immutable frontend asset: %v", err)
	}
}

func TestFrontendServiceV3DoesNotAcceptALegacyFrontendOnlyGrant(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	reader := &fakeFrontendExtensionReader{item: extension}
	exactTrust := NewExecutableTrustService(reader, &memoryExecutableTrustStore{})
	legacyTrust := &fakeFrontendTrustStore{grant: frontendGrantForExtension(extension)}
	service := NewFrontendService(reader, legacyTrust).WithExecutableTrust(exactTrust, true)

	status, err := service.Frontend(context.Background(), frontendSuperAdmin(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustRequired {
		t.Fatalf("legacy frontend-only grant must not bypass V3 exact trust: %#v", status)
	}
	if _, err := service.Asset(context.Background(), frontendSuperAdmin(), extension.ID, extension.AdminFrontendDigest, "entry"); !errors.Is(err, ErrFrontendTrustUnavailable) {
		t.Fatalf("legacy frontend-only grant opened V3 asset: %v", err)
	}
}

func TestFrontendServiceBuiltinComponentUsesSourceTrust(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceBuiltin)
	extension.Type = TypeTheme
	extension.Manifest.Type = TypeTheme
	extension.IsSystem = true
	extension.IsDeletable = false
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, &fakeFrontendTrustStore{})

	status, err := service.Frontend(context.Background(), frontendSuperAdmin(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustSourceTrusted {
		t.Fatalf("unexpected source trust: %#v", status)
	}
}

func TestFrontendServiceChangedDigestFallsBackToSchema(t *testing.T) {
	extension := prebuiltFrontendFixture(t, SourceUploaded)
	old := frontendGrantForExtension(extension)
	entry := extension.Manifest.SettingsDocument.UI.Component.Entry
	if err := os.WriteFile(filepath.Join(extension.PackagePath, entry), []byte("export function mount() { return () => {} }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ComputeAdminFrontendDigest(extension.Manifest, extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	extension.Version = "1.0.1"
	extension.Manifest.Version = extension.Version
	extension.AdminFrontendDigest = changed
	service := NewFrontendService(&fakeFrontendExtensionReader{item: extension}, &fakeFrontendTrustStore{all: []FrontendTrustGrant{old}})

	status, err := service.Frontend(context.Background(), frontendSuperAdmin(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.TrustState != FrontendTrustInvalidated || status.Kind != AdminFrontendKindPrebuiltComponent {
		t.Fatalf("changed digest should require reapproval: %#v", status)
	}
}

func frontendSuperAdmin() identity.Actor {
	return identity.Actor{ID: 1, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}}
}

func prebuiltFrontendFixture(t *testing.T, source string) Extension {
	t.Helper()
	root := t.TempDir()
	entry := "frontend/admin/dist/settings.mjs"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, entry)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, entry), []byte("export function mount() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		ID: "prebuilt.plugin", Name: "Prebuilt", Version: "1.0.0", Type: TypePlugin,
		Settings: []ManifestSetting{{Key: "title", Type: "text", Label: LocalizedText{Default: "Title"}}},
		SettingsDocument: SettingsDocument{SchemaVersion: 1, Explicit: true, UI: SettingsUI{
			Mode: "component", Layout: "form", Component: &SettingsComponent{ID: "settings", APIVersion: 1, Entry: entry},
		}},
	}
	manifest.SettingsDocument.Fields = manifest.Settings
	adminDigest, err := ComputeAdminFrontendDigest(manifest, root)
	if err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return Extension{
		ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Type: manifest.Type,
		Status: StatusEnabled, Source: source, IsDeletable: true, Manifest: manifest,
		PackagePath: root, PackageDigest: packageDigest, AdminFrontendDigest: adminDigest,
	}
}

func frontendGrantForExtension(extension Extension) FrontendTrustGrant {
	component := extension.Manifest.SettingsDocument.UI.Component
	return FrontendTrustGrant{
		ID: 1, ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, AdminFrontendDigest: extension.AdminFrontendDigest,
		APIVersion: component.APIVersion, ComponentIDs: []string{component.ID}, GrantedByUserID: 1, GrantedAt: time.Now(),
	}
}

type fakeFrontendExtensionReader struct {
	item  Extension
	items []Extension
	err   error
}

func (r *fakeFrontendExtensionReader) Get(_ context.Context, id string) (Extension, error) {
	if r.err != nil {
		return Extension{}, r.err
	}
	for _, item := range r.items {
		if item.ID == id {
			return item, nil
		}
	}
	if id != r.item.ID {
		return Extension{}, ErrExtensionNotFound
	}
	return r.item, nil
}

func (r *fakeFrontendExtensionReader) List(context.Context) ([]Extension, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.items != nil {
		return append([]Extension(nil), r.items...), nil
	}
	if r.item.ID == "" {
		return []Extension{}, nil
	}
	return []Extension{r.item}, nil
}

type fakeFrontendTrustStore struct {
	grant   FrontendTrustGrant
	all     []FrontendTrustGrant
	created FrontendTrustGrantInput
	revoked FrontendRevocationInput
}

func (s *fakeFrontendTrustStore) FrontendGrant(context.Context, string, string, string) (FrontendTrustGrant, error) {
	if s.grant.ID == 0 || s.grant.RevokedAt != nil {
		return FrontendTrustGrant{}, ErrFrontendGrantNotFound
	}
	return s.grant, nil
}

func (s *fakeFrontendTrustStore) LiveFrontendGrants(context.Context, string) ([]FrontendTrustGrant, error) {
	if len(s.all) > 0 {
		return append([]FrontendTrustGrant(nil), s.all...), nil
	}
	if s.grant.ID != 0 && s.grant.RevokedAt == nil {
		return []FrontendTrustGrant{s.grant}, nil
	}
	return nil, nil
}

func (s *fakeFrontendTrustStore) CreateFrontendGrant(_ context.Context, input FrontendTrustGrantInput) (FrontendTrustGrant, error) {
	s.created = input
	s.grant = FrontendTrustGrant{
		ID: 1, ExtensionID: input.ExtensionID, ExtensionVersion: input.ExtensionVersion,
		PackageDigest: input.PackageDigest, AdminFrontendDigest: input.AdminFrontendDigest,
		APIVersion: input.APIVersion, ComponentIDs: append([]string(nil), input.ComponentIDs...), GrantedByUserID: input.GrantedByUserID,
	}
	return s.grant, nil
}

func (s *fakeFrontendTrustStore) RevokeFrontendGrant(_ context.Context, input FrontendRevocationInput) (FrontendTrustGrant, error) {
	s.revoked = input
	now := time.Now()
	s.grant.RevokedAt = &now
	return s.grant, nil
}

func (s *fakeFrontendTrustStore) RevokeAllFrontendGrants(context.Context, string, int64) error {
	return nil
}
