package settingssurface_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	settingssurface "github.com/keeptrue/sforum/apps/api/app/Models/Extensions/settingssurface"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func intPointer(value int) *int { return &value }

// 治理表只存覆盖项：无行=manifest 默认，删行=恢复默认。这条语义是整张表的存在
// 理由，所以它对着真实迁移后的表验证一次。
func TestPostgresStoreStoresSparseOverrides(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	const extensionID = "settings-surface-test.demo"
	if _, err := pool.Exec(ctx, `DELETE FROM settings_surface_items WHERE extension_id = $1`, extensionID); err != nil {
		t.Fatalf("clear fixture rows: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM settings_surface_items WHERE extension_id = $1`, extensionID)
	})

	store := settingssurface.NewPostgresStore(pool)
	key := settingssurface.OverrideKey{
		ExtensionID:    extensionID,
		Point:          "forum.settings.profile.tabs",
		ContributionID: "demo.tab",
	}

	// 无行 = 用 manifest 默认：新库里没有这个扩展的任何覆盖。
	overrides, err := store.ListOverrides(ctx)
	if err != nil {
		t.Fatalf("list overrides: %v", err)
	}
	for _, item := range overrides {
		if item.Key.ExtensionID == extensionID {
			t.Fatalf("unexpected pre-existing override: %#v", item)
		}
	}

	created, err := store.UpsertOverride(ctx, settingssurface.OverrideInput{
		Key:       key,
		Enabled:   false,
		SortOrder: intPointer(7),
	})
	if err != nil {
		t.Fatalf("upsert override: %v", err)
	}
	if created.Enabled || created.SortOrder == nil || *created.SortOrder != 7 {
		t.Fatalf("created override = %#v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("timestamps must be written by the store: %#v", created)
	}

	// 同一个键再写一次是覆盖，不是插入第二行。
	updated, err := store.UpsertOverride(ctx, settingssurface.OverrideInput{Key: key, Enabled: true})
	if err != nil {
		t.Fatalf("upsert override again: %v", err)
	}
	if !updated.Enabled || updated.SortOrder != nil {
		t.Fatalf("updated override = %#v", updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("upsert must not recreate the row: created=%v now=%v", created.CreatedAt, updated.CreatedAt)
	}

	// 键归一化：大小写不同的扩展/贡献 id 命中同一行。
	normalized := settingssurface.OverrideKey{
		ExtensionID:    "SETTINGS-SURFACE-TEST.DEMO",
		Point:          "forum.settings.profile.tabs",
		ContributionID: "DEMO.TAB",
	}
	if _, err := store.UpsertOverride(ctx, settingssurface.OverrideInput{Key: normalized, Enabled: false}); err != nil {
		t.Fatalf("upsert normalized key: %v", err)
	}
	overrides, err = store.ListOverrides(ctx)
	if err != nil {
		t.Fatalf("list overrides: %v", err)
	}
	matches := 0
	for _, item := range overrides {
		if item.Key.ExtensionID == extensionID && item.Key.ContributionID == key.ContributionID && item.Key.ItemKey == "" {
			matches++
			if item.Enabled {
				t.Fatalf("normalized upsert must update the same row, got %#v", item)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one override row for the normalized key, got %d", matches)
	}

	// 字段级覆盖与 tab 级覆盖共存：item_key 区分。
	fieldKey := key
	fieldKey.ItemKey = "theme"
	if _, err := store.UpsertOverride(ctx, settingssurface.OverrideInput{Key: fieldKey, Enabled: false}); err != nil {
		t.Fatalf("upsert field override: %v", err)
	}
	if err := store.DeleteOverride(ctx, fieldKey); err != nil {
		t.Fatalf("delete field override: %v", err)
	}
	if err := store.DeleteOverride(ctx, fieldKey); !errors.Is(err, settingssurface.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}

	// 删行 = 恢复 manifest 默认。
	if err := store.DeleteOverride(ctx, key); err != nil {
		t.Fatalf("delete override: %v", err)
	}
	overrides, err = store.ListOverrides(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	for _, item := range overrides {
		if item.Key.ExtensionID == extensionID {
			t.Fatalf("override survived deletion: %#v", item)
		}
	}
}

func TestPostgresStoreRejectsIncompleteKeys(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	store := settingssurface.NewPostgresStore(pool)
	if _, err := store.UpsertOverride(ctx, settingssurface.OverrideInput{
		Key: settingssurface.OverrideKey{ExtensionID: "demo.plugin", ContributionID: "tab"},
	}); !errors.Is(err, settingssurface.ErrInvalid) {
		t.Fatalf("upsert without a point = %v, want ErrInvalid", err)
	}
	if err := store.DeleteOverride(ctx, settingssurface.OverrideKey{}); !errors.Is(err, settingssurface.ErrInvalid) {
		t.Fatalf("delete without a key = %v, want ErrInvalid", err)
	}
}
