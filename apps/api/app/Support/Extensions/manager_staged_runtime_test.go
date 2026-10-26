package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestManagerStagesPublishesAndRollsBackExactRuntimeInstances(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	oldExtension := managerStagedExtension("staged.manager", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), oldExtension); err != nil {
		t.Fatal(err)
	}
	old, err := manager.ActiveRuntimeInstance(oldExtension.ID)
	if err != nil {
		t.Fatal(err)
	}

	newExtension := managerStagedExtension(oldExtension.ID, "2.0.0", "digest-2")
	staged, err := manager.StageRuntimeInstance(context.Background(), newExtension)
	if err != nil {
		t.Fatal(err)
	}
	if staged.Active || staged.ExtensionVersion != "2.0.0" || staged.ArtifactDigest != "digest-2" {
		t.Fatalf("staged snapshot = %#v", staged)
	}
	if active, _ := manager.ActiveRuntimeInstance(oldExtension.ID); active.Identity != old.Identity {
		t.Fatalf("staging changed active identity: %#v", active)
	}
	if _, err := manager.AcquireRuntimeCall(context.Background(), staged.Identity, RuntimeCallRoute); !errors.Is(err, ErrRuntimeInstanceNotActive) {
		t.Fatalf("inactive ordinary admission = %v", err)
	}
	cleanup := acquireManagerRuntimeCall(t, manager, staged.Identity, RuntimeCallLifecycleCleanup)
	cleanup.Release()
	if _, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstance(context.Background(), staged.Identity); !errors.Is(err, ErrRuntimeInstanceNotDrained) {
		t.Fatalf("publish before old drain = %v", err)
	}
	if _, err := manager.BeginDrain(old.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.WaitDrain(context.Background(), old.Identity); err != nil {
		t.Fatal(err)
	}
	published, err := manager.PublishRuntimeInstance(context.Background(), staged.Identity)
	if err != nil || !published.Active || published.Identity != staged.Identity || published.Admission.Draining {
		t.Fatalf("published snapshot = %#v, %v", published, err)
	}
	retained, err := manager.InspectRuntimeInstance(old.Identity)
	if err != nil || retained.Active || !retained.Admission.Draining {
		t.Fatalf("retained old snapshot = %#v, %v", retained, err)
	}

	if _, err := manager.BeginDrain(staged.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.WaitDrain(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := manager.PublishRuntimeInstance(context.Background(), old.Identity)
	if err != nil || !rolledBack.Active || rolledBack.Identity != old.Identity || rolledBack.Admission.Draining {
		t.Fatalf("rollback snapshot = %#v, %v", rolledBack, err)
	}
	replacement, err := manager.InspectRuntimeInstance(staged.Identity)
	if err != nil || replacement.Active || !replacement.Admission.Draining {
		t.Fatalf("rolled-back replacement = %#v, %v", replacement, err)
	}
	if got := starter.activeIdentity(oldExtension.ID); got != old.Identity {
		t.Fatalf("protocol active identity = %#v", got)
	}
}

func TestManagerPublishFromRejectsQuarantinedExactSource(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	sourceExtension := managerStagedExtension("source-cas.manager", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), sourceExtension); err != nil {
		t.Fatal(err)
	}
	source, err := manager.ActiveRuntimeInstance(sourceExtension.ID)
	if err != nil {
		t.Fatal(err)
	}
	target, err := manager.StageRuntimeInstance(t.Context(), managerStagedExtension(sourceExtension.ID, "2.0.0", "digest-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(t.Context(), target.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(source.Identity); err != nil {
		t.Fatal(err)
	}
	revoked := errors.New("exact trust revoked")
	if _, err := manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}, revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstanceFrom(t.Context(), target.Identity, RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}); !errors.Is(err, ErrRuntimeInstanceConflict) {
		t.Fatalf("publish from quarantined source = %v", err)
	}
	active, err := manager.ActiveRuntimeInstance(sourceExtension.ID)
	if err != nil || active.Identity != source.Identity || !active.Admission.Quarantined {
		t.Fatalf("quarantined source changed = %#v, %v", active, err)
	}
	if got := starter.activeIdentity(sourceExtension.ID); got != source.Identity {
		t.Fatalf("protocol target bypassed source CAS: %#v", got)
	}
}

