package profilecontroller

// 收藏名单 HTTP 层用例：这两条读路由是权限最敏感的端点——他人视角必须由
// 服务端只出 public 行，不能靠前端过滤。用例覆盖匿名/他人/本人三种视角、
// 真实缓存头、产品开关关闭、用户名不存在、分页钳制。
//
// 复用 controller_test.go 的装配（newProfileTestAppWithOptions + profileFakeStore
// + performProfileRequest），不新建测试框架。

import (
	"context"
	"encoding/json"
	"io"
	nethttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
)

const (
	profileBookmarkOwnerID = 7
	// 造数：2 条 public + 2 条 private，target id 互不重叠，便于断言「私密一条都不出现」。
	profileBookmarkPublicTopicID    = 101
	profileBookmarkPublicCommentID  = 202
	profileBookmarkPrivateTopicID   = 303
	profileBookmarkPrivateCommentID = 404
)

type profileBookmarkListCall struct {
	viewerID int64
	ownerID  int64
	limit    int
	offset   int
}

// profileBookmarkListerFake 复刻生产侧 Forum 的可见性口径：viewer 不是行主时
// 只返回 public 行。这样 HTTP 层能拿到「服务端过滤后」的真实响应，而不是靠
// 测试自己筛。
type profileBookmarkListerFake struct {
	entries []profile.BookmarkItem
	calls   []profileBookmarkListCall
	err     error
}

func (f *profileBookmarkListerFake) ListForViewer(_ context.Context, viewerID, ownerUserID int64, limit, offset int) (profile.BookmarkPage, error) {
	f.calls = append(f.calls, profileBookmarkListCall{viewerID: viewerID, ownerID: ownerUserID, limit: limit, offset: offset})
	if f.err != nil {
		return profile.BookmarkPage{}, f.err
	}
	visible := make([]profile.BookmarkItem, 0, len(f.entries))
	for _, entry := range f.entries {
		if viewerID != ownerUserID && entry.Visibility != forum.BookmarkVisibilityPublic {
			continue
		}
		item := entry
		// 与生产适配器（providers.ForumBookmarkLister）同一口径：非行主视角
		// 清空提醒列。adapter 自身的清空行为由 Providers 包的专项用例锁定；
		// 这里复刻它，让 HTTP 层断言的是「链上真实形状」。
		if viewerID != ownerUserID {
			item.RemindAt = nil
			item.RemindedAt = nil
		}
		visible = append(visible, item)
	}
	page := profile.BookmarkPage{Items: []profile.BookmarkItem{}, Total: int64(len(visible))}
	if offset >= len(visible) {
		return page, nil
	}
	end := offset + limit
	if end > len(visible) {
		end = len(visible)
	}
	page.Items = visible[offset:end]
	page.HasMore = end < len(visible)
	return page, nil
}

func newProfileBookmarkEntries() []profile.BookmarkItem {
	remindAt := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	remindedAt := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	return []profile.BookmarkItem{
		{TargetType: forum.BookmarkTargetTopic, TargetID: profileBookmarkPublicTopicID, Visibility: forum.BookmarkVisibilityPublic, RemindAt: &remindAt, RemindedAt: &remindedAt},
		{TargetType: forum.BookmarkTargetComment, TargetID: profileBookmarkPublicCommentID, Visibility: forum.BookmarkVisibilityPublic, RemindAt: &remindAt},
		{TargetType: forum.BookmarkTargetTopic, TargetID: profileBookmarkPrivateTopicID, Visibility: forum.BookmarkVisibilityPrivate, RemindAt: &remindAt},
		{TargetType: forum.BookmarkTargetComment, TargetID: profileBookmarkPrivateCommentID, Visibility: forum.BookmarkVisibilityPrivate, RemindedAt: &remindedAt},
	}
}

