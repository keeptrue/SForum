package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	extensionDatabaseRuntimeConnectionLimit        = 8
	extensionDatabaseRuntimeStatementTimeout       = "5s"
	extensionDatabaseRuntimeIdleTransactionTimeout = "15s"
)

func ensureExtensionDatabaseResources(
	ctx context.Context,
	tx pgx.Tx,
	extensionID string,
	identifiers ExtensionDatabaseIdentifiers,
) (string, error) {
	databaseName, err := currentExtensionDatabaseName(ctx, tx)
	if err != nil {
		return "", err
	}
	if err := ensureExtensionDatabaseRole(ctx, tx, identifiers.OwnerRole, false, ""); err != nil {
		return "", err
	}
	if err := ensureExtensionDatabaseRole(ctx, tx, identifiers.RuntimeRole, true, identifiers.OwnerRole); err != nil {
		return "", err
	}
	if err := ensureExtensionDatabaseSchema(ctx, tx, identifiers.Schema, identifiers.OwnerRole); err != nil {
		return "", err
	}
	if err := validateExtensionDatabaseRoleIsolation(
		ctx, tx, identifiers.OwnerRole, identifiers.Schema, "", databaseName, false,
	); err != nil {
		return "", err
	}
	if err := validateExtensionDatabaseRoleIsolation(
		ctx, tx, identifiers.RuntimeRole, identifiers.Schema,
		identifiers.OwnerRole, databaseName, true,
	); err != nil {
		return "", err
	}
	if err := configureExtensionDatabaseOwnership(ctx, tx, identifiers); err != nil {
		return "", err
	}
	if err := validateExtensionDatabaseRoleIsolation(
		ctx, tx, identifiers.RuntimeRole, identifiers.Schema,
		identifiers.OwnerRole, databaseName, true,
	); err != nil {
		return "", err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO extension_database_resources (
			extension_id, schema_name, owner_role_name, runtime_role_name
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (extension_id) DO NOTHING
	`, extensionID, identifiers.Schema, identifiers.OwnerRole, identifiers.RuntimeRole)
	if err != nil {
		return "", fmt.Errorf("insert extension database resources: %w", err)
	}
	resource, err := loadExtensionDatabaseResource(ctx, tx, extensionID, true)
	if err != nil {
		return "", err
	}
	if !resource.matches(identifiers) {
		return "", ErrExtensionDatabaseResourceConflict
	}
	return databaseName, nil
}

func currentExtensionDatabaseName(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
) (string, error) {
	var databaseName string
	if err := querier.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		return "", fmt.Errorf("read extension database name: %w", err)
	}
	if !validPostgresCatalogName(databaseName) {
		return "", ErrExtensionDatabaseResourceConflict
	}
	return databaseName, nil
}

func ensureExtensionDatabaseRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	allowLogin bool,
	allowedMembership string,
) error {
	if !validPostgresIdentifier(roleName) {
		return ErrExtensionDatabaseIdentifier
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	var canLogin, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole, rolreplication,
		       rolbypassrls, rolconnlimit
		FROM pg_roles WHERE rolname = $1
	`, roleName).Scan(
		&canLogin, &superuser, &createDatabase, &createRole, &replication,
		&bypassRLS, &connectionLimit,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		quoted := pgx.Identifier{roleName}.Sanitize()
		if _, err := tx.Exec(ctx, `CREATE ROLE `+quoted+` NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
			return fmt.Errorf("create extension database role %s: %w", roleName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect extension database role %s: %w", roleName, err)
	}
	if superuser || createDatabase || createRole || replication || bypassRLS || (!allowLogin && canLogin) {
		return fmt.Errorf("%w: role %s has elevated attributes", ErrExtensionDatabaseResourceConflict, roleName)
	}
	if allowLogin && connectionLimit != -1 && connectionLimit != extensionDatabaseRuntimeConnectionLimit {
		return fmt.Errorf(
			"%w: role %s has unexpected connection limit %d",
			ErrExtensionDatabaseResourceConflict, roleName, connectionLimit,
		)
	}
	rows, err := tx.Query(ctx, `
		SELECT granted.rolname
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS member ON member.oid = memberships.member
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		WHERE member.rolname = $1
		ORDER BY granted.rolname
	`, roleName)
	if err != nil {
		return fmt.Errorf("inspect extension database role memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var grantedRole string
		if err := rows.Scan(&grantedRole); err != nil {
			return err
		}
		if allowedMembership == "" || grantedRole != allowedMembership {
			return fmt.Errorf(
				"%w: role %s inherits unexpected role %s",
				ErrExtensionDatabaseResourceConflict, roleName, grantedRole,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func ensureExtensionDatabaseSchema(
	ctx context.Context,
	tx pgx.Tx,
	schemaName string,
	ownerRoleName string,
) error {
	if !validPostgresIdentifier(schemaName) || !validPostgresIdentifier(ownerRoleName) {
		return ErrExtensionDatabaseIdentifier
	}
	var existingOwner string
	err := tx.QueryRow(ctx, `
		SELECT roles.rolname
		FROM pg_namespace
		JOIN pg_roles AS roles ON roles.oid = pg_namespace.nspowner
		WHERE pg_namespace.nspname = $1
	`, schemaName).Scan(&existingOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		// PG 16 起 CREATE SCHEMA ... AUTHORIZATION 就要求调用者能 SET ROLE 到 owner
		// （17.12 起 REASSIGN OWNED 同规则，CVE-2026-120146）。宿主对扩展 owner 只有
		// CREATEROLE 建 role 留下的 ADMIN 行（inherit=false, set=false），因此必须在
		// 同一事务内临时取得接收者权限，建完立即归还。
		if err := acquireExtensionDatabaseObjectRecipientAuthority(ctx, tx, ownerRoleName); err != nil {
			return err
		}
		query := `CREATE SCHEMA ` + pgx.Identifier{schemaName}.Sanitize() +
			` AUTHORIZATION ` + pgx.Identifier{ownerRoleName}.Sanitize()
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("create extension database schema %s: %w", schemaName, err)
		}
		if err := releaseExtensionDatabaseObjectRecipientAuthority(ctx, tx, ownerRoleName); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect extension database schema %s: %w", schemaName, err)
	}
	if existingOwner != ownerRoleName {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func validateExtensionDatabaseRoleIsolation(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	schemaName string,
	allowedMembership string,
	databaseName string,
	allowExactSearchPath bool,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresIdentifier(schemaName) ||
		!validPostgresCatalogName(databaseName) {
		return ErrExtensionDatabaseIdentifier
	}
	rows, err := tx.Query(ctx, `
		SELECT settings.setdatabase, COALESCE(databases.datname, ''), setting
		FROM pg_db_role_setting AS settings
		LEFT JOIN pg_database AS databases ON databases.oid = settings.setdatabase
		CROSS JOIN LATERAL unnest(settings.setconfig) AS setting
		WHERE settings.setrole = (SELECT oid FROM pg_roles WHERE rolname = $1)
		ORDER BY settings.setdatabase, setting
	`, roleName)
	if err != nil {
		return fmt.Errorf("inspect extension database role settings: %w", err)
	}
	expectedSettings := extensionDatabaseRuntimeSettings(schemaName)
	seenSettings := make(map[string]struct{}, len(expectedSettings))
	for rows.Next() {
		var databaseOID uint32
		var configuredDatabase, setting string
		if err := rows.Scan(&databaseOID, &configuredDatabase, &setting); err != nil {
			rows.Close()
			return err
		}
		normalized := strings.ReplaceAll(setting, " ", "")
		_, expected := expectedSettings[normalized]
		_, duplicate := seenSettings[normalized]
		if !allowExactSearchPath || !expected || duplicate || databaseOID == 0 ||
			configuredDatabase != databaseName {
			rows.Close()
			return fmt.Errorf(
				"%w: role %s has unexpected setting %q for database %q",
				ErrExtensionDatabaseResourceConflict, roleName, setting, configuredDatabase,
			)
		}
		seenSettings[normalized] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var ownershipConflict bool
	err = tx.QueryRow(ctx, `
		WITH role_identity AS (
			SELECT oid FROM pg_roles WHERE rolname = $1
		)
		SELECT
		  EXISTS (SELECT 1 FROM pg_database, role_identity WHERE datdba = role_identity.oid)
		  OR EXISTS (
			SELECT 1 FROM pg_namespace, role_identity
			WHERE nspowner = role_identity.oid AND nspname <> $2
		  )
		  OR EXISTS (
			SELECT 1 FROM pg_class
			JOIN pg_namespace ON pg_namespace.oid = pg_class.relnamespace
			CROSS JOIN role_identity
			WHERE pg_class.relowner = role_identity.oid
			  AND pg_namespace.nspname <> $2
			  AND pg_namespace.nspname <> 'pg_toast'
			  AND pg_namespace.nspname NOT LIKE 'pg_temp_%'
			  AND pg_namespace.nspname NOT LIKE 'pg_toast_temp_%'
		  )
		  OR EXISTS (
			SELECT 1 FROM pg_proc
			JOIN pg_namespace ON pg_namespace.oid = pg_proc.pronamespace
			CROSS JOIN role_identity
			WHERE pg_proc.proowner = role_identity.oid AND pg_namespace.nspname <> $2
		  )
		  OR EXISTS (
			SELECT 1 FROM pg_type
			JOIN pg_namespace ON pg_namespace.oid = pg_type.typnamespace
			CROSS JOIN role_identity
			WHERE pg_type.typowner = role_identity.oid
			  AND pg_namespace.nspname <> $2
			  AND pg_namespace.nspname <> 'pg_toast'
			  AND pg_namespace.nspname NOT LIKE 'pg_temp_%'
			  AND pg_namespace.nspname NOT LIKE 'pg_toast_temp_%'
		  )
		  OR EXISTS (SELECT 1 FROM pg_extension, role_identity WHERE extowner = role_identity.oid)
		  OR EXISTS (SELECT 1 FROM pg_tablespace, role_identity WHERE spcowner = role_identity.oid)
	`, roleName, schemaName).Scan(&ownershipConflict)
	if err != nil {
		return fmt.Errorf("inspect extension database role ownership: %w", err)
	}
	if ownershipConflict {
		return fmt.Errorf(
			"%w: role %s owns objects outside schema %s",
			ErrExtensionDatabaseResourceConflict, roleName, schemaName,
		)
	}

	membershipRows, err := tx.Query(ctx, `
		SELECT granted.rolname
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS member ON member.oid = memberships.member
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		WHERE member.rolname = $1
	`, roleName)
	if err != nil {
		return fmt.Errorf("verify extension database role memberships: %w", err)
	}
	defer membershipRows.Close()
	for membershipRows.Next() {
		var granted string
		if err := membershipRows.Scan(&granted); err != nil {
			return err
		}
		if allowedMembership == "" || granted != allowedMembership {
			return fmt.Errorf(
				"%w: role %s inherits unexpected role %s",
				ErrExtensionDatabaseResourceConflict, roleName, granted,
			)
		}
	}
	return membershipRows.Err()
}

func configureExtensionDatabaseOwnership(
	ctx context.Context,
	tx pgx.Tx,
	identifiers ExtensionDatabaseIdentifiers,
) error {
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	owner := pgx.Identifier{identifiers.OwnerRole}.Sanitize()
	runtime := pgx.Identifier{identifiers.RuntimeRole}.Sanitize()
	schema := pgx.Identifier{identifiers.Schema}.Sanitize()
	queries := []string{
		`ALTER ROLE ` + owner + ` NOLOGIN`,
		`ALTER ROLE ` + runtime + ` INHERIT`,
		`GRANT ` + owner + ` TO ` + runtime,
		`REVOKE ALL ON SCHEMA ` + schema + ` FROM PUBLIC`,
		`GRANT USAGE, CREATE ON SCHEMA ` + schema + ` TO ` + owner,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + owner + ` IN SCHEMA ` + schema + ` REVOKE ALL ON TABLES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + owner + ` IN SCHEMA ` + schema + ` REVOKE ALL ON SEQUENCES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + owner + ` IN SCHEMA ` + schema + ` REVOKE ALL ON FUNCTIONS FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + owner + ` IN SCHEMA ` + schema + ` REVOKE ALL ON TYPES FROM PUBLIC`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("configure extension database ownership: %w", err)
		}
	}
	return nil
}

func activateExtensionDatabaseRuntimeRole(
	ctx context.Context,
	tx pgx.Tx,
	identifiers ExtensionDatabaseIdentifiers,
	databaseName string,
	password string,
) error {
	if !identifiers.Valid() || !validPostgresCatalogName(databaseName) ||
		!extensionDatabasePasswordPattern.MatchString(password) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	runtime := pgx.Identifier{identifiers.RuntimeRole}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	schema := pgx.Identifier{identifiers.Schema}.Sanitize()
	queries := []string{
		`ALTER ROLE ` + runtime + ` LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT ` + strconv.Itoa(extensionDatabaseRuntimeConnectionLimit) + ` PASSWORD '` + password + `'`,
		`GRANT CONNECT ON DATABASE ` + database + ` TO ` + runtime,
		`ALTER ROLE ` + runtime + ` IN DATABASE ` + database + ` SET search_path TO ` + schema + `, pg_catalog`,
		`ALTER ROLE ` + runtime + ` IN DATABASE ` + database + ` SET statement_timeout TO '` + extensionDatabaseRuntimeStatementTimeout + `'`,
		`ALTER ROLE ` + runtime + ` IN DATABASE ` + database + ` SET idle_in_transaction_session_timeout TO '` + extensionDatabaseRuntimeIdleTransactionTimeout + `'`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("%w: activate runtime role: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	if err := validateExtensionDatabaseRuntimeLimits(ctx, tx, identifiers, databaseName); err != nil {
		return fmt.Errorf("%w: validate runtime limits: %v", ErrExtensionDatabaseCredential, err)
	}
	return nil
}

func validateExtensionDatabaseRuntimeLimits(
	ctx context.Context,
	tx pgx.Tx,
	identifiers ExtensionDatabaseIdentifiers,
	databaseName string,
) error {
	if !identifiers.Valid() || !validPostgresCatalogName(databaseName) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	if err := tx.QueryRow(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit
		FROM pg_roles WHERE rolname = $1
	`, identifiers.RuntimeRole).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit,
	); err != nil {
		return err
	}
	if !canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != extensionDatabaseRuntimeConnectionLimit {
		return ErrExtensionDatabaseResourceConflict
	}

	expectedSettings := extensionDatabaseRuntimeSettings(identifiers.Schema)
	seenSettings := make(map[string]struct{}, len(expectedSettings))
	rows, err := tx.Query(ctx, `
		SELECT setting
		FROM pg_db_role_setting AS settings
		JOIN pg_database AS databases ON databases.oid = settings.setdatabase
		CROSS JOIN LATERAL unnest(settings.setconfig) AS setting
		WHERE settings.setrole = (SELECT oid FROM pg_roles WHERE rolname = $1)
		  AND databases.datname = $2
	`, identifiers.RuntimeRole, databaseName)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var setting string
		if err := rows.Scan(&setting); err != nil {
			return err
		}
		normalized := strings.ReplaceAll(setting, " ", "")
		if _, expected := expectedSettings[normalized]; !expected {
			return ErrExtensionDatabaseResourceConflict
		}
		seenSettings[normalized] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(seenSettings) != len(expectedSettings) {
		return ErrExtensionDatabaseResourceConflict
	}
	return nil
}

func extensionDatabaseRuntimeSettings(schemaName string) map[string]struct{} {
	return map[string]struct{}{
		"search_path=" + schemaName + ",pg_catalog":                                             {},
		"statement_timeout=" + extensionDatabaseRuntimeStatementTimeout:                         {},
		"idle_in_transaction_session_timeout=" + extensionDatabaseRuntimeIdleTransactionTimeout: {},
	}
}

func disableExtensionDatabaseRuntimeRole(
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
	role := pgx.Identifier{roleName}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	if _, err := tx.Exec(ctx, `ALTER ROLE `+role+` NOLOGIN PASSWORD NULL`); err != nil {
		return fmt.Errorf("%w: disable runtime role: %v", ErrExtensionDatabaseCredential, err)
	}
	if _, err := tx.Exec(ctx, `REVOKE CONNECT ON DATABASE `+database+` FROM `+role); err != nil {
		return fmt.Errorf("%w: revoke database connect: %v", ErrExtensionDatabaseCredential, err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE usename = $1 AND datname = $2 AND pid <> pg_backend_pid()
	`, roleName, databaseName); err != nil {
		return fmt.Errorf("%w: terminate revoked role sessions: %v", ErrExtensionDatabaseCredential, err)
	}
	return nil
}
