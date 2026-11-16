package identity

import (
	"slices"
	"testing"
)

func TestSuperAdminCanUseEveryPermission(t *testing.T) {
	actor := Actor{
		ID:       1,
		Status:   UserStatusActive,
		RoleKeys: []string{RoleSuperAdmin},
	}

	if !actor.Can("made.up.permission") {
		t.Fatal("expected super admin to pass any permission")
	}
}

func TestInactiveActorCannotUsePermissions(t *testing.T) {
	actor := Actor{
		ID:          2,
		Status:      UserStatusDisabled,
		Permissions: map[string]bool{PermissionAdminAccess: true},
	}

	if actor.Can(PermissionAdminAccess) {
		t.Fatal("expected disabled actor to fail permission check")
	}
}

func TestMemberCanEditOwnPost(t *testing.T) {
	actor := Actor{
		ID:          3,
		Status:      UserStatusActive,
		Permissions: map[string]bool{PermissionPostEditOwn: true},
	}
	post := PostSummary{ID: 10, AuthorUserID: 3}

	if !CanEditPost(actor, post) {
		t.Fatal("expected actor to edit own post")
	}
}

func TestMemberCannotEditOtherPostWithoutAnyPermission(t *testing.T) {
	actor := Actor{
		ID:          3,
		Status:      UserStatusActive,
		Permissions: map[string]bool{PermissionPostEditOwn: true},
	}
	post := PostSummary{ID: 10, AuthorUserID: 4}

	if CanEditPost(actor, post) {
		t.Fatal("expected actor not to edit someone else's post")
	}
}

func TestLegacySettingsManageImpliesMailManage(t *testing.T) {
	actor := Actor{
		ID:          4,
		Status:      UserStatusActive,
		Permissions: map[string]bool{PermissionSettingsManage: true},
	}
	if !actor.Can(PermissionSettingsMailManage) {
		t.Fatal("expected settings.manage parent to satisfy settings.mail.manage")
	}
	if actor.Can(PermissionSEOManage) {
		t.Fatal("settings.manage must not imply unrelated permissions")
	}
}

func TestExpandEffectivePermissionsAddsChildren(t *testing.T) {
	got := ExpandEffectivePermissions([]string{PermissionUserManage, PermissionPostCreate}, nil)
	// user.manage 只展开 user.view，不得展开 user.permission_override。
	wantChildren := []string{PermissionUserView, PermissionUserManage, PermissionPostCreate}
	for _, key := range wantChildren {
		found := false
		for _, item := range got {
			if item == key {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected expanded list to contain %s, got %v", key, got)
		}
	}
	for _, item := range got {
		if item == PermissionUserPermissionOverride {
			t.Fatalf("user.manage must not expand to %s, got %v", PermissionUserPermissionOverride, got)
		}
	}
}

// TestUserManageDoesNotGrantPermissionOverride 回归：仅有 user.manage 时
// Can(user.permission_override) 必须为 false，防止运营通过兼容展开提权。
func TestUserManageDoesNotGrantPermissionOverride(t *testing.T) {
	actor := Actor{
		ID:     1,
		Status: UserStatusActive,
		Permissions: map[string]bool{
			PermissionUserManage: true,
		},
	}
	if !actor.Can(PermissionUserManage) {
		t.Fatal("expected Can(user.manage)")
	}
	if !actor.Can(PermissionUserView) {
		t.Fatal("expected user.manage to expand to user.view")
	}
	if actor.Can(PermissionUserPermissionOverride) {
		t.Fatal("user.manage must not imply user.permission_override")
	}
}

// TestExpandSkipsDeniedChild 是 P1-1 的纯函数侧证据：管理员对持有 settings.manage
// 的账号 deny settings.mail.manage 之后，父权限展开不得把该键重新加回有效集。
//
// 变异要求：把 ExpandEffectivePermissions 里的 `if denied[child] { continue }` 删掉，
// 这条测试必须红。
func TestExpandSkipsDeniedChild(t *testing.T) {
	denied := map[string]bool{PermissionSettingsMailManage: true}
	got := ExpandEffectivePermissions([]string{PermissionSettingsManage}, denied)

	if slices.Contains(got, PermissionSettingsMailManage) {
		t.Fatalf("deny 被父权限展开复活：%v", got)
	}
	// 兄弟子权限照常展开：deny 只压住自己那一个键，不是把整棵子树关掉。
	for _, key := range []string{PermissionSettingsSiteManage, PermissionSettingsManage} {
		if !slices.Contains(got, key) {
			t.Fatalf("期望展开结果包含 %s，得到 %v", key, got)
		}
	}
}

// TestCanDeniesChildDespiteParent 是 P1-1 的裁决侧证据：Actor.Permissions 里
// 那个被 deny 的键是**显式 false 条目**（applyDenied 写入），Can() 必须返回 false，
// 而同一父权限下的兄弟子权限仍然放行。
//
// 变异要求：把 hasExpandedPermission 改回「perms[permission] 为 false 时继续回退查
// 父权限」，这条测试必须红。
func TestCanDeniesChildDespiteParent(t *testing.T) {
	permissions := map[string]bool{PermissionSettingsManage: true}
	applyDenied(permissions, map[string]bool{PermissionSettingsMailManage: true})

	// 显式 false 必须真的写进 map（只写不删），否则父权限回退会把它读成「从未授予」。
	allowed, present := permissions[PermissionSettingsMailManage]
	if !present || allowed {
		t.Fatalf("deny 没有落成显式 false 条目：present=%t allowed=%t", present, allowed)
	}

	actor := Actor{ID: 5, Status: UserStatusActive, Permissions: permissions}
	if actor.Can(PermissionSettingsMailManage) {
		t.Fatal("deny 被父权限展开复活：Can(settings.mail.manage) 必须为 false")
	}
	if !actor.Can(PermissionSettingsSiteManage) {
		t.Fatal("同一父权限下的兄弟子权限不应被 deny 影响：Can(settings.site.manage) 必须为 true")
	}
	if origin := actor.GrantSource(PermissionSettingsMailManage); origin != PermissionOriginNone {
		t.Fatalf("GrantSource 必须与 Can 同构，得到 %q", origin)
	}
}

