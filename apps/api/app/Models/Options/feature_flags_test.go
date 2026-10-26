package options

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestFeatureFlagsRecommendedDefaultsAndRestore(t *testing.T) {
	store := &memoryOptionStore{values: map[string]string{}}
	service := NewService(store)
	ctx := context.Background()
	if err := service.EnsureDefaults(ctx); err != nil {
		t.Fatalf("ensure defaults: %v", err)
	}

	flags, err := service.FeatureFlags(ctx)
	if err != nil {
		t.Fatalf("FeatureFlags: %v", err)
	}
	if !flags.Search || !flags.Registration || !flags.Attachments {
		t.Fatalf("expected beginner-friendly defaults enabled, got %#v", flags)
	}

	// 公开列表应包含 features.search，不应包含非 public 的 features.webhooks。
	public, err := service.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := map[string]bool{}
	for _, item := range public {
		names[item.Name] = true
	}
	if !names[NameFeatureSearch] {
		t.Fatal("public options missing features.search")
	}
	if names[NameFeatureWebhooks] {
		t.Fatal("public options must not expose features.webhooks")
	}

	admin := identity.Actor{
		ID:     1,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionSettingsSiteManage: true,
		},
	}
	_, err = service.UpdateMany(ctx, admin, []UpdateInput{
		{Name: NameFeatureSearch, Value: "disabled"},
	})
	if err != nil {
		t.Fatalf("disable search: %v", err)
	}
	enabled, err := service.IsFeatureEnabled(ctx, NameFeatureSearch)
	if err != nil || enabled {
		t.Fatalf("expected search disabled, enabled=%v err=%v", enabled, err)
	}
	missing, err := service.MissingRequiredFeatures(ctx, []string{NameFeatureSearch, NameFeatureMentions})
	if err != nil {
		t.Fatalf("MissingRequiredFeatures: %v", err)
	}
	if len(missing) != 1 || missing[0] != NameFeatureSearch {
		t.Fatalf("unexpected missing: %#v", missing)
	}

	restored, err := service.RestoreFeatureFlagDefaults(ctx, admin)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(restored) != len(FeatureFlagCatalog()) {
		t.Fatalf("restore count %d want %d", len(restored), len(FeatureFlagCatalog()))
	}
	enabled, _ = service.IsFeatureEnabled(ctx, NameFeatureSearch)
	if !enabled {
		t.Fatal("search should be re-enabled after restore")
	}

	// 无权限拒绝。
	guest := identity.Actor{ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}}
	if _, err := service.RestoreFeatureFlagDefaults(ctx, guest); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

// memoryOptionStore 是 feature flag 测试用的最小 store（仅实现 Store 接口）。
type memoryOptionStore struct {
	values map[string]string
}

func (s *memoryOptionStore) List(context.Context) ([]Option, error) {
	out := make([]Option, 0, len(s.values))
	for name, value := range s.values {
		out = append(out, Option{Name: name, Value: value})
	}
	return out, nil
}

func (s *memoryOptionStore) Upsert(_ context.Context, input UpdateInput) (Option, error) {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[input.Name] = input.Value
	return Option{Name: input.Name, Value: input.Value}, nil
}

func (s *memoryOptionStore) InsertMissing(_ context.Context, input UpdateInput) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	if _, ok := s.values[input.Name]; !ok {
		s.values[input.Name] = input.Value
	}
	return nil
}
