package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

var ErrCoreAuthorityConflict = errors.New("core database authority conflicts with the Host ownership boundary")

type coreMigrationAuthority struct {
	DatabaseName string
	OwnerRole    string
	SessionRole  string
}

type coreOwnedRelation struct {
	schema string
	name   string
	kind   string
}

type coreOwnedRoutine struct {
	schema    string
	name      string
	arguments string
	kind      string
}

type coreOwnedType struct {
	schema string
	name   string
}

type coreAuthorityTransactionBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func prepareCoreMigrationAuthority(ctx context.Context, db coreAuthorityTransactionBeginner) (coreMigrationAuthority, error) {
	return prepareCoreMigrationAuthorityForVersion(ctx, db, "")
}

func prepareCoreMigrationAuthorityForVersion(
	ctx context.Context,
	db coreAuthorityTransactionBeginner,
	targetVersion string,
) (coreMigrationAuthority, error) {
	if ctx == nil || db == nil {
		return coreMigrationAuthority{}, fmt.Errorf("%w: database connection is required", ErrCoreAuthorityConflict)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return coreMigrationAuthority{}, fmt.Errorf("begin Core ownership convergence: %w", err)
	}
	defer tx.Rollback()

	var authority coreMigrationAuthority
	var currentRole string
	if err := tx.QueryRowContext(ctx, `SELECT current_database(), session_user, current_user`).Scan(
		&authority.DatabaseName, &authority.SessionRole, &currentRole,
	); err != nil {
		return coreMigrationAuthority{}, fmt.Errorf("inspect Core migration identity: %w", err)
	}
	if currentRole != authority.SessionRole {
		return coreMigrationAuthority{}, fmt.Errorf(
			"%w: migration DATABASE_URL must authenticate without a preset role (session=%s current=%s)",
			ErrCoreAuthorityConflict, authority.SessionRole, currentRole,
		)
	}
	authority.OwnerRole, err = coreauthority.OwnerRoleName(authority.DatabaseName)
	if err != nil {
		return coreMigrationAuthority{}, fmt.Errorf("derive Core owner role: %w", err)
	}
	if err := lockCorePhysicalAuthority(ctx, tx); err != nil {
		return coreMigrationAuthority{}, err
	}
	if err := ensureCoreOwnerRole(ctx, tx, authority); err != nil {
		return coreMigrationAuthority{}, err
	}
	compatibilityErr, err := reconcileCoreKernelAuthority(ctx, tx, authority, targetVersion)
	if err != nil {
		return coreMigrationAuthority{}, err
	}
	if err := validateCoreOwnershipSources(ctx, tx, authority); err != nil {
		return coreMigrationAuthority{}, err
	}
	if err := transferCoreObjectOwnership(ctx, tx, authority); err != nil {
		return coreMigrationAuthority{}, err
	}
	if err := validateCoreOwnership(ctx, tx, authority); err != nil {
		return coreMigrationAuthority{}, err
	}
	if err := tx.Commit(); err != nil {
		return coreMigrationAuthority{}, fmt.Errorf("commit Core ownership convergence: %w", err)
	}
	if compatibilityErr != nil {
		return coreMigrationAuthority{}, compatibilityErr
	}
	return authority, nil
}

