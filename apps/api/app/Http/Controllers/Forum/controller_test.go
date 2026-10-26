package forumcontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

type forumTestEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type forumTestErrorData struct {
	Reason string `json:"reason"`
}

func TestMapForumCooldownErrorKeepsIndependentReasonAndRetryAt(t *testing.T) {
	retryAt := time.Date(2026, 7, 30, 12, 0, 30, 0, time.UTC)
	for _, test := range []struct {
		cause  error
		reason string
	}{
		{cause: forum.ErrTopicCooldown, reason: forum.CodeTopicCooldown},
		{cause: forum.ErrCommentCooldown, reason: forum.CodeCommentCooldown},
	} {
		mapped := mapForumError(&forum.CooldownError{Cause: test.cause, RetryAt: retryAt})
		apiErr, ok := mapped.(*apphttp.APIError)
		if !ok || apiErr.Reason != test.reason || apiErr.RetryAt == nil || !apiErr.RetryAt.Equal(retryAt) {
			t.Fatalf("mapped cooldown = %#v", mapped)
		}
	}
}

func TestControllerListsPublicForumData(t *testing.T) {
	app, _, _ := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/categories", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 categories, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var categories forumTestEnvelope[[]forum.Category]
	if err := json.NewDecoder(resp.Body).Decode(&categories); err != nil {
		t.Fatalf("decode categories: %v", err)
	}
	if len(categories.Data) != 1 || categories.Data[0].Slug != "general" {
		t.Fatalf("unexpected categories %#v", categories.Data)
	}

	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 topics, got %d", resp.StatusCode)
	}
}

func TestComposerReferenceSelectorRequiresLoginAndReturnsMinimalLabels(t *testing.T) {
	app, _, store := newForumTestApp()
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/composer/references?kind=topic&query=release&limit=5", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", resp.StatusCode)
	}

	cookie := loginForumUser(t, app, 1)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/composer/references?kind=topic&query=release&limit=5", nil, cookie)
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("authenticated status=%d body=%s", resp.StatusCode, body)
	}
	var envelope forumTestEnvelope[forum.ReferenceOptionList]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != 1 || envelope.Data.Items[0].Label != "Release topic" {
		t.Fatalf("response = %#v", envelope.Data)
	}
	if store.referenceInput.Kind != forum.ReferenceKindTopic || store.referenceInput.Query != "release" || store.referenceInput.Limit != 5 {
		t.Fatalf("selector input = %#v", store.referenceInput)
	}
}

func TestPublicForumJSONOmitsEditableSourceAndAuthorizedReadsStayDedicated(t *testing.T) {
	app, _, store := newForumTestApp()
	store.actionTopic = forum.TopicSummary{
		ID: 10, AuthorUserID: 1, Status: forum.TopicStatusActive,
		CurrentRevision: 3, CreatedAt: time.Now().UTC(),
	}
	store.topicEditSource = forum.EditableContentSource{
		RawContent: "M1_TOPIC_HTTP_SOURCE_SECRET", SourceFormat: forum.SourceFormatEditorDocument,
		EditorType: forum.EditorTypeTiptap, EditorVersion: "sf-editor-v1",
		ContentHash: "M1_TOPIC_HTTP_SOURCE_HASH", AttachmentIDs: []int64{7}, CurrentRevision: 3,
	}
	store.commentEditSource = forum.EditableContentSource{
		RawContent: "M1_COMMENT_HTTP_SOURCE_SECRET", SourceFormat: forum.SourceFormatMarkdown,
		EditorType: forum.EditorTypeMarkdown, ContentHash: "M1_COMMENT_HTTP_SOURCE_HASH", CurrentRevision: 2,
	}

	for _, path := range []string{
		"/api/v1/topics/10",
		"/api/v1/topics/by-slug/topic",
		"/api/v1/topics/10/comments?view=tree",
		"/api/v1/topics/10/comments?view=flat",
		"/api/v1/comments/20/replies",
	} {
		resp := performForumRequest(t, app, nethttp.MethodGet, path, nil, nil)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != nethttp.StatusOK {
			t.Fatalf("GET %s status=%d err=%v body=%s", path, resp.StatusCode, err, body)
		}
		serialized := string(body)
		for _, forbidden := range []string{"rawContent", "contentHash", "sourceFormat", "editorType", "editorVersion", "M1_TOPIC_HTTP_SOURCE_SECRET", "M1_COMMENT_HTTP_SOURCE_SECRET"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("public GET %s contains %q: %s", path, forbidden, serialized)
			}
		}
	}

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/edit-source", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("anonymous edit-source status=%d", resp.StatusCode)
	}

	strangerCookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/edit-source", nil, strangerCookie)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusNotFound {
		t.Fatalf("unrelated edit-source status=%d", resp.StatusCode)
	}

	ownerCookie := loginForumUser(t, app, 1)
	for _, mutation := range []struct {
		method     string
		path       string
		body       []byte
		wantStatus int
	}{
		{method: nethttp.MethodPost, path: "/api/v1/topics", body: []byte(`{"categorySlug":"general","title":"M1 public response","content":{"rawContent":"ordinary visible body","sourceFormat":"markdown","editorType":"markdown"}}`), wantStatus: nethttp.StatusCreated},
		{method: nethttp.MethodPatch, path: "/api/v1/topics/10", body: []byte(`{"expectedRevision":3,"content":{"rawContent":"ordinary updated body","sourceFormat":"markdown","editorType":"markdown"}}`), wantStatus: nethttp.StatusOK},
		{method: nethttp.MethodPost, path: "/api/v1/topics/10/comments", body: []byte(`{"content":{"rawContent":"ordinary visible reply","sourceFormat":"markdown","editorType":"markdown"}}`), wantStatus: nethttp.StatusCreated},
		{method: nethttp.MethodPatch, path: "/api/v1/comments/20", body: []byte(`{"expectedRevision":1,"content":{"rawContent":"ordinary updated reply","sourceFormat":"markdown","editorType":"markdown"}}`), wantStatus: nethttp.StatusOK},
	} {
		resp = performForumRequest(t, app, mutation.method, mutation.path, mutation.body, ownerCookie)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != mutation.wantStatus {
			t.Fatalf("%s %s status=%d err=%v body=%s", mutation.method, mutation.path, resp.StatusCode, err, body)
		}
		for _, forbidden := range []string{"\"rawContent\"", "\"contentHash\"", "\"sourceFormat\"", "\"editorType\"", "\"editorVersion\""} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("mutation response %s %s contains %s: %s", mutation.method, mutation.path, forbidden, body)
			}
		}
	}

	for _, endpoint := range []struct {
		path   string
		marker string
	}{
		{path: "/api/v1/topics/10/edit-source", marker: "M1_TOPIC_HTTP_SOURCE_SECRET"},
		{path: "/api/v1/comments/20/edit-source", marker: "M1_COMMENT_HTTP_SOURCE_SECRET"},
	} {
		resp = performForumRequest(t, app, nethttp.MethodGet, endpoint.path, nil, ownerCookie)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != nethttp.StatusOK || !strings.Contains(string(body), endpoint.marker) {
			t.Fatalf("authorized GET %s status=%d err=%v body=%s", endpoint.path, resp.StatusCode, err, body)
		}
	}

	moderatorCookie := loginForumUser(t, app, 5)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/edit-source", nil, moderatorCookie)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("edit-any topic source status=%d", resp.StatusCode)
	}
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/edit-source", nil, moderatorCookie)
	resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("edit-any comment source status=%d", resp.StatusCode)
	}
}

type controllerProtectedShortcodeDispatcher struct {
	calls int
}

func (d *controllerProtectedShortcodeDispatcher) RenderProtectedFragments(_ context.Context, requests []contentregistry.ForumProtectedShortcodeRenderRequest, _ string) ([]contentregistry.ForumProtectedShortcodeRenderResult, error) {
	d.calls += len(requests)
	results := make([]contentregistry.ForumProtectedShortcodeRenderResult, len(requests))
	for index := range requests {
		results[index] = contentregistry.ForumProtectedShortcodeRenderResult{HTML: "<p>TEST_ONLY_HTTP_ALLOWED</p>", Rendered: true}
	}
	return results, nil
}

