package http

import (
	"context"
	"strconv"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// ForumResourceGuardPolicy loads authoritative topic/comment ownership for
// declared Core own-resource permissions. Resource ids come only from the
// frozen route/request params; callers never supply an owner id.
type ForumResourceGuardPolicy interface {
	LoadTopicResourceGuardSubject(context.Context, int64) (forum.TopicResourceGuardSubject, error)
	LoadCommentResourceGuardSubject(context.Context, int64) (forum.CommentResourceGuardSubject, error)
}

func forumTopicEditGuardEvaluator(policy ForumResourceGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if evaluation.Descriptor.RouteID != "core.route.forum.update_topic" {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return authorizeForumTopicResource(
			ctx, evaluation, policy,
			identity.PermissionTopicEditAny, identity.PermissionTopicEditOwn,
		)
	}
}

func forumTopicDeleteGuardEvaluator(policy ForumResourceGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if evaluation.Descriptor.RouteID != "core.route.forum.delete_topic" {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return authorizeForumTopicResource(
			ctx, evaluation, policy,
			identity.PermissionTopicDeleteAny, identity.PermissionTopicDeleteOwn,
		)
	}
}

func forumCommentWriteGuardEvaluator(policy ForumResourceGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		var globalPermission, ownPermission string
		switch evaluation.Descriptor.RouteID {
		case "core.route.forum.update_comment":
			globalPermission = identity.PermissionPostEditAny
			ownPermission = identity.PermissionPostEditOwn
		case "core.route.forum.delete_comment":
			globalPermission = identity.PermissionPostDeleteAny
			ownPermission = identity.PermissionPostDeleteOwn
		default:
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		return authorizeForumCommentResource(ctx, evaluation, policy, globalPermission, ownPermission)
	}
}

func authorizeForumTopicResource(
	ctx context.Context,
	evaluation routes.CoreGuardEvaluation,
	policy ForumResourceGuardPolicy,
	globalPermission, ownPermission string,
) error {
	if err := requireAuthenticatedCoreGuardActor(ctx, evaluation); err != nil {
		return err
	}
	// 全局管理/版主权限不依赖资源所有权；保持与既有 any 路径一致，不做 Store I/O。
	if evaluation.Request.Permissions["*"] || evaluation.Request.Permissions[globalPermission] {
		return nil
	}
	if !evaluation.Request.Permissions[ownPermission] {
		return routes.ErrCoreGuardPermissionDenied
	}
	if policy == nil || evaluation.Request.Query != "" {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	topicID, err := strconv.ParseInt(evaluation.Request.Params["topicID"], 10, 64)
	if err != nil || topicID <= 0 {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	subject, err := policy.LoadTopicResourceGuardSubject(ctx, topicID)
	if err != nil || !subject.Exists || subject.TopicID != topicID || subject.AuthorUserID <= 0 {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	// 软删后核心 Store 已拒绝更新/删除；own 路径同样 fail-closed。
	if subject.Status == forum.TopicStatusDeleted {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	if evaluation.Request.ActorID != subject.AuthorUserID {
		return routes.ErrCoreGuardPermissionDenied
	}
	return nil
}

func authorizeForumCommentResource(
	ctx context.Context,
	evaluation routes.CoreGuardEvaluation,
	policy ForumResourceGuardPolicy,
	globalPermission, ownPermission string,
) error {
	if err := requireAuthenticatedCoreGuardActor(ctx, evaluation); err != nil {
		return err
	}
	if evaluation.Request.Permissions["*"] || evaluation.Request.Permissions[globalPermission] {
		return nil
	}
	if !evaluation.Request.Permissions[ownPermission] {
		return routes.ErrCoreGuardPermissionDenied
	}
	if policy == nil || evaluation.Request.Query != "" {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	commentID, err := strconv.ParseInt(evaluation.Request.Params["commentID"], 10, 64)
	if err != nil || commentID <= 0 {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	subject, err := policy.LoadCommentResourceGuardSubject(ctx, commentID)
	if err != nil || !subject.Exists || subject.CommentID != commentID || subject.AuthorUserID <= 0 {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	if subject.Status == forum.CommentStatusDeleted {
		return routes.ErrCoreGuardEvaluatorUnavailable
	}
	if evaluation.Request.ActorID != subject.AuthorUserID {
		return routes.ErrCoreGuardPermissionDenied
	}
	return nil
}
