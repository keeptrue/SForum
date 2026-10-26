package identity

import (
	"context"
	"errors"
	"reflect"
	"testing"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentityRegistryRoleSuggestionsRequireActiveRoleManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		actor Actor
	}{
		{
			name: "active without permission",
			actor: Actor{
				ID:     2,
				Status: UserStatusActive,
			},
		},
		{
			name: "inactive with permission",
			actor: Actor{
				ID:          3,
				Status:      UserStatusDisabled,
				Permissions: map[string]bool{PermissionRoleManage: true},
			},
		},
		{
			name: "inactive super admin",
			actor: Actor{
				ID:       4,
				Status:   UserStatusBanned,
				RoleKeys: []string{RoleSuperAdmin},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeIdentityRegistryStore{}
			service := (&Service{}).WithIdentityRegistryStore(store)

			if _, err := service.ListRoleSuggestions(context.Background(), test.actor, identityregistry.RoleSuggestionFilter{}); !errors.Is(err, ErrPermissionDenied) {
				t.Fatalf("ListRoleSuggestions error = %v, want ErrPermissionDenied", err)
			}
			if _, err := service.ListRoleSuggestionPage(context.Background(), test.actor, identityregistry.RoleSuggestionPageInput{}); !errors.Is(err, ErrPermissionDenied) {
				t.Fatalf("ListRoleSuggestionPage error = %v, want ErrPermissionDenied", err)
			}
			if _, err := service.DecideRoleSuggestion(context.Background(), test.actor, RoleSuggestionDecisionInput{}); !errors.Is(err, ErrPermissionDenied) {
				t.Fatalf("DecideRoleSuggestion error = %v, want ErrPermissionDenied", err)
			}
			if store.listCalls != 0 || store.pageCalls != 0 || store.decideCalls != 0 {
				t.Fatalf("denied actor reached registry store: list=%d page=%d decide=%d", store.listCalls, store.pageCalls, store.decideCalls)
			}
		})
	}
}

func TestIdentityRegistryRoleSuggestionsFailClosedWhenUnconfigured(t *testing.T) {
	t.Parallel()

	actor := roleManagerActor(7)
	service := &Service{}

	if _, err := service.ListRoleSuggestions(context.Background(), actor, identityregistry.RoleSuggestionFilter{}); !errors.Is(err, ErrIdentityRegistryUnavailable) {
		t.Fatalf("ListRoleSuggestions error = %v, want ErrIdentityRegistryUnavailable", err)
	}
	if _, err := service.ListRoleSuggestionPage(context.Background(), actor, identityregistry.RoleSuggestionPageInput{}); !errors.Is(err, ErrIdentityRegistryUnavailable) {
		t.Fatalf("ListRoleSuggestionPage error = %v, want ErrIdentityRegistryUnavailable", err)
	}
	if _, err := service.DecideRoleSuggestion(context.Background(), actor, RoleSuggestionDecisionInput{}); !errors.Is(err, ErrIdentityRegistryUnavailable) {
		t.Fatalf("DecideRoleSuggestion error = %v, want ErrIdentityRegistryUnavailable", err)
	}

	var nilService *Service
	if _, err := nilService.ListRoleSuggestions(context.Background(), actor, identityregistry.RoleSuggestionFilter{}); !errors.Is(err, ErrIdentityRegistryUnavailable) {
		t.Fatalf("nil Service ListRoleSuggestions error = %v, want ErrIdentityRegistryUnavailable", err)
	}
	if _, err := nilService.ListRoleSuggestionPage(context.Background(), actor, identityregistry.RoleSuggestionPageInput{}); !errors.Is(err, ErrIdentityRegistryUnavailable) {
		t.Fatalf("nil Service ListRoleSuggestionPage error = %v, want ErrIdentityRegistryUnavailable", err)
	}
	if _, err := nilService.DecideRoleSuggestion(context.Background(), actor, RoleSuggestionDecisionInput{}); !errors.Is(err, ErrIdentityRegistryUnavailable) {
		t.Fatalf("nil Service DecideRoleSuggestion error = %v, want ErrIdentityRegistryUnavailable", err)
	}
}

func TestListRoleSuggestionsPassesFilterAndRepositoryErrorThrough(t *testing.T) {
	t.Parallel()

	wantFilter := identityregistry.RoleSuggestionFilter{
		ApprovalState:    " APPROVED ",
		RoleKey:          " Operators ",
		PermissionKey:    " Plugin.Example.Read ",
		OwnerExtensionID: " Example.Plugin ",
		Limit:            999,
	}
	wantResult := []identityregistry.RoleSuggestion{{
		ID:            31,
		ApprovalState: identityregistry.RoleSuggestionApproved,
		Applied:       true,
		Revision:      2,
	}}
	store := &fakeIdentityRegistryStore{listResult: wantResult}
	service := (&Service{}).WithIdentityRegistryStore(store)

	got, err := service.ListRoleSuggestions(context.Background(), roleManagerActor(8), wantFilter)
	if err != nil {
		t.Fatalf("ListRoleSuggestions returned error: %v", err)
	}
	if !reflect.DeepEqual(got, wantResult) {
		t.Fatalf("result = %#v, want %#v", got, wantResult)
	}
	if !reflect.DeepEqual(store.listFilter, wantFilter) {
		t.Fatalf("filter = %#v, want pass-through %#v", store.listFilter, wantFilter)
	}

	wantErr := errors.New("list failed")
	store.listErr = wantErr
	if _, err := service.ListRoleSuggestions(context.Background(), roleManagerActor(8), wantFilter); !errors.Is(err, wantErr) {
		t.Fatalf("ListRoleSuggestions error = %v, want repository error", err)
	}
}

