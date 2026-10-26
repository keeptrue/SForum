package forum

import (
	"context"
	"strings"
	"unicode/utf8"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const revisionRedactionConfirmation = "REDACT"

// RestoreTopic intentionally re-enters UpdateTopic. This keeps filters,
// rendering, moderation, cache invalidation, and search projection canonical.
func (s *Service) RestoreTopic(ctx context.Context, actor identity.Actor, topicID, revisionNo int64, input RestoreRevisionInput) (TopicDetail, error) {
	if !actor.Can(identity.PermissionTopicRevisionViewAny) || !actor.Can(identity.PermissionTopicEditAny) {
		return TopicDetail{}, identity.ErrPermissionDenied
	}
	topic, err := s.store.GetTopicForAction(ctx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	if topic.Status == TopicStatusDeleted || !matchesExpectedRevision(input.ExpectedRevision, topic.CurrentRevision) {
		if topic.Status == TopicStatusDeleted {
			return TopicDetail{}, ErrTopicNotFound
		}
		return TopicDetail{}, ErrRevisionConflict
	}
	if err := validateRestoreReason(input.Reason); err != nil {
		return TopicDetail{}, err
	}
	target, err := s.store.GetTopicRevision(ctx, topicID, revisionNo)
	if err != nil {
		return TopicDetail{}, err
	}
	content := revisionContentInput(target, target.SnapshotComplete)
	restore := UpdateTopicInput{
		TopicID: topicID, ExpectedRevision: input.ExpectedRevision, Reason: strings.TrimSpace(input.Reason),
		Content: &content, Operation: RevisionOperationRestore, RestoredFromRevisionID: target.ID,
		RestoredFromRevisionNo: target.RevisionNo, HistoricalAttachmentOwnerID: topic.AuthorUserID,
	}
	if target.SnapshotComplete {
		if target.TopicMetadata == nil || strings.TrimSpace(target.TopicMetadata.Title) == "" || strings.TrimSpace(target.TopicMetadata.CategorySlug) == "" {
			return TopicDetail{}, ErrRevisionNotRestorable
		}
		restore.Title = stringPtr(target.TopicMetadata.Title)
		restore.CategorySlug = stringPtr(target.TopicMetadata.CategorySlug)
		restore.TagSlugs = append([]string{}, target.TopicMetadata.TagSlugs...)
	}
	return s.UpdateTopic(ctx, actor, restore)
}

func (s *Service) RestoreComment(ctx context.Context, actor identity.Actor, commentID, revisionNo int64, input RestoreRevisionInput) (Comment, error) {
	if !actor.Can(identity.PermissionPostRevisionViewAny) || !actor.Can(identity.PermissionPostEditAny) {
		return Comment{}, identity.ErrPermissionDenied
	}
	comment, err := s.store.GetCommentSummary(ctx, commentID)
	if err != nil {
		return Comment{}, err
	}
	if comment.Status == CommentStatusDeleted || !matchesExpectedRevision(input.ExpectedRevision, comment.CurrentRevision) {
		if comment.Status == CommentStatusDeleted {
			return Comment{}, ErrCommentNotFound
		}
		return Comment{}, ErrRevisionConflict
	}
	if err := validateRestoreReason(input.Reason); err != nil {
		return Comment{}, err
	}
	target, err := s.store.GetCommentRevision(ctx, commentID, revisionNo)
	if err != nil {
		return Comment{}, err
	}
	return s.UpdateComment(ctx, actor, UpdateCommentInput{
		CommentID: commentID, ExpectedRevision: input.ExpectedRevision, Reason: strings.TrimSpace(input.Reason),
		Content: revisionContentInput(target, target.SnapshotComplete), Operation: RevisionOperationRestore,
		RestoredFromRevisionID: target.ID, RestoredFromRevisionNo: target.RevisionNo,
		HistoricalAttachmentOwnerID: comment.AuthorUserID,
	})
}

func (s *Service) RedactTopicRevision(ctx context.Context, actor identity.Actor, topicID, revisionNo int64, input RedactRevisionInput) error {
	if !actor.IsSuperAdmin() {
		return ErrRevisionRedactionForbidden
	}
	if err := validateRedactionInput(input); err != nil {
		return err
	}
	return s.store.RedactTopicRevision(ctx, RevisionRedactionRecord{TargetID: topicID, TargetType: "topic", RevisionNo: revisionNo, ExpectedRevision: input.ExpectedRevision, ActorUserID: actor.ID, Reason: strings.TrimSpace(input.Reason)})
}

func (s *Service) RedactCommentRevision(ctx context.Context, actor identity.Actor, commentID, revisionNo int64, input RedactRevisionInput) error {
	if !actor.IsSuperAdmin() {
		return ErrRevisionRedactionForbidden
	}
	if err := validateRedactionInput(input); err != nil {
		return err
	}
	return s.store.RedactCommentRevision(ctx, RevisionRedactionRecord{TargetID: commentID, TargetType: "comment", RevisionNo: revisionNo, ExpectedRevision: input.ExpectedRevision, ActorUserID: actor.ID, Reason: strings.TrimSpace(input.Reason)})
}

func revisionContentInput(detail ForumRevisionDetail, restoreAttachments bool) ContentInput {
	content := ContentInput{RawContent: detail.RawContent, SourceFormat: detail.SourceFormat, EditorType: detail.EditorType, EditorVersion: detail.EditorVersion}
	if restoreAttachments {
		attachments := append([]int64{}, detail.Attachments.IDs...)
		content.AttachmentIDs = &attachments
	}
	return content
}

func validateRestoreReason(reason string) error {
	if strings.TrimSpace(reason) == "" || utf8.RuneCountInString(strings.TrimSpace(reason)) > 500 {
		return ErrRevisionReasonRequired
	}
	return nil
}

func validateRedactionInput(input RedactRevisionInput) error {
	if input.ExpectedRevision <= 0 || strings.TrimSpace(input.Confirmation) != revisionRedactionConfirmation || strings.TrimSpace(input.Reason) == "" || utf8.RuneCountInString(strings.TrimSpace(input.Reason)) > 500 {
		return ErrRevisionRedactionForbidden
	}
	return nil
}

func stringPtr(value string) *string { return &value }