func TestProtectedShortcodePublicHTTPIsPrivateAndMarkerFree(t *testing.T) {
	const marker = "M8_HTTP_SECRET_MARKER_8C4A9B"
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "protected-test-secret"})
	users := controllerForumActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive},
	}}
	store := &controllerForumStore{protectedSource: `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"public"}]},{"type":"sforumShortcodeBlock","attrs":{"id":"sforum-shortcodes.login","contractVersion":"sforum-shortcodes.login@1","arguments":{}},"content":[{"type":"paragraph","content":[{"type":"text","text":"` + marker + `"}]}]}]}`}
	dispatcher := &controllerProtectedShortcodeDispatcher{}
	controller := NewController(forum.NewService(forum.ServiceConfig{Store: store, Settings: store}), users, manager).
		WithProtectedShortcodes(forum.ProtectedShortcodeAuthorizerFunc(func(_ context.Context, request forum.ProtectedShortcodeAuthorizationRequest) (forum.ProtectedShortcodeDecision, error) {
			return forum.ProtectedShortcodeDecision{Allowed: request.Viewer.ID == 1}, nil
		}), dispatcher)
	loginProvider := forumRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			_, err := manager.Start(c, userID)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "en-US", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})

	paths := []string{
		"/api/v1/topics/10",
		"/api/v1/topics/by-slug/topic",
		"/api/v1/topics/10/comments?view=tree",
		"/api/v1/comments/20/replies",
	}
	for _, path := range paths {
		resp := performForumRequest(t, app, nethttp.MethodGet, path, nil, nil)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != nethttp.StatusOK {
			t.Fatalf("anonymous GET %s status=%d err=%v body=%s", path, resp.StatusCode, err, body)
		}
		if cacheControl := resp.Header.Get("Cache-Control"); cacheControl != "private, no-store" {
			t.Fatalf("anonymous GET %s cache-control=%q", path, cacheControl)
		}
		vary := resp.Header.Values("Vary")
		varyText := strings.Join(vary, ",")
		for _, required := range []string{"Cookie", "Authorization", "Accept-Language"} {
			if !strings.Contains(varyText, required) {
				t.Fatalf("anonymous GET %s vary=%q missing %s", path, varyText, required)
			}
		}
		serialized := string(body)
		for _, forbidden := range []string{marker, "TEST_ONLY_HTTP_ALLOWED", "rawContent", "contentHash", "sourceFormat"} {
			if strings.Contains(serialized, forbidden) {
				t.Fatalf("anonymous GET %s leaked %q: %s", path, forbidden, serialized)
			}
		}
		if !strings.Contains(serialized, "Protected content unavailable") {
			t.Fatalf("anonymous GET %s lacks Host fallback: %s", path, serialized)
		}
	}
	if dispatcher.calls != 0 {
		t.Fatalf("anonymous reads reached protected renderer: %d", dispatcher.calls)
	}

	allowedCookie := loginForumUser(t, app, 1)
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10", nil, allowedCookie)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != nethttp.StatusOK || resp.Header.Get("Cache-Control") != "private, no-store" ||
		!strings.Contains(string(body), "TEST_ONLY_HTTP_ALLOWED") || strings.Contains(string(body), marker) || dispatcher.calls != 1 {
		t.Fatalf("allowed response status=%d cache=%q calls=%d err=%v body=%s", resp.StatusCode, resp.Header.Get("Cache-Control"), dispatcher.calls, err, body)
	}
}

func TestGuestReadLoginRequiredUsesBearerAuthenticationResult(t *testing.T) {
	for _, test := range []struct {
		name       string
		bearer     forumTestBearer
		wantStatus int
	}{
		{
			name: "active PAT allowed",
			bearer: forumTestBearer{auth: apitokens.Authenticated{
				UserID: 1, TokenID: 1, PublicID: "active", Scopes: []string{identity.PermissionPostCreate},
			}},
			wantStatus: nethttp.StatusOK,
		},
		{name: "inactive or deleted PAT rejected", bearer: forumTestBearer{err: apitokens.ErrTokenInvalid}, wantStatus: nethttp.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &controllerForumStore{guestRead: "login_required"}
			controller := NewController(forum.NewService(forum.ServiceConfig{Store: store, Settings: store, Publisher: nil}), controllerForumActors{}, nil)
			app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
				BearerTokens:   test.bearer,
				RouteProviders: []apphttp.RouteProvider{controller},
			})
			req := httptest.NewRequest(nethttp.MethodGet, "/api/v1/categories", nil)
			req.Header.Set("Authorization", "Bearer sft_forum-test")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("expected %d, got %d", test.wantStatus, resp.StatusCode)
			}
		})
	}
}

func TestControllerListsCategoryGroupsAndTags(t *testing.T) {
	app, _, _ := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/category-groups", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 category groups, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var groups forumTestEnvelope[[]forum.CategoryGroup]
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		t.Fatalf("decode category groups: %v", err)
	}
	if len(groups.Data) != 1 || len(groups.Data[0].Categories) != 1 || groups.Data[0].Categories[0].Slug != "general" {
		t.Fatalf("unexpected category groups %#v", groups.Data)
	}

	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/tags", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 tags, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var tags forumTestEnvelope[[]forum.Tag]
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	if len(tags.Data) != 1 || tags.Data[0].Slug != "go" || tags.Data[0].Status != forum.TagStatusActive {
		t.Fatalf("unexpected tags %#v", tags.Data)
	}
}

func TestControllerPassesTagSlugToTopicList(t *testing.T) {
	app, _, store := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics?tagSlug=nuxt", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 topics, got %d", resp.StatusCode)
	}
	if store.lastTopicList.TagSlug != "nuxt" {
		t.Fatalf("expected tagSlug nuxt, got %#v", store.lastTopicList)
	}
}

func TestControllerRequiresLoginAndPermissionToCreateTopic(t *testing.T) {
	app, _, store := newForumTestApp()

	body := []byte(`{"categorySlug":"general","title":"新帖子","content":{"rawContent":"正文","sourceFormat":"markdown","editorType":"markdown"}}`)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", body, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}

	cookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", body, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without topic.create, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var errorBody forumTestEnvelope[forumTestErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&errorBody); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if errorBody.Data.Reason != "permission.denied" {
		t.Fatalf("expected permission.denied, got %q", errorBody.Data.Reason)
	}

	cookie = loginForumUser(t, app, 1)
	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", body, cookie)
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("expected 201 create topic, got %d", resp.StatusCode)
	}
	if store.createdTopic.Title != "新帖子" {
		t.Fatalf("expected store create topic call, got %#v", store.createdTopic)
	}
}

func TestControllerCreateTopicAcceptsTagSlugs(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 1)

	body := []byte(`{"categorySlug":"general","title":"带标签帖子","tagSlugs":["Go","nuxt"],"content":{"rawContent":"正文","sourceFormat":"markdown","editorType":"markdown"}}`)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics", body, cookie)
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("expected 201 create topic, got %d", resp.StatusCode)
	}
	if !stringSlicesEqual(store.createdTopic.TagSlugs, []string{"go", "nuxt"}) {
		t.Fatalf("expected normalized tag slugs on create topic, got %#v", store.createdTopic.TagSlugs)
	}
}

func TestControllerAdminForumPermissions(t *testing.T) {
	app, _, _ := newForumTestApp()
	body := []byte(`{"slug":"support","name":"支持","visibility":"public","position":1}`)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/category-groups", body, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}

	cookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/category-groups", body, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without category.manage, got %d", resp.StatusCode)
	}

	cookie = loginForumUser(t, app, 3)
	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/tags", []byte(`{"slug":"go","name":"Go","status":"active"}`), cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without tag.manage, got %d", resp.StatusCode)
	}
}

// 后台分类/分组列表必须包含隐藏项，否则隐藏项既不可见也不可维护；
// 公开读接口仍只返回公开项。
func TestControllerAdminForumTaxonomyListsHiddenEntries(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 3)

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/category-groups", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 admin category groups, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var groups forumTestEnvelope[[]forum.CategoryGroup]
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		t.Fatalf("decode admin category groups: %v", err)
	}
	if len(groups.Data) != 2 || groups.Data[1].Visibility != "hidden" || len(groups.Data[1].Categories) != 1 {
		t.Fatalf("expected hidden group in admin listing, got %#v", groups.Data)
	}
	if !store.listGroupsIncludeHidden {
		t.Fatal("admin category group listing must request hidden entries")
	}

	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/category-groups", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 public category groups, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var publicGroups forumTestEnvelope[[]forum.CategoryGroup]
	if err := json.NewDecoder(resp.Body).Decode(&publicGroups); err != nil {
		t.Fatalf("decode public category groups: %v", err)
	}
	if len(publicGroups.Data) != 1 || store.listGroupsIncludeHidden {
		t.Fatalf("public listing must stay public-only, got %#v", publicGroups.Data)
	}
}

func TestControllerAdminForumDeleteTaxonomyRequiresPermission(t *testing.T) {
	app, _, store := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/admin/forum/categories/2", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}

	cookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/admin/forum/categories/2", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without category.manage, got %d", resp.StatusCode)
	}
	resp = performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/admin/forum/category-groups/2", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 for group delete without category.manage, got %d", resp.StatusCode)
	}
	if store.deletedCategoryID != 0 || store.deletedGroupID != 0 {
		t.Fatalf("denied requests must not reach the store: category=%d group=%d", store.deletedCategoryID, store.deletedGroupID)
	}
}

