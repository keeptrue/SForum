package extensions

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestPostgresLegacyPluginRuntimePublicationCommitsExactStateAndFullSet(t *testing.T) {
	fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_full_set", StatusInstalled)
	unrelated := fixture.secondMember()
	genesis, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, []PluginRuntimeMember{unrelated},
	)
	if err != nil {
		t.Fatal(err)
	}

	enabled, publication, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 71)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Status != StatusEnabled || publication.Revision <= genesis.Revision ||
		publication.Reason != PluginRuntimePublicationEnable || publication.ActorUserID != 71 {
		t.Fatalf("enabled=%+v publication=%+v", enabled, publication)
	}
	assertPluginRuntimePublicationMembers(t, publication, transitionMember(target), unrelated)
	assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusEnabled)

	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO mail_provider_selection (slot, extension_id) VALUES ('mail.provider', $1)
	`, target.ID); err != nil {
		t.Fatal(err)
	}
	disabled, publication, err := fixture.store.DisableLegacyPluginRuntime(fixture.ctx, enabled, 72)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != StatusDisabled || publication.Reason != PluginRuntimePublicationDisable ||
		publication.ActorUserID != 72 {
		t.Fatalf("disabled=%+v publication=%+v", disabled, publication)
	}
	assertPluginRuntimePublicationMembers(t, publication, unrelated)
	assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusDisabled)
	var mailSelections int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM mail_provider_selection WHERE extension_id = $1
	`, target.ID).Scan(&mailSelections); err != nil || mailSelections != 1 {
		t.Fatalf("disable must keep the mail provider preference: selections=%d error=%v", mailSelections, err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 3)
}

func TestPostgresLegacyPluginRuntimePublicationFailsClosedBeforeStateMutation(t *testing.T) {
	t.Run("missing genesis", func(t *testing.T) {
		fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_no_genesis", StatusInstalled)
		_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 81)
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("missing genesis error=%v", err)
		}
		assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusInstalled)
		assertPluginRuntimePublicationCount(t, fixture, 0)
	})

	t.Run("invalid genesis", func(t *testing.T) {
		fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_bad_genesis", StatusInstalled)
		if _, err := fixture.store.publishPluginRuntimePublication(
			fixture.ctx, PluginRuntimePublicationEnable, 9, []PluginRuntimeMember{transitionMember(target)},
		); err != nil {
			t.Fatal(err)
		}
		_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 82)
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("invalid genesis error=%v", err)
		}
		assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusInstalled)
		assertPluginRuntimePublicationCount(t, fixture, 1)
	})

	t.Run("open lifecycle", func(t *testing.T) {
		fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_open_lifecycle", StatusInstalled)
		if _, err := fixture.store.publishPluginRuntimePublication(
			fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.pool.Exec(fixture.ctx, `
			INSERT INTO extension_lifecycle_operations (extension_id) VALUES ($1)
		`, target.ID); err != nil {
			t.Fatal(err)
		}
		_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 83)
		if !errors.Is(err, ErrLifecycleOperationInProgress) {
			t.Fatalf("open lifecycle error=%v", err)
		}
		assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusInstalled)
		assertPluginRuntimePublicationCount(t, fixture, 1)
	})

	t.Run("stale exact artifact", func(t *testing.T) {
		fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_stale_artifact", StatusInstalled)
		if _, err := fixture.store.publishPluginRuntimePublication(
			fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
		); err != nil {
			t.Fatal(err)
		}
		target.ActiveVersionID++
		_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 84)
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("stale artifact error=%v", err)
		}
		assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusInstalled)
		assertPluginRuntimePublicationCount(t, fixture, 1)
	})

	t.Run("caller manifest drift", func(t *testing.T) {
		fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_manifest_drift", StatusInstalled)
		if _, err := fixture.store.publishPluginRuntimePublication(
			fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
		); err != nil {
			t.Fatal(err)
		}
		target.Manifest.Backend.Entry = ""
		_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 85)
		if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("manifest drift error=%v", err)
		}
		assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusInstalled)
		assertPluginRuntimePublicationCount(t, fixture, 1)
	})
}

