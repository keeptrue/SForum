package extensions

import (
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// PluginRuntimePublicationTransition 描述一次精确生命周期切换对
// 不可变 desired full-set 的期望变更。Source 可为 nil（首次启用或
// 已无 runtime 成员的幂等重放）；Target 始终是 journal 绑定的精确制品。
// Reason 与 Activate 必须同向：enable/upgrade/rollback 要求 Activate=true；
// disable/uninstall 要求 Activate=false。startup_reconcile/recovery 属于
// 权威全量投影，不是本 lifecycle transition 入口。
type PluginRuntimePublicationTransition struct {
	Source      *Extension
	Target      Extension
	Activate    bool
	Reason      PluginRuntimePublicationReason
	ActorUserID int64
}

// TransitionPluginRuntimeDesiredMembers 从最新 full-set 成员出发，保留所有
// 无关成员，并对 Target.ID 做精确 CAS：当前成员可为 exact source、已是
// exact target / 无成员的幂等重放，或任何其它同 extension 成员冲突。
// 可执行成员资格只由 trim 后的 manifest.backend.entry 决定；声明型插件
// 永不进入成员集。本函数不读 mutable extensions 表。
func TransitionPluginRuntimeDesiredMembers(
	latest []PluginRuntimeMember,
	transition PluginRuntimePublicationTransition,
) ([]PluginRuntimeMember, error) {
	if err := validatePluginRuntimePublicationTransition(transition); err != nil {
		return nil, err
	}

	targetMember, targetExecutable, err := exactPluginRuntimeTransitionArtifact(transition.Target)
	if err != nil {
		return nil, err
	}

	var sourceMember PluginRuntimeMember
	sourceExecutable := false
	if transition.Source != nil {
		sourceMember, sourceExecutable, err = exactPluginRuntimeTransitionArtifact(*transition.Source)
		if err != nil {
			return nil, err
		}
		if sourceMember.ExtensionID != targetMember.ExtensionID {
			return nil, ErrPluginRuntimePublicationConflict
		}
	}
	if err := validatePluginRuntimeTransitionArtifacts(
		transition, sourceMember, sourceExecutable, targetMember, targetExecutable,
	); err != nil {
		return nil, err
	}

	// 旧 full-set 必须先通过既有 canonical 校验；重复 ID 或非法成员直接冲突。
	canonicalLatest, _, err := canonicalPluginRuntimeMembers(latest)
	if err != nil {
		return nil, ErrPluginRuntimePublicationConflict
	}

	current, found := pluginRuntimeMemberForExtension(canonicalLatest, targetMember.ExtensionID)
	desiredPresent := transition.Activate && targetExecutable
	if found {
		switch {
		case current == targetMember:
			// 已是 exact target：激活时幂等保留，停用时精确移除。
		case transition.Source != nil && sourceExecutable && current == sourceMember:
			// 当前成员精确等于 source：允许切换。
		default:
			// 其它任何同 extension 成员（含 source 缺失/不匹配）一律冲突。
			return nil, ErrPluginRuntimePublicationConflict
		}
	}

	next := make([]PluginRuntimeMember, 0, len(canonicalLatest)+1)
	for _, member := range canonicalLatest {
		if member.ExtensionID == targetMember.ExtensionID {
			continue
		}
		next = append(next, member)
	}
	if desiredPresent {
		next = append(next, targetMember)
	}
	canonicalNext, _, err := canonicalPluginRuntimeMembers(next)
	if err != nil {
		return nil, ErrPluginRuntimePublicationConflict
	}
	return canonicalNext, nil
}

func validatePluginRuntimeTransitionArtifacts(
	transition PluginRuntimePublicationTransition,
	sourceMember PluginRuntimeMember,
	sourceExecutable bool,
	targetMember PluginRuntimeMember,
	targetExecutable bool,
) error {
	switch transition.Reason {
	case PluginRuntimePublicationEnable,
		PluginRuntimePublicationDisable,
		PluginRuntimePublicationUninstall:
		if transition.Source != nil &&
			(sourceMember != targetMember || sourceExecutable != targetExecutable) {
			return ErrPluginRuntimePublicationConflict
		}
	case PluginRuntimePublicationUpgrade,
		PluginRuntimePublicationRollback:
		if transition.Source == nil || sourceMember == targetMember {
			return ErrPluginRuntimePublicationConflict
		}
	default:
		return ErrPluginRuntimePublicationConflict
	}
	return nil
}

// validatePluginRuntimePublicationTransition 统一 reason/Activate 方向，
// 避免 lifecycle journal 与 runtime publication 两套互相矛盾的权威。
func validatePluginRuntimePublicationTransition(
	transition PluginRuntimePublicationTransition,
) error {
	if transition.ActorUserID < 0 {
		return ErrPluginRuntimePublicationConflict
	}
	switch transition.Reason {
	case PluginRuntimePublicationEnable,
		PluginRuntimePublicationUpgrade,
		PluginRuntimePublicationRollback:
		if !transition.Activate {
			return ErrPluginRuntimePublicationConflict
		}
	case PluginRuntimePublicationDisable,
		PluginRuntimePublicationUninstall:
		if transition.Activate {
			return ErrPluginRuntimePublicationConflict
		}
	default:
		// startup_reconcile/recovery 与未知 reason 均不属 lifecycle transition。
		return ErrPluginRuntimePublicationConflict
	}
	return nil
}

// exactPluginRuntimeTransitionArtifact 校验调用方给出的 Extension 是否为
// 内部一致的精确插件制品，并返回其 runtime 成员身份与是否可执行。
func exactPluginRuntimeTransitionArtifact(
	extension Extension,
) (PluginRuntimeMember, bool, error) {
	if extension.Type != TypePlugin ||
		extension.ID == "" || extension.ID != strings.TrimSpace(extension.ID) ||
		extension.Version == "" || extension.Version != strings.TrimSpace(extension.Version) ||
		extension.ActiveVersionID <= 0 ||
		!validPackageDigest(extension.PackageDigest) {
		return PluginRuntimeMember{}, false, ErrPluginRuntimePublicationConflict
	}

	manifest := extensionmanifest.Normalize(extension.Manifest)
	if err := extensionmanifest.Validate(manifest); err != nil ||
		manifest.ID != extension.ID ||
		manifest.Type != TypePlugin ||
		manifest.Version != extension.Version {
		return PluginRuntimeMember{}, false, ErrPluginRuntimePublicationConflict
	}

	member := PluginRuntimeMember{
		ExtensionID:        extension.ID,
		ExtensionVersionID: extension.ActiveVersionID,
		ExtensionVersion:   extension.Version,
		PackageDigest:      extension.PackageDigest,
	}
	if !validPluginRuntimeMember(member) {
		return PluginRuntimeMember{}, false, ErrPluginRuntimePublicationConflict
	}
	// 与权威投影一致：仅 trim 后非空 backend.entry 才拥有 subprocess 成员资格。
	executable := strings.TrimSpace(manifest.Backend.Entry) != ""
	return member, executable, nil
}

func pluginRuntimeMemberForExtension(
	members []PluginRuntimeMember,
	extensionID string,
) (PluginRuntimeMember, bool) {
	for _, member := range members {
		if member.ExtensionID == extensionID {
			return member, true
		}
	}
	return PluginRuntimeMember{}, false
}

// TransitionPluginRuntimeTrustRevocationMembers removes every executable
// member owned by one revoked extension while preserving the exact unrelated
// desired set. The PostgreSQL producer decides whether an already absent
// member needs a new revision.
func TransitionPluginRuntimeTrustRevocationMembers(
	latest []PluginRuntimeMember,
	extensionID string,
) ([]PluginRuntimeMember, error) {
	if extensionID == "" || extensionID != strings.TrimSpace(extensionID) {
		return nil, ErrPluginRuntimePublicationConflict
	}
	canonical, _, err := canonicalPluginRuntimeMembers(latest)
	if err != nil {
		return nil, ErrPluginRuntimePublicationConflict
	}
	next := make([]PluginRuntimeMember, 0, len(canonical))
	for _, member := range canonical {
		if member.ExtensionID != extensionID {
			next = append(next, member)
		}
	}
	return next, nil
}
