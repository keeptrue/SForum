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

// TestRestrictActorKeepsDeniedChildPermission 是 P1-1 补刀的回归护栏。
//
// 场景：用户持有 settings.manage，管理员又对 settings.mail.manage 写了 deny。
// 装载期把 deny 表示成 Actor.Permissions 里的显式 false 条目；RestrictActor 重建
// Permissions 时如果只装命中的 scope，PAT 主体的 map 就只剩父权限键，
// Can(settings.mail.manage) 会走父回退重新放行 —— deny 在 PAT 路径被绕过。
//
// 可达性：core.route.mail.* 的 guard 是 permission_any
// ["settings.mail.manage", "settings.manage"]，父键本身能过 guard，真正拦住 deny
// 的是控制器里那句 Can(settings.mail.manage)。所以这条 map 语义就是授权结论。
//
// 变异要求：删掉 RestrictActor 里保留 false 条目的那个循环，第一条断言必须红。
func TestRestrictActorKeepsDeniedChildPermission(t *testing.T) {
	actor := identity.Actor{
		ID: 5, Status: identity.UserStatusActive,
		RoleKeys: []string{identity.RoleMember},
		Permissions: map[string]bool{
			identity.PermissionSettingsManage:     true,
			identity.PermissionSettingsMailManage: false,
		},
	}

	restricted := RestrictActor(actor, []string{identity.PermissionSettingsManage})
	if restricted.Can(identity.PermissionSettingsMailManage) {
		t.Fatalf("deny 被 PAT 路径绕过：Can(settings.mail.manage)=true，Permissions=%v", restricted.Permissions)
	}
	// 父权限展开本身仍然生效：deny 只压住自己那一个键。
	if !restricted.Can(identity.PermissionSettingsSiteManage) {
		t.Fatalf("父键展开不应被 deny 影响：Can(settings.site.manage)=false，Permissions=%v", restricted.Permissions)
	}
	// 显式 false 必须真的留在 map 里（只写不删），否则下游还会走父回退。
	if granted, present := restricted.Permissions[identity.PermissionSettingsMailManage]; !present || granted {
		t.Fatalf("deny 条目没有保留成显式 false：present=%t granted=%t", present, granted)
	}

	// scope 为空也不能把 deny 丢掉：deny 与 token scope 无关。
	empty := RestrictActor(actor, nil)
	if granted, present := empty.Permissions[identity.PermissionSettingsMailManage]; !present || granted {
		t.Fatalf("空 scope 下 deny 条目被丢弃：present=%t granted=%t", present, granted)
	}
	if empty.Can(identity.PermissionSettingsMailManage) {
		t.Fatal("空 scope 下 deny 被绕过")
	}
	if empty.Can(identity.PermissionSettingsManage) {
		t.Fatal("没有 scope 的父权限不应残留")
	}
}

func TestRestrictActorDropsOutOfScopeEarned(t *testing.T) {
	// D26：Earned 不参与裁决（Can 读 Permissions），但它会通过 GrantSource 解释来源，
	// 而 Actor 的字段对令牌消费者是可见的。只裁 Permissions 不裁 Earned，
	// 消费者就会展示「已解锁」而实际调用 403 —— 解释层与裁决层分叉。
	actor := identity.Actor{
		ID: 4, Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionTopicCreate:      true,
			identity.PermissionAttachmentUpload: true,
		},
		TrustLevel: 2, TrustLevelResolved: true,
		Earned: map[string]bool{
			identity.PermissionTopicCreate:      true,
			identity.PermissionAttachmentUpload: true,
		},
	}
	restricted := RestrictActor(actor, []string{identity.PermissionTopicCreate})

	if !restricted.Earned[identity.PermissionTopicCreate] {
		t.Fatal("scope 内的已挣得键必须保留，否则来源解释会退化成 static")
	}
	if restricted.Earned[identity.PermissionAttachmentUpload] {
		t.Fatal("超出 scope 的已挣得键必须被裁掉（D26）")
	}
	if got := restricted.GrantSource(identity.PermissionAttachmentUpload); got != identity.PermissionOriginNone {
		t.Fatalf("超 scope 键的来源必须是 none，实际 %q：解释层与裁决层分叉", got)
	}
	if got := restricted.GrantSource(identity.PermissionTopicCreate); got != identity.PermissionOriginEarned {
		t.Fatalf("scope 内的已挣得键来源应为 earned，实际 %q", got)
	}
	// RestrictActor 按值传参，但 map 是引用：必须新建 map，不能就地删调用方的键。
	if !actor.Earned[identity.PermissionAttachmentUpload] {
		t.Fatal("RestrictActor 不得就地改写调用方的 Earned map")
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