func newProfileBookmarkApp(t *testing.T, lister profile.BookmarkLister) *fiber.App {
	t.Helper()
	app, _, _ := newProfileTestAppWithOptions(
		profileForumReadPolicy{guestRead: "public", ok: true},
		profileTestOptions{bookmarks: lister},
	)
	return app
}

func decodeProfileBookmarkPage(t *testing.T, resp *nethttp.Response) profile.BookmarkPage {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 bookmark page, got %d body=%s", resp.StatusCode, body)
	}
	var envelope profileEnvelope[profile.BookmarkPage]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode bookmark page: %v", err)
	}
	return envelope.Data
}

func assertProfileBookmarkError(t *testing.T, resp *nethttp.Response, wantStatus int, wantReason string) {
	t.Helper()
	defer resp.Body.Close()
	var envelope profileEnvelope[apphttpErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if resp.StatusCode != wantStatus || envelope.Code != wantStatus || envelope.Data.Reason != wantReason {
		t.Fatalf("expected %d %q, got status=%d code=%d reason=%q",
			wantStatus, wantReason, resp.StatusCode, envelope.Code, envelope.Data.Reason)
	}
}

// apphttpErrorData 镜像 apphttp.ErrorData 的传输字段：错误体形状是契约的一部分。
type apphttpErrorData struct {
	Reason            string              `json:"reason"`
	Fields            map[string][]string `json:"fields,omitempty"`
	RetryAfterSeconds *int                `json:"retryAfterSeconds,omitempty"`
}

// TestProfileBookmarksHTTPAnonymousSeesOnlyPublicRows 固定最敏感的一条：
// 匿名访问他人名单时，服务端只返回 public 行，私密目标一条都不能出现在响应里。
func TestProfileBookmarksHTTPAnonymousSeesOnlyPublicRows(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app := newProfileBookmarkApp(t, lister)

	page := decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/bookmarks", nil, nil))
	if len(page.Items) != 2 || page.Total != 2 || page.HasMore {
		t.Fatalf("anonymous page = %#v, want 2 public rows", page)
	}
	for _, item := range page.Items {
		if item.Visibility != forum.BookmarkVisibilityPublic {
			t.Fatalf("anonymous viewer received a non-public row: %#v", item)
		}
	}
	assertNoPrivateProfileBookmarkTargets(t, page.Items)

	if len(lister.calls) != 1 {
		t.Fatalf("lister calls = %d, want 1", len(lister.calls))
	}
	call := lister.calls[0]
	if call.viewerID != 0 || call.ownerID != profileBookmarkOwnerID {
		t.Fatalf("anonymous must pass viewer=0 owner=%d, got %#v", profileBookmarkOwnerID, call)
	}
}

// TestProfileBookmarksHTTPIgnoresClientSuppliedViewer 固定「视角来自会话，不来自查询串」：
// 匿名请求带上 viewerId/owner 参数也不能拿到私密行。
func TestProfileBookmarksHTTPIgnoresClientSuppliedViewer(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app := newProfileBookmarkApp(t, lister)

	page := decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet,
		"/api/v1/profiles/alice/bookmarks?viewerId=7&owner=7&userId=7", nil, nil))
	assertNoPrivateProfileBookmarkTargets(t, page.Items)
	if lister.calls[0].viewerID != 0 {
		t.Fatalf("client-supplied viewer leaked into the service: %#v", lister.calls[0])
	}
}

