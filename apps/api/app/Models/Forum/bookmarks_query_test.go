package forum

import (
	"strings"
	"testing"
)

// TestBookmarkListReminderColumnsGatesOwnerOnlyData 是 SQL 层那道闸的静态锁。
//
// 提醒时间是行主私有计划数据：他人视角必须在 SELECT 里就把两列置 NULL
// （CASE WHEN $4），而不是靠 Go 侧事后清空。端到端用例里 Go 兜底会遮蔽这一层，
// 所以这里直接断言构造出来的列表达式——删掉 CASE 只留兜底，这条会红。
//
// 只断关键片段（占位符与列名），不绑死整段 SQL：列顺序、空白、其它 SELECT
// 字段的调整都不该让这条用例失败。
func TestBookmarkListReminderColumnsGatesOwnerOnlyData(t *testing.T) {
	t.Run("other viewer gates both columns", func(t *testing.T) {
		columns, args := bookmarkListReminderColumns(false)
		if !strings.Contains(columns, "CASE WHEN $4") {
			t.Fatalf("other-view columns must gate on the owner flag: %q", columns)
		}
		if strings.Count(columns, "CASE WHEN $4") != 2 {
			t.Fatalf("both reminder columns must be gated, got %q", columns)
		}
		for _, column := range []string{"bookmarks.remind_at", "bookmarks.reminded_at"} {
			if !strings.Contains(columns, column) {
				t.Fatalf("other-view columns missing %q: %q", column, columns)
			}
		}
		// 布尔参数必须是 false（$4 为假 → CASE 结果 NULL），且只追加一个。
		if len(args) != 1 || args[0] != false {
			t.Fatalf("other-view args = %#v, want [false]", args)
		}
	})

	t.Run("owner reads both columns directly", func(t *testing.T) {
		columns, args := bookmarkListReminderColumns(true)
		if strings.Contains(columns, "CASE WHEN") {
			t.Fatalf("owner view must read the columns directly: %q", columns)
		}
		for _, column := range []string{"bookmarks.remind_at", "bookmarks.reminded_at"} {
			if !strings.Contains(columns, column) {
				t.Fatalf("owner-view columns missing %q: %q", column, columns)
			}
		}
		if len(args) != 0 {
			t.Fatalf("owner view must not append the gate arg, got %#v", args)
		}
	})
}
