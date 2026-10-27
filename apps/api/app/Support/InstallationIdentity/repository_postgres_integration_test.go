package installationidentity

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresRepositoryConcurrentInitializationAndTamperRejection(t *testing.T) {
	fixture := newPostgresIdentityFixture(t, "concurrent")
	const workers = 32
	identities := make(chan string, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			identity, err := fixture.repository.Ensure(fixture.ctx)
			if err != nil {
				errorsFound <- err
				return
			}
			identities <- identity
		}()
	}
	wait.Wait()
	close(identities)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	var expected string
	for identity := range identities {
		if expected == "" {
			expected = identity
		}
		if identity != expected || !Valid(identity) {
			t.Fatalf("concurrent identity = %q want %q", identity, expected)
		}
	}
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM host_installation_identity`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("installation identity rows = %d", count)
	}
	for name, statement := range map[string]string{
		"update":   `UPDATE host_installation_identity SET installation_id = repeat('b', 64)`,
		"delete":   `DELETE FROM host_installation_identity`,
		"truncate": `TRUNCATE host_installation_identity`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.pool.Exec(fixture.ctx, statement); err == nil ||
				!strings.Contains(err.Error(), "host installation identity is immutable") {
				t.Fatalf("tamper error = %v", err)
			}
		})
	}
	identity, err := fixture.repository.Ensure(fixture.ctx)
	if err != nil || identity != expected {
		t.Fatalf("identity after tamper = %q, %v want %q", identity, err, expected)
	}
}

func TestPostgresRepositoryDatabaseClonePreservesIdentity(t *testing.T) {
	source := newPostgresIdentityFixture(t, "clone_source")
	identity, err := source.repository.Ensure(source.ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := newPostgresIdentityFixture(t, "clone_target")
	if _, err := target.pool.Exec(target.ctx, `
		INSERT INTO host_installation_identity (singleton, installation_id)
		VALUES (TRUE, $1)
	`, identity); err != nil {
		t.Fatal(err)
	}
	cloned, err := target.repository.Ensure(target.ctx)
	if err != nil || cloned != identity {
		t.Fatalf("cloned identity = %q, %v want %q", cloned, err, identity)
	}
}

type postgresIdentityFixture struct {
	ctx        context.Context
	admin      *pgxpool.Pool
	pool       *pgxpool.Pool
	repository *Repository
	schema     string
}

func newPostgresIdentityFixture(t *testing.T, label string) *postgresIdentityFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("host_installation_%s_%d", label, time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.MaxConns = 40
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if err := applyHostInstallationIdentityMigration(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	fixture := &postgresIdentityFixture{
		ctx: ctx, admin: admin, pool: pool,
		repository: NewPostgresRepository(pool), schema: schema,
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		admin.Close()
	})
	return fixture
}

func applyHostInstallationIdentityMigration(ctx context.Context, pool *pgxpool.Pool) error {
	body, err := fs.ReadFile(migrations.Files(), "202607160032_host_installation_identity.sql")
	if err != nil {
		return err
	}
	up := strings.SplitN(string(body), "-- +goose Down", 2)[0]
	_, err = pool.Exec(ctx, up)
	return err
}
