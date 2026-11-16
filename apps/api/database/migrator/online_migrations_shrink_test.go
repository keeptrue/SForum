package migrator

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"unicode"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

// 收缩型 DDL 棘轮的正反例。
//
// 正例：登记了就放行；扩张型 DDL 不受影响；未声明 OnlineSafe 的迁移不受影响。
// 反例：声明了 OnlineSafe 却在 Up 段收缩 schema 且没登记 → malformed（CI 退出码 3）。

const onlineSafePrologue = "-- +goose Up\n-- +sforum OnlineSafe\n" +
	"SET LOCAL lock_timeout = '5s';\nSET LOCAL statement_timeout = '1min';\n"

func migrationWith(upBody string) fstest.MapFS {
	return fstest.MapFS{
		"00001_shrink.sql": {Data: []byte(onlineSafePrologue + upBody + "\n-- +goose Down\n")},
	}
}

// 反例：未登记的 DROP COLUMN 必须判 malformed，且原因点名语句。
func TestOnlineSafeShrinkDDLRequiresRegistration(t *testing.T) {
	files := migrationWith("ALTER TABLE users DROP COLUMN nickname;")
	declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_shrink.sql", nil)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if declaration.Form != OnlineSafeFormMalformed {
		t.Fatalf("form = %q, want malformed（未登记的收缩 DDL 必须红）", declaration.Form)
	}
	for _, want := range []string{"shrink DDL requires explicit registration", "drop column", "ALTER TABLE users DROP COLUMN nickname"} {
		if !strings.Contains(declaration.Reason, want) {
			t.Fatalf("reason = %q, want it to mention %q", declaration.Reason, want)
		}
	}
}

// 正例：同一条语句登记后放行（Migration + Statement 都对上）。
func TestOnlineSafeShrinkDDLRegisteredIsAllowed(t *testing.T) {
	files := migrationWith("ALTER TABLE users DROP COLUMN nickname;")
	registry := []ShrinkDDLRegistration{{
		Migration: "00001_shrink.sql",
		Statement: "ALTER TABLE users DROP COLUMN nickname",
		Reason:    "测试：先扩后缩已排在回滚窗口之后",
	}}
	declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_shrink.sql", registry)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if declaration.Form != OnlineSafeFormDeclared {
		t.Fatalf("form = %q, want declared（已登记就必须放行）", declaration.Form)
	}
}

// 登记的匹配键必须同时对上迁移与语句：换个迁移名不算已登记。
func TestOnlineSafeShrinkDDLRegistrationIsPerMigration(t *testing.T) {
	files := migrationWith("ALTER TABLE users DROP COLUMN nickname;")
	registry := []ShrinkDDLRegistration{{
		Migration: "99999_other.sql",
		Statement: "ALTER TABLE users DROP COLUMN nickname",
		Reason:    "测试：登记在别的迁移上，不该放行本迁移",
	}}
	declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_shrink.sql", registry)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if declaration.Form != OnlineSafeFormMalformed {
		t.Fatalf("form = %q, want malformed（登记必须按迁移隔离）", declaration.Form)
	}
}

// 三种收缩形态都要认出来；扩张型写法一律不许误报。
func TestOnlineSafeShrinkDDLKinds(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		wantKind  string
	}{
		{"drop column", "ALTER TABLE users DROP COLUMN IF EXISTS nickname;", "drop column"},
		{"rename column", "ALTER TABLE users RENAME COLUMN nickname TO display_name;", "rename column"},
		{"alter type", "ALTER TABLE users ALTER COLUMN nickname TYPE varchar(32);", "alter column type"},
		{"alter type set data type", "ALTER TABLE users ALTER COLUMN nickname SET DATA TYPE varchar(32);", "alter column type"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			files := migrationWith(testCase.statement)
			declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_shrink.sql", nil)
			if err != nil {
				t.Fatalf("classify: %v", err)
			}
			if declaration.Form != OnlineSafeFormMalformed {
				t.Fatalf("%q 未被识别为收缩 DDL", testCase.statement)
			}
			if !strings.Contains(declaration.Reason, testCase.wantKind) {
				t.Fatalf("reason = %q, want kind %q", declaration.Reason, testCase.wantKind)
			}
		})
	}
}

