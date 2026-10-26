package extensionsruntime

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

const extensionDatabaseCoreSchema = coreauthority.PublicSchema

var (
	extensionDatabaseRawCoreTablePrivileges = extensionDatabasePrivileges{
		"SELECT": false, "INSERT": false, "UPDATE": false, "DELETE": false,
	}
	extensionDatabaseRawCoreSequencePrivileges = extensionDatabasePrivileges{
		"USAGE": false, "SELECT": false, "UPDATE": false,
	}
	extensionDatabaseCoreViewTablePrivileges = extensionDatabasePrivileges{"SELECT": false}
)

type extensionDatabasePrivileges map[string]bool

type extensionDatabaseCoreRelation struct {
	oid        uint32
	schema     string
	name       string
	isSequence bool
	isRiver    bool
}

type extensionDatabaseCoreIdentity struct {
	ownerRole   string
	sessionRole string
}

// raw_core 只开放既有 Core 基础表的 DML；DDL、函数执行和对象所有权属于更高风险边界。
func grantExtensionDatabaseRawCoreAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	allowedKernelOwners ...map[string]struct{},
) error {
	if !validPostgresIdentifier(roleName) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	role := pgx.Identifier{roleName}.Sanitize()
	coreSchema := pgx.Identifier{extensionDatabaseCoreSchema}.Sanitize()
	if _, err := tx.Exec(ctx, `GRANT USAGE ON SCHEMA `+coreSchema+` TO `+role); err != nil {
		return fmt.Errorf("%w: grant raw Core schema usage: %v", ErrExtensionDatabaseCredential, err)
	}
	relations, err := loadExtensionDatabaseCoreRelations(ctx, tx, allowedKernelOwners...)
	if err != nil {
		return err
	}
	for _, relation := range relations {
		if relation.schema != extensionDatabaseCoreSchema || relation.isRiver {
			continue
		}
		object := pgx.Identifier{relation.schema, relation.name}.Sanitize()
		query := `GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE ` + object + ` TO ` + role
		if relation.isSequence {
			query = `GRANT USAGE, SELECT, UPDATE ON SEQUENCE ` + object + ` TO ` + role
		}
		if _, err := tx.Exec(ctx, query); err != nil {
			return fmt.Errorf("%w: grant raw Core relation authority: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	return nil
}

// PostgreSQL 默认向 PUBLIC 开放函数执行；只省略显式 GRANT 不能形成 raw_core 边界。
// 同时收敛 Core 与原始数据库登录的默认权限，避免滚动升级中新建函数重新泄露给存量租约。
func hardenExtensionDatabaseCoreRoutineAuthority(
	ctx context.Context,
	tx pgx.Tx,
	allowedKernelOwners map[string]struct{},
) error {
	identity, err := loadExtensionDatabaseCoreIdentity(ctx, tx)
	if err != nil {
		return err
	}
	routines, err := tx.Query(ctx, `
		SELECT namespaces.nspname, routines.proname, owners.rolname
		FROM pg_proc AS routines
		JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		JOIN pg_roles AS owners ON owners.oid = routines.proowner
		WHERE namespaces.nspname IN ($1, $2)
		ORDER BY namespaces.nspname, routines.proname, routines.oid
	`, extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema)
	if err != nil {
		return fmt.Errorf("inspect Core routine ownership: %w", err)
	}
	for routines.Next() {
		var schema, name, owner string
		if err := routines.Scan(&schema, &name, &owner); err != nil {
			routines.Close()
			return err
		}
		expectedOwner := identity.ownerRole
		isRiver := schema == extensionDatabaseCoreSchema && coreauthority.IsRiverObjectName(name)
		if isRiver {
			expectedOwner = identity.sessionRole
		}
		_, allowedKernelOwner := allowedKernelOwners[owner]
		if owner != expectedOwner && (!allowedKernelOwner || isRiver) {
			routines.Close()
			return ErrExtensionDatabaseResourceConflict
		}
	}
	if err := routines.Err(); err != nil {
		routines.Close()
		return err
	}
	routines.Close()

	seen := make(map[string]struct{}, 2)
	for _, roleName := range []string{identity.sessionRole, identity.ownerRole} {
		if _, ok := seen[roleName]; ok {
			continue
		}
		seen[roleName] = struct{}{}
		role := pgx.Identifier{roleName}.Sanitize()
		// PostgreSQL 的内置 PUBLIC EXECUTE 是全局默认；按 schema 的 REVOKE 无法覆盖它。
		if _, err := tx.Exec(ctx,
			`ALTER DEFAULT PRIVILEGES FOR ROLE `+role+` REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`,
		); err != nil {
			return fmt.Errorf("%w: harden Core routine default privileges: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	for _, schemaName := range []string{extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema} {
		schema := pgx.Identifier{schemaName}.Sanitize()
		if _, err := tx.Exec(ctx, `REVOKE EXECUTE ON ALL ROUTINES IN SCHEMA `+schema+` FROM PUBLIC`); err != nil {
			return fmt.Errorf("%w: harden existing Core routine privileges: %v", ErrExtensionDatabaseCredential, err)
		}
		if _, err := tx.Exec(ctx, `REVOKE CREATE, USAGE ON SCHEMA `+schema+` FROM PUBLIC`); err != nil {
			return fmt.Errorf("%w: harden Core schema privileges: %v", ErrExtensionDatabaseCredential, err)
		}
	}
	return nil
}

func loadExtensionDatabaseCoreRelations(
	ctx context.Context,
	tx pgx.Tx,
	allowedKernelOwners ...map[string]struct{},
) ([]extensionDatabaseCoreRelation, error) {
	identity, err := loadExtensionDatabaseCoreIdentity(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
			SELECT classes.oid, namespaces.nspname, classes.relname,
			       classes.relkind = 'S', owners.rolname
			FROM pg_class AS classes
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			JOIN pg_roles AS owners ON owners.oid = classes.relowner
			WHERE (namespaces.nspname = $1 AND classes.relkind IN ('r', 'p', 'S'))
			   OR (namespaces.nspname = $2 AND classes.relkind IN ('r', 'p', 'S', 'v', 'm', 'f'))
			ORDER BY namespaces.nspname, classes.relkind, classes.relname
		`, extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema)
	if err != nil {
		return nil, fmt.Errorf("inspect raw Core relations: %w", err)
	}
	defer rows.Close()
	relations := make([]extensionDatabaseCoreRelation, 0)
	allowedOwners := map[string]struct{}(nil)
	if len(allowedKernelOwners) > 0 {
		allowedOwners = allowedKernelOwners[0]
	}
	for rows.Next() {
		var relation extensionDatabaseCoreRelation
		var owner string
		if err := rows.Scan(&relation.oid, &relation.schema, &relation.name, &relation.isSequence, &owner); err != nil {
			return nil, err
		}
		relation.isRiver = relation.schema == extensionDatabaseCoreSchema && coreauthority.IsRiverObjectName(relation.name)
		expectedOwner := identity.ownerRole
		if relation.isRiver {
			expectedOwner = identity.sessionRole
		}
		_, allowedKernelOwner := allowedOwners[owner]
		if owner != expectedOwner && (!allowedKernelOwner || relation.isRiver) {
			return nil, ErrExtensionDatabaseResourceConflict
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return relations, nil
}

func loadExtensionDatabaseCoreIdentity(ctx context.Context, tx pgx.Tx) (extensionDatabaseCoreIdentity, error) {
	var databaseName string
	var identity extensionDatabaseCoreIdentity
	if err := tx.QueryRow(ctx, `SELECT current_database(), session_user`).Scan(
		&databaseName, &identity.sessionRole,
	); err != nil {
		return extensionDatabaseCoreIdentity{}, fmt.Errorf("inspect Core database identity: %w", err)
	}
	ownerRole, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil || !validPostgresIdentifier(identity.sessionRole) || !validPostgresIdentifier(ownerRole) {
		return extensionDatabaseCoreIdentity{}, ErrExtensionDatabaseRegistryInvalid
	}
	var schemaCount, ownedSchemaCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE owners.rolname = $3)
		FROM pg_namespace AS namespaces
		JOIN pg_roles AS owners ON owners.oid = namespaces.nspowner
		WHERE namespaces.nspname IN ($1, $2)
	`, extensionDatabaseCoreSchema, extensionDatabaseCoreViewSchema, ownerRole).Scan(
		&schemaCount, &ownedSchemaCount,
	); err != nil {
		return extensionDatabaseCoreIdentity{}, fmt.Errorf("inspect Core schema ownership: %w", err)
	}
	if schemaCount != 2 || ownedSchemaCount != schemaCount {
		return extensionDatabaseCoreIdentity{}, ErrExtensionDatabaseResourceConflict
	}
	identity.ownerRole = ownerRole
	return identity, nil
}

func validateExtensionDatabaseRawCoreAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	powers []string,
	allowedKernelOwners ...map[string]struct{},
) error {
	if !validPostgresIdentifier(roleName) {
		return ErrExtensionDatabaseRegistryInvalid
	}
	// kernel 的对象所有权模型单独收敛；本校验只证明 raw_core 没有扩大成 DDL 权限。
	if containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantKernel) {
		return nil
	}
	wantRawCore := containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantRawCore)
	wantCoreViews := containsExtensionDatabasePower(powers, extensionmanifest.DatabaseGrantCoreViews)
	relations, err := loadExtensionDatabaseCoreRelations(ctx, tx, allowedKernelOwners...)
	if err != nil {
		return err
	}
	tablePrivileges, sequencePrivileges, err := loadExtensionDatabaseDirectCorePrivileges(ctx, tx, roleName)
	if err != nil {
		return err
	}
	for _, relation := range relations {
		privileges := tablePrivileges[relation.oid]
		expected := extensionDatabasePrivileges(nil)
		if relation.isSequence {
			privileges = sequencePrivileges[relation.oid]
		}
		if wantRawCore && relation.schema == extensionDatabaseCoreSchema && !relation.isRiver {
			expected = extensionDatabaseRawCoreTablePrivileges
			if relation.isSequence {
				expected = extensionDatabaseRawCoreSequencePrivileges
			}
		} else if wantCoreViews && relation.schema == extensionDatabaseCoreViewSchema && !relation.isSequence {
			expected = extensionDatabaseCoreViewTablePrivileges
		}
		if !sameExtensionDatabasePrivileges(privileges, expected) {
			return ErrExtensionDatabaseResourceConflict
		}
	}

	schemaPrivileges, functionPrivilege, err := loadExtensionDatabaseDirectCoreNonRelationPrivileges(ctx, tx, roleName)
	if err != nil {
		return err
	}
	if functionPrivilege {
		return ErrExtensionDatabaseResourceConflict
	}
	for _, expectation := range []struct {
		schema string
		usage  bool
	}{
		{schema: extensionDatabaseCoreSchema, usage: wantRawCore},
		{schema: extensionDatabaseCoreViewSchema, usage: wantCoreViews},
	} {
		expected := extensionDatabasePrivileges(nil)
		if expectation.usage {
			expected = extensionDatabasePrivileges{"USAGE": false}
		}
		if !sameExtensionDatabasePrivileges(schemaPrivileges[expectation.schema], expected) {
			return ErrExtensionDatabaseResourceConflict
		}
	}
	if err := validateExtensionDatabaseEffectiveCoreAuthority(
		ctx, tx, roleName, relations, wantRawCore, wantCoreViews,
	); err != nil {
		return err
	}
	return nil
}

func loadExtensionDatabaseDirectCorePrivileges(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
) (map[uint32]extensionDatabasePrivileges, map[uint32]extensionDatabasePrivileges, error) {
	rows, err := tx.Query(ctx, `
			SELECT classes.oid, classes.relkind = 'S',
			       privileges.privilege_type, privileges.is_grantable
			FROM pg_class AS classes
		JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			LEFT JOIN LATERAL (
				SELECT acl.privilege_type, acl.is_grantable
			FROM aclexplode(classes.relacl) AS acl
			WHERE acl.grantee = (SELECT oid FROM pg_roles WHERE rolname = $2)
		) AS privileges ON true
			WHERE (namespaces.nspname = $1 AND classes.relkind IN ('r', 'p', 'S'))
			   OR (namespaces.nspname = $3 AND classes.relkind IN ('r', 'p', 'S', 'v', 'm', 'f'))
			ORDER BY namespaces.nspname, classes.relkind, classes.relname, privileges.privilege_type
		`, extensionDatabaseCoreSchema, roleName, extensionDatabaseCoreViewSchema)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect raw Core relation ACLs: %w", err)
	}
	defer rows.Close()
	tables := make(map[uint32]extensionDatabasePrivileges)
	sequences := make(map[uint32]extensionDatabasePrivileges)
	for rows.Next() {
		var oid uint32
		var isSequence bool
		var privilege *string
		var isGrantable *bool
		if err := rows.Scan(&oid, &isSequence, &privilege, &isGrantable); err != nil {
			return nil, nil, err
		}
		target := tables
		if isSequence {
			target = sequences
		}
		if target[oid] == nil {
			target[oid] = make(extensionDatabasePrivileges)
		}
		if privilege != nil && isGrantable != nil {
			target[oid][*privilege] = *isGrantable
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return tables, sequences, nil
}

func loadExtensionDatabaseDirectCoreNonRelationPrivileges(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
) (map[string]extensionDatabasePrivileges, bool, error) {
	schemaRows, err := tx.Query(ctx, `
		SELECT namespaces.nspname, privileges.privilege_type, privileges.is_grantable
		FROM pg_namespace AS namespaces
		CROSS JOIN LATERAL aclexplode(namespaces.nspacl) AS privileges
		WHERE namespaces.nspname IN ($1, $3)
		  AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $2)
	`, extensionDatabaseCoreSchema, roleName, extensionDatabaseCoreViewSchema)
	if err != nil {
		return nil, false, fmt.Errorf("inspect raw Core schema ACL: %w", err)
	}
	schemaPrivileges := make(map[string]extensionDatabasePrivileges)
	for schemaRows.Next() {
		var schema, privilege string
		var isGrantable bool
		if err := schemaRows.Scan(&schema, &privilege, &isGrantable); err != nil {
			schemaRows.Close()
			return nil, false, err
		}
		if schemaPrivileges[schema] == nil {
			schemaPrivileges[schema] = make(extensionDatabasePrivileges)
		}
		schemaPrivileges[schema][privilege] = isGrantable
	}
	schemaRows.Close()
	if err := schemaRows.Err(); err != nil {
		return nil, false, err
	}

	var functionPrivilege bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_proc AS functions
			JOIN pg_namespace AS namespaces ON namespaces.oid = functions.pronamespace
			WHERE namespaces.nspname IN ($1, $3)
			  AND has_function_privilege($2, functions.oid, 'EXECUTE')
		)
	`, extensionDatabaseCoreSchema, roleName, extensionDatabaseCoreViewSchema).Scan(&functionPrivilege); err != nil {
		return nil, false, fmt.Errorf("inspect raw Core function ACLs: %w", err)
	}
	return schemaPrivileges, functionPrivilege, nil
}

func validateExtensionDatabaseEffectiveCoreAuthority(
	ctx context.Context,
	tx pgx.Tx,
	roleName string,
	relations []extensionDatabaseCoreRelation,
	wantRawCore bool,
	wantCoreViews bool,
) error {
	for _, expectation := range []struct {
		schema string
		usage  bool
	}{
		{schema: extensionDatabaseCoreSchema, usage: wantRawCore},
		{schema: extensionDatabaseCoreViewSchema, usage: wantCoreViews},
	} {
		var schemaUsage, schemaCreate, schemaUsageGrant, schemaCreateGrant bool
		if err := tx.QueryRow(ctx, `
			SELECT has_schema_privilege($1, $2, 'USAGE'),
			       has_schema_privilege($1, $2, 'CREATE'),
			       has_schema_privilege($1, $2, 'USAGE WITH GRANT OPTION'),
			       has_schema_privilege($1, $2, 'CREATE WITH GRANT OPTION')
		`, roleName, expectation.schema).Scan(
			&schemaUsage, &schemaCreate, &schemaUsageGrant, &schemaCreateGrant,
		); err != nil {
			return fmt.Errorf("inspect effective Core schema authority: %w", err)
		}
		if schemaUsage != expectation.usage || schemaCreate || schemaUsageGrant || schemaCreateGrant {
			return ErrExtensionDatabaseResourceConflict
		}
	}

	var directColumnPrivilege bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_attribute AS attributes
			JOIN pg_class AS classes ON classes.oid = attributes.attrelid
			JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
			CROSS JOIN LATERAL aclexplode(attributes.attacl) AS privileges
			WHERE (
			  (namespaces.nspname = $1 AND classes.relkind IN ('r', 'p'))
			  OR (namespaces.nspname = $3 AND classes.relkind IN ('r', 'p', 'v', 'm', 'f'))
			)
			  AND privileges.grantee = (SELECT oid FROM pg_roles WHERE rolname = $2)
		)
	`, extensionDatabaseCoreSchema, roleName, extensionDatabaseCoreViewSchema).Scan(&directColumnPrivilege); err != nil {
		return fmt.Errorf("inspect direct raw Core column authority: %w", err)
	}
	if directColumnPrivilege {
		return ErrExtensionDatabaseResourceConflict
	}

	for _, relation := range relations {
		expectedRaw := wantRawCore && relation.schema == extensionDatabaseCoreSchema && !relation.isRiver
		expectedView := wantCoreViews && relation.schema == extensionDatabaseCoreViewSchema && !relation.isSequence
		if relation.isSequence {
			var usage, selectPrivilege, update, usageGrant, selectGrant, updateGrant bool
			if err := tx.QueryRow(ctx, `
				SELECT has_sequence_privilege($1, $2::oid, 'USAGE'),
				       has_sequence_privilege($1, $2::oid, 'SELECT'),
				       has_sequence_privilege($1, $2::oid, 'UPDATE'),
				       has_sequence_privilege($1, $2::oid, 'USAGE WITH GRANT OPTION'),
				       has_sequence_privilege($1, $2::oid, 'SELECT WITH GRANT OPTION'),
				       has_sequence_privilege($1, $2::oid, 'UPDATE WITH GRANT OPTION')
			`, roleName, relation.oid).Scan(
				&usage, &selectPrivilege, &update, &usageGrant, &selectGrant, &updateGrant,
			); err != nil {
				return fmt.Errorf("inspect effective raw Core sequence authority: %w", err)
			}
			if usage != expectedRaw || selectPrivilege != expectedRaw || update != expectedRaw ||
				usageGrant || selectGrant || updateGrant {
				return ErrExtensionDatabaseResourceConflict
			}
			continue
		}

		var selectPrivilege, insert, update, deletePrivilege, truncate, references, trigger bool
		var selectGrant, insertGrant, updateGrant, deleteGrant, truncateGrant, referencesGrant, triggerGrant bool
		if err := tx.QueryRow(ctx, `
			SELECT has_table_privilege($1, $2::oid, 'SELECT'),
			       has_table_privilege($1, $2::oid, 'INSERT'),
			       has_table_privilege($1, $2::oid, 'UPDATE'),
			       has_table_privilege($1, $2::oid, 'DELETE'),
			       has_table_privilege($1, $2::oid, 'TRUNCATE'),
			       has_table_privilege($1, $2::oid, 'REFERENCES'),
			       has_table_privilege($1, $2::oid, 'TRIGGER'),
			       has_table_privilege($1, $2::oid, 'SELECT WITH GRANT OPTION'),
			       has_table_privilege($1, $2::oid, 'INSERT WITH GRANT OPTION'),
			       has_table_privilege($1, $2::oid, 'UPDATE WITH GRANT OPTION'),
			       has_table_privilege($1, $2::oid, 'DELETE WITH GRANT OPTION'),
			       has_table_privilege($1, $2::oid, 'TRUNCATE WITH GRANT OPTION'),
			       has_table_privilege($1, $2::oid, 'REFERENCES WITH GRANT OPTION'),
			       has_table_privilege($1, $2::oid, 'TRIGGER WITH GRANT OPTION')
		`, roleName, relation.oid).Scan(
			&selectPrivilege, &insert, &update, &deletePrivilege, &truncate, &references, &trigger,
			&selectGrant, &insertGrant, &updateGrant, &deleteGrant, &truncateGrant, &referencesGrant, &triggerGrant,
		); err != nil {
			return fmt.Errorf("inspect effective raw Core table authority: %w", err)
		}
		if selectPrivilege != (expectedRaw || expectedView) || insert != expectedRaw || update != expectedRaw || deletePrivilege != expectedRaw ||
			truncate || references || trigger || selectGrant || insertGrant || updateGrant || deleteGrant ||
			truncateGrant || referencesGrant || triggerGrant {
			return ErrExtensionDatabaseResourceConflict
		}

		var columnConflict bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_attribute AS attributes
				WHERE attributes.attrelid = $2::oid
				  AND attributes.attnum > 0 AND NOT attributes.attisdropped
				  AND (
				    has_column_privilege($1, $2::oid, attributes.attnum, 'SELECT') <> $3
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'INSERT') <> $4
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'UPDATE') <> $4
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'REFERENCES')
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'SELECT WITH GRANT OPTION')
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'INSERT WITH GRANT OPTION')
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'UPDATE WITH GRANT OPTION')
				    OR has_column_privilege($1, $2::oid, attributes.attnum, 'REFERENCES WITH GRANT OPTION')
				  )
			)
		`, roleName, relation.oid, expectedRaw || expectedView, expectedRaw).Scan(&columnConflict); err != nil {
			return fmt.Errorf("inspect effective raw Core column authority: %w", err)
		}
		if columnConflict {
			return ErrExtensionDatabaseResourceConflict
		}
	}
	return nil
}

func sameExtensionDatabasePrivileges(actual, expected extensionDatabasePrivileges) bool {
	if len(actual) != len(expected) {
		return false
	}
	for privilege, grantable := range expected {
		if actualGrantable, ok := actual[privilege]; !ok || actualGrantable != grantable {
			return false
		}
	}
	return true
}
