package forum

import (
	"context"
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

var ErrProtectedShortcodePolicyUnavailable = errors.New("forum: protected shortcode policy unavailable")

type ProtectedShortcodePolicyProjection interface {
	AuthorDecision(context.Context, int64, string, int64) (authenticated, resourceAuthor, topicAuthor, found bool, err error)
	ReplyEligibility(context.Context, int64, int64) (authenticated, eligible, found bool, err error)
	PublicCommentTopicID(context.Context, int64) (topicID int64, found bool, err error)
}

type ProtectedShortcodePolicy struct {
	actors      identity.ActorStore
	projections ProtectedShortcodePolicyProjection
}

func NewProtectedShortcodePolicy(actors identity.ActorStore, projections ProtectedShortcodePolicyProjection) *ProtectedShortcodePolicy {
	return &ProtectedShortcodePolicy{actors: actors, projections: projections}
}

func (p *ProtectedShortcodePolicy) AuthorizeProtectedShortcode(
	ctx context.Context,
	request ProtectedShortcodeAuthorizationRequest,
) (ProtectedShortcodeDecision, error) {
	fallback := protectedShortcodeFallbackCode(request.ID)
	if !protectedShortcodeRequestValid(request) || p == nil || p.actors == nil || p.projections == nil {
		return ProtectedShortcodeDecision{FallbackCode: fallback}, ErrProtectedShortcodePolicyUnavailable
	}
	if request.Viewer.ID <= 0 {
		return ProtectedShortcodeDecision{FallbackCode: fallback}, nil
	}
	actor, err := p.actors.LoadActor(ctx, request.Viewer.ID)
	if err != nil || actor.ID != request.Viewer.ID || !actor.IsActive() {
		return ProtectedShortcodeDecision{FallbackCode: fallback}, err
	}

	switch request.ID {
	case contentregistry.ShortcodeLoginID:
		return ProtectedShortcodeDecision{Allowed: true}, nil
	case contentregistry.ShortcodeReplyID:
		topicID, found, resolveErr := p.protectedShortcodeTopicID(ctx, request.Resource)
		if resolveErr != nil || !found {
			return ProtectedShortcodeDecision{FallbackCode: fallback}, resolveErr
		}
		authenticated, eligible, found, projectionErr := p.projections.ReplyEligibility(ctx, actor.ID, topicID)
		return ProtectedShortcodeDecision{Allowed: found && authenticated && eligible, FallbackCode: fallback}, projectionErr
	case contentregistry.ShortcodeOnlyAuthorID:
		if request.Resource.Type != "comment" {
			return ProtectedShortcodeDecision{FallbackCode: fallback}, nil
		}
		authenticated, resourceAuthor, topicAuthor, found, projectionErr := p.projections.AuthorDecision(
			ctx, actor.ID, request.Resource.Type, request.Resource.ID,
		)
		return ProtectedShortcodeDecision{
			Allowed: found && authenticated && (resourceAuthor || topicAuthor), FallbackCode: fallback,
		}, projectionErr
	default:
		return ProtectedShortcodeDecision{FallbackCode: "shortcode.protected.unavailable"}, ErrProtectedShortcodePolicyUnavailable
	}
}

func (p *ProtectedShortcodePolicy) protectedShortcodeTopicID(
	ctx context.Context,
	resource contentregistry.ShortcodeResourceKey,
) (int64, bool, error) {
	switch resource.Type {
	case "topic":
		return resource.ID, resource.ID > 0, nil
	case "comment":
		return p.projections.PublicCommentTopicID(ctx, resource.ID)
	default:
		return 0, false, ErrProtectedShortcodePolicyUnavailable
	}
}

func protectedShortcodeRequestValid(request ProtectedShortcodeAuthorizationRequest) bool {
	if (request.Resource.Type != "topic" && request.Resource.Type != "comment") || request.Resource.ID <= 0 {
		return false
	}
	versions := map[string]string{
		contentregistry.ShortcodeLoginID:      contentregistry.ShortcodeLoginID + "@1",
		contentregistry.ShortcodeReplyID:      contentregistry.ShortcodeReplyID + "@1",
		contentregistry.ShortcodeOnlyAuthorID: contentregistry.ShortcodeOnlyAuthorID + "@1",
	}
	return versions[request.ID] != "" && request.ContractVersion == versions[request.ID]
}

func protectedShortcodeFallbackCode(id string) string {
	switch id {
	case contentregistry.ShortcodeLoginID:
		return "shortcode.login.required"
	case contentregistry.ShortcodeReplyID:
		return "shortcode.reply.required"
	case contentregistry.ShortcodeOnlyAuthorID:
		return "shortcode.only_author.private"
	default:
		return "shortcode.protected.unavailable"
	}
}
