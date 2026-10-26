package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func (s *PostgresIdentityUserFieldValueStore) setPreparedTx(
	ctx context.Context,
	tx pgx.Tx,
	input preparedIdentityUserFieldSet,
) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, string, error) {
	if err := requireIdentityUserFieldSerializableTransaction(ctx, tx); err != nil {
		return IdentityUserFieldValueMutation{}, nil, "", err
	}
	if err := lockIdentityUserFieldIdempotencyKey(ctx, tx, input.idempotencyKey); err != nil {
		return IdentityUserFieldValueMutation{}, nil, "", err
	}
	input, err := s.canonicalizeIdentityUserFieldSet(ctx, tx, input)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, "", err
	}
	fingerprint, err := identityUserFieldSetFingerprint(input)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, "", err
	}
	field, registryRevision, err := s.resolveLiveField(input.fieldID)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	tip, err := lockExactIdentityUserField(ctx, tx, field)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if err := lockIdentityUserFieldUsers(ctx, tx, input.actorUserID, input.userID); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if err := authorizeIdentityUserFieldPermission(ctx, tx, input.actorUserID, field.WritePermission); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	claim := identityregistry.UserFieldSchemaClaim{
		FieldID: field.ID, ContractVersion: field.ContractVersion, Artifact: field.Artifact,
	}
	if err := mapIdentityUserFieldSchemaError(s.registry.ValidateUserFieldValue(claim, input.value)); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if err := lockIdentityUserFieldValueKey(ctx, tx, input.userID, input.fieldID); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	receipt, receiptFound, err := loadIdentityUserFieldEvent(ctx, tx, input.idempotencyKey)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if receiptFound {
		replayed, err := resolveIdentityUserFieldReplay(
			ctx, tx, receipt, IdentityUserFieldValueActionSet, fingerprint, s.valueDigest,
		)
		if err == nil && !identityUserFieldValueProvenanceMatches(
			replayed.Value, field.ID, field.Artifact.ExtensionID,
			field.ContractVersion, field.SchemaDigest, tip.revision,
		) {
			err = ErrIdentityUserFieldDeclarationStale
		}
		return replayed, newIdentityUserFieldCommitFence(s.registry, registryRevision, field), fingerprint, err
	}
	current, found, err := s.loadIdentityUserFieldValueForUpdate(ctx, tx, input.userID, input.fieldID)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if (!found && input.expectedRevision != 0) ||
		(found && current.metadata.Revision != input.expectedRevision) {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, ErrIdentityUserFieldValueStateConflict
	}
	if found && !identityUserFieldValueProvenanceMatches(
		current.metadata, field.ID, field.Artifact.ExtensionID,
		field.ContractVersion, field.SchemaDigest, tip.revision,
	) {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, ErrIdentityUserFieldDeclarationStale
	}
	previousRevision, previousDigest := int64(0), ""
	if found {
		previousRevision = current.metadata.Revision
		previousDigest = current.metadata.ValueDigest
	}
	nextRevision := previousRevision + 1
	auditID, err := insertIdentityUserFieldAudit(ctx, tx, identityUserFieldAuditInput{
		action: IdentityUserFieldValueActionSet, mode: "write",
		userID: input.userID, fieldID: field.ID, ownerExtensionID: field.Artifact.ExtensionID,
		fieldContractVersion: field.ContractVersion, fieldSchemaDigest: field.SchemaDigest,
		declarationRevision: tip.revision, declarationDigest: tip.declarationDigest,
		previousRevision: previousRevision, nextRevision: nextRevision,
		previousValueDigest: previousDigest, nextValueDigest: input.valueDigest,
		actorUserID: input.actorUserID,
	})
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	stored, err := s.upsertIdentityUserFieldValue(
		ctx, tx, input, field, tip.revision, auditID, found,
	)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	event, err := insertIdentityUserFieldEvent(
		ctx, tx, stored.metadata, IdentityUserFieldValueActionSet,
		input.idempotencyKey, fingerprint, previousRevision, previousDigest,
		input.valueDigest, input.actorUserID, auditID,
	)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	return IdentityUserFieldValueMutation{Value: stored.metadata, Event: event, CurrentAvailable: true},
		newIdentityUserFieldCommitFence(s.registry, registryRevision, field), fingerprint, nil
}

