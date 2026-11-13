package notificationscontroller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"
	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

const (
	maxPreferenceUpdates  = 200
	maxAdminPolicyUpdates = 500
	// 代理或浏览器未正确传播断开时，租期仍会回收订阅额度。
	notificationStreamMaxLifetime = time.Minute
)

type Controller struct {
	store         notifications.Store
	revisions     notifications.RecipientRevisionStore
	preferences   notifications.PreferenceStore
	adminPolicy   notifications.AdminPolicyStore
	sessions      *authsession.Manager
	users         identity.ActorStore
	auditor       audit.Writer
	targets       notifications.TargetVisibilityResolver
	previews      notifications.TargetPreviewResolver
	subscriptions webPushSubscriptionStore
	pushDevices   notifications.PushDeviceStore
	// celebrations 是庆祝展示层读面（见 Models/Notifications/celebrations.go）。
	// nil = 装配没接：端点回 503 而不是悄悄返回空列表，避免「庆祝功能关掉了」
	// 与「查出问题」两种状态在客户端无法区分。
	celebrations   notifications.CelebrationStore
	channels       ChannelRuntime
	channelAuditor audit.IDWriter
	outbox         *notifications.Outbox
	creator        interface {
		Create(context.Context, notifications.CreateInput) (notifications.Notification, error)
	}
}

func NewController(store notifications.Store, sessions *authsession.Manager, users identity.ActorStore, creator interface {
	Create(context.Context, notifications.CreateInput) (notifications.Notification, error)
}) *Controller {
	preferences, _ := store.(notifications.PreferenceStore)
	revisions, _ := store.(notifications.RecipientRevisionStore)
	adminPolicy, _ := store.(notifications.AdminPolicyStore)
	subscriptions, _ := store.(webPushSubscriptionStore)
	pushDevices, _ := store.(notifications.PushDeviceStore)
	celebrations, _ := store.(notifications.CelebrationStore)
	return &Controller{store: store, preferences: preferences, revisions: revisions, adminPolicy: adminPolicy, subscriptions: subscriptions, pushDevices: pushDevices, celebrations: celebrations, sessions: sessions, users: users, creator: creator}
}

func (h *Controller) WithAuditor(writer audit.Writer) *Controller {
	if h != nil {
		h.auditor = writer
	}
	return h
}

func (h *Controller) WithTargetVisibility(resolver notifications.TargetVisibilityResolver) *Controller {
	if h != nil {
		h.targets = resolver
	}
	return h
}
func (h *Controller) WithTargetPreview(resolver notifications.TargetPreviewResolver) *Controller {
	if h != nil {
		h.previews = resolver
	}
	return h
}
func (h *Controller) RegisterRoutes(api fiber.Router) {
	group := api.Group("/notifications")
	group.Get("", h.list)
	group.Get("/unread-count", h.unreadCount)
	group.Patch("/:id/read", h.markRead)
	group.Post("/read-all", h.markAllRead)
	group.Get("/stream", h.stream)
	// 庆祝展示层：必须在 `/:id` **之前**注册，否则 "celebrations" 会被当成 :id。
	group.Get("/celebrations", h.listCelebrations)
	group.Post("/celebrations/seen", h.acknowledgeCelebrations)
	group.Get("/:id", h.detail)
	preferences := api.Group("/notification-preferences")
	preferences.Get("", h.listPreferences)
	preferences.Put("", h.replacePreferences)
	preferences.Post("/restore", h.restorePreferences)
	admin := api.Group("/admin/notifications")
	admin.Get("/policy", h.listAdminPolicy)
	admin.Put("/policy", h.replaceAdminPolicy)
	admin.Post("/policy/restore", h.restoreAdminPolicy)
	admin.Post("/test", h.adminTest)
	admin.Get("/channels", h.listChannels)
	admin.Put("/channels/:channel", h.selectChannel)
	admin.Post("/channels/:channel/reset", h.resetChannel)
	admin.Post("/channels/:channel/test", h.testChannel)
	admin.Get("/deliveries", h.listChannelDeliveries)
	// 原生推送设备注册（App / 桌面端）：自服务，只允许当前用户操作自己的设备。
	pushDevices := api.Group("/push/devices")
	pushDevices.Get("", h.listPushDevices)
	pushDevices.Post("", h.registerPushDevice)
	pushDevices.Delete("/:deviceId", h.revokePushDevice)

	webPush := api.Group("/web-push")
	webPush.Get("/config", h.webPushConfig)
	webPush.Get("/subscriptions", h.listWebPushSubscriptions)
	webPush.Post("/subscriptions", h.createWebPushSubscription)
	webPush.Delete("/subscriptions/:id", h.revokeWebPushSubscription)
}

