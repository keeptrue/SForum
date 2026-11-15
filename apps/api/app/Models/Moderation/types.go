package moderation

import (
	"errors"
	"time"
)

const (
	TargetTypeTopic   = "topic"
	TargetTypeComment = "comment"

	ReasonSpam     = "spam"
	ReasonAbuse    = "abuse"
	ReasonIllegal  = "illegal"
	ReasonOffTopic = "off_topic"
	ReasonOther    = "other"

	StatusOpen      = "open"
	StatusReviewing = "reviewing"
	StatusResolved  = "resolved"
	StatusRejected  = "rejected"

	CodeReportNotFound      = "moderation.report_not_found"
	CodeReportInvalid       = "moderation.report_invalid"
	CodeReportDuplicate     = "moderation.report_duplicate"
	CodeReportTargetInvalid = "moderation.report_target_invalid"
	CodeReportTerminal      = "moderation.report_terminal"
	CodeReportConflict      = "moderation.report_conflict"
)

var (
	ErrReportNotFound      = errors.New("moderation: report not found")
	ErrReportInvalid       = errors.New("moderation: invalid report input")
	ErrReportDuplicate     = errors.New("moderation: duplicate open report")
	ErrReportTargetInvalid = errors.New("moderation: reported target not found or not reportable")
	// ErrReportTerminal：举报已结案（resolved/rejected）。结案结论是审计事实，
	// 不能被再次更新改写；需要改判只能走新的举报或人工数据处理。
	ErrReportTerminal = errors.New("moderation: report already closed")
	// ErrReportConflict：调用方持有的版本已过期（并发更新已被别人抢先提交）。
	ErrReportConflict = errors.New("moderation: report revision conflict")
)

// isTerminalStatus 判定举报状态是否已进入终态。
//
// 状态机是单向的：open/reviewing 是活跃态，可以互相流转或进入 resolved/rejected；
// resolved/rejected 是终态，任何再更新都必须失败。把这条不变式收在一个谓词里，
// 是为了让「更新被拒」与「更新未命中」共用同一个判定，而不是各处硬编码状态字面量。
func isTerminalStatus(status string) bool {
	return status == StatusResolved || status == StatusRejected
}

// Report 是 moderation_reports 行的领域结构。
type Report struct {
	ID             int64      `json:"id"`
	ReporterUserID int64      `json:"reporterUserId"`
	ReporterName   string     `json:"reporterName,omitempty"`
	TargetType     string     `json:"targetType"`
	TargetID       int64      `json:"targetId"`
	ReasonCode     string     `json:"reasonCode"`
	Body           string     `json:"body"`
	Status         string     `json:"status"`
	ReviewerUserID *int64     `json:"reviewerUserId,omitempty"`
	ReviewerName   string     `json:"reviewerName,omitempty"`
	ReviewNote     string     `json:"reviewNote"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	// Revision 是乐观锁版本，每次成功更新 +1；客户端下次写入时回传它。
	Revision int64 `json:"revision"`
}

// CreateReportInput 是举报创建入参。
type CreateReportInput struct {
	ReporterUserID int64
	TargetType     string
	TargetID       int64
	ReasonCode     string
	Body           string
}

// ReportListInput 是审核队列列表查询入参。
type ReportListInput struct {
	Status     string
	TargetType string
	ReporterID int64
	Page       int
	PerPage    int
}

// ReportList 是分页举报列表。
type ReportList struct {
	Items   []Report `json:"items"`
	Total   int64    `json:"total"`
	Page    int      `json:"page"`
	PerPage int      `json:"perPage"`
}

// UpdateReportInput 是审核更新入参。
type UpdateReportInput struct {
	ReportID       int64
	ReviewerUserID int64
	Status         string
	ReviewNote     string
	// ExpectedRevision 是调用方读到的版本；0 表示未声明期望版本（旧客户端），
	// 此时仍然受终态守卫约束，只是不做 CAS。
	ExpectedRevision int64
	// AllowReviewerOverride 只有 moderation.manage 才会被置位：审核人归属默认
	// 首占不变，改派审核人是管理动作。
	AllowReviewerOverride bool
}
