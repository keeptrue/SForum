package options

import (
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestOptionGuardManagePermissionsUsesStaticCatalogWithoutStoreIO(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	permissions, ok := service.OptionGuardManagePermissions([]string{NameSiteName, NameForumDefaultCategorySlug, NameSiteURL})
	if !ok || len(permissions) != 2 || permissions[0] != identity.PermissionCategoryManage ||
		permissions[1] != identity.PermissionSettingsSiteManage {
		t.Fatalf("permissions = %#v, ok=%v", permissions, ok)
	}
	for range 100 {
		if _, ok := service.OptionGuardManagePermissions([]string{NameSiteName}); !ok {
			t.Fatal("static option policy disappeared")
		}
	}
	if store.listCalls != 0 {
		t.Fatalf("option guard reached Store: calls=%d", store.listCalls)
	}
	if _, ok := service.OptionGuardManagePermissions([]string{"future.option"}); ok {
		t.Fatal("unknown option was accepted")
	}
	if _, ok := service.OptionGuardManagePermissions([]string{}); ok {
		t.Fatal("empty mutation was accepted")
	}
	if all, ok := service.OptionGuardManagePermissions(nil); !ok || len(all) < 2 {
		t.Fatalf("management catalog = %#v, ok=%v", all, ok)
	}
}
