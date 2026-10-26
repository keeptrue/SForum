package apitokens

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestCreateAuthenticateRevoke(t *testing.T) {
	store := &memStore{byPublic: map[string]Record{}, byID: map[int64]Record{}}
	actor := identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionTopicCreate: true,
			identity.PermissionPostCreate:  true,
		},
	}
	svc := NewService(store, staticActorLoader{actor: actor})
	created, err := svc.Create(context.Background(), actor, CreateInput{
		Name: "ci", Scopes: []string{identity.PermissionTopicCreate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Plaintext == "" || created.ID == 0 {
		t.Fatalf("created=%#v", created)
	}

	auth, err := svc.AuthenticatePlaintext(context.Background(), created.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserID != 7 || len(auth.Scopes) != 1 {
		t.Fatalf("auth=%#v", auth)
	}

	if err := svc.Revoke(context.Background(), actor, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticatePlaintext(context.Background(), created.Plaintext); err != ErrTokenRevoked {
		t.Fatalf("want revoked, got %v", err)
	}
}

func TestAuthenticateRejectsMissingOrInactiveUser(t *testing.T) {
	active := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
	for _, test := range []struct {
		name   string
		loader ActorLoader
	}{
		{name: "missing actor loader", loader: nil},
		{name: "deleted user", loader: staticActorLoader{err: identity.ErrUserNotFound}},
		{name: "disabled user", loader: staticActorLoader{actor: identity.Actor{ID: 7, Status: identity.UserStatusDisabled}}},
		{name: "banned user", loader: staticActorLoader{actor: identity.Actor{ID: 7, Status: identity.UserStatusBanned}}},
		{name: "mismatched user", loader: staticActorLoader{actor: identity.Actor{ID: 8, Status: identity.UserStatusActive}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memStore{byPublic: map[string]Record{}, byID: map[int64]Record{}}
			creator := NewService(store, staticActorLoader{actor: active})
			created, err := creator.Create(context.Background(), active, CreateInput{Name: "test", Scopes: []string{identity.PermissionTopicCreate}})
			if err != nil {
				t.Fatal(err)
			}
			svc := NewService(store, test.loader)
			if _, err := svc.AuthenticatePlaintext(context.Background(), created.Plaintext); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("expected invalid token, got %v", err)
			}
			if store.byID[created.ID].LastUsedAt != nil {
				t.Fatal("inactive user token must not update last_used")
			}
		})
	}
}

func TestAuthenticateRejectsExpiredTokenBeforeActorLoad(t *testing.T) {
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionTopicCreate: true}}
	store := &memStore{byPublic: map[string]Record{}, byID: map[int64]Record{}}
	svc := NewService(store, staticActorLoader{actor: actor})
	now := time.Now().UTC()
	svc.now = func() time.Time { return now }
	expiresAt := now.Add(time.Minute)
	created, err := svc.Create(context.Background(), actor, CreateInput{Name: "expiring", Scopes: []string{identity.PermissionTopicCreate}, ExpiresAt: &expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return expiresAt.Add(time.Second) }
	if _, err := svc.AuthenticatePlaintext(context.Background(), created.Plaintext); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected expired token, got %v", err)
	}
}

func TestRestrictActorStripsSuperAdmin(t *testing.T) {
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		RoleKeys: []string{identity.RoleSuperAdmin, identity.RoleMember},
		Permissions: map[string]bool{
			identity.PermissionTopicCreate: true,
		},
	}
	if !actor.IsSuperAdmin() {
		t.Fatal("expected super admin")
	}
	// 仍为 super_admin 时，当前权限覆盖全部 scope；结果写入 Permissions 后去掉角色绕过。
	restricted := RestrictActor(actor, []string{identity.PermissionTopicCreate, identity.PermissionSettingsManage})
	if restricted.IsSuperAdmin() {
		t.Fatal("PAT must not retain super_admin bypass")
	}
	if !restricted.Can(identity.PermissionTopicCreate) {
		t.Fatal("expected topic.create from scopes ∩ current super_admin")
	}
	if !restricted.Can(identity.PermissionSettingsManage) {
		t.Fatal("expected settings.manage from scopes ∩ current super_admin")
	}
	// 未列入 scopes 的权限不得出现。
	if restricted.Can(identity.PermissionUserManage) {
		t.Fatal("permission without scope must not pass")
	}
}

