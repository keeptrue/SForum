package main

import (
	"strings"
	"testing"
)

// seed 的 `database:` 行必须走 config 的脱敏实现（R7-OPS-02 复审）。
//
// 调用面没有断言时，把 config.RedactDatabaseURL 改成 `return raw`、或者退回到当年那份
// 按第一个 `@` 切分的实现，`go test ./config/ ./cmd/sforum/` 依然全绿：config 的
// TestRedactDatabaseURLShapes 打的是包内那份，管不到 seed 到底用了谁。
func TestSeedDatabaseSummaryRedactsCredentials(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		forbids []string
	}{
		{
			// 口令带 `@`：按第一个 `@` 切会输出 postgres://u:***@ss@host:5432/db。
			name:    "password containing at signs",
			raw:     "postgres://u:p@ss@host:5432/db",
			want:    "postgres://u:***@host:5432/db",
			forbids: []string{"p@ss", "p%40ss"},
		},
		{
			// query 可能带 password=，必须整段丢弃。
			name:    "query carrying a password",
			raw:     "postgres://u:secret@host:5432/db?password=secret",
			want:    "postgres://u:***@host:5432/db",
			forbids: []string{"secret", "password="},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := seedDatabaseSummary(testCase.raw)
			if got != testCase.want {
				t.Fatalf("seedDatabaseSummary(%q) = %q, want %q", testCase.raw, got, testCase.want)
			}
			for _, forbidden := range testCase.forbids {
				if strings.Contains(got, forbidden) {
					t.Fatalf("seedDatabaseSummary(%q) = %q 含 %q", testCase.raw, got, forbidden)
				}
			}
		})
	}
}
