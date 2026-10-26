package notificationscontroller

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

// pushDeviceTestStore 复用通知测试假 store，并补上设备注册能力（控制器按接口断言接线）。
type pushDeviceTestStore struct {
	notificationTestStore
	devices      map[int64][]notifications.PushDevice
	registerCall int
	revokeCalls  int
}

func newPushDeviceTestStore() *pushDeviceTestStore {
	return &pushDeviceTestStore{devices: map[int64][]notifications.PushDevice{}}
}

func (s *pushDeviceTestStore) RegisterPushDevice(_ context.Context, input notifications.RegisterPushDeviceInput) (notifications.PushDevice, error) {
	// 假 store 不做校验：校验必须已经发生在控制器/领域层，这里断言收到的输入是归一化后的。
	if _, err := notifications.NormalizePushDeviceInput(input); err != nil {
		return notifications.PushDevice{}, err
	}
	s.registerCall++
	item := notifications.PushDevice{
		ID: int64(s.registerCall), DeviceID: input.DeviceID, Platform: input.Platform,
		AppVersion: input.AppVersion, Locale: input.Locale, DeviceName: input.DeviceName,
		Status: notifications.PushDeviceStatusActive,
	}
	s.devices[input.UserID] = append(s.devices[input.UserID], item)
	return item, nil
}

func (s *pushDeviceTestStore) ListPushDevices(_ context.Context, userID int64, includeRevoked bool) ([]notifications.PushDevice, error) {
	items := make([]notifications.PushDevice, 0, len(s.devices[userID]))
	for _, item := range s.devices[userID] {
		if !includeRevoked && item.Status != notifications.PushDeviceStatusActive {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *pushDeviceTestStore) RevokePushDevice(_ context.Context, userID int64, deviceID string) error {
	s.revokeCalls++
	for index, item := range s.devices[userID] {
		if item.DeviceID == deviceID && item.Status == notifications.PushDeviceStatusActive {
			s.devices[userID][index].Status = notifications.PushDeviceStatusRevoked
			return nil
		}
	}
	// 不属于当前用户或已撤销：统一未找到，避免暴露归属。
	return notifications.ErrPushDeviceNotFound
}

func pushDeviceTestApp(t *testing.T, store *pushDeviceTestStore, userID int64) *fiber.App {
	t.Helper()
	controller := NewController(store, nil, nil, nil)
	app := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens: notificationBearer{auth: apitokens.Authenticated{
			UserID: userID, TokenID: 5, PublicID: "push", Scopes: []string{"post.create"},
		}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
	return app
}

func pushDeviceRequest(t *testing.T, app *fiber.App, method, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer sft_push-device")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode %s: %v", string(raw), err)
		}
	}
	return resp.StatusCode, envelope
}

func TestPushDeviceRegisterAndListAreOwnerScoped(t *testing.T) {
	store := newPushDeviceTestStore()
	app := pushDeviceTestApp(t, store, 42)

	status, envelope := pushDeviceRequest(t, app, http.MethodPost, "/api/v1/push/devices", map[string]any{
		"deviceId": "install-42-ios", "platform": "ios", "token": "fcm-token-for-device-42",
		"appVersion": "1.4.0", "locale": "zh-CN", "deviceName": "iPhone",
	})
	if status != http.StatusOK {
		t.Fatalf("register expected 200, got %d (%#v)", status, envelope)
	}
	data, _ := envelope["data"].(map[string]any)
	if data["deviceId"] != "install-42-ios" || data["platform"] != "ios" {
		t.Fatalf("unexpected register payload: %#v", data)
	}
	if _, leaked := data["token"]; leaked {
		t.Fatal("response must never contain the push token")
	}

	status, envelope = pushDeviceRequest(t, app, http.MethodGet, "/api/v1/push/devices", nil)
	if status != http.StatusOK {
		t.Fatalf("list expected 200, got %d", status)
	}
	list, _ := envelope["data"].(map[string]any)
	items, _ := list["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected the caller's own device, got %#v", list)
	}
	raw, _ := json.Marshal(items[0])
	if bytes.Contains(raw, []byte("fcm-token-for-device-42")) {
		t.Fatalf("device list leaked the token: %s", raw)
	}

	// 另一个用户的列表为空：所有权按 user_id 过滤。
	otherApp := pushDeviceTestApp(t, store, 43)
	status, envelope = pushDeviceRequest(t, otherApp, http.MethodGet, "/api/v1/push/devices", nil)
	if status != http.StatusOK {
		t.Fatalf("other user list expected 200, got %d", status)
	}
	otherList, _ := envelope["data"].(map[string]any)
	if items, _ := otherList["items"].([]any); len(items) != 0 {
		t.Fatalf("other user must not see devices: %#v", otherList)
	}
}

