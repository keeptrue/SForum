package extensions

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestExecutableTrustChallengeBindsExactImpactAndIsOneUse(t *testing.T) {
	extension := exactTrustExtension(t, "demo.trusted")
	extensions := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	trustStore := &memoryExecutableTrustStore{now: func() time.Time { return now }}
	service := NewExecutableTrustService(extensions, trustStore)
	service.now = func() time.Time { return now }
	service.random = bytes.NewReader(bytes.Repeat([]byte{0x2a}, 32))

	challenge, err := service.Challenge(context.Background(), extensionManager(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Token == "" || challenge.Impact.Digest == "" || challenge.Impact.PackageDigest != extension.PackageDigest {
		t.Fatalf("incomplete challenge: %#v", challenge)
	}
	if challenge.Impact.ArtifactDigests["backend"] == "" || challenge.Impact.ArtifactDigests["migration:migrations/001.sql"] == "" {
		t.Fatalf("executable digests missing: %#v", challenge.Impact.ArtifactDigests)
	}
	if trustStore.challenge.TokenHash == "" || trustStore.challenge.TokenHash == challenge.Token {
		t.Fatal("store must persist only the token hash")
	}
	if err := service.ConfirmEnable(context.Background(), extensionManager(), extension, ""); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("missing token: %v", err)
	}

	otherSuperAdmin := extensionManager()
	otherSuperAdmin.ID = 99
	if err := service.ConfirmEnable(context.Background(), otherSuperAdmin, extension, challenge.Token); !errors.Is(err, ErrTrustChallengeInvalid) {
		t.Fatalf("wrong actor: %v", err)
	}
	if err := service.ConfirmEnable(context.Background(), extensionManager(), extension, challenge.Token); err != nil {
		t.Fatal(err)
	}
	if err := trustStore.RevokeAll(context.Background(), extension.ID, extensionManager().ID, "test"); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmEnable(context.Background(), extensionManager(), extension, challenge.Token); !errors.Is(err, ErrTrustChallengeReplayed) {
		t.Fatalf("replayed token: %v", err)
	}
}

func TestExecutableTrustRuntimeIdentityUsesExactLiveGrant(t *testing.T) {
	extension := exactTrustExtension(t, "demo.runtime-identity")
	store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	trustStore := &memoryExecutableTrustStore{}
	service := NewExecutableTrustService(store, trustStore)

	if _, err := service.RuntimeIdentity(context.Background(), extension); !errors.Is(err, ErrTrustGrantNotFound) {
		t.Fatalf("untrusted runtime identity: %v", err)
	}
	challenge, err := service.Challenge(context.Background(), extensionManager(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmEnable(context.Background(), extensionManager(), extension, challenge.Token); err != nil {
		t.Fatal(err)
	}
	identity, err := service.RuntimeIdentity(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if identity.TrustGrantID != "1" || identity.ImpactDigest == "" {
		t.Fatalf("unexpected runtime identity: %#v", identity)
	}

	builtin := extension
	builtin.Source = SourceBuiltin
	builtinIdentity, err := service.RuntimeIdentity(context.Background(), builtin)
	if err != nil {
		t.Fatal(err)
	}
	if builtinIdentity.TrustGrantID != "builtin" || builtinIdentity.ImpactDigest == "" {
		t.Fatalf("unexpected builtin identity: %#v", builtinIdentity)
	}
}

func TestDigestInstalledFileStreamsLargeArtifact(t *testing.T) {
	extension := exactTrustExtension(t, "demo.streaming-digest")
	const relative = "backend/large-plugin"
	target := filepath.Join(extension.PackagePath, filepath.FromSlash(relative))
	file, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(32 * 1024 * 1024); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	digest, err := digestInstalledFile(extension, relative)
	runtime.ReadMemStats(&after)
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4*1024*1024 {
		t.Fatalf("streaming a 32 MiB artifact allocated %d bytes", allocated)
	}
}

func TestExecutableTrustReviewTargetsStagedArtifactWithoutInvalidatingActiveGrant(t *testing.T) {
	active := exactTrustExtension(t, "demo.staged-trust")
	store := &fakeExtensionStore{items: map[string]Extension{active.ID: active}}
	trustStore := &memoryExecutableTrustStore{}
	service := NewExecutableTrustService(store, trustStore)
	actor := extensionManager()

	challenge, err := service.Challenge(context.Background(), actor, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmEnable(context.Background(), actor, active, challenge.Token); err != nil {
		t.Fatal(err)
	}
	trusted, err := service.TrustedArtifact(context.Background(), active)
	if err != nil || !trusted {
		t.Fatalf("active artifact grant trusted=%t err=%v", trusted, err)
	}

	candidate := exactTrustExtension(t, active.ID)
	candidate.Version = "2.0.0"
	candidate.Manifest.Version = candidate.Version
	if err := writeManifest(candidate.PackagePath, candidate.Manifest); err != nil {
		t.Fatal(err)
	}
	candidate.PackageDigest, err = extensionpackage.DigestTree(candidate.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	active.StagedVersion = &ExtensionVersion{
		ID: 2, Version: candidate.Version, Manifest: candidate.Manifest,
		PackageDigest: candidate.PackageDigest, AdminFrontendDigest: candidate.AdminFrontendDigest,
		PackagePath: candidate.PackagePath, InstalledAt: candidate.InstalledAt,
	}
	store.items[active.ID] = active

	status, err := service.Status(context.Background(), actor, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Impact.ExtensionVersion != candidate.Version || status.Impact.PackageDigest != candidate.PackageDigest || status.Trusted {
		t.Fatalf("staged trust status did not bind candidate: %#v", status)
	}
	stagedChallenge, err := service.Challenge(context.Background(), actor, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stagedChallenge.Impact.ExtensionVersion != candidate.Version || stagedChallenge.Impact.PackageDigest != candidate.PackageDigest {
		t.Fatalf("staged challenge did not bind candidate: %#v", stagedChallenge.Impact)
	}
	trusted, err = service.TrustedArtifact(context.Background(), active)
	if err != nil || !trusted {
		t.Fatalf("staging candidate invalidated active grant: trusted=%t err=%v", trusted, err)
	}
}

func TestExecutableTrustExplicitStagedTargetRecoversDisabledPlugin(t *testing.T) {
	active := exactTrustExtension(t, "demo.disabled-staged-trust")
	active.Status = StatusDisabled
	active.ActiveVersionID = 1

	candidate := exactTrustExtension(t, active.ID)
	candidate.Version = "2.0.0"
	candidate.Manifest.Version = candidate.Version
	candidate.ActiveVersionID = 2
	if err := writeManifest(candidate.PackagePath, candidate.Manifest); err != nil {
		t.Fatal(err)
	}
	var err error
	candidate.PackageDigest, err = extensionpackage.DigestTree(candidate.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	active.StagedVersion = &ExtensionVersion{
		ID: candidate.ActiveVersionID, Version: candidate.Version, Manifest: candidate.Manifest,
		PackageDigest: candidate.PackageDigest, AdminFrontendDigest: candidate.AdminFrontendDigest,
		PackagePath: candidate.PackagePath, InstalledAt: candidate.InstalledAt,
	}

	store := &fakeExtensionStore{items: map[string]Extension{active.ID: active}}
	trustStore := &memoryExecutableTrustStore{}
	service := NewExecutableTrustService(store, trustStore)
	actor := extensionManager()

	currentStatus, err := service.Status(context.Background(), actor, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if currentStatus.Impact.ExtensionVersion != active.Version ||
		currentStatus.Impact.PackageDigest != active.PackageDigest {
		t.Fatalf("default disabled trust target=%#v", currentStatus.Impact)
	}

	stagedStatus, err := service.StatusForStaged(context.Background(), actor, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stagedStatus.Impact.ExtensionVersion != candidate.Version ||
		stagedStatus.Impact.PackageDigest != candidate.PackageDigest {
		t.Fatalf("explicit staged trust target=%#v", stagedStatus.Impact)
	}

	challenge, err := service.ChallengeForStaged(context.Background(), actor, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := service.ConfirmLifecycleAuthority(context.Background(), actor, candidate, challenge.Token)
	if err != nil {
		t.Fatal(err)
	}
	if authority.Grant == nil || authority.Impact.ExtensionVersion != candidate.Version ||
		authority.Impact.PackageDigest != candidate.PackageDigest {
		t.Fatalf("staged challenge authority=%#v", authority)
	}
}

func TestExecutableTrustAuditsChallengeDeniedGrantAndRevoke(t *testing.T) {
	extension := exactTrustExtension(t, "demo.audit")
	store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	auditor := &recordingAuditor{}
	service := NewExecutableTrustService(store, &memoryExecutableTrustStore{}).WithAuditor(auditor)
	actor := extensionManager()

	challenge, err := service.Challenge(context.Background(), actor, extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := actor
	other.ID++
	if err := service.ConfirmEnable(context.Background(), other, extension, challenge.Token); !errors.Is(err, ErrTrustChallengeInvalid) {
		t.Fatalf("wrong actor must be denied, got %v", err)
	}
	if err := service.ConfirmEnable(context.Background(), actor, extension, challenge.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Revoke(context.Background(), actor, extension.ID); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{
		audit.ActionExtensionTrustChallenge,
		audit.ActionExtensionTrustDenied,
		audit.ActionExtensionTrustGrant,
		audit.ActionExtensionTrustRevoke,
	} {
		if !auditor.hasAction(action) {
			t.Fatalf("missing audit action %s", action)
		}
	}
}

func TestExecutableTrustRevokeClosesOnlyTrustRequiredRuntime(t *testing.T) {
	uploaded := exactTrustExtension(t, "demo.runtime-revoke")
	builtin := exactTrustExtension(t, "demo.runtime-builtin")
	builtin.Source = SourceBuiltin
	inert := exactTrustExtension(t, "demo.runtime-inert")
	inert.Manifest.Backend = ManifestBackend{}
	inert.Manifest.Migrations = nil
	if RequiresExecutableTrust(inert) {
		t.Fatal("inert regression fixture unexpectedly requires current executable trust")
	}
	store := &fakeExtensionStore{items: map[string]Extension{
		uploaded.ID: uploaded,
		builtin.ID:  builtin,
		inert.ID:    inert,
	}}
	sink := &recordingExecutableTrustRevocationSink{}
	trustStore := &memoryExecutableTrustStore{}
	service := NewExecutableTrustService(store, trustStore).WithRevocationSink(sink)
	actor := extensionManager()

	if _, err := service.Revoke(context.Background(), actor, uploaded.ID); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 1 || sink.calls[0] != uploaded.ID+":operator_revoked" {
		t.Fatalf("runtime revoke calls=%#v", sink.calls)
	}
	if _, err := service.Revoke(context.Background(), actor, builtin.ID); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("builtin trust root was quarantined: %#v", sink.calls)
	}
	if err := service.RevokeAllForExtension(context.Background(), builtin.ID, actor.ID, "builtin_cleanup"); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 1 || trustStore.revokeAllCalls != 1 {
		t.Fatalf("builtin full revoke crossed trust domain: calls=%#v durable=%d", sink.calls, trustStore.revokeAllCalls)
	}
	if err := service.RevokeAllForExtension(context.Background(), inert.ID, actor.ID, "historical_cleanup"); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 2 || sink.calls[1] != inert.ID+":historical_cleanup" {
		t.Fatalf("historical uploaded grant cleanup was skipped: %#v", sink.calls)
	}

	sink.err = errors.New("runtime fence failed")
	if err := service.RevokeAllForExtension(context.Background(), uploaded.ID, actor.ID, "package_changed"); !errors.Is(err, sink.err) {
		t.Fatalf("runtime fence error=%v", err)
	}
}

func TestExecutableTrustRevokeAuditsUnknownAndLocalClosureFailure(t *testing.T) {
	extension := exactTrustExtension(t, "demo.revoke-audit-failure")
	store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	actor := extensionManager()
	for _, test := range []struct {
		name       string
		durableErr error
		localErr   error
		outcome    string
	}{
		{
			name: "unknown commit", durableErr: errors.Join(
				ErrTrustRevocationCommitUnknown, errors.New("commit response lost"),
			), outcome: "unknown",
		},
		{name: "local closure", localErr: errors.New("runtime closure failed"), outcome: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			auditor := &recordingAuditor{}
			trustStore := &memoryExecutableTrustStore{revokeAllErr: test.durableErr}
			sink := &recordingExecutableTrustRevocationSink{afterErr: test.localErr}
			service := NewExecutableTrustService(store, trustStore).
				WithAuditor(auditor).
				WithRevocationSink(sink)
			_, err := service.Revoke(t.Context(), actor, extension.ID)
			if test.durableErr != nil && !errors.Is(err, test.durableErr) {
				t.Fatalf("revoke error=%v missing durable=%v", err, test.durableErr)
			}
			if test.localErr != nil && !errors.Is(err, test.localErr) {
				t.Fatalf("revoke error=%v missing local=%v", err, test.localErr)
			}
			var event *audit.Event
			for index := range auditor.events {
				if auditor.events[index].Action == audit.ActionExtensionTrustRevoke {
					event = &auditor.events[index]
				}
			}
			if event == nil || event.Metadata["outcome"] != test.outcome || event.Metadata["succeeded"] != false {
				t.Fatalf("revoke audit=%#v", event)
			}
		})
	}
}

func TestExecutableTrustRevokeAllCleansUpWithoutArtifactLookup(t *testing.T) {
	trustStore := &memoryExecutableTrustStore{}
	sink := &recordingExecutableTrustRevocationSink{}
	service := NewExecutableTrustService(&fakeExtensionStore{items: map[string]Extension{}}, trustStore).
		WithRevocationSink(sink)
	err := service.RevokeAllForExtension(context.Background(), "missing.runtime", 1, "package_changed")
	if err != nil {
		t.Fatalf("orphan trust cleanup error=%v", err)
	}
	if trustStore.revokeAllCalls != 1 || sink.durableCalls != 1 ||
		len(sink.calls) != 1 || sink.calls[0] != "missing.runtime:package_changed" {
		t.Fatalf(
			"orphan cleanup durable=%d sink durable=%d calls=%#v",
			trustStore.revokeAllCalls, sink.durableCalls, sink.calls,
		)
	}
}

func TestExecutableTrustChallengeRejectsExpiredAndChangedImpact(t *testing.T) {
	extension := exactTrustExtension(t, "demo.expiring")
	extensions := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	trustStore := &memoryExecutableTrustStore{now: func() time.Time { return now }}
	service := NewExecutableTrustService(extensions, trustStore)
	service.now = func() time.Time { return now }

	challenge, err := service.Challenge(context.Background(), extensionManager(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(DefaultTrustChallengeTTL + time.Second)
	if err := service.ConfirmEnable(context.Background(), extensionManager(), extension, challenge.Token); !errors.Is(err, ErrTrustChallengeExpired) {
		t.Fatalf("expired token: %v", err)
	}

	now = now.Add(time.Second)
	challenge, err = service.Challenge(context.Background(), extensionManager(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := extension
	changed.Manifest.Routes = append(changed.Manifest.Routes, ManifestRoute{Path: "/changed", Methods: []string{"POST"}, Access: RouteAccessLogin})
	if err := service.ConfirmEnable(context.Background(), extensionManager(), changed, challenge.Token); !errors.Is(err, ErrTrustChallengeStale) {
		t.Fatalf("changed impact: %v", err)
	}
}

func TestExecutableTrustChallengeRejectsEveryExecutableDigestChange(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *Extension)
		change  func(*testing.T, *Extension)
	}{
		{
			name: "backend bytes and package digest",
			change: func(t *testing.T, extension *Extension) {
				writeTrustFile(t, extension, "backend/plugin", "changed-plugin-binary", 0o755)
				refreshTrustDeclaredFileDigest(t, extension, "backend/plugin")
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "migration bytes",
			change: func(t *testing.T, extension *Extension) {
				writeTrustFile(t, extension, "migrations/001.sql", "SELECT 2;", 0o600)
				refreshTrustDeclaredFileDigest(t, extension, "migrations/001.sql")
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "migration declaration",
			change: func(t *testing.T, extension *Extension) {
				writeTrustFile(t, extension, "migrations/002.sql", "SELECT 2;", 0o600)
				const digest = "8e7003d62f9d8cbd28da2f243bb0d215bfd4622c716be09be89a8764d9f4c7cb"
				extension.Manifest.Migrations = append(extension.Manifest.Migrations, ManifestMigration{
					ID: extension.ID + ".migration.second", ContractVersion: extension.ID + ".migration.second@1",
					Path: "migrations/002.sql", Digest: digest, Transaction: "required",
				})
				extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, ManifestPackageFile{
					ID: extension.ID + ".file.migration-second", Kind: "migration", Path: "migrations/002.sql", Digest: digest,
				})
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "admin frontend bytes",
			prepare: func(t *testing.T, extension *Extension) {
				addTrustAdminFrontend(t, extension)
			},
			change: func(t *testing.T, extension *Extension) {
				writeTrustFile(t, extension, "frontend/admin/dist/settings.mjs", "export function mount() { return () => {} }\n", 0o600)
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "admin frontend contract",
			prepare: func(t *testing.T, extension *Extension) {
				addTrustAdminFrontend(t, extension)
			},
			change: func(t *testing.T, extension *Extension) {
				extension.Manifest.SettingsDocument.UI.Component.APIVersion = 2
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "requested network authority",
			change: func(t *testing.T, extension *Extension) {
				extension.Manifest.Capabilities = nil
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "permission declaration",
			change: func(t *testing.T, extension *Extension) {
				extension.Manifest.Permissions = append(extension.Manifest.Permissions, "demo.use")
				refreshTrustPackageIdentity(t, extension)
			},
		},
		{
			name: "required feature declaration",
			change: func(t *testing.T, extension *Extension) {
				extension.Manifest.RequiresFeatures = append(extension.Manifest.RequiresFeatures, "features.demo")
				refreshTrustPackageIdentity(t, extension)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := exactTrustExtension(t, "demo.changed")
			if test.prepare != nil {
				test.prepare(t, &extension)
			}
			store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
			trustStore := &memoryExecutableTrustStore{}
			service := NewExecutableTrustService(store, trustStore)
			challenge, err := service.Challenge(context.Background(), extensionManager(), extension.ID)
			if err != nil {
				t.Fatal(err)
			}

			test.change(t, &extension)
			if err := service.ConfirmEnable(context.Background(), extensionManager(), extension, challenge.Token); !errors.Is(err, ErrTrustChallengeStale) {
				t.Fatalf("changed exact artifact must be stale, got %v", err)
			}
		})
	}
}

func TestCanonicalTrustImpactDigestBindsAuthorityContractsAndDependencies(t *testing.T) {
	base, err := buildTrustImpact(exactTrustExtension(t, "demo.contracts"), TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*TrustImpact)
	}{
		{name: "raw request authority", change: func(impact *TrustImpact) { impact.RequestedAuthority.RawRequest = true }},
		{name: "raw core database authority", change: func(impact *TrustImpact) { impact.RequestedAuthority.RawCoreDatabase = true }},
		{name: "host contract", change: func(impact *TrustImpact) { impact.Contracts.HostAPI = "sforum.host/v2" }},
		{name: "frontend contract", change: func(impact *TrustImpact) { impact.Contracts.FrontendAPI = "sforum.component/v2" }},
		{name: "dependency", change: func(impact *TrustImpact) {
			impact.Dependencies = []ManifestDependency{{ID: "demo.parent", Version: "^2.0.0", Kind: "required"}}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			digest, err := canonicalTrustImpactDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base.Digest {
				t.Fatalf("%s change did not invalidate impact digest", test.name)
			}
		})
	}
}

func TestManifestV3TrustImpactIncludesEveryDeclarationAndExecutableDigest(t *testing.T) {
	extension := completeV3TrustExtension(t, "demo.v3-trust")
	addTrustQueryRuntimeContract(t, &extension)
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	if impact.SchemaVersion != TrustImpactSchemaV2 || impact.ManifestContract != "sforum.manifest@3" || impact.Contracts.HostAPI != "sforum.host-api@2" {
		t.Fatalf("incorrect V3 contracts: %#v", impact)
	}
	if impact.ArtifactDigests["guard:demo.v3-trust.guard.raw"] == "" || impact.ArtifactDigests["l2:demo.v3-trust.file.l2"] == "" {
		t.Fatalf("custom guard or L2 digest missing: %#v", impact.ArtifactDigests)
	}
	if !impact.RequestedAuthority.RawRequest || !impact.RequestedAuthority.RawCoreDatabase || !RequiresExecutableTrust(extension) {
		t.Fatalf("high-risk authority was not derived from V3 declarations: %#v", impact.RequestedAuthority)
	}
	wantDatabaseGrants := []string{
		extensionmanifest.DatabaseGrantOwnSchema,
		extensionmanifest.DatabaseGrantCoreViews,
		extensionmanifest.DatabaseGrantHostCommands,
		extensionmanifest.DatabaseGrantRawCore,
	}
	if impact.Database == nil || impact.Database.Authority != "" || !reflect.DeepEqual(impact.Database.Grants, wantDatabaseGrants) {
		t.Fatalf("database authority was not normalized into exact grants: %#v", impact.Database)
	}
	if len(impact.GuardDeclarations) != 1 || len(impact.MigrationDeclarations) != 1 || len(impact.Schedules) != 1 ||
		len(impact.RegistryComponents) != 1 || len(impact.Templates) != 1 || len(impact.Assets) != 1 || len(impact.Content) != 1 ||
		impact.Database == nil || len(impact.Cache) != 1 || len(impact.SEO) != 1 || len(impact.Services) != 1 || len(impact.Commands) != 1 ||
		len(impact.AdminSurfaces) != 1 || len(impact.Queries) != 1 || len(impact.QueryResultFilters) != 1 || impact.Identity == nil || len(impact.PermissionDefinitions) != 1 ||
		len(impact.Media) != 1 || len(impact.Navigation) != 1 || len(impact.Regions) != 1 || len(impact.Dependencies) != 1 ||
		impact.Lifecycle == nil || len(impact.OpenAPI) != 1 || len(impact.PackageFiles) != 10 {
		t.Fatalf("incomplete V3 trust impact: %#v", impact)
	}
}

func TestTrustImpactDatabaseGrantSetIsCanonicalAndDigestBound(t *testing.T) {
	extension := completeV3TrustExtension(t, "demo.database-grants")
	extension.Manifest.Database = &ManifestDatabase{
		ContractVersion: extension.ID + ".database@1",
		Grants: []string{
			extensionmanifest.DatabaseGrantRawCore,
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
		},
		CoreCompatibility: ">=1.0.0 <2.0.0",
		Backup:            extensionmanifest.ManifestBackupPolicy{Required: true, Strategy: "operator_snapshot"},
		Retention:         extensionmanifest.ManifestRetention{OnDisable: "retain", OnUninstall: "retain"},
	}
	base, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		extensionmanifest.DatabaseGrantOwnSchema,
		extensionmanifest.DatabaseGrantCoreViews,
		extensionmanifest.DatabaseGrantRawCore,
	}
	if base.Database == nil || !reflect.DeepEqual(base.Database.Grants, want) || !base.RequestedAuthority.RawCoreDatabase {
		t.Fatalf("canonical trust database grants = %#v", base.Database)
	}

	changed := base
	database := *base.Database
	database.Grants = append([]string(nil), database.Grants...)
	database.Grants = append(database.Grants, extensionmanifest.DatabaseGrantHostCommands)
	database.Grants = extensionmanifest.DatabaseGrants(&database)
	changed.Database = &database
	digest, err := canonicalTrustImpactDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if digest == base.Digest {
		t.Fatal("adding one exact database grant did not invalidate trust impact")
	}
}

func TestManifestV3EveryDeclarationInvalidatesCanonicalTrustImpact(t *testing.T) {
	extension := completeV3TrustExtension(t, "demo.v3-digest")
	addTrustQueryRuntimeContract(t, &extension)
	base, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*TrustImpact)
	}{
		{name: "manifest contract", change: func(impact *TrustImpact) { impact.ManifestContract = "sforum.manifest@4" }},
		{name: "backend", change: func(impact *TrustImpact) { impact.Backend.HostAPIVersion = "sforum.host-api@3" }},
		{name: "guard declaration", change: func(impact *TrustImpact) { impact.GuardDeclarations = nil }},
		{name: "migration declaration", change: func(impact *TrustImpact) { impact.MigrationDeclarations = nil }},
		{name: "schedule", change: func(impact *TrustImpact) { impact.Schedules = nil }},
		{name: "registry component", change: func(impact *TrustImpact) { impact.RegistryComponents = nil }},
		{name: "template", change: func(impact *TrustImpact) { impact.Templates = nil }},
		{name: "asset", change: func(impact *TrustImpact) { impact.Assets = nil }},
		{name: "content", change: func(impact *TrustImpact) { impact.Content = nil }},
		{name: "database", change: func(impact *TrustImpact) { impact.Database = nil }},
		{name: "cache", change: func(impact *TrustImpact) { impact.Cache = nil }},
		{name: "seo", change: func(impact *TrustImpact) { impact.SEO = nil }},
		{name: "service", change: func(impact *TrustImpact) { impact.Services = nil }},
		{name: "command", change: func(impact *TrustImpact) { impact.Commands = nil }},
		{name: "admin surface", change: func(impact *TrustImpact) { impact.AdminSurfaces = nil }},
		{name: "query", change: func(impact *TrustImpact) { impact.Queries = nil }},
		{name: "query result filter", change: func(impact *TrustImpact) { impact.QueryResultFilters = nil }},
		{name: "identity", change: func(impact *TrustImpact) { impact.Identity = nil }},
		{name: "permission definition", change: func(impact *TrustImpact) { impact.PermissionDefinitions = nil }},
		{name: "media", change: func(impact *TrustImpact) { impact.Media = nil }},
		{name: "navigation", change: func(impact *TrustImpact) { impact.Navigation = nil }},
		{name: "region", change: func(impact *TrustImpact) { impact.Regions = nil }},
		{name: "dependency", change: func(impact *TrustImpact) { impact.Dependencies = nil }},
		{name: "lifecycle", change: func(impact *TrustImpact) { impact.Lifecycle = nil }},
		{name: "openapi", change: func(impact *TrustImpact) { impact.OpenAPI = nil }},
		{name: "package file", change: func(impact *TrustImpact) { impact.PackageFiles = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.change(&changed)
			digest, err := canonicalTrustImpactDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base.Digest {
				t.Fatalf("%s change did not invalidate impact digest", test.name)
			}
		})
	}
}

func TestEmptySEOFamilyPreservesPreSEOTrustDigestShape(t *testing.T) {
	impact, err := buildTrustImpact(exactTrustExtension(t, "demo.pre-seo"), TrustActionEnable)
	if err != nil {
		t.Fatal(err)
	}
	changed := impact
	changed.SEO = []ManifestSEO{}
	digest, err := canonicalTrustImpactDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	if digest != impact.Digest {
		t.Fatal("empty additive SEO family invalidated a package that could not declare SEO")
	}
}

func TestManifestV3DatabaseOperationFieldsInvalidateCanonicalTrustImpact(t *testing.T) {
	newImpact := func() TrustImpact {
		impact, err := buildTrustImpact(completeV3TrustExtension(t, "demo.database-operation-digest"), TrustActionEnable)
		if err != nil {
			t.Fatal(err)
		}
		impact.Database = &ManifestDatabase{
			ContractVersion: "demo.database-operation-digest.database@1", Authority: "own_schema",
			Schema: "logical_schema", Role: "logical_role",
			Operations: []extensionmanifest.ManifestDatabaseOperation{{
				ID: "demo.database-operation-digest.items.query", StatementVersion: "1", Kind: "query",
				Path: "database/items.sql", Digest: strings.Repeat("a", 64),
				Parameters: []extensionmanifest.ManifestDatabaseParameter{{
					Schema: "demo.database-operation-digest.item-id@1", Field: "item_id",
					Kind: "int64", Nullable: false, MaxBytes: 64,
				}},
				ResultSchema: "demo.database-operation-digest.items.result@1",
				Columns:      []extensionmanifest.ManifestDatabaseColumn{{Name: "item_id", Nullable: false}},
				MaxRows:      100, TimeoutMS: 1000,
			}},
		}
		impact.Digest, err = canonicalTrustImpactDigest(impact)
		if err != nil {
			t.Fatal(err)
		}
		return impact
	}
	base := newImpact()
	tests := []struct {
		name   string
		change func(*extensionmanifest.ManifestDatabaseOperation)
	}{
		{name: "id", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.ID += ".changed" }},
		{name: "statement version", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.StatementVersion = "2" }},
		{name: "kind", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Kind = "execute" }},
		{name: "path", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Path = "database/changed.sql" }},
		{name: "digest", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Digest = strings.Repeat("b", 64) }},
		{name: "parameter schema", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Parameters[0].Schema += ".changed" }},
		{name: "parameter field", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Parameters[0].Field = "changed_id" }},
		{name: "parameter kind", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Parameters[0].Kind = "string" }},
		{name: "parameter nullable", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Parameters[0].Nullable = true }},
		{name: "parameter bytes", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Parameters[0].MaxBytes++ }},
		{name: "result schema", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.ResultSchema += ".changed" }},
		{name: "result column", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Columns[0].Name = "changed_id" }},
		{name: "result nullable", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.Columns[0].Nullable = true }},
		{name: "row limit", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.MaxRows++ }},
		{name: "affected limit", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.MaxAffectedRows = 1 }},
		{name: "query invalidation tags", change: func(value *extensionmanifest.ManifestDatabaseOperation) {
			value.QueryInvalidationTags = []string{"demo.database-operation-digest.items"}
		}},
		{name: "timeout", change: func(value *extensionmanifest.ManifestDatabaseOperation) { value.TimeoutMS++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := newImpact()
			test.change(&changed.Database.Operations[0])
			digest, err := canonicalTrustImpactDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base.Digest {
				t.Fatalf("database operation %s change did not invalidate trust impact", test.name)
			}
		})
	}
}