func ensureCoreOwnerRole(ctx context.Context, tx *sql.Tx, authority coreMigrationAuthority) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, authority.OwnerRole).Scan(&exists); err != nil {
		return fmt.Errorf("inspect Core owner role: %w", err)
	}
	created := !exists
	if created {
		role := pgx.Identifier{authority.OwnerRole}.Sanitize()
		if _, err := tx.ExecContext(ctx, `CREATE ROLE `+role+` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
			return fmt.Errorf("create database-specific Core owner role %s: %w; the migration login needs CREATEROLE", authority.OwnerRole, err)
		}
	}
	if err := validateCoreOwnerRole(ctx, tx, authority); err != nil {
		return err
	}
	if err := configureCoreMigrationDatabaseCreate(ctx, tx, authority, false); err != nil {
		return err
	}
	if err := validateCoreOwnerIsolation(ctx, tx, authority); err != nil {
		return err
	}

	var adminOption, inheritOption, setOption bool
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(bool_or(memberships.admin_option), FALSE),
		       COALESCE(bool_or(memberships.inherit_option), FALSE),
		       COALESCE(bool_or(memberships.set_option), FALSE)
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		WHERE granted.rolname = $1 AND member.rolname = $2
	`, authority.OwnerRole, authority.SessionRole).Scan(&adminOption, &inheritOption, &setOption); err != nil {
		return fmt.Errorf("inspect Core owner membership: %w", err)
	}
	if created && (!adminOption || !inheritOption || !setOption) {
		owner := pgx.Identifier{authority.OwnerRole}.Sanitize()
		session := pgx.Identifier{authority.SessionRole}.Sanitize()
		var sessionSuperuser bool
		if err := tx.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = $1`, authority.SessionRole).Scan(&sessionSuperuser); err != nil {
			return fmt.Errorf("inspect Core migration login attributes: %w", err)
		}
		// PostgreSQL 17 gives a non-super CREATEROLE creator a bootstrap-granted
		// ADMIN-only row. The creator cannot grant ADMIN back to itself, so a
		// second grantor-specific row supplies only the missing effective options.
		adminGrant := "FALSE"
		if sessionSuperuser {
			adminGrant = "TRUE"
		} else if !adminOption {
			return fmt.Errorf(
				"%w: migration login %s lacks creator ADMIN membership in %s",
				ErrCoreAuthorityConflict, authority.SessionRole, authority.OwnerRole,
			)
		}
		if _, err := tx.ExecContext(ctx, `GRANT `+owner+` TO `+session+` WITH ADMIN `+adminGrant+`, INHERIT TRUE, SET TRUE`); err != nil {
			return fmt.Errorf("grant Core owner role to migration login %s: %w", authority.SessionRole, err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(bool_or(memberships.admin_option), FALSE),
			       COALESCE(bool_or(memberships.inherit_option), FALSE),
			       COALESCE(bool_or(memberships.set_option), FALSE)
			FROM pg_auth_members AS memberships
			JOIN pg_roles AS granted ON granted.oid = memberships.roleid
			JOIN pg_roles AS member ON member.oid = memberships.member
			WHERE granted.rolname = $1 AND member.rolname = $2
		`, authority.OwnerRole, authority.SessionRole).Scan(&adminOption, &inheritOption, &setOption); err != nil {
			return fmt.Errorf("validate converged Core owner membership: %w", err)
		}
	}
	if !adminOption || !inheritOption || !setOption {
		return fmt.Errorf(
			"%w: migration login %s must have direct ADMIN, INHERIT, and SET membership in %s",
			ErrCoreAuthorityConflict, authority.SessionRole, authority.OwnerRole,
		)
	}
	var canSet bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_has_role($1, $2, 'SET')`, authority.SessionRole, authority.OwnerRole).Scan(&canSet); err != nil {
		return fmt.Errorf("validate Core owner SET membership: %w", err)
	}
	if !canSet {
		return fmt.Errorf(
			"%w: migration login %s cannot SET ROLE %s; restore its direct SET membership",
			ErrCoreAuthorityConflict, authority.SessionRole, authority.OwnerRole,
		)
	}
	return nil
}

func validateCoreOwnerRole(ctx context.Context, tx *sql.Tx, authority coreMigrationAuthority) error {
	var canLogin, inherit, superuser, createDatabase, createRole, replication, bypassRLS bool
	var connectionLimit int
	var validUntil sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		SELECT rolcanlogin, rolinherit, rolsuper, rolcreatedb, rolcreaterole,
		       rolreplication, rolbypassrls, rolconnlimit, rolvaliduntil
		FROM pg_roles WHERE rolname = $1
	`, authority.OwnerRole).Scan(
		&canLogin, &inherit, &superuser, &createDatabase, &createRole,
		&replication, &bypassRLS, &connectionLimit, &validUntil,
	); err != nil {
		return fmt.Errorf("read Core owner role attributes: %w", err)
	}
	if canLogin || !inherit || superuser || createDatabase || createRole || replication || bypassRLS ||
		connectionLimit != -1 || validUntil.Valid {
		return fmt.Errorf("%w: Core owner role %s has unsafe attributes", ErrCoreAuthorityConflict, authority.OwnerRole)
	}

	var memberships, settings int
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_auth_members WHERE member = (SELECT oid FROM pg_roles WHERE rolname = $1)),
		  (SELECT count(*) FROM pg_db_role_setting WHERE setrole = (SELECT oid FROM pg_roles WHERE rolname = $1))
	`, authority.OwnerRole).Scan(&memberships, &settings); err != nil {
		return fmt.Errorf("inspect Core owner memberships and settings: %w", err)
	}
	if memberships != 0 || settings != 0 {
		return fmt.Errorf(
			"%w: Core owner role %s must not inherit roles or carry role/database settings",
			ErrCoreAuthorityConflict, authority.OwnerRole,
		)
	}
	return nil
}

func validateCoreOwnerIsolation(ctx context.Context, tx *sql.Tx, authority coreMigrationAuthority) error {
	var conflict bool
	if err := tx.QueryRowContext(ctx, `
		WITH owner AS (SELECT oid FROM pg_roles WHERE rolname = $1)
		SELECT
		  has_database_privilege($1, current_database(), 'CREATE')
		  OR EXISTS (SELECT 1 FROM pg_database, owner WHERE datdba = owner.oid)
		  OR EXISTS (SELECT 1 FROM pg_tablespace, owner WHERE spcowner = owner.oid)
		  OR EXISTS (SELECT 1 FROM pg_extension, owner WHERE extowner = owner.oid)
		  OR EXISTS (
			SELECT 1 FROM pg_namespace, owner
			WHERE nspowner = owner.oid AND nspname NOT IN ($2, $3)
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			CROSS JOIN owner
			WHERE classes.relowner = owner.oid
			  AND (
			    namespaces.nspname NOT IN ($2, $3, 'pg_toast')
			    OR (namespaces.nspname = $2 AND classes.relname LIKE 'river\_%' ESCAPE '\')
			  )
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_proc AS routines
			JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
			CROSS JOIN owner
			WHERE routines.proowner = owner.oid
			  AND (
			    namespaces.nspname NOT IN ($2, $3)
			    OR (namespaces.nspname = $2 AND routines.proname LIKE 'river\_%' ESCAPE '\')
			  )
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_type AS types
			JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
			CROSS JOIN owner
			WHERE types.typowner = owner.oid
			  AND (
			    namespaces.nspname NOT IN ($2, $3, 'pg_toast')
			    OR (namespaces.nspname = $2 AND types.typname ~ '^_?river_')
			  )
		  )
	`, authority.OwnerRole, coreauthority.PublicSchema, coreauthority.StableCoreViewSchema).Scan(&conflict); err != nil {
		return fmt.Errorf("inspect Core owner isolation: %w", err)
	}
	if conflict {
		return fmt.Errorf(
			"%w: Core owner role %s owns database, extension, River, plugin-schema, or non-Core objects",
			ErrCoreAuthorityConflict, authority.OwnerRole,
		)
	}
	return nil
}

type coreAuthoritySQLExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func configureCoreMigrationDatabaseCreate(
	ctx context.Context,
	executor coreAuthoritySQLExecutor,
	authority coreMigrationAuthority,
	allowed bool,
) error {
	if executor == nil {
		return fmt.Errorf("%w: Core migration privilege executor is required", ErrCoreAuthorityConflict)
	}
	command := "REVOKE CREATE ON DATABASE " + pgx.Identifier{authority.DatabaseName}.Sanitize() +
		" FROM " + pgx.Identifier{authority.OwnerRole}.Sanitize()
	operation := "revoke"
	if allowed {
		command = "GRANT CREATE ON DATABASE " + pgx.Identifier{authority.DatabaseName}.Sanitize() +
			" TO " + pgx.Identifier{authority.OwnerRole}.Sanitize()
		operation = "grant"
	}
	if _, err := executor.ExecContext(ctx, command); err != nil {
		return fmt.Errorf("%s temporary Core migration database CREATE privilege: %w", operation, err)
	}
	return nil
}

func validateCoreOwnershipSources(ctx context.Context, tx *sql.Tx, authority coreMigrationAuthority) error {
	type conflict struct {
		kind   string
		object string
		owner  string
	}
	queries := []struct {
		kind  string
		query string
	}{
		{"schema", `
			SELECT namespaces.nspname, owners.rolname
			FROM pg_namespace AS namespaces
			JOIN pg_roles AS owners ON owners.oid = namespaces.nspowner
			WHERE namespaces.nspname IN ($1, $2)
			  AND owners.rolname NOT IN ($3, $4)
			  AND NOT (namespaces.nspname = $1 AND owners.rolname = 'pg_database_owner')
			LIMIT 1
		`},
		{"relation", `
			SELECT namespaces.nspname || '.' || classes.relname, owners.rolname
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			JOIN pg_roles AS owners ON owners.oid = classes.relowner
			WHERE namespaces.nspname IN ($1, $2)
			  AND NOT (namespaces.nspname = $1 AND classes.relname LIKE 'river\_%' ESCAPE '\')
			  AND owners.rolname NOT IN ($3, $4)
			LIMIT 1
		`},
		{"routine", `
			SELECT namespaces.nspname || '.' || routines.proname, owners.rolname
			FROM pg_proc AS routines
			JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
			JOIN pg_roles AS owners ON owners.oid = routines.proowner
			WHERE namespaces.nspname IN ($1, $2)
			  AND NOT (namespaces.nspname = $1 AND routines.proname LIKE 'river\_%' ESCAPE '\')
			  AND owners.rolname NOT IN ($3, $4)
			LIMIT 1
		`},
		{"type", `
			SELECT namespaces.nspname || '.' || types.typname, owners.rolname
			FROM pg_type AS types
			JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
			JOIN pg_roles AS owners ON owners.oid = types.typowner
			WHERE namespaces.nspname IN ($1, $2)
			  AND NOT (namespaces.nspname = $1 AND types.typname ~ '^_?river_')
			  AND owners.rolname NOT IN ($3, $4)
			LIMIT 1
		`},
	}
	for _, candidate := range queries {
		var found conflict
		found.kind = candidate.kind
		err := tx.QueryRowContext(
			ctx, candidate.query,
			coreauthority.PublicSchema, coreauthority.StableCoreViewSchema,
			authority.SessionRole, authority.OwnerRole,
		).Scan(&found.object, &found.owner)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect existing Core %s ownership: %w", candidate.kind, err)
		}
		return fmt.Errorf(
			"%w: existing Core %s %s is owned by unexpected role %s; reassign it to %s before migrating",
			ErrCoreAuthorityConflict, found.kind, found.object, found.owner, authority.SessionRole,
		)
	}
	return nil
}

func transferCoreObjectOwnership(ctx context.Context, tx *sql.Tx, authority coreMigrationAuthority) error {
	owner := pgx.Identifier{authority.OwnerRole}.Sanitize()
	for _, schemaName := range []string{coreauthority.PublicSchema, coreauthority.StableCoreViewSchema} {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schemaName).Scan(&exists); err != nil {
			return err
		}
		if exists {
			schema := pgx.Identifier{schemaName}.Sanitize()
			if _, err := tx.ExecContext(ctx, `ALTER SCHEMA `+schema+` OWNER TO `+owner); err != nil {
				return fmt.Errorf("transfer Core schema %s ownership: %w", schemaName, err)
			}
		}
	}

	relations, err := loadCoreOwnedRelations(ctx, tx, authority.OwnerRole)
	if err != nil {
		return err
	}
	for _, relation := range relations {
		object := pgx.Identifier{relation.schema, relation.name}.Sanitize()
		command := "TABLE"
		switch relation.kind {
		case "S":
			command = "SEQUENCE"
		case "v":
			command = "VIEW"
		case "m":
			command = "MATERIALIZED VIEW"
		case "f":
			command = "FOREIGN TABLE"
		case "c":
			command = "TYPE"
		}
		if _, err := tx.ExecContext(ctx, `ALTER `+command+` `+object+` OWNER TO `+owner); err != nil {
			return fmt.Errorf("transfer Core relation %s.%s ownership: %w", relation.schema, relation.name, err)
		}
	}

	routines, err := loadCoreOwnedRoutines(ctx, tx, authority.OwnerRole)
	if err != nil {
		return err
	}
	for _, routine := range routines {
		object := pgx.Identifier{routine.schema, routine.name}.Sanitize()
		command := "FUNCTION"
		if routine.kind == "p" {
			command = "PROCEDURE"
		} else if routine.kind == "a" {
			command = "AGGREGATE"
		}
		if _, err := tx.ExecContext(ctx, `ALTER `+command+` `+object+`(`+routine.arguments+`) OWNER TO `+owner); err != nil {
			return fmt.Errorf("transfer Core routine %s.%s ownership: %w", routine.schema, routine.name, err)
		}
	}

	types, err := loadCoreOwnedTypes(ctx, tx, authority.OwnerRole)
	if err != nil {
		return err
	}
	for _, coreType := range types {
		object := pgx.Identifier{coreType.schema, coreType.name}.Sanitize()
		if _, err := tx.ExecContext(ctx, `ALTER TYPE `+object+` OWNER TO `+owner); err != nil {
			return fmt.Errorf("transfer Core type %s.%s ownership: %w", coreType.schema, coreType.name, err)
		}
	}
	return nil
}

func loadCoreOwnedRelations(ctx context.Context, tx *sql.Tx, ownerRole string) ([]coreOwnedRelation, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT namespaces.nspname, classes.relname, classes.relkind::TEXT
		FROM pg_class AS classes
		JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
		WHERE namespaces.nspname IN ($1, $2)
		  AND classes.relkind IN ('r', 'p', 'S', 'v', 'm', 'f', 'c')
		  AND NOT (namespaces.nspname = $1 AND classes.relname LIKE 'river\_%' ESCAPE '\')
		  AND NOT (
		    classes.relkind = 'S' AND EXISTS (
		      SELECT 1 FROM pg_depend AS dependencies
		      WHERE dependencies.classid = 'pg_class'::regclass
		        AND dependencies.objid = classes.oid
		        AND dependencies.refclassid = 'pg_class'::regclass
		        AND dependencies.deptype IN ('a', 'i')
		    )
		  )
		  AND classes.relowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		ORDER BY classes.relkind, namespaces.nspname, classes.relname
	`, coreauthority.PublicSchema, coreauthority.StableCoreViewSchema, ownerRole)
	if err != nil {
		return nil, fmt.Errorf("load Core relations for ownership transfer: %w", err)
	}
	defer rows.Close()
	var relations []coreOwnedRelation
	for rows.Next() {
		var relation coreOwnedRelation
		if err := rows.Scan(&relation.schema, &relation.name, &relation.kind); err != nil {
			return nil, err
		}
		relations = append(relations, relation)
	}
	return relations, rows.Err()
}

func loadCoreOwnedRoutines(ctx context.Context, tx *sql.Tx, ownerRole string) ([]coreOwnedRoutine, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT namespaces.nspname, routines.proname,
		       pg_get_function_identity_arguments(routines.oid), routines.prokind::TEXT
		FROM pg_proc AS routines
		JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		WHERE namespaces.nspname IN ($1, $2)
		  AND NOT (namespaces.nspname = $1 AND routines.proname LIKE 'river\_%' ESCAPE '\')
		  AND routines.proowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		ORDER BY namespaces.nspname, routines.proname, routines.oid
	`, coreauthority.PublicSchema, coreauthority.StableCoreViewSchema, ownerRole)
	if err != nil {
		return nil, fmt.Errorf("load Core routines for ownership transfer: %w", err)
	}
	defer rows.Close()
	var routines []coreOwnedRoutine
	for rows.Next() {
		var routine coreOwnedRoutine
		if err := rows.Scan(&routine.schema, &routine.name, &routine.arguments, &routine.kind); err != nil {
			return nil, err
		}
		routines = append(routines, routine)
	}
	return routines, rows.Err()
}

func loadCoreOwnedTypes(ctx context.Context, tx *sql.Tx, ownerRole string) ([]coreOwnedType, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT namespaces.nspname, types.typname
		FROM pg_type AS types
		JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
		WHERE namespaces.nspname IN ($1, $2)
		  AND types.typtype IN ('d', 'e', 'r', 'm')
		  AND NOT (namespaces.nspname = $1 AND types.typname ~ '^_?river_')
		  AND types.typowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		ORDER BY namespaces.nspname, types.typname
	`, coreauthority.PublicSchema, coreauthority.StableCoreViewSchema, ownerRole)
	if err != nil {
		return nil, fmt.Errorf("load Core types for ownership transfer: %w", err)
	}
	defer rows.Close()
	var types []coreOwnedType
	for rows.Next() {
		var coreType coreOwnedType
		if err := rows.Scan(&coreType.schema, &coreType.name); err != nil {
			return nil, err
		}
		types = append(types, coreType)
	}
	return types, rows.Err()
}

func validateCoreOwnership(ctx context.Context, tx *sql.Tx, authority coreMigrationAuthority) error {
	var conflict bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  EXISTS (
			SELECT 1
			FROM pg_namespace AS namespaces
			WHERE namespaces.nspname IN ($1, $2)
			  AND namespaces.nspowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			WHERE namespaces.nspname IN ($1, $2)
			  AND NOT (namespaces.nspname = $1 AND classes.relname LIKE 'river\_%' ESCAPE '\')
			  AND classes.relowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_proc AS routines
			JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
			WHERE namespaces.nspname IN ($1, $2)
			  AND NOT (namespaces.nspname = $1 AND routines.proname LIKE 'river\_%' ESCAPE '\')
			  AND routines.proowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		  )
		  OR EXISTS (
			SELECT 1
			FROM pg_type AS types
			JOIN pg_namespace AS namespaces ON namespaces.oid = types.typnamespace
			WHERE namespaces.nspname IN ($1, $2)
			  AND NOT (namespaces.nspname = $1 AND types.typname ~ '^_?river_')
			  AND types.typowner <> (SELECT oid FROM pg_roles WHERE rolname = $3)
		  )
	`, coreauthority.PublicSchema, coreauthority.StableCoreViewSchema, authority.OwnerRole).Scan(&conflict); err != nil {
		return fmt.Errorf("validate Core object ownership: %w", err)
	}
	if conflict {
		return fmt.Errorf("%w: Core schemas contain objects not owned by %s", ErrCoreAuthorityConflict, authority.OwnerRole)
	}
	return validateCoreOwnerIsolation(ctx, tx, authority)
}

func validateCoreMigrationConnection(ctx context.Context, db *sql.DB, authority coreMigrationAuthority) error {
	var sessionRole, currentRole string
	var canSet bool
	if err := db.QueryRowContext(ctx, `
		SELECT session_user, current_user, pg_has_role(session_user, $1, 'SET')
	`, authority.OwnerRole).Scan(&sessionRole, &currentRole, &canSet); err != nil {
		return fmt.Errorf("inspect Core migration connection authority: %w", err)
	}
	if sessionRole != authority.SessionRole || currentRole != authority.OwnerRole || !canSet {
		return fmt.Errorf(
			"%w: migration connection must authenticate as %s and SET ROLE %s (session=%s current=%s canSet=%t)",
			ErrCoreAuthorityConflict, authority.SessionRole, authority.OwnerRole,
			sessionRole, currentRole, canSet,
		)
	}
	return nil
}
