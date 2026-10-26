package notificationscontroller

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

type webPushControllerStore struct {
	notificationTestStore
	createInput notifications.CreateWebPushSubscriptionInput
	items       []notifications.WebPushSubscription
	activeOnly  bool
	revokeUser  int64
	revokeID    int64
	revokeErr   error
}

func (s *webPushControllerStore) CreateWebPushSubscription(_ context.Context, input notifications.CreateWebPushSubscriptionInput) (notifications.WebPushSubscription, error) {
	s.createInput = input
	return notifications.WebPushSubscription{
		ID: 17, UserID: input.UserID, Endpoint: input.Endpoint, EndpointOrigin: "https://push.example.test",
		P256DHKey: input.P256DHKey, AuthKey: input.AuthKey, ContentEncoding: input.ContentEncoding, Status: "active",
	}, nil
}

func (s *webPushControllerStore) ListWebPushSubscriptions(_ context.Context, userID int64, activeOnly bool) ([]notifications.WebPushSubscription, error) {
	if userID != 42 {
		return nil, notifications.ErrSubscriptionNotFound
	}
	s.activeOnly = activeOnly
	return s.items, nil
}

func (s *webPushControllerStore) RevokeWebPushSubscription(_ context.Context, userID, id int64) error {
	s.revokeUser, s.revokeID = userID, id
	return s.revokeErr
}

func (*webPushControllerStore) ListChannelDeliveries(context.Context, int) ([]notifications.ChannelDelivery, error) {
	return nil, nil
}

func TestWebPushSubscriptionRoutesRequireLoginUseCurrentUserAndRedactSecrets(t *testing.T) {
	secretEndpoint := "https://push.example.test/private/subscription-token"
	store := &webPushControllerStore{items: []notifications.WebPushSubscription{{
		ID: 17, UserID: 42, Endpoint: secretEndpoint, EndpointOrigin: "https://push.example.test",
		P256DHKey: bytes.Repeat([]byte{1}, 65), AuthKey: bytes.Repeat([]byte{2}, 16), ContentEncoding: "aes128gcm", Status: "active",
	}}}
	app := webPushTestApp(store)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/web-push/subscriptions"},
		{http.MethodPost, "/api/v1/web-push/subscriptions"},
		{http.MethodDelete, "/api/v1/web-push/subscriptions/17"},
	} {
		resp := notificationSettingsRequest(t, app, route.method, route.path, "", nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d", route.method, route.path, resp.StatusCode)
		}
	}

	p256dh := bytes.Repeat([]byte{3}, 65)
	authKey := bytes.Repeat([]byte{4}, 16)
	resp := notificationSettingsRequest(t, app, http.MethodPost, "/api/v1/web-push/subscriptions", "sft_user", map[string]any{
		"userId":   999,
		"endpoint": secretEndpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(p256dh),
			"auth":   base64.RawURLEncoding.EncodeToString(authKey),
		},
	})
	createBody := readResponseBody(t, resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, createBody)
	}
	if store.createInput.UserID != 42 || store.createInput.Endpoint != secretEndpoint || !bytes.Equal(store.createInput.P256DHKey, p256dh) || !bytes.Equal(store.createInput.AuthKey, authKey) {
		t.Fatalf("create input=%#v", store.createInput)
	}
	assertWebPushResponseRedacted(t, createBody, secretEndpoint, p256dh, authKey)

	resp = notificationSettingsRequest(t, app, http.MethodGet, "/api/v1/web-push/subscriptions", "sft_user", nil)
	listBody := readResponseBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, listBody)
	}
	if !store.activeOnly {
		t.Fatal("member subscription list included inactive history")
	}
	assertWebPushResponseRedacted(t, listBody, secretEndpoint, store.items[0].P256DHKey, store.items[0].AuthKey)

	resp = notificationSettingsRequest(t, app, http.MethodDelete, "/api/v1/web-push/subscriptions/17", "sft_user", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || store.revokeUser != 42 || store.revokeID != 17 {
		t.Fatalf("revoke status=%d user=%d id=%d", resp.StatusCode, store.revokeUser, store.revokeID)
	}
}

func TestWebPushSubscriptionTheftReturnsNotFound(t *testing.T) {
	store := &webPushControllerStore{revokeErr: notifications.ErrSubscriptionNotFound}
	app := webPushTestApp(store)
	resp := notificationSettingsRequest(t, app, http.MethodDelete, "/api/v1/web-push/subscriptions/88", "sft_user", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || store.revokeUser != 42 || store.revokeID != 88 {
		t.Fatalf("status=%d user=%d id=%d", resp.StatusCode, store.revokeUser, store.revokeID)
	}
}

func webPushTestApp(store *webPushControllerStore) *fiber.App {
	controller := NewController(store, nil, settingsActors{actor: identity.Actor{ID: 42, Status: identity.UserStatusActive}}, nil)
	return apphttp.NewApp(notificationTestConfig(), slog.Default(), apphttp.Dependencies{
		BearerTokens:   notificationBearer{auth: apitokens.Authenticated{UserID: 42, TokenID: 7, PublicID: "web-push"}},
		RouteProviders: []apphttp.RouteProvider{controller},
	})
}

func readResponseBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertWebPushResponseRedacted(t *testing.T, body []byte, endpoint string, p256dh, auth []byte) {
	t.Helper()
	for _, secret := range [][]byte{
		[]byte(endpoint),
		[]byte(base64.RawURLEncoding.EncodeToString(p256dh)),
		[]byte(base64.RawURLEncoding.EncodeToString(auth)),
	} {
		if bytes.Contains(body, secret) {
			t.Fatalf("response leaked subscription secret %q: %s", secret, body)
		}
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"endpointOrigin":"https://push.example.test"`)) {
		t.Fatalf("response omitted safe endpoint origin: %s", body)
	}
}
