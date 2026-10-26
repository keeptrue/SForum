package identitycontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	"github.com/keeptrue/sforum/apps/api/config"
)

// sessionTestStore 是会话管理 controller 测试用的轻量 store。
// 只实现登录/会话目录相关路径需要的方法，其余返回零值以满足接口。
type sessionTestStore struct {
	mu                      sync.Mutex
	users                   map[int64]identity.CurrentUser
	creds                   map[int64]string
	loginIndex              map[string]int64
	sessions                []sessionTestRow
	loginAudits             int
	emailVerificationTokens int
	nextUserID              int64
}

type sessionTestRow struct {
	userID       int64
	sid          string
	deviceName   string
	browser      string
	os           string
	userAgentRaw string
	ipPrefix     string
	createdAt    time.Time
	lastSeenAt   time.Time
	revokedAt    *time.Time
	revokeReason string
}

func newSessionTestStore() *sessionTestStore {
	return &sessionTestStore{
		users: map[int64]identity.CurrentUser{}, creds: map[int64]string{},
		loginIndex: map[string]int64{}, nextUserID: 1,
	}
}

func (s *sessionTestStore) WithBootstrapTx(ctx context.Context, fn func(context.Context, identity.TxStore) error) error {
	return fn(ctx, sessionTestTxStore{s})
}

func (s *sessionTestStore) AnyUserExists(context.Context) (bool, error) { return len(s.users) > 0, nil }

// UserKindOf：会话路径的 fake 不涉及机器人语义，一律按人类处理。
func (s *sessionTestStore) UserKindOf(context.Context, int64) (identity.UserKind, error) {
	return identity.UserKindHuman, nil
}
func (s *sessionTestStore) FindRegistrationConflicts(_ context.Context, username, email string) (identity.RegistrationConflicts, error) {
	return identity.RegistrationConflicts{
		UsernameTaken: s.loginIndex[lower(username)] != 0,
		EmailTaken:    s.loginIndex[lower(email)] != 0,
	}, nil
}
func (s *sessionTestStore) GetCurrentUser(_ context.Context, userID int64) (identity.CurrentUser, error) {
	u, ok := s.users[userID]
	if !ok {
		return identity.CurrentUser{}, errors.New("user not found")
	}
	return u, nil
}
func (s *sessionTestStore) GetCurrentUserByEmail(_ context.Context, email string) (identity.CurrentUser, error) {
	uid, ok := s.loginIndex[lower(email)]
	if !ok {
		return identity.CurrentUser{}, identity.ErrUserNotFound
	}
	return s.GetCurrentUser(context.Background(), uid)
}
func (s *sessionTestStore) UpdateCurrentUserAppearance(ctx context.Context, userID int64, preference identity.AppearancePreference) (identity.CurrentUser, error) {
	s.mu.Lock()
	user, ok := s.users[userID]
	if ok {
		copy := preference
		user.Appearance = &copy
		s.users[userID] = user
	}
	s.mu.Unlock()
	if !ok {
		return identity.CurrentUser{}, identity.ErrUserNotFound
	}
	return s.GetCurrentUser(ctx, userID)
}
func (s *sessionTestStore) ClearCurrentUserAppearance(ctx context.Context, userID int64) (identity.CurrentUser, error) {
	s.mu.Lock()
	user, ok := s.users[userID]
	if ok {
		user.Appearance = nil
		s.users[userID] = user
	}
	s.mu.Unlock()
	if !ok {
		return identity.CurrentUser{}, identity.ErrUserNotFound
	}
	return s.GetCurrentUser(ctx, userID)
}
func (s *sessionTestStore) GetCredentialByLogin(_ context.Context, login string) (identity.CredentialUser, error) {
	uid, ok := s.loginIndex[lower(login)]
	if !ok {
		return identity.CredentialUser{}, identity.ErrCredentialNotFound
	}
	u := s.users[uid]
	return identity.CredentialUser{CurrentUser: u, PasswordHash: s.creds[uid]}, nil
}
func (s *sessionTestStore) LoadActor(ctx context.Context, userID int64) (identity.Actor, error) {
	u, err := s.GetCurrentUser(ctx, userID)
	if err != nil {
		return identity.Actor{}, err
	}
	perms := map[string]bool{}
	for _, p := range u.Permissions {
		perms[p] = true
	}
	return identity.Actor{ID: u.ID, Status: u.Status, RoleKeys: u.RoleKeys, Permissions: perms}, nil
}

