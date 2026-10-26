package profilecontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	"github.com/keeptrue/sforum/apps/api/config"
)

type profileEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

func TestControllerPublicProfileRead(t *testing.T) {
	app, _, _ := newProfileTestApp()
	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body profileEnvelope[profile.PublicProfile]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Username != "alice" {
		t.Fatalf("expected username alice, got %q", body.Data.Username)
	}
}

func TestControllerPublicProfileRequiresLoginBeforeLoadingData(t *testing.T) {
	app, _, store := newProfileTestAppWithPolicy(profileForumReadPolicy{guestRead: "login_required", ok: true})
	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
	if store.publicProfileCalls != 0 {
		t.Fatalf("profile data loaded before guest-read authorization: calls=%d", store.publicProfileCalls)
	}

	cookie := loginProfileUser(t, app, 7)
	resp = performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 with login, got %d", resp.StatusCode)
	}
	if store.publicProfileCalls != 1 {
		t.Fatalf("expected one authorized profile load, got %d", store.publicProfileCalls)
	}
}

func TestControllerPublicProfileFailsClosedWithoutPolicySnapshot(t *testing.T) {
	app, _, store := newProfileTestAppWithPolicy(profileForumReadPolicy{})
	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if resp.StatusCode != nethttp.StatusServiceUnavailable {
		t.Fatalf("expected 503 without policy snapshot, got %d", resp.StatusCode)
	}
	if store.publicProfileCalls != 0 {
		t.Fatalf("profile data loaded without policy snapshot: calls=%d", store.publicProfileCalls)
	}
}

func TestControllerMyProfileRequiresLogin(t *testing.T) {
	app, _, _ := newProfileTestApp()
	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
}

func TestControllerUpdateProfileRequiresLogin(t *testing.T) {
	app, _, _ := newProfileTestApp()
	body := []byte(`{"bio":"hello"}`)
	resp := performProfileRequest(t, app, nethttp.MethodPut, "/api/v1/profile", body, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
}

func TestControllerUpdateProfileAllowsCurrentUser(t *testing.T) {
	app, _, store := newProfileTestApp()
	cookie := loginProfileUser(t, app, 7)
	body := []byte(`{"bio":"updated bio"}`)
	resp := performProfileRequest(t, app, nethttp.MethodPut, "/api/v1/profile", body, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 update, got %d", resp.StatusCode)
	}
	if store.upserted.Bio != "updated bio" {
		t.Fatalf("expected upserted bio, got %q", store.upserted.Bio)
	}
	// 只能写自己：upsert 目标必须是当前登录用户。
	if store.upserted.UserID != 7 {
		t.Fatalf("expected upsert target user 7, got %d", store.upserted.UserID)
	}
}

func TestControllerUpdateProfileRejectsInvalidWebsite(t *testing.T) {
	app, _, _ := newProfileTestApp()
	cookie := loginProfileUser(t, app, 7)
	body := []byte(`{"websiteUrl":"not-a-url"}`)
	resp := performProfileRequest(t, app, nethttp.MethodPut, "/api/v1/profile", body, cookie)
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422 invalid website, got %d", resp.StatusCode)
	}
}

