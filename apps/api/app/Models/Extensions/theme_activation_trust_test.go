package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestThemeActivationTrustLeavesOrdinaryUploadedThemeBuildless(t *testing.T) {
	target := exactThemeRuntimeExtensionFixture(t, "ordinary.theme", "/ordinary")
	target.Source = SourceUploaded
	target.Status = StatusInstalled
	store := newFakeExtensionStore(map[string]Extension{target.ID: target})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true),
	)

	active, err := service.ActivateThemeFromPreview(
		context.Background(), extensionManager(), target.ID, themeTrustActivationInput(Extension{}, target, ""),
	)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != target.ID || store.activeThemeID != target.ID || store.activateThemeExactCalls != 1 {
		t.Fatalf("active=%#v activeID=%q calls=%d", active, store.activeThemeID, store.activateThemeExactCalls)
	}
}

func TestThemeActivationTrustRequiresActorBoundExactChallengeBeforeSwitch(t *testing.T) {
	ctx := context.Background()
	current := exactThemeRuntimeExtensionFixture(t, "current.theme", "/current")
	current.Source = SourceBuiltin
	current.Status = StatusEnabled
	target := executableThemeFixture(t, "trusted.theme", "1.0.0", StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	registry := &themeActivationApprovalRegistry{}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithPageRegistry(registry),
	)
	actor := extensionManager()
	input := themeTrustActivationInput(current, target, "")
	status, err := trust.Status(ctx, actor, target.ID)
	if err != nil || !status.TrustRequired || status.Trusted || len(status.Impact.RegistryComponents) != 1 {
		t.Fatalf("L2 trust status=%#v err=%v", status, err)
	}

	if _, err := service.ActivateThemeFromPreview(ctx, actor, target.ID, input); !errors.Is(err, ErrTrustChallengeRequired) {
		t.Fatalf("missing challenge error=%v", err)
	}
	if store.activeThemeID != current.ID || store.activateThemeExactCalls != 0 || registry.ordinary != 0 {
		t.Fatalf("untrusted activation changed state: active=%q dbCalls=%d registryCalls=%d", store.activeThemeID, store.activateThemeExactCalls, registry.ordinary)
	}

	challenge, err := trust.Challenge(ctx, actor, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := actor
	other.ID++
	input.ConfirmationToken = challenge.Token
	if _, err := service.ActivateThemeFromPreview(ctx, other, target.ID, input); !errors.Is(err, ErrTrustChallengeInvalid) {
		t.Fatalf("wrong actor error=%v", err)
	}
	if store.activeThemeID != current.ID || store.activateThemeExactCalls != 0 || registry.ordinary != 0 {
		t.Fatalf("wrong actor changed state: active=%q dbCalls=%d registryCalls=%d", store.activeThemeID, store.activateThemeExactCalls, registry.ordinary)
	}

	active, err := service.ActivateThemeFromPreview(ctx, actor, target.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	trusted, trustErr := trust.TrustedArtifact(ctx, target)
	if active.ID != target.ID || store.activeThemeID != target.ID || !trusted || trustErr != nil || registry.ordinary != 1 {
		t.Fatalf("active=%#v activeID=%q trusted=%t trustErr=%v registryCalls=%d", active, store.activeThemeID, trusted, trustErr, registry.ordinary)
	}
}

func TestThemeActivationTrustRejectsExpiredAndStaleChallengesBeforeSwitch(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		ctx := context.Background()
		current := exactThemeRuntimeExtensionFixture(t, "expired.current", "/current")
		current.Status = StatusEnabled
		target := executableThemeFixture(t, "expired.theme", "1.0.0", StatusInstalled)
		store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
		store.activeThemeID = current.ID
		now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
		trustStore := &memoryExecutableTrustStore{now: func() time.Time { return now }}
		trust := NewExecutableTrustService(store, trustStore)
		trust.now = func() time.Time { return now }
		registry := &themeActivationApprovalRegistry{}
		service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithPageRegistry(registry))
		actor := extensionManager()
		challenge, err := trust.Challenge(ctx, actor, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(DefaultTrustChallengeTTL + time.Second)
		_, err = service.ActivateThemeFromPreview(
			ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
		)
		if !errors.Is(err, ErrTrustChallengeExpired) || store.activeThemeID != current.ID || store.activateThemeExactCalls != 0 || registry.ordinary != 0 {
			t.Fatalf("error=%v active=%q dbCalls=%d registryCalls=%d", err, store.activeThemeID, store.activateThemeExactCalls, registry.ordinary)
		}
	})

	t.Run("stale artifact", func(t *testing.T) {
		ctx := context.Background()
		current := exactThemeRuntimeExtensionFixture(t, "stale.current", "/current")
		current.Status = StatusEnabled
		target := executableThemeFixture(t, "stale.theme", "1.0.0", StatusInstalled)
		store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
		store.activeThemeID = current.ID
		trustStore := &memoryExecutableTrustStore{}
		trust := NewExecutableTrustService(store, trustStore)
		registry := &themeActivationApprovalRegistry{}
		service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithPageRegistry(registry))
		actor := extensionManager()
		challenge, err := trust.Challenge(ctx, actor, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		mutateExecutableTheme(t, &target)
		store.items[target.ID] = target
		_, err = service.ActivateThemeFromPreview(
			ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
		)
		if !errors.Is(err, ErrTrustChallengeStale) || store.activeThemeID != current.ID || store.activateThemeExactCalls != 0 || registry.ordinary != 0 {
			t.Fatalf("error=%v active=%q dbCalls=%d registryCalls=%d", err, store.activeThemeID, store.activateThemeExactCalls, registry.ordinary)
		}
	})
}

