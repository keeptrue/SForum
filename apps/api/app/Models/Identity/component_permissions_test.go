package identity

import (
	"context"
	"errors"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type componentPermissionActorStore struct {
	actor Actor
	err   error
}

func (s componentPermissionActorStore) LoadActor(context.Context, int64) (Actor, error) {
	return s.actor, s.err
}

type componentPermissionRegistry struct {
	byKey map[string]identityregistry.PermissionContribution
	err   error
}

func (r componentPermissionRegistry) ResolvePermission(key string) (identityregistry.PermissionContribution, error) {
	if r.err != nil {
		return identityregistry.PermissionContribution{}, r.err
	}
	value, ok := r.byKey[key]
	if !ok {
		return identityregistry.PermissionContribution{}, identityregistry.ErrNotFound
	}
	return value, nil
}

func TestComponentPermissionAuthorizerCorePermission(t *testing.T) {
	t.Parallel()

	authorizer := NewComponentPermissionAuthorizer(
		componentPermissionActorStore{
			actor: Actor{
				ID: 7, Status: UserStatusActive,
				Permissions: map[string]bool{PermissionUserView: true},
			},
		},
		nil,
	)
	allowed, err := authorizer.AuthorizeComponent(t.Context(), 7, PermissionUserView)
	if err != nil || !allowed {
		t.Fatalf("core allow: allowed=%v err=%v", allowed, err)
	}

	allowed, err = authorizer.AuthorizeComponent(t.Context(), 7, PermissionUserManage)
	if err != nil || allowed {
		t.Fatalf("core deny: allowed=%v err=%v", allowed, err)
	}
}

func TestComponentPermissionAuthorizerExtensionPermissionLiveCatalog(t *testing.T) {
	t.Parallel()

	const key = "plugin.membership.view"
	contribution := identityregistry.PermissionContribution{
		PermissionDefinition: identityregistry.PermissionDefinition{
			Key: key, ContractVersion: "1",
		},
		Artifact: identityregistry.Artifact{
			ExtensionID: "plugin.membership", ExtensionVersion: "1.0.0",
			PackageDigest: "sha256:plugin-a",
		},
	}
	registry := componentPermissionRegistry{
		byKey: map[string]identityregistry.PermissionContribution{key: contribution},
	}
	authorizer := NewComponentPermissionAuthorizer(
		componentPermissionActorStore{
			actor: Actor{
				ID: 3, Status: UserStatusActive,
				Permissions: map[string]bool{key: true},
			},
		},
		registry,
	)

	allowed, err := authorizer.AuthorizeComponent(t.Context(), 3, key)
	if err != nil || !allowed {
		t.Fatalf("live extension allow: allowed=%v err=%v", allowed, err)
	}

	// 目录中已退役的 key 即使旧 grant 仍在也必须拒绝。
	// authorizer 持有 registry 值拷贝，但 map 引用共享，必须就地删除。
	delete(registry.byKey, key)
	allowed, err = authorizer.AuthorizeComponent(t.Context(), 3, key)
	if err != nil || allowed {
		t.Fatalf("retired extension deny: allowed=%v err=%v", allowed, err)
	}
}

func TestComponentPermissionAuthorizerFailClosed(t *testing.T) {
	t.Parallel()

	authorizer := NewComponentPermissionAuthorizer(nil, nil)
	if _, err := authorizer.AuthorizeComponent(t.Context(), 1, "plugin.x"); !errors.Is(err, ErrComponentPermissionAuthorityUnavailable) {
		t.Fatalf("missing registry: %v", err)
	}
	if allowed, err := authorizer.AuthorizeComponent(t.Context(), 0, PermissionUserView); err != nil || allowed {
		t.Fatalf("anonymous: allowed=%v err=%v", allowed, err)
	}
}
