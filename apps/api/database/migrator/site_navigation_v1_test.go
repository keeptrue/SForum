package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const siteNavigationV1MigrationVersion = int64(202607280072)
const siteNavigationSnapshotActorMigrationVersion = int64(202607280073)
const siteNavigationMaterializedDefaultsMigrationVersion = int64(202607290074)
const siteNavigationIconHiddenMigrationVersion = int64(202607290075)
const siteNavigationDynamicMaxItemsMigrationVersion = int64(202607290076)
const siteNavigationMobileCategoriesMigrationVersion = int64(202607310001)

func TestSiteNavigationV1MigrationPreservesLegacyTopbarRows(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, 202607120003); err != nil {
		t.Fatalf("migrate legacy SiteChrome schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO site_nav_items (label_zh_cn, label_en_us, href, open_in_new_tab, position, enabled)
		VALUES ('自定义文档', 'Custom docs', 'https://example.test/docs', TRUE, 15, FALSE)
	`); err != nil {
		t.Fatalf("insert legacy custom row: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationV1MigrationVersion, true); err != nil {
		t.Fatalf("apply site navigation migration: %v", err)
	}
	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM site_navigation_state WHERE id = 1`).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("navigation state revision=%d err=%v", revision, err)
	}
	var sourceKey, kind, href string
	var enabled bool
	var position int
	if err := db.QueryRowContext(ctx, `
		SELECT d.source_key, d.link_kind, d.href, p.enabled, p.position
		FROM site_navigation_definitions d
		JOIN site_navigation_placements p ON p.source_key = d.source_key
		WHERE d.label_en_us = 'Custom docs'
	`).Scan(&sourceKey, &kind, &href, &enabled, &position); err != nil {
		t.Fatalf("read migrated custom row: %v", err)
	}
	if !strings.HasPrefix(sourceKey, "operator.migrated.") || strings.Contains(sourceKey, "site_nav_items") ||
		kind != "externalLink" || href != "https://example.test/docs" || enabled || position != 15 {
		t.Fatalf("migrated custom row key=%q kind=%q href=%q enabled=%t position=%d", sourceKey, kind, href, enabled, position)
	}
	var coreCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM site_navigation_placements
		WHERE source_key IN ('core.home', 'core.categories', 'core.tags')
		  AND location = 'public.topbar.primary'
	`).Scan(&coreCount); err != nil || coreCount != 3 {
		t.Fatalf("legacy core placement count=%d err=%v", coreCount, err)
	}
	var legacyCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM site_nav_items`).Scan(&legacyCount); err != nil || legacyCount != 5 {
		t.Fatalf("legacy rows changed count=%d err=%v", legacyCount, err)
	}

	if _, err := provider.ApplyVersion(ctx, siteNavigationV1MigrationVersion, false); err != nil {
		t.Fatalf("rollback unused site navigation migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationV1MigrationVersion, true); err != nil {
		t.Fatalf("reapply site navigation migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO site_navigation_snapshots (revision, operation, affected_locations, document)
		VALUES (1, 'legacy_snapshot', '[]'::jsonb, '{"revision":1,"definitions":[],"placements":[]}'::jsonb)
	`); err != nil {
		t.Fatalf("seed pre-actor navigation snapshot: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationSnapshotActorMigrationVersion, true); err != nil {
		t.Fatalf("apply navigation snapshot actor migration: %v", err)
	}
	var legacyActorID sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT actor_user_id FROM site_navigation_snapshots WHERE operation = 'legacy_snapshot'`).Scan(&legacyActorID); err != nil || legacyActorID.Valid {
		t.Fatalf("legacy snapshot actor=%#v err=%v, want null", legacyActorID, err)
	}
}

func TestSiteNavigationMaterializedDefaultsPreserveExplicitPlacements(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	migrateSiteNavigationFixture(t, ctx, provider, siteNavigationSnapshotActorMigrationVersion)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO site_navigation_placements
		(source_key, location, position, enabled, visibility)
		VALUES ('core.home', 'public.sidebar.primary', 77, FALSE, 'public')
	`); err != nil {
		t.Fatalf("seed explicit sidebar override: %v", err)
	}

	if _, err := provider.ApplyVersion(ctx, siteNavigationMaterializedDefaultsMigrationVersion, true); err != nil {
		t.Fatalf("apply materialized navigation defaults: %v", err)
	}

	var revision int64
	if err := db.QueryRowContext(ctx, `SELECT revision FROM site_navigation_state WHERE id = 1`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("navigation revision=%d err=%v, want 2", revision, err)
	}
	for location, want := range map[string]int{
		"public.topbar.primary":  4,
		"public.sidebar.primary": 4,
		"public.mobile.primary":  3,
		"public.footer.primary":  0,
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM site_navigation_placements WHERE location = $1`, location).Scan(&count); err != nil || count != want {
			t.Fatalf("navigation placement count for %s=%d err=%v, want %d", location, count, err, want)
		}
	}
	var enabled bool
	var position int
	if err := db.QueryRowContext(ctx, `
		SELECT enabled, position
		FROM site_navigation_placements
		WHERE source_key = 'core.home' AND location = 'public.sidebar.primary'
	`).Scan(&enabled, &position); err != nil || enabled || position != 77 {
		t.Fatalf("explicit sidebar placement enabled=%t position=%d err=%v", enabled, position, err)
	}
	var searchKind string
	if err := db.QueryRowContext(ctx, `
		SELECT d.source_kind, p.enabled, p.position
		FROM site_navigation_definitions d
		JOIN site_navigation_placements p ON p.source_key = d.source_key
		WHERE d.href = '/search' AND p.location = 'public.topbar.primary'
	`).Scan(&searchKind, &enabled, &position); err != nil || searchKind != "operator" || !enabled || position != 30 {
		t.Fatalf("legacy search placement kind=%q enabled=%t position=%d err=%v", searchKind, enabled, position, err)
	}
	var sourceKind, href, icon string
	if err := db.QueryRowContext(ctx, `
		SELECT source_kind, href, icon FROM site_navigation_definitions WHERE source_key = 'core.dynamic.categories'
	`).Scan(&sourceKind, &href, &icon); err != nil || sourceKind != "dynamic" || href != "" || icon != "i-lucide-folders" {
		t.Fatalf("dynamic category definition kind=%q href=%q icon=%q err=%v", sourceKind, href, icon, err)
	}
	var publicRevision string
	if err := db.QueryRowContext(ctx, `SELECT value FROM web_options WHERE name = 'site.public_surface_revision'`).Scan(&publicRevision); err != nil || publicRevision != "2" {
		t.Fatalf("public surface revision=%q err=%v, want 2", publicRevision, err)
	}
}

