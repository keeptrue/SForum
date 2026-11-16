package identity

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type staticLoginVerificationPolicy struct {
	policy LoginVerificationPolicy
}

func (p staticLoginVerificationPolicy) LoginVerificationPolicy(context.Context) (LoginVerificationPolicy, error) {
	return p.policy, nil
}

type failingLoginVerificationPolicy struct{}

func (failingLoginVerificationPolicy) LoginVerificationPolicy(context.Context) (LoginVerificationPolicy, error) {
	return LoginVerificationPolicy{}, errors.New("options unavailable")
}

// erroringLoginLockout 让失败锁定读取返回错误，模拟 Redis 语句超时/连接池饱和。
type erroringLoginLockout struct {
	*fakeLoginLockout
	err error
}

func (l erroringLoginLockout) RequiresVerification(context.Context, string) (bool, error) {
	return false, l.err
}

// failingFingerprintStore 让会话指纹查询返回错误，模拟会话表读故障（F10-2）。
type failingFingerprintStore struct {
	*fakeStore
}

func (failingFingerprintStore) HasSessionFingerprint(context.Context, int64, string) (bool, error) {
	return false, errors.New("session table unavailable")
}

const loginVerificationTestPassword = "correct horse battery staple"

func newLoginVerificationTest(t *testing.T, policy LoginVerificationPolicyResolver) (*Service, *fakeStore, CurrentUser) {
	t.Helper()
	service, store := newTestService(t)
	registered, err := service.Register(testContext(t), RegisterInput{
		Username: "victim", Email: "victim@example.com", Password: loginVerificationTestPassword,
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	service.WithLoginVerification(policy)
	return service, store, registered
}

func TestLoginVerificationAlwaysRequiresVerification(t *testing.T) {
	service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, Always: true},
	})
	ctx := testContext(t)

	if _, err := service.Login(ctx, LoginInput{Login: "victim", Password: loginVerificationTestPassword}); !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("expected verification requirement, got %v", err)
	}
	current, err := service.Login(ctx, LoginInput{
		Login: "victim", Password: loginVerificationTestPassword, HumanVerified: true,
	})
	if err != nil {
		t.Fatalf("verified login returned error: %v", err)
	}
	if current.Username != "victim" {
		t.Fatalf("unexpected current user: %#v", current)
	}
}

func TestLoginVerificationGateOffIgnoresTriggers(t *testing.T) {
	service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Always: true, NewDevice: true, Privileged: true},
	})
	if _, err := service.Login(testContext(t), LoginInput{Login: "victim", Password: loginVerificationTestPassword}); err != nil {
		t.Fatalf("gate off must not require verification, got %v", err)
	}
}

func TestLoginVerificationSkipFlagBypassesTriggers(t *testing.T) {
	service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, Always: true},
	})
	if _, err := service.Login(testContext(t), LoginInput{
		Login: "victim", Password: loginVerificationTestPassword, SkipHumanVerification: true,
	}); err != nil {
		t.Fatalf("skip flag must bypass verification, got %v", err)
	}
}

// TestLoginVerificationSkipScopeExcludesLockout 固定 SkipHumanVerification 的豁免范围（P2-1）。
//
// `issueApiToken` 那条路径会置 SkipHumanVerification，它过去短路在**失败锁定判定之前**，
// 于是携带形状合法 `"issueApiToken":{...}` 的登录请求连账号失败阈值触发的
// login_verify:acct 验证都能跳过。现在豁免只覆盖策略类触发条件。
//
// 变异要求：把 loginRequiresVerification 里的 skip 短路移回函数最前面，
// 第一行（skip + lockout）必须红。
func TestLoginVerificationSkipScopeExcludesLockout(t *testing.T) {
	tests := []struct {
		name        string
		policy      LoginVerificationPolicy
		lockout     bool
		skip        bool
		wantRequire bool
	}{
		{
			// 失败锁定触发的验证永远不被豁免。
			name:        "skip does not bypass lockout",
			policy:      LoginVerificationPolicy{Enabled: true, Always: true},
			lockout:     true,
			skip:        true,
			wantRequire: true,
		},
		{
			// 策略类触发条件仍然被豁免（原生/CLI 客户端的既有取舍）。
			name:        "skip still bypasses always",
			policy:      LoginVerificationPolicy{Enabled: true, Always: true},
			lockout:     false,
			skip:        true,
			wantRequire: false,
		},
		{
			// 对照组：不豁免时失败锁定照旧生效，不回归。
			name:        "lockout still requires verification without skip",
			policy:      LoginVerificationPolicy{Enabled: true, Always: true},
			lockout:     true,
			skip:        false,
			wantRequire: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{policy: test.policy})
			service.WithLoginLockout(&fakeLoginLockout{verificationRequired: test.lockout}, staticLoginLockoutPolicy{})

			_, err := service.Login(testContext(t), LoginInput{
				Login: "victim", Password: loginVerificationTestPassword, SkipHumanVerification: test.skip,
			})
			required := errors.Is(err, ErrLoginVerificationRequired)
			if required != test.wantRequire {
				t.Fatalf("required=%t want=%t (err=%v)", required, test.wantRequire, err)
			}
		})
	}
}

