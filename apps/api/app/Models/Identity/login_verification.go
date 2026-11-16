package identity

import (
	"context"
	"log/slog"
)

// LoginInput 是一次密码登录的输入。ClientIP/DeviceFingerprint 只用于节流与
// login_risk 判定；HumanVerified/SkipHumanVerification 由 HTTP 层设置。
type LoginInput struct {
	Login    string
	Password string
	// ClientIP 用于分层登录节流（account+IP / IP）；可空。
	ClientIP string
	// DeviceFingerprint 是与会话登记同口径的 UA 指纹（useragent.UserAgentRaw），
	// 仅用于 login_risk 的新设备判定；空值视为已知设备。
	DeviceFingerprint string
	// HumanVerified 仅由 HTTP 层在 login_risk 验证成功后设置。
	HumanVerified bool
	// SkipHumanVerification 由 HTTP 层在「登录同时签发 API Token」时设置。
	// 只豁免策略类触发条件（always/new_device/privileged），**不豁免**失败锁定
	// 触发的验证。这是已知取舍：该路径仍会签发浏览器会话，等同一处可绕过的验证
	// 豁免面，见 knowledge/decisions/2026-10-22-login-human-verification-triggers.md。
	SkipHumanVerification bool
}

// WithLoginVerification 注入登录强化策略（可选；总是/新设备/特权账号）。
func (s *Service) WithLoginVerification(policy LoginVerificationPolicyResolver) *Service {
	s.loginVerification = policy
	return s
}

// LoginVerificationPolicy 是运营配置的登录强化触发策略。
//
// 四个触发条件相互独立，但都受 Enabled（human_verification.scenarios.login_risk）
// 总闸约束；关闭总闸时 Always/NewDevice/Privileged 一律不生效。
type LoginVerificationPolicy struct {
	// Enabled 是 human_verification.scenarios.login_risk 总闸。
	Enabled bool
	// Always 要求每次密码登录都先完成人机验证。
	Always bool
	// NewDevice 要求从未见过的设备（UA 指纹未命中活跃会话）先完成验证。
	NewDevice bool
	// Privileged 要求管理面账号先完成验证。
	Privileged bool
}

// LoginVerificationPolicyResolver 读取登录强化策略（通常由 Options 实现）。
type LoginVerificationPolicyResolver interface {
	LoginVerificationPolicy(ctx context.Context) (LoginVerificationPolicy, error)
}

// loginRequiresVerification 判定本次登录是否必须先完成人机验证。
//
// 判定顺序：失败锁定（历史行为，与运营配置无关）→ 显式豁免 → 总闸 →
// 总是验证 → 新设备 → 特权账号。判定发生在密码校验通过之后，错误密码既不会
// 消耗验证令牌，也不会产生账号存在性信号。
//
// 失败锁定**排在显式豁免之前**：账号失败阈值触发的验证永远不被豁免（P2-1）。
// `SkipHumanVerification` 只覆盖策略类触发条件（always/new_device/privileged），
// 因为那条豁免是为「登录同时签发 API Token」的原生/CLI 客户端保留的取舍，
// 不能让携带该形状的请求连失败锁定一起跳过。
//
// 失败姿态按触发条件分别降级，三处降级互不牵连，各写一条可区分的 warn
// （degradation 字段分别是 lockout_unavailable / policy_unavailable /
// device_fingerprint_unavailable）：
//   - 失败锁定读取失败（Redis 抖动）：只跳过失败锁定这一支，**继续**走下面的
//     策略链。`loginVerificationPolicy` 读的是 Options，与 Redis 无关，所以
//     Redis 故障没有理由连带放弃 always/new_device/privileged 判定（F-10）。
//   - 策略读取失败（Options 读取失败）：跳过策略类触发条件，即 fail open，
//     避免配置抖动把合法用户挡在门外。
//   - 设备指纹查询失败（会话表读失败）：只在 NewDevice 分支内 fail open，按
//     「已知设备」处理。这同样是「本该校验却没校验」的窗口，所以也留痕（F10-2）。
//
// 三处降级都不把存储故障当 error 上抛：本函数只回答「是否需要验证」，把 Redis
// 或会话表抖动变成登录路径的 500 是比多一次验证更坏的取舍。
func loginRequiresVerification(s *Service, ctx context.Context, loginKey string, credential CredentialUser, input LoginInput) (bool, error) {
	if s.loginLockout != nil {
		required, err := s.loginLockout.RequiresVerification(ctx, loginKey)
		switch {
		case err != nil:
			// 只降级 lockout 这一支，策略链照旧判定。
			slog.Warn("identity: login lockout verification read failed; continuing with policy triggers",
				"degradation", "lockout_unavailable",
				"error", err,
			)
		case required:
			return true, nil
		}
	}
	// 显式豁免只覆盖下面的策略类触发条件；失败锁定已在上面判过。
	if input.SkipHumanVerification {
		return false, nil
	}
	policy := loginVerificationPolicy(s, ctx)
	if !policy.Enabled {
		return false, nil
	}
	if policy.Always {
		return true, nil
	}
	if policy.NewDevice {
		known, err := s.HasKnownDevice(ctx, credential.ID, input.DeviceFingerprint)
		switch {
		case err != nil:
			// 会话表读失败时按已知设备放行，但必须留痕：否则「新设备验证」
			// 会在故障窗口里静默失效，运营看不到任何信号（F10-2）。
			slog.Warn("identity: device fingerprint lookup failed; treating login as known device",
				"degradation", "device_fingerprint_unavailable",
				"error", err,
			)
		case !known:
			return true, nil
		}
	}
	if policy.Privileged && privilegedAccount(credential.CurrentUser) {
		return true, nil
	}
	return false, nil
}

func loginVerificationPolicy(s *Service, ctx context.Context) LoginVerificationPolicy {
	if s.loginVerification == nil {
		return LoginVerificationPolicy{}
	}
	policy, err := s.loginVerification.LoginVerificationPolicy(ctx)
	if err != nil {
		// 与 lockout 降级用不同的消息与 degradation 值，便于线上区分是哪一层坏了。
		slog.Warn("identity: login verification policy read failed; falling back to no policy triggers",
			"degradation", "policy_unavailable",
			"error", err,
		)
		return LoginVerificationPolicy{}
	}
	return policy
}

// privilegedAccount 判定账号是否属于需要强化的管理面账号：
// 初始超管标记、super_admin 角色，或拥有后台访问权限。
func privilegedAccount(current CurrentUser) bool {
	if current.IsInitialSuperAdmin {
		return true
	}
	for _, roleKey := range current.RoleKeys {
		if roleKey == RoleSuperAdmin {
			return true
		}
	}
	for _, permission := range current.Permissions {
		if permission == PermissionAdminAccess {
			return true
		}
	}
	return false
}
