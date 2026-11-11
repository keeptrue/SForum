package trustlevelscontroller

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// Controller 是信任等级的 HTTP 面（公开 1 条 + 后台 3 条）。
//
// 产品开关语义（D9）：`features.trust_levels` 关闭时四条路由**一律 404**——本刀之前
// 这些端点不存在，「逐字节等价」意味着 404，不是 403、不是空对象。
//
// 权限语义：路由目录里的声明只是目录，不是运行时守卫；`admin.access` / `trust.manage`
// 在这里由领域层用 actor.Can 真正执行（徽章那刀 P0 的教训）。
type Controller struct {
	options  *options.Service
	resolver *trustlevels.Resolver
	users    identity.ActorStore
	sessions *authsession.Manager

	// 以下四个缝只在测试里替换（生产装配留 nil 走 options / LoadActor）。
	// 它们存在的原因是：路由级断言（404/403/200/422）必须能在没有数据库的情况下跑。
	actorLoader    func(fiber.Ctx) (identity.Actor, error)
	featureEnabled func(context.Context) (bool, error)
	policyReader   func(context.Context) (trustlevels.Policy, error)
	configWriter   func(context.Context, identity.Actor, string) error
}

// WithActorLoader 替换 actor 装载（测试用）。
func (h *Controller) WithActorLoader(loader func(fiber.Ctx) (identity.Actor, error)) *Controller {
	h.actorLoader = loader
	return h
}

// WithFeatureGate 替换产品开关（测试用）。
func (h *Controller) WithFeatureGate(gate func(context.Context) (bool, error)) *Controller {
	h.featureEnabled = gate
	return h
}

// WithPolicyReader 替换策略读取（测试用）。
func (h *Controller) WithPolicyReader(reader func(context.Context) (trustlevels.Policy, error)) *Controller {
	h.policyReader = reader
	return h
}

// WithConfigWriter 替换配置写入（测试用）。
func (h *Controller) WithConfigWriter(writer func(context.Context, identity.Actor, string) error) *Controller {
	h.configWriter = writer
	return h
}

func NewController(
	optionsService *options.Service,
	resolver *trustlevels.Resolver,
	users identity.ActorStore,
	sessions *authsession.Manager,
) *Controller {
	return &Controller{options: optionsService, resolver: resolver, users: users, sessions: sessions}
}

func (h *Controller) RegisterRoutes(api fiber.Router) {
	api.Get("/trust-levels", h.getPublic)
	api.Get("/admin/trust-levels", h.getAdmin)
	api.Put("/admin/trust-levels", h.updateAdmin)
	api.Post("/admin/trust-levels/restore-defaults", h.restoreDefaults)
}

// ---- 公开面 ----

func (h *Controller) getPublic(c fiber.Ctx) error {
	enabled, err := h.enabled(c.Context())
	if err != nil {
		return err
	}
	if !enabled {
		return fiber.NewError(fiber.StatusNotFound, "trust_levels.not_found")
	}
	policy, err := h.policy(c.Context())
	if err != nil {
		return err
	}
	payload := buildPublicPayload(policy)

	// 进度只给**本人**（D1 隐私边界）：他人视角只有 level + levelSlug。
	if actor, ok := h.viewer(c); ok {
		payload.Viewer = h.viewerPayload(c.Context(), actor)
	}
	return apphttp.OK(c, payload)
}

func (h *Controller) viewerPayload(ctx context.Context, actor identity.Actor) *viewerTrustLevel {
	if !actor.TrustLevelResolved {
		return nil
	}
	viewer := &viewerTrustLevel{
		Level:     actor.TrustLevel,
		LevelSlug: trustlevels.Level(actor.TrustLevel).Slug(),
	}
	if actor.TrustLevel >= int(trustlevels.LevelLeader) || h.resolver == nil {
		return viewer
	}
	activity, err := h.resolver.Activity(ctx, actor.ID)
	if err != nil {
		return viewer
	}
	policy, err := h.policy(ctx)
	if err != nil {
		return viewer
	}
	if metric, current, threshold, ok := trustlevels.Progress(activity, trustlevels.Level(actor.TrustLevel), policy); ok {
		viewer.NextLevel = actor.TrustLevel + 1
		viewer.Bottleneck = &trustLevelBottleneck{Metric: metric, Current: current, Threshold: threshold}
	}
	return viewer
}

// ---- 后台面 ----

func (h *Controller) getAdmin(c fiber.Ctx) error {
	if _, err := h.requireAdmin(c, identity.PermissionAdminAccess); err != nil {
		return err
	}
	policy, err := h.policy(c.Context())
	if err != nil {
		return err
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "trust_levels.encode_failed")
	}
	return apphttp.OK(c, adminPayload{
		Config:      encoded,
		Levels:      buildAdminPayload(policy).Levels,
		Eligible:    nonNilStrings(trustlevels.EligiblePermissions()),
		Recommended: recommendedConfig(),
	})
}

