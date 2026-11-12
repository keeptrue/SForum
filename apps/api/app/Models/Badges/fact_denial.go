package badges

import (
	"errors"
	"fmt"
)

// FactWriteDenial 是「插件事实上报被拒」的领域错误。
//
// 为什么需要一个**有生产者**的类型：顶层错误码 `forum.badge_fact_write_denied`
// 与协议 `ErrorDetail.reason`（六个冻结字面量）必须同源。如果命令自己拼字符串、
// HTTP 侧再凭 `errors.Is(ErrBadgeFactWriteDenied)` 映射，那个 sentinel 就永远没有
// 生产者——映射成了死代码，§13#19 的「新码没 case 落 default → 500」会换个形式复发。
//
// 形状选择：`Reason` 是不可变字段（构造后不改），`Unwrap` 指向 sentinel，让
// `errors.Is` 与 `errors.As` 两条路都能用：HTTP 侧按 sentinel 取错误码，
// 命令侧按 `*FactWriteDenial` 取 reason 投影协议错误。
type FactWriteDenial struct {
	// Reason 是冻结的拒绝原因（spec §9 Errata 3 的六个字面量之一）。
	Reason string
}

// NewBadgeFactWriteDenied 构造一次拒绝。
//
// reason 由调用方（Host 命令）传入：原因是 wire 词汇，域层不复制那份字面量表，
// 否则两边各有一份常量，漂移时没人会发现。
func NewBadgeFactWriteDenied(reason string) error {
	return &FactWriteDenial{Reason: reason}
}

func (d *FactWriteDenial) Error() string {
	if d == nil {
		return "badges: fact write denied"
	}
	return fmt.Sprintf("badges: fact write denied (%s)", d.Reason)
}

// Unwrap 让 `errors.Is(err, ErrBadgeFactWriteDenied)` 成立，HTTP 映射据此取顶层码。
func (d *FactWriteDenial) Unwrap() error { return ErrBadgeFactWriteDenied }

// BadgeFactWriteDenialReason 从错误链里取冻结 reason；取不到时回退到
// `fact_write_failed`（未知错误按存储失败处理，绝不猜成参数错误）。
func BadgeFactWriteDenialReason(err error) string {
	var denial *FactWriteDenial
	if errors.As(err, &denial) && denial != nil && denial.Reason != "" {
		return denial.Reason
	}
	return "fact_write_failed"
}
