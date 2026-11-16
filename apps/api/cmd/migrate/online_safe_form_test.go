package main

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

const safeMigration = "-- +goose Up\n-- +sforum OnlineSafe\nSET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\nSELECT 1;\n-- +goose Down\n"

func TestValidateArgumentsAcceptsOnlineSafeForm(t *testing.T) {
	for _, flag := range []string{"--check-no-pending", "--check-online-safe", "--check-online-safe-form"} {
		if err := validateArguments([]string{flag}); err != nil {
			t.Fatalf("%s must be accepted: %v", flag, err)
		}
	}
	if err := validateArguments([]string{"--check-online-safe-form", "--check-online-safe"}); err == nil {
		t.Fatal("two flags must stay rejected")
	}
}

// 未声明只报告、不红：离线迁移是合法形态。
func TestRunOnlineSafeFormCheckReportsUndeclaredWithoutFailing(t *testing.T) {
	files := fstest.MapFS{
		"00001_safe.sql":    {Data: []byte(safeMigration)},
		"00002_offline.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n")},
	}
	var out bytes.Buffer
	code := runOnlineSafeFormCheck(&out, files)
	if code != 0 {
		t.Fatalf("undeclared migrations must not fail the check: code=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "scanned=2 declared=1 undeclared=1 malformed=0") {
		t.Fatalf("unexpected report: %s", out.String())
	}
}

// 畸形声明必须红：NO TRANSACTION 与缺 SET LOCAL 各一条。
func TestRunOnlineSafeFormCheckFailsMalformedDeclarations(t *testing.T) {
	files := fstest.MapFS{
		"00001_nontx.sql": {Data: []byte(
			"-- +goose Up\n-- +goose NO TRANSACTION\n-- +sforum OnlineSafe\n" +
				"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\n-- +goose Down\n")},
		"00002_nolock.sql": {Data: []byte(
			"-- +goose Up\n-- +sforum OnlineSafe\nSET LOCAL statement_timeout = '1min';\nSELECT 1;\n-- +goose Down\n")},
	}
	var out bytes.Buffer
	code := runOnlineSafeFormCheck(&out, files)
	if code != 3 {
		t.Fatalf("malformed declarations must exit 3, got %d (%s)", code, out.String())
	}
	report := out.String()
	for _, want := range []string{
		"malformed 00001_nontx.sql: online migrations must run transactionally",
		"malformed 00002_nolock.sql: missing SET LOCAL lock_timeout",
		"scanned=2 declared=0 undeclared=0 malformed=2",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

// Down 段里的 directive 不算声明：这是最容易写错的一处（旧用例已钉，离线门同样钉）。
func TestRunOnlineSafeFormCheckIgnoresDownSectionDirective(t *testing.T) {
	files := fstest.MapFS{
		"00001_down.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n-- +sforum OnlineSafe\n")},
	}
	var out bytes.Buffer
	if code := runOnlineSafeFormCheck(&out, files); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), "undeclared=1") {
		t.Fatalf("Down-section directive must count as undeclared: %s", out.String())
	}
}

// 读不到迁移文件是 1（基础设施问题），不是 3（形态不合格）。
func TestRunOnlineSafeFormCheckReportsReadFailure(t *testing.T) {
	var out bytes.Buffer
	if code := runOnlineSafeFormCheck(&out, failingFS{}); code != 1 {
		t.Fatalf("unreadable migrations must exit 1, got %d", code)
	}
	if !strings.Contains(out.String(), "online-safe-form check failed") {
		t.Fatalf("unexpected report: %s", out.String())
	}
}

type failingFS struct{}

func (failingFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }
