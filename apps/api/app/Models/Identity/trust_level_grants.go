package identity

import "slices"

// trustGrantState 是「信任等级带来的权限变更」的唯一计算输入。
//
// 两个装载器（`LoadActor` 的池路径与 `LoadEffectiveActorTx` 的事务路径）都必须走
// `applyTrustGrants`：两处各写一份就是本仓反复出现的「两份」失败（D22 的同形）。
//
// 这里的顺序是**先扣后并**：扣减针对基线（静态授予），合并针对已挣得。
// TL0 的已挣得集合恒为空（`Grants` 对 `level <= LevelNewcomer` 直接返回 nil），
// 所以「先扣后并」不会把刚扣掉的键又并回来；反过来「先并后扣」则会在将来
// 若有人放开 TL0 的 earnable 时静默吃掉那次授予。
type trustGrantState struct {
	// Level 与 Resolved 来自 `resolveTrustGrants`。Resolved=false（产品关闭 / 统计
	// 读失败 / 未注入）时**什么都不做**：关闭态 = 不做减法（D9 的 F 组判据）。
	Level    int
	Resolved bool
	// Kind 决定 bot 是否旁路（bot 不进人类信任阶梯，D25）。
	Kind UserKind
	// Earned 是该等级挣得的键（D23），只增不减。
	Earned []string
	// Permissions 是**静态生效集**（角色授予 ∪ allow 覆盖），就地修改。
	Permissions map[string]bool
	// AllowOverride 是 `user_permission_overrides` 里 effect='allow' 的键。
	// 显式 allow 的键不扣（§4-bis 第 4 条）：运营「手动放开某人」不能被静默推翻。
	AllowOverride map[string]bool
	// Denied 是显式 deny 的键（D28）。已挣得不得覆盖 deny。
	Denied map[string]bool
}

// applyTrustGrants 是扣减（Z3-b″ 减法门）与已挣得合并（D23）的**唯一**实现。
//
// 返回被扣除的键（证据/日志用）与 earned 独占键（来源解释用，不参与裁决）。
func applyTrustGrants(trust TrustLevelSource, state trustGrantState) (deducted []string, earnedOnly map[string]bool) {
	if state.Permissions == nil || !state.Resolved {
		return nil, nil
	}
	return deductBaselineGrants(trust, state), mergeEarnedKeys(state)
}

// needsBaselineDeduction 报告这次装载是否**可能**扣键：门真的会生效时才为真。
//
// 它同时是「要不要多读一次 allow 覆盖」的判据：TL1+ / 未解析 / bot 都返回 false，
// 那些路径的查询次数与本刀之前逐字相同（G 组预算不变）。
func needsBaselineDeduction(trust TrustLevelSource, level int, kind UserKind) bool {
	if trust == nil || kind.IsBot() {
		return false
	}
	return len(trust.BaselineGrantsFor(level)) > 0
}

// deductBaselineGrants 实现「扣」与四条「不扣」判据。
//
// 不扣：未解析（外层已挡）/ bot / 显式 allow 覆盖 / 等级已到 TL1+（由
// `BaselineGrantsFor` 返回空集表达）。只改 Permissions，**不动 Earned**：
// Earned 的语义是「只因为等级才有」，扣减不是它的成员。
func deductBaselineGrants(trust TrustLevelSource, state trustGrantState) []string {
	if !needsBaselineDeduction(trust, state.Level, state.Kind) {
		return nil
	}
	var deducted []string
	for _, key := range trust.BaselineGrantsFor(state.Level) {
		if key == "" || !state.Permissions[key] {
			continue
		}
		if state.AllowOverride[key] {
			continue
		}
		delete(state.Permissions, key)
		deducted = append(deducted, key)
	}
	slices.Sort(deducted)
	return deducted
}

// mergeEarnedKeys 实现 D23 的合并：有效集 ∪= earned ∖ static ∖ denied。
func mergeEarnedKeys(state trustGrantState) map[string]bool {
	if len(state.Earned) == 0 {
		return nil
	}
	var earnedOnly map[string]bool
	for _, key := range state.Earned {
		if key == "" || state.Denied[key] || state.Permissions[key] {
			continue
		}
		state.Permissions[key] = true
		if earnedOnly == nil {
			earnedOnly = make(map[string]bool)
		}
		earnedOnly[key] = true
	}
	return earnedOnly
}

// applyCurrentUserTrustGrants 是池路径的适配器：`CurrentUser` 用切片表达有效集。
//
// 没有任何变更时**不重建切片**，保证「无等级 / 无 earned」路径的输出顺序与本刀之前
// 逐字节相同（JSON 里 permissions 的数组顺序也属于对外契约）。
func applyCurrentUserTrustGrants(trust TrustLevelSource, current *CurrentUser, level int, earned []string, allowOverride map[string]bool) (deducted []string, earnedOnly map[string]bool) {
	if current == nil {
		return nil, nil
	}
	present := make(map[string]bool, len(current.Permissions))
	for _, key := range current.Permissions {
		present[key] = true
	}
	deducted, earnedOnly = applyTrustGrants(trust, trustGrantState{
		Level:         level,
		Resolved:      true,
		Kind:          current.Kind,
		Earned:        earned,
		Permissions:   present,
		AllowOverride: allowOverride,
		Denied:        current.denied,
	})
	if len(deducted) == 0 && earnedOnly == nil {
		return deducted, earnedOnly
	}
	current.Permissions = current.Permissions[:0]
	for key := range present {
		current.Permissions = append(current.Permissions, key)
	}
	slices.Sort(current.Permissions)
	return deducted, earnedOnly
}

// applyActorTrustGrants 是事务路径的适配器：`Actor.Permissions` 本来就是 map。
func applyActorTrustGrants(trust TrustLevelSource, actor *Actor, level int, earned []string, allowOverride, denied map[string]bool) (deducted []string, earnedOnly map[string]bool) {
	if actor == nil {
		return nil, nil
	}
	return applyTrustGrants(trust, trustGrantState{
		Level:         level,
		Resolved:      true,
		Kind:          actor.Kind,
		Earned:        earned,
		Permissions:   actor.Permissions,
		AllowOverride: allowOverride,
		Denied:        denied,
	})
}
