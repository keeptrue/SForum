package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresStoreStaticUpgradeStagesWithoutChangingActiveVersion(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	id := fmt.Sprintf("test.staged-version.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, id)
	})
	store := NewPostgresStore(pool)

	v1 := stagedVersionTestManifest(id, "1.0.0")
	first, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: v1, PackagePath: "/tmp/staged-v1", PackageDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("save initial version: %v", err)
	}
	if first.Version != "1.0.0" || first.ActiveVersionID == 0 || first.StagedVersion != nil || first.Status != StatusInstalled {
		t.Fatalf("unexpected initial extension: %#v", first)
	}
	activeVersionID := first.ActiveVersionID
	if _, err := store.Enable(ctx, id, TypePlugin); err != nil {
		t.Fatalf("enable initial version: %v", err)
	}

	v2 := stagedVersionTestManifest(id, "2.0.0")
	staged, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: v2, PackagePath: "/tmp/staged-v2", PackageDigest: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("stage upgrade: %v", err)
	}
	assertStaticUpgradeStaged(t, staged, activeVersionID)

	replayed, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: v2, PackagePath: "/tmp/staged-v2-replay", PackageDigest: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("replay staged upgrade: %v", err)
	}
	assertStaticUpgradeStaged(t, replayed, activeVersionID)
	if replayed.StagedVersion.ID != staged.StagedVersion.ID {
		t.Fatalf("replay allocated another immutable version: first=%d replay=%d", staged.StagedVersion.ID, replayed.StagedVersion.ID)
	}

	var versionCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_versions WHERE extension_id = $1
	`, id).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 2 {
		t.Fatalf("version count=%d, want 2", versionCount)
	}

	typeChange := stagedVersionTestManifest(id, "3.0.0")
	typeChange.Type = TypeTheme
	if _, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: typeChange, PackagePath: "/tmp/staged-v3", PackageDigest: strings.Repeat("c", 64),
	}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("extension type change error=%v, want ErrInvalidManifest", err)
	}
}

func TestPostgresStorePackagePathReferencedIncludesHistoricalVersions(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	id := fmt.Sprintf("test.package-reference.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, id)
	})
	store := NewPostgresStore(pool)
	paths := []string{"/tmp/package-reference-v1", "/tmp/package-reference-v2", "/tmp/package-reference-v3"}
	digests := []string{strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64)}
	for index := range paths {
		if _, err := store.SaveInstalled(ctx, SaveInstalledInput{
			Manifest:      stagedVersionTestManifest(id, fmt.Sprintf("%d.0.0", index+1)),
			PackagePath:   paths[index],
			PackageDigest: digests[index],
		}); err != nil {
			t.Fatalf("save version %d: %v", index+1, err)
		}
	}

	// v2 is no longer active or staged after v3 replaces the staged pointer,
	// but its immutable version row still owns the package bytes.
	referenced, err := store.PackagePathReferenced(ctx, paths[1])
	if err != nil {
		t.Fatalf("check historical package reference: %v", err)
	}
	if !referenced {
		t.Fatal("historical extension_versions row was not treated as a package owner")
	}
	referenced, err = store.PackagePathReferenced(ctx, "/tmp/package-reference-missing")
	if err != nil {
		t.Fatalf("check missing package reference: %v", err)
	}
	if referenced {
		t.Fatal("unknown package path was reported as referenced")
	}

	cleanupPath := fmt.Sprintf("/tmp/package-reference-cleanup-%d", time.Now().UnixNano())
	targetPath := cleanupPath + "-target"
	operationID, cleanupID := insertPhysicalLifecyclePackageReference(t, ctx, pool, id, cleanupPath, targetPath)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_cleanup_records WHERE id = $1`, cleanupID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, operationID)
	})
	for _, path := range []string{cleanupPath, targetPath} {
		referenced, err = store.PackagePathReferenced(ctx, path)
		if err != nil {
			t.Fatalf("check physical lifecycle package reference %q: %v", path, err)
		}
		if !referenced {
			t.Fatalf("physical lifecycle package reference %q was not retained", path)
		}
	}

	var operationCompletedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT completed_at
		FROM extension_lifecycle_operations
		WHERE id = $1
	`, operationID).Scan(&operationCompletedAt); err != nil {
		t.Fatalf("load cleanup operation completion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extension_lifecycle_cleanup_records
		SET status = 'finalized',
		    physical_identity_present = FALSE,
		    physical_package_present = FALSE,
		    physical_runtime_recovery_present = FALSE,
		    finalized_at = statement_timestamp(),
		    finalized_operation_revision = 1,
		    finalized_operation_completed_at = $2,
		    purge_receipt_id = $3,
		    purge_proof = '{"kind":"test"}'::jsonb,
		    purge_proof_digest = $4,
		    revision = revision + 1,
		    updated_at = statement_timestamp()
		WHERE id = $1
	`, cleanupID, operationCompletedAt, fmt.Sprintf("test-receipt-%d", cleanupID), strings.Repeat("9", 64)); err != nil {
		t.Fatalf("finalize lifecycle package reference: %v", err)
	}
	for _, path := range []string{cleanupPath, targetPath} {
		referenced, err = store.PackagePathReferenced(ctx, path)
		if err != nil {
			t.Fatalf("check finalized lifecycle package reference %q: %v", path, err)
		}
		if referenced {
			t.Fatalf("finalized lifecycle package reference %q still claimed physical bytes", path)
		}
	}
}

