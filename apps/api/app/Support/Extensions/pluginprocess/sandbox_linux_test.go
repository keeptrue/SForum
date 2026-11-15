//go:build linux

package pluginprocess

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// 配置解析：默认全部开启、网络默认关闭；拼错的值一律落到更严格的一侧。
func TestSandboxOptionsFromEnv(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	t.Run("defaults", func(t *testing.T) {
		options := SandboxOptionsFromEnv(env(nil))
		if !options.Enabled || !options.Filesystem || !options.Cgroup {
			t.Fatalf("defaults must enable the sandbox: %#v", options)
		}
		if options.Network != NetworkOff {
			t.Fatalf("default network policy = %q, want off", options.Network)
		}
		if options.MemoryMax != defaultMemoryMax || options.PidsMax != defaultPidsMax {
			t.Fatalf("default limits = %q/%q", options.MemoryMax, options.PidsMax)
		}
	})
	t.Run("kill switch", func(t *testing.T) {
		if options := SandboxOptionsFromEnv(env(map[string]string{SandboxEnv: "off"})); options.Enabled {
			t.Fatal("SFORUM_PLUGIN_SANDBOX=off must disable the launcher")
		}
		if options := SandboxOptionsFromEnv(env(map[string]string{SandboxFilesystemEnv: "0"})); options.Filesystem {
			t.Fatal("filesystem switch must be honoured")
		}
		if options := SandboxOptionsFromEnv(env(map[string]string{SandboxCgroupEnv: "false"})); options.Cgroup {
			t.Fatal("cgroup switch must be honoured")
		}
	})
	t.Run("typos fail closed", func(t *testing.T) {
		if options := SandboxOptionsFromEnv(env(map[string]string{SandboxFilesystemEnv: "of"})); !options.Filesystem {
			t.Fatal("a misspelled filesystem switch must not disable confinement")
		}
		if options := SandboxOptionsFromEnv(env(map[string]string{SandboxNetworkEnv: "of"})); options.Network != NetworkAll {
			t.Fatalf("a misspelled network policy = %q, want the strictest (all)", options.Network)
		}
		if options := SandboxOptionsFromEnv(env(map[string]string{SandboxNetworkEnv: "bind"})); options.Network != NetworkBind {
			t.Fatalf("network policy bind was not parsed: %q", options.Network)
		}
	})
	t.Run("extra read write is filtered like settings paths", func(t *testing.T) {
		root := t.TempDir()
		good := filepath.Join(root, "storage")
		existingFile := filepath.Join(root, "ca.pem")
		if err := os.WriteFile(existingFile, []byte("pem"), 0o600); err != nil {
			t.Fatalf("stage existing file: %v", err)
		}
		options := SandboxOptionsFromEnv(env(map[string]string{
			SandboxExtraReadWriteEnv: strings.Join([]string{
				".",                // 相对路径：会按启动器 cwd 解析成制品目录
				good,               // 合法：绝对 + 深度 2 + 尚不存在
				"/files",           // 一级组件：URL path 形态
				existingFile,       // 已存在且不是目录：只读用途
				"https://cdn/x",    // URL
				"relative/storage", // 相对
			}, ":"),
		}))
		if len(options.ExtraReadWrite) != 1 || options.ExtraReadWrite[0] != good {
			t.Fatalf("EXTRA_RW filter kept %#v, want only %q", options.ExtraReadWrite, good)
		}
	})
	t.Run("extra read only", func(t *testing.T) {
		options := SandboxOptionsFromEnv(env(map[string]string{SandboxExtraReadOnlyEnv: "/srv/data:/opt/ca: "}))
		if len(options.ExtraReadOnly) != 2 || options.ExtraReadOnly[0] != "/srv/data" || options.ExtraReadOnly[1] != "/opt/ca" {
			t.Fatalf("extra read-only paths = %#v", options.ExtraReadOnly)
		}
	})
}

