package hostapi

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

type pluginJobLifecycleIntegrationHarness struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
	store *PostgresPluginJobLifecycleStore
}

func newPluginJobLifecycleIntegrationHarness(t *testing.T) *pluginJobLifecycleIntegrationHarness {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required for plugin job lifecycle integration tests")
	}

	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration admin pool: %v", err)
	}
	schema := fmt.Sprintf("plugin_job_lifecycle_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create integration schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+identifier+" CASCADE")
		admin.Close()
	})

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated integration pool: %v", err)
	}
	t.Cleanup(pool.Close)

	driver := riverpgxv5.New(pool)
	riverMigrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: schema})
	if err != nil {
		t.Fatalf("create isolated River migrator: %v", err)
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		t.Fatalf("migrate isolated River schema: %v", err)
	}
	installPluginJobMigrationLedger(t, ctx, pool)

	riverClient, err := river.NewClient(driver, &river.Config{Schema: schema})
	if err != nil {
		t.Fatalf("create isolated River client: %v", err)
	}
	return &pluginJobLifecycleIntegrationHarness{
		ctx: ctx, pool: pool, river: riverClient,
		store: NewPostgresPluginJobLifecycleStore(pool, riverClient),
	}
}

func installPluginJobMigrationLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	const filename = "202607140003_extension_plugin_job_migrations.sql"
	body, err := fs.ReadFile(migrations.Files(), filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	up := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(up) != 2 {
		t.Fatalf("migration %s has no Down boundary", filename)
	}
	if _, err := pool.Exec(ctx, up[0]); err != nil {
		t.Fatalf("install plugin job migration ledger: %v", err)
	}
}

func (h *pluginJobLifecycleIntegrationHarness) insertJob(
	t *testing.T,
	args river.JobArgs,
	opts *river.InsertOpts,
) *rivertype.JobRow {
	t.Helper()
	result, err := h.river.Insert(h.ctx, args, opts)
	if err != nil {
		t.Fatalf("insert River job: %v", err)
	}
	if result == nil || result.Job == nil {
		t.Fatal("River returned no inserted job")
	}
	return result.Job
}

func (h *pluginJobLifecycleIntegrationHarness) job(t *testing.T, jobID int64) *rivertype.JobRow {
	t.Helper()
	row, err := h.river.JobGet(h.ctx, jobID)
	if err != nil {
		t.Fatalf("get River job %d: %v", jobID, err)
	}
	return row
}

func (h *pluginJobLifecycleIntegrationHarness) countJobs(
	t *testing.T,
	extensionID string,
	artifactDigest string,
) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*)
		FROM river_job
		WHERE kind = 'extension.plugin_job'
		  AND args ->> 'extensionId' = $1
		  AND args ->> 'artifactDigest' = $2
	`, extensionID, artifactDigest).Scan(&count); err != nil {
		t.Fatalf("count exact plugin jobs: %v", err)
	}
	return count
}

func (h *pluginJobLifecycleIntegrationHarness) countLedger(t *testing.T, oldJobID int64) int {
	t.Helper()
	var count int
	if err := h.pool.QueryRow(h.ctx, `
		SELECT count(*) FROM extension_plugin_job_migrations WHERE old_job_id = $1
	`, oldJobID).Scan(&count); err != nil {
		t.Fatalf("count plugin job migration ledger: %v", err)
	}
	return count
}

func integrationPluginJobRuntime(
	extensionID string,
	jobName string,
	version string,
	digest string,
	schemaVersion string,
	trustGrantID string,
) PluginJobRuntimeContract {
	return PluginJobRuntimeContract{
		Contract: supportjobs.PluginJobContract{
			ExtensionID: extensionID, ExtensionVersion: version, ArtifactDigest: digest,
			JobName: jobName, JobContract: "1", PayloadSchemaID: "schema." + jobName,
			PayloadSchemaVersion: schemaVersion,
		},
		TrustGrantID: trustGrantID,
	}
}

func integrationPluginJobArgs(runtime PluginJobRuntimeContract, payload map[string]any) PluginJobArgs {
	contract := runtime.Contract
	return PluginJobArgs{
		EnvelopeVersion: supportjobs.PluginJobEnvelopeVersion,
		ExtensionID:     contract.ExtensionID, ExtensionVersion: contract.ExtensionVersion,
		ArtifactDigest: contract.ArtifactDigest, TrustGrantID: runtime.TrustGrantID,
		JobName: contract.JobName, JobContractVersion: contract.JobContract,
		PayloadSchemaID: contract.PayloadSchemaID, PayloadSchemaVersion: contract.PayloadSchemaVersion,
		Payload: payload, EnqueuedAt: time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC),
	}
}

func integrationPluginJobMigrationInput(
	source PluginJobRuntimeContract,
	target PluginJobRuntimeContract,
	migrator PluginJobPayloadMigrator,
) (supportjobs.PluginJobMigration, PluginJobLifecycleInput) {
	migration := supportjobs.PluginJobMigration{ID: source.Contract.JobName + "-v1-v2", From: source.Contract, To: target.Contract}
	return migration, PluginJobLifecycleInput{
		ExtensionID:     source.Contract.ExtensionID,
		SourceContracts: map[string]PluginJobRuntimeContract{source.Contract.JobName: source},
		TargetContracts: map[string]PluginJobRuntimeContract{target.Contract.JobName: target},
		Migrations:      []supportjobs.PluginJobMigration{migration},
		Migrators:       map[string]PluginJobPayloadMigrator{migration.ID: migrator},
	}
}

type integrationForeignJobArgs struct {
	ExtensionID any `json:"extensionId"`
}

func (integrationForeignJobArgs) Kind() string { return PluginJobKind }

type integrationWrongKindArgs struct {
	ExtensionID string `json:"extensionId"`
}

func (integrationWrongKindArgs) Kind() string { return "extension.foreign_job" }
