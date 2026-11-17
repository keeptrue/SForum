package main

import (
	"errors"
	"fmt"
	"testing"
)

// 退出码三态是脚本契约：0 一致/已执行、1 有缺陷或前提漂移、2 用法错误。
// 任何一处把缺陷吞成 0，CI 与 dev 启动前的自检就失去意义。
func TestExitCodeForThreeStates(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "defect", err: defectError(errors.New("artifact is not loadable")), want: artifactExitDefect},
		{name: "usage", err: usageError(errors.New("--reason is required")), want: artifactExitUsage},
		{name: "unclassified", err: errors.New("database unreachable"), want: 1},
		{name: "wrapped defect", err: fmt.Errorf("commit: %w", defectError(errors.New("drift"))), want: artifactExitDefect},
		{name: "wrapped usage", err: fmt.Errorf("flags: %w", usageError(errors.New("bad flag"))), want: artifactExitUsage},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := exitCodeFor(testCase.err); got != testCase.want {
				t.Fatalf("exit code=%d want %d (err=%v)", got, testCase.want, testCase.err)
			}
		})
	}
}

// 缺陷错误必须同时保留原始错误链，调用方才能继续 errors.Is 判断。
func TestDefectErrorKeepsCause(t *testing.T) {
	cause := errors.New("boom")
	err := defectError(cause)
	if !errors.Is(err, cause) {
		t.Fatalf("defect error must unwrap to its cause, got %v", err)
	}
	if err.Error() != cause.Error() {
		t.Fatalf("defect error text must stay the cause text, got %q", err.Error())
	}
}

// nil 不得被包装成「有缺陷」，否则健康路径会变成非零退出。
func TestNilErrorStaysNil(t *testing.T) {
	if defectError(nil) != nil || usageError(nil) != nil {
		t.Fatal("nil error must not become a coded error")
	}
}

// 相对 package_path 必须以显式 root 为基准解析，不能依赖调用者 cwd。
func TestResolveArtifactRoot(t *testing.T) {
	absolute := "/var/lib/sforum/artifact"
	if got := resolveArtifactRoot("/opt/sforum", absolute); got != absolute {
		t.Fatalf("absolute path must stay unchanged, got %q", got)
	}
	// durable 行里是相对 API 工作目录的路径（apps/api + ../../storage/...），
	// 基准目录就是那个工作目录；Join 会做 Clean。
	if got := resolveArtifactRoot("apps/api", "../../storage/x"); got != "storage/x" {
		t.Fatalf("relative path must resolve against root, got %q", got)
	}
	if got := resolveArtifactRoot("/opt/sforum", ""); got != "" {
		t.Fatalf("empty path must stay empty, got %q", got)
	}
}