// WrapCommand 的契约：保留 cwd/env，重排 argv，把报告管道接在 ExtraFiles 之后。
func TestWrapCommandRewritesInvocation(t *testing.T) {
	EnableLauncher()
	cmd := exec.Command("/bin/echo")
	cmd.Args = []string{"/bin/echo", "hello"}
	cmd.Dir = "/tmp"
	cmd.Env = []string{"APP_ENV=test"}
	spec := SandboxSpec{
		PluginID:    "probe.plugin",
		Entry:       "/bin/echo",
		Argv0:       "plugin-entry",
		ArtifactDir: "/bin",
		Policy:      SandboxPolicy{Filesystem: false, Network: NetworkOff, Cgroup: false},
	}
	hostPath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve host executable: %v", err)
	}
	if err := WrapCommand(cmd, spec); err != nil {
		t.Fatalf("wrap command: %v", err)
	}
	if cmd.Path != hostPath {
		t.Fatalf("exec path = %q, want the host binary %q", cmd.Path, hostPath)
	}
	if len(cmd.Args) < 2 || cmd.Args[0] != LauncherArgv0 {
		t.Fatalf("launcher argv = %#v", cmd.Args)
	}
	var decoded SandboxSpec
	if err := json.Unmarshal([]byte(cmd.Args[1]), &decoded); err != nil {
		t.Fatalf("decode spec from argv: %v", err)
	}
	if decoded.Entry != "/bin/echo" || decoded.ReportFD != 3 {
		t.Fatalf("decoded spec = %#v", decoded)
	}
	if len(cmd.Args) != 4 || cmd.Args[2] != "plugin-entry" || cmd.Args[3] != "hello" {
		t.Fatalf("plugin argv = %#v, want the readable entry name followed by the original args", cmd.Args[2:])
	}
	if cmd.Dir != "/tmp" || len(cmd.Env) != 1 || cmd.Env[0] != "APP_ENV=test" {
		t.Fatalf("cwd/env were not preserved: dir=%q env=%v", cmd.Dir, cmd.Env)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("report pipe was not attached: %#v", cmd.ExtraFiles)
	}
	_ = cmd.ExtraFiles[0].Close()
	cmd.ExtraFiles = nil
}

// 没有声明启动器的二进制必须得到「可诊断的降级」，而不是被改写成一次怪异启动。
// 用子进程跑，避免与本包其它用例的 EnableLauncher 抢状态。
func TestWrapCommandRequiresRegisteredLauncher(t *testing.T) {
	if os.Getenv("SFORUM_SANDBOX_TEST_UNREGISTERED_CHILD") != "" {
		cmd := exec.Command("/bin/true")
		cmd.Args = []string{"/bin/true"}
		err := WrapCommand(cmd, SandboxSpec{Entry: "/bin/true", ArtifactDir: "/bin"})
		if !errors.Is(err, ErrLauncherNotRegistered) {
			t.Fatalf("WrapCommand without registration = %v, want ErrLauncherNotRegistered", err)
		}
		if cmd.Path != "/bin/true" || len(cmd.Args) != 1 {
			t.Fatalf("command was rewritten despite the failure: path=%q args=%v", cmd.Path, cmd.Args)
		}
		if _, ok := TakeLaunchReport(cmd); ok {
			t.Fatal("no report may exist for a command that was never wrapped")
		}
		return
	}
	sub := exec.Command(os.Args[0], "-test.run=TestWrapCommandRequiresRegisteredLauncher")
	sub.Env = append(os.Environ(), "SFORUM_SANDBOX_TEST_UNREGISTERED_CHILD=1")
	if output, err := sub.CombinedOutput(); err != nil {
		t.Fatalf("unregistered-launcher subprocess failed: %v\n%s", err, output)
	}
}

