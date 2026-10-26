package systemactor

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type systemActorFakeStore struct {
	users            map[string]identity.CurrentUser
	roles            map[string]identity.Role
	assignments      map[int64][]string
	nextID           int64
	anyHumanUser     bool
	credentialCalled bool
	hasCredential    bool
}

func newSystemActorFakeStore() *systemActorFakeStore {
	return &systemActorFakeStore{
		users:       map[string]identity.CurrentUser{},
		roles:       map[string]identity.Role{"builtin_auto": {ID: 5, Key: "builtin_auto"}},
		assignments: map[int64][]string{},
		nextID:      100,
	}
}

func (s *systemActorFakeStore) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	for _, user := range s.users {
		if user.ID == userID {
			return identity.Actor{
				ID: userID, Status: user.Status,
				RoleKeys: append([]string(nil), s.assignments[userID]...),
			}, nil
		}
	}
	return identity.Actor{}, identity.ErrUserNotFound
}

func (s *systemActorFakeStore) AnyUserExists(context.Context) (bool, error) {
	return s.anyHumanUser || len(s.users) > 0, nil
}

func (s *systemActorFakeStore) GetCurrentUserByEmail(_ context.Context, email string) (identity.CurrentUser, error) {
	if user, ok := s.users[email]; ok {
		return user, nil
	}
	return identity.CurrentUser{}, identity.ErrUserNotFound
}

func (s *systemActorFakeStore) GetCredentialByLogin(context.Context, string) (identity.CredentialUser, error) {
	if s.hasCredential {
		return identity.CredentialUser{}, nil
	}
	return identity.CredentialUser{}, identity.ErrCredentialNotFound
}

func (s *systemActorFakeStore) WithBootstrapTx(
	ctx context.Context, fn func(context.Context, identity.TxStore) error,
) error {
	return fn(ctx, systemActorFakeTx{s})
}

type systemActorFakeTx struct{ inner *systemActorFakeStore }

func (t systemActorFakeTx) AnyUserExists(context.Context) (bool, error) {
	return t.inner.AnyUserExists(context.Background())
}

func (t systemActorFakeTx) FindRegistrationConflicts(context.Context, string, string) (identity.RegistrationConflicts, error) {
	return identity.RegistrationConflicts{}, nil
}

func (t systemActorFakeTx) CreateUser(_ context.Context, input identity.CreateUserInput) (identity.CurrentUser, error) {
	if _, exists := t.inner.users[input.Email]; exists {
		return identity.CurrentUser{}, errors.New("duplicate email")
	}
	t.inner.nextID++
	user := identity.CurrentUser{
		ID: t.inner.nextID, Username: input.Username,
		Status: identity.UserStatusActive, EmailVerified: input.EmailVerified,
	}
	t.inner.users[input.Email] = user
	return user, nil
}

func (t systemActorFakeTx) CreateCredential(context.Context, int64, string) error {
	t.inner.credentialCalled = true
	return nil
}

func (t systemActorFakeTx) GetDefaultRole(context.Context) (identity.Role, error) {
	return identity.Role{ID: 1, Key: identity.RoleMember}, nil
}

func (t systemActorFakeTx) GetRole(_ context.Context, roleKey string) (identity.Role, error) {
	role, ok := t.inner.roles[roleKey]
	if !ok {
		return identity.Role{}, errors.New("role not found")
	}
	return role, nil
}

func (t systemActorFakeTx) AssignRole(_ context.Context, userID int64, roleID int64) error {
	for key, role := range t.inner.roles {
		if role.ID != roleID {
			continue
		}
		for _, assigned := range t.inner.assignments[userID] {
			if assigned == key {
				return nil
			}
		}
		t.inner.assignments[userID] = append(t.inner.assignments[userID], key)
		return nil
	}
	return errors.New("role not found")
}

