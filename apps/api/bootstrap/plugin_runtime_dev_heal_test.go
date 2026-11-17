package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	artifactintegrity "github.com/keeptrue/sforum/apps/api/app/Support/Extensions/ArtifactIntegrity"
	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

// ---- 入口开关（无需数据库） ----

type devHealProbeHealer struct {
	calls   int
	outcome pluginRuntimeDevHealOutcome
	err     error
}

func (p *devHealProbeHealer) HealPluginRuntimePublication(context.Context) (pluginRuntimeDevHealOutcome, error) {
	p.calls++
	return p.outcome, p.err
}

type devHealProbeVerifier struct {
	calls int
	err   error
}

func (p *devHealProbeVerifier) VerifyPluginRuntimePublicationArtifacts(context.Context, int64) error {
	p.calls++
	return p.err
}

func devHealArtifactDefect() error {
	return artifactintegrity.EntryUnavailable(
		"fixture.plugin", "1.0.0", strings.Repeat("a", 64), "backend/plugin", os.ErrNotExist,
	)
}

func devHealTestLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// 生产与 Safe Mode 必须自愈函数**未被调用**：探针 healer/verifier 计数为 0，
// 原始制品缺陷原样上抛（启动继续走既有 recovery-only）。
func TestMaybeHealPluginRuntimePublicationSkipsProductionAndSafeMode(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.Config
	}{
		{name: "production", cfg: config.Config{AppEnv: "production"}},
		{name: "test env", cfg: config.Config{AppEnv: "test"}},
		{name: "safe mode", cfg: config.Config{AppEnv: "development", SafeMode: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			healer := &devHealProbeHealer{}
			verifier := &devHealProbeVerifier{}
			defect := devHealArtifactDefect()
			var logs bytes.Buffer
			err := maybeHealPluginRuntimePublication(
				t.Context(), test.cfg, healer, verifier, devHealTestLogger(&logs), defect,
			)
			if !errors.Is(err, defect) {
				t.Fatalf("verification defect must be preserved, got %v", err)
			}
			if healer.calls != 0 || verifier.calls != 0 {
				t.Fatalf("self-heal must not be called: healer=%d verifier=%d",
					healer.calls, verifier.calls)
			}
		})
	}
}

func TestMaybeHealPluginRuntimePublicationRunsInDevelopment(t *testing.T) {
	healer := &devHealProbeHealer{outcome: pluginRuntimeDevHealOutcome{
		Healed: []pluginRuntimeDevHealedMember{{
			ExtensionID: "fixture.plugin", Version: "1.0.0",
			FromDigest: strings.Repeat("a", 64), ToDigest: strings.Repeat("b", 64), Revision: 9,
		}},
	}}
	verifier := &devHealProbeVerifier{}
	var logs bytes.Buffer
	err := maybeHealPluginRuntimePublication(
		t.Context(), config.Config{AppEnv: "development"}, healer, verifier,
		devHealTestLogger(&logs), devHealArtifactDefect(),
	)
	if err != nil {
		t.Fatalf("healed startup must clear the defect, got %v", err)
	}
	if healer.calls != 1 || verifier.calls != 1 {
		t.Fatalf("dev path must call healer then re-verify: healer=%d verifier=%d",
			healer.calls, verifier.calls)
	}
}

func TestMaybeHealPluginRuntimePublicationKeepsDefectWithoutCandidate(t *testing.T) {
	healer := &devHealProbeHealer{}
	verifier := &devHealProbeVerifier{}
	defect := devHealArtifactDefect()
	var logs bytes.Buffer
	err := maybeHealPluginRuntimePublication(
		t.Context(), config.Config{AppEnv: "development"}, healer, verifier,
		devHealTestLogger(&logs), defect,
	)
	if !errors.Is(err, defect) {
		t.Fatalf("no candidate must stay fail-closed, got %v", err)
	}
	if healer.calls != 1 || verifier.calls != 0 {
		t.Fatalf("healer=%d verifier=%d", healer.calls, verifier.calls)
	}
}

func TestMaybeHealPluginRuntimePublicationKeepsFailedReverification(t *testing.T) {
	healer := &devHealProbeHealer{outcome: pluginRuntimeDevHealOutcome{
		Healed: []pluginRuntimeDevHealedMember{{ExtensionID: "fixture.plugin", Revision: 3}},
	}}
	verifyErr := devHealArtifactDefect()
	verifier := &devHealProbeVerifier{err: verifyErr}
	var logs bytes.Buffer
	err := maybeHealPluginRuntimePublication(
		t.Context(), config.Config{AppEnv: "development"}, healer, verifier,
		devHealTestLogger(&logs), devHealArtifactDefect(),
	)
	if !errors.Is(err, verifyErr) {
		t.Fatalf("post-heal verification failure must be reported, got %v", err)
	}
}