// 端到端接线：WrapCommand → 宿主二进制作为启动器 → 报告。这一条守的是「宿主声明
// 启动器之后，插件命令真的会被启动器接管并且能读到能力报告」。
func TestWrapCommandLaunchesThroughHostLauncher(t *testing.T) {
	EnableLauncher()
	cmd := exec.Command("/bin/echo")
	cmd.Args = []string{"/bin/echo", "sandbox"}
	spec := SandboxSpec{
		PluginID:    "transport.probe",
		Entry:       "/bin/echo",
		Argv0:       "echo",
		ArtifactDir: "/bin",
		TempRoot:    t.TempDir(),
		Policy:      SandboxPolicy{Filesystem: false, Network: NetworkOff, Cgroup: false},
	}
	if err := WrapCommand(cmd, spec); err != nil {
		t.Fatalf("wrap command: %v", err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("run wrapped command: %v", err)
	}
	if strings.TrimSpace(string(output)) != "sandbox" {
		t.Fatalf("wrapped command output = %q", output)
	}
	report, ok := TakeLaunchReport(cmd)
	if !ok {
		t.Fatal("launcher did not produce a capability report")
	}
	if report.PluginID != "transport.probe" || report.PID <= 0 {
		t.Fatalf("report identity = %#v", report)
	}
	if report.Filesystem != StatusDisabled || report.Cgroup != StatusDisabled {
		t.Fatalf("policy was disabled but report claims enforcement: %#v", report)
	}
	if !report.NoNewPrivs {
		t.Fatal("PR_SET_NO_NEW_PRIVS must be applied even when the layers are disabled")
	}
	if report.TempDir == "" {
		t.Fatal("launcher must always prepare the per-plugin temp dir")
	}
	// 报告是一次性的：再取一次必须为空，否则管道/fd 会泄漏。
	if _, ok := TakeLaunchReport(cmd); ok {
		t.Fatal("the launcher report was handed out twice")
	}
}

// 启动失败（exec 目标不可执行）也必须留下能力报告，并且不把报告管道留在宿主里。
//
// 这是 P1-1 的守点：真实失败路径（protocol.go 的 16 个分支）走 abortPluginStart →
// DrainLaunchReport；这里用同一条 drain 语义证明「报告在 exec 之前就已经在管道里」，
// 因此失败也能回答「这次启动到底有没有被内核约束」。
func TestLaunchReportSurvivesExecFailure(t *testing.T) {
	EnableLauncher()
	dir := t.TempDir()
	target := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("stage non-executable target: %v", err)
	}
	before := pendingLaunchReports()
	cmd := exec.Command(target)
	cmd.Args = []string{target}
	spec := SandboxSpec{
		PluginID:    "exec.failure.probe",
		Entry:       target,
		Argv0:       target,
		ArtifactDir: dir,
		TempRoot:    shortTempRoot(t),
		Policy:      SandboxPolicy{Filesystem: true, Network: NetworkOff, Cgroup: false},
	}
	if err := WrapCommand(cmd, spec); err != nil {
		t.Fatalf("wrap command: %v", err)
	}
	if err := cmd.Run(); err == nil {
		t.Fatal("exec of a 0644 file must fail")
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != launcherExitExec {
			t.Fatalf("launcher exit = %v, want %d", err, launcherExitExec)
		}
	}
	report, ok := TakeLaunchReport(cmd)
	if !ok {
		t.Fatal("failed start produced no capability report")
	}
	if report.PluginID != spec.PluginID {
		t.Fatalf("report identity = %#v", report)
	}
	if report.Filesystem != StatusEnforced {
		t.Fatalf("report must still record the applied confinement: %#v", report)
	}
	if got := pendingLaunchReports(); got != before {
		t.Fatalf("launch report pipes leaked: before=%d after=%d", before, got)
	}
}

