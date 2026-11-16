package identity

import (
	"context"
	"strings"
)

// AdminSetUserPasswordResult 是管理员重置密码的结果摘要（不含任何密钥）。
type AdminSetUserPasswordResult struct {
	// RevokedSessions 因改密而强制下线的活跃设备数。
	RevokedSessions int `json:"revokedSessions"`
}

// AdminSetUserPassword 管理员直接为目标用户设置新密码。
//
// 权限边界：
//   - actor 必须持有 user.manage；
//   - 目标若是 super_admin，则只有同样是 super_admin 的管理员才能操作
//     （与 AdminRevokeUserSessions / ReplaceUserRoles 对称）；
//   - 允许管理员重置自己的密码（与「禁止改自己状态/角色」不同——改密是恢复入口）。
//
// 安全副作用（与公开密码重置一致）：
//  1. 校验站点密码策略；
//  2. 写入新 Argon2id 哈希；
//  3. 递增 current_token_version，使旧会话/令牌失效；
//  4. 撤销该用户全部活跃会话；
//  5. 在同一事务里撤销该用户全部未撤销的 PAT（api_tokens），
//     机器客户端必须重新登录换取新令牌。
func (s *Service) AdminSetUserPassword(
	ctx context.Context,
	actor Actor,
	targetUserID int64,
	newPassword string,
) (AdminSetUserPasswordResult, error) {
	if !actor.Can(PermissionUserManage) {
		return AdminSetUserPasswordResult{}, ErrPermissionDenied
	}
	if targetUserID <= 0 {
		return AdminSetUserPasswordResult{}, ErrUserNotFound
	}
	if strings.TrimSpace(newPassword) == "" {
		return AdminSetUserPasswordResult{}, NewRegisterInvalid(FieldMessages{
			FieldPassword: {MessagePasswordMin},
		})
	}

	target, err := s.store.GetAdminUser(ctx, targetUserID)
	if err != nil {
		return AdminSetUserPasswordResult{}, err
	}
	if containsString(target.RoleKeys, RoleSuperAdmin) && !actor.IsSuperAdmin() {
		return AdminSetUserPasswordResult{}, ErrSuperAdminSessionLocked
	}

	policy, err := s.passwordPolicies.PasswordPolicy(ctx)
	if err != nil {
		return AdminSetUserPasswordResult{}, err
	}
	if fields := policy.Validate(newPassword); len(fields) > 0 {
		return AdminSetUserPasswordResult{}, NewRegisterInvalid(fields)
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return AdminSetUserPasswordResult{}, err
	}
	if err := s.store.UpdateUserPassword(ctx, targetUserID, hash); err != nil {
		return AdminSetUserPasswordResult{}, err
	}
	// 先抬版本再撤会话：即便会话行暂时残留，旧 cookie 也会因 version 不匹配而失效。
	if err := s.store.IncrementUserTokenVersion(ctx, targetUserID); err != nil {
		return AdminSetUserPasswordResult{}, err
	}
	revoked, err := s.store.RevokeUserSessions(ctx, targetUserID, RevokeReasonPasswordReset)
	if err != nil {
		return AdminSetUserPasswordResult{}, err
	}
	return AdminSetUserPasswordResult{RevokedSessions: revoked}, nil
}
