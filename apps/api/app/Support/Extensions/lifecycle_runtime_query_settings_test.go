package extensionsruntime

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestRuntimeQuerySettingsRestartPublishesExactReplacement(t *testing.T) {
	for _, test := range []struct {
		name      string
		extension extensions.Extension
	}{
		{name: "query owner", extension: legacyRuntimeQueryExtension("1.0.0", 'a', 41)},
		{name: "filter only", extension: legacyRuntimeFilterOnlyExtension("1.0.0", 'c', 43)},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.extension.PackagePath = t.TempDir()
			starter := newManagerStagedStarter()
			manager := NewManager(ManagerConfig{Starter: starter})
			if err := manager.Start(t.Context(), test.extension); err != nil {
				t.Fatal(err)
			}
			queries := queryregistry.New()
			boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
				Manager: manager, Queries: queries,
			})
			if _, err := boundary.PublishRuntimeQueries(t.Context(), test.extension); err != nil {
				t.Fatal(err)
			}
			source, err := manager.ActiveRuntimeInstance(test.extension.ID)
			if err != nil {
				t.Fatal(err)
			}

			restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), test.extension)
			if err != nil {
				t.Fatal(err)
			}
			if manager.RuntimeInstanceAvailable(source.Identity) {
				t.Fatal("prepared settings restart left source admission open")
			}
			if err := restart.RestartRuntimeQueriesForSettings(t.Context(), test.extension); err != nil {
				t.Fatal(err)
			}
			target, err := manager.ActiveRuntimeInstance(test.extension.ID)
			if err != nil || target.Identity == source.Identity || !manager.RuntimeInstanceAvailable(target.Identity) {
				t.Fatalf("replacement runtime = %#v, %v", target, err)
			}
			publication, found := queries.SnapshotPublication(test.extension.ID)
			if !found || publication.Artifact.RuntimeInstanceID != target.Identity.InstanceID ||
				!runtimeQueryArtifactMatchesInstance(publication.Artifact, test.extension, target) {
				t.Fatalf("replacement Query publication = %#v", publication)
			}
			if _, err := manager.InspectRuntimeInstance(source.Identity); !errors.Is(err, ErrRuntimeInstanceNotFound) {
				t.Fatalf("source runtime was not cleaned up: %v", err)
			}
		})
	}
}

func TestRuntimeQuerySettingsRestartCancellationRestoresExactSource(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourcePublication, _ := queries.SnapshotPublication(extension.ID)
	restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatal("prepared source admission remained open")
	}

	starter.publishStarted = make(chan struct{})
	starter.publishContinue = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- restart.RestartRuntimeQueriesForSettings(ctx, extension)
	}()

	waitSettingsRuntimePublish(t, starter.publishStarted)
	cancel()
	starter.publishContinue <- struct{}{}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled settings restart = %v", err)
	}
	if manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatal("failed restart reopened source before settings rollback")
	}
	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("failed target remained active: %v", err)
	}

	restoreDone := make(chan error, 1)
	go func() {
		restoreDone <- restart.RestoreRuntimeQueriesAfterSettingsRollback(t.Context())
	}()
	// Runtime admission reopens only after the caller has restored old settings.
	waitSettingsRuntimePublish(t, starter.publishStarted)
	starter.publishContinue <- struct{}{}
	if err := <-restoreDone; err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity != source.Identity || !manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatalf("source runtime was not restored: %#v, %v", active, err)
	}
	publication, found := queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact != sourcePublication.Artifact {
		t.Fatalf("source Query publication was not retained: %#v", publication)
	}
	target := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "instance-2"}
	if _, err := manager.InspectRuntimeInstance(target); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("failed settings runtime was not removed: %v", err)
	}
}

