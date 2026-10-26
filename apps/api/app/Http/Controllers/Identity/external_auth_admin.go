package identitycontroller

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// 管理员 Login Methods 界面（见 plans/2026-07-27-github-social-login-builtin-plugin.md M3 / T1C / T8B）。
//
// 端点：
//   - GET    /admin/identity/providers         — Host 聚合（package catalog + live Registry）；
//     返回**仅已启用并存在的**提供商（live RuntimeInstanceID 存在）；未启用的 discovered-only 不再展示。
//   - PATCH  /admin/identity/providers/{pid}    — CAS 激活/排序（ownership 仅 Host 派生）
//   - POST   /admin/identity/providers/{pid}/probe — 真实 provider.probe 运行时操作
//   - POST   /admin/identity/providers/reset    — 还原推荐默认（操作全 off，保留 secrets）
//
// 权限：identity.provider.manage；executable trust 仍 super_admin-only。
// 展示信息全部来自 Host catalog + Manifest，不含 `if provider == github` 分支。
//
// 状态语义（UI 必须区分，不可混用）：
//   - discovered：包目录可见（含未信任/未启用）
//   - trusted：exact-artifact 可执行信任已授予，或 live Registry 带 VersionID+digest
//   - enabled：live RuntimeInstanceID 存在（进程可执行）
//   - configured：必需设置已齐（IsProviderConfigured；未接线时 false）
//   - probed：最近一次探测 ok=true（pending/unavailable 永不当成功）
//   - publiclyActivated：Host 激活目录至少开启 login/registration/link 之一，
//     且 artifactBound + enabled + configured + 非 Safe Mode；不等于插件 enabled
//
// T8B：live Registry 保持可执行权威；package catalog 不得写入 Registry。

// adminListIdentityProviders 返回管理员视角的 provider 聚合。
func (h *Controller) adminListIdentityProviders(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionIdentityProviderManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	items, err := h.listAdminIdentityProviderItems(c.Context(), apphttp.Locale(c))
	if err != nil {
		return err
	}
	return apphttp.OK(c, items)
}

