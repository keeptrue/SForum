package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	"github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions/builtinauto"
	"github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions/systemactor"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
	extensionpackage "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionPackage"
	"github.com/zhuchunshu/sforum/apps/api/database/migrations"
)

type builtinAutoIntegrationPromoter struct {
	restarted []string
	inputs    []extensions.RestartInput
}

func (p *builtinAutoIntegrationPromoter) Upgrade(
	context.Context, identity.Actor, string, extensions.UpgradeInput,
) (extensions.Extension, error) {
	return extensions.Extension{}, nil
}

func (p *builtinAutoIntegrationPromoter) Restart(
	_ context.Context, _ identity.Actor, id string, input extensions.RestartInput,
) (extensions.Extension, error) {
	p.restarted = append(p.restarted, id)
	p.inputs = append(p.inputs, input)
	return extensions.Extension{ID: id, Status: extensions.StatusEnabled}, nil
}

type builtinAutoIntegrationActors struct {
	actor identity.Actor
}

func (a builtinAutoIntegrationActors) EnsureBuiltinAutoActor(context.Context) (identity.Actor, error) {
	return a.actor, nil
}

type builtinAutoIntegrationPolicy struct{ enabled bool }

func (p builtinAutoIntegrationPolicy) BuiltinAutoUpdateEnabled(context.Context) (bool, error) {
	return p.enabled, nil
}

type builtinAutoIntegrationProbe struct{}

func (builtinAutoIntegrationProbe) ProviderSelected(context.Context, string) (bool, error) {
	return false, nil
}