// 会话目录方法
func (s *sessionTestStore) CreateSession(_ context.Context, input authsession.SessionRecordInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	// fasthttp 会复用请求缓冲区：UA 字符串必须克隆后再存，
	// 否则下一次请求会就地改写已落库的指纹。
	userAgentRaw := strings.Clone(input.UserAgentRaw)
	for i, row := range s.sessions {
		if row.sid == input.SID {
			s.sessions[i] = sessionTestRow{
				userID: input.UserID, sid: input.SID, deviceName: input.DeviceName,
				browser: input.Browser, os: input.OS, userAgentRaw: userAgentRaw, ipPrefix: input.IPPrefix,
				createdAt: now, lastSeenAt: now,
			}
			return nil
		}
	}
	s.sessions = append(s.sessions, sessionTestRow{
		userID: input.UserID, sid: input.SID, deviceName: input.DeviceName,
		browser: input.Browser, os: input.OS, userAgentRaw: userAgentRaw, ipPrefix: input.IPPrefix,
		createdAt: now, lastSeenAt: now,
	})
	return nil
}
func (s *sessionTestStore) IsSessionRevoked(_ context.Context, userID int64, sid string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.sessions {
		if row.userID == userID && row.sid == sid {
			return row.revokedAt != nil, nil
		}
	}
	return true, nil
}
func (s *sessionTestStore) ListUserSessions(_ context.Context, userID int64, currentSID string, includeHistory bool, page, perPage int) (identity.SessionListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []identity.SessionRecord{}
	for _, row := range s.sessions {
		if row.userID != userID {
			continue
		}
		if !includeHistory && row.revokedAt != nil {
			continue
		}
		items = append(items, identity.SessionRecord{
			ID: row.sid, DeviceName: row.deviceName, Browser: row.browser, OS: row.os,
			IPPrefix: row.ipPrefix, CreatedAt: row.createdAt, LastSeenAt: row.lastSeenAt,
			IsCurrent: row.sid == currentSID, RevokedAt: row.revokedAt, RevokeReason: row.revokeReason,
		})
	}
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	return identity.SessionListResult{Items: items, Total: int64(len(items)), Page: page, PerPage: perPage}, nil
}
func (s *sessionTestStore) RevokeSession(_ context.Context, userID int64, sid, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, row := range s.sessions {
		if row.userID == userID && row.sid == sid {
			if row.revokedAt != nil {
				return nil
			}
			now := time.Now().UTC()
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = reason
			return nil
		}
	}
	return identity.ErrSessionNotFound
}
func (s *sessionTestStore) RevokeOtherSessions(_ context.Context, userID int64, currentSID, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	count := 0
	for i, row := range s.sessions {
		if row.userID == userID && row.sid != currentSID && row.revokedAt == nil {
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = reason
			count++
		}
	}
	return count, nil
}
func (s *sessionTestStore) RevokeUserSessions(_ context.Context, userID int64, reason string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	count := 0
	for i, row := range s.sessions {
		if row.userID == userID && row.revokedAt == nil {
			s.sessions[i].revokedAt = &now
			s.sessions[i].revokeReason = reason
			count++
		}
	}
	return count, nil
}
func (s *sessionTestStore) ClearUserClientIPs(_ context.Context, _ int64) (identity.ClearUserClientIPsResult, error) {
	return identity.ClearUserClientIPsResult{}, nil
}

func (s *sessionTestStore) DeleteOldRevokedSessions(_ context.Context, keepDays int) (int, error) {
	return 0, nil
}
func (s *sessionTestStore) HasSessionFingerprint(_ context.Context, userID int64, fingerprint string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.sessions {
		if row.userID == userID && row.userAgentRaw == fingerprint && row.revokedAt == nil {
			return true, nil
		}
	}
	return false, nil
}
func (s *sessionTestStore) EnforceMaxSessions(_ context.Context, userID int64, _ string, maxDevices int) (int, error) {
	return 0, nil // 测试不聚焦登录踢出
}
func (s *sessionTestStore) TouchSessionLastSeen(_ context.Context, userID int64, sid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, row := range s.sessions {
		if row.userID == userID && row.sid == sid && row.revokedAt == nil {
			s.sessions[i].lastSeenAt = time.Now().UTC()
			return nil
		}
	}
	return nil
}

