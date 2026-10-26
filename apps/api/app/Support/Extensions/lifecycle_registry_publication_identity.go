package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func (b *PostgresLifecycleBoundaryRegistries) validateIdentityTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	if !lifecycleMaterialsPublishIdentity(source, target) {
		return nil
	}
	if b == nil || b.identity == nil || b.identityStore == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	extensionID := lifecycleComponentExtensionID(source, target)
	snapshot := b.identity.Snapshot()
	for _, desired := range []*lifecycleRegistryMaterial{source, target} {
		var publication *identityregistry.Publication
		if desired != nil {
			publication = desired.identityPublication
		}
		graph, err := lifecycleIdentityGraph(
			snapshot, extensionID, publication,
			lifecycleIdentityPublication(source), lifecycleIdentityPublication(target),
		)
		if err != nil {
			return err
		}
		probe := identityregistry.New()
		if _, err := probe.ReplaceAllIfRevision(0, graph, snapshot.Tombstones, snapshot.SafeMode); err != nil {
			return wrapLifecycleIdentityError("validate identity registry publication", err)
		}
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileIdentity(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	return b.reconcileIdentityTx(ctx, nil, request, source, target, desired)
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileIdentityTx(
	ctx context.Context,
	tx pgx.Tx,
	request LifecycleBoundaryRequest,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if !lifecycleMaterialsPublishIdentity(source, target) {
		return nil
	}
	if b == nil || b.identity == nil || b.identityStore == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	tombstones, err := b.reconcileIdentityDurable(ctx, tx, request, source, target, desired)
	if err != nil {
		return err
	}
	return b.replaceIdentityGraph(ctx, request.TargetExtension.ID, source, target, desired, tombstones)
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileIdentityDurable(
	ctx context.Context,
	tx pgx.Tx,
	request LifecycleBoundaryRequest,
	source, target, desired *lifecycleRegistryMaterial,
) ([]identityregistry.Tombstone, error) {
	if b == nil || b.identityStore == nil || ctx == nil {
		return nil, ErrLifecycleRegistryPublicationUnavailable
	}
	desiredPublication := lifecycleIdentityPublication(desired)
	input := identityregistry.ReconcilePublicationInput{
		ExtensionID:   request.TargetExtension.ID,
		AllowedSource: lifecycleIdentityArtifact(source),
		AllowedTarget: lifecycleIdentityArtifact(target),
		Desired:       desiredPublication, ActorUserID: request.ActorUserID, AuditEventID: request.AuditEventID,
	}
	var durable identityregistry.DurableState
	var err error
	if tx != nil {
		store, ok := b.identityStore.(identityregistry.TransactionalPublicationStore)
		if !ok {
			return nil, ErrLifecycleRegistryPublicationUnavailable
		}
		durable, err = store.ReconcileTx(ctx, tx, input)
	} else {
		durable, err = b.identityStore.Reconcile(ctx, input)
	}
	if err != nil {
		return nil, wrapLifecycleIdentityError("reconcile durable identity registry", err)
	}
	if desiredPublication != nil {
		if err := identityregistry.ValidateDurablePublication(durable, *desiredPublication); err != nil {
			return nil, wrapLifecycleIdentityError("validate reconciled identity publication", err)
		}
	} else if err := identityregistry.ValidateDurableRetirement(
		durable, request.TargetExtension.ID,
	); err != nil {
		return nil, wrapLifecycleIdentityError("validate retired identity publication", err)
	}
	tombstones, err := identityregistry.DurableStateToTombstones(durable)
	if err != nil {
		return nil, wrapLifecycleIdentityError("restore durable identity ownership", err)
	}
	return tombstones, nil
}

func (b *PostgresLifecycleBoundaryRegistries) replaceIdentityGraph(
	ctx context.Context,
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
	tombstones []identityregistry.Tombstone,
) error {
	if b == nil || b.identity == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	for attempts := 0; attempts < 16; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := b.identity.Snapshot()
		graph, graphErr := lifecycleIdentityGraph(
			snapshot, extensionID, lifecycleIdentityPublication(desired),
			lifecycleIdentityPublication(source), lifecycleIdentityPublication(target),
		)
		if graphErr != nil {
			return graphErr
		}
		if _, replaceErr := b.identity.ReplaceAllIfRevision(
			snapshot.Revision, graph, tombstones, snapshot.SafeMode,
		); replaceErr == nil {
			return nil
		} else if !errors.Is(replaceErr, identityregistry.ErrRevisionConflict) {
			return wrapLifecycleIdentityError("publish identity registry graph", replaceErr)
		}
	}
	return identityregistry.ErrRevisionConflict
}

func (b *PostgresLifecycleBoundaryRegistries) restoreIdentityPublications(
	ctx context.Context,
	items []extensions.Extension,
	safeMode bool,
) error {
	if !lifecycleIdentityRestoreRequired(b, items) {
		return nil
	}
	if b == nil || b.identity == nil || b.identityStore == nil || ctx == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	durable, err := b.identityStore.LoadDurableState(ctx)
	if err != nil {
		return wrapLifecycleIdentityError("load durable identity registry", err)
	}
	b.identitySuppressed = nil

	pluginPublications := make([]identityregistry.Publication, 0, len(items))
	validatedPublications := make([]identityregistry.Publication, 0, len(items))
	if !safeMode {
		// Build every expected enabled publication first so adoption never
		// publishes a partial in-memory graph while other owners are still missing.
		type expectedIdentityPublication struct {
			extensionID string
			publication identityregistry.Publication
			suppressed  bool
		}
		expected := make([]expectedIdentityPublication, 0, len(items))
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if item.Type != extensions.TypePlugin || item.Status != extensions.StatusEnabled ||
				!manifestPublishesIdentity(item.Manifest) {
				continue
			}
			binding := extensions.LifecycleRuntimeBinding{
				ExtensionID: item.ID, ExtensionVersion: item.Version,
				PackageDigest: item.PackageDigest, VersionID: item.ActiveVersionID,
			}
			suppressed := false
			if manifestIdentityRequiresRuntime(item.Manifest.Identity) {
				runtime, runtimeErr := b.manager.ActiveRuntimeInstance(item.ID)
				if runtimeErr != nil {
					// Validate the durable declaration even while boot-loop suppression
					// keeps its executable process surface closed.
					binding.RuntimeInstanceID = "durable-restore-validation"
					suppressed = true
				} else if !runtimeInstanceMatchesExtension(runtime, item) ||
					!b.manager.RuntimeInstanceAvailable(runtime.Identity) {
					return fmt.Errorf("%w: startup identity runtime for %s is not exact and available",
						ErrLifecycleRegistryPublicationConflict, item.ID)
				} else {
					binding.RuntimeInstanceID = runtime.Identity.InstanceID
				}
			}
			publication, buildErr := buildLifecycleIdentityPublication(item, binding)
			if buildErr != nil || publication == nil {
				return fmt.Errorf("restore identity registry for %s: %w", item.ID, buildErr)
			}
			expected = append(expected, expectedIdentityPublication{
				extensionID: item.ID, publication: *publication, suppressed: suppressed,
			})
		}
		// Stable order matches the adopter batch lock order (extension id).
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].extensionID < expected[j].extensionID
		})

		// 启动安全冗余：先退休「不在期望 enabled 集合」的残留 active tip。
		// 覆盖手动 DELETE 插件、不完整卸载（root 已 tombstone / leaf 仍 active）等场景，
		// 避免 ValidateDurablePublicationSet 因孤儿声明阻断 Host 启动。
		// 仍在 expected 内的 owner 走严格 exact 校验，绝不自动吞掉 digest 漂移。
		expectedIDs := make([]string, 0, len(expected))
		for _, item := range expected {
			expectedIDs = append(expectedIDs, item.extensionID)
		}
		orphans, orphanErr := identityregistry.ActiveOrphanOwners(durable, expectedIDs)
		if orphanErr != nil {
			return wrapLifecycleIdentityError("detect orphan durable identity owners", orphanErr)
		}
		if len(orphans) > 0 {
			retirer, hasRetirer := b.identityStore.(identityregistry.OrphanIdentityRetirer)
			if !hasRetirer {
				return wrapLifecycleIdentityError(
					"retire orphan durable identity owners "+strings.Join(orphans, ","),
					identityregistry.ErrArtifactConflict,
				)
			}
			repaired, retireErr := retirer.RetireOrphanPublications(ctx, orphans)
			if retireErr != nil {
				return wrapLifecycleIdentityError(
					"retire orphan durable identity owners "+strings.Join(orphans, ","), retireErr,
				)
			}
			durable = repaired
		}

		// Safe Mode never reaches this branch. Normal mode may adopt only on
		// ErrNotFound (missing root/history), never on conflict/stale/partial shapes.
		// Collect every missing publication first, then invoke the adopter ONCE.
		adopter, hasAdopter := b.identityStore.(identityregistry.LegacyPublicationAdopter)
		missing := make([]identityregistry.Publication, 0)
		suppressed := make(map[string]error)
		for _, item := range expected {
			if err := ctx.Err(); err != nil {
				return err
			}
			validateErr := identityregistry.ValidateDurablePublication(durable, item.publication)
			if errors.Is(validateErr, identityregistry.ErrStale) && b.identityRecovery != nil {
				recovered, repaired, recoveryErr := b.identityRecovery.RecoverStaleIdentityPublication(
					ctx, item.publication,
				)
				if recoveryErr != nil {
					return wrapLifecycleIdentityError(
						"recover stale durable identity publication for "+item.extensionID,
						recoveryErr,
					)
				}
				if repaired {
					durable = recovered
					validateErr = identityregistry.ValidateDurablePublication(durable, item.publication)
				}
			}
			if validateErr != nil {
				if !errors.Is(validateErr, identityregistry.ErrNotFound) {
					// One plugin whose enabled artifact and durable publication
					// disagree must not take the whole Host down: the enabled
					// artifact can advance ahead of the publication when a
					// lifecycle activation is interrupted. Keep that owner
					// unpublished for this boot (exact-fence semantics stay
					// closed) and let the next enable/upgrade republish it.
					suppressed[item.extensionID] = validateErr
					continue
				}
				if !hasAdopter {
					suppressed[item.extensionID] = validateErr
					continue
				}
				missing = append(missing, item.publication)
			}
			validatedPublications = append(validatedPublications, item.publication)
			if !item.suppressed {
				pluginPublications = append(pluginPublications, item.publication)
			}
		}
		if len(suppressed) > 0 {
			for extensionID, cause := range suppressed {
				b.identitySuppressed = append(b.identitySuppressed, extensionID)
				slog.ErrorContext(ctx, "extension identity publication suppressed for this boot",
					"extensionId", extensionID, "error", cause)
			}
			sort.Strings(b.identitySuppressed)
			durable = durableStateWithoutOwners(durable, suppressed)
		}
		if len(missing) > 0 {
			if _, adoptErr := adopter.AdoptLegacyPublications(ctx, missing); adoptErr != nil {
				owners := make([]string, 0, len(missing))
				for _, publication := range missing {
					owners = append(owners, publication.Artifact.ExtensionID)
				}
				return wrapLifecycleIdentityError(
					"adopt legacy durable identity publications for "+strings.Join(owners, ","), adoptErr,
				)
			}
			// Prefer a fresh read after the adopter commits so concurrent
			// startup nodes observe the same durable tip set.
			reloaded, reloadErr := b.identityStore.LoadDurableState(ctx)
			if reloadErr != nil {
				return wrapLifecycleIdentityError("reload durable identity registry after adoption", reloadErr)
			}
			durable = durableStateWithoutOwners(reloaded, suppressed)
			// Re-validate the full expected set before any in-memory publish.
			if revalidateErr := identityregistry.ValidateDurablePublicationSet(durable, validatedPublications); revalidateErr != nil {
				return wrapLifecycleIdentityError("validate adopted durable identity publication set", revalidateErr)
			}
		} else if err := identityregistry.ValidateDurablePublicationSet(durable, validatedPublications); err != nil {
			return wrapLifecycleIdentityError("validate durable identity publication set", err)
		}
	}

	tombstones, err := identityregistry.DurableStateToTombstones(durable)
	if err != nil {
		return wrapLifecycleIdentityError("restore durable identity ownership", err)
	}

	for attempts := 0; attempts < 16; attempts++ {
		snapshot := b.identity.Snapshot()
		publications := coreLifecycleIdentityPublications(snapshot.Publications)
		publications = append(publications, pluginPublications...)
		if _, err := b.identity.ReplaceAllIfRevision(
			snapshot.Revision, publications, tombstones, safeMode,
		); err == nil {
			return nil
		} else if !errors.Is(err, identityregistry.ErrRevisionConflict) {
			return wrapLifecycleIdentityError("restore identity registry publication", err)
		}
	}
	return identityregistry.ErrRevisionConflict
}