// cgroup 写组逻辑：在「已经委派好 memory+pids」的根上真的写进限额并把自己搬进去。// cgroup 写组逻辑：在「已经委派好 memory+pids」的根上真的写进限额并把自己搬进去。
// 这不是模拟内核语义，而是验证我们写的是哪个文件、写的是什么值。
func TestCgroupWriterAppliesLimitsToDelegatedRoot(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "cgroup.controllers"), "memory pids\n")
	writeFixture(t, filepath.Join(root, "cgroup.subtree_control"), "memory pids\n")
	writeFixture(t, filepath.Join(root, "cgroup.procs"), "")

	outcome := applySandboxCgroup(SandboxSpec{
		PluginID:           "writer.probe",
		CgroupRootOverride: root,
		Policy:             SandboxPolicy{Cgroup: true},
		MemoryMax:          "64M",
		PidsMax:            "24",
	})
	if outcome.Status != StatusEnforced {
		t.Fatalf("cgroup status = %s reason=%q", outcome.Status, outcome.Reason)
	}
	if outcome.Limits["memory.max"] != "64M" || outcome.Limits["pids.max"] != "24" {
		t.Fatalf("reported limits = %#v", outcome.Limits)
	}
	if memory := readCgroupValue(t, filepath.Join(outcome.Path, "memory.max")); memory != "64M" {
		t.Fatalf("memory.max on disk = %q", memory)
	}
	if pids := readCgroupValue(t, filepath.Join(outcome.Path, "pids.max")); pids != "24" {
		t.Fatalf("pids.max on disk = %q", pids)
	}
	if procs := readCgroupValue(t, filepath.Join(outcome.Path, "cgroup.procs")); procs != "0" {
		t.Fatalf("cgroup.procs = %q, want the self-pid write", procs)
	}
	if !strings.HasPrefix(filepath.Base(outcome.Path), cgroupPluginPrefix) {
		t.Fatalf("cgroup name %q does not carry the plugin prefix", outcome.Path)
	}
}

// 控制器缺失时必须降级并给出原因，而且不留半个组。
func TestCgroupWriterDegradesWithoutControllerDelegation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write a 0444 subtree_control; this fixture needs an unprivileged owner")
	}
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "cgroup.controllers"), "memory pids\n")
	// 真实的降级形态：控制器可用但委派被拒（EBUSY/EACCES），而不是「没有控制器」。
	writeFixture(t, filepath.Join(root, "cgroup.subtree_control"), "\n")
	if err := os.Chmod(filepath.Join(root, "cgroup.subtree_control"), 0o444); err != nil {
		t.Fatalf("chmod subtree_control: %v", err)
	}

	outcome := applySandboxCgroup(SandboxSpec{
		PluginID:           "writer.probe",
		CgroupRootOverride: root,
		Policy:             SandboxPolicy{Cgroup: true},
		MemoryMax:          "64M",
		PidsMax:            "24",
	})
	if outcome.Status != StatusDegraded {
		t.Fatalf("cgroup status = %s, want degraded", outcome.Status)
	}
	if !strings.Contains(outcome.Reason, "subtree_control") && !strings.Contains(outcome.Reason, "delegate") {
		t.Fatalf("degradation reason %q does not explain the refused delegation", outcome.Reason)
	}
	if !strings.Contains(outcome.Reason, "permission denied") && !strings.Contains(outcome.Reason, "operation not permitted") {
		t.Fatalf("degradation reason %q should carry the errno from the refused write", outcome.Reason)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read fixture root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cgroupPluginPrefix) {
			t.Fatalf("a degraded cgroup left a stray directory: %s", entry.Name())
		}
	}
}

// 名字必须稳定、可读、且不含路径分隔符（cgroup 文件名会直接拼进 /sys/fs/cgroup）。
func TestSandboxNameSuffixIsStableAndSafe(t *testing.T) {
	first := sandboxNameSuffix("sforum-ai.provider/v2")
	second := sandboxNameSuffix("sforum-ai.provider/v2")
	if first != second {
		t.Fatalf("name suffix is not stable: %q vs %q", first, second)
	}
	if strings.ContainsAny(first, "/\n\t ") || strings.HasPrefix(first, ".") {
		t.Fatalf("name suffix %q is not safe for a cgroup path", first)
	}
	if !strings.HasPrefix(first, "sforum-ai.provider-v2-") {
		t.Fatalf("name suffix %q lost its readable prefix", first)
	}
	if other := sandboxNameSuffix("another-plugin"); other == first {
		t.Fatalf("different plugins collapsed to the same cgroup name: %q", first)
	}
}

