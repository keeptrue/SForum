package migrator

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestClassifyOnlineSafeFormThreeStates(t *testing.T) {
	files := fstest.MapFS{
		"00001_declared.sql": {Data: []byte(
			"-- +goose Up\n-- +sforum OnlineSafe\n" +
				"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\nSELECT 1;\n-- +goose Down\n")},
		"00002_offline.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n")},
		"00003_nontx.sql": {Data: []byte(
			"-- +goose Up\n-- +goose NO TRANSACTION\n-- +sforum OnlineSafe\n" +
				"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\n-- +goose Down\n")},
		"00004_nolock.sql": {Data: []byte(
			"-- +goose Up\n-- +sforum OnlineSafe\nSET LOCAL statement_timeout = '1min';\n-- +goose Down\n")},
		"00005_nostmt.sql": {Data: []byte(
			"-- +goose Up\n-- +sforum OnlineSafe\nSET LOCAL lock_timeout = '5s';\n-- +goose Down\n")},
	}
	report, err := CheckOnlineSafeForm(files)
	if err != nil {
		t.Fatalf("CheckOnlineSafeForm: %v", err)
	}
	declared, undeclared, malformed := report.Counts()
	if declared != 1 || undeclared != 1 || malformed != 3 {
		t.Fatalf("counts = %d/%d/%d, want 1/1/3", declared, undeclared, malformed)
	}
	reasons := map[string]string{}
	for _, declaration := range report.Malformed() {
		reasons[declaration.Path] = declaration.Reason
	}
	if reasons["00003_nontx.sql"] != "online migrations must run transactionally" {
		t.Fatalf("NO TRANSACTION reason = %q", reasons["00003_nontx.sql"])
	}
	if reasons["00004_nolock.sql"] != "missing SET LOCAL lock_timeout" {
		t.Fatalf("missing lock_timeout reason = %q", reasons["00004_nolock.sql"])
	}
	if reasons["00005_nostmt.sql"] != "missing SET LOCAL statement_timeout" {
		t.Fatalf("missing statement_timeout reason = %q", reasons["00005_nostmt.sql"])
	}
}

// 布尔投影必须与三态判定同源：declared=true 仅当形态是 declared，畸形必须带原因。
func TestMigrationDeclaresOnlineSafeMatchesForm(t *testing.T) {
	files := fstest.MapFS{
		"00001_declared.sql": {Data: []byte(
			"-- +goose Up\n-- +sforum OnlineSafe\n" +
				"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\n-- +goose Down\n")},
		"00002_offline.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n")},
		"00003_nontx.sql": {Data: []byte(
			"-- +goose Up\n-- +goose NO TRANSACTION\n-- +sforum OnlineSafe\n-- +goose Down\n")},
	}
	if declared, err := migrationDeclaresOnlineSafe(files, "00001_declared.sql"); err != nil || !declared {
		t.Fatalf("declared migration = (%v, %v)", declared, err)
	}
	if declared, err := migrationDeclaresOnlineSafe(files, "00002_offline.sql"); err != nil || declared {
		t.Fatalf("undeclared migration = (%v, %v)", declared, err)
	}
	if _, err := migrationDeclaresOnlineSafe(files, "00003_nontx.sql"); err == nil ||
		!strings.Contains(err.Error(), "transactionally") {
		t.Fatalf("malformed migration error = %v", err)
	}
}

// 常设门：嵌入式迁移集里不允许出现畸形声明（离线即可判定，不需要数据库）。
func TestEmbeddedOnlineSafeFormsAreWellFormed(t *testing.T) {
	report, err := CheckOnlineSafeForm(migrations.Files())
	if err != nil {
		t.Fatalf("CheckOnlineSafeForm: %v", err)
	}
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	expected := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			expected++
		}
	}
	if len(report.Declarations) != expected {
		t.Fatalf("scanned %d migrations, want %d", len(report.Declarations), expected)
	}
	if malformed := report.Malformed(); len(malformed) > 0 {
		t.Fatalf("embedded migrations contain malformed online declarations: %#v", malformed)
	}
	declared, undeclared, _ := report.Counts()
	if declared == 0 || undeclared == 0 {
		t.Fatalf("expected both declared and undeclared migrations in the embedded set: %d/%d", declared, undeclared)
	}
}