func TestRestrictActorDemotedSuperAdminLosesRevokedScopes(t *testing.T) {
	// 已降级为普通用户：仅剩 topic.create，令牌 scopes 仍含 settings.manage。
	actor := identity.Actor{
		ID: 1, Status: identity.UserStatusActive,
		RoleKeys: []string{identity.RoleMember},
		Permissions: map[string]bool{
			identity.PermissionTopicCreate: true,
		},
	}
	restricted := RestrictActor(actor, []string{
		identity.PermissionTopicCreate,
		identity.PermissionSettingsManage,
	})
	if restricted.Can(identity.PermissionSettingsManage) {
		t.Fatal("demoted user must not keep settings.manage via old PAT scope")
	}
	if !restricted.Can(identity.PermissionTopicCreate) {
		t.Fatal("retained permission ∩ scope should work")
	}
}

func TestRestrictActorIntersectsCurrentPermissionsWithScopes(t *testing.T) {
	// 令牌 scopes 含 topic.create + post.create；用户当前只剩 post.create。
	actor := identity.Actor{
		ID: 2, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionPostCreate: true,
		},
	}
	restricted := RestrictActor(actor, []string{
		identity.PermissionTopicCreate,
		identity.PermissionPostCreate,
	})
	if restricted.Can(identity.PermissionTopicCreate) {
		t.Fatal("revoked user permission must not remain on PAT")
	}
	if !restricted.Can(identity.PermissionPostCreate) {
		t.Fatal("current permission ∩ scope should remain")
	}
}

func TestRestrictActorScopeWithoutCurrentPermissionDenied(t *testing.T) {
	actor := identity.Actor{
		ID: 3, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionTopicCreate: true,
		},
	}
	// scope 有 settings.manage，但用户从未持有。
	restricted := RestrictActor(actor, []string{identity.PermissionSettingsManage})
	if restricted.Can(identity.PermissionSettingsManage) {
		t.Fatal("scope alone must not grant permission")
	}
	if restricted.Can(identity.PermissionTopicCreate) {
		t.Fatal("permission without matching scope must not remain")
	}
}

func TestRejectScopeUserLacks(t *testing.T) {
	svc := NewService(&memStore{byPublic: map[string]Record{}, byID: map[int64]Record{}}, nil)
	actor := identity.Actor{ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionPostCreate: true}}
	_, err := svc.Create(context.Background(), actor, CreateInput{Name: "x", Scopes: []string{identity.PermissionSettingsManage}})
	if err != ErrScopeNotAllowed {
		t.Fatalf("got %v", err)
	}
}

type memStore struct {
	next     int64
	byPublic map[string]Record
	byID     map[int64]Record
}

type staticActorLoader struct {
	actor identity.Actor
	err   error
}

func (l staticActorLoader) LoadActor(context.Context, int64) (identity.Actor, error) {
	return l.actor, l.err
}

func (m *memStore) Create(_ context.Context, userID int64, publicID, tokenHash, name string, scopes []string, expiresAt *time.Time) (Record, error) {
	m.next++
	rec := Record{ID: m.next, UserID: userID, PublicID: publicID, TokenHash: tokenHash, Name: name, Scopes: scopes, ExpiresAt: expiresAt, CreatedAt: time.Now().UTC()}
	m.byPublic[publicID] = rec
	m.byID[rec.ID] = rec
	return rec, nil
}
func (m *memStore) ListByUser(context.Context, int64, bool) ([]Record, error) { return nil, nil }
func (m *memStore) GetByPublicID(_ context.Context, publicID string) (Record, error) {
	rec, ok := m.byPublic[publicID]
	if !ok {
		return Record{}, ErrTokenNotFound
	}
	return rec, nil
}
func (m *memStore) GetByIDForUser(_ context.Context, userID, id int64) (Record, error) {
	rec, ok := m.byID[id]
	if !ok || rec.UserID != userID {
		return Record{}, ErrTokenNotFound
	}
	return rec, nil
}
func (m *memStore) Revoke(_ context.Context, userID, id int64) error {
	rec, ok := m.byID[id]
	if !ok || rec.UserID != userID {
		return ErrTokenNotFound
	}
	now := time.Now().UTC()
	rec.RevokedAt = &now
	m.byID[id] = rec
	m.byPublic[rec.PublicID] = rec
	return nil
}
func (m *memStore) TouchLastUsed(_ context.Context, id int64) error {
	rec, ok := m.byID[id]
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	rec.LastUsedAt = &now
	m.byID[id] = rec
	m.byPublic[rec.PublicID] = rec
	return nil
}
