package identity

import (
	"errors"
	"slices"
	"testing"
)

// seedGrantGuardTarget 造一个非 super_admin 的目标账号，供改角色用例使用。
func seedGrantGuardTarget(store *fakeStore, id int64) {
	store.users[id] = CurrentUser{ID: id, Username: "target", Status: UserStatusActive}
	store.userEmails[id] = "target@example.com"
}

// grantGuardManagerActor 是持有 role.manage 但不是 super_admin 的操作者。
func grantGuardManagerActor(id int64) Actor {
	return Actor{
		ID:     id,
		Status: UserStatusActive,
		Permissions: map[string]bool{
			PermissionAdminAccess: true,
			PermissionRoleManage:  true,
			PermissionUserManage:  true,
		},
	}
}

// TestReplaceUserRolesRejectsRoleGrantBeyondActor 验证改用户角色与建号共用同一条
// 不变量：operator 不能把带 database.manage 的 tech_admin 授予别人。
func TestReplaceUserRolesRejectsRoleGrantBeyondActor(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	seedGrantGuardTarget(store, 7)
	ctx := testContext(t)

	_, err := service.ReplaceUserRoles(ctx, operatorActor(10), 7, []string{RoleTechAdmin})
	if !errors.Is(err, ErrRoleGrantExceedsActor) {
		t.Fatalf("expected ErrRoleGrantExceedsActor, got %v", err)
	}
	// 拒绝必须发生在写库之前：目标用户的角色映射保持为空。
	if got := store.userRoleIDs[7]; len(got) != 0 {
		t.Fatalf("denied grant must not reach the store, got %v", got)
	}
	// 422 + roleKeys 的既有契约不变。
	var fieldsErr *AdminUserFieldsError
	if !errors.As(err, &fieldsErr) {
		t.Fatalf("error must expose *AdminUserFieldsError, got %T", err)
	}
	if messages := fieldsErr.Fields[FieldRoleKeys]; !slices.Contains(messages, CodeRoleGrantExceedsActor) {
		t.Fatalf("roleKeys messages=%v", messages)
	}
}

// TestReplaceUserRolesAllowsGrantsInsideActorPermissionSet 覆盖允许侧：默认注册组
// 与「权限是操作者子集」的自建角色都必须照常可授予。
func TestReplaceUserRolesAllowsGrantsInsideActorPermissionSet(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	seedGrantGuardTarget(store, 7)
	ctx := testContext(t)

	detail, err := service.ReplaceUserRoles(ctx, operatorActor(10), 7, []string{RoleMember})
	if err != nil {
		t.Fatalf("operator must still grant the default member role, got %v", err)
	}
	if !slices.Contains(detail.RoleKeys, RoleMember) {
		t.Fatalf("expected member role applied, got %v", detail.RoleKeys)
	}

	detail, err = service.ReplaceUserRoles(ctx, operatorActor(10), 7, []string{"support"})
	if err != nil {
		t.Fatalf("operator must grant a role inside their own permission set, got %v", err)
	}
	if !slices.Contains(detail.RoleKeys, "support") {
		t.Fatalf("expected support role applied, got %v", detail.RoleKeys)
	}
}

// TestReplaceRolePermissionsRejectsPermissionSetBeyondActor 验证权限写路径：
// operator 不能把 database.manage 写进任何角色，否则可用该角色重新登录取得它。
func TestReplaceRolePermissionsRejectsPermissionSetBeyondActor(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	ctx := testContext(t)
	before := slices.Clone(store.rolePerms[5])

	err := service.ReplaceRolePermissions(ctx, grantGuardManagerActor(10), "support", []string{
		PermissionUserManage, PermissionDatabaseManage,
	})
	if !errors.Is(err, ErrRoleGrantExceedsActor) {
		t.Fatalf("expected ErrRoleGrantExceedsActor, got %v", err)
	}
	if got := store.rolePerms[5]; !slices.Equal(got, before) {
		t.Fatalf("denied permission write must not reach the store: before=%v after=%v", before, got)
	}
	var fieldsErr *AdminUserFieldsError
	if !errors.As(err, &fieldsErr) {
		t.Fatalf("error must expose *AdminUserFieldsError, got %T", err)
	}
	if fieldsErr.Reason != CodeRoleGrantExceedsActor {
		t.Fatalf("reason=%q, want %q", fieldsErr.Reason, CodeRoleGrantExceedsActor)
	}
	if messages := fieldsErr.Fields[FieldPermissions]; !slices.Contains(messages, CodeRoleGrantExceedsActor) {
		t.Fatalf("permissions messages=%v", messages)
	}
}

// TestReplaceRolePermissionsAllowsPermissionSetInsideActor 覆盖允许侧：写入操作者
// 自己的权限子集必须照常落库。
func TestReplaceRolePermissionsAllowsPermissionSetInsideActor(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	ctx := testContext(t)
	next := []string{PermissionAdminAccess, PermissionUserManage}

	if err := service.ReplaceRolePermissions(ctx, grantGuardManagerActor(10), "support", next); err != nil {
		t.Fatalf("operator must write a permission set inside their own scope, got %v", err)
	}
	if got := store.rolePerms[5]; !slices.Equal(got, next) {
		t.Fatalf("expected role permissions %v, got %v", next, got)
	}
}

// TestReplaceRolePermissionsKeepsSuperAdminLock 回归：权限写路径的既有护栏
// （super_admin 锁、未知权限键）仍先于新的不变量生效。
func TestReplaceRolePermissionsKeepsExistingGuards(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	ctx := testContext(t)

	if err := service.ReplaceRolePermissions(ctx, grantGuardManagerActor(10), RoleSuperAdmin, []string{PermissionAdminAccess}); !errors.Is(err, ErrSystemRoleLocked) {
		t.Fatalf("expected ErrSystemRoleLocked, got %v", err)
	}
	if err := service.ReplaceRolePermissions(ctx, grantGuardManagerActor(10), "support", []string{"ghost.permission"}); !errors.Is(err, ErrInvalidPermission) {
		t.Fatalf("expected ErrInvalidPermission, got %v", err)
	}
}

// TestReplaceUserRolesKeepsUnknownRoleFieldError 回归：checkUserRoleGrant 里
// validateRoles 仍先于权限包含判定执行，未知角色照旧落 roleKeys + role.invalid，
// 不能被新不变量改写成 role.grant_exceeds_actor。
func TestReplaceUserRolesKeepsUnknownRoleFieldError(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	seedGrantGuardTarget(store, 7)

	_, err := service.ReplaceUserRoles(testContext(t), operatorActor(10), 7, []string{"ghost"})
	if !errors.Is(err, ErrInvalidUserUpdate) {
		t.Fatalf("expected ErrInvalidUserUpdate, got %v", err)
	}
	var fieldsErr *AdminUserFieldsError
	if !errors.As(err, &fieldsErr) {
		t.Fatalf("error must expose *AdminUserFieldsError, got %T", err)
	}
	if fieldsErr.Reason != CodeRoleInvalid {
		t.Fatalf("reason=%q, want %q", fieldsErr.Reason, CodeRoleInvalid)
	}
	if messages := fieldsErr.Fields[FieldRoleKeys]; !slices.Contains(messages, MessageRoleUnknown) {
		t.Fatalf("roleKeys messages=%v", messages)
	}
}
