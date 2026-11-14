package trustlevelscontroller

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// CodeInvalidConfig 是配置解码失败的**唯一**机器码（D-N14）。
//
// 前端与契约为它分流；字段级明细放在同一个响应的 details.errors 里，不按档位变码——
// 一变码，前端的「配置无效」处理就得先枚举全部码，等于把校验细节搬进前端。
const CodeInvalidConfig = "trust_levels.invalid_config"

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

	// 规则编辑面依赖（M1，见 WithRuleEditingDeps）。三者都可能为 nil：
	//   - refCandidates 为 nil → 目录端点下发 unavailable 候选、写路径跳过引用校验；
	//   - factCandidates 为 nil → 没有插件事实候选（`fact.count` 不出现在目录里）；
	//   - impactEstimator 为 nil → 估算端点返回 500（它会真的查库，不能假装算过）。
	refCandidates   trustlevels.RefCandidateSource
	factCandidates  func() []conditions.FactCandidate
	impactEstimator trustlevels.RuleImpactEstimator
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
	// 两条静态段：与 `/admin/trust-levels` 同前缀但不同段数，Fiber 不会混淆；
	// 仍排在裸路径之前，与徽章那刀「静态段永远先注册」的教训保持一致。
	api.Get("/admin/trust-levels/conditions", h.adminListRuleConditions)
	api.Post("/admin/trust-levels/rule-impact", h.adminEstimateRuleImpact)
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
	rank := trustlevels.Level(actor.TrustLevel)
	policy, err := h.policy(ctx)
	viewer := &viewerTrustLevel{
		Level:     actor.TrustLevel,
		LevelSlug: viewerLevelSlug(policy, rank),
	}
	if err != nil || h.resolver == nil {
		return viewer
	}
	// 「已满级」的判据跟着配置走（LastRank），不是内置的 TL4：运营删档/加档之后
	// 钉死 4 会让 5 档站点永远显示「还差一点到 TL5」。
	if rank >= policy.LastRank() {
		return viewer
	}
	activity, err := h.resolver.Activity(ctx, actor.ID)
	if err != nil {
		return viewer
	}
	if metric, current, threshold, ok := trustlevels.Progress(activity, rank, policy); ok {
		viewer.NextLevel = actor.TrustLevel + 1
		viewer.Bottleneck = &trustLevelBottleneck{Metric: metric, Current: current, Threshold: threshold}
	}
	return viewer
}

