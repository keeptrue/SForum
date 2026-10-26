package attachments

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

func TestAuthorizeAttachmentViewByReferencedResource(t *testing.T) {
	store := &accessFakeStore{
		item: Attachment{
			ID: 1, PublicID: "abc", Status: StatusActive, Visibility: VisibilityPublic,
			Owner: &OwnerSummary{ID: 10},
		},
	}
	optStore := &fakeOptionStore{items: map[string]string{
		options.NameForumGuestRead: "login_required",
	}}
	service := NewService(store, options.NewService(optStore))
	member := identity.Actor{ID: 20, Status: identity.UserStatusActive}
	author := identity.Actor{ID: 10, Status: identity.UserStatusActive}
	moderator := identity.Actor{ID: 30, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionModerationReview: true}}

	for _, test := range []struct {
		name    string
		ref     ReferenceAccess
		actor   identity.Actor
		wantErr error
	}{
		{name: "anonymous active login required", ref: forumAccess("topic", "active", "active", "public", 10), wantErr: ErrGuestLoginRequired},
		{name: "member active", ref: forumAccess("topic", "active", "active", "public", 10), actor: member},
		{name: "member locked topic", ref: forumAccess("topic", "locked", "locked", "public", 10), actor: member},
		{name: "member pending denied", ref: forumAccess("topic", "pending", "pending", "public", 10), actor: member, wantErr: identity.ErrPermissionDenied},
		{name: "author pending", ref: forumAccess("topic", "pending", "pending", "public", 10), actor: author},
		{name: "member hidden denied", ref: forumAccess("comment", "hidden", "active", "public", 10), actor: member, wantErr: identity.ErrPermissionDenied},
		{name: "author hidden denied", ref: forumAccess("comment", "hidden", "active", "public", 10), actor: author, wantErr: identity.ErrPermissionDenied},
		{name: "member deleted denied", ref: forumAccess("comment", "deleted", "active", "public", 10), actor: member, wantErr: identity.ErrPermissionDenied},
		{name: "member hidden category denied", ref: forumAccess("topic", "active", "active", "hidden", 10), actor: member, wantErr: identity.ErrPermissionDenied},
		{name: "moderator hidden", ref: forumAccess("comment", "hidden", "active", "public", 10), actor: moderator},
		{name: "moderator deleted", ref: forumAccess("topic", "deleted", "deleted", "public", 10), actor: moderator},
		{name: "moderator hidden category", ref: forumAccess("topic", "active", "active", "hidden", 10), actor: moderator},
	} {
		t.Run(test.name, func(t *testing.T) {
			store.refs = []ReferenceAccess{test.ref}
			err := service.authorizeAttachmentView(context.Background(), test.actor, store.item)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("got %v, want %v", err, test.wantErr)
			}
		})
	}

	store.refs = nil
	if err := service.authorizeAttachmentView(context.Background(), member, store.item); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("unreferenced member: %v", err)
	}
	if err := service.authorizeAttachmentView(context.Background(), author, store.item); err != nil {
		t.Fatalf("unreferenced owner: %v", err)
	}

	store.refs = []ReferenceAccess{{AttachmentReference: AttachmentReference{ResourceType: ResourceTypeUser, Context: ContextAvatar}, Exists: true}}
	if err := service.authorizeAttachmentView(context.Background(), identity.Actor{}, store.item); err != nil {
		t.Fatalf("avatar anon: %v", err)
	}
}

func TestAuthorizeAttachmentViewPublicMode(t *testing.T) {
	store := &accessFakeStore{
		item: Attachment{
			ID: 1, PublicID: "abc", Status: StatusActive, Visibility: VisibilityPublic,
		},
		refs: []ReferenceAccess{forumAccess("topic", "active", "active", "public", 1)},
	}
	optStore := &fakeOptionStore{items: map[string]string{
		options.NameForumGuestRead: "public",
	}}
	service := NewService(store, options.NewService(optStore))
	if err := service.authorizeAttachmentView(context.Background(), identity.Actor{}, store.item); err != nil {
		t.Fatalf("public mode: %v", err)
	}
}

