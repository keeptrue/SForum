package notificationscontroller

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgxpool"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestNotificationListRejectsInvalidPATBeforeResolvingUserID(t *testing.T) {
	store := &notificationTestStore{}
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{err: apitokens.ErrTokenInvalid},
		RouteProviders: []apphttp.RouteProvider{controller},
	})

	resp := notificationRequest(t, app, "sft_disabled-user")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if store.listCalls != 0 {
		t.Fatalf("invalid PAT reached notification store %d times", store.listCalls)
	}
}

func TestNotificationListAcceptsAuthenticatedActivePAT(t *testing.T) {
	store := &notificationTestStore{}
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens: notificationBearer{auth: apitokens.Authenticated{
			UserID: 42, TokenID: 7, PublicID: "active", Scopes: []string{"post.create"},
		}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})

	resp := notificationRequest(t, app, "sft_active-user")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if store.lastUserID != 42 {
		t.Fatalf("notification query user=%d", store.lastUserID)
	}
}

func TestNotificationListForwardsServerFiltersAndCursor(t *testing.T) {
	store := &notificationTestStore{}
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "active"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?beforeId=88&limit=10&category=moderation&type=moderation_approved&unread=true", nil)
	req.Header.Set("Authorization", "Bearer sft_active-user")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := store.lastListInput; got.RecipientUserID != 42 || got.BeforeID != 88 || got.Limit != 10 || got.Category != "moderation" || got.Type != "moderation_approved" || got.Unread == nil || !*got.Unread {
		t.Fatalf("unexpected list input: %#v", got)
	}
}

func TestNotificationDetailUsesCurrentRecipientAndReturnsPreview(t *testing.T) {
	store := &notificationTestStore{detail: notifications.Notification{ID: 55, RecipientUserID: 42, Type: notifications.TypeReply, TargetType: "comment", TargetID: 9, Payload: json.RawMessage(`{"topicId":3}`)}}
	controller := NewController(store, nil, nil, nil).
		WithTargetVisibility(notificationVisibilityFixture{available: true, path: "/t/3#comment-9"}).
		WithTargetPreview(notificationPreviewFixture{available: true, preview: notifications.TargetPreview{TopicID: 3, TopicTitle: "A topic", Content: notifications.TargetPreviewContent{Type: "comment", ID: 9, Excerpt: "A reply"}}})
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "active"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/55", nil)
	req.Header.Set("Authorization", "Bearer sft_active-user")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || store.lastDetailUserID != 42 || store.lastDetailID != 55 || !bytes.Contains(body, []byte(`"topicTitle":"A topic"`)) || !bytes.Contains(body, []byte(`"targetPath":"/t/3#comment-9"`)) {
		t.Fatalf("status=%d store=%d/%d body=%s", resp.StatusCode, store.lastDetailUserID, store.lastDetailID, body)
	}
}

func TestNotificationDetailReturnsNotFoundWithoutLeakingForeignRows(t *testing.T) {
	store := &notificationTestStore{detailErr: notifications.ErrNotificationNotFound}
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "active"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	for _, path := range []string{"/api/v1/notifications/77", "/api/v1/notifications/not-an-id"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer sft_active-user")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("path=%s status=%d", path, resp.StatusCode)
		}
	}
}