// listAdminIdentityProviderItems 构建 Host 管理员聚合（无权限检查，便于单测）。
// locale 仅解析插件声明的 label/icon 展示元数据。
func (h *Controller) listAdminIdentityProviderItems(ctx context.Context, locale string) ([]adminIdentityProviderItem, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	safeMode := false
	if h.providerCatalog != nil {
		safeMode = h.providerCatalog.Snapshot().SafeMode
	}

	// byID 合并：package catalog（discovered） + live Registry（可执行权威） + activation。
	byID := map[string]*adminIdentityProviderItem{}

	// 1. Host 扩展/包目录：enable 前即可 discovered；disabled/drifted 仍可检查。
	if h.packageCatalog != nil {
		candidates, err := h.packageCatalog.ListAuthProviderCandidates(ctx)
		if err != nil {
			// 包目录失败 fail closed：不得静默只返回 live 子集（会隐藏 staged 内置）。
			return nil, fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
		}
		for _, candidate := range candidates {
			candidate = identity.NormalizeAuthProviderPackageCandidate(candidate)
			if candidate.ProviderID == "" || candidate.Kind != identityregistry.ProviderKindAuth {
				continue
			}
			item := &adminIdentityProviderItem{
				ID:                    candidate.ProviderID,
				Kind:                  candidate.Kind,
				ContractVersion:       candidate.ContractVersion,
				Priority:              candidate.Priority,
				Operations:            append([]string(nil), candidate.Operations...),
				OwnerExtensionID:      candidate.OwnerExtensionID,
				OwnerExtensionVersion: candidate.OwnerExtensionVersion,
				OwnerPackageDigest:    candidate.OwnerPackageDigest,
				Discovered:            true,
				// 包目录 Trusted 来自 lifecycle grant 投影；可执行仍看 live Registry。
				Trusted:      candidate.Trusted,
				Enabled:      false, // 可执行 enabled 仅由 live RuntimeInstanceID 决定
				CallbackPath: identity.ExternalAuthCallbackPath(candidate.ProviderID),
				SettingsPath: adminIdentityProviderSettingsPath(candidate.OwnerExtensionID),
				SafeMode:     safeMode,
				Label: identityregistry.ResolveProviderLabel(identityregistry.Provider{
					Label:        candidate.Label,
					LabelLocales: candidate.LabelLocales,
				}, locale),
				Icon: strings.TrimSpace(candidate.Icon),
			}
			if abs, absErr := h.absoluteCallbackURL(ctx, candidate.ProviderID); absErr == nil {
				item.CallbackURL = abs
			}
			byID[candidate.ProviderID] = item
		}
	}

	// 2. live Identity Registry：可执行权威（RuntimeInstanceID / 精确 artifact / 绑定 schema ops）。
	if h.providerCatalog != nil {
		for _, provider := range h.providerCatalog.Providers(identityregistry.ProviderKindAuth) {
			if provider.Artifact.Core {
				continue
			}
			ops := make([]string, 0, len(provider.Operations))
			for _, op := range provider.Operations {
				ops = append(ops, op.Name)
			}
			callbackPath := identity.ExternalAuthCallbackPath(provider.ID)
			callbackURL := ""
			if abs, absErr := h.absoluteCallbackURL(ctx, provider.ID); absErr == nil {
				callbackURL = abs
			}
			label := identityregistry.ResolveProviderLabel(provider.Provider, locale)
			icon := strings.TrimSpace(provider.Icon)
			liveTrusted := provider.Artifact.PackageDigest != "" && provider.Artifact.VersionID != 0
			liveEnabled := strings.TrimSpace(provider.Artifact.RuntimeInstanceID) != ""

			existing, found := byID[provider.ID]
			if !found {
				existing = &adminIdentityProviderItem{
					ID:           provider.ID,
					Kind:         provider.Kind,
					Discovered:   true,
					CallbackPath: callbackPath,
					SafeMode:     safeMode,
				}
				byID[provider.ID] = existing
			}
			// live 覆盖可执行字段；展示元数据优先 live（exact 运行制品），否则保留包目录。
			existing.ContractVersion = firstNonEmpty(provider.ContractVersion, existing.ContractVersion)
			if provider.Priority != 0 || existing.Priority == 0 {
				existing.Priority = provider.Priority
			}
			if len(ops) > 0 {
				existing.Operations = ops
			}
			existing.OwnerExtensionID = firstNonEmpty(provider.Artifact.ExtensionID, existing.OwnerExtensionID)
			existing.OwnerExtensionVersion = firstNonEmpty(provider.Artifact.ExtensionVersion, existing.OwnerExtensionVersion)
			existing.OwnerPackageDigest = firstNonEmpty(provider.Artifact.PackageDigest, existing.OwnerPackageDigest)
			existing.RuntimeInstanceID = provider.Artifact.RuntimeInstanceID
			existing.Discovered = true
			existing.Trusted = existing.Trusted || liveTrusted
			existing.Enabled = liveEnabled
			existing.CallbackPath = callbackPath
			existing.CallbackURL = callbackURL
			existing.SettingsPath = adminIdentityProviderSettingsPath(existing.OwnerExtensionID)
			existing.SafeMode = safeMode
			if label != "" {
				existing.Label = label
			}
			if icon != "" {
				existing.Icon = icon
			}
			// 配置门控：未接线时保持 false（不得臆测为已配置）。
			if h.externalAuthService != nil {
				if configured, cfgErr := h.externalAuthService.IsProviderConfigured(ctx, provider.ID); cfgErr == nil {
					existing.Configured = configured
				}
			}
		}
	}

	// 3. 合并 Host 激活目录状态（失败 fail closed）。
	items := make([]adminIdentityProviderItem, 0, len(byID))
	for _, item := range byID {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		return items[i].ID < items[j].ID
	})

	if h.activationStore != nil {
		activations, err := h.activationStore.List(ctx)
		if err != nil {
			return nil, fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
		}
		byAct := map[string]identity.ProviderActivation{}
		for _, a := range activations {
			byAct[a.ProviderID] = a
		}
		for i := range items {
			if a, ok := byAct[items[i].ID]; ok {
				items[i].LoginEnabled = a.LoginEnabled
				items[i].RegistrationEnabled = a.RegistrationEnabled
				items[i].LinkEnabled = a.LinkEnabled
				items[i].Priority = a.Priority
				items[i].Revision = a.Revision
				// 绑定 artifact 与 live 不一致时：仍展示激活意图，但标记 artifact 漂移。
				items[i].ArtifactBound = a.OwnerPackageDigest != "" &&
					a.OwnerPackageDigest == items[i].OwnerPackageDigest &&
					a.OwnerExtensionID == items[i].OwnerExtensionID
				intentOn := a.LoginEnabled || a.RegistrationEnabled || a.LinkEnabled
				items[i].Activated = intentOn
				items[i].PubliclyActivated = intentOn && items[i].ArtifactBound && !safeMode && items[i].Enabled && items[i].Configured
				items[i].LastProbeAt = a.LastProbeAt
				items[i].LastProbeOK = a.LastProbeOK
				items[i].LastProbeReason = a.LastProbeReason
			}
			// probe_pending / probe_unavailable 永不当作成功健康检查。
			if items[i].LastProbeReason == identity.ProbeReasonPending ||
				items[i].LastProbeReason == identity.ProbeReasonUnavailable {
				falseVal := false
				items[i].LastProbeOK = &falseVal
				items[i].Probed = false
			} else if items[i].LastProbeOK != nil {
				items[i].Probed = *items[i].LastProbeOK
			}
			// 包目录-only 候选：仍可报告 configured（若扩展 settings 可解析）。
			if !items[i].Configured && h.externalAuthService != nil && items[i].OwnerExtensionID != "" {
				if configured, cfgErr := h.externalAuthService.IsProviderConfigured(ctx, items[i].ID); cfgErr == nil {
					items[i].Configured = configured
				}
			}
		}
	}

	// 仅显示已启用并存在的提供商插件（live RuntimeInstanceID 存在）。
	// 包目录-only 的 pre-enable discovered 不再在管理员列表中展示（直到启用后存在）。
	// 这满足「后台登录方式页面只加载显示符合条件且已启用并存在的提供商插件」需求。
	items = slices.DeleteFunc(items, func(i adminIdentityProviderItem) bool {
		return !i.Enabled
	})

	return items, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type adminIdentityProviderItem struct {
	ID                    string   `json:"id"`
	Kind                  string   `json:"kind"`
	ContractVersion       string   `json:"contractVersion"`
	Priority              int      `json:"priority"`
	Operations            []string `json:"operations"`
	OwnerExtensionID      string   `json:"ownerExtensionId"`
	OwnerExtensionVersion string   `json:"ownerExtensionVersion,omitempty"`
	OwnerPackageDigest    string   `json:"ownerPackageDigest"`
	Discovered            bool     `json:"discovered"`
	Trusted               bool     `json:"trusted"`
	Enabled               bool     `json:"enabled"`
	Configured            bool     `json:"configured"`
	Probed                bool     `json:"probed"`
	ArtifactBound         bool     `json:"artifactBound"`
	RuntimeInstanceID     string   `json:"runtimeInstanceId,omitempty"`
	// Activated 表示 Host 激活意图（任一操作开关为 on），含 artifact 漂移时的意图。
	Activated bool `json:"activated"`
	// PubliclyActivated 表示对访客有效：意图 on + artifactBound + enabled + configured + 非 Safe Mode。
	PubliclyActivated   bool   `json:"publiclyActivated"`
	LoginEnabled        bool   `json:"loginEnabled"`
	RegistrationEnabled bool   `json:"registrationEnabled"`
	LinkEnabled         bool   `json:"linkEnabled"`
	Revision            int64  `json:"revision"`
	CallbackPath        string `json:"callbackPath"`
	// CallbackURL 优先由后台 site.url 派生，未设置时回退 APP_URL；无法形成时为空字符串。
	CallbackURL  string `json:"callbackUrl,omitempty"`
	SettingsPath string `json:"settingsPath,omitempty"`
	SafeMode     bool   `json:"safeMode"`
	// Label / Icon 来自插件 Identity 声明（Host 按 locale 解析）；Core 不得按 id 猜品牌。
	Label           string     `json:"label,omitempty"`
	Icon            string     `json:"icon,omitempty"`
	LastProbeAt     *time.Time `json:"lastProbeAt,omitempty"`
	LastProbeOK     *bool      `json:"lastProbeOk,omitempty"`
	LastProbeReason string     `json:"lastProbeReason,omitempty"`
}