func insertPhysicalLifecyclePackageReference(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID string,
	retainedPath string,
	targetPath string,
) (int64, int64) {
	t.Helper()
	requestFingerprint := strings.Repeat("8", 64)
	packageDigest := strings.Repeat("7", 64)
	var operationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest,
			operation, state, plan_version, idempotency_key, request_fingerprint,
			authority_type, removal_mode, terminal_result, started_at, completed_at
		)
		VALUES (
			$1, '9.0.0', $2,
			'uninstall', 'enabled', 'v3-test', $3, $4,
			'builtin', 'complete_removal', 'succeeded', statement_timestamp(), statement_timestamp()
		)
		RETURNING id
	`, extensionID, packageDigest, "package-reference-"+extensionID, requestFingerprint).Scan(&operationID); err != nil {
		t.Fatalf("insert lifecycle package operation: %v", err)
	}

	var cleanupID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_cleanup_records (
			cleanup_id, operation_id, operation, step_id, position,
			first_attempt, last_attempt, cleanup_mode, record_kind, status,
			retained_extension_id, retained_extension_version, retained_package_digest,
			retained_version_id, retained_runtime_instance_id, retained_package_path,
			identity_snapshot, package_snapshot, runtime_recovery_snapshot,
			target_extension_id, target_extension_version, target_package_digest,
			target_version_id, target_runtime_instance_id, target_package_path
		)
		VALUES (
			$1, $2, 'uninstall', 'uninstall.after', 0,
			1, 1, 'uninstall_complete_removal', 'uninstall_tombstone', 'pending',
			$3, '9.0.0', $4,
			9001, 'runtime-retained', $5,
			'{"kind":"identity"}'::jsonb, '{"kind":"package"}'::jsonb, '{"kind":"runtime"}'::jsonb,
			$3, '9.0.0', $4,
			9002, 'runtime-target', $6
		)
		RETURNING id
	`, "cleanup-"+extensionID, operationID, extensionID, packageDigest, retainedPath, targetPath).Scan(&cleanupID); err != nil {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_lifecycle_operations WHERE id = $1`, operationID)
		t.Fatalf("insert physical lifecycle package reference: %v", err)
	}
	return operationID, cleanupID
}

// 摘要相同即同一件制品：再次保存只允许把 package_path 修到本次校验过的路径。
// 少了这条，目录重建或 EXTENSION_ROOT 变化后行会永久判 missing（artifactState），
// 而 exact-artifact 语义不允许为此换 digest。
func TestPostgresStoreSaveInstalledRepairsRelocatedPackagePath(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	id := fmt.Sprintf("test.package-path-repair.%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id = $1`, id)
	})
	store := NewPostgresStore(pool)
	manifest := stagedVersionTestManifest(id, "1.0.0")
	digest := strings.Repeat("a", 64)

	first, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: manifest, PackagePath: "/tmp/repair-old-path", PackageDigest: digest,
	})
	if err != nil {
		t.Fatalf("save initial version: %v", err)
	}
	repaired, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: manifest, PackagePath: "/tmp/repair-new-path", PackageDigest: digest,
	})
	if err != nil {
		t.Fatalf("repair save: %v", err)
	}
	if repaired.PackagePath != "/tmp/repair-new-path" {
		t.Fatalf("package path was not repaired: %q", repaired.PackagePath)
	}
	if repaired.ActiveVersionID != first.ActiveVersionID || repaired.PackageDigest != digest || repaired.Version != "1.0.0" {
		t.Fatalf("repair changed artifact identity: before=%#v after=%#v", first, repaired)
	}

	var versionCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM extension_versions WHERE extension_id = $1
	`, id).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 {
		t.Fatalf("version count=%d, want 1 (repair must not allocate a new immutable version)", versionCount)
	}

	// 路径已一致时重放不产生额外写入，也不改身份。
	replayed, err := store.SaveInstalled(ctx, SaveInstalledInput{
		Manifest: manifest, PackagePath: "/tmp/repair-new-path", PackageDigest: digest,
	})
	if err != nil {
		t.Fatalf("replay save: %v", err)
	}
	if replayed.ActiveVersionID != first.ActiveVersionID || replayed.PackagePath != "/tmp/repair-new-path" {
		t.Fatalf("replay changed the recorded artifact: %#v", replayed)
	}
}

func stagedVersionTestManifest(id, version string) Manifest {
	return Manifest{ID: id, Name: "Staged Version Test", Version: version, Type: TypePlugin}
}

func assertStaticUpgradeStaged(t *testing.T, extension Extension, activeVersionID int64) {
	t.Helper()
	if extension.Version != "1.0.0" || extension.PackageDigest != strings.Repeat("a", 64) {
		t.Fatalf("static upload changed active artifact: %#v", extension)
	}
	if extension.ActiveVersionID != activeVersionID || extension.Status != StatusEnabled {
		t.Fatalf("static upload changed active identity/status: %#v", extension)
	}
	if extension.StagedVersion == nil || extension.StagedVersion.Version != "2.0.0" ||
		extension.StagedVersion.PackageDigest != strings.Repeat("b", 64) {
		t.Fatalf("candidate was not staged: %#v", extension.StagedVersion)
	}
}
