package identity

import (
	"context"
	"errors"
	"fmt"
)

// CodeRoleGrantExceedsActor 是「授予的角色超出操作者权限」的稳定错误码。
const CodeRoleGrantExceedsActor = "role.grant_exceeds_actor"

// FieldPermissions 是「改角色权限」写路径的字段键。
//
// 它必须等于真实请求体的 JSON tag：PUT /roles/:roleKey/permissions 的 body 是
// {"permissions": [...]}（Controllers/Identity 的 replaceRolePermissionsRequest），
// web 端发的也是 { permissions: ... }。写错这个键，前端就找不到表单落点。
const FieldPermissions = "permissions"

// ErrRoleGrantExceedsActor 是角色授予不变量被违反时的领域哨兵。
//
// 不变量：操作者只能授予自己已经持有的权限（外加站点默认注册基线）。
// 否则持有 user.manage 的 operator 可以铸出带 database.manage 的 tech_admin，
// 再用 Reveal 取走凭据 —— 这是跨角色提权，而不是越权编辑。
var ErrRoleGrantExceedsActor = errors.New("identity: role grant exceeds actor permissions")

// RoleGrantExceedsActorError 同时满足两条既有契约：
//
//   - errors.Is(err, ErrRoleGrantExceedsActor)：领域层与测试的稳定判定；
//   - errors.As(err, *AdminUserFieldsError)：transport 把问题落到 roleKeys 字段，
//     响应带可读 message（422 + fields），而不是落到 500。
type RoleGrantExceedsActorError struct {
	*AdminUserFieldsError
	RoleKey string
	// Field 是字段级错误的落点：角色集路径是 roleKeys，权限集路径是 permissions。
	Field string
	// Permission 是角色持有、而操作者没有的那一项权限。
	Permission string
}

func (e *RoleGrantExceedsActorError) Error() string {
	// 权限集路径没有目标角色，RoleKey 为空：消息只报那一项权限。
	if e.RoleKey == "" {
		return fmt.Sprintf("identity: granting permission %q requires the actor to hold it", e.Permission)
	}
	return fmt.Sprintf("identity: granting role %q requires permission %q the actor does not hold", e.RoleKey, e.Permission)
}

// Unwrap 返回多错误：字段级错误给 transport，哨兵给领域判定。
func (e *RoleGrantExceedsActorError) Unwrap() []error {
	return []error{ErrRoleGrantExceedsActor, e.AdminUserFieldsError}
}

func newRoleGrantExceedsActorError(roleKey, permission string) *RoleGrantExceedsActorError {
	return newRoleGrantExceedsActorFieldError(FieldRoleKeys, roleKey, permission)
}

// newRoleGrantExceedsActorFieldError 让两条写路径把提示落到各自的字段上。
func newRoleGrantExceedsActorFieldError(field, roleKey, permission string) *RoleGrantExceedsActorError {
	if field == "" {
		field = FieldRoleKeys
	}
	return &RoleGrantExceedsActorError{
		AdminUserFieldsError: &AdminUserFieldsError{
			Reason: CodeRoleGrantExceedsActor,
			// 字段消息直接用本错误码：localization 目录已有 zh-CN/en-US 双语文案，
			// 前端提示与 reason 保持一致。
			Fields: FieldMessages{field: {CodeRoleGrantExceedsActor}},
		},
		RoleKey:    roleKey,
		Field:      field,
		Permission: permission,
	}
}

// RoleGrantGuard 校验「这组角色是否在操作者的可授予范围内」。
//
// 建号、改角色、改角色权限共用同一条不变量，因此独立成协作者，
// 不挂在 Service 上：Service 的接收者方法数已是架构非增长基线。
type RoleGrantGuard struct {
	store Store
}

func NewRoleGrantGuard(store Store) *RoleGrantGuard {
	return &RoleGrantGuard{store: store}
}

