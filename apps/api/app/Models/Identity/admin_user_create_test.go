package identity

import (
	"errors"
	"strings"
	"testing"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func TestCreateAdminUserRequiresUserManage(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	ctx := testContext(t)

	member := Actor{ID: 9, Status: UserStatusActive, Permissions: map[string]bool{}}
	_, err := NewAdminUserCreator(service).Create(ctx, member, AdminCreateUserInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "correct horse battery staple",
	})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
	if len(store.users) != 0 {
		t.Fatalf("permission denial must not create a user, got %d users", len(store.users))
	}
}

func TestCreateAdminUserRequiresExactlyOnePasswordMode(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)

	t.Run("both modes", func(t *testing.T) {
		_, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
			Username:         "both",
			Email:            "both@example.com",
			Password:         "correct horse battery staple",
			GeneratePassword: true,
		})
		var invalid *RegisterInvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("expected register invalid, got %v", err)
		}
		if !fieldMessagesContain(invalid.Fields, FieldPassword, MessagePasswordModeConflict) {
			t.Fatalf("fields = %+v", invalid.Fields)
		}
	})

	t.Run("neither mode", func(t *testing.T) {
		_, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
			Username: "none",
			Email:    "none@example.com",
		})
		var invalid *RegisterInvalidError
		if !errors.As(err, &invalid) {
			t.Fatalf("expected register invalid, got %v", err)
		}
		if !fieldMessagesContain(invalid.Fields, FieldPassword, MessagePasswordRequired) {
			t.Fatalf("fields = %+v", invalid.Fields)
		}
	})
}

func TestCreateAdminUserValidatesHostPolicies(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)

	_, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username: "",
		Email:    "not-an-email",
		Password: "short",
	})
	var invalid *RegisterInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected register invalid, got %v", err)
	}
	for field, message := range map[string]string{
		FieldUsername: MessageUsernameRequired,
		FieldEmail:    MessageEmailInvalid,
		FieldPassword: MessagePasswordMin,
	} {
		if !fieldMessagesContain(invalid.Fields, field, message) {
			t.Fatalf("field %s missing %s: %+v", field, message, invalid.Fields)
		}
	}

	longName := strings.Repeat("长", maxAdminDisplayNameLength+1)
	_, err = NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username:    "long-name",
		Email:       "long-name@example.com",
		DisplayName: longName,
		Password:    "correct horse battery staple",
	})
	// 超长显示名现在也是字段级错误：创建弹窗要把提示标在输入框旁。
	// 创建链路沿用 auth.register_invalid（一条响应只能有一个 reason），
	// 与 username/email/password 的错误合并成同一份 fields。
	if !errors.As(err, &invalid) {
		t.Fatalf("expected register invalid for long display name, got %v", err)
	}
	if !fieldMessagesContain(invalid.Fields, FieldDisplayName, MessageDisplayNameTooLong) {
		t.Fatalf("fields = %+v", invalid.Fields)
	}
}

func TestCreateAdminUserAssignsDefaultMemberRole(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)

	result, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}
	if !containsString(result.User.RoleKeys, RoleMember) {
		t.Fatalf("default role missing: %+v", result.User.RoleKeys)
	}
	if result.GeneratedPassword != "" {
		t.Fatalf("manual password mode must not return a generated password")
	}
	if len(store.credentials[result.User.ID]) == 0 {
		t.Fatal("expected a stored password hash")
	}
	ok, err := VerifyPassword("correct horse battery staple", store.credentials[result.User.ID])
	if err != nil || !ok {
		t.Fatalf("stored hash must verify the submitted password: ok=%v err=%v", ok, err)
	}
}

func TestCreateAdminUserRejectsUnknownRole(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)

	_, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "correct horse battery staple",
		RoleKeys: []string{"missing-role"},
	})
	// 未知用户组落到 roleKeys 字段：创建弹窗才能把提示标在用户组选择器旁。
	var roleErr *AdminUserFieldsError
	if !errors.As(err, &roleErr) {
		t.Fatalf("expected AdminUserFieldsError, got %v", err)
	}
	if roleErr.Reason != CodeRoleInvalid {
		t.Fatalf("reason = %q, want %q", roleErr.Reason, CodeRoleInvalid)
	}
	if !fieldMessagesContain(roleErr.Fields, FieldRoleKeys, MessageRoleUnknown) {
		t.Fatalf("fields = %+v", roleErr.Fields)
	}
}

