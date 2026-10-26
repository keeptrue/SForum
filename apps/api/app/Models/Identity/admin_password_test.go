package identity

import (
	"errors"
	"strings"
	"testing"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

func TestAdminSetUserPasswordRequiresPermission(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[2] = CurrentUser{ID: 2, Username: "member", Status: UserStatusActive}

	_, err := service.AdminSetUserPassword(ctx, Actor{
		ID: 9, Status: UserStatusActive, Permissions: map[string]bool{},
	}, 2, "a-very-strong-password")
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied without user.manage, got %v", err)
	}
}

func TestAdminSetUserPasswordProtectsSuperAdmin(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[2] = CurrentUser{ID: 2, Username: "sa", Status: UserStatusActive}
	store.userRoleIDs[2] = []int64{1} // super_admin

	_, err := service.AdminSetUserPassword(ctx, userManageActor(1), 2, "a-very-strong-password")
	if !errors.Is(err, ErrSuperAdminSessionLocked) {
		t.Fatalf("expected ErrSuperAdminSessionLocked, got %v", err)
	}
}

func TestAdminSetUserPasswordUpdatesHashAndRevokesSessions(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[2] = CurrentUser{ID: 2, Username: "alice", Status: UserStatusActive}
	store.userRoleIDs[2] = []int64{2} // member
	_ = store.CreateSession(ctx, authsession.SessionRecordInput{
		UserID: 2, SID: "sid-old", DeviceName: "Chrome",
	})

	result, err := service.AdminSetUserPassword(ctx, userManageActor(1), 2, "a-very-strong-password")
	if err != nil {
		t.Fatalf("AdminSetUserPassword: %v", err)
	}
	if result.RevokedSessions != 1 {
		t.Fatalf("expected 1 revoked session, got %d", result.RevokedSessions)
	}
	if store.updatedPasswordUserID != 2 {
		t.Fatalf("expected password update for user 2, got %d", store.updatedPasswordUserID)
	}
	if store.updatedPasswordHash == "" || strings.Contains(store.updatedPasswordHash, "a-very-strong-password") {
		t.Fatalf("expected argon2 hash, got %q", store.updatedPasswordHash)
	}
	if store.tokenVersions[2] < 1 {
		t.Fatalf("expected token version bumped, got %d", store.tokenVersions[2])
	}
	for _, row := range store.sessions {
		if row.userID == 2 && row.revokedAt == nil {
			t.Fatal("expected target sessions revoked")
		}
		if row.userID == 2 && row.revokeReason != RevokeReasonPasswordReset {
			t.Fatalf("expected revoke reason password_reset, got %q", row.revokeReason)
		}
	}
}

func TestAdminSetUserPasswordRejectsWeakPassword(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[2] = CurrentUser{ID: 2, Username: "bob", Status: UserStatusActive}

	_, err := service.AdminSetUserPassword(ctx, userManageActor(1), 2, "short")
	var invalid *RegisterInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected RegisterInvalidError, got %v", err)
	}
}

func TestAdminSetUserPasswordAllowsSelf(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[7] = CurrentUser{ID: 7, Username: "self-admin", Status: UserStatusActive}

	if _, err := service.AdminSetUserPassword(ctx, userManageActor(7), 7, "a-very-strong-password"); err != nil {
		t.Fatalf("self password reset should be allowed: %v", err)
	}
	if store.updatedPasswordUserID != 7 {
		t.Fatalf("expected self password update, got user %d", store.updatedPasswordUserID)
	}
}

func TestAdminSetUserPasswordSuperAdminCanResetSuperAdmin(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[2] = CurrentUser{ID: 2, Username: "target-sa", Status: UserStatusActive}
	store.userRoleIDs[2] = []int64{1}

	actor := Actor{
		ID: 99, Status: UserStatusActive,
		RoleKeys:    []string{RoleSuperAdmin},
		Permissions: map[string]bool{PermissionUserManage: true},
	}
	// IsSuperAdmin() 依赖 RoleKeys 中的 super_admin。
	if !actor.IsSuperAdmin() {
		t.Fatal("test actor should be super_admin")
	}

	if _, err := service.AdminSetUserPassword(ctx, actor, 2, "a-very-strong-password"); err != nil {
		t.Fatalf("super_admin should reset another super_admin: %v", err)
	}
}