func lifecycleIdentityRestoreRequired(b *PostgresLifecycleBoundaryRegistries, items []extensions.Extension) bool {
	if b != nil && b.identitySet {
		return true
	}
	for _, item := range items {
		if item.Type == extensions.TypePlugin && manifestPublishesIdentity(item.Manifest) {
			return true
		}
	}
	return false
}

// durableStateWithoutOwners drops every owner in skip from the durable snapshot.
// Startup uses it to validate the rest of the expected set after one plugin's
// publication was suppressed, without touching the immutable durable history.
func durableStateWithoutOwners(
	state identityregistry.DurableState,
	skip map[string]error,
) identityregistry.DurableState {
	if len(skip) == 0 {
		return state
	}
	pruned := identityregistry.DurableState{
		Owners:   make([]identityregistry.DurableOwner, 0, len(state.Owners)),
		Tips:     make([]identityregistry.DurableDeclarationTip, 0, len(state.Tips)),
		RootTips: make([]identityregistry.DurableRootPublicationTip, 0, len(state.RootTips)),
	}
	for _, owner := range state.Owners {
		if _, dropped := skip[owner.OwnerExtensionID]; dropped {
			continue
		}
		pruned.Owners = append(pruned.Owners, owner)
	}
	for _, tip := range state.Tips {
		if _, dropped := skip[tip.OwnerExtensionID]; dropped {
			continue
		}
		pruned.Tips = append(pruned.Tips, tip)
	}
	for _, tip := range state.RootTips {
		if _, dropped := skip[tip.OwnerExtensionID]; dropped {
			continue
		}
		pruned.RootTips = append(pruned.RootTips, tip)
	}
	return pruned
}

