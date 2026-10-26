package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

var _ IdentitySessionPolicyEffectStore = (*PostgresIdentitySessionPolicyStore)(nil)

const identitySessionPolicyEffectCleanupTimeout = 2 * time.Second

// RunIfCurrent serializes the accepted Host session effect with every
// Select/Reset/lifecycle invalidation. The transaction is lock-only and always
// rolls back after the callback; the callback owns its separate Host effects.
func (s *PostgresIdentitySessionPolicyStore) RunIfCurrent(
	ctx context.Context,
	expected IdentitySessionPolicyResolution,
	authority IdentitySessionAuthority,
	effect func(context.Context) error,
) error {
	if ctx == nil || authority.UserID <= 0 || authority.TokenVersion < 0 || effect == nil {
		return ErrIdentitySessionPolicyInvalid
	}
	if !s.configured() {
		return ErrIdentitySessionPolicyStoreUnavailable
	}
	if err := s.acquireIdentitySessionPolicyEffectConnection(ctx); err != nil {
		return mapIdentitySessionPolicyStoreError(err)
	}
	defer s.releaseIdentitySessionPolicyEffectConnection()

	// Same-process mutations wait before borrowing from the main pool. The lock
	// transaction itself uses a direct bounded connection so the Host callback
	// can safely write audit/session state through a MaxConns=1 main pool.
	if err := s.effectGate.lockRead(ctx); err != nil {
		return mapIdentitySessionPolicyStoreError(err)
	}
	defer s.effectGate.unlockRead()
	connection, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig)
	if err != nil {
		return mapIdentitySessionPolicyStoreError(err)
	}
	var tx pgx.Tx
	defer func() { cleanupIdentitySessionPolicyEffectConnection(ctx, tx, connection) }()
	tx, err = connection.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return mapIdentitySessionPolicyStoreError(err)
	}

	if expected.Source == IdentitySessionPolicySourcePlugin {
		if expected.Provider == nil || expected.Selection == nil {
			return ErrIdentitySessionPolicyDeclarationStale
		}
		tip, err := lockExactIdentitySessionPolicyProvider(ctx, tx, *expected.Provider)
		if err != nil {
			return publicIdentitySessionPolicyStoreError(err)
		}
		if identitySessionPolicyEvidenceForProvider(*expected.Provider, tip.revision) !=
			expected.Selection.IdentitySessionPolicyEvidence {
			return ErrIdentitySessionPolicyDeclarationStale
		}
	}
	if err := lockIdentitySessionPolicyEffectUser(ctx, tx, authority); err != nil {
		return publicIdentitySessionPolicyStoreError(err)
	}
	// Accepted effects share this lock with each other; Select/Reset/lifecycle
	// take the existing exclusive lock and therefore wait until every accepted
	// issue/renew callback has returned.
	if err := lockIdentitySessionPolicyEffectSelection(ctx, tx); err != nil {
		return mapIdentitySessionPolicyStoreError(err)
	}
	current, _, err := currentIdentitySessionPolicySelectionTx(ctx, tx, false)
	if err != nil {
		return mapIdentitySessionPolicyStoreError(err)
	}
	return s.registry.RunWithSessionPolicyLease(expected.PolicyID, func(claim identityregistry.SessionPolicyLeaseClaim) error {
		if err := validateIdentitySessionPolicyEffectResolution(expected, current, claim); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		effectCtx, endEffect := beginSessionPolicyEffectContext(ctx, s)
		defer endEffect()
		return effect(effectCtx)
	})
}

func (s *PostgresIdentitySessionPolicyStore) acquireIdentitySessionPolicyEffectConnection(ctx context.Context) error {
	select {
	case s.effectConnectionSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *PostgresIdentitySessionPolicyStore) releaseIdentitySessionPolicyEffectConnection() {
	<-s.effectConnectionSlots
}

func cleanupIdentitySessionPolicyEffectConnection(
	parent context.Context,
	tx pgx.Tx,
	connection *pgx.Conn,
) {
	if tx != nil {
		rollbackCtx, cancelRollback := context.WithTimeout(
			context.WithoutCancel(parent),
			identitySessionPolicyEffectCleanupTimeout,
		)
		_ = tx.Rollback(rollbackCtx)
		cancelRollback()
	}
	if connection != nil {
		closeCtx, cancelClose := context.WithTimeout(
			context.WithoutCancel(parent),
			identitySessionPolicyEffectCleanupTimeout,
		)
		_ = connection.Close(closeCtx)
		cancelClose()
	}
}

func lockIdentitySessionPolicyEffectUser(
	ctx context.Context,
	tx pgx.Tx,
	authority IdentitySessionAuthority,
) error {
	var status UserStatus
	var tokenVersion int64
	err := tx.QueryRow(ctx, `
		SELECT status, current_token_version
		FROM users
		WHERE id = $1
		FOR SHARE
	`, authority.UserID).Scan(&status, &tokenVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentitySessionPolicyDeclarationStale
	}
	if err != nil {
		return err
	}
	if status != UserStatusActive || tokenVersion != authority.TokenVersion {
		return ErrIdentitySessionPolicyDeclarationStale
	}
	return nil
}

func validateIdentitySessionPolicyEffectResolution(
	expected IdentitySessionPolicyResolution,
	current IdentitySessionPolicySelection,
	claim identityregistry.SessionPolicyLeaseClaim,
) error {
	switch expected.Source {
	case IdentitySessionPolicySourceSafeMode:
		if expected.PolicyID != IdentitySessionPolicyCoreDefault || expected.Provider != nil || !claim.SafeMode {
			return ErrIdentitySessionPolicyDeclarationStale
		}
		return nil
	case IdentitySessionPolicySourceCore:
		if expected.PolicyID != IdentitySessionPolicyCoreDefault || expected.Provider != nil ||
			expected.Selection == nil || claim.SafeMode ||
			current.Revision != expected.Selection.Revision ||
			current.IdentitySessionPolicyEvidence != expected.Selection.IdentitySessionPolicyEvidence {
			return ErrIdentitySessionPolicyDeclarationStale
		}
		return nil
	case IdentitySessionPolicySourcePlugin:
		if expected.Provider == nil || expected.Selection == nil || claim.SafeMode ||
			current.Revision != expected.Selection.Revision ||
			current.IdentitySessionPolicyEvidence != expected.Selection.IdentitySessionPolicyEvidence {
			return ErrIdentitySessionPolicyDeclarationStale
		}
		if claim.Provider == nil || !identitySessionPolicyProviderMatches(*claim.Provider, *expected.Provider) {
			return ErrIdentitySessionPolicyDeclarationStale
		}
		return nil
	default:
		return ErrIdentitySessionPolicyDeclarationStale
	}
}