// TestProfileBookmarksHTTPOwnerSeesPrivateRows 固定本人视角：含私密行，且每条
// 都带 visibility 字段供前端加锁标记。
func TestProfileBookmarksHTTPOwnerSeesPrivateRows(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app := newProfileBookmarkApp(t, lister)
	cookie := loginProfileUser(t, app, profileBookmarkOwnerID)

	page := decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/bookmarks", nil, cookie))
	if len(page.Items) != 4 || page.Total != 4 {
		t.Fatalf("owner page = %#v, want 4 rows including private", page)
	}
	private := 0
	for _, item := range page.Items {
		if item.Visibility == "" {
			t.Fatalf("owner row is missing visibility: %#v", item)
		}
		if item.Visibility == forum.BookmarkVisibilityPrivate {
			private++
		}
	}
	if private != 2 {
		t.Fatalf("owner private rows = %d, want 2", private)
	}
	if call := lister.calls[0]; call.viewerID != profileBookmarkOwnerID {
		t.Fatalf("owner view must pass viewer=%d, got %#v", profileBookmarkOwnerID, call)
	}
	// 本人视角（回归）：两列仍在，不能把功能修坏。
	remindAt := time.Date(2027, 3, 1, 8, 0, 0, 0, time.UTC)
	remindedAt := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	ownerRows := 0
	for _, item := range page.Items {
		switch item.TargetID {
		case profileBookmarkPublicTopicID:
			if item.RemindAt == nil || !item.RemindAt.Equal(remindAt) || item.RemindedAt == nil || !item.RemindedAt.Equal(remindedAt) {
				t.Fatalf("owner must keep both reminder columns: %#v", item)
			}
			ownerRows++
		case profileBookmarkPublicCommentID:
			if item.RemindAt == nil || item.RemindedAt != nil {
				t.Fatalf("owner scheduled-but-unsent row shape mismatch: %#v", item)
			}
			ownerRows++
		}
	}
	if ownerRows != 2 {
		t.Fatalf("owner reminder-bearing rows = %d, want 2", ownerRows)
	}

	// 本人的 /profile/bookmarks 与公开路径同一口径（都含私密）。
	own := decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile/bookmarks", nil, cookie))
	if len(own.Items) != 4 || own.Total != 4 {
		t.Fatalf("self list = %#v, want 4 rows", own)
	}
}

// TestProfileBookmarksHTTPOtherMemberSeesOnlyPublicRows 固定「登录但不是行主」：
// 登录态不能成为看私密的理由。
func TestProfileBookmarksHTTPOtherMemberSeesOnlyPublicRows(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app, _, _ := newProfileTestAppWithOptions(
		profileForumReadPolicy{guestRead: "public", ok: true},
		profileTestOptions{
			bookmarks: lister,
			actors: map[int64]identity.Actor{
				profileBookmarkOwnerID: {ID: profileBookmarkOwnerID, Status: identity.UserStatusActive},
				8:                      {ID: 8, Status: identity.UserStatusActive},
			},
		},
	)
	cookie := loginProfileUser(t, app, 8)

	page := decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/bookmarks", nil, cookie))
	assertNoPrivateProfileBookmarkTargets(t, page.Items)
	if len(page.Items) != 2 {
		t.Fatalf("other member page = %#v, want 2 public rows", page)
	}
	if call := lister.calls[0]; call.viewerID != 8 || call.ownerID != profileBookmarkOwnerID {
		t.Fatalf("other member must pass viewer=8 owner=%d, got %#v", profileBookmarkOwnerID, call)
	}
	// 登录他人同样不得拿到行主的提醒时间。
	for _, item := range page.Items {
		if item.RemindAt != nil || item.RemindedAt != nil {
			t.Fatalf("other member received private schedule data: %#v", item)
		}
	}
}

