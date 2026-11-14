package identity

import (
	"errors"
	"slices"
	"testing"
)

func seedGrantGuardRoles(t *testing.T, store *fakeStore) {
	t.Helper()
	store.seedRole(Role{ID: 3, Key: RoleTechAdmin, Alias: "技术管理", IsSystem: true, IsEnabled: true})
	store.rolePerms[3] = []string{PermissionAdminAccess, PermissionDatabaseManage}
	store.seedRole(Role{ID: 4, Key: RoleModerator, Alias: "版主", IsSystem: true, IsEnabled: true})
	store.rolePerms[4] = []string{PermissionAdminAccess, PermissionTopicEditAny}
	// 只含操作者自身权限的自建角色：允许。
	store.seedRole(Role{ID: 5, Key: "support", Alias: "客服", IsDeletable: true, IsEnabled: true})
	store.rolePerms[5] = []string{PermissionUserManage}
}

func operatorActor(id int64) Actor {
	return Actor{
		ID:     id,
		Status: UserStatusActive,
		Permissions: map[string]bool{
			PermissionAdminAccess: true,
			PermissionUserView:    true,
			PermissionUserManage:  true,
		},
	}
}

func superAdminActor(id int64) Actor {
	return Actor{ID: id, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
}

func TestRoleGrantGuardCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		actor    Actor
		roleKeys []string
		wantErr  bool
	}{
		{name: "operator cannot mint tech_admin", actor: operatorActor(10), roleKeys: []string{RoleTechAdmin}, wantErr: true},
		{name: "operator cannot mint moderator", actor: operatorActor(10), roleKeys: []string{RoleModerator}, wantErr: true},
		{name: "super admin can grant tech_admin", actor: superAdminActor(1), roleKeys: []string{RoleTechAdmin}},
		{name: "operator can grant the default member role", actor: operatorActor(10), roleKeys: []string{RoleMember}},
		{name: "empty role list is allowed", actor: operatorActor(10)},
		{name: "operator can grant a role inside their own permission set", actor: operatorActor(10), roleKeys: []string{"support"}},
		{name: "unknown role keys stay with validateRoles", actor: operatorActor(10), roleKeys: []string{"ghost"}},
		{
			name:     "one denied role fails the whole grant",
			actor:    operatorActor(10),
			roleKeys: []string{RoleMember, RoleTechAdmin},
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, store := newTestService(t)
			seedGrantGuardRoles(t, store)
			guard := NewRoleGrantGuard(service.store)

			err := guard.Check(testContext(t), tt.actor, tt.roleKeys)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrRoleGrantExceedsActor) {
				t.Fatalf("expected ErrRoleGrantExceedsActor, got %v", err)
			}
			// transport 依赖字段级错误把提示落到 roleKeys 上，而不是落到 500。
			var fieldsErr *AdminUserFieldsError
			if !errors.As(err, &fieldsErr) {
				t.Fatalf("error must expose *AdminUserFieldsError, got %T", err)
			}
			if fieldsErr.Reason != CodeRoleGrantExceedsActor {
				t.Fatalf("reason=%q, want %q", fieldsErr.Reason, CodeRoleGrantExceedsActor)
			}
			if messages := fieldsErr.Fields[FieldRoleKeys]; !slices.Contains(messages, CodeRoleGrantExceedsActor) {
				t.Fatalf("roleKeys messages=%v", messages)
			}
		})
	}
}

func TestRoleGrantGuardNamesTheMissingPermission(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)

	err := NewRoleGrantGuard(service.store).Check(testContext(t), operatorActor(10), []string{RoleTechAdmin})
	var grantErr *RoleGrantExceedsActorError
	if !errors.As(err, &grantErr) {
		t.Fatalf("expected *RoleGrantExceedsActorError, got %v", err)
	}
	if grantErr.RoleKey != RoleTechAdmin || grantErr.Permission != PermissionDatabaseManage {
		t.Fatalf("role=%q permission=%q", grantErr.RoleKey, grantErr.Permission)
	}
}

func TestCreateAdminUserRejectsRoleGrantBeyondActor(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	ctx := testContext(t)
	input := AdminCreateUserInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "correct horse battery staple",
		RoleKeys: []string{RoleTechAdmin},
	}

	_, err := NewAdminUserCreator(service).Create(ctx, operatorActor(10), input)
	if !errors.Is(err, ErrRoleGrantExceedsActor) {
		t.Fatalf("expected ErrRoleGrantExceedsActor, got %v", err)
	}
	if len(store.users) != 0 {
		t.Fatalf("denied grant must not create a user, got %d", len(store.users))
	}

	result, err := NewAdminUserCreator(service).Create(ctx, superAdminActor(1), input)
	if err != nil {
		t.Fatalf("super admin must grant tech_admin, got %v", err)
	}
	if result.User.ID == 0 {
		t.Fatalf("expected created user, got %#v", result)
	}
}

func TestCreateAdminUserKeepsDefaultMemberAndSuperAdminGuards(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	seedGrantGuardRoles(t, store)
	ctx := testContext(t)
	creator := NewAdminUserCreator(service)

	// 空角色列表回落 member：operator 的常规建号不能被新不变量挡住。
	created, err := creator.Create(ctx, operatorActor(10), AdminCreateUserInput{
		Username: "bob",
		Email:    "bob@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("operator must still create default members, got %v", err)
	}
	if created.User.ID == 0 {
		t.Fatalf("expected created user, got %#v", created)
	}

	// 既有 super_admin 保护仍先于权限包含判定生效。
	_, err = creator.Create(ctx, operatorActor(10), AdminCreateUserInput{
		Username: "carol",
		Email:    "carol@example.com",
		Password: "correct horse battery staple",
		RoleKeys: []string{RoleSuperAdmin},
	})
	if !errors.Is(err, ErrSuperAdminGrantRestricted) {
		t.Fatalf("expected ErrSuperAdminGrantRestricted, got %v", err)
	}
}