func TestPushDeviceRevokeRequiresOwnership(t *testing.T) {
	store := newPushDeviceTestStore()
	owner := pushDeviceTestApp(t, store, 42)
	if status, _ := pushDeviceRequest(t, owner, http.MethodPost, "/api/v1/push/devices", map[string]any{
		"deviceId": "install-42-android", "platform": "android", "token": "fcm-token-owner-42",
	}); status != http.StatusOK {
		t.Fatalf("register expected 200, got %d", status)
	}

	intruder := pushDeviceTestApp(t, store, 99)
	status, envelope := pushDeviceRequest(t, intruder, http.MethodDelete, "/api/v1/push/devices/install-42-android", nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-user revoke must be 404, got %d (%#v)", status, envelope)
	}
	data, _ := envelope["data"].(map[string]any)
	if data["reason"] != "notification.push_device_not_found" {
		t.Fatalf("unexpected reason: %#v", data)
	}

	status, _ = pushDeviceRequest(t, owner, http.MethodDelete, "/api/v1/push/devices/install-42-android", nil)
	if status != http.StatusOK {
		t.Fatalf("owner revoke expected 200, got %d", status)
	}
	status, _ = pushDeviceRequest(t, owner, http.MethodDelete, "/api/v1/push/devices/install-42-android", nil)
	if status != http.StatusNotFound {
		t.Fatalf("second revoke must be 404, got %d", status)
	}
}

func TestPushDeviceRegisterValidationAndAuth(t *testing.T) {
	store := newPushDeviceTestStore()
	app := pushDeviceTestApp(t, store, 42)

	cases := []struct {
		name   string
		body   map[string]any
		status int
		reason string
	}{
		{
			name:   "unknown platform",
			body:   map[string]any{"deviceId": "install-web-1", "platform": "web", "token": "web-token-123456"},
			status: http.StatusUnprocessableEntity,
			reason: "notification.push_device_platform_unsupported",
		},
		{
			name:   "short token",
			body:   map[string]any{"deviceId": "install-short-1", "platform": "ios", "token": "tiny"},
			status: http.StatusUnprocessableEntity,
			reason: "notification.push_device_invalid",
		},
		{
			name:   "bad device id",
			body:   map[string]any{"deviceId": "x", "platform": "ios", "token": "fcm-token-1234567890"},
			status: http.StatusUnprocessableEntity,
			reason: "notification.push_device_invalid",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, envelope := pushDeviceRequest(t, app, http.MethodPost, "/api/v1/push/devices", testCase.body)
			if status != testCase.status {
				t.Fatalf("expected %d, got %d (%#v)", testCase.status, status, envelope)
			}
			data, _ := envelope["data"].(map[string]any)
			if data["reason"] != testCase.reason {
				t.Fatalf("expected reason %q, got %#v", testCase.reason, data)
			}
		})
	}

	// 未认证：Bearer 无效时控制器不得触达 store。
	callsBefore := store.registerCall
	unauthenticated := apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{err: apitokens.ErrTokenInvalid},
		RouteProviders: []apphttp.RouteProvider{NewController(store, nil, nil, nil)},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/push/devices", nil)
	req.Header.Set("Authorization", "Bearer sft_revoked")
	resp, err := unauthenticated.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
	if store.registerCall != callsBefore {
		t.Fatalf("unauthenticated request must not reach the store (%d -> %d calls)", callsBefore, store.registerCall)
	}
}