func TestSiteNavigationMobileCategoriesMigrationPreservesExistingPlacement(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	migrateSiteNavigationFixture(t, ctx, provider, siteNavigationDynamicMaxItemsMigrationVersion)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO site_navigation_placements
		(source_key, location, position, enabled, visibility, max_items)
		VALUES ('core.dynamic.categories', 'public.mobile.primary', 73, FALSE, 'public', 9)
	`); err != nil {
		t.Fatalf("seed explicit mobile category placement: %v", err)
	}

	if _, err := provider.ApplyVersion(ctx, siteNavigationMobileCategoriesMigrationVersion, true); err != nil {
		t.Fatalf("apply mobile category navigation migration: %v", err)
	}

	var enabled bool
	var position, maxItems int
	if err := db.QueryRowContext(ctx, `
		SELECT enabled, position, max_items
		FROM site_navigation_placements
		WHERE source_key = 'core.dynamic.categories'
		  AND location = 'public.mobile.primary'
	`).Scan(&enabled, &position, &maxItems); err != nil || enabled || position != 73 || maxItems != 9 {
		t.Fatalf("mobile category placement enabled=%t position=%d maxItems=%d err=%v", enabled, position, maxItems, err)
	}
}

func TestSiteNavigationMobileCategoriesMigrationMaterializesDefault(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	migrateSiteNavigationFixture(t, ctx, provider, siteNavigationDynamicMaxItemsMigrationVersion)

	if _, err := provider.ApplyVersion(ctx, siteNavigationMobileCategoriesMigrationVersion, true); err != nil {
		t.Fatalf("apply mobile category navigation migration: %v", err)
	}

	var enabled bool
	var position, maxItems int
	if err := db.QueryRowContext(ctx, `
		SELECT enabled, position, max_items
		FROM site_navigation_placements
		WHERE source_key = 'core.dynamic.categories'
		  AND location = 'public.mobile.primary'
	`).Scan(&enabled, &position, &maxItems); err != nil || !enabled || position != 40 || maxItems != 0 {
		t.Fatalf("mobile category default enabled=%t position=%d maxItems=%d err=%v", enabled, position, maxItems, err)
	}
}

type failingNavigationAudit struct{}

func (failingNavigationAudit) AppendTx(context.Context, pgx.Tx, audit.Event) error {
	return errors.New("navigation audit is unavailable")
}

func TestSiteNavigationCommandsAreAtomicAndRetainSnapshots(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, 202607120003); err != nil {
		t.Fatalf("migrate command integration schema: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationV1MigrationVersion, true); err != nil {
		t.Fatalf("apply site navigation migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationSnapshotActorMigrationVersion, true); err != nil {
		t.Fatalf("apply navigation snapshot actor migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationIconHiddenMigrationVersion, true); err != nil {
		t.Fatalf("apply navigation icon-hidden migration: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, siteNavigationDynamicMaxItemsMigrationVersion, true); err != nil {
		t.Fatalf("apply navigation max-items migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (id, username, username_lower, email, email_lower, display_name)
		VALUES (77, 'navigation-admin', 'navigation-admin', 'navigation-admin@example.test', 'navigation-admin@example.test', 'Navigation admin')
	`); err != nil {
		t.Fatalf("seed navigation audit actor: %v", err)
	}
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var poolSchema string
	var poolActorCount int
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&poolSchema); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = 77`).Scan(&poolActorCount); err != nil || poolSchema != schema || poolActorCount != 1 {
		t.Fatalf("pgx schema=%q actor count=%d err=%v, want schema=%q and actor", poolSchema, poolActorCount, err, schema)
	}

	store := sitechrome.NewPostgresStore(pool)
	optionsService := options.NewService(options.NewPostgresStore(pool))
	actor := identity.Actor{ID: 77, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true}}
	revisionBumper := options.NewPublicSurfaceRevisionTxBumper(optionsService)
	service := sitechrome.NewService(store).WithNavigationCommandDependencies(store, audit.NewPostgresWriter(pool), revisionBumper)
	operator := sitechrome.NavigationDefinition{SourceKey: "operator.docs", SourceKind: sitechrome.NavigationSourceOperator, LinkKind: sitechrome.NavigationLinkInternal, LabelZhCN: "文档", LabelEnUS: "Docs", Href: "/docs"}
	document, err := service.ApplyNavigationDocument(ctx, actor, sitechrome.NavigationApplyInput{ExpectedRevision: 1, Reason: "initial", Document: sitechrome.NavigationDocument{Definitions: []sitechrome.NavigationDefinition{operator}, Placements: []sitechrome.NavigationPlacement{{SourceKey: operator.SourceKey, Location: sitechrome.NavigationLocationTopbar, Order: 11, Enabled: true, Visibility: sitechrome.NavigationVisibilityPublic}}}})
	if err != nil {
		t.Fatal(err)
	}
	if document.Revision != 2 {
		t.Fatalf("initial revision=%d", document.Revision)
	}
	var revision, publicRevision, snapshotCount, auditCount int
	if err := db.QueryRowContext(ctx, `SELECT revision FROM site_navigation_state WHERE id = 1`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("stored revision=%d err=%v", revision, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT value::int FROM web_options WHERE name = 'site.public_surface_revision'`).Scan(&publicRevision); err != nil || publicRevision != 2 {
		t.Fatalf("public revision=%d err=%v", publicRevision, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM site_navigation_snapshots`).Scan(&snapshotCount); err != nil || snapshotCount != 1 {
		t.Fatalf("snapshot count=%d err=%v", snapshotCount, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE action = $1`, audit.ActionSiteNavigationApply).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}

	failed := sitechrome.NewService(store).WithNavigationCommandDependencies(store, failingNavigationAudit{}, revisionBumper)
	_, err = failed.ApplyNavigationDocument(ctx, actor, sitechrome.NavigationApplyInput{ExpectedRevision: 2, Reason: "must rollback", Document: document})
	if err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("failed audit error=%v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT revision FROM site_navigation_state WHERE id = 1`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("audit rollback revision=%d err=%v", revision, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM site_navigation_snapshots`).Scan(&snapshotCount); err != nil || snapshotCount != 1 {
		t.Fatalf("audit rollback snapshots=%d err=%v", snapshotCount, err)
	}

	concurrentInput := sitechrome.NavigationApplyInput{ExpectedRevision: 2, Reason: "concurrent admin", Document: document}
	start := make(chan struct{})
	results := make(chan error, 2)
	var concurrentWG sync.WaitGroup
	for range 2 {
		concurrentWG.Add(1)
		go func() {
			defer concurrentWG.Done()
			<-start
			_, err := service.ApplyNavigationDocument(ctx, actor, concurrentInput)
			results <- err
		}()
	}
	close(start)
	concurrentWG.Wait()
	close(results)
	var concurrentSuccess, concurrentConflict int
	for err := range results {
		if err == nil {
			concurrentSuccess++
		} else if errors.Is(err, sitechrome.ErrConflict) {
			concurrentConflict++
		} else {
			t.Fatalf("concurrent command error=%v", err)
		}
	}
	if concurrentSuccess != 1 || concurrentConflict != 1 {
		t.Fatalf("concurrent results success=%d conflict=%d", concurrentSuccess, concurrentConflict)
	}
	current, err := service.ReadAdminNavigationDocument(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ExecuteNavigationTransaction(ctx, current.Revision, func(_ context.Context, _ pgx.Tx, prior sitechrome.NavigationDocument) (sitechrome.NavigationTransactionResult, error) {
		invalid := prior
		invalid.Placements = append(invalid.Placements, sitechrome.NavigationPlacement{SourceKey: "core.home", Location: sitechrome.NavigationLocationTopbar, Order: 100001, Enabled: true, Visibility: sitechrome.NavigationVisibilityPublic})
		return sitechrome.NavigationTransactionResult{Document: invalid, Operation: "forced_store_failure"}, nil
	})
	if err == nil {
		t.Fatal("database constraint failure was accepted")
	}
	storedAfterFailure, err := store.ReadNavigationDocument(ctx)
	if err != nil || storedAfterFailure.Revision != current.Revision {
		t.Fatalf("store failure rollback revision=%d err=%v want=%d", storedAfterFailure.Revision, err, current.Revision)
	}

	for sequence := 0; sequence < sitechrome.NavigationMaxSnapshots+3; sequence++ {
		current, err := service.ReadAdminNavigationDocument(ctx, actor)
		if err != nil {
			t.Fatal(err)
		}
		for index := range current.Placements {
			if current.Placements[index].SourceKey == operator.SourceKey && current.Placements[index].Location == sitechrome.NavigationLocationTopbar {
				current.Placements[index].Order = 20 + sequence
			}
		}
		if _, err := service.ApplyNavigationDocument(ctx, actor, sitechrome.NavigationApplyInput{ExpectedRevision: current.Revision, Reason: fmt.Sprintf("reorder %d", sequence), Document: current}); err != nil {
			t.Fatal(err)
		}
	}
	snapshots, err := service.ListNavigationSnapshots(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != sitechrome.NavigationMaxSnapshots {
		t.Fatalf("retained snapshots=%d", len(snapshots))
	}
	if snapshots[0].ActorUserID != actor.ID {
		t.Fatalf("newest snapshot actor=%d, want %d", snapshots[0].ActorUserID, actor.ID)
	}
	current, err = service.ReadAdminNavigationDocument(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RestoreNavigationSnapshot(ctx, actor, snapshots[len(snapshots)-1].ID, current.Revision, "restore history"); err != nil {
		t.Fatal(err)
	}
	snapshots, err = service.ListNavigationSnapshots(ctx, actor)
	if err != nil || len(snapshots) != sitechrome.NavigationMaxSnapshots || snapshots[0].Operation != "snapshot_restore" {
		t.Fatalf("restore snapshots=%#v err=%v", snapshots, err)
	}
}

func migrateSiteNavigationFixture(t *testing.T, ctx context.Context, provider *goose.Provider, through int64) {
	t.Helper()
	if _, err := provider.UpTo(ctx, 202607120003); err != nil {
		t.Fatalf("migrate legacy SiteChrome schema: %v", err)
	}
	for _, version := range []int64{
		siteNavigationV1MigrationVersion,
		siteNavigationSnapshotActorMigrationVersion,
		siteNavigationMaterializedDefaultsMigrationVersion,
		siteNavigationIconHiddenMigrationVersion,
		siteNavigationDynamicMaxItemsMigrationVersion,
	} {
		if version > through {
			break
		}
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply site navigation fixture migration %d: %v", version, err)
		}
	}
}
