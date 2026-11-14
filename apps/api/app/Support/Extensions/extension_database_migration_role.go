package extensionsruntime

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const extensionDatabaseMigrationFailureCredential = "migration.scoped_credential_failed"

func prepareExtensionDatabaseMigrationRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	ownerRoleName string,
	schemaName string,
	databaseName string,
	password string,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresIdentifier(ownerRoleName) ||
		!validPostgresIdentifier(schemaName) || !validPostgresCatalogName(databaseName) ||
		!extensionDatabasePasswordPattern.MatchString(password) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	var superuser, createDatabase, createRole, replication, bypassRLS bool
	err := tx.QueryRow(ctx, `
		SELECT rolsuper, rolcreatedb, rolcreaterole, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname = $1
	`, roleName).Scan(&superuser, &createDatabase, &createRole, &replication, &bypassRLS)
	quotedRole := pgx.Identifier{roleName}.Sanitize()
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `CREATE ROLE `+quotedRole+` NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
			return fmt.Errorf("create extension migration role: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect extension migration role: %w", err)
	} else if superuser || createDatabase || createRole || replication || bypassRLS {
		return ErrExtensionDatabaseResourceConflict
	}
	// The deterministic name is not authority. A precreated role must already
	// be isolated to this exact owner/schema/database before Host mutates it.
	if err := validateExtensionDatabaseRoleIsolation(
		ctx, tx, roleName, schemaName, ownerRoleName, databaseName, true,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `ALTER ROLE `+quotedRole+` NOLOGIN PASSWORD NULL NOINHERIT`); err != nil {
		return fmt.Errorf("disable previous extension migration credential: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE usename = $1 AND datname = $2 AND pid <> pg_backend_pid()
	`, roleName, databaseName); err != nil {
		return fmt.Errorf("terminate previous extension migration sessions: %w", err)
	}
	owner := pgx.Identifier{ownerRoleName}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	schema := pgx.Identifier{schemaName}.Sanitize()
	queries := []string{
		`GRANT ` + owner + ` TO ` + quotedRole,
		`GRANT CONNECT ON DATABASE ` + database + ` TO ` + quotedRole,
		`ALTER ROLE ` + quotedRole + ` IN DATABASE ` + database + ` SET search_path TO ` + schema + `, pg_catalog`,
		`ALTER ROLE ` + quotedRole + ` LOGIN PASSWORD '` + password + `'`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("activate extension migration credential: %w", err)
		}
	}
	return validateExtensionDatabaseRoleIsolation(
		ctx, tx, roleName, schemaName, ownerRoleName, databaseName, true,
	)
}

func revokeExtensionDatabaseMigrationRole(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	ownerRoleName string,
	schemaName string,
	databaseName string,
) error {
	if !validPostgresIdentifier(roleName) || !validPostgresIdentifier(ownerRoleName) ||
		!validPostgresIdentifier(schemaName) || !validPostgresCatalogName(databaseName) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		return err
	}
	role := pgx.Identifier{roleName}.Sanitize()
	owner := pgx.Identifier{ownerRoleName}.Sanitize()
	database := pgx.Identifier{databaseName}.Sanitize()
	if _, err := tx.Exec(ctx, `ALTER ROLE `+role+` NOLOGIN PASSWORD NULL`); err != nil {
		return fmt.Errorf("disable extension migration role: %w", err)
	}
	if _, err := tx.Exec(ctx, `REVOKE CONNECT ON DATABASE `+database+` FROM `+role); err != nil {
		return fmt.Errorf("revoke extension migration connect: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE usename = $1 AND datname = $2 AND pid <> pg_backend_pid()
	`, roleName, databaseName); err != nil {
		return fmt.Errorf("terminate extension migration sessions: %w", err)
	}
	// REASSIGN OWNED 在 PG 17.12 起要求调用者能 SET ROLE 到接收者，
	// 先临时获得扩展 owner 的转移权限，回收结束后立即归还。
	if err := acquireExtensionDatabaseObjectRecipientAuthority(ctx, tx, ownerRoleName); err != nil {
		return err
	}
	for _, query := range []string{
		`REASSIGN OWNED BY ` + role + ` TO ` + owner,
		`REVOKE ` + owner + ` FROM ` + role,
		`DROP OWNED BY ` + role,
		`ALTER ROLE ` + role + ` IN DATABASE ` + database + ` RESET ALL`,
	} {
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("retire extension migration role: %w", err)
		}
	}
	if err := releaseExtensionDatabaseObjectRecipientAuthority(ctx, tx, ownerRoleName); err != nil {
		return err
	}
	return validateExtensionDatabaseRoleIsolation(
		ctx, tx, roleName, schemaName, "", databaseName, false,
	)
}