// SuppressedIdentityExtensions reports the plugins whose durable Identity
// publication was suppressed during this boot. They are published nowhere until
// a lifecycle operation republishes them.
func (b *PostgresLifecycleBoundaryRegistries) SuppressedIdentityExtensions() []string {
	if b == nil || len(b.identitySuppressed) == 0 {
		return nil
	}
	return append([]string(nil), b.identitySuppressed...)
}

func coreLifecycleIdentityPublications(input []identityregistry.Publication) []identityregistry.Publication {
	result := make([]identityregistry.Publication, 0, len(input))
	for _, publication := range input {
		if publication.Artifact.Core {
			result = append(result, publication)
		}
	}
	return result
}

func lifecycleMaterialsPublishIdentity(materials ...*lifecycleRegistryMaterial) bool {
	for _, material := range materials {
		if material != nil && material.identityPublication != nil {
			return true
		}
	}
	return false
}

func lifecycleIdentityPublication(material *lifecycleRegistryMaterial) *identityregistry.Publication {
	if material == nil {
		return nil
	}
	return material.identityPublication
}

func lifecycleIdentityArtifact(material *lifecycleRegistryMaterial) *identityregistry.Artifact {
	publication := lifecycleIdentityPublication(material)
	if publication == nil {
		return nil
	}
	artifact := publication.Artifact
	return &artifact
}

