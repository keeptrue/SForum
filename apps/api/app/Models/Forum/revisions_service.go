package forum

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func (s *Service) ListTopicRevisions(ctx context.Context, actor identity.Actor, topicID int64, input RevisionListInput) (RevisionList, error) {
	if !actor.Can(identity.PermissionTopicRevisionViewAny) {
		return RevisionList{}, identity.ErrPermissionDenied
	}
	return s.store.ListTopicRevisions(ctx, topicID, input)
}

// ListTopicContributionTimeline 公开贡献时间线：无需修订查看权限，仅主题公开可读。
func (s *Service) ListTopicContributionTimeline(ctx context.Context, topicID int64, input RevisionListInput) (TopicContributionTimeline, error) {
	if topicID <= 0 {
		return TopicContributionTimeline{}, ErrTopicNotFound
	}
	return s.store.ListTopicContributionTimeline(ctx, topicID, input)
}

func (s *Service) GetTopicRevision(ctx context.Context, actor identity.Actor, topicID int64, revisionNo int64) (ForumRevisionDetail, error) {
	if !actor.Can(identity.PermissionTopicRevisionViewAny) {
		return ForumRevisionDetail{}, identity.ErrPermissionDenied
	}
	detail, err := s.store.GetTopicRevision(ctx, topicID, revisionNo)
	if err != nil {
		return ForumRevisionDetail{}, err
	}
	return s.withRevisionPreview(ctx, detail, "topic")
}

func (s *Service) ListCommentRevisions(ctx context.Context, actor identity.Actor, commentID int64, input RevisionListInput) (RevisionList, error) {
	if !actor.Can(identity.PermissionPostRevisionViewAny) {
		return RevisionList{}, identity.ErrPermissionDenied
	}
	return s.store.ListCommentRevisions(ctx, commentID, input)
}

func (s *Service) GetCommentRevision(ctx context.Context, actor identity.Actor, commentID int64, revisionNo int64) (ForumRevisionDetail, error) {
	if !actor.Can(identity.PermissionPostRevisionViewAny) {
		return ForumRevisionDetail{}, identity.ErrPermissionDenied
	}
	detail, err := s.store.GetCommentRevision(ctx, commentID, revisionNo)
	if err != nil {
		return ForumRevisionDetail{}, err
	}
	return s.withRevisionPreview(ctx, detail, "comment")
}

func (s *Service) withRevisionPreview(ctx context.Context, detail ForumRevisionDetail, resourceKind string) (ForumRevisionDetail, error) {
	settings, err := s.resolvedSettings(ctx)
	if err != nil {
		return ForumRevisionDetail{}, err
	}
	rendered, err := renderContentWithContext(s, ctx, ContentInput{
		RawContent:    detail.RawContent,
		SourceFormat:  detail.SourceFormat,
		EditorType:    detail.EditorType,
		EditorVersion: detail.EditorVersion,
	}, settings.ExcerptRuneLimit, resourceKind)
	if err != nil {
		return ForumRevisionDetail{}, err
	}
	detail.Preview = &HistoricalPreview{
		HTMLContent:   rendered.HTMLContent,
		PlainText:     rendered.PlainText,
		Excerpt:       rendered.Excerpt,
		RenderVersion: rendered.RenderVersion,
	}
	return detail, nil
}

func (s *Service) ListAdminForumTopics(ctx context.Context, actor identity.Actor, input AdminForumContentListInput) (AdminForumContentList, error) {
	if !canReadAdminTopicContent(actor) {
		return AdminForumContentList{}, identity.ErrPermissionDenied
	}
	return s.store.ListAdminForumTopics(ctx, input)
}

func (s *Service) GetAdminForumTopic(ctx context.Context, actor identity.Actor, topicID int64) (AdminForumTopicDetail, error) {
	if !canReadAdminTopicContent(actor) {
		return AdminForumTopicDetail{}, identity.ErrPermissionDenied
	}
	return s.store.GetAdminForumTopic(ctx, topicID)
}

func (s *Service) ListAdminForumComments(ctx context.Context, actor identity.Actor, input AdminForumContentListInput) (AdminForumContentList, error) {
	if !canReadAdminCommentContent(actor) {
		return AdminForumContentList{}, identity.ErrPermissionDenied
	}
	return s.store.ListAdminForumComments(ctx, input)
}

func (s *Service) GetAdminForumComment(ctx context.Context, actor identity.Actor, commentID int64) (AdminForumCommentDetail, error) {
	if !canReadAdminCommentContent(actor) {
		return AdminForumCommentDetail{}, identity.ErrPermissionDenied
	}
	return s.store.GetAdminForumComment(ctx, commentID)
}

func canReadAdminTopicContent(actor identity.Actor) bool {
	return actor.Can(identity.PermissionAdminAccess) &&
		(actor.Can(identity.PermissionTopicEditAny) || actor.Can(identity.PermissionTopicRevisionViewAny))
}

func canReadAdminCommentContent(actor identity.Actor) bool {
	return actor.Can(identity.PermissionAdminAccess) &&
		(actor.Can(identity.PermissionPostEditAny) || actor.Can(identity.PermissionPostRevisionViewAny))
}
