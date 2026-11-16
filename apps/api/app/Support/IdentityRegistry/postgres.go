package identityregistry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore persists Identity Registry ownership tips and role suggestions.
// Approval consumes the catalog and adds one mapping plus immutable evidence.
//
// trustImpactValidator is required only for LegacyPublicationAdopter. Ordinary
// Store/PublicationStore paths ignore it. NewPostgresStore leaves it nil so
// legacy adoption fails closed unless production or tests inject a verifier.
type PostgresStore struct {
	pool                      *pgxpool.Pool
	trustImpactValidator      StoredTrustImpactValidator
	sessionPolicyInvalidator  SessionPolicyLifecycleInvalidator
	sessionPolicyMutationGate SessionPolicyLifecycleMutationGate
}

// PostgresStoreDependencies are instance-scoped production dependencies. Test
// seams may omit the lifecycle invalidator only when they do not exercise the
// selected Session Policy retirement boundary.
type PostgresStoreDependencies struct {
	StoredTrustImpactValidator StoredTrustImpactValidator
	SessionPolicyInvalidator   SessionPolicyLifecycleInvalidator
}

const (
	maxRoleSuggestionDecisionAttempts   = 3
	roleSuggestionCommitReadbackTimeout = 2 * time.Second
)

// NewPostgresStore constructs the ordinary Identity Registry repository.
// Legacy adoption is unavailable until a store is built with
// NewPostgresStoreWithStoredTrustImpactValidator.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// NewPostgresStoreWithDependencies constructs the production repository with
// every cross-module transaction hook made explicit and instance-scoped.
func NewPostgresStoreWithDependencies(
	pool *pgxpool.Pool,
	dependencies PostgresStoreDependencies,
) *PostgresStore {
	mutationGate, _ := dependencies.SessionPolicyInvalidator.(SessionPolicyLifecycleMutationGate)
	return &PostgresStore{
		pool:                      pool,
		trustImpactValidator:      dependencies.StoredTrustImpactValidator,
		sessionPolicyInvalidator:  dependencies.SessionPolicyInvalidator,
		sessionPolicyMutationGate: mutationGate,
	}
}

// NewPostgresStoreWithStoredTrustImpactValidator constructs a repository that
// may adopt pre-feature enabled plugins. validator must be the production
// Models/Extensions.ValidateStoredTrustImpact (or an equivalent test double);
// a nil validator keeps adoption fail-closed.
func NewPostgresStoreWithStoredTrustImpactValidator(
	pool *pgxpool.Pool,
	validator StoredTrustImpactValidator,
) *PostgresStore {
	return NewPostgresStoreWithDependencies(pool, PostgresStoreDependencies{
		StoredTrustImpactValidator: validator,
	})
}

// HasStoredTrustImpactValidator reports whether this instance can perform
// legacy adoption. Ordinary NewPostgresStore returns false; production
// lifecycle wiring and tests use this probe without exposing the function.
func (s *PostgresStore) HasStoredTrustImpactValidator() bool {
	return s != nil && s.trustImpactValidator != nil
}

// HasSessionPolicyLifecycleInvalidator reports whether Registry retirement can
// atomically reset a selected third-party Session Policy before unpublication.
func (s *PostgresStore) HasSessionPolicyLifecycleInvalidator() bool {
	return s != nil && s.sessionPolicyInvalidator != nil
}

// HasSessionPolicyLifecycleMutationGate reports whether lifecycle writers wait
// outside the main pool while accepted session effects are in flight.
func (s *PostgresStore) HasSessionPolicyLifecycleMutationGate() bool {
	return s != nil && s.sessionPolicyMutationGate != nil
}

