package notificationscontroller

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

const (
	webPushSlot     = "notification.channel.web_push"
	webPushContract = "sforum.web-push.channel@1"
	webPushInput    = "sforum.web-push.channel.request@1"
)

var ErrChannelRuntimeUnavailable = errors.New("notification channel runtime is unavailable")

type webPushSubscriptionStore interface {
	CreateWebPushSubscription(context.Context, notifications.CreateWebPushSubscriptionInput) (notifications.WebPushSubscription, error)
	ListWebPushSubscriptions(context.Context, int64, bool) ([]notifications.WebPushSubscription, error)
	RevokeWebPushSubscription(context.Context, int64, int64) error
	ListChannelDeliveries(context.Context, int) ([]notifications.ChannelDelivery, error)
}

type ChannelRuntime interface {
	NotificationChannelInspection(context.Context) (extensions.ProviderSlotInspection, error)
	SelectNotificationChannel(context.Context, string, string, int64, int64, int64) (any, error)
	ResetNotificationChannel(context.Context, string, int64, int64, int64) error
	ProbeNotificationChannel(context.Context, string, string, string) (map[string]any, error)
}

func (h *Controller) WithChannels(runtime ChannelRuntime, auditor audit.IDWriter, outbox *notifications.Outbox) *Controller {
	if h != nil {
		h.channels, h.channelAuditor, h.outbox = runtime, auditor, outbox
	}
	return h
}

func (h *Controller) listChannels(c fiber.Ctx) error {
	if _, err := h.adminActor(c); err != nil {
		return err
	}
	if h.channels == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	inspection, err := h.channels.NotificationChannelInspection(c.Context())
	if err != nil {
		return err
	}
	items := make([]extensions.ProviderSlotInspectionItem, 0, 1)
	for _, item := range inspection.Slots {
		if item.Contract.Slot == webPushSlot {
			items = append(items, item)
		}
	}
	return apphttp.OK(c, map[string]any{"revision": inspection.Revision, "items": items})
}

