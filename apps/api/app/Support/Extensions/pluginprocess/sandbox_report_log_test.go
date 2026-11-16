//go:build linux

package pluginprocess

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// F2：Report.Notes 是运维唯一的「落点偏移 / 锁降级」信号，必须在日志边界真的出现。
// 旧实现两个分支都只写固定字段，notes 在 logLaunchReport 里被静默丢掉——
// 与 F1（Delegate=yes 单独不成立）叠加时，日志一切正常但组落在 system.slice。
func TestLogLaunchReportIncludesNotes(t *testing.T) {
	const note = "cgroup lands in the shared slice /sys/fs/cgroup/system.slice, outside the API unit"
	cases := []struct {
		name    string
		report  Report
		level   string
		message string
	}{
		{
			name: "enforced",
			report: Report{
				PluginID: "demo.plugin", PID: 42,
				Filesystem: StatusEnforced, Network: StatusDisabled, Cgroup: StatusEnforced,
				CgroupPath: "/sys/fs/cgroup/system.slice/sforum.service/sforum-plugin-demo",
				Notes:      []string{note, "landlock fs rules: 3 added"},
			},
			level: "INFO", message: "plugin sandbox enforced",
		},
		{
			name: "degraded",
			report: Report{
				PluginID: "demo.plugin", PID: 43,
				Filesystem: StatusEnforced, Network: StatusDisabled, Cgroup: StatusDegraded,
				CgroupReason: "create cgroup: permission denied",
				Notes:        []string{note, "cgroup create lock unavailable: timed out after 2s"},
			},
			level: "WARN", message: "plugin sandbox degraded",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var buffer bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(previous) })

			logLaunchReport(test.report)

			out := buffer.String()
			if !strings.Contains(out, `"msg":"`+test.message+`"`) {
				t.Fatalf("log line missing: %s", out)
			}
			if !strings.Contains(out, `"level":"`+test.level+`"`) {
				t.Fatalf("log level = %s, want %s: %s", out, test.level, out)
			}
			for _, want := range test.report.Notes {
				if !strings.Contains(out, want) {
					t.Fatalf("note %q missing from the log line: %s", want, out)
				}
			}
		})
	}
}

// 没有 notes 时字段仍在（null/空数组），让「无提示」与「提示被吞」在日志里可区分。
func TestLogLaunchReportKeepsEmptyNotesField(t *testing.T) {
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	logLaunchReport(Report{PluginID: "demo.plugin", PID: 44, Filesystem: StatusEnforced, Cgroup: StatusEnforced})
	out := buffer.String()
	if !strings.Contains(out, `"notes":`) {
		t.Fatalf("empty notes must still be rendered as a field: %s", out)
	}
	if strings.Contains(out, `"notes":null`) == false && strings.Contains(out, `"notes":[]`) == false {
		t.Fatalf("unexpected empty-notes encoding: %s", out)
	}
}
