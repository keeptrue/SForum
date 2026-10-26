package extensions

import (
	"context"
	"errors"
	"fmt"

	assetregistry "github.com/keeptrue/sforum/apps/api/app/Support/AssetRegistry"
)

var errAssetPublicationConflict = errors.New("extensions: asset publication snapshot conflicts with exact extension state")

// assetPublicationSnapshot is captured by the lifecycle caller before any DB or
// process-local mutation. Helpers below never manufacture an expected revision
// by reading the Registry immediately before their CAS.
type assetPublicationSnapshot struct {
	revision     uint64
	publications []assetregistry.Publication
}

type exactAssetMutation struct {
	before        assetPublicationSnapshot
	afterRevision uint64
	changed       bool
	rollbackSafe  bool
}

func (s *serviceCore) captureAssetPublicationSnapshot() assetPublicationSnapshot {
	if s == nil || s.assetRegistry == nil {
		return assetPublicationSnapshot{}
	}
	snapshot := s.assetRegistry.Snapshot()
	return assetPublicationSnapshot{revision: snapshot.Revision, publications: snapshot.Publications}
}

func (s *serviceCore) validateThemeAssetTransition(
	ctx context.Context,
	expected assetPublicationSnapshot,
	target,
	source *Extension,
) error {
	if s == nil || s.assetRegistry == nil {
		return nil
	}
	targetPublication, err := s.themeAssetPublication(ctx, target, false)
	if err != nil {
		return err
	}
	desired, err := themeAssetTransitionPublications(expected.publications, targetPublication, source)
	if err != nil {
		return err
	}
	// Replaying the captured graph through a disposable Registry applies the
	// same graph, dependency, and exact-artifact declaration-drift validation as
	// the eventual production CAS without mutating the shared Registry.
	probe := assetregistry.New()
	revision, err := probe.ReplaceAll(expected.publications)
	if err != nil {
		return err
	}
	_, err = probe.ReplaceAllIfRevision(revision, desired)
	return err
}

func (s *serviceCore) publishThemeAssetTransition(
	ctx context.Context,
	expected assetPublicationSnapshot,
	target,
	source *Extension,
) (assetPublicationSnapshot, error) {
	if s == nil || s.assetRegistry == nil {
		return expected, nil
	}
	targetPublication, err := s.themeAssetPublication(ctx, target, true)
	if err != nil {
		return expected, err
	}
	desired, err := themeAssetTransitionPublications(expected.publications, targetPublication, source)
	if err != nil {
		return expected, err
	}
	revision, err := s.assetRegistry.ReplaceAllIfRevision(expected.revision, desired)
	if err != nil {
		return expected, err
	}
	return assetPublicationSnapshot{revision: revision, publications: desired}, nil
}

// rollbackThemeAssetTransition restores only the exact source captured by the
// failed transaction. If its live trust/lifecycle authority disappeared, the
// failed target is removed and no historical theme publication is resurrected.
func (s *serviceCore) rollbackThemeAssetTransition(
	ctx context.Context,
	expected assetPublicationSnapshot,
	restoreSource,
	failedTarget *Extension,
) (assetPublicationSnapshot, error) {
	if s == nil || s.assetRegistry == nil {
		return expected, nil
	}
	restorePublication, authorityErr := s.themeAssetPublication(ctx, restoreSource, true)
	if authorityErr != nil {
		desired, transitionErr := themeAssetTransitionPublications(expected.publications, nil, failedTarget)
		if transitionErr != nil {
			return expected, errors.Join(authorityErr, transitionErr)
		}
		revision, clearErr := s.assetRegistry.ReplaceAllIfRevision(expected.revision, desired)
		if clearErr != nil {
			quarantineErr := s.quarantineCapturedThemeTarget(expected, failedTarget)
			return expected, errors.Join(authorityErr, clearErr, quarantineErr)
		}
		return assetPublicationSnapshot{revision: revision, publications: desired}, authorityErr
	}
	desired, err := themeAssetTransitionPublications(expected.publications, restorePublication, failedTarget)
	if err != nil {
		return expected, err
	}
	revision, err := s.assetRegistry.ReplaceAllIfRevision(expected.revision, desired)
	if err != nil {
		return expected, errors.Join(err, s.quarantineCapturedThemeTarget(expected, failedTarget))
	}
	return assetPublicationSnapshot{revision: revision, publications: desired}, nil
}

