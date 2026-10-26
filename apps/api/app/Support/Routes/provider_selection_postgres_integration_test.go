package routes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestPostgresProviderSelectionExactArtifactCASAuditAndInvalidation(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tablesReady bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('extension_route_provider_selections') IS NOT NULL`).Scan(&tablesReady); err != nil {
		t.Fatal(err)
	}
	if !tablesReady {
		t.Skip("route provider selection migration is not applied")
	}

	registry, artifact, key, request := providerSelectionTestRegistry(t)
	unique := fmt.Sprintf("route-selection-%d", time.Now().UnixNano())
	key.TargetRouteID = "core.route.selection.pg_" + fmt.Sprintf("%d", time.Now().UnixNano())
	target := coreRoute(key.TargetRouteID, "POST", "/topics")
	key.TargetContractVersion = target.ContractVersion
	request.Key = key
	artifact.ExtensionID = "selection.pg." + fmt.Sprintf("%d", time.Now().UnixNano())
	artifact.RuntimeInstanceID = "runtime-pg"
	request.ProviderArtifact = artifact
	request.ProviderRouteID = artifact.ExtensionID + ".writer"
	request.ProviderContractVersion = request.ProviderRouteID + "@1"
	replacement := modifierRoute(request.ProviderRouteID, key.TargetRouteID, "/topics", "replace", "POST", 100)
	if _, err := registry.Publish(Publication{
		Core:    []CoreRoute{target},
		Plugins: []PluginRouteSet{{Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{replacement}}},
	}); err != nil {
		t.Fatal(err)
	}

	var actorID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users
		(username,username_lower,email,email_lower,display_name) VALUES ($1,$1,$2,$2,$1) RETURNING id`,
		unique, unique+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if _, err := pool.Exec(ctx, `INSERT INTO extensions (id,type,name,status,source,is_system,is_deletable)
		VALUES ($1,'plugin',$1,'enabled','uploaded',false,true)`, artifact.ExtensionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO extension_versions
		(extension_id,version,manifest,package_path,package_digest) VALUES ($1,$2,'{}'::jsonb,$3,$4) RETURNING id`,
		artifact.ExtensionID, artifact.ExtensionVersion, "/tmp/"+unique, artifact.PackageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id=$2 WHERE id=$1`, artifact.ExtensionID, versionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_route_provider_selections WHERE provider_extension_id=$1`, artifact.ExtensionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extension_route_provider_selection_events WHERE target_route_id=$1`, key.TargetRouteID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM extensions WHERE id=$1`, artifact.ExtensionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, actorID)
	})

	request.ActorUserID = actorID
	request.AuditEventID = time.Now().UnixNano()
	store := NewPostgresProviderSelectionStore(pool)
	api := NewProviderSelectionAPI(registry, store)
	selected, err := api.Select(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ProviderExtensionVersionID != versionID || selected.Revision != 1 {
		t.Fatalf("selected = %#v", selected)
	}
	changedContractKey := key
	changedContractKey.TargetContractVersion = strings.TrimSuffix(key.TargetContractVersion, "@1") + "@2"
	if _, err := store.Selected(ctx, changedContractKey); !errors.Is(err, ErrProviderSelectionStale) {
		t.Fatalf("changed target contract error = %v", err)
	}
	desired, err := store.Desired(ctx, changedContractKey)
	if err != nil || desired.Revision != 1 || desired.Key.TargetContractVersion != key.TargetContractVersion {
		t.Fatalf("stale durable selection = %#v, %v", desired, err)
	}
	if _, err := api.Select(ctx, request); !errors.Is(err, ErrProviderSelectionRevisionConflict) {
		t.Fatalf("stale select CAS error = %v", err)
	}
	plan, err := api.BuildExecutionPlan(ctx, "POST", "/topics")
	if err != nil || plan.Terminal().RouteID != request.ProviderRouteID {
		t.Fatalf("selected plan = %#v, %v", plan.Terminal(), err)
	}

	request.ExpectedRevision = 1
	request.AuditEventID++
	selected, err = api.Select(ctx, request)
	if err != nil || selected.Revision != 2 {
		t.Fatalf("CAS reselection = %#v, %v", selected, err)
	}
	if err := api.Reset(ctx, ResetProviderRequest{
		Key: key, ExpectedRevision: 1, ActorUserID: actorID, AuditEventID: request.AuditEventID + 1,
	}); !errors.Is(err, ErrProviderSelectionRevisionConflict) {
		t.Fatalf("stale reset CAS error = %v", err)
	}
	count, err := api.InvalidateExtension(ctx, InvalidateProviderRequest{
		ExtensionID: artifact.ExtensionID, ActorUserID: actorID,
		AuditEventID: request.AuditEventID + 2, ReasonCode: "extension_disabled",
	})
	if err != nil || count != 1 {
		t.Fatalf("invalidate count=%d err=%v", count, err)
	}
	if _, err := store.Selected(ctx, key); !errors.Is(err, ErrProviderSelectionNotFound) {
		t.Fatalf("selection survived invalidation: %v", err)
	}
	events, err := store.ListEvents(ctx, key, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Action != "invalidate" || events[0].PreviousProvider == nil ||
		events[0].PreviousProvider.ProviderExtensionID != artifact.ExtensionID || events[0].ReasonCode != "extension_disabled" {
		t.Fatalf("events = %#v", events)
	}

	if _, err := pool.Exec(ctx, `UPDATE extensions SET status='disabled' WHERE id=$1`, artifact.ExtensionID); err != nil {
		t.Fatal(err)
	}
	request.ExpectedRevision = 0
	request.AuditEventID += 3
	if _, err := api.Select(ctx, request); !errors.Is(err, ErrProviderSelectionStale) {
		t.Fatalf("disabled exact artifact error = %v", err)
	}
}
