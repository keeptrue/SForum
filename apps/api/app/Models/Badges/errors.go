package badges

import "errors"

// 领域错误。HTTP 层按错误码映射状态码；这些错误本身不带状态码，
// 保持模型层不依赖传输层。
var (
	// ErrBadgesDisabled 表示徽章功能被运营关闭（features.badges）。
	ErrBadgesDisabled = errors.New("badges: feature disabled")
	// ErrBadgeNotFound 表示徽章或分组不存在。
	ErrBadgeNotFound = errors.New("badges: not found")
	// ErrBadgeSlugTaken 表示 slug 已被占用。
	ErrBadgeSlugTaken = errors.New("badges: slug already taken")
	// ErrBadgeRuleInvalid 表示规则不合法（tier / grant_mode / metric / threshold / icon）。
	ErrBadgeRuleInvalid = errors.New("badges: invalid rule")
	// ErrBadgeRuleModeConflict 表示「授予方式与规则」自相矛盾（manual 却带 rule）。
	//
	// 它与 ErrBadgeRuleInvalid 刻意分开：修法完全不同——前者要运营改**授予方式或清空规则**，
	// 后者要改**指标/阈值/条件**。共用一码时运维只会看到「请检查指标与阈值的组合」，
	// 而指标根本没问题（plan §13 #19 的同族教训）。
	ErrBadgeRuleModeConflict = errors.New("badges: rule mode conflict")
	// ErrBadgeManualOnly 表示该徽章不是手动徽章，不能手动授予或收回。
	ErrBadgeManualOnly = errors.New("badges: manual grant required")
	// ErrBadgeAlreadyGranted 表示该用户已持有该徽章。
	ErrBadgeAlreadyGranted = errors.New("badges: already granted")
	// ErrBadgeTierNotFound 表示徽章等级不存在。
	ErrBadgeTierNotFound = errors.New("badges: tier not found")
	// ErrBadgeTierKeyTaken 表示等级 key 已被占用。
	ErrBadgeTierKeyTaken = errors.New("badges: tier key already taken")
	// ErrBadgeTierInvalid 表示等级入参不合法（key 形状、双语名、图标、颜色、位次）。
	ErrBadgeTierInvalid = errors.New("badges: invalid tier")
	// ErrBadgeTierInUse 表示该等级仍被徽章引用，不能直接删除（必须先指定 migrateTo）。
	ErrBadgeTierInUse = errors.New("badges: tier in use")
	// ErrBadgeGrantBatchInvalid 表示批量授予入参不合法（空列表、超上限、非手动徽章）。
	ErrBadgeGrantBatchInvalid = errors.New("badges: invalid grant batch")
	// ErrBadgeFactInvalid 表示事实入参不合法（userID/factKey 为空、delta 为 0）。
	ErrBadgeFactInvalid = errors.New("badges: invalid fact")
	// ErrBadgeRuleReferenceInvalid 表示规则指向的对象不存在（分类/标签/角色/provider/徽章/主题）。
	ErrBadgeRuleReferenceInvalid = errors.New("badges: rule reference invalid")
	// ErrBadgeRuleCycle 表示规则之间通过 badge.owned 形成依赖环（含自引用）。
	ErrBadgeRuleCycle = errors.New("badges: rule cycle")
	// ErrBadgeBackfillUnavailable 表示存在复杂规则徽章，但装配里没有后台回扫入口。
	// 宁可报错，也不要让运营看到「重算完成、0 人获得」而实际一半徽章没算。
	ErrBadgeBackfillUnavailable = errors.New("badges: rule backfill unavailable")
	// ErrBadgeFactWriteDenied 表示插件事实写入被拒（参数非法、key 未声明、存储失败）。
	// Host 命令按冻结 reason 细分，这里只承载顶层错误码。
	ErrBadgeFactWriteDenied = errors.New("badges: fact write denied")
)