func TestThemeActivationTrustCompensatesFailedStagedActivationExactlyAndConsumesToken(t *testing.T) {
	ctx := context.Background()
	current := executableThemeFixture(t, "staged.trusted.theme", "1.0.0", StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current})
	store.activeThemeID = current.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	actor := extensionManager()
	currentChallenge, err := trust.Challenge(ctx, actor, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := trust.ConfirmEnable(ctx, actor, current, currentChallenge.Token); err != nil {
		t.Fatal(err)
	}

	candidate := executableThemeFixture(t, current.ID, "2.0.0", StatusInstalled)
	current.StagedVersion = &ExtensionVersion{
		ID: 2, Version: candidate.Version, Manifest: candidate.Manifest,
		PackageDigest: candidate.PackageDigest, PackagePath: candidate.PackagePath,
		InstalledAt: candidate.InstalledAt,
	}
	store.items[current.ID] = current
	candidateChallenge, err := trust.Challenge(ctx, actor, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("activate candidate failed")
	store.activateThemeExactErr = injected
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true))
	input := themeTrustActivationInput(current, candidate, candidateChallenge.Token)

	_, err = service.ActivateThemeFromPreview(ctx, actor, current.ID, input)
	if !errors.Is(err, injected) || store.activeThemeID != current.ID || store.activateThemeExactCalls != 1 {
		t.Fatalf("error=%v active=%q calls=%d", err, store.activeThemeID, store.activateThemeExactCalls)
	}
	currentTrusted, currentErr := trust.TrustedArtifact(ctx, current)
	candidateTrusted, candidateErr := trust.TrustedArtifact(ctx, candidate)
	if !currentTrusted || currentErr != nil || candidateTrusted || candidateErr != nil {
		t.Fatalf("current=(%t,%v) candidate=(%t,%v)", currentTrusted, currentErr, candidateTrusted, candidateErr)
	}

	store.activateThemeExactErr = nil
	_, err = service.ActivateThemeFromPreview(ctx, actor, current.ID, input)
	if !errors.Is(err, ErrTrustChallengeReplayed) || store.activateThemeExactCalls != 1 {
		t.Fatalf("replay error=%v calls=%d", err, store.activateThemeExactCalls)
	}
}