func TestControllerUploadAvatarRequiresLogin(t *testing.T) {
	app, _, _ := newProfileTestApp()
	resp := performProfileRequest(t, app, nethttp.MethodPost, "/api/v1/profile/avatar", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
}

func TestControllerUploadAvatarRequiresAttachmentUploadPermission(t *testing.T) {
	app, _, _ := newProfileTestApp()
	cookie := loginProfileUser(t, app, 7)
	body, contentType := multipartAvatarBody(t)
	resp := performProfileMultipartRequest(t, app, nethttp.MethodPost, "/api/v1/profile/avatar", body, contentType, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without attachment.upload, got %d", resp.StatusCode)
	}
}

func newProfileTestApp() (*fiber.App, *authsession.Manager, *profileFakeStore) {
	return newProfileTestAppWithPolicy(profileForumReadPolicy{guestRead: "public", ok: true})
}

func newProfileTestAppWithPolicy(policy profileForumReadPolicy) (*fiber.App, *authsession.Manager, *profileFakeStore) {
	return newProfileTestAppWithEditor(policy, nil)
}

func newProfileTestAppWithEditor(policy profileForumReadPolicy, editor profile.NicknameEditor) (*fiber.App, *authsession.Manager, *profileFakeStore) {
	return newProfileTestAppWithOptions(policy, profileTestOptions{editor: editor})
}

// profileTestOptions 让单个测试只替换它关心的装配件，其余保持默认。
type profileTestOptions struct {
	editor    profile.NicknameEditor
	tabs      profile.ProfileTabProvider
	actors    map[int64]identity.Actor
	actorsErr error
}

func newProfileTestAppWithOptions(policy profileForumReadPolicy, opts profileTestOptions) (*fiber.App, *authsession.Manager, *profileFakeStore) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	actors := opts.actors
	if actors == nil {
		actors = map[int64]identity.Actor{
			7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		}
	}
	users := profileFakeActors{actors: actors, err: opts.actorsErr}
	store := &profileFakeStore{
		user:    profile.UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: profile.Profile{UserID: 7, Bio: "hello"},
		stats:   profile.ProfileStats{TopicCount: 3, CommentCount: 12},
	}
	service := profile.NewService(store)
	if opts.editor != nil {
		service.WithNicknameEditor(opts.editor)
	}
	if opts.tabs != nil {
		service.WithProfileTabs(opts.tabs)
	}
	controller := NewController(service, users, manager, policy)
	loginProvider := profileRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID, err := strconv.ParseInt(c.Params("id"), 10, 64)
			if err != nil || userID == 0 {
				userID = 7
			}
			_, err = manager.Start(c, userID)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
	return app, manager, store
}

type profileForumReadPolicy struct {
	guestRead string
	ok        bool
}

func (p profileForumReadPolicy) ForumReadPolicySnapshot() (string, string, uint64, bool) {
	if !p.ok {
		return "", "", 0, false
	}
	return p.guestRead, "hidden", 1, true
}

func loginProfileUser(t *testing.T, app *fiber.App, userID int64) *nethttp.Cookie {
	t.Helper()
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/test-login/"+strconv.FormatInt(userID, 10), nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK || len(resp.Cookies()) == 0 {
		t.Fatalf("expected login cookie, got %d", resp.StatusCode)
	}
	return resp.Cookies()[0]
}