func TestControllerAdminForumDeleteTaxonomy(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 3)

	resp := performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/admin/forum/categories/2", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 delete category, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if store.deletedCategoryID != 2 {
		t.Fatalf("expected store delete for category 2, got %d", store.deletedCategoryID)
	}

	resp = performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/admin/forum/category-groups/5", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 delete category group, got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if store.deletedGroupID != 5 {
		t.Fatalf("expected store delete for group 5, got %d", store.deletedGroupID)
	}
}

func TestControllerAdminForumDeleteTaxonomyConflictReasons(t *testing.T) {
	for _, test := range []struct {
		name        string
		path        string
		categoryErr error
		groupErr    error
		wantStatus  int
		wantReason  string
	}{
		{name: "category still has topics", path: "/api/v1/admin/forum/categories/2", categoryErr: forum.ErrCategoryNotEmpty, wantStatus: nethttp.StatusConflict, wantReason: forum.CodeCategoryNotEmpty},
		{name: "category missing", path: "/api/v1/admin/forum/categories/2", categoryErr: forum.ErrCategoryNotFound, wantStatus: nethttp.StatusNotFound, wantReason: forum.CodeCategoryNotFound},
		{name: "group still has categories", path: "/api/v1/admin/forum/category-groups/2", groupErr: forum.ErrCategoryGroupNotEmpty, wantStatus: nethttp.StatusConflict, wantReason: forum.CodeCategoryGroupNotEmpty},
		{name: "group missing", path: "/api/v1/admin/forum/category-groups/2", groupErr: forum.ErrCategoryGroupNotFound, wantStatus: nethttp.StatusNotFound, wantReason: forum.CodeCategoryGroupNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, _, store := newForumTestApp()
			store.deleteCategoryErr = test.categoryErr
			store.deleteGroupErr = test.groupErr
			cookie := loginForumUser(t, app, 3)

			resp := performForumRequest(t, app, nethttp.MethodDelete, test.path, nil, cookie)
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("expected %d, got %d", test.wantStatus, resp.StatusCode)
			}
			defer resp.Body.Close()
			var failure forumTestEnvelope[forumTestErrorData]
			if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
				t.Fatalf("decode failure envelope: %v", err)
			}
			if failure.Data.Reason != test.wantReason {
				t.Fatalf("expected reason %q, got %q", test.wantReason, failure.Data.Reason)
			}
		})
	}
}

// 默认发帖分类被删除会让发帖失去兜底归属，服务端必须在删除前拦截。
func TestControllerAdminForumDeleteDefaultCategoryRejected(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 3)

	resp := performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/admin/forum/categories/1", nil, cookie)
	if resp.StatusCode != nethttp.StatusConflict {
		t.Fatalf("expected 409 for default category delete, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var failure forumTestEnvelope[forumTestErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&failure); err != nil {
		t.Fatalf("decode failure envelope: %v", err)
	}
	if failure.Data.Reason != forum.CodeCategoryIsDefault {
		t.Fatalf("expected reason %q, got %q", forum.CodeCategoryIsDefault, failure.Data.Reason)
	}
	if store.deletedCategoryID != 0 {
		t.Fatalf("default category must not reach the store, got %d", store.deletedCategoryID)
	}
}

func TestControllerAdminForumVisualFields(t *testing.T) {
	app, _, _ := newForumTestApp()
	cookie := loginForumUser(t, app, 4)

	categoryBody := []byte(`{"groupId":1,"slug":"support","name":"支持","visibility":"public","defaultSort":"latest","icon":"i-tabler-help","iconColor":"#0f766e"}`)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/categories", categoryBody, cookie)
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("expected 201 create category, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var categoryOut forumTestEnvelope[forum.Category]
	if err := json.NewDecoder(resp.Body).Decode(&categoryOut); err != nil {
		t.Fatalf("decode category: %v", err)
	}
	if categoryOut.Data.Icon != "i-tabler-help" || categoryOut.Data.IconColor != "#0f766e" {
		t.Fatalf("expected category visual fields in response, got %#v", categoryOut.Data)
	}

	tagBody := []byte(`{"slug":"go","name":"Go","status":"active","icon":"i-lucide-tag","iconColor":"#2563eb"}`)
	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/tags", tagBody, cookie)
	if resp.StatusCode != nethttp.StatusCreated {
		t.Fatalf("expected 201 create tag, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var tagOut forumTestEnvelope[forum.Tag]
	if err := json.NewDecoder(resp.Body).Decode(&tagOut); err != nil {
		t.Fatalf("decode tag: %v", err)
	}
	if tagOut.Data.Icon != "i-lucide-tag" || tagOut.Data.IconColor != "#2563eb" {
		t.Fatalf("expected tag visual fields in response, got %#v", tagOut.Data)
	}
}

func TestControllerAdminSettingsResetWithPermission(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 4)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/settings/reset", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 settings reset, got %d", resp.StatusCode)
	}
	if !store.settingsReset {
		t.Fatal("expected settings reset to reach service manager")
	}
}

func TestControllerAdminSettingsBindsPaginationFields(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 4)
	resp := performForumRequest(t, app, nethttp.MethodPut, "/api/v1/admin/forum/settings", []byte(`{"topicsPerPage":30,"commentsPerPage":40}`), cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 settings update, got %d", resp.StatusCode)
	}
	if store.updatedSettings.TopicsPerPage == nil || *store.updatedSettings.TopicsPerPage != 30 {
		t.Fatalf("topicsPerPage input = %#v", store.updatedSettings.TopicsPerPage)
	}
	if store.updatedSettings.CommentsPerPage == nil || *store.updatedSettings.CommentsPerPage != 40 {
		t.Fatalf("commentsPerPage input = %#v", store.updatedSettings.CommentsPerPage)
	}
}

func TestControllerUpdateTopicRequiresLoginAndPermission(t *testing.T) {
	app, _, _ := newForumTestApp()
	body := []byte(`{"title":"新标题","expectedRevision":1}`)

	// 未登录 -> 401。
	resp := performForumRequest(t, app, nethttp.MethodPatch, "/api/v1/topics/10", body, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}

	// 登录但无编辑权限 -> 403。
	cookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodPatch, "/api/v1/topics/10", body, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without edit permission, got %d", resp.StatusCode)
	}
}

func TestControllerUpdateTopicAllowsModerator(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 5) // 版主，含 topic.edit_any。

	body := []byte(`{"title":"版主编辑标题","categorySlug":"general","tagSlugs":["go"],"expectedRevision":1,"reason":"moderation correction"}`)
	resp := performForumRequest(t, app, nethttp.MethodPatch, "/api/v1/topics/10", body, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 update topic, got %d", resp.StatusCode)
	}
	if store.updatedTopic.Title != "版主编辑标题" {
		t.Fatalf("expected title update, got %#v", store.updatedTopic)
	}
}

func TestControllerDeleteTopicRequiresPermission(t *testing.T) {
	app, _, store := newForumTestApp()

	// 未登录 -> 401。
	resp := performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/topics/10", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without session, got %d", resp.StatusCode)
	}

	// 无权限 -> 403。
	cookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/topics/10", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without delete permission, got %d", resp.StatusCode)
	}

	// 版主 -> 200。
	cookie = loginForumUser(t, app, 5)
	resp = performForumRequest(t, app, nethttp.MethodDelete, "/api/v1/topics/10", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 delete topic, got %d", resp.StatusCode)
	}
	if store.deletedTopicID != 10 {
		t.Fatalf("expected deleted topic id 10, got %d", store.deletedTopicID)
	}
}

func TestControllerTopicActionRequiresPermission(t *testing.T) {
	actions := []string{"hide", "restore", "lock", "unlock", "pin", "unpin"}

	for _, action := range actions {
		t.Run(action+"/denied", func(t *testing.T) {
			app, _, _ := newForumTestApp()
			// 无权限用户 -> 403。
			cookie := loginForumUser(t, app, 2)
			resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/"+action, nil, cookie)
			if resp.StatusCode != nethttp.StatusForbidden {
				t.Fatalf("expected 403 for %s without permission, got %d", action, resp.StatusCode)
			}
		})
		t.Run(action+"/unauthenticated", func(t *testing.T) {
			app, _, _ := newForumTestApp()
			resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/"+action, nil, nil)
			if resp.StatusCode != nethttp.StatusUnauthorized {
				t.Fatalf("expected 401 for %s without session, got %d", action, resp.StatusCode)
			}
		})
	}
}

func TestControllerTopicActionAllowsModerator(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 5)

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/lock", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 lock, got %d", resp.StatusCode)
	}
	if store.appliedAction != forum.TopicActionLock {
		t.Fatalf("expected applied action lock, got %s", store.appliedAction)
	}

	resp = performForumRequest(t, app, nethttp.MethodPost, "/api/v1/topics/10/pin", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 pin, got %d", resp.StatusCode)
	}
	if store.appliedAction != forum.TopicActionPin {
		t.Fatalf("expected applied action pin, got %s", store.appliedAction)
	}
}