// adminIdentityProviderSettingsPath 指向扩展 Host 渲染设置页（与 adminModules 子路径一致，不含 /admin 前缀）。
func adminIdentityProviderSettingsPath(extensionID string) string {
	extensionID = strings.TrimSpace(extensionID)
	if extensionID == "" {
		return ""
	}
	return "/extensions/" + extensionID + "/pages/settings"
}

// adminPatchIdentityProvider CAS 更新 provider 激活/排序。
// ownership / package digest 仅从 live Registry 派生；浏览器声明被忽略并拒绝权威。
func (h *Controller) adminPatchIdentityProvider(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionIdentityProviderManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.activationStore == nil || h.externalAuthService == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("providerId")))
	if providerID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	var req patchIdentityProviderRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	// 拒绝浏览器 ownership/artifact 作为权威：若客户端提交了非空声明，直接 422。
	if strings.TrimSpace(req.OwnerExtensionID) != "" || strings.TrimSpace(req.OwnerPackageDigest) != "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.provider_ownership_rejected")
	}

	input, prepErr := h.externalAuthService.PrepareActivationInput(
		providerID,
		req.LoginEnabled, req.RegistrationEnabled, req.LinkEnabled,
		req.Priority, req.ExpectedRevision,
	)
	if prepErr != nil {
		return mapActivationMutationError(prepErr)
	}
	updated, err := h.activationStore.Upsert(c.Context(), input)
	if err != nil {
		if errors.Is(err, identity.ErrProviderActivationNoMutation) {
			// 无实质变更：返回当前状态，不写 audit（无 mutation）。
			return apphttp.OK(c, updated)
		}
		return mapActivationMutationError(err)
	}
	h.auditProviderActivation(c, actor.ID, identity.AuditActionProviderActivationUpdate, map[string]any{
		"providerId":          updated.ProviderID,
		"ownerExtensionId":    updated.OwnerExtensionID,
		"ownerPackageDigest":  updated.OwnerPackageDigest,
		"loginEnabled":        updated.LoginEnabled,
		"registrationEnabled": updated.RegistrationEnabled,
		"linkEnabled":         updated.LinkEnabled,
		"priority":            updated.Priority,
		"revision":            updated.Revision,
		"expectedRevision":    req.ExpectedRevision,
	})
	return apphttp.OK(c, updated)
}

