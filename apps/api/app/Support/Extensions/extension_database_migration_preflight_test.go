package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestLifecycleMigrationPreflightResultReportsTransactionsAndBackup(t *testing.T) {
	artifact := extensionDatabaseDiscoveryArtifact(
		"fixture.plugin", "2.0.0",
		[]extensions.ManifestMigration{extensionDatabaseDiscoveryMigration("fixture.plugin.migration.initial", "initial")},
	)
	artifact.LifecycleMigrationArtifact.MigrationsDigest = strings.Repeat("a", 64)
	artifact.Database = extensions.ManifestDatabase{
		ContractVersion: "fixture.plugin.database@1",
		Grants:          []string{extensionmanifest.DatabaseGrantOwnSchema, extensionmanifest.DatabaseGrantHostCommands},
		Backup:          extensionmanifest.ManifestBackupPolicy{Required: true, Strategy: "operator_snapshot"},
	}
	step := extensionDatabaseMigrationStepPlan{
		Position: 1, Direction: "up", Declaration: artifact.Migrations[0], Artifact: artifact,
		Statements: []string{"CREATE TABLE items (id BIGINT)"}, Transactional: false,
		WarningCode: extensionDatabaseMigrationWarningNonTransactional,
	}
	dryRun := extensionDatabaseMigrationDryRun{
		EnginePlan: LifecycleMigrationEnginePlan{
			OperationID: 10, Operation: extensions.LifecycleMachineInstall,
			Mode: LifecycleBoundaryMigrationInstall, StepID: "lifecycle.install.02.host.migrating",
			Attempt: 2, PlanDigest: strings.Repeat("b", 64), Target: artifact.LifecycleMigrationArtifact,
		},
		Identifiers: ExtensionDatabaseIdentifiers{Schema: "sforum_ext_s_fixture", OwnerRole: "sforum_ext_o_fixture"},
		Target:      artifact, Steps: []extensionDatabaseMigrationStepPlan{step},
		Digest: strings.Repeat("c", 64), HasNonTransactional: true,
		WarningCode: extensionDatabaseMigrationWarningNonTransactional,
	}

	result := lifecycleMigrationPreflightResult(dryRun)
	wantStatementDigest := sha256.Sum256([]byte(step.Statements[0]))
	if result.Operation != "install" || result.Attempt != 2 || result.DryRunDigest != dryRun.Digest ||
		result.Target.DatabaseContractVersion != artifact.Database.ContractVersion ||
		!reflect.DeepEqual(result.Target.DatabaseGrants, artifact.Database.Grants) ||
		!result.Backup.Required || !reflect.DeepEqual(result.Backup.Strategies, []string{"operator_snapshot"}) ||
		!result.HasNonTransactional || !reflect.DeepEqual(result.Warnings, []string{extensionDatabaseMigrationWarningNonTransactional}) ||
		len(result.Steps) != 1 || !result.Steps[0].NoTransaction ||
		result.Steps[0].ExecutionMode != "non_transactional" ||
		result.Steps[0].StatementDigests[0] != hex.EncodeToString(wantStatementDigest[:]) {
		t.Fatalf("unexpected preflight result: %#v", result)
	}
}

