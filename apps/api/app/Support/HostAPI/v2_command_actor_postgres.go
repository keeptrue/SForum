package hostapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func (b *PostgresProtocolV2CommandBackend) AuthorizeActorDelegation(
	ctx context.Context,
	tx pgx.Tx,
	scope protocolV2CommandScope,
	delegation protocolV2VerifiedActorDelegation,
	fingerprint string,
	receiptExists bool,
	requiredPermissions []string,
) (int64, error) {
	if b == nil || tx == nil || !validResolvedProtocolV2CommandScope(scope) ||
		delegation.ActorUserID <= 0 || !protocolV2SHA256Hex(delegation.DelegationIDDigest) ||
		delegation.RuntimeEpoch <= 0 || delegation.RuntimeInstanceID == "" || scope.RuntimeEpoch <= 0 || scope.RuntimeInstanceID == "" ||
		delegation.RuntimeEpoch != scope.RuntimeEpoch || delegation.RuntimeInstanceID != scope.RuntimeInstanceID ||
		!protocolV2SHA256Hex(fingerprint) {
		return 0, invalidProtocolV2CommandActorDelegation()
	}
	actor, err := loadProtocolV2CommandActor(ctx, tx, delegation.ActorUserID)
	if err != nil {
		return 0, err
	}
	for _, permission := range requiredPermissions {
		if !actor.Can(permission) {
			return 0, deniedProtocolV2CommandActorPermission()
		}
	}

	var existingActorID int64
	var existingFingerprint string
	err = tx.QueryRow(ctx, `
		SELECT actor_user_id, request_fingerprint
		FROM extension_host_command_actor_delegation_consumptions
		WHERE extension_id = $1 AND command_id = $2
		  AND command_version = $3 AND idempotency_key = $4
		FOR UPDATE
	`, scope.ExtensionID, scope.CommandID, scope.CommandVersion, scope.IdempotencyKey).Scan(
		&existingActorID, &existingFingerprint,
	)
	switch {
	case err == nil:
		if !receiptExists || existingActorID != delegation.ActorUserID || existingFingerprint != fingerprint {
			return 0, replayedProtocolV2CommandActorDelegation()
		}
		return actor.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, fmt.Errorf("load Host Command actor delegation consumption: %w", err)
	case receiptExists:
		return 0, missingProtocolV2CommandActorDelegationEvidence()
	}

	commandTag, err := tx.Exec(ctx, `
		INSERT INTO extension_host_command_actor_delegation_consumptions (
			delegation_id_digest,
			extension_id, extension_version_id, extension_version, package_digest,
			runtime_epoch, runtime_instance_id, actor_user_id,
			command_id, command_version, audience, idempotency_key, request_fingerprint,
			issued_at, not_before, expires_at, consumed_at
		)
		SELECT
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, statement_timestamp()
		WHERE $15 <= statement_timestamp() AND $16 >= statement_timestamp()
	`, delegation.DelegationIDDigest,
		scope.ExtensionID, scope.ExtensionVersionID, scope.ExtensionVersion, scope.PackageDigest,
		scope.RuntimeEpoch, scope.RuntimeInstanceID, actor.ID,
		scope.CommandID, scope.CommandVersion, ProtocolV2ActorDelegationAudience,
		scope.IdempotencyKey, fingerprint,
		delegation.IssuedAt, delegation.NotBefore, delegation.ExpiresAt,
	)
	if err != nil {
		var postgresErr *pgconn.PgError
		if errors.As(err, &postgresErr) && postgresErr.Code == "23505" {
			return 0, replayedProtocolV2CommandActorDelegation()
		}
		return 0, fmt.Errorf("consume Host Command actor delegation: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return 0, invalidProtocolV2CommandActorDelegation()
	}
	return actor.ID, nil
}

func loadProtocolV2CommandActor(ctx context.Context, tx pgx.Tx, userID int64) (identity.Actor, error) {
	actor, err := identity.LoadEffectiveActorTx(ctx, tx, userID)
	if errors.Is(err, identity.ErrActorInactive) {
		return identity.Actor{}, inactiveProtocolV2CommandActor()
	}
	if err != nil {
		return identity.Actor{}, fmt.Errorf("load Host Command actor: %w", err)
	}
	return actor, nil
}

func invalidProtocolV2CommandActorDelegation() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED,
		"host.command_actor_delegation_invalid",
		"The Host-signed actor delegation is invalid or stale.",
		false,
	)
}

func replayedProtocolV2CommandActorDelegation() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
		"host.command_actor_delegation_replayed",
		"The Host-signed actor delegation was already used for another command request.",
		false,
	)
}

func missingProtocolV2CommandActorDelegationEvidence() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
		"host.command_actor_delegation_evidence_missing",
		"The committed command has no matching actor delegation evidence.",
		false,
	)
}

func inactiveProtocolV2CommandActor() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
		"host.command_actor_inactive",
		"The delegated actor is missing or inactive.",
		false,
	)
}

func deniedProtocolV2CommandActorPermission() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
		"host.command_actor_permission_denied",
		"The delegated actor no longer has permission to execute this Host Command.",
		false,
	)
}

var _ protocolV2CommandBackend = (*PostgresProtocolV2CommandBackend)(nil)
