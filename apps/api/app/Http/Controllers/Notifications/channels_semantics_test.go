package notificationscontroller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// 渠道名语义：未知渠道是**客户端错误**（路径指向不存在的渠道资源），
// 不是「通知服务暂时不可用」。改造前 `PUT/POST /admin/notifications/channels/{channel}`
// 对任何非 web_push 的名字都答 503，客户端会把「写错名字」当成后端故障去重试。
func TestAdminNotificationChannelUnknownChannelIsNotFound(t *testing.T) {
	runtime := &channelRuntimeFixture{}
	auditor := &channelAuditFixture{}
	controller := newChannelTestController(t, runtime, auditor)

	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPut, path: "/api/v1/admin/notifications/channels/email"},
		{method: http.MethodPost, path: "/api/v1/admin/notifications/channels/email/reset"},
		{method: http.MethodPost, path: "/api/v1/admin/notifications/channels/email/test"},
	} {
		response := channelTestRequest(t, controller, request.method, request.path, `{"candidateId":"x","expectedRevision":1,"revision":1}`)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status=%d, want 404 (%s)", request.method, request.path, response.StatusCode, body)
		}
		if !containsReason(body, "notification.channel_not_found") {
			t.Fatalf("%s %s reason missing: %s", request.method, request.path, body)
		}
	}
	if runtime.selectCalls != 0 || runtime.resetCalls != 0 || len(auditor.events) != 0 {
		t.Fatalf("unknown channel must not reach the runtime or the audit log: %#v %#v", runtime, auditor.events)
	}
}

// 允许路径：已知渠道照常走运行时与审计，证明上面的 404 不是「恒 404」。
func TestAdminNotificationChannelKnownChannelSelectsAndResets(t *testing.T) {
	runtime := &channelRuntimeFixture{}
	auditor := &channelAuditFixture{}
	controller := newChannelTestController(t, runtime, auditor)

	response := channelTestRequest(t, controller, http.MethodPut, "/api/v1/admin/notifications/channels/web_push",
		`{"candidateId":"candidate-1","expectedRevision":3}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || runtime.selectCalls != 1 {
		t.Fatalf("select status=%d calls=%d", response.StatusCode, runtime.selectCalls)
	}

	response = channelTestRequest(t, controller, http.MethodPost, "/api/v1/admin/notifications/channels/web_push/reset", `{"revision":4}`)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || runtime.resetCalls != 1 {
		t.Fatalf("reset status=%d calls=%d", response.StatusCode, runtime.resetCalls)
	}
	if len(auditor.events) != 2 || auditor.events[0].Action != audit.ActionNotificationChannelSelect ||
		auditor.events[1].Action != audit.ActionNotificationChannelReset {
		t.Fatalf("audit events = %#v", auditor.events)
	}
}

func newChannelTestController(t *testing.T, runtime ChannelRuntime, auditor *channelAuditFixture) *Controller {
	t.Helper()
	actor := identity.Actor{ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{
		identity.PermissionSettingsNotificationsManage: true,
	}}
	return NewController(&settingsStore{}, nil, settingsActors{actor: actor}, nil).
		WithAuditor(auditor).
		WithChannels(runtime, auditor, nil)
}

func channelTestRequest(t *testing.T, controller *Controller, method, path, body string) *http.Response {
	t.Helper()
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "channels"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return notificationSettingsRequest(t, app, method, path, "sft_channels", json.RawMessage(body))
}

func containsReason(body []byte, reason string) bool {
	var envelope struct {
		Data struct {
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	return envelope.Data.Reason == reason
}

type channelAuditFixture struct {
	events []audit.Event
	nextID int64
}

func (a *channelAuditFixture) Append(_ context.Context, event audit.Event) error {
	a.events = append(a.events, event)
	return nil
}

func (a *channelAuditFixture) AppendReturningID(_ context.Context, event audit.Event) (int64, error) {
	a.events = append(a.events, event)
	a.nextID++
	return a.nextID, nil
}

type channelRuntimeFixture struct {
	selectCalls int
	resetCalls  int
}

func (r *channelRuntimeFixture) NotificationChannelInspection(context.Context) (extensions.ProviderSlotInspection, error) {
	return extensions.ProviderSlotInspection{}, nil
}

func (r *channelRuntimeFixture) SelectNotificationChannel(context.Context, string, string, int64, int64, int64) (any, error) {
	r.selectCalls++
	return map[string]any{"selected": true}, nil
}

func (r *channelRuntimeFixture) ResetNotificationChannel(context.Context, string, int64, int64, int64) error {
	r.resetCalls++
	return nil
}

func (r *channelRuntimeFixture) ProbeNotificationChannel(context.Context, string, string, string) (map[string]any, error) {
	return map[string]any{}, nil
}
