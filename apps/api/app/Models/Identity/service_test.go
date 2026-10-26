package identity

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func TestRegisterFirstUserAssignsSuperAdminAndMember(t *testing.T) {
	service, _ := newTestService(t)

	first, err := service.Register(testContext(t), RegisterInput{
		Username:    "admin",
		Email:       "admin@example.com",
		Password:    "correct horse battery staple",
		DisplayName: "Admin",
		Locale:      "zh-CN",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if !slices.Contains(first.RoleKeys, RoleSuperAdmin) {
		t.Fatalf("expected first user to have super_admin, got %v", first.RoleKeys)
	}
	if !slices.Contains(first.RoleKeys, RoleMember) {
		t.Fatalf("expected first user to have member, got %v", first.RoleKeys)
	}
	if !slices.Contains(first.Permissions, PermissionAdminAccess) {
		t.Fatalf("expected first user to have admin access permission, got %v", first.Permissions)
	}
	if !first.IsInitialSuperAdmin {
		t.Fatal("expected first user to be initial super admin")
	}
	if first.Avatar.Kind != avatar.KindInitials || first.Avatar.Alt != "Admin" {
		t.Fatalf("expected current user initials avatar, got %#v", first.Avatar)
	}
}

func TestRegisterEmitsUserRegisteredEvent(t *testing.T) {
	_, store := newTestService(t)
	publisher := &fakeIdentityEventPublisher{}
	service := NewServiceWithEvents(store, publisher)

	current, err := service.Register(testContext(t), RegisterInput{
		Username: "member",
		Email:    "member@example.com",
		Password: "correct horse battery staple",
		Locale:   "zh-CN",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	// E1.3：先 validate before_register，再 observe registered。
	if !publisher.seen(appevents.UserBeforeRegister) || !publisher.seen(appevents.UserRegistered) {
		t.Fatalf("expected before_register + registered events, got %#v", publisher.names)
	}
	registered, ok := publisher.envelope(appevents.UserRegistered)
	if !ok {
		t.Fatal("missing user.registered envelope")
	}
	if registered.Payload["userId"] != current.ID {
		t.Fatalf("expected payload user id %d, got %#v", current.ID, registered.Payload)
	}
}

// TestRegisterBeforeRegisterCanReject 插件拒绝时不得创建用户，且 password 不进 payload。
func TestRegisterBeforeRegisterCanReject(t *testing.T) {
	_, store := newTestService(t)
	publisher := &fakeIdentityEventPublisher{results: map[string]appevents.Result{
		appevents.UserBeforeRegister: {
			OK:      false,
			Reason:  "registration.disposable_email",
			Message: "请使用常用邮箱注册",
		},
	}}
	service := NewServiceWithEvents(store, publisher)

	_, err := service.Register(testContext(t), RegisterInput{
		Username: "spammer",
		Email:    "user@mailinator.com",
		Password: "correct horse battery staple",
		Locale:   "zh-CN",
	})
	var rejected *appevents.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected RejectedError, got %v", err)
	}
	if rejected.Reason != "registration.disposable_email" {
		t.Fatalf("unexpected reason %q", rejected.Reason)
	}
	if store.userCount() != 0 {
		t.Fatalf("rejected registration must not create users, count=%d", store.userCount())
	}
	if publisher.seen(appevents.UserRegistered) {
		t.Fatal("user.registered must not fire after reject")
	}
	beforeEnv, ok := publisher.envelope(appevents.UserBeforeRegister)
	if !ok {
		t.Fatal("missing user.before_register")
	}
	if beforeEnv.Payload["email"] != "user@mailinator.com" {
		t.Fatalf("expected email in payload, got %#v", beforeEnv.Payload)
	}
	if beforeEnv.Payload["username"] != "spammer" {
		t.Fatalf("expected username in payload, got %#v", beforeEnv.Payload)
	}
	// 安全：密码不得出现在任何事件 payload 中。
	for i, payload := range publisher.payloads {
		for key := range payload {
			if strings.Contains(strings.ToLower(key), "password") {
				t.Fatalf("password-related key %q leaked in event[%d] payload %#v", key, i, payload)
			}
		}
		for _, value := range payload {
			if text, ok := value.(string); ok && strings.Contains(text, "correct horse battery staple") {
				t.Fatalf("password value leaked in event payload %#v", payload)
			}
		}
	}
}

// TestValidateRegisterBeforeRegisterCanReject ValidateRegister 路径同样走插件校验。
func TestValidateRegisterBeforeRegisterCanReject(t *testing.T) {
	_, store := newTestService(t)
	publisher := &fakeIdentityEventPublisher{results: map[string]appevents.Result{
		appevents.UserBeforeRegister: {
			OK:     false,
			Reason: "registration.blocked",
		},
	}}
	service := NewServiceWithEvents(store, publisher)

	err := service.ValidateRegister(testContext(t), RegisterInput{
		Username: "blocked",
		Email:    "blocked@example.com",
		Password: "correct horse battery staple",
		Locale:   "en-US",
	})
	var rejected *appevents.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("expected RejectedError, got %v", err)
	}
	beforeEnv, ok := publisher.envelope(appevents.UserBeforeRegister)
	if !ok {
		t.Fatal("missing user.before_register on ValidateRegister")
	}
	if beforeEnv.Payload["locale"] != "en-US" {
		t.Fatalf("expected locale in payload, got %#v", beforeEnv.Payload)
	}
	if beforeEnv.Kind != appevents.KindValidate {
		t.Fatalf("expected kind=validate, got %q", beforeEnv.Kind)
	}
}

// TestRegisterBeforeRegisterNotInvokedWhenFieldsInvalid 字段非法时不调用插件。
func TestRegisterBeforeRegisterNotInvokedWhenFieldsInvalid(t *testing.T) {
	_, store := newTestService(t)
	publisher := &fakeIdentityEventPublisher{}
	service := NewServiceWithEvents(store, publisher)

	_, err := service.Register(testContext(t), RegisterInput{
		Username: "",
		Email:    "not-an-email",
		Password: "short",
	})
	if err == nil {
		t.Fatal("expected field validation error")
	}
	if publisher.seen(appevents.UserBeforeRegister) {
		t.Fatal("plugin validate must not run on invalid fields")
	}
}

func TestRegisterSecondUserAssignsDefaultMember(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}

	second, err := service.Register(ctx, RegisterInput{
		Username:    "member1",
		Email:       "member1@example.com",
		Password:    "correct horse battery staple",
		DisplayName: "Member One",
		Locale:      "zh-CN",
	})
	if err != nil {
		t.Fatalf("second Register returned error: %v", err)
	}
	if slices.Contains(second.RoleKeys, RoleSuperAdmin) {
		t.Fatalf("expected second user not to have super_admin, got %v", second.RoleKeys)
	}
	if !slices.Contains(second.RoleKeys, RoleMember) {
		t.Fatalf("expected second user to have member, got %v", second.RoleKeys)
	}
	if !slices.Contains(second.Permissions, PermissionTopicCreate) {
		t.Fatalf("expected second user to have topic create permission, got %v", second.Permissions)
	}
	if second.IsInitialSuperAdmin {
		t.Fatal("expected second user not to be initial super admin")
	}
}

// TestRegistrationStatusDoesNotLeakBootstrapState 验证 M4：公开的 registration-status 端点
// 不再返回 NextUserIsInitialSuperAdmin=true，无论系统是否已有用户，
// 消除"首注册窗口、下个注册者成 super_admin"这一首用户劫持的信息面。
func TestRegistrationStatusDoesNotLeakBootstrapState(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	// bootstrap 窗口（无用户）也不暴露 true。
	status, err := service.RegistrationStatus(ctx)
	if err != nil {
		t.Fatalf("RegistrationStatus returned error: %v", err)
	}
	if status.NextUserIsInitialSuperAdmin {
		t.Fatal("expected NextUserIsInitialSuperAdmin to be false before any user exists (no bootstrap leak)")
	}
	if !status.RegistrationEnabled {
		t.Fatal("expected registrationEnabled=true during bootstrap (no users yet)")
	}

	_, err = service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	// 有用户后同样不暴露。
	status, err = service.RegistrationStatus(ctx)
	if err != nil {
		t.Fatalf("RegistrationStatus after register returned error: %v", err)
	}
	if status.NextUserIsInitialSuperAdmin {
		t.Fatal("expected NextUserIsInitialSuperAdmin to be false after a user exists")
	}
	if !status.RegistrationEnabled {
		t.Fatal("expected default open registration after bootstrap")
	}
}

type staticRegistrationPolicy struct {
	enabled bool
	err     error
}

func (p staticRegistrationPolicy) RegistrationEnabled(context.Context) (bool, error) {
	return p.enabled, p.err
}

func TestRegisterRejectedWhenDisabledAfterBootstrap(t *testing.T) {
	_, store := newTestService(t)
	service := NewServiceWithPolicies(store, nil, nil, staticRegistrationPolicy{enabled: false})
	ctx := testContext(t)

	// 无用户时即使开关关闭也允许 bootstrap。
	first, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("bootstrap register should succeed: %v", err)
	}
	if !first.IsInitialSuperAdmin {
		t.Fatal("expected first user to be initial super admin")
	}

	status, err := service.RegistrationStatus(ctx)
	if err != nil {
		t.Fatalf("RegistrationStatus: %v", err)
	}
	if status.RegistrationEnabled {
		t.Fatal("expected registrationEnabled=false after bootstrap when policy is closed")
	}

	_, err = service.Register(ctx, RegisterInput{
		Username: "member2",
		Email:    "member2@example.com",
		Password: "correct horse battery staple",
	})
	if !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatalf("expected ErrRegistrationDisabled, got %v", err)
	}
}

