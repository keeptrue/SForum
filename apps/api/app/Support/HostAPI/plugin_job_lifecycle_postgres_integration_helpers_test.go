package hostapi

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

type pluginJobLifecycleIntegrationHarness struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	river *river.Client[pgx.Tx]
	store *PostgresPluginJobLifecycleStore
}

func newPluginJobLifecycleIntegrationHarness(t *testing.T) *pluginJobLifecycleIntegrationHarness {
	t.Helper()
	databaseURL := pluginJobLifecycleIntegrationHarnessTemplate.Clone(t, "plugin_job_lifecycle")

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open integration pool: %v", err)
	}
	t.Cleanup(pool.Close)

	driver := riverpgxv5.New(pool)
	riverClient, err := river.NewClient(driver, &river.Config{Schema: "public"})
	if err != nil {
		t.Fatalf("create River client: %v", err)
	}
	return &pluginJobLifecycleIntegrationHarness{
		ctx: ctx, pool: pool, river: riverClient,
		store: NewPostgresPluginJobLifecycleStore(pool, riverClient),
	}
}

var pluginJobLifecycleIntegrationHarnessTemplate = testdb.NewTemplate(
	"plugin_job_lifecycle",
	preparePluginJobLifecycleIntegrationHarnessTemplate,
)

func preparePluginJobLifecycleIntegrationHarnessTemplate(ctx context.Context, databaseURL string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	driver := riverpgxv5.New(pool)
	riverMigrator, err := rivermigrate.New(driver, &rivermigrate.Config{Schema: "public"})
	if err != nil {
		return err
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("migrate River template: %w", err)
	}
	return installPluginJobMigrationLedger(ctx, pool)
}

func installPluginJobMigrationLedger(ctx context.Context, pool *pgxpool.Pool) error {
	const filename = "202607140003_extension_plugin_job_migrations.sql"
	body, err := fs.ReadFile(migrations.Files(), filename)
	if err != nil {
		return fmt.Errorf("read %s: %w", filename, err)
	}
	up := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(up) != 2 {
		return fmt.Errorf("migration %s has no Down boundary", filename)
	}
	if _, err := pool.Exec(ctx, up[0]); err != nil {
		return fmt.Errorf("install plugin job migration ledger: %w", err)
	}
	return nil
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
