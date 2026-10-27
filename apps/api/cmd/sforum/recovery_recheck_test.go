package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const recheckDigest = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

// seedRecheckExtension 写入一条启用的扩展与其精确 active 制品。
func seedRecheckExtension(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) extensions.PluginRuntimeMember {
	t.Helper()
	// extensions.active_version_id 是后加的外键，先写扩展行再写版本行。
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system)
		VALUES ($1, 'plugin', $1, 'enabled', 'uploaded', false)
	`, id); err != nil {
		t.Fatalf("seed extension: %v", err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '1.0.0', '{}'::jsonb, 'packages/recheck', $2)
		RETURNING id
	`, id, recheckDigest).Scan(&versionID); err != nil {
		t.Fatalf("seed extension version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, id, versionID); err != nil {
		t.Fatalf("bind active version: %v", err)
	}
	return extensions.PluginRuntimeMember{
		ExtensionID:        id,
		ExtensionVersionID: versionID,
		ExtensionVersion:   "1.0.0",
		PackageDigest:      recheckDigest,
	}
}

// seedRecheckPublication 直接写入一条不可变 full-set（fixture 用，绕过 lifecycle journal）。
// 必须在单事务内写完 header + members：full-set 一致性是 DEFERRABLE 约束触发器，
// 只在提交时校验，逐条自动提交会让 header 单独提交成"计数不符"。
func seedRecheckPublication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, members []extensions.PluginRuntimeMember) int64 {
	t.Helper()
	digest, err := extensions.PluginRuntimeMembersDigest(members)
	if err != nil {
		t.Fatalf("members digest: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var revision int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason, actor_user_id)
		VALUES ($1, $2, 'startup_reconcile', NULL)
		RETURNING revision
	`, len(members), digest).Scan(&revision); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	for _, member := range members {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_runtime_publication_members (
				publication_revision, extension_id, extension_version_id,
				extension_version, package_digest
			) VALUES ($1, $2, $3, $4, $5)
		`, revision, member.ExtensionID, member.ExtensionVersionID,
			member.ExtensionVersion, member.PackageDigest); err != nil {
			t.Fatalf("seed publication member: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seeded publication: %v", err)
	}
	return revision
}

func TestRecoveryRecheckPublishesRecoveryRevisionForExactDesiredSet(t *testing.T) {
	fixture := newRecoveryPostgresFixture(t)
	ctx := fixture.ctx
	member := seedRecheckExtension(t, ctx, fixture.pool, "recovery.recheck.exact")
	genesis := seedRecheckPublication(t, ctx, fixture.pool, []extensions.PluginRuntimeMember{member})

	result, err := recheckPluginRuntimeRecovery(ctx, fixture.pool, "operator confirmed the plugin artifacts")
	if err != nil {
		t.Fatalf("recheck exact desired set: %v", err)
	}
	if result.Revision <= genesis {
		t.Fatalf("recheck must advance the immutable revision: genesis=%d result=%d", genesis, result.Revision)
	}
	if result.MemberCount != 1 || result.Reason != string(extensions.PluginRuntimePublicationRecovery) {
		t.Fatalf("unexpected recovery publication: %#v", result)
	}
	stored, err := extensions.NewPostgresStore(fixture.pool).
		PluginRuntimePublicationByRevision(ctx, result.Revision)
	if err != nil {
		t.Fatalf("load published recovery revision: %v", err)
	}
	if stored.Reason != extensions.PluginRuntimePublicationRecovery || len(stored.Members) != 1 ||
		stored.Members[0].ExtensionID != member.ExtensionID {
		t.Fatalf("published recovery revision is not exact: %#v", stored)
	}
	var audits int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'extension.cli_recovery'
		  AND metadata->>'mode' = 'runtime_recheck'
		  AND (metadata->>'revision')::bigint = $1
	`, result.Revision).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("recovery recheck audit rows = %d, want 1", audits)
	}
}

func TestRecoveryRecheckRefusesArtifactDriftWithoutWriting(t *testing.T) {
	fixture := newRecoveryPostgresFixture(t)
	ctx := fixture.ctx
	member := seedRecheckExtension(t, ctx, fixture.pool, "recovery.recheck.drift")
	genesis := seedRecheckPublication(t, ctx, fixture.pool, []extensions.PluginRuntimeMember{member})

	// durable 证据漂移：active 制品换成另一个版本，publication 仍指向旧版本。
	var driftedVersionID int64
	if err := fixture.pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '2.0.0', '{}'::jsonb, 'packages/recheck', $2)
		RETURNING id
	`, member.ExtensionID, strings.Repeat("b", 64)).Scan(&driftedVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, member.ExtensionID, driftedVersionID); err != nil {
		t.Fatal(err)
	}

	_, err := recheckPluginRuntimeRecovery(ctx, fixture.pool, "operator recheck after drift")
	if !errors.Is(err, extensions.ErrPluginRuntimeRecoveryRecheckMismatch) {
		t.Fatalf("recheck drift error = %v, want ErrPluginRuntimeRecoveryRecheckMismatch", err)
	}
	var mismatch *recoveryRecheckMismatch
	if !asRecoveryRecheckMismatch(err, &mismatch) {
		t.Fatalf("drift must carry the artifact list: %v", err)
	}
	if len(mismatch.Artifacts) == 0 {
		t.Fatal("drift must list every offending artifact")
	}
	var revisions int
	if err := fixture.pool.QueryRow(ctx, `
		SELECT count(*) FROM plugin_runtime_publications WHERE revision > $1
	`, genesis).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 {
		t.Fatalf("recheck wrote %d revision(s) on drift; it must write none", revisions)
	}
}