func TestRegisterAllowedWhenEnabled(t *testing.T) {
	_, store := newTestService(t)
	service := NewServiceWithPolicies(store, nil, nil, staticRegistrationPolicy{enabled: true})
	ctx := testContext(t)

	if _, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	}); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := service.Register(ctx, RegisterInput{
		Username: "member2",
		Email:    "member2@example.com",
		Password: "correct horse battery staple",
	}); err != nil {
		t.Fatalf("second register should succeed when open: %v", err)
	}
}

func TestRegisterRejectsInvalidFields(t *testing.T) {
	service, _ := newTestService(t)

	_, err := service.Register(testContext(t), RegisterInput{
		Username: " ",
		Email:    "not-an-email",
		Password: "short",
	})
	fields := registerInvalidFields(t, err)
	expected := FieldMessages{
		FieldUsername: {MessageUsernameRequired},
		FieldEmail:    {MessageEmailInvalid},
		FieldPassword: {MessagePasswordMin},
	}
	if !reflect.DeepEqual(fields, expected) {
		t.Fatalf("expected fields %#v, got %#v", expected, fields)
	}
}

func TestRegisterUsesConfiguredPasswordPolicy(t *testing.T) {
	_, store := newTestService(t)
	service := NewServiceWithPasswordPolicy(store, staticPasswordPolicyResolver{policy: PasswordPolicy{
		MinLength:        8,
		MaxLength:        64,
		RequireUppercase: true,
		RequireNumber:    true,
	}})

	err := service.ValidateRegister(context.Background(), RegisterInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "lowercaseonly",
	})
	fields := registerInvalidFields(t, err)
	if !fieldMessagesContain(fields, FieldPassword, MessagePasswordUppercase) {
		t.Fatalf("expected uppercase policy error, got %#v", fields)
	}
	if !fieldMessagesContain(fields, FieldPassword, MessagePasswordNumber) {
		t.Fatalf("expected number policy error, got %#v", fields)
	}

	_, err = service.Register(context.Background(), RegisterInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "Alice123",
	})
	if err != nil {
		t.Fatalf("expected password matching configured policy to register: %v", err)
	}
}

func TestRegisterRejectsMissingEmail(t *testing.T) {
	service, _ := newTestService(t)

	_, err := service.Register(testContext(t), RegisterInput{
		Username: "admin",
		Email:    " ",
		Password: "correct horse battery staple",
	})
	fields := registerInvalidFields(t, err)
	expected := FieldMessages{FieldEmail: {MessageEmailRequired}}
	if !reflect.DeepEqual(fields, expected) {
		t.Fatalf("expected fields %#v, got %#v", expected, fields)
	}
}

func TestRegisterRejectsDuplicateUsernameAndEmail(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}

	_, err = service.Register(ctx, RegisterInput{
		Username: " admin ",
		Email:    "ADMIN@example.com",
		Password: "correct horse battery staple",
	})
	fields := registerInvalidFields(t, err)
	expected := FieldMessages{
		FieldUsername: {MessageUsernameTaken},
		FieldEmail:    {MessageEmailTaken},
	}
	if !reflect.DeepEqual(fields, expected) {
		t.Fatalf("expected fields %#v, got %#v", expected, fields)
	}
}

func TestRegisterRejectsDuplicateUsername(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}

	_, err = service.Register(ctx, RegisterInput{
		Username: "ADMIN",
		Email:    "new@example.com",
		Password: "correct horse battery staple",
	})
	fields := registerInvalidFields(t, err)
	expected := FieldMessages{FieldUsername: {MessageUsernameTaken}}
	if !reflect.DeepEqual(fields, expected) {
		t.Fatalf("expected fields %#v, got %#v", expected, fields)
	}
}