func (h *Controller) adminActor(c fiber.Ctx) (identity.Actor, error) {
	actor, err := apphttp.LoadActor(c, h.sessions, h.users)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.Can(identity.PermissionSettingsNotificationsManage) {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

type adminPolicyRequest struct {
	Revision int64                             `json:"revision"`
	Items    []notifications.AdminPolicyUpdate `json:"items"`
}

func (h *Controller) listAdminPolicy(c fiber.Ctx) error {
	if _, err := h.adminActor(c); err != nil {
		return err
	}
	if h.adminPolicy == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	catalog, err := h.adminPolicy.ListAdminPolicy(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, catalog)
}

func (h *Controller) replaceAdminPolicy(c fiber.Ctx) error {
	actor, err := h.adminActor(c)
	if err != nil {
		return err
	}
	if h.adminPolicy == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var request adminPolicyRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	if request.Revision <= 0 || len(request.Items) > maxAdminPolicyUpdates {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	catalog, err := h.adminPolicy.ReplaceAdminPolicy(c.Context(), request.Revision, request.Items)
	if errors.Is(err, notifications.ErrPolicyConflict) {
		return fiber.NewError(fiber.StatusConflict, "notification.policy_conflict")
	}
	if errors.Is(err, notifications.ErrPreferenceInvalid) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	if err != nil {
		return err
	}
	h.appendAudit(c, actor.ID, audit.ActionNotificationPolicyUpdate, map[string]any{
		"previousRevision": request.Revision,
		"itemCount":        len(request.Items),
	})
	return apphttp.OK(c, catalog)
}

func (h *Controller) restoreAdminPolicy(c fiber.Ctx) error {
	actor, err := h.adminActor(c)
	if err != nil {
		return err
	}
	if h.adminPolicy == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var request struct {
		Revision int64 `json:"revision"`
	}
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	if request.Revision <= 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	catalog, err := h.adminPolicy.RestoreAdminPolicy(c.Context(), request.Revision)
	if errors.Is(err, notifications.ErrPolicyConflict) {
		return fiber.NewError(fiber.StatusConflict, "notification.policy_conflict")
	}
	if err != nil {
		return err
	}
	h.appendAudit(c, actor.ID, audit.ActionNotificationPolicyRestore, map[string]any{
		"previousRevision": request.Revision,
	})
	return apphttp.OK(c, catalog)
}

func (h *Controller) stream(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.revisions == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var wakes <-chan struct{}
	release := func() {}
	if source, ok := h.revisions.(notifications.RecipientRevisionWakeStore); ok {
		wakes, release, err = source.SubscribeRevision(userID)
		if errors.Is(err, notifications.ErrRevisionConnectionLimit) {
			return fiber.NewError(fiber.StatusTooManyRequests, "notification.rate_limited")
		}
		if err != nil && !errors.Is(err, notifications.ErrRevisionWakeUnavailable) {
			return err
		}
	}
	clientRevision, _ := strconv.ParseInt(c.Get("Last-Event-ID"), 10, 64)
	if queryRevision, parseErr := strconv.ParseInt(c.Query("revision"), 10, 64); parseErr == nil && queryRevision >= 0 {
		clientRevision = queryRevision
	}
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-store, no-transform")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	baseContext := c.Context()
	serverDone := c.RequestCtx().Done()
	return c.SendStreamWriter(func(writer *bufio.Writer) {
		if release != nil {
			defer release()
		}
		streamContext, cancel := notificationStreamContext(baseContext)
		defer cancel()
		go func() {
			select {
			case <-serverDone:
				cancel()
			case <-streamContext.Done():
			}
		}()
		streamRevisionEvents(streamContext, writer, h.revisions, userID, wakes, clientRevision, 10*time.Second, 15*time.Second)
	})
}

func notificationStreamContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, notificationStreamMaxLifetime)
}

func streamRevisionEvents(ctx context.Context, writer *bufio.Writer, revisions notifications.RecipientRevisionStore, userID int64, wakes <-chan struct{}, clientRevision int64, heartbeatEvery, reconcileEvery time.Duration) {
	last, err := revisions.RecipientRevision(ctx, userID)
	if err != nil {
		return
	}
	if last != clientRevision && !writeRevisionEvent(writer, last) {
		return
	}
	reconcile := time.NewTicker(reconcileEvery)
	heartbeat := time.NewTicker(heartbeatEvery)
	defer reconcile.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wakes:
		case <-reconcile.C:
		case <-heartbeat.C:
			if _, err := writer.WriteString(": heartbeat\n\n"); err != nil || writer.Flush() != nil {
				return
			}
			continue
		}
		current, err := revisions.RecipientRevision(ctx, userID)
		if err != nil {
			return
		}
		if current != last {
			last = current
			if !writeRevisionEvent(writer, current) {
				return
			}
		}
	}
}