// 其余 Store 方法：会话管理测试路径不触发，零值返回。
func (s *sessionTestStore) ListPermissions(context.Context) ([]identity.Permission, error) {
	return nil, nil
}
func (s *sessionTestStore) ListPermissionMatrix(context.Context) (identity.PermissionMatrix, error) {
	return identity.PermissionMatrix{}, nil
}
func (s *sessionTestStore) ListUsers(context.Context, identity.UserListInput) (identity.AdminUserList, error) {
	return identity.AdminUserList{}, nil
}
func (s *sessionTestStore) GetAdminUser(_ context.Context, userID int64) (identity.AdminUserDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		// 部分会话测试只预置 sessions 行、不落 users 表；返回非 super 占位目标以保持旧路径。
		return identity.AdminUserDetail{
			AdminUserSummary: identity.AdminUserSummary{
				ID: userID, Status: identity.UserStatusActive,
			},
		}, nil
	}
	return identity.AdminUserDetail{
		AdminUserSummary: identity.AdminUserSummary{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			Status: u.Status, EmailVerified: u.EmailVerified, RoleKeys: append([]string(nil), u.RoleKeys...),
			IsInitialSuperAdmin: u.IsInitialSuperAdmin,
		},
		Permissions:      append([]string(nil), u.Permissions...),
		Sessions:         []identity.AdminSessionInspect{},
		RecentAuthEvents: []identity.AdminAuthEvent{},
	}, nil
}
func (s *sessionTestStore) GetEmailVerificationTarget(_ context.Context, userID int64) (identity.EmailVerificationTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return identity.EmailVerificationTarget{}, identity.ErrUserNotFound
	}
	return identity.EmailVerificationTarget{UserID: userID, Username: u.Username, Status: u.Status, EmailVerified: u.EmailVerified}, nil
}
func (s *sessionTestStore) CreateEmailVerificationToken(_ context.Context, input identity.CreateEmailVerificationTokenInput) (identity.EmailVerificationToken, error) {
	s.mu.Lock()
	s.emailVerificationTokens++
	s.mu.Unlock()
	return identity.EmailVerificationToken{UserID: input.UserID, Email: input.Email, TokenHash: input.TokenHash}, nil
}
func (s *sessionTestStore) ConfirmEmailVerification(context.Context, string) (int64, error) {
	return 0, identity.ErrEmailVerificationTokenNotFound
}
func (s *sessionTestStore) IsEmailVerified(_ context.Context, userID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return false, identity.ErrUserNotFound
	}
	return u.EmailVerified, nil
}
func (s *sessionTestStore) SetAdminUserEmailVerified(_ context.Context, _ int64, targetUserID int64, verified bool) (identity.AdminUserDetail, error) {
	s.mu.Lock()
	u, ok := s.users[targetUserID]
	if ok {
		u.EmailVerified = verified
		s.users[targetUserID] = u
	}
	s.mu.Unlock()
	if !ok {
		return identity.AdminUserDetail{}, identity.ErrUserNotFound
	}
	return s.GetAdminUser(context.Background(), targetUserID)
}
func (s *sessionTestStore) UpdateAdminUser(context.Context, int64, int64, identity.AdminUpdateUserInput) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (s *sessionTestStore) CreateAdminUser(ctx context.Context, _ int64, input identity.AdminCreateUserStoreInput) (identity.AdminUserDetail, error) {
	s.mu.Lock()
	id := s.nextUserID
	s.nextUserID++
	s.users[id] = identity.CurrentUser{
		ID:            id,
		Username:      input.Username,
		DisplayName:   input.DisplayName,
		Locale:        input.Locale,
		Status:        identity.UserStatusActive,
		EmailVerified: input.EmailVerified,
		RoleKeys:      append([]string(nil), input.RoleKeys...),
	}
	s.loginIndex[lower(input.Username)] = id
	s.loginIndex[lower(input.Email)] = id
	s.creds[id] = input.PasswordHash
	s.mu.Unlock()
	return s.GetAdminUser(ctx, id)
}
func (s *sessionTestStore) ListRoles(context.Context) ([]identity.Role, error) {
	return []identity.Role{
		{ID: 1, Key: identity.RoleSuperAdmin, Alias: "超级管理员", IsSystem: true, IsEnabled: true},
		{ID: 2, Key: identity.RoleMember, Alias: "普通会员", IsSystem: true, IsDefault: true, IsEnabled: true},
	}, nil
}
func (s *sessionTestStore) CreateRole(context.Context, identity.RoleInput) (identity.Role, error) {
	return identity.Role{}, nil
}
func (s *sessionTestStore) UpdateRole(context.Context, string, identity.RoleInput) (identity.Role, error) {
	return identity.Role{}, nil
}
func (s *sessionTestStore) DeleteRole(context.Context, string) error { return nil }
func (s *sessionTestStore) ReplaceRolePermissions(context.Context, int64, string, []string) error {
	return nil
}
func (s *sessionTestStore) ReplaceUserRoles(context.Context, int64, int64, []string) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (s *sessionTestStore) ReplaceUserPermissionOverrides(context.Context, int64, int64, identity.PermissionOverrides) (identity.AdminUserDetail, error) {
	return identity.AdminUserDetail{}, nil
}
func (s *sessionTestStore) RecordLoginAudit(context.Context, identity.LoginAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginAudits++
	return nil
}
func (s *sessionTestStore) CreatePasswordResetToken(context.Context, identity.CreatePasswordResetTokenInput) (identity.PasswordResetToken, error) {
	return identity.PasswordResetToken{}, nil
}
func (s *sessionTestStore) ConsumePasswordResetToken(context.Context, string) (int64, error) {
	return 0, nil
}
func (s *sessionTestStore) ConfirmPasswordResetAtomic(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (s *sessionTestStore) UpdateUserPassword(_ context.Context, userID int64, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return identity.ErrUserNotFound
	}
	s.creds[userID] = passwordHash
	return nil
}
func (s *sessionTestStore) GetUserTokenVersion(context.Context, int64) (int64, error) { return 0, nil }
func (s *sessionTestStore) IncrementUserTokenVersion(context.Context, int64) error    { return nil }

