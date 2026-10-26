package http

import (
	"context"
	"errors"
	"strconv"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func identityAdminGuardEvaluator(policy IdentityAdminGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if err := requireIdentityAdminAuthority(ctx, evaluation); !errors.Is(err, routes.ErrCoreGuardEvaluatorUnavailable) {
			return err
		}
		if !identityAdminSubjectRoute(evaluation.Descriptor.RouteID) || policy == nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if err := requireIdentityAdminSubjectRoutePermission(evaluation); err != nil {
			return err
		}
		userID, err := strconv.ParseInt(evaluation.Request.Params["userID"], 10, 64)
		if err != nil || userID <= 0 {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		// 这些后台写路由是 deliberate Store-I/O exception。目标保护等级必须
		// 来自当前 PostgreSQL 状态，不能使用无法跨 API 节点失效的本地缓存。
		subject, err := policy.LoadAdminGuardSubject(ctx, userID)
		if err != nil || subject.UserID != userID || !subject.Exists {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return authorizeIdentityAdminSubject(evaluation, subject)
	}
}

func requireIdentityAdminSubjectRoutePermission(evaluation routes.CoreGuardEvaluation) error {
	if evaluation.Descriptor.RouteID == "core.route.identity.replace_user_permission_overrides" {
		return requireCoreGuardPermission(evaluation, identity.PermissionUserPermissionOverride)
	}
	return requireCoreGuardPermission(evaluation, identity.PermissionUserManage)
}

func identityAdminSubjectRoute(routeID string) bool {
	switch routeID {
	case "core.route.identity.update_user",
		"core.route.identity.admin_clear_user_client_ips",
		"core.route.identity.set_user_email_verification",
		"core.route.identity.admin_set_user_password",
		"core.route.identity.replace_user_permission_overrides",
		"core.route.identity.replace_user_roles",
		"core.route.identity.admin_revoke_user_sessions":
		return true
	default:
		return false
	}
}

func authorizeIdentityAdminSubject(evaluation routes.CoreGuardEvaluation, subject identity.AdminGuardSubject) error {
	actorIsSuperAdmin := evaluation.Request.Permissions["*"]
	switch evaluation.Descriptor.RouteID {
	case "core.route.identity.update_user":
		if err := requireCoreGuardPermission(evaluation, identity.PermissionUserManage); err != nil {
			return err
		}
		if subject.IsSuperAdmin && !actorIsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		var input identityAdminUpdateGuardInput
		if err := decodeGuardJSON(evaluation.Request.Body, &input); err != nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if input.Status == nil {
			return nil
		}
		status := identity.UserStatus(strings.TrimSpace(*input.Status))
		if evaluation.Request.ActorID == subject.UserID ||
			subject.IsInitialSuperAdmin && status != identity.UserStatusActive {
			return routes.ErrCoreGuardPermissionDenied
		}
		if status == identity.UserStatusBanned {
			return requireCoreGuardPermission(evaluation, identity.PermissionUserBan)
		}
		if status != identity.UserStatusActive && status != identity.UserStatusDisabled {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return nil
	case "core.route.identity.admin_clear_user_client_ips":
		if subject.IsSuperAdmin && !actorIsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		return requireCoreGuardPermission(evaluation, identity.PermissionUserManage)
	case "core.route.identity.set_user_email_verification":
		if subject.IsSuperAdmin && !actorIsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		var input identityAdminEmailVerificationGuardInput
		if err := decodeGuardJSON(evaluation.Request.Body, &input); err != nil || input.Verified == nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return requireCoreGuardPermission(evaluation, identity.PermissionUserManage)
	case "core.route.identity.admin_set_user_password":
		// 与 AdminSetUserPassword 服务边界对齐：需要 user.manage；
		// 目标为 super_admin 时仅 super_admin 可操作；允许管理员重置自己的密码。
		if err := requireCoreGuardPermission(evaluation, identity.PermissionUserManage); err != nil {
			return err
		}
		if subject.IsSuperAdmin && !actorIsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		var input identityAdminSetPasswordGuardInput
		if err := decodeGuardJSON(evaluation.Request.Body, &input); err != nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if strings.TrimSpace(input.Password) == "" {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return nil
	case "core.route.identity.replace_user_permission_overrides":
		if evaluation.Request.ActorID == subject.UserID || subject.IsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		return requireCoreGuardPermission(evaluation, identity.PermissionUserPermissionOverride)
	case "core.route.identity.replace_user_roles":
		if err := requireCoreGuardPermission(evaluation, identity.PermissionUserManage); err != nil {
			return err
		}
		if evaluation.Request.ActorID == subject.UserID {
			return routes.ErrCoreGuardPermissionDenied
		}
		var input identityAdminRolesGuardInput
		if err := decodeGuardJSON(evaluation.Request.Body, &input); err != nil {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		nextIsSuperAdmin := containsGuardString(input.RoleKeys, identity.RoleSuperAdmin)
		if subject.IsInitialSuperAdmin && !nextIsSuperAdmin ||
			subject.IsSuperAdmin != nextIsSuperAdmin && !actorIsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		return nil
	case "core.route.identity.admin_revoke_user_sessions":
		if evaluation.Request.ActorID == subject.UserID || subject.IsSuperAdmin && !actorIsSuperAdmin {
			return routes.ErrCoreGuardPermissionDenied
		}
		return requireCoreGuardPermission(evaluation, identity.PermissionUserManage)
	default:
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
}

type identityAdminUpdateGuardInput struct {
	Username    *string `json:"username"`
	Email       *string `json:"email"`
	DisplayName *string `json:"displayName"`
	Locale      *string `json:"locale"`
	Status      *string `json:"status"`
	Bio         *string `json:"bio"`
	Signature   *string `json:"signature"`
	Location    *string `json:"location"`
	WebsiteURL  *string `json:"websiteUrl"`
}

type identityAdminRolesGuardInput struct {
	RoleKeys []string `json:"roleKeys"`
}

type identityAdminEmailVerificationGuardInput struct {
	Verified *bool `json:"verified"`
}

type identityAdminSetPasswordGuardInput struct {
	Password string `json:"password"`
}

func containsGuardString(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}