func TestManifestV3HighRiskDeclarationsRequireExactArtifactTrust(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Manifest)
	}{
		{name: "custom guard", change: func(manifest *Manifest) { manifest.Guards = []ManifestGuard{{Kind: "custom"}} }},
		{name: "raw route", change: func(manifest *Manifest) { manifest.Routes = []ManifestRoute{{Guard: extensionmanifest.GuardCoreRaw}} }},
		{name: "raw database", change: func(manifest *Manifest) { manifest.Database = &ManifestDatabase{Authority: "raw_core"} }},
		{name: "kernel database", change: func(manifest *Manifest) { manifest.Database = &ManifestDatabase{Authority: "kernel"} }},
		{name: "L2 component", change: func(manifest *Manifest) { manifest.Components = []ManifestComponent{{L2Component: "demo.file"}} }},
		{name: "lifecycle execute", change: func(manifest *Manifest) {
			manifest.Lifecycle = &ManifestLifecycle{Enable: &extensionmanifest.ManifestLifecycleOperation{Execute: "lifecycle.enable"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extension := Extension{Source: SourceUploaded}
			test.change(&extension.Manifest)
			if !RequiresExecutableTrust(extension) {
				t.Fatal("high-risk V3 declaration did not require exact-artifact trust")
			}
		})
	}
}

func TestManifestV3TrustRejectsChangedL2BytesAgainstDeclaration(t *testing.T) {
	extension := completeV3TrustExtension(t, "demo.v3-l2-change")
	writeTrustFile(t, &extension, "frontend/card.mjs", "export function mount() { throw new Error('changed') }\n", 0o600)
	refreshTrustPackageIdentity(t, &extension)
	if _, err := buildTrustImpact(extension, TrustActionEnable); !errors.Is(err, ErrFrontendPackageChanged) {
		t.Fatalf("changed L2 bytes must fail exact-artifact impact generation, got %v", err)
	}
}

func TestServiceV3TrustAllowsAlreadyGrantedDelegatedEnable(t *testing.T) {
	extension := exactTrustExtension(t, "demo.enable")
	store := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	trustStore := &memoryExecutableTrustStore{now: func() time.Time { return time.Now().UTC() }}
	trust := NewExecutableTrustService(store, trustStore)
	service := NewServiceWithOptions(store, t.TempDir(), "", &fakeRuntimeManager{}, WithExecutableTrust(trust, true))

	if _, err := service.Enable(context.Background(), extensionManager(), extension.ID, EnableInput{}); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("enable without challenge: %v", err)
	}
	challenge, err := service.IssueExecutableTrustChallenge(context.Background(), extensionManager(), extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(context.Background(), extensionManager(), extension.ID, EnableInput{ConfirmationToken: challenge.Token}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Disable(context.Background(), techAdminPluginManager(), extension.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(context.Background(), techAdminPluginManager(), extension.ID, EnableInput{}); err != nil {
		t.Fatalf("delegated manager should operate an already trusted digest: %v", err)
	}
}

func TestV3StaticInstallByDelegatedManagerDoesNotExecutePackage(t *testing.T) {
	store := &fakeExtensionStore{items: map[string]Extension{}}
	runtime := &countingRuntimeManager{}
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	service := NewServiceWithOptions(store, t.TempDir(), "", runtime, WithExecutableTrust(trust, true))
	archive := extensionArchive(t, validManifest("delegated.backend", TypePlugin),
		zipFile{name: "backend/plugin", body: "#!/bin/sh\n", mode: 0o755},
		zipFile{name: "migrations/001_init.sql", body: "SELECT 1;", mode: 0o644},
	)

	result, err := service.InstallOrUpgradeArchive(context.Background(), techAdminPluginManager(), ArchiveInput{FileName: "plugin.zip", Data: archive})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extension.Status != StatusInstalled || runtime.checks != 0 || runtime.starts != 0 {
		t.Fatalf("static install executed package code: result=%#v checks=%d starts=%d", result, runtime.checks, runtime.starts)
	}
	status, err := service.ExecutableTrustStatus(context.Background(), techAdminPluginManager(), result.Extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.TrustRequired || status.Trusted || len(status.Impact.Binaries) != 1 || len(status.Impact.Migrations) != 1 ||
		len(status.Impact.Routes) != 1 || len(status.Impact.Jobs) != 1 || status.Impact.ArtifactDigests["backend"] == "" ||
		status.Impact.ArtifactDigests["migration:migrations/001_init.sql"] == "" {
		t.Fatalf("delegated manager received incomplete static impact preview: %#v", status)
	}
	if runtime.checks != 0 || runtime.starts != 0 {
		t.Fatalf("impact preview executed package code: checks=%d starts=%d", runtime.checks, runtime.starts)
	}
}

func addTrustQueryRuntimeContract(t *testing.T, extension *Extension) {
	t.Helper()
	const schemaPath = "schemas/query-items-result.json"
	writeTrustFile(t, extension, schemaPath,
		`{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}},"additionalProperties":false}`,
		0o600,
	)
	digest, err := digestInstalledFile(*extension, schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	query := &extension.Manifest.Queries[0]
	query.Sort = []string{"id"}
	query.Handler = extension.ID + ".query.items.execute"
	query.IdentityFields = []string{"id"}
	query.DefaultSort = []ManifestQuerySort{{Field: "id"}}
	extension.Manifest.QueryResultFilters = []ManifestQueryResultFilter{{
		ID: extension.ID + ".query.items.decorate", ContractVersion: extension.ID + ".query.items.decorate@1",
		QueryID: query.ID, QueryContractVersion: query.ContractVersion, QueryPlanVersion: query.PlanVersion,
		Handler: extension.ID + ".query.items.decorate", FailurePolicy: extensionmanifest.QueryResultFilterFailureFailClosed,
		TimeoutMS: extensionmanifest.ManifestQueryResultFilterDefaultTimeoutMS,
	}}
	extension.Manifest.PackageFiles = append(extension.Manifest.PackageFiles, ManifestPackageFile{
		ID: extension.ID + ".query.items.result", Kind: "schema", Path: schemaPath, Digest: digest, Version: "1",
	})
	refreshTrustPackageIdentity(t, extension)
}

func completeV3TrustExtension(t *testing.T, id string) Extension {
	t.Helper()
	item := exactTrustExtension(t, id)
	for relative, body := range map[string]string{
		"backend/guard":            "guard-binary",
		"frontend/card.mjs":        "export function mount() {}\n",
		"frontend/card.css":        ".card {}\n",
		"templates/card.html":      "<article></article>\n",
		"openapi/routes.yaml":      "openapi: 3.1.0\n",
		"schemas/card-props.json":  "{\"type\":\"object\"}\n",
		"schemas/card-result.json": "{\"type\":\"object\"}\n",
	} {
		writeTrustFile(t, &item, relative, body, 0o600)
	}
	mustDigest := func(relative string) string {
		digest, err := digestInstalledFile(item, relative)
		if err != nil {
			t.Fatal(err)
		}
		return digest
	}
	backendDigest := mustDigest("backend/plugin")
	guardDigest := mustDigest("backend/guard")
	migrationDigest := mustDigest("migrations/001.sql")
	l2Digest := mustDigest("frontend/card.mjs")
	assetDigest := mustDigest("frontend/card.css")
	templateDigest := mustDigest("templates/card.html")
	openAPIDigest := mustDigest("openapi/routes.yaml")
	// 组件 props/result schema 必须有同包精确声明（与 provider 同一规则）。
	cardPropsDigest := mustDigest("schemas/card-props.json")
	cardResultDigest := mustDigest("schemas/card-result.json")

	item.Manifest.ManifestVersion = extensionmanifest.ManifestVersionV3
	item.Manifest.Backend = ManifestBackend{
		Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
		Digest: backendDigest, HostAPIVersion: "sforum.host-api@2",
	}
	item.Manifest.Permissions = []string{id + ".manage"}
	item.Manifest.Migrations = []ManifestMigration{{
		ID: id + ".migration.initial", ContractVersion: id + ".migration.initial@1",
		Path: "migrations/001.sql", Digest: migrationDigest, Transaction: "required",
	}}
	item.Manifest.Guards = []ManifestGuard{{
		ID: id + ".guard.raw", ContractVersion: id + ".guard.raw@1", Kind: "raw_request",
		Entry: "backend/guard", Digest: guardDigest, Permissions: []string{id + ".manage"},
	}}
	item.Manifest.Routes = []ManifestRoute{{
		ID: id + ".route.run", ContractVersion: id + ".route.run@1", Action: "add",
		// 决定 R1（2026-10-16）：raw 权限来自本 helper 已声明的 kind=raw_request 守卫，
		// 不再引用已退役的 core.guard.raw_request。
		Path: "/api/demo/run", Methods: []string{"POST"}, Guard: id + ".guard.raw",
		Fallback: "closed", Mode: "http", Handler: "route.run", RequestSchema: id + ".route.run.request@1",
		ResponseSchema: id + ".route.run.response@1",
	}}
	item.Manifest.Hooks = []ManifestHook{{
		ID: id + ".hook.before", ContractVersion: id + ".hook.before@1", Name: "topic.before_create",
		Kind: "filter", Handler: "hook.before", InputSchema: id + ".hook.before.input@1", ResultSchema: id + ".hook.before.result@1",
	}}
	item.Manifest.Events = []ManifestEvent{{
		ID: id + ".event.created", ContractVersion: id + ".event.created@1", Name: "topic.created",
		Kind: "observe", Handler: "event.created", InputSchema: id + ".event.created.input@1",
	}}
	item.Manifest.Jobs = []ManifestJob{{
		ID: id + ".job.refresh", ContractVersion: id + ".job.refresh@1", Name: id + ".refresh",
		Handler: "job.refresh", PayloadSchema: id + ".job.refresh.payload@1", RetryPolicy: "bounded",
	}}
	item.Manifest.Schedules = []ManifestSchedule{{
		ID: id + ".schedule.refresh", ContractVersion: id + ".schedule.refresh@1",
		JobID: id + ".job.refresh", Cron: "0 * * * *", Timezone: "UTC",
	}}
	item.Manifest.Providers = []ManifestProvider{{
		ID: id + ".provider.search", ContractVersion: id + ".provider.search@1", Slot: id + ".search",
		Label: "Search", Handler: "provider.search",
	}}
	item.Manifest.Templates = []ManifestTemplate{{
		ID: id + ".template.card", ContractVersion: id + ".template.card@1", Action: "add",
		Path: "templates/card.html", Digest: templateDigest, ViewModelSchema: id + ".template.card.model@1",
	}}
	item.Manifest.Assets = []ManifestAsset{{
		Handle: id + ".asset.card", ContractVersion: id + ".asset.card@1", Type: "style",
		Path: "frontend/card.css", Digest: assetDigest, Loading: "defer",
	}}
	item.Manifest.Components = []ManifestComponent{{
		ID: id + ".component.card", ContractVersion: id + ".component.card@1", Action: "add",
		L2Component: id + ".file.l2", PropsSchema: id + ".component.card.props@1",
		ResultSchema: id + ".component.card.result@1",
	}}
	item.Manifest.Content = []ManifestContent{{
		ID: id + ".content.card", ContractVersion: id + ".content.card@1", Kind: "block",
		Handler: "content.card", Schema: id + ".content.card.schema@1",
	}}
	item.Manifest.Database = &ManifestDatabase{
		ContractVersion: id + ".database@1", Authority: "raw_core", CoreCompatibility: ">=1.0.0 <2.0.0",
		Backup:    extensionmanifest.ManifestBackupPolicy{Required: true, Strategy: "operator_snapshot"},
		Retention: extensionmanifest.ManifestRetention{OnDisable: "retain", OnUninstall: "retain"},
	}
	item.Manifest.Cache = []ManifestCache{{
		ID: id + ".cache.results", ContractVersion: id + ".cache.results@1", Namespace: id + ".results", Policy: "actor",
	}}
	item.Manifest.SEO = []ManifestSEO{{
		ID: id + ".seo.topic-title", ContractVersion: id + ".seo.topic-title@1",
		Scope: "core.page.topic", Kind: "title", Action: "filter", Handler: id + ".seo.topic-title",
		FailurePolicy: "fallback", TimeoutMS: 500,
	}}
	item.Manifest.Services = []ManifestService{{
		ID: id + ".service.lookup", ContractVersion: id + ".service.lookup@1", Action: "add", Handler: "service.lookup",
		RequestSchema: id + ".service.lookup.request@1", ResponseSchema: id + ".service.lookup.response@1",
	}}
	item.Manifest.Commands = []ManifestCommand{{
		ID: id + ".command.write", ContractVersion: id + ".command.write@1", Handler: "command.write",
		Permission: id + ".manage", InputSchema: id + ".command.write.input@1", ResultSchema: id + ".command.write.result@1",
	}}
	item.Manifest.AdminSurfaces = []ManifestAdminSurface{{
		ID: id + ".admin.notice", ContractVersion: id + ".admin.notice@1", Kind: "notice", Action: "add",
		Label: "Demo", Handler: "admin.notice", Permission: id + ".manage",
	}}
	item.Manifest.Queries = []ManifestQuery{{
		ID: id + ".query.items", ContractVersion: id + ".query.items@1", Entity: id + ".item",
		PlanVersion: id + ".query.items.plan@1", Fields: []string{"id"}, Pagination: "cursor",
		ResultSchema: id + ".query.items.result@1", PermissionPolicy: id + ".manage",
	}}
	item.Manifest.Identity = &ManifestIdentity{ContractVersion: id + ".identity@1", SessionPolicy: "core.session.default"}
	item.Manifest.PermissionDefinitions = []ManifestPermissionDefinition{{
		Key: id + ".manage", ContractVersion: id + ".permission.manage@1", Label: LocalizedText{Default: "Manage demo"},
		Description: LocalizedText{Default: "Manage demo."}, AssignmentPolicy: "host",
	}}
	item.Manifest.Media = []ManifestMediaPipeline{{
		ID: id + ".media.image", ContractVersion: id + ".media.image@1", Action: "add",
		MIMEs: []string{"image/png"}, Handler: "media.image",
	}}
	item.Manifest.Navigation = []ManifestNavigation{{
		ID: id + ".navigation.link", ContractVersion: id + ".navigation.link@1", Kind: "item",
		Action: "add", Label: "Demo", Href: "/demo",
	}}
	item.Manifest.Regions = []ManifestRegion{{
		ID: id + ".region.sidebar", ContractVersion: id + ".region.sidebar@1", Action: "add",
		Kind: "widget", Label: "Sidebar",
	}}
	item.Manifest.Dependencies = []ManifestDependency{{ID: "demo.base", Version: "^1.0.0", Kind: "required"}}
	item.Manifest.Lifecycle = &ManifestLifecycle{
		ContractVersion: id + ".lifecycle@1",
		Enable: &extensionmanifest.ManifestLifecycleOperation{
			Plan: "lifecycle.enable.plan", Execute: "lifecycle.enable", ProgressSchema: id + ".lifecycle.progress@1",
			CheckpointSchema: id + ".lifecycle.checkpoint@1",
		},
	}
	item.Manifest.OpenAPI = []ManifestOpenAPIFragment{{
		ID: id + ".openapi.routes", ContractVersion: id + ".openapi.routes@1", Path: "openapi/routes.yaml",
		Digest: openAPIDigest, Namespace: id + ".api",
	}}
	item.Manifest.PackageFiles = []ManifestPackageFile{
		{ID: id + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: backendDigest},
		{ID: id + ".file.guard", Kind: "executable", Path: "backend/guard", Digest: guardDigest},
		{ID: id + ".file.migration", Kind: "migration", Path: "migrations/001.sql", Digest: migrationDigest},
		{ID: id + ".file.l2", Kind: "frontend", Path: "frontend/card.mjs", Digest: l2Digest},
		{ID: id + ".file.asset", Kind: "asset", Path: "frontend/card.css", Digest: assetDigest},
		{ID: id + ".file.template", Kind: "template", Path: "templates/card.html", Digest: templateDigest},
		{ID: id + ".file.openapi", Kind: "openapi", Path: "openapi/routes.yaml", Digest: openAPIDigest},
		{ID: id + ".component.card.props", Kind: "schema", Path: "schemas/card-props.json", Digest: cardPropsDigest, Version: "1"},
		{ID: id + ".component.card.result", Kind: "schema", Path: "schemas/card-result.json", Digest: cardResultDigest, Version: "1"},
	}
	refreshTrustPackageIdentity(t, &item)
	return item
}

func exactTrustExtension(t *testing.T, id string) Extension {
	t.Helper()
	const (
		backendDigest   = "7f512da80a46ab9883d159705be0e5467d4e2e8f03d4efc75c9165e7c7c597f6"
		migrationDigest = "17db4fd369edb9244b9f91d9aeed145c3d04ad8ba6e95d06247f07a63527d11a"
	)
	item := installedExtension(id, TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Manifest.Backend.Digest = backendDigest
	item.Source = SourceUploaded
	item.IsDeletable = true
	item.Manifest.Permissions = []string{"topic.create"}
	item.Manifest.Routes = []ManifestRoute{{
		ID: id + ".route.run", ContractVersion: id + ".route.run@1", Action: "add",
		Path: "/run", Methods: []string{"POST"}, Guard: "core.guard.permission",
		Access: RouteAccessPermission, Permission: "topic.create", Fallback: "closed", Mode: "http",
		Handler: "demo.run", RequestSchema: id + ".route.run.request@1", ResponseSchema: id + ".route.run.response@1",
	}}
	item.Manifest.Migrations = []ManifestMigration{{
		ID: id + ".migration.init", ContractVersion: id + ".migration.init@1",
		Path: "migrations/001.sql", Digest: migrationDigest, Transaction: "required",
	}}
	item.Manifest.Capabilities = []string{"net.outbound"}
	item.Manifest.PackageFiles = []ManifestPackageFile{
		{ID: id + ".file.backend", Kind: "executable", Path: "backend/plugin", Digest: backendDigest},
		{ID: id + ".file.migration", Kind: "migration", Path: "migrations/001.sql", Digest: migrationDigest},
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backend/plugin"), []byte("plugin-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "migrations/001.sql"), []byte("SELECT 1;"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(root, item.Manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	item.PackagePath = root
	item.PackageDigest = digest
	return item
}

func addTrustAdminFrontend(t *testing.T, extension *Extension) {
	t.Helper()
	component := &SettingsComponent{ID: "settings", APIVersion: 1, Entry: "frontend/admin/dist/settings.mjs"}
	extension.Manifest.Settings = []ManifestSetting{{Key: "title", Type: "text", Label: LocalizedText{Default: "Title"}}}
	extension.Manifest.SettingsDocument = SettingsDocument{
		SchemaVersion: 1,
		Explicit:      true,
		UI:            SettingsUI{Mode: "component", Layout: "form", Component: component},
		Fields:        extension.Manifest.Settings,
	}
	writeTrustFile(t, extension, component.Entry, "export function mount() {}\n", 0o600)
	refreshTrustPackageIdentity(t, extension)
}

func writeTrustFile(t *testing.T, extension *Extension, relative, body string, mode os.FileMode) {
	t.Helper()
	target := filepath.Join(extension.PackagePath, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func refreshTrustDeclaredFileDigest(t *testing.T, extension *Extension, relative string) {
	t.Helper()
	digest, err := digestInstalledFile(*extension, relative)
	if err != nil {
		t.Fatal(err)
	}
	if extension.Manifest.Backend.Entry == relative {
		extension.Manifest.Backend.Digest = digest
	}
	for index := range extension.Manifest.Migrations {
		if extension.Manifest.Migrations[index].Path == relative {
			extension.Manifest.Migrations[index].Digest = digest
		}
	}
	for index := range extension.Manifest.PackageFiles {
		if extension.Manifest.PackageFiles[index].Path == relative {
			extension.Manifest.PackageFiles[index].Digest = digest
		}
	}
}

func refreshTrustPackageIdentity(t *testing.T, extension *Extension) {
	t.Helper()
	if err := writeManifest(extension.PackagePath, extension.Manifest); err != nil {
		t.Fatal(err)
	}
	if component := extension.Manifest.SettingsDocument.UI.Component; component != nil && component.Entry != "" {
		digest, err := ComputeAdminFrontendDigest(extension.Manifest, extension.PackagePath)
		if err != nil {
			t.Fatal(err)
		}
		extension.AdminFrontendDigest = digest
	}
	digest, err := extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest = digest
}

type memoryExecutableTrustStore struct {
	mu              sync.Mutex
	now             func() time.Time
	challenge       TrustChallengeRecord
	consumed        bool
	nextGrantID     int64
	grants          map[TrustIdentity]TrustGrant
	revokedGrantIDs map[int64]bool
	revokeGrantErr  error
	revokeAllErr    error
	revokeAllCalls  int
}

func (s *memoryExecutableTrustStore) CreateChallenge(_ context.Context, input TrustChallengeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenge = input
	s.consumed = false
	return nil
}

func (s *memoryExecutableTrustStore) HasLiveGrant(_ context.Context, identity TrustIdentity) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, granted := s.grants[identity]
	return granted, nil
}

func (s *memoryExecutableTrustStore) LiveGrant(_ context.Context, identity TrustIdentity) (TrustGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, granted := s.grants[identity]
	if !granted {
		return TrustGrant{}, ErrTrustGrantNotFound
	}
	return grant, nil
}

func (s *memoryExecutableTrustStore) ConsumeChallenge(_ context.Context, input TrustConsumeInput) (TrustGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.TokenHash == "" || input.TokenHash != s.challenge.TokenHash || input.ActorUserID != s.challenge.ActorUserID {
		return TrustGrant{}, ErrTrustChallengeInvalid
	}
	if s.consumed {
		return TrustGrant{}, ErrTrustChallengeReplayed
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now()
	}
	if !now.Before(s.challenge.ExpiresAt) {
		return TrustGrant{}, ErrTrustChallengeExpired
	}
	if input.Identity != s.challenge.Identity {
		return TrustGrant{}, ErrTrustChallengeStale
	}
	s.consumed = true
	if s.grants == nil {
		s.grants = map[TrustIdentity]TrustGrant{}
	}
	if grant, granted := s.grants[input.Identity]; granted {
		return grant, nil
	}
	s.nextGrantID++
	grant := TrustGrant{
		ID: s.nextGrantID, ExtensionID: input.Identity.ExtensionID,
		ExtensionVersion: input.Identity.ExtensionVersion, PackageDigest: input.Identity.PackageDigest,
		Action: input.Identity.Action, ImpactDigest: input.Identity.ImpactDigest,
		GrantedByUserID: input.ActorUserID, GrantedAt: now, created: true,
	}
	s.grants[input.Identity] = grant
	return grant, nil
}

func (s *memoryExecutableTrustStore) EnsureLiveGrant(_ context.Context, input TrustEnsureGrantInput) (TrustGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.ActorUserID <= 0 {
		return TrustGrant{}, ErrTrustGrantNotFound
	}
	if s.grants == nil {
		s.grants = map[TrustIdentity]TrustGrant{}
	}
	if grant, granted := s.grants[input.Identity]; granted {
		return grant, nil
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now()
	}
	s.nextGrantID++
	grant := TrustGrant{
		ID: s.nextGrantID, ExtensionID: input.Identity.ExtensionID,
		ExtensionVersion: input.Identity.ExtensionVersion, PackageDigest: input.Identity.PackageDigest,
		Action: input.Identity.Action, ImpactDigest: input.Identity.ImpactDigest,
		GrantedByUserID: input.ActorUserID, GrantedAt: now, created: true,
	}
	s.grants[input.Identity] = grant
	return grant, nil
}

func (s *memoryExecutableTrustStore) revokeExactGrant(
	_ context.Context,
	grant TrustGrant,
	_ int64,
	_ string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokeGrantErr != nil {
		return s.revokeGrantErr
	}
	for identity, live := range s.grants {
		if live.ID == grant.ID && live.ExtensionID == grant.ExtensionID &&
			live.ExtensionVersion == grant.ExtensionVersion && live.PackageDigest == grant.PackageDigest &&
			live.Action == grant.Action && live.ImpactDigest == grant.ImpactDigest {
			delete(s.grants, identity)
			if s.revokedGrantIDs == nil {
				s.revokedGrantIDs = map[int64]bool{}
			}
			s.revokedGrantIDs[live.ID] = true
			return nil
		}
	}
	if s.revokedGrantIDs[grant.ID] {
		return nil
	}
	return ErrTrustGrantNotFound
}

func (s *memoryExecutableTrustStore) RevokeAll(_ context.Context, extensionID string, _ int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeAllCalls++
	for identity, grant := range s.grants {
		if identity.ExtensionID == extensionID {
			delete(s.grants, identity)
			if s.revokedGrantIDs == nil {
				s.revokedGrantIDs = map[int64]bool{}
			}
			s.revokedGrantIDs[grant.ID] = true
		}
	}
	return s.revokeAllErr
}

type countingRuntimeManager struct {
	checks int
	starts int
}

type recordingExecutableTrustRevocationSink struct {
	calls        []string
	durableCalls int
	err          error
	afterErr     error
	afterDurable func(error)
}

func (s *recordingExecutableTrustRevocationSink) RevokeExecutableTrust(
	ctx context.Context,
	extensionID string,
	reason string,
	durable func(context.Context) error,
) error {
	s.calls = append(s.calls, extensionID+":"+reason)
	if s.err != nil {
		return s.err
	}
	s.durableCalls++
	durableErr := durable(ctx)
	if s.afterDurable != nil {
		s.afterDurable(durableErr)
	}
	return errors.Join(durableErr, s.afterErr)
}

func (m *countingRuntimeManager) Check(context.Context, Extension) error {
	m.checks++
	return nil
}

func (m *countingRuntimeManager) Start(context.Context, Extension) error {
	m.starts++
	return nil
}

func (*countingRuntimeManager) Stop(context.Context, Extension) error { return nil }
func (*countingRuntimeManager) Status(context.Context, Extension) RuntimeStatus {
	return RuntimeStatus{State: RuntimeStopped}
}
func (*countingRuntimeManager) EmitHook(context.Context, string, map[string]any) {}

// TestDevAutoEnsureLiveGrantIsSuperAdminOnlyAndIdempotent 覆盖 dev 自动激活通道的
// grant 发放边界：非 super_admin 必须被拒（denied），重复发放必须幂等（allowed），
// 因为 watcher 会对同一个包反复调用（每次文件事件）。
func TestDevAutoEnsureLiveGrantIsSuperAdminOnlyAndIdempotent(t *testing.T) {
	extension := exactTrustExtension(t, "demo.dev-auto-grant")
	extensions := &fakeExtensionStore{items: map[string]Extension{extension.ID: extension}}
	trustStore := &memoryExecutableTrustStore{}
	service := NewExecutableTrustService(extensions, trustStore)
	ctx := context.Background()

	if err := service.EnsureLiveGrantForDevAuto(ctx, techAdminPluginManager(), extension); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("non super_admin dev grant must be denied: %v", err)
	}
	if len(trustStore.grants) != 0 {
		t.Fatalf("denied dev grant must not write a grant: %#v", trustStore.grants)
	}

	if err := service.EnsureLiveGrantForDevAuto(ctx, extensionManager(), extension); err != nil {
		t.Fatal(err)
	}
	if len(trustStore.grants) != 1 {
		t.Fatalf("first dev grant must create exactly one live grant: %#v", trustStore.grants)
	}
	if err := service.EnsureLiveGrantForDevAuto(ctx, extensionManager(), extension); err != nil {
		t.Fatal(err)
	}
	if len(trustStore.grants) != 1 {
		t.Fatalf("repeated dev grant must reuse the live grant: %#v", trustStore.grants)
	}

	// 不需要可执行信任的包不进入 grant 通道（无副作用）。
	plain := installedExtension("demo.plain-grant", TypePlugin, ManifestBackend{})
	if err := service.EnsureLiveGrantForDevAuto(ctx, extensionManager(), plain); err != nil {
		t.Fatalf("trust-free extension must be a no-op: %v", err)
	}
	if len(trustStore.grants) != 1 {
		t.Fatalf("trust-free extension must not write a grant: %#v", trustStore.grants)
	}
}