// TestProfileBookmarksHTTPCacheHeadersDifferByViewer 断言真实缓存头：
// 本人视角含私密行，必须 private, no-store；他人/匿名只含公开行，短缓存 + Vary: Cookie。
func TestProfileBookmarksHTTPCacheHeadersDifferByViewer(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app := newProfileBookmarkApp(t, lister)

	anonymous := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/bookmarks", nil, nil)
	if cache := anonymous.Header.Get("Cache-Control"); cache != "public, max-age=30" {
		t.Fatalf("anonymous cache-control = %q, want %q", cache, "public, max-age=30")
	}
	if vary := anonymous.Header.Get("Vary"); !strings.Contains(vary, "Cookie") {
		t.Fatalf("anonymous Vary = %q, want Cookie", vary)
	}
	_, _ = io.Copy(io.Discard, anonymous.Body)
	anonymous.Body.Close()

	cookie := loginProfileUser(t, app, profileBookmarkOwnerID)
	owner := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/bookmarks", nil, cookie)
	if cache := owner.Header.Get("Cache-Control"); cache != "private, no-store" {
		t.Fatalf("owner cache-control = %q, want %q", cache, "private, no-store")
	}
	vary := owner.Header.Get("Vary")
	for _, header := range []string{"Cookie", "Authorization", "Accept-Language"} {
		if !strings.Contains(vary, header) {
			t.Fatalf("owner Vary = %q, want %s", vary, header)
		}
	}
	_, _ = io.Copy(io.Discard, owner.Body)
	owner.Body.Close()

	self := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile/bookmarks", nil, cookie)
	if cache := self.Header.Get("Cache-Control"); cache != "private, no-store" {
		t.Fatalf("self list cache-control = %q, want %q", cache, "private, no-store")
	}
	_, _ = io.Copy(io.Discard, self.Body)
	self.Body.Close()
}

// TestProfileBookmarksHTTPDisabledFeatureReturns403 固定开关关闭口径：两条读路由
// 与 Forum 写路由同一个 403 forum.bookmarks_disabled，错误体形状一致。
func TestProfileBookmarksHTTPDisabledFeatureReturns403(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries(), err: forum.ErrBookmarksDisabled}
	app := newProfileBookmarkApp(t, lister)
	cookie := loginProfileUser(t, app, profileBookmarkOwnerID)

	for _, path := range []string{"/api/v1/profiles/alice/bookmarks", "/api/v1/profile/bookmarks"} {
		t.Run(path, func(t *testing.T) {
			resp := performProfileRequest(t, app, nethttp.MethodGet, path, nil, cookie)
			defer resp.Body.Close()
			var envelope profileEnvelope[apphttpErrorData]
			if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			// 与 Forum 写路由同一形状：{code, message, data.reason}，reason 用具名常量。
			if resp.StatusCode != nethttp.StatusForbidden || envelope.Code != nethttp.StatusForbidden ||
				envelope.Data.Reason != forum.CodeBookmarksDisabled || envelope.Message == "" {
				t.Fatalf("disabled feature response = status=%d envelope=%#v", resp.StatusCode, envelope)
			}
		})
	}
}

// TestProfileBookmarksHTTPUnknownUsernameReturns404 固定 not-found 不泄露存在性：
// 名单端点的 404 与既有资料端点逐字一致（同 status、同 reason、同 message）。
func TestProfileBookmarksHTTPUnknownUsernameReturns404(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app, _, store := newProfileTestAppWithOptions(
		profileForumReadPolicy{guestRead: "public", ok: true},
		profileTestOptions{bookmarks: lister},
	)
	store.lookupErr = profile.ErrProfileNotFound

	profileResp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/ghost", nil, nil)
	bookmarksResp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/ghost/bookmarks", nil, nil)
	defer bookmarksResp.Body.Close()

	var profileBody, bookmarksBody profileEnvelope[apphttpErrorData]
	if err := json.NewDecoder(profileResp.Body).Decode(&profileBody); err != nil {
		t.Fatalf("decode profile error: %v", err)
	}
	profileResp.Body.Close()
	if err := json.NewDecoder(bookmarksResp.Body).Decode(&bookmarksBody); err != nil {
		t.Fatalf("decode bookmarks error: %v", err)
	}
	if profileResp.StatusCode != nethttp.StatusNotFound || bookmarksResp.StatusCode != nethttp.StatusNotFound {
		t.Fatalf("status profile=%d bookmarks=%d, want 404/404", profileResp.StatusCode, bookmarksResp.StatusCode)
	}
	if bookmarksBody.Data.Reason != profileBody.Data.Reason || bookmarksBody.Data.Reason != profile.CodeProfileNotFound {
		t.Fatalf("reason leak: profile=%q bookmarks=%q", profileBody.Data.Reason, bookmarksBody.Data.Reason)
	}
	if bookmarksBody.Message != profileBody.Message {
		t.Fatalf("message differs between endpoints: profile=%q bookmarks=%q", profileBody.Message, bookmarksBody.Message)
	}
	if len(lister.calls) != 0 {
		t.Fatalf("unknown username must not reach the lister, got %d calls", len(lister.calls))
	}
}

