package sitechrome

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestServiceListPublicExtensionNavItems(t *testing.T) {
	service := NewService(newFakeStore())
	if items := service.ListPublicExtensionNavItems(context.Background()); items != nil {
		t.Fatalf("nil provider should return nil, got %#v", items)
	}
	service.WithExtensionNavItems(fakeExtensionNavProvider{
		items: []ExtensionNavItem{{
			ExtensionID: "demo.plugin",
			ID:          "docs",
			Order:       10,
			Label:       map[string]string{"zh-CN": "文档"},
			Kind:        "hostLink",
			URL:         "/docs",
		}},
	})
	items := service.ListPublicExtensionNavItems(context.Background())
	if len(items) != 1 || items[0].ID != "docs" {
		t.Fatalf("unexpected extension nav items: %#v", items)
	}
	// 解析失败应降级为空，不 panic。
	service.WithExtensionNavItems(fakeExtensionNavProvider{err: errors.New("boom")})
	if items := service.ListPublicExtensionNavItems(context.Background()); items != nil {
		t.Fatalf("error should degrade to nil, got %#v", items)
	}
}

type fakeExtensionNavProvider struct {
	items []ExtensionNavItem
	err   error
}

func (p fakeExtensionNavProvider) ExtensionNavItems(context.Context) ([]ExtensionNavItem, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.items, nil
}

func TestServiceNavCRUDAndPermissions(t *testing.T) {
	store := newFakeStore()
	service := NewService(store)
	admin := manageActor()
	guest := identity.Actor{ID: 2, Status: identity.UserStatusActive}

	if _, err := service.ListAdminNavItems(context.Background(), guest); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("guest admin list should deny: %v", err)
	}

	created, err := service.CreateNavItem(context.Background(), admin, CreateNavItemInput{
		LabelZhCN: "  文档  ",
		LabelEnUS: "Docs",
		Href:      "/docs",
		Position:  5,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("CreateNavItem: %v", err)
	}
	if created.LabelZhCN != "文档" || created.Href != "/docs" {
		t.Fatalf("unexpected created nav: %+v", created)
	}

	// 公开列表只含 enabled。
	public, err := service.ListPublicNavItems(context.Background())
	if err != nil || len(public) != 1 {
		t.Fatalf("public nav list = %#v err=%v", public, err)
	}

	disabled := false
	updated, err := service.UpdateNavItem(context.Background(), admin, UpdateNavItemInput{
		ID:      created.ID,
		Enabled: &disabled,
	})
	if err != nil || updated.Enabled {
		t.Fatalf("UpdateNavItem disable failed: %+v err=%v", updated, err)
	}
	public, err = service.ListPublicNavItems(context.Background())
	if err != nil || len(public) != 0 {
		t.Fatalf("disabled nav should hide: %#v err=%v", public, err)
	}

	if err := service.DeleteNavItem(context.Background(), admin, created.ID); err != nil {
		t.Fatalf("DeleteNavItem: %v", err)
	}
	if err := service.DeleteNavItem(context.Background(), admin, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete want not found, got %v", err)
	}
}