func TestOnlineSafeExpansionDDLIsAllowed(t *testing.T) {
	files := migrationWith(strings.Join([]string{
		"CREATE TABLE demo (id bigint PRIMARY KEY);",
		"ALTER TABLE demo ADD COLUMN IF NOT EXISTS label text;",
		"ALTER TABLE demo ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();",
		"ALTER TABLE demo ALTER COLUMN label DROP NOT NULL;",
		"CREATE INDEX IF NOT EXISTS demo_label_idx ON demo (label);",
		"ALTER TABLE demo ADD CONSTRAINT demo_label_check CHECK (label <> '') NOT VALID;",
		"CREATE OR REPLACE VIEW demo_v AS SELECT id, label FROM demo;",
	}, "\n"))
	declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_shrink.sql", nil)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if declaration.Form != OnlineSafeFormDeclared {
		t.Fatalf("扩张型 DDL 被误判为收缩：%s", declaration.Reason)
	}
}

// 注释与美元引用块里的字样不算语句（否则一条说明文字就能把门骗红）。
func TestOnlineSafeShrinkDDLDetectorIgnoresCommentsAndQuotedBodies(t *testing.T) {
	files := migrationWith(strings.Join([]string{
		"-- 这条迁移**不**做 DROP COLUMN nickname，见决定记录",
		"/* 也不要 RENAME COLUMN */",
		"CREATE OR REPLACE FUNCTION demo_guard() RETURNS trigger AS $$",
		"BEGIN",
		"  EXECUTE 'ALTER TABLE demo DROP COLUMN unused';",
		"  RETURN NEW;",
		"END;",
		"$$ LANGUAGE plpgsql;",
	}, "\n"))
	declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_shrink.sql", nil)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if declaration.Form != OnlineSafeFormDeclared {
		t.Fatalf("注释/函数体里的字样被当成真语句：%s", declaration.Reason)
	}
}

// 未声明 OnlineSafe 的迁移不参与这条棘轮：它本来就走停机窗口。
func TestUndeclaredMigrationMayShrink(t *testing.T) {
	files := fstest.MapFS{
		"00001_offline.sql": {Data: []byte("-- +goose Up\nALTER TABLE users DROP COLUMN nickname;\n-- +goose Down\n")},
	}
	declaration, err := classifyOnlineSafeFormWithRegistry(files, "00001_offline.sql", nil)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if declaration.Form != OnlineSafeFormUndeclared {
		t.Fatalf("form = %q, want undeclared（未声明的迁移不该被这条棘轮判红）", declaration.Form)
	}
}

// 反向自检：登记表本身不许烂掉 —— 每条都要有中文理由，且迁移与语句真的对得上。
//
// 只钉「登记项有效」，不钉「登记表必须为空」：真要做收缩时登记是合法路径，
// 但登记了却对不上实现（迁移改名/语句改写）会让棘轮静默失效，那必须红。
func TestRegisteredShrinkDDLEntriesAreWellFormed(t *testing.T) {
	for _, entry := range registeredShrinkDDL {
		if entry.Migration == "" || entry.Statement == "" {
			t.Fatalf("登记项缺少迁移名或语句：%#v", entry)
		}
		if !hasHanRune(entry.Reason) {
			t.Fatalf("登记项缺少中文理由：%#v", entry)
		}
		body, err := fs.ReadFile(migrations.Files(), entry.Migration)
		if err != nil {
			t.Fatalf("登记项指向不存在的迁移 %q：%v", entry.Migration, err)
		}
		upBody := upSectionOf(string(body))
		if len(unregisteredShrinkDDL(entry.Migration, upBody, registeredShrinkDDL)) == 0 {
			continue // 这条迁移的收缩语句全部已登记
		}
		detected := detectShrinkDDL(upBody)
		matched := false
		for _, statement := range detected {
			if statement.Statement == entry.Statement {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf(
				"登记项 %q 的语句 %q 在该迁移 Up 段里找不到（迁移被改写后必须同步登记表，否则棘轮静默失效）",
				entry.Migration,
				entry.Statement,
			)
		}
	}
}

// upSectionOf 取 Up 段文本（到 `-- +goose Down` 为止），与 classifyOnlineSafeForm 同口径。
func upSectionOf(body string) string {
	lines := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "-- +goose Down" {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func hasHanRune(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
