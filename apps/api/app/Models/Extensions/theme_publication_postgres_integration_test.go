package extensions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
	"github.com/pressly/goose/v3"
)

func TestPostgresThemeActivationPublishesAtomicallyAndRollsBackInsertFailure(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "atomic")
	target := fixture.saveTheme("target", "1.0.0", strings.Repeat("a", 64))
	current := fixture.activeTheme()
	input := exactThemeActivationInput(current, target, 99, false)
	before := fixture.latestRevision()

	functionName := fmt.Sprintf("test_theme_publication_fail_%d", time.Now().UnixNano())
	triggerName := functionName + "_trigger"
	quotedFunction := pgx.Identifier{functionName}.Sanitize()
	quotedTrigger := pgx.Identifier{triggerName}.Sanitize()
	if _, err := fixture.pool.Exec(fixture.ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.theme_id = '%s' THEN
		    RAISE EXCEPTION 'forced theme publication failure';
		  END IF;
		  RETURN NEW;
		END
		$$
	`, quotedFunction, strings.ReplaceAll(target.ID, "'", "''"))); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, fmt.Sprintf(`
		CREATE TRIGGER %s BEFORE INSERT ON theme_runtime_publications
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, quotedTrigger, quotedFunction)); err != nil {
		t.Fatal(err)
	}
	dropFailureTrigger := func() {
		_, _ = fixture.pool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON theme_runtime_publications`, quotedTrigger))
		_, _ = fixture.pool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, quotedFunction))
	}
	t.Cleanup(dropFailureTrigger)

	if _, err := fixture.store.ActivateThemeExact(fixture.ctx, target.ID, input); err == nil {
		t.Fatal("activation unexpectedly survived forced publication failure")
	}
	if active := fixture.activeTheme(); active.ID != current.ID || active.PackageDigest != current.PackageDigest {
		t.Fatalf("failed publication changed active theme: %#v", active)
	}
	if got := fixture.latestRevision(); got != before {
		t.Fatalf("failed publication revision=%d, want %d", got, before)
	}
	dropFailureTrigger()

	result, err := fixture.store.ActivateThemeExact(fixture.ctx, target.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extension.ID != target.ID || result.Publication.Revision <= before ||
		result.Publication.ThemeID != target.ID || result.Publication.ActorUserID != 99 {
		t.Fatalf("activation result=%#v", result)
	}
	latest, err := fixture.store.LatestThemeRuntimePublication(fixture.ctx)
	if err != nil || !sameThemeRuntimePublication(latest, result.Publication) {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
	byRevision, err := fixture.store.ThemeRuntimePublicationByRevision(fixture.ctx, result.Publication.Revision)
	if err != nil || !sameThemeRuntimePublication(byRevision, result.Publication) {
		t.Fatalf("by revision=%#v err=%v", byRevision, err)
	}
}

func TestPostgresThemeActivationConcurrentCASHasOneWinner(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "concurrent")
	left := fixture.saveTheme("left", "1.0.0", strings.Repeat("b", 64))
	right := fixture.saveTheme("right", "1.0.0", strings.Repeat("c", 64))
	current := fixture.activeTheme()
	start := make(chan struct{})
	type outcome struct {
		result ThemeActivationResult
		err    error
	}
	results := make(chan outcome, 2)
	for index, target := range []Extension{left, right} {
		go func(actor int64, candidate Extension) {
			<-start
			result, err := fixture.store.ActivateThemeExact(
				fixture.ctx, candidate.ID, exactThemeActivationInput(current, candidate, actor, false),
			)
			results <- outcome{result: result, err: err}
		}(int64(100+index), target)
	}
	close(start)
	var winner ThemeActivationResult
	var succeeded, stale int
	for range 2 {
		out := <-results
		switch {
		case out.err == nil:
			succeeded++
			winner = out.result
		case errors.Is(out.err, ErrThemePreviewStale):
			stale++
		default:
			t.Fatalf("concurrent activation error=%v", out.err)
		}
	}
	if succeeded != 1 || stale != 1 {
		t.Fatalf("succeeded=%d stale=%d", succeeded, stale)
	}
	if active := fixture.activeTheme(); active.ID != winner.Extension.ID {
		t.Fatalf("active=%s winner=%s", active.ID, winner.Extension.ID)
	}
	latest, err := fixture.store.LatestThemeRuntimePublication(fixture.ctx)
	if err != nil || !sameThemeRuntimePublication(latest, winner.Publication) {
		t.Fatalf("latest=%#v winner=%#v err=%v", latest, winner.Publication, err)
	}
}

func TestPostgresThemeCompensationRestoresPriorApprovalAndRejectsReplay(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "compensation")
	priorActor := fixture.addUser("prior")
	targetActor := fixture.addUser("target")
	previous := fixture.saveTheme("previous", "1.0.0", strings.Repeat("d", 64))
	target := fixture.saveTheme("target", "2.0.0", strings.Repeat("e", 64))

	current := fixture.activeTheme()
	previousResult, err := fixture.store.ActivateThemeExact(
		fixture.ctx, previous.ID, exactThemeActivationInput(current, previous, priorActor, true),
	)
	if err != nil {
		t.Fatal(err)
	}
	previous = previousResult.Extension
	pageID := fixture.prefix + ".approved"
	fixture.pageIDs = append(fixture.pageIDs, pageID)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO page_provider_bindings (
			page_id, extension_id, contribution_id, version, package_digest, approved_by
		) VALUES ($1, $2, 'approved', $3, $4, $5)
	`, pageID, previous.ID, previous.Version, previous.PackageDigest, priorActor); err != nil {
		t.Fatal(err)
	}

	failed, err := fixture.store.ActivateThemeExact(
		fixture.ctx, target.ID, exactThemeActivationInput(previous, target, targetActor, true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !failed.Publication.SourceCoreReplacementsApproved || failed.Publication.SourceActorUserID != priorActor ||
		!failed.Publication.CoreReplacementsApproved || failed.Publication.ActorUserID != targetActor {
		t.Fatalf("failed publication lost exact actors: %#v", failed.Publication)
	}
	restored, err := fixture.store.CompensateThemeActivation(fixture.ctx, failed.Publication, &previous)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Publication.Reason != ThemeRuntimePublicationCompensation ||
		restored.Publication.ThemeID != previous.ID || !restored.Publication.CoreReplacementsApproved ||
		restored.Publication.ActorUserID != priorActor || !restored.Publication.SourceCoreReplacementsApproved ||
		restored.Publication.SourceActorUserID != targetActor {
		t.Fatalf("compensation=%#v", restored.Publication)
	}
	if _, err := fixture.store.CompensateThemeActivation(fixture.ctx, failed.Publication, &previous); !errors.Is(err, ErrThemePublicationConflict) {
		t.Fatalf("stale compensation error=%v", err)
	}
}

func TestPostgresPageBindingReplayClearsDeletedHistoricalApprover(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "deleted-approver-replay")
	actorID := fixture.addUser("historical-approver")
	pageStore := pages.NewPostgresStore(fixture.pool)
	binding := pages.ProviderBinding{
		PageID: fixture.prefix + ".page", ExtensionID: fixture.prefix + ".theme",
		ContributionID: "replacement", Version: "1.0.0", PackageDigest: strings.Repeat("a", 64),
		ApprovedBy: actorID, TemplatePath: "templates/page.html", ContractVersion: "sforum.page@1",
	}
	if err := pageStore.ReplaceExtensionBindings(fixture.ctx, []string{binding.ExtensionID}, []pages.ProviderBinding{binding}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM users WHERE id = $1`, actorID); err != nil {
		t.Fatal(err)
	}
	if err := pageStore.ReplaceExtensionBindings(fixture.ctx, []string{binding.ExtensionID}, []pages.ProviderBinding{binding}); err != nil {
		t.Fatalf("replay with deleted historical approver: %v", err)
	}
	replayed, ok, err := pageStore.GetBinding(fixture.ctx, binding.PageID)
	if err != nil || !ok {
		t.Fatalf("replayed binding missing: ok=%t err=%v", ok, err)
	}
	if replayed.ApprovedBy != 0 {
		t.Fatalf("replayed approved_by=%d want NULL", replayed.ApprovedBy)
	}
}

func TestPostgresActiveThemeMutationBypassesFailClosed(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "bypasses")
	active := fixture.saveTheme("active", "1.0.0", strings.Repeat("f", 64))
	current := fixture.activeTheme()
	activated, err := fixture.store.ActivateThemeExact(
		fixture.ctx, active.ID, exactThemeActivationInput(current, active, 101, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	active = activated.Extension
	revision := activated.Publication.Revision

	for name, operation := range map[string]func() error{
		"enable":  func() error { _, err := fixture.store.Enable(fixture.ctx, active.ID, TypePlugin); return err },
		"disable": func() error { _, err := fixture.store.Disable(fixture.ctx, active.ID); return err },
		"delete":  func() error { return fixture.store.Delete(fixture.ctx, active.ID) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, ErrThemeActivationRequired) {
				t.Fatalf("error=%v", err)
			}
			if got := fixture.latestRevision(); got != revision {
				t.Fatalf("bypass published revision=%d, want %d", got, revision)
			}
			if got := fixture.activeTheme(); got.ID != active.ID || got.PackageDigest != active.PackageDigest {
				t.Fatalf("bypass changed active theme: %#v", got)
			}
		})
	}
}

func TestPostgresBuiltinThemeSyncStagesUntilExactActivation(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "builtin-upgrade")
	id := fixture.prefix + ".builtin"
	v1, err := fixture.store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
		Manifest:    Manifest{ID: id, Name: "Builtin", Version: "1.0.0", Type: TypeTheme},
		PackagePath: "/tmp/" + id + "/v1", PackageDigest: strings.Repeat("1", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.extensionIDs = append(fixture.extensionIDs, id)
	current := fixture.activeTheme()
	activated, err := fixture.store.ActivateThemeExact(
		fixture.ctx, id, exactThemeActivationInput(current, v1, 102, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	revision := activated.Publication.Revision
	v2, err := fixture.store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
		Manifest:    Manifest{ID: id, Name: "Builtin", Version: "2.0.0", Type: TypeTheme},
		PackagePath: "/tmp/" + id + "/v2", PackageDigest: strings.Repeat("2", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != "1.0.0" || v2.PackageDigest != strings.Repeat("1", 64) || v2.StagedVersion == nil ||
		v2.StagedVersion.Version != "2.0.0" || v2.StagedVersion.PackageDigest != strings.Repeat("2", 64) {
		t.Fatalf("builtin sync did not retain v1 and stage v2: %#v", v2)
	}
	if got := fixture.latestRevision(); got != revision {
		t.Fatalf("inert builtin sync published revision=%d, want %d", got, revision)
	}
	if _, err := fixture.store.PromoteStagedVersion(fixture.ctx, StagedVersionCASInput{
		ExtensionID:             id,
		ExpectedActiveVersionID: v2.ActiveVersionID, ExpectedActiveVersion: v2.Version,
		ExpectedActivePackageDigest: v2.PackageDigest,
		ExpectedStagedVersionID:     v2.StagedVersion.ID, ExpectedStagedVersion: v2.StagedVersion.Version,
		ExpectedPackageDigest: v2.StagedVersion.PackageDigest,
	}); !errors.Is(err, ErrThemeActivationRequired) {
		t.Fatalf("direct active theme promotion error=%v", err)
	}

	target := v2
	target.Version = v2.StagedVersion.Version
	target.PackageDigest = v2.StagedVersion.PackageDigest
	result, err := fixture.store.ActivateThemeExact(
		fixture.ctx, id, exactThemeActivationInput(v2, target, 103, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extension.Version != "2.0.0" || result.Extension.PackageDigest != strings.Repeat("2", 64) ||
		result.Extension.StagedVersion != nil || result.Publication.Revision <= revision {
		t.Fatalf("exact staged activation=%#v", result)
	}
	if _, err := fixture.store.RollbackExtensionVersion(fixture.ctx, RollbackExtensionVersionInput{
		ExtensionID:             id,
		ExpectedActiveVersionID: result.Extension.ActiveVersionID, ExpectedActiveVersion: result.Extension.Version,
		ExpectedActivePackageDigest: result.Extension.PackageDigest,
		TargetVersionID:             v2.ActiveVersionID, TargetVersion: v2.Version,
		TargetPackageDigest: v2.PackageDigest,
	}); !errors.Is(err, ErrThemeActivationRequired) {
		t.Fatalf("direct active theme rollback error=%v", err)
	}
	if got := fixture.latestRevision(); got != result.Publication.Revision {
		t.Fatalf("direct artifact mutation published revision=%d, want %d", got, result.Publication.Revision)
	}
}

func TestPostgresBuiltinSyncRejectsUploadedIDCollision(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "builtin-source-collision")
	uploaded := fixture.saveTheme("uploaded", "1.0.0", strings.Repeat("5", 64))
	current := fixture.activeTheme()
	activated, err := fixture.store.ActivateThemeExact(
		fixture.ctx, uploaded.ID, exactThemeActivationInput(current, uploaded, 106, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	before := activated.Extension
	revision := activated.Publication.Revision

	_, err = fixture.store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
		Manifest: Manifest{
			ID: uploaded.ID, Name: "Builtin collision", Version: "2.0.0", Type: TypeTheme,
		},
		PackagePath: "/tmp/" + uploaded.ID + "/builtin-v2", PackageDigest: strings.Repeat("6", 64),
	})
	if !errors.Is(err, ErrNotDeletable) {
		t.Fatalf("builtin/uploaded collision error=%v", err)
	}
	after, err := fixture.store.Get(fixture.ctx, uploaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Source != SourceUploaded || after.IsSystem || !after.IsDeletable ||
		after.ActiveVersionID != before.ActiveVersionID || after.Version != before.Version ||
		after.PackageDigest != before.PackageDigest || after.StagedVersion != nil {
		t.Fatalf("collision mutated uploaded identity: before=%#v after=%#v", before, after)
	}
	if got := fixture.latestRevision(); got != revision {
		t.Fatalf("collision published revision=%d want=%d", got, revision)
	}
}

func TestPostgresRemovedActiveBuiltinConvergesBeforePrune(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "builtin-prune")
	saveBuiltin := func(label, digit string) Extension {
		id := fixture.prefix + "." + label
		item, err := fixture.store.SaveBuiltin(fixture.ctx, SaveBuiltinInput{
			Manifest:    Manifest{ID: id, Name: label, Version: "1.0.0", Type: TypeTheme},
			PackagePath: "/tmp/" + id, PackageDigest: strings.Repeat(digit, 64),
		})
		if err != nil {
			t.Fatal(err)
		}
		fixture.extensionIDs = append(fixture.extensionIDs, id)
		return item
	}
	removed := saveBuiltin("removed", "3")
	replacement := saveBuiltin("replacement", "4")
	current := fixture.activeTheme()
	removedResult, err := fixture.store.ActivateThemeExact(
		fixture.ctx, removed.ID, exactThemeActivationInput(current, removed, 104, false),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.PruneMissingBuiltins(fixture.ctx, []string{replacement.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Get(fixture.ctx, removed.ID); err != nil {
		t.Fatalf("active missing builtin was pruned before convergence: %v", err)
	}
	if got := fixture.latestRevision(); got != removedResult.Publication.Revision {
		t.Fatalf("prune published revision=%d", got)
	}

	repaired, err := fixture.store.ActivateTheme(fixture.ctx, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Publication.Reason != ThemeRuntimePublicationStartupRepair || repaired.Extension.ID != replacement.ID {
		t.Fatalf("startup repair=%#v", repaired)
	}
	if _, err := fixture.store.PruneMissingBuiltins(fixture.ctx, []string{replacement.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Get(fixture.ctx, removed.ID); !errors.Is(err, ErrExtensionNotFound) {
		t.Fatalf("converged missing builtin error=%v", err)
	}
}

func TestPostgresSameThemeRegistryFailureAppendsCompensation(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "same-theme")
	priorActor := fixture.addUser("prior")
	targetActor := fixture.addUser("target")
	packageFixture := exactThemeRuntimeExtensionFixture(t, fixture.prefix+".same", "/same")
	manifest := Manifest{ID: packageFixture.ID, Name: "Same", Version: packageFixture.Version, Type: TypeTheme}
	if err := writeManifest(packageFixture.PackagePath, manifest); err != nil {
		t.Fatal(err)
	}
	// Manifest became part of the exact content-addressed snapshot.
	digest, err := extensionpackage.DigestTree(packageFixture.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	packageFixture.PackageDigest = digest
	theme, err := fixture.store.SaveInstalled(fixture.ctx, SaveInstalledInput{
		Manifest: manifest, PackagePath: packageFixture.PackagePath, PackageDigest: packageFixture.PackageDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.extensionIDs = append(fixture.extensionIDs, theme.ID)
	current := fixture.activeTheme()
	initial, err := fixture.store.ActivateThemeExact(
		fixture.ctx, theme.ID, exactThemeActivationInput(current, theme, priorActor, true),
	)
	if err != nil {
		t.Fatal(err)
	}
	theme = initial.Extension
	pageID := fixture.prefix + ".same-approved"
	fixture.pageIDs = append(fixture.pageIDs, pageID)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO page_provider_bindings (
			page_id, extension_id, contribution_id, version, package_digest, approved_by
		) VALUES ($1, $2, 'same', $3, $4, $5)
	`, pageID, theme.ID, theme.Version, theme.PackageDigest, priorActor); err != nil {
		t.Fatal(err)
	}
	candidatePackage := exactThemeRuntimeExtensionFixture(t, theme.ID, "/same-v2")
	candidateManifest := manifest
	candidateManifest.Version = "2.0.0"
	if err := writeManifest(candidatePackage.PackagePath, candidateManifest); err != nil {
		t.Fatal(err)
	}
	candidateDigest, err := extensionpackage.DigestTree(candidatePackage.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	stagedTheme, err := fixture.store.SaveInstalled(fixture.ctx, SaveInstalledInput{
		Manifest: candidateManifest, PackagePath: candidatePackage.PackagePath, PackageDigest: candidateDigest,
	})
	if err != nil || stagedTheme.StagedVersion == nil {
		t.Fatalf("stage same-theme v2=%#v err=%v", stagedTheme, err)
	}

	registry := &themeActivationApprovalRegistry{err: errors.New("runtime publication failed")}
	service := NewServiceWithOptions(fixture.store, t.TempDir(), "", LocalRuntimeManager{}, WithPageRegistry(registry))
	actor := extensionManager()
	actor.ID = targetActor
	_, err = service.ActivateThemeFromPreview(fixture.ctx, actor, theme.ID, ThemeActivationInput{
		Version: stagedTheme.StagedVersion.Version, PackageDigest: stagedTheme.StagedVersion.PackageDigest,
		CurrentThemeID: theme.ID, CurrentThemeVersion: theme.Version, CurrentThemeDigest: theme.PackageDigest,
		ApproveCoreReplacements: true,
	})
	if !errors.Is(err, ErrBuildFailed) {
		t.Fatalf("activation error=%v", err)
	}
	publication, err := fixture.store.LatestThemeRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if publication.Revision != initial.Publication.Revision+2 || publication.Reason != ThemeRuntimePublicationCompensation ||
		publication.ThemeID != theme.ID || publication.ActorUserID != priorActor ||
		publication.SourceActorUserID != targetActor || !publication.CoreReplacementsApproved ||
		!publication.SourceCoreReplacementsApproved {
		t.Fatalf("same-theme compensation=%#v", publication)
	}
	restored, err := fixture.store.Get(fixture.ctx, theme.ID)
	if err != nil || restored.Version != theme.Version || restored.PackageDigest != theme.PackageDigest ||
		restored.StagedVersion == nil || restored.StagedVersion.Version != candidateManifest.Version ||
		restored.StagedVersion.PackageDigest != candidateDigest {
		t.Fatalf("same-theme restored=%#v err=%v", restored, err)
	}
}

type themePublicationPGFixture struct {
	t      *testing.T
	ctx    context.Context
	admin  *pgxpool.Pool
	pool   *pgxpool.Pool
	store  *PostgresStore
	schema string
	prefix string

	extensionIDs []string
	userIDs      []int64
	pageIDs      []string
}

func newThemePublicationPGFixture(t *testing.T, label string) *themePublicationPGFixture {
	return newThemePublicationPGFixtureWithInitialPublication(t, label, true)
}

func newThemePublicationPGFixtureWithoutPublication(t *testing.T, label string) *themePublicationPGFixture {
	return newThemePublicationPGFixtureWithInitialPublication(t, label, false)
}

func newThemePublicationPGFixtureWithInitialPublication(
	t *testing.T,
	label string,
	publishInitial bool,
) *themePublicationPGFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("theme_publication_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	// admin 必须晚于 setup 归还才关闭：puddle 的 Pool.Close 会等待所有已取出的
	// 连接归还，defer 顺序反了就会在 fixture 失败时永久挂住（并行跑时整包挂死）。
	keepAdmin := false
	defer func() {
		if !keepAdmin {
			admin.Close()
		}
	}()
	setup, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Release()
	if _, err := setup.Exec(ctx, `SELECT pg_advisory_lock(202607150020)`); err != nil {
		t.Fatal(err)
	}
	setupLocked := true
	defer func() {
		if setupLocked {
			_, _ = setup.Exec(context.Background(), `SELECT pg_advisory_unlock(202607150020)`)
		}
	}()
	if _, err := setup.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	removeSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		db.Close()
		removeSchema()
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 202607140015); err != nil {
		db.Close()
		removeSchema()
		t.Fatalf("migrate isolated theme schema to registry: %v", err)
	}
	// 202610170002（Version GC 台账）也在列：GC 的主题发布钉住用例必须在
	// 隔离 schema 里跑——主题发布账本是 append-only 的，往共享库里插测试行
	// 会让最新修订指向测试制品，真实 watcher 会永久 apply 失败。
	for _, version := range []int64{
		202607150019, 202607150020, 202607150021, 202607160027,
		202607270063, 202607300002, 202610170002, 202610230001,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			db.Close()
			removeSchema()
			t.Fatalf("apply isolated theme migration %d: %v", version, err)
		}
	}
	if err := db.Close(); err != nil {
		removeSchema()
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `SELECT pg_advisory_unlock(202607150020)`); err != nil {
		removeSchema()
		t.Fatal(err)
	}
	setupLocked = false

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		removeSchema()
		t.Fatal(err)
	}
	var currentSchema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil || currentSchema != schema {
		pool.Close()
		removeSchema()
		t.Fatalf("isolated theme schema=%q want=%q err=%v", currentSchema, schema, err)
	}
	fixture := &themePublicationPGFixture{
		t: t, ctx: ctx, admin: admin, pool: pool, store: NewPostgresStore(pool), schema: schema,
		prefix: fmt.Sprintf("test.theme-publication.%s.%d", label, time.Now().UnixNano()),
	}
	keepAdmin = true
	t.Cleanup(fixture.cleanup)
	base := fixture.saveTheme("base", "1.0.0", strings.Repeat("0", 64))
	if publishInitial {
		if _, err := fixture.store.ActivateTheme(ctx, base.ID); err != nil {
			t.Fatal(err)
		}
	} else if command, err := fixture.pool.Exec(ctx, `
		UPDATE extensions SET status = 'enabled', updated_at = statement_timestamp()
		WHERE id = $1 AND type = 'theme' AND active_version_id = $2
	`, base.ID, base.ActiveVersionID); err != nil || command.RowsAffected() != 1 {
		t.Fatalf("prepare legacy active theme: rows=%d error=%v", command.RowsAffected(), err)
	}
	return fixture
}

