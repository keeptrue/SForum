package options

import (
	"context"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestPublicSurfaceRevisionDefaultsAndBump(t *testing.T) {
	store := &fakeStore{items: map[string]string{}}
	service := NewServiceWithDefaults(store, Defaults{})
	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}

	rev, err := service.PublicSurfaceRevision(context.Background())
	if err != nil {
		t.Fatalf("PublicSurfaceRevision: %v", err)
	}
	if rev != 1 {
		t.Fatalf("default revision want 1, got %d", rev)
	}

	// 公开可读。
	option, err := service.Get(context.Background(), NamePublicSurfaceRevision)
	if err != nil {
		t.Fatalf("Get public revision: %v", err)
	}
	if option.Value != "1" {
		t.Fatalf("public option value want 1, got %q", option.Value)
	}

	next, err := service.BumpPublicSurfaceRevision(context.Background())
	if err != nil {
		t.Fatalf("Bump: %v", err)
	}
	if next != 2 {
		t.Fatalf("bump want 2, got %d", next)
	}
	rev, err = service.PublicSurfaceRevision(context.Background())
	if err != nil || rev != 2 {
		t.Fatalf("after bump revision=%d err=%v", rev, err)
	}

	// 运营不可手写覆盖。
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionSettingsSiteManage: true},
	}
	if _, err := service.Update(context.Background(), actor, UpdateInput{
		Name: NamePublicSurfaceRevision, Value: "99",
	}); err == nil {
		t.Fatal("expected actor Update of public surface revision to fail")
	}
	rev, err = service.PublicSurfaceRevision(context.Background())
	if err != nil || rev != 2 {
		t.Fatalf("revision must stay 2 after rejected update, got %d err=%v", rev, err)
	}
}

func TestParsePublicSurfaceRevision(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"", 1},
		{"0", 1},
		{"-3", 1},
		{"1", 1},
		{"42", 42},
		{" 7 ", 7},
		{"nope", 1},
	}
	for _, tc := range cases {
		if got := parsePublicSurfaceRevision(tc.raw); got != tc.want {
			t.Fatalf("parse %q want %d got %d", tc.raw, tc.want, got)
		}
	}
}