// Check 逐个角色比对权限集。角色是否存在由调用方既有的校验负责：这里遇到
// 未知 key 直接跳过，避免与 validateRoles 争抢同一类错误。
func (g *RoleGrantGuard) Check(ctx context.Context, actor Actor, roleKeys []string) error {
	if g == nil || g.store == nil || len(roleKeys) == 0 {
		return nil
	}
	byKey, err := g.rolesByKey(ctx)
	if err != nil {
		return err
	}
	baseline := g.registrationBaseline(byKey)
	for _, key := range roleKeys {
		role, ok := byKey[key]
		if !ok {
			continue
		}
		if permission, ok := firstUngrantablePermission(actor, role, baseline); !ok {
			return newRoleGrantExceedsActorError(role.Key, permission)
		}
	}
	return nil
}

// CheckPermissionSet 校验「这组权限是否都在操作者自己的权限集内」。
//
// 这是同一条不变量的另一半：改用户角色走角色集，改角色权限走权限集。少了这一半，
// 持有 role.manage 的 operator 可以给任意角色（包括自己所属的组）写上自己并不持有
// 的 database.manage，再用那个角色重新登录取得它 —— 同样是跨角色提权。
// 注册基线照旧放行，否则「把 member 组补成默认注册集」这种正常维护会被误挡。
func (g *RoleGrantGuard) CheckPermissionSet(ctx context.Context, actor Actor, permissionKeys []string) error {
	if g == nil || g.store == nil || len(permissionKeys) == 0 {
		return nil
	}
	byKey, err := g.rolesByKey(ctx)
	if err != nil {
		return err
	}
	baseline := g.registrationBaseline(byKey)
	for _, permission := range permissionKeys {
		if actor.Can(permission) {
			continue
		}
		if _, shared := baseline[permission]; shared {
			continue
		}
		return newRoleGrantExceedsActorFieldError(FieldPermissions, "", permission)
	}
	return nil
}

// checkUserRoleGrant 是改用户角色路径的完整前置校验：先验角色标识存在，
// 再验这些角色持有的权限都在操作者自己手里（与建号 admin_user_create.go 同一条不变量）。
//
// 抽成普通函数而不是内联进 ReplaceUserRoles：service.go 已是架构非增长基线，
// 调用点必须保持原有行数。错误映射与内联版本逐字一致（ErrInvalidRole 仍落 roleKeys）。
func checkUserRoleGrant(ctx context.Context, service *Service, actor Actor, roleKeys []string) error {
	if err := service.validateRoles(ctx, roleKeys); err != nil {
		return mapAdminRoleValidationError(err)
	}
	return NewRoleGrantGuard(service.store).Check(ctx, actor, roleKeys)
}

// checkRolePermissionGrant 是改角色权限路径的完整前置校验：先验权限键存在，
// 再验它们都在操作者自己手里。抽出理由同上。
func checkRolePermissionGrant(ctx context.Context, service *Service, actor Actor, permissionKeys []string) error {
	if err := service.validatePermissions(ctx, permissionKeys); err != nil {
		return err
	}
	return NewRoleGrantGuard(service.store).CheckPermissionSet(ctx, actor, permissionKeys)
}

// rolesByKey 读一次角色目录：两条不变量都要用它算注册基线。
func (g *RoleGrantGuard) rolesByKey(ctx context.Context) (map[string]Role, error) {
	roles, err := g.store.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]Role, len(roles))
	for _, role := range roles {
		byKey[role.Key] = role
	}
	return byKey, nil
}

// registrationBaseline 返回「任何访客自助注册就能拿到」的权限集合。
//
// 授予默认注册组本身不产生提权，所以 operator 必须仍能建普通会员：基线权限
// 视为操作者已持有。member 角色缺失（异常安装）时退回内置种子，保持默认流程可用。
func (g *RoleGrantGuard) registrationBaseline(byKey map[string]Role) map[string]struct{} {
	baseline := make(map[string]struct{})
	if member, ok := byKey[RoleMember]; ok {
		for _, permission := range member.PermissionKeys {
			baseline[permission] = struct{}{}
		}
		return baseline
	}
	for _, permission := range SeedMemberPermissions {
		baseline[permission] = struct{}{}
	}
	return baseline
}

// firstUngrantablePermission 返回角色持有但操作者不持有的第一项权限。
func firstUngrantablePermission(actor Actor, role Role, baseline map[string]struct{}) (string, bool) {
	for _, permission := range role.PermissionKeys {
		if actor.Can(permission) {
			continue
		}
		if _, shared := baseline[permission]; shared {
			continue
		}
		return permission, false
	}
	return "", true
}