func TestPostgresLifecycleMigrationPreflightIsExactAndReadOnly(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE preflight_probe (id BIGINT);`, "auto")
	setExtensionDatabasePreflightBackup(t, fixture, true, "operator_snapshot")

	first, err := fixture.engine.PreflightLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.engine.PreflightLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated preflight drifted: first=%#v second=%#v", first, second)
	}
	if first.PlanDigest != fixture.plan.PlanDigest || !validLifecycleCleanupDigest(first.DryRunDigest) ||
		first.SchemaName != fixture.identifiers.Schema || first.OwnerRoleName != fixture.identifiers.OwnerRole ||
		first.Target.VersionID != fixture.plan.Target.VersionID ||
		first.Target.PackageDigest != fixture.plan.Target.PackageDigest ||
		first.Target.MigrationsDigest != fixture.plan.Target.MigrationsDigest ||
		!first.Backup.Required || !reflect.DeepEqual(first.Backup.Strategies, []string{"operator_snapshot"}) ||
		len(first.Steps) != 1 || first.Steps[0].Checksum != fixture.plan.Target.Migrations[0].Digest ||
		first.Steps[0].TransactionPolicy != "auto" || !first.Steps[0].NoTransaction ||
		first.Steps[0].ExecutionMode != "non_transactional" ||
		first.Steps[0].WarningCode != extensionDatabaseMigrationWarningNonTransactional ||
		len(first.Steps[0].StatementDigests) != 1 {
		t.Fatalf("unexpected exact preflight: %#v", first)
	}
	assertExtensionDatabasePreflightDidNotMutate(t, fixture)
}

func TestPostgresLifecycleMigrationPreflightRejectsChecksumDriftWithoutLedgerWrites(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `CREATE TABLE checksum_preflight_probe (id BIGINT);`, "required")
	if err := os.WriteFile(fixture.migrationPath, []byte("CREATE TABLE tampered_preflight_probe (id BIGINT);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.engine.PreflightLifecycleMigration(fixture.ctx, fixture.plan)
	if !errors.Is(err, ErrExtensionDatabaseMigrationChecksumDrift) {
		t.Fatalf("checksum drift error = %v", err)
	}
	assertExtensionDatabasePreflightDidNotMutate(t, fixture)
}

func setExtensionDatabasePreflightBackup(
	t *testing.T,
	fixture extensionDatabaseMigrationEngineFixture,
	required bool,
	strategy string,
) {
	t.Helper()
	var manifestJSON []byte
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT manifest FROM extension_versions WHERE id = $1
	`, fixture.plan.Target.VersionID).Scan(&manifestJSON); err != nil {
		t.Fatal(err)
	}
	var manifest extensions.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Database.Backup = extensionmanifest.ManifestBackupPolicy{Required: required, Strategy: strategy}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_versions SET manifest = $2::jsonb WHERE id = $1
	`, fixture.plan.Target.VersionID, manifestJSON); err != nil {
		t.Fatal(err)
	}
}

func assertExtensionDatabasePreflightDidNotMutate(
	t *testing.T,
	fixture extensionDatabaseMigrationEngineFixture,
) {
	t.Helper()
	var resources, plans, steps, state, proofs int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_resources WHERE extension_id = $1),
		  (SELECT count(*) FROM extension_database_migration_plans WHERE extension_id = $1),
		  (SELECT count(*) FROM extension_database_migration_steps AS steps
		   JOIN extension_database_migration_plans AS plans ON plans.id = steps.plan_id
		   WHERE plans.extension_id = $1),
		  (SELECT count(*) FROM extension_database_migration_state WHERE extension_id = $1),
		  (SELECT count(*) FROM extension_database_migration_proofs AS proofs
		   JOIN extension_database_migration_plans AS plans ON plans.id = proofs.plan_id
		   WHERE plans.extension_id = $1)
	`, fixture.extensionID).Scan(&resources, &plans, &steps, &state, &proofs); err != nil {
		t.Fatal(err)
	}
	if resources != 0 || plans != 0 || steps != 0 || state != 0 || proofs != 0 {
		t.Fatalf("preflight wrote database state: resources=%d plans=%d steps=%d state=%d proofs=%d",
			resources, plans, steps, state, proofs)
	}
	var schemaPresent, ownerRolePresent, runtimeRolePresent bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1),
		  EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2),
		  EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $3)
	`, fixture.identifiers.Schema, fixture.identifiers.OwnerRole, fixture.identifiers.RuntimeRole).Scan(
		&schemaPresent, &ownerRolePresent, &runtimeRolePresent,
	); err != nil {
		t.Fatal(err)
	}
	if schemaPresent || ownerRolePresent || runtimeRolePresent {
		t.Fatalf("preflight provisioned resources: schema=%v owner=%v runtime=%v",
			schemaPresent, ownerRolePresent, runtimeRolePresent)
	}
	qualified := fixture.identifiers.Schema + ".preflight_probe"
	var migrationExecuted bool
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT to_regclass($1) IS NOT NULL`, qualified).Scan(&migrationExecuted); err != nil {
		t.Fatal(err)
	}
	if migrationExecuted {
		t.Fatal("preflight executed package migration SQL")
	}
}
