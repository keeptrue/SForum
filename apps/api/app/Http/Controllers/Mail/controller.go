package mailcontroller

import (
	"context"

	"github.com/gofiber/fiber/v3"
	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

type Controller struct {
	extensions extensions.Store
	deliveries notifications.Store
	providers  *extensionsruntime.MailProviderRegistry
	users      identity.ActorStore
	sessions   *authsession.Manager
	options    *options.Service
	policy     notifications.CorePolicyStore
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
	group.Get("/policy", h.getPolicy)
	group.Put("/policy", h.updatePolicy)
	group.Post("/policy/restore", h.restorePolicy)
}
func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	actor, err := apphttp.LoadActor(c, h.sessions, h.users)
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