func (s *PostgresIdentityUserFieldValueStore) canonicalizeIdentityUserFieldSet(
	ctx context.Context,
	tx pgx.Tx,
	input preparedIdentityUserFieldSet,
) (preparedIdentityUserFieldSet, error) {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT $1::jsonb`, string(input.canonicalValue)).Scan(&raw); err != nil {
		return preparedIdentityUserFieldSet{}, err
	}
	canonical, decoded, err := decodeStoredIdentityUserFieldJSON(raw)
	if err != nil {
		return preparedIdentityUserFieldSet{}, err
	}
	input.canonicalValue = canonical
	input.value = decoded
	input.valueDigest = s.valueDigest(input.userID, input.fieldID, canonical)
	return input, nil
}

func (s *PostgresIdentityUserFieldValueStore) erasePreparedTx(
	ctx context.Context,
	tx pgx.Tx,
	input preparedIdentityUserFieldErase,
	privacy bool,
	fingerprint string,
) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, string, error) {
	if err := requireIdentityUserFieldSerializableTransaction(ctx, tx); err != nil {
		return IdentityUserFieldValueMutation{}, nil, "", err
	}
	if err := lockIdentityUserFieldIdempotencyKey(ctx, tx, input.idempotencyKey); err != nil {
		return IdentityUserFieldValueMutation{}, nil, "", err
	}
	if privacy {
		receipt, receiptFound, err := loadIdentityUserFieldEvent(ctx, tx, input.idempotencyKey)
		if err != nil {
			return IdentityUserFieldValueMutation{}, nil, fingerprint, err
		}
		if receiptFound {
			replayed, err := resolveIdentityUserFieldReplay(
				ctx, tx, receipt, IdentityUserFieldValueActionErase, fingerprint, s.valueDigest,
			)
			return replayed, nil, fingerprint, err
		}
		if err := lockIdentityUserFieldPrivacyUsers(ctx, tx, input.actorUserID, input.userID); err != nil {
			return IdentityUserFieldValueMutation{}, nil, fingerprint, err
		}
		if err := lockIdentityUserFieldValueKey(ctx, tx, input.userID, input.fieldID); err != nil {
			return IdentityUserFieldValueMutation{}, nil, fingerprint, err
		}
		current, found, err := s.loadIdentityUserFieldValueForUpdate(ctx, tx, input.userID, input.fieldID)
		if err != nil {
			return IdentityUserFieldValueMutation{}, nil, fingerprint, err
		}
		if !found {
			return IdentityUserFieldValueMutation{}, nil, fingerprint, ErrIdentityUserFieldValueNotFound
		}
		result, err := s.eraseIdentityUserFieldValue(
			ctx, tx, input, current, fingerprint, "privacy", "",
		)
		return result, nil, fingerprint, err
	}

	field, registryRevision, err := s.resolveLiveField(input.fieldID)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	tip, err := lockExactIdentityUserField(ctx, tx, field)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if err := lockIdentityUserFieldUsers(ctx, tx, input.actorUserID, input.userID); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if err := authorizeIdentityUserFieldPermission(ctx, tx, input.actorUserID, field.WritePermission); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	claim := identityregistry.UserFieldSchemaClaim{
		FieldID: field.ID, ContractVersion: field.ContractVersion, Artifact: field.Artifact,
	}
	if err := mapIdentityUserFieldSchemaError(s.registry.ValidateUserFieldSchemaClaim(claim)); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if err := lockIdentityUserFieldValueKey(ctx, tx, input.userID, input.fieldID); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	receipt, receiptFound, err := loadIdentityUserFieldEvent(ctx, tx, input.idempotencyKey)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if receiptFound {
		replayed, err := resolveIdentityUserFieldReplay(
			ctx, tx, receipt, IdentityUserFieldValueActionErase, fingerprint, s.valueDigest,
		)
		if err == nil && !identityUserFieldValueProvenanceMatches(
			replayed.Value, field.ID, field.Artifact.ExtensionID,
			field.ContractVersion, field.SchemaDigest, tip.revision,
		) {
			err = ErrIdentityUserFieldDeclarationStale
		}
		return replayed, newIdentityUserFieldCommitFence(s.registry, registryRevision, field), fingerprint, err
	}
	current, found, err := s.loadIdentityUserFieldValueForUpdate(ctx, tx, input.userID, input.fieldID)
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	if !found {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, ErrIdentityUserFieldValueNotFound
	}
	if !identityUserFieldValueProvenanceMatches(
		current.metadata, field.ID, field.Artifact.ExtensionID,
		field.ContractVersion, field.SchemaDigest, tip.revision,
	) {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, ErrIdentityUserFieldDeclarationStale
	}
	if current.metadata.State != IdentityUserFieldValueStateActive ||
		current.metadata.Revision != input.expectedRevision || current.metadata.ValueDigest == "" {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, ErrIdentityUserFieldValueStateConflict
	}
	if err := mapIdentityUserFieldSchemaError(s.registry.ValidateUserFieldValue(claim, current.decoded)); err != nil {
		return IdentityUserFieldValueMutation{}, nil, fingerprint, err
	}
	result, err := s.eraseIdentityUserFieldValue(
		ctx, tx, input, current, fingerprint, "write", tip.declarationDigest,
	)
	return result, newIdentityUserFieldCommitFence(s.registry, registryRevision, field), fingerprint, err
}

func (s *PostgresIdentityUserFieldValueStore) loadIdentityUserFieldValueForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	userID int64,
	fieldID string,
) (storedIdentityUserFieldValue, bool, error) {
	value, err := scanStoredIdentityUserFieldValue(
		tx.QueryRow(
			ctx,
			identityUserFieldValueSelect+` WHERE user_id = $1 AND field_id = $2 FOR UPDATE`,
			userID,
			fieldID,
		),
		s.valueDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedIdentityUserFieldValue{}, false, nil
	}
	return value, err == nil, err
}

func (s *PostgresIdentityUserFieldValueStore) upsertIdentityUserFieldValue(
	ctx context.Context,
	tx pgx.Tx,
	input preparedIdentityUserFieldSet,
	field identityregistry.UserFieldContribution,
	declarationRevision int64,
	auditID int64,
	found bool,
) (storedIdentityUserFieldValue, error) {
	if !found {
		return scanStoredIdentityUserFieldValue(tx.QueryRow(ctx, `
			INSERT INTO identity_user_field_values (
				user_id, field_id, owner_extension_id, field_contract_version,
				field_schema_digest, declaration_revision, value_json, state, revision,
				updated_by_user_id, updated_audit_event_id
			) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, 'active', 1, $8, $9)
			RETURNING `+identityUserFieldValueColumns,
			input.userID, field.ID, field.Artifact.ExtensionID, field.ContractVersion,
			field.SchemaDigest, declarationRevision, string(input.canonicalValue),
			input.actorUserID, auditID,
		), s.valueDigest)
	}
	value, err := scanStoredIdentityUserFieldValue(tx.QueryRow(ctx, `
		UPDATE identity_user_field_values
		SET value_json = $3::jsonb,
		    state = 'active', revision = revision + 1,
		    updated_by_user_id = $4, updated_audit_event_id = $5,
		    updated_at = statement_timestamp(),
		    erased_at = NULL, erased_by_user_id = NULL, erase_audit_event_id = NULL
		WHERE user_id = $1 AND field_id = $2 AND revision = $6
		RETURNING `+identityUserFieldValueColumns,
		input.userID, field.ID, string(input.canonicalValue), input.actorUserID,
		auditID, input.expectedRevision,
	), s.valueDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedIdentityUserFieldValue{}, ErrIdentityUserFieldValueStateConflict
	}
	return value, err
}

func (s *PostgresIdentityUserFieldValueStore) eraseIdentityUserFieldValue(
	ctx context.Context,
	tx pgx.Tx,
	input preparedIdentityUserFieldErase,
	current storedIdentityUserFieldValue,
	fingerprint string,
	mode string,
	declarationDigest string,
) (IdentityUserFieldValueMutation, error) {
	if current.metadata.State != IdentityUserFieldValueStateActive ||
		current.metadata.Revision != input.expectedRevision || current.metadata.ValueDigest == "" {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStateConflict
	}
	nextRevision := current.metadata.Revision + 1
	auditID, err := insertIdentityUserFieldAudit(ctx, tx, identityUserFieldAuditInput{
		action: IdentityUserFieldValueActionErase, mode: mode,
		userID: current.metadata.UserID, fieldID: current.metadata.FieldID,
		ownerExtensionID:     current.metadata.OwnerExtensionID,
		fieldContractVersion: current.metadata.FieldContractVersion,
		fieldSchemaDigest:    current.metadata.FieldSchemaDigest,
		declarationRevision:  current.metadata.DeclarationRevision,
		declarationDigest:    declarationDigest,
		previousRevision:     current.metadata.Revision, nextRevision: nextRevision,
		previousValueDigest: current.metadata.ValueDigest,
		actorUserID:         input.actorUserID,
	})
	if err != nil {
		return IdentityUserFieldValueMutation{}, err
	}
	stored, err := scanStoredIdentityUserFieldValue(tx.QueryRow(ctx, `
		UPDATE identity_user_field_values
		SET value_json = NULL, state = 'erased', revision = revision + 1,
		    updated_by_user_id = $3, updated_audit_event_id = $4,
		    updated_at = statement_timestamp(), erased_at = statement_timestamp(),
		    erased_by_user_id = $3, erase_audit_event_id = $4
		WHERE user_id = $1 AND field_id = $2 AND revision = $5 AND state = 'active'
		RETURNING `+identityUserFieldValueColumns,
		current.metadata.UserID, current.metadata.FieldID,
		nullableIdentityUserFieldActor(input.actorUserID), auditID, input.expectedRevision,
	), s.valueDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStateConflict
	}
	if err != nil {
		return IdentityUserFieldValueMutation{}, err
	}
	event, err := insertIdentityUserFieldEvent(
		ctx, tx, stored.metadata, IdentityUserFieldValueActionErase,
		input.idempotencyKey, fingerprint, current.metadata.Revision,
		current.metadata.ValueDigest, "", input.actorUserID, auditID,
	)
	if err != nil {
		return IdentityUserFieldValueMutation{}, err
	}
	return IdentityUserFieldValueMutation{Value: stored.metadata, Event: event, CurrentAvailable: true}, nil
}