func (s *PostgresStore) LoadDurableState(ctx context.Context) (DurableState, error) {
	if s == nil || s.pool == nil || ctx == nil {
		return DurableState{}, ErrInvalid
	}

	// Owners and declaration tips are one restart unit. A shared snapshot avoids
	// observing a lifecycle commit between the two queries and restoring only
	// half of its permanent ownership graph.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return DurableState{}, mapStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	state, err := loadDurableStateFrom(ctx, tx)
	if err != nil {
		return DurableState{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DurableState{}, mapStoreError(err)
	}
	return state, nil
}

func (s *PostgresStore) ListRoleSuggestions(
	ctx context.Context,
	filter RoleSuggestionFilter,
) ([]RoleSuggestion, error) {
	page, err := s.ListRoleSuggestionPage(ctx, RoleSuggestionPageInput{Filter: filter})
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func (s *PostgresStore) ListRoleSuggestionPage(
	ctx context.Context,
	input RoleSuggestionPageInput,
) (RoleSuggestionPage, error) {
	if s == nil || s.pool == nil || ctx == nil {
		return RoleSuggestionPage{}, ErrInvalid
	}
	filter := normalizeRoleSuggestionFilter(input.Filter)
	if filter.ApprovalState != "" &&
		filter.ApprovalState != RoleSuggestionPending &&
		filter.ApprovalState != RoleSuggestionApproved &&
		filter.ApprovalState != RoleSuggestionRejected {
		return RoleSuggestionPage{}, ErrInvalid
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return RoleSuggestionPage{}, mapStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	cursor, err := decodeRoleSuggestionCursor(input.Cursor, filter)
	if err != nil {
		return RoleSuggestionPage{}, err
	}
	if cursor.HighWaterID == 0 {
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(max(id), 0)
			FROM extension_permission_role_suggestions
			WHERE ($1 = '' OR approval_state = $1)
			  AND ($2 = '' OR role_key = $2)
			  AND ($3 = '' OR permission_key = $3)
			  AND ($4 = '' OR owner_extension_id = $4)
		`, filter.ApprovalState, filter.RoleKey, filter.PermissionKey, filter.OwnerExtensionID).Scan(&cursor.HighWaterID); err != nil {
			return RoleSuggestionPage{}, mapStoreError(err)
		}
	}

	rows, err := tx.Query(ctx, roleSuggestionSelectSQL+`
			WHERE ($1 = '' OR suggestion.approval_state = $1)
			  AND ($2 = '' OR suggestion.role_key = $2)
			  AND ($3 = '' OR suggestion.permission_key = $3)
			  AND ($4 = '' OR suggestion.owner_extension_id = $4)
			  AND suggestion.id > $5
			  AND suggestion.id <= $6
			ORDER BY suggestion.id ASC
			LIMIT $7
		`, filter.ApprovalState, filter.RoleKey, filter.PermissionKey,
		filter.OwnerExtensionID, cursor.AfterID, cursor.HighWaterID, filter.Limit+1)
	if err != nil {
		return RoleSuggestionPage{}, mapStoreError(err)
	}
	defer rows.Close()

	result := make([]RoleSuggestion, 0, filter.Limit+1)
	for rows.Next() {
		suggestion, scanErr := scanRoleSuggestion(rows)
		if scanErr != nil {
			return RoleSuggestionPage{}, mapStoreError(scanErr)
		}
		result = append(result, suggestion)
	}
	if err := rows.Err(); err != nil {
		return RoleSuggestionPage{}, mapStoreError(err)
	}
	rows.Close()

	page := RoleSuggestionPage{Items: result}
	if len(result) > filter.Limit {
		page.Items = result[:filter.Limit]
		next := roleSuggestionCursor{
			Version: roleSuggestionCursorVersion, AfterID: page.Items[len(page.Items)-1].ID,
			HighWaterID: cursor.HighWaterID, ApprovalState: filter.ApprovalState,
			RoleKey: filter.RoleKey, PermissionKey: filter.PermissionKey,
			OwnerExtensionID: filter.OwnerExtensionID,
		}
		page.NextCursor, err = encodeRoleSuggestionCursor(next)
		if err != nil {
			return RoleSuggestionPage{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RoleSuggestionPage{}, mapStoreError(err)
	}
	return page, nil
}

func encodeRoleSuggestionCursor(cursor roleSuggestionCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", ErrInvalid
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeRoleSuggestionCursor(raw string, filter RoleSuggestionFilter) (roleSuggestionCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return roleSuggestionCursor{Version: roleSuggestionCursorVersion}, nil
	}
	if raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return roleSuggestionCursor{}, ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return roleSuggestionCursor{}, ErrInvalid
	}
	var cursor roleSuggestionCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return roleSuggestionCursor{}, ErrInvalid
	}
	if cursor.Version != roleSuggestionCursorVersion || cursor.AfterID <= 0 ||
		cursor.HighWaterID <= 0 || cursor.AfterID >= cursor.HighWaterID ||
		cursor.ApprovalState != filter.ApprovalState || cursor.RoleKey != filter.RoleKey ||
		cursor.PermissionKey != filter.PermissionKey ||
		cursor.OwnerExtensionID != filter.OwnerExtensionID {
		return roleSuggestionCursor{}, ErrInvalid
	}
	return cursor, nil
}

func (s *PostgresStore) DecideRoleSuggestion(
	ctx context.Context,
	input DecideRoleSuggestionInput,
) (RoleSuggestion, error) {
	if s == nil || s.pool == nil || ctx == nil || !validRoleSuggestionDecision(input) {
		return RoleSuggestion{}, ErrInvalid
	}
	approvalState := strings.ToLower(strings.TrimSpace(input.ApprovalState))
	action := roleSuggestionDecisionAction(approvalState)
	if action == "" {
		return RoleSuggestion{}, ErrInvalid
	}
	for attempt := 0; attempt < maxRoleSuggestionDecisionAttempts; attempt++ {
		updated, err := s.decideRoleSuggestionOnce(ctx, input, approvalState, action)
		if !errors.Is(err, errRetryableIdentityRegistryTransaction) {
			return updated, err
		}
		if ctx.Err() != nil {
			return RoleSuggestion{}, mapStoreError(ctx.Err())
		}
	}
	// Repeated PostgreSQL serialization/deadlock aborts are exposed as a stable
	// CAS conflict. The caller must refresh before attempting another decision.
	return RoleSuggestion{}, ErrRevisionConflict
}

// decideTerminalRoleSuggestion handles legacy apply and exact idempotent replay
// of a terminal decision/grant without rewriting suggestion history.
func decideTerminalRoleSuggestion(
	ctx context.Context,
	tx pgx.Tx,
	current RoleSuggestion,
	input DecideRoleSuggestionInput,
	approvalState string,
	action string,
) (RoleSuggestion, error) {
	// Exact replay of the original pending→terminal decision.
	if input.ExpectedRevision == current.Revision-1 &&
		current.ApprovalState == approvalState &&
		(approvalState == RoleSuggestionRejected || current.Applied) {
		if err := validateRoleSuggestionReplay(
			ctx, tx, current, input, action, current.DecisionAuditEventID, false,
		); err != nil {
			return RoleSuggestion{}, err
		}
		return current, nil
	}

	// Legacy approved review-only row: explicit apply with expected revision 2.
	if current.ApprovalState == RoleSuggestionApproved &&
		approvalState == RoleSuggestionApproved &&
		!current.Applied &&
		input.ExpectedRevision == current.Revision {
		if err := lockActiveRoleSuggestionArtifact(ctx, tx, current); err != nil {
			return RoleSuggestion{}, err
		}
		if err := lockAndAuthorizeRoleSuggestionActor(ctx, tx, input.ActorUserID); err != nil {
			return RoleSuggestion{}, err
		}
		roleID, err := lockRoleSuggestionTarget(ctx, tx, current.RoleKey)
		if err != nil {
			return RoleSuggestion{}, err
		}
		if err := requireRoleSuggestionCatalog(ctx, tx, current); err != nil {
			return RoleSuggestion{}, err
		}
		// 同一条授予不变量（R8-BE-06）也覆盖 legacy 显式 apply：028 只审不批的遗留行
		// 升级后仍是 approved ∧ ¬Applied ∧ revision 2，这条分支同样插 role_permissions。
		// 漏了它，只持 role.manage 的人就能靠「apply 一条历史建议」横向提权。
		if err := requireRoleSuggestionActorHoldsPermission(
			ctx, tx, input.ActorUserID, current.PermissionKey,
		); err != nil {
			return RoleSuggestion{}, err
		}
		rolePermissionAdded, err := addRoleSuggestionPermission(ctx, tx, roleID, current.PermissionKey)
		if err != nil {
			return RoleSuggestion{}, err
		}
		// New actor-bound apply audit. The grant retains it independently from the
		// original review audit, so legacy apply never rewrites decision history.
		auditEventID, err := insertRoleSuggestionAuditEvent(
			ctx, tx, action, input.ActorUserID, current, approvalState,
			input.ExpectedRevision, rolePermissionAdded, true,
		)
		if err != nil {
			return RoleSuggestion{}, err
		}
		if err := insertRoleSuggestionGrant(
			ctx, tx, current, roleID, input.ActorUserID, auditEventID,
		); err != nil {
			return RoleSuggestion{}, err
		}
		return loadRoleSuggestion(ctx, tx, current.ID)
	}

	// Exact replay of a completed apply (approved + Applied, expected rev 2).
	if current.ApprovalState == RoleSuggestionApproved &&
		approvalState == RoleSuggestionApproved &&
		current.Applied &&
		input.ExpectedRevision == current.Revision {
		if err := validateRoleSuggestionReplay(
			ctx, tx, current, input, action, current.AppliedAuditEventID, true,
		); err != nil {
			return RoleSuggestion{}, err
		}
		return current, nil
	}

	return RoleSuggestion{}, ErrRevisionConflict
}

func (s *PostgresStore) readbackRoleSuggestionDecision(
	ctx context.Context,
	input DecideRoleSuggestionInput,
	approvalState string,
	commitErr error,
) (RoleSuggestion, error) {
	// Ambiguous commit: if the durable terminal/grant matches this decision,
	// return it instead of forcing the client through a revision conflict. The
	// original request may already be canceled after COMMIT, so readback gets a
	// fresh, short deadline while preserving context values.
	readbackCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), roleSuggestionCommitReadbackTimeout,
	)
	defer cancel()

	tx, err := s.pool.BeginTx(readbackCtx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
	})
	if err != nil {
		return RoleSuggestion{}, mapStoreError(commitErr)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	current, err := loadRoleSuggestion(readbackCtx, tx, input.ID)
	if err != nil {
		return RoleSuggestion{}, mapStoreError(commitErr)
	}
	if current.ApprovalState != approvalState {
		return RoleSuggestion{}, mapStoreError(commitErr)
	}
	legacyApply := approvalState == RoleSuggestionApproved &&
		current.Applied && input.ExpectedRevision == current.Revision
	originalDecision := input.ExpectedRevision == current.Revision-1 &&
		(approvalState == RoleSuggestionRejected ||
			(approvalState == RoleSuggestionApproved && current.Applied))
	if !legacyApply && !originalDecision {
		return RoleSuggestion{}, mapStoreError(commitErr)
	}
	auditEventID := current.DecisionAuditEventID
	if legacyApply {
		auditEventID = current.AppliedAuditEventID
	}
	if err := validateRoleSuggestionReplay(
		readbackCtx, tx, current, input, roleSuggestionDecisionAction(approvalState),
		auditEventID, legacyApply,
	); err != nil {
		return RoleSuggestion{}, err
	}
	return current, nil
}

const roleSuggestionSelectSQL = `
	SELECT suggestion.id, suggestion.permission_key, suggestion.owner_extension_id,
	       suggestion.extension_version_id, suggestion.extension_version,
	       suggestion.package_digest, suggestion.permission_contract_version,
	       suggestion.declaration_digest, suggestion.role_key,
	       suggestion.approval_state, suggestion.revision,
	       suggestion.decided_by_user_id, suggestion.decision_audit_event_id,
	       suggestion.decided_at, suggestion.created_at, suggestion.updated_at,
	       (grant_row.suggestion_id IS NOT NULL) AS applied,
	       grant_row.applied_by_user_id, grant_row.applied_audit_event_id,
	       grant_row.applied_at
	FROM extension_permission_role_suggestions AS suggestion
	LEFT JOIN extension_permission_role_grants AS grant_row
	  ON grant_row.suggestion_id = suggestion.id
`

type roleSuggestionScanner interface {
	Scan(dest ...any) error
}

func scanRoleSuggestion(scanner roleSuggestionScanner) (RoleSuggestion, error) {
	var suggestion RoleSuggestion
	var decidedBy, decisionAudit *int64
	var decidedAt *time.Time
	var appliedBy, appliedAudit *int64
	var appliedAt *time.Time
	err := scanner.Scan(
		&suggestion.ID, &suggestion.PermissionKey, &suggestion.OwnerExtensionID,
		&suggestion.ExtensionVersionID, &suggestion.ExtensionVersion, &suggestion.PackageDigest,
		&suggestion.PermissionContractVersion, &suggestion.DeclarationDigest, &suggestion.RoleKey,
		&suggestion.ApprovalState, &suggestion.Revision, &decidedBy, &decisionAudit,
		&decidedAt, &suggestion.CreatedAt, &suggestion.UpdatedAt,
		&suggestion.Applied, &appliedBy, &appliedAudit, &appliedAt,
	)
	if err != nil {
		return RoleSuggestion{}, err
	}
	if decidedBy != nil {
		suggestion.DecidedByUserID = *decidedBy
	}
	if decisionAudit != nil {
		suggestion.DecisionAuditEventID = *decisionAudit
	}
	if decidedAt != nil {
		value := *decidedAt
		suggestion.DecidedAt = &value
	}
	if appliedBy != nil {
		suggestion.AppliedByUserID = *appliedBy
	}
	if appliedAudit != nil {
		suggestion.AppliedAuditEventID = *appliedAudit
	}
	if appliedAt != nil {
		value := *appliedAt
		suggestion.AppliedAt = &value
	}
	return suggestion, nil
}

func lockRoleSuggestion(ctx context.Context, tx pgx.Tx, id int64) (RoleSuggestion, error) {
	suggestion, err := scanRoleSuggestion(tx.QueryRow(ctx, roleSuggestionSelectSQL+`
		WHERE suggestion.id = $1
		FOR UPDATE OF suggestion
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleSuggestion{}, ErrNotFound
	}
	if err != nil {
		return RoleSuggestion{}, mapStoreError(err)
	}
	return suggestion, nil
}

func loadRoleSuggestion(ctx context.Context, tx pgx.Tx, id int64) (RoleSuggestion, error) {
	suggestion, err := scanRoleSuggestion(tx.QueryRow(ctx, roleSuggestionSelectSQL+`
		WHERE suggestion.id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleSuggestion{}, ErrNotFound
	}
	if err != nil {
		return RoleSuggestion{}, mapStoreError(err)
	}
	return suggestion, nil
}

func lockActiveRoleSuggestionArtifact(
	ctx context.Context,
	tx pgx.Tx,
	suggestion RoleSuggestion,
) error {
	var artifactValid bool
	err := tx.QueryRow(ctx, `
		SELECT TRUE
		FROM extension_versions AS version
		JOIN extensions AS extension ON extension.id = version.extension_id
		WHERE version.id = $1
		  AND version.extension_id = $2
		  AND version.version = $3
		  AND version.package_digest = $4
		  AND extension.type = 'plugin'
		  AND extension.status = 'enabled'
		  AND extension.active_version_id = version.id
		FOR NO KEY UPDATE OF version, extension
	`, suggestion.ExtensionVersionID, suggestion.OwnerExtensionID,
		suggestion.ExtensionVersion, suggestion.PackageDigest).Scan(&artifactValid)
	if errors.Is(err, pgx.ErrNoRows) || !artifactValid {
		return ErrStale
	}
	if err != nil {
		return mapStoreError(err)
	}

	var owner string
	err = tx.QueryRow(ctx, `
		SELECT owner_extension_id
		FROM extension_identity_registry_owners
		WHERE identity_kind = 'permission'
		  AND stable_id = $1
		  AND owner_extension_id = $2
		FOR UPDATE
	`, suggestion.PermissionKey, suggestion.OwnerExtensionID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) || owner != suggestion.OwnerExtensionID {
		return ErrStale
	}
	if err != nil {
		return mapStoreError(err)
	}

	var active bool
	err = tx.QueryRow(ctx, `
		SELECT registry_state = 'active'
		  AND owner_extension_id = $2
		  AND extension_version_id = $3
		  AND extension_version = $4
		  AND package_digest = $5
		  AND contract_version = $6
		  AND declaration_digest = $7
		FROM extension_identity_registry_declarations
		WHERE identity_kind = 'permission'
		  AND stable_id = $1
		ORDER BY revision DESC
		LIMIT 1
	`, suggestion.PermissionKey, suggestion.OwnerExtensionID,
		suggestion.ExtensionVersionID, suggestion.ExtensionVersion,
		suggestion.PackageDigest, suggestion.PermissionContractVersion,
		suggestion.DeclarationDigest).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return ErrStale
	}
	if err != nil {
		return mapStoreError(err)
	}
	return nil
}

func lockAndAuthorizeRoleSuggestionActor(ctx context.Context, tx pgx.Tx, actorUserID int64) error {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status
		FROM users
		WHERE id = $1
		FOR KEY SHARE
	`, actorUserID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || status != "active" {
		return ErrUnauthorized
	}
	if err != nil {
		return mapStoreError(err)
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `
		SELECT extension_identity_actor_can_manage_roles($1)
	`, actorUserID).Scan(&allowed); err != nil {
		return mapStoreError(err)
	}
	if !allowed {
		return ErrUnauthorized
	}
	return nil
}

// requireRoleSuggestionActorHoldsPermission 是授予不变量的第二半（R8-BE-06）：
// 批准者必须自己已持有被建议的权限。少了它，role.manage 就成了横向提权通道 ——
// 批准「给 member 加 database.manage」再以 member 身份重新登录即可取得该权限，
// 与建号铸权（BE2-01）、改角色权限（BE2-02）是同一个不变量的第三处写路径。
//
// 判定必须留在 SQL：deny 优先、override allow、生效角色的有效权限语义已由
// extension_identity_actor_holds_permission 权威实现（与 trigger 用的
// extension_identity_actor_can_manage_roles 同构），在 Go 里重写会与它漂移。
// 驳回不调用这里：驳回不授予任何权限，必须永远可用。
func requireRoleSuggestionActorHoldsPermission(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
	permissionKey string,
) error {
	var holds bool
	if err := tx.QueryRow(ctx, `
		SELECT extension_identity_actor_holds_permission($1, $2)
	`, actorUserID, permissionKey).Scan(&holds); err != nil {
		return mapStoreError(err)
	}
	if !holds {
		return ErrUnauthorized
	}
	return nil
}

func validateRoleSuggestionReplay(
	ctx context.Context,
	tx pgx.Tx,
	current RoleSuggestion,
	input DecideRoleSuggestionInput,
	action string,
	auditEventID int64,
	legacyApply bool,
) error {
	if err := lockAndAuthorizeRoleSuggestionActor(ctx, tx, input.ActorUserID); err != nil {
		return err
	}

	evidenceActorID := current.DecidedByUserID
	if legacyApply {
		evidenceActorID = current.AppliedByUserID
	}
	if evidenceActorID != input.ActorUserID {
		return ErrUnauthorized
	}
	if auditEventID <= 0 {
		return ErrRevisionConflict
	}

	var auditActorID *int64
	var auditAction string
	var metadataJSON []byte
	if err := tx.QueryRow(ctx, `
		SELECT actor_user_id, action, metadata
		FROM audit_events
		WHERE id = $1
		FOR KEY SHARE
	`, auditEventID).Scan(&auditActorID, &auditAction, &metadataJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRevisionConflict
		}
		return mapStoreError(err)
	}
	if auditActorID == nil || *auditActorID != input.ActorUserID {
		return ErrUnauthorized
	}

	var evidence roleSuggestionAuditEvidence
	if err := json.Unmarshal(metadataJSON, &evidence); err != nil {
		return ErrRevisionConflict
	}
	if auditAction != action ||
		evidence.SuggestionID != current.ID ||
		evidence.PermissionKey != current.PermissionKey ||
		evidence.OwnerExtensionID != current.OwnerExtensionID ||
		evidence.ExtensionVersionID != current.ExtensionVersionID ||
		evidence.ExtensionVersion != current.ExtensionVersion ||
		evidence.PackageDigest != current.PackageDigest ||
		evidence.PermissionContractVersion != current.PermissionContractVersion ||
		evidence.DeclarationDigest != current.DeclarationDigest ||
		evidence.RoleKey != current.RoleKey ||
		evidence.ExpectedRevision != input.ExpectedRevision ||
		evidence.ApprovalState != current.ApprovalState ||
		evidence.PermissionCatalogRegistered == nil || *evidence.PermissionCatalogRegistered ||
		evidence.RolePermissionAdded == nil ||
		evidence.RoleGrantApplied == nil {
		return ErrRevisionConflict
	}

	if current.ApprovalState == RoleSuggestionRejected {
		if *evidence.RolePermissionAdded || *evidence.RoleGrantApplied || current.Applied {
			return ErrRevisionConflict
		}
		return nil
	}
	if !*evidence.RoleGrantApplied || !current.Applied ||
		current.AppliedByUserID != input.ActorUserID ||
		current.AppliedAuditEventID != auditEventID {
		return ErrRevisionConflict
	}
	return nil
}

func lockRoleSuggestionTarget(ctx context.Context, tx pgx.Tx, roleKey string) (int64, error) {
	var roleID int64
	err := tx.QueryRow(ctx, `
		SELECT id
		FROM roles
		WHERE key = $1
		  AND key <> 'super_admin'
		  AND is_enabled = TRUE
		FOR NO KEY UPDATE
	`, roleKey).Scan(&roleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrTargetConflict
	}
	if err != nil {
		return 0, mapStoreError(err)
	}
	return roleID, nil
}

// requireRoleSuggestionCatalog consumes an existing declaration-bound catalog
// row. Approval never creates Host permissions or catalog ownership.
func requireRoleSuggestionCatalog(
	ctx context.Context,
	tx pgx.Tx,
	suggestion RoleSuggestion,
) error {
	var catalogOwner string
	err := tx.QueryRow(ctx, `
		SELECT catalog.owner_extension_id
		FROM extension_permission_catalog AS catalog
		JOIN permissions AS permission
		  ON permission.key = catalog.permission_key
		WHERE catalog.permission_key = $1
		FOR KEY SHARE OF catalog, permission
	`, suggestion.PermissionKey).Scan(&catalogOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTargetConflict
	}
	if err != nil {
		return mapStoreError(err)
	}
	if catalogOwner != suggestion.OwnerExtensionID {
		return ErrTargetConflict
	}
	return nil
}

func addRoleSuggestionPermission(
	ctx context.Context,
	tx pgx.Tx,
	roleID int64,
	permissionKey string,
) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, roleID, permissionKey)
	if err != nil {
		return false, mapStoreError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func insertRoleSuggestionAuditEvent(
	ctx context.Context,
	tx pgx.Tx,
	action string,
	actorUserID int64,
	suggestion RoleSuggestion,
	approvalState string,
	expectedRevision int64,
	rolePermissionAdded bool,
	roleGrantApplied bool,
) (int64, error) {
	metadata, err := json.Marshal(roleSuggestionAuditMetadata{
		SuggestionID:                suggestion.ID,
		PermissionKey:               suggestion.PermissionKey,
		OwnerExtensionID:            suggestion.OwnerExtensionID,
		ExtensionVersionID:          suggestion.ExtensionVersionID,
		ExtensionVersion:            suggestion.ExtensionVersion,
		PackageDigest:               suggestion.PackageDigest,
		PermissionContractVersion:   suggestion.PermissionContractVersion,
		DeclarationDigest:           suggestion.DeclarationDigest,
		RoleKey:                     suggestion.RoleKey,
		ExpectedRevision:            expectedRevision,
		ApprovalState:               approvalState,
		PermissionCatalogRegistered: false,
		RolePermissionAdded:         rolePermissionAdded,
		RoleGrantApplied:            roleGrantApplied,
	})
	if err != nil {
		return 0, ErrInvalid
	}
	var auditEventID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, $2, $3::jsonb)
		RETURNING id
	`, actorUserID, action, string(metadata)).Scan(&auditEventID)
	if err != nil {
		return 0, mapStoreError(err)
	}
	if auditEventID <= 0 {
		return 0, ErrInvalid
	}
	return auditEventID, nil
}

func insertRoleSuggestionGrant(
	ctx context.Context,
	tx pgx.Tx,
	suggestion RoleSuggestion,
	roleID, actorUserID, auditEventID int64,
) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO extension_permission_role_grants (
			suggestion_id, permission_key, owner_extension_id, role_key, role_id,
			applied_by_user_id, applied_audit_event_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (suggestion_id) DO NOTHING
	`, suggestion.ID, suggestion.PermissionKey, suggestion.OwnerExtensionID,
		suggestion.RoleKey, roleID, actorUserID, auditEventID)
	if err != nil {
		return mapStoreError(err)
	}
	if tag.RowsAffected() != 1 {
		// Suggestion FOR UPDATE makes this an idempotency defense, not a normal
		// race path. Never accept foreign evidence if that lock order regresses.
		var existingPermission, existingOwner, existingRole string
		var existingRoleID, existingActor, existingAudit int64
		if scanErr := tx.QueryRow(ctx, `
			SELECT permission_key, owner_extension_id, role_key, role_id,
			       applied_by_user_id, applied_audit_event_id
			FROM extension_permission_role_grants
			WHERE suggestion_id = $1
		`, suggestion.ID).Scan(
			&existingPermission, &existingOwner, &existingRole, &existingRoleID,
			&existingActor, &existingAudit,
		); scanErr != nil {
			return mapStoreError(scanErr)
		}
		if existingPermission != suggestion.PermissionKey ||
			existingOwner != suggestion.OwnerExtensionID ||
			existingRole != suggestion.RoleKey ||
			existingRoleID != roleID || existingActor != actorUserID ||
			existingAudit != auditEventID {
			return ErrRevisionConflict
		}
		return nil
	}
	return nil
}

func casDecideRoleSuggestion(
	ctx context.Context,
	tx pgx.Tx,
	id, expectedRevision int64,
	approvalState string,
	actorUserID, auditEventID int64,
) (RoleSuggestion, error) {
	// decided_at/updated_at 由迁移 trigger 统一赋值，避免应用时钟漂移。
	// Grant evidence must already exist for approved decisions so the trigger
	// can verify the additive mapping without creating catalog ownership.
	tag, err := tx.Exec(ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = $2,
		    revision = revision + 1,
		    decided_by_user_id = $3,
		    decision_audit_event_id = $4
		WHERE id = $1
		  AND revision = $5
		  AND approval_state = 'pending'
	`, id, approvalState, actorUserID, auditEventID, expectedRevision)
	if err != nil {
		return RoleSuggestion{}, mapStoreError(err)
	}
	if tag.RowsAffected() != 1 {
		return RoleSuggestion{}, ErrRevisionConflict
	}
	return loadRoleSuggestion(ctx, tx, id)
}

// mapStoreError collapses PostgreSQL details into stable sentinels so callers
// never surface raw constraint/trigger text to HTTP or logs by default.
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalid),
		errors.Is(err, ErrNotFound),
		errors.Is(err, ErrRevisionConflict),
		errors.Is(err, ErrStale),
		errors.Is(err, ErrTargetConflict),
		errors.Is(err, ErrUnauthorized):
		return err
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return errIdentityRegistryStore
	}
	switch pgErr.Code {
	case "40001", "40P01":
		return errRetryableIdentityRegistryTransaction
	case "23505":
		return ErrRevisionConflict
	case "23503":
		// Exact catalog, audit, role, and actor references are all fail-closed.
		return ErrInvalid
	case "P0001", "23514":
		message := strings.ToLower(pgErr.Message)
		switch {
		case strings.Contains(message, "decision is stale"):
			return ErrStale
		case strings.Contains(message, "exact artifact is inactive"):
			return ErrStale
		case strings.Contains(message, "target is unavailable"):
			return ErrTargetConflict
		case strings.Contains(message, "host catalog is unavailable"):
			return ErrTargetConflict
		case strings.Contains(message, "catalog is unavailable"):
			return ErrTargetConflict
		case strings.Contains(message, "grant evidence is missing"):
			return ErrInvalid
		case strings.Contains(message, "actor lacks role.manage"):
			return ErrUnauthorized
		case strings.Contains(message, "host cas evidence"):
			return ErrRevisionConflict
		case strings.Contains(message, "actor is not active"):
			return ErrInvalid
		default:
			return ErrInvalid
		}
	default:
		return errIdentityRegistryStore
	}
}

var errIdentityRegistryStore = errors.New("identity registry store operation failed")
var errRetryableIdentityRegistryTransaction = errors.New("identity registry transaction should retry")

var _ Store = (*PostgresStore)(nil)