// TestProfileBookmarksHTTPPaginationClamp 固定分页钳制与 OpenAPI 声明一致：
// page 1..200、perPage 1..50、默认 20。
func TestProfileBookmarksHTTPPaginationClamp(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app := newProfileBookmarkApp(t, lister)
	cookie := loginProfileUser(t, app, profileBookmarkOwnerID)

	cases := []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
	}{
		{name: "defaults", query: "", wantLimit: 20, wantOffset: 0},
		{name: "zero values fall back to defaults", query: "?page=0&perPage=0", wantLimit: 20, wantOffset: 0},
		{name: "perPage above max is clamped", query: "?perPage=201", wantLimit: 50, wantOffset: 0},
		{name: "page above max is clamped", query: "?page=201&perPage=10", wantLimit: 10, wantOffset: 1990},
		{name: "page 2 offsets by one page", query: "?page=2&perPage=10", wantLimit: 10, wantOffset: 10},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			lister.calls = nil
			_ = decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet,
				"/api/v1/profiles/alice/bookmarks"+test.query, nil, cookie))
			if len(lister.calls) != 1 {
				t.Fatalf("lister calls = %d, want 1", len(lister.calls))
			}
			call := lister.calls[0]
			if call.limit != test.wantLimit || call.offset != test.wantOffset {
				t.Fatalf("limit/offset = %d/%d, want %d/%d", call.limit, call.offset, test.wantLimit, test.wantOffset)
			}
		})
	}

	// /profile/bookmarks 走同一套钳制。
	lister.calls = nil
	_ = decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile/bookmarks?perPage=201", nil, cookie))
	if call := lister.calls[0]; call.limit != 50 || call.offset != 0 {
		t.Fatalf("self list limit/offset = %d/%d, want 50/0", call.limit, call.offset)
	}
}

// TestProfileBookmarksHTTPRequiresLoginForSelfList 固定本人列表必须登录。
func TestProfileBookmarksHTTPRequiresLoginForSelfList(t *testing.T) {
	lister := &profileBookmarkListerFake{entries: newProfileBookmarkEntries()}
	app := newProfileBookmarkApp(t, lister)

	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile/bookmarks", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if len(lister.calls) != 0 {
		t.Fatalf("anonymous self list must not reach the lister, got %d calls", len(lister.calls))
	}
}

// TestProfileBookmarksHTTPEmptyListKeepsArrayShape 固定空名单是 [] 而不是 null：
// 前端把 items 当数组消费。
func TestProfileBookmarksHTTPEmptyListKeepsArrayShape(t *testing.T) {
	app := newProfileBookmarkApp(t, &profileBookmarkListerFake{})
	page := decodeProfileBookmarkPage(t, performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/bookmarks", nil, nil))
	if page.Items == nil || len(page.Items) != 0 || page.Total != 0 || page.HasMore {
		t.Fatalf("empty page = %#v", page)
	}
}

func assertNoPrivateProfileBookmarkTargets(t *testing.T, items []profile.BookmarkItem) {
	t.Helper()
	for _, item := range items {
		switch item.TargetID {
		case profileBookmarkPrivateTopicID, profileBookmarkPrivateCommentID:
			t.Fatalf("private bookmark target %d leaked to a non-owner viewer: %#v", item.TargetID, item)
		}
		if item.Visibility == forum.BookmarkVisibilityPrivate {
			t.Fatalf("private bookmark leaked to a non-owner viewer: %#v", item)
		}
	}
}