func TestRuntimeQuerySettingsRestartSourceStopFailureRollsBackBeforeAdmission(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	starter := &settingsRestartStopFailureStarter{managerStagedStarter: newManagerStagedStarter()}
	manager := NewManager(ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourcePublication, _ := queries.SnapshotPublication(extension.ID)
	restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	stopErr := errors.New("source process refused to stop")
	starter.failIdentity, starter.stopErr = source.Identity, stopErr
	if err := restart.RestartRuntimeQueriesForSettings(t.Context(), extension); !errors.Is(err, stopErr) {
		t.Fatalf("source stop failure = %v", err)
	}
	if manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatal("source stop failure reopened admission before settings rollback")
	}
	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("failed replacement remained active: %v", err)
	}
	publication, found := queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact != sourcePublication.Artifact {
		t.Fatalf("source stop failure did not roll Query publication back: %#v", publication)
	}

	starter.stopErr = nil
	if err := restart.RestoreRuntimeQueriesAfterSettingsRollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity != source.Identity || !manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatalf("source was not restored after setting rollback: %#v, %v", active, err)
	}
}

func TestRuntimeQuerySettingsRestartCannotBypassSourceQuarantine(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourcePublication, _ := queries.SnapshotPublication(extension.ID)
	restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	revoked := errors.New("trust revoked during settings restart")
	if _, err := manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}, revoked); err != nil {
		t.Fatal(err)
	}
	if err := restart.RestartRuntimeQueriesForSettings(t.Context(), extension); !errors.Is(err, ErrRuntimeInstanceConflict) {
		t.Fatalf("restart after source quarantine = %v", err)
	}
	if err := restart.RestoreRuntimeQueriesAfterSettingsRollback(t.Context()); !errors.Is(err, ErrRuntimeAdmissionQuarantined) || !errors.Is(err, revoked) {
		t.Fatalf("restore after source quarantine = %v", err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity != source.Identity || !active.Admission.Quarantined {
		t.Fatalf("quarantined source changed = %#v, %v", active, err)
	}
	publication, found := queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact != sourcePublication.Artifact {
		t.Fatalf("source Query publication changed = %#v", publication)
	}
	if _, err := manager.InspectRuntimeInstance(RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "instance-2"}); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("failed settings target survived source quarantine: %v", err)
	}
	assertRuntimeQuerySettingsPublicationLockReleased(t, boundary)
}

func TestRuntimeQuerySettingsRestoreRetriesTargetCleanupBeforeSourceAdmission(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	starter := newSettingsRestartMultiFailureStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourcePublication, _ := queries.SnapshotPublication(extension.ID)
	target := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "instance-2"}
	starter.failStop(source.Identity, 1, errors.New("source stop failed"))
	starter.failStop(target, 1, errors.New("first target cleanup failed"))

	restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if err := restart.RestartRuntimeQueriesForSettings(t.Context(), extension); err == nil {
		t.Fatal("settings restart unexpectedly succeeded")
	}
	if manager.RuntimeInstanceAvailable(source.Identity) || manager.RuntimeInstanceAvailable(target) {
		t.Fatal("failed restart left an owned runtime admitted")
	}
	if err := restart.RestoreRuntimeQueriesAfterSettingsRollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || active.Identity != source.Identity || !manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatalf("restored source = %#v, %v", active, err)
	}
	if _, err := manager.InspectRuntimeInstance(target); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("retained target survived restore: %v", err)
	}
	publication, found := queries.SnapshotPublication(extension.ID)
	if !found || publication.Artifact != sourcePublication.Artifact {
		t.Fatalf("restored Query publication = %#v", publication)
	}
}

func TestRuntimeQuerySettingsRestoreFailureQuarantinesOwnedRuntimes(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	starter := newSettingsRestartMultiFailureStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	target := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "instance-2"}
	starter.beforeStop = func(identity RuntimeInstanceIdentity) {
		if identity == source.Identity {
			_, _ = queries.ReplaceAll(nil, true)
		}
	}
	starter.failStop(source.Identity, 1, errors.New("source stop failed"))
	starter.failStop(target, 10, errors.New("target cleanup failed"))

	restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if err := restart.RestartRuntimeQueriesForSettings(t.Context(), extension); err == nil {
		t.Fatal("settings restart unexpectedly succeeded")
	}
	if err := restart.RestoreRuntimeQueriesAfterSettingsRollback(t.Context()); err == nil {
		t.Fatal("settings restore unexpectedly succeeded")
	}
	if manager.RuntimeInstanceAvailable(source.Identity) || manager.RuntimeInstanceAvailable(target) {
		t.Fatal("restore failure left an owned runtime admitted")
	}
	if err := restart.KeepRuntimeQueriesClosed(); err != nil {
		t.Fatalf("repeat keep-closed = %v", err)
	}
	assertRuntimeQuerySettingsPublicationLockReleased(t, boundary)
}

