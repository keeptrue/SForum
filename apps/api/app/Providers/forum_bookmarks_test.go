package providers

import (
	"context"
	"errors"
	"testing"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

// bookmarkProviderGate 记录被查询的开关名，用来锁定 features.bookmarks 的契约。
// 实现 forum.FeatureGate（与生产 options.Service 同一接口）。
type bookmarkProviderGate struct {
	enabled bool
	names   []string
}

func (g *bookmarkProviderGate) IsFeatureEnabled(_ context.Context, name string) (bool, error) {
	g.names = append(g.names, name)
	return g.enabled, nil
}

// bookmarkProviderStore 只覆盖可见性解析需要的方法；Store 其余方法保持 nil。
// 装配链测试只走到 GetTopic 为止——构造函数是惰性的，不会碰其它方法。
type bookmarkProviderStore struct {
	forum.Store
	topic        forum.TopicDetail
	topicOK      bool
	bookmarkList forum.BookmarkList
}

func (s *bookmarkProviderStore) GetTopic(context.Context, int64) (forum.TopicDetail, error) {
	if !s.topicOK {
		return forum.TopicDetail{}, forum.ErrTopicNotFound
	}
	return s.topic, nil
}

// BookmarkStore 三方法：让嵌入 store 同时满足 BookmarkStore 断言，装配链才能
// 把它挂到 BookmarkService 上。写路径在本测试里根本不该走到 store（gate 拦截
// /合法放行都会因为目标不存在而提前返回），实现只做哨兵。
func (s *bookmarkProviderStore) ApplyBookmark(context.Context, forum.BookmarkApplyInput) (forum.BookmarkState, error) {
	return forum.BookmarkState{}, forum.ErrInvalidBookmarkTarget
}

func (s *bookmarkProviderStore) BookmarkedTargetIDs(context.Context, int64, string, []int64) (map[int64]struct{}, error) {
	return map[int64]struct{}{}, nil
}

func (s *bookmarkProviderStore) ListUserBookmarks(context.Context, int64, bool, int, int) (forum.BookmarkList, error) {
	return s.bookmarkList, nil
}

// TestForumProviderBookmarkServiceHonoursFeatureGate 固定「gate 在装配链上」：
// 经 provider 装配（attachForumEngagement → controller.WithBookmarks）取到的
// BookmarkService——而不是测试里现 new 的实例——在 features.bookmarks 关闭时
// 必须拒绝写操作。这样以后改装配把 gate 接丢了，这条用例会变红。
//
// optionsService 形状是 *options.Service，但 gate 走的是 FeatureGate 接口；
// 这里用最小化的装配（controller + service + attach 返回值）验证同一链路，
// 不需要真的构造一个 options.Service。
func TestForumProviderBookmarkServiceHonoursFeatureGate(t *testing.T) {
	gate := &bookmarkProviderGate{enabled: false}
	store := &bookmarkProviderStore{topic: forum.TopicDetail{TopicSummary: forum.TopicSummary{ID: 7}}, topicOK: true}
	provider := NewForumProvider(store, nil, nil)

	bookmarks := attachForumBookmarks(provider.controller, provider.service, nil)
	// gate 沿 FeatureGate 接口注入（与 controller.WithBookmarks 无关，
	// attachForumBookmarks 的返回值就是链上那个实例）。
	bookmarks.WithFeatureGate(gate)
	got := provider.BookmarkService()
	if got == nil {
		t.Fatal("provider did not wire the bookmark service")
	}
	if got != bookmarks {
		t.Fatal("provider.BookmarkService() must return the same instance the chain attached")
	}

	_, err := got.BookmarkTopic(context.Background(), identity.Actor{ID: 9, Status: identity.UserStatusActive}, 7, forum.BookmarkWriteInput{})
	if !errors.Is(err, forum.ErrBookmarksDisabled) {
		t.Fatalf("want ErrBookmarksDisabled, got %v", err)
	}
	if len(gate.names) == 0 || gate.names[0] != options.NameFeatureBookmarks {
		t.Fatalf("feature gate names = %#v, want [%q]", gate.names, options.NameFeatureBookmarks)
	}
}

// TestForumBookmarkListerClearsReminderFieldsForOthers 固定适配器的第二道闸：
// store 条目带着提醒时间，非行主视角经适配器后两列必须被清空；行主视角保留。
// 这里用真实 BookmarkService（fake store 可控返回），保证测的是生产适配器。
func TestForumBookmarkListerClearsReminderFieldsForOthers(t *testing.T) {
	remindAt := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	remindedAt := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	store := &bookmarkProviderStore{}
	store.bookmarkList = forum.BookmarkList{Items: []forum.BookmarkEntry{
		{TargetType: forum.BookmarkTargetTopic, TargetID: 1, Visibility: forum.BookmarkVisibilityPublic,
			RemindAt: &remindAt, RemindedAt: &remindedAt},
	}, Total: 1}
	bookmarks := forum.NewBookmarkService(forum.NewService(forum.ServiceConfig{Store: store})).WithStore(store)
	lister := NewForumBookmarkLister(bookmarks)

	other, err := lister.ListForViewer(context.Background(), 8, 7, 20, 0)
	if err != nil {
		t.Fatalf("other view: %v", err)
	}
	if len(other.Items) != 1 || other.Items[0].RemindAt != nil || other.Items[0].RemindedAt != nil {
		t.Fatalf("adapter leaked private schedule data to another viewer: %#v", other.Items)
	}

	owner, err := lister.ListForViewer(context.Background(), 7, 7, 20, 0)
	if err != nil {
		t.Fatalf("owner view: %v", err)
	}
	if owner.Items[0].RemindAt == nil || owner.Items[0].RemindedAt == nil {
		t.Fatalf("owner view must keep the reminder: %#v", owner.Items)
	}
}

// TestForumProviderBookmarkServiceEnabledByDefault 固定另一侧：经装配链取到的
// service 在没有注入 gate（optionsService 为 nil）时默认放行（与 reactions 同姿态）。
func TestForumProviderBookmarkServiceEnabledByDefault(t *testing.T) {
	store := &bookmarkProviderStore{topic: forum.TopicDetail{TopicSummary: forum.TopicSummary{ID: 7}}, topicOK: true}
	provider := NewForumProvider(store, nil, nil)
	attachForumEngagement(provider.controller, provider.service, nil)

	got := provider.BookmarkService()
	if got == nil {
		t.Fatal("provider did not wire the bookmark service")
	}
	// 放行的证据：请求穿过 service 进入 store（哨兵返回目标非法），而不是被
	// gate 以 ErrBookmarksDisabled 拦截。
	_, err := got.BookmarkTopic(context.Background(), identity.Actor{ID: 9, Status: identity.UserStatusActive}, 7, forum.BookmarkWriteInput{})
	if errors.Is(err, forum.ErrBookmarksDisabled) {
		t.Fatalf("no gate injected must default to enabled, got %v", err)
	}
	if err == nil {
		t.Fatal("sentinel store must fail the write so this test can prove it was reached")
	}
}

// TestNewForumProviderWiresBookmarkServiceWithoutManualAttach 锁住构造函数里的
// 装配调用（forum.go 的 attachForumEngagement）：只调 NewForumProvider，不手动
// attach，断言收藏协作者确实被接上、且就是 controller 内部那个实例。
//
// 这条防的是「装配链断裂没人拦」：把构造函数里的 attachForumEngagement 删掉，
// BookmarkService() 会变 nil，本用例立刻红；把 BookmarkService() 改成现场 new
// 一个新实例，实例同一性断言会红（那样会丢掉 gate 装配）。
func TestNewForumProviderWiresBookmarkServiceWithoutManualAttach(t *testing.T) {
	store := &bookmarkProviderStore{topic: forum.TopicDetail{TopicSummary: forum.TopicSummary{ID: 7}}, topicOK: true}
	provider := NewForumProvider(store, nil, nil)

	bookmarks := provider.BookmarkService()
	if bookmarks == nil {
		t.Fatal("NewForumProvider must attach the bookmark service (attachForumEngagement missing?)")
	}
	if internal := provider.controller.BookmarkService(); internal != bookmarks {
		t.Fatal("provider.BookmarkService() must be the same instance the controller holds, not a fresh one")
	}

	// 同一个实例上注入关闭的 gate：写操作必须被拒绝，证明这条链上的实例就是
	// 真正生效的那个（而不是一个没人用的副本）。
	gate := &bookmarkProviderGate{enabled: false}
	bookmarks.WithFeatureGate(gate)
	_, err := bookmarks.BookmarkTopic(context.Background(), identity.Actor{ID: 9, Status: identity.UserStatusActive}, 7, forum.BookmarkWriteInput{})
	if !errors.Is(err, forum.ErrBookmarksDisabled) {
		t.Fatalf("want ErrBookmarksDisabled on the wired instance, got %v", err)
	}
	if len(gate.names) == 0 || gate.names[0] != options.NameFeatureBookmarks {
		t.Fatalf("feature gate names = %#v, want [%q]", gate.names, options.NameFeatureBookmarks)
	}
}
