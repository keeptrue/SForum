package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	providerID       = "sforum.web-push.channel"
	providerSlot     = "notification.channel.web_push"
	providerContract = "sforum.web-push.channel@1"
	requestSchema    = "sforum.web-push.channel.request@1"
	responseSchema   = "sforum.web-push.channel.response@1"
	maxPayloadBytes  = 4096
)

var webPushHTTPClient webpush.HTTPClient

func newWebPushProviderRegistry() (*pluginv2.ProviderRegistry, error) {
	return pluginv2.NewProviderRegistry(pluginv2.ProviderDefinition{
		ID: providerID, Slot: providerSlot, ContractVersion: providerContract,
		Label: "SForum Web Push", Handler: "web-push.invoke",
		RequestSchema: requestSchema, ResponseSchema: responseSchema,
		Fallback: "closed", TimeoutMS: 5000, Execute: invokeWebPush,
	})
}

func invokeWebPush(ctx context.Context, call *pluginv2.ProviderCall) (*protocolwire.TypedDocument, error) {
	values := pluginv2.TypedDocumentValues(call.Input)
	operation, _ := values["operation"].(string)
	config := webPushConfig{
		subscriber: strings.TrimSpace(os.Getenv("SFORUM_SETTING_SUBSCRIBER")),
		publicKey:  strings.TrimSpace(os.Getenv("SFORUM_SETTING_VAPID_PUBLIC_KEY")),
		privateKey: strings.TrimSpace(os.Getenv("SFORUM_SETTING_VAPID_PRIVATE_KEY")),
	}
	if err := config.validate(); err != nil {
		return providerDocument(false, "permanent", "web_push.configuration_invalid", "Web Push VAPID settings are incomplete.", "")
	}
	if operation == "probe" {
		return providerDocument(true, "", "web_push.ready", "Web Push VAPID settings are ready.", config.publicKey)
	}
	if operation != "send" {
		return nil, fmt.Errorf("unsupported Web Push operation")
	}
	return sendWebPush(ctx, values, config)
}

type webPushConfig struct {
	subscriber, publicKey, privateKey string
}

func (c webPushConfig) validate() error {
	if !strings.HasPrefix(c.subscriber, "mailto:") && !strings.HasPrefix(c.subscriber, "https://") {
		return fmt.Errorf("invalid VAPID subscriber")
	}
	if len(c.publicKey) < 32 || len(c.privateKey) < 32 {
		return fmt.Errorf("invalid VAPID key pair")
	}
	return nil
}

func sendWebPush(ctx context.Context, values map[string]any, config webPushConfig) (*protocolwire.TypedDocument, error) {
	subscriptionValue, _ := values["subscription"].(map[string]any)
	notification, _ := values["notification"].(map[string]any)
	subscription := &webpush.Subscription{
		Endpoint: stringValue(subscriptionValue, "endpoint"),
		Keys:     webpush.Keys{P256dh: stringValue(subscriptionValue, "p256dh"), Auth: stringValue(subscriptionValue, "auth")},
	}
	payload, err := json.Marshal(notification)
	if err != nil || len(payload) == 0 || len(payload) > maxPayloadBytes {
		return providerDocument(false, "permanent", "web_push.payload_invalid", "Web Push payload is invalid.", "")
	}
	response, err := webpush.SendNotificationWithContext(ctx, payload, subscription, &webpush.Options{
		HTTPClient: webPushHTTPClient,
		Subscriber: config.subscriber, VAPIDPublicKey: config.publicKey, VAPIDPrivateKey: config.privateKey,
		TTL: 300, Urgency: webpush.UrgencyNormal,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return providerDocument(false, "temporary", "web_push.transport_failed", "Web Push transport failed.", "")
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent:
		return providerDocument(true, "", "web_push.sent", "", "")
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		return providerDocument(false, "permanent", "web_push.subscription_expired", "The browser subscription expired.", "")
	case response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return providerDocument(false, "permanent", "web_push.subscription_invalid", "The browser subscription was rejected.", "")
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return providerDocument(false, "temporary", "web_push.service_unavailable", "The push service is temporarily unavailable.", "")
	default:
		return providerDocument(false, "permanent", "web_push.rejected", "The push service rejected the request.", "")
	}
}

func providerDocument(ok bool, classification, reason, message, publicKey string) (*protocolwire.TypedDocument, error) {
	values := map[string]any{"ok": ok, "reason": reason}
	if classification != "" {
		values["classification"] = classification
	}
	if message != "" {
		values["message"] = message
	}
	if publicKey != "" {
		values["publicKey"] = publicKey
	}
	return pluginv2.NewTypedDocument(responseSchema, values)
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