func TestRuntimeQuerySettingsKeepClosedRemovesOnlyExactFailedTargetPublication(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	target := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "settings-target"}
	desired, err := buildLifecycleQueryPublication(extension, extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
		RuntimeInstanceID: target.InstanceID,
	})
	if err != nil || desired == nil {
		t.Fatalf("build target publication: %#v, %v", desired, err)
	}

	for _, test := range []struct {
		name          string
		published     queryregistry.Publication
		wantRemaining bool
	}{
		{name: "exact target", published: *desired},
		{name: "newer runtime", published: func() queryregistry.Publication {
			newer := *desired
			newer.Artifact.RuntimeInstanceID = "newer-runtime"
			return newer
		}(), wantRemaining: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries := queryregistry.New()
			if _, err := queries.Publish(test.published); err != nil {
				t.Fatal(err)
			}
			rollbackErr := errors.New("query rollback CAS failed")
			transaction := &runtimeQuerySettingsRestartTransaction{
				boundary: &PostgresLifecycleBoundaryRegistries{queries: queries},
				source: RuntimeInstanceSnapshot{
					Identity:         RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "settings-source"},
					ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest,
				},
				target:        target,
				queryMutation: runtimeQuerySettingsMutationFailure{err: rollbackErr},
			}
			if err := transaction.closeTargetQueryPublicationLocked(); !errors.Is(err, rollbackErr) {
				t.Fatalf("close failed target publication = %v", err)
			}
			remaining, found := queries.SnapshotPublication(extension.ID)
			if found != test.wantRemaining {
				t.Fatalf("remaining publication found=%v value=%#v", found, remaining)
			}
			if found && remaining.Artifact != test.published.Artifact {
				t.Fatalf("newer publication changed: %#v", remaining)
			}
		})
	}
}

func TestRuntimeQuerySettingsRestartRejectsLifecycleV2(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.Manifest.Lifecycle = &extensions.ManifestLifecycle{ContractVersion: extension.ID + ".lifecycle@1"}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{Starter: newManagerStagedStarter()}), Queries: queryregistry.New(),
	})
	if _, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension); !errors.Is(err, extensions.ErrRuntimeQuerySettingsRestartUnavailable) {
		t.Fatalf("Lifecycle V2 settings restart = %v", err)
	}
}