// sessionTestTxStore 仅满足注册 bootstrap tx。
type sessionTestTxStore struct{ store *sessionTestStore }

func (tx sessionTestTxStore) AnyUserExists(context.Context) (bool, error) {
	return len(tx.store.users) > 0, nil
}
func (tx sessionTestTxStore) FindRegistrationConflicts(_ context.Context, username, email string) (identity.RegistrationConflicts, error) {
	return identity.RegistrationConflicts{
		UsernameTaken: tx.store.loginIndex[lower(username)] != 0,
		EmailTaken:    tx.store.loginIndex[lower(email)] != 0,
	}, nil
}
func (tx sessionTestTxStore) CreateUser(ctx context.Context, input identity.CreateUserInput) (identity.CurrentUser, error) {
	tx.store.mu.Lock()
	defer tx.store.mu.Unlock()
	u := identity.CurrentUser{
		ID: tx.store.nextUserID, Username: input.Username, DisplayName: input.DisplayName,
		Locale: input.Locale, Status: identity.UserStatusActive, IsInitialSuperAdmin: input.IsInitialSuperAdmin,
		RoleKeys: []string{identity.RoleMember}, Permissions: []string{identity.PermissionTopicCreate},
	}
	u.Avatar = avatar.NewViewBuilder(nil).AvatarView(ctx, avatar.User{
		UserID:      u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Email:       input.Email,
	}, avatar.Source{})
	tx.store.nextUserID++
	tx.store.users[u.ID] = u
	tx.store.loginIndex[lower(input.Username)] = u.ID
	tx.store.loginIndex[lower(input.Email)] = u.ID
	return u, nil
}
func (tx sessionTestTxStore) CreateCredential(_ context.Context, userID int64, passwordHash string) error {
	tx.store.creds[userID] = passwordHash
	return nil
}
func (tx sessionTestTxStore) GetDefaultRole(context.Context) (identity.Role, error) {
	return identity.Role{ID: 2, Key: identity.RoleMember}, nil
}
func (tx sessionTestTxStore) GetRole(context.Context, string) (identity.Role, error) {
	return identity.Role{ID: 1, Key: identity.RoleSuperAdmin}, nil
}
func (tx sessionTestTxStore) AssignRole(context.Context, int64, int64) error { return nil }
func (tx sessionTestTxStore) LoadCurrentUserAccess(_ context.Context, current *identity.CurrentUser) error {
	return nil
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		}
	}
	return string(b)
}

// newSessionTestApp 构建带真实 session manager + 真实 identity.Service 的 fiber app。
func newSessionTestApp(t *testing.T) (*fiber.App, *sessionTestStore) {
	return newSessionTestAppWithVerification(t, humanverify.NewDisabledService(), nil)
}

func newSessionTestAppWithVerification(t *testing.T, verifier humanverify.Verifier, policy identity.EmailVerificationPolicyResolver) (*fiber.App, *sessionTestStore) {
	t.Helper()
	store := newSessionTestStore()
	service := identity.NewService(store)
	// 真实 Manager（内存 session store），注入测试 store 作为会话目录。
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:   "test-secret",
			SessionStore: store,
			TokenVersion: store.GetUserTokenVersion,
		},
	)
	controller := NewControllerWithAuthSessions(service, manager, verifier).
		WithAppearancePreferences(identity.NewAppearancePreferenceService(store)).
		WithEmailVerification(identity.NewEmailVerificationService(store, nil, policy, identity.EmailVerificationConfig{}))
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app, store
}

type emailVerificationRequiredTestPolicy struct{}

func (emailVerificationRequiredTestPolicy) EmailVerificationPolicy(context.Context) (identity.EmailVerificationPolicy, error) {
	return identity.EmailVerificationPolicy{Required: true}, nil
}

func newDueRenewalSessionTestApp(t *testing.T) (*fiber.App, *sessionTestStore, *atomic.Int32) {
	t.Helper()
	store := newSessionTestStore()
	service := identity.NewService(store)
	gateCalls := &atomic.Int32{}
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:      "due-renewal-test-secret",
			SessionStore:    store,
			TokenVersion:    store.GetUserTokenVersion,
			RenewalInterval: time.Nanosecond,
			RenewalEffectGate: func(context.Context, int64, int64, authsession.RenewalEffect) error {
				gateCalls.Add(1)
				return errors.New("test renewal policy denied")
			},
		},
	)
	controller := NewControllerWithAuthSessions(service, manager, nil)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app, store, gateCalls
}

type loginRiskTestLockout struct {
	required bool
}

func (l *loginRiskTestLockout) IsLocked(context.Context, string, string) (bool, error) {
	return false, nil
}

