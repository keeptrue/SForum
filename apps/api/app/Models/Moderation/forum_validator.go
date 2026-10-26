package moderation

import (
	"context"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// ForumTargetValidator 复用 forum store 判定 topic/comment 是否可被公开举报。
// 可举报 = 目标存在且处于公开可见状态（topic: active/locked；comment: active）。
type ForumTargetValidator struct {
	forumStore forum.Store
}

func NewForumTargetValidator(forumStore forum.Store) *ForumTargetValidator {
	return &ForumTargetValidator{forumStore: forumStore}
}

func (v *ForumTargetValidator) IsReportableTopic(ctx context.Context, topicID int64) (bool, error) {
	topic, err := v.forumStore.GetTopicForAction(ctx, topicID)
	if err != nil {
		// 不存在视为不可举报，但不把 store 错误透传给举报者。
		return false, nil
	}
	return topic.Status == forum.TopicStatusActive || topic.Status == forum.TopicStatusLocked, nil
}

func (v *ForumTargetValidator) IsReportableComment(ctx context.Context, commentID int64) (bool, error) {
	summary, err := v.forumStore.GetCommentSummary(ctx, commentID)
	if err != nil {
		return false, nil
	}
	return summary.Status == forum.CommentStatusActive, nil
}
