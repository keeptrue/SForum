package options

import (
	"context"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestBuiltinAutoUpdateOptionDefaultsEnabled(t *testing.T) {
	store := &memoryOptionStore{values: map[string]string{}}
	service := NewService(store)
	ctx := context.Background()
	if err := service.EnsureDefaults(ctx); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	if store.values[NameExtensionBuiltinAutoUpdate] != enabledOptionValue(true) {
		t.Fatalf("default = %q, want enabled", store.values[NameExtensionBuiltinAutoUpdate])
	}
	enabled, err := BuiltinAutoUpdateEnabled(ctx, service)
	if err != nil || !enabled {
		t.Fatalf("BuiltinAutoUpdateEnabled = %v, %v; want true, nil", enabled, err)
	}
}

func TestBuiltinAutoUpdateOptionCanBeDisabled(t *testing.T) {
	store := &memoryOptionStore{values: map[string]string{}}
	service := NewService(store)
	ctx := context.Background()
	actor := identity.Actor{
		ID:     1,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionExtensionPluginManage: true,
		},
	}
	if _, err := service.UpdateMany(ctx, actor, []UpdateInput{
		{Name: NameExtensionBuiltinAutoUpdate, Value: "disabled"},
	}); err != nil {
		t.Fatalf("UpdateMany: %v", err)
	}
	enabled, err := BuiltinAutoUpdateEnabled(ctx, service)
	if err != nil || enabled {
		t.Fatalf("BuiltinAutoUpdateEnabled = %v, %v; want false, nil", enabled, err)
	}
}

func TestBuiltinAutoUpdateOptionInvalidValueFallsBackToEnabled(t *testing.T) {
	store := &memoryOptionStore{values: map[string]string{NameExtensionBuiltinAutoUpdate: "nonsense"}}
	service := NewService(store)
	enabled, err := BuiltinAutoUpdateEnabled(context.Background(), service)
	if err != nil || !enabled {
		t.Fatalf("BuiltinAutoUpdateEnabled = %v, %v; want true, nil", enabled, err)
	}
}