func (l *loginRiskTestLockout) RequiresVerification(context.Context, string) (bool, error) {
	return l.required, nil
}

func (*loginRiskTestLockout) RecordFailure(context.Context, string, string, int, time.Duration) error {
	return nil
}

func (l *loginRiskTestLockout) ClearFailures(context.Context, string, string) error {
	l.required = false
	return nil
}

type loginRiskTestPolicy struct{}

func (loginRiskTestPolicy) LoginLockoutPolicy(context.Context) (identity.LoginLockoutPolicy, error) {
	return identity.LoginLockoutPolicy{MaxFailures: 5, LockoutMinutes: 15}, nil
}

func newLoginRiskTestApp(t *testing.T) (*fiber.App, *identity.Service, *loginRiskTestLockout) {
	t.Helper()
	store := newSessionTestStore()
	service := identity.NewService(store)
	lockout := &loginRiskTestLockout{}
	service.WithLoginLockout(lockout, loginRiskTestPolicy{})
	manager := authsession.NewManager(
		session.NewStore(session.Config{IdleTimeout: time.Hour}),
		authsession.Config{
			HashSecret:   "test-secret",
			SessionStore: store,
			TokenVersion: store.GetUserTokenVersion,
		},
	)
	verifier := humanverify.NewService(
		humanverify.ServiceConfig{
			Enabled:         true,
			EnabledPurposes: map[humanverify.Purpose]bool{humanverify.PurposeLoginRisk: true},
		},
		fakeAltchaProvider{},
		nil,
	)
	controller := NewControllerWithAuthSessions(service, manager, verifier)
	app := apphttp.NewApp(config.Config{CSRFEnabled: false}, nil, apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app, service, lockout
}

func performLogin(t *testing.T, app *fiber.App, body map[string]any) *nethttp.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/login", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	return resp
}

func TestLoginRiskVerificationRecovery(t *testing.T) {
	app, _, lockout := newLoginRiskTestApp(t)
	_ = registerAndLogin(t, app)
	lockout.required = true

	wrong := performLogin(t, app, map[string]any{
		"login": "alice", "password": "wrong password",
	})
	wrong.Body.Close()
	if wrong.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("wrong password must stay generic, got %d", wrong.StatusCode)
	}

	missing := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
	})
	missing.Body.Close()
	if missing.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("missing verification must be rejected, got %d", missing.StatusCode)
	}

	valid := performLogin(t, app, map[string]any{
		"login": "alice", "password": "correct horse battery staple",
		"humanVerification": map[string]any{"provider": "altcha", "token": "valid-token"},
	})
	valid.Body.Close()
	if valid.StatusCode != nethttp.StatusOK {
		t.Fatalf("valid verification must recover login, got %d", valid.StatusCode)
	}
	if lockout.required {
		t.Fatal("successful recovery must clear account risk")
	}
}

// registerAndLogin 注册一个用户并登录，返回会话 cookie 供后续请求。
func registerAndLogin(t *testing.T, app *fiber.App) *nethttp.Cookie {
	t.Helper()
	regBody, _ := json.Marshal(map[string]any{
		"username": "alice", "email": "alice@example.com", "password": "correct horse battery staple",
	})
	regReq := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/register", bytes.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regResp, err := app.Test(regReq)
	if err != nil {
		t.Fatalf("register request failed: %v", err)
	}
	regResp.Body.Close()
	if regResp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("register expected 201, got %d", regResp.StatusCode)
	}
	if len(regResp.Cookies()) == 0 {
		t.Fatal("expected session cookie after register")
	}
	return regResp.Cookies()[0]
}

func requestEmailVerification(t *testing.T, app *fiber.App, cookie *nethttp.Cookie, body map[string]any) *nethttp.Response {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/email-verification/request", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("email verification request failed: %v", err)
	}
	return resp
}

func TestEmailVerificationMailRequiresExplicitRequest(t *testing.T) {
	app, store := newSessionTestAppWithVerification(t, humanverify.NewDisabledService(), emailVerificationRequiredTestPolicy{})
	cookie := registerAndLogin(t, app)
	if store.emailVerificationTokens != 0 {
		t.Fatalf("registration created %d verification tokens before user action", store.emailVerificationTokens)
	}

	resp := requestEmailVerification(t, app, cookie, map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("explicit request status=%d want 200", resp.StatusCode)
	}
	if store.emailVerificationTokens != 1 {
		t.Fatalf("explicit request created %d verification tokens want 1", store.emailVerificationTokens)
	}
}

