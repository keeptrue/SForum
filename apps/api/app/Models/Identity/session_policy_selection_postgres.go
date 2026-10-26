package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	identitySessionPolicyTransactionAttempts = 3
	identitySessionPolicyReadbackTimeout     = 2 * time.Second
	identitySessionPolicyEffectConnections   = 4
)

type PostgresIdentitySessionPolicyStore struct {
	pool                  *pgxpool.Pool
	registry              *identityregistry.Registry
	effectGate            identitySessionPolicyEffectGate
	effectConnectionSlots chan struct{}
}

var _ IdentitySessionPolicyStore = (*PostgresIdentitySessionPolicyStore)(nil)

func NewPostgresIdentitySessionPolicyStore(
	pool *pgxpool.Pool,
	registry *identityregistry.Registry,
) (*PostgresIdentitySessionPolicyStore, error) {
	if pool == nil || registry == nil {
		return nil, ErrIdentitySessionPolicyStoreUnavailable
	}
	return &PostgresIdentitySessionPolicyStore{
		pool:                  pool,
		registry:              registry,
		effectConnectionSlots: make(chan struct{}, identitySessionPolicyEffectConnections),
	}, nil
}

func (s *PostgresIdentitySessionPolicyStore) Current(
	ctx context.Context,
) (IdentitySessionPolicySelection, error) {
	if ctx == nil {
		return IdentitySessionPolicySelection{}, ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return IdentitySessionPolicySelection{}, ErrIdentitySessionPolicyStoreUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return IdentitySessionPolicySelection{}, mapIdentitySessionPolicyStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	selection, _, err := currentIdentitySessionPolicySelectionTx(ctx, tx, false)
	if err != nil {
		return IdentitySessionPolicySelection{}, mapIdentitySessionPolicyStoreError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentitySessionPolicySelection{}, mapIdentitySessionPolicyStoreError(err)
	}
	return selection, nil
}

func (s *PostgresIdentitySessionPolicyStore) Candidate(
	ctx context.Context,
	policyID string,
) (IdentitySessionPolicyEvidence, error) {
	policyID = strings.ToLower(strings.TrimSpace(policyID))
	if ctx == nil || policyID == IdentitySessionPolicyCoreDefault || !validIdentityUserFieldID(policyID) {
		return IdentitySessionPolicyEvidence{}, ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return IdentitySessionPolicyEvidence{}, ErrIdentitySessionPolicyStoreUnavailable
	}
	claim, err := resolveIdentitySessionPolicyCandidate(s.registry, policyID)
	if err != nil {
		return IdentitySessionPolicyEvidence{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		return IdentitySessionPolicyEvidence{}, mapIdentitySessionPolicyStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tip, err := lockExactIdentitySessionPolicyProvider(ctx, tx, claim.provider)
	if err != nil {
		return IdentitySessionPolicyEvidence{}, publicIdentitySessionPolicyStoreError(err)
	}
	if err := validateIdentitySessionPolicyRegistryClaim(s.registry, claim); err != nil {
		return IdentitySessionPolicyEvidence{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentitySessionPolicyEvidence{}, mapIdentitySessionPolicyStoreError(err)
	}
	return identitySessionPolicyEvidenceForProvider(claim.provider, tip.revision), nil
}

func (s *PostgresIdentitySessionPolicyStore) Resolve(
	ctx context.Context,
) (IdentitySessionPolicyResolution, error) {
	if ctx == nil {
		return IdentitySessionPolicyResolution{}, ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return IdentitySessionPolicyResolution{}, ErrIdentitySessionPolicyStoreUnavailable
	}
	snapshot := s.registry.Snapshot()
	if snapshot.SafeMode {
		return identitySessionPolicySafeModeResolution(snapshot), nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return IdentitySessionPolicyResolution{}, mapIdentitySessionPolicyStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	selection, _, err := currentIdentitySessionPolicySelectionTx(ctx, tx, false)
	if err != nil {
		return IdentitySessionPolicyResolution{}, mapIdentitySessionPolicyStoreError(err)
	}
	if selection.PolicyID == IdentitySessionPolicyCoreDefault {
		if err := tx.Commit(ctx); err != nil {
			return IdentitySessionPolicyResolution{}, mapIdentitySessionPolicyStoreError(err)
		}
		return IdentitySessionPolicyResolution{
			PolicyID:         IdentitySessionPolicyCoreDefault,
			Source:           IdentitySessionPolicySourceCore,
			Selection:        &selection,
			RegistryRevision: snapshot.Revision,
			RegistryDigest:   snapshot.Digest,
		}, nil
	}
	claim, err := resolveIdentitySessionPolicyCandidateFromSnapshot(snapshot, selection.PolicyID)
	if err != nil {
		return IdentitySessionPolicyResolution{}, err
	}
	tip, err := lockExactIdentitySessionPolicyProvider(ctx, tx, claim.provider)
	if err != nil {
		return IdentitySessionPolicyResolution{}, publicIdentitySessionPolicyStoreError(err)
	}
	liveEvidence := identitySessionPolicyEvidenceForProvider(claim.provider, tip.revision)
	if selection.IdentitySessionPolicyEvidence != liveEvidence {
		return IdentitySessionPolicyResolution{}, ErrIdentitySessionPolicyDeclarationStale
	}
	if err := validateIdentitySessionPolicyRegistryClaim(s.registry, claim); err != nil {
		if errors.Is(err, ErrIdentitySessionPolicySafeMode) {
			return identitySessionPolicySafeModeResolution(s.registry.Snapshot()), nil
		}
		return IdentitySessionPolicyResolution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentitySessionPolicyResolution{}, mapIdentitySessionPolicyStoreError(err)
	}
	provider := claim.provider
	return IdentitySessionPolicyResolution{
		PolicyID:         selection.PolicyID,
		Source:           IdentitySessionPolicySourcePlugin,
		Selection:        &selection,
		Provider:         &provider,
		RegistryRevision: claim.revision,
		RegistryDigest:   claim.digest,
	}, nil
}

func identitySessionPolicySafeModeResolution(
	snapshot identityregistry.Snapshot,
) IdentitySessionPolicyResolution {
	return IdentitySessionPolicyResolution{
		PolicyID:         IdentitySessionPolicyCoreDefault,
		Source:           IdentitySessionPolicySourceSafeMode,
		RegistryRevision: snapshot.Revision,
		RegistryDigest:   snapshot.Digest,
	}
}

func (s *PostgresIdentitySessionPolicyStore) Select(
	ctx context.Context,
	input SelectIdentitySessionPolicyInput,
) (IdentitySessionPolicyMutation, error) {
	prepared, err := prepareIdentitySessionPolicySelect(input)
	if err != nil || ctx == nil {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyStoreUnavailable
	}
	var result IdentitySessionPolicyMutation
	err = s.runIdentitySessionPolicyMutation(ctx, func() error {
		var mutationErr error
		result, mutationErr = s.selectMutation(ctx, prepared)
		return mutationErr
	})
	return result, publicIdentitySessionPolicyStoreError(err)
}

func (s *PostgresIdentitySessionPolicyStore) selectMutation(
	ctx context.Context,
	prepared preparedIdentitySessionPolicySelect,
) (IdentitySessionPolicyMutation, error) {
	for attempt := 0; attempt < identitySessionPolicyTransactionAttempts; attempt++ {
		result, commitErr := s.selectOnce(ctx, prepared)
		if errors.Is(commitErr, errIdentitySessionPolicyRetry) && ctx.Err() == nil {
			continue
		}
		return result, commitErr
	}
	return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyRevisionConflict
}

func (s *PostgresIdentitySessionPolicyStore) selectOnce(
	ctx context.Context,
	input preparedIdentitySessionPolicySelect,
) (IdentitySessionPolicyMutation, error) {
	claim, err := resolveIdentitySessionPolicyCandidate(s.registry, input.candidate.PolicyID)
	if err != nil {
		return IdentitySessionPolicyMutation{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	tip, err := lockExactIdentitySessionPolicyProvider(ctx, tx, claim.provider)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	// Registry authority always precedes actor and selection locks. Lifecycle
	// invalidation follows the same order, preventing upgrade/select deadlocks.
	if err := authorizeIdentitySessionPolicyActor(ctx, tx, input.actorUserID); err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	liveEvidence := identitySessionPolicyEvidenceForProvider(claim.provider, tip.revision)
	if liveEvidence != input.candidate {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyDeclarationStale
	}
	if err := lockIdentitySessionPolicySelection(ctx, tx); err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	previous, present, err := currentIdentitySessionPolicySelectionTx(ctx, tx, true)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	if previous.Revision != input.expectedRevision {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyRevisionConflict
	}
	if present && previous.IdentitySessionPolicyEvidence == liveEvidence {
		if err := validateIdentitySessionPolicyRegistryClaim(s.registry, claim); err != nil {
			return IdentitySessionPolicyMutation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
		}
		return IdentitySessionPolicyMutation{Selection: previous}, nil
	}

	nextRevision := input.expectedRevision + 1
	var previousEvidence *IdentitySessionPolicyEvidence
	if present {
		value := previous.IdentitySessionPolicyEvidence
		previousEvidence = &value
	}
	selectedEvidence := liveEvidence
	auditID, err := insertIdentitySessionPolicyAudit(
		ctx,
		tx,
		IdentitySessionPolicyActionSelect,
		previousEvidence,
		&selectedEvidence,
		nextRevision,
		input.actorUserID,
		"",
		0,
	)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	selection, err := writeIdentitySessionPolicySelection(
		ctx,
		tx,
		liveEvidence,
		input.expectedRevision,
		input.actorUserID,
		auditID,
		present,
	)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	event, err := insertIdentitySessionPolicyEvent(
		ctx,
		tx,
		IdentitySessionPolicyActionSelect,
		previousEvidence,
		&selectedEvidence,
		input.actorUserID,
		auditID,
		"",
		nextRevision,
	)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	if err := validateIdentitySessionPolicyRegistryClaim(s.registry, claim); err != nil {
		return IdentitySessionPolicyMutation{}, err
	}
	result := IdentitySessionPolicyMutation{Selection: selection, Event: &event, Changed: true}
	if err := tx.Commit(ctx); err != nil {
		return s.resolveIdentitySessionPolicyCommit(ctx, result, err)
	}
	return result, nil
}

func (s *PostgresIdentitySessionPolicyStore) Reset(
	ctx context.Context,
	input ResetIdentitySessionPolicyInput,
) (IdentitySessionPolicyMutation, error) {
	prepared, err := prepareIdentitySessionPolicyReset(input)
	if err != nil || ctx == nil {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyStoreUnavailable
	}
	var result IdentitySessionPolicyMutation
	err = s.runIdentitySessionPolicyMutation(ctx, func() error {
		var mutationErr error
		result, mutationErr = s.reset(ctx, prepared)
		return mutationErr
	})
	return result, publicIdentitySessionPolicyStoreError(err)
}

func (s *PostgresIdentitySessionPolicyStore) reset(
	ctx context.Context,
	prepared preparedIdentitySessionPolicyReset,
) (IdentitySessionPolicyMutation, error) {
	for attempt := 0; attempt < identitySessionPolicyTransactionAttempts; attempt++ {
		result, commitErr := s.resetOnce(ctx, prepared)
		if errors.Is(commitErr, errIdentitySessionPolicyRetry) && ctx.Err() == nil {
			continue
		}
		return result, commitErr
	}
	return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyRevisionConflict
}

func (s *PostgresIdentitySessionPolicyStore) resetOnce(
	ctx context.Context,
	input preparedIdentitySessionPolicyReset,
) (IdentitySessionPolicyMutation, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := authorizeIdentitySessionPolicyActor(ctx, tx, input.actorUserID); err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	if err := lockIdentitySessionPolicySelection(ctx, tx); err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	previous, present, err := currentIdentitySessionPolicySelectionTx(ctx, tx, true)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	if previous.Revision != input.expectedRevision {
		return IdentitySessionPolicyMutation{}, ErrIdentitySessionPolicyRevisionConflict
	}
	if !present || previous.PolicyID == IdentitySessionPolicyCoreDefault {
		if err := tx.Commit(ctx); err != nil {
			return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
		}
		return IdentitySessionPolicyMutation{Selection: previous}, nil
	}

	nextRevision := input.expectedRevision + 1
	previousEvidence := previous.IdentitySessionPolicyEvidence
	auditID, err := insertIdentitySessionPolicyAudit(
		ctx,
		tx,
		IdentitySessionPolicyActionReset,
		&previousEvidence,
		nil,
		nextRevision,
		input.actorUserID,
		input.reasonCode,
		0,
	)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	selection, err := resetIdentitySessionPolicySelection(
		ctx,
		tx,
		input.expectedRevision,
		input.actorUserID,
		auditID,
	)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	event, err := insertIdentitySessionPolicyEvent(
		ctx,
		tx,
		IdentitySessionPolicyActionReset,
		&previousEvidence,
		nil,
		input.actorUserID,
		auditID,
		input.reasonCode,
		nextRevision,
	)
	if err != nil {
		return IdentitySessionPolicyMutation{}, mapIdentitySessionPolicyStoreError(err)
	}
	result := IdentitySessionPolicyMutation{Selection: selection, Event: &event, Changed: true}
	if err := tx.Commit(ctx); err != nil {
		return s.resolveIdentitySessionPolicyCommit(ctx, result, err)
	}
	return result, nil
}

func (s *PostgresIdentitySessionPolicyStore) ListEvents(
	ctx context.Context,
	limit int,
) ([]IdentitySessionPolicyEvent, error) {
	if ctx == nil {
		return nil, ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return nil, ErrIdentitySessionPolicyStoreUnavailable
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, action, previous_selection, selected_selection,
		       actor_user_id, audit_event_id, reason_code, selection_revision, created_at
		FROM identity_session_policy_selection_events
		ORDER BY selection_revision DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, mapIdentitySessionPolicyStoreError(err)
	}
	defer rows.Close()
	events := make([]IdentitySessionPolicyEvent, 0)
	for rows.Next() {
		event, err := scanIdentitySessionPolicyEvent(rows)
		if err != nil {
			return nil, mapIdentitySessionPolicyStoreError(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, mapIdentitySessionPolicyStoreError(err)
	}
	return events, nil
}
