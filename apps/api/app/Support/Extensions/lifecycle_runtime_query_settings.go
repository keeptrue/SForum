package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

const runtimeQuerySettingsCompensationTimeout = 5 * time.Second

type runtimeQuerySettingsRestartTransaction struct {
	mu            sync.Mutex
	boundary      *PostgresLifecycleBoundaryRegistries
	source        RuntimeInstanceSnapshot
	target        RuntimeInstanceIdentity
	queryMutation extensions.RuntimeQueryPublicationMutation
	lockHeld      bool
	done          bool
}

// PrepareRuntimeQueriesForSettings closes source admission before Service
// persists new settings. The returned transaction owns publicationMu until it
// either commits the exact replacement or restores/keeps the source closed.
func (b *PostgresLifecycleBoundaryRegistries) PrepareRuntimeQueriesForSettings(
	ctx context.Context,
	extension extensions.Extension,
) (extensions.RuntimeQuerySettingsRestartTransaction, error) {
	if err := validateRuntimeQuerySettingsRestartInput(b, ctx, extension); err != nil {
		return nil, err
	}
	b.publicationMu.Lock()
	source, err := b.preflightRuntimeQuerySettingsRestartLocked(extension)
	if err != nil {
		b.publicationMu.Unlock()
		return nil, err
	}
	if _, err := b.manager.BeginDrainContext(ctx, source.Identity); err != nil {
		b.publicationMu.Unlock()
		return nil, fmt.Errorf("drain source settings runtime: %w", err)
	}
	transaction := &runtimeQuerySettingsRestartTransaction{
		boundary: b, source: source, lockHeld: true,
	}
	if err := b.manager.WaitDrain(ctx, source.Identity); err != nil {
		compensationCtx, cancel := runtimeQuerySettingsCompensationContext(ctx)
		_, resumeErr := b.manager.ResumeRuntimeInstanceContext(compensationCtx, source.Identity)
		cancel()
		waitErr := fmt.Errorf("wait source settings runtime drain: %w", err)
		if resumeErr == nil {
			transaction.finishLocked()
			return nil, waitErr
		}
		closeErr := transaction.KeepRuntimeQueriesClosed()
		return nil, errors.Join(waitErr, fmt.Errorf("resume source settings runtime: %w", resumeErr), closeErr)
	}
	return transaction, nil
}

// RestartRuntimeQueriesForSettings publishes the replacement only while the
// source remains drained. Any error leaves source admission closed so Service
// can restore the old setting document before reopening it.
func (t *runtimeQuerySettingsRestartTransaction) RestartRuntimeQueriesForSettings(
	ctx context.Context,
	extension extensions.Extension,
) error {
	if t == nil {
		return extensions.ErrRuntimeQuerySettingsRestartUnavailable
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.boundary == nil || !t.lockHeld || t.done || ctx == nil {
		return extensions.ErrRuntimeQuerySettingsRestartUnavailable
	}
	if err := validateRuntimeQuerySettingsRestartInput(t.boundary, ctx, extension); err != nil {
		return err
	}
	if !runtimeQueryInstanceMatches(t.source, extension) {
		return fmt.Errorf("%w: settings source artifact changed", ErrLifecycleRegistryPublicationConflict)
	}

	target, err := t.boundary.manager.StageRuntimeInstance(ctx, extension)
	if err != nil {
		return fmt.Errorf("stage settings runtime: %w", err)
	}
	t.target = target.Identity
	if _, err := t.boundary.manager.HealthRuntimeInstance(ctx, target.Identity); err != nil {
		cleanupErr := t.closeFailedTarget(ctx)
		return errors.Join(fmt.Errorf("health settings runtime: %w", err), cleanupErr)
	}
	if _, err := t.boundary.manager.PublishRuntimeInstanceFrom(ctx, target.Identity, RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: t.source.Identity,
		ExtensionVersion:        t.source.ExtensionVersion,
		ArtifactDigest:          t.source.ArtifactDigest,
	}); err != nil {
		cleanupErr := t.closeFailedTarget(ctx)
		return errors.Join(fmt.Errorf("publish settings runtime: %w", err), cleanupErr)
	}

	mutation, err := t.boundary.PublishRuntimeQueries(ctx, extension)
	if err != nil {
		cleanupErr := t.closeFailedTarget(ctx)
		return errors.Join(fmt.Errorf("publish settings runtime queries: %w", err), cleanupErr)
	}
	t.queryMutation = mutation

	compensationCtx, cancel := runtimeQuerySettingsCompensationContext(ctx)
	defer cancel()
	if err := t.boundary.manager.StopRuntimeInstance(compensationCtx, t.source.Identity); err != nil {
		queryErr := mutation.Rollback()
		cleanupErr := t.closeFailedTarget(compensationCtx)
		return errors.Join(fmt.Errorf("stop source settings runtime: %w", err), queryErr, cleanupErr)
	}
	t.finishLocked()
	return nil
}

