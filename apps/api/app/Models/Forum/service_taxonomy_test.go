package forum

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func categoryManagerActor() identity.Actor {
	return identity.Actor{
		ID:          21,
		Status:      identity.UserStatusActive,
		Permissions: map[string]bool{identity.PermissionCategoryManage: true},
	}
}

func TestTaxonomyServiceDeleteCategoryRequiresCategoryManage(t *testing.T) {
	store := newServiceFakeStore()
	store.listCategoriesResult = []Category{{ID: 7, Slug: "support", Name: "Support", Visibility: "public"}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})
	taxonomy := NewTaxonomyService(service)

	_, err := taxonomy.DeleteCategory(context.Background(), identity.Actor{ID: 3, Status: identity.UserStatusActive}, 7)
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("DeleteCategory error = %v, want permission denied", err)
	}
	if store.deletedCategoryID != 0 {
		t.Fatalf("denied delete must not reach the store, got %d", store.deletedCategoryID)
	}
}

// 默认发帖分类被删除会让 createTopic 失去兜底归属，必须先换默认分类。
func TestTaxonomyServiceDeleteCategoryRejectsDefaultCategory(t *testing.T) {
	store := newServiceFakeStore()
	store.listCategoriesResult = []Category{{ID: 7, Slug: "general", Name: "综合讨论", Visibility: "public"}}
	settings := testForumSettings()
	settings.DefaultCategorySlug = "general"
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: settings}})
	taxonomy := NewTaxonomyService(service)

	_, err := taxonomy.DeleteCategory(context.Background(), categoryManagerActor(), 7)
	if !errors.Is(err, ErrCategoryIsDefault) {
		t.Fatalf("DeleteCategory error = %v, want default category protection", err)
	}
	if store.deletedCategoryID != 0 {
		t.Fatalf("default category must not reach the store, got %d", store.deletedCategoryID)
	}
}

func TestTaxonomyServiceDeleteCategoryRejectsUnknownCategory(t *testing.T) {
	store := newServiceFakeStore()
	store.listCategoriesResult = []Category{{ID: 7, Slug: "support", Name: "Support", Visibility: "public"}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})
	taxonomy := NewTaxonomyService(service)

	_, err := taxonomy.DeleteCategory(context.Background(), categoryManagerActor(), 99)
	if !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("DeleteCategory error = %v, want not found", err)
	}
	if store.deletedCategoryID != 0 {
		t.Fatalf("unknown category must not reach the store, got %d", store.deletedCategoryID)
	}
}

func TestTaxonomyServiceDeleteCategoryDeletesAndEmitsEvent(t *testing.T) {
	store := newServiceFakeStore()
	store.listCategoriesResult = []Category{{ID: 7, Slug: "support", Name: "Support", Visibility: "hidden"}}
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})
	taxonomy := NewTaxonomyService(service)

	deleted, err := taxonomy.DeleteCategory(context.Background(), categoryManagerActor(), 7)
	if err != nil {
		t.Fatalf("DeleteCategory returned error: %v", err)
	}
	if deleted.ID != 7 || store.deletedCategoryID != 7 {
		t.Fatalf("expected category 7 deleted, got deleted=%#v store=%d", deleted, store.deletedCategoryID)
	}
	if !publisher.seen(appevents.CategoryDeleted) {
		t.Fatalf("expected %s event, got %#v", appevents.CategoryDeleted, publisher.names)
	}
}

func TestTaxonomyServiceDeleteCategoryGroupRequiresCategoryManage(t *testing.T) {
	store := newServiceFakeStore()
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})
	taxonomy := NewTaxonomyService(service)

	_, err := taxonomy.DeleteCategoryGroup(context.Background(), identity.Actor{ID: 3, Status: identity.UserStatusActive}, 4)
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("DeleteCategoryGroup error = %v, want permission denied", err)
	}
	if store.deletedGroupID != 0 {
		t.Fatalf("denied delete must not reach the store, got %d", store.deletedGroupID)
	}
}

func TestTaxonomyServiceDeleteCategoryGroupPropagatesNotEmpty(t *testing.T) {
	store := newServiceFakeStore()
	store.deleteGroupErr = ErrCategoryGroupNotEmpty
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})
	taxonomy := NewTaxonomyService(service)

	_, err := taxonomy.DeleteCategoryGroup(context.Background(), categoryManagerActor(), 4)
	if !errors.Is(err, ErrCategoryGroupNotEmpty) {
		t.Fatalf("DeleteCategoryGroup error = %v, want not empty", err)
	}
	if publisher.seen(appevents.CategoryDeleted) {
		t.Fatal("failed delete must not emit a delete event")
	}
}

func TestTaxonomyServiceDeleteCategoryGroupDeletesAndEmitsEvent(t *testing.T) {
	store := newServiceFakeStore()
	publisher := &fakeEventPublisher{}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}, Publisher: publisher})
	taxonomy := NewTaxonomyService(service)

	deleted, err := taxonomy.DeleteCategoryGroup(context.Background(), categoryManagerActor(), 4)
	if err != nil {
		t.Fatalf("DeleteCategoryGroup returned error: %v", err)
	}
	if deleted.ID != 4 || store.deletedGroupID != 4 {
		t.Fatalf("expected group 4 deleted, got deleted=%#v store=%d", deleted, store.deletedGroupID)
	}
	if !publisher.seen(appevents.CategoryDeleted) {
		t.Fatalf("expected %s event, got %#v", appevents.CategoryDeleted, publisher.names)
	}
}

// 后台列表口径必须显式请求隐藏项；公开口径保持过滤。
func TestServiceListTaxonomyForwardsIncludeHidden(t *testing.T) {
	store := newServiceFakeStore()
	store.listCategoriesResult = []Category{{ID: 7, Slug: "support", Name: "Support", Visibility: "hidden"}}
	service := NewService(ServiceConfig{Store: store, Settings: fakeSettingsResolver{settings: testForumSettings()}})

	if _, err := service.ListCategories(context.Background(), true); err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if !store.listCategoriesIncludeHidden {
		t.Fatal("expected includeHidden=true forwarded to the store")
	}
	if _, err := service.ListCategories(context.Background(), false); err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if store.listCategoriesIncludeHidden {
		t.Fatal("expected includeHidden=false forwarded to the store")
	}
}
