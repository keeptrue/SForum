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
)

// HTTP 错误码。与 contracts/openapi 的 error.code 取值逐字一致。
const (
	CodeBadgesDisabled      = "forum.badges_disabled"
	CodeBadgeNotFound       = "forum.badge_not_found"
	CodeBadgeSlugTaken      = "forum.badge_slug_taken"
	CodeBadgeRuleInvalid    = "forum.badge_rule_invalid"
	CodeBadgeManualOnly     = "forum.badge_manual_only"
	CodeBadgeAlreadyGranted = "forum.badge_already_granted"
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
	default:
		return ""
	}
}
