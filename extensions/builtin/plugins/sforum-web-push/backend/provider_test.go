package main

import (
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

func TestWebPushProviderEncryptsAndClassifiesFakeEndpoint(t *testing.T) {
	tests := []struct {
		name, wantReason, wantClass string
		status                      int
	}{
		{name: "accepted", status: http.StatusCreated, wantReason: "web_push.sent"},
		{name: "expired", status: http.StatusGone, wantReason: "web_push.subscription_expired", wantClass: "permanent"},
		{name: "temporary", status: http.StatusServiceUnavailable, wantReason: "web_push.service_unavailable", wantClass: "temporary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var received []byte
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.Header.Get("Authorization") == "" || r.Header.Get("Content-Encoding") != "aes128gcm" {
					t.Errorf("unexpected Web Push request: method=%s headers=%v", r.Method, r.Header)
				}
				received, _ = io.ReadAll(r.Body)
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			webPushHTTPClient = server.Client()
			t.Cleanup(func() { webPushHTTPClient = nil })

			vapidPrivate, vapidPublic, err := webpush.GenerateVAPIDKeys()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("SFORUM_SETTING_SUBSCRIBER", "mailto:test@example.com")
			t.Setenv("SFORUM_SETTING_VAPID_PRIVATE_KEY", vapidPrivate)
			t.Setenv("SFORUM_SETTING_VAPID_PUBLIC_KEY", vapidPublic)
			p256dh, auth := subscriptionKeys(t)
			input, err := pluginv2.NewTypedDocument(requestSchema, map[string]any{
				"operation": "send", "deliveryId": "7",
				"subscription": map[string]any{"endpoint": server.URL, "p256dh": p256dh, "auth": auth},
				"notification": map[string]any{"title": "SForum", "body": "private plaintext", "url": "/notifications", "tag": "notification-7"},
			})
			if err != nil {
				t.Fatal(err)
			}
			output, err := invokeWebPush(context.Background(), &pluginv2.ProviderCall{Input: input})
			if err != nil {
				t.Fatal(err)
			}
			values := pluginv2.TypedDocumentValues(output)
			classification, _ := values["classification"].(string)
			if values["reason"] != test.wantReason || classification != test.wantClass {
				t.Fatalf("provider result = %#v", values)
			}
			if string(received) == "" || string(received) == "private plaintext" {
				t.Fatalf("payload was not encrypted: %q", received)
			}
		})
	}
}

func TestWebPushProbeNeverReturnsPrivateKey(t *testing.T) {
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SFORUM_SETTING_SUBSCRIBER", "mailto:test@example.com")
	t.Setenv("SFORUM_SETTING_VAPID_PRIVATE_KEY", privateKey)
	t.Setenv("SFORUM_SETTING_VAPID_PUBLIC_KEY", publicKey)
	input, _ := pluginv2.NewTypedDocument(requestSchema, map[string]any{"operation": "probe"})
	output, err := invokeWebPush(context.Background(), &pluginv2.ProviderCall{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	values := pluginv2.TypedDocumentValues(output)
	if values["publicKey"] != publicKey {
		t.Fatalf("probe public key = %#v", values)
	}
	for _, value := range values {
		if value == privateKey {
			t.Fatal("probe leaked VAPID private key")
		}
	}
}

func subscriptionKeys(t *testing.T) (string, string) {
	t.Helper()
	private, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil || len(private) == 0 {
		t.Fatal(err)
	}
	public := elliptic.Marshal(elliptic.P256(), x, y)
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(public), base64.RawURLEncoding.EncodeToString(auth)
}