// viewerLevelSlug 把 rank 折成契约里的 levelSlug（= 档位身份 id）。
//
// 策略读不到时回落到内置 id：配置暂时坏掉不应该让等级卡的标识变成空串，
// 那会把「配置错误」伪装成「这个人没有等级」。
func viewerLevelSlug(policy trustlevels.Policy, rank trustlevels.Level) string {
	if id := policy.TierID(rank); id != "" {
		return id
	}
	return trustlevels.BuiltinTierID(rank)
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
		Config:       encoded,
		Levels:       buildAdminLevels(policy),
		Eligible:     nonNilStrings(trustlevels.EligiblePermissions()),
		Baseline:     nonNilStrings(trustlevels.BaselineGrants()),
		Recommended:  recommendedConfig(),
		IconChoices:  nonNilStrings(trustlevels.TierIcons()),
		ColorChoices: nonNilStrings(trustlevels.TierColors()),
		NamePool:     toTrustLevelNamePool(trustlevels.TierNamePool()),
		TierLimits:   tierCountBounds{Min: trustlevels.MinTiers, Max: trustlevels.MaxTiers},
		NextTierSeq:  policy.NextTierSeq(),
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
		// 传输层只做投影，不重新判断业务规则：明细来自领域层的 *ConfigErrors。
		return configErrorResponse(c, err)
	}
	// 引用存在性检查是**写路径专属**（DecodeConfig 无 ctx 无库，读路径也会调它）。
	// 主题/分类/角色被删之后，旧规则仍要能读出来（运行时该叶子返回确定值 false/0）；
	// 但新存一份引用已死对象的规则会立刻 422，否则那条条件永远不成立而没人知道为什么。
	referenceDetails, referenceErr := h.ruleReferenceDetails(c.Context(), policy)
	if referenceErr != nil {
		slog.WarnContext(c.Context(), "trust_levels rule reference check failed", "error", referenceErr)
		return fiber.NewError(fiber.StatusInternalServerError, "trust_levels.reference_check_failed")
	}
	if len(referenceDetails) > 0 {
		return configErrorResponse(c, &trustlevels.ConfigErrors{Details: referenceDetails})
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

// ruleReferenceDetails 在写路径校验规则里的对象引用是否仍然存在。
//
// 两个返回值分工明确：`details` 是**运营可修的 422**（引用的对象没了），`err` 是
// **环境故障**（读候选失败 / 未装配），后者必须与前者走不同状态码——把读库失败
// 报成 422 会让运营去改一份本来正确的配置。
//
// 未装配候选来源时**跳过**：生产装配一定会注入（bootstrap）；单元测试不必为一条
// 422 用例去造整套 badges store。跳过是显式且唯一的宽容点。
func (h *Controller) ruleReferenceDetails(ctx context.Context, policy trustlevels.Policy) ([]trustlevels.FieldError, error) {
	if h.refCandidates == nil {
		return nil, nil
	}
	details, err := trustlevels.RuleReferenceIssues(ctx, policy, h.refCandidates, conditions.FactKeys(h.declaredFactCandidates()))
	if err != nil {
		// 读候选失败 = 无法判定引用是否成立。必须吵，不能当成「没有失效引用」放行：
		// 那会让「删了分类后仍能保存引用它的规则」变成默认行为。
		return nil, err
	}
	return details, nil
}

func (h *Controller) restoreDefaults(c fiber.Ctx) error {
	actor, err := h.requireAdmin(c, identity.PermissionTrustManage)
	if err != nil {
		return err
	}
	// 读当前策略只为拿墓碑：恢复默认是整份覆盖（D-N15），但被删掉的档位 id 必须
	// 留下来，否则这个按钮会悄悄打破同一份契约里的「id 永不复用」。
	// 读不出来时退回纯推荐值：配置已经坏掉的站点更需要「恢复默认」可用。
	previous, err := h.policy(c.Context())
	if err != nil {
		slog.WarnContext(c.Context(), "trust_levels restore defaults: read previous policy failed",
			"userId", actor.ID, "error", err)
		previous = trustlevels.Policy{}
	}
	encoded, err := trustlevels.EncodeConfig(trustlevels.RestoreDefaults(previous))
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "trust_levels.encode_failed")
	}
	if err := h.save(c.Context(), actor, encoded); err != nil {
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

// configErrorResponse 把解码错误投影成 422：机器码不变，明细变细（D-N14）。
//
// 明细可能为空（结构化错误之外的兜底），那种情况回落到旧的「只给统一码」行为——
// 前端不能因为一条没有明细的错误就崩掉。
func configErrorResponse(c fiber.Ctx, err error) error {
	details := trustlevels.ConfigErrorDetails(err)
	if len(details) == 0 {
		return fiber.NewError(fiber.StatusUnprocessableEntity, CodeInvalidConfig)
	}
	projected := make([]trustLevelConfigError, 0, len(details))
	for _, detail := range details {
		projected = append(projected, trustLevelConfigError{
			Rank:       int(detail.Rank),
			TierID:     detail.TierID,
			Field:      detail.Field,
			ReasonCode: detail.Code,
			Params:     detail.Params,
			Reason:     detail.Reason,
		})
	}
	return apphttp.NewErrorWithDetails(fiber.StatusUnprocessableEntity, CodeInvalidConfig, map[string]any{
		"errors": projected,
	})
}

func recommendedConfig() string {
	encoded, err := trustlevels.EncodeConfig(trustlevels.DefaultPolicy())
	if err != nil {
		return ""
	}
	return encoded
}
