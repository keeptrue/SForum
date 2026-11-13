package conditions

import (
	"testing"
	"time"

	timezone "github.com/keeptrue/sforum/apps/api/app/Support/Timezone"
)

// calendarDaysBetween 是 `identity.account_age` 的算术核心。它必须按**日历日**而非
// 小时数折算：夏令时切换日只有 23/25 小时，`Sub/Hours/24` 会算出 0。
func TestCalendarDaysBetween(t *testing.T) {
	shanghai := time.FixedZone("Asia/Shanghai", 8*3600)
	// 必须经 `timezone.Load`：运行镜像不带系统 tzdata，`time.LoadLocation` 在
	// alpine/scratch/distroless 上解析不了非 UTC 时区（tests/validate-runtime-tzdata.mjs
	// 把这条当部署硬约束）。`timezone` 包 blank-import 了 `time/tzdata`，所以这里
	// 的 DST 用例在容器里同样成立，不是「本机碰巧有 zoneinfo」。
	newYork, err := timezone.Load("America/New_York")
	if err != nil {
		// 内嵌库缺条目属于构建问题，直接红：静默跳过会让 DST 用例悄悄消失。
		t.Fatalf("timezone.Load(America/New_York) 失败：%v", err)
	}

	cases := []struct {
		name string
		from time.Time
		to   time.Time
		want int64
	}{
		{
			name: "普通跨天（进程本地日）",
			from: time.Date(2026, 11, 3, 4, 25, 0, 0, shanghai),
			to:   time.Date(2026, 11, 13, 4, 25, 0, 0, shanghai),
			want: 10,
		},
		{
			name: "同一本地日内的两个时刻算 0 天",
			from: time.Date(2026, 11, 13, 0, 5, 0, 0, shanghai),
			to:   time.Date(2026, 11, 13, 23, 55, 0, 0, shanghai),
			want: 0,
		},
		{
			name: "跨零点 1 分钟也算 1 个日历日",
			from: time.Date(2026, 11, 12, 23, 59, 0, 0, shanghai),
			to:   time.Date(2026, 11, 13, 0, 1, 0, 0, shanghai),
			want: 1,
		},
		{
			name: "未来时刻夹到 0（GREATEST 语义）",
			from: time.Date(2026, 11, 15, 4, 25, 0, 0, shanghai),
			to:   time.Date(2026, 11, 13, 4, 25, 0, 0, shanghai),
			want: 0,
		},
	}
	// 2026-03-08 是美东夏令时切换日（只有 23 小时）。按小时折算会得到 0，
	// 按日历日必须得到 1。
	cases = append(cases, struct {
		name string
		from time.Time
		to   time.Time
		want int64
	}{
		name: "夏令时切换日（23 小时）仍算 1 个日历日",
		from: time.Date(2026, 3, 7, 23, 30, 0, 0, newYork),
		to:   time.Date(2026, 3, 8, 0, 30, 0, 0, newYork),
		want: 1,
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := calendarDaysBetween(tc.from, tc.to); got != tc.want {
				t.Fatalf("calendarDaysBetween = %d，期望 %d", got, tc.want)
			}
		})
	}
}