// 非制品族错误（数据库/事务）不是自愈的对象：不能把一次连接故障当成制品缺陷。
func TestMaybeHealPluginRuntimePublicationIgnoresNonArtifactErrors(t *testing.T) {
	healer := &devHealProbeHealer{}
	verifier := &devHealProbeVerifier{}
	boom := errors.New("database unreachable")
	err := maybeHealPluginRuntimePublication(
		t.Context(), config.Config{AppEnv: "development"}, healer, verifier,
		devHealTestLogger(&bytes.Buffer{}), boom,
	)
	if !errors.Is(err, boom) {
		t.Fatalf("non-artifact error must pass through, got %v", err)
	}
	if healer.calls != 0 || verifier.calls != 0 {
		t.Fatalf("healer=%d verifier=%d", healer.calls, verifier.calls)
	}
}

// ---- 集成：真 PG + 真制品 ----

type devHealDevAutoLookup struct{ pool *pgxpool.Pool }

func (l devHealDevAutoLookup) GetCurrentUserByEmail(
	ctx context.Context,
	email string,
) (identity.CurrentUser, error) {
	var userID int64
	if err := l.pool.QueryRow(ctx,
		`SELECT id FROM users WHERE email_lower = lower($1)`, email,
	).Scan(&userID); err != nil {
		return identity.CurrentUser{}, identity.ErrUserNotFound
	}
	return identity.CurrentUser{ID: userID}, nil
}

type devHealPostgresFixture struct {
	ctx             context.Context
	pool            *pgxpool.Pool
	store           *extensions.PostgresStore
	artifactsRoot   string
	extensionID     string
	brokenVersionID int64
	brokenMember    extensions.PluginRuntimeMember
	revision        int64
	logs            *bytes.Buffer
	healer          *pluginRuntimeDevHealer
}

// newDevHealPostgresFixture 造一份「publication 钉住的制品已坏」的现场：
// 坏制品缺 backend/plugin，候选制品自洽但（默认）还没有 durable 行。
func newDevHealPostgresFixture(t *testing.T, withCandidateRow bool) *devHealPostgresFixture {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.IsolatedURL(t))
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(pool.Close)
	fixture := &devHealPostgresFixture{
		ctx:           ctx,
		pool:          pool,
		store:         extensions.NewPostgresStore(pool),
		extensionID:   fmt.Sprintf("dev.selfheal.%d", time.Now().UnixNano()),
		logs:          &bytes.Buffer{},
		artifactsRoot: t.TempDir(),
	}
	// dev 系统账号：审计归因必须落在 dev-auto 上，而不是 0/NULL。
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ('dev-auto', 'dev-auto', $1, $1, 'SForum dev auto')
		ON CONFLICT (email_lower) DO NOTHING
	`, extensions.DevAutoEmail); err != nil {
		t.Fatalf("seed dev-auto account: %v", err)
	}

	// 用 testartifact 造真实制品（真实包 + 补占位 backend/plugin），与发布期
	// 闸门用的是同一套构造手法；根目录取 fixture 的，便于磁盘扫描用例搜索。
	artifacts := bootstrapArtifactsAt(t, fixture.artifactsRoot)
	broken := artifacts.Materialize(t, fixture.extensionID, "1.0.0")
	if err := os.Remove(filepath.Join(broken.Path, "backend", "plugin")); err != nil {
		t.Fatalf("break pinned artifact: %v", err)
	}
	candidate := artifacts.Materialize(t, fixture.extensionID, "1.0.0")

	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'Dev self-heal fixture', 'disabled')
	`, fixture.extensionID); err != nil {
		t.Fatalf("seed extension: %v", err)
	}
	fixture.brokenVersionID = insertDevHealVersion(t, pool, fixture.extensionID, broken.Path, broken.Digest)
	if withCandidateRow {
		insertDevHealVersion(t, pool, fixture.extensionID, candidate.Path, candidate.Digest)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE extensions SET active_version_id = $2 WHERE id = $1`,
		fixture.extensionID, fixture.brokenVersionID); err != nil {
		t.Fatalf("bind active version: %v", err)
	}
	fixture.brokenMember = extensions.PluginRuntimeMember{
		ExtensionID:        fixture.extensionID,
		ExtensionVersionID: fixture.brokenVersionID,
		ExtensionVersion:   "1.0.0",
		PackageDigest:      broken.Digest,
	}
	fixture.revision = seedDevHealPublication(t, pool, fixture.brokenMember)
	if seeded, err := fixture.store.LatestPluginRuntimePublication(ctx); err != nil || seeded.Revision != fixture.revision {
		t.Fatalf("seeded publication must load: %#v err=%v", seeded, err)
	}
	var isolation string
	if err := pool.QueryRow(ctx, `SHOW transaction_isolation`).Scan(&isolation); err != nil || isolation != "read committed" {
		t.Fatalf("fixture isolation=%q err=%v", isolation, err)
	}

	fixture.healer = newPluginRuntimeDevHealer(pluginRuntimeDevHealConfig{
		pool:          pool,
		store:         fixture.store,
		extensionRoot: fixture.artifactsRoot,
		builtinRoot:   "",
		identities:    devHealDevAutoLookup{pool: pool},
		logger:        devHealTestLogger(fixture.logs),
		appEnv:        "development",
	})
	return fixture
}

func insertDevHealVersion(t *testing.T, pool *pgxpool.Pool, extensionID, packagePath, digest string) int64 {
	t.Helper()
	var versionID int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO extension_versions (extension_id, version, manifest, package_path, package_digest)
		VALUES ($1, '1.0.0', '{}'::jsonb, $2, $3)
		RETURNING id
	`, extensionID, packagePath, digest).Scan(&versionID); err != nil {
		t.Fatalf("seed extension version: %v", err)
	}
	return versionID
}

