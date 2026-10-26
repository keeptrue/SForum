package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	identityUserFieldTransactionAttempts = 3
	identityUserFieldReadbackTimeout     = 2 * time.Second
)

var errIdentityUserFieldRetry = errors.New("identity: user-field transaction should retry")
var errIdentityUserFieldCommitNotVerified = errors.New("identity: user-field commit was not verified")

type PostgresIdentityUserFieldValueStore struct {
	pool      *pgxpool.Pool
	registry  *identityregistry.Registry
	digestKey []byte
}

type IdentityUserFieldPrivacyTxStore interface {
	IdentityUserFieldPrivacyStore
	EraseForPrivacyTx(
		context.Context,
		pgx.Tx,
		EraseIdentityUserFieldValueInput,
	) (IdentityUserFieldValueMutation, error)
}

var (
	_ IdentityUserFieldValueStore     = (*PostgresIdentityUserFieldValueStore)(nil)
	_ IdentityUserFieldPrivacyTxStore = (*PostgresIdentityUserFieldValueStore)(nil)
)

func NewPostgresIdentityUserFieldValueStore(
	pool *pgxpool.Pool,
	registry *identityregistry.Registry,
	digestKey []byte,
) (*PostgresIdentityUserFieldValueStore, error) {
	if pool == nil || registry == nil {
		return nil, ErrIdentityUserFieldValueStoreUnavailable
	}
	if len(digestKey) != 32 {
		return nil, ErrIdentityUserFieldDigestKeyInvalid
	}
	return &PostgresIdentityUserFieldValueStore{
		pool: pool, registry: registry, digestKey: append([]byte(nil), digestKey...),
	}, nil
}

func (s *PostgresIdentityUserFieldValueStore) Set(
	ctx context.Context,
	input SetIdentityUserFieldValueInput,
) (IdentityUserFieldValueMutation, error) {
	prepared, err := prepareIdentityUserFieldSet(input)
	if err != nil || ctx == nil {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStoreUnavailable
	}
	return s.mutate(ctx, prepared.idempotencyKey, IdentityUserFieldValueActionSet,
		func(tx pgx.Tx) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, string, error) {
			return s.setPreparedTx(ctx, tx, prepared)
		})
}

func (s *PostgresIdentityUserFieldValueStore) SetTx(
	ctx context.Context,
	tx pgx.Tx,
	input SetIdentityUserFieldValueInput,
) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, error) {
	prepared, err := prepareIdentityUserFieldSet(input)
	if err != nil || ctx == nil || tx == nil {
		return IdentityUserFieldValueMutation{}, nil, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueMutation{}, nil, ErrIdentityUserFieldValueStoreUnavailable
	}
	result, fence, _, err := s.setPreparedTx(ctx, tx, prepared)
	return result, fence, callerIdentityUserFieldStoreError(mapIdentityUserFieldStoreError(err))
}

func (s *PostgresIdentityUserFieldValueStore) Erase(
	ctx context.Context,
	input EraseIdentityUserFieldValueInput,
) (IdentityUserFieldValueMutation, error) {
	prepared, err := prepareIdentityUserFieldErase(input)
	if err != nil || prepared.actorUserID <= 0 || ctx == nil {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStoreUnavailable
	}
	fingerprint, err := identityUserFieldEraseFingerprint(prepared, "write")
	if err != nil {
		return IdentityUserFieldValueMutation{}, err
	}
	return s.mutate(ctx, prepared.idempotencyKey, IdentityUserFieldValueActionErase,
		func(tx pgx.Tx) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, string, error) {
			return s.erasePreparedTx(ctx, tx, prepared, false, fingerprint)
		})
}

func (s *PostgresIdentityUserFieldValueStore) EraseTx(
	ctx context.Context,
	tx pgx.Tx,
	input EraseIdentityUserFieldValueInput,
) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, error) {
	prepared, err := prepareIdentityUserFieldErase(input)
	if err != nil || prepared.actorUserID <= 0 || ctx == nil || tx == nil {
		return IdentityUserFieldValueMutation{}, nil, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueMutation{}, nil, ErrIdentityUserFieldValueStoreUnavailable
	}
	fingerprint, err := identityUserFieldEraseFingerprint(prepared, "write")
	if err != nil {
		return IdentityUserFieldValueMutation{}, nil, err
	}
	result, fence, _, err := s.erasePreparedTx(ctx, tx, prepared, false, fingerprint)
	return result, fence, callerIdentityUserFieldStoreError(mapIdentityUserFieldStoreError(err))
}

