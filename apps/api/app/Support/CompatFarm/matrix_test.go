package compatfarm

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	apilts "github.com/keeptrue/sforum/apps/api/app/Support/APILTS"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// apps/api/app/Support/CompatFarm -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", ".."))
}

func TestLoadMatrixShape(t *testing.T) {
	root := repoRoot(t)
	matrixPath := filepath.Join(root, "tests", "compat", "matrix.yaml")
	matrix, err := LoadMatrix(matrixPath)
	if err != nil {
		t.Fatal(err)
	}
	if matrix.Schema != SchemaVersion || len(matrix.RequiredCells()) < 1 {
		t.Fatalf("matrix = %#v", matrix)
	}
	if len(matrix.DeprecatedCells()) != 0 {
		t.Fatalf("removed compatibility cells remain: %#v", matrix.DeprecatedCells())
	}
}

// TestRunMatrixGatePassesRequired 跑完整农场：真实进程 + RPC。
func TestRunMatrixGatePassesRequired(t *testing.T) {
	if testing.Short() {
		t.Skip("compat farm builds real plugin binaries")
	}
	root := repoRoot(t)
	matrixPath := filepath.Join(root, "tests", "compat", "matrix.yaml")
	// CI 使用隔离变量，避免让整轮 go test 误启共享数据库集成测试。
	// 本地仍兼容通用变量，并默认连接开发 compose 的 15432 端口。
	dbURL := os.Getenv("SFORUM_COMPAT_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = os.Getenv("SFORUM_TEST_DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = "postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable"
	}
	result, err := RunMatrix(context.Background(), matrixPath, RunOptions{
		RepoRoot:    root,
		LTS:         apilts.New(),
		DatabaseURL: dbURL,
		WorkDir:     t.TempDir(),
		Now:         func() time.Time { return time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		for _, cell := range result.Cells {
			t.Logf("cell %s outcome=%s msg=%s evidence=%#v", cell.ID, cell.Outcome, cell.Message, cell.Evidence)
		}
		t.Fatalf("farm not ok")
	}
	if len(result.Cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(result.Cells))
	}
	for _, cell := range result.Cells {
		if cell.Outcome != OutcomePass {
			t.Fatalf("cell %s outcome=%s msg=%s", cell.ID, cell.Outcome, cell.Message)
		}
		if !cell.Evidence.ProcessStarted {
			t.Fatalf("cell %s missing process start evidence", cell.ID)
		}
		if cell.Evidence.Request == "" || cell.Evidence.Response == "" {
			t.Fatalf("cell %s missing request/response evidence: %#v", cell.ID, cell.Evidence)
		}
	}
}

func TestRunMatrixMissingFileFailsGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-matrix.yaml")
	result, err := RunMatrix(context.Background(), path, RunOptions{RepoRoot: repoRoot(t)})
	if err == nil {
		t.Fatal("expected error for missing matrix")
	}
	if result.OK {
		t.Fatal("missing matrix must not be OK")
	}
	if len(result.Cells) == 0 || result.Cells[0].Outcome != OutcomeMissing {
		t.Fatalf("want missing outcome: %#v", result.Cells)
	}
}

func TestRunMatrixRejectsSkipAsGateFailure(t *testing.T) {
	// 未知 protocol/manifest 组合必须 skip 并失败门禁，不得静默通过。
	dir := t.TempDir()
	path := filepath.Join(dir, "matrix.yaml")
	body := `version: 1
schema: sforum.compat-farm@1
cells:
  - id: unknown-combo
    sforum: current
    protocol: v9
    manifest: v99
    database: none
    browser: none
    status: required
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunMatrix(context.Background(), path, RunOptions{RepoRoot: repoRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("skipped required cell must fail gate")
	}
	if result.Cells[0].Outcome != OutcomeSkip {
		t.Fatalf("outcome = %s", result.Cells[0].Outcome)
	}
}