func (h *Controller) updateAdmin(c fiber.Ctx) error {
	// 写权限在**解码之前**判：否则非法 body 会先返回 422，把「你没被授权」盖掉，
	// 审查也就无法区分「配置错」与「越权尝试」。
	actor, err := h.requireAdmin(c, identity.PermissionTrustManage)
	if err != nil {
		return err
	}
	var request adminUpdateRequest
	// 分工：请求体绑定失败属于传输层（JSON 坏、字段类型不符），这里就地回 422
	// `validation.invalid`；它**不经过** apphttp.MapOptionsError，因为那条路上没有
	// options 领域错误可映射。领域错误（含写库失败）一律走 MapOptionsError。
	if err := c.Bind().Body(&request); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	// 请求体就是 trust.levels 的 Config 形状：未知字段被 DisallowUnknownFields 拒绝，
	// 不存在「PUT 的 DTO 比 Config 宽松」的第二个形状。
	policy, err := trustlevels.DecodeConfig(request.Config)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "trust_levels.invalid_config")
	}
	encoded, err := trustlevels.EncodeConfig(policy)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "trust_levels.encode_failed")
	}
	if err := h.save(c.Context(), actor, encoded); err != nil {
		// 原始 err 只进日志，不进行响应：apphttp.MapOptionsError 负责把
		// 输入错误（422）与写库失败（500）分开，运维看状态码就知道该改配置还是查库。
		slog.WarnContext(c.Context(), "trust_levels save failed", "userId", actor.ID, "error", err)
		return apphttp.MapOptionsError(err)
	}
	return h.getAdmin(c)
}

func (h *Controller) restoreDefaults(c fiber.Ctx) error {
	actor, err := h.requireAdmin(c, identity.PermissionTrustManage)
	if err != nil {
		return err
	}
	if err := h.save(c.Context(), actor, recommendedConfig()); err != nil {
		slog.WarnContext(c.Context(), "trust_levels restore defaults failed", "userId", actor.ID, "error", err)
		return apphttp.MapOptionsError(err)
	}
	return h.getAdmin(c)
}

// ---- 基础设施 ----

func (h *Controller) enabled(ctx context.Context) (bool, error) {
	if h.featureEnabled != nil {
		return h.featureEnabled(ctx)
	}
	if h.options == nil {
		return false, nil
	}
	flags, err := h.options.FeatureFlags(ctx)
	if err != nil {
		return false, err
	}
	return flags.TrustLevels, nil
}

// policy 组装当前生效策略：trust.levels 单键 + 既有 trust.new_user_days（R6 单一来源）。
func (h *Controller) policy(ctx context.Context) (trustlevels.Policy, error) {
	if h.policyReader != nil {
		return h.policyReader(ctx)
	}
	if h.options == nil {
		return trustlevels.DefaultPolicy(), nil
	}
	raw, err := h.options.WebOption(ctx, options.NameTrustLevels)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	policy, err := trustlevels.DecodeConfig(raw)
	if err != nil {
		return trustlevels.Policy{}, err
	}
	trust, err := h.options.TrustPolicy(ctx)
	if err == nil && trust.NewUserDays >= 0 {
		policy.NewUserDays = trust.NewUserDays
	}
	return policy, nil
}

func (h *Controller) viewer(c fiber.Ctx) (identity.Actor, bool) {
	actor, err := h.loadActor(c)
	if err != nil || actor.ID <= 0 {
		return identity.Actor{}, false
	}
	return actor, true
}

// requireAdmin 先判产品开关（关闭 → 404），再判后台读权限（admin.access），
// 写接口由 options.Update 内部按 trust.levels 的 managePermission 再判一次。
func (h *Controller) requireAdmin(c fiber.Ctx, permission string) (identity.Actor, error) {
	enabled, err := h.enabled(c.Context())
	if err != nil {
		return identity.Actor{}, err
	}
	if !enabled {
		return identity.Actor{}, fiber.NewError(fiber.StatusNotFound, "trust_levels.not_found")
	}
	actor, err := h.loadActor(c)
	if err != nil {
		return identity.Actor{}, err
	}
	if !actor.Can(permission) {
		return identity.Actor{}, fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	return actor, nil
}

// loadActor 默认走 apphttp.LoadActor；测试通过 WithActorLoader 注入。
func (h *Controller) loadActor(c fiber.Ctx) (identity.Actor, error) {
	if h.actorLoader != nil {
		return h.actorLoader(c)
	}
	return apphttp.LoadActor(c, h.sessions, h.users)
}

// save 写回 trust.levels；写权限由 options.Update 内部按 managePermission 再判一次。
func (h *Controller) save(ctx context.Context, actor identity.Actor, value string) error {
	if h.configWriter != nil {
		return h.configWriter(ctx, actor, value)
	}
	_, err := h.options.Update(ctx, actor, options.UpdateInput{
		Name:  options.NameTrustLevels,
		Value: value,
	})
	return err
}

func recommendedConfig() string {
	encoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		return ""
	}
	return encoded
}