func TestThemeActivationTrustCompensatesRegistryFailureButKeepsPriorGrant(t *testing.T) {
	t.Run("new grant is revoked", func(t *testing.T) {
		ctx := context.Background()
		current := exactThemeRuntimeExtensionFixture(t, "registry.current", "/current")
		current.Status = StatusEnabled
		target := executableThemeFixture(t, "registry.theme", "1.0.0", StatusInstalled)
		store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
		store.activeThemeID = current.ID
		trustStore := &memoryExecutableTrustStore{}
		trust := NewExecutableTrustService(store, trustStore)
		registry := &themeActivationApprovalRegistry{err: errors.New("registry failed")}
		service := NewServiceWithOptions(
			store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithPageRegistry(registry),
		)
		actor := extensionManager()
		challenge, err := trust.Challenge(ctx, actor, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.ActivateThemeFromPreview(
			ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
		)
		trusted, trustErr := trust.TrustedArtifact(ctx, target)
		if !errors.Is(err, ErrBuildFailed) || store.activeThemeID != current.ID || trusted || trustErr != nil {
			t.Fatalf("error=%v active=%q trusted=%t trustErr=%v", err, store.activeThemeID, trusted, trustErr)
		}
	})

	t.Run("existing exact grant survives", func(t *testing.T) {
		ctx := context.Background()
		current := exactThemeRuntimeExtensionFixture(t, "existing.current", "/current")
		current.Status = StatusEnabled
		target := executableThemeFixture(t, "existing.theme", "1.0.0", StatusInstalled)
		store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
		store.activeThemeID = current.ID
		trustStore := &memoryExecutableTrustStore{}
		trust := NewExecutableTrustService(store, trustStore)
		actor := extensionManager()
		challenge, err := trust.Challenge(ctx, actor, target.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := trust.ConfirmEnable(ctx, actor, target, challenge.Token); err != nil {
			t.Fatal(err)
		}
		registry := &themeActivationApprovalRegistry{err: errors.New("registry failed")}
		service := NewServiceWithOptions(
			store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithPageRegistry(registry),
		)
		_, err = service.ActivateThemeFromPreview(ctx, actor, target.ID, themeTrustActivationInput(current, target, ""))
		trusted, trustErr := trust.TrustedArtifact(ctx, target)
		if !errors.Is(err, ErrBuildFailed) || !trusted || trustErr != nil {
			t.Fatalf("error=%v trusted=%t trustErr=%v", err, trusted, trustErr)
		}
	})
}

func TestThemeActivationTrustSerializesConcurrentUseOfOneChallenge(t *testing.T) {
	ctx := context.Background()
	current := exactThemeRuntimeExtensionFixture(t, "concurrent.current", "/current")
	current.Status = StatusEnabled
	target := executableThemeFixture(t, "concurrent.theme", "1.0.0", StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true))
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := themeTrustActivationInput(current, target, challenge.Token)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := service.ActivateThemeFromPreview(ctx, actor, target.ID, input)
			results <- err
		}()
	}
	succeeded, stale := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrThemePreviewStale):
			stale++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	trusted, trustErr := trust.TrustedArtifact(ctx, target)
	if succeeded != 1 || stale != 1 || !trusted || trustErr != nil || store.activeThemeID != target.ID {
		t.Fatalf("success=%d stale=%d trusted=%t trustErr=%v active=%q", succeeded, stale, trusted, trustErr, store.activeThemeID)
	}
}

func TestThemeActivationTrustRechecksGrantAfterDatabaseSwitch(t *testing.T) {
	ctx := context.Background()
	current := exactThemeRuntimeExtensionFixture(t, "recheck.current", "/current")
	current.Status = StatusEnabled
	target := executableThemeFixture(t, "recheck.theme", "1.0.0", StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	registry := &themeActivationApprovalRegistry{}
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithPageRegistry(registry),
	)
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	store.afterActivateThemeExact = func() {
		if err := trust.RevokeAllForExtension(ctx, target.ID, actor.ID, "concurrent_compensation"); err != nil {
			t.Errorf("revoke concurrent grant: %v", err)
		}
	}

	_, err = service.ActivateThemeFromPreview(
		ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
	)
	trusted, trustErr := trust.TrustedArtifact(ctx, target)
	if !errors.Is(err, ErrTrustGrantNotFound) || store.activeThemeID != current.ID ||
		registry.ordinary != 0 || trusted || trustErr != nil {
		t.Fatalf("error=%v active=%q registryCalls=%d trusted=%t trustErr=%v", err, store.activeThemeID, registry.ordinary, trusted, trustErr)
	}
}

func TestThemeActivationTrustDoesNotRevokeGrantAdoptedByAnotherActivation(t *testing.T) {
	ctx := context.Background()
	current := exactThemeRuntimeExtensionFixture(t, "adopted.current", "/current")
	current.Status = StatusEnabled
	target := executableThemeFixture(t, "adopted.theme", "1.0.0", StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true))
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := trust.confirmEnable(ctx, actor, target, challenge.Token)
	if err != nil || !receipt.created {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	current.Status = StatusDisabled
	target.Status = StatusEnabled
	store.items[current.ID] = current
	store.items[target.ID] = target
	store.activeThemeID = target.ID
	injected := errors.New("other activation lost database race")

	err = service.compensateThemeActivationTrust(ctx, actor, receipt, target, &current, injected)
	trusted, trustErr := trust.TrustedArtifact(ctx, target)
	if !errors.Is(err, injected) || !trusted || trustErr != nil {
		t.Fatalf("error=%v trusted=%t trustErr=%v", err, trusted, trustErr)
	}
}