// 可写路径启发式的拒绝面：URL（含冒号切分残骸 `//x`）/ 一级组件 / 已存在的文件 /
// 穿越或根目录。
//
// 这些都是「看起来像绝对路径，但不是数据目录」的真实形态——把它们升格成可写面
// 等于把只读设置或 URL 路径变成写权限。
func TestSandboxWritableSettingPathRejectsAmbiguousValues(t *testing.T) {
	root := t.TempDir()
	existingFile := filepath.Join(root, "ca-bundle.pem")
	if err := os.WriteFile(existingFile, []byte("pem"), 0o600); err != nil {
		t.Fatalf("stage existing file: %v", err)
	}
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "absolute data dir", value: filepath.Join(root, "storage-root"), want: true},
		{name: "deep absolute dir", value: "/var/lib/sforum/storage", want: true},
		{name: "url", value: "https://cdn.example/base", want: false},
		{name: "colon-split residue", value: "//cdn/x", want: false},
		{name: "single component", value: "/files", want: false},
		{name: "existing file", value: existingFile, want: false},
		{name: "root", value: "/", want: false},
		{name: "relative", value: "data/storage", want: false},
		{name: "traversal", value: "/srv/../etc", want: false},
		{name: "empty", value: "", want: false},
	}
	for _, testCase := range cases {
		path, ok := sandboxWritableSettingPath(testCase.value)
		t.Logf("case %-16s value=%-38q kept=%-5t cleaned=%q", testCase.name, testCase.value, ok, path)
		if ok != testCase.want {
			t.Fatalf("%s: sandboxWritableSettingPath(%q) = %q,%t, want %t",
				testCase.name, testCase.value, path, ok, testCase.want)
		}
	}
}

// 规则位裁剪：文件路径不能带目录专属的 access 位，否则 add_rule 直接 EINVAL。// 规则位裁剪：文件路径不能带目录专属的 access 位，否则 add_rule 直接 EINVAL。
func TestGrantAccessForPathNarrowsRegularFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "artifact")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("stage file: %v", err)
	}
	handled := landlockHandledAccess(8)
	dirAccess := grantAccessForPath(dir, clampLandlockAccess(landlockWritableAccess, handled))
	fileAccess := grantAccessForPath(file, clampLandlockAccess(landlockWritableAccess, handled))
	if fileAccess&unix.LANDLOCK_ACCESS_FS_READ_DIR != 0 || fileAccess&unix.LANDLOCK_ACCESS_FS_REFER != 0 {
		t.Fatalf("regular file kept directory-only rights: %#x", fileAccess)
	}
	if fileAccess&unix.LANDLOCK_ACCESS_FS_READ_FILE == 0 || fileAccess&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 {
		t.Fatalf("regular file lost its read/write rights: %#x", fileAccess)
	}
	if dirAccess&unix.LANDLOCK_ACCESS_FS_MAKE_REG == 0 {
		t.Fatalf("directory lost MAKE_REG: %#x", dirAccess)
	}
	if grantAccessForPath(filepath.Join(dir, "missing"), handled) != 0 {
		t.Fatal("missing paths must be skipped instead of failing the whole ruleset")
	}
}

func TestLimitValueValidation(t *testing.T) {
	for _, valid := range []string{"512", "512M", "1G", "0"} {
		if value, ok := parseLimitValue(valid); !ok || value != valid {
			t.Fatalf("parseLimitValue(%q) = %q,%t", valid, value, ok)
		}
	}
	for _, invalid := range []string{"", "512MB", "max", "1M\n", "-1", "M512"} {
		if _, ok := parseLimitValue(invalid); ok {
			t.Fatalf("parseLimitValue(%q) must be rejected", invalid)
		}
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}