func (s *serviceCore) quarantineCapturedThemeTarget(
	expected assetPublicationSnapshot,
	failedTarget *Extension,
) error {
	if s == nil || s.assetRegistry == nil || failedTarget == nil {
		return nil
	}
	for _, publication := range expected.publications {
		if publication.Artifact.OwnerKind == assetregistry.OwnerKindTheme &&
			assetArtifactMatchesExtension(publication.Artifact, *failedTarget) {
			_, _, err := s.assetRegistry.QuarantineExact(publication.Artifact)
			return err
		}
	}
	return nil
}

func (s *serviceCore) themeAssetPublication(
	ctx context.Context,
	extension *Extension,
	requireLiveAuthority bool,
) (*assetregistry.Publication, error) {
	if extension == nil {
		return nil, nil
	}
	if extension.Type != TypeTheme {
		return nil, fmt.Errorf("%w: non-theme asset transition", errAssetPublicationConflict)
	}
	return s.extensionAssetPublication(ctx, *extension, requireLiveAuthority)
}

func (s *serviceCore) validateExtensionAssetPublication(
	ctx context.Context,
	expected assetPublicationSnapshot,
	extension Extension,
) error {
	if s == nil || s.assetRegistry == nil {
		return nil
	}
	publication, err := s.extensionAssetPublication(ctx, extension, false)
	if err != nil {
		return err
	}
	if publication == nil && !assetSnapshotHasExtension(expected, extension.ID) {
		return nil
	}
	desired, err := replaceExtensionAssetPublication(expected.publications, extension.ID, publication)
	if err != nil {
		return err
	}
	probe := assetregistry.New()
	revision, err := probe.ReplaceAll(expected.publications)
	if err != nil {
		return err
	}
	_, err = probe.ReplaceAllIfRevision(revision, desired)
	return err
}

// publishExactExtensionAssetPublication changes only one owner. Unrelated
// lifecycle writers do not turn a valid legacy enable into a full-graph CAS
// conflict. Publish has no writer receipt, so an absent-to-present result is
// never destructively rolled back: another exact writer is indistinguishable.
func (s *serviceCore) publishExactExtensionAssetPublication(
	ctx context.Context,
	caller assetPublicationSnapshot,
	extension Extension,
) (exactAssetMutation, error) {
	if s == nil || s.assetRegistry == nil {
		return exactAssetMutation{}, nil
	}
	publication, err := s.extensionAssetPublication(ctx, extension, true)
	if err != nil {
		return exactAssetMutation{}, err
	}
	if publication == nil {
		return s.quarantineExactAssetPublication(ctx, caller, extension)
	}
	before := s.captureAssetPublicationSnapshot()
	revision, err := s.assetRegistry.Publish(*publication)
	if err != nil {
		return exactAssetMutation{}, err
	}
	changed := !assetSnapshotHasExtension(before, extension.ID)
	return exactAssetMutation{
		before: before, afterRevision: revision, changed: changed,
		rollbackSafe: false,
	}, nil
}

func (s *serviceCore) extensionAssetPublication(
	ctx context.Context,
	extension Extension,
	requireLiveAuthority bool,
) (*assetregistry.Publication, error) {
	if !hasPublicAssetPayload(extension.Manifest) {
		return nil, nil
	}
	if s == nil || s.executableTrust == nil {
		return nil, ErrPublicFrontendUnavailable
	}
	impactDigest := ""
	if requireLiveAuthority {
		identity, err := s.executableTrust.RuntimeIdentity(ctx, extension)
		if err != nil || identity.ImpactDigest == "" {
			if err == nil {
				err = ErrTrustGrantNotFound
			}
			return nil, err
		}
		impactDigest = identity.ImpactDigest
	} else {
		impact, err := buildTrustImpact(extension, TrustActionEnable)
		if err != nil {
			return nil, err
		}
		impactDigest = impact.Digest
	}
	publication, err := BuildPublicAssetPublication(extension, impactDigest)
	if err != nil {
		return nil, err
	}
	return &publication, nil
}

