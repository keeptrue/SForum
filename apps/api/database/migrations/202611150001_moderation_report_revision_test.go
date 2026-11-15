package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModerationReportRevisionMigration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("202611150001_moderation_report_revision.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sections := strings.Split(string(raw), "-- +goose Down")
	if len(sections) != 2 {
		t.Fatal("moderation report revision migration has no Down section")
	}
	up := sections[0]
	for _, fragment := range []string{
		"-- +sforum OnlineSafe",
		"SET LOCAL lock_timeout",
		"SET LOCAL statement_timeout",
		"ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;",
		"ADD CONSTRAINT moderation_reports_revision_positive CHECK (revision > 0) NOT VALID",
		"VALIDATE CONSTRAINT moderation_reports_revision_positive",
	} {
		if !strings.Contains(up, fragment) {
			t.Fatalf("moderation report revision migration missing %q", fragment)
		}
	}

	// OnlineSafe 清单第 4 条：CHECK 内联在 ADD COLUMN 里会在 ACCESS EXCLUSIVE 下
	// 扫全表校验；必须 NOT VALID + 后续 VALIDATE。这条断言防回归，不靠人读。
	if strings.Contains(up, "DEFAULT 1 CHECK") {
		t.Fatal("CHECK must not be inlined into ADD COLUMN; use NOT VALID + VALIDATE CONSTRAINT")
	}

	// 顺序也属于契约：先登记（NOT VALID）再校验，反了就退化成全表阻塞扫描。
	addColumn := strings.Index(up, "ADD COLUMN IF NOT EXISTS revision")
	addConstraint := strings.Index(up, "ADD CONSTRAINT moderation_reports_revision_positive")
	validate := strings.Index(up, "VALIDATE CONSTRAINT moderation_reports_revision_positive")
	if !(addColumn < addConstraint && addConstraint < validate) {
		t.Fatalf("expected ADD COLUMN < ADD CONSTRAINT NOT VALID < VALIDATE, got %d/%d/%d", addColumn, addConstraint, validate)
	}

	// 旧二进制的写入不设置 revision；没有默认值就会让零停机窗口里的旧写入直接失败。
	if !strings.Contains(up, "DEFAULT 1") {
		t.Fatal("revision must carry a default so the previous binary keeps writing")
	}

	down := sections[1]
	if !strings.Contains(down, "DROP COLUMN IF EXISTS revision") {
		t.Fatalf("moderation report revision down migration missing column drop: %s", down)
	}
}