func TestManagerPublishFromRollsProtocolBackWhenSourceIsQuarantinedDuringPublish(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	sourceExtension := managerStagedExtension("source-post-cas.manager", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), sourceExtension); err != nil {
		t.Fatal(err)
	}
	source, _ := manager.ActiveRuntimeInstance(sourceExtension.ID)
	target, err := manager.StageRuntimeInstance(t.Context(), managerStagedExtension(sourceExtension.ID, "2.0.0", "digest-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(t.Context(), target.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(source.Identity); err != nil {
		t.Fatal(err)
	}
	starter.publishStarted = make(chan struct{}, 2)
	starter.publishContinue = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, publishErr := manager.PublishRuntimeInstanceFrom(t.Context(), target.Identity, RuntimeInstanceArtifactIdentity{
			RuntimeInstanceIdentity: source.Identity,
			ExtensionVersion:        source.ExtensionVersion,
			ArtifactDigest:          source.ArtifactDigest,
		})
		result <- publishErr
	}()
	<-starter.publishStarted
	revoked := errors.New("trust revoked during protocol publish")
	if _, err := manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}, revoked); err != nil {
		t.Fatal(err)
	}
	close(starter.publishContinue)
	if err := <-result; !errors.Is(err, ErrRuntimeInstanceConflict) {
		t.Fatalf("post-publish source CAS = %v", err)
	}
	active, err := manager.ActiveRuntimeInstance(sourceExtension.ID)
	if err != nil || active.Identity != source.Identity || !active.Admission.Quarantined {
		t.Fatalf("post-publish source changed = %#v, %v", active, err)
	}
	if got := starter.activeIdentity(sourceExtension.ID); got != source.Identity {
		t.Fatalf("protocol rollback identity = %#v", got)
	}
}

func TestManagerPublishFromQuarantinesSourceWhenProtocolRollbackFails(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	sourceExtension := managerStagedExtension("source-rollback-failure.manager", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), sourceExtension); err != nil {
		t.Fatal(err)
	}
	source, _ := manager.ActiveRuntimeInstance(sourceExtension.ID)
	target, err := manager.StageRuntimeInstance(t.Context(), managerStagedExtension(sourceExtension.ID, "2.0.0", "digest-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(t.Context(), target.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(source.Identity); err != nil {
		t.Fatal(err)
	}
	starter.publishStarted = make(chan struct{}, 2)
	starter.publishContinue = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, publishErr := manager.PublishRuntimeInstanceFrom(t.Context(), target.Identity, RuntimeInstanceArtifactIdentity{
			RuntimeInstanceIdentity: source.Identity,
			ExtensionVersion:        source.ExtensionVersion,
			ArtifactDigest:          source.ArtifactDigest,
		})
		result <- publishErr
	}()
	<-starter.publishStarted
	targetFailure := errors.New("target quarantined during publish")
	if _, err := manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: target.Identity,
		ExtensionVersion:        target.ExtensionVersion,
		ArtifactDigest:          target.ArtifactDigest,
	}, targetFailure); err != nil {
		t.Fatal(err)
	}
	rollbackFailure := errors.New("source protocol rollback failed")
	starter.failPublish(source.Identity, rollbackFailure)
	close(starter.publishContinue)
	if err := <-result; !errors.Is(err, ErrRuntimeInstanceConflict) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("failed protocol rollback = %v", err)
	}
	active, err := manager.ActiveRuntimeInstance(sourceExtension.ID)
	if err != nil || active.Identity != source.Identity || !active.Admission.Quarantined ||
		!errors.Is(active.Admission.QuarantineCause, rollbackFailure) {
		t.Fatalf("source after failed protocol rollback = %#v, %v", active, err)
	}
	if got := starter.activeIdentity(sourceExtension.ID); got != target.Identity {
		t.Fatalf("failed rollback unexpectedly changed protocol identity = %#v", got)
	}
}

