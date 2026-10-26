package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

func TestThemeAssetActivationPublishesFirstUploadedL2AfterTrust(t *testing.T) {
	ctx := t.Context()
	current := exactThemeRuntimeExtensionFixture(t, "asset.current-theme", "/current")
	current.Source = SourceBuiltin
	current.Status = StatusEnabled
	target := executableThemeFixture(t, "asset.uploaded-theme", "1.0.0", StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{current.ID: current, target.ID: target})
	store.activeThemeID = current.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	assets := assetregistry.New()
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{},
		WithExecutableTrust(trust, true), WithAssetRegistry(assets),
	)
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, target.ID)
	if err != nil {
		t.Fatal(err)
	}

	active, err := service.ActivateThemeFromPreview(
		ctx, actor, target.ID, themeTrustActivationInput(current, target, challenge.Token),
	)
	if err != nil {
		t.Fatal(err)
	}
	publication, found := assets.SnapshotPublication(target.ID)
	if active.ID != target.ID || !found || publication.Artifact.OwnerKind != assetregistry.OwnerKindTheme ||
		publication.Artifact.PackageDigest != target.PackageDigest || len(publication.Assets) == 0 {
		t.Fatalf("active=%#v publication=%#v found=%t", active, publication, found)
	}
}

func TestLegacyAssetOnlyPluginEnableAndConcurrentExactReplayConverge(t *testing.T) {
	ctx := t.Context()
	item := assetServiceFixture(t, "asset.only-plugin", TypePlugin, SourceUploaded, StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	assets := assetregistry.New()
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{},
		WithExecutableTrust(trust, true), WithAssetRegistry(assets),
	)
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, item.ID)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var calls sync.WaitGroup
	for range 2 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			<-start
			_, err := service.Enable(ctx, actor, item.ID, EnableInput{ConfirmationToken: challenge.Token})
			errs <- err
		}()
	}
	close(start)
	calls.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent enable: %v", err)
		}
	}
	publication, found := assets.SnapshotPublication(item.ID)
	if !found || publication.Artifact.OwnerKind != assetregistry.OwnerKindPlugin || len(publication.Assets) != 1 {
		t.Fatalf("asset-only publication=%#v found=%t", publication, found)
	}
	stored, err := store.Get(ctx, item.ID)
	if err != nil || stored.Status != StatusEnabled {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestLegacyEnableOwnerPublishPreservesUnrelatedConcurrentWriter(t *testing.T) {
	ctx := t.Context()
	item := assetServiceFixture(t, "asset.concurrent-enable", TypePlugin, SourceUploaded, StatusInstalled)
	unrelated := assetServiceFixture(t, "asset.concurrent-enable-other", TypePlugin, SourceBuiltin, StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item, unrelated.ID: unrelated})
	trustStore := &assetLiveGrantHookStore{memoryExecutableTrustStore: &memoryExecutableTrustStore{}}
	trust := NewExecutableTrustService(store, trustStore)
	assets := assetregistry.New()
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
	unrelatedPublication := mustAssetPublication(t, service, unrelated)
	var hookErr error
	trustStore.hook = func() {
		_, hookErr = assets.Publish(unrelatedPublication)
	}
	actor := extensionManager()
	challenge, err := trust.Challenge(ctx, actor, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Enable(ctx, actor, item.ID, EnableInput{ConfirmationToken: challenge.Token}); err != nil {
		t.Fatal(err)
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	for _, extensionID := range []string{item.ID, unrelated.ID} {
		if _, found := assets.SnapshotPublication(extensionID); !found {
			t.Fatalf("missing publication %q after concurrent owner publish", extensionID)
		}
	}
	stored, err := store.Get(ctx, item.ID)
	if err != nil || stored.Status != StatusEnabled {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestThemeAssetTransitionRejectsStaleDriftAndMultipleThemes(t *testing.T) {
	ctx := t.Context()
	source := assetServiceFixture(t, "asset.source-theme", TypeTheme, SourceBuiltin, StatusEnabled)
	target := assetServiceFixture(t, "asset.target-theme", TypeTheme, SourceBuiltin, StatusInstalled)
	unrelated := assetServiceFixture(t, "asset.unrelated-plugin", TypePlugin, SourceBuiltin, StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{source.ID: source, target.ID: target, unrelated.ID: unrelated})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})

	t.Run("stale revision", func(t *testing.T) {
		assets := assetregistry.New()
		service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
		sourcePublication := mustAssetPublication(t, service, source)
		if _, err := assets.Publish(sourcePublication); err != nil {
			t.Fatal(err)
		}
		expected := service.captureAssetPublicationSnapshot()
		if _, err := assets.Publish(mustAssetPublication(t, service, unrelated)); err != nil {
			t.Fatal(err)
		}
		if _, err := service.publishThemeAssetTransition(ctx, expected, &target, &source); !errors.Is(err, assetregistry.ErrRevisionConflict) {
			t.Fatalf("stale transition error=%v", err)
		}
		if _, found := assets.SnapshotPublication(source.ID); !found {
			t.Fatal("stale switch removed exact source")
		}
		if _, found := assets.SnapshotPublication(target.ID); found {
			t.Fatal("stale switch published target")
		}
	})

	t.Run("same artifact declaration drift", func(t *testing.T) {
		assets := assetregistry.New()
		service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
		drifted := mustAssetPublication(t, service, source)
		drifted.Assets[0].Path = "frontend/public/drifted.css"
		if _, err := assets.Publish(drifted); err != nil {
			t.Fatal(err)
		}
		expected := service.captureAssetPublicationSnapshot()
		if err := service.validateThemeAssetTransition(ctx, expected, &source, &source); !errors.Is(err, assetregistry.ErrArtifactConflict) {
			t.Fatalf("declaration drift error=%v", err)
		}
	})

	// 观察到「主题发布既不匹配 outgoing 也不匹配 incoming」这一分支（此前只是代码阅读
	// 得出的候选）：残留的第三方主题发布会让任何后续切换失败，即使新主题包本身完全合法。
	// 与上一个子测试不同：这里只有一个主题发布，因此不会在「多于一个主题」处提前返回，
	// 而是走到 source/target 匹配判断。
	t.Run("stale third theme publication blocks every switch", func(t *testing.T) {
		assets := assetregistry.New()
		service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
		stale := assetServiceFixture(t, "asset.stale-theme", TypeTheme, SourceBuiltin, StatusDisabled)
		store.items[stale.ID] = stale
		if _, err := assets.Publish(mustAssetPublication(t, service, stale)); err != nil {
			t.Fatal(err)
		}
		expected := service.captureAssetPublicationSnapshot()
		if err := service.validateThemeAssetTransition(ctx, expected, &target, &source); !errors.Is(err, errAssetPublicationConflict) {
			t.Fatalf("stale third theme publication must be rejected, got %v", err)
		}
		// 清理残留发布后，同一切换必须通过：证明拒绝来自残留状态而不是新主题包。
		if _, err := assets.ReplaceAll(nil); err != nil {
			t.Fatal(err)
		}
		if err := service.validateThemeAssetTransition(ctx, service.captureAssetPublicationSnapshot(), &target, &source); err != nil {
			t.Fatalf("after clearing the stale publication the same switch must pass: %v", err)
		}
	})

	t.Run("exactly one theme owner", func(t *testing.T) {
		assets := assetregistry.New()
		service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
		if _, err := assets.ReplaceAll([]assetregistry.Publication{
			mustAssetPublication(t, service, source), mustAssetPublication(t, service, target),
		}); err != nil {
			t.Fatal(err)
		}
		if err := service.validateThemeAssetTransition(ctx, service.captureAssetPublicationSnapshot(), &target, &source); !errors.Is(err, errAssetPublicationConflict) {
			t.Fatalf("multiple themes error=%v", err)
		}
	})
}

func TestThemeAssetRollbackDoesNotResurrectRevokedSource(t *testing.T) {
	ctx := t.Context()
	source := assetServiceFixture(t, "asset.revoked-source", TypeTheme, SourceUploaded, StatusEnabled)
	target := assetServiceFixture(t, "asset.rollback-target", TypeTheme, SourceBuiltin, StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{source.ID: source, target.ID: target})
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	grantPublicFrontend(t, trust, source)
	assets := assetregistry.New()
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
	if _, err := assets.Publish(mustAssetPublication(t, service, source)); err != nil {
		t.Fatal(err)
	}
	before := service.captureAssetPublicationSnapshot()
	after, err := service.publishThemeAssetTransition(ctx, before, &target, &source)
	if err != nil {
		t.Fatal(err)
	}
	if err := trustStore.RevokeAll(ctx, source.ID, 99, "test_revoke"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.rollbackThemeAssetTransition(ctx, after, &source, &target); !errors.Is(err, ErrTrustGrantNotFound) {
		t.Fatalf("rollback error=%v", err)
	}
	snapshot := assets.Snapshot()
	for _, publication := range snapshot.Publications {
		if publication.Artifact.OwnerKind == assetregistry.OwnerKindTheme {
			t.Fatalf("revoked source or failed target survived rollback: %#v", snapshot)
		}
	}
}

func TestThemeActivationPageFailureRestoresExactAssetSource(t *testing.T) {
	ctx := t.Context()
	source := assetServiceFixture(t, "asset.compensation-source", TypeTheme, SourceBuiltin, StatusEnabled)
	target := assetServiceFixture(t, "asset.compensation-target", TypeTheme, SourceBuiltin, StatusInstalled)
	store := newFakeExtensionStore(map[string]Extension{source.ID: source, target.ID: target})
	store.activeThemeID = source.ID
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	assets := assetregistry.New()
	pageFailure := errors.New("page publication failed")
	service := NewServiceWithOptions(
		store, t.TempDir(), "", LocalRuntimeManager{},
		WithExecutableTrust(trust, true), WithAssetRegistry(assets),
		WithPageRegistry(&themeActivationApprovalRegistry{err: pageFailure}),
	)
	if _, err := assets.Publish(mustAssetPublication(t, service, source)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ActivateTheme(ctx, extensionManager(), target.ID); err == nil || !strings.Contains(err.Error(), pageFailure.Error()) {
		t.Fatalf("activation error=%v", err)
	}
	active, err := store.ActiveTheme(ctx)
	if err != nil || !sameThemeExactArtifact(active, source) {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	if publication, found := assets.SnapshotPublication(source.ID); !found || !assetArtifactMatchesExtension(publication.Artifact, source) {
		t.Fatalf("source publication=%#v found=%t", publication, found)
	}
	if _, found := assets.SnapshotPublication(target.ID); found {
		t.Fatal("failed target survived exact compensation")
	}
}

func TestThemeAssetRollbackRevisionConflictQuarantinesFailedTarget(t *testing.T) {
	ctx := t.Context()
	source := assetServiceFixture(t, "asset.race-source", TypeTheme, SourceBuiltin, StatusEnabled)
	target := assetServiceFixture(t, "asset.race-target", TypeTheme, SourceBuiltin, StatusInstalled)
	unrelated := assetServiceFixture(t, "asset.race-unrelated", TypePlugin, SourceBuiltin, StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{source.ID: source, target.ID: target, unrelated.ID: unrelated})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	assets := assetregistry.New()
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
	if _, err := assets.Publish(mustAssetPublication(t, service, source)); err != nil {
		t.Fatal(err)
	}
	after, err := service.publishThemeAssetTransition(ctx, service.captureAssetPublicationSnapshot(), &target, &source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.Publish(mustAssetPublication(t, service, unrelated)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.rollbackThemeAssetTransition(ctx, after, &source, &target); !errors.Is(err, assetregistry.ErrRevisionConflict) {
		t.Fatalf("rollback conflict error=%v", err)
	}
	if _, found := assets.SnapshotPublication(target.ID); found {
		t.Fatal("failed target survived rollback CAS conflict")
	}
	if _, found := assets.SnapshotPublication(unrelated.ID); !found {
		t.Fatal("exact quarantine removed unrelated concurrent publication")
	}
}

func TestServiceAssetRestoreAndSafeModeKeepOnlyAuthoritativeGraph(t *testing.T) {
	ctx := t.Context()
	plugin := assetServiceFixture(t, "asset.restore-plugin", TypePlugin, SourceUploaded, StatusEnabled)
	theme := assetServiceFixture(t, "asset.restore-theme", TypeTheme, SourceBuiltin, StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{plugin.ID: plugin, theme.ID: theme})
	store.activeThemeID = theme.ID
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	grantPublicFrontend(t, trust, plugin)
	assets := assetregistry.New()
	core := assetregistry.Publication{Artifact: assetregistry.Artifact{
		ExtensionID: "core.assets", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), ImpactDigest: strings.Repeat("b", 64),
		OwnerKind: assetregistry.OwnerKindCore, Core: true,
	}}
	if _, err := assets.Publish(core); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))

	if err := service.RestoreActiveThemeRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot := assets.Snapshot(); len(snapshot.Publications) != 3 {
		t.Fatalf("restored snapshot=%#v", snapshot)
	}
	if err := service.RestoreSafeModeThemeRegistry(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot := assets.Snapshot()
	if len(snapshot.Publications) != 1 || snapshot.Publications[0].Artifact.OwnerKind != assetregistry.OwnerKindCore {
		t.Fatalf("safe mode snapshot=%#v", snapshot)
	}
}

func TestUnboundServiceRestoreDoesNotPublishAssetsAndLateBindHasNoSideEffect(t *testing.T) {
	assets := assetregistry.New()
	core := assetregistry.Publication{Artifact: assetregistry.Artifact{
		ExtensionID: "core.late-assets", ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), ImpactDigest: strings.Repeat("b", 64),
		OwnerKind: assetregistry.OwnerKindCore, Core: true,
	}}
	if _, err := assets.Publish(core); err != nil {
		t.Fatal(err)
	}
	before := assets.Snapshot()
	service := NewService(&fakeExtensionStore{}, t.TempDir())
	if err := service.RestoreActiveThemeRegistry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.RestoreSafeModeThemeRegistry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if bound := service.BindAssetRegistry(assets); bound != service || service.assetRegistry != assets {
		t.Fatal("late binding did not retain the shared Registry")
	}
	after := assets.Snapshot()
	if after.Revision != before.Revision || after.Digest != before.Digest || len(after.Publications) != len(before.Publications) {
		t.Fatalf("restore/bind mutated external Registry: before=%#v after=%#v", before, after)
	}
}

func TestLegacyDisableAndUninstallRemoveExactAssetPublications(t *testing.T) {
	ctx := t.Context()
	for _, action := range []string{"disable", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			status := StatusEnabled
			if action == "uninstall" {
				status = StatusDisabled
			}
			item := assetServiceFixture(t, "asset.legacy-"+action, TypePlugin, SourceUploaded, status)
			store := newFakeExtensionStore(map[string]Extension{item.ID: item})
			trustStore := &memoryExecutableTrustStore{}
			trust := NewExecutableTrustService(store, trustStore)
			grantPublicFrontend(t, trust, item)
			assets := assetregistry.New()
			service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
			if _, err := assets.Publish(mustAssetPublication(t, service, item)); err != nil {
				t.Fatal(err)
			}
			if action == "disable" {
				if _, err := service.Disable(ctx, extensionManager(), item.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := service.UninstallWithResult(ctx, extensionManager(), item.ID, UninstallInput{RetainPackage: true}); err != nil {
				t.Fatal(err)
			}
			if _, found := assets.SnapshotPublication(item.ID); found {
				t.Fatal("exact publication survived lifecycle removal")
			}
		})
	}
}

func TestRemoveExactAssetPublicationCannotDeleteNewerArtifact(t *testing.T) {
	old := assetServiceFixture(t, "asset.stale-remove", TypePlugin, SourceBuiltin, StatusEnabled)
	newer := old
	newer.Version = "2.0.0"
	newer.Manifest.Version = newer.Version
	newer.PackageDigest = strings.Repeat("c", 64)
	store := newFakeExtensionStore(map[string]Extension{old.ID: newer})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	assets := assetregistry.New()
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
	oldPublication := mustAssetPublication(t, service, old)
	if _, err := assets.Publish(oldPublication); err != nil {
		t.Fatal(err)
	}
	stale := service.captureAssetPublicationSnapshot()
	newPublication := oldPublication
	newPublication.Artifact.ExtensionVersion = newer.Version
	newPublication.Artifact.PackageDigest = newer.PackageDigest
	if _, err := assets.ReplaceAllIfRevision(stale.revision, []assetregistry.Publication{newPublication}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.quarantineExactAssetPublication(t.Context(), stale, old); !errors.Is(err, assetregistry.ErrArtifactConflict) {
		t.Fatalf("stale removal error=%v", err)
	}
	publication, found := assets.SnapshotPublication(newer.ID)
	if !found || publication.Artifact.ExtensionVersion != newer.Version {
		t.Fatalf("newer publication=%#v found=%t", publication, found)
	}
}

func TestExactQuarantinePreservesAndRollsBackWithUnrelatedConcurrentWriter(t *testing.T) {
	item := assetServiceFixture(t, "asset.concurrent-remove", TypePlugin, SourceBuiltin, StatusEnabled)
	unrelated := assetServiceFixture(t, "asset.concurrent-remove-other", TypePlugin, SourceBuiltin, StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item, unrelated.ID: unrelated})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	assets := assetregistry.New()
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
	if _, err := assets.Publish(mustAssetPublication(t, service, item)); err != nil {
		t.Fatal(err)
	}
	caller := service.captureAssetPublicationSnapshot()
	if _, err := assets.Publish(mustAssetPublication(t, service, unrelated)); err != nil {
		t.Fatal(err)
	}
	mutation, err := service.quarantineExactAssetPublication(t.Context(), caller, item)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := assets.SnapshotPublication(item.ID); found {
		t.Fatal("exact owner survived quarantine")
	}
	if _, found := assets.SnapshotPublication(unrelated.ID); !found {
		t.Fatal("unrelated concurrent owner was removed")
	}
	if err := service.rollbackExactAssetMutation(mutation); err != nil {
		t.Fatal(err)
	}
	for _, extensionID := range []string{item.ID, unrelated.ID} {
		if _, found := assets.SnapshotPublication(extensionID); !found {
			t.Fatalf("missing publication %q after exact rollback", extensionID)
		}
	}
}

func TestExactPublishRollbackNeverDeletesIndistinguishableWriter(t *testing.T) {
	item := assetServiceFixture(t, "asset.publish-receipt", TypePlugin, SourceBuiltin, StatusEnabled)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	trust := NewExecutableTrustService(store, &memoryExecutableTrustStore{})
	assets := assetregistry.New()
	service := NewServiceWithOptions(store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets))
	mutation, err := service.publishExactExtensionAssetPublication(t.Context(), service.captureAssetPublicationSnapshot(), item)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.rollbackExactAssetMutation(mutation); !errors.Is(err, assetregistry.ErrRevisionConflict) {
		t.Fatalf("owner publish rollback error=%v", err)
	}
	if _, found := assets.SnapshotPublication(item.ID); !found {
		t.Fatal("rollback deleted an owner publication without a unique writer receipt")
	}
}

func TestLifecycleFinalizerUsesFrozenExactAssetAuthority(t *testing.T) {
	ctx := t.Context()
	item := assetServiceFixture(t, "asset.lifecycle-finalizer", TypePlugin, SourceUploaded, StatusDisabled)
	store := newFakeExtensionStore(map[string]Extension{item.ID: item})
	trustStore := &memoryExecutableTrustStore{}
	trust := NewExecutableTrustService(store, trustStore)
	grantPublicFrontend(t, trust, item)
	authority := lifecycleAuthorityTestGrant(t, item)
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	operation := LifecycleOperation{
		ID: 701, ExtensionID: item.ID, ExtensionVersion: item.Version, PackageDigest: item.PackageDigest,
		Operation: string(LifecycleMachineUninstall), RemovalMode: LifecycleRemovalPreserve,
		TerminalResult: LifecycleTerminalSucceeded, CompletedAt: &completedAt,
		AuthoritySnapshot: authorityJSON,
	}

	t.Run("exact cleanup", func(t *testing.T) {
		assets := assetregistry.New()
		finalizerCalls := 0
		service := NewServiceWithOptions(
			store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets),
			WithLifecycleCleanupFinalizer(func(context.Context, int64) (LifecycleCleanupFinalization, error) {
				finalizerCalls++
				return LifecycleCleanupFinalization{OperationID: operation.ID, Status: "finalized", PhysicalPurgeComplete: true}, nil
			}),
		)
		if _, err := assets.Publish(mustAssetPublication(t, service, item)); err != nil {
			t.Fatal(err)
		}
		if _, err := service.finalizeLifecycleUninstall(ctx, item.ID, operation.RemovalMode, operation, false); err != nil {
			t.Fatal(err)
		}
		if finalizerCalls != 1 {
			t.Fatalf("finalizer calls=%d", finalizerCalls)
		}
		if _, found := assets.SnapshotPublication(item.ID); found {
			t.Fatal("exact lifecycle publication survived finalization")
		}
	})

	t.Run("forced stale cleanup fails visibly", func(t *testing.T) {
		assets := assetregistry.New()
		finalizerCalls := 0
		service := NewServiceWithOptions(
			store, t.TempDir(), "", LocalRuntimeManager{}, WithExecutableTrust(trust, true), WithAssetRegistry(assets),
			WithLifecycleCleanupFinalizer(func(context.Context, int64) (LifecycleCleanupFinalization, error) {
				finalizerCalls++
				return LifecycleCleanupFinalization{}, nil
			}),
		)
		newer := mustAssetPublication(t, service, item)
		newer.Artifact.ExtensionVersion = "2.0.0"
		newer.Artifact.PackageDigest = strings.Repeat("d", 64)
		if _, err := assets.Publish(newer); err != nil {
			t.Fatal(err)
		}
		forced := operation
		forced.Forced = true
		_, err := service.finalizeLifecycleUninstall(ctx, item.ID, forced.RemovalMode, forced, false)
		if !errors.Is(err, ErrLifecycleCleanupFinalization) || !strings.Contains(err.Error(), "forced=true") {
			t.Fatalf("forced stale cleanup error=%v", err)
		}
		if finalizerCalls != 0 {
			t.Fatalf("physical finalizer ran after stale asset conflict: %d", finalizerCalls)
		}
		publication, found := assets.SnapshotPublication(item.ID)
		if !found || publication.Artifact.ExtensionVersion != "2.0.0" {
			t.Fatalf("newer publication=%#v found=%t", publication, found)
		}
	})
}

func mustAssetPublication(t *testing.T, service *Service, extension Extension) assetregistry.Publication {
	t.Helper()
	publication, err := service.extensionAssetPublication(t.Context(), extension, true)
	if err != nil {
		t.Fatal(err)
	}
	if publication == nil {
		t.Fatal("expected asset publication")
	}
	return *publication
}

func assetServiceFixture(t *testing.T, id, extensionType, source, status string) Extension {
	t.Helper()
	root := t.TempDir()
	assetPath := "frontend/public/shared.css"
	body := []byte(".asset-service { color: var(--sf-accent); }\n")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, assetPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, assetPath), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if extensionType == TypeTheme {
		if err := os.WriteFile(filepath.Join(root, "theme.json"), []byte(`{"schemaVersion":1,"styles":{"tokens":{}}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	item := installedExtension(id, extensionType, ManifestBackend{})
	item.Source = source
	item.Status = status
	item.IsDeletable = source == SourceUploaded
	item.PackagePath = root
	item.Manifest.ManifestVersion = 3
	item.Manifest.PackageFiles = []ManifestPackageFile{{
		ID: id + ".file.style", Kind: "asset", Path: assetPath, Digest: bytesDigest(body),
	}}
	item.Manifest.Assets = []ManifestAsset{{
		Handle: id + ".asset.style", ContractVersion: id + ".asset.style@1",
		Type: "style", Path: assetPath, Digest: bytesDigest(body), Loading: "blocking",
	}}
	if err := writeManifest(root, item.Manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(root)
	if err != nil {
		t.Fatal(err)
	}
	item.PackageDigest = digest
	return item
}

type assetLiveGrantHookStore struct {
	*memoryExecutableTrustStore
	once sync.Once
	hook func()
}

func (s *assetLiveGrantHookStore) LiveGrant(ctx context.Context, identity TrustIdentity) (TrustGrant, error) {
	s.once.Do(func() {
		if s.hook != nil {
			s.hook()
		}
	})
	return s.memoryExecutableTrustStore.LiveGrant(ctx, identity)
}