func TestControllerPassesTreeAndFlatCommentViews(t *testing.T) {
	app, _, store := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments?view=tree", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 tree comments, got %d", resp.StatusCode)
	}
	if store.lastCommentView != "tree" {
		t.Fatalf("expected tree view, got %q", store.lastCommentView)
	}
	defer resp.Body.Close()
	var treeBody forumTestEnvelope[forum.CommentList]
	if err := json.NewDecoder(resp.Body).Decode(&treeBody); err != nil {
		t.Fatalf("decode tree comments: %v", err)
	}
	if len(treeBody.Data.Items) != 1 || len(treeBody.Data.Items[0].Children) != 1 {
		t.Fatalf("expected nested tree response, got %#v", treeBody.Data.Items)
	}

	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments?view=flat", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 flat comments, got %d", resp.StatusCode)
	}
	if store.lastCommentView != "flat" {
		t.Fatalf("expected flat view, got %q", store.lastCommentView)
	}
}

// TestControllerCommentPageResolve 验证评论页码反查接口的公开可访问性与响应结构。
func TestControllerCommentPageResolve(t *testing.T) {
	app, _, store := newForumTestApp()

	// 默认 before=0 → page 1（mock GetCommentSummary 返回 topic 10 / active / id 20）。
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments/20/page", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 for comment page resolve, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var body forumTestEnvelope[struct {
		Page    int `json:"page"`
		PerPage int `json:"perPage"`
	}]
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode comment page response: %v", err)
	}
	if body.Data.Page != 1 || body.Data.PerPage != 20 {
		t.Fatalf("expected page=1 perPage=20, got page=%d perPage=%d", body.Data.Page, body.Data.PerPage)
	}

	// 跨页：before=20 → page 2。
	store.countCommentsBefore = 20
	resp2 := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/comments/20/page", nil, nil)
	if resp2.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 for cross-page resolve, got %d", resp2.StatusCode)
	}
	defer resp2.Body.Close()
	var body2 forumTestEnvelope[struct {
		Page    int `json:"page"`
		PerPage int `json:"perPage"`
	}]
	if err := json.NewDecoder(resp2.Body).Decode(&body2); err != nil {
		t.Fatalf("decode comment page response: %v", err)
	}
	if body2.Data.Page != 2 {
		t.Fatalf("expected page=2 when 20 comments before, got %d", body2.Data.Page)
	}
}

func TestControllerRevisionHistoryPermissionsAndPayloads(t *testing.T) {
	app, _, store := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/revisions", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}

	cookie := loginForumUser(t, app, 2)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/revisions", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without history permission, got %d", resp.StatusCode)
	}
	if store.topicRevisionCalls != 0 {
		t.Fatal("permission-denied history request should not reach store")
	}

	cookie = loginForumUser(t, app, 6)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/revisions?perPage=200", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 revision list, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var listBody forumTestEnvelope[forum.RevisionList]
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode revision list: %v", err)
	}
	if listBody.Data.PerPage != 100 || len(listBody.Data.Items) != 1 {
		t.Fatalf("unexpected list response %#v", listBody.Data)
	}
	rawList, err := json.Marshal(listBody.Data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawList, []byte("rawContent")) || bytes.Contains(rawList, []byte("legacy source")) {
		t.Fatalf("revision list leaked raw source: %s", rawList)
	}

	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics/10/revisions/1", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 revision detail, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var detailBody forumTestEnvelope[forum.ForumRevisionDetail]
	if err := json.NewDecoder(resp.Body).Decode(&detailBody); err != nil {
		t.Fatalf("decode revision detail: %v", err)
	}
	if detailBody.Data.RawContent != "legacy source" || detailBody.Data.Preview == nil || detailBody.Data.Preview.HTMLContent == "" {
		t.Fatalf("expected authorized raw source and preview, got %#v", detailBody.Data)
	}
}

func TestControllerCommentRevisionPermissionAndRedactedErrors(t *testing.T) {
	app, _, store := newForumTestApp()
	cookie := loginForumUser(t, app, 6)

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/revisions", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 comment revision list, got %d", resp.StatusCode)
	}

	store.revisionErr = forum.ErrRevisionNotFound
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/revisions/999", nil, cookie)
	if resp.StatusCode != nethttp.StatusNotFound {
		t.Fatalf("expected 404 missing revision, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var missing forumTestEnvelope[forumTestErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&missing); err != nil {
		t.Fatalf("decode missing error: %v", err)
	}
	if missing.Data.Reason != forum.CodeRevisionNotFound {
		t.Fatalf("expected %s, got %q", forum.CodeRevisionNotFound, missing.Data.Reason)
	}

	store.revisionErr = forum.ErrRevisionRedacted
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/comments/20/revisions/1", nil, cookie)
	if resp.StatusCode != nethttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422 redacted revision, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var redacted forumTestEnvelope[forumTestErrorData]
	if err := json.NewDecoder(resp.Body).Decode(&redacted); err != nil {
		t.Fatalf("decode redacted error: %v", err)
	}
	if redacted.Data.Reason != forum.CodeRevisionRedacted {
		t.Fatalf("expected %s, got %q", forum.CodeRevisionRedacted, redacted.Data.Reason)
	}
}

func TestControllerAdminContentReadPermissions(t *testing.T) {
	app, _, store := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/topics", nil, nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}

	cookie := loginForumUser(t, app, 6)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/topics", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without admin.access, got %d", resp.StatusCode)
	}
	if store.adminTopicCalls != 0 {
		t.Fatal("admin.access denial should not reach admin topic store")
	}

	cookie = loginForumUser(t, app, 8)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/comments", nil, cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without edit/history permission, got %d", resp.StatusCode)
	}

	cookie = loginForumUser(t, app, 7)
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/topics?titlePrefix=公", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 admin topic list, got %d", resp.StatusCode)
	}
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/topics/10", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 admin topic detail, got %d", resp.StatusCode)
	}
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/comments", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 admin comment list, got %d", resp.StatusCode)
	}
	resp = performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/content/comments/20", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 admin comment detail, got %d", resp.StatusCode)
	}
	if store.adminTopicCalls == 0 || store.adminCommentCalls == 0 {
		t.Fatalf("expected admin content stores to be called, topics=%d comments=%d", store.adminTopicCalls, store.adminCommentCalls)
	}
}