func (s *PostgresIdentityUserFieldValueStore) EraseForPrivacy(
	ctx context.Context,
	input EraseIdentityUserFieldValueInput,
) (IdentityUserFieldValueMutation, error) {
	prepared, err := prepareIdentityUserFieldErase(input)
	if err != nil || ctx == nil {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStoreUnavailable
	}
	fingerprint, err := identityUserFieldEraseFingerprint(prepared, "privacy")
	if err != nil {
		return IdentityUserFieldValueMutation{}, err
	}
	return s.mutate(ctx, prepared.idempotencyKey, IdentityUserFieldValueActionErase,
		func(tx pgx.Tx) (IdentityUserFieldValueMutation, IdentityUserFieldCommitFence, string, error) {
			return s.erasePreparedTx(ctx, tx, prepared, true, fingerprint)
		})
}

func (s *PostgresIdentityUserFieldValueStore) EraseForPrivacyTx(
	ctx context.Context,
	tx pgx.Tx,
	input EraseIdentityUserFieldValueInput,
) (IdentityUserFieldValueMutation, error) {
	prepared, err := prepareIdentityUserFieldErase(input)
	if err != nil || ctx == nil || tx == nil {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStoreUnavailable
	}
	fingerprint, err := identityUserFieldEraseFingerprint(prepared, "privacy")
	if err != nil {
		return IdentityUserFieldValueMutation{}, err
	}
	result, _, _, err := s.erasePreparedTx(ctx, tx, prepared, true, fingerprint)
	return result, callerIdentityUserFieldStoreError(mapIdentityUserFieldStoreError(err))
}