func TestPostgresLegacyPluginRuntimePublicationConflictRollsBackMutableState(t *testing.T) {
	fixture, target := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_atomic_conflict", StatusInstalled)
	stale := PluginRuntimeMember{
		ExtensionID: target.ID, ExtensionVersionID: 104,
		ExtensionVersion: "9.9.9", PackageDigest: strings.Repeat("e", 64),
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO extension_versions (id, extension_id, version, package_digest, manifest)
		VALUES ($1, $2, $3, $4, '{}'::jsonb)
	`, stale.ExtensionVersionID, stale.ExtensionID, stale.ExtensionVersion, stale.PackageDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, []PluginRuntimeMember{stale},
	); err != nil {
		t.Fatal(err)
	}

	_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, 91)
	if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
		t.Fatalf("desired-set CAS error=%v", err)
	}
	assertLegacyPluginRuntimeStatus(t, fixture, target.ID, StatusInstalled)
	assertPluginRuntimePublicationCount(t, fixture, 1)
}

func TestPostgresLegacyPluginRuntimePublicationConcurrentEnableKeepsFullSet(t *testing.T) {
	fixture, first := newLegacyPluginRuntimePublicationPGFixture(t, "legacy_concurrent", StatusInstalled)
	second := transitionFixturePlugin(
		t, "second.plugin", 102, "2.0.0", strings.Repeat("c", 64), "backend/plugin",
	)
	setLegacyPluginRuntimeFixtureArtifact(t, fixture, second, StatusInstalled)
	if _, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
	); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsByPlugin := make(chan error, 2)
	var wait sync.WaitGroup
	for index, target := range []Extension{first, second} {
		wait.Add(1)
		go func(actorUserID int64, target Extension) {
			defer wait.Done()
			<-start
			_, _, err := fixture.store.EnableLegacyPluginRuntime(fixture.ctx, target, actorUserID)
			errorsByPlugin <- err
		}(int64(101+index), target)
	}
	close(start)
	wait.Wait()
	close(errorsByPlugin)
	for err := range errorsByPlugin {
		if err != nil {
			t.Fatal(err)
		}
	}

	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationMembers(t, latest, transitionMember(first), transitionMember(second))
	assertLegacyPluginRuntimeStatus(t, fixture, first.ID, StatusEnabled)
	assertLegacyPluginRuntimeStatus(t, fixture, second.ID, StatusEnabled)
	assertPluginRuntimePublicationCount(t, fixture, 3)
}

func newLegacyPluginRuntimePublicationPGFixture(
	t *testing.T,
	label string,
	status string,
) (*pluginRuntimePublicationPGFixture, Extension) {
	t.Helper()
	fixture := newPluginRuntimePublicationPGFixture(t, label)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		ALTER TABLE extensions
			ADD COLUMN updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp();
		CREATE TABLE extension_lifecycle_operations (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL,
			completed_at TIMESTAMPTZ
		);
		CREATE TABLE mail_provider_selection (
			slot TEXT PRIMARY KEY,
			extension_id TEXT NOT NULL
		);
	`); err != nil {
		t.Fatal(err)
	}
	target := transitionFixturePlugin(
		t, "fixture.plugin", 101, "1.0.0", strings.Repeat("b", 64), "backend/plugin",
	)
	setLegacyPluginRuntimeFixtureArtifact(t, fixture, target, status)
	return fixture, target
}

func setLegacyPluginRuntimeFixtureArtifact(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	target Extension,
	status string,
) {
	t.Helper()
	manifestJSON, err := json.Marshal(target.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions
		SET status = $2, active_version_id = $3
		WHERE id = $1
	`, target.ID, status, target.ActiveVersionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_versions
		SET manifest = $2::jsonb
		WHERE id = $1
	`, target.ActiveVersionID, string(manifestJSON)); err != nil {
		t.Fatal(err)
	}
}

func assertLegacyPluginRuntimeStatus(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	extensionID string,
	want string,
) {
	t.Helper()
	var got string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT status FROM extensions WHERE id = $1
	`, extensionID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("extension status=%q want=%q", got, want)
	}
}
