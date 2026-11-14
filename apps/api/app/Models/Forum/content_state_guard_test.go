package forum

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 主题与评论共用同一份不可编辑状态集合，常量一旦漂移这里先炸。
func TestUnavailableContentStatusesCoverTopicAndCommentConstants(t *testing.T) {
	for name, status := range map[string]string{
		"topic hidden":    TopicStatusHidden,
		"topic deleted":   TopicStatusDeleted,
		"comment hidden":  CommentStatusHidden,
		"comment deleted": CommentStatusDeleted,
	} {
		if _, ok := unavailableContentStatuses[status]; !ok {
			t.Fatalf("%s status %q must be treated as unavailable", name, status)
		}
	}
	for _, status := range []string{TopicStatusActive, TopicStatusPending, TopicStatusLocked} {
		if _, ok := unavailableContentStatuses[status]; ok {
			t.Fatalf("status %q must stay editable", status)
		}
	}
}

func TestServiceUpdateTopicRejectsUnavailableStatusAsNotFound(t *testing.T) {
	author := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicEditOwn: true},
	}
	moderator := identity.Actor{
		ID:          20,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionTopicEditAny: true},
	}
	stranger := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	tests := []struct {
		name   string
		status string
		actor  identity.Actor
	}{
		{name: "author edits hidden topic", status: TopicStatusHidden, actor: author},
		{name: "author edits deleted topic", status: TopicStatusDeleted, actor: author},
		{name: "moderator edits hidden topic", status: TopicStatusHidden, actor: moderator},
		{name: "moderator edits deleted topic", status: TopicStatusDeleted, actor: moderator},
		// 无权限者也不该拿到 PermissionDenied：那等于确认该 ID 仍然存在。
		{name: "stranger probes hidden topic", status: TopicStatusHidden, actor: stranger},
		{name: "stranger probes deleted topic", status: TopicStatusDeleted, actor: stranger},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newServiceFakeStore()
			store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: tt.status, CurrentRevision: 3}
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})

			title := "被拒绝的编辑"
			_, err := service.UpdateTopic(context.Background(), tt.actor, UpdateTopicInput{
				TopicID: 7, ExpectedRevision: 3, Title: &title,
			})
			if !errors.Is(err, ErrTopicNotFound) {
				t.Fatalf("expected ErrTopicNotFound for status %q, got %v", tt.status, err)
			}
			if store.updatedTopic.TopicID != 0 {
				t.Fatalf("unavailable topic must not reach the store write, got %#v", store.updatedTopic)
			}
		})
	}
}

func TestServiceUpdateCommentRejectsUnavailableStatusAsNotFound(t *testing.T) {
	author := identity.Actor{
		ID:          12,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostEditOwn: true},
	}
	moderator := identity.Actor{
		ID:          20,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionPostEditAny: true},
	}
	stranger := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{}}

	tests := []struct {
		name   string
		status string
		actor  identity.Actor
	}{
		{name: "author edits hidden comment", status: CommentStatusHidden, actor: author},
		{name: "author edits deleted comment", status: CommentStatusDeleted, actor: author},
		{name: "moderator edits hidden comment", status: CommentStatusHidden, actor: moderator},
		{name: "moderator edits deleted comment", status: CommentStatusDeleted, actor: moderator},
		{name: "stranger probes hidden comment", status: CommentStatusHidden, actor: stranger},
		{name: "stranger probes deleted comment", status: CommentStatusDeleted, actor: stranger},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newServiceFakeStore()
			store.commentSummary = CommentSummary{ID: 5, TopicID: 7, AuthorUserID: 12, Status: tt.status, CurrentRevision: 3}
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})

			_, err := service.UpdateComment(context.Background(), tt.actor, UpdateCommentInput{
				CommentID: 5, ExpectedRevision: 3,
				Content: ContentInput{RawContent: "被拒绝的编辑", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
			})
			if !errors.Is(err, ErrCommentNotFound) {
				t.Fatalf("expected ErrCommentNotFound for status %q, got %v", tt.status, err)
			}
			if store.updatedComment.CommentID != 0 {
				t.Fatalf("unavailable comment must not reach the store write, got %#v", store.updatedComment)
			}
		})
	}
}

// 回归护栏：可见/待审/锁定状态必须仍可编辑，版主审核队列不能因为状态谓词被锁死。
func TestServiceUpdateContentAllowsReviewableStatuses(t *testing.T) {
	for _, status := range []string{TopicStatusActive, TopicStatusPending, TopicStatusLocked} {
		t.Run("topic "+status, func(t *testing.T) {
			store := newServiceFakeStore()
			store.actionTopic = TopicSummary{ID: 7, AuthorUserID: 12, Status: status, CurrentRevision: 1}
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})
			moderator := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicEditAny: true}}

			title := "审核中的修订"
			if _, err := service.UpdateTopic(context.Background(), moderator, UpdateTopicInput{
				TopicID: 7, ExpectedRevision: 1, Reason: "moderation correction", Title: &title,
			}); err != nil {
				t.Fatalf("moderator must still edit %q topics, got %v", status, err)
			}
			if store.updatedTopic.TopicID != 7 {
				t.Fatalf("expected store write for %q topic, got %#v", status, store.updatedTopic)
			}
		})
	}

	for _, status := range []string{CommentStatusActive, CommentStatusPending} {
		t.Run("comment "+status, func(t *testing.T) {
			store := newServiceFakeStore()
			store.commentSummary = CommentSummary{ID: 5, TopicID: 7, AuthorUserID: 12, Status: status, CurrentRevision: 1}
			service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})
			moderator := identity.Actor{ID: 20, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostEditAny: true}}

			if _, err := service.UpdateComment(context.Background(), moderator, UpdateCommentInput{
				CommentID: 5, ExpectedRevision: 1, Reason: "moderation correction",
				Content: ContentInput{RawContent: "审核中的评论修订", SourceFormat: SourceFormatMarkdown, EditorType: EditorTypeMarkdown},
			}); err != nil {
				t.Fatalf("moderator must still edit %q comments, got %v", status, err)
			}
			if store.updatedComment.CommentID != 5 {
				t.Fatalf("expected store write for %q comment, got %#v", status, store.updatedComment)
			}
		})
	}
}