func TestManagerPublishIfNoActiveNeverOverwritesReplacement(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	sourceExtension := managerStagedExtension("restore-cas.manager", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), sourceExtension); err != nil {
		t.Fatal(err)
	}
	source, _ := manager.ActiveRuntimeInstance(sourceExtension.ID)
	replacement, err := manager.StageRuntimeInstance(t.Context(), managerStagedExtension(sourceExtension.ID, "2.0.0", "digest-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(t.Context(), replacement.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(source.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstanceFrom(t.Context(), replacement.Identity, RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: source.Identity,
		ExtensionVersion:        source.ExtensionVersion,
		ArtifactDigest:          source.ArtifactDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstanceIfNoActive(t.Context(), source.Identity); !errors.Is(err, ErrRuntimeInstanceConflict) {
		t.Fatalf("restore over active replacement = %v", err)
	}
	active, err := manager.ActiveRuntimeInstance(sourceExtension.ID)
	if err != nil || active.Identity != replacement.Identity {
		t.Fatalf("active replacement changed = %#v, %v", active, err)
	}
}

func TestManagerPreparesDatabaseCatalogBeforeActiveAndStagedStarts(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	prepared := []string{}
	manager.SetStartPreparer(func(_ context.Context, extension extensions.Extension) error {
		prepared = append(prepared, extension.Version)
		return nil
	})
	active := managerStagedExtension("prepared.manager", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	staged := managerStagedExtension(active.ID, "2.0.0", "digest-2")
	if _, err := manager.StageRuntimeInstance(context.Background(), staged); err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 2 || prepared[0] != "1.0.0" || prepared[1] != "2.0.0" {
		t.Fatalf("prepared versions = %#v", prepared)
	}
}

func TestManagerExactStopAndDiscardNeverRemoveReplacement(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("remove.staged", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	active, _ := manager.ActiveRuntimeInstance(extension.ID)

	candidateExtension := managerStagedExtension(extension.ID, "2.0.0", "digest-2")
	candidate, err := manager.StageRuntimeInstance(context.Background(), candidateExtension)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DiscardRuntimeInstance(context.Background(), candidate.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InspectRuntimeInstance(candidate.Identity); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("discarded Manager identity = %v", err)
	}
	if status := manager.Status(context.Background(), candidateExtension); status.State != extensions.RuntimeRunning {
		t.Fatalf("discarded upgrade candidate changed active status: %#v", status)
	}
	if got := starter.activeIdentity(extension.ID); got != active.Identity {
		t.Fatalf("discard changed active protocol identity: %#v", got)
	}
	if err := manager.DiscardRuntimeInstance(context.Background(), active.Identity); !errors.Is(err, ErrRuntimeInstanceActive) {
		t.Fatalf("active discard = %v", err)
	}
	if err := manager.StopRuntimeInstance(context.Background(), active.Identity); !errors.Is(err, ErrRuntimeInstanceNotDrained) {
		t.Fatalf("undrained stop = %v", err)
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopRuntimeInstance(context.Background(), active.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stopped active lookup = %v", err)
	}
}

func TestManagerConvergesAfterExactProtocolProcessExit(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("crashed.staged", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	crashed, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	starter.crash(crashed.Identity)

	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("dead exact runtime remained active: %v", err)
	}
	if status := manager.Status(t.Context(), extension); status.State != extensions.RuntimeFailed || status.LastError == "" {
		t.Fatalf("dead exact runtime status = %#v", status)
	}
	if _, err := manager.BeginDrain(crashed.Identity); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopRuntimeInstance(t.Context(), crashed.Identity); err != nil {
		t.Fatalf("already-exited runtime did not converge: %v", err)
	}
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatalf("restart after crash: %v", err)
	}
	restarted, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || restarted.Identity == crashed.Identity {
		t.Fatalf("restart did not publish a fresh exact runtime: %#v, %v", restarted, err)
	}
}

func TestManagerStopsDrainedRuntimeAfterRegistryDeactivation(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("deactivated.staged", "1.0.0", "digest-1")
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	if !manager.HookBus().UnregisterRuntime(extension.ID, active.Identity.InstanceID) {
		t.Fatal("deactivation did not close the exact hook registry")
	}
	if err := manager.StopRuntimeInstance(t.Context(), active.Identity); err != nil {
		t.Fatalf("stop retained runtime after registry deactivation: %v", err)
	}
	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("stopped runtime remained active: %v", err)
	}
}

func TestManagerDiscardedInitialCandidateReturnsToStoppedStatus(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("initial.staged", "1.0.0", "digest-1")
	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(context.Background(), extension); status.State != extensions.RuntimeStarting {
		t.Fatalf("staged status = %#v", status)
	}
	if err := manager.DiscardRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status(context.Background(), extension); status.State != extensions.RuntimeStopped {
		t.Fatalf("discarded status = %#v", status)
	}
}

func TestManagerPublishRejectsConcurrentLifecycleCleanup(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("busy.staged", "1.0.0", "digest-1")
	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	cleanup := acquireManagerRuntimeCall(t, manager, staged.Identity, RuntimeCallLifecycleCleanup)
	if _, err := manager.PublishRuntimeInstance(context.Background(), staged.Identity); !errors.Is(err, ErrRuntimeInstanceBusy) {
		t.Fatalf("publish with cleanup = %v", err)
	}
	if err := manager.DiscardRuntimeInstance(context.Background(), staged.Identity); !errors.Is(err, ErrRuntimeInstanceBusy) {
		t.Fatalf("discard with cleanup = %v", err)
	}
	cleanup.Release()
	if err := manager.DiscardRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPublishBlocksNewSourceAndTargetCleanupAcrossPointerSwitch(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	source := managerStagedExtension("switch.staged", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	active, _ := manager.ActiveRuntimeInstance(source.ID)
	target := managerStagedExtension(source.ID, "2.0.0", "digest-2")
	staged, err := manager.StageRuntimeInstance(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(active.Identity); err != nil {
		t.Fatal(err)
	}
	starter.publishStarted = make(chan struct{}, 1)
	starter.publishContinue = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := manager.PublishRuntimeInstance(context.Background(), staged.Identity)
		result <- err
	}()
	<-starter.publishStarted
	if _, err := manager.AcquireRuntimeCall(context.Background(), active.Identity, RuntimeCallLifecycleCleanup); !errors.Is(err, ErrRuntimeInstanceBusy) {
		t.Fatalf("source cleanup during switch = %v", err)
	}
	if _, err := manager.AcquireRuntimeCall(context.Background(), staged.Identity, RuntimeCallLifecycleCleanup); !errors.Is(err, ErrRuntimeInstanceBusy) {
		t.Fatalf("target cleanup during switch = %v", err)
	}
	close(starter.publishContinue)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestManagerFailedRollbackPublishRestoresRetainedDrain(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	source := managerStagedExtension("failed.rollback", "1.0.0", "digest-1")
	if err := manager.Start(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	old, _ := manager.ActiveRuntimeInstance(source.ID)
	target := managerStagedExtension(source.ID, "2.0.0", "digest-2")
	staged, err := manager.StageRuntimeInstance(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(old.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublishRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginDrain(staged.Identity); err != nil {
		t.Fatal(err)
	}
	starter.failPublish(old.Identity, errors.New("registry publication failed"))
	if _, err := manager.PublishRuntimeInstance(context.Background(), old.Identity); err == nil {
		t.Fatal("expected rollback publication failure")
	}
	retained, err := manager.InspectRuntimeInstance(old.Identity)
	if err != nil || retained.Active || !retained.Admission.Draining {
		t.Fatalf("failed rollback reopened retained gate: %#v, %v", retained, err)
	}
	active, err := manager.ActiveRuntimeInstance(source.ID)
	if err != nil || active.Identity != staged.Identity || !active.Admission.Draining {
		t.Fatalf("failed rollback changed current identity: %#v, %v", active, err)
	}
}

func TestManagerHealthRejectsFrozenManifestDrift(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerStagedExtension("manifest-drift.staged", "1.0.0", "digest-1")
	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.DiscardRuntimeInstance(context.Background(), staged.Identity) })

	starter.mu.Lock()
	protocolSnapshot := starter.instances[staged.Identity]
	protocolSnapshot.ManifestDigest = "different-manifest"
	starter.instances[staged.Identity] = protocolSnapshot
	starter.mu.Unlock()

	if _, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity); !errors.Is(err, ErrRuntimeInstanceConflict) {
		t.Fatalf("health manifest drift = %v", err)
	}
	managed, err := manager.InspectRuntimeInstance(staged.Identity)
	if err != nil || managed.Active {
		t.Fatalf("manifest drift changed Manager state: %#v, %v", managed, err)
	}
}

// Protocol V2 process staging must not require optional Lifecycle V2 declarations.
// Genesis plugins such as sforum.admin-surface-reference are valid process plugins
// without lifecycle actions.
func TestManagerStagesProtocolV2ProcessWithoutLifecycle(t *testing.T) {
	extension := managerStagedExtension("process.no-lifecycle", "1.0.0", "digest-process")
	extension.Manifest.Lifecycle = nil
	if err := validateManagedStagedExtension(extension); err != nil {
		t.Fatalf("validate without lifecycle: %v", err)
	}

	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	staged, err := manager.StageRuntimeInstance(context.Background(), extension)
	if err != nil {
		t.Fatalf("stage Protocol V2 without lifecycle: %v", err)
	}
	t.Cleanup(func() { _ = manager.DiscardRuntimeInstance(context.Background(), staged.Identity) })
	if staged.Active || staged.ExtensionVersion != "1.0.0" || staged.ArtifactDigest != "digest-process" {
		t.Fatalf("staged snapshot = %#v", staged)
	}
	if _, err := manager.HealthRuntimeInstance(context.Background(), staged.Identity); err != nil {
		t.Fatalf("health Protocol V2 without lifecycle: %v", err)
	}
}

func TestValidateManagedStagedExtensionRejectsMalformedOrNonV2(t *testing.T) {
	valid := managerStagedExtension("process.valid", "1.0.0", "digest-valid")
	valid.Manifest.Lifecycle = nil
	if err := validateManagedStagedExtension(valid); err != nil {
		t.Fatalf("valid process artifact rejected: %v", err)
	}

	tests := map[string]func(extensions.Extension) extensions.Extension{
		"unsupported protocol 1": func(extension extensions.Extension) extensions.Extension {
			extension.Manifest.Backend.ProtocolVersion = 1
			return extension
		},
		"empty protocol": func(extension extensions.Extension) extensions.Extension {
			extension.Manifest.Backend.ProtocolVersion = 0
			return extension
		},
		"id mismatch": func(extension extensions.Extension) extensions.Extension {
			extension.Manifest.ID = "other.id"
			return extension
		},
		"version mismatch": func(extension extensions.Extension) extensions.Extension {
			extension.Manifest.Version = "9.9.9"
			return extension
		},
		"empty digest": func(extension extensions.Extension) extensions.Extension {
			extension.PackageDigest = ""
			return extension
		},
		"theme type": func(extension extensions.Extension) extensions.Extension {
			extension.Type = extensions.TypeTheme
			extension.Manifest.Type = extensions.TypeTheme
			return extension
		},
		"whitespace id": func(extension extensions.Extension) extensions.Extension {
			extension.ID = " process.valid "
			extension.Manifest.ID = " process.valid "
			return extension
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateManagedStagedExtension(mutate(valid)); !errors.Is(err, ErrRuntimeAdmissionInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// Lifecycle paths remain fail-closed when Manifest.lifecycle is omitted.
func TestLifecycleCallRejectsProtocolV2ProcessWithoutLifecycleBeforeRemote(t *testing.T) {
	if _, _, _, err := lifecycleOperationContract(nil, LifecycleActionEnable); !errors.Is(err, ErrInvalidLifecycleRun) {
		t.Fatalf("nil lifecycle contract error = %v", err)
	}

	request := exactCoordinatorTestRequest(
		t, extensions.LifecycleMachineEnable, extensions.LifecycleMachineEnableAction, 2, extensions.LifecycleRuntimeTarget,
	)
	// Exact process plugin without optional lifecycle declaration.
	request.TargetExtension.Manifest.Lifecycle = nil
	request.Extension.Manifest.Lifecycle = nil
	runnerCalls := 0
	adapter := &ExactLifecycleCoordinatorRuntimeAdapter{
		admission: exactLifecycleCoordinatorAdmissionFunc(func(
			context.Context, RuntimeInstanceIdentity, RuntimeCallClass,
		) (*RuntimeAdmissionLease, error) {
			t.Fatal("lifecycle admission must not run without a declared lifecycle contract")
			return nil, nil
		}),
		runner: exactLifecycleCoordinatorRunnerFunc(func(
			context.Context, RuntimeInstanceIdentity, extensions.Extension, LifecycleInvocation,
		) (LifecycleRunResult, error) {
			runnerCalls++
			return LifecycleRunResult{}, nil
		}),
	}
	if _, err := adapter.RunLifecycleAction(context.Background(), request, nil); !errors.Is(err, ErrInvalidLifecycleRun) {
		t.Fatalf("lifecycle call without declaration = %v", err)
	}
	if runnerCalls != 0 {
		t.Fatalf("remote lifecycle runner calls = %d", runnerCalls)
	}
}

type managerStagedStarter struct {
	mu        sync.Mutex
	next      int
	instances map[RuntimeInstanceIdentity]ProtocolRuntimeInstanceSnapshot
	active    map[string]RuntimeInstanceIdentity

	publishStarted  chan struct{}
	publishContinue chan struct{}
	publishErrors   map[RuntimeInstanceIdentity]error
}

func newManagerStagedStarter() *managerStagedStarter {
	return &managerStagedStarter{
		instances: make(map[RuntimeInstanceIdentity]ProtocolRuntimeInstanceSnapshot),
		active:    make(map[string]RuntimeInstanceIdentity),
	}
}

func (s *managerStagedStarter) Start(ctx context.Context, extension extensions.Extension) (RouteTarget, error) {
	target, err := s.StartInstance(ctx, extension)
	if err != nil {
		return RouteTarget{}, err
	}
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}
	if _, err := s.HealthInstance(ctx, identity); err != nil {
		return RouteTarget{}, err
	}
	if _, err := s.PublishInstance(ctx, identity); err != nil {
		return RouteTarget{}, err
	}
	return target, nil
}

func (s *managerStagedStarter) Stop(_ context.Context, extension extensions.Extension) error {
	s.mu.Lock()
	identity := s.active[extension.ID]
	s.mu.Unlock()
	if identity.InstanceID == "" {
		return nil
	}
	return s.StopInstance(context.Background(), identity)
}

func (s *managerStagedStarter) StartInstance(_ context.Context, extension extensions.Extension) (RouteTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: fmt.Sprintf("instance-%d", s.next)}
	target := RouteTarget{BaseURL: fmt.Sprintf("http://127.0.0.1:%d", 44000+s.next), InstanceID: identity.InstanceID}
	manifestDigest, err := protocolRuntimeManifestDigest(extension.Manifest)
	if err != nil {
		return RouteTarget{}, err
	}
	s.instances[identity] = ProtocolRuntimeInstanceSnapshot{
		Identity: identity, ExtensionVersion: extension.Version, ArtifactDigest: extension.PackageDigest,
		ManifestDigest: manifestDigest, ProtocolVersion: 2, Target: target, State: ProtocolRuntimeStaged, Healthy: true,
	}
	return target, nil
}

func (s *managerStagedStarter) InspectInstance(identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.instances[identity]
	if !ok {
		return ProtocolRuntimeInstanceSnapshot{}, ErrRuntimeInstanceNotFound
	}
	return snapshot, nil
}

func (s *managerStagedStarter) HealthInstance(_ context.Context, identity RuntimeInstanceIdentity) (PluginHealth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.instances[identity]
	if !ok {
		return PluginHealth{}, ErrRuntimeInstanceNotFound
	}
	snapshot.Healthy = true
	snapshot.Ready = true
	snapshot.ReadinessChecked = true
	s.instances[identity] = snapshot
	return PluginHealth{OK: true}, nil
}

func (s *managerStagedStarter) PublishInstance(_ context.Context, identity RuntimeInstanceIdentity) (ProtocolRuntimeInstanceSnapshot, error) {
	if s.publishStarted != nil {
		s.publishStarted <- struct{}{}
		<-s.publishContinue
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.publishErrors[identity]; err != nil {
		return ProtocolRuntimeInstanceSnapshot{}, err
	}
	snapshot, ok := s.instances[identity]
	if !ok {
		return ProtocolRuntimeInstanceSnapshot{}, ErrRuntimeInstanceNotFound
	}
	if !snapshot.Ready {
		return ProtocolRuntimeInstanceSnapshot{}, ErrProtocolInstanceNotReady
	}
	if previous := s.active[identity.ExtensionID]; previous.InstanceID != "" && previous != identity {
		old := s.instances[previous]
		old.State = ProtocolRuntimeRetained
		s.instances[previous] = old
	}
	snapshot.State = ProtocolRuntimePublished
	s.instances[identity] = snapshot
	s.active[identity.ExtensionID] = identity
	return snapshot, nil
}

func (s *managerStagedStarter) failPublish(identity RuntimeInstanceIdentity, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publishErrors == nil {
		s.publishErrors = make(map[RuntimeInstanceIdentity]error)
	}
	s.publishErrors[identity] = err
}

func (*managerStagedStarter) RunLifecycleInstance(context.Context, RuntimeInstanceIdentity, extensions.Extension, LifecycleInvocation) (LifecycleRunResult, error) {
	return LifecycleRunResult{}, nil
}

func (s *managerStagedStarter) StopInstance(_ context.Context, identity RuntimeInstanceIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.instances[identity]; !ok {
		return ErrRuntimeInstanceNotFound
	}
	delete(s.instances, identity)
	if s.active[identity.ExtensionID] == identity {
		delete(s.active, identity.ExtensionID)
	}
	return nil
}

func (s *managerStagedStarter) DiscardInstance(_ context.Context, identity RuntimeInstanceIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.instances[identity]
	if !ok {
		return ErrRuntimeInstanceNotFound
	}
	if snapshot.State != ProtocolRuntimeStaged {
		return ErrProtocolInstancePublished
	}
	delete(s.instances, identity)
	return nil
}

func (s *managerStagedStarter) activeIdentity(extensionID string) RuntimeInstanceIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[extensionID]
}

func (s *managerStagedStarter) crash(identity RuntimeInstanceIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.instances, identity)
	if s.active[identity.ExtensionID] == identity {
		delete(s.active, identity.ExtensionID)
	}
}

func managerStagedExtension(id, version, digest string) extensions.Extension {
	extension := managerRuntimeExtension(id, version, digest)
	extension.Manifest.Backend.ProtocolVersion = 2
	extension.Manifest.Backend.HostAPIVersion = "sforum.host.v2"
	extension.Manifest.Lifecycle = &extensions.ManifestLifecycle{ContractVersion: "demo.lifecycle@1"}
	return extension
}

var _ StagedRuntimeStarter = (*managerStagedStarter)(nil)