// patchIdentityProviderRequest 故意不把 ownership 当作可写权威字段。
// 若客户端仍提交 ownerExtensionId/ownerPackageDigest，handler 拒绝。
type patchIdentityProviderRequest struct {
	OwnerExtensionID    string `json:"ownerExtensionId"`
	OwnerPackageDigest  string `json:"ownerPackageDigest"`
	ExpectedRevision    int64  `json:"expectedRevision"`
	LoginEnabled        *bool  `json:"loginEnabled"`
	RegistrationEnabled *bool  `json:"registrationEnabled"`
	LinkEnabled         *bool  `json:"linkEnabled"`
	Priority            *int   `json:"priority"`
}

// adminProbeIdentityProvider 触发真实 provider.probe 运行时操作（T8B）。
//
// 规则：
//   - Safe Mode / 无 live 可执行进程 / 未声明 provider.probe → probe_unavailable（ok=false）
//   - 成功调用插件 Probe 后持久化真实 ok/reason（可 ok=true）
//   - probe_pending 不是产品 probe 实现，不得作为成功路径写入
//   - reason/message 经 Host 脱敏；不得证明 Client Secret 正确性
func (h *Controller) adminProbeIdentityProvider(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionIdentityProviderManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.activationStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("providerId")))
	if providerID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}

	probedAt := time.Now()
	ok := false
	reason := identity.ProbeReasonUnavailable
	message := ""

	// Safe Mode：第三方可执行面关闭。
	if h.providerCatalog != nil && h.providerCatalog.Snapshot().SafeMode {
		reason = identity.ProbeReasonUnavailable
	} else if h.authFlow == nil {
		// 无 AuthProviderFlow：无法调用精确运行时。
		reason = identity.ProbeReasonUnavailable
	} else {
		probeResult, probeErr := h.authFlow.Probe(c.Context(), providerID)
		if probeErr != nil {
			// 解析失败 / 无 runtime / 未声明操作 → unavailable（非 pending 占位）。
			reason = identity.ProbeReasonUnavailable
			// 保留稳定短码，避免把内部错误细节泄漏给浏览器。
			if errors.Is(probeErr, identity.ErrAuthProviderNotFound) {
				reason = identity.ProbeReasonUnavailable
			}
		} else {
			ok = probeResult.OK
			reason = probeResult.Reason
			message = probeResult.Message
			// 防御：插件若仍返回 probe_pending，Host 降级为 unavailable。
			if reason == identity.ProbeReasonPending {
				ok = false
				reason = identity.ProbeReasonUnavailable
			}
		}
	}

	result := identity.ProviderActivationProbeResult{
		ProviderID: providerID,
		OK:         ok,
		Reason:     reason,
		At:         probedAt,
	}
	if err := h.activationStore.RecordProbe(c.Context(), result); err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	auditMeta := map[string]any{
		"providerId": providerID,
		"ok":         ok,
		"reason":     reason,
		"probedAt":   probedAt.UTC().Format(time.RFC3339Nano),
	}
	if message != "" {
		auditMeta["message"] = message
	}
	h.auditProviderActivation(c, actor.ID, identity.AuditActionProviderProbe, auditMeta)
	resp := map[string]any{
		"providerId": providerID,
		"ok":         ok,
		"status":     reason,
		"reason":     reason,
		"probedAt":   probedAt,
	}
	if message != "" {
		resp["message"] = message
	}
	return apphttp.OK(c, resp)
}