func multipartAvatarBody(t *testing.T) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "avatar.jpg")
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := part.Write([]byte("not-a-real-image")); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func performProfileRequest(t *testing.T, app *fiber.App, method, path string, body []byte, cookie *nethttp.Cookie) *nethttp.Response {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

func performProfileMultipartRequest(t *testing.T, app *fiber.App, method, path string, body []byte, contentType string, cookie *nethttp.Cookie) *nethttp.Response {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return resp
}

type profileFakeActors struct {
	actors map[int64]identity.Actor
	// err 模拟会话有效但主体加载失败（账号已删除、身份存储抖动）。
	err error
}

func (a profileFakeActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	if a.err != nil {
		return identity.Actor{}, a.err
	}
	return a.actors[userID], nil
}

type profileRouteProviderFunc func(api fiber.Router)

func (f profileRouteProviderFunc) RegisterRoutes(api fiber.Router) { f(api) }

type profileFakeStore struct {
	user               profile.UserProfileSummary
	profile            profile.Profile
	stats              profile.ProfileStats
	recent             []forum.TopicSummary
	activityTopics     []forum.TopicSummary
	comments           []profile.ProfileCommentActivity
	upserted           profile.Profile
	publicProfileCalls int
}

func (s *profileFakeStore) GetProfile(context.Context, int64) (profile.Profile, error) {
	return s.profile, nil
}

func (s *profileFakeStore) UpsertProfile(_ context.Context, input profile.Profile) (profile.Profile, error) {
	s.upserted = input
	return input, nil
}

func (s *profileFakeStore) SetAvatarAttachment(_ context.Context, userID int64, attachmentID *int64, actorUserID int64) (profile.Profile, error) {
	s.profile.UserID = userID
	s.profile.AvatarAttachmentID = attachmentID
	return s.profile, nil
}

func (s *profileFakeStore) GetAvatarAttachment(context.Context, int64) (profile.AvatarAttachment, error) {
	return profile.AvatarAttachment{}, profile.ErrProfileInvalid
}

func (s *profileFakeStore) GetUserSummaryByUsername(context.Context, string) (profile.UserProfileSummary, error) {
	s.publicProfileCalls++
	return s.user, nil
}

func (s *profileFakeStore) GetUserSummaryByID(context.Context, int64) (profile.UserProfileSummary, error) {
	return s.user, nil
}

func (s *profileFakeStore) GetProfileStats(context.Context, int64) (profile.ProfileStats, error) {
	return s.stats, nil
}

// 公开检索当前没有 HTTP 路由；这个 fake 只需要满足 Store 接口。
func (s *profileFakeStore) SearchPublicUsers(context.Context, string, int) ([]profile.UserSearchHit, error) {
	return nil, nil
}

func (s *profileFakeStore) ListRecentTopics(context.Context, int64, int) ([]forum.TopicSummary, error) {
	return s.recent, nil
}

func (s *profileFakeStore) ListRecentActivityTopics(context.Context, int64, int) ([]forum.TopicSummary, error) {
	return s.activityTopics, nil
}

func (s *profileFakeStore) ListActivityTopics(_ context.Context, _ int64, limit, offset int) ([]forum.TopicSummary, error) {
	if offset >= len(s.activityTopics) {
		return []forum.TopicSummary{}, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(s.activityTopics) {
		end = len(s.activityTopics)
	}
	return s.activityTopics[offset:end], nil
}

func (s *profileFakeStore) ListRecentComments(context.Context, int64, int) ([]profile.ProfileCommentActivity, error) {
	return s.comments, nil
}

func (s *profileFakeStore) ListActivityComments(_ context.Context, _ int64, limit, offset int) ([]profile.ProfileCommentActivity, error) {
	if offset >= len(s.comments) {
		return []profile.ProfileCommentActivity{}, nil
	}
	end := offset + limit
	if limit <= 0 || end > len(s.comments) {
		end = len(s.comments)
	}
	return s.comments[offset:end], nil
}

func TestControllerPublicActivitiesPaginates(t *testing.T) {
	app, _, store := newProfileTestApp()
	store.stats = profile.ProfileStats{TopicCount: 3, CommentCount: 0}
	store.activityTopics = []forum.TopicSummary{
		{ID: 1, Title: "a", Slug: "a"},
		{ID: 2, Title: "b", Slug: "b"},
		{ID: 3, Title: "c", Slug: "c"},
	}
	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice/activities?kind=topic&page=1&perPage=2", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body profileEnvelope[profile.ActivityPage]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Page != 1 || body.Data.PerPage != 2 || body.Data.Total != 3 || !body.Data.HasMore || len(body.Data.Items) != 2 {
		t.Fatalf("unexpected activity page: %#v", body.Data)
	}
	if body.Data.Kind != profile.ActivityKindTopic || body.Data.Items[0].Kind != profile.ActivityKindTopic {
		t.Fatalf("unexpected kind: %#v", body.Data)
	}
}

// controllerTabProvider 记录 Controller 交给服务层的 viewer，并按接口约定过滤：
// tab.Permission == "" || viewer.Can(tab.Permission)。
type controllerTabProvider struct {
	tabs   []profile.ProfileExtensionTab
	viewer identity.Actor
	calls  int
}

func (p *controllerTabProvider) ProfileTabs(_ context.Context, viewer identity.Actor) ([]profile.ProfileExtensionTab, error) {
	p.calls++
	p.viewer = viewer
	visible := make([]profile.ProfileExtensionTab, 0, len(p.tabs))
	for _, tab := range p.tabs {
		if tab.Permission == "" || viewer.Can(tab.Permission) {
			visible = append(visible, tab)
		}
	}
	return visible, nil
}

func controllerProfileTabs() []profile.ProfileExtensionTab {
	return []profile.ProfileExtensionTab{
		{ExtensionID: "demo.plugin", ID: "public", Kind: "hostLink", URL: "/tags"},
		{ExtensionID: "demo.plugin", ID: "gated", Kind: "hostLink", URL: "/moderation", Permission: "demo.moderate"},
		{ExtensionID: "demo.plugin", ID: "widget", Kind: "widget", ComponentID: "demo.stats"},
	}
}

func profileTabIDs(tabs []profile.ProfileExtensionTab) []string {
	ids := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		ids = append(ids, tab.ID)
	}
	return ids
}