func replaceExtensionAssetPublication(
	current []assetregistry.Publication,
	extensionID string,
	target *assetregistry.Publication,
) ([]assetregistry.Publication, error) {
	extensionID = normalizeID(extensionID)
	if extensionID == "" || (target != nil && normalizeID(target.Artifact.ExtensionID) != extensionID) {
		return nil, errAssetPublicationConflict
	}
	desired := make([]assetregistry.Publication, 0, len(current)+1)
	for _, publication := range current {
		if normalizeID(publication.Artifact.ExtensionID) != extensionID {
			desired = append(desired, publication)
		}
	}
	if target != nil {
		desired = append(desired, *target)
	}
	return desired, nil
}

// quarantineExactAssetPublication removes one caller-captured exact artifact and
// every current transitive hard dependent through the Registry's owner-exact
// quarantine swap. A stale disable/uninstall cannot delete a newer publication.
func (s *serviceCore) quarantineExactAssetPublication(
	ctx context.Context,
	caller assetPublicationSnapshot,
	extension Extension,
) (exactAssetMutation, error) {
	if s == nil || s.assetRegistry == nil {
		return exactAssetMutation{}, nil
	}
	extensionID := normalizeID(extension.ID)
	var artifact *assetregistry.Artifact
	for index := range caller.publications {
		publication := caller.publications[index]
		if normalizeID(publication.Artifact.ExtensionID) != extensionID {
			continue
		}
		if !assetArtifactMatchesExtension(publication.Artifact, extension) {
			return exactAssetMutation{}, errAssetPublicationConflict
		}
		copy := publication.Artifact
		artifact = &copy
		break
	}
	if artifact == nil {
		if !hasPublicAssetPayload(extension.Manifest) {
			return exactAssetMutation{}, nil
		}
		impact, err := buildTrustImpact(extension, TrustActionEnable)
		ownerKind, ownerOK := publicAssetOwnerKind(extension)
		if err != nil || !ownerOK {
			if err == nil {
				err = errAssetPublicationConflict
			}
			return exactAssetMutation{}, err
		}
		artifact = &assetregistry.Artifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, ImpactDigest: impact.Digest,
			OwnerKind: ownerKind,
		}
	}
	before := s.captureAssetPublicationSnapshot()
	revision, quarantined, err := s.assetRegistry.QuarantineExact(*artifact)
	if err != nil {
		return exactAssetMutation{}, err
	}
	changed := len(quarantined) > 0
	post := s.captureAssetPublicationSnapshot()
	return exactAssetMutation{
		before: before, afterRevision: revision, changed: changed,
		rollbackSafe: changed && revision == before.revision+1 && post.revision == revision,
	}, nil
}

func assetSnapshotHasExtension(snapshot assetPublicationSnapshot, extensionID string) bool {
	extensionID = normalizeID(extensionID)
	for _, publication := range snapshot.publications {
		if normalizeID(publication.Artifact.ExtensionID) == extensionID {
			return true
		}
	}
	return false
}

func (s *serviceCore) rollbackExactAssetMutation(mutation exactAssetMutation) error {
	if s == nil || s.assetRegistry == nil || !mutation.changed {
		return nil
	}
	if !mutation.rollbackSafe {
		return assetregistry.ErrRevisionConflict
	}
	_, err := s.assetRegistry.ReplaceAllIfRevision(mutation.afterRevision, mutation.before.publications)
	return err
}