func newForumTestApp() (*fiber.App, *authsession.Manager, *controllerForumStore) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerForumActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionTopicCreate:    true,
			identity.PermissionTopicEditOwn:   true,
			identity.PermissionTopicDeleteOwn: true,
			identity.PermissionPostCreate:     true,
			identity.PermissionPostEditOwn:    true,
			identity.PermissionPostDeleteOwn:  true,
		}},
		2: {ID: 2, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
		3: {ID: 3, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionCategoryManage: true,
		}},
		4: {ID: 4, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionCategoryManage:      true,
			identity.PermissionTagManage:           true,
			identity.PermissionForumSettingsManage: true,
		}},
		5: {ID: 5, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionTopicEditAny:   true,
			identity.PermissionTopicDeleteAny: true,
			identity.PermissionPostEditAny:    true,
			identity.PermissionTopicLock:      true,
			identity.PermissionTopicPin:       true,
		}},
		6: {ID: 6, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionTopicRevisionViewAny: true,
			identity.PermissionPostRevisionViewAny:  true,
		}},
		7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionAdminAccess:          true,
			identity.PermissionTopicRevisionViewAny: true,
			identity.PermissionPostRevisionViewAny:  true,
		}},
		8: {ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{
			identity.PermissionAdminAccess: true,
		}},
	}}
	store := &controllerForumStore{}
	controller := NewController(forum.NewService(forum.ServiceConfig{Store: store, Settings: store, Publisher: nil}), users, manager)
	loginProvider := forumRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID, err := strconv.ParseInt(c.Params("id"), 10, 64)
			if err != nil || userID == 0 {
				userID = 1
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

func loginForumUser(t *testing.T, app *fiber.App, userID int64) *nethttp.Cookie {
	t.Helper()
	req := httptest.NewRequest(nethttp.MethodPost, "/api/v1/test-login/"+strconv.FormatInt(userID, 10), nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected login 200, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) == 0 {
		t.Fatal("expected login cookie")
	}
	return resp.Cookies()[0]
}

func performForumRequest(t *testing.T, app *fiber.App, method string, path string, body []byte, cookie *nethttp.Cookie) *nethttp.Response {
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

type controllerForumActors struct {
	actors map[int64]identity.Actor
}

func (s controllerForumActors) LoadActor(_ context.Context, userID int64) (identity.Actor, error) {
	return s.actors[userID], nil
}

type forumRouteProviderFunc func(api fiber.Router)

func (f forumRouteProviderFunc) RegisterRoutes(api fiber.Router) {
	f(api)
}

type controllerForumStore struct {
	createdTopic    forum.CreateTopicRecord
	updatedTopic    forum.UpdateTopicRecord
	deletedTopicID  int64
	appliedAction   string
	actionTopic     forum.TopicSummary
	lastCommentView string
	lastTopicList   forum.TopicListInput
	// countCommentsBefore 可注入 ResolveCommentPage 的"排在前面的评论数"，模拟跨页定位。
	countCommentsBefore  int64
	settingsReset        bool
	updatedSettings      forum.UpdateForumSettingsInput
	guestRead            string
	revisionErr          error
	topicRevisionCalls   int
	commentRevisionCalls int
	adminTopicCalls      int
	adminCommentCalls    int
	topicEditSource      forum.EditableContentSource
	commentEditSource    forum.EditableContentSource
	referenceInput       forum.ReferenceSelectorInput
	protectedSource      string
	// 分类/分组列表的公开 vs 后台口径记录，以及删除路径的调用记录与可注入错误。
	listCategoriesIncludeHidden bool
	listGroupsIncludeHidden     bool
	deletedCategoryID           int64
	deletedGroupID              int64
	deleteCategoryErr           error
	deleteGroupErr              error
}

func (s *controllerForumStore) LoadPublicShortcodeSources(_ context.Context, resources []contentregistry.ShortcodeResourceKey) (map[contentregistry.ShortcodeResourceKey]forum.PublicShortcodeSource, error) {
	result := make(map[contentregistry.ShortcodeResourceKey]forum.PublicShortcodeSource, len(resources))
	if strings.TrimSpace(s.protectedSource) == "" {
		return result, nil
	}
	for _, resource := range resources {
		result[resource] = forum.PublicShortcodeSource{Resource: resource, RawContent: s.protectedSource, SourceFormat: forum.SourceFormatEditorDocument}
	}
	return result, nil
}

func (s *controllerForumStore) ListReferenceOptions(_ context.Context, input forum.ReferenceSelectorInput) ([]forum.ReferenceOption, error) {
	s.referenceInput = input
	return []forum.ReferenceOption{{ID: 10, Label: "Release topic", SecondaryLabel: "General"}}, nil
}

func (s *controllerForumStore) ListCategories(_ context.Context, includeHidden bool) ([]forum.Category, error) {
	s.listCategoriesIncludeHidden = includeHidden
	items := []forum.Category{{ID: 1, Slug: "general", Name: "综合讨论", Visibility: "public"}}
	if includeHidden {
		items = append(items, forum.Category{ID: 2, Slug: "internal", Name: "内部版块", Visibility: "hidden"})
	}
	return items, nil
}

func (s *controllerForumStore) ListCategoryGroups(_ context.Context, includeHidden bool) ([]forum.CategoryGroup, error) {
	s.listGroupsIncludeHidden = includeHidden
	groups := []forum.CategoryGroup{{
		ID:         1,
		Slug:       "default",
		Name:       "默认版块",
		Visibility: "public",
		Categories: []forum.Category{{ID: 1, Slug: "general", Name: "综合讨论", Visibility: "public"}},
	}}
	if includeHidden {
		groups = append(groups, forum.CategoryGroup{
			ID:         2,
			Slug:       "internal",
			Name:       "内部版块",
			Visibility: "hidden",
			Categories: []forum.Category{{ID: 2, Slug: "internal", Name: "内部版块", Visibility: "hidden"}},
		})
	}
	return groups, nil
}

func (s *controllerForumStore) DeleteCategoryGroup(_ context.Context, groupID int64) (forum.CategoryGroup, error) {
	s.deletedGroupID = groupID
	if s.deleteGroupErr != nil {
		return forum.CategoryGroup{}, s.deleteGroupErr
	}
	return forum.CategoryGroup{ID: groupID, Slug: "default", Name: "默认版块", Visibility: "public"}, nil
}

func (s *controllerForumStore) DeleteCategory(_ context.Context, categoryID int64) (forum.Category, error) {
	s.deletedCategoryID = categoryID
	if s.deleteCategoryErr != nil {
		return forum.Category{}, s.deleteCategoryErr
	}
	return forum.Category{ID: categoryID, GroupID: 1, Slug: "general", Name: "综合讨论", Visibility: "public"}, nil
}

func (s *controllerForumStore) ListTags(_ context.Context, includePending bool) ([]forum.Tag, error) {
	if includePending {
		return []forum.Tag{
			{ID: 1, Slug: "go", Name: "Go", Status: forum.TagStatusActive},
			{ID: 2, Slug: "pending", Name: "Pending", Status: forum.TagStatusPending},
		}, nil
	}
	return []forum.Tag{{ID: 1, Slug: "go", Name: "Go", Status: forum.TagStatusActive}}, nil
}

func (s *controllerForumStore) CreateCategoryGroup(_ context.Context, input forum.CreateCategoryGroupInput) (forum.CategoryGroup, error) {
	return forum.CategoryGroup{ID: 2, Slug: input.Slug, Name: input.Name, Description: input.Description, Visibility: input.Visibility, Position: input.Position}, nil
}

func (s *controllerForumStore) UpdateCategoryGroup(_ context.Context, input forum.UpdateCategoryGroupInput) (forum.CategoryGroup, error) {
	item := forum.CategoryGroup{ID: input.ID, Slug: "default", Name: "默认版块", Visibility: "public"}
	if input.Slug != nil {
		item.Slug = *input.Slug
	}
	if input.Name != nil {
		item.Name = *input.Name
	}
	return item, nil
}

func (s *controllerForumStore) CreateCategory(_ context.Context, input forum.CreateCategoryInput) (forum.Category, error) {
	return forum.Category{ID: 2, GroupID: input.GroupID, Slug: input.Slug, Name: input.Name, Description: input.Description, Icon: input.Icon, IconColor: input.IconColor, Visibility: input.Visibility, Position: input.Position, DefaultSort: input.DefaultSort}, nil
}

func (s *controllerForumStore) UpdateCategory(_ context.Context, input forum.UpdateCategoryInput) (forum.Category, error) {
	item := forum.Category{ID: input.ID, GroupID: 1, Slug: "general", Name: "综合讨论", Visibility: "public", DefaultSort: "latest"}
	if input.Slug != nil {
		item.Slug = *input.Slug
	}
	if input.Name != nil {
		item.Name = *input.Name
	}
	if input.Icon != nil {
		item.Icon = *input.Icon
	}
	if input.IconColor != nil {
		item.IconColor = *input.IconColor
	}
	return item, nil
}

func (s *controllerForumStore) CreateTag(_ context.Context, input forum.CreateTagInput) (forum.Tag, error) {
	return forum.Tag{ID: 2, Slug: input.Slug, Name: input.Name, Description: input.Description, Icon: input.Icon, IconColor: input.IconColor, Status: input.Status}, nil
}

func (s *controllerForumStore) UpdateTag(_ context.Context, input forum.UpdateTagInput) (forum.Tag, error) {
	item := forum.Tag{ID: input.ID, Slug: "go", Name: "Go", Status: forum.TagStatusActive}
	if input.Slug != nil {
		item.Slug = *input.Slug
	}
	if input.Name != nil {
		item.Name = *input.Name
	}
	if input.Icon != nil {
		item.Icon = *input.Icon
	}
	if input.IconColor != nil {
		item.IconColor = *input.IconColor
	}
	if input.Status != nil {
		item.Status = *input.Status
	}
	return item, nil
}

func (s *controllerForumStore) ListTopics(_ context.Context, input forum.TopicListInput) (forum.TopicList, error) {
	s.lastTopicList = input
	return forum.TopicList{Items: []forum.TopicSummary{{ID: 10, Title: "公开帖子", Slug: "topic", Status: forum.TopicStatusActive}}, Total: 1, Page: input.Page, PerPage: input.PerPage}, nil
}

func (s *controllerForumStore) ListAllTopicIDs(context.Context) ([]int64, error) {
	return []int64{10}, nil
}

func (s *controllerForumStore) GetTopic(context.Context, int64) (forum.TopicDetail, error) {
	content := forum.PublicRenderedContent{}
	if s.protectedSource != "" {
		content = forum.PublicRenderedContent{HTMLContent: "<p>stored public fallback</p>", PlainText: "public Protected content unavailable", Excerpt: "public Protected content unavailable", RenderVersion: forum.RenderVersionEditorDocument}
	}
	return forum.TopicDetail{TopicSummary: forum.TopicSummary{ID: 10, Title: "公开帖子", Slug: "topic", Status: forum.TopicStatusActive}, Content: content}, nil
}

func (s *controllerForumStore) GetTopicBySlug(context.Context, string) (forum.TopicDetail, error) {
	return s.GetTopic(context.Background(), 10)
}

func (s *controllerForumStore) TopicSlugExists(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (s *controllerForumStore) ActiveTopicTitleExists(context.Context, string, int64) (bool, error) {
	return false, nil
}

func (s *controllerForumStore) AutoLockIdleTopics(context.Context, int, int) (int, error) {
	return 0, nil
}

func (s *controllerForumStore) CreateTopic(_ context.Context, input forum.CreateTopicRecord) (forum.TopicDetail, error) {
	s.createdTopic = input
	input.Content.ID = 100
	return forum.TopicDetail{
		TopicSummary: forum.TopicSummary{ID: 10, AuthorUserID: input.AuthorUserID, Title: input.Title, Slug: input.Slug, Status: forum.TopicStatusActive},
		Content:      forum.ToPublicRenderedContent(input.Content),
	}, nil
}

func (s *controllerForumStore) UpdateTopic(_ context.Context, input forum.UpdateTopicRecord) (forum.TopicDetail, error) {
	s.updatedTopic = input
	title := input.Title
	if title == "" {
		title = "公开帖子"
	}
	return forum.TopicDetail{TopicSummary: forum.TopicSummary{ID: input.TopicID, Title: title, Slug: "topic", Status: forum.TopicStatusActive, CurrentRevision: input.ExpectedRevision + 1}, UpdateApplied: true}, nil
}

func (s *controllerForumStore) DeleteTopic(_ context.Context, topicID int64) (forum.TopicDetail, error) {
	s.deletedTopicID = topicID
	return forum.TopicDetail{TopicSummary: forum.TopicSummary{ID: topicID, Status: forum.TopicStatusDeleted}}, nil
}

func (s *controllerForumStore) ApplyTopicAction(_ context.Context, input forum.TopicLifecycleInput) (forum.TopicLifecycleRecord, error) {
	s.appliedAction = input.Action
	return forum.TopicLifecycleRecord{TopicID: input.TopicID, Status: forum.TopicStatusActive, IsPinned: input.Action == forum.TopicActionPin}, nil
}

func (s *controllerForumStore) ResolveTopicTags(_ context.Context, input forum.ResolveTopicTagsInput) ([]forum.TopicTagSummary, error) {
	items := make([]forum.TopicTagSummary, 0, len(input.Slugs))
	for index, slug := range input.Slugs {
		items = append(items, forum.TopicTagSummary{ID: int64(index + 1), Slug: slug, Name: slug, Status: forum.TagStatusActive})
	}
	return items, nil
}

func (s *controllerForumStore) GetTopicForComment(context.Context, int64) (forum.TopicSummary, error) {
	return forum.TopicSummary{ID: 10, Status: forum.TopicStatusActive}, nil
}

func (s *controllerForumStore) GetTopicForAction(context.Context, int64) (forum.TopicSummary, error) {
	if s.actionTopic.ID == 0 {
		return forum.TopicSummary{ID: 10, AuthorUserID: 1, Status: forum.TopicStatusActive, CurrentRevision: 1}, nil
	}
	return s.actionTopic, nil
}

func (s *controllerForumStore) GetTopicEditSource(context.Context, int64) (forum.EditableContentSource, error) {
	return s.topicEditSource, nil
}

func (s *controllerForumStore) CreateComment(_ context.Context, input forum.CreateCommentRecord) (forum.Comment, error) {
	return forum.Comment{ID: 20, TopicID: input.TopicID, AuthorUserID: input.AuthorUserID, Content: forum.ToPublicRenderedContent(input.Content), Status: forum.CommentStatusActive}, nil
}

func (s *controllerForumStore) GetCommentSummary(context.Context, int64) (forum.CommentSummary, error) {
	return forum.CommentSummary{ID: 20, TopicID: 10, AuthorUserID: 1, Status: forum.CommentStatusActive, CurrentRevision: 1, CreatedAt: time.Now().UTC()}, nil
}

func (s *controllerForumStore) GetCommentEditSource(context.Context, int64) (forum.EditableContentSource, error) {
	return s.commentEditSource, nil
}

func (s *controllerForumStore) CountCommentsBefore(context.Context, int64, time.Time, int64, bool, int64) (int64, error) {
	return s.countCommentsBefore, nil
}

func (s *controllerForumStore) UpdateComment(_ context.Context, input forum.UpdateCommentRecord) (forum.Comment, error) {
	return forum.Comment{ID: input.CommentID, AuthorUserID: 1, Content: forum.ToPublicRenderedContent(input.Content), Status: forum.CommentStatusActive, CurrentRevision: input.ExpectedRevision + 1, UpdateApplied: true}, nil
}

func (s *controllerForumStore) DeleteComment(context.Context, int64) (forum.Comment, error) {
	return forum.Comment{ID: 20, Status: forum.CommentStatusDeleted}, nil
}

func (s *controllerForumStore) ListComments(_ context.Context, input forum.CommentListInput) (forum.CommentList, error) {
	s.lastCommentView = input.View
	childParent := int64(20)
	renderVersion := ""
	if s.protectedSource != "" {
		renderVersion = forum.RenderVersionEditorDocument
	}
	root := forum.Comment{ID: 20, TopicID: input.TopicID, Status: forum.CommentStatusActive, Content: forum.PublicRenderedContent{ID: 1, HTMLContent: "<p>root</p>", RenderVersion: renderVersion}}
	child := forum.Comment{ID: 21, TopicID: input.TopicID, ParentID: &childParent, Status: forum.CommentStatusActive, Content: forum.PublicRenderedContent{ID: 2, HTMLContent: "<p>child</p>", RenderVersion: renderVersion}}
	items := []forum.Comment{root, child}
	if input.View == "tree" {
		root.Children = []forum.Comment{child}
		items = []forum.Comment{root}
	}
	return forum.CommentList{Items: items, Total: int64(len(items)), Page: input.Page, PerPage: input.PerPage, View: input.View}, nil
}

func (s *controllerForumStore) ListCommentReplies(context.Context, forum.CommentReplyListInput) ([]forum.Comment, error) {
	renderVersion := ""
	if s.protectedSource != "" {
		renderVersion = forum.RenderVersionEditorDocument
	}
	return []forum.Comment{{ID: 21, TopicID: 10, Status: forum.CommentStatusActive, Content: forum.PublicRenderedContent{RenderVersion: renderVersion}}}, nil
}

func (s *controllerForumStore) ListTopicRevisions(_ context.Context, _ int64, input forum.RevisionListInput) (forum.RevisionList, error) {
	s.topicRevisionCalls++
	perPage := input.PerPage
	if perPage > 100 {
		perPage = 100
	}
	return forum.RevisionList{
		Items: []forum.ForumRevisionSummary{{
			ID:               101,
			RevisionNo:       1,
			Current:          true,
			Operation:        "migration",
			Origin:           "migration",
			ChangedFields:    []string{"content"},
			CommittedAt:      time.Now(),
			SnapshotComplete: false,
			RestorableFields: []string{"content"},
		}},
		PerPage: perPage,
	}, nil
}

func (s *controllerForumStore) ListTopicContributionTimeline(_ context.Context, _ int64, input forum.RevisionListInput) (forum.TopicContributionTimeline, error) {
	perPage := input.PerPage
	if perPage <= 0 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}
	return forum.TopicContributionTimeline{
		Items: []forum.TopicContributionEvent{{
			RevisionNo:    1,
			Current:       true,
			Operation:     "create",
			Origin:        "self",
			ChangedFields: []string{"content"},
			CommittedAt:   time.Now(),
		}},
		PerPage: perPage,
	}, nil
}

func (s *controllerForumStore) GetTopicRevision(context.Context, int64, int64) (forum.ForumRevisionDetail, error) {
	s.topicRevisionCalls++
	if s.revisionErr != nil {
		return forum.ForumRevisionDetail{}, s.revisionErr
	}
	detail := forum.ForumRevisionDetail{
		ForumRevisionSummary: forum.ForumRevisionSummary{
			ID:               101,
			RevisionNo:       1,
			Current:          true,
			Operation:        "migration",
			Origin:           "migration",
			ChangedFields:    []string{"content"},
			CommittedAt:      time.Now(),
			SnapshotComplete: false,
			RestorableFields: []string{"content"},
		},
		RawContent:    "legacy source",
		SourceFormat:  forum.SourceFormatMarkdown,
		EditorType:    forum.EditorTypeMarkdown,
		EditorVersion: "test",
		RenderVersion: forum.RenderVersion,
		ContentHash:   "hash",
	}
	return detail, nil
}

func (s *controllerForumStore) ListCommentRevisions(_ context.Context, _ int64, input forum.RevisionListInput) (forum.RevisionList, error) {
	s.commentRevisionCalls++
	perPage := input.PerPage
	if perPage > 100 {
		perPage = 100
	}
	return forum.RevisionList{
		Items: []forum.ForumRevisionSummary{{
			ID:               201,
			RevisionNo:       1,
			Current:          true,
			Operation:        "create",
			Origin:           "self",
			ChangedFields:    []string{"content"},
			CommittedAt:      time.Now(),
			SnapshotComplete: true,
			RestorableFields: []string{"attachments", "content"},
		}},
		PerPage: perPage,
	}, nil
}

func (s *controllerForumStore) GetCommentRevision(context.Context, int64, int64) (forum.ForumRevisionDetail, error) {
	s.commentRevisionCalls++
	if s.revisionErr != nil {
		return forum.ForumRevisionDetail{}, s.revisionErr
	}
	return forum.ForumRevisionDetail{
		ForumRevisionSummary: forum.ForumRevisionSummary{
			ID:               201,
			RevisionNo:       1,
			Current:          true,
			Operation:        "create",
			Origin:           "self",
			ChangedFields:    []string{"content"},
			CommittedAt:      time.Now(),
			SnapshotComplete: true,
			RestorableFields: []string{"attachments", "content"},
		},
		RawContent:    "comment source",
		SourceFormat:  forum.SourceFormatMarkdown,
		EditorType:    forum.EditorTypeMarkdown,
		EditorVersion: "test",
		RenderVersion: forum.RenderVersion,
		ContentHash:   "hash",
	}, nil
}

func (s *controllerForumStore) RedactTopicRevision(context.Context, forum.RevisionRedactionRecord) error {
	return nil
}

func (s *controllerForumStore) RedactCommentRevision(context.Context, forum.RevisionRedactionRecord) error {
	return nil
}

func (s *controllerForumStore) ListAdminForumTopics(context.Context, forum.AdminForumContentListInput) (forum.AdminForumContentList, error) {
	s.adminTopicCalls++
	return forum.AdminForumContentList{Items: []forum.AdminForumContentRow{{
		TargetType:      "topic",
		ID:              10,
		TopicID:         10,
		TopicTitle:      "公开帖子",
		CategorySlug:    "general",
		AuthorUserID:    1,
		Status:          forum.TopicStatusActive,
		Title:           "公开帖子",
		Excerpt:         "摘要",
		CurrentRevision: 1,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}}, PerPage: 20}, nil
}

func (s *controllerForumStore) GetAdminForumTopic(context.Context, int64) (forum.AdminForumTopicDetail, error) {
	s.adminTopicCalls++
	return forum.AdminForumTopicDetail{
		AdminForumContentRow: forum.AdminForumContentRow{
			TargetType:      "topic",
			ID:              10,
			TopicID:         10,
			TopicTitle:      "公开帖子",
			CategorySlug:    "general",
			AuthorUserID:    1,
			Status:          forum.TopicStatusActive,
			Title:           "公开帖子",
			Excerpt:         "摘要",
			CurrentRevision: 1,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		},
		Content: forum.RenderedContent{RawContent: "body", HTMLContent: "<p>body</p>", PlainText: "body", SourceFormat: forum.SourceFormatMarkdown},
		Slug:    "topic",
	}, nil
}

func (s *controllerForumStore) ListAdminForumComments(context.Context, forum.AdminForumContentListInput) (forum.AdminForumContentList, error) {
	s.adminCommentCalls++
	return forum.AdminForumContentList{Items: []forum.AdminForumContentRow{{
		TargetType:      "comment",
		ID:              20,
		TopicID:         10,
		TopicTitle:      "公开帖子",
		CategorySlug:    "general",
		AuthorUserID:    1,
		Status:          forum.CommentStatusActive,
		Excerpt:         "评论摘要",
		CurrentRevision: 1,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}}, PerPage: 20}, nil
}

func (s *controllerForumStore) GetAdminForumComment(context.Context, int64) (forum.AdminForumCommentDetail, error) {
	s.adminCommentCalls++
	return forum.AdminForumCommentDetail{
		AdminForumContentRow: forum.AdminForumContentRow{
			TargetType:      "comment",
			ID:              20,
			TopicID:         10,
			TopicTitle:      "公开帖子",
			CategorySlug:    "general",
			AuthorUserID:    1,
			Status:          forum.CommentStatusActive,
			Excerpt:         "评论摘要",
			CurrentRevision: 1,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		},
		Content:       forum.RenderedContent{RawContent: "comment", HTMLContent: "<p>comment</p>", PlainText: "comment", SourceFormat: forum.SourceFormatMarkdown},
		RootCommentID: 20,
		PathKey:       "000000000020",
	}, nil
}

func (s *controllerForumStore) LatestAuthorTopicCreatedAt(context.Context, int64) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *controllerForumStore) CountAuthorTopicsSince(context.Context, int64, time.Time) (int64, error) {
	return 0, nil
}
func (s *controllerForumStore) LatestAuthorCommentCreatedAt(context.Context, int64) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *controllerForumStore) CountAuthorCommentsSince(context.Context, int64, time.Time) (int64, error) {
	return 0, nil
}

