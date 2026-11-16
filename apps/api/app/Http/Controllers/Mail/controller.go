package mailcontroller

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/gofiber/fiber/v3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	notificationjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Notifications"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// deliveryEnqueuer 是重放投递所需的最小入队能力（*supportjobs.Dispatcher 满足它）。
// 为 nil 时 replay 端点 fail-closed 成 503，而不是「重置了状态却没重新入队」。
type deliveryEnqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs, opts supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error)
}

type Controller struct {
	extensions   extensions.Store
	deliveries   notifications.Store
	providers    *extensionsruntime.MailProviderRegistry
	users        identity.ActorStore
	sessions     *authsession.Manager
	options      *options.Service
	policy       notifications.CorePolicyStore
	deliveryJobs deliveryEnqueuer
}

// WithDeliveryEnqueuer 注入重放投递的入队能力（bootstrap 传入 jobDispatcher）。
func (h *Controller) WithDeliveryEnqueuer(enqueuer deliveryEnqueuer) *Controller {
	h.deliveryJobs = enqueuer
	return h
}

func NewController(extensionStore extensions.Store, deliveries notifications.Store, providers *extensionsruntime.MailProviderRegistry, users identity.ActorStore, sessions *authsession.Manager, optionService *options.Service, policy notifications.CorePolicyStore) *Controller {
	return &Controller{extensions: extensionStore, deliveries: deliveries, providers: providers, users: users, sessions: sessions, options: optionService, policy: policy}
}
func (h *Controller) RegisterRoutes(api fiber.Router) {
	group := api.Group("/admin/mail")
	group.Get("/providers", h.listProviders)
	group.Put("/provider", h.selectProvider)
	group.Post("/provider/reset", h.resetProvider)
	group.Get("/deliveries", h.listDeliveries)
	// 重放终态失败的投递（failed）：重置成 queued 并重新入队（P2-6）。
	group.Post("/deliveries/:deliveryID/replay", h.replayDelivery)
	group.Get("/policy", h.getPolicy)
	group.Put("/policy", h.updatePolicy)
	group.Post("/policy/restore", h.restorePolicy)
}
func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	actor, err := apphttp.RequireActor(c, h.sessions, h.users)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.Can(identity.PermissionSettingsMailManage) {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}
func (h *Controller) authorize(c fiber.Ctx) error { _, err := h.actor(c); return err }
func (h *Controller) listProviders(c fiber.Ctx) error {
	if err := h.authorize(c); err != nil {
		return err
	}
	items, err := h.extensions.List(c.Context())
	if err != nil {
		return err
	}
	selection, state, err := h.providers.Resolve(c.Context())
	if err != nil {
		return err
	}
	providers := []map[string]any{}
	listed := map[string]bool{}
	for _, item := range items {
		if item.Status != extensions.StatusEnabled {
			continue
		}
		for _, provider := range item.Manifest.Providers {
			if provider.Slot == extensionsruntime.MailProviderSlot {
				providers = append(providers, map[string]any{
					"extensionId": item.ID,
					"label":       provider.Label,
					"healthy":     item.Runtime == nil || item.Runtime.State == extensions.RuntimeRunning,
					"status":      "enabled",
				})
				listed[item.ID] = true
			}
		}
	}
	// Keep the selected provider visible even while it is disabled or
	// restarting; the admin surface must not look like nothing is selected.
	if selection.ExtensionID != "" && !listed[selection.ExtensionID] {
		providers = append(providers, map[string]any{
			"extensionId": selection.ExtensionID,
			"label":       selection.Label,
			"healthy":     false,
			"status":      "unavailable",
		})
	}
	return apphttp.OK(c, map[string]any{
		"items":      providers,
		"selected":   selection,
		"configured": state != extensionsruntime.MailProviderUnconfigured,
		"state":      string(state),
	})
}

func (h *Controller) getPolicy(c fiber.Ctx) error {
	if _, err := h.actor(c); err != nil {
		return err
	}
	policy, err := h.readPolicy(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, policy)
}

func (h *Controller) updatePolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var policy options.NotificationPolicy
	if err := c.Bind().Body(&policy); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.policy_invalid")
	}
	if err := h.writePolicy(c.Context(), actor, policy); err != nil {
		return err
	}
	return apphttp.OK(c, policy)
}

func (h *Controller) restorePolicy(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.restoreCorePolicy(c.Context(), actor); err != nil {
		return err
	}
	policy, err := h.readPolicy(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, policy)
}