func (t systemActorFakeTx) LoadCurrentUserAccess(context.Context, *identity.CurrentUser) error {
	return nil
}

func TestEnsureSystemActorRequiresExistingUser(t *testing.T) {
	store := newSystemActorFakeStore()
	_, err := Ensure(context.Background(), store, Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: "builtin_auto",
		RequireExistingUser: true,
	})
	if !errors.Is(err, ErrNoUsers) {
		t.Fatalf("error = %v, want ErrNoUsers", err)
	}
	if len(store.users) != 0 {
		t.Fatalf("system user must not be created without a human user: %#v", store.users)
	}
}

func TestEnsureSystemActorCreatesCredentiallessUserAndRole(t *testing.T) {
	store := newSystemActorFakeStore()
	store.anyHumanUser = true
	actor, err := Ensure(context.Background(), store, Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: "builtin_auto",
		RequireExistingUser: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if actor.ID == 0 || len(actor.RoleKeys) != 1 || actor.RoleKeys[0] != "builtin_auto" {
		t.Fatalf("actor = %#v", actor)
	}
	if store.credentialCalled {
		t.Fatal("system actor must not receive a password credential")
	}
}

func TestEnsureSystemActorIsIdempotent(t *testing.T) {
	store := newSystemActorFakeStore()
	store.anyHumanUser = true
	spec := Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: "builtin_auto",
		RequireExistingUser: true,
	}
	first, err := Ensure(context.Background(), store, spec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(context.Background(), store, spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(store.users) != 1 || len(store.assignments[first.ID]) != 1 {
		t.Fatalf("first=%#v second=%#v users=%#v assignments=%#v",
			first, second, store.users, store.assignments)
	}
}

func TestEnsureSystemActorDevModeAllowsEmptyDatabase(t *testing.T) {
	store := newSystemActorFakeStore()
	actor, err := Ensure(context.Background(), store, Spec{
		Username: "dev-auto", Email: "dev-auto@sforum.invalid",
		DisplayName: "SForum dev auto", RoleKey: "builtin_auto",
		RequireExistingUser: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if actor.ID == 0 {
		t.Fatal("dev mode system actor was not created")
	}
}

func TestEnsureSystemActorRefusesMemberAccountAtReservedEmail(t *testing.T) {
	store := newSystemActorFakeStore()
	store.anyHumanUser = true
	store.users["builtin-auto@sforum.invalid"] = identity.CurrentUser{
		ID: 200, Username: "builtin-auto", RoleKeys: []string{identity.RoleMember},
	}
	_, err := Ensure(context.Background(), store, Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: "builtin_auto",
		RequireExistingUser: true,
	})
	if err == nil {
		t.Fatal("system actor must not adopt a member account")
	}
}

func TestEnsureSystemActorRefusesCredentialAccountAtReservedEmail(t *testing.T) {
	store := newSystemActorFakeStore()
	store.anyHumanUser = true
	store.hasCredential = true
	store.users["builtin-auto@sforum.invalid"] = identity.CurrentUser{
		ID: 200, Username: "builtin-auto",
	}
	_, err := Ensure(context.Background(), store, Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: "builtin_auto",
		RequireExistingUser: true,
	})
	if err == nil {
		t.Fatal("system actor must not adopt an account with a password credential")
	}
}

func TestEnsureSystemActorRefusesUsernameCollisionAtReservedEmail(t *testing.T) {
	store := newSystemActorFakeStore()
	store.anyHumanUser = true
	store.users["builtin-auto@sforum.invalid"] = identity.CurrentUser{
		ID: 200, Username: "attacker",
	}
	_, err := Ensure(context.Background(), store, Spec{
		Username: "builtin-auto", Email: "builtin-auto@sforum.invalid",
		DisplayName: "SForum builtin auto", RoleKey: "builtin_auto",
		RequireExistingUser: true,
	})
	if err == nil {
		t.Fatal("system actor must not adopt a different username")
	}
}