func TestRuntimeQuerySettingsRestartRejectsExternalRegistrySurfaces(t *testing.T) {
	base := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	if runtimeQuerySettingsHasExternalSurfaces(base.Manifest) {
		t.Fatal("pure Query fixture unexpectedly requires aggregate restart")
	}
	allowed := base.Manifest
	allowed.Migrations = []extensions.ManifestMigration{{ID: "migration"}}
	allowed.PermissionDefinitions = []extensions.ManifestPermissionDefinition{{Key: "query.read"}}
	if runtimeQuerySettingsHasExternalSurfaces(allowed) {
		t.Fatal("static migration/permission declarations unexpectedly require aggregate runtime restart")
	}
	for _, test := range []struct {
		name   string
		mutate func(*extensions.Manifest)
	}{
		{name: "route", mutate: func(manifest *extensions.Manifest) { manifest.Routes = []extensions.ManifestRoute{{ID: "route"}} }},
		{name: "hook", mutate: func(manifest *extensions.Manifest) { manifest.Hooks = []extensions.ManifestHook{{ID: "hook"}} }},
		{name: "job", mutate: func(manifest *extensions.Manifest) { manifest.Jobs = []extensions.ManifestJob{{ID: "job"}} }},
		{name: "component", mutate: func(manifest *extensions.Manifest) {
			manifest.Components = []extensions.ManifestComponent{{ID: "component"}}
		}},
		{name: "cache", mutate: func(manifest *extensions.Manifest) { manifest.Cache = []extensions.ManifestCache{{ID: "cache"}} }},
		{name: "seo", mutate: func(manifest *extensions.Manifest) { manifest.SEO = []extensions.ManifestSEO{{ID: "seo"}} }},
		{name: "identity", mutate: func(manifest *extensions.Manifest) {
			manifest.Identity = &extensions.ManifestIdentity{ContractVersion: "identity@1"}
		}},
		{name: "database", mutate: func(manifest *extensions.Manifest) {
			manifest.Database = &extensions.ManifestDatabase{ContractVersion: "database@1"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := base.Manifest
			test.mutate(&manifest)
			if !runtimeQuerySettingsHasExternalSurfaces(manifest) {
				t.Fatalf("%s surface escaped aggregate restart fence", test.name)
			}
		})
	}
}

func TestRuntimeQuerySettingsRestartPreflightRejectsPluginPages(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	if err := os.WriteFile(extension.PackagePath+"/theme.json", []byte(`{
		"pages":[{"id":"query.page","action":"add","path":"/query","contract":"query.page@1","access":"public"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queryregistry.New(),
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension); !errors.Is(err, extensions.ErrRuntimeQuerySettingsRestartUnavailable) {
		t.Fatalf("plugin page settings preflight = %v", err)
	}
}

func TestRuntimeQuerySettingsRestartRequiresExactPackageRoot(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: NewManager(ManagerConfig{Starter: newManagerStagedStarter()}), Queries: queryregistry.New(),
	})
	if _, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension); !errors.Is(err, extensions.ErrRuntimeQuerySettingsRestartUnavailable) {
		t.Fatalf("missing package root settings preflight = %v", err)
	}
}

func TestRuntimeQuerySettingsPrepareBoundsManagerBarrierWait(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}

	release, err := manager.lockRuntimeSetTransition(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	_, prepareErr := boundary.PrepareRuntimeQueriesForSettings(ctx, extension)
	cancel()
	release()
	if !errors.Is(prepareErr, context.DeadlineExceeded) || !manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatalf("bounded prepare = %v, source available=%t", prepareErr, manager.RuntimeInstanceAvailable(source.Identity))
	}

	// A timed-out waiter must release publicationMu as well as the Manager wait.
	restart, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if err := restart.RestoreRuntimeQueriesAfterSettingsRollback(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeQuerySettingsPrepareQuarantinesWhenSourceCannotResume(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.AcquireRuntimeCall(t.Context(), source.Identity, RuntimeCallProvider)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, prepareErr := boundary.PrepareRuntimeQueriesForSettings(ctx, extension)
		done <- prepareErr
	}()
	waitRuntimeQuerySettingsDrain(t, manager, source.Identity)
	forceCause := errors.New("source resume denied")
	if _, err := manager.ForceDrain(source.Identity, forceCause); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, forceCause) {
		t.Fatalf("prepare resume failure = %v", err)
	}
	snapshot, err := manager.InspectRuntimeInstance(source.Identity)
	if err != nil || !snapshot.Admission.Forced || !snapshot.Admission.Quarantined ||
		manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatalf("failed source compensation = %#v, %v", snapshot, err)
	}
	assertRuntimeQuerySettingsPublicationLockReleased(t, boundary)
}

func TestRuntimeQuerySettingsPrepareDoubleFailureQuarantinesSource(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	queries := queryregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queries,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	source, lease, err := manager.AcquireActiveRuntimeCall(t.Context(), extension.ID, RuntimeCallProvider)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	ctx, cancel := context.WithCancel(t.Context())
	type prepareResult struct {
		restart extensions.RuntimeQuerySettingsRestartTransaction
		err     error
	}
	result := make(chan prepareResult, 1)
	go func() {
		restart, prepareErr := boundary.PrepareRuntimeQueriesForSettings(ctx, extension)
		result <- prepareResult{restart: restart, err: prepareErr}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, inspectErr := manager.InspectRuntimeInstance(source.Identity)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if snapshot.Admission.Draining {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("settings prepare did not drain the source runtime")
		}
		time.Sleep(time.Millisecond)
	}
	quarantineCause := errors.New("concurrent runtime quarantine")
	if _, err := manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}, quarantineCause); err != nil {
		t.Fatal(err)
	}
	cancel()

	select {
	case prepared := <-result:
		if prepared.restart != nil || !errors.Is(prepared.err, context.Canceled) ||
			!errors.Is(prepared.err, ErrRuntimeAdmissionQuarantined) || !errors.Is(prepared.err, quarantineCause) {
			t.Fatalf("double-failure prepare restart=%#v err=%v", prepared.restart, prepared.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("double-failure settings prepare did not return")
	}
	snapshot, err := manager.InspectRuntimeInstance(source.Identity)
	if err != nil || !snapshot.Admission.Quarantined || manager.RuntimeInstanceAvailable(source.Identity) {
		t.Fatalf("double-failure source = %#v, %v", snapshot, err)
	}
	assertRuntimeQuerySettingsPublicationLockReleased(t, boundary)
}

func TestRuntimeQuerySettingsRestartRejectsActivePageSnapshot(t *testing.T) {
	extension := legacyRuntimeQueryExtension("1.0.0", 'a', 41)
	extension.PackagePath = t.TempDir()
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	runtime, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	pageRegistry := pages.NewRegistry(nil)
	if _, err := pageRegistry.PublishExtensionIfRevision(pages.RuntimeArtifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, RuntimeInstanceID: runtime.Identity.InstanceID,
	}, nil, 0); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Manager: manager, Queries: queryregistry.New(), Pages: pageRegistry,
	})
	if _, err := boundary.PublishRuntimeQueries(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.PrepareRuntimeQueriesForSettings(t.Context(), extension); !errors.Is(err, extensions.ErrRuntimeQuerySettingsRestartUnavailable) {
		t.Fatalf("active page snapshot settings preflight = %v", err)
	}
}

func waitSettingsRuntimePublish(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime publication")
	}
}

func waitRuntimeQuerySettingsDrain(t *testing.T, manager *Manager, identity RuntimeInstanceIdentity) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		snapshot, err := manager.InspectRuntimeInstance(identity)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Admission.Draining {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("timed out waiting for settings source drain")
		}
	}
}

func assertRuntimeQuerySettingsPublicationLockReleased(t *testing.T, boundary *PostgresLifecycleBoundaryRegistries) {
	t.Helper()
	locked := make(chan struct{})
	go func() {
		boundary.publicationMu.Lock()
		boundary.publicationMu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("settings transaction retained publication lock")
	}
}

type settingsRestartMultiFailureStarter struct {
	*managerStagedStarter
	failureMu   sync.Mutex
	stopErrors  map[RuntimeInstanceIdentity]error
	stopFailure map[RuntimeInstanceIdentity]int
	beforeStop  func(RuntimeInstanceIdentity)
}

func newSettingsRestartMultiFailureStarter() *settingsRestartMultiFailureStarter {
	return &settingsRestartMultiFailureStarter{
		managerStagedStarter: newManagerStagedStarter(),
		stopErrors:           make(map[RuntimeInstanceIdentity]error),
		stopFailure:          make(map[RuntimeInstanceIdentity]int),
	}
}

func (s *settingsRestartMultiFailureStarter) StopInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	s.failureMu.Lock()
	beforeStop := s.beforeStop
	remaining := s.stopFailure[identity]
	err := s.stopErrors[identity]
	if remaining > 0 {
		s.stopFailure[identity] = remaining - 1
	}
	s.failureMu.Unlock()
	if beforeStop != nil {
		beforeStop(identity)
	}
	if remaining > 0 {
		return err
	}
	return s.managerStagedStarter.StopInstance(ctx, identity)
}

func (s *settingsRestartMultiFailureStarter) failStop(identity RuntimeInstanceIdentity, count int, err error) {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	s.stopFailure[identity] = count
	s.stopErrors[identity] = err
}

type settingsRestartStopFailureStarter struct {
	*managerStagedStarter
	failIdentity RuntimeInstanceIdentity
	stopErr      error
}

func (s *settingsRestartStopFailureStarter) StopInstance(ctx context.Context, identity RuntimeInstanceIdentity) error {
	if s.stopErr != nil && identity == s.failIdentity {
		return s.stopErr
	}
	return s.managerStagedStarter.StopInstance(ctx, identity)
}

type runtimeQuerySettingsMutationFailure struct {
	err error
}

func (m runtimeQuerySettingsMutationFailure) Rollback() error {
	return m.err
}

var _ StagedRuntimeStarter = (*settingsRestartStopFailureStarter)(nil)
var _ StagedRuntimeStarter = (*settingsRestartMultiFailureStarter)(nil)

var _ extensions.RuntimeQuerySettingsRestarter = (*PostgresLifecycleBoundaryRegistries)(nil)