func (s *serviceCore) restoreEnabledAssetPublications(
	ctx context.Context,
	expected assetPublicationSnapshot,
	items []Extension,
	safeMode bool,
) (assetPublicationSnapshot, error) {
	if s == nil || s.assetRegistry == nil {
		return expected, nil
	}
	desired := make([]assetregistry.Publication, 0, len(items))
	for _, publication := range expected.publications {
		if publication.Artifact.OwnerKind == assetregistry.OwnerKindCore {
			desired = append(desired, publication)
		}
	}
	if !safeMode {
		activeThemes := 0
		for _, extension := range items {
			if extension.Status != StatusEnabled || !hasPublicAssetPayload(extension.Manifest) {
				continue
			}
			if extension.Type == TypeTheme {
				activeThemes++
				if activeThemes > 1 {
					return expected, errAssetPublicationConflict
				}
			}
			publication, err := s.extensionAssetPublication(ctx, extension, true)
			if errors.Is(err, ErrTrustGrantNotFound) || errors.Is(err, ErrFrontendPackageChanged) {
				continue
			}
			if err != nil {
				return expected, err
			}
			if publication != nil {
				desired = append(desired, *publication)
			}
		}
	}
	revision, err := s.assetRegistry.ReplaceAllIfRevision(expected.revision, desired)
	if err != nil {
		return expected, err
	}
	return assetPublicationSnapshot{revision: revision, publications: desired}, nil
}

// quarantineLifecycleAssetPublication binds terminal uninstall cleanup to the
// immutable lifecycle authority document. It never discovers a mutable current
// artifact and then treats that value as the caller's expectation.
func (s *serviceCore) quarantineLifecycleAssetPublication(operation LifecycleOperation) error {
	if s == nil || s.assetRegistry == nil {
		return nil
	}
	authority, err := decodeLifecycleAuthoritySnapshot(operation.AuthoritySnapshot)
	if err != nil {
		return err
	}
	impact := authority.Impact
	if impact.ExtensionID != operation.ExtensionID || impact.ExtensionVersion != operation.ExtensionVersion ||
		normalizedPublicDigest(impact.PackageDigest) != normalizedPublicDigest(operation.PackageDigest) ||
		impact.Action != TrustActionEnable || normalizedPublicDigest(impact.Digest) == "" {
		return errAssetPublicationConflict
	}
	artifact := assetregistry.Artifact{
		ExtensionID: operation.ExtensionID, ExtensionVersion: operation.ExtensionVersion,
		PackageDigest: operation.PackageDigest, ImpactDigest: impact.Digest,
		OwnerKind: assetregistry.OwnerKindPlugin,
	}
	_, _, err = s.assetRegistry.QuarantineExact(artifact)
	return err
}

func themeAssetTransitionPublications(
	current []assetregistry.Publication,
	target *assetregistry.Publication,
	source *Extension,
) ([]assetregistry.Publication, error) {
	desired := make([]assetregistry.Publication, 0, len(current)+1)
	var activeTheme *assetregistry.Publication
	for index := range current {
		publication := current[index]
		if publication.Artifact.OwnerKind != assetregistry.OwnerKindTheme {
			desired = append(desired, publication)
			continue
		}
		if activeTheme != nil {
			return nil, errAssetPublicationConflict
		}
		copy := publication
		activeTheme = &copy
	}

	if activeTheme != nil {
		matchesSource := source != nil && assetArtifactMatchesExtension(activeTheme.Artifact, *source)
		matchesTarget := target != nil && activeTheme.Artifact == target.Artifact
		if !matchesSource && !matchesTarget {
			return nil, errAssetPublicationConflict
		}
	}
	if target != nil {
		if target.Artifact.OwnerKind != assetregistry.OwnerKindTheme {
			return nil, errAssetPublicationConflict
		}
		desired = append(desired, *target)
	}
	return desired, nil
}

func assetArtifactMatchesExtension(artifact assetregistry.Artifact, extension Extension) bool {
	wantOwner, ok := publicAssetOwnerKind(extension)
	return ok && artifact.OwnerKind == wantOwner && normalizeID(artifact.ExtensionID) == normalizeID(extension.ID) &&
		artifact.ExtensionVersion == extension.Version &&
		normalizedPublicDigest(artifact.PackageDigest) == normalizedPublicDigest(extension.PackageDigest)
}