func TestCreateAdminUserProtectsSuperAdminGrant(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)
	input := AdminCreateUserInput{
		Username: "root2",
		Email:    "root2@example.com",
		Password: "correct horse battery staple",
		RoleKeys: []string{RoleSuperAdmin},
	}

	if _, err := NewAdminUserCreator(service).Create(ctx, userManageActor(1), input); !errors.Is(err, ErrSuperAdminGrantRestricted) {
		t.Fatalf("non super admin must not grant super_admin, got %v", err)
	}

	superAdmin := Actor{ID: 2, Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}}
	result, err := NewAdminUserCreator(service).Create(ctx, superAdmin, input)
	if err != nil {
		t.Fatalf("super admin create: %v", err)
	}
	if !containsString(result.User.RoleKeys, RoleSuperAdmin) {
		t.Fatalf("expected super_admin role, got %+v", result.User.RoleKeys)
	}
}

func TestCreateAdminUserGeneratedPassword(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)

	result, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username:         "generated",
		Email:            "generated@example.com",
		GeneratePassword: true,
		EmailVerified:    true,
	})
	if err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}
	if len(result.GeneratedPassword) < RecommendedPasswordMinLength {
		t.Fatalf("generated password too short: %q", result.GeneratedPassword)
	}
	if !result.User.EmailVerified {
		t.Fatal("emailVerified must be carried into the created account")
	}
	ok, err := VerifyPassword(result.GeneratedPassword, store.credentials[result.User.ID])
	if err != nil || !ok {
		t.Fatalf("stored hash must verify the generated password: ok=%v err=%v", ok, err)
	}
	if strings.Contains(store.credentials[result.User.ID], result.GeneratedPassword) {
		t.Fatal("stored hash must not contain the plaintext password")
	}
}

func TestCreateAdminUserMapsConflicts(t *testing.T) {
	t.Parallel()
	service, store := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)
	store.loginIndex["alice"] = 1
	store.loginIndex["alice@example.com"] = 1

	_, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "correct horse battery staple",
	})
	var invalid *RegisterInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected register invalid, got %v", err)
	}
	// 创建也是管理员操作：冲突话术必须用后台语境（不能说「请直接登录」）。
	if !fieldMessagesContain(invalid.Fields, FieldUsername, MessageUserUsernameTaken) ||
		!fieldMessagesContain(invalid.Fields, FieldEmail, MessageUserEmailTaken) {
		t.Fatalf("fields = %+v", invalid.Fields)
	}
}

func TestCreateAdminUserNormalizesInput(t *testing.T) {
	t.Parallel()
	service, _ := newTestService(t)
	ctx := testContext(t)
	actor := userManageActor(1)

	result, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username: "  alice  ",
		Email:    "  alice@example.com  ",
		Password: "correct horse battery staple",
		RoleKeys: []string{" member ", ""},
	})
	if err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}
	if result.User.Username != "alice" {
		t.Fatalf("username = %q", result.User.Username)
	}
	if result.User.DisplayName != "alice" {
		t.Fatalf("blank display name should fall back to username, got %q", result.User.DisplayName)
	}
	if result.User.Locale != "zh-CN" {
		t.Fatalf("locale = %q", result.User.Locale)
	}
}

func TestCreateAdminUserEmitsRegisteredEvent(t *testing.T) {
	t.Parallel()
	_, store := newTestService(t)
	publisher := &fakeIdentityEventPublisher{}
	service := NewServiceWithEvents(store, publisher)
	ctx := testContext(t)
	actor := userManageActor(7)

	result, err := NewAdminUserCreator(service).Create(ctx, actor, AdminCreateUserInput{
		Username: "evented",
		Email:    "evented@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("CreateAdminUser: %v", err)
	}
	envelope, ok := publisher.envelope(appevents.UserRegistered)
	if !ok {
		t.Fatalf("expected %s event, got %+v", appevents.UserRegistered, publisher.names)
	}
	if envelope.ActorUserID != actor.ID {
		t.Fatalf("event actor = %d, want %d", envelope.ActorUserID, actor.ID)
	}
	if envelope.Payload["username"] != result.User.Username {
		t.Fatalf("event payload = %+v", envelope.Payload)
	}
}
