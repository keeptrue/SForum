package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	protocolV2CommandAuditSchema         = "sforum.host-command-audit@1"
	protocolV2HostCommandLegacyAuthority = "host_commands"
)

// PostgresProtocolV2CommandBackend stores command audit and replay evidence in
// the same transaction as domain writes. It has no command catalog of its own.
type PostgresProtocolV2CommandBackend struct {
	pool *pgxpool.Pool
}

// PostgresProtocolV2HostCommandBackend adds the legacy manifest/trust authority
// gate required by Host Commands. The underlying backend deliberately remains
// authority-neutral because DatabaseService reuses its transaction receipts for
// independently approved own-schema operations.
type PostgresProtocolV2HostCommandBackend struct {
	*PostgresProtocolV2CommandBackend
}

func NewPostgresProtocolV2CommandBackend(pool *pgxpool.Pool) *PostgresProtocolV2CommandBackend {
	return &PostgresProtocolV2CommandBackend{pool: pool}
}

func NewPostgresProtocolV2HostCommandBackend(pool *pgxpool.Pool) *PostgresProtocolV2HostCommandBackend {
	return &PostgresProtocolV2HostCommandBackend{
		PostgresProtocolV2CommandBackend: NewPostgresProtocolV2CommandBackend(pool),
	}
}

func (b *PostgresProtocolV2CommandBackend) Begin(ctx context.Context) (pgx.Tx, error) {
	if b == nil || b.pool == nil || ctx == nil {
		return nil, errors.New("hostapi: PostgreSQL command backend is unavailable")
	}
	return b.pool.BeginTx(ctx, pgx.TxOptions{})
}

func (b *PostgresProtocolV2CommandBackend) ResolveScope(
	ctx context.Context,
	tx pgx.Tx,
	requested protocolV2CommandScope,
) (protocolV2CommandScope, error) {
	return b.resolveScope(ctx, tx, requested, "")
}

func (b *PostgresProtocolV2CommandBackend) ResolveHostCommandScope(
	ctx context.Context,
	tx pgx.Tx,
	requested protocolV2CommandScope,
) (protocolV2CommandScope, error) {
	return b.resolveScope(ctx, tx, requested, protocolV2HostCommandLegacyAuthority)
}

func (b *PostgresProtocolV2HostCommandBackend) ResolveScope(
	ctx context.Context,
	tx pgx.Tx,
	requested protocolV2CommandScope,
) (protocolV2CommandScope, error) {
	if b == nil || b.PostgresProtocolV2CommandBackend == nil {
		return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
	}
	return b.ResolveHostCommandScope(ctx, tx, requested)
}

func (b *PostgresProtocolV2CommandBackend) resolveScope(
	ctx context.Context,
	tx pgx.Tx,
	requested protocolV2CommandScope,
	requiredAuthority string,
) (protocolV2CommandScope, error) {
	identity := ProtocolV2RuntimeIdentityFromContext(ctx)
	if b == nil || tx == nil || identity == nil || strings.TrimSpace(requested.ExtensionID) == "" ||
		requested.ExtensionID != identity.GetExtensionId() {
		return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
	}

	var manifestDatabaseJSON []byte
	var system bool
	resolved := requested
	// Do not require active_version_id here. A live, authenticated candidate
	// broker must call Host Commands during pre-publication lifecycle hooks. The
	// broker token plus exact immutable version and live enable grant are the
	// admission fence; lifecycle drain closes the broker before revocation.
	err := tx.QueryRow(ctx, `
		SELECT extension_versions.id, extensions.source, extensions.is_system,
		       COALESCE(extension_versions.manifest -> 'database', 'null'::jsonb)
		FROM extension_versions
		JOIN extensions ON extensions.id = extension_versions.extension_id
		WHERE extension_versions.extension_id = $1
		  AND extension_versions.version = $2
		  AND extension_versions.package_digest = $3
		FOR SHARE OF extension_versions, extensions
	`, identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest()).Scan(
		&resolved.ExtensionVersionID, &resolved.AuthorityType, &system, &manifestDatabaseJSON,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
	}
	if err != nil {
		return protocolV2CommandScope{}, fmt.Errorf("resolve Host Command artifact: %w", err)
	}
	resolved.ExtensionVersion = identity.GetExtensionVersion()
	resolved.PackageDigest = identity.GetArtifactDigest()
	if identity.GetRuntimeEpoch() == 0 || identity.GetRuntimeEpoch() > uint64(^uint64(0)>>1) || strings.TrimSpace(identity.GetInstanceId()) == "" {
		return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
	}
	resolved.RuntimeEpoch = int64(identity.GetRuntimeEpoch())
	resolved.RuntimeInstanceID = identity.GetInstanceId()
	manifestGrants, err := protocolV2CommandDatabaseGrants(manifestDatabaseJSON)
	if err != nil || requiredAuthority != "" && !protocolV2CommandHasDatabaseGrant(manifestGrants, requiredAuthority) {
		return protocolV2CommandScope{}, deniedProtocolV2HostCommandAuthority()
	}

	switch resolved.AuthorityType {
	case "builtin":
		if identity.GetTrustGrantId() != "builtin" || !system {
			return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
		}
		resolved.AuthorityType = "builtin"
		resolved.TrustGrantID = 0
	case "uploaded":
		grantID, parseErr := strconv.ParseInt(identity.GetTrustGrantId(), 10, 64)
		if parseErr != nil || grantID <= 0 || strconv.FormatInt(grantID, 10) != identity.GetTrustGrantId() || system {
			return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
		}
		var liveGrantID int64
		var grantDatabaseJSON []byte
		err = tx.QueryRow(ctx, `
			SELECT id, COALESCE(impact_document -> 'database', 'null'::jsonb)
			FROM extension_trust_grants
			WHERE id = $1 AND extension_id = $2 AND extension_version = $3
			  AND package_digest = $4 AND action = 'enable' AND revoked_at IS NULL
			FOR SHARE
		`, grantID, identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest()).Scan(
			&liveGrantID, &grantDatabaseJSON,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
		}
		if err != nil {
			return protocolV2CommandScope{}, fmt.Errorf("resolve Host Command trust grant: %w", err)
		}
		grantPowers, grantErr := protocolV2CommandDatabaseGrants(grantDatabaseJSON)
		if grantErr != nil || !reflect.DeepEqual(grantPowers, manifestGrants) {
			return protocolV2CommandScope{}, deniedProtocolV2HostCommandAuthority()
		}
		resolved.AuthorityType = "trust_grant"
		resolved.TrustGrantID = liveGrantID
	default:
		return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
	}
	if !validResolvedProtocolV2CommandScope(resolved) {
		return protocolV2CommandScope{}, staleProtocolV2CommandIdentity()
	}
	return resolved, nil
}

func protocolV2CommandDatabaseGrants(document []byte) ([]string, error) {
	var database *extensionmanifest.ManifestDatabase
	if len(document) == 0 || json.Unmarshal(document, &database) != nil || database == nil {
		return nil, errors.New("hostapi: database authority declaration is invalid")
	}
	grants := extensionmanifest.DatabaseGrants(database)
	if len(grants) == 0 {
		return nil, errors.New("hostapi: database authority declaration is empty")
	}
	seen := make(map[string]bool, len(grants))
	for _, grant := range grants {
		switch grant {
		case extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantHostCommands,
			extensionmanifest.DatabaseGrantRawCore,
			extensionmanifest.DatabaseGrantKernel:
		default:
			return nil, errors.New("hostapi: database authority declaration contains an unknown grant")
		}
		if seen[grant] {
			return nil, errors.New("hostapi: database authority declaration contains a duplicate grant")
		}
		seen[grant] = true
	}
	return grants, nil
}

func protocolV2CommandHasDatabaseGrant(grants []string, required string) bool {
	for _, grant := range grants {
		if grant == required {
			return true
		}
	}
	return false
}

func (b *PostgresProtocolV2CommandBackend) LockIdempotency(
	ctx context.Context,
	tx pgx.Tx,
	scope protocolV2CommandScope,
) (*protocolV2CommandReceipt, error) {
	if tx == nil || !validResolvedProtocolV2CommandScope(scope) {
		return nil, errors.New("hostapi: resolved Host Command scope is required")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, protocolV2CommandLockKey(scope)); err != nil {
		return nil, fmt.Errorf("lock Host Command idempotency scope: %w", err)
	}

	var (
		fingerprint, transactionID, auditEventID string
		resultJSON                               []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT request_fingerprint, result, transaction_id, audit_event_id::text
		FROM extension_host_command_receipts
		WHERE extension_id = $1 AND command_id = $2
		  AND command_version = $3 AND idempotency_key = $4
		FOR UPDATE
	`, scope.ExtensionID, scope.CommandID, scope.CommandVersion, scope.IdempotencyKey).Scan(
		&fingerprint, &resultJSON, &transactionID, &auditEventID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load Host Command receipt: %w", err)
	}
	result := &hostv2.CommandResult{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(resultJSON, result); err != nil {
		return nil, fmt.Errorf("decode Host Command receipt: %w", err)
	}
	if result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED ||
		result.GetTransactionId() != transactionID || result.GetAuditEventId() != auditEventID {
		return nil, errors.New("hostapi: stored Host Command receipt is inconsistent")
	}
	return &protocolV2CommandReceipt{Fingerprint: fingerprint, Result: result}, nil
}

func (b *PostgresProtocolV2CommandBackend) SaveResult(
	ctx context.Context,
	tx pgx.Tx,
	scope protocolV2CommandScope,
	receipt protocolV2CommandReceipt,
) error {
	if tx == nil || !validResolvedProtocolV2CommandScope(scope) ||
		len(receipt.Fingerprint) != sha256.Size*2 || receipt.Result == nil ||
		receipt.Result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
		return errors.New("hostapi: invalid Host Command receipt")
	}
	auditEventID, err := strconv.ParseInt(receipt.Result.GetAuditEventId(), 10, 64)
	if err != nil || auditEventID <= 0 || strings.TrimSpace(receipt.Result.GetTransactionId()) == "" {
		return errors.New("hostapi: invalid Host Command audit or transaction reference")
	}
	resultJSON, err := (protojson.MarshalOptions{}).Marshal(receipt.Result)
	if err != nil {
		return fmt.Errorf("encode Host Command receipt: %w", err)
	}
	var trustGrantID any
	if scope.AuthorityType == "trust_grant" {
		trustGrantID = scope.TrustGrantID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO extension_host_command_receipts (
			extension_id, extension_version_id, extension_version, package_digest,
			authority_type, trust_grant_id, command_id, command_version,
			idempotency_key, request_fingerprint, result, transaction_id, audit_event_id
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13
		)
	`, scope.ExtensionID, scope.ExtensionVersionID, scope.ExtensionVersion, scope.PackageDigest,
		scope.AuthorityType, trustGrantID, scope.CommandID, scope.CommandVersion,
		scope.IdempotencyKey, receipt.Fingerprint, resultJSON,
		receipt.Result.GetTransactionId(), auditEventID)
	if err != nil {
		return fmt.Errorf("insert Host Command receipt: %w", err)
	}
	return nil
}

func (b *PostgresProtocolV2CommandBackend) AppendAudit(
	ctx context.Context,
	tx pgx.Tx,
	event protocolV2CommandAudit,
) (string, error) {
	if tx == nil || !validResolvedProtocolV2CommandScope(event.Scope) ||
		event.ExtensionID != event.Scope.ExtensionID || event.CommandID != event.Scope.CommandID ||
		event.CommandVersion != event.Scope.CommandVersion || event.IdempotencyKey != event.Scope.IdempotencyKey ||
		strings.TrimSpace(event.TransactionID) == "" || event.ActorUserID < 0 {
		return "", errors.New("hostapi: invalid Host Command audit")
	}
	impact, err := protocolV2CommandAuditImpact(event.Impact)
	if err != nil {
		return "", err
	}
	metadata, err := json.Marshal(map[string]any{
		"schemaVersion":      protocolV2CommandAuditSchema,
		"extensionId":        event.Scope.ExtensionID,
		"extensionVersionId": event.Scope.ExtensionVersionID,
		"extensionVersion":   event.Scope.ExtensionVersion,
		"packageDigest":      event.Scope.PackageDigest,
		"authorityType":      event.Scope.AuthorityType,
		"trustGrantId":       event.Scope.TrustGrantID,
		"commandId":          event.Scope.CommandID,
		"commandVersion":     event.Scope.CommandVersion,
		"idempotencyKey":     event.Scope.IdempotencyKey,
		"transactionId":      event.TransactionID,
		"impact":             impact,
	})
	if err != nil {
		return "", fmt.Errorf("encode Host Command audit: %w", err)
	}
	var actorUserID any
	if event.ActorUserID > 0 {
		actorUserID = event.ActorUserID
	}
	var auditEventID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES ($1, 'extension.host_command.committed', $2::jsonb)
		RETURNING id
	`, actorUserID, metadata).Scan(&auditEventID); err != nil {
		return "", fmt.Errorf("insert Host Command audit: %w", err)
	}
	return strconv.FormatInt(auditEventID, 10), nil
}

func protocolV2CommandAuditImpact(items []*hostv2.ImpactItem) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		body, err := (protojson.MarshalOptions{}).Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("encode Host Command audit impact: %w", err)
		}
		result = append(result, body)
	}
	return result, nil
}

func protocolV2CommandLockKey(scope protocolV2CommandScope) int64 {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"sforum:host-command-receipt@1", scope.ExtensionID, scope.CommandID,
		scope.CommandVersion, scope.IdempotencyKey,
	}, "\x00")))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}

func validResolvedProtocolV2CommandScope(scope protocolV2CommandScope) bool {
	if strings.TrimSpace(scope.ExtensionID) == "" || scope.ExtensionVersionID <= 0 ||
		strings.TrimSpace(scope.ExtensionVersion) == "" || len(scope.PackageDigest) != sha256.Size*2 ||
		strings.TrimSpace(scope.CommandID) == "" || strings.TrimSpace(scope.CommandVersion) == "" ||
		!validProtocolV2CommandIdempotencyKey(scope.IdempotencyKey) {
		return false
	}
	return scope.AuthorityType == "builtin" && scope.TrustGrantID == 0 ||
		scope.AuthorityType == "trust_grant" && scope.TrustGrantID > 0
}

func staleProtocolV2CommandIdentity() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME,
		"host.command_identity_stale",
		"The Host Command runtime identity is no longer active for this exact artifact.",
		false,
	)
}

func deniedProtocolV2HostCommandAuthority() error {
	return newProtocolV2CommandError(
		protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
		"host.command_authority_denied",
		"The exact extension artifact has no approved Host Command authority.",
		false,
	)
}

var _ protocolV2CommandBackend = (*PostgresProtocolV2CommandBackend)(nil)
var _ protocolV2CommandBackend = (*PostgresProtocolV2HostCommandBackend)(nil)