func TestLoginVerificationPolicyFailureFailsOpen(t *testing.T) {
	service, _, _ := newLoginVerificationTest(t, failingLoginVerificationPolicy{})
	if _, err := service.Login(testContext(t), LoginInput{Login: "victim", Password: loginVerificationTestPassword}); err != nil {
		t.Fatalf("policy read failure must fail open, got %v", err)
	}
}

// TestLoginVerificationLockoutReadFailureKeepsPolicyChain 固定 F-10：失败锁定
// 读取失败只能降级 lockout 那一支，不能把 always/new_device/privileged 一起放弃。
//
// 历史缺陷是 `return false, nil`，即 Redis 抖动直接让运营开的「总是验证」失效。
//
// 三条策略分支各自独立受守卫：只保住 always、把 new_device/privileged 一起丢掉，
// 同样是可静默上线的绕过（管理员账号与新设备不再强制验证），因此表里为每条策略
// 各留一行。
//
// 变异要求（三种都要红）：
//   - 把 lockout 错误分支改回 `return false, nil` → 三行全红；
//   - 改成「只保留 always」（lockout 错误时直接 `if policy.Always { return true }`
//     再 `return false`）→ new_device 与 privileged 两行必须红，always 行仍绿。
func TestLoginVerificationLockoutReadFailureKeepsPolicyChain(t *testing.T) {
	tests := []struct {
		name        string
		policy      LoginVerificationPolicy
		fingerprint string
		wantRequire bool
	}{
		{
			// 变异靶子：策略要求总是验证时，Redis 抖动不得关掉验证。
			name:        "always still requires verification",
			policy:      LoginVerificationPolicy{Enabled: true, Always: true},
			wantRequire: true,
		},
		{
			// 同一条策略链的第三段：新设备触发条件不得被 Redis 抖动连带放弃。
			// 空指纹会被当成已知设备（见 HasKnownDevice），所以必须送一个陌生 UA。
			name:        "new device still requires verification",
			policy:      LoginVerificationPolicy{Enabled: true, NewDevice: true},
			fingerprint: "UnknownAgent/1.0",
			wantRequire: true,
		},
		{
			// 同一条策略链的第四段：特权账号（首个注册者即初始超管）不得被连带放弃。
			name:        "privileged account still requires verification",
			policy:      LoginVerificationPolicy{Enabled: true, Privileged: true},
			wantRequire: true,
		},
		{
			// 对照：策略全关时仍然不要求，降级不凭空制造验证。
			name:        "all triggers off stays open",
			policy:      LoginVerificationPolicy{},
			wantRequire: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{policy: test.policy})
			service.WithLoginLockout(&erroringLoginLockout{
				fakeLoginLockout: &fakeLoginLockout{},
				err:              errors.New("redis: connection pool timeout"),
			}, staticLoginLockoutPolicy{})

			_, err := service.Login(testContext(t), LoginInput{
				Login: "victim", Password: loginVerificationTestPassword,
				DeviceFingerprint: test.fingerprint,
			})
			if required := errors.Is(err, ErrLoginVerificationRequired); required != test.wantRequire {
				t.Fatalf("required=%t want=%t (err=%v)", required, test.wantRequire, err)
			}
		})
	}
}

// TestLoginVerificationDegradationsLogDistinctly 固定三处降级在日志里可区分：
// lockout 不可用、策略读取失败、设备指纹查询失败必须给出不同的消息和
// degradation 值，否则线上只能看到「有人没被验证」而不知道该修 Redis、
// Options 还是会话表。
func TestLoginVerificationDegradationsLogDistinctly(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	lockoutOnly, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, Always: true},
	})
	lockoutOnly.WithLoginLockout(&erroringLoginLockout{
		fakeLoginLockout: &fakeLoginLockout{},
		err:              errors.New("redis: i/o timeout"),
	}, staticLoginLockoutPolicy{})
	if _, err := lockoutOnly.Login(testContext(t), LoginInput{
		Login: "victim", Password: loginVerificationTestPassword,
	}); !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("lockout degradation must still require verification, got %v", err)
	}
	lockoutLogs := buf.String()
	if !strings.Contains(lockoutLogs, "degradation=lockout_unavailable") {
		t.Fatalf("lockout degradation not logged: %s", lockoutLogs)
	}
	if strings.Contains(lockoutLogs, "degradation=policy_unavailable") {
		t.Fatalf("lockout degradation must not be reported as policy failure: %s", lockoutLogs)
	}

	buf.Reset()
	policyOnly, _, _ := newLoginVerificationTest(t, failingLoginVerificationPolicy{})
	policyOnly.WithLoginLockout(&fakeLoginLockout{}, staticLoginLockoutPolicy{})
	if _, err := policyOnly.Login(testContext(t), LoginInput{
		Login: "victim", Password: loginVerificationTestPassword,
	}); err != nil {
		t.Fatalf("policy read failure must fail open, got %v", err)
	}
	policyLogs := buf.String()
	if !strings.Contains(policyLogs, "degradation=policy_unavailable") {
		t.Fatalf("policy degradation not logged: %s", policyLogs)
	}
	if strings.Contains(policyLogs, "degradation=lockout_unavailable") {
		t.Fatalf("policy degradation must not be reported as lockout failure: %s", policyLogs)
	}

	// 第三处：会话指纹查询失败按已知设备放行，但同样必须留痕（F10-2）。
	buf.Reset()
	fingerprintOnly, store, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, NewDevice: true},
	})
	fingerprintOnly.store = failingFingerprintStore{fakeStore: store}
	if _, err := fingerprintOnly.Login(testContext(t), LoginInput{
		Login: "victim", Password: loginVerificationTestPassword, DeviceFingerprint: "UnknownAgent/1.0",
	}); err != nil {
		t.Fatalf("fingerprint read failure must fail open, got %v", err)
	}
	fingerprintLogs := buf.String()
	if !strings.Contains(fingerprintLogs, "degradation=device_fingerprint_unavailable") {
		t.Fatalf("device fingerprint degradation not logged: %s", fingerprintLogs)
	}
	if strings.Contains(fingerprintLogs, "degradation=lockout_unavailable") ||
		strings.Contains(fingerprintLogs, "degradation=policy_unavailable") {
		t.Fatalf("fingerprint degradation must be reported as its own cause: %s", fingerprintLogs)
	}
}

