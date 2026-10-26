package migrator

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestOnlineSafePendingMigrationsAcceptsDeclaredSQL(t *testing.T) {
	files := fstest.MapFS{
		"00001_safe.sql":    {Data: []byte("-- +goose Up\n-- +sforum OnlineSafe\nSET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\nSELECT 1;\n-- +goose Down\n")},
		"00002_applied.sql": {Data: []byte("-- +goose Up\nSELECT 2;\n-- +goose Down\n")},
	}
	statuses := []*goose.MigrationStatus{
		migrationStatus("00001_safe.sql", goose.StatePending),
		migrationStatus("00002_applied.sql", goose.StateApplied),
	}

	pending, err := onlineSafePendingMigrations(files, statuses)
	if err != nil {
		t.Fatalf("classify online-safe migrations: %v", err)
	}
	if len(pending) != 1 || pending[0] != "00001_safe.sql" {
		t.Fatalf("unexpected pending migrations: %#v", pending)
	}
}

func TestOnlineSafePendingMigrationsRejectsUndeclaredSQL(t *testing.T) {
	files := fstest.MapFS{
		"00001_unsafe.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n")},
	}

	_, err := onlineSafePendingMigrations(files, []*goose.MigrationStatus{
		migrationStatus("00001_unsafe.sql", goose.StatePending),
	})
	if !errors.Is(err, ErrOnlineMigrationUnsafe) {
		t.Fatalf("expected unsafe migration error, got %v", err)
	}
}

func TestMigrationDeclaresOnlineSafeIgnoresDownDirective(t *testing.T) {
	files := fstest.MapFS{
		"00001_unsafe.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n-- +sforum OnlineSafe\n")},
	}

	declared, err := migrationDeclaresOnlineSafe(files, "00001_unsafe.sql")
	if err != nil {
		t.Fatalf("read directive: %v", err)
	}
	if declared {
		t.Fatal("a directive in the Down section must not authorize online migration")
	}
}

func TestOnlineSafePendingMigrationsRequiresTimeouts(t *testing.T) {
	files := fstest.MapFS{
		"00001_unbounded.sql": {Data: []byte("-- +goose Up\n-- +sforum OnlineSafe\nSELECT 1;\n-- +goose Down\n")},
	}

	_, err := onlineSafePendingMigrations(files, []*goose.MigrationStatus{
		migrationStatus("00001_unbounded.sql", goose.StatePending),
	})
	if !errors.Is(err, ErrOnlineMigrationUnsafe) {
		t.Fatalf("expected unbounded migration to be rejected, got %v", err)
	}
}

func TestEmbeddedOnlineSafeDeclarationsAreBounded(t *testing.T) {
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	declared := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, readErr := fs.ReadFile(migrations.Files(), entry.Name())
		if readErr != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), readErr)
		}
		if !strings.Contains(string(body), onlineSafeDirective) {
			continue
		}
		declared++
		safe, declarationErr := migrationDeclaresOnlineSafe(migrations.Files(), entry.Name())
		if declarationErr != nil {
			t.Fatalf("migration %s has an invalid online declaration: %v", entry.Name(), declarationErr)
		}
		if !safe {
			t.Fatalf("migration %s has an unrecognized online declaration", entry.Name())
		}
	}
	if declared == 0 {
		t.Fatal("expected at least one embedded online-safe migration")
	}
}

func migrationStatus(path string, state goose.State) *goose.MigrationStatus {
	return &goose.MigrationStatus{
		Source: &goose.Source{Type: goose.TypeSQL, Path: path},
		State:  state,
	}
}
