package extensionpackage

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 同一棵插件树有两个写者：host dev watcher（本包）与发行脚本
// sforum-app-store 的 `plugin/scripts/build.sh` + `sync-digests.mjs`。
// 两边必须产出逐字节相同的二进制与 manifest——只要有一处口径不同，插件树就会
// 在两个 digest 之间永久抖动，发版树永远 `git status` 不干净。
//
// 这两条测试把口径钉在代码里：改任何一边都必须同时改另一边并更新断言。

func TestBackendBuildArgsMatchReleaseBuilder(t *testing.T) {
	// 对照 plugin/scripts/build.sh:
	//   CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o plugin ./
	// 唯一允许不同的是输出路径（dev 先写 .tmp 再按字节比较替换）。
	want := []string{
		"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w",
		"-o", "backend/plugin.tmp", "./",
	}
	got := backendBuildArgs("backend/plugin.tmp")
	if len(got) != len(want) {
		t.Fatalf("backend build argv = %q, want %q", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("backend build argv[%d] = %q, want %q (full: %q)", index, got[index], want[index], got)
		}
	}
}

func TestMarshalManifestJSONMatchesReleaseSerializer(t *testing.T) {
	// 同一份文档经 sync-digests.mjs 的 `JSON.stringify(manifest, null, 2) + "\n"`
	// 落盘时的字节形态：键排序、2 空格缩进、结尾换行，且 `<` `>` `&` 不转义。
	manifest := map[string]any{
		"sforumVersion": ">=1.0.0 <2.0.0",
		"description":   "supports a<b> & c",
		"packageFiles": []any{
			map[string]any{"path": "backend/plugin", "digest": "abc"},
		},
	}
	body, err := marshalManifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "description": "supports a<b> & c",
  "packageFiles": [
    {
      "digest": "abc",
      "path": "backend/plugin"
    }
  ],
  "sforumVersion": ">=1.0.0 <2.0.0"
}
`
	if string(body) != want {
		t.Fatalf("manifest bytes diverged from the release serializer:\n got: %q\nwant: %q", body, want)
	}
	for _, escaped := range []string{`\u003e`, `\u003c`, `\u0026`} {
		if bytes.Contains(body, []byte(escaped)) {
			t.Fatalf("manifest still HTML-escapes %s: %s", escaped, body)
		}
	}
}

func TestBackendBuildPlanPinsToolchainAndWorkspace(t *testing.T) {
	// `go` 指令只是下限；replace 的文件系统路径会被逐字写进 buildinfo。
	// 两个构建者（dev watcher 与发行脚本）必须消掉这两个变量才可能产出同一份字节。
	dir := t.TempDir()
	depDir := filepath.Join(dir, "sibling", "core", "apps", "api")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "module example.com/plugin\n\ngo 1.26.10\n\n" +
		"require example.com/core/apps/api v0.0.0\n\n" +
		"replace example.com/core/apps/api => ./sibling/core/apps/api\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := newBackendBuildPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()

	if plan.Work == "" {
		t.Fatal("plan should create a workspace for a filesystem replace")
	}
	body, err := os.ReadFile(plan.Work)
	if err != nil {
		t.Fatal(err)
	}
	wantUse := filepath.Clean(depDir)
	if !strings.Contains(string(body), wantUse) {
		t.Fatalf("go.work should use the resolved absolute path %q:\n%s", wantUse, body)
	}
	if !strings.Contains(string(body), "go 1.26.10") {
		t.Fatalf("go.work should carry the module go directive:\n%s", body)
	}
	for _, want := range []string{"GOTOOLCHAIN=go1.26.10", "GOWORK=" + plan.Work} {
		found := false
		for _, entry := range plan.Env {
			if entry == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("build env is missing %q", want)
		}
	}

	workDir := filepath.Dir(plan.Work)
	plan.Cleanup()
	if _, err := os.Stat(workDir); !os.IsNotExist(err) {
		t.Fatalf("temporary workspace should be removed after cleanup, stat err = %v", err)
	}
}

func TestBackendBuildPlanLeavesDefaultsWithoutReplace(t *testing.T) {
	// 没有文件系统 replace 的模块不需要 workspace；显式 GOWORK=off，避免被上层
	// 目录的 go.work 意外影响。没有 go 行就不钉工具链。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/plugin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := newBackendBuildPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Cleanup()
	if plan.Work != "" {
		t.Fatalf("plan should not create a workspace without a filesystem replace, got %q", plan.Work)
	}
	for _, entry := range plan.Env {
		if strings.HasPrefix(entry, "GOTOOLCHAIN=") {
			t.Fatalf("build env should not pin GOTOOLCHAIN without a go directive: %q", entry)
		}
	}
	found := false
	for _, entry := range plan.Env {
		if entry == "GOWORK=off" {
			found = true
		}
	}
	if !found {
		t.Fatal("build env should set GOWORK=off when no workspace is created")
	}
}
