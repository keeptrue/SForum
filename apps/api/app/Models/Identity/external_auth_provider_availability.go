package identity

import (
	"context"
	"errors"
	"strings"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// 有效可用性（effective availability）评估。
//
// T1C 权威规则（M1R Activation, Catalog, Probe, And Audit）：
//   - ownership / live artifact / supported operations / trust / enablement /
//     configuration / runtime health / Safe Mode 一律从 Host 状态派生；
//   - 浏览器 ownership/artifact 声明无权威；
//   - artifact 变更、disable、trust revoke、uninstall 或 Safe Mode 必须移除
//     有效可用性，直到按规则故意重新激活；
//   - 公开 catalog 只返回有效可用 provider；Host 状态查询失败 fail closed。

// AuthProviderAvailability 是单 provider 单 operation 的 Host 评估结果。
type AuthProviderAvailability struct {
	ProviderID         string
	Operation          ExternalAuthOperation
	Available          bool
	Reason             string // 稳定短 reason；Available=true 时为空
	OwnerExtensionID   string
	OwnerPackageDigest string
	OwnerVersionID     int64
	RuntimeInstanceID  string
	SupportedOps       []string
	ActivationRevision int64
}

// AuthProviderCatalogEntry 是公开 catalog 的一项（仅有效可用 auth provider）。
// Label / Icon 来自插件 Identity 声明（展示元数据），非 Host 硬编码品牌。
type AuthProviderCatalogEntry struct {
	ProviderID          string
	Kind                string
	ContractVersion     string
	Priority            int
	Operations          []string // live 声明的 operations
	ActivatedOperations []string // 有效可用的 Host 操作（login/registration/link）
	OwnerExtensionID    string
	// Label 为按请求 locale 解析后的展示名（插件 labelLocales/label）。
	Label string
	// Icon 为插件声明的 Iconify 名称；空表示 Host 壳使用通用图标。
	Icon string
}

// EvaluateOperationAvailability 评估 provider+operation 是否有效可用。
//
// 顺序（任一失败 → unavailable）：
//  1. Safe Mode → unavailable
//  2. Host 激活目录 flag off / 缺失 → not_enabled
//  3. live Registry 解析失败（禁用/卸载/不可见）→ unavailable
//  4. live 无 RuntimeInstanceID（不可执行）→ unavailable
//  5. activation 绑定的 owner/digest 与 live 不一致 → artifact_mismatch
//  6. live 未声明该 operation → not_enabled / unsupported
//  7. 可选 Configured 钩子返回 false → unavailable（settings 接线前可为 nil）
func (s *ExternalAuthService) EvaluateOperationAvailability(
	ctx context.Context,
	providerID string,
	op ExternalAuthOperation,
) (AuthProviderAvailability, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	out := AuthProviderAvailability{ProviderID: providerID, Operation: op}
	if providerID == "" || op == "" {
		out.Reason = "auth.provider_not_found"
		return out, nil
	}
	if s == nil {
		out.Reason = "auth.provider_unavailable"
		return out, nil
	}
	if s.isSafeMode() {
		out.Reason = "auth.provider_unavailable"
		return out, nil
	}
	if s.deps.ActivationStore == nil {
		out.Reason = "auth.provider_not_enabled"
		return out, nil
	}
	act, err := s.deps.ActivationStore.Get(ctx, providerID)
	if err != nil {
		if errors.Is(err, ErrProviderActivationNotFound) {
			out.Reason = "auth.provider_not_enabled"
			return out, nil
		}
		// Host 状态查询失败：fail closed，向上抛出让 catalog 返回 503。
		return out, err
	}
	out.ActivationRevision = act.Revision
	out.OwnerExtensionID = act.OwnerExtensionID
	out.OwnerPackageDigest = act.OwnerPackageDigest
	out.OwnerVersionID = act.OwnerExtensionVersionID
	if !activationFlagEnabled(act, op) {
		out.Reason = "auth.provider_not_enabled"
		return out, nil
	}

	live, err := s.providerContribution(providerID)
	if err != nil {
		out.Reason = "auth.provider_unavailable"
		return out, nil
	}
	if live.Artifact.Core || strings.TrimSpace(live.Artifact.RuntimeInstanceID) == "" {
		out.Reason = "auth.provider_unavailable"
		return out, nil
	}
	out.RuntimeInstanceID = live.Artifact.RuntimeInstanceID
	out.SupportedOps = providerOperationNames(live)

	// activation 必须绑定到精确 live artifact；漂移即失效，需故意重新激活。
	if !activationMatchesLiveArtifact(act, live) {
		out.Reason = "auth.provider_unavailable"
		return out, nil
	}
	// 覆盖为 live 权威值（避免激活行陈旧字段泄漏到调用方）。
	out.OwnerExtensionID = live.Artifact.ExtensionID
	out.OwnerPackageDigest = live.Artifact.PackageDigest
	out.OwnerVersionID = live.Artifact.VersionID

	if !authProviderSupportsExternalOp(live, op) {
		out.Reason = "auth.provider_not_enabled"
		return out, nil
	}
	if s.deps.IsProviderConfigured != nil {
		configured, cfgErr := s.deps.IsProviderConfigured(ctx, providerID)
		if cfgErr != nil {
			return out, cfgErr
		}
		if !configured {
			out.Reason = "auth.provider_unavailable"
			return out, nil
		}
	}
	out.Available = true
	out.Reason = ""
	return out, nil
}

// IsEffectivelyAvailable 是 EvaluateOperationAvailability 的布尔便捷方法。
// Host 状态查询错误向上返回；业务不可用返回 (false, nil)。
func (s *ExternalAuthService) IsEffectivelyAvailable(
	ctx context.Context,
	providerID string,
	op ExternalAuthOperation,
) (bool, error) {
	av, err := s.EvaluateOperationAvailability(ctx, providerID, op)
	if err != nil {
		return false, err
	}
	return av.Available, nil
}

// ListEffectivePublicCatalog 构建公开 catalog：仅有效可用的 auth provider。
// activations 查询失败时返回错误（fail closed，不得静默暴露部分 catalog）。
// recovery providers 不经 Host activation 目录，但同样要求 live + RuntimeInstanceID + 非 Safe Mode。
// locale 仅用于解析插件声明的 LabelLocales；不影响可执行状态。
func (s *ExternalAuthService) ListEffectivePublicCatalog(
	ctx context.Context,
	registry *identityregistry.Registry,
	locale string,
) ([]AuthProviderCatalogEntry, error) {
	if registry == nil {
		return []AuthProviderCatalogEntry{}, nil
	}
	if s != nil && s.isSafeMode() {
		// Safe Mode：第三方 auth 不可用；不暴露任何插件 auth 入口。
		return []AuthProviderCatalogEntry{}, nil
	}

	// Host 激活目录：失败必须 fail closed。
	activationByID := map[string]ProviderActivation{}
	if s != nil && s.deps.ActivationStore != nil {
		acts, err := s.deps.ActivationStore.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range acts {
			activationByID[a.ProviderID] = a
		}
	}

	entries := make([]AuthProviderCatalogEntry, 0)
	for _, provider := range registry.Providers(identityregistry.ProviderKindAuth) {
		if provider.Artifact.Core || strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" {
			continue
		}
		if len(provider.Operations) == 0 {
			continue
		}
		act, hasAct := activationByID[provider.ID]
		if !hasAct {
			continue
		}
		if !activationMatchesLiveArtifact(act, provider) {
			continue
		}
		activated := make([]string, 0, 3)
		for _, op := range []ExternalAuthOperation{
			ExternalAuthOperationLogin,
			ExternalAuthOperationRegistration,
			ExternalAuthOperationLink,
		} {
			if !activationFlagEnabled(act, op) {
				continue
			}
			if !authProviderSupportsExternalOp(provider, op) {
				continue
			}
			if s != nil && s.deps.IsProviderConfigured != nil {
				configured, cfgErr := s.deps.IsProviderConfigured(ctx, provider.ID)
				if cfgErr != nil {
					return nil, cfgErr
				}
				if !configured {
					continue
				}
			}
			activated = append(activated, string(op))
		}
		if len(activated) == 0 {
			continue
		}
		ops := providerOperationNames(provider)
		priority := provider.Priority
		if act.Priority != 0 {
			priority = act.Priority
		}
		entries = append(entries, AuthProviderCatalogEntry{
			ProviderID:          provider.ID,
			Kind:                provider.Kind,
			ContractVersion:     provider.ContractVersion,
			Priority:            priority,
			Operations:          ops,
			ActivatedOperations: activated,
			OwnerExtensionID:    provider.Artifact.ExtensionID,
			// 展示名/图标由插件 Identity 声明注入；Host 不硬编码供应商品牌。
			Label: identityregistry.ResolveProviderLabel(provider.Provider, locale),
			Icon:  strings.TrimSpace(provider.Icon),
		})
	}

	// recovery：不经 activation 目录；live + runtime 即可（非 Safe Mode 已在上方处理）。
	for _, provider := range registry.Providers(identityregistry.ProviderKindRecovery) {
		if provider.Artifact.Core || strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" {
			continue
		}
		if len(provider.Operations) == 0 {
			continue
		}
		entries = append(entries, AuthProviderCatalogEntry{
			ProviderID:          provider.ID,
			Kind:                provider.Kind,
			ContractVersion:     provider.ContractVersion,
			Priority:            provider.Priority,
			Operations:          providerOperationNames(provider),
			ActivatedOperations: []string{},
			OwnerExtensionID:    provider.Artifact.ExtensionID,
			Label:               identityregistry.ResolveProviderLabel(provider.Provider, locale),
			Icon:                strings.TrimSpace(provider.Icon),
		})
	}
	return entries, nil
}