func TestNotificationListScrubsUnavailableForumTargetsPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	stamp := time.Now().UnixNano()
	username := fmt.Sprintf("notification_target_%d", stamp)
	var userID, categoryGroupID, categoryID int64
	t.Cleanup(func() {
		if userID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM notifications WHERE recipient_user_id=$1`, userID)
		}
		if categoryID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM comments WHERE topic_id IN (SELECT id FROM topics WHERE category_id=$1)`, categoryID)
			_, _ = pool.Exec(context.Background(), `DELETE FROM topics WHERE category_id=$1`, categoryID)
		}
		if userID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM posts WHERE created_by_user_id=$1`, userID)
		}
		if categoryID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM categories WHERE id=$1`, categoryID)
		}
		if categoryGroupID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM category_groups WHERE id=$1`, categoryGroupID)
		}
		if userID > 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		}
	})
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
VALUES ($1,$1,$2,$2,$1,'active') RETURNING id`, username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	categorySlug := fmt.Sprintf("notification-target-%d", stamp)
	if err := pool.QueryRow(ctx, `INSERT INTO category_groups (slug,name,visibility) VALUES ($1,$1,'public') RETURNING id`, categorySlug+"-group").Scan(&categoryGroupID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO categories (group_id,slug,name,visibility) VALUES ($1,$2,$2,'public') RETURNING id`, categoryGroupID, categorySlug).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	insertPost := func(label string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO posts (raw_content,html_content,plain_text,content_hash,created_by_user_id)
VALUES ($1,$1,$1,$2,$3) RETURNING id`, label, fmt.Sprintf("hash-%d-%s", stamp, label), userID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertTopic := func(label, status string) int64 {
		var id int64
		postID := insertPost(label)
		if err := pool.QueryRow(ctx, `INSERT INTO topics (category_id,author_user_id,content_id,title,slug,status)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, categoryID, userID, postID, label, fmt.Sprintf("%s-%d", label, stamp), status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertComment := func(topicID int64, label, status string) int64 {
		var id int64
		postID := insertPost(label)
		if err := pool.QueryRow(ctx, `INSERT INTO comments (topic_id,content_id,author_user_id,path_key,status)
VALUES ($1,$2,$3,$4,$5) RETURNING id`, topicID, postID, userID, fmt.Sprintf("%020d", postID), status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	visibleTopic := insertTopic("visible-topic", "active")
	hiddenTopic := insertTopic("hidden-topic", "hidden")
	deletedTopic := insertTopic("deleted-topic", "deleted")
	visibleComment := insertComment(visibleTopic, "visible-comment", "active")
	hiddenComment := insertComment(visibleTopic, "hidden-comment", "hidden")
	deletedComment := insertComment(visibleTopic, "deleted-comment", "deleted")
	hiddenTopicComment := insertComment(hiddenTopic, "hidden-topic-comment", "active")
	preview, available, err := forum.NewPostgresStore(pool).ResolveNotificationTargetPreview(ctx, "comment", visibleComment)
	if err != nil || !available || preview.TopicTitle != "visible-topic" || preview.Content.Excerpt != "visible-comment" || preview.Context == nil || preview.Context.Type != "topic" {
		t.Fatalf("visible comment preview=%#v available=%t err=%v", preview, available, err)
	}

	store := notifications.NewPostgresStore(pool)
	type expectedTarget struct {
		id        int64
		available bool
		path      string
		secret    string
	}
	expected := make(map[int64]expectedTarget)
	create := func(label, targetType string, targetID int64, available bool, path string) {
		secret := "private-" + label
		actorID := userID
		item, err := store.Create(ctx, notifications.CreateInput{
			RecipientUserID: userID,
			Type:            notifications.TypeMention,
			ActorUserID:     &actorID,
			TargetType:      targetType,
			TargetID:        targetID,
			Payload:         json.RawMessage(fmt.Sprintf(`{"title":%q,"reviewNote":%q,"excerpt":%q,"route":%q}`, secret, secret, secret, "/"+secret)),
			DedupeKey:       fmt.Sprintf("target-visibility:%d:%s", stamp, label),
		})
		if err != nil {
			t.Fatal(err)
		}
		expected[item.ID] = expectedTarget{id: item.ID, available: available, path: path, secret: secret}
	}
	create("visible-comment", "comment", visibleComment, true, fmt.Sprintf("/t/%d#comment-%d", visibleTopic, visibleComment))
	create("hidden-topic", "topic", hiddenTopic, false, "")
	create("deleted-topic", "topic", deletedTopic, false, "")
	create("hidden-comment", "comment", hiddenComment, false, "")
	create("deleted-comment", "comment", deletedComment, false, "")
	create("hidden-topic-comment", "comment", hiddenTopicComment, false, "")

	controller := NewController(store, nil, nil, store).WithTargetVisibility(forum.NewPostgresStore(pool))
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: userID, TokenID: 7, PublicID: "active"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications?limit=20", nil)
	req.Header.Set("Authorization", "Bearer sft_active-user")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var envelope struct {
		Data notifications.Page `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != len(expected) {
		t.Fatalf("items=%d want=%d body=%s", len(envelope.Data.Items), len(expected), body)
	}
	for _, item := range envelope.Data.Items {
		want, ok := expected[item.ID]
		if !ok {
			t.Fatalf("unexpected notification id=%d", item.ID)
		}
		if want.available {
			if !item.TargetAvailable || item.TargetPath != want.path || item.ActorUserID == nil || !strings.Contains(string(item.Payload), want.secret) {
				t.Fatalf("visible target was not preserved: %#v", item)
			}
			continue
		}
		if item.TargetAvailable || item.TargetPath != "" || item.TargetType != "unavailable" || item.TargetID != 0 || item.ActorUserID != nil || string(item.Payload) != "{}" {
			t.Fatalf("unavailable target leaked fields: %#v", item)
		}
		if bytes.Contains(body, []byte(want.secret)) {
			t.Fatalf("unavailable target leaked marker %q in response", want.secret)
		}
	}
}

func TestRevisionStreamHonorsCursorAndSendsOnlyRevision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &revisionTestStore{revision: 7}
	wakes := make(chan struct{}, 1)
	wakes <- struct{}{}
	store.afterCalls = 2
	store.after = func() { cancel() }
	var output bytes.Buffer

	streamRevisionEvents(ctx, bufio.NewWriter(&output), store, 42, wakes, 6, time.Hour, time.Hour)
	body := output.String()
	if !strings.Contains(body, "id: 7\nevent: revision\ndata: {\"revision\":7}\n\n") {
		t.Fatalf("missing revision event: %q", body)
	}
	for _, private := range []string{"payload", "actor", "target", "recipient"} {
		if strings.Contains(body, private) {
			t.Fatalf("stream leaked %q in %q", private, body)
		}
	}
}

func TestRevisionStreamSuppressesMatchingLastEventCursor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &revisionTestStore{revision: 7, afterCalls: 1, after: cancel}
	var output bytes.Buffer
	streamRevisionEvents(ctx, bufio.NewWriter(&output), store, 42, nil, 7, time.Hour, time.Hour)
	if output.Len() != 0 {
		t.Fatalf("matching cursor produced output: %q", output.String())
	}
}

