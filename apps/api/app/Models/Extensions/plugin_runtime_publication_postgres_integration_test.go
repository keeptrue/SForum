package extensions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
	"github.com/pressly/goose/v3"
)

func TestPostgresPluginRuntimePublicationFullSetRoundTrip(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "full-set")
	if _, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx); !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		t.Fatalf("empty latest error=%v", err)
	}

	members := []PluginRuntimeMember{fixture.secondMember(), fixture.firstMember()}
	publication, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 42, members,
	)
	if err != nil {
		t.Fatal(err)
	}
	if publication.Revision <= 0 || publication.MemberCount != 2 || publication.ActorUserID != 42 ||
		publication.Members[0].ExtensionID != "fixture.plugin" ||
		publication.Members[1].ExtensionID != "second.plugin" {
		t.Fatalf("publication=%#v", publication)
	}
	if members[0].ExtensionID != "second.plugin" {
		t.Fatalf("publish mutated caller order: %#v", members)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, publication) {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
	byRevision, err := fixture.store.PluginRuntimePublicationByRevision(fixture.ctx, publication.Revision)
	if err != nil || !samePluginRuntimePublication(byRevision, publication) {
		t.Fatalf("by revision=%#v err=%v", byRevision, err)
	}

	var sqlDigest string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT encode(sha256(convert_to(coalesce(string_agg(
			octet_length(extension_id)::text || ':' || extension_id ||
			octet_length(extension_version_id::text)::text || ':' || extension_version_id::text ||
			octet_length(extension_version)::text || ':' || extension_version ||
			octet_length(package_digest)::text || ':' || package_digest,
			'' ORDER BY extension_id COLLATE "C"
		), ''), 'UTF8')), 'hex')
		FROM plugin_runtime_publication_members
		WHERE publication_revision = $1
	`, publication.Revision).Scan(&sqlDigest); err != nil {
		t.Fatal(err)
	}
	if sqlDigest != publication.MembersDigest {
		t.Fatalf("Go digest=%s SQL digest=%s", publication.MembersDigest, sqlDigest)
	}

	empty, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationRecovery, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Revision <= publication.Revision || empty.MemberCount != 0 || len(empty.Members) != 0 ||
		empty.MembersDigest != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty publication=%#v", empty)
	}
	if latest, err = fixture.store.LatestPluginRuntimePublication(fixture.ctx); err != nil || !samePluginRuntimePublication(latest, empty) {
		t.Fatalf("empty latest=%#v err=%v", latest, err)
	}
	if _, err := fixture.store.PluginRuntimePublicationByRevision(fixture.ctx, empty.Revision+1000); !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		t.Fatalf("missing revision error=%v", err)
	}
	if _, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 1, []PluginRuntimeMember{fixture.themeMember()},
	); !errors.Is(err, ErrPluginRuntimePublicationConflict) || !strings.Contains(err.Error(), "must be a plugin") {
		t.Fatalf("theme publication error=%v", err)
	}
}

func TestPostgresPluginRuntimeNodeApplyCASAndWorkerRetry(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "node-cas")
	publication, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationEnable, 7, []PluginRuntimeMember{fixture.firstMember()},
	)
	if err != nil {
		t.Fatal(err)
	}
	api := PluginRuntimeNodeIdentity{NodeID: "node-shared", ProcessRole: PluginRuntimeProcessAPI, BootID: "boot-api"}
	worker := PluginRuntimeNodeIdentity{NodeID: "node-shared", ProcessRole: PluginRuntimeProcessWorker, BootID: "boot-worker"}
	for _, identity := range []PluginRuntimeNodeIdentity{api, worker} {
		node, err := fixture.store.RegisterPluginRuntimeNode(fixture.ctx, identity, time.Minute)
		if err != nil || node.LastAppliedRevision != 0 || node.PluginRuntimeNodeIdentity != identity ||
			!node.LeaseExpiresAt.After(node.LastSeenAt) {
			t.Fatalf("registered node=%#v err=%v", node, err)
		}
	}

	start := make(chan struct{})
	results := make(chan PluginRuntimePublicationAck, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			ack, err := fixture.store.BeginPluginRuntimePublicationApply(fixture.ctx, api, publication.Revision)
			results <- ack
			errs <- err
		}()
	}
	close(start)
	first := <-results
	firstErr := <-errs
	second := <-results
	secondErr := <-errs
	if firstErr != nil || secondErr != nil || first.Status != PluginRuntimeAckApplying ||
		first.Revision != 1 || first.AttemptCount != 1 || first != second {
		t.Fatalf("concurrent begin first=%#v/%v second=%#v/%v", first, firstErr, second, secondErr)
	}

	drifted := publication
	drifted.Reason = PluginRuntimePublicationRecovery
	if _, err := fixture.store.CompletePluginRuntimePublicationApply(
		fixture.ctx, api, drifted, first.Revision,
		[]PluginRuntimeAppliedMember{{PluginRuntimeMember: fixture.firstMember(), RuntimeInstanceID: "runtime-api"}},
	); !errors.Is(err, ErrPluginRuntimeAckConflict) {
		t.Fatalf("drifted desired completion error=%v", err)
	}
	wrong := fixture.firstMember()
	wrong.PackageDigest = strings.Repeat("f", 64)
	if _, err := fixture.store.CompletePluginRuntimePublicationApply(
		fixture.ctx, api, publication, first.Revision,
		[]PluginRuntimeAppliedMember{{PluginRuntimeMember: wrong, RuntimeInstanceID: "runtime-api"}},
	); !errors.Is(err, ErrPluginRuntimeAckConflict) {
		t.Fatalf("wrong applied member error=%v", err)
	}

	appliedMembers := []PluginRuntimeAppliedMember{{
		PluginRuntimeMember: fixture.firstMember(), RuntimeInstanceID: "runtime-api",
	}}
	type completion struct {
		ack PluginRuntimePublicationAck
		err error
	}
	completed := make(chan completion, 2)
	startComplete := make(chan struct{})
	for range 2 {
		go func() {
			<-startComplete
			ack, err := fixture.store.CompletePluginRuntimePublicationApply(
				fixture.ctx, api, publication, first.Revision, appliedMembers,
			)
			completed <- completion{ack: ack, err: err}
		}()
	}
	close(startComplete)
	var winner PluginRuntimePublicationAck
	var succeeded, conflicted int
	for range 2 {
		result := <-completed
		if result.err == nil {
			succeeded++
			winner = result.ack
		} else if errors.Is(result.err, ErrPluginRuntimeAckConflict) {
			conflicted++
		} else {
			t.Fatalf("concurrent complete error=%v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 || winner.Status != PluginRuntimeAckApplied ||
		winner.Revision != 2 || winner.AppliedMemberCount == nil || *winner.AppliedMemberCount != 1 ||
		winner.AppliedMembersDigest != publication.MembersDigest || winner.AppliedAt == nil {
		t.Fatalf("winner=%#v succeeded=%d conflicted=%d", winner, succeeded, conflicted)
	}
	node, err := fixture.store.GetPluginRuntimeNode(fixture.ctx, api)
	if err != nil || node.LastAppliedRevision != publication.Revision {
		t.Fatalf("api node=%#v err=%v", node, err)
	}
	var runtimeInstanceID string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT runtime_instance_id
		FROM plugin_runtime_applied_members
		WHERE publication_revision = $1 AND node_id = $2
		  AND process_role = $3 AND boot_id = $4
	`, publication.Revision, api.NodeID, api.ProcessRole, api.BootID).Scan(&runtimeInstanceID); err != nil {
		t.Fatal(err)
	}
	if runtimeInstanceID != "runtime-api" {
		t.Fatalf("runtime instance=%q", runtimeInstanceID)
	}
	if _, err := fixture.store.BeginPluginRuntimePublicationApply(fixture.ctx, api, publication.Revision); !errors.Is(err, ErrPluginRuntimeAckConflict) {
		t.Fatalf("applied replay error=%v", err)
	}

	empty, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationRecovery, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	workerAck, err := fixture.store.BeginPluginRuntimePublicationApply(fixture.ctx, worker, empty.Revision)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := fixture.store.FailPluginRuntimePublicationApply(
		fixture.ctx, worker, empty.Revision, workerAck.Revision, "worker start failed",
	)
	if err != nil || failed.Status != PluginRuntimeAckFailed || failed.Revision != 2 || failed.AttemptCount != 1 {
		t.Fatalf("failed worker ack=%#v err=%v", failed, err)
	}
	if _, err := fixture.store.FailPluginRuntimePublicationApply(
		fixture.ctx, worker, empty.Revision, workerAck.Revision, "stale",
	); !errors.Is(err, ErrPluginRuntimeAckConflict) {
		t.Fatalf("stale worker failure error=%v", err)
	}
	retry, err := fixture.store.BeginPluginRuntimePublicationApply(fixture.ctx, worker, empty.Revision)
	if err != nil || retry.Status != PluginRuntimeAckApplying || retry.Revision != 3 || retry.AttemptCount != 2 {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	workerApplied, err := fixture.store.CompletePluginRuntimePublicationApply(
		fixture.ctx, worker, empty, retry.Revision, nil,
	)
	if err != nil || workerApplied.Status != PluginRuntimeAckApplied || workerApplied.Revision != 4 ||
		workerApplied.AppliedMemberCount == nil || *workerApplied.AppliedMemberCount != 0 ||
		workerApplied.AppliedMembersDigest != empty.MembersDigest {
		t.Fatalf("worker applied=%#v err=%v", workerApplied, err)
	}
}

