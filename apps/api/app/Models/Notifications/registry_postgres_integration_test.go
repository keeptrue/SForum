package notifications_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestRegistryPluginPolicyDefaultsDisabledPostgres(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var descriptorTable string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(to_regclass('public.notification_type_descriptors')::text,'')`).Scan(&descriptorTable); err != nil || descriptorTable == "" {
		t.Skip("notification platform migrations are not applied")
	}

	typeID := "registry.default_disabled_test.notice"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM notification_type_policies WHERE type=$1`, typeID)
		_, _ = pool.Exec(ctx, `DELETE FROM notification_type_descriptors WHERE type=$1`, typeID)
	}
	cleanup()
	t.Cleanup(cleanup)

	owner := registryOwner("registry.default_disabled_test", "1.0.0", "a")
	declaration := registryDeclaration(typeID, 1)
	declaration.RecommendedChannels = []string{"in_app", "web_push"}
	registry := notifications.NewPersistentRegistry(pool)
	if _, err := registry.Publish(ctx, owner, []extensionmanifest.ManifestNotificationType{declaration}, 0); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `
		SELECT channel,enabled,recommended_enabled
		FROM notification_type_policies WHERE type=$1 ORDER BY channel`, typeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var channel string
		var enabled, recommended bool
		if err := rows.Scan(&channel, &enabled, &recommended); err != nil {
			t.Fatal(err)
		}
		if enabled || recommended {
			t.Fatalf("new plugin policy %s enabled=%v recommended=%v", channel, enabled, recommended)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("policy row count = %d, want 3", count)
	}

	if _, err := registry.Deactivate(ctx, owner, registry.Snapshot().Revision); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err := pool.QueryRow(ctx, `SELECT active FROM notification_type_descriptors WHERE type=$1`, typeID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("deactivation removed exact admission but left descriptor active")
	}
}
