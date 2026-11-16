package identity

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestMemberAliasCanChangeButRoleCannotBeDeleted(t *testing.T) {
	service, store := newTestService(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}

	role, err := service.UpdateRole(testContext(t), admin, RoleMember, RoleInput{
		Alias:       "注册用户",
		Description: "所有开放注册用户",
	})
	if err != nil {
		t.Fatalf("UpdateRole returned error: %v", err)
	}
	if role.Key != RoleMember || role.Alias != "注册用户" {
		t.Fatalf("unexpected role after update: %#v", role)
	}

	err = service.DeleteRole(testContext(t), admin, RoleMember)
	if err != ErrDefaultRoleLocked {
		t.Fatalf("expected default role lock, got %v; store=%#v", err, store)
	}
}

func TestBuiltInTemplateRolesCannotBeDeleted(t *testing.T) {
	service, store := newTestService(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	ctx := testContext(t)

	for i, template := range SeedRoleTemplates {
		store.seedRole(Role{
			ID:          int64(10 + i),
			Key:         template.Key,
			Alias:       template.Alias,
			Description: template.Description,
			IsSystem:    true,
			IsDeletable: false,
			IsEnabled:   true,
		})
		if err := service.DeleteRole(ctx, admin, template.Key); err != ErrSystemRoleLocked {
			t.Fatalf("expected system lock for %s, got %v", template.Key, err)
		}
	}
}

func TestBuiltInTemplateRolePermissionsCanBeReplacedExceptSuperAdmin(t *testing.T) {
	service, store := newTestService(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	ctx := testContext(t)

	moderator := SeedRoleTemplates[0]
	store.seedRole(Role{
		ID:          20,
		Key:         moderator.Key,
		Alias:       moderator.Alias,
		IsSystem:    true,
		IsDeletable: false,
		IsEnabled:   true,
	})
	store.rolePerms[20] = append([]string(nil), moderator.PermissionKeys...)

	// 模板角色允许站点微调权限集合（与 super_admin 权限锁定区分）。
	next := []string{PermissionAdminAccess, PermissionModerationReview, PermissionTopicLock}
	if err := service.ReplaceRolePermissions(ctx, admin, RoleModerator, next); err != nil {
		t.Fatalf("ReplaceRolePermissions for moderator returned error: %v", err)
	}
	if got := store.rolePerms[20]; !slices.Equal(got, next) {
		t.Fatalf("expected moderator permissions %v, got %v", next, got)
	}

	if err := service.ReplaceRolePermissions(ctx, admin, RoleSuperAdmin, []string{PermissionAdminAccess}); err != ErrSystemRoleLocked {
		t.Fatalf("expected super_admin permission lock, got %v", err)
	}
}

func TestNonAdminCannotManageRoles(t *testing.T) {
	service, _ := newTestService(t)
	member := Actor{ID: 2, Status: UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.CreateRole(testContext(t), member, RoleInput{
		Key:         "moderator",
		Alias:       "版主",
		Description: "管理内容",
	})
	if err != ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestCreateRoleRejectsBlankRequiredFields(t *testing.T) {
	service, _ := newTestService(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}

	for _, input := range []RoleInput{
		{Key: "", Alias: "版主"},
		{Key: "moderator", Alias: ""},
		{Key: "  ", Alias: "  "},
	} {
		if _, err := service.CreateRole(testContext(t), admin, input); err == nil {
			t.Fatalf("expected CreateRole to reject blank fields for %#v", input)
		}
	}
}

func TestCreateRoleNormalizesInput(t *testing.T) {
	service, _ := newTestService(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}

	role, err := service.CreateRole(testContext(t), admin, RoleInput{
		Key:         " moderator ",
		Alias:       " 版主 ",
		Description: " 管理内容 ",
	})
	if err != nil {
		t.Fatalf("CreateRole returned error: %v", err)
	}
	if role.Key != "moderator" || role.Alias != "版主" || role.Description != "管理内容" {
		t.Fatalf("expected normalized role fields, got %#v", role)
	}
}

func TestDirectAllowAddsUserPermission(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	member := registerMemberForPermissionTest(t, service, ctx)

	detail, err := service.ReplaceUserPermissionOverrides(ctx, admin, member.ID, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if err != nil {
		t.Fatalf("ReplaceUserPermissionOverrides returned error: %v", err)
	}
	if !slices.Contains(detail.Permissions, PermissionAdminAccess) {
		t.Fatalf("expected direct allow to add admin access, got %v", detail.Permissions)
	}
}

func TestDirectDenyOverridesRolePermission(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	member := registerMemberForPermissionTest(t, service, ctx)

	detail, err := service.ReplaceUserPermissionOverrides(ctx, admin, member.ID, PermissionOverrides{
		Deny: []string{PermissionTopicCreate},
	})
	if err != nil {
		t.Fatalf("ReplaceUserPermissionOverrides returned error: %v", err)
	}
	if slices.Contains(detail.Permissions, PermissionTopicCreate) {
		t.Fatalf("expected direct deny to remove topic create, got %v", detail.Permissions)
	}
}

// 越权替换（允许路径）：操作者自己持有的键可以授予目标。
func TestUserPermissionOverrideAllowsGrantActorHolds(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := Actor{ID: 9, Status: UserStatusActive, Permissions: map[string]bool{
		PermissionUserPermissionOverride: true,
		PermissionAdminAccess:            true,
	}}
	member := registerMemberForPermissionTest(t, service, ctx)

	detail, err := service.ReplaceUserPermissionOverrides(ctx, actor, member.ID, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if err != nil {
		t.Fatalf("granting a permission the actor holds must succeed: %v", err)
	}
	if !slices.Contains(detail.Permissions, PermissionAdminAccess) {
		t.Fatalf("expected the override to land, got %v", detail.Permissions)
	}
}

// 越权替换（拒绝路径）：把「自己手里没有」的权限授出去必须被拒——否则
// user.permission_override 持有者可以把别人的权限换成自己可控的集合。
func TestUserPermissionOverrideRejectsGrantActorLacks(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := Actor{ID: 9, Status: UserStatusActive, Permissions: map[string]bool{
		PermissionUserPermissionOverride: true,
	}}
	member := registerMemberForPermissionTest(t, service, ctx)

	_, err := service.ReplaceUserPermissionOverrides(ctx, actor, member.ID, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if !errors.Is(err, ErrRoleGrantExceedsActor) {
		t.Fatalf("expected ErrRoleGrantExceedsActor, got %v", err)
	}
	var typed *RoleGrantExceedsActorError
	if !errors.As(err, &typed) || typed.Field != "permissions" {
		t.Fatalf("expected a field-scoped permission error, got %#v", err)
	}
}

// Deny 不要求操作者先持有该权限：撤销一项自己没有的权限不会扩大任何人的权限集合，
// 而要求持有会让「清理历史遗留 grant」变成不可能的操作。
func TestUserPermissionOverrideDenyDoesNotRequireActorToHold(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := Actor{ID: 9, Status: UserStatusActive, Permissions: map[string]bool{
		PermissionUserPermissionOverride: true,
	}}
	member := registerMemberForPermissionTest(t, service, ctx)

	if _, err := service.ReplaceUserPermissionOverrides(ctx, actor, member.ID, PermissionOverrides{
		Deny: []string{PermissionAdminAccess},
	}); err != nil {
		t.Fatalf("denying a permission the actor does not hold must stay allowed: %v", err)
	}
}

func TestNonAdminCannotManageUserPermissions(t *testing.T) {
	service, _ := newTestService(t)
	member := Actor{ID: 2, Status: UserStatusActive, Permissions: map[string]bool{}}

	_, err := service.ReplaceUserPermissionOverrides(testContext(t), member, 2, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if err != ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestInitialSuperAdminCannotLoseSuperAdminRole(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	adminUser, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	// 用另一个 super_admin（不同 ID）操作 initial super_admin target，避免触发 self-change 检查。
	otherSuperAdmin := Actor{ID: adminUser.ID + 100, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}

	_, err = service.ReplaceUserRoles(ctx, otherSuperAdmin, adminUser.ID, []string{RoleMember})
	if err != ErrInitialSuperAdminLocked {
		t.Fatalf("expected initial super admin lock, got %v", err)
	}
}

func TestUnknownPermissionIsRejected(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	member := registerMemberForPermissionTest(t, service, ctx)

	_, err := service.ReplaceUserPermissionOverrides(ctx, admin, member.ID, PermissionOverrides{
		Allow: []string{"unknown.permission"},
	})
	if err != ErrInvalidPermission {
		t.Fatalf("expected invalid permission, got %v", err)
	}
}

func TestUserPermissionOverrideConflictIsRejected(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	admin := Actor{ID: 1, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	member := registerMemberForPermissionTest(t, service, ctx)

	_, err := service.ReplaceUserPermissionOverrides(ctx, admin, member.ID, PermissionOverrides{
		Allow: []string{PermissionPostCreate},
		Deny:  []string{PermissionPostCreate},
	})
	if err != ErrPermissionOverrideConflict {
		t.Fatalf("expected permission override conflict, got %v", err)
	}
}

func TestSuperAdminPermissionOverridesAreLocked(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	adminUser, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	// 用另一个 super_admin（不同 ID）操作 target，避免 self-change 检查。
	otherSuperAdmin := Actor{ID: adminUser.ID + 100, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}

	_, err = service.ReplaceUserPermissionOverrides(ctx, otherSuperAdmin, adminUser.ID, PermissionOverrides{
		Deny: []string{PermissionAdminAccess},
	})
	if err != ErrSuperAdminOverridesLocked {
		t.Fatalf("expected super admin overrides lock, got %v", err)
	}
}

func registerMemberForPermissionTest(t *testing.T, service *Service, ctx context.Context) CurrentUser {
	t.Helper()
	if _, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	}); err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}
	member, err := service.Register(ctx, RegisterInput{
		Username: "member",
		Email:    "member@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("second Register returned error: %v", err)
	}
	return member
}

// TestUserManagerCannotChangeOwnRoles 验证 H1：持有 user.manage 的非 super_admin
// 不能修改自己的角色，防止自我提权到 super_admin。
func TestUserManagerCannotChangeOwnRoles(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	// manager 有 user.manage 权限但不是 super_admin。
	manager := Actor{ID: 50, Status: UserStatusActive, Permissions: map[string]bool{PermissionUserManage: true}}

	_, err := service.ReplaceUserRoles(ctx, manager, 50, []string{RoleSuperAdmin})
	if err != ErrSelfRoleChange {
		t.Fatalf("expected ErrSelfRoleChange, got %v", err)
	}
}

// TestUserManagerCannotChangeOwnOverrides 验证 H1：禁止改自己的权限覆盖。
func TestUserManagerCannotChangeOwnOverrides(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	// 覆盖编辑需要显式 user.permission_override，不再由 user.manage 继承。
	manager := Actor{ID: 50, Status: UserStatusActive, Permissions: map[string]bool{PermissionUserPermissionOverride: true}}

	_, err := service.ReplaceUserPermissionOverrides(ctx, manager, 50, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if err != ErrSelfRoleChange {
		t.Fatalf("expected ErrSelfRoleChange, got %v", err)
	}
}

// TestUserManageAloneCannotReplacePermissionOverrides 回归：仅有 user.manage
// 的 actor 不能编辑他人权限例外（Critical：运营不得借此提权）。
func TestUserManageAloneCannotReplacePermissionOverrides(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	member := registerMemberForPermissionTest(t, service, ctx)
	manager := Actor{ID: 50, Status: UserStatusActive, Permissions: map[string]bool{PermissionUserManage: true}}

	_, err := service.ReplaceUserPermissionOverrides(ctx, manager, member.ID, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if err != ErrPermissionDenied {
		t.Fatalf("expected ErrPermissionDenied for user.manage only, got %v", err)
	}
}

// TestExplicitPermissionOverrideCanReplace 显式持有 user.permission_override 仍可编辑。
func TestExplicitPermissionOverrideCanReplace(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	member := registerMemberForPermissionTest(t, service, ctx)
	// 新不变量（与改角色权限同源）：授予者必须自己持有被授予的键。
	// 旧版本这里只给 user.permission_override，等于把「越权替换」写成了预期行为。
	manager := Actor{ID: 50, Status: UserStatusActive, Permissions: map[string]bool{
		PermissionUserPermissionOverride: true,
		PermissionAdminAccess:            true,
	}}

	detail, err := service.ReplaceUserPermissionOverrides(ctx, manager, member.ID, PermissionOverrides{
		Allow: []string{PermissionAdminAccess},
	})
	if err != nil {
		t.Fatalf("expected explicit permission_override to succeed, got %v", err)
	}
	if !slices.Contains(detail.PermissionOverrides.Allow, PermissionAdminAccess) {
		t.Fatalf("expected allow override applied, got %#v", detail.PermissionOverrides)
	}
}

// TestNonSuperAdminCannotGrantSuperAdminRole 验证 H1：非 super_admin 的 user.manage
// 持有者不能把 super_admin 角色授予他人（即使 target 不是自己）。
func TestNonSuperAdminCannotGrantSuperAdminRole(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	// 先注册一个首 super_admin（让 store 里有 super_admin 角色），再注册一个普通 member 作为 target。
	_, _ = service.Register(ctx, RegisterInput{
		Username: "firstadmin",
		Email:    "firstadmin@example.com",
		Password: "correct horse battery staple",
	})
	target, err := service.Register(ctx, RegisterInput{
		Username: "target",
		Email:    "target@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	// manager 有 user.manage 但非 super_admin，尝试给 target 加 super_admin。
	manager := Actor{ID: 60, Status: UserStatusActive, Permissions: map[string]bool{PermissionUserManage: true}}

	_, err = service.ReplaceUserRoles(ctx, manager, target.ID, []string{RoleSuperAdmin})
	if err != ErrSuperAdminGrantRestricted {
		t.Fatalf("expected ErrSuperAdminGrantRestricted, got %v", err)
	}
}

// TestSuperAdminCanGrantSuperAdminRole 验证 H1：super_admin 可以把 super_admin 授予他人。
func TestSuperAdminCanGrantSuperAdminRole(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	_, _ = service.Register(ctx, RegisterInput{
		Username: "firstadmin",
		Email:    "firstadmin@example.com",
		Password: "correct horse battery staple",
	})
	target, err := service.Register(ctx, RegisterInput{
		Username: "target2",
		Email:    "target2@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	// 另一个 super_admin（ID 与 target 不同）操作 target。
	superAdmin := Actor{ID: 70, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}

	_, err = service.ReplaceUserRoles(ctx, superAdmin, target.ID, []string{RoleSuperAdmin})
	if err != nil {
		t.Fatalf("expected super_admin to grant super_admin role, got %v", err)
	}
}

// TestUpdateAdminUserRequiresManage 无 user.manage 不得更新账户。
func TestUpdateAdminUserRequiresManage(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	member := registerMemberForPermissionTest(t, service, ctx)
	displayName := "New Name"
	_, err := service.UpdateAdminUser(ctx, Actor{ID: 1, Status: UserStatusActive}, member.ID, AdminUpdateUserInput{
		DisplayName: &displayName,
	})
	if err != ErrPermissionDenied {
		t.Fatalf("expected ErrPermissionDenied, got %v", err)
	}
}

// TestUpdateAdminUserUpdatesAccountAndProfile 可更新展示名、资料与状态。
func TestUpdateAdminUserUpdatesAccountAndProfile(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	member := registerMemberForPermissionTest(t, service, ctx)
	manager := Actor{
		ID:     50,
		Status: UserStatusActive,
		Permissions: map[string]bool{
			PermissionUserManage: true,
			PermissionUserBan:    true,
		},
	}
	displayName := "  Renamed Member  "
	bio := "  hello bio  "
	status := UserStatusDisabled
	detail, err := service.UpdateAdminUser(ctx, manager, member.ID, AdminUpdateUserInput{
		DisplayName: &displayName,
		Bio:         &bio,
		Status:      &status,
	})
	if err != nil {
		t.Fatalf("UpdateAdminUser: %v", err)
	}
	if detail.DisplayName != "Renamed Member" {
		t.Fatalf("displayName = %q", detail.DisplayName)
	}
	if detail.Profile.Bio != "hello bio" {
		t.Fatalf("bio = %q", detail.Profile.Bio)
	}
	if detail.Status != UserStatusDisabled {
		t.Fatalf("status = %s", detail.Status)
	}
}

// TestUpdateAdminUserBanRequiresUserBan 仅 user.manage 不能封禁。
func TestUpdateAdminUserBanRequiresUserBan(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	member := registerMemberForPermissionTest(t, service, ctx)
	manager := Actor{ID: 50, Status: UserStatusActive, Permissions: map[string]bool{PermissionUserManage: true}}
	status := UserStatusBanned
	_, err := service.UpdateAdminUser(ctx, manager, member.ID, AdminUpdateUserInput{Status: &status})
	if err != ErrPermissionDenied {
		t.Fatalf("expected ErrPermissionDenied for ban without user.ban, got %v", err)
	}
}

// TestUpdateAdminUserCannotChangeOwnStatus 禁止改自己的状态，避免自锁。
func TestUpdateAdminUserCannotChangeOwnStatus(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	// 先 bootstrap 首用户，再注册普通会员作为「自己」。
	_, err := service.Register(ctx, RegisterInput{
		Username: "firstadmin",
		Email:    "firstadmin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("bootstrap Register: %v", err)
	}
	self, err := service.Register(ctx, RegisterInput{
		Username: "selfmanager",
		Email:    "selfmanager@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// 显式标记为 super_admin，避免非超管改超管账户的保护抢先拦截。
	manager := Actor{
		ID:          self.ID,
		Status:      UserStatusActive,
		RoleKeys:    []string{RoleSuperAdmin},
		Permissions: map[string]bool{PermissionUserManage: true},
	}
	status := UserStatusDisabled
	_, err = service.UpdateAdminUser(ctx, manager, self.ID, AdminUpdateUserInput{Status: &status})
	if err != ErrSelfStatusChange {
		t.Fatalf("expected ErrSelfStatusChange, got %v", err)
	}
}

// TestNonSuperAdminCannotDemoteNonInitialSuperAdmin 回归：非超管不得摘掉非初始 super_admin。
func TestNonSuperAdminCannotDemoteNonInitialSuperAdmin(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	initial, err := service.Register(ctx, RegisterInput{
		Username: "firstadmin",
		Email:    "firstadmin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register initial admin: %v", err)
	}
	// 再造一个非初始 super_admin 作为 demote 目标。
	target, err := service.Register(ctx, RegisterInput{
		Username: "secondadmin",
		Email:    "secondadmin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register second admin: %v", err)
	}
	if _, err := store.ReplaceUserRoles(ctx, initial.ID, target.ID, []string{RoleSuperAdmin}); err != nil {
		t.Fatalf("seed target as super_admin: %v", err)
	}

	manager := Actor{ID: 60, Status: UserStatusActive, Permissions: map[string]bool{PermissionUserManage: true}}
	_, err = service.ReplaceUserRoles(ctx, manager, target.ID, []string{RoleMember})
	if err != ErrSuperAdminGrantRestricted {
		t.Fatalf("expected ErrSuperAdminGrantRestricted on demote, got %v", err)
	}
}

// TestSuperAdminCanDemoteNonInitialSuperAdmin 超管可以撤销非初始 super_admin。
func TestSuperAdminCanDemoteNonInitialSuperAdmin(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	initial, err := service.Register(ctx, RegisterInput{
		Username: "firstadmin",
		Email:    "firstadmin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register initial admin: %v", err)
	}
	target, err := service.Register(ctx, RegisterInput{
		Username: "secondadmin",
		Email:    "secondadmin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register second admin: %v", err)
	}
	if _, err := store.ReplaceUserRoles(ctx, initial.ID, target.ID, []string{RoleSuperAdmin}); err != nil {
		t.Fatalf("seed target as super_admin: %v", err)
	}

	// 用另一个 super_admin actor（非 target）执行 demote，避免 self-change。
	actor := Actor{ID: initial.ID, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	detail, err := service.ReplaceUserRoles(ctx, actor, target.ID, []string{RoleMember})
	if err != nil {
		t.Fatalf("expected super_admin demote to succeed, got %v", err)
	}
	if slices.Contains(detail.RoleKeys, RoleSuperAdmin) {
		t.Fatalf("expected super_admin removed, got %v", detail.RoleKeys)
	}
}