func TestEmailVerificationMailRequiresConfiguredHumanVerification(t *testing.T) {
	verifier := humanverify.NewService(humanverify.ServiceConfig{
		Enabled: true,
		EnabledPurposes: map[humanverify.Purpose]bool{
			humanverify.PurposeEmailVerification: true,
		},
	}, fakeAltchaProvider{}, nil)
	app, store := newSessionTestAppWithVerification(t, verifier, emailVerificationRequiredTestPolicy{})
	cookie := registerAndLogin(t, app)

	missing := requestEmailVerification(t, app, cookie, map[string]any{})
	missing.Body.Close()
	if missing.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("missing verification status=%d want 422", missing.StatusCode)
	}
	if store.emailVerificationTokens != 0 {
		t.Fatalf("missing verification created %d tokens", store.emailVerificationTokens)
	}

	verified := requestEmailVerification(t, app, cookie, map[string]any{
		"humanVerification": map[string]any{
			"provider": humanverify.ProviderAltcha,
			"token":    "verified-altcha-payload",
		},
	})
	defer verified.Body.Close()
	if verified.StatusCode != nethttp.StatusOK {
		t.Fatalf("verified request status=%d want 200", verified.StatusCode)
	}
	if store.emailVerificationTokens != 1 {
		t.Fatalf("verified request created %d tokens want 1", store.emailVerificationTokens)
	}
}

// TestListSessionsRequiresAuth 验证未登录访问设备列表返回 401（denied 路径）。
func TestListSessionsRequiresAuth(t *testing.T) {
	app, _ := newSessionTestApp(t)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/auth/sessions", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 unauthenticated, got %d", resp.StatusCode)
	}
}

func TestAppearancePreferenceRequiresAuth(t *testing.T) {
	app, _ := newSessionTestApp(t)
	req := httptest.NewRequest(nethttp.MethodPut, "/api/v1/auth/appearance", bytes.NewBufferString(`{"theme":"ocean_blue","lightBackground":"paper"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 unauthenticated, got %d", resp.StatusCode)
	}
}

func TestAppearancePreferenceSaveValidateAndClear(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)

	put := func(body string) *nethttp.Response {
		req := httptest.NewRequest(nethttp.MethodPut, "/api/v1/auth/appearance", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	invalid := put(`{"theme":"neon","lightBackground":"night_blue"}`)
	invalid.Body.Close()
	if invalid.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422 invalid preference, got %d", invalid.StatusCode)
	}

	saved := put(`{"theme":"CUSTOM:#4F46E5","lightBackground":"PAPER"}`)
	saved.Body.Close()
	if saved.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 saved preference, got %d", saved.StatusCode)
	}
	preference := store.users[1].Appearance
	if preference == nil || preference.Theme != "custom:#4f46e5" || preference.LightBackground != "paper" {
		t.Fatalf("unexpected saved preference: %#v", preference)
	}

	clearReq := httptest.NewRequest(nethttp.MethodDelete, "/api/v1/auth/appearance", nil)
	clearReq.AddCookie(cookie)
	cleared, err := app.Test(clearReq)
	if err != nil {
		t.Fatal(err)
	}
	cleared.Body.Close()
	if cleared.StatusCode != nethttp.StatusOK || store.users[1].Appearance != nil {
		t.Fatalf("clear should restore site inheritance: status=%d preference=%#v", cleared.StatusCode, store.users[1].Appearance)
	}
}

// TestListSessionsReturnsCurrentDevice 验证登录后能获取设备列表并标记当前设备。
func TestListSessionsReturnsCurrentDevice(t *testing.T) {
	app, _ := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/auth/sessions", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var envelope struct {
		Data identity.SessionListResult `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(envelope.Data.Items) != 1 {
		t.Fatalf("expected 1 session, got %d", len(envelope.Data.Items))
	}
	if !envelope.Data.Items[0].IsCurrent {
		t.Fatal("expected the session to be marked isCurrent")
	}
	if envelope.Data.Items[0].ID == "" {
		t.Fatal("expected non-empty opaque session id")
	}
}

// TestRevokeSessionReturns404ForOthersSID 验证越权：下线不属于自己的 sid 返回 404，
// 不泄漏该 sid 是否属于他人（denied 路径）。
func TestRevokeSessionReturns404ForOthersSID(t *testing.T) {
	app, _ := newSessionTestApp(t)
	_ = registerAndLogin(t, app)

	req := httptest.NewRequest(nethttp.MethodDelete, "/api/v1/auth/sessions/someone-else-sid", nil)
	req.Header.Set("Content-Type", "application/json")
	// 删除是 unsafe 方法，但 CSRF 关闭，直接发。
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	// 未带 cookie → 401（先于 404）。
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without cookie, got %d", resp.StatusCode)
	}
}

// TestRevokeOtherSessionsSucceeds 验证下线其他设备返回被下线数。
func TestRevokeOtherSessionsSucceeds(t *testing.T) {
	app, _ := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/auth/sessions/revoke-others", nil)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHostSessionRecoveryControllersBypassDueRenewal(t *testing.T) {
	t.Run("ordinary session control", func(t *testing.T) {
		app, _, gateCalls := newDueRenewalSessionTestApp(t)
		cookie := registerAndLogin(t, app)
		req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/auth/session", nil)
		req.AddCookie(cookie)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("ordinary session request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != nethttp.StatusUnauthorized || gateCalls.Load() != 1 {
			t.Fatalf("ordinary session status=%d renewal calls=%d", resp.StatusCode, gateCalls.Load())
		}
	})

	for _, test := range []struct {
		name       string
		method     string
		path       func(*sessionTestStore) string
		prepare    func(*sessionTestStore)
		wantStatus int
	}{
		{
			name: "logout", method: nethttp.MethodPost,
			path:       func(*sessionTestStore) string { return "/api/v1/auth/logout" },
			wantStatus: nethttp.StatusOK,
		},
		{
			name: "single session", method: nethttp.MethodDelete,
			path: func(store *sessionTestStore) string {
				store.mu.Lock()
				defer store.mu.Unlock()
				return "/api/v1/auth/sessions/" + store.sessions[0].sid
			},
			wantStatus: nethttp.StatusOK,
		},
		{
			name: "other sessions", method: nethttp.MethodPost,
			path:       func(*sessionTestStore) string { return "/api/v1/auth/sessions/revoke-others" },
			wantStatus: nethttp.StatusOK,
		},
		{
			name: "admin revoke all", method: nethttp.MethodPost,
			path: func(*sessionTestStore) string { return "/api/v1/users/2/sessions/revoke" },
			prepare: func(store *sessionTestStore) {
				store.mu.Lock()
				defer store.mu.Unlock()
				actor := store.users[1]
				actor.Permissions = append(actor.Permissions, identity.PermissionUserManage)
				store.users[1] = actor
				store.sessions = append(store.sessions, sessionTestRow{userID: 2, sid: "target-session", lastSeenAt: time.Now()})
			},
			wantStatus: nethttp.StatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, store, gateCalls := newDueRenewalSessionTestApp(t)
			cookie := registerAndLogin(t, app)
			if test.prepare != nil {
				test.prepare(store)
			}
			req := httptest.NewRequest(test.method, test.path(store), nil)
			req.AddCookie(cookie)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("recovery request failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.wantStatus || gateCalls.Load() != 0 {
				t.Fatalf("status=%d want=%d renewal calls=%d", resp.StatusCode, test.wantStatus, gateCalls.Load())
			}
		})
	}
}