func writeRevisionEvent(writer *bufio.Writer, revision int64) bool {
	payload, _ := json.Marshal(map[string]int64{"revision": revision})
	_, err := fmt.Fprintf(writer, "id: %d\nevent: revision\ndata: %s\n\n", revision, payload)
	return err == nil && writer.Flush() == nil
}

func (h *Controller) adminTest(c fiber.Ctx) error {
	actor, err := h.adminActor(c)
	if err != nil {
		return err
	}
	item, err := h.creator.Create(c.Context(), notifications.CreateInput{
		RecipientUserID: actor.ID,
		Type:            notifications.TypeAdminTest,
		TargetType:      "system",
		Payload:         []byte(`{}`),
		DedupeKey:       fmt.Sprintf("admin_test:%d:%d", actor.ID, time.Now().UnixNano()),
	})
	if err != nil {
		return err
	}
	h.appendAudit(c, actor.ID, audit.ActionNotificationChannelTest, map[string]any{
		"channel": "in_app",
	})
	return apphttp.JSON(c, fiber.StatusCreated, apphttp.MessageOK, item)
}

type preferencesRequest struct {
	Revision int64                           `json:"revision"`
	Items    []notifications.PreferenceInput `json:"items"`
}

func (h *Controller) listPreferences(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.preferences == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	catalog, err := h.preferences.ListPreferences(c.Context(), userID)
	if err != nil {
		return err
	}
	return apphttp.OK(c, catalog)
}