func (f *themePublicationPGFixture) saveTheme(label, version, digest string) Extension {
	f.t.Helper()
	id := f.prefix + "." + label
	item, err := f.store.SaveInstalled(f.ctx, SaveInstalledInput{
		Manifest:    Manifest{ID: id, Name: label, Version: version, Type: TypeTheme},
		PackagePath: "/tmp/" + id, PackageDigest: digest,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.extensionIDs = append(f.extensionIDs, id)
	return item
}

func (f *themePublicationPGFixture) addUser(label string) int64 {
	f.t.Helper()
	name := strings.ReplaceAll(f.prefix+"."+label, ".", "_")
	var id int64
	if err := f.pool.QueryRow(f.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES ($1, $1, $2, $2, $3) RETURNING id
	`, name, name+"@example.test", label).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	f.userIDs = append(f.userIDs, id)
	return id
}

func (f *themePublicationPGFixture) activeTheme() Extension {
	f.t.Helper()
	active, err := f.store.ActiveTheme(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return active
}

func (f *themePublicationPGFixture) latestRevision() int64 {
	f.t.Helper()
	publication, err := f.store.LatestThemeRuntimePublication(f.ctx)
	if errors.Is(err, ErrThemePublicationNotFound) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return publication.Revision
}

func (f *themePublicationPGFixture) cleanup() {
	f.pool.Close()
	_, _ = f.admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE")
	f.admin.Close()
}

func exactThemeActivationInput(current, target Extension, actorID int64, approve bool) ThemeActivationInput {
	return ThemeActivationInput{
		Version: target.Version, PackageDigest: target.PackageDigest,
		CurrentThemeID: current.ID, CurrentThemeVersion: current.Version, CurrentThemeDigest: current.PackageDigest,
		ApproveCoreReplacements: approve, ActorUserID: actorID,
	}
}

func (f *themePublicationPGFixture) saveBuiltinTheme(id, label, version, digit string) Extension {
	f.t.Helper()
	item, err := f.store.SaveBuiltin(f.ctx, SaveBuiltinInput{
		Manifest:    Manifest{ID: id, Name: label, Version: version, Type: TypeTheme},
		PackagePath: "/tmp/" + id, PackageDigest: strings.Repeat(digit, 64),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.extensionIDs = append(f.extensionIDs, id)
	return item
}

func (f *themePublicationPGFixture) publicationRow(revision int64) (bool, string, sql.NullInt64) {
	f.t.Helper()
	var approved bool
	var source string
	var actor sql.NullInt64
	if err := f.pool.QueryRow(f.ctx, `
		SELECT core_replacements_approved, approval_source, actor_user_id
		FROM theme_runtime_publications WHERE revision = $1
	`, revision).Scan(&approved, &source, &actor); err != nil {
		f.t.Fatal(err)
	}
	return approved, source, actor
}

// 首次安装/启动修复：Host 自带的受保护默认主题由 Host 自己批准核心页替换，
// 发布账本记录 host 来源且不带审批用户。
func TestPostgresStartupRepairHostApprovesProtectedDefaultTheme(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "host-approve-default")
	fixture.saveBuiltinTheme(DefaultThemeID, "default", "1.0.0", "e")

	result, err := fixture.store.ActivateTheme(fixture.ctx, DefaultThemeID)
	if err != nil {
		t.Fatal(err)
	}
	publication := result.Publication
	if !publication.CoreReplacementsApproved || publication.ApprovalSource != ThemeApprovalSourceHost ||
		publication.ActorUserID != 0 || publication.Reason != ThemeRuntimePublicationStartupRepair ||
		publication.ThemeID != DefaultThemeID {
		t.Fatalf("startup repair publication=%+v", publication)
	}
	approved, source, actor := fixture.publicationRow(publication.Revision)
	if !approved || source != ThemeApprovalSourceHost || actor.Valid {
		t.Fatalf("persisted approval=%v source=%q actor=%v", approved, source, actor)
	}
}

// 受保护判定必须同时满足「默认主题 ID + builtin + system + 不可删除」：
// 其它内置主题仍然需要 super_admin 显式批准。
func TestPostgresStartupRepairDoesNotHostApproveOtherThemes(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "host-approve-other")
	other := fixture.saveBuiltinTheme(fixture.prefix+".other-builtin", "other", "1.0.0", "f")

	result, err := fixture.store.ActivateTheme(fixture.ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Publication.CoreReplacementsApproved ||
		result.Publication.ApprovalSource == ThemeApprovalSourceHost || result.Publication.ActorUserID != 0 {
		t.Fatalf("non-default builtin theme was host-approved: %+v", result.Publication)
	}
	uploaded := fixture.saveTheme("uploaded", "1.0.0", strings.Repeat("a", 64))
	result, err = fixture.store.ActivateTheme(fixture.ctx, uploaded.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Publication.CoreReplacementsApproved {
		t.Fatalf("uploaded theme was auto-approved: %+v", result.Publication)
	}
}

// 升级前的老库：已有活动主题、发布账本为空。默认主题由 Host 批准，
// 上传主题保持未批准（必须由 super_admin 显式批准）。
func TestPostgresGenesisHostApprovesOnlyProtectedDefaultTheme(t *testing.T) {
	fixture := newThemePublicationPGFixtureWithoutPublication(t, "genesis-host")
	fixture.saveBuiltinTheme(DefaultThemeID, "default", "1.0.0", "b")
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions SET status = 'disabled' WHERE type = 'theme' AND id <> $1
	`, DefaultThemeID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions SET status = 'enabled' WHERE id = $1
	`, DefaultThemeID); err != nil {
		t.Fatal(err)
	}
	publication, err := fixture.store.EnsureInitialThemeRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !publication.CoreReplacementsApproved || publication.ApprovalSource != ThemeApprovalSourceHost ||
		publication.ActorUserID != 0 || publication.ThemeID != DefaultThemeID {
		t.Fatalf("genesis publication=%+v", publication)
	}

	uploadedFixture := newThemePublicationPGFixtureWithoutPublication(t, "genesis-uploaded")
	uploaded := uploadedFixture.activeTheme()
	if uploaded.Source == SourceBuiltin {
		t.Fatalf("fixture active theme should be uploaded, got %#v", uploaded)
	}
	uploadedPublication, err := uploadedFixture.store.EnsureInitialThemeRuntimePublication(uploadedFixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if uploadedPublication.CoreReplacementsApproved ||
		uploadedPublication.ApprovalSource == ThemeApprovalSourceHost {
		t.Fatalf("uploaded theme genesis was host-approved: %+v", uploadedPublication)
	}
}

// 人工激活永远保留人工来源：批准 → admin + actor；不批准 → 未批准。
func TestPostgresHumanActivationKeepsAdminProvenance(t *testing.T) {
	fixture := newThemePublicationPGFixture(t, "human-approval")
	defaultTheme := fixture.saveBuiltinTheme(DefaultThemeID, "default", "1.0.0", "c")
	actor := fixture.addUser("approver")
	current := fixture.activeTheme()

	unapproved, err := fixture.store.ActivateThemeExact(
		fixture.ctx, defaultTheme.ID, exactThemeActivationInput(current, defaultTheme, actor, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	if unapproved.Publication.CoreReplacementsApproved ||
		unapproved.Publication.ApprovalSource == ThemeApprovalSourceHost {
		t.Fatalf("human activation without approval was approved: %+v", unapproved.Publication)
	}

	current = fixture.activeTheme()
	approved, err := fixture.store.ActivateThemeExact(
		fixture.ctx, defaultTheme.ID, exactThemeActivationInput(current, defaultTheme, actor, true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !approved.Publication.CoreReplacementsApproved ||
		approved.Publication.ApprovalSource != ThemeApprovalSourceAdmin ||
		approved.Publication.ActorUserID != actor {
		t.Fatalf("human approval provenance=%+v", approved.Publication)
	}
}