func TestThemeActivationTrustAuditsCompensationFailure(t *testing.T) {
	ctx := context.Background()
	current := exactThemeRuntimeExtensionFixture(t, "audit.current", "/current")
	current.Status = StatusEnabled
	target := executableThemeFixture(t, "audit.theme", "1.0.0", StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	revokeErr := errors.New("trust storage unavailable")
	trustStore := &memoryExecutableTrustStore{revokeGrantErr: revokeErr}
	auditor := &recordingAuditor{}
	trust := NewExecutableTrustService(store, trustStore).WithAuditor(auditor)
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true))
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	activateErr := errors.New("database activation failed")
	store.activateThemeExactErr = activateErr
	_, err = service.ActivateThemeFromPreview(
		ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
	)
	if !errors.Is(err, activateErr) || !errors.Is(err, revokeErr) {
		t.Fatalf("combined error=%v", err)
	}
	if len(store.events) == 0 || store.events[len(store.events)-1].Action != EventEnableFailed {
		t.Fatalf("missing compensation failure event: %#v", store.events)
	}
	found := false
	for _, event := range auditor.events {
		if event.Action == audit.ActionExtensionTrustRevoke && event.Metadata["compensation"] == true && event.Metadata["succeeded"] == false {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing failed compensation audit: %#v", auditor.events)
	}

	// 补偿存储恢复后，原 token 仍必须保持 consumed，不能重放。
	trustStore.revokeGrantErr = nil
	if err := trust.RevokeAllForExtension(ctx, target.ID, actor.ID, "test_cleanup"); err != nil {
		t.Fatal(err)
	}
	store.activateThemeExactErr = nil
	_, err = service.ActivateThemeFromPreview(
		ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
	)
	if !errors.Is(err, ErrTrustChallengeReplayed) {
		t.Fatalf("consumed token replay error=%v", err)
	}
}

func executableThemeFixture(t *testing.T, id, version, status string) Extension {
	t.Helper()
	root := t.TempDir()
	entryPath := "frontend/public/theme.mjs"
	entryBody := []byte("export const apiVersion = 1\nexport function mount() { return () => {} }\n")
	writeThemeRuntimeExtensionFile(t, root, entryPath, string(entryBody))
	writeThemeRuntimeExtensionFile(t, root, "theme.json", `{"schemaVersion":1,"styles":{"tokens":{}}}`)
	entryDigest := themeTrustBytesDigest(entryBody)
	manifest := installedExtension(id, TypeTheme, ManifestBackend{}).Manifest
	manifest.ManifestVersion = 3
	manifest.Version = version
	manifest.Components = []ManifestComponent{{
		ID: id + ".component.shell", ContractVersion: id + ".component.shell@1",
		Action: "add", L2Component: id + ".file.l2", PropsSchema: id + ".component.shell.props@1",
	}}
	manifest.PackageFiles = []ManifestPackageFile{{
		ID: id + ".file.l2", Kind: "frontend", Path: entryPath, Digest: entryDigest,
	}}
	if err := writeManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	packageDigest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return Extension{
		ID: id, Name: manifest.Name, Version: version, Type: TypeTheme, Status: status,
		Source: SourceUploaded, IsDeletable: true, Manifest: manifest,
		PackageDigest: packageDigest, PackagePath: root,
		InstalledAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func mutateExecutableTheme(t *testing.T, extension *Extension) {
	t.Helper()
	path := extension.Manifest.PackageFiles[0].Path
	body := []byte("export const apiVersion = 1\nexport function mount(target) { target.dataset.changed = '1' }\n")
	if err := os.WriteFile(filepath.Join(extension.PackagePath, filepath.FromSlash(path)), body, 0o600); err != nil {
		t.Fatal(err)
	}
	extension.Manifest.PackageFiles[0].Digest = themeTrustBytesDigest(body)
	if err := writeManifest(extension.PackagePath, extension.Manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest = digest
}

func themeTrustActivationInput(current, target Extension, token string) ThemeActivationInput {
	return ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: current.ID, CurrentThemeVersion: current.Version, CurrentThemeDigest: current.PackageDigest,
		ConfirmationToken: token,
	}
}

func themeTrustBytesDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