func (s *controllerForumStore) ForumSettings(context.Context) (forum.ForumSettings, error) {
	return forum.ForumSettings{
		DefaultCategorySlug:    "general",
		TagCreationMode:        forum.TagCreationModeControlled,
		TagPublicPages:         true,
		TagMinPerTopic:         0,
		TagMaxPerTopic:         5,
		TopicsPerPage:          20,
		CommentsPerPage:        20,
		TopicTitleMinRunes:     2,
		TopicTitleMaxRunes:     100,
		TopicContentMinRunes:   0,
		TopicContentMaxRunes:   50000,
		CommentMinRunes:        1,
		CommentMaxRunes:        10000,
		CommentMaxNestingDepth: 5,
		TreeDescendantsPerRoot: 50,
		ExcerptRuneLimit:       180,
		GuestRead:              s.guestRead,
	}, nil
}

type forumTestBearer struct {
	auth apitokens.Authenticated
	err  error
}

func (b forumTestBearer) AuthenticatePlaintext(fiber.Ctx, string) (apitokens.Authenticated, error) {
	return b.auth, b.err
}

func (s *controllerForumStore) UpdateForumSettings(_ context.Context, _ identity.Actor, input forum.UpdateForumSettingsInput) (forum.ForumSettings, error) {
	s.updatedSettings = input
	settings, _ := s.ForumSettings(context.Background())
	if input.DefaultCategorySlug != nil {
		settings.DefaultCategorySlug = *input.DefaultCategorySlug
	}
	if input.TagCreationMode != nil {
		settings.TagCreationMode = *input.TagCreationMode
	}
	if input.TagPublicPages != nil {
		settings.TagPublicPages = *input.TagPublicPages
	}
	if input.TagMaxPerTopic != nil {
		settings.TagMaxPerTopic = *input.TagMaxPerTopic
	}
	if input.TopicsPerPage != nil {
		settings.TopicsPerPage = *input.TopicsPerPage
	}
	if input.CommentsPerPage != nil {
		settings.CommentsPerPage = *input.CommentsPerPage
	}
	return settings, nil
}