// promoteToUserManager 给已注册的测试用户授予 user.manage 权限（绕过 RBAC，直接改 store）。
func promoteToUserManager(t *testing.T, app *fiber.App, store *sessionTestStore, userID int64) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	u := store.users[userID]
	u.Permissions = append(u.Permissions, identity.PermissionUserManage)
	store.users[userID] = u
}

// TestAdminRevokeUserSessionsRequiresAuth 验证未登录调用管理员下线返回 401。
func TestAdminRevokeUserSessionsRequiresAuth(t *testing.T) {
	app, _ := newSessionTestApp(t)
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/users/2/sessions/revoke", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

// TestAdminRevokeUserSessionsForbiddenWithoutPermission 验证无 user.manage 的登录用户被 403 拒绝。
func TestAdminRevokeUserSessionsForbiddenWithoutPermission(t *testing.T) {
	app, _ := newSessionTestApp(t)
	cookie := registerAndLogin(t, app) // member，无 user.manage

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/users/2/sessions/revoke", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without user.manage, got %d", resp.StatusCode)
	}
}

// TestAdminRevokeUserSessionsSucceeds 验证持 user.manage 的管理员能下线目标用户全部设备。
func TestAdminRevokeUserSessionsSucceeds(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app) // 用户 1
	// 授予 user.manage。
	promoteToUserManager(t, app, store, 1)
	// 为用户 1 预置额外设备（确保管理员能下线他人，且用户 2 也有设备可下线）。
	store.mu.Lock()
	store.sessions = append(store.sessions,
		sessionTestRow{userID: 2, sid: "u2-a", lastSeenAt: time.Now()},
		sessionTestRow{userID: 2, sid: "u2-b", lastSeenAt: time.Now()},
	)
	store.mu.Unlock()

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/users/2/sessions/revoke", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			Revoked int `json:"revoked"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.Revoked != 2 {
		t.Fatalf("expected 2 revoked, got %d", envelope.Data.Revoked)
	}
}

// TestAdminRevokeUserSessionsDeniesSelf 验证管理员不能下线自己（400）。
func TestAdminRevokeUserSessionsDeniesSelf(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app) // 用户 1
	promoteToUserManager(t, app, store, 1)

	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/users/1/sessions/revoke", nil)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusBadRequest {
		t.Fatalf("expected 400 when revoking self, got %d", resp.StatusCode)
	}
}

