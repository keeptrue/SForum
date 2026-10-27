package extensions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresStorePromoteStagedVersionExactCAS(t *testing.T) {
	fixture := newStagedCASTestFixture(t, "promote")
	input := fixture.input()

	wrongDigest := input
	wrongDigest.ExpectedPackageDigest = strings.Repeat("c", 64)
	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, wrongDigest); !errors.Is(err, ErrStagedVersionConflict) {
		t.Fatalf("wrong digest error=%v, want conflict", err)
	}
	assertStagedCASState(t, fixture, fixture.activeID, fixture.activeDigest, fixture.candidateID)

	wrongActive := input
	wrongActive.ExpectedActivePackageDigest = strings.Repeat("c", 64)
	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, wrongActive); !errors.Is(err, ErrStagedVersionConflict) {
		t.Fatalf("wrong active digest error=%v, want conflict", err)
	}
	assertStagedCASState(t, fixture, fixture.activeID, fixture.activeDigest, fixture.candidateID)

	foreign := newStagedCASTestFixture(t, "foreign")
	foreignInput := input
	foreignInput.ExpectedStagedVersionID = foreign.candidateID
	foreignInput.ExpectedPackageDigest = foreign.candidateDigest
	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, foreignInput); !errors.Is(err, ErrStagedVersionConflict) {
		t.Fatalf("foreign candidate error=%v, want conflict", err)
	}
	assertStagedCASState(t, fixture, fixture.activeID, fixture.activeDigest, fixture.candidateID)

	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, StagedVersionCASInput{}); !errors.Is(err, ErrStagedVersionInvalid) {
		t.Fatalf("empty identity error=%v, want invalid", err)
	}
	invalidDigest := input
	invalidDigest.ExpectedPackageDigest = strings.Repeat("B", 64)
	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, invalidDigest); !errors.Is(err, ErrStagedVersionInvalid) {
		t.Fatalf("non-canonical digest error=%v, want invalid", err)
	}

	promoted, err := fixture.store.PromoteStagedVersion(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.ActiveVersionID != fixture.candidateID || promoted.PackageDigest != fixture.candidateDigest ||
		promoted.Version != "2.0.0" || promoted.StagedVersion != nil || promoted.Status != StatusEnabled {
		t.Fatalf("unexpected promoted extension: %#v", promoted)
	}
	assertImmutableVersionRetained(t, fixture, fixture.activeID)
	assertImmutableVersionRetained(t, fixture, fixture.candidateID)

	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, input); !errors.Is(err, ErrStagedVersionConflict) {
		t.Fatalf("promotion replay error=%v, want identity conflict", err)
	}
	assertStagedCASState(t, fixture, fixture.candidateID, fixture.candidateDigest, 0)
	assertStagedCASPackagesRetained(t, fixture)
}

func TestPostgresStoreStagedVersionCASConcurrentReplay(t *testing.T) {
	for _, operation := range []string{"promote", "discard"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newStagedCASTestFixture(t, "concurrent-"+operation)
			input := fixture.input()
			start := make(chan struct{})
			results := make(chan error, 2)
			for range 2 {
				go func() {
					<-start
					var err error
					if operation == "promote" {
						_, err = fixture.store.PromoteStagedVersion(fixture.ctx, input)
					} else {
						_, err = fixture.store.DiscardStagedVersion(fixture.ctx, input)
					}
					results <- err
				}()
			}
			close(start)
			var succeeded, rejected int
			for range 2 {
				err := <-results
				switch {
				case err == nil:
					succeeded++
				case errors.Is(err, ErrStagedVersionNotFound), errors.Is(err, ErrStagedVersionConflict):
					rejected++
				default:
					t.Fatalf("concurrent %s error=%v", operation, err)
				}
			}
			if succeeded != 1 || rejected != 1 {
				t.Fatalf("concurrent %s succeeded=%d rejected=%d", operation, succeeded, rejected)
			}
			if operation == "promote" {
				assertStagedCASState(t, fixture, fixture.candidateID, fixture.candidateDigest, 0)
			} else {
				assertStagedCASState(t, fixture, fixture.activeID, fixture.activeDigest, 0)
			}
			assertImmutableVersionRetained(t, fixture, fixture.candidateID)
			assertStagedCASPackagesRetained(t, fixture)
		})
	}
}

