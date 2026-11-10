package profile

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// profileBookmarkListerFunc 让测试直接观察 Profile 传给收藏域的口径参数。
type profileBookmarkListerFunc func(ctx context.Context, viewerID, ownerUserID int64, limit, offset int) (BookmarkPage, error)

func (f profileBookmarkListerFunc) ListForViewer(ctx context.Context, viewerID, ownerUserID int64, limit, offset int) (BookmarkPage, error) {
	return f(ctx, viewerID, ownerUserID, limit, offset)
}

func bookmarkTestActor(id int64) identity.Actor {
	return identity.Actor{ID: id, Status: identity.UserStatusActive}
}

// TestListProfileBookmarksPassesViewerAndOwnerSeparately 固定最小接口的语义：
// Profile 把 viewerID 与 ownerUserID 分开下传，「本人看全部、他人只看公开」的
// 判定由 Forum 执行，Profile 自己不判断可见性。
func TestListProfileBookmarksPassesViewerAndOwnerSeparately(t *testing.T) {
	store := &fakeStore{user: UserProfileSummary{UserID: 42, Username: "alice"}}
	var gotViewer, gotOwner int64
	service := NewService(store).WithBookmarkLister(profileBookmarkListerFunc(func(_ context.Context, viewerID, ownerUserID int64, limit, offset int) (BookmarkPage, error) {
		gotViewer, gotOwner = viewerID, ownerUserID
		return BookmarkPage{Items: []BookmarkItem{{TargetType: "topic", TargetID: 7, Visibility: "public"}}, Total: 1}, nil
	}))

	view, err := service.ListProfileBookmarks(context.Background(), "alice", bookmarkTestActor(42), 1, 20)
	if err != nil {
		t.Fatalf("ListProfileBookmarks: %v", err)
	}
	if gotViewer != 42 || gotOwner != 42 {
		t.Fatalf("owner view must pass viewer=%d owner=%d", gotViewer, gotOwner)
	}
	if !view.OwnerView {
		t.Fatal("own profile request must be marked OwnerView")
	}
	if len(view.Page.Items) != 1 || view.Page.Items[0].TargetID != 7 {
		t.Fatalf("unexpected page %#v", view.Page)
	}

	other, err := service.ListProfileBookmarks(context.Background(), "alice", bookmarkTestActor(9), 1, 20)
	if err != nil {
		t.Fatalf("ListProfileBookmarks(other): %v", err)
	}
	if gotViewer != 9 || gotOwner != 42 {
		t.Fatalf("other view must pass viewer=%d owner=%d", gotViewer, gotOwner)
	}
	if other.OwnerView {
		t.Fatal("other viewer must not be marked OwnerView")
	}

	anonymous, err := service.ListProfileBookmarks(context.Background(), "alice", identity.Actor{}, 1, 20)
	if err != nil {
		t.Fatalf("ListProfileBookmarks(anonymous): %v", err)
	}
	if gotViewer != 0 || anonymous.OwnerView {
		t.Fatalf("anonymous view must pass viewer=0 and OwnerView=false, got viewer=%d", gotViewer)
	}
}

// TestListProfileBookmarksNormalizesPagination 固定分页边界（默认 20、上限 50、
// 最多 200 页），与资料活动分页同一套。
func TestListProfileBookmarksNormalizesPagination(t *testing.T) {
	store := &fakeStore{user: UserProfileSummary{UserID: 42}}
	var gotLimit, gotOffset int
	service := NewService(store).WithBookmarkLister(profileBookmarkListerFunc(func(_ context.Context, _, _ int64, limit, offset int) (BookmarkPage, error) {
		gotLimit, gotOffset = limit, offset
		return BookmarkPage{}, nil
	}))

	if _, err := service.ListProfileBookmarks(context.Background(), "alice", identity.Actor{}, 1, 0); err != nil {
		t.Fatalf("default page: %v", err)
	}
	if gotLimit != 20 || gotOffset != 0 {
		t.Fatalf("default page limit=%d offset=%d", gotLimit, gotOffset)
	}
	if _, err := service.ListProfileBookmarks(context.Background(), "alice", identity.Actor{}, 3, 500); err != nil {
		t.Fatalf("clamped page: %v", err)
	}
	if gotLimit != 50 || gotOffset != 100 {
		t.Fatalf("clamped page limit=%d offset=%d", gotLimit, gotOffset)
	}
}

// TestListMyBookmarksRequiresActiveActor 固定本人名单的登录要求。
func TestListMyBookmarksRequiresActiveActor(t *testing.T) {
	service := NewService(&fakeStore{}).WithBookmarkLister(profileBookmarkListerFunc(func(context.Context, int64, int64, int, int) (BookmarkPage, error) {
		t.Fatal("anonymous request must not reach the lister")
		return BookmarkPage{}, nil
	}))
	if _, err := service.ListMyBookmarks(context.Background(), identity.Actor{}, 1, 20); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("want permission denied, got %v", err)
	}
}

// TestListProfileBookmarksWithoutListerReturnsEmptyPage 固定可选面缺失时的姿态：
// 返回空页而不是报错，资料页不该因为一个可选区块 500。
func TestListProfileBookmarksWithoutListerReturnsEmptyPage(t *testing.T) {
	service := NewService(&fakeStore{user: UserProfileSummary{UserID: 42}})
	view, err := service.ListProfileBookmarks(context.Background(), "alice", identity.Actor{}, 1, 20)
	if err != nil {
		t.Fatalf("ListProfileBookmarks: %v", err)
	}
	if view.Page.Items == nil || len(view.Page.Items) != 0 || view.Page.Total != 0 {
		t.Fatalf("unexpected empty page %#v", view.Page)
	}
}

// TestListProfileBookmarksPropagatesFeatureGateError 固定开关关闭时的传播：
// Forum 返回 ErrBookmarksDisabled，Profile 原样上抛给 HTTP 层映射 403。
func TestListProfileBookmarksPropagatesFeatureGateError(t *testing.T) {
	sentinel := errors.New("forum: bookmarks are disabled")
	service := NewService(&fakeStore{user: UserProfileSummary{UserID: 42}}).WithBookmarkLister(profileBookmarkListerFunc(func(context.Context, int64, int64, int, int) (BookmarkPage, error) {
		return BookmarkPage{}, sentinel
	}))
	if _, err := service.ListProfileBookmarks(context.Background(), "alice", identity.Actor{}, 1, 20); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
}

// TestListProfileBookmarksRequiresUsername 固定空用户名走 not found，而不是
// 「查到了 0 号用户」。
func TestListProfileBookmarksRequiresUsername(t *testing.T) {
	service := NewService(&fakeStore{})
	if _, err := service.ListProfileBookmarks(context.Background(), "   ", identity.Actor{}, 1, 20); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("want ErrProfileNotFound, got %v", err)
	}
}