type channelSelectionRequest struct {
	CandidateID      string `json:"candidateId"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

func (h *Controller) selectChannel(c fiber.Ctx) error {
	actor, err := h.adminActor(c)
	if err != nil {
		return err
	}
	if c.Params("channel") != "web_push" || h.channels == nil || h.channelAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var body channelSelectionRequest
	if err := c.Bind().Body(&body); err != nil || strings.TrimSpace(body.CandidateID) == "" || body.ExpectedRevision < 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.channel_unavailable")
	}
	auditID, err := h.channelAuditor.AppendReturningID(c.Context(), audit.Event{ActorUserID: actor.ID,
		Action: audit.ActionNotificationChannelSelect, Metadata: map[string]any{"channel": "web_push", "candidateId": body.CandidateID, "expectedRevision": body.ExpectedRevision}})
	if err != nil {
		return err
	}
	selection, err := h.channels.SelectNotificationChannel(c.Context(), webPushSlot, body.CandidateID, body.ExpectedRevision, actor.ID, auditID)
	if err != nil {
		if errors.Is(err, ErrChannelRuntimeUnavailable) {
			return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
		}
		return fiber.NewError(fiber.StatusConflict, "notification.policy_conflict")
	}
	return apphttp.OK(c, selection)
}

func (h *Controller) resetChannel(c fiber.Ctx) error {
	actor, err := h.adminActor(c)
	if err != nil {
		return err
	}
	if c.Params("channel") != "web_push" || h.channels == nil || h.channelAuditor == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var body struct {
		Revision int64 `json:"revision"`
	}
	if err := c.Bind().Body(&body); err != nil || body.Revision <= 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.channel_unavailable")
	}
	auditID, err := h.channelAuditor.AppendReturningID(c.Context(), audit.Event{ActorUserID: actor.ID,
		Action: audit.ActionNotificationChannelReset, Metadata: map[string]any{"channel": "web_push", "revision": body.Revision}})
	if err != nil {
		return err
	}
	err = h.channels.ResetNotificationChannel(c.Context(), webPushSlot, body.Revision, actor.ID, auditID)
	if err != nil {
		if errors.Is(err, ErrChannelRuntimeUnavailable) {
			return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
		}
		return fiber.NewError(fiber.StatusConflict, "notification.policy_conflict")
	}
	return apphttp.OK(c, map[string]any{"reset": true})
}

func (h *Controller) testChannel(c fiber.Ctx) error {
	actor, err := h.adminActor(c)
	if err != nil {
		return err
	}
	if c.Params("channel") != "web_push" || h.outbox == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	delivery, err := h.outbox.QueueChannel(c.Context(), notifications.CreateInput{
		RecipientUserID: actor.ID, Type: notifications.TypeAdminTest, Category: "system",
		PayloadVersion: 1, TargetType: "system", Payload: []byte(`{}`),
		DedupeKey: fmt.Sprintf("admin-channel-test:%d:%d", actor.ID, time.Now().UnixNano()),
	}, "web_push")
	if err != nil {
		return err
	}
	h.appendAudit(c, actor.ID, audit.ActionNotificationChannelTest, map[string]any{"channel": "web_push", "deliveryId": delivery.ID})
	return apphttp.JSON(c, fiber.StatusAccepted, apphttp.MessageOK, delivery)
}

func (h *Controller) listChannelDeliveries(c fiber.Ctx) error {
	if _, err := h.adminActor(c); err != nil {
		return err
	}
	if h.subscriptions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	items, err := h.subscriptions.ListChannelDeliveries(c.Context(), limit)
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) webPushConfig(c fiber.Ctx) error {
	if _, err := h.userID(c); err != nil {
		return err
	}
	if h.channels == nil {
		return apphttp.OK(c, map[string]any{"available": false, "workerPath": "/_sforum/notifications/sw.js", "scope": "/_sforum/notifications/"})
	}
	output, err := h.channels.ProbeNotificationChannel(c.Context(), webPushSlot, webPushContract, webPushInput)
	if err != nil {
		return apphttp.OK(c, map[string]any{"available": false, "workerPath": "/_sforum/notifications/sw.js", "scope": "/_sforum/notifications/"})
	}
	publicKey, _ := output["publicKey"].(string)
	ok, _ := output["ok"].(bool)
	return apphttp.OK(c, map[string]any{"available": ok && publicKey != "", "publicKey": publicKey,
		"workerPath": "/_sforum/notifications/sw.js", "scope": "/_sforum/notifications/"})
}

func (h *Controller) listWebPushSubscriptions(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.subscriptions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	items, err := h.subscriptions.ListWebPushSubscriptions(c.Context(), userID, true)
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) createWebPushSubscription(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.subscriptions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.subscription_invalid")
	}
	p256dh, pErr := decodeSubscriptionKey(body.Keys.P256DH)
	authKey, aErr := decodeSubscriptionKey(body.Keys.Auth)
	if pErr != nil || aErr != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.subscription_invalid")
	}
	item, err := h.subscriptions.CreateWebPushSubscription(c.Context(), notifications.CreateWebPushSubscriptionInput{
		UserID: userID, Endpoint: body.Endpoint, P256DHKey: p256dh, AuthKey: authKey, ContentEncoding: "aes128gcm",
	})
	if errors.Is(err, notifications.ErrSubscriptionInvalid) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.subscription_invalid")
	}
	if err != nil {
		return err
	}
	h.appendAudit(c, userID, audit.ActionNotificationSubscriptionCreate, map[string]any{"subscriptionId": item.ID})
	return apphttp.JSON(c, fiber.StatusCreated, apphttp.MessageOK, item)
}

func (h *Controller) revokeWebPushSubscription(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	id, parseErr := strconv.ParseInt(c.Params("id"), 10, 64)
	if parseErr != nil || id <= 0 || h.subscriptions == nil {
		return fiber.NewError(fiber.StatusNotFound, "notification.not_found")
	}
	if err := h.subscriptions.RevokeWebPushSubscription(c.Context(), userID, id); err != nil {
		return fiber.NewError(fiber.StatusNotFound, "notification.not_found")
	}
	h.appendAudit(c, userID, audit.ActionNotificationSubscriptionRevoke, map[string]any{"subscriptionId": id})
	return apphttp.OK(c, map[string]any{"revoked": true})
}

func decodeSubscriptionKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}
