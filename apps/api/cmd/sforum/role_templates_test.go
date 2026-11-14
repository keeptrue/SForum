package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/roletemplates"
)

// seedTempRepo 造一个最小仓库骨架：AGENTS.md + extensions（findRepoRoot 的标记）
// + apps/api/go.mod + apps/web/app/config（apps 布局校验的标记）。
// 测试全程只在 t.TempDir() 里操作，不碰真实仓库文件。
func seedTempRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# temp repo\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "extensions"), 0o755); err != nil {
		t.Fatalf("create extensions: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "apps", "api"), 0o755); err != nil {
		t.Fatalf("create apps/api: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "api", "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "apps", "web", "app", "config"), 0o755); err != nil {
		t.Fatalf("create apps/web/app/config: %v", err)
	}
	return root
}

// TestRoleTemplatesOutputPathResolvesFromAnyWorkingDirectory 钉住「默认路径按仓库根解析」：
// 从 apps/api 和从仓库根跑，必须落到同一个文件。旧实现是 cwd 相对的 "../web/..."，
// 从仓库根跑会写到 repo 之外。
func TestRoleTemplatesOutputPathResolvesFromAnyWorkingDirectory(t *testing.T) {
	root := seedTempRepo(t)
	want := filepath.Join(root, filepath.FromSlash(roletemplates.DefaultOutput))

	for name, start := range map[string]string{
		"from-apps-api":  filepath.Join(root, "apps", "api"),
		"from-repo-root": root,
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(start)
			path, explicit, err := roleTemplatesOutputPath("")
			if err != nil {
				t.Fatalf("resolve default path: %v", err)
			}
			if explicit {
				t.Fatal("the default path must not be reported as explicit --out")
			}
			if path != want {
				t.Fatalf("path = %q, want %q", path, want)
			}
		})
	}
}

// TestRoleTemplatesWriteKeepsDefaultInsideRepo 默认写盘不得越出仓库根，也不得凭空建目录。
func TestRoleTemplatesWriteKeepsDefaultInsideRepo(t *testing.T) {
	root := seedTempRepo(t)
	t.Chdir(filepath.Join(root, "apps", "api"))
	target := filepath.Join(root, filepath.FromSlash(roletemplates.DefaultOutput))

	cmd := newRoleTemplatesCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--write"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--write with default path: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read generated catalog: %v", err)
	}
	if string(got) != roletemplates.Current() {
		t.Fatal("written catalog differs from the rendered seeds")
	}
	// 仓库根之外不该多出任何 ../web 目录（旧 cwd 相对实现的副作用）。
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "web")); err == nil {
		t.Fatal("--write escaped the repo root and created a sibling web directory")
	}
}

// TestRoleTemplatesOutputPathFailsOutsideRepo 解析失败必须报错且不落盘。
func TestRoleTemplatesOutputPathFailsOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	path, explicit, err := roleTemplatesOutputPath("")
	if err == nil {
		t.Fatalf("expected an error outside the repo, got path %q", path)
	}
	if path != "" || explicit {
		t.Fatalf("failed resolution must not return a path (path=%q explicit=%v)", path, explicit)
	}

	cmd := newRoleTemplatesCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--write"})
	if execErr := cmd.Execute(); execErr == nil {
		t.Fatal("--write must fail when the repo root cannot be resolved")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("read temp dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed resolution must not create anything, found %d entries", len(entries))
	}
}

// TestRoleTemplatesExplicitOutStaysCwdRelative 显式 --out 保持 cwd 相对语义，并允许建父目录。
func TestRoleTemplatesExplicitOutStaysCwdRelative(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	target := filepath.Join(dir, "nested", "catalog.ts")

	writeCmd := newRoleTemplatesCommand()
	writeCmd.SetOut(io.Discard)
	writeCmd.SetErr(io.Discard)
	writeCmd.SetArgs([]string{"--write", "--out", target})
	if err := writeCmd.Execute(); err != nil {
		t.Fatalf("--write --out: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read explicit output: %v", err)
	}
	if string(got) != roletemplates.Current() {
		t.Fatal("explicit output differs from the rendered seeds")
	}

	checkCmd := newRoleTemplatesCommand()
	checkCmd.SetOut(io.Discard)
	checkCmd.SetErr(&bytes.Buffer{})
	checkCmd.SetArgs([]string{"--check", "--out", target})
	if err := checkCmd.Execute(); err != nil {
		t.Fatalf("--check --out must accept a fresh file: %v", err)
	}
}