func (h *Controller) readPolicy(ctx context.Context) (options.NotificationPolicy, error) {
	if h.policy != nil {
		return h.policy.NotificationPolicy(ctx)
	}
	return h.options.NotificationPolicy(ctx)
}

func (h *Controller) writePolicy(ctx context.Context, actor identity.Actor, policy options.NotificationPolicy) error {
	if h.policy != nil {
		if err := h.policy.UpdateCoreNotificationPolicy(ctx, policy); err != nil {
			return err
		}
	}
	_, err := h.options.UpdateMany(ctx, actor, options.NotificationPolicyUpdateInputs(policy))
	return err
}

func (h *Controller) restoreCorePolicy(ctx context.Context, actor identity.Actor) error {
	if h.policy != nil {
		if err := h.policy.RestoreCoreNotificationPolicy(ctx); err != nil {
			return err
		}
	}
	_, err := h.options.UpdateMany(ctx, actor, options.NotificationPolicyRecommendedInputs())
	return err
}

type selectRequest struct {
	ExtensionID string `json:"extensionId"`
}

func (h *Controller) selectProvider(c fiber.Ctx) error {
	if err := h.authorize(c); err != nil {
		return err
	}
	var req selectRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "mail.provider_invalid")
	}
	if err := h.providers.Select(c.Context(), req.ExtensionID); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "mail.provider_unavailable")
	}
	return apphttp.OK(c, map[string]bool{"selected": true})
}
func (h *Controller) resetProvider(c fiber.Ctx) error {
	if err := h.authorize(c); err != nil {
		return err
	}
	if err := h.providers.RestoreDefault(c.Context()); err != nil {
		return err
	}
	selection, state, err := h.providers.Resolve(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{
		"selected":         selection,
		"configured":       state != extensionsruntime.MailProviderUnconfigured,
		"state":            string(state),
		"secretsPreserved": true,
	})
}
func (h *Controller) listDeliveries(c fiber.Ctx) error {
	if err := h.authorize(c); err != nil {
		return err
	}
	items, err := h.deliveries.ListDeliveries(c.Context(), 50)
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

// replayDelivery 把一条终态失败的投递重置为 queued 并重新入队。
//
// 权限：settings.mail.manage（与其它管理面一致）。状态判定在 store 层走
// outbox.CanReplay + CanTransition；这里只负责把「状态不允许」翻成 409、
// 「找不到」翻成 404，并在入队失败时把结论如实告诉调用方。
func (h *Controller) replayDelivery(c fiber.Ctx) error {
	if err := h.authorize(c); err != nil {
		return err
	}
	if h.deliveryJobs == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	deliveryID, err := strconv.ParseInt(c.Params("deliveryID"), 10, 64)
	if err != nil || deliveryID <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "mail.delivery_not_found")
	}
	delivery, err := h.deliveries.ReplayDelivery(c.Context(), deliveryID)
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrDeliveryNotFound):
			return fiber.NewError(fiber.StatusNotFound, "mail.delivery_not_found")
		case errors.Is(err, notifications.ErrDeliveryNotReplayable):
			// 409 而不是 422：客户端拿到的是「当前状态不允许」，重试同一个请求
			// 在有新状态之前没有意义，但也可能因为并发重放成功而改变。
			return fiber.NewError(fiber.StatusConflict, "mail.delivery_not_replayable")
		default:
			return err
		}
	}
	if _, err := h.deliveryJobs.Enqueue(c.Context(), notificationjobs.DeliverMailArgs{DeliveryID: delivery.ID},
		notificationjobs.DeliverMailArgs{}.EnqueueOptions()); err != nil {
		// 入队失败必须补偿：状态已经被重置成 queued，若就此返回 503，行会停在
		// 「queued 但没有 job」，既不会被重试也不会进终态（P2-6 补刀）。
		// 补偿本身失败也不阻断响应——mail.sweep_stale 会把停在 queued 的行收成
		// failed，这是最后一道兜底。
		if compensateErr := h.deliveries.FailReplayDelivery(c.Context(), delivery.ID, "replay_enqueue_failed"); compensateErr != nil {
			slog.Default().Error("mail delivery replay compensation failed",
				"deliveryId", delivery.ID, "error", compensateErr)
		}
		return fiber.NewError(fiber.StatusServiceUnavailable, "mail.delivery_replay_enqueue_failed")
	}
	return apphttp.OK(c, map[string]any{"delivery": delivery})
}