// PrepareActivationInput 从 live Host 状态构造激活写入输入。
// 拒绝浏览器 ownership/artifact；拒绝启用 live 未声明的操作。
func (s *ExternalAuthService) PrepareActivationInput(
	providerID string,
	loginEnabled, registrationEnabled, linkEnabled *bool,
	priority *int,
	expectedRevision int64,
) (ProviderActivationInput, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "" {
		return ProviderActivationInput{}, ErrAuthProviderNotFound
	}
	if s != nil && s.isSafeMode() {
		// Safe Mode 下仍允许管理员写入激活意图（绑定当前若不可解析则拒绝启用）。
		// 但无法解析 live artifact 时不得创建/更新绑定。
	}
	live, err := s.providerContribution(providerID)
	if err != nil {
		return ProviderActivationInput{}, ErrExternalAuthProviderUnavailable
	}
	if live.Artifact.Core || strings.TrimSpace(live.Artifact.PackageDigest) == "" {
		return ProviderActivationInput{}, ErrExternalAuthProviderUnavailable
	}
	// 启用操作必须 live 声明对应 start/complete 对中的至少 start（或 complete）。
	if loginEnabled != nil && *loginEnabled && !authProviderSupportsExternalOp(live, ExternalAuthOperationLogin) {
		return ProviderActivationInput{}, ErrProviderActivationUnsupportedOperation
	}
	if registrationEnabled != nil && *registrationEnabled && !authProviderSupportsExternalOp(live, ExternalAuthOperationRegistration) {
		return ProviderActivationInput{}, ErrProviderActivationUnsupportedOperation
	}
	if linkEnabled != nil && *linkEnabled && !authProviderSupportsExternalOp(live, ExternalAuthOperationLink) {
		return ProviderActivationInput{}, ErrProviderActivationUnsupportedOperation
	}
	return ProviderActivationInput{
		ProviderID:          providerID,
		OwnerExtensionID:    live.Artifact.ExtensionID,
		OwnerPackageDigest:  live.Artifact.PackageDigest,
		OwnerExtensionVerID: live.Artifact.VersionID,
		LoginEnabled:        loginEnabled,
		RegistrationEnabled: registrationEnabled,
		LinkEnabled:         linkEnabled,
		Priority:            priority,
		ExpectedRevision:    expectedRevision,
	}, nil
}

