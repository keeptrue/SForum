package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/internal/testdb/guard"
)

const (
	// EnvFullTemplateDatabase names the database cloned after the isolated
	// runner has applied the complete Core and River migration chain once.
	EnvFullTemplateDatabase = "SFORUM_TEST_FULL_TEMPLATE_DATABASE"

	defaultFullTemplateDatabase = "sforum_test_template"
)

// TemplatePrepareFunc builds a package-specific database template exactly once.
// It must close every connection it opens before returning.
type TemplatePrepareFunc func(context.Context, string) error

// DatabaseTemplate lazily prepares one database template and clones it for
// tests. A template is created only from the isolated runner's empty template;
// initialization failures are sticky and fail every caller closed.
type DatabaseTemplate struct {
	name    string
	prepare TemplatePrepareFunc

	once   sync.Once
	err    error
	url    string
	schema string
}

// NewTemplate returns a package-owned template. The name only contributes to
// the generated database identifier; it never selects an existing database.
func NewTemplate(name string, prepare TemplatePrepareFunc) *DatabaseTemplate {
	return &DatabaseTemplate{
		name:    strings.TrimSpace(name),
		prepare: prepare,
	}
}

// Clone returns an isolated database cloned from this template. Cleanup drops
// the clone with FORCE after test-owned pools have had a chance to close.
func (tpl *DatabaseTemplate) Clone(t testing.TB, prefix string) string {
	t.Helper()
	if tpl == nil {
		t.Fatal("testdb: nil database template")
	}
	if tpl.prepare == nil {
		t.Fatal("testdb: database template has no prepare function")
	}
	baseURL := IsolatedURL(t)
	tpl.once.Do(func() {
		tpl.err = tpl.initialize(baseURL)
	})
	if tpl.err != nil {
		t.Fatalf("testdb: prepare template %q: %v", tpl.name, tpl.err)
	}
	return cloneTemplateDatabase(t, baseURL, tpl.url, prefix, tpl.schema)
}

// MigratedDatabaseURL returns a fresh database cloned from the fully migrated
// runner template.
func MigratedDatabaseURL(t testing.TB, prefix string) string {
	t.Helper()
	baseURL := IsolatedURL(t)
	template := envTemplateName(EnvFullTemplateDatabase, defaultFullTemplateDatabase)
	return cloneTemplateDatabase(t, baseURL, template, prefix, "")
}

func (tpl *DatabaseTemplate) initialize(baseURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fullTemplate := envTemplateName(EnvFullTemplateDatabase, defaultFullTemplateDatabase)
	admin, err := pgx.Connect(ctx, databaseURLWithName(baseURL, "postgres"))
	if err != nil {
		return fmt.Errorf("connect to disposable cluster: %w", err)
	}
	defer admin.Close(context.Background())

	if err := requireTemplateDatabase(ctx, admin, fullTemplate); err != nil {
		return err
	}
	name := generatedDatabaseName("sforum_tpl_" + tpl.name)
	if err := createDatabaseFromTemplate(ctx, admin, name, fullTemplate); err != nil {
		return err
	}
	templateURL := databaseURLWithName(baseURL, name)

	prepared := false
	defer func() {
		if prepared {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupCtx,
			"DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	}()

	schema := generatedDatabaseName("fixture_" + tpl.name)
	templateDB, err := pgx.Connect(ctx, templateURL)
	if err != nil {
		return fmt.Errorf("connect to prepared database %s: %w", name, err)
	}
	if _, err := templateDB.Exec(ctx,
		"CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize(),
	); err != nil {
		_ = templateDB.Close(context.Background())
		return fmt.Errorf("create fixture schema %s: %w", schema, err)
	}
	if err := templateDB.Close(ctx); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx,
		"ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+
			" SET search_path TO "+pgx.Identifier{schema}.Sanitize()+", public",
	); err != nil {
		return fmt.Errorf("set template search_path %s, public: %w", schema, err)
	}

	if err := tpl.prepare(ctx, templateURL); err != nil {
		return fmt.Errorf("prepare database %s: %w", name, err)
	}
	if _, err := admin.Exec(ctx,
		"ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+" IS_TEMPLATE true",
	); err != nil {
		return fmt.Errorf("mark database %s as template: %w", name, err)
	}
	prepared = true
	tpl.url = name
	tpl.schema = schema
	return nil
}

func cloneTemplateDatabase(t testing.TB, baseURL, template, prefix, schema string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, databaseURLWithName(baseURL, "postgres"))
	if err != nil {
		t.Fatalf("testdb: connect to disposable cluster: %v", err)
	}
	if err := requireTemplateDatabase(ctx, admin, template); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("testdb: %v", err)
	}

	name := generatedDatabaseName("sforum_test_" + strings.TrimSpace(prefix))
	if err := createDatabaseFromTemplate(ctx, admin, name, template); err != nil {
		_ = admin.Close(context.Background())
		t.Fatalf("testdb: clone template %s: %v", template, err)
	}
	if schema != "" {
		if _, err := admin.Exec(ctx,
			"ALTER DATABASE "+pgx.Identifier{name}.Sanitize()+
				" SET search_path TO "+pgx.Identifier{schema}.Sanitize()+", public",
		); err != nil {
			_, _ = admin.Exec(context.Background(),
				"DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			_ = admin.Close(context.Background())
			t.Fatalf("testdb: set clone search_path %s, public: %v", schema, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx,
			"DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); err != nil {
			t.Errorf("testdb: drop cloned database %s: %v", name, err)
		}
		_ = admin.Close(context.Background())
	})

	cloneURL := databaseURLWithName(baseURL, name)
	ValidatedURL(t, cloneURL)
	return cloneURL
}

func requireTemplateDatabase(ctx context.Context, admin *pgx.Conn, name string) error {
	var exists bool
	if err := admin.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`,
		name,
	).Scan(&exists); err != nil {
		return fmt.Errorf("inspect template %s: %w", name, err)
	}
	if !exists {
		return fmt.Errorf(
			"template database %q is missing; run through ./scripts/test-api-isolated.sh",
			name,
		)
	}
	return nil
}

func createDatabaseFromTemplate(ctx context.Context, admin *pgx.Conn, name, template string) error {
	statement := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize() +
		" TEMPLATE " + pgx.Identifier{template}.Sanitize()
	if _, err := admin.Exec(ctx, statement); err != nil {
		return err
	}
	return nil
}

func envTemplateName(envName, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	return fallback
}

func databaseURLWithName(baseURL, name string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	parsed.Path = "/" + name
	return parsed.String()
}

func generatedDatabaseName(prefix string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_':
			return r
		default:
			return '_'
		}
	}, prefix)
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		cleaned = "database"
	}
	if len(cleaned) > 40 {
		cleaned = cleaned[:40]
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("%s_%d", cleaned, time.Now().UnixNano())
	}
	return cleaned + "_" + hex.EncodeToString(suffix[:])
}

// RedactedTemplateURL is useful in diagnostics without exposing credentials.
func RedactedTemplateURL(raw string) string {
	return guard.Redact(raw)
}
