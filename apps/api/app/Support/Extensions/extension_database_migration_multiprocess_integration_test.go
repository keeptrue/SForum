package extensionsruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	extensionMigrationProcessHelperEnv = "SFORUM_EXTENSION_MIGRATION_PROCESS_HELPER"
	extensionMigrationProcessPlanEnv   = "SFORUM_EXTENSION_MIGRATION_PROCESS_PLAN"
)

// TestPostgresLifecycleMigrationEngineRunsPlanOnceAcrossProcesses proves that
// the PostgreSQL advisory lock and ledger, rather than process-local state,
// serialize migration execution across independently booted nodes.
func TestPostgresLifecycleMigrationEngineRunsPlanOnceAcrossProcesses(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `
		CREATE TABLE multiprocess_probe (id BIGINT PRIMARY KEY, note TEXT NOT NULL);
		INSERT INTO multiprocess_probe (id, note) VALUES (1, 'once');
	`, "required")

	encodedPlan, err := json.Marshal(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	databaseURL := testdb.RequireIsolatedURL(t)

	type processResult struct {
		index  int
		output string
		err    error
	}
	start := make(chan struct{})
	results := make(chan processResult, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExtensionMigrationProcessHelper$")
			command.Env = append(os.Environ(),
				extensionMigrationProcessHelperEnv+"=1",
				extensionMigrationProcessPlanEnv+"="+base64.RawStdEncoding.EncodeToString(encodedPlan),
				"SFORUM_TEST_DATABASE_URL="+databaseURL,
			)
			output, runErr := command.CombinedOutput()
			results <- processResult{index: index, output: string(output), err: runErr}
		}(index)
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("migration process %d failed: %v\n%s", result.index, result.err, result.output)
		}
	}

	proof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.TargetReady || proof.SourceResumeSafe || proof.PlanDigest != fixture.plan.PlanDigest {
		t.Fatalf("unexpected multi-process proof: %#v", proof)
	}
	var note string
	query := `SELECT note FROM ` + pgx.Identifier{fixture.identifiers.Schema, "multiprocess_probe"}.Sanitize() + ` WHERE id = 1`
	if err := fixture.pool.QueryRow(fixture.ctx, query).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != "once" {
		t.Fatalf("unexpected migration result %q", note)
	}
	var plans, appliedSteps, stateRows int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_migration_plans WHERE extension_id = $1),
		  (SELECT count(*) FROM extension_database_migration_steps AS steps
		   JOIN extension_database_migration_plans AS plans ON plans.id = steps.plan_id
		   WHERE plans.extension_id = $1 AND steps.status = 'applied'),
		  (SELECT count(*) FROM extension_database_migration_state WHERE extension_id = $1)
	`, fixture.extensionID).Scan(&plans, &appliedSteps, &stateRows); err != nil {
		t.Fatal(err)
	}
	if plans != 1 || appliedSteps != 1 || stateRows != 1 {
		t.Fatalf("migration was not cross-process once-only: plans=%d applied=%d state=%d", plans, appliedSteps, stateRows)
	}
}

func TestExtensionMigrationProcessHelper(t *testing.T) {
	if os.Getenv(extensionMigrationProcessHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	encoded := strings.TrimSpace(os.Getenv(extensionMigrationProcessPlanEnv))
	planJSON, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var plan LifecycleMigrationEnginePlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		t.Fatal(err)
	}
	databaseURL := testdb.RequireIsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 2
	config.ConnConfig.RuntimeParams["application_name"] = fmt.Sprintf("sforum-p5-migration-node-%d", os.Getpid())
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := NewPostgresLifecycleMigrationEngine(pool, nil).ReconcileLifecycleMigration(ctx, plan); err != nil {
		t.Fatal(err)
	}
}
