package attachments

import (
	"context"
	"testing"

	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// 已认领的扩展图标必须对所有访客可读，但绝不能因此让附件 API 返回存储直链。
func TestExtensionReferencePredicateSplit(t *testing.T) {
	image := ReferenceAccess{
		AttachmentReference: AttachmentReference{
			ResourceType: extensionreference.ResourceType, ResourceID: 0,
			Context: "example.oauth2:client:sfo_1",
		},
		ContentType: "image/png", Exists: true,
	}
	if !isAnonymousReadableReference(image) {
		t.Fatal("extension image reference must be anonymously readable")
	}
	if isPermanentURLReference(image) {
		t.Fatal("extension reference must never return the storage public URL")
	}

	// content_type 读时重查：非白名单 MIME 一律 fail-closed。
	svg := image
	svg.ContentType = "image/svg+xml"
	if isAnonymousReadableReference(svg) {
		t.Fatal("extension reference with non-image MIME must stay private")
	}

	site := ReferenceAccess{
		AttachmentReference: AttachmentReference{
			ResourceType: ResourceTypeSite, ResourceID: 0, Context: ContextLogo,
		},
		ContentType: "image/png", Exists: true,
	}
	if !isAnonymousReadableReference(site) || !isPermanentURLReference(site) {
		t.Fatal("site assets must stay anonymous-readable and permanent-URL")
	}
}

func TestAuthorizeExtensionReferenceForAnonymousReader(t *testing.T) {
	store := &accessFakeStore{
		item: Attachment{ID: 1, PublicID: "abc", Status: StatusActive, Visibility: VisibilityPublic},
		refs: []ReferenceAccess{{
			AttachmentReference: AttachmentReference{
				ResourceType: extensionreference.ResourceType, ResourceID: 0,
				Context: "example.oauth2:client:sfo_1",
			},
			ContentType: "image/png", Exists: true,
		}},
	}
	service := NewService(store, options.NewService(&fakeOptionStore{items: map[string]string{
		options.NameForumGuestRead: "login_required",
	}}))
	// 匿名读者：扩展公开图标必须可读，且不受论坛游客读策略影响。
	if err := service.authorizeAttachmentView(context.Background(), identity.Actor{}, store.item); err != nil {
		t.Fatalf("anonymous extension read denied: %v", err)
	}
	// 但 URL 装饰仍走代理路径，不暴露存储 PublicURL。
	if !service.shouldProxyAuthorizedURL(context.Background(), store.item) {
		t.Fatal("extension reference must keep the proxied media path")
	}

	store.refs[0].ContentType = "text/html"
	if err := service.authorizeAttachmentView(context.Background(), identity.Actor{}, store.item); err == nil {
		t.Fatal("non-image extension reference must stay private")
	}
}

func TestExtensionAttachmentPolicyWithoutOptionsService(t *testing.T) {
	var service *Service
	policy, err := service.ExtensionAttachmentPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.MaxBytes != extensionreference.MaxImageBytes || policy.ReferenceLimit != extensionreference.DefaultLimit {
		t.Fatalf("policy = %#v", policy)
	}
}