func TestLoginVerificationLockoutStillAppliesWithGateOff(t *testing.T) {
	service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: false, Always: true},
	})
	service.WithLoginLockout(&fakeLoginLockout{verificationRequired: true}, staticLoginLockoutPolicy{})
	if _, err := service.Login(testContext(t), LoginInput{Login: "victim", Password: loginVerificationTestPassword}); !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("lockout requirement must survive the disabled gate, got %v", err)
	}
}

func TestLoginVerificationWrongPasswordStaysGeneric(t *testing.T) {
	service, _, _ := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, Always: true},
	})
	if _, err := service.Login(testContext(t), LoginInput{Login: "victim", Password: "wrong password"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password must stay generic, got %v", err)
	}
}

func TestLoginVerificationNewDevice(t *testing.T) {
	service, store, registered := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, NewDevice: true},
	})
	ctx := testContext(t)

	// 无指纹（客户端未送 UA）视为已知设备。
	if _, err := service.Login(ctx, LoginInput{Login: "victim", Password: loginVerificationTestPassword}); err != nil {
		t.Fatalf("empty fingerprint must be treated as known, got %v", err)
	}
	// 陌生 UA：未命中活跃会话。
	if _, err := service.Login(ctx, LoginInput{
		Login: "victim", Password: loginVerificationTestPassword, DeviceFingerprint: "UnknownAgent/1.0",
	}); !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("unknown device must require verification, got %v", err)
	}
	// 已有活跃会话命中同一指纹：放行。
	store.sessions = append(store.sessions, fakeSessionRow{
		userID: registered.ID, sid: "sid-known", userAgentRaw: "KnownAgent/1.0",
		createdAt: time.Now().UTC(), lastSeenAt: time.Now().UTC(),
	})
	if _, err := service.Login(ctx, LoginInput{
		Login: "victim", Password: loginVerificationTestPassword, DeviceFingerprint: "KnownAgent/1.0",
	}); err != nil {
		t.Fatalf("known device must pass, got %v", err)
	}
}

func TestLoginVerificationPrivilegedAccount(t *testing.T) {
	service, store, registered := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, Privileged: true},
	})
	ctx := testContext(t)

	// 首个注册者会同时拿到 member 与 super_admin 角色并被标记为初始超管；
	// 这里退回纯 member，验证普通账号路径。
	store.userRoleIDs[registered.ID] = []int64{2}
	member := store.users[registered.ID]
	member.IsInitialSuperAdmin = false
	store.users[registered.ID] = member

	// 默认 member：不要求验证。
	if _, err := service.Login(ctx, LoginInput{Login: "victim", Password: loginVerificationTestPassword}); err != nil {
		t.Fatalf("member account must not require verification, got %v", err)
	}
	// super_admin 角色：要求验证。
	store.userRoleIDs[registered.ID] = []int64{1}
	if _, err := service.Login(ctx, LoginInput{Login: "victim", Password: loginVerificationTestPassword}); !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("super admin must require verification, got %v", err)
	}
}

func TestLoginVerificationPrivilegedByAdminAccessPermission(t *testing.T) {
	service, store, registered := newLoginVerificationTest(t, staticLoginVerificationPolicy{
		policy: LoginVerificationPolicy{Enabled: true, Privileged: true},
	})
	// 只授予后台访问权限、不挂 super_admin 角色键，验证判定不依赖单一角色名。
	store.seedRole(Role{ID: 77, Key: "helpdesk", Alias: "Helpdesk", IsEnabled: true})
	store.rolePerms[77] = []string{PermissionAdminAccess}
	store.userRoleIDs[registered.ID] = []int64{77}

	if _, err := service.Login(testContext(t), LoginInput{Login: "victim", Password: loginVerificationTestPassword}); !errors.Is(err, ErrLoginVerificationRequired) {
		t.Fatalf("admin.access holder must require verification, got %v", err)
	}
}