func TestServiceRejectsInvalidNavAndFriendLink(t *testing.T) {
	service := NewService(newFakeStore())
	admin := manageActor()

	if _, err := service.CreateNavItem(context.Background(), admin, CreateNavItemInput{
		LabelZhCN: "x", LabelEnUS: "x", Href: "javascript:alert(1)",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad href should reject: %v", err)
	}
	if _, err := service.CreateFriendLink(context.Background(), admin, CreateFriendLinkInput{
		Name: "A", URL: "//evil.example",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("protocol-relative url should reject: %v", err)
	}
}

func TestServiceAnnouncementActiveWindow(t *testing.T) {
	store := newFakeStore()
	service := NewService(store)
	admin := manageActor()

	past := time.Now().UTC().Add(-2 * time.Hour)
	future := time.Now().UTC().Add(2 * time.Hour)
	expiredEnd := time.Now().UTC().Add(-time.Hour)

	active, err := service.CreateAnnouncement(context.Background(), admin, CreateAnnouncementInput{
		TitleZhCN: "进行中", BodyZhCN: "可见", Style: StyleInfo, Enabled: true,
		StartsAt: &past, EndsAt: &future,
	})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	_, err = service.CreateAnnouncement(context.Background(), admin, CreateAnnouncementInput{
		TitleZhCN: "已过期", BodyZhCN: "隐藏", Style: StyleWarning, Enabled: true,
		EndsAt: &expiredEnd,
	})
	if err != nil {
		t.Fatalf("create expired: %v", err)
	}
	_, err = service.CreateAnnouncement(context.Background(), admin, CreateAnnouncementInput{
		TitleZhCN: "未启用", BodyZhCN: "隐藏", Style: StyleDanger, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create disabled: %v", err)
	}

	public, err := service.ListPublicAnnouncements(context.Background())
	if err != nil {
		t.Fatalf("ListPublicAnnouncements: %v", err)
	}
	if len(public) != 1 || public[0].ID != active.ID {
		t.Fatalf("public announcements = %#v", public)
	}

	adminList, err := service.ListAdminAnnouncements(context.Background(), admin)
	if err != nil || len(adminList) != 3 {
		t.Fatalf("admin list want 3, got %#v err=%v", adminList, err)
	}
}

func manageActor() identity.Actor {
	return identity.Actor{
		ID:     1,
		Status: identity.UserStatusActive,
		Permissions: map[string]bool{
			identity.PermissionSettingsSiteManage: true,
		},
	}
}

type fakeStore struct {
	navs          map[int64]NavItem
	links         map[int64]FriendLink
	announcements map[int64]Announcement
	nextID        int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		navs:          map[int64]NavItem{},
		links:         map[int64]FriendLink{},
		announcements: map[int64]Announcement{},
		nextID:        1,
	}
}

func (s *fakeStore) ListNavItems(_ context.Context, enabledOnly bool) ([]NavItem, error) {
	out := make([]NavItem, 0, len(s.navs))
	for _, item := range s.navs {
		if enabledOnly && !item.Enabled {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *fakeStore) CreateNavItem(_ context.Context, input CreateNavItemInput) (NavItem, error) {
	id := s.nextID
	s.nextID++
	now := time.Now().UTC()
	item := NavItem{
		ID: id, LabelZhCN: input.LabelZhCN, LabelEnUS: input.LabelEnUS, Href: input.Href,
		OpenInNewTab: input.OpenInNewTab, Position: input.Position, Enabled: input.Enabled,
		CreatedAt: now, UpdatedAt: now,
	}
	s.navs[id] = item
	return item, nil
}

func (s *fakeStore) UpdateNavItem(_ context.Context, input UpdateNavItemInput) (NavItem, error) {
	item, ok := s.navs[input.ID]
	if !ok {
		return NavItem{}, ErrNotFound
	}
	if input.LabelZhCN != nil {
		item.LabelZhCN = *input.LabelZhCN
	}
	if input.LabelEnUS != nil {
		item.LabelEnUS = *input.LabelEnUS
	}
	if input.Href != nil {
		item.Href = *input.Href
	}
	if input.OpenInNewTab != nil {
		item.OpenInNewTab = *input.OpenInNewTab
	}
	if input.Position != nil {
		item.Position = *input.Position
	}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	item.UpdatedAt = time.Now().UTC()
	s.navs[input.ID] = item
	return item, nil
}

func (s *fakeStore) DeleteNavItem(_ context.Context, id int64) error {
	if _, ok := s.navs[id]; !ok {
		return ErrNotFound
	}
	delete(s.navs, id)
	return nil
}

func (s *fakeStore) ListFriendLinks(_ context.Context, enabledOnly bool) ([]FriendLink, error) {
	out := make([]FriendLink, 0, len(s.links))
	for _, item := range s.links {
		if enabledOnly && !item.Enabled {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *fakeStore) CreateFriendLink(_ context.Context, input CreateFriendLinkInput) (FriendLink, error) {
	id := s.nextID
	s.nextID++
	now := time.Now().UTC()
	item := FriendLink{
		ID: id, Name: input.Name, URL: input.URL, Description: input.Description, LogoURL: input.LogoURL,
		Position: input.Position, Enabled: input.Enabled, CreatedAt: now, UpdatedAt: now,
	}
	s.links[id] = item
	return item, nil
}

func (s *fakeStore) UpdateFriendLink(_ context.Context, input UpdateFriendLinkInput) (FriendLink, error) {
	item, ok := s.links[input.ID]
	if !ok {
		return FriendLink{}, ErrNotFound
	}
	if input.Name != nil {
		item.Name = *input.Name
	}
	if input.URL != nil {
		item.URL = *input.URL
	}
	if input.Description != nil {
		item.Description = *input.Description
	}
	if input.LogoURL != nil {
		item.LogoURL = *input.LogoURL
	}
	if input.Position != nil {
		item.Position = *input.Position
	}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	item.UpdatedAt = time.Now().UTC()
	s.links[input.ID] = item
	return item, nil
}

func (s *fakeStore) DeleteFriendLink(_ context.Context, id int64) error {
	if _, ok := s.links[id]; !ok {
		return ErrNotFound
	}
	delete(s.links, id)
	return nil
}

func (s *fakeStore) ListAnnouncements(_ context.Context, enabledOnly bool, activeNow bool) ([]Announcement, error) {
	now := time.Now().UTC()
	out := make([]Announcement, 0, len(s.announcements))
	for _, item := range s.announcements {
		if enabledOnly && !item.Enabled {
			continue
		}
		if activeNow {
			if item.StartsAt != nil && item.StartsAt.After(now) {
				continue
			}
			if item.EndsAt != nil && item.EndsAt.Before(now) {
				continue
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (s *fakeStore) CreateAnnouncement(_ context.Context, input CreateAnnouncementInput) (Announcement, error) {
	id := s.nextID
	s.nextID++
	now := time.Now().UTC()
	item := Announcement{
		ID: id, TitleZhCN: input.TitleZhCN, TitleEnUS: input.TitleEnUS,
		BodyZhCN: input.BodyZhCN, BodyEnUS: input.BodyEnUS, Style: input.Style, Href: input.Href,
		Dismissible: input.Dismissible, Position: input.Position, Enabled: input.Enabled,
		StartsAt: input.StartsAt, EndsAt: input.EndsAt, CreatedAt: now, UpdatedAt: now,
	}
	s.announcements[id] = item
	return item, nil
}

func (s *fakeStore) UpdateAnnouncement(_ context.Context, input UpdateAnnouncementInput) (Announcement, error) {
	item, ok := s.announcements[input.ID]
	if !ok {
		return Announcement{}, ErrNotFound
	}
	// 测试 fake 仅覆盖常用字段；完整路径由 postgres 承担。
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	item.UpdatedAt = time.Now().UTC()
	s.announcements[input.ID] = item
	return item, nil
}

func (s *fakeStore) DeleteAnnouncement(_ context.Context, id int64) error {
	if _, ok := s.announcements[id]; !ok {
		return ErrNotFound
	}
	delete(s.announcements, id)
	return nil
}
