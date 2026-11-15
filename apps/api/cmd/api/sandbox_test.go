//go:build linux

package main

// 这组用例守的是 main 里那条**接线**：插件子进程会重新 exec 本二进制，而这只有
// 在 main 的第一条语句就把启动器调用接管掉时才成立。TestMain 复刻了 main 的做法，
// 因此端到端用例里的「启动器」就是这份 cmd/api 测试二进制本身。

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/pluginprocess"
)

func TestMain(m *testing.M) {
	if pluginprocess.IsLauncherInvocation(os.Args) {
		os.Exit(pluginprocess.RunLauncher(os.Args))
	}
	os.Exit(m.Run())
}

func TestEnterPluginSandboxLauncherDeclaresLauncherSupport(t *testing.T) {
	enterPluginSandboxLauncher()
	if !pluginprocess.LauncherEnabled() {
		t.Fatal("main 的第一条语句必须声明本二进制可作为沙箱启动器")
	}
}

// 端到端：main 声明启动器之后，被包装的插件命令真的会被本二进制接管并跑起来，
// 能力报告也能被宿主读回。
func TestWrappedPluginCommandRunsThroughThisBinary(t *testing.T) {
	enterPluginSandboxLauncher()
	cmd := exec.Command("/bin/echo")
	cmd.Args = []string{"/bin/echo", "wrapped-through-main"}
	spec := pluginprocess.SandboxSpec{
		PluginID:    "cmd.api.probe",
		Entry:       "/bin/echo",
		Argv0:       "echo",
		ArtifactDir: "/bin",
		TempRoot:    t.TempDir(),
		Policy: pluginprocess.SandboxPolicy{
			Filesystem: false,
			Network:    pluginprocess.NetworkOff,
			Cgroup:     false,
		},
	}
	if err := pluginprocess.WrapCommand(cmd, spec); err != nil {
		t.Fatalf("wrap plugin command: %v", err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run wrapped command: %v", err)
	}
	if strings.TrimSpace(string(output)) != "wrapped-through-main" {
		t.Fatalf("plugin command output = %q", output)
	}
	report, ok := pluginprocess.TakeLaunchReport(cmd)
	if !ok {
		t.Fatal("launcher produced no capability report")
	}
	if report.PluginID != "cmd.api.probe" || !report.NoNewPrivs {
		t.Fatalf("launcher report = %#v", report)
	}
}