func wrapLifecycleIdentityError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, identityregistry.ErrConflict) || errors.Is(err, identityregistry.ErrArtifactConflict) ||
		errors.Is(err, identityregistry.ErrRevisionConflict) || errors.Is(err, identityregistry.ErrSafeMode) ||
		errors.Is(err, identityregistry.ErrStale) {
		return fmt.Errorf("%w: %s: %v", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (b *PostgresLifecycleBoundaryRegistries) PublishRuntimeIdentity(
	ctx context.Context,
	extension extensions.Extension,
	actorUserID int64,
	auditEventID int64,
) (extensions.RuntimeIdentityPublicationMutation, error) {
	target, err := b.legacyRuntimeIdentityMaterial(extension)
	if err != nil || target == nil {
		return nil, err
	}
	if err := b.reconcileLegacyRuntimeIdentity(ctx, extension, nil, target, target, actorUserID, auditEventID); err != nil {
		return nil, err
	}
	return legacyRuntimeIdentityPublicationMutation{
		boundary: b, extension: extension, actorUserID: actorUserID,
		auditEventID: auditEventID, mode: legacyRuntimeIdentityRollbackRetire,
	}, nil
}

func (b *PostgresLifecycleBoundaryRegistries) QuarantineRuntimeIdentity(
	ctx context.Context,
	extension extensions.Extension,
	actorUserID int64,
	auditEventID int64,
) (extensions.RuntimeIdentityPublicationMutation, error) {
	source, err := b.legacyRuntimeIdentityMaterial(extension)
	if err != nil || source == nil {
		return nil, err
	}
	if err := b.reconcileLegacyRuntimeIdentity(ctx, extension, source, nil, nil, actorUserID, auditEventID); err != nil {
		return nil, err
	}
	return legacyRuntimeIdentityPublicationMutation{
		boundary: b, extension: extension, actorUserID: actorUserID,
		auditEventID: auditEventID, mode: legacyRuntimeIdentityRollbackPublish,
	}, nil
}

func (b *PostgresLifecycleBoundaryRegistries) legacyRuntimeIdentityMaterial(
	extension extensions.Extension,
) (*lifecycleRegistryMaterial, error) {
	if !manifestPublishesIdentity(extension.Manifest) {
		return nil, nil
	}
	if b == nil || b.identity == nil || b.identityStore == nil {
		return nil, ErrLifecycleRegistryPublicationUnavailable
	}
	binding := extensions.LifecycleRuntimeBinding{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
	}
	if manifestIdentityRequiresRuntime(extension.Manifest.Identity) {
		if b.manager == nil {
			return nil, ErrLifecycleRegistryPublicationUnavailable
		}
		runtime, err := b.manager.ActiveRuntimeInstance(extension.ID)
		if err != nil || !runtimeInstanceMatchesExtension(runtime, extension) ||
			!b.manager.RuntimeInstanceAvailable(runtime.Identity) {
			return nil, fmt.Errorf(
				"%w: legacy identity runtime for %s is not exact and available",
				ErrLifecycleRegistryPublicationConflict, extension.ID,
			)
		}
		binding.RuntimeInstanceID = runtime.Identity.InstanceID
	}
	publication, err := buildLifecycleIdentityPublication(extension, binding)
	if err != nil || publication == nil {
		return nil, err
	}
	return &lifecycleRegistryMaterial{
		extension: extension, binding: binding, identityPublication: publication,
	}, nil
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileLegacyRuntimeIdentity(
	ctx context.Context,
	extension extensions.Extension,
	source, target, desired *lifecycleRegistryMaterial,
	actorUserID int64,
	auditEventID int64,
) error {
	request := LifecycleBoundaryRequest{
		Operation:       extensions.LifecycleMachineEnable,
		TargetExtension: extension,
		ActorUserID:     actorUserID,
		AuditEventID:    auditEventID,
	}
	if desired == nil {
		request.Operation = extensions.LifecycleMachineDisable
	}
	b.publicationMu.Lock()
	defer b.publicationMu.Unlock()
	if err := b.validateIdentityTransition(source, target); err != nil {
		return err
	}
	tombstones, err := b.reconcileIdentityDurable(ctx, nil, request, source, target, desired)
	if err != nil {
		return err
	}
	if err := b.replaceIdentityGraph(ctx, request.TargetExtension.ID, source, target, desired, tombstones); err != nil {
		if restoreErr := b.restoreLegacyRuntimeIdentityDurable(ctx, request, source, target, desired); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore durable identity publication after process graph failure: %w", restoreErr))
		}
		return err
	}
	return nil
}

func (b *PostgresLifecycleBoundaryRegistries) restoreLegacyRuntimeIdentityDurable(
	ctx context.Context,
	request LifecycleBoundaryRequest,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	_ = target
	if desired == nil {
		if source == nil {
			return nil
		}
		request.Operation = extensions.LifecycleMachineEnable
		_, err := b.reconcileIdentityDurable(ctx, nil, request, nil, source, source)
		return err
	}
	request.Operation = extensions.LifecycleMachineDisable
	_, err := b.reconcileIdentityDurable(ctx, nil, request, desired, nil, nil)
	return err
}

type legacyRuntimeIdentityRollbackMode string

const (
	legacyRuntimeIdentityRollbackRetire  legacyRuntimeIdentityRollbackMode = "retire"
	legacyRuntimeIdentityRollbackPublish legacyRuntimeIdentityRollbackMode = "publish"
)

type legacyRuntimeIdentityPublicationMutation struct {
	boundary     *PostgresLifecycleBoundaryRegistries
	extension    extensions.Extension
	actorUserID  int64
	auditEventID int64
	mode         legacyRuntimeIdentityRollbackMode
}

func (m legacyRuntimeIdentityPublicationMutation) Rollback() error {
	if m.boundary == nil {
		return ErrLifecycleRegistryPublicationUnavailable
	}
	switch m.mode {
	case legacyRuntimeIdentityRollbackRetire:
		_, err := m.boundary.QuarantineRuntimeIdentity(
			context.Background(), m.extension, m.actorUserID, m.auditEventID,
		)
		return err
	case legacyRuntimeIdentityRollbackPublish:
		_, err := m.boundary.PublishRuntimeIdentity(
			context.Background(), m.extension, m.actorUserID, m.auditEventID,
		)
		return err
	default:
		return ErrLifecycleRegistryPublicationInvalid
	}
}

// BuildLifecycleIdentityPublication constructs the exact lifecycle Identity
// Registry publication used by enable/restore paths. Integration tests call this
// to prove the same Schema/provider metadata path without driving the full
// coordinator.
func BuildLifecycleIdentityPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*identityregistry.Publication, error) {
	return buildLifecycleIdentityPublication(extension, binding)
}

func buildLifecycleIdentityPublication(
	extension extensions.Extension,
	binding extensions.LifecycleRuntimeBinding,
) (*identityregistry.Publication, error) {
	if !manifestPublishesIdentity(extension.Manifest) {
		return nil, nil
	}
	if extension.ID == "" || extension.Version == "" || extension.ActiveVersionID <= 0 ||
		extension.ID != strings.TrimSpace(extension.ID) || extension.Version != strings.TrimSpace(extension.Version) ||
		!validLifecycleCleanupDigest(extension.PackageDigest) || extension.Type != extensions.TypePlugin ||
		extension.Manifest.ID != extension.ID || extension.Manifest.Version != extension.Version ||
		extension.Manifest.Type != extensions.TypePlugin {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}

	artifact := identityregistry.Artifact{
		ExtensionID: extension.ID, ExtensionVersion: extension.Version,
		PackageDigest: extension.PackageDigest, VersionID: extension.ActiveVersionID,
	}
	if manifestIdentityRequiresRuntime(extension.Manifest.Identity) {
		if validateExactCoordinatorBinding("identity registry", binding, extension, true) != nil {
			return nil, ErrLifecycleRegistryPublicationInvalid
		}
		artifact.RuntimeInstanceID = binding.RuntimeInstanceID
	}

	publication := identityregistry.Publication{Artifact: artifact}
	publication.Permissions = make([]identityregistry.PermissionDefinition, 0, len(extension.Manifest.PermissionDefinitions))
	for _, permission := range extension.Manifest.PermissionDefinitions {
		publication.Permissions = append(publication.Permissions, identityregistry.PermissionDefinition{
			Key: permission.Key, ContractVersion: permission.ContractVersion,
			Label: permission.Label.Resolve("en-US"), Description: permission.Description.Resolve("en-US"),
			LabelLocales:       cloneLocalizedValues(permission.Label.ByLocale),
			DescriptionLocales: cloneLocalizedValues(permission.Description.ByLocale),
			RecommendedRoles:   append([]string(nil), permission.RecommendedRoles...),
			AssignmentPolicy:   permission.AssignmentPolicy,
		})
	}
	if declared := extension.Manifest.Identity; declared != nil {
		identity := identityregistry.IdentityDeclaration{
			ContractVersion: declared.ContractVersion,
			SessionPolicy:   declared.SessionPolicy,
			RiskHooks:       append([]string(nil), declared.RiskHooks...),
			UserFields:      make([]identityregistry.UserField, 0, len(declared.UserFields)),
			Providers:       make([]identityregistry.Provider, 0, len(declared.Providers)),
		}
		for _, field := range declared.UserFields {
			identity.UserFields = append(identity.UserFields, identityregistry.UserField{
				ID: field.ID, ContractVersion: field.ContractVersion, Type: field.Type,
				Schema: field.Schema, ReadPermission: field.ReadPermission,
				WritePermission: field.WritePermission,
			})
		}
		for _, provider := range declared.Providers {
			// 展示元数据由插件 manifest 注入；Host 公共 catalog 再按 Accept-Language 解析。
			var labelLocales map[string]string
			var defaultLabel string
			if provider.Label != nil && !provider.Label.IsEmpty() {
				labelLocales = cloneLocalizedValues(provider.Label.ByLocale)
				defaultLabel = strings.TrimSpace(provider.Label.Default)
				if defaultLabel == "" {
					defaultLabel = provider.Label.Resolve("en-US")
				}
				if defaultLabel != "" {
					if labelLocales == nil {
						labelLocales = map[string]string{}
					}
					if strings.TrimSpace(labelLocales["en-US"]) == "" {
						labelLocales["en-US"] = defaultLabel
					}
				}
			}
			mapped := identityregistry.Provider{
				ID: provider.ID, ContractVersion: provider.ContractVersion,
				Kind: provider.Kind, Handler: provider.Handler, Priority: provider.Priority,
				Label:        defaultLabel,
				LabelLocales: labelLocales,
				Icon:         strings.TrimSpace(provider.Icon),
			}
			for _, operation := range provider.Operations {
				mapped.Operations = append(mapped.Operations, identityregistry.ProviderOperation{
					Name: operation.Name, InputSchema: operation.InputSchema,
					OutputSchema: operation.OutputSchema, TimeoutMS: operation.TimeoutMS,
					FailurePolicy: operation.FailurePolicy,
				})
			}
			identity.Providers = append(identity.Providers, mapped)
		}
		publication.Identity = &identity
	}
	bound, err := bindLifecycleIdentitySchemas(extension, publication)
	if err != nil {
		return nil, err
	}
	publication = bound

	// Freeze canonical Registry ordering into lifecycle material and its digest.
	probe := identityregistry.New()
	if _, err := probe.Publish(publication); err != nil {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	frozen, found := probe.SnapshotPublication(extension.ID)
	if !found {
		return nil, ErrLifecycleRegistryPublicationInvalid
	}
	return &frozen, nil
}

func cloneLocalizedValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for locale, value := range values {
		cloned[locale] = value
	}
	return cloned
}

func manifestPublishesIdentity(manifest extensions.Manifest) bool {
	return manifest.Identity != nil || len(manifest.PermissionDefinitions) > 0
}

func manifestIdentityRequiresRuntime(identity *extensions.ManifestIdentity) bool {
	if identity == nil {
		return false
	}
	for _, provider := range identity.Providers {
		if len(provider.Operations) > 0 {
			return true
		}
	}
	return len(identity.RiskHooks) > 0 ||
		identity.SessionPolicy != "" && identity.SessionPolicy != "core.session.default"
}

func lifecycleIdentityGraph(
	snapshot identityregistry.Snapshot,
	extensionID string,
	desired *identityregistry.Publication,
	allowed ...*identityregistry.Publication,
) ([]identityregistry.Publication, error) {
	allowedPublications := make(map[identityregistry.Artifact]identityregistry.Publication, len(allowed))
	for _, publication := range allowed {
		if publication == nil {
			continue
		}
		if existing, found := allowedPublications[publication.Artifact]; found &&
			!identityregistry.EqualPublicContract(existing, *publication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		allowedPublications[publication.Artifact] = *publication
	}

	publications := make([]identityregistry.Publication, 0, len(snapshot.Publications)+1)
	for _, publication := range snapshot.Publications {
		if publication.Artifact.ExtensionID != extensionID {
			publications = append(publications, publication)
			continue
		}
		frozen, ok := allowedPublications[publication.Artifact]
		if !ok || !identityregistry.EqualPublicContract(frozen, publication) {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
	}
	if desired != nil {
		if snapshot.SafeMode {
			return nil, ErrLifecycleRegistryPublicationConflict
		}
		publications = append(publications, *desired)
	}
	return publications, nil
}
