package apitokens

import (
	"context"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// ScopedActorStore 在 Bearer PAT 请求上把 Actor 权限收窄到 token scopes。
type ScopedActorStore struct {
	Inner identity.ActorStore
}

func (s ScopedActorStore) LoadActor(ctx context.Context, userID int64) (identity.Actor, error) {
	actor, err := s.Inner.LoadActor(ctx, userID)
	if err != nil {
		return identity.Actor{}, err
	}
	scopes := ScopesFromContext(ctx)
	if len(scopes) == 0 {
		return actor, nil
	}
	return RestrictActor(actor, scopes), nil
}