// RestoreRuntimeQueriesAfterSettingsRollback restores publication before
// source admission. Failure keeps the runtime graph closed.
func (t *runtimeQuerySettingsRestartTransaction) RestoreRuntimeQueriesAfterSettingsRollback(ctx context.Context) error {
	if t == nil {
		return extensions.ErrRuntimeQuerySettingsRestartUnavailable
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.boundary == nil || !t.lockHeld || t.done || ctx == nil {
		return extensions.ErrRuntimeQuerySettingsRestartUnavailable
	}
	if t.queryMutation != nil {
		if err := t.queryMutation.Rollback(); err != nil {
			return t.failClosedLocked(fmt.Errorf("restore source query publication: %w", err))
		}
	}
	// The target must be unreachable before the source can become active again.
	// A prior cleanup may have failed after draining it, so retry the exact stop.
	if t.target.InstanceID != "" {
		if err := t.closeFailedTarget(ctx); err != nil {
			return t.failClosedLocked(err)
		}
	}

	source, err := t.boundary.manager.InspectRuntimeInstance(t.source.Identity)
	if err != nil {
		return t.failClosedLocked(fmt.Errorf("inspect source settings runtime: %w", err))
	}
	if source.Active {
		_, err = t.boundary.manager.ResumeRuntimeInstanceContext(ctx, t.source.Identity)
	} else {
		_, err = t.boundary.manager.PublishRuntimeInstanceIfNoActive(ctx, t.source.Identity)
	}
	if err != nil {
		return t.failClosedLocked(fmt.Errorf("restore source settings runtime: %w", err))
	}
	t.finishLocked()
	return nil
}

// KeepRuntimeQueriesClosed releases transaction serialization after the Host
// failed to restore the old settings. It deliberately never resumes source.
func (t *runtimeQuerySettingsRestartTransaction) KeepRuntimeQueriesClosed() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.keepClosedLocked()
}

func (t *runtimeQuerySettingsRestartTransaction) failClosedLocked(cause error) error {
	return errors.Join(cause, t.keepClosedLocked())
}

func (t *runtimeQuerySettingsRestartTransaction) keepClosedLocked() error {
	if !t.lockHeld || t.done {
		return nil
	}
	queryErr := t.closeTargetQueryPublicationLocked()
	sourceErr := t.quarantineExact(t.source.Identity, t.source.ExtensionVersion, t.source.ArtifactDigest)
	var targetErr error
	if t.target.InstanceID != "" {
		targetErr = t.quarantineExact(t.target, t.source.ExtensionVersion, t.source.ArtifactDigest)
	}
	t.finishLocked()
	return errors.Join(queryErr, sourceErr, targetErr)
}

func (t *runtimeQuerySettingsRestartTransaction) closeTargetQueryPublicationLocked() error {
	if t.queryMutation == nil {
		return nil
	}
	rollbackErr := t.queryMutation.Rollback()
	if rollbackErr == nil || t.boundary == nil || t.boundary.queries == nil || t.target.InstanceID == "" {
		return rollbackErr
	}

	// Rollback normally restores the source with artifact CAS. If that CAS
	// fails, remove only this transaction's exact target publication; a newer
	// runtime instance must remain untouched.
	current, found := t.boundary.queries.SnapshotPublication(t.source.Identity.ExtensionID)
	if !found || current.Artifact.ExtensionID != t.source.Identity.ExtensionID ||
		current.Artifact.ExtensionVersion != t.source.ExtensionVersion ||
		current.Artifact.PackageDigest != t.source.ArtifactDigest ||
		current.Artifact.RuntimeInstanceID != t.target.InstanceID {
		return rollbackErr
	}
	_, _, removeErr := t.boundary.queries.Remove(current.Artifact)
	return errors.Join(rollbackErr, removeErr)
}

func (t *runtimeQuerySettingsRestartTransaction) closeFailedTarget(ctx context.Context) error {
	if t.target.InstanceID == "" {
		return nil
	}
	compensationCtx, cancel := runtimeQuerySettingsCompensationContext(ctx)
	defer cancel()
	if _, err := t.boundary.manager.BeginDrainContext(compensationCtx, t.target); err != nil {
		if errors.Is(err, ErrRuntimeInstanceNotFound) {
			return nil
		}
		if quarantineErr := t.quarantineExact(t.target, t.source.ExtensionVersion, t.source.ArtifactDigest); quarantineErr != nil {
			return errors.Join(fmt.Errorf("drain failed settings runtime: %w", err), quarantineErr)
		}
	}
	if err := t.boundary.manager.WaitDrain(compensationCtx, t.target); err != nil && !errors.Is(err, ErrRuntimeInstanceNotFound) {
		return fmt.Errorf("wait failed settings runtime drain: %w", err)
	}
	return t.stopRetainedTarget(compensationCtx)
}

func (t *runtimeQuerySettingsRestartTransaction) quarantineExact(
	identity RuntimeInstanceIdentity,
	version string,
	digest string,
) error {
	_, err := t.boundary.manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
		RuntimeInstanceIdentity: identity, ExtensionVersion: version, ArtifactDigest: digest,
	}, extensions.ErrRuntimeQuerySettingsRestartUnavailable)
	if errors.Is(err, ErrRuntimeInstanceNotFound) {
		return nil
	}
	return err
}