func (s *PostgresIdentityUserFieldValueStore) Get(
	ctx context.Context,
	input ReadIdentityUserFieldValueInput,
) (IdentityUserFieldValueRead, error) {
	input.FieldID = strings.ToLower(strings.TrimSpace(input.FieldID))
	if ctx == nil || input.ActorUserID <= 0 || input.UserID <= 0 ||
		!validIdentityUserFieldID(input.FieldID) {
		return IdentityUserFieldValueRead{}, ErrIdentityUserFieldValueInvalid
	}
	if !s.configured() {
		return IdentityUserFieldValueRead{}, ErrIdentityUserFieldValueStoreUnavailable
	}
	field, registryRevision, err := s.resolveLiveField(input.FieldID)
	if err != nil {
		return IdentityUserFieldValueRead{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return IdentityUserFieldValueRead{}, mapIdentityUserFieldStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tip, err := lockExactIdentityUserField(ctx, tx, field)
	if err == nil {
		err = lockIdentityUserFieldUsers(ctx, tx, input.ActorUserID, input.UserID)
	}
	if err == nil {
		err = authorizeIdentityUserFieldPermission(ctx, tx, input.ActorUserID, field.ReadPermission)
	}
	var stored storedIdentityUserFieldValue
	if err == nil {
		stored, err = scanStoredIdentityUserFieldValue(
			tx.QueryRow(
				ctx,
				identityUserFieldValueSelect+` WHERE user_id = $1 AND field_id = $2 FOR SHARE`,
				input.UserID,
				input.FieldID,
			),
			s.valueDigest,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrIdentityUserFieldValueNotFound
		}
	}
	if err == nil && stored.metadata.State == IdentityUserFieldValueStateErased {
		err = ErrIdentityUserFieldValueNotFound
	}
	if err == nil && !identityUserFieldValueProvenanceMatches(
		stored.metadata, field.ID, field.Artifact.ExtensionID,
		field.ContractVersion, field.SchemaDigest, tip.revision,
	) {
		err = ErrIdentityUserFieldDeclarationStale
	}
	if err == nil {
		err = mapIdentityUserFieldSchemaError(s.registry.ValidateUserFieldValue(
			identityregistry.UserFieldSchemaClaim{
				FieldID: field.ID, ContractVersion: field.ContractVersion, Artifact: field.Artifact,
			},
			stored.decoded,
		))
	}
	if err == nil {
		err = newIdentityUserFieldCommitFence(s.registry, registryRevision, field)()
	}
	if err != nil {
		return IdentityUserFieldValueRead{}, publicIdentityUserFieldStoreError(mapIdentityUserFieldStoreError(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return IdentityUserFieldValueRead{}, publicIdentityUserFieldStoreError(mapIdentityUserFieldStoreError(err))
	}
	return IdentityUserFieldValueRead{
		IdentityUserFieldValue: stored.metadata,
		Value:                  append([]byte(nil), stored.raw...),
	}, nil
}

func (s *PostgresIdentityUserFieldValueStore) configured() bool {
	return s != nil && s.pool != nil && s.registry != nil && len(s.digestKey) == 32
}

func (s *PostgresIdentityUserFieldValueStore) valueDigest(
	userID int64,
	fieldID string,
	value []byte,
) string {
	return identityUserFieldHMAC(s.digestKey, userID, fieldID, value)
}

func (s *PostgresIdentityUserFieldValueStore) resolveLiveField(
	fieldID string,
) (identityregistry.UserFieldContribution, uint64, error) {
	for range 3 {
		revision := s.registry.Revision()
		field, err := s.registry.ResolveUserField(fieldID)
		if err != nil {
			return identityregistry.UserFieldContribution{}, 0, ErrIdentityUserFieldDeclarationStale
		}
		if revision != s.registry.Revision() {
			continue
		}
		if field.Artifact.Core || field.Artifact.VersionID <= 0 ||
			!validIdentityUserFieldDigest(field.SchemaDigest) {
			return identityregistry.UserFieldContribution{}, 0, ErrIdentityUserFieldSchemaUnavailable
		}
		return field, revision, nil
	}
	return identityregistry.UserFieldContribution{}, 0, ErrIdentityUserFieldDeclarationStale
}

func (s *PostgresIdentityUserFieldValueStore) mutate(
	ctx context.Context,
	idempotencyKey string,
	action string,
	operation func(pgx.Tx) (
		IdentityUserFieldValueMutation,
		IdentityUserFieldCommitFence,
		string,
		error,
	),
) (IdentityUserFieldValueMutation, error) {
	for attempt := 0; attempt < identityUserFieldTransactionAttempts; attempt++ {
		tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return IdentityUserFieldValueMutation{}, mapIdentityUserFieldStoreError(err)
		}
		result, fence, fingerprint, operationErr := operation(tx)
		if operationErr != nil {
			_ = tx.Rollback(context.Background())
			mayFollowConcurrentCommit := identityUserFieldErrorMayFollowConcurrentCommit(operationErr)
			mapped := mapIdentityUserFieldStoreError(operationErr)
			if fingerprint != "" && mayFollowConcurrentCommit {
				verified, found, readbackErr := s.readbackMutation(
					ctx, idempotencyKey, action, fingerprint,
				)
				if readbackErr == nil && found {
					verified.Replayed = true
					return verified, nil
				}
			}
			if errors.Is(mapped, errIdentityUserFieldRetry) &&
				attempt+1 < identityUserFieldTransactionAttempts && ctx.Err() == nil {
				continue
			}
			return IdentityUserFieldValueMutation{}, publicIdentityUserFieldStoreError(mapped)
		}

		if fence != nil {
			if err := fence(); err != nil {
				_ = tx.Rollback(context.Background())
				return IdentityUserFieldValueMutation{}, fmt.Errorf(
					"finalize identity user-field admission: %w", err,
				)
			}
		}
		if err := tx.Commit(ctx); err == nil {
			return result, nil
		} else if identityUserFieldCommitDefinitelyFailed(err) {
			mapped := mapIdentityUserFieldStoreError(err)
			// Fence is created inside each attempt and has no external side effect;
			// a definitely failed commit may retry with a fresh transaction/fence.
			if errors.Is(mapped, errIdentityUserFieldRetry) &&
				attempt+1 < identityUserFieldTransactionAttempts && ctx.Err() == nil {
				continue
			}
			return IdentityUserFieldValueMutation{}, publicIdentityUserFieldStoreError(mapped)
		} else {
			verified, found, verificationErr := s.readbackMutation(
				ctx, idempotencyKey, action, fingerprint,
			)
			if verificationErr == nil && found {
				verified.Replayed = true
				return verified, nil
			}
			if verificationErr == nil {
				verificationErr = errIdentityUserFieldCommitNotVerified
			}
			return IdentityUserFieldValueMutation{}, &IdentityUserFieldCommitUnknownError{
				CommitError:       fmt.Errorf("commit identity user-field value: %w", err),
				VerificationError: verificationErr,
			}
		}
	}
	return IdentityUserFieldValueMutation{}, ErrIdentityUserFieldValueStateConflict
}

func (s *PostgresIdentityUserFieldValueStore) readbackMutation(
	ctx context.Context,
	key string,
	action string,
	fingerprint string,
) (IdentityUserFieldValueMutation, bool, error) {
	readbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), identityUserFieldReadbackTimeout)
	defer cancel()
	tx, err := s.pool.BeginTx(readbackCtx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return IdentityUserFieldValueMutation{}, false, fmt.Errorf("begin identity user-field readback: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result, found, err := replayIdentityUserFieldMutation(
		readbackCtx, tx, key, action, fingerprint, s.valueDigest,
	)
	if err != nil || !found {
		return IdentityUserFieldValueMutation{}, found, err
	}
	if err := tx.Commit(readbackCtx); err != nil {
		return IdentityUserFieldValueMutation{}, false, fmt.Errorf("commit identity user-field readback: %w", err)
	}
	return result, true, nil
}