func TestRegisterRejectsDuplicateEmail(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}

	_, err = service.Register(ctx, RegisterInput{
		Username: "member",
		Email:    "ADMIN@example.com",
		Password: "correct horse battery staple",
	})
	fields := registerInvalidFields(t, err)
	expected := FieldMessages{FieldEmail: {MessageEmailTaken}}
	if !reflect.DeepEqual(fields, expected) {
		t.Fatalf("expected fields %#v, got %#v", expected, fields)
	}
}

func TestRegisterRollsBackWhenCurrentUserAccessCannotLoad(t *testing.T) {
	service, store := newTestService(t)
	store.loadAccessErr = errors.New("load current user access failed")

	_, err := service.Register(testContext(t), RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err == nil {
		t.Fatal("expected Register to return an error")
	}
	if len(store.users) != 0 {
		t.Fatalf("expected user insert to roll back, got %#v", store.users)
	}
	if len(store.credentials) != 0 {
		t.Fatalf("expected credential insert to roll back, got %#v", store.credentials)
	}
	if len(store.loginIndex) != 0 {
		t.Fatalf("expected login index to roll back, got %#v", store.loginIndex)
	}
	if store.nextUserID != 1 {
		t.Fatalf("expected next user id to roll back to 1, got %d", store.nextUserID)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	_, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	_, err = service.Login(ctx, LoginInput{
		Login:    "admin",
		Password: "wrong password",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials, got %v", err)
	}
}

func TestLoginPropagatesCredentialStoreErrors(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	_, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	store.credentialErr = errors.New("load current user access failed")
	_, err = service.Login(ctx, LoginInput{
		Login:    "admin",
		Password: "correct horse battery staple",
	})
	if err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected internal credential loading error, got %v", err)
	}
}

func TestLoginRejectsDisabledUser(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	registered, err := service.Register(ctx, RegisterInput{
		Username: "admin",
		Email:    "admin@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	disabled := store.users[registered.ID]
	disabled.Status = UserStatusDisabled
	store.users[registered.ID] = disabled

	_, err = service.Login(ctx, LoginInput{
		Login:    "admin",
		Password: "correct horse battery staple",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected invalid credentials for disabled user, got %v", err)
	}
}

func TestLoginRiskVerificationAllowsCorrectPasswordRecovery(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)
	registered, err := service.Register(ctx, RegisterInput{
		Username: "victim", Email: "victim@example.com", Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatal(err)
	}
	lockout := &fakeLoginLockout{verificationRequired: true}
	service.WithLoginLockout(lockout, staticLoginLockoutPolicy{})

	_, err = service.Login(ctx, LoginInput{Login: "victim", Password: "wrong password", ClientIP: "198.51.100.10"})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password must remain generic, got %v", err)
	}

	_, err = service.Login(ctx, LoginInput{Login: "victim", Password: "correct horse battery staple", ClientIP: "198.51.100.10"})
	if !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("correct password should require verification, got %v", err)
	}

	current, err := service.Login(ctx, LoginInput{
		Login: "victim", Password: "correct horse battery staple", ClientIP: "198.51.100.10", HumanVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != registered.ID || lockout.clearCalls != 1 {
		t.Fatalf("recovery current=%#v clearCalls=%d", current, lockout.clearCalls)
	}
}

func TestRecordLoginAuditDelegatesToStore(t *testing.T) {
	service, store := newTestService(t)

	err := service.RecordLoginAudit(testContext(t), LoginAudit{
		UserID:      42,
		Action:      AuditActionLogin,
		IPAddress:   "203.0.113.10",
		UserAgent:   "test-agent",
		SessionHash: "hash",
	})
	if err != nil {
		t.Fatalf("RecordLoginAudit returned error: %v", err)
	}
	if len(store.loginAudits) != 1 {
		t.Fatalf("expected one login audit, got %#v", store.loginAudits)
	}
	if store.loginAudits[0].IPAddress != "203.0.113.10" || store.loginAudits[0].SessionHash != "hash" {
		t.Fatalf("unexpected login audit: %#v", store.loginAudits[0])
	}
}

func registerInvalidFields(t *testing.T, err error) FieldMessages {
	t.Helper()
	var registerErr *RegisterInvalidError
	if !errors.As(err, &registerErr) {
		t.Fatalf("expected register invalid error, got %v", err)
	}
	return registerErr.Fields
}

type staticPasswordPolicyResolver struct {
	policy PasswordPolicy
	err    error
}

func (r staticPasswordPolicyResolver) PasswordPolicy(context.Context) (PasswordPolicy, error) {
	return r.policy, r.err
}

func newTestService(t *testing.T) (*Service, *fakeStore) {
	t.Helper()

	store := &fakeStore{
		nextUserID:    1,
		users:         map[int64]CurrentUser{},
		userEmails:    map[int64]string{},
		credentials:   map[int64]string{},
		loginIndex:    map[string]int64{},
		roles:         map[string]Role{},
		roleByID:      map[int64]Role{},
		userRoleIDs:   map[int64][]int64{},
		rolePerms:     map[int64][]string{},
		userOverrides: map[int64]PermissionOverrides{},
		nextCustomID:  100,
	}
	store.seedRole(Role{ID: 1, Key: RoleSuperAdmin, Alias: "超级管理员", IsSystem: true, IsDeletable: false, IsEnabled: true})
	store.seedRole(Role{ID: 2, Key: RoleMember, Alias: "普通会员", IsSystem: true, IsDefault: true, IsDeletable: false, IsEnabled: true})
	store.rolePerms[1] = []string{PermissionAdminAccess, PermissionRoleManage}
	store.rolePerms[2] = []string{PermissionTopicCreate, PermissionPostCreate}

	return NewService(store), store
}

func TestCurrentUserAppearancePreferenceLifecycle(t *testing.T) {
	t.Parallel()
	_, store := newTestService(t)
	service := NewAppearancePreferenceService(store)
	store.users[1] = CurrentUser{ID: 1, Username: "alice", Status: UserStatusActive}

	updated, err := service.Update(context.Background(), 1, AppearancePreference{
		Theme: "custom:#4f46e5", LightBackground: "paper",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Appearance == nil || updated.Appearance.Theme != "custom:#4f46e5" || updated.Appearance.LightBackground != "paper" {
		t.Fatalf("appearance was not persisted: %#v", updated.Appearance)
	}

	cleared, err := service.Clear(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Appearance != nil {
		t.Fatalf("cleared appearance should inherit site defaults: %#v", cleared.Appearance)
	}
}

func TestCurrentUserAppearanceRejectsIncompletePreference(t *testing.T) {
	t.Parallel()
	_, store := newTestService(t)
	service := NewAppearancePreferenceService(store)
	_, err := service.Update(context.Background(), 1, AppearancePreference{Theme: "pine_teal"})
	if !errors.Is(err, ErrInvalidAppearancePreference) {
		t.Fatalf("expected invalid appearance preference, got %v", err)
	}
}

func TestCurrentUserAppearanceNormalizesAndRejectsUnknownValues(t *testing.T) {
	t.Parallel()
	_, store := newTestService(t)
	service := NewAppearancePreferenceService(store)
	store.users[1] = CurrentUser{ID: 1, Username: "alice", Status: UserStatusActive}
	updated, err := service.Update(context.Background(), 1, AppearancePreference{
		Theme: " CUSTOM:#4F46E5 ", LightBackground: " PAPER ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Appearance == nil || updated.Appearance.Theme != "custom:#4f46e5" || updated.Appearance.LightBackground != "paper" {
		t.Fatalf("appearance was not normalized: %#v", updated.Appearance)
	}
	_, err = service.Update(context.Background(), 1, AppearancePreference{
		Theme: "neon", LightBackground: "night_blue",
	})
	if !errors.Is(err, ErrInvalidAppearancePreference) {
		t.Fatalf("expected invalid appearance preference, got %v", err)
	}
}

type fakeIdentityEventPublisher struct {
	names     []string
	payloads  []map[string]any
	envelopes []appevents.Envelope
	results   map[string]appevents.Result
}

func (p *fakeIdentityEventPublisher) Emit(_ context.Context, envelope appevents.Envelope) appevents.Result {
	p.names = append(p.names, envelope.Name)
	p.payloads = append(p.payloads, envelope.Payload)
	p.envelopes = append(p.envelopes, envelope)
	if result, ok := p.results[envelope.Name]; ok {
		return result
	}
	return appevents.Result{OK: true}
}

func (p *fakeIdentityEventPublisher) seen(name string) bool {
	for _, item := range p.names {
		if item == name {
			return true
		}
	}
	return false
}

func (p *fakeIdentityEventPublisher) envelope(name string) (appevents.Envelope, bool) {
	for _, item := range p.envelopes {
		if item.Name == name {
			return item, true
		}
	}
	return appevents.Envelope{}, false
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	return context.Background()
}

type staticLoginLockoutPolicy struct{}

func (staticLoginLockoutPolicy) LoginLockoutPolicy(context.Context) (LoginLockoutPolicy, error) {
	return LoginLockoutPolicy{MaxFailures: 2, LockoutMinutes: 15}, nil
}

type fakeLoginLockout struct {
	locked               bool
	verificationRequired bool
	recordCalls          int
	clearCalls           int
}

func (f *fakeLoginLockout) IsLocked(context.Context, string, string) (bool, error) {
	return f.locked, nil
}

func (f *fakeLoginLockout) RequiresVerification(context.Context, string) (bool, error) {
	return f.verificationRequired, nil
}

func (f *fakeLoginLockout) RecordFailure(context.Context, string, string, int, time.Duration) error {
	f.recordCalls++
	return nil
}

func (f *fakeLoginLockout) ClearFailures(context.Context, string, string) error {
	f.clearCalls++
	f.verificationRequired = false
	return nil
}

type fakeStore struct {
	mu            sync.Mutex
	nextUserID    int64
	nextCustomID  int64
	users         map[int64]CurrentUser
	userEmails    map[int64]string
	userProfiles  map[int64]AdminUserProfile
	credentials   map[int64]string
	loginIndex    map[string]int64
	roles         map[string]Role
	roleByID      map[int64]Role
	userRoleIDs   map[int64][]int64
	rolePerms     map[int64][]string
	userOverrides map[int64]PermissionOverrides
	loginAudits   []LoginAudit
	// kinds 只为机器人语义的用例准备；未声明的账号一律视为人类。
	kinds         map[int64]UserKind
	credentialErr error
	loadAccessErr error
	// 密码重置测试钩子。
	createdResetToken      CreatePasswordResetTokenInput
	consumedResetTokenHash string
	consumeResetTokenErr   error
	consumeResetUserID     int64
	passwordUpdated        bool
	updatedPasswordHash    string
	updatedPasswordUserID  int64
	// 令牌版本号（M8）测试钩子。
	tokenVersions map[int64]int64
	// 会话目录测试钩子。
	sessions       []fakeSessionRow
	revokeCalls    []fakeRevokeCall
	enforceCalls   []int // 每次调用传入的 maxDevices
	sessionRevoked map[string]bool
}

func (s *fakeStore) UpdateCurrentUserAppearance(ctx context.Context, userID int64, preference AppearancePreference) (CurrentUser, error) {
	user, ok := s.users[userID]
	if !ok {
		return CurrentUser{}, ErrUserNotFound
	}
	copy := preference
	user.Appearance = &copy
	s.users[userID] = user
	return s.GetCurrentUser(ctx, userID)
}

func (s *fakeStore) ClearCurrentUserAppearance(ctx context.Context, userID int64) (CurrentUser, error) {
	user, ok := s.users[userID]
	if !ok {
		return CurrentUser{}, ErrUserNotFound
	}
	user.Appearance = nil
	s.users[userID] = user
	return s.GetCurrentUser(ctx, userID)
}

type fakeSessionRow struct {
	userID       int64
	sid          string
	sessionHash  string
	deviceName   string
	browser      string
	os           string
	userAgentRaw string
	ipPrefix     string
	ipAddress    string
	createdAt    time.Time
	lastSeenAt   time.Time
	revokedAt    *time.Time
	revokeReason string
}

type fakeRevokeCall struct {
	userID int64
	sid    string
	reason string
	others bool
}

func (s *fakeStore) seedRole(role Role) {
	s.roles[role.Key] = role
	s.roleByID[role.ID] = role
}

func (s *fakeStore) WithBootstrapTx(ctx context.Context, fn func(context.Context, TxStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.snapshot()
	if err := fn(ctx, s); err != nil {
		s.restore(snapshot)
		return err
	}
	return nil
}

func (s *fakeStore) userCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}

func (s *fakeStore) AnyUserExists(context.Context) (bool, error) {
	return len(s.users) > 0, nil
}

// UserKindOf 默认把账号当人类；需要验证机器人语义的用例通过 kinds 显式声明。
func (s *fakeStore) UserKindOf(_ context.Context, userID int64) (UserKind, error) {
	if kind, ok := s.kinds[userID]; ok {
		return kind, nil
	}
	return UserKindHuman, nil
}

func (s *fakeStore) FindRegistrationConflicts(_ context.Context, username string, email string) (RegistrationConflicts, error) {
	return RegistrationConflicts{
		UsernameTaken: s.loginIndex[strings.ToLower(username)] != 0,
		EmailTaken:    s.loginIndex[strings.ToLower(email)] != 0,
	}, nil
}

func (s *fakeStore) CreateUser(ctx context.Context, input CreateUserInput) (CurrentUser, error) {
	user := CurrentUser{
		ID:                  s.nextUserID,
		Username:            input.Username,
		DisplayName:         input.DisplayName,
		Locale:              input.Locale,
		Status:              UserStatusActive,
		IsInitialSuperAdmin: input.IsInitialSuperAdmin,
	}
	user.Avatar = avatar.NewViewBuilder(nil).AvatarView(ctx, avatar.User{
		UserID:      user.ID,
		Username:    user.Username,
		DisplayName: user.DisplayName,
		Email:       input.Email,
	}, avatar.Source{})
	s.nextUserID++
	s.users[user.ID] = user
	s.userEmails[user.ID] = input.Email
	s.loginIndex[strings.ToLower(input.Username)] = user.ID
	s.loginIndex[strings.ToLower(input.Email)] = user.ID
	return user, nil
}

func (s *fakeStore) CreateCredential(_ context.Context, userID int64, passwordHash string) error {
	s.credentials[userID] = passwordHash
	return nil
}

func (s *fakeStore) GetDefaultRole(context.Context) (Role, error) {
	return s.roles[RoleMember], nil
}

func (s *fakeStore) GetRole(_ context.Context, roleKey string) (Role, error) {
	role, ok := s.roles[roleKey]
	if !ok {
		return Role{}, errors.New("role not found")
	}
	return role, nil
}

func (s *fakeStore) AssignRole(_ context.Context, userID int64, roleID int64) error {
	s.userRoleIDs[userID] = append(s.userRoleIDs[userID], roleID)
	return nil
}

func (s *fakeStore) LoadCurrentUserAccess(_ context.Context, current *CurrentUser) error {
	if s.loadAccessErr != nil {
		return s.loadAccessErr
	}
	*current = s.withAccess(*current)
	return nil
}

func (s *fakeStore) GetCurrentUser(_ context.Context, userID int64) (CurrentUser, error) {
	user, ok := s.users[userID]
	if !ok {
		return CurrentUser{}, errors.New("user not found")
	}
	return s.withAccess(user), nil
}

func (s *fakeStore) GetCurrentUserByEmail(_ context.Context, email string) (CurrentUser, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for userID, stored := range s.userEmails {
		if strings.ToLower(stored) == email {
			return s.GetCurrentUser(context.Background(), userID)
		}
	}
	// 回退 loginIndex（部分测试只写了 loginIndex）。
	if userID, ok := s.loginIndex[email]; ok {
		return s.GetCurrentUser(context.Background(), userID)
	}
	return CurrentUser{}, ErrUserNotFound
}

func (s *fakeStore) GetCredentialByLogin(_ context.Context, login string) (CredentialUser, error) {
	if s.credentialErr != nil {
		return CredentialUser{}, s.credentialErr
	}
	userID, ok := s.loginIndex[strings.ToLower(login)]
	if !ok {
		return CredentialUser{}, ErrCredentialNotFound
	}
	user, err := s.GetCurrentUser(context.Background(), userID)
	if err != nil {
		return CredentialUser{}, err
	}
	return CredentialUser{CurrentUser: user, PasswordHash: s.credentials[userID]}, nil
}

func (s *fakeStore) LoadActor(ctx context.Context, userID int64) (Actor, error) {
	user, err := s.GetCurrentUser(ctx, userID)
	if err != nil {
		return Actor{}, err
	}
	permissions := map[string]bool{}
	for _, permission := range user.Permissions {
		permissions[permission] = true
	}
	return Actor{ID: user.ID, Status: user.Status, RoleKeys: user.RoleKeys, Permissions: permissions}, nil
}

func (s *fakeStore) ListPermissions(context.Context) ([]Permission, error) {
	permissions := make([]Permission, 0, len(SeedPermissions))
	for _, permission := range SeedPermissions {
		permissions = append(permissions, Permission{
			Key:         permission.Key,
			Module:      permission.Module,
			Description: permission.Description,
		})
	}
	return permissions, nil
}

func (s *fakeStore) ListPermissionMatrix(context.Context) (PermissionMatrix, error) {
	permissions, _ := s.ListPermissions(context.Background())
	roles := make([]RolePermissionSet, 0, len(s.roles))
	for _, role := range s.roles {
		roles = append(roles, RolePermissionSet{
			RoleKey:        role.Key,
			PermissionKeys: slices.Clone(s.rolePerms[role.ID]),
		})
	}
	return PermissionMatrix{Permissions: permissions, Roles: roles}, nil
}

func (s *fakeStore) ListUsers(_ context.Context, input UserListInput) (AdminUserList, error) {
	items := make([]AdminUserSummary, 0, len(s.users))
	for _, user := range s.users {
		items = append(items, s.adminSummary(user))
	}
	return AdminUserList{Items: items, Total: int64(len(items)), Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *fakeStore) GetAdminUser(_ context.Context, userID int64) (AdminUserDetail, error) {
	user, ok := s.users[userID]
	if !ok {
		return AdminUserDetail{}, errors.New("user not found")
	}
	user = s.withAccess(user)
	return AdminUserDetail{
		AdminUserSummary:    s.adminSummary(user),
		Permissions:         slices.Clone(user.Permissions),
		PermissionOverrides: s.cloneOverrides(userID),
		Profile:             s.cloneProfile(userID),
		Activity:            AdminUserActivity{},
		Sessions:            []AdminSessionInspect{},
		RecentAuthEvents:    []AdminAuthEvent{},
	}, nil
}

func (s *fakeStore) UpdateAdminUser(_ context.Context, _ int64, targetUserID int64, input AdminUpdateUserInput) (AdminUserDetail, error) {
	user, ok := s.users[targetUserID]
	if !ok {
		return AdminUserDetail{}, ErrUserNotFound
	}
	if input.Username != nil {
		user.Username = *input.Username
		// 重建 login index 中的用户名键。
		for key, id := range s.loginIndex {
			if id == targetUserID && key != strings.ToLower(s.userEmails[targetUserID]) {
				delete(s.loginIndex, key)
			}
		}
		s.loginIndex[strings.ToLower(*input.Username)] = targetUserID
	}
	if input.Email != nil {
		oldEmail := s.userEmails[targetUserID]
		delete(s.loginIndex, strings.ToLower(oldEmail))
		s.userEmails[targetUserID] = *input.Email
		s.loginIndex[strings.ToLower(*input.Email)] = targetUserID
	}
	if input.DisplayName != nil {
		user.DisplayName = *input.DisplayName
	}
	if input.Locale != nil {
		user.Locale = *input.Locale
	}
	if input.Status != nil {
		user.Status = *input.Status
	}
	s.users[targetUserID] = user

	profile := s.userProfiles[targetUserID]
	if input.Bio != nil {
		profile.Bio = *input.Bio
	}
	if input.Signature != nil {
		profile.Signature = *input.Signature
	}
	if input.Location != nil {
		profile.Location = *input.Location
	}
	if input.WebsiteURL != nil {
		profile.WebsiteURL = *input.WebsiteURL
	}
	if s.userProfiles == nil {
		s.userProfiles = map[int64]AdminUserProfile{}
	}
	s.userProfiles[targetUserID] = profile
	return s.GetAdminUser(context.Background(), targetUserID)
}

func (s *fakeStore) ListRoles(context.Context) ([]Role, error) {
	roles := make([]Role, 0, len(s.roles))
	for _, role := range s.roles {
		role.PermissionKeys = slices.Clone(s.rolePerms[role.ID])
		roles = append(roles, role)
	}
	return roles, nil
}

func (s *fakeStore) CreateRole(_ context.Context, input RoleInput) (Role, error) {
	role := Role{ID: s.nextCustomID, Key: input.Key, Alias: input.Alias, Description: input.Description, IsDeletable: true, IsEnabled: true}
	s.nextCustomID++
	s.seedRole(role)
	return role, nil
}

func (s *fakeStore) UpdateRole(_ context.Context, roleKey string, input RoleInput) (Role, error) {
	role := s.roles[roleKey]
	role.Alias = input.Alias
	role.Description = input.Description
	s.seedRole(role)
	return role, nil
}

func (s *fakeStore) DeleteRole(_ context.Context, roleKey string) error {
	delete(s.roles, roleKey)
	return nil
}

func (s *fakeStore) ReplaceRolePermissions(_ context.Context, _ int64, roleKey string, permissions []string) error {
	role := s.roles[roleKey]
	s.rolePerms[role.ID] = permissions
	return nil
}

func (s *fakeStore) ReplaceUserRoles(_ context.Context, _ int64, targetUserID int64, roleKeys []string) (AdminUserDetail, error) {
	roleIDs := make([]int64, 0, len(roleKeys))
	for _, roleKey := range roleKeys {
		role, ok := s.roles[roleKey]
		if !ok {
			return AdminUserDetail{}, errors.New("role not found")
		}
		roleIDs = append(roleIDs, role.ID)
	}
	s.userRoleIDs[targetUserID] = roleIDs
	return s.GetAdminUser(context.Background(), targetUserID)
}

func (s *fakeStore) ReplaceUserPermissionOverrides(_ context.Context, _ int64, targetUserID int64, overrides PermissionOverrides) (AdminUserDetail, error) {
	s.userOverrides[targetUserID] = PermissionOverrides{
		Allow: slices.Clone(overrides.Allow),
		Deny:  slices.Clone(overrides.Deny),
	}
	return s.GetAdminUser(context.Background(), targetUserID)
}

func (s *fakeStore) RecordLoginAudit(_ context.Context, input LoginAudit) error {
	s.loginAudits = append(s.loginAudits, input)
	return nil
}

func (s *fakeStore) CreatePasswordResetToken(_ context.Context, input CreatePasswordResetTokenInput) (PasswordResetToken, error) {
	s.createdResetToken = input
	return PasswordResetToken{
		ID:        1,
		UserID:    input.UserID,
		TokenHash: input.TokenHash,
		ExpiresAt: input.ExpiresAt,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *fakeStore) ConsumePasswordResetToken(_ context.Context, tokenHash string) (int64, error) {
	if s.consumeResetTokenErr != nil {
		return 0, s.consumeResetTokenErr
	}
	s.consumedResetTokenHash = tokenHash
	s.passwordUpdated = true
	return s.consumeResetUserID, nil
}

func (s *fakeStore) UpdateUserPassword(_ context.Context, userID int64, passwordHash string) error {
	s.updatedPasswordHash = passwordHash
	s.updatedPasswordUserID = userID
	return nil
}

func (s *fakeStore) ConfirmPasswordResetAtomic(_ context.Context, tokenHash string, passwordHash string, revokeReason string) (int64, error) {
	userID, err := s.ConsumePasswordResetToken(context.Background(), tokenHash)
	if err != nil {
		return 0, err
	}
	if err := s.UpdateUserPassword(context.Background(), userID, passwordHash); err != nil {
		return 0, err
	}
	_ = s.IncrementUserTokenVersion(context.Background(), userID)
	_, _ = s.RevokeUserSessions(context.Background(), userID, revokeReason)
	return userID, nil
}

// GetUserTokenVersion 返回内存中记录的令牌版本号（M8 测试用）。
func (s *fakeStore) GetUserTokenVersion(_ context.Context, userID int64) (int64, error) {
	if s.tokenVersions == nil {
		return 0, nil
	}
	return s.tokenVersions[userID], nil
}

// IncrementUserTokenVersion 递增内存令牌版本号（M8 测试用）。
func (s *fakeStore) IncrementUserTokenVersion(_ context.Context, userID int64) error {
	if s.tokenVersions == nil {
		s.tokenVersions = map[int64]int64{}
	}
	s.tokenVersions[userID]++
	return nil
}

func (s *fakeStore) CreateSession(_ context.Context, input authsession.SessionRecordInput) error {
	now := time.Now().UTC()
	// 对齐 PostgresStore 的 ON CONFLICT (sid) DO NOTHING：sid 碰撞时返回错误而非覆盖。
	for _, row := range s.sessions {
		if row.sid == input.SID {
			return errors.New("create user session: sid already exists (collision or duplicate)")
		}
	}
	s.sessions = append(s.sessions, fakeSessionRow{
		userID: input.UserID, sid: input.SID, sessionHash: input.SessionHash,
		deviceName: input.DeviceName, browser: input.Browser, os: input.OS,
		userAgentRaw: input.UserAgentRaw, ipPrefix: input.IPPrefix, ipAddress: input.IPAddress,
		createdAt: now, lastSeenAt: now,
	})
	return nil
}

func (s *fakeStore) IsSessionRevoked(_ context.Context, userID int64, sid string) (bool, error) {
	for _, row := range s.sessions {
		if row.userID == userID && row.sid == sid {
			return row.revokedAt != nil, nil
		}
	}
	return true, nil // 不存在则保守视为已撤销
}

func (s *fakeStore) ListUserSessions(_ context.Context, userID int64, currentSID string, includeHistory bool, page int, perPage int) (SessionListResult, error) {
	page, perPage = normalizeSessionPage(page, perPage)
	var filtered []fakeSessionRow
	for _, row := range s.sessions {
		if row.userID != userID {
			continue
		}
		if !includeHistory && row.revokedAt != nil {
			continue
		}
		filtered = append(filtered, row)
	}
	total := int64(len(filtered))
	items := []SessionRecord{}
	for _, row := range filtered {
		items = append(items, SessionRecord{
			ID: row.sid, DeviceName: row.deviceName, Browser: row.browser, OS: row.os,
			IPPrefix: row.ipPrefix, CreatedAt: row.createdAt, LastSeenAt: row.lastSeenAt,
			IsCurrent: row.sid == currentSID, RevokedAt: row.revokedAt, RevokeReason: row.revokeReason,
		})
	}
	return SessionListResult{Items: items, Total: total, Page: page, PerPage: perPage}, nil
}

func (s *fakeStore) RevokeSession(_ context.Context, userID int64, sid string, reason string) error {
	s.revokeCalls = append(s.revokeCalls, fakeRevokeCall{userID: userID, sid: sid, reason: reason})
	for i, row := range s.sessions {
		if row.userID == userID && row.sid == sid {
			if row.revokedAt != nil {
				return nil // 已下线，幂等
			}
			now := time.Now().UTC()
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = reason
			return nil
		}
	}
	return ErrSessionNotFound
}

func (s *fakeStore) RevokeOtherSessions(_ context.Context, userID int64, currentSID string, reason string) (int, error) {
	s.revokeCalls = append(s.revokeCalls, fakeRevokeCall{userID: userID, sid: currentSID, reason: reason, others: true})
	count := 0
	now := time.Now().UTC()
	for i, row := range s.sessions {
		if row.userID == userID && row.sid != currentSID && row.revokedAt == nil {
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = reason
			count++
		}
	}
	return count, nil
}

func (s *fakeStore) RevokeUserSessions(_ context.Context, userID int64, reason string) (int, error) {
	s.revokeCalls = append(s.revokeCalls, fakeRevokeCall{userID: userID, reason: reason})
	count := 0
	now := time.Now().UTC()
	for i, row := range s.sessions {
		if row.userID == userID && row.revokedAt == nil {
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = reason
			count++
		}
	}
	return count, nil
}

func (s *fakeStore) ClearUserClientIPs(_ context.Context, userID int64) (ClearUserClientIPsResult, error) {
	// 测试桩：统计并清空会话行上的 IP 字段。
	var result ClearUserClientIPsResult
	for i := range s.sessions {
		if s.sessions[i].userID != userID {
			continue
		}
		if s.sessions[i].ipPrefix != "" || s.sessions[i].ipAddress != "" {
			s.sessions[i].ipPrefix = ""
			s.sessions[i].ipAddress = ""
			result.SessionsCleared++
		}
	}
	return result, nil
}

func (s *fakeStore) DeleteOldRevokedSessions(_ context.Context, keepDays int) (int, error) {
	if keepDays < 1 {
		keepDays = 30
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -keepDays)
	kept := s.sessions[:0]
	deleted := 0
	for _, row := range s.sessions {
		if row.revokedAt != nil && row.revokedAt.Before(cutoff) {
			deleted++
			continue
		}
		kept = append(kept, row)
	}
	s.sessions = kept
	return deleted, nil
}

func (s *fakeStore) EnforceMaxSessions(_ context.Context, userID int64, currentSID string, maxDevices int) (int, error) {
	s.enforceCalls = append(s.enforceCalls, maxDevices)
	if maxDevices <= 0 {
		return 0, nil
	}
	if currentSID == "" {
		return 0, nil
	}
	// 收集除 currentSID 外的该用户活跃会话，按 lastSeenAt 排序。
	var active []fakeSessionRow
	for _, row := range s.sessions {
		if row.userID == userID && row.revokedAt == nil && row.sid != currentSID {
			active = append(active, row)
		}
	}
	if len(active)+1 <= maxDevices {
		return 0, nil
	}
	// 按 lastSeenAt 降序排序（最新在前）。
	for i := 0; i < len(active)-1; i++ {
		for j := i + 1; j < len(active); j++ {
			if active[j].lastSeenAt.After(active[i].lastSeenAt) {
				active[i], active[j] = active[j], active[i]
			}
		}
	}
	// 保留最新的 maxDevices-1 个，踢掉其余的。
	kicked := active[maxDevices-1:]
	kickedSet := map[string]bool{}
	for _, row := range kicked {
		kickedSet[row.sid] = true
	}
	now := time.Now().UTC()
	count := 0
	for i, row := range s.sessions {
		if row.userID == userID && kickedSet[row.sid] {
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = RevokeReasonMaxExceeded
			count++
		}
	}
	return count, nil
}

func (s *fakeStore) TouchSessionLastSeen(_ context.Context, userID int64, sid string) error {
	for i, row := range s.sessions {
		if row.userID == userID && row.sid == sid && row.revokedAt == nil {
			s.sessions[i].lastSeenAt = time.Now().UTC()
			return nil
		}
	}
	return nil
}

func (s *fakeStore) HasSessionFingerprint(_ context.Context, userID int64, fingerprint string) (bool, error) {
	for _, row := range s.sessions {
		if row.userID == userID && row.userAgentRaw == fingerprint && row.revokedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) withAccess(user CurrentUser) CurrentUser {
	roleIDs := s.userRoleIDs[user.ID]
	roleKeys := make([]string, 0, len(roleIDs))
	permissionSet := map[string]bool{}
	for _, roleID := range roleIDs {
		role := s.roleByID[roleID]
		roleKeys = append(roleKeys, role.Key)
		for _, permission := range s.rolePerms[roleID] {
			permissionSet[permission] = true
		}
	}
	overrides := s.userOverrides[user.ID]
	for _, permission := range overrides.Allow {
		permissionSet[permission] = true
	}
	for _, permission := range overrides.Deny {
		delete(permissionSet, permission)
	}
	permissions := make([]string, 0, len(permissionSet))
	for permission := range permissionSet {
		permissions = append(permissions, permission)
	}
	user.RoleKeys = roleKeys
	user.Permissions = permissions
	return user
}

func (s *fakeStore) adminSummary(user CurrentUser) AdminUserSummary {
	user = s.withAccess(user)
	return AdminUserSummary{
		ID:                  user.ID,
		Username:            user.Username,
		Email:               s.userEmails[user.ID],
		EmailVerified:       user.EmailVerified,
		DisplayName:         user.DisplayName,
		Locale:              user.Locale,
		Status:              user.Status,
		IsInitialSuperAdmin: user.IsInitialSuperAdmin,
		RoleKeys:            slices.Clone(user.RoleKeys),
	}
}

func (s *fakeStore) cloneOverrides(userID int64) PermissionOverrides {
	overrides := s.userOverrides[userID]
	return PermissionOverrides{
		Allow: slices.Clone(overrides.Allow),
		Deny:  slices.Clone(overrides.Deny),
	}
}

func (s *fakeStore) cloneProfile(userID int64) AdminUserProfile {
	if s.userProfiles == nil {
		return AdminUserProfile{}
	}
	return s.userProfiles[userID]
}

type fakeStoreSnapshot struct {
	nextUserID    int64
	nextCustomID  int64
	users         map[int64]CurrentUser
	userEmails    map[int64]string
	credentials   map[int64]string
	loginIndex    map[string]int64
	roles         map[string]Role
	roleByID      map[int64]Role
	userRoleIDs   map[int64][]int64
	rolePerms     map[int64][]string
	userOverrides map[int64]PermissionOverrides
}

func (s *fakeStore) snapshot() fakeStoreSnapshot {
	return fakeStoreSnapshot{
		nextUserID:    s.nextUserID,
		nextCustomID:  s.nextCustomID,
		users:         cloneIntCurrentUserMap(s.users),
		userEmails:    cloneIntStringMap(s.userEmails),
		credentials:   cloneIntStringMap(s.credentials),
		loginIndex:    cloneStringIntMap(s.loginIndex),
		roles:         cloneStringRoleMap(s.roles),
		roleByID:      cloneIntRoleMap(s.roleByID),
		userRoleIDs:   cloneIntIntSliceMap(s.userRoleIDs),
		rolePerms:     cloneIntStringSliceMap(s.rolePerms),
		userOverrides: cloneIntOverridesMap(s.userOverrides),
	}
}

func (s *fakeStore) restore(snapshot fakeStoreSnapshot) {
	s.nextUserID = snapshot.nextUserID
	s.nextCustomID = snapshot.nextCustomID
	s.users = snapshot.users
	s.userEmails = snapshot.userEmails
	s.credentials = snapshot.credentials
	s.loginIndex = snapshot.loginIndex
	s.roles = snapshot.roles
	s.roleByID = snapshot.roleByID
	s.userRoleIDs = snapshot.userRoleIDs
	s.rolePerms = snapshot.rolePerms
	s.userOverrides = snapshot.userOverrides
}

func cloneIntCurrentUserMap(input map[int64]CurrentUser) map[int64]CurrentUser {
	output := make(map[int64]CurrentUser, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneIntStringMap(input map[int64]string) map[int64]string {
	output := make(map[int64]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneStringIntMap(input map[string]int64) map[string]int64 {
	output := make(map[string]int64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneStringRoleMap(input map[string]Role) map[string]Role {
	output := make(map[string]Role, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneIntRoleMap(input map[int64]Role) map[int64]Role {
	output := make(map[int64]Role, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneIntIntSliceMap(input map[int64][]int64) map[int64][]int64 {
	output := make(map[int64][]int64, len(input))
	for key, value := range input {
		output[key] = slices.Clone(value)
	}
	return output
}

func cloneIntStringSliceMap(input map[int64][]string) map[int64][]string {
	output := make(map[int64][]string, len(input))
	for key, value := range input {
		output[key] = slices.Clone(value)
	}
	return output
}

func cloneIntOverridesMap(input map[int64]PermissionOverrides) map[int64]PermissionOverrides {
	output := make(map[int64]PermissionOverrides, len(input))
	for key, value := range input {
		output[key] = PermissionOverrides{
			Allow: slices.Clone(value.Allow),
			Deny:  slices.Clone(value.Deny),
		}
	}
	return output
}

// 机器人账号不允许交互式登录：它由系统驱动，可登录的机器人等于给所有人开了一个
// 冒充 AI 的入口。恢复为人类后必须能重新登录，否则这是一个单向操作。
func TestBotAccountCannotSignIn(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	user, err := service.Register(ctx, RegisterInput{
		Username: "sforum_ai",
		Email:    "ai@example.com",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if _, err := service.Login(ctx, LoginInput{Login: "sforum_ai", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("a human account should sign in: %v", err)
	}

	store.kinds = map[int64]UserKind{user.ID: UserKindBot}
	if _, err := service.Login(ctx, LoginInput{Login: "sforum_ai", Password: "correct horse battery staple"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("a bot account must not sign in, got %v", err)
	}

	store.kinds[user.ID] = UserKindHuman
	if _, err := service.Login(ctx, LoginInput{Login: "sforum_ai", Password: "correct horse battery staple"}); err != nil {
		t.Fatalf("restoring the account to human must restore sign-in: %v", err)
	}
}

// normalize 必须把校验后的 kind 写进返回值。它的错误形态很隐蔽：校验函数返回
// 的是新的结构，调用方从不读 input，所以写回 input 会让这次更新静默失效——
// 保存返回 200，值却没变。这正是它被发现的方式。
func TestNormalizeAdminUpdateUserInputCarriesKind(t *testing.T) {
	service, _ := newTestService(t)
	ctx := testContext(t)

	bot := UserKindBot
	out, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{Kind: &bot})
	if err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	if out.Kind == nil || *out.Kind != UserKindBot {
		t.Fatalf("normalized kind must survive into the result, got %+v", out.Kind)
	}

	// 非法值必须被拒绝，而不是静默忽略后当作未提交处理。
	invalid := UserKind("robot")
	if _, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{Kind: &invalid}); !errors.Is(err, ErrInvalidUserUpdate) {
		t.Fatalf("an invalid kind must be rejected, got %v", err)
	}

	// 空白与大小写差异应当被规范化，而不是当成另一个值。
	spaced := UserKind("  BOT  ")
	normalized, err := service.normalizeAdminUpdateUserInput(ctx, AdminUpdateUserInput{Kind: &spaced})
	if err != nil {
		t.Fatalf("normalize returned error: %v", err)
	}
	if normalized.Kind == nil || *normalized.Kind != UserKindBot {
		t.Fatalf("kind should be trimmed and lowercased by validation, got %+v", normalized.Kind)
	}
}
