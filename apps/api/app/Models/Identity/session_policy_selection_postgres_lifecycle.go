package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const identitySessionPolicyLifecycleInvalidationReason = "identity_registry_publication_changed"

var _ identityregistry.SessionPolicyLifecycleInvalidator = (*PostgresIdentitySessionPolicyStore)(nil)
var _ identityregistry.SessionPolicyLifecycleMutationGate = (*PostgresIdentitySessionPolicyStore)(nil)

// RunSessionPolicyMutation keeps every same-process Select, Reset, and Registry
// lifecycle transaction outside the main pool while an accepted effect is in
// flight. Cross-process ordering remains authoritative in PostgreSQL.
func (s *PostgresIdentitySessionPolicyStore) RunSessionPolicyMutation(
	ctx context.Context,
	mutation func() error,
) error {
	return s.runIdentitySessionPolicyMutation(ctx, mutation)
}

func (s *PostgresIdentitySessionPolicyStore) runIdentitySessionPolicyMutation(
	ctx context.Context,
	mutation func() error,
) error {
	if ctx == nil || mutation == nil {
		return ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return ErrIdentitySessionPolicyStoreUnavailable
	}
	if isSessionPolicyEffectContext(ctx, s) {
		return ErrIdentitySessionPolicyInvalid
	}
	if err := s.effectGate.lockWrite(ctx); err != nil {
		return err
	}
	defer s.effectGate.unlockWrite()
	return mutation()
}

func (s *PostgresIdentitySessionPolicyStore) InvalidateSessionPolicySelectionTx(
	ctx context.Context,
	tx pgx.Tx,
	transition identityregistry.SessionPolicyLifecycleTransition,
) error {
	if ctx == nil || tx == nil || !s.configured() ||
		transition.OwnerExtensionID == "" || transition.ActorUserID <= 0 ||
		transition.LifecycleAuditEventID <= 0 {
		return ErrIdentitySessionPolicyInvalid
	}

	actorUserID, err := lockIdentitySessionPolicyLifecycleActor(ctx, tx, transition.ActorUserID)
	if err != nil {
		return err
	}
	if err := lockIdentitySessionPolicySelection(ctx, tx); err != nil {
		return err
	}
	previous, present, err := currentIdentitySessionPolicySelectionTx(ctx, tx, true)
	if err != nil {
		return err
	}
	if !present || previous.PolicyID == IdentitySessionPolicyCoreDefault ||
		previous.OwnerExtensionID != transition.OwnerExtensionID {
		return nil
	}
	if preserved, ok := identitySessionPolicyEvidenceForDurableProvider(transition.PreservedProvider); ok &&
		previous.IdentitySessionPolicyEvidence == preserved {
		return nil
	}

	previousEvidence := previous.IdentitySessionPolicyEvidence
	nextRevision := previous.Revision + 1
	auditID, err := insertIdentitySessionPolicyAudit(
		ctx,
		tx,
		IdentitySessionPolicyActionInvalidate,
		&previousEvidence,
		nil,
		nextRevision,
		actorUserID,
		identitySessionPolicyLifecycleInvalidationReason,
		transition.LifecycleAuditEventID,
	)
	if err != nil {
		return err
	}
	if _, err := resetIdentitySessionPolicySelection(
		ctx,
		tx,
		previous.Revision,
		actorUserID,
		auditID,
	); err != nil {
		return err
	}
	_, err = insertIdentitySessionPolicyEvent(
		ctx,
		tx,
		IdentitySessionPolicyActionInvalidate,
		&previousEvidence,
		nil,
		actorUserID,
		auditID,
		identitySessionPolicyLifecycleInvalidationReason,
		nextRevision,
	)
	return err
}

func lockIdentitySessionPolicyLifecycleActor(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
) (int64, error) {
	var locked int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM users WHERE id = $1 FOR KEY SHARE
	`, actorUserID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return locked, err
}

func identitySessionPolicyEvidenceForDurableProvider(
	tip *identityregistry.DurableDeclarationTip,
) (IdentitySessionPolicyEvidence, bool) {
	if tip == nil || tip.IdentityKind != identityregistry.TombstoneKindProvider ||
		tip.RegistryState != identityregistry.RegistryStateActive || tip.StableID == "" ||
		tip.OwnerExtensionID == "" || tip.ExtensionVersionID <= 0 || tip.ExtensionVersion == "" ||
		tip.PackageDigest == "" || tip.ContractVersion == "" || tip.Revision <= 0 {
		return IdentitySessionPolicyEvidence{}, false
	}
	evidence := IdentitySessionPolicyEvidence{
		PolicyID:                tip.StableID,
		ProviderContractVersion: tip.ContractVersion,
		OwnerExtensionID:        tip.OwnerExtensionID,
		OwnerExtensionVersionID: tip.ExtensionVersionID,
		OwnerExtensionVersion:   tip.ExtensionVersion,
		OwnerPackageDigest:      tip.PackageDigest,
		DeclarationRevision:     tip.Revision,
	}
	return evidence, validIdentitySessionPolicyEvidence(evidence)
}