// adminResetIdentityProviders 还原推荐默认：所有 provider 操作全 off、priority=0。
// 保留 secrets（由 settings 层独立管理），UI 须明示这一点。
func (h *Controller) adminResetIdentityProviders(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionIdentityProviderManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.activationStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	activations, err := h.activationStore.List(c.Context())
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	resetCount := 0
	for _, a := range activations {
		if _, err := h.activationStore.ResetOperationsToDefaults(c.Context(), a.ProviderID); err != nil {
			if errors.Is(err, identity.ErrProviderActivationNoMutation) {
				continue
			}
			return mapActivationMutationError(err)
		}
		resetCount++
	}
	if resetCount > 0 {
		h.auditProviderActivation(c, actor.ID, identity.AuditActionProviderActivationReset, map[string]any{
			"resetCount":       resetCount,
			"secretsPreserved": true,
		})
	}
	return apphttp.OK(c, map[string]any{
		"reset":            true,
		"resetCount":       resetCount,
		"secretsPreserved": true,
	})
}

func (h *Controller) auditProviderActivation(c fiber.Ctx, actorUserID int64, action string, meta map[string]any) {
	if h == nil || h.auditor == nil || actorUserID <= 0 || action == "" {
		return
	}
	_ = h.auditor.Append(c.Context(), audit.Event{
		ActorUserID: actorUserID,
		Action:      action,
		Metadata:    meta,
	})
}

func mapActivationMutationError(err error) error {
	switch {
	case errors.Is(err, identity.ErrProviderActivationCASConflict):
		return fiber.NewError(fiber.StatusConflict, "auth.provider_activation_cas_conflict")
	case errors.Is(err, identity.ErrProviderActivationUnsupportedOperation):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.provider_operation_unsupported")
	case errors.Is(err, identity.ErrProviderActivationOwnershipRejected):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.provider_ownership_rejected")
	case errors.Is(err, identity.ErrAuthProviderNotFound):
		return fiber.NewError(fiber.StatusNotFound, "auth.provider_not_found")
	case errors.Is(err, identity.ErrExternalAuthProviderUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	default:
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
}
