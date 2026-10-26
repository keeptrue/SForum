package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type notificationActorLoaderFixture struct {
	actor identity.Actor
	err   error
}

func (f notificationActorLoaderFixture) LoadActor(context.Context, int64) (identity.Actor, error) {
	return f.actor, f.err
}

func TestModerationNotificationTargetsRequireCurrentReviewPermission(t *testing.T) {
	allowed := notificationTargetVisibilityAdapter{identities: notificationActorLoaderFixture{actor: identity.Actor{
		ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true},
	}}}
	available, path, err := allowed.ResolveNotificationTarget(context.Background(), 7, "moderation_topic", 42)
	if err != nil || !available || !strings.Contains(path, "reviewType=topic") || !strings.Contains(path, "reviewId=42") {
		t.Fatalf("allowed moderation target = available:%v path:%q err:%v", available, path, err)
	}

	denied := notificationTargetVisibilityAdapter{identities: notificationActorLoaderFixture{actor: identity.Actor{ID: 8, Status: identity.UserStatusActive}}}
	if available, path, err := denied.ResolveNotificationTarget(context.Background(), 8, "moderation_comment", 9); err != nil || available || path != "" {
		t.Fatalf("denied moderation target = available:%v path:%q err:%v", available, path, err)
	}

	injected := errors.New("actor unavailable")
	failing := notificationTargetVisibilityAdapter{identities: notificationActorLoaderFixture{err: injected}}
	if _, _, err := failing.ResolveNotificationTarget(context.Background(), 8, "moderation_topic", 9); !errors.Is(err, injected) {
		t.Fatalf("actor resolution error = %v", err)
	}
}
