package forum_test

import (
	"context"
	"errors"
	"testing"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

type referenceSelectorStore struct {
	forum.Store
	input forum.ReferenceSelectorInput
	items []forum.ReferenceOption
}

func (s *referenceSelectorStore) ListReferenceOptions(
	_ context.Context,
	input forum.ReferenceSelectorInput,
) ([]forum.ReferenceOption, error) {
	s.input = input
	return s.items, nil
}

func TestReferenceSelectorAllowsActiveOrdinaryAuthor(t *testing.T) {
	store := &referenceSelectorStore{items: []forum.ReferenceOption{
		{ID: 1, Label: "Alpha"}, {ID: 2, Label: "Beta"}, {ID: 3, Label: "Gamma"},
	}}
	selector := forum.NewReferenceSelector(store)
	result, err := selector.ListReferenceOptions(context.Background(), identity.Actor{
		ID: 9, Status: identity.UserStatusActive,
	}, forum.ReferenceSelectorInput{Kind: forum.ReferenceKindTopic, Query: "  test  ", Limit: 2})
	if err != nil {
		t.Fatalf("ListReferenceOptions: %v", err)
	}
	if len(result.Items) != 2 || !result.HasMore {
		t.Fatalf("result = %#v", result)
	}
	if store.input.Query != "test" || store.input.Limit != 2 {
		t.Fatalf("normalized input = %#v", store.input)
	}
}

func TestReferenceSelectorRejectsDeniedAndInvalidRequests(t *testing.T) {
	selector := forum.NewReferenceSelector(&referenceSelectorStore{})
	for _, actor := range []identity.Actor{
		{},
		{ID: 7, Status: identity.UserStatusDisabled},
	} {
		_, err := selector.ListReferenceOptions(context.Background(), actor, forum.ReferenceSelectorInput{Kind: forum.ReferenceKindUser})
		if !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("actor %#v error = %v", actor, err)
		}
	}
	_, err := selector.ListReferenceOptions(context.Background(), identity.Actor{
		ID: 7, Status: identity.UserStatusActive,
	}, forum.ReferenceSelectorInput{Kind: "private-resource"})
	if !errors.Is(err, forum.ErrInvalidReferenceSelector) {
		t.Fatalf("invalid kind error = %v", err)
	}
}

func TestReferenceSelectorBoundsLimitAndQuery(t *testing.T) {
	store := &referenceSelectorStore{}
	selector := forum.NewReferenceSelector(store)
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive}
	if _, err := selector.ListReferenceOptions(context.Background(), actor, forum.ReferenceSelectorInput{
		Kind: forum.ReferenceKindComment, Query: string(make([]rune, forum.ReferenceSelectorMaxQueryRunes+1)),
	}); !errors.Is(err, forum.ErrInvalidReferenceSelector) {
		t.Fatalf("oversized query error = %v", err)
	}
	if _, err := selector.ListReferenceOptions(context.Background(), actor, forum.ReferenceSelectorInput{
		Kind: forum.ReferenceKindCategory, Limit: 100,
	}); err != nil {
		t.Fatalf("bounded request: %v", err)
	}
	if store.input.Limit != forum.ReferenceSelectorMaxLimit {
		t.Fatalf("limit = %d", store.input.Limit)
	}
}