func TestRevisionStreamReconcilesMissedWakeAndHeartbeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &revisionTestStore{revisions: []int64{4, 5}, afterCalls: 2, after: cancel}
	var output bytes.Buffer
	streamRevisionEvents(ctx, bufio.NewWriter(&output), store, 42, nil, 4, time.Millisecond, 2*time.Millisecond)
	body := output.String()
	if !strings.Contains(body, ": heartbeat\n\n") {
		t.Fatalf("missing heartbeat: %q", body)
	}
	if !strings.Contains(body, "data: {\"revision\":5}") {
		t.Fatalf("missed wake was not reconciled from durable revision: %q", body)
	}
}

func TestRevisionStreamStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &revisionTestStore{revision: 2}
	var output bytes.Buffer
	streamRevisionEvents(ctx, bufio.NewWriter(&output), store, 42, nil, 2, time.Hour, time.Hour)
	if store.calls != 1 {
		t.Fatalf("revision reads=%d want=1", store.calls)
	}
}

func TestNotificationStreamContextHasBoundedLifetime(t *testing.T) {
	ctx, cancel := notificationStreamContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("notification stream context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > notificationStreamMaxLifetime {
		t.Fatalf("notification stream lifetime=%s want within %s", remaining, notificationStreamMaxLifetime)
	}
}

func TestNotificationStreamReturnsConnectionLimitWithoutBody(t *testing.T) {
	store := &notificationTestStore{revisionErr: notifications.ErrRevisionConnectionLimit}
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "active"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/stream", nil)
	req.Header.Set("Authorization", "Bearer sft_active-user")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 429, got %d: %s", resp.StatusCode, body)
	}
}

