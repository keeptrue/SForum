// Package outbox 定义宿主可靠投递的共享状态约定（F3.1）。
//
// 各业务表（mail_deliveries、未来 webhook_deliveries）可保留独立 schema，
// 但状态机、终态、可重放与 completed_at 判定应复用本包，避免每个 vertical
// 各自发明一轮。
//
// 状态机（推荐路径）:
//
//	queued → sending → sent
//	                 → failed  （永久失败，可人工/API replay）
//	                 → skipped （策略跳过，默认不 replay）
//	sending → dead           （超过重试预算，需运维干预）
//
// 临时失败应保持 sending（或回到 queued）并返回可重试错误，由 River 调度，
// 不要立刻标记 failed。
package outbox

import "strings"

// 稳定状态常量。写入 DB CHECK 约束时请与此保持一致。
const (
	StatusQueued  = "queued"
	StatusSending = "sending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
	StatusDead    = "dead"
)

// Alias 兼容：extension_event_deliveries 使用 running/succeeded 命名。
// 映射到共享语义时用 Normalize。
const (
	legacyRunning   = "running"
	legacySucceeded = "succeeded"
)

// Normalize 将领域别名折叠到共享状态词表。
func Normalize(status string) string {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case StatusQueued:
		return StatusQueued
	case StatusSending, legacyRunning:
		return StatusSending
	case StatusSent, legacySucceeded:
		return StatusSent
	case StatusFailed:
		return StatusFailed
	case StatusSkipped:
		return StatusSkipped
	case StatusDead:
		return StatusDead
	default:
		return strings.TrimSpace(status)
	}
}

// IsTerminal 表示记录不再被自动 worker 推进。
func IsTerminal(status string) bool {
	switch Normalize(status) {
	case StatusSent, StatusFailed, StatusSkipped, StatusDead:
		return true
	default:
		return false
	}
}

// IsSuccess 表示业务侧投递成功。
func IsSuccess(status string) bool {
	return Normalize(status) == StatusSent
}

// ShouldSetCompletedAt 终态写入时是否应填 completed_at。
func ShouldSetCompletedAt(status string) bool {
	return IsTerminal(status)
}

// CanReplay 是否允许运营/系统发起重放（重新入队）。
// failed 与 dead 可 replay；skipped 默认否（策略选择，不是故障）。
func CanReplay(status string) bool {
	switch Normalize(status) {
	case StatusFailed, StatusDead:
		return true
	default:
		return false
	}
}

// CanTransition 校验允许的状态迁移。from 为空视为新建（仅允许进入 queued）。
func CanTransition(from, to string) bool {
	fromN := Normalize(from)
	toN := Normalize(to)
	if toN == "" {
		return false
	}
	if fromN == "" || fromN == toN {
		return toN == StatusQueued || fromN == toN
	}
	switch fromN {
	case StatusQueued:
		return toN == StatusSending || toN == StatusSkipped || toN == StatusFailed || toN == StatusDead
	case StatusSending:
		return toN == StatusSent || toN == StatusFailed || toN == StatusSkipped || toN == StatusDead || toN == StatusSending
	case StatusFailed, StatusDead:
		// replay：回到 queued 后重新走发送路径
		return toN == StatusQueued
	case StatusSkipped, StatusSent:
		return false
	default:
		return false
	}
}

// ExhaustedAttempts 在 attempt 已达/超过 maxAttempts 时建议转入 dead。
// maxAttempts <= 0 表示不启用预算（永不因次数转 dead）。
func ExhaustedAttempts(attemptCount, maxAttempts int) bool {
	if maxAttempts <= 0 {
		return false
	}
	return attemptCount >= maxAttempts
}

// HasAttemptBudget 报告这次尝试是否仍在调度预算内 —— 即「还该不该发」。
//
// 它与 ExhaustedAttempts 是两个问题，混用会造成两种相反的错：
//   - 发送前用 ExhaustedAttempts（attempt >= max）判断，会把预算内的最后一次
//     调度直接判死、一次请求都不发（RV-52：5 次预算实际只发 4 次）；
//   - 失败后用 HasAttemptBudget 判断，则永远等不到终态，记录卡在 sending。
//
// 所以：发送前问 HasAttemptBudget，失败后问 ExhaustedAttempts。
// maxAttempts <= 0 表示不启用预算，永远允许发送。
func HasAttemptBudget(attemptCount, maxAttempts int) bool {
	if maxAttempts <= 0 {
		return true
	}
	return attemptCount <= maxAttempts
}