func (s *ExternalAuthService) isSafeMode() bool {
	if s == nil {
		return false
	}
	if s.deps.SafeMode != nil {
		return s.deps.SafeMode()
	}
	return false
}

func activationFlagEnabled(act ProviderActivation, op ExternalAuthOperation) bool {
	switch op {
	case ExternalAuthOperationLogin:
		return act.LoginEnabled
	case ExternalAuthOperationRegistration:
		return act.RegistrationEnabled
	case ExternalAuthOperationLink:
		return act.LinkEnabled
	default:
		return false
	}
}

func activationMatchesLiveArtifact(act ProviderActivation, live identityregistry.ProviderContribution) bool {
	if strings.TrimSpace(act.OwnerExtensionID) == "" || strings.TrimSpace(act.OwnerPackageDigest) == "" {
		return false
	}
	if act.OwnerExtensionID != live.Artifact.ExtensionID {
		return false
	}
	if act.OwnerPackageDigest != live.Artifact.PackageDigest {
		return false
	}
	// VersionID 在激活行中可选；若两侧均非零则必须一致。
	if act.OwnerExtensionVersionID != 0 && live.Artifact.VersionID != 0 &&
		act.OwnerExtensionVersionID != live.Artifact.VersionID {
		return false
	}
	return true
}

func authProviderSupportsExternalOp(live identityregistry.ProviderContribution, op ExternalAuthOperation) bool {
	// live 可能只声明 start、只声明 complete，或两者都有；任一相关名即可视为支持。
	startName := ""
	completeName := ""
	switch op {
	case ExternalAuthOperationLogin:
		startName, completeName = AuthOperationLoginStart, AuthOperationLoginComplete
	case ExternalAuthOperationRegistration:
		startName, completeName = AuthOperationRegistrationStart, AuthOperationRegistrationComplete
	case ExternalAuthOperationLink:
		startName, completeName = AuthOperationLinkStart, AuthOperationLinkComplete
	default:
		return false
	}
	for _, operation := range live.Operations {
		if operation.Name == startName || operation.Name == completeName || operation.Name == string(op) {
			return true
		}
	}
	// inspect-only（无 operations）不得被 Host 激活为可执行入口。
	return false
}

func providerOperationNames(live identityregistry.ProviderContribution) []string {
	out := make([]string, 0, len(live.Operations))
	for _, op := range live.Operations {
		out = append(out, op.Name)
	}
	return out
}