func TestNotificationStreamUsesLastEventIDAndNoStoreHeaders(t *testing.T) {
	wakes := make(chan struct{}, 1)
	wakes <- struct{}{}
	store := &notificationTestStore{revision: 7, revisionFailAfter: 2, revisionWakes: wakes}
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "active"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/stream", nil)
	req.Header.Set("Authorization", "Bearer sft_active-user")
	req.Header.Set("Last-Event-ID", "6")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "id: 7\nevent: revision\ndata: {\"revision\":7}") {
		t.Fatalf("unexpected stream response status=%d body=%q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store, no-transform" {
		t.Fatalf("cache-control=%q", got)
	}
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("x-accel-buffering=%q", got)
	}
}

type notificationBearer struct {
	auth apitokens.Authenticated
	err  error
}

func (b notificationBearer) AuthenticatePlaintext(fiber.Ctx, string) (apitokens.Authenticated, error) {
	return b.auth, b.err
}

type notificationTestStore struct {
	listCalls         int
	lastUserID        int64
	lastListInput     notifications.ListInput
	revisionErr       error
	revision          int64
	revisionCalls     int
	revisionFailAfter int
	revisionWakes     <-chan struct{}
	detail            notifications.Notification
	detailErr         error
	lastDetailUserID  int64
	lastDetailID      int64
}

type notificationVisibilityFixture struct {
	available bool
	path      string
}

func (r notificationVisibilityFixture) ResolveNotificationTarget(context.Context, int64, string, int64) (bool, string, error) {
	return r.available, r.path, nil
}

type notificationPreviewFixture struct {
	preview   notifications.TargetPreview
	available bool
}

func (r notificationPreviewFixture) ResolveNotificationTargetPreview(context.Context, int64, string, int64) (notifications.TargetPreview, bool, error) {
	return r.preview, r.available, nil
}

func (s *notificationTestStore) List(_ context.Context, input notifications.ListInput) (notifications.Page, error) {
	s.listCalls++
	s.lastUserID = input.RecipientUserID
	s.lastListInput = input
	return notifications.Page{Items: []notifications.Notification{}}, nil
}

func (s *notificationTestStore) GetNotification(_ context.Context, userID, id int64) (notifications.Notification, error) {
	s.lastDetailUserID = userID
	s.lastDetailID = id
	return s.detail, s.detailErr
}

func (s *notificationTestStore) RecipientRevision(context.Context, int64) (int64, error) {
	s.revisionCalls++
	if s.revisionFailAfter > 0 && s.revisionCalls >= s.revisionFailAfter {
		return 0, io.EOF
	}
	return s.revision, nil
}
func (s *notificationTestStore) SubscribeRevision(int64) (<-chan struct{}, func(), error) {
	return s.revisionWakes, func() {}, s.revisionErr
}

func (*notificationTestStore) UnreadCount(context.Context, int64) (int64, error) { return 0, nil }
func (*notificationTestStore) MarkRead(context.Context, int64, int64) error      { return nil }
func (*notificationTestStore) MarkAllRead(context.Context, int64) (int64, error) {
	return 0, nil
}
func (*notificationTestStore) GetDelivery(context.Context, int64) (notifications.MailDelivery, error) {
	return notifications.MailDelivery{}, nil
}
func (*notificationTestStore) UpdateDelivery(context.Context, notifications.DeliveryUpdate) error {
	return nil
}
func (*notificationTestStore) ListDeliveries(context.Context, int) ([]notifications.MailDelivery, error) {
	return nil, nil
}

func notificationRequest(t *testing.T, app *fiber.App, token string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func notificationTestConfig() config.Config {
	return config.Config{AppName: "SForum", AppEnv: "test", CSRFEnabled: false, AppLocale: "zh-CN", SupportedLocales: []string{"zh-CN", "en-US"}}
}

type revisionTestStore struct {
	mu         sync.Mutex
	revision   int64
	revisions  []int64
	calls      int
	afterCalls int
	after      func()
}

func (s *revisionTestStore) RecipientRevision(context.Context, int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	revision := s.revision
	if len(s.revisions) > 0 {
		index := s.calls - 1
		if index >= len(s.revisions) {
			index = len(s.revisions) - 1
		}
		revision = s.revisions[index]
	}
	if s.afterCalls == s.calls && s.after != nil {
		s.after()
	}
	return revision, nil
}

var _ notifications.RecipientRevisionStore = (*revisionTestStore)(nil)
