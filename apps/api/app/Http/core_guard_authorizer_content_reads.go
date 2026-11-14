package http

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// 本文件放「后台读取」类的 contextual 求值器：它们把运行时服务层的复合判定
// （AND 套 OR）原样复现成守卫，使路由目录声明与真实行为一致。
//
// 单独成文件的原因：core_guard_authorizer.go 已接近架构门禁的 1000 行上限，
// 新能力按职责拆到自己的文件，而不是把主文件推过线。

// requireForumContentReadAuthority 复现后台内容读取的运行时判定：
// admin.access 且（编辑任意 或 查看任意修订）。
//
// 为什么必须是 contextual：这是 AND 套 OR，单个 permission_any 表达不了。
// 之前 catalog 只写了 edit_any/revision_view_any，等于漏掉 admin.access —— 只持有
// 内容权限但进不了后台的账号会被目录声明成「允许」，与实际 403 不一致。
// 未知 RouteID 一律 fail-closed：这个求值器只服务已知的四条读取路由。
func requireForumContentReadAuthority(_ context.Context, evaluation routes.CoreGuardEvaluation) error {
	switch evaluation.Descriptor.RouteID {
	case "core.route.forum.admin_content_topics", "core.route.forum.admin_content_topic":
		if err := requireAllCoreGuardPermissions(evaluation, identity.PermissionAdminAccess); err != nil {
			return err
		}
		return requireCoreGuardPermission(evaluation,
			identity.PermissionTopicEditAny,
			identity.PermissionTopicRevisionViewAny,
		)
	case "core.route.forum.admin_content_comments", "core.route.forum.admin_content_comment":
		if err := requireAllCoreGuardPermissions(evaluation, identity.PermissionAdminAccess); err != nil {
			return err
		}
		return requireCoreGuardPermission(evaluation,
			identity.PermissionPostEditAny,
			identity.PermissionPostRevisionViewAny,
		)
	default:
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
}

// requireAttachmentUploadPolicyUserAuthority 复现用户级上传策略的运行时判定：
// attachment.upload_policy.manage 且 user.view（UploadPolicy.GetUser/SetUser/DeleteUser）。
//
// 角色级（.../roles*）仍然只要 manage，因此两条策略分开注册：目录里角色级是
// permission_any 单键，用户级走这个求值器。未知 RouteID fail-closed。
func requireAttachmentUploadPolicyUserAuthority(_ context.Context, evaluation routes.CoreGuardEvaluation) error {
	switch evaluation.Descriptor.RouteID {
	case "core.route.attachments.get_user_upload_policy",
		"core.route.attachments.set_user_upload_policy",
		"core.route.attachments.delete_user_upload_policy":
		return requireAllCoreGuardPermissions(evaluation,
			identity.PermissionAttachmentUploadPolicyManage,
			identity.PermissionUserView,
		)
	default:
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
}
