package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

var ErrComponentPermissionAuthorityUnavailable = errors.New("identity: component permission authority is unavailable")

type ComponentPermissionRegistry interface {
	ResolvePermission(string) (identityregistry.PermissionContribution, error)
}

// ComponentPermissionAuthorizer keeps retained Host grants separate from the
// live permission catalog. A retired extension key is denied even when an old
// role or user override still contains that key.
type ComponentPermissionAuthorizer struct {
	actors   ActorStore
	registry ComponentPermissionRegistry
}

func NewComponentPermissionAuthorizer(
	actors ActorStore,
	registry ComponentPermissionRegistry,
) *ComponentPermissionAuthorizer {
	return &ComponentPermissionAuthorizer{actors: actors, registry: registry}
}

func (a *ComponentPermissionAuthorizer) AuthorizeComponent(
	ctx context.Context,
	actorUserID int64,
	permission string,
) (bool, error) {
	permission = strings.ToLower(strings.TrimSpace(permission))
	if ctx == nil || permission == "" {
		return false, ErrComponentPermissionAuthorityUnavailable
	}
	if actorUserID <= 0 {
		return false, nil
	}

	core := isCoreComponentPermission(permission)
	var first identityregistry.PermissionContribution
	if !core {
		if a == nil || a.registry == nil {
			return false, ErrComponentPermissionAuthorityUnavailable
		}
		resolved, err := a.registry.ResolvePermission(permission)
		if errors.Is(err, identityregistry.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("resolve component permission: %w", err)
		}
		first = resolved
	}
	if a == nil || a.actors == nil {
		return false, ErrComponentPermissionAuthorityUnavailable
	}
	actor, err := a.actors.LoadActor(ctx, actorUserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("load component actor: %w", err)
	}
	if actor.ID != actorUserID || !actor.IsActive() || !actor.Can(permission) {
		return false, nil
	}
	if core {
		return true, nil
	}

	current, err := a.registry.ResolvePermission(permission)
	if errors.Is(err, identityregistry.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("recheck component permission: %w", err)
	}
	return sameActiveComponentPermission(first, current), nil
}

func isCoreComponentPermission(permission string) bool {
	for _, seeded := range SeedPermissions {
		if seeded.Key == permission {
			return true
		}
	}
	return false
}

func sameActiveComponentPermission(
	left identityregistry.PermissionContribution,
	right identityregistry.PermissionContribution,
) bool {
	return left.Key == right.Key && left.ContractVersion == right.ContractVersion && left.Artifact == right.Artifact
}
