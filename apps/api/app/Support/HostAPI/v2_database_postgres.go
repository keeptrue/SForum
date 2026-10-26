package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type postgresProtocolV2DatabaseBackend struct {
	pool               *pgxpool.Pool
	commands           *PostgresProtocolV2CommandBackend
	queryInvalidations *supportjobs.Dispatcher
}

type postgresProtocolV2DatabaseTx struct {
	tx                 pgx.Tx
	commands           *PostgresProtocolV2CommandBackend
	queryInvalidations *supportjobs.Dispatcher
	readOnly           bool
}

// NewPostgresProtocolV2DatabaseRuntime freezes the Host-owned operation
// catalog and binds it to the exact-artifact PostgreSQL authority resolver.
func NewPostgresProtocolV2DatabaseRuntime(
	pool *pgxpool.Pool,
	queries []ProtocolV2DatabaseQueryDefinition,
	executes []ProtocolV2DatabaseExecuteDefinition,
	options ...ProtocolV2DatabaseRuntimeOption,
) (ProtocolV2DatabaseRuntime, error) {
	if pool == nil {
		return nil, errors.New("hostapi: PostgreSQL database pool is required")
	}
	runtimeOptions := resolveProtocolV2DatabaseRuntimeOptions(options)
	backend := &postgresProtocolV2DatabaseBackend{
		pool: pool, commands: NewPostgresProtocolV2CommandBackend(pool),
		queryInvalidations: runtimeOptions.queryInvalidations,
	}
	return newProtocolV2DatabaseRuntime(backend, queries, executes, options...)
}

func (b *postgresProtocolV2DatabaseBackend) Begin(ctx context.Context, readOnly bool) (protocolV2DatabaseTx, error) {
	if b == nil || b.pool == nil || b.commands == nil || ctx == nil {
		return nil, errors.New("hostapi: PostgreSQL database backend is unavailable")
	}
	options := pgx.TxOptions{}
	if readOnly {
		options.AccessMode = pgx.ReadOnly
	}
	tx, err := b.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &postgresProtocolV2DatabaseTx{
		tx: tx, commands: b.commands, queryInvalidations: b.queryInvalidations, readOnly: readOnly,
	}, nil
}

func (t *postgresProtocolV2DatabaseTx) ResolveScope(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
	requiredScope string,
	operationID string,
	statementVersion string,
) (protocolV2DatabaseScope, error) {
	if t == nil || t.tx == nil || t.commands == nil || !validProtocolV2QueryIdentity(identity) {
		return protocolV2DatabaseScope{}, staleProtocolV2DatabaseIdentity()
	}
	commandScope, err := t.resolveArtifact(ctx, identity, operationID, statementVersion)
	if err != nil {
		return protocolV2DatabaseScope{}, staleProtocolV2DatabaseIdentity()
	}
	if requiredScope != ProtocolV2DatabaseOwnSchema {
		return protocolV2DatabaseScope{}, newProtocolV2DatabaseError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.database_authority_denied",
			"The exact extension artifact has no authority for this database operation.",
			false,
		)
	}
	var schemaName, roleName string
	grantQuery := `
		SELECT resources.schema_name, resources.runtime_role_name
		FROM extension_database_grants AS grants
		JOIN extension_database_resources AS resources
		  ON resources.extension_id = grants.extension_id
		WHERE grants.extension_id = $1
		  AND grants.extension_version_id = $2
		  AND grants.extension_version = $3
		  AND grants.package_digest = $4
		  AND grants.status = 'active'
		  AND grants.revoked_at IS NULL
		  AND resources.status = 'provisioned'
		  AND resources.schema_retained`
	if !t.readOnly {
		grantQuery += " FOR SHARE OF grants, resources"
	}
	err = t.tx.QueryRow(ctx, grantQuery, commandScope.ExtensionID, commandScope.ExtensionVersionID,
		commandScope.ExtensionVersion, commandScope.PackageDigest).Scan(&schemaName, &roleName)
	if errors.Is(err, pgx.ErrNoRows) {
		return protocolV2DatabaseScope{}, staleProtocolV2DatabaseIdentity()
	}
	if err != nil {
		return protocolV2DatabaseScope{}, fmt.Errorf("resolve database grant: %w", err)
	}
	powerQuery := `
		SELECT 1
		FROM extension_database_grant_powers AS powers
		JOIN extension_database_grants AS grants ON grants.id = powers.grant_id
		WHERE grants.extension_id = $1
		  AND grants.extension_version_id = $2
		  AND grants.extension_version = $3
		  AND grants.package_digest = $4
		  AND powers.power = $5`
	if !t.readOnly {
		powerQuery += " FOR SHARE OF powers"
	}
	var ownSchemaPower int
	err = t.tx.QueryRow(ctx, powerQuery, commandScope.ExtensionID, commandScope.ExtensionVersionID,
		commandScope.ExtensionVersion, commandScope.PackageDigest,
		extensionmanifest.DatabaseGrantOwnSchema).Scan(&ownSchemaPower)
	if errors.Is(err, pgx.ErrNoRows) {
		return protocolV2DatabaseScope{}, newProtocolV2DatabaseError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.database_authority_denied",
			"The exact extension artifact has no authority for this database operation.",
			false,
		)
	}
	if err != nil {
		return protocolV2DatabaseScope{}, fmt.Errorf("resolve database grant power: %w", err)
	}
	return protocolV2DatabaseScope{
		ExtensionID: commandScope.ExtensionID, ExtensionVersionID: commandScope.ExtensionVersionID,
		ExtensionVersion: commandScope.ExtensionVersion, PackageDigest: commandScope.PackageDigest,
		TrustGrantID: commandScope.TrustGrantID, AuthorityType: commandScope.AuthorityType,
		SchemaName: schemaName, RuntimeRoleName: roleName, Scope: requiredScope,
		OperationID: operationID, StatementVersion: statementVersion,
	}, nil
}