// HTTP 错误码。与 contracts/openapi 的 error.code 取值逐字一致。
const (
	CodeBadgesDisabled        = "forum.badges_disabled"
	CodeBadgeNotFound         = "forum.badge_not_found"
	CodeBadgeSlugTaken        = "forum.badge_slug_taken"
	CodeBadgeRuleInvalid      = "forum.badge_rule_invalid"
	CodeBadgeRuleModeConflict = "forum.badge_rule_mode_conflict"
	CodeBadgeManualOnly       = "forum.badge_manual_only"
	CodeBadgeAlreadyGranted   = "forum.badge_already_granted"
	// CodeBadgeTierNotFound 等五个码由「徽章等级表」这一刀新增；
	// 中英双语文案在 app/Support/Localization/messages.go，缺一条本地化测试就会红。
	CodeBadgeTierNotFound      = "forum.badge_tier_not_found"
	CodeBadgeTierKeyTaken      = "forum.badge_tier_key_taken"
	CodeBadgeTierInvalid       = "forum.badge_tier_invalid"
	CodeBadgeTierInUse         = "forum.badge_tier_in_use"
	CodeBadgeGrantBatchInvalid = "forum.badge_grant_batch_invalid"
	// 规则引擎三码：形状过复杂 / 条件未知 / 引用失效 / 成环。
	CodeBadgeRuleTooComplex       = "forum.badge_rule_too_complex"
	CodeBadgeRuleUnknownCond      = "forum.badge_rule_unknown_condition"
	CodeBadgeRuleReferenceInvalid = "forum.badge_rule_reference_invalid"
	CodeBadgeRuleCycle            = "forum.badge_rule_cycle"
	CodeBadgeBackfillUnavailable  = "forum.badge_backfill_unavailable"
	// CodeBadgeFactWriteDenied 是插件事实上报（Host 命令 badges.fact.record）被拒的
	// 顶层错误码。细节在 ErrorDetail.reason 的六个冻结字面量里（spec §9 Errata 3）。
	CodeBadgeFactWriteDenied = "forum.badge_fact_write_denied"
)

// ErrorCode 把领域错误映射到稳定的错误码字符串。
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrBadgesDisabled):
		return CodeBadgesDisabled
	case errors.Is(err, ErrBadgeNotFound):
		return CodeBadgeNotFound
	case errors.Is(err, ErrBadgeSlugTaken):
		return CodeBadgeSlugTaken
	// 顺序敏感：mode 冲突必须排在 rule_invalid 之前（errors.Is 匹配的是同一条链）。
	case errors.Is(err, ErrBadgeRuleModeConflict):
		return CodeBadgeRuleModeConflict
	case errors.Is(err, ErrBadgeRuleInvalid):
		return CodeBadgeRuleInvalid
	case errors.Is(err, ErrBadgeManualOnly):
		return CodeBadgeManualOnly
	case errors.Is(err, ErrBadgeAlreadyGranted):
		return CodeBadgeAlreadyGranted
	case errors.Is(err, ErrBadgeTierNotFound):
		return CodeBadgeTierNotFound
	case errors.Is(err, ErrBadgeTierKeyTaken):
		return CodeBadgeTierKeyTaken
	case errors.Is(err, ErrBadgeTierInvalid):
		return CodeBadgeTierInvalid
	case errors.Is(err, ErrBadgeTierInUse):
		return CodeBadgeTierInUse
	case errors.Is(err, ErrBadgeGrantBatchInvalid):
		return CodeBadgeGrantBatchInvalid
	case errors.Is(err, ErrBadgeRuleReferenceInvalid):
		return CodeBadgeRuleReferenceInvalid
	case errors.Is(err, ErrBadgeRuleCycle):
		return CodeBadgeRuleCycle
	case errors.Is(err, ErrBadgeBackfillUnavailable):
		return CodeBadgeBackfillUnavailable
	case errors.Is(err, ErrBadgeFactWriteDenied):
		return CodeBadgeFactWriteDenied
	default:
		return ""
	}
}