// readPublicProfileBody 同时返回解码后的聚合与原始 JSON，后者用来证明
// permission 这类后端专用字段没有出现在响应里。
func readPublicProfileBody(t *testing.T, resp *nethttp.Response) (profile.PublicProfile, map[string]any) {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var decoded profileEnvelope[profile.PublicProfile]
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode raw envelope: %v", err)
	}
	return decoded.Data, envelope
}

func assertNoTabPermissionField(t *testing.T, envelope map[string]any) {
	t.Helper()
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("missing data envelope: %#v", envelope)
	}
	rawTabs, ok := data["extensionTabs"].([]any)
	if !ok {
		t.Fatalf("missing extensionTabs: %#v", data)
	}
	for _, rawTab := range rawTabs {
		tab, ok := rawTab.(map[string]any)
		if !ok {
			t.Fatalf("unexpected tab shape: %#v", rawTab)
		}
		if _, leaked := tab["permission"]; leaked {
			t.Fatalf("permission must never reach clients: %#v", tab)
		}
	}
}

func TestControllerPublicProfileFiltersExtensionTabsByViewer(t *testing.T) {
	tabs := &controllerTabProvider{tabs: controllerProfileTabs()}
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true}, profileTestOptions{
		tabs: tabs,
		actors: map[int64]identity.Actor{
			7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
			8: {ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{"demo.moderate": true}},
		},
	})

	anonymousResp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if anonymousResp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 anonymous, got %d", anonymousResp.StatusCode)
	}
	anonymous, envelope := readPublicProfileBody(t, anonymousResp)
	if ids := profileTabIDs(anonymous.ExtensionTabs); len(ids) != 2 || ids[0] != "public" || ids[1] != "widget" {
		t.Fatalf("anonymous tabs = %v, want [public widget]", ids)
	}
	assertNoTabPermissionField(t, envelope)

	cookie := loginProfileUser(t, app, 8)
	memberResp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, cookie)
	if memberResp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 with session, got %d", memberResp.StatusCode)
	}
	member, memberEnvelope := readPublicProfileBody(t, memberResp)
	if ids := profileTabIDs(member.ExtensionTabs); len(ids) != 3 || ids[1] != "gated" {
		t.Fatalf("authenticated tabs = %v, want gated tab included", ids)
	}
	if tabs.viewer.ID != 8 || !tabs.viewer.Can("demo.moderate") {
		t.Fatalf("provider received viewer %#v, want authenticated user 8", tabs.viewer)
	}
	assertNoTabPermissionField(t, memberEnvelope)
}

// 会话存在但主体加载失败时必须降级为匿名视角：公开页不能因为登录态异常 500。
func TestControllerPublicProfileDegradesToAnonymousWhenActorLookupFails(t *testing.T) {
	tabs := &controllerTabProvider{tabs: controllerProfileTabs()}
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true}, profileTestOptions{
		tabs: tabs,
		actors: map[int64]identity.Actor{
			7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{"demo.moderate": true}},
		},
		actorsErr: errors.New("actor store unavailable"),
	})

	cookie := loginProfileUser(t, app, 7)
	resp := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 when actor lookup fails, got %d", resp.StatusCode)
	}
	data, envelope := readPublicProfileBody(t, resp)
	if ids := profileTabIDs(data.ExtensionTabs); len(ids) != 2 || ids[0] != "public" || ids[1] != "widget" {
		t.Fatalf("degraded tabs = %v, want anonymous [public widget]", ids)
	}
	if tabs.viewer.ID != 0 || len(tabs.viewer.Permissions) != 0 {
		t.Fatalf("degraded viewer = %#v, want zero-value actor", tabs.viewer)
	}
	assertNoTabPermissionField(t, envelope)
}