func TestBuiltinPluginAutoUpdateSelectsOnlyEnabledBuiltinPlugins(t *testing.T) {
	ctx, pool := openBuiltinAutoUpdateDatabase(t)
	var systemRole, deletable bool
	if err := pool.QueryRow(ctx, `SELECT is_system, is_deletable FROM roles WHERE key = 'builtin_auto'`).Scan(&systemRole, &deletable); err != nil {
		t.Fatalf("load builtin_auto role: %v", err)
	}
	if !systemRole || deletable {
		t.Fatalf("builtin_auto role flags = system:%v deletable:%v", systemRole, deletable)
	}
	var permissionCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id
		WHERE r.key = 'builtin_auto'
	`).Scan(&permissionCount); err != nil {
		t.Fatal(err)
	}
	if permissionCount != 2 {
		t.Fatalf("builtin_auto permissions = %d, want 2", permissionCount)
	}
	store := extensions.NewPostgresStore(pool)
	suffix := time.Now().UnixNano()
	enabledID := "test.builtin-auto.enabled." + strconv.FormatInt(suffix, 10)
	disabledID := "test.builtin-auto.disabled." + strconv.FormatInt(suffix, 10)

	root := t.TempDir()
	enabled := saveBuiltinAutoIntegrationVersion(t, ctx, store, root, enabledID, "1.0.0")
	if enabled.Status != extensions.StatusEnabled {
		t.Fatalf("enabled fixture status = %q", enabled.Status)
	}
	staged := saveBuiltinAutoIntegrationVersion(t, ctx, store, root, enabledID, "1.1.0")
	if staged.StagedVersion == nil || staged.StagedVersion.Version != "1.1.0" {
		t.Fatalf("expected staged 1.1.0, got %#v", staged)
	}

	disabled := saveBuiltinAutoIntegrationVersion(t, ctx, store, root, disabledID, "1.0.0")
	if _, err := pool.Exec(ctx, `UPDATE extensions SET status = 'disabled' WHERE id = $1`, disabled.ID); err != nil {
		t.Fatal(err)
	}
	saveBuiltinAutoIntegrationVersion(t, ctx, store, root, disabledID, "1.1.0")

	promoter := &builtinAutoIntegrationPromoter{}
	updater := builtinauto.New(
		store, promoter, builtinAutoIntegrationPolicy{enabled: true},
		builtinAutoIntegrationActors{actor: identity.Actor{Status: identity.UserStatusActive}},
		builtinAutoIntegrationProbe{}, slog.New(slog.NewTextHandler(io.Discard, nil)), false, false,
	)
	summary, err := updater.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Updated) != 1 || summary.Updated[0] != enabledID {
		t.Fatalf("summary = %#v", summary)
	}
	if len(promoter.restarted) != 1 || promoter.restarted[0] != enabledID {
		t.Fatalf("restarted = %#v", promoter.restarted)
	}
	if !promoter.inputs[0].ConfirmCapabilities {
		t.Fatal("builtin restart must confirm host-owned capabilities")
	}
	if !builtinAutoIntegrationHasEvent(t, ctx, pool, enabledID, extensions.EventBuiltinAutoUpdated) {
		t.Fatal("missing builtin_auto_updated event")
	}
}

// openBuiltinAutoUpdateDatabase 创建一次性数据库并跑完整迁移。
// 这样 SaveBuiltin 产生的 plugin runtime publication 不会污染共享测试库的不可变 full set。
func openBuiltinAutoUpdateDatabase(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	baseConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	databaseName := fmt.Sprintf("builtin_auto_%d", time.Now().UnixNano())
	quotedDatabase := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quotedDatabase); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quotedDatabase+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})

	config := baseConfig.Copy()
	config.Database = databaseName
	db := stdlib.OpenDB(*config)
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("migrate isolated builtin auto-update database: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.Database = databaseName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func saveBuiltinAutoIntegrationVersion(
	t *testing.T,
	ctx context.Context,
	store *extensions.PostgresStore,
	root, id, version string,
) extensions.Extension {
	t.Helper()
	packagePath := filepath.Join(root, id, version)
	if err := os.MkdirAll(packagePath, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := extensions.Manifest{
		ManifestVersion: 3,
		ID:              id,
		Name:            "Builtin Auto Integration",
		Description:     "Builtin auto-update integration fixture.",
		URL:             "https://example.com",
		Author:          extensions.ManifestAuthor{Name: "SForum"},
		Version:         version,
		Type:            extensions.TypePlugin,
		SForumVersion:   ">=0.1.0",
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, extensions.ManifestFileName), body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.SaveBuiltin(ctx, extensions.SaveBuiltinInput{
		Manifest: manifest, PackagePath: packagePath, PackageDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func builtinAutoIntegrationHasEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID, action string,
) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM extension_events WHERE extension_id = $1 AND action = $2
		)
	`, extensionID, action).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestBuiltinAutoRestartRealLegacyArtifact(t *testing.T) {
	ctx, pool := openBuiltinAutoUpdateDatabase(t)
	var userID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ('builtin_auto_test', 'builtin_auto_test', 'builtin-auto-test@example.test', 'builtin-auto-test@example.test', 'Builtin Auto Test')
		RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if userID == 0 {
		t.Fatal("human user was not created")
	}
	store := extensions.NewPostgresStore(pool)
	if _, err := store.EnsureInitialPluginRuntimePublication(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	id := "test.builtin-auto.restart." + strconv.FormatInt(time.Now().UnixNano(), 10)
	saveBuiltinAutoIntegrationVersion(t, ctx, store, root, id, "1.0.0")
	staged := saveBuiltinAutoIntegrationVersion(t, ctx, store, root, id, "1.1.0")
	if staged.StagedVersion == nil {
		t.Fatal("expected staged artifact")
	}

	writer := audit.NewPostgresWriter(pool)
	trust := extensions.NewExecutableTrustService(
		store, extensions.NewPostgresExecutableTrustStore(pool),
	).WithAuditor(writer)
	service := extensions.NewServiceWithOptions(
		store, t.TempDir(), "", nil,
		extensions.WithAuditor(writer),
		extensions.WithExecutableTrust(trust, true),
	)
	// Use the exact production system account/role, not a super_admin shortcut:
	// this proves the builtin_auto role has enough authority for the lifecycle.
	actor, err := systemactor.Ensure(ctx, identity.NewPostgresStore(pool), systemactor.Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: identity.RoleBuiltinAuto,
		RequireExistingUser: true,
	})
	if err != nil {
		t.Fatalf("ensure builtin auto actor: %v", err)
	}
	if !actor.Can(identity.PermissionExtensionPluginManage) {
		t.Fatalf("builtin-auto actor lacks extension.plugin.manage: %#v", actor)
	}
	// Second call exercises the existing-account adoption path (username,
	// member-role, and credential guards) against the real store.
	reused, err := systemactor.Ensure(ctx, identity.NewPostgresStore(pool), systemactor.Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: identity.RoleBuiltinAuto,
		RequireExistingUser: true,
	})
	if err != nil || reused.ID != actor.ID {
		t.Fatalf("reused builtin-auto actor = %#v, %v; want id %d", reused, err, actor.ID)
	}
	updated, err := service.Restart(ctx, actor, id, extensions.RestartInput{
		ConfirmCapabilities: true,
		IdempotencyKey:      "builtin-auto-restart-integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != "1.1.0" || updated.StagedVersion != nil || updated.Status != extensions.StatusEnabled {
		t.Fatalf("updated = version %q staged %#v status %q",
			updated.Version, updated.StagedVersion, updated.Status)
	}
}

func TestBuiltinAutoUpdateAdvisoryLockIsSingleOwner(t *testing.T) {
	ctx, pool := openBuiltinAutoUpdateDatabase(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		withBuiltinAutoUpdateLock(ctx, pool, logger, func(context.Context) {
			close(started)
			<-release
		})
		close(done)
	}()
	<-started

	secondRan := false
	withBuiltinAutoUpdateLock(ctx, pool, logger, func(context.Context) {
		secondRan = true
	})
	if secondRan {
		t.Fatal("second lock holder must not run while the first holds the lock")
	}

	close(release)
	<-done

	thirdRan := false
	withBuiltinAutoUpdateLock(ctx, pool, logger, func(context.Context) {
		thirdRan = true
	})
	if !thirdRan {
		t.Fatal("lock must be released after the first holder returns")
	}
}
