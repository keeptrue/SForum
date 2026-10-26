package forum

import (
	"context"
	"fmt"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

type protectedPolicyActorStore map[int64]identity.Actor

func (s protectedPolicyActorStore) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	actor, ok := s[userID]
	if !ok {
		return identity.Actor{}, fmt.Errorf("actor not found")
	}
	return actor, nil
}

type protectedPolicyProjection struct{}

func (protectedPolicyProjection) AuthorDecision(_ context.Context, actorID int64, resourceType string, resourceID int64) (bool, bool, bool, bool, error) {
	if resourceType != "comment" || resourceID != 100 {
		return false, false, false, false, nil
	}
	return actorID > 0, actorID == 2, actorID == 1, true, nil
}

func (protectedPolicyProjection) ReplyEligibility(_ context.Context, actorID, topicID int64) (bool, bool, bool, error) {
	if topicID != 10 {
		return false, false, false, nil
	}
	return actorID > 0, actorID == 1 || actorID == 2, true, nil
}

func (protectedPolicyProjection) PublicCommentTopicID(_ context.Context, commentID int64) (int64, bool, error) {
	if commentID != 100 {
		return 0, false, nil
	}
	return 10, true, nil
}

func TestProtectedShortcodePolicyActorAndResourceMatrix(t *testing.T) {
	actors := protectedPolicyActorStore{
		1: {ID: 1, Status: identity.UserStatusActive},
		2: {ID: 2, Status: identity.UserStatusActive},
		3: {ID: 3, Status: identity.UserStatusActive},
		4: {ID: 4, Status: identity.UserStatusActive, RoleKeys: []string{"staff"}},
		5: {ID: 5, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin}},
		6: {ID: 6, Status: identity.UserStatusDisabled},
		7: {ID: 7, Status: identity.UserStatusBanned},
	}
	policy := NewProtectedShortcodePolicy(actors, protectedPolicyProjection{})
	topic := contentregistry.ShortcodeResourceKey{Type: "topic", ID: 10}
	comment := contentregistry.ShortcodeResourceKey{Type: "comment", ID: 100}
	tests := []struct {
		name     string
		id       string
		resource contentregistry.ShortcodeResourceKey
		viewer   int64
		allowed  bool
		fallback string
	}{
		{name: "login anonymous", id: contentregistry.ShortcodeLoginID, resource: topic, fallback: "shortcode.login.required"},
		{name: "login active", id: contentregistry.ShortcodeLoginID, resource: topic, viewer: 3, allowed: true},
		{name: "login inactive", id: contentregistry.ShortcodeLoginID, resource: topic, viewer: 6, fallback: "shortcode.login.required"},
		{name: "login banned", id: contentregistry.ShortcodeLoginID, resource: topic, viewer: 7, fallback: "shortcode.login.required"},
		{name: "reply topic author", id: contentregistry.ShortcodeReplyID, resource: topic, viewer: 1, allowed: true},
		{name: "reply accepted commenter", id: contentregistry.ShortcodeReplyID, resource: topic, viewer: 2, allowed: true},
		{name: "reply unrelated", id: contentregistry.ShortcodeReplyID, resource: topic, viewer: 3, fallback: "shortcode.reply.required"},
		{name: "reply staff has no bypass", id: contentregistry.ShortcodeReplyID, resource: topic, viewer: 4, fallback: "shortcode.reply.required"},
		{name: "reply super admin has no bypass", id: contentregistry.ShortcodeReplyID, resource: topic, viewer: 5, fallback: "shortcode.reply.required"},
		{name: "reply resolves comment topic", id: contentregistry.ShortcodeReplyID, resource: comment, viewer: 2, allowed: true},
		{name: "only author comment author", id: contentregistry.ShortcodeOnlyAuthorID, resource: comment, viewer: 2, allowed: true},
		{name: "only author topic author", id: contentregistry.ShortcodeOnlyAuthorID, resource: comment, viewer: 1, allowed: true},
		{name: "only author unrelated", id: contentregistry.ShortcodeOnlyAuthorID, resource: comment, viewer: 3, fallback: "shortcode.only_author.private"},
		{name: "only author staff no bypass", id: contentregistry.ShortcodeOnlyAuthorID, resource: comment, viewer: 4, fallback: "shortcode.only_author.private"},
		{name: "only author rejected in topic", id: contentregistry.ShortcodeOnlyAuthorID, resource: topic, viewer: 1, fallback: "shortcode.only_author.private"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, _ := policy.AuthorizeProtectedShortcode(t.Context(), ProtectedShortcodeAuthorizationRequest{
				Resource: test.resource, ID: test.id, ContractVersion: test.id + "@1", Viewer: identity.Actor{ID: test.viewer, Status: identity.UserStatusActive},
			})
			if decision.Allowed != test.allowed || (!test.allowed && decision.FallbackCode != test.fallback) {
				t.Fatalf("decision=%#v", decision)
			}
		})
	}
}

func TestProtectedShortcodePolicyRejectsStaleActorAndContract(t *testing.T) {
	actors := protectedPolicyActorStore{1: {ID: 1, Status: identity.UserStatusBanned}}
	policy := NewProtectedShortcodePolicy(actors, protectedPolicyProjection{})
	resource := contentregistry.ShortcodeResourceKey{Type: "topic", ID: 10}
	for _, request := range []ProtectedShortcodeAuthorizationRequest{
		{Resource: resource, ID: contentregistry.ShortcodeLoginID, ContractVersion: contentregistry.ShortcodeLoginID + "@1", Viewer: identity.Actor{ID: 1, Status: identity.UserStatusActive}},
		{Resource: resource, ID: contentregistry.ShortcodeLoginID, ContractVersion: contentregistry.ShortcodeLoginID + "@2", Viewer: identity.Actor{ID: 1, Status: identity.UserStatusActive}},
	} {
		decision, _ := policy.AuthorizeProtectedShortcode(t.Context(), request)
		if decision.Allowed {
			t.Fatalf("stale actor or contract was allowed: %#v", request)
		}
	}
}