func TestPostgresPluginRuntimeExpiredBootCannotResume(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "expired")
	publication, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	identity := PluginRuntimeNodeIdentity{NodeID: "node-expired", ProcessRole: PluginRuntimeProcessAPI, BootID: "boot-expired"}
	if _, err := fixture.store.RegisterPluginRuntimeNode(fixture.ctx, identity, 250*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ack, err := fixture.store.BeginPluginRuntimePublicationApply(fixture.ctx, identity, publication.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CompletePluginRuntimePublicationApply(
		fixture.ctx, identity, publication, ack.Revision, nil,
	); !errors.Is(err, ErrPluginRuntimeNodeLeaseLost) {
		t.Fatalf("expired completion error=%v", err)
	}
	if _, err := fixture.store.HeartbeatPluginRuntimeNode(fixture.ctx, identity, time.Minute); !errors.Is(err, ErrPluginRuntimeNodeLeaseLost) {
		t.Fatalf("expired heartbeat error=%v", err)
	}
	if _, err := fixture.store.GetPluginRuntimeNode(fixture.ctx, identity); !errors.Is(err, ErrPluginRuntimeNodeLeaseLost) {
		t.Fatalf("expired get error=%v", err)
	}
	if _, err := fixture.store.RegisterPluginRuntimeNode(fixture.ctx, identity, time.Minute); !errors.Is(err, ErrPluginRuntimeNodeLeaseLost) {
		t.Fatalf("expired boot resurrection error=%v", err)
	}
	newBoot := identity
	newBoot.BootID = "boot-new"
	node, err := fixture.store.RegisterPluginRuntimeNode(fixture.ctx, newBoot, time.Minute)
	if err != nil || node.LastAppliedRevision != 0 {
		t.Fatalf("new boot=%#v err=%v", node, err)
	}
}

type pluginRuntimePublicationPGFixture struct {
	t      *testing.T
	ctx    context.Context
	admin  *pgxpool.Pool
	pool   *pgxpool.Pool
	store  *PostgresStore
	schema string
}

func newPluginRuntimePublicationPGFixture(t *testing.T, label string) *pluginRuntimePublicationPGFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// PostgreSQL 会把标识符截断到 63 字节；唯一部分必须放在前面，避免
	// 长 label 在 -count 重跑时把尾部时间戳截掉并复用旧 schema。
	schema := fmt.Sprintf("prp_%d_%s", time.Now().UnixNano(), label)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	removeSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed',
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			package_digest TEXT NOT NULL,
			manifest JSONB NOT NULL DEFAULT '{}'::jsonb
		);
		ALTER TABLE extensions
			ADD CONSTRAINT extensions_active_version_fk
			FOREIGN KEY (active_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
		INSERT INTO extensions (id, type) VALUES
			('fixture.plugin', 'plugin'),
			('second.plugin', 'plugin'),
			('fixture.theme', 'theme');
		INSERT INTO extension_versions (id, extension_id, version, package_digest) VALUES
			(101, 'fixture.plugin', '1.0.0', repeat('b', 64)),
			(102, 'second.plugin', '2.0.0', repeat('c', 64)),
			(103, 'fixture.theme', '1.0.0', repeat('d', 64));
	`); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607160027, true); err != nil {
		db.Close()
		removeSchema()
		admin.Close()
		t.Fatalf("apply plugin runtime publication migration: %v", err)
	}
	if err := db.Close(); err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &pluginRuntimePublicationPGFixture{
		t: t, ctx: ctx, admin: admin, pool: pool, store: NewPostgresStore(pool), schema: schema,
	}
	t.Cleanup(fixture.cleanup)
	return fixture
}

func (f *pluginRuntimePublicationPGFixture) cleanup() {
	f.pool.Close()
	_, _ = f.admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE")
	f.admin.Close()
}

func (f *pluginRuntimePublicationPGFixture) firstMember() PluginRuntimeMember {
	return PluginRuntimeMember{
		ExtensionID: "fixture.plugin", ExtensionVersionID: 101,
		ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("b", 64),
	}
}

func (f *pluginRuntimePublicationPGFixture) secondMember() PluginRuntimeMember {
	return PluginRuntimeMember{
		ExtensionID: "second.plugin", ExtensionVersionID: 102,
		ExtensionVersion: "2.0.0", PackageDigest: strings.Repeat("c", 64),
	}
}

func (f *pluginRuntimePublicationPGFixture) themeMember() PluginRuntimeMember {
	return PluginRuntimeMember{
		ExtensionID: "fixture.theme", ExtensionVersionID: 103,
		ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("d", 64),
	}
}
