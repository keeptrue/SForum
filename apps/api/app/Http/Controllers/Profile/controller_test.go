package profilecontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
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
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := profileFakeActors{actors: map[int64]identity.Actor{
		7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
	}}
	store := &profileFakeStore{
		user:    profile.UserProfileSummary{UserID: 7, Username: "alice", DisplayName: "Alice"},
		profile: profile.Profile{UserID: 7, Bio: "hello"},
		stats:   profile.ProfileStats{TopicCount: 3, CommentCount: 12},
	}
	service := profile.NewService(store)
	if editor != nil {
		service.WithNicknameEditor(editor)
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
}

func (a profileFakeActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
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