func TestListRoleSuggestionPagePassesOpaqueCursorThrough(t *testing.T) {
	t.Parallel()

	wantInput := identityregistry.RoleSuggestionPageInput{
		Filter: identityregistry.RoleSuggestionFilter{
			ApprovalState: identityregistry.RoleSuggestionPending,
			Limit:         25,
		},
		Cursor: "opaque-cursor",
	}
	wantPage := identityregistry.RoleSuggestionPage{
		Items:      []identityregistry.RoleSuggestion{{ID: 77}},
		NextCursor: "next-cursor",
	}
	store := &fakeIdentityRegistryStore{pageResult: wantPage}
	service := (&Service{}).WithIdentityRegistryStore(store)

	got, err := service.ListRoleSuggestionPage(context.Background(), roleManagerActor(8), wantInput)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantPage) || !reflect.DeepEqual(store.pageInput, wantInput) {
		t.Fatalf("page=%#v input=%#v, want page=%#v input=%#v", got, store.pageInput, wantPage, wantInput)
	}
}

func TestDecideRoleSuggestionBindsActorAndPreservesCAS(t *testing.T) {
	t.Parallel()

	store := &fakeIdentityRegistryStore{
		decideResult: identityregistry.RoleSuggestion{
			ID:            41,
			ApprovalState: identityregistry.RoleSuggestionApproved,
			Applied:       true,
			Revision:      6,
		},
	}
	service := (&Service{}).WithIdentityRegistryStore(store)
	input := RoleSuggestionDecisionInput{
		ID:               41,
		ExpectedRevision: 5,
		ApprovalState:    identityregistry.RoleSuggestionApproved,
	}

	got, err := service.DecideRoleSuggestion(context.Background(), roleManagerActor(93), input)
	if err != nil {
		t.Fatalf("DecideRoleSuggestion returned error: %v", err)
	}
	if got != store.decideResult {
		t.Fatalf("result = %#v, want %#v", got, store.decideResult)
	}
	want := identityregistry.DecideRoleSuggestionInput{
		ID:               input.ID,
		ExpectedRevision: input.ExpectedRevision,
		ApprovalState:    input.ApprovalState,
		ActorUserID:      93,
	}
	if store.decideInput != want {
		t.Fatalf("repository input = %#v, want actor-bound %#v", store.decideInput, want)
	}
}

func TestDecideRoleSuggestionPreservesRepositorySentinel(t *testing.T) {
	t.Parallel()

	store := &fakeIdentityRegistryStore{decideErr: identityregistry.ErrRevisionConflict}
	service := (&Service{}).WithIdentityRegistryStore(store)

	_, err := service.DecideRoleSuggestion(context.Background(), roleManagerActor(12), RoleSuggestionDecisionInput{
		ID:               2,
		ExpectedRevision: 4,
		ApprovalState:    identityregistry.RoleSuggestionRejected,
	})
	if !errors.Is(err, identityregistry.ErrRevisionConflict) {
		t.Fatalf("DecideRoleSuggestion error = %v, want ErrRevisionConflict", err)
	}
}

func TestDecideRoleSuggestionMapsRepositoryAuthorizationFailure(t *testing.T) {
	t.Parallel()

	service := (&Service{}).WithIdentityRegistryStore(&fakeIdentityRegistryStore{
		decideErr: identityregistry.ErrUnauthorized,
	})
	if _, err := service.DecideRoleSuggestion(context.Background(), roleManagerActor(1), RoleSuggestionDecisionInput{
		ID:               51,
		ExpectedRevision: 1,
		ApprovalState:    identityregistry.RoleSuggestionApproved,
	}); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("DecideRoleSuggestion error=%v, want ErrPermissionDenied", err)
	}

	typeOfInput := reflect.TypeOf(RoleSuggestionDecisionInput{})
	for _, forbidden := range []string{"ActorUserID", "RoleKey", "PermissionKey", "Permissions"} {
		if _, found := typeOfInput.FieldByName(forbidden); found {
			t.Fatalf("decision input exposes forbidden authority field %q", forbidden)
		}
	}
}

func roleManagerActor(id int64) Actor {
	return Actor{
		ID:          id,
		Status:      UserStatusActive,
		Permissions: map[string]bool{PermissionRoleManage: true},
	}
}

type fakeIdentityRegistryStore struct {
	listCalls    int
	listFilter   identityregistry.RoleSuggestionFilter
	listResult   []identityregistry.RoleSuggestion
	listErr      error
	pageCalls    int
	pageInput    identityregistry.RoleSuggestionPageInput
	pageResult   identityregistry.RoleSuggestionPage
	pageErr      error
	decideCalls  int
	decideInput  identityregistry.DecideRoleSuggestionInput
	decideResult identityregistry.RoleSuggestion
	decideErr    error
}

func (s *fakeIdentityRegistryStore) LoadDurableState(context.Context) (identityregistry.DurableState, error) {
	return identityregistry.DurableState{}, nil
}

func (s *fakeIdentityRegistryStore) ListRoleSuggestions(_ context.Context, filter identityregistry.RoleSuggestionFilter) ([]identityregistry.RoleSuggestion, error) {
	s.listCalls++
	s.listFilter = filter
	return s.listResult, s.listErr
}

func (s *fakeIdentityRegistryStore) ListRoleSuggestionPage(_ context.Context, input identityregistry.RoleSuggestionPageInput) (identityregistry.RoleSuggestionPage, error) {
	s.pageCalls++
	s.pageInput = input
	return s.pageResult, s.pageErr
}

func (s *fakeIdentityRegistryStore) DecideRoleSuggestion(_ context.Context, input identityregistry.DecideRoleSuggestionInput) (identityregistry.RoleSuggestion, error) {
	s.decideCalls++
	s.decideInput = input
	return s.decideResult, s.decideErr
}
