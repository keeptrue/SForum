package http

import (
	"context"
	"strconv"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

type ForumCommentCreateGuardPolicy interface {
	LoadCommentCreateGuardSubject(context.Context, int64) (forum.CommentCreateGuardSubject, error)
}

func forumCommentCreateGuardEvaluator(policy ForumCommentCreateGuardPolicy) routes.CoreGuardEvaluatorFunc {
	return func(ctx context.Context, evaluation routes.CoreGuardEvaluation) error {
		if policy == nil || evaluation.Descriptor.RouteID != "core.route.forum.create_comment" {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if err := requireDeclaredCoreGuardPermission(ctx, evaluation); err != nil {
			return err
		}
		topicID, err := strconv.ParseInt(evaluation.Request.Params["topicID"], 10, 64)
		if err != nil || topicID <= 0 {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		subject, err := policy.LoadCommentCreateGuardSubject(ctx, topicID)
		if err != nil || !subject.Exists || subject.TopicID != topicID {
			return routes.ErrCoreGuardEvaluatorUnavailable
		}
		if subject.Status != forum.TopicStatusActive {
			return routes.ErrCoreGuardPermissionDenied
		}
		return nil
	}
}
