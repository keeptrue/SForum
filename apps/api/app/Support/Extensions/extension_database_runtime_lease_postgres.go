package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

const extensionDatabaseCoreViewSchema = "sforum_core_v1"

func createExtensionDatabaseRuntimeLeaseRole(
	ctx context.Context,
	tx pgx.Tx,
	identifiers ExtensionDatabaseIdentifiers,
	roleName string,
	databaseName string,
	powers []string,
	password string,
	expiresAt time.Time,
) (string, error) {
	if !identifiers.Valid() || !validPostgresIdentifier(roleName) ||
		!validPostgresCatalogName(databaseName) || !extensionDatabasePasswordPattern.MatchString(password) ||
		expiresAt.IsZero() || len(powers) == 0 {
		return "", ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return "", err
	}
	var roleExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, roleName).Scan(&roleExists); err != nil {
		return "", fmt.Errorf("inspect extension database runtime lease role: %w", err)
	}
	if roleExists {
		return "", ErrExtensionDatabaseRuntimeLeaseConflict
	}
	wantKernel := containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel)
	identity, kernelCoreOwners, err := validateExtensionDatabaseKernelCoreState(ctx, tx)
	if err != nil {
		return "", fmt.Errorf("validate Core state before runtime lease issue: %w", err)
	}
	role := pgx.Identifier{roleName}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	owner := pgx.Identifier{identifiers.OwnerRole}.Sanitize()
	if _, err := tx.Exec(ctx, `CREATE ROLE `+role+` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
		return "", fmt.Errorf("%w: create runtime lease role: %v", ErrExtensionDatabaseCredential, err)
	}

	searchPath := extensionDatabaseRuntimeLeaseSearchPath(identifiers.Schema, powers)
	queries := []string{`GRANT CONNECT ON DATABASE ` + database + ` TO ` + role}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantOwnSchema) {
		schema := pgx.Identifier{identifiers.Schema}.Sanitize()
		queries = append(queries,
			`GRANT `+owner+` TO `+role+` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
			`ALTER DEFAULT PRIVILEGES FOR ROLE `+role+` IN SCHEMA `+schema+` GRANT ALL ON TABLES TO `+owner,
			`ALTER DEFAULT PRIVILEGES FOR ROLE `+role+` IN SCHEMA `+schema+` GRANT ALL ON SEQUENCES TO `+owner,
			`ALTER DEFAULT PRIVILEGES FOR ROLE `+role+` IN SCHEMA `+schema+` GRANT ALL ON FUNCTIONS TO `+owner,
			`ALTER DEFAULT PRIVILEGES FOR ROLE `+role+` IN SCHEMA `+schema+` GRANT USAGE ON TYPES TO `+owner,
		)
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantCoreViews) && !wantKernel {
		coreViews := pgx.Identifier{extensionDatabaseCoreViewSchema}.Sanitize()
		queries = append(queries,
			`GRANT USAGE ON SCHEMA `+coreViews+` TO `+role,
			`GRANT SELECT ON ALL TABLES IN SCHEMA `+coreViews+` TO `+role,
		)
	}
	if wantKernel {
		coreOwner := pgx.Identifier{identity.ownerRole}.Sanitize()
		queries = append(queries,
			`GRANT `+coreOwner+` TO `+role+` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
			`ALTER DEFAULT PRIVILEGES FOR ROLE `+role+` REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`,
		)
	}
	queries = append(queries,
		`ALTER ROLE `+role+` LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT `+
			strconv.Itoa(extensionDatabaseRuntimeLeaseConnectionLimit)+` VALID UNTIL '`+expiresAt.UTC().Format(time.RFC3339Nano)+`' PASSWORD '`+password+`'`,
		`ALTER ROLE `+role+` IN DATABASE `+database+` SET search_path TO `+searchPath,
		`ALTER ROLE `+role+` IN DATABASE `+database+` SET statement_timeout TO '`+extensionDatabaseRuntimeStatementTimeout+`'`,
		`ALTER ROLE `+role+` IN DATABASE `+database+` SET idle_in_transaction_session_timeout TO '`+extensionDatabaseRuntimeIdleTransactionTimeout+`'`,
	)
	if err := acquireExtensionDatabaseKernelOwnerAuthority(ctx, tx, kernelCoreOwners); err != nil {
		return "", err
	}
	configurationErr := func() error {
		for _, query := range queries {
			if _, err := tx.Exec(ctx, query); err != nil {
				return fmt.Errorf("%w: configure runtime lease role: %v", ErrExtensionDatabaseCredential, err)
			}
		}
		if err := hardenExtensionDatabaseCoreRoutineAuthority(ctx, tx, kernelCoreOwners); err != nil {
			return err
		}
		if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore) && !wantKernel {
			return grantExtensionDatabaseRawCoreAuthority(ctx, tx, roleName, kernelCoreOwners)
		}
		return nil
	}()
	if configurationErr != nil {
		return "", configurationErr
	}
	if err := releaseExtensionDatabaseKernelOwnerAuthority(ctx, tx, kernelCoreOwners); err != nil {
		return "", err
	}
	if err := validateExtensionDatabaseRuntimeLeaseRole(
		ctx, tx, identifiers, roleName, databaseName, powers, searchPath, expiresAt,
	); err != nil {
		return "", err
	}
	return strings.ReplaceAll(searchPath, `"`, ""), nil
}

func validateExtensionDatabaseRuntimeLeaseRole(
	ctx context.Context,
	tx pgx.Tx,
	identifiers ExtensionDatabaseIdentifiers,
	roleName string,
	databaseName string,
	powers []string,
	searchPath string,
	expiresAt time.Time,
) error {
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil
		FROM pg_roles WHERE rolname = $1
	`, roleName).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	); err != nil {
		return fmt.Errorf("inspect runtime lease role: %w", err)
	}
	if !canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != extensionDatabaseRuntimeLeaseConnectionLimit || validUntil == nil ||
		!validUntil.UTC().Equal(expiresAt.UTC()) {
		return ErrExtensionDatabaseResourceConflict
	}

	extraKernelRoles := []string(nil)
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		extraKernelRoles = append(extraKernelRoles, roleName)
	}
	identity, kernelCoreOwners, err := validateExtensionDatabaseKernelCoreState(ctx, tx, extraKernelRoles...)
	if err != nil {
		return fmt.Errorf("validate Core state after runtime lease issue: %w", err)
	}
	expectedMemberships := make(map[string]extensionDatabaseMembershipOptions, 2)
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantOwnSchema) {
		expectedMemberships[identifiers.OwnerRole] = extensionDatabaseMembershipOptions{
			inherit: true,
			set:     true,
		}
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		expectedMemberships[identity.ownerRole] = extensionDatabaseMembershipOptions{inherit: true}
	}
	rows, err := tx.Query(ctx, `
		SELECT granted.rolname, memberships.admin_option,
		       memberships.inherit_option, memberships.set_option
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS member ON member.oid = memberships.member
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		WHERE member.rolname = $1
	`, roleName)
	if err != nil {
		return fmt.Errorf("inspect runtime lease memberships: %w", err)
	}
	defer rows.Close()
	memberships := 0
	for rows.Next() {
		var granted string
		var actual extensionDatabaseMembershipOptions
		if err := rows.Scan(&granted, &actual.admin, &actual.inherit, &actual.set); err != nil {
			return err
		}
		memberships++
		expected, ok := expectedMemberships[granted]
		if !ok || actual != expected {
			return ErrExtensionDatabaseResourceConflict
		}
	}
	if err := rows.Err(); err != nil || memberships != len(expectedMemberships) {
		if err != nil {
			return err
		}
		return ErrExtensionDatabaseResourceConflict
	}

	expectedSettings := map[string]struct{}{
		normalizeExtensionDatabaseLeaseSetting("search_path=" + searchPath):                     {},
		"statement_timeout=" + extensionDatabaseRuntimeStatementTimeout:                         {},
		"idle_in_transaction_session_timeout=" + extensionDatabaseRuntimeIdleTransactionTimeout: {},
	}
	settings, err := tx.Query(ctx, `
		SELECT setting
		FROM pg_db_role_setting AS role_settings
		JOIN pg_database AS databases ON databases.oid = role_settings.setdatabase
		CROSS JOIN LATERAL unnest(role_settings.setconfig) AS setting
		WHERE role_settings.setrole = (SELECT oid FROM pg_roles WHERE rolname = $1)
		  AND databases.datname = $2
	`, roleName, databaseName)
	if err != nil {
		return fmt.Errorf("inspect runtime lease settings: %w", err)
	}
	defer settings.Close()
	seen := map[string]struct{}{}
	for settings.Next() {
		var setting string
		if err := settings.Scan(&setting); err != nil {
			return err
		}
		normalized := normalizeExtensionDatabaseLeaseSetting(setting)
		if _, ok := expectedSettings[normalized]; !ok {
			return ErrExtensionDatabaseResourceConflict
		}
		seen[normalized] = struct{}{}
	}
	if err := settings.Err(); err != nil {
		return err
	}
	if len(seen) != len(expectedSettings) {
		return ErrExtensionDatabaseResourceConflict
	}
	if err := validateExtensionDatabaseRawCoreAuthority(ctx, tx, roleName, powers, kernelCoreOwners); err != nil {
		return fmt.Errorf("validate runtime lease Core ACL authority: %w", err)
	}
	// 持久 owner 不得携带 Core 权限，否则普通 own_schema 租约会继承已撤销运行时的权力。
	if err := validateExtensionDatabaseRawCoreAuthority(ctx, tx, identifiers.OwnerRole, nil, kernelCoreOwners); err != nil {
		return fmt.Errorf("validate persistent extension owner Core isolation: %w", err)
	}
	return nil
}

func normalizeExtensionDatabaseLeaseSetting(value string) string {
	return strings.NewReplacer(" ", "", `"`, "").Replace(value)
}

func disableExtensionDatabaseRuntimeLeaseRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	databaseName string,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresCatalogName(databaseName) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, roleName).Scan(&exists); err != nil {
		return fmt.Errorf("inspect revoked runtime lease role: %w", err)
	}
	if !exists {
		return nil
	}
	role := pgx.Identifier{roleName}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	for _, query := range []string{
		`ALTER ROLE ` + role + ` NOLOGIN PASSWORD NULL VALID UNTIL 'epoch'`,
		`REVOKE CONNECT ON DATABASE ` + database + ` FROM ` + role,
	} {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("%w: disable runtime lease role: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE usename = $1 AND datname = $2 AND pid <> pg_backend_pid()
	`, roleName, databaseName); err != nil {
		return fmt.Errorf("%w: terminate runtime lease sessions: %v", ErrExtensionDatabaseCredential, err)
	}
	return nil
}

func cleanupExtensionDatabaseRuntimeLeaseRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	ownerRoleName string,
	schemaName string,
	databaseName string,
	powers []string,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresIdentifier(ownerRoleName) ||
		!validPostgresIdentifier(schemaName) || !validPostgresCatalogName(databaseName) || len(powers) == 0 {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, roleName).Scan(&exists); err != nil {
		return fmt.Errorf("inspect runtime lease cleanup role: %w", err)
	}
	if !exists {
		return nil
	}
	role := pgx.Identifier{roleName}.Sanitize()
	owner := pgx.Identifier{ownerRoleName}.Sanitize()
	wantKernel := containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel)
	retirementOwners := map[string]struct{}{roleName: {}}
	if wantKernel {
		_, kernelOwners, err := validateExtensionDatabaseKernelCoreState(ctx, tx)
		if err != nil {
			return err
		}
		retirementOwners = kernelOwners
	}
	if err := acquireExtensionDatabaseKernelOwnerAuthority(ctx, tx, retirementOwners); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE usename = $1 AND datname = $2 AND pid <> pg_backend_pid()
	`, roleName, databaseName); err != nil {
		return fmt.Errorf("%w: terminate fenced runtime lease sessions: %v", ErrExtensionDatabaseCredential, err)
	}
	if wantKernel {
		if err := hardenExtensionDatabaseCoreRoutineAuthority(ctx, tx, retirementOwners); err != nil {
			return err
		}
		if err := reassignExtensionDatabaseKernelCoreObjects(ctx, tx, roleName); err != nil {
			return err
		}
		if err := validateExtensionDatabaseKernelRemainingOwnership(
			ctx, tx, roleName, schemaName,
			containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantOwnSchema),
		); err != nil {
			return err
		}
	}
	retirementQueries := make([]string, 0, 3)
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantOwnSchema) {
		retirementQueries = append(retirementQueries, `REASSIGN OWNED BY `+role+` TO `+owner)
	}
	retirementQueries = append(retirementQueries, `DROP OWNED BY `+role, `DROP ROLE `+role)
	for _, query := range retirementQueries {
		if err := executeExtensionDatabaseRuntimeLeaseRetirement(ctx, tx, query); err != nil {
			return fmt.Errorf("%w: retire runtime lease role: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	delete(retirementOwners, roleName)
	if err := releaseExtensionDatabaseKernelOwnerAuthority(ctx, tx, retirementOwners); err != nil {
		return err
	}
	return nil
}

func isMissingExtensionDatabaseRuntimeLeaseRole(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42704"
}

// PostgreSQL 的语句错误会中止整个事务。用 savepoint 仅吞掉“目标 role 已
// 不存在”，才能让重启回收继续落库；权限、依赖和其它对象错误仍保持 fail-closed。
func executeExtensionDatabaseRuntimeLeaseRetirement(ctx context.Context, tx pgx.Tx, query string) error {
	const savepoint = "extension_database_runtime_lease_retirement"
	if _, err := tx.Exec(ctx, `SAVEPOINT `+savepoint); err != nil {
		return fmt.Errorf("open runtime lease retirement savepoint: %w", err)
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		if !isMissingExtensionDatabaseRuntimeLeaseRole(err) {
			return err
		}
		if _, rollbackErr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT `+savepoint); rollbackErr != nil {
			return fmt.Errorf("rollback missing runtime lease role retirement: %w", rollbackErr)
		}
	}
	if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT `+savepoint); err != nil {
		return fmt.Errorf("release runtime lease retirement savepoint: %w", err)
	}
	return nil
}

type extensionDatabaseMembershipOptions struct {
	admin   bool
	inherit bool
	set     bool
}

func extendExtensionDatabaseRuntimeLeaseRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	expiresAt time.Time,
) error {
	if !validPostgresIdentifier(roleName) || expiresAt.IsZero() {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	role := pgx.Identifier{roleName}.Sanitize()
	if _, err := tx.Exec(ctx, `ALTER ROLE `+role+` VALID UNTIL '`+expiresAt.UTC().Format(time.RFC3339Nano)+`'`); err != nil {
		return fmt.Errorf("%w: extend runtime lease role: %v", ErrExtensionDatabaseCredential, err)
	}
	var validUntil *time.Time
	if err := tx.QueryRow(ctx, `SELECT rolvaliduntil FROM pg_roles WHERE rolname = $1`, roleName).Scan(&validUntil); err != nil {
		return fmt.Errorf("inspect extended runtime lease role: %w", err)
	}
	if validUntil == nil || !validUntil.UTC().Equal(expiresAt.UTC()) {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func extensionDatabaseRuntimeLeaseSearchPath(schemaName string, powers []string) string {
	parts := make([]string, 0, 4)
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantOwnSchema) {
		parts = append(parts, pgx.Identifier{schemaName}.Sanitize())
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantCoreViews) {
		parts = append(parts, pgx.Identifier{extensionDatabaseCoreViewSchema}.Sanitize())
	}
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore) ||
		containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		parts = append(parts, pgx.Identifier{"public"}.Sanitize())
	}
	parts = append(parts, "pg_catalog")
	return strings.Join(parts, ", ")
}

func containsExtensionDatabasePower(powers []string, expected string) bool {
	for _, power := range powers {
		if power == expected {
			return true
		}
	}
	return false
}
