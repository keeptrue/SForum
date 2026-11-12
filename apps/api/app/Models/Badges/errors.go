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
)

// HTTP 错误码。与 contracts/openapi 的 error.code 取值逐字一致。
const (
	CodeBadgesDisabled      = "forum.badges_disabled"
	CodeBadgeNotFound       = "forum.badge_not_found"
	CodeBadgeSlugTaken      = "forum.badge_slug_taken"
	CodeBadgeRuleInvalid    = "forum.badge_rule_invalid"
	CodeBadgeManualOnly     = "forum.badge_manual_only"
	CodeBadgeAlreadyGranted = "forum.badge_already_granted"
	// CodeBadgeTierNotFound 等五个码由「徽章等级表」这一刀新增；
	// 中英双语文案在 app/Support/Localization/messages.go，缺一条本地化测试就会红。
	CodeBadgeTierNotFound      = "forum.badge_tier_not_found"
	CodeBadgeTierKeyTaken      = "forum.badge_tier_key_taken"
	CodeBadgeTierInvalid       = "forum.badge_tier_invalid"
	CodeBadgeTierInUse         = "forum.badge_tier_in_use"
	CodeBadgeGrantBatchInvalid = "forum.badge_grant_batch_invalid"
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
	default:
		return ""
	}
}