func (t *runtimeQuerySettingsRestartTransaction) stopRetainedTarget(ctx context.Context) error {
	if t.target.InstanceID == "" {
		return nil
	}
	if err := t.boundary.manager.StopRuntimeInstance(ctx, t.target); err != nil && !errors.Is(err, ErrRuntimeInstanceNotFound) {
		return fmt.Errorf("stop failed settings runtime: %w", err)
	}
	return nil
}

func (t *runtimeQuerySettingsRestartTransaction) finishLocked() {
	if !t.lockHeld {
		return
	}
	t.lockHeld = false
	t.done = true
	t.boundary.publicationMu.Unlock()
}

func validateRuntimeQuerySettingsRestartInput(
	b *PostgresLifecycleBoundaryRegistries,
	ctx context.Context,
	extension extensions.Extension,
) error {
	if b == nil || b.manager == nil || b.queries == nil || ctx == nil ||
		extension.Type != extensions.TypePlugin || extension.Status != extensions.StatusEnabled ||
		strings.TrimSpace(extension.Manifest.Backend.Entry) == "" || extension.Manifest.Backend.ProtocolVersion != 2 ||
		!hasQueryRegistryPublication(extension.Manifest) ||
		(extension.Manifest.Lifecycle != nil && strings.TrimSpace(extension.Manifest.Lifecycle.ContractVersion) != "") ||
		runtimeQuerySettingsHasExternalSurfaces(extension.Manifest) {
		return extensions.ErrRuntimeQuerySettingsRestartUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.pages != nil {
		if _, published := b.pages.ExtensionSnapshot(extension.ID); published {
			return fmt.Errorf("%w: active plugin page publication requires an aggregate settings restart", extensions.ErrRuntimeQuerySettingsRestartUnavailable)
		}
	}
	root := extensions.PackageContentRoot(extension)
	info, err := os.Stat(root)
	if root == "" || err != nil || !info.IsDir() {
		return extensions.ErrRuntimeQuerySettingsRestartUnavailable
	}
	pkg, err := pages.LoadThemePackage(root)
	if err != nil {
		return fmt.Errorf("%w: inspect plugin page package: %v", extensions.ErrRuntimeQuerySettingsRestartUnavailable, err)
	}
	if len(pkg.Pages) != 0 || len(pkg.Widgets) != 0 || len(pkg.Skin.CSS) != 0 || strings.TrimSpace(pkg.Skin.Tokens) != "" {
		return fmt.Errorf("%w: plugin theme material requires an aggregate settings restart", extensions.ErrRuntimeQuerySettingsRestartUnavailable)
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) preflightRuntimeQuerySettingsRestartLocked(
	extension extensions.Extension,
) (RuntimeInstanceSnapshot, error) {
	source, err := b.manager.ActiveRuntimeInstance(extension.ID)
	if err != nil || !runtimeQueryInstanceMatches(source, extension) ||
		!b.manager.RuntimeInstanceAvailable(source.Identity) {
		return RuntimeInstanceSnapshot{}, errors.Join(extensions.ErrRuntimeQuerySettingsRestartUnavailable, err)
	}
	publication, found := b.queries.SnapshotPublication(extension.ID)
	if !found || !runtimeQueryArtifactMatchesInstance(publication.Artifact, extension, source) {
		return RuntimeInstanceSnapshot{}, fmt.Errorf("%w: active query publication does not match the source runtime", ErrLifecycleRegistryPublicationConflict)
	}
	return source, nil
}

func runtimeQuerySettingsHasExternalSurfaces(manifest extensions.Manifest) bool {
	return strings.TrimSpace(manifest.Admin.Entry) != "" || len(manifest.Admin.Pages) != 0 ||
		len(manifest.AdminPages) != 0 || len(manifest.Routes) != 0 || len(manifest.OpenAPI) != 0 ||
		len(manifest.Hooks) != 0 || len(manifest.Events) != 0 || len(manifest.Jobs) != 0 ||
		len(manifest.Providers) != 0 || len(manifest.Contributions) != 0 || len(manifest.Guards) != 0 ||
		len(manifest.Schedules) != 0 || len(manifest.Components) != 0 || len(manifest.Templates) != 0 ||
		len(manifest.Assets) != 0 || len(manifest.Content) != 0 || manifest.Database != nil || len(manifest.Cache) != 0 ||
		len(manifest.SEO) != 0 || len(manifest.Services) != 0 || len(manifest.Commands) != 0 ||
		len(manifest.AdminSurfaces) != 0 || manifest.Identity != nil || len(manifest.Media) != 0 ||
		len(manifest.Navigation) != 0 || len(manifest.Regions) != 0
}

func runtimeQuerySettingsCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, runtimeQuerySettingsCompensationTimeout)
}

var _ extensions.RuntimeQuerySettingsRestarter = (*PostgresLifecycleBoundaryRegistries)(nil)
var _ extensions.RuntimeQuerySettingsRestartTransaction = (*runtimeQuerySettingsRestartTransaction)(nil)