func (s *controllerForumStore) ResetForumSettings(context.Context, identity.Actor) (forum.ForumSettings, error) {
	s.settingsReset = true
	return s.ForumSettings(context.Background())
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ = time.Time{}

// --- 搜索端点测试 ---

// fakeSearchService 记录搜索请求并返回预设结果。
type fakeSearchService struct {
	lastInput SearchInput
	result    SearchOutput
	err       error
}

func (f *fakeSearchService) Search(_ context.Context, input SearchInput) (SearchOutput, error) {
	f.lastInput = input
	return f.result, f.err
}

// newForumTestAppWithSearch 构造带 search service 的测试 app。
func newForumTestAppWithSearch(searchSvc SearchService) (*fiber.App, *controllerForumStore) {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerForumActors{actors: map[int64]identity.Actor{
		1: {ID: 1, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
	}}
	store := &controllerForumStore{}
	controller := NewControllerWithSearch(forum.NewService(forum.ServiceConfig{Store: store, Settings: store, Publisher: nil}), searchSvc, nil, users, manager)
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app, store
}

func TestControllerSearchEndpoint(t *testing.T) {
	svc := &fakeSearchService{result: SearchOutput{
		Items: []forum.TopicSummary{{
			ID: 1, Title: "Go 指南", Slug: "go-guide", Status: "active",
			CategoryID: 1, CategorySlug: "general", CategoryName: "综合讨论",
			AuthorUserID: 1, Excerpt: "hello",
		}},
		Total:   1,
		Page:    1,
		PerPage: 20,
	}}
	app, _ := newForumTestAppWithSearch(svc)

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/search?query=go", nil, nil)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200 search, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var out forumTestEnvelope[SearchOutput]
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(out.Data.Items) != 1 || out.Data.Items[0].Title != "Go 指南" {
		t.Fatalf("unexpected search items %#v", out.Data.Items)
	}
	if svc.lastInput.Query != "go" {
		t.Fatalf("expected query 'go', got %q", svc.lastInput.Query)
	}
}

func TestControllerSearchWithoutServiceReturns503(t *testing.T) {
	// 无 search service 时应返回 503。
	app, _ := newForumTestAppWithSearch(nil)

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/search?query=go", nil, nil)
	if resp.StatusCode != nethttp.StatusServiceUnavailable {
		t.Fatalf("expected 503 when search unavailable, got %d", resp.StatusCode)
	}
}

func TestControllerTopicsRejectsQuery(t *testing.T) {
	// topics 列表带 query 应返回 400，引导走 /search。
	app, _, _ := newForumTestApp()

	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/topics?query=keyword", nil, nil)
	if resp.StatusCode != nethttp.StatusBadRequest {
		t.Fatalf("expected 400 for topics with query, got %d", resp.StatusCode)
	}
}

// --- 搜索重建端点测试 ---

type fakeReindexService struct {
	reindexRun    ReindexRunOutput
	reindexErr    error
	statusOutput  ReindexStatusOutput
	statusErr     error
	runsOutput    []ReindexRunOutput
	reindexCalled bool
}

func (f *fakeReindexService) Reindex(_ context.Context, _ int64) (ReindexRunOutput, error) {
	f.reindexCalled = true
	return f.reindexRun, f.reindexErr
}

func (f *fakeReindexService) ReindexStatus(_ context.Context) (ReindexStatusOutput, error) {
	return f.statusOutput, f.statusErr
}

func (f *fakeReindexService) ListReindexRuns(_ context.Context) ([]ReindexRunOutput, error) {
	return f.runsOutput, nil
}

// newForumTestAppWithReindex 构造带 reindex service 的测试 app。
// actor 6 拥有 search.manage 权限。
func newForumTestAppWithReindex(reindexer ReindexService) *fiber.App {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerForumActors{actors: map[int64]identity.Actor{
		6: {ID: 6, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSearchManage: true}},
		7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
	}}
	store := &controllerForumStore{}
	controller := NewControllerWithSearch(forum.NewService(forum.ServiceConfig{Store: store, Settings: store, Publisher: nil}), nil, reindexer, users, manager)
	loginProvider := forumRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			if userID == 0 {
				userID = 6
			}
			_, err := manager.Start(c, userID)
			return err
		})
	})
	app := apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
	return app
}