func (h *Controller) replacePreferences(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.preferences == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var request preferencesRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	if request.Revision < 0 || len(request.Items) > maxPreferenceUpdates {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	catalog, err := h.preferences.ReplacePreferences(c.Context(), userID, request.Revision, request.Items)
	if errors.Is(err, notifications.ErrPreferenceConflict) {
		return fiber.NewError(fiber.StatusConflict, "notification.policy_conflict")
	}
	if errors.Is(err, notifications.ErrPreferenceInvalid) {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	if err != nil {
		return err
	}
	h.appendAudit(c, userID, audit.ActionNotificationPreferencesUpdate, map[string]any{
		"previousRevision": request.Revision,
		"itemCount":        len(request.Items),
	})
	return apphttp.OK(c, catalog)
}

func (h *Controller) restorePreferences(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.preferences == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var request struct {
		Revision int64 `json:"revision"`
	}
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	if request.Revision < 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.preference_invalid")
	}
	catalog, err := h.preferences.RestorePreferences(c.Context(), userID, request.Revision)
	if errors.Is(err, notifications.ErrPreferenceConflict) {
		return fiber.NewError(fiber.StatusConflict, "notification.policy_conflict")
	}
	if err != nil {
		return err
	}
	h.appendAudit(c, userID, audit.ActionNotificationPreferencesRestore, map[string]any{
		"previousRevision": request.Revision,
	})
	return apphttp.OK(c, catalog)
}

func (h *Controller) appendAudit(c fiber.Ctx, actorUserID int64, action string, metadata map[string]any) {
	if h == nil || h.auditor == nil {
		return
	}
	_ = h.auditor.Append(c.Context(), audit.Event{
		ActorUserID: actorUserID,
		Action:      action,
		Metadata:    metadata,
	})
}
func (h *Controller) userID(c fiber.Ctx) (int64, error) {
	id, ok, err := apphttp.ResolveUserID(c, h.sessions)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	return id, nil
}
func (h *Controller) list(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	beforeID, _ := strconv.ParseInt(c.Query("beforeId", "0"), 10, 64)
	var unread *bool
	if value := c.Query("unread"); value == "true" || value == "false" {
		parsed := value == "true"
		unread = &parsed
	}
	page, err := h.store.List(c.Context(), notifications.ListInput{RecipientUserID: userID, Limit: limit, BeforeID: beforeID, Category: c.Query("category"), Type: c.Query("type"), Unread: unread})
	if err != nil {
		return err
	}
	page, err = notifications.ResolveSafeTargets(c.Context(), h.targets, userID, page)
	if err != nil {
		return err
	}
	return apphttp.OK(c, page)
}

// celebrationAckRequest 是 POST /notifications/celebrations/seen 的请求体。
//
// 只有游标一个字段：庆祝层不维护「已读/未读」，它维护的是一个单调游标，
// 客户端每次把自己的当前游标报上来即可（乱序到达由 GREATEST 兜住）。
type celebrationAckRequest struct {
	CursorID int64 `json:"cursorId"`
}

func (h *Controller) listCelebrations(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.celebrations == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	feed, err := notifications.ListCelebrations(c.Context(), h.celebrations, h.targets, userID, notifications.DefaultCelebrationLimit)
	if err != nil {
		return err
	}
	// 响应含「我这个账号收到了什么」：必须是 private + no-store，
	// 与收件箱同口径（写 public 会把 A 的庆祝投给 B）。
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	return apphttp.OK(c, feed)
}

func (h *Controller) acknowledgeCelebrations(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.celebrations == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	var request celebrationAckRequest
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.celebration_invalid")
	}
	// 负游标按 0 处理（不报错）：它是客户端本地状态，钳到 0 等价于「什么都没看过」，
	// 而 GREATEST upsert 保证这次钳零不会把已推进的游标退回去。
	stored, err := notifications.AcknowledgeCelebrations(c.Context(), h.celebrations, userID, request.CursorID)
	if err != nil {
		return err
	}
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	return apphttp.OK(c, map[string]int64{"cursorId": stored})
}

func (h *Controller) detail(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "notification.not_found")
	}
	store, ok := h.store.(notifications.DetailStore)
	if !ok {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.channel_unavailable")
	}
	item, err := store.GetNotification(c.Context(), userID, id)
	if errors.Is(err, notifications.ErrNotificationNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "notification.not_found")
	}
	if err != nil {
		return err
	}
	detail, err := notifications.ResolveNotificationDetail(c.Context(), h.targets, h.previews, userID, item)
	if err != nil {
		return err
	}
	return apphttp.OK(c, detail)
}

func (h *Controller) unreadCount(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	count, err := h.store.UnreadCount(c.Context(), userID)
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]int64{"count": count})
}
func (h *Controller) markRead(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil || id <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "notification.not_found")
	}
	if err := h.store.MarkRead(c.Context(), userID, id); err != nil {
		if errors.Is(err, notifications.ErrNotificationNotFound) {
			return fiber.NewError(fiber.StatusNotFound, "notification.not_found")
		}
		return err
	}
	return apphttp.OK(c, map[string]bool{"read": true})
}
func (h *Controller) markAllRead(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	count, err := h.store.MarkAllRead(c.Context(), userID)
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]int64{"updated": count})
}