func seedDevHealPublication(
	t *testing.T,
	pool *pgxpool.Pool,
	member extensions.PluginRuntimeMember,
) int64 {
	t.Helper()
	ctx := context.Background()
	digest, err := extensions.PluginRuntimeMembersDigest([]extensions.PluginRuntimeMember{member})
	if err != nil {
		t.Fatalf("members digest: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publication seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason, actor_user_id)
		VALUES (1, $1, 'startup_reconcile', NULL)
		RETURNING revision
	`, digest).Scan(&revision); err != nil {
		t.Fatalf("seed publication: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_runtime_publication_members (
			publication_revision, extension_id, extension_version_id,
			extension_version, package_digest
		) VALUES ($1, $2, $3, $4, $5)
	`, revision, member.ExtensionID, member.ExtensionVersionID,
		member.ExtensionVersion, member.PackageDigest); err != nil {
		t.Fatalf("seed publication member: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publication seed: %v", err)
	}
	return revision
}

// sealDevHealLedger 追加一条空 full-set，避免把坏成员留给同包后续用例继承。
func sealDevHealLedger(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	digest, err := extensions.PluginRuntimeMembersDigest(nil)
	if err != nil {
		t.Fatalf("empty members digest: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason, actor_user_id)
		VALUES (0, $1, 'startup_reconcile', NULL)
	`, digest); err != nil {
		t.Fatalf("seal publication ledger: %v", err)
	}
}

func TestDevelopmentSelfHealRepointsPinnedArtifact(t *testing.T) {
	fixture := newDevHealPostgresFixture(t, true)

	outcome, err := fixture.healer.HealPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatalf("self-heal: %v", err)
	}
	if len(outcome.Healed) != 1 {
		t.Fatalf("healed=%#v", outcome.Healed)
	}
	healed := outcome.Healed[0]
	if healed.ExtensionID != fixture.extensionID || healed.FromDigest != fixture.brokenMember.PackageDigest ||
		healed.ToDigest == "" || healed.ToDigest == healed.FromDigest || healed.Revision <= fixture.revision {
		t.Fatalf("healed=%#v", healed)
	}

	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatalf("latest publication: %v", err)
	}
	if latest.Revision != healed.Revision || latest.Reason != extensions.PluginRuntimePublicationRecovery ||
		len(latest.Members) != 1 || latest.Members[0].PackageDigest != healed.ToDigest {
		t.Fatalf("latest=%#v", latest)
	}
	// 自愈后的 full-set 必须过后来的启动期只读自检。
	if err := fixture.store.VerifyPluginRuntimePublicationArtifacts(fixture.ctx, 0); err != nil {
		t.Fatalf("post-heal verification: %v", err)
	}

	var (
		actorID  *int64
		metadata string
	)
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT actor_user_id, metadata::text FROM audit_events
		WHERE action = $1
		ORDER BY id DESC LIMIT 1
	`, devSelfHealAuditAction).Scan(&actorID, &metadata); err != nil {
		t.Fatalf("read self-heal audit: %v", err)
	}
	if actorID == nil {
		t.Fatal("self-heal audit must be attributed to the dev-auto system account")
	}
	var devAutoID int64
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT id FROM users WHERE email_lower = lower($1)`, extensions.DevAutoEmail,
	).Scan(&devAutoID); err != nil {
		t.Fatalf("load dev-auto id: %v", err)
	}
	if *actorID != devAutoID {
		t.Fatalf("audit actor=%d want dev-auto %d", *actorID, devAutoID)
	}
	for _, want := range []string{
		`"extensionId"`, `"fromDigest"`, `"toDigest"`, `"publicationRevision"`, `"mode": "development_self_heal"`,
	} {
		if !strings.Contains(metadata, want) {
			t.Fatalf("audit metadata missing %s: %s", want, metadata)
		}
	}
	t.Logf("audit row: action=%s actor=%d metadata=%s", devSelfHealAuditAction, *actorID, metadata)
	logs := fixture.logs.String()
	t.Logf("structured log: %s", strings.TrimSpace(logs))
	for _, want := range []string{
		"extensionId=" + fixture.extensionID,
		"fromDigest=" + fixture.brokenMember.PackageDigest,
		"toDigest=" + healed.ToDigest,
		fmt.Sprintf("revision=%d", healed.Revision),
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("structured log missing %q: %s", want, logs)
		}
	}
}

func TestDevelopmentSelfHealDiscoversCandidateOnDisk(t *testing.T) {
	fixture := newDevHealPostgresFixture(t, false)

	outcome, err := fixture.healer.HealPluginRuntimePublication(fixture.ctx)
	if err != nil || len(outcome.Healed) != 1 {
		t.Fatalf("heal outcome=%#v err=%v", outcome, err)
	}
	// 盘上有、行没有时补登记一行：新 digest 才允许有新行。
	var (
		rowDigest string
		rowPath   string
	)
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT package_digest, package_path FROM extension_versions
		WHERE extension_id = $1 AND package_digest = $2
	`, fixture.extensionID, outcome.Healed[0].ToDigest).Scan(&rowDigest, &rowPath); err != nil {
		t.Fatalf("candidate row was not registered: %v", err)
	}
	if rowPath == "" || !strings.Contains(rowPath, fixture.extensionID) {
		t.Fatalf("candidate row path=%q", rowPath)
	}
	if err := fixture.store.VerifyPluginRuntimePublicationArtifacts(fixture.ctx, 0); err != nil {
		t.Fatalf("post-heal verification: %v", err)
	}
}

func TestDevelopmentSelfHealFailsClosedWithoutCandidate(t *testing.T) {
	fixture := newDevHealPostgresFixture(t, false)
	// 没有 durable 候选行，也没有任何可搜索的根目录：盘上只有那一份坏制品。
	fixture.healer.cfg.extensionRoot = ""
	fixture.healer.cfg.builtinRoot = ""
	t.Cleanup(func() { sealDevHealLedger(t, fixture.pool) })

	outcome, err := fixture.healer.HealPluginRuntimePublication(fixture.ctx)
	if err != nil || len(outcome.Healed) != 0 {
		t.Fatalf("heal outcome=%#v err=%v", outcome, err)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || latest.Revision != fixture.revision {
		t.Fatalf("publication must not move: latest=%#v err=%v", latest, err)
	}
	if err := fixture.store.VerifyPluginRuntimePublicationArtifacts(fixture.ctx, 0); !errors.Is(err, artifactintegrity.ErrArtifactUnusable) {
		t.Fatalf("broken publication must stay fail-closed, got %v", err)
	}
}

// 构建窗口：候选目录在、backend/plugin 不在（build-builtin-plugins.sh 的
// --exclude 窗口）。它不能被选中，也不能因此退回「先发布后修」。
func TestDevelopmentSelfHealRejectsIncompleteBuildWindowCandidate(t *testing.T) {
	fixture := newDevHealPostgresFixture(t, false)
	// 让盘上唯一候选缺入口：把 fixture 里那份自洽候选破坏掉。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		DELETE FROM extension_versions WHERE extension_id = $1 AND id <> $2
	`, fixture.extensionID, fixture.brokenVersionID); err != nil {
		t.Fatalf("drop candidate row: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(fixture.artifactsRoot, fixture.extensionID, "1.0.0"))
	if err != nil {
		t.Fatalf("read artifact layout: %v", err)
	}
	for _, entry := range entries {
		entryPath := filepath.Join(fixture.artifactsRoot, fixture.extensionID, "1.0.0", entry.Name())
		if _, err := os.Stat(filepath.Join(entryPath, "backend", "plugin")); err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(entryPath, "backend", "plugin")); err != nil {
			t.Fatalf("open build window: %v", err)
		}
	}
	// 坏制品所在目录本身已缺入口；把它的 digest 从搜索里排除（它就是 pinned）。
	t.Cleanup(func() { sealDevHealLedger(t, fixture.pool) })

	outcome, err := fixture.healer.HealPluginRuntimePublication(fixture.ctx)
	if err != nil || len(outcome.Healed) != 0 {
		t.Fatalf("incomplete candidate must not be selected: outcome=%#v err=%v", outcome, err)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || latest.Revision != fixture.revision {
		t.Fatalf("publication must not move: latest=%#v err=%v", latest, err)
	}
}