func TestControllerReindexRequiresLogin(t *testing.T) {
	app := newForumTestAppWithReindex(&fakeReindexService{})

	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/search/reindex", []byte(`{}`), nil)
	if resp.StatusCode != nethttp.StatusUnauthorized {
		t.Fatalf("expected 401 without login, got %d", resp.StatusCode)
	}
}

func TestControllerReindexRequiresPermission(t *testing.T) {
	app := newForumTestAppWithReindex(&fakeReindexService{})

	cookie := loginForumUser(t, app, 7) // 无 search.manage
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/search/reindex", []byte(`{}`), cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403 without permission, got %d", resp.StatusCode)
	}
}

func TestControllerReindexTriggersRebuild(t *testing.T) {
	svc := &fakeReindexService{reindexRun: ReindexRunOutput{ID: 1, Total: 5, Status: "running"}}
	app := newForumTestAppWithReindex(svc)

	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/search/reindex", []byte(`{}`), cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !svc.reindexCalled {
		t.Fatal("expected Reindex to be called")
	}
	defer resp.Body.Close()
	var out forumTestEnvelope[ReindexRunOutput]
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.ID != 1 || out.Data.Total != 5 {
		t.Fatalf("unexpected run %#v", out.Data)
	}
}

func TestControllerReindexStatusReturnsProgress(t *testing.T) {
	svc := &fakeReindexService{statusOutput: ReindexStatusOutput{
		ReindexRunOutput: ReindexRunOutput{ID: 2, Total: 10, Status: "running"},
		Processed:        7, Remaining: 3, Percent: 70,
	}}
	app := newForumTestAppWithReindex(svc)

	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/search/reindex", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var out forumTestEnvelope[ReindexStatusOutput]
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.Percent != 70 || out.Data.Processed != 7 {
		t.Fatalf("unexpected status %#v", out.Data)
	}
}

func TestControllerReindexRunsReturnsHistory(t *testing.T) {
	svc := &fakeReindexService{runsOutput: []ReindexRunOutput{{ID: 1, Total: 5, Status: "completed"}}}
	app := newForumTestAppWithReindex(svc)

	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/search/reindex/runs", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var out forumTestEnvelope[[]ReindexRunOutput]
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0].Status != "completed" {
		t.Fatalf("unexpected runs %#v", out.Data)
	}
}

func TestControllerReindexWithoutServiceReturns503(t *testing.T) {
	// reindexer 为 nil 时应返回 503。
	app := newForumTestAppWithReindex(nil)

	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/search/reindex", []byte(`{}`), cookie)
	if resp.StatusCode != nethttp.StatusServiceUnavailable {
		t.Fatalf("expected 503 when reindexer nil, got %d", resp.StatusCode)
	}
}

type fakeSearchProviderAdmin struct {
	state      SearchProvidersState
	selectID   string
	selectErr  error
	restoreErr error
	listErr    error
}

func (f *fakeSearchProviderAdmin) List(context.Context) (SearchProvidersState, error) {
	if f.listErr != nil {
		return SearchProvidersState{}, f.listErr
	}
	return f.state, nil
}

func (f *fakeSearchProviderAdmin) Select(_ context.Context, extensionID string) error {
	f.selectID = extensionID
	return f.selectErr
}

func (f *fakeSearchProviderAdmin) RestoreDefault(context.Context) error {
	return f.restoreErr
}

func newForumTestAppWithSearchProviders(admin SearchProviderAdmin) *fiber.App {
	manager := authsession.NewManager(session.NewStore(), authsession.Config{HashSecret: "test-secret"})
	users := controllerForumActors{actors: map[int64]identity.Actor{
		6: {ID: 6, Status: identity.UserStatusActive, Permissions: map[string]bool{identity.PermissionSearchManage: true}},
		7: {ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{}},
	}}
	store := &controllerForumStore{}
	controller := NewControllerWithSearch(forum.NewService(forum.ServiceConfig{Store: store, Settings: store, Publisher: nil}), nil, nil, users, manager).
		WithSearchProviderAdmin(admin)
	loginProvider := forumRouteProviderFunc(func(api fiber.Router) {
		api.Post("/test-login/:id", func(c fiber.Ctx) error {
			userID, _ := strconv.ParseInt(c.Params("id"), 10, 64)
			if userID == 0 {
				userID = 6
			}
			_, err := manager.Start(c, userID)
			return err
		})
	})
	return apphttp.NewApp(config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}, slog.Default(), apphttp.Dependencies{
		RouteProviders: []apphttp.RouteProvider{controller, loginProvider},
	})
}

func TestControllerListSearchProviders(t *testing.T) {
	admin := &fakeSearchProviderAdmin{state: SearchProvidersState{
		Items: []SearchProviderItem{
			{ExtensionID: "sforum.search-site", Label: "Site Search", Healthy: true, IsDefault: true},
		},
		Selected:           SearchProviderItem{ExtensionID: "sforum.search-site", Label: "Site Search", Healthy: true, IsDefault: true},
		Pinned:             false,
		DefaultExtensionID: "sforum.search-site",
	}}
	app := newForumTestAppWithSearchProviders(admin)
	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodGet, "/api/v1/admin/forum/search/providers", nil, cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var out forumTestEnvelope[SearchProvidersState]
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.DefaultExtensionID != "sforum.search-site" || len(out.Data.Items) != 1 {
		t.Fatalf("unexpected state %#v", out.Data)
	}
}

func TestControllerSelectSearchProvider(t *testing.T) {
	admin := &fakeSearchProviderAdmin{}
	app := newForumTestAppWithSearchProviders(admin)
	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodPut, "/api/v1/admin/forum/search/provider", []byte(`{"extensionId":"sforum.search-meilisearch"}`), cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if admin.selectID != "sforum.search-meilisearch" {
		t.Fatalf("selectID=%q", admin.selectID)
	}
}

func TestControllerSelectSearchProviderRequiresPermission(t *testing.T) {
	app := newForumTestAppWithSearchProviders(&fakeSearchProviderAdmin{})
	cookie := loginForumUser(t, app, 7)
	resp := performForumRequest(t, app, nethttp.MethodPut, "/api/v1/admin/forum/search/provider", []byte(`{"extensionId":"x"}`), cookie)
	if resp.StatusCode != nethttp.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestControllerResetSearchProvider(t *testing.T) {
	admin := &fakeSearchProviderAdmin{}
	app := newForumTestAppWithSearchProviders(admin)
	cookie := loginForumUser(t, app, 6)
	resp := performForumRequest(t, app, nethttp.MethodPost, "/api/v1/admin/forum/search/provider/reset", []byte(`{}`), cookie)
	if resp.StatusCode != nethttp.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