func (t *postgresProtocolV2DatabaseTx) resolveArtifact(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
	operationID string,
	statementVersion string,
) (protocolV2CommandScope, error) {
	requested := protocolV2CommandScope{
		ExtensionID: identity.GetExtensionId(), CommandID: databaseReceiptCommandID(operationID),
		CommandVersion: statementVersion, IdempotencyKey: "scope-resolution",
	}
	if !t.readOnly {
		return t.commands.ResolveScope(ctx, t.tx, requested)
	}
	var source string
	var system bool
	err := t.tx.QueryRow(ctx, `
		SELECT extension_versions.id, extensions.source, extensions.is_system
		FROM extension_versions
		JOIN extensions ON extensions.id = extension_versions.extension_id
		WHERE extension_versions.extension_id = $1
		  AND extension_versions.version = $2
		  AND extension_versions.package_digest = $3
	`, identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest()).Scan(
		&requested.ExtensionVersionID, &source, &system,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return protocolV2CommandScope{}, staleProtocolV2DatabaseIdentity()
	}
	if err != nil {
		return protocolV2CommandScope{}, fmt.Errorf("resolve database artifact: %w", err)
	}
	requested.ExtensionVersion = identity.GetExtensionVersion()
	requested.PackageDigest = identity.GetArtifactDigest()
	if identity.GetTrustGrantId() == "builtin" {
		if source != "builtin" || !system {
			return protocolV2CommandScope{}, staleProtocolV2DatabaseIdentity()
		}
		requested.AuthorityType = "builtin"
		return requested, nil
	}
	if source != "uploaded" || system {
		return protocolV2CommandScope{}, staleProtocolV2DatabaseIdentity()
	}
	grantID, parseErr := strconv.ParseInt(identity.GetTrustGrantId(), 10, 64)
	if parseErr != nil || grantID <= 0 || strconv.FormatInt(grantID, 10) != identity.GetTrustGrantId() {
		return protocolV2CommandScope{}, staleProtocolV2DatabaseIdentity()
	}
	var trusted bool
	if err := t.tx.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM extension_trust_grants
		  WHERE id = $1 AND extension_id = $2 AND extension_version = $3
		    AND package_digest = $4 AND action = 'enable' AND revoked_at IS NULL
		)
	`, grantID, identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest()).Scan(&trusted); err != nil {
		return protocolV2CommandScope{}, fmt.Errorf("resolve database trust grant: %w", err)
	}
	if !trusted {
		return protocolV2CommandScope{}, staleProtocolV2DatabaseIdentity()
	}
	requested.AuthorityType = "trust_grant"
	requested.TrustGrantID = grantID
	return requested, nil
}

func (t *postgresProtocolV2DatabaseTx) Query(
	ctx context.Context,
	scope protocolV2DatabaseScope,
	statement string,
	arguments []any,
	limit int,
	offset int,
) ([]map[string]any, error) {
	if t == nil || t.tx == nil || limit <= 0 || offset < 0 {
		return nil, errors.New("hostapi: invalid PostgreSQL database query")
	}
	if err := t.enterDatabaseScope(ctx, scope); err != nil {
		return nil, err
	}
	queryArguments := append([]any(nil), arguments...)
	queryArguments = append(queryArguments, limit, offset)
	query := "SELECT * FROM (" + statement + ") AS sforum_database_operation LIMIT $" +
		strconv.Itoa(len(arguments)+1) + " OFFSET $" + strconv.Itoa(len(arguments)+2)
	rows, err := t.tx.Query(ctx, query, queryArguments...)
	if err != nil {
		return nil, err
	}
	result, err := collectProtocolV2DatabaseRows(rows, limit)
	if err != nil {
		return nil, err
	}
	if err := t.leaveDatabaseScope(ctx, scope); err != nil {
		return nil, err
	}
	return result, nil
}

func (t *postgresProtocolV2DatabaseTx) Execute(
	ctx context.Context,
	scope protocolV2DatabaseScope,
	statement string,
	arguments []any,
	returnsRow bool,
) (uint64, []map[string]any, error) {
	if t == nil || t.tx == nil || scope.AuthorityType == "" {
		return 0, nil, errors.New("hostapi: invalid PostgreSQL database execute")
	}
	if err := t.enterDatabaseScope(ctx, scope); err != nil {
		return 0, nil, err
	}
	if !returnsRow {
		tag, err := t.tx.Exec(ctx, statement, arguments...)
		if err != nil {
			return 0, nil, err
		}
		if err := t.leaveDatabaseScope(ctx, scope); err != nil {
			return 0, nil, err
		}
		return uint64(tag.RowsAffected()), nil, nil
	}
	rows, err := t.tx.Query(ctx, statement, arguments...)
	if err != nil {
		return 0, nil, err
	}
	result, err := collectProtocolV2DatabaseRows(rows, 2)
	if err != nil {
		return 0, nil, err
	}
	affected := rows.CommandTag().RowsAffected()
	if err := t.leaveDatabaseScope(ctx, scope); err != nil {
		return 0, nil, err
	}
	return uint64(affected), result, nil
}

func (t *postgresProtocolV2DatabaseTx) LockReceipt(
	ctx context.Context,
	scope protocolV2DatabaseScope,
	_ string,
) (*protocolV2DatabaseReceipt, error) {
	if t == nil || t.tx == nil || t.commands == nil || scope.IdempotencyKey == "" {
		return nil, errors.New("hostapi: invalid database receipt scope")
	}
	receipt, err := t.commands.LockIdempotency(ctx, t.tx, databaseCommandScope(scope))
	if err != nil || receipt == nil {
		return nil, err
	}
	affected, err := strconv.ParseUint(receipt.Result.GetCommittedRevision(), 10, 64)
	if err != nil {
		return nil, errors.New("hostapi: stored database receipt is inconsistent")
	}
	return &protocolV2DatabaseReceipt{
		Fingerprint: receipt.Fingerprint, AffectedRows: affected,
		Result: cloneProtocolV2Document(receipt.Result.GetOutput()),
	}, nil
}

func (t *postgresProtocolV2DatabaseTx) SaveReceipt(
	ctx context.Context,
	scope protocolV2DatabaseScope,
	fingerprint string,
	receipt protocolV2DatabaseReceipt,
) error {
	if t == nil || t.tx == nil || t.commands == nil || receipt.Fingerprint != fingerprint {
		return errors.New("hostapi: invalid database receipt")
	}
	transactionID, err := newProtocolV2CommandID("dbtxn")
	if err != nil {
		return fmt.Errorf("create database transaction id: %w", err)
	}
	commandScope := databaseCommandScope(scope)
	auditID, err := t.commands.AppendAudit(ctx, t.tx, protocolV2CommandAudit{
		Scope: commandScope, ExtensionID: scope.ExtensionID,
		CommandID: commandScope.CommandID, CommandVersion: scope.StatementVersion,
		TransactionID: transactionID, IdempotencyKey: scope.IdempotencyKey,
		Impact: []*hostv2.ImpactItem{{
			Module: "extensions.database", Action: "execute", ResourceType: "plugin_schema",
			ResourceId: scope.ExtensionID, Summary: "Execute a registered own-schema database operation.",
		}},
	})
	if err != nil {
		return fmt.Errorf("append database audit: %w", err)
	}
	result := &hostv2.CommandResult{
		State: hostv2.CommandState_COMMAND_STATE_COMMITTED, TransactionId: transactionID,
		AuditEventId: auditID, CommittedRevision: strconv.FormatUint(receipt.AffectedRows, 10),
		Output: cloneProtocolV2Document(receipt.Result),
	}
	return t.commands.SaveResult(ctx, t.tx, commandScope, protocolV2CommandReceipt{
		Fingerprint: fingerprint, Result: result,
	})
}

func (t *postgresProtocolV2DatabaseTx) EnqueueQueryInvalidation(
	ctx context.Context,
	owner string,
	tags []string,
) error {
	if len(tags) == 0 {
		return nil
	}
	if t == nil || t.tx == nil || t.queryInvalidations == nil {
		return errors.New("hostapi: Query invalidation dispatcher is unavailable")
	}
	_, err := queryregistryjobs.EnqueueInvalidationTx(ctx, t.queryInvalidations, t.tx, owner, tags)
	return err
}

func (t *postgresProtocolV2DatabaseTx) Commit(ctx context.Context) error {
	if t == nil || t.tx == nil {
		return errors.New("hostapi: database transaction is unavailable")
	}
	return t.tx.Commit(ctx)
}

func (t *postgresProtocolV2DatabaseTx) Rollback(ctx context.Context) error {
	if t == nil || t.tx == nil {
		return nil
	}
	return t.tx.Rollback(ctx)
}

func (t *postgresProtocolV2DatabaseTx) enterDatabaseScope(ctx context.Context, scope protocolV2DatabaseScope) error {
	if scope.AuthorityType != "builtin" && scope.AuthorityType != "trust_grant" {
		return staleProtocolV2DatabaseIdentity()
	}
	if scope.OperationID == "" {
		return errors.New("hostapi: database operation identity is incomplete")
	}
	if scope.Scope != ProtocolV2DatabaseOwnSchema {
		return errors.New("hostapi: database statement scope is invalid")
	}
	if scope.SchemaName == "" || scope.RuntimeRoleName == "" {
		return errors.New("hostapi: database schema scope is incomplete")
	}
	// Own-schema statements execute as the least-privileged runtime role.
	if _, err := t.tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{scope.RuntimeRoleName}.Sanitize()); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{scope.SchemaName}.Sanitize()+", pg_catalog")
	return err
}

func (t *postgresProtocolV2DatabaseTx) leaveDatabaseScope(ctx context.Context, scope protocolV2DatabaseScope) error {
	if scope.AuthorityType == "" {
		return errors.New("hostapi: database scope is incomplete")
	}
	if _, err := t.tx.Exec(ctx, "RESET ROLE"); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, "SET LOCAL search_path TO pg_catalog, public")
	return err
}

func collectProtocolV2DatabaseRows(rows pgx.Rows, maximum int) ([]map[string]any, error) {
	defer rows.Close()
	fields := rows.FieldDescriptions()
	if len(fields) == 0 || len(fields) > protocolV2DatabaseMaximumColumns {
		return nil, errors.New("hostapi: database result has invalid columns")
	}
	names := make([]string, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for index, field := range fields {
		name := string(field.Name)
		if _, exists := seen[name]; exists {
			return nil, errors.New("hostapi: database result has duplicate columns")
		}
		seen[name] = struct{}{}
		names[index] = name
	}
	result := make([]map[string]any, 0, maximum)
	for rows.Next() {
		if len(result) >= maximum {
			return nil, errors.New("hostapi: database result exceeded its fetch bound")
		}
		values, err := rows.Values()
		if err != nil || len(values) != len(names) {
			return nil, errors.New("hostapi: database result could not be read")
		}
		row := make(map[string]any, len(names))
		for index, value := range values {
			row[names[index]] = value
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func databaseCommandScope(scope protocolV2DatabaseScope) protocolV2CommandScope {
	return protocolV2CommandScope{
		ExtensionID: scope.ExtensionID, ExtensionVersionID: scope.ExtensionVersionID,
		ExtensionVersion: scope.ExtensionVersion, PackageDigest: scope.PackageDigest,
		AuthorityType: scope.AuthorityType, TrustGrantID: scope.TrustGrantID,
		CommandID: databaseReceiptCommandID(scope.OperationID), CommandVersion: scope.StatementVersion,
		IdempotencyKey: scope.IdempotencyKey,
	}
}

func databaseReceiptCommandID(operationID string) string {
	return "sforum.database." + operationID
}

func staleProtocolV2DatabaseIdentity() error {
	return newProtocolV2DatabaseError(
		protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME,
		"host.database_runtime_stale",
		"The exact extension artifact or database grant is no longer active.",
		false,
	)
}

var _ protocolV2DatabaseBackend = (*postgresProtocolV2DatabaseBackend)(nil)
var _ protocolV2DatabaseTx = (*postgresProtocolV2DatabaseTx)(nil)
