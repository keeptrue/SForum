package notificationjobs

import (
	"strings"
	"unicode/utf8"
)

// errorSummaryLimit 是 mail_deliveries / notification_channel_deliveries 的
// error_summary 写入口径（字节）。
const errorSummaryLimit = 160

// truncateUTF8 按 UTF-8 字符边界裁剪。
//
// 不能按字节切：多字节字符被劈开后是非法 UTF-8，PostgreSQL 的 text 列直接拒写，
// 于是「最后一次调度写终态」这一步本身失败，记录又留回 sending —— RV-50 的
// 终态收敛会被截断方式整个抵消。先用 ToValidUTF8 兜住上游带进来的坏字节，
// 再退到最近的 rune 起点。
func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	clean := strings.ToValidUTF8(value, "\uFFFD")
	if len(clean) <= limit {
		return clean
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(clean[cut]) {
		cut--
	}
	return clean[:cut]
}