// 与 SiteChrome.publicNavigation / Pages.setPrivatePageResponse 对齐：
// 响应随请求者身份变化，必须声明 private + no-store 和三个变体维度。
func assertViewerScopedHeaders(t *testing.T, response *nethttp.Response) {
	t.Helper()
	if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", got)
	}
	assertVaryFields(t, response.Header.Get("Vary"), "Cookie", "Authorization", "Accept-Language")
}

func assertVaryFields(t *testing.T, value string, expected ...string) {
	t.Helper()
	fields := make(map[string]struct{})
	for _, field := range strings.Split(value, ",") {
		fields[strings.ToLower(strings.TrimSpace(field))] = struct{}{}
	}
	for _, field := range expected {
		if _, ok := fields[strings.ToLower(field)]; !ok {
			t.Fatalf("Vary = %q, missing %s", value, field)
		}
	}
}

// 公开资料页的内容随 Cookie/Authorization 变化（受限扩展 tab 只对有权者出现），
// 匿名与带会话两条路径都必须带同一组变体头，否则中间缓存会把带 tab 的响应喂给游客。
func TestControllerPublicProfileSetsViewerScopedHeaders(t *testing.T) {
	tabs := &controllerTabProvider{tabs: controllerProfileTabs()}
	app, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "public", ok: true}, profileTestOptions{
		tabs: tabs,
		actors: map[int64]identity.Actor{
			7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
			8: {ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{"demo.moderate": true}},
		},
	})

	anonymous := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if anonymous.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 anonymous, got %d", anonymous.StatusCode)
	}
	assertViewerScopedHeaders(t, anonymous)
	anonymous.Body.Close()

	cookie := loginProfileUser(t, app, 8)
	authenticated := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profiles/alice", nil, cookie)
	if authenticated.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 with session, got %d", authenticated.StatusCode)
	}
	assertViewerScopedHeaders(t, authenticated)
	authenticated.Body.Close()

	// 头在游客读取策略检查之前设置：被拒绝的响应同样是「按请求者变化」的。
	loginRequiredApp, _, _ := newProfileTestAppWithOptions(profileForumReadPolicy{guestRead: "login_required", ok: true}, profileTestOptions{})
	denied := performProfileRequest(t, loginRequiredApp, nethttp.MethodGet, "/api/v1/profiles/alice", nil, nil)
	if denied.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", denied.StatusCode)
	}
	assertViewerScopedHeaders(t, denied)
	denied.Body.Close()
}

// 自助读取同样按 viewer 返回内容（自己的资料、以自己为基准的改名预检），
// 所以与公开页共用同一组头；写接口本身不是可缓存方法，不加。
func TestControllerSelfProfileReadsSetViewerScopedHeaders(t *testing.T) {
	editor := &nicknameTestEditor{availability: identity.NicknameAvailability{
		Available:  true,
		Duplicates: 2,
		Normalized: "Alicia",
	}}
	app, _, _ := newProfileTestAppWithEditor(profileForumReadPolicy{guestRead: "public", ok: true}, editor)
	cookie := loginProfileUser(t, app, 7)

	ownProfile := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile", nil, cookie)
	if ownProfile.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 own profile, got %d", ownProfile.StatusCode)
	}
	assertViewerScopedHeaders(t, ownProfile)
	ownProfile.Body.Close()

	availability := performProfileRequest(t, app, nethttp.MethodGet, "/api/v1/profile/nickname/availability?value=Alicia", nil, cookie)
	if availability.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 nickname availability, got %d", availability.StatusCode)
	}
	assertViewerScopedHeaders(t, availability)
	availability.Body.Close()
}
