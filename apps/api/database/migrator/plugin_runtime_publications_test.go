package migrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const pluginRuntimePublicationVersion = int64(202607160027)

func TestPluginRuntimePublicationMigrationExactSetsCASHistoryAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	seedPluginRuntimeVersionTable(t, ctx, db)
	setDigest := pluginRuntimeFixtureSetDigest()

	if _, err := provider.ApplyVersion(ctx, pluginRuntimePublicationVersion, true); err != nil {
		t.Fatalf("apply plugin runtime publications: %v", err)
	}
	assertPluginRuntimePublicationSchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publications (
			member_count, members_digest, reason
		) VALUES (0, 'not-a-digest', 'startup_reconcile')
	`); err == nil {
		t.Fatal("invalid desired full-set digest was accepted")
	}
	incomplete, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := incomplete.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publications (
			member_count, members_digest, reason
		) VALUES (1, repeat('a', 64), 'startup_reconcile')
	`); err != nil {
		incomplete.Rollback()
		t.Fatal(err)
	}
	if err := incomplete.Commit(); err == nil {
		t.Fatal("incomplete desired full set committed")
	}

	wrongDigest, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wrongDigestRevision int64
	if err := wrongDigest.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (1, repeat('a', 64), 'startup_reconcile') RETURNING revision
	`).Scan(&wrongDigestRevision); err != nil {
		wrongDigest.Rollback()
		t.Fatal(err)
	}
	if _, err := wrongDigest.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_members (
			publication_revision, extension_id, extension_version_id,
			extension_version, package_digest
		) VALUES ($1, 'fixture.plugin', 101, '1.0.0', repeat('b', 64))
	`, wrongDigestRevision); err != nil {
		wrongDigest.Rollback()
		t.Fatal(err)
	}
	if err := wrongDigest.Commit(); err == nil || !strings.Contains(err.Error(), "invalid full set") {
		t.Fatalf("wrong desired full-set digest commit error=%v", err)
	}

	themeMember, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var themePublication int64
	if err := themeMember.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (1, $1, 'startup_reconcile') RETURNING revision
	`, pluginRuntimeTestMembersDigest(pluginRuntimeTestMember{
		extensionID: "fixture.theme", versionID: 103,
		version: "1.0.0", digest: strings.Repeat("d", 64),
	})).Scan(&themePublication); err != nil {
		themeMember.Rollback()
		t.Fatal(err)
	}
	if _, err := themeMember.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_members (
			publication_revision, extension_id, extension_version_id,
			extension_version, package_digest
		) VALUES ($1, 'fixture.theme', 103, '1.0.0', repeat('d', 64))
	`, themePublication); err == nil || !strings.Contains(err.Error(), "must be a plugin") {
		themeMember.Rollback()
		t.Fatalf("theme runtime member insert error=%v", err)
	}
	_ = themeMember.Rollback()

	// Member publication and extension type changes serialize on the extension
	// row. Otherwise two transactions could both validate against stale MVCC
	// snapshots and commit a theme into the plugin runtime set.
	var isolatedSchema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&isolatedSchema); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	typeFence, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var typeFenceRevision int64
	typeFenceMember := pluginRuntimeTestMember{
		extensionID: "race.plugin", versionID: 104,
		version: "1.0.0", digest: strings.Repeat("e", 64),
	}
	if err := typeFence.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (1, $1, 'startup_reconcile') RETURNING revision
	`, pluginRuntimeTestMembersDigest(typeFenceMember)).Scan(&typeFenceRevision); err != nil {
		typeFence.Rollback()
		t.Fatal(err)
	}
	if _, err := typeFence.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_members (
			publication_revision, extension_id, extension_version_id,
			extension_version, package_digest
		) VALUES ($1, 'race.plugin', 104, '1.0.0', repeat('e', 64))
	`, typeFenceRevision); err != nil {
		typeFence.Rollback()
		t.Fatal(err)
	}
	blockedConn, err := db.Conn(ctx)
	if err != nil {
		typeFence.Rollback()
		t.Fatal(err)
	}
	if _, err := blockedConn.ExecContext(ctx, `SELECT set_config('search_path', $1, false)`, isolatedSchema); err != nil {
		blockedConn.Close()
		typeFence.Rollback()
		t.Fatal(err)
	}
	blockedTypeChange, err := blockedConn.BeginTx(ctx, nil)
	if err != nil {
		blockedConn.Close()
		typeFence.Rollback()
		t.Fatal(err)
	}
	if _, err := blockedTypeChange.ExecContext(ctx, `SET LOCAL lock_timeout = '100ms'`); err != nil {
		blockedTypeChange.Rollback()
		blockedConn.Close()
		typeFence.Rollback()
		t.Fatal(err)
	}
	if _, err := blockedTypeChange.ExecContext(ctx, `
		UPDATE extensions SET type = 'theme' WHERE id = 'race.plugin'
	`); err == nil || !strings.Contains(err.Error(), "lock timeout") {
		blockedTypeChange.Rollback()
		blockedConn.Close()
		typeFence.Rollback()
		t.Fatalf("concurrent plugin type mutation error=%v", err)
	}
	_ = blockedTypeChange.Rollback()
	_ = blockedConn.Close()
	if err := typeFence.Commit(); err != nil {
		t.Fatalf("commit type-fenced publication: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET type = 'theme' WHERE id = 'race.plugin'
	`); err == nil || !strings.Contains(err.Error(), "type is immutable") {
		t.Fatalf("committed plugin type mutation error=%v", err)
	}

	var publicationRevision int64
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (
			member_count, members_digest, reason, actor_user_id
		) VALUES (1, $1, 'enable', 42)
		RETURNING revision
	`, setDigest).Scan(&publicationRevision); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_members (
			publication_revision, extension_id, extension_version_id,
			extension_version, package_digest
		) VALUES ($1, 'fixture.plugin', 101, '1.0.0', repeat('b', 64))
	`, publicationRevision); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit exact desired full set: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE extensions SET type = 'theme' WHERE id = 'fixture.plugin'
	`); err == nil || !strings.Contains(err.Error(), "type is immutable") {
		t.Fatalf("published plugin type mutation error=%v", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_publications SET reason = 'recovery'
		WHERE revision = $1
	`, publicationRevision); err == nil {
		t.Fatal("desired publication was mutable")
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM plugin_runtime_publication_members
		WHERE publication_revision = $1
	`, publicationRevision); err == nil {
		t.Fatal("desired member was deletable")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (
			node_id, process_role, boot_id, lease_expires_at
		) VALUES ('node-a', 'scheduler', 'boot-a', statement_timestamp() + interval '1 minute')
	`); err == nil {
		t.Fatal("unknown plugin runtime process role was accepted")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (
			node_id, process_role, boot_id, lease_expires_at
		) VALUES ('node-a', 'api', 'boot-a', statement_timestamp() + interval '1 minute')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (
			node_id, process_role, boot_id, first_seen_at, last_seen_at, lease_expires_at
		) VALUES (
			'node-time', 'api', 'boot-time', statement_timestamp() - interval '2 minutes',
			statement_timestamp() - interval '1 minute', statement_timestamp() + interval '1 minute'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes
		SET first_seen_at = first_seen_at - interval '1 second'
		WHERE node_id = 'node-time' AND process_role = 'api' AND boot_id = 'boot-time'
	`); err == nil || !strings.Contains(err.Error(), "first-seen time is immutable") {
		t.Fatalf("node first-seen mutation error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes
		SET last_seen_at = statement_timestamp(),
		    lease_expires_at = statement_timestamp() + interval '2 minutes'
		WHERE node_id = 'node-time' AND process_role = 'api' AND boot_id = 'boot-time'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes
		SET last_seen_at = first_seen_at + interval '30 seconds'
		WHERE node_id = 'node-time' AND process_role = 'api' AND boot_id = 'boot-time'
	`); err == nil || !strings.Contains(err.Error(), "last-seen time cannot move backwards") {
		t.Fatalf("node last-seen regression error=%v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status,
			applied_member_count, applied_members_digest, applied_at
		) VALUES ($1, 'node-a', 'api', 'boot-a', 'applied',
		          1, $2, statement_timestamp())
	`, publicationRevision, setDigest); err == nil {
		t.Fatal("acknowledgement skipped the applying CAS state")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status
		) VALUES ($1, 'node-a', 'api', 'boot-a', 'applying')
	`, publicationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'failed', error_reason = 'start failed',
		    updated_at = statement_timestamp()
		WHERE publication_revision = $1 AND node_id = 'node-a'
		  AND process_role = 'api' AND boot_id = 'boot-a'
	`, publicationRevision); err == nil {
		t.Fatal("acknowledgement update without CAS revision was accepted")
	}

	partial, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'applied', applied_member_count = 1,
		    applied_members_digest = $2,
		    applied_at = statement_timestamp(), updated_at = statement_timestamp(),
		    revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'node-a'
		  AND process_role = 'api' AND boot_id = 'boot-a' AND revision = 1
	`, publicationRevision, setDigest); err != nil {
		partial.Rollback()
		t.Fatal(err)
	}
	if err := partial.Commit(); err == nil {
		t.Fatal("partial applied full set committed")
	}

	mismatched, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mismatched.ExecContext(ctx, `
		INSERT INTO plugin_runtime_applied_members (
			publication_revision, node_id, process_role, boot_id,
			extension_id, extension_version_id, extension_version,
			package_digest, runtime_instance_id
		) VALUES ($1, 'node-a', 'api', 'boot-a',
		          'fixture.plugin', 101, '1.0.0', repeat('c', 64), 'runtime-wrong')
	`, publicationRevision); err == nil {
		mismatched.Rollback()
		t.Fatal("applied member with a foreign artifact tuple was accepted")
	}
	_ = mismatched.Rollback()

	applied, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applied.ExecContext(ctx, `
		INSERT INTO plugin_runtime_applied_members (
			publication_revision, node_id, process_role, boot_id,
			extension_id, extension_version_id, extension_version,
			package_digest, runtime_instance_id
		) VALUES ($1, 'node-a', 'api', 'boot-a',
		          'fixture.plugin', 101, '1.0.0', repeat('b', 64), 'runtime-exact')
	`, publicationRevision); err != nil {
		applied.Rollback()
		t.Fatal(err)
	}
	if _, err := applied.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes
		SET last_applied_revision = $1, last_seen_at = statement_timestamp(),
		    lease_expires_at = statement_timestamp() + interval '1 minute'
		WHERE node_id = 'node-a' AND process_role = 'api' AND boot_id = 'boot-a'
	`, publicationRevision); err != nil {
		applied.Rollback()
		t.Fatal(err)
	}
	if _, err := applied.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'applied', applied_member_count = 1,
		    applied_members_digest = $2,
		    applied_at = statement_timestamp(), updated_at = statement_timestamp(),
		    revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'node-a'
		  AND process_role = 'api' AND boot_id = 'boot-a' AND revision = 1
	`, publicationRevision, setDigest); err != nil {
		applied.Rollback()
		t.Fatal(err)
	}
	if err := applied.Commit(); err != nil {
		t.Fatalf("commit exact applied full set: %v", err)
	}

	var status, runtimeInstanceID string
	var ackRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT a.status, a.revision, m.runtime_instance_id
		FROM plugin_runtime_publication_acks AS a
		JOIN plugin_runtime_applied_members AS m
		  USING (publication_revision, node_id, process_role, boot_id)
		WHERE a.publication_revision = $1 AND a.node_id = 'node-a'
		  AND a.process_role = 'api' AND a.boot_id = 'boot-a'
	`, publicationRevision).Scan(&status, &ackRevision, &runtimeInstanceID); err != nil {
		t.Fatal(err)
	}
	if status != "applied" || ackRevision != 2 || runtimeInstanceID != "runtime-exact" {
		t.Fatalf("applied evidence status=%q revision=%d runtime=%q", status, ackRevision, runtimeInstanceID)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET revision = revision + 1, updated_at = statement_timestamp()
		WHERE publication_revision = $1 AND node_id = 'node-a'
		  AND process_role = 'api' AND boot_id = 'boot-a'
	`, publicationRevision); err == nil {
		t.Fatal("terminal applied acknowledgement was mutable")
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM plugin_runtime_applied_members
		WHERE publication_revision = $1
	`, publicationRevision); err == nil {
		t.Fatal("applied runtime-instance evidence was deletable")
	}

	firstMember := pluginRuntimeTestMember{
		extensionID: "fixture.plugin", versionID: 101,
		version: "1.0.0", digest: strings.Repeat("b", 64),
	}
	secondMember := pluginRuntimeTestMember{
		extensionID: "second.plugin", versionID: 102,
		version: "2.0.0", digest: strings.Repeat("c", 64),
	}
	multiDigest := pluginRuntimeTestMembersDigest(firstMember, secondMember)
	multiDesired, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var multiRevision int64
	if err := multiDesired.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (2, $1, 'startup_reconcile') RETURNING revision
	`, multiDigest).Scan(&multiRevision); err != nil {
		multiDesired.Rollback()
		t.Fatal(err)
	}
	if _, err := multiDesired.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_members (
			publication_revision, extension_id, extension_version_id,
			extension_version, package_digest
		) VALUES
			($1, 'fixture.plugin', 101, '1.0.0', repeat('b', 64)),
			($1, 'second.plugin', 102, '2.0.0', repeat('c', 64))
	`, multiRevision); err != nil {
		multiDesired.Rollback()
		t.Fatal(err)
	}
	if err := multiDesired.Commit(); err != nil {
		t.Fatalf("commit multi-member desired set: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (node_id, process_role, boot_id, lease_expires_at)
		VALUES ('node-multi', 'api', 'boot-multi', statement_timestamp() + interval '2 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status
		) VALUES ($1, 'node-multi', 'api', 'boot-multi', 'applying')
	`, multiRevision); err != nil {
		t.Fatal(err)
	}
	multiApplied, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := multiApplied.ExecContext(ctx, `
		INSERT INTO plugin_runtime_applied_members (
			publication_revision, node_id, process_role, boot_id,
			extension_id, extension_version_id, extension_version,
			package_digest, runtime_instance_id
		) VALUES
			($1, 'node-multi', 'api', 'boot-multi',
			 'fixture.plugin', 101, '1.0.0', repeat('b', 64), 'runtime-multi-a'),
			($1, 'node-multi', 'api', 'boot-multi',
			 'second.plugin', 102, '2.0.0', repeat('c', 64), 'runtime-multi-b')
	`, multiRevision); err != nil {
		multiApplied.Rollback()
		t.Fatal(err)
	}
	if _, err := multiApplied.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes
		SET last_applied_revision = $1, last_seen_at = statement_timestamp(),
		    lease_expires_at = statement_timestamp() + interval '2 minutes'
		WHERE node_id = 'node-multi' AND process_role = 'api' AND boot_id = 'boot-multi'
	`, multiRevision); err != nil {
		multiApplied.Rollback()
		t.Fatal(err)
	}
	if _, err := multiApplied.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'applied', applied_member_count = 2,
		    applied_members_digest = $2, applied_at = statement_timestamp(),
		    updated_at = statement_timestamp(), revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'node-multi'
		  AND process_role = 'api' AND boot_id = 'boot-multi' AND revision = 1
	`, multiRevision, multiDigest); err != nil {
		multiApplied.Rollback()
		t.Fatal(err)
	}
	if err := multiApplied.Commit(); err != nil {
		t.Fatalf("commit multi-member applied set: %v", err)
	}

	emptyDigest := pluginRuntimeTestMembersDigest()
	var emptyRevision int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO plugin_runtime_publications (member_count, members_digest, reason)
		VALUES (0, $1, 'startup_reconcile') RETURNING revision
	`, emptyDigest).Scan(&emptyRevision); err != nil {
		t.Fatalf("commit empty desired set: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (node_id, process_role, boot_id, lease_expires_at)
		VALUES ('node-worker', 'worker', 'boot-worker', statement_timestamp() + interval '2 minutes')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status
		) VALUES ($1, 'node-worker', 'worker', 'boot-worker', 'applying')
	`, emptyRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'failed', error_reason = 'worker start failed',
		    updated_at = statement_timestamp(), revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'node-worker'
		  AND process_role = 'worker' AND boot_id = 'boot-worker' AND revision = 1;
	`, emptyRevision); err != nil {
		t.Fatalf("fail worker acknowledgement: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'applying', error_reason = '', attempt_count = attempt_count + 1,
		    started_at = statement_timestamp(), updated_at = statement_timestamp(),
		    revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'node-worker'
		  AND process_role = 'worker' AND boot_id = 'boot-worker' AND revision = 2;
	`, emptyRevision); err != nil {
		t.Fatalf("retry worker acknowledgement: %v", err)
	}
	emptyApplied, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyApplied.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes
		SET last_applied_revision = $1, last_seen_at = statement_timestamp(),
		    lease_expires_at = statement_timestamp() + interval '2 minutes'
		WHERE node_id = 'node-worker' AND process_role = 'worker' AND boot_id = 'boot-worker'
	`, emptyRevision); err != nil {
		emptyApplied.Rollback()
		t.Fatal(err)
	}
	if _, err := emptyApplied.ExecContext(ctx, `
		UPDATE plugin_runtime_publication_acks
		SET status = 'applied', applied_member_count = 0,
		    applied_members_digest = $2, applied_at = statement_timestamp(),
		    updated_at = statement_timestamp(), revision = revision + 1
		WHERE publication_revision = $1 AND node_id = 'node-worker'
		  AND process_role = 'worker' AND boot_id = 'boot-worker' AND revision = 3
	`, emptyRevision, emptyDigest); err != nil {
		emptyApplied.Rollback()
		t.Fatal(err)
	}
	if err := emptyApplied.Commit(); err != nil {
		t.Fatalf("commit retried empty worker set: %v", err)
	}
	var workerAttempt int
	var workerAckRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT attempt_count, revision FROM plugin_runtime_publication_acks
		WHERE publication_revision = $1 AND node_id = 'node-worker'
		  AND process_role = 'worker' AND boot_id = 'boot-worker'
	`, emptyRevision).Scan(&workerAttempt, &workerAckRevision); err != nil {
		t.Fatal(err)
	}
	if workerAttempt != 2 || workerAckRevision != 4 {
		t.Fatalf("worker retry attempt=%d ack revision=%d", workerAttempt, workerAckRevision)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status
		) VALUES ($1, 'node-worker', 'worker', 'boot-worker', 'applying')
	`, publicationRevision); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("out-of-order worker acknowledgement error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes SET last_applied_revision = 0
		WHERE node_id = 'node-worker' AND process_role = 'worker' AND boot_id = 'boot-worker'
	`); err == nil || !strings.Contains(err.Error(), "cannot move backwards") {
		t.Fatalf("backward worker node revision error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (
			node_id, process_role, boot_id, last_applied_revision, lease_expires_at
		) VALUES ('node-inherited', 'api', 'boot-inherited', $1,
		          statement_timestamp() + interval '1 minute')
	`, multiRevision); err == nil || !strings.Contains(err.Error(), "start at revision zero") {
		t.Fatalf("inherited node revision error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (node_id, process_role, boot_id, lease_expires_at)
		VALUES ('node-no-ack', 'api', 'boot-no-ack',
		        statement_timestamp() + interval '1 minute')
	`); err != nil {
		t.Fatal(err)
	}
	progressWithoutAck, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := progressWithoutAck.ExecContext(ctx, `
		UPDATE plugin_runtime_nodes SET last_applied_revision = $1
		WHERE node_id = 'node-no-ack' AND process_role = 'api' AND boot_id = 'boot-no-ack'
	`, multiRevision); err != nil {
		progressWithoutAck.Rollback()
		t.Fatal(err)
	}
	if err := progressWithoutAck.Commit(); err == nil || !strings.Contains(err.Error(), "no live applied acknowledgement") {
		t.Fatalf("node progress without applied ack error=%v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (
			node_id, process_role, boot_id, first_seen_at, last_seen_at, lease_expires_at
		) VALUES (
			'node-expired', 'api', 'boot-expired',
			statement_timestamp() - interval '3 minutes',
			statement_timestamp() - interval '2 minutes',
			statement_timestamp() - interval '1 minute'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status
		) VALUES ($1, 'node-expired', 'api', 'boot-expired', 'applying')
	`, multiRevision); err == nil || !strings.Contains(err.Error(), "live node lease") {
		t.Fatalf("expired lease acknowledgement error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_nodes (node_id, process_role, boot_id, lease_expires_at)
		VALUES ('node-expired-apply', 'api', 'boot-expired-apply',
		        statement_timestamp() + interval '1 second')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_publication_acks (
			publication_revision, node_id, process_role, boot_id, status
		) VALUES ($1, 'node-expired-apply', 'api', 'boot-expired-apply', 'applying')
	`, publicationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `SELECT pg_sleep(1.1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO plugin_runtime_applied_members (
			publication_revision, node_id, process_role, boot_id,
			extension_id, extension_version_id, extension_version,
			package_digest, runtime_instance_id
		) VALUES ($1, 'node-expired-apply', 'api', 'boot-expired-apply',
		          'fixture.plugin', 101, '1.0.0', repeat('b', 64), 'runtime-expired')
	`, publicationRevision); err == nil || !strings.Contains(err.Error(), "live node lease") {
		t.Fatalf("expired lease applied member error=%v", err)
	}

	if _, err := provider.ApplyVersion(ctx, pluginRuntimePublicationVersion, false); err == nil {
		t.Fatal("plugin runtime publication Down must refuse durable evidence")
	}
	assertPluginRuntimePublicationSchema(t, ctx, db, true)

	clearPluginRuntimePublicationEvidence(t, ctx, db)
	if _, err := provider.ApplyVersion(ctx, pluginRuntimePublicationVersion, false); err != nil {
		t.Fatalf("rollback empty plugin runtime publications: %v", err)
	}
	assertPluginRuntimePublicationSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, pluginRuntimePublicationVersion, true); err != nil {
		t.Fatalf("reapply plugin runtime publications: %v", err)
	}
	assertPluginRuntimePublicationSchema(t, ctx, db, true)
}

func pluginRuntimeFixtureSetDigest() string {
	return pluginRuntimeTestMembersDigest(pluginRuntimeTestMember{
		extensionID: "fixture.plugin", versionID: 101,
		version: "1.0.0", digest: strings.Repeat("b", 64),
	})
}

type pluginRuntimeTestMember struct {
	extensionID string
	versionID   int64
	version     string
	digest      string
}

func pluginRuntimeTestMembersDigest(members ...pluginRuntimeTestMember) string {
	sort.Slice(members, func(i, j int) bool { return members[i].extensionID < members[j].extensionID })
	var canonical strings.Builder
	for _, member := range members {
		for _, field := range []string{
			member.extensionID, strconv.FormatInt(member.versionID, 10),
			member.version, member.digest,
		} {
			canonical.WriteString(strconv.Itoa(len([]byte(field))))
			canonical.WriteByte(':')
			canonical.WriteString(field)
		}
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}

func seedPluginRuntimeVersionTable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL
		);
		INSERT INTO extensions (id, type) VALUES
			('fixture.plugin', 'plugin'),
			('second.plugin', 'plugin'),
			('race.plugin', 'plugin'),
			('fixture.theme', 'theme');

		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL,
			version TEXT NOT NULL,
			package_digest TEXT NOT NULL
		);
		INSERT INTO extension_versions (id, extension_id, version, package_digest)
		VALUES
			(101, 'fixture.plugin', '1.0.0', repeat('b', 64)),
			(102, 'second.plugin', '2.0.0', repeat('c', 64)),
			(103, 'fixture.theme', '1.0.0', repeat('d', 64)),
			(104, 'race.plugin', '1.0.0', repeat('e', 64));
	`); err != nil {
		t.Fatal(err)
	}
}

func assertPluginRuntimePublicationSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{
		"plugin_runtime_publications",
		"plugin_runtime_publication_members",
		"plugin_runtime_nodes",
		"plugin_runtime_publication_acks",
		"plugin_runtime_applied_members",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL
		`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("table %s exists=%t want=%t", table, exists, want)
		}
	}
	if !want {
		return
	}
	var triggerExists bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger
			WHERE tgname = 'plugin_runtime_publication_notify' AND NOT tgisinternal
		)
	`).Scan(&triggerExists); err != nil {
		t.Fatal(err)
	}
	if !triggerExists {
		t.Fatal("plugin runtime publication notification trigger is missing")
	}
}

func clearPluginRuntimePublicationEvidence(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DROP TRIGGER plugin_runtime_applied_member_immutable ON plugin_runtime_applied_members;
		DROP TRIGGER plugin_runtime_applied_member_no_truncate ON plugin_runtime_applied_members;
		DELETE FROM plugin_runtime_applied_members;
		DROP TRIGGER plugin_runtime_ack_no_delete ON plugin_runtime_publication_acks;
		DROP TRIGGER plugin_runtime_ack_no_truncate ON plugin_runtime_publication_acks;
		DELETE FROM plugin_runtime_publication_acks;
		DELETE FROM plugin_runtime_nodes;
		DROP TRIGGER plugin_runtime_publication_member_immutable ON plugin_runtime_publication_members;
		DROP TRIGGER plugin_runtime_publication_member_no_truncate ON plugin_runtime_publication_members;
		DELETE FROM plugin_runtime_publication_members;
		DROP TRIGGER plugin_runtime_publication_immutable ON plugin_runtime_publications;
		DROP TRIGGER plugin_runtime_publication_no_truncate ON plugin_runtime_publications;
		DELETE FROM plugin_runtime_publications;
	`); err != nil {
		t.Fatal(err)
	}
}