func TestPostgresStoreStagedVersionCASRollsBackWriteFailure(t *testing.T) {
	fixture := newStagedCASTestFixture(t, "rollback")
	functionName := fmt.Sprintf("test_staged_cas_fail_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	quotedFunction := pgx.Identifier{functionName}.Sanitize()
	quotedTrigger := pgx.Identifier{triggerName}.Sanitize()
	if _, err := fixture.pool.Exec(fixture.ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  RAISE EXCEPTION 'forced staged CAS rollback test';
		END
		$$
	`, quotedFunction)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, fmt.Sprintf(`
		CREATE TRIGGER %s
		BEFORE UPDATE OF active_version_id, staged_version_id ON extensions
		FOR EACH ROW WHEN (OLD.id = '%s')
		EXECUTE FUNCTION %s()
	`, quotedTrigger, strings.ReplaceAll(fixture.id, "'", "''"), quotedFunction)); err != nil {
		_, _ = fixture.pool.Exec(fixture.ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFunction))
		t.Fatal(err)
	}
	dropFailureTrigger := func() {
		_, _ = fixture.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON extensions`, quotedTrigger))
		_, _ = fixture.pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFunction))
	}
	t.Cleanup(dropFailureTrigger)

	input := fixture.input()
	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, input); err == nil {
		t.Fatal("forced promotion failure unexpectedly succeeded")
	}
	assertStagedCASState(t, fixture, fixture.activeID, fixture.activeDigest, fixture.candidateID)
	if _, err := fixture.store.DiscardStagedVersion(fixture.ctx, input); err == nil {
		t.Fatal("forced discard failure unexpectedly succeeded")
	}
	assertStagedCASState(t, fixture, fixture.activeID, fixture.activeDigest, fixture.candidateID)

	dropFailureTrigger()
	discarded, err := fixture.store.DiscardStagedVersion(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if discarded.ActiveVersionID != fixture.activeID || discarded.PackageDigest != fixture.activeDigest || discarded.StagedVersion != nil {
		t.Fatalf("unexpected discard result: %#v", discarded)
	}
	assertImmutableVersionRetained(t, fixture, fixture.candidateID)
	if _, err := fixture.store.DiscardStagedVersion(fixture.ctx, input); !errors.Is(err, ErrStagedVersionNotFound) {
		t.Fatalf("discard replay error=%v, want staged version not found", err)
	}
}

type stagedCASTestFixture struct {
	ctx             context.Context
	pool            *pgxpool.Pool
	store           *PostgresStore
	id              string
	activeID        int64
	activeDigest    string
	candidateID     int64
	candidateDigest string
	activePath      string
	candidatePath   string
}

func newStagedCASTestFixture(t *testing.T, label string) stagedCASTestFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	id := fmt.Sprintf("test.staged-cas.%s.%d", label, time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, id)
	})
	store := NewPostgresStore(pool)
	packageRoot := t.TempDir()
	activePath := filepath.Join(packageRoot, "v1")
	candidatePath := filepath.Join(packageRoot, "v2")
	for _, path := range []string{activePath, candidatePath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "artifact.marker"), []byte(path), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	activeDigest := strings.Repeat("a", 64)
	active, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest:    stagedVersionTestManifest(id, "1.0.0"),
		PackagePath: activePath, PackageDigest: activeDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enable(ctx, id, TypePlugin); err != nil {
		t.Fatal(err)
	}
	candidateDigest := strings.Repeat("b", 64)
	staged, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest:    stagedVersionTestManifest(id, "2.0.0"),
		PackagePath: candidatePath, PackageDigest: candidateDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if staged.StagedVersion == nil {
		t.Fatal("fixture candidate was not staged")
	}
	return stagedCASTestFixture{
		ctx: ctx, pool: pool, store: store, id: id,
		activeID: active.ActiveVersionID, activeDigest: activeDigest,
		candidateID: staged.StagedVersion.ID, candidateDigest: candidateDigest,
		activePath: activePath, candidatePath: candidatePath,
	}
}

func (f stagedCASTestFixture) input() StagedVersionCASInput {
	return StagedVersionCASInput{
		ExtensionID:             f.id,
		ExpectedActiveVersionID: f.activeID, ExpectedActiveVersion: "1.0.0",
		ExpectedActivePackageDigest: f.activeDigest,
		ExpectedStagedVersionID:     f.candidateID, ExpectedStagedVersion: "2.0.0",
		ExpectedPackageDigest: f.candidateDigest,
	}
}

func assertStagedCASState(
	t *testing.T,
	fixture stagedCASTestFixture,
	activeVersionID int64,
	activeDigest string,
	stagedVersionID int64,
) {
	t.Helper()
	item, err := fixture.store.Get(fixture.ctx, fixture.id)
	if err != nil {
		t.Fatal(err)
	}
	if item.ActiveVersionID != activeVersionID || item.PackageDigest != activeDigest {
		t.Fatalf("active version changed: %#v", item)
	}
	if stagedVersionID == 0 {
		if item.StagedVersion != nil {
			t.Fatalf("unexpected staged version: %#v", item.StagedVersion)
		}
		return
	}
	if item.StagedVersion == nil || item.StagedVersion.ID != stagedVersionID {
		t.Fatalf("staged version changed: %#v", item.StagedVersion)
	}
}

func assertImmutableVersionRetained(t *testing.T, fixture stagedCASTestFixture, versionID int64) {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*)
		FROM extension_versions
		WHERE id = $1 AND extension_id = $2
	`, versionID, fixture.id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("immutable version %d retained rows=%d", versionID, count)
	}
}

func assertStagedCASPackagesRetained(t *testing.T, fixture stagedCASTestFixture) {
	t.Helper()
	for _, path := range []string{fixture.activePath, fixture.candidatePath} {
		if _, err := os.Stat(filepath.Join(path, "artifact.marker")); err != nil {
			t.Fatalf("artifact package %q was not retained: %v", path, err)
		}
	}
}