func TestDecorateURLAlwaysProxiesAuthorizedResources(t *testing.T) {
	store := &accessFakeStore{
		item: Attachment{
			ID: 1, PublicID: "pub1", Status: StatusActive, Visibility: VisibilityPublic,
			ObjectKey: "k", Provider: "local", ContentType: "image/jpeg",
		},
		refs: []ReferenceAccess{forumAccess("post", "active", "active", "public", 1)},
	}
	optStore := &fakeOptionStore{items: map[string]string{
		options.NameForumGuestRead: "login_required",
	}}
	adapter := &fakeStorageAdapter{publicBaseURL: "https://cdn.example.com"}
	service := NewServiceWithAdapterFactory(store, options.NewService(optStore), func(storage.Config) (storage.Adapter, error) {
		return adapter, nil
	})
	// 需要 import storage — 用 decorate 直接测
	decorated := service.decorateURL(context.Background(), store.item)
	if decorated.URL != mediaAttachmentURLPath("pub1") {
		t.Fatalf("expected proxy URL, got %q", decorated.URL)
	}
	optStore.items[options.NameForumGuestRead] = "public"
	service.options.Invalidate()
	decorated = service.decorateURL(context.Background(), store.item)
	if decorated.URL != mediaAttachmentURLPath("pub1") {
		t.Fatalf("public forum mode must still use revocable proxy URL, got %q", decorated.URL)
	}

	store.item.ContentType = "application/pdf"
	decorated = service.decorateURL(context.Background(), store.item)
	if decorated.URL != mediaAttachmentURLPath("pub1") {
		t.Fatalf("non-image attachments must keep the original proxy URL, got %q", decorated.URL)
	}

	// 头像仍可用 CDN
	store.item.ContentType = "image/jpeg"
	store.refs = []ReferenceAccess{{AttachmentReference: AttachmentReference{ResourceType: ResourceTypeUser, Context: ContextAvatar}, Exists: true}}
	decorated = service.decorateURL(context.Background(), store.item)
	if decorated.URL != "https://cdn.example.com/k" {
		t.Fatalf("avatar should keep CDN URL, got %q", decorated.URL)
	}
}

type accessFakeStore struct {
	item Attachment
	refs []ReferenceAccess
}

func (s *accessFakeStore) Create(context.Context, CreateAttachmentInput) (Attachment, error) {
	return Attachment{}, ErrInvalidAttachment
}
func (s *accessFakeStore) GetByPublicID(_ context.Context, publicID string) (Attachment, error) {
	if s.item.PublicID == publicID {
		return s.item, nil
	}
	return Attachment{}, ErrAttachmentNotFound
}
func (s *accessFakeStore) GetByID(_ context.Context, id int64) (Attachment, error) {
	if s.item.ID == id {
		return s.item, nil
	}
	return Attachment{}, ErrAttachmentNotFound
}
func (s *accessFakeStore) List(context.Context, AttachmentListInput) (AttachmentList, error) {
	return AttachmentList{}, nil
}
func (s *accessFakeStore) ListReferences(context.Context, int64) ([]AttachmentReference, error) {
	items := make([]AttachmentReference, 0, len(s.refs))
	for _, ref := range s.refs {
		items = append(items, ref.AttachmentReference)
	}
	return items, nil
}
func (s *accessFakeStore) ListReferenceAccess(context.Context, int64) ([]ReferenceAccess, error) {
	return s.refs, nil
}
func (s *accessFakeStore) UpdateStatus(context.Context, int64, string, bool) (Attachment, error) {
	return Attachment{}, nil
}
func (s *accessFakeStore) ListCleanupCandidates(context.Context, time.Time, int) ([]Attachment, error) {
	return nil, nil
}
func (s *accessFakeStore) DeleteMetadata(context.Context, int64) error { return nil }

func forumAccess(resourceType, resourceStatus, topicStatus, categoryVisibility string, authorID int64) ReferenceAccess {
	return ReferenceAccess{
		AttachmentReference: AttachmentReference{ResourceType: resourceType, ResourceID: 1, Context: "inline"},
		AuthorUserID:        authorID, ResourceStatus: resourceStatus, TopicStatus: topicStatus,
		CategoryVisibility: categoryVisibility, Exists: true,
	}
}
