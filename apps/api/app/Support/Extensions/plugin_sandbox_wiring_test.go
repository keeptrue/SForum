//go:build linux

package extensionsruntime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/pluginprocess"
)

// 这组用例守的是「宿主接线」这件事本身：prepareVerifiedPluginExecutable 之后，
// 命令必须被改写成「经沙箱启动器 exec」，且 spec 里的关键字段来自真实制品。
//
// 用子进程跑：EnableLauncher 是进程级状态，而同包里还有一批真的会启动插件进程的
// 用例——它们的 backend 是 shell 脚本，没有 /bin/sh 的 exec 权限（见决策记录
// 「脚本型后端」一节），不该被这条用例带进来。
func TestAttachPluginSandboxRewritesPreparedCommand(t *testing.T) {
	if os.Getenv("SFORUM_SANDBOX_ATTACH_CHILD") != "" {
		t.Setenv(pluginprocess.SandboxEnv, "on")
		t.Setenv(pluginprocess.SandboxFilesystemEnv, "on")
		t.Setenv(pluginprocess.SandboxCgroupEnv, "on")
		t.Setenv(pluginprocess.SandboxNetworkEnv, "off")
		pluginprocess.EnableLauncher()

		extension, _ := execSentinelExtension(t, "")
		entry, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
		if !ok {
			t.Fatal("resolve backend entry")
		}
		cmd, err := preparePluginCommand(extension, entry)
		if err != nil {
			t.Fatalf("prepare plugin command: %v", err)
		}
		// P0：设置项里的绝对路径值必须被接线进可写面（不认插件名/键名）。
		storageRoot := filepath.Join(t.TempDir(), "storage-root")
		cmd.Env = append(cmd.Env,
			"SFORUM_SETTING_ROOT_PATH="+storageRoot,
			"SFORUM_SETTING_PUBLIC_BASE_URL=https://cdn.example/base",
		)
		attachPluginSandbox(cmd, extension)

		hostPath, err := os.Executable()
		if err != nil {
			t.Fatalf("resolve host executable: %v", err)
		}
		if cmd.Path != hostPath {
			t.Fatalf("plugin exec path = %q, want the host binary %q", cmd.Path, hostPath)
		}
		if len(cmd.Args) < 2 || cmd.Args[0] != pluginprocess.LauncherArgv0 {
			t.Fatalf("launcher argv = %#v", cmd.Args)
		}
		if len(cmd.ExtraFiles) != 1 {
			t.Fatalf("sandbox report pipe was not attached: %#v", cmd.ExtraFiles)
		}
		var spec pluginprocess.SandboxSpec
		if err := json.Unmarshal([]byte(cmd.Args[1]), &spec); err != nil {
			t.Fatalf("decode sandbox spec: %v", err)
		}
		if spec.PluginID != extension.ID || spec.ArtifactDir != cmd.Dir {
			t.Fatalf("spec identity = %#v, want artifact dir %q", spec, cmd.Dir)
		}
		if spec.Entry != cmd.Args[2] && spec.Entry != entry {
			t.Fatalf("spec entry = %q, want the prepared exec target", spec.Entry)
		}
		if !spec.Policy.Filesystem || !spec.Policy.Cgroup {
			t.Fatalf("spec policy = %#v, want filesystem and cgroup enabled", spec.Policy)
		}
		if strings.TrimSpace(spec.MemoryMax) == "" || strings.TrimSpace(spec.PidsMax) == "" {
			t.Fatalf("spec limits = %q/%q", spec.MemoryMax, spec.PidsMax)
		}
		if len(spec.ExtraReadWrite) != 1 || spec.ExtraReadWrite[0] != storageRoot {
			t.Fatalf("settings path was not wired into the writable surface: %v", spec.ExtraReadWrite)
		}
		// 失败/中止路径必须能取走报告：abortPluginStart 是启动失败的唯一出口
		// （P1-1）。cmd 从未 Start，这里验证的正是「取报告 + 关管道」这一步。
		abortPluginStart(cmd, nil)
		if _, ok := pluginprocess.TakeLaunchReport(cmd); ok {
			t.Fatal("abortPluginStart did not release the launcher report")
		}
		requireNoReportPipeLeak(t)
		return
	}

	sub := exec.Command(os.Args[0], "-test.run=TestAttachPluginSandboxRewritesPreparedCommand")
	sub.Env = append(os.Environ(), "SFORUM_SANDBOX_ATTACH_CHILD=1")
	if output, err := sub.CombinedOutput(); err != nil {
		t.Fatalf("wiring subprocess failed: %v\n%s", err, output)
	}
}

// requireNoReportPipeLeak 反复包装并中止：报告管道必须是「一次一份、随取随关」，
// 不能每失败一次就在宿主里留一对 fd。
func requireNoReportPipeLeak(t *testing.T) {
	t.Helper()
	before := countOpenFDs(t)
	for attempt := 0; attempt < 5; attempt++ {
		extension, _ := execSentinelExtension(t, "")
		entry, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
		if !ok {
			t.Fatal("resolve backend entry")
		}
		cmd, err := preparePluginCommand(extension, entry)
		if err != nil {
			t.Fatalf("prepare plugin command: %v", err)
		}
		attachPluginSandbox(cmd, extension)
		abortPluginStart(cmd, nil)
	}
	if after := countOpenFDs(t); after > before+2 {
		t.Fatalf("sandbox report pipes leak: open fds %d -> %d after 5 aborted starts", before, after)
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("count open fds: %v", err)
	}
	return len(entries)
}