func TestRecoveryRecheckReasonValidation(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t"} {
		if _, err := validateRecoveryRecheckReason(raw); err == nil {
			t.Fatalf("reason %q must be rejected", raw)
		}
	}
	if _, err := validateRecoveryRecheckReason(strings.Repeat("x", recoveryRecheckReasonLimit+1)); err == nil {
		t.Fatal("oversized reason must be rejected")
	}
	reason, err := validateRecoveryRecheckReason("  operator confirmed convergence  ")
	if err != nil || reason != "operator confirmed convergence" {
		t.Fatalf("valid reason = %q, %v", reason, err)
	}
}

// 同一个测试进程只编译一次 CLI：R8 断言的是进程退出码，但没必要每个用例
// 都重新链接一遍。
var (
	sforumCLIOnce sync.Once
	sforumCLIPath string
	sforumCLIErr  error
)

// buildSforumCLI 真的编译一个 CLI 二进制：R8 要求断言进程退出码，
// 只测 RunE 的返回值证明不了 cobra 之外的退出路径。
func buildSforumCLI(t *testing.T) string {
	t.Helper()
	sforumCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sforum-cli-")
		if err != nil {
			sforumCLIErr = err
			return
		}
		binary := filepath.Join(dir, "sforum")
		build := exec.Command("go", "build", "-o", binary, ".")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, buildErr := build.CombinedOutput(); buildErr != nil {
			sforumCLIErr = fmt.Errorf("build sforum CLI: %w\n%s", buildErr, output)
			return
		}
		sforumCLIPath = binary
	})
	if sforumCLIErr != nil {
		t.Fatalf("%v", sforumCLIErr)
	}
	return sforumCLIPath
}

// TestRecoveryRecheckRejectsMissingReasonBeforeConnecting 断言两件事：
// 退出码为 1，且失败原因是参数校验而不是连接失败——非法输入不触库。
func TestRecoveryRecheckRejectsMissingReasonBeforeConnecting(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the sforum CLI")
	}
	binary := buildSforumCLI(t)
	command := exec.Command(binary, "recovery", "recheck",
		"--database-url", "postgres://nobody:nobody@127.0.0.1:1/never?sslmode=disable")
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("missing --reason must exit 1, got %v\n%s", err, output)
	}
	text := string(output)
	if !strings.Contains(text, "--reason is required") {
		t.Fatalf("missing --reason must fail on validation, got:\n%s", text)
	}
	if strings.Contains(text, "connect recovery database") {
		t.Fatalf("validation must happen before connecting, got:\n%s", text)
	}
}

// TestRecoveryActivationStateNoCandidatesExitsZero 覆盖 R8 的另一半：
// 定向修复没有候选时必须 exit 0 并打印 no-op 文案，而不是静默成功。
func TestRecoveryActivationStateNoCandidatesExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the sforum CLI")
	}
	databaseURL := testdb.IsolatedURL(t)
	binary := buildSforumCLI(t)
	command := exec.Command(binary, "recovery", "activation-state", "--database-url", databaseURL)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("activation-state with no candidates must exit 0, got %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "no interrupted activation publication matched") {
		t.Fatalf("no-op must print an explicit message, got:\n%s", output)
	}
}
