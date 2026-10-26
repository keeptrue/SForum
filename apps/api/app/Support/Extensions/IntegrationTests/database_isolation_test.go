package extensionsruntime_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
)

const pluginTestClusterMarker = "sforum.disposable-test-cluster.v1"

// Reject inherited development connections before any fixture can mutate state.
func TestMain(m *testing.M) {
	target := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if target == "" {
		if strings.TrimSpace(os.Getenv("DATABASE_URL")) != "" {
			fmt.Fprintln(os.Stderr, "plugin integration tests require an explicit isolated SFORUM_TEST_DATABASE_URL; use scripts/test-api-isolated.sh")
			os.Exit(2)
		}
	} else {
		if err := validatePluginTestDatabaseURL(target); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := verifyPluginTestCluster(ctx, target)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.Setenv("DATABASE_URL", target); err != nil {
			fmt.Fprintln(os.Stderr, "set isolated test database environment:", err)
			os.Exit(2)
		}
	}
	os.Exit(m.Run())
}

func validatePluginTestDatabaseURL(target string) error {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" {
		return fmt.Errorf("plugin integration tests require an explicit PostgreSQL URL")
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if !strings.HasPrefix(name, "sforum_test_") || len(name) <= len("sforum_test_") {
		return fmt.Errorf("plugin integration database name must start with sforum_test_")
	}
	for _, key := range []string{"dbname", "host", "hostaddr", "port", "service", "options", "role", "search_path"} {
		if parsed.Query().Has(key) {
			return fmt.Errorf("plugin integration database URL must not override %s", key)
		}
	}
	return nil
}

func verifyPluginTestCluster(ctx context.Context, target string) error {
	connection, err := pgx.Connect(ctx, target)
	if err != nil {
		return fmt.Errorf("connect isolated plugin test database failed")
	}
	defer connection.Close(context.Background())
	var marker string
	var developmentDatabase bool
	if err := connection.QueryRow(ctx, `
SELECT COALESCE(shobj_description(oid, 'pg_database'), ''),
       EXISTS (SELECT 1 FROM pg_database WHERE datname = 'sforum')
FROM pg_database WHERE datname = 'postgres'`).Scan(&marker, &developmentDatabase); err != nil {
		return fmt.Errorf("inspect isolated plugin test cluster failed")
	}
	if marker != pluginTestClusterMarker || developmentDatabase {
		return fmt.Errorf("plugin integration tests require a disposable PostgreSQL cluster without the sforum database; use scripts/test-api-isolated.sh")
	}
	return nil
}

// Each fixture owns its database; fixed plugin IDs never touch the shared seed.
func isolatedPluginTestDatabaseURL(t *testing.T) string {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if base == "" {
		t.Skip("use scripts/test-api-isolated.sh for database plugin integration tests")
	}
	if err := validatePluginTestDatabaseURL(base); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(base)
	parsed.Path = "/postgres"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, parsed.String())
	if err != nil {
		t.Fatal("connect isolated fixture administrator failed")
	}
	defer admin.Close(context.Background())
	name := fmt.Sprintf("sforum_test_plugin_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create isolated plugin fixture: %v", err)
	}
	adminURL := parsed.String()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		connection, err := pgx.Connect(cleanupCtx, adminURL)
		if err != nil {
			t.Error("connect isolated fixture cleanup failed")
			return
		}
		defer connection.Close(context.Background())
		if _, err := connection.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop isolated plugin fixture: %v", err)
			return
		}
		owner, err := coreauthority.OwnerRoleName(name)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := connection.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+pgx.Identifier{owner}.Sanitize()); err != nil {
			t.Errorf("drop isolated fixture owner: %v", err)
		}
	})
	parsed.Path = "/" + name
	target := parsed.String()
	if err := migrator.Up(ctx, migrator.Config{DatabaseURL: target}); err != nil {
		t.Fatalf("migrate isolated plugin fixture: %v", err)
	}
	fixture, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal("connect isolated plugin fixture failed")
	}
	defer fixture.Close(context.Background())
	if _, err := fixture.Exec(ctx, `INSERT INTO users (username, username_lower, email, email_lower, display_name)
VALUES ('fixture_actor', 'fixture_actor', 'fixture@example.test', 'fixture@example.test', 'Fixture Actor')`); err != nil {
		t.Fatalf("seed isolated plugin actor: %v", err)
	}
	t.Logf("isolated plugin fixture database: %s", name)
	return target
}

func TestPluginIntegrationDatabaseURLGuard(t *testing.T) {
	for _, test := range []struct {
		name, target string
		valid        bool
	}{
		{"explicit test database", "postgres://localhost/sforum_test_run", true},
		{"development database", "postgres://localhost/sforum", false},
		{"empty database suffix", "postgres://localhost/sforum_test_", false},
		{"database override", "postgres://localhost/sforum_test_run?dbname=sforum", false},
		{"connection override", "postgres://localhost/sforum_test_run?host=other", false},
		{"session override", "postgres://localhost/sforum_test_run?options=-csearch_path%3Dpublic", false},
		{"keyword connection", "dbname=sforum", false},
		{"malformed URL", "postgres://%zz/sforum_test_run", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validatePluginTestDatabaseURL(test.target) == nil; got != test.valid {
				t.Fatalf("URL validation result=%v, want %v", got, test.valid)
			}
		})
	}
}