func adminSetUserPasswordRequest(userID int64, password string) *nethttp.Request {
	body, _ := json.Marshal(map[string]string{"password": password})
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/users/"+strconv.FormatInt(userID, 10)+"/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func adminSetEmailVerificationRequest(userID int64, verified bool) *nethttp.Request {
	body, _ := json.Marshal(map[string]bool{"verified": verified})
	req := httptest.NewRequest(nethttp.MethodPut, "/api/v1/users/"+strconv.FormatInt(userID, 10)+"/email-verification", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestAdminSetEmailVerificationRequiresAuth(t *testing.T) {
	app, _ := newSessionTestApp(t)
	resp, err := app.Test(adminSetEmailVerificationRequest(2, true))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

func TestAdminSetEmailVerificationForbiddenWithoutPermission(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)
	seedSessionTestMember(t, store, 2, "member", false)
	req := adminSetEmailVerificationRequest(2, true)
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without user.manage, got %d", resp.StatusCode)
	}
}

func TestAdminSetEmailVerificationUpdatesBothStates(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)
	promoteToUserManager(t, app, store, 1)
	seedSessionTestMember(t, store, 2, "member", false)
	for _, verified := range []bool{true, false} {
		req := adminSetEmailVerificationRequest(2, verified)
		req.AddCookie(cookie)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		if resp.StatusCode != nethttp.StatusOK {
			resp.Body.Close()
			t.Fatalf("verified=%v expected 200, got %d", verified, resp.StatusCode)
		}
		var envelope struct {
			Data identity.AdminUserDetail `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			resp.Body.Close()
			t.Fatalf("decode response: %v", err)
		}
		resp.Body.Close()
		if envelope.Data.EmailVerified != verified {
			t.Fatalf("response verified=%v want=%v", envelope.Data.EmailVerified, verified)
		}
	}
}

// TestAdminSetUserPasswordRequiresAuth 未登录必须 401。
func TestAdminSetUserPasswordRequiresAuth(t *testing.T) {
	app, _ := newSessionTestApp(t)
	req := adminSetUserPasswordRequest(2, "a-very-strong-password")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", resp.StatusCode)
	}
}

// TestAdminSetUserPasswordForbiddenWithoutPermission 登录但无 user.manage 必须 403。
func TestAdminSetUserPasswordForbiddenWithoutPermission(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)
	seedSessionTestMember(t, store, 2, "member", false)

	req := adminSetUserPasswordRequest(2, "a-very-strong-password")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without user.manage, got %d", resp.StatusCode)
	}
}

// TestAdminSetUserPasswordSucceeds 持 user.manage 的管理员可重置普通用户密码并撤销会话。
func TestAdminSetUserPasswordSucceeds(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)
	promoteToUserManager(t, app, store, 1)
	seedSessionTestMember(t, store, 2, "member", false)
	store.mu.Lock()
	store.sessions = append(store.sessions,
		sessionTestRow{userID: 2, sid: "u2-a", lastSeenAt: time.Now()},
		sessionTestRow{userID: 2, sid: "u2-b", lastSeenAt: time.Now()},
	)
	store.mu.Unlock()

	req := adminSetUserPasswordRequest(2, "a-very-strong-password")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var envelope struct {
		Data struct {
			RevokedSessions int `json:"revokedSessions"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.RevokedSessions != 2 {
		t.Fatalf("expected 2 revoked sessions, got %d", envelope.Data.RevokedSessions)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.creds[2] == "" {
		t.Fatal("expected password hash updated for target user")
	}
}

// TestAdminSetUserPasswordDeniesNonSuperAdminTargetingSuperAdmin 非 super_admin 不能重置 super_admin 密码。
func TestAdminSetUserPasswordDeniesNonSuperAdminTargetingSuperAdmin(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)
	promoteToUserManager(t, app, store, 1)
	seedSessionTestMember(t, store, 2, "super_admin", true)

	req := adminSetUserPasswordRequest(2, "a-very-strong-password")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 for super_admin target, got %d", resp.StatusCode)
	}
}

// TestAdminSetUserPasswordAllowsSelf 管理员可重置自己的密码（恢复入口）。
func TestAdminSetUserPasswordAllowsSelf(t *testing.T) {
	app, store := newSessionTestApp(t)
	cookie := registerAndLogin(t, app)
	promoteToUserManager(t, app, store, 1)

	req := adminSetUserPasswordRequest(1, "a-very-strong-password")
	req.AddCookie(cookie)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 for self password reset, got %d", resp.StatusCode)
	}
}

func seedSessionTestMember(t *testing.T, store *sessionTestStore, userID int64, roleKey string, isSuper bool) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	roleKeys := []string{roleKey}
	if isSuper {
		roleKeys = []string{identity.RoleSuperAdmin}
	}
	idText := strconv.FormatInt(userID, 10)
	store.users[userID] = identity.CurrentUser{
		ID: userID, Username: "user-" + idText, DisplayName: "User " + idText,
		Status: identity.UserStatusActive, RoleKeys: roleKeys,
	}
	if store.nextUserID <= userID {
		store.nextUserID = userID + 1
	}
}
