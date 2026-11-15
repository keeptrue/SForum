//go:build linux

package pluginprocess

// 这组用例是 EX2-01 的**运行时证据**：不是在源码里找关键字，而是把测试二进制自己
// 当成启动器重新 exec，让内核真的去拒绝/放行，再把结论读回来。
//
// 机制：TestMain 在最前面拦两种特殊 argv/env
//  1. IsLauncherInvocation(os.Args) → RunLauncher（与 cmd/api、cmd/worker 完全同一条
//     生产入口）；
//  2. SFORUM_SANDBOX_TEST_PROBE → 探针进程本体（一个不跑 testing 框架的小程序，
//     这样 stdout 只有探针自己的结果，不会混进 testing 的输出）。
//
// 因此每个探针都是同一份二进制里的三个角色：父（断言）、启动器（加固 + exec）、
// 插件（被加固后观察自己的世界）。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	probeProcessEnv   = "SFORUM_SANDBOX_TEST_PROBE"
	probeCanaryEnv    = "SFORUM_SANDBOX_TEST_CANARY"
	probeArtifactEnv  = "SFORUM_SANDBOX_TEST_ARTIFACT"
	probeTCPEnv       = "SFORUM_SANDBOX_TEST_TCP"
	probeHostDirEnv   = "SFORUM_SANDBOX_TEST_HOST_DIR"
	probeDataDirEnv   = "SFORUM_SANDBOX_TEST_DATA_DIR"
	probeHoldEnv      = "SFORUM_SANDBOX_TEST_HOLD"
	probeResultPrefix = "SFORUM_SANDBOX_PROBE_RESULT "
	probeTimeout      = 30 * time.Second
)

func TestMain(m *testing.M) {
	if IsLauncherInvocation(os.Args) {
		os.Exit(RunLauncher(os.Args))
	}
	if os.Getenv(probeProcessEnv) != "" {
		os.Exit(runSandboxProbeProcess())
	}
	os.Exit(m.Run())
}

// sandboxProbeResult 是插件进程（已被内核约束）对自身世界的观察。
type sandboxProbeResult struct {
	UID        int      `json:"uid"`
	Argv0      string   `json:"argv0"`
	Shadow     string   `json:"shadow"`
	Canary     string   `json:"canary"`
	Artifact   string   `json:"artifact"`
	TempFile   string   `json:"temp_file"`
	TCP        string   `json:"tcp"`
	UnixSocket string   `json:"unix_socket"`
	DataDir    string   `json:"data_dir"`
	EtcHosts   string   `json:"etc_hosts_write"`
	HomeWrite  string   `json:"home_write"`
	HostDir    string   `json:"host_dir_write"`
	CgroupLine string   `json:"cgroup"`
	NoNewPrivs bool     `json:"no_new_privs"`
	ExtraFDs   []string `json:"extra_fds"`
}

func runSandboxProbeProcess() int {
	result := sandboxProbeResult{
		UID:        os.Getuid(),
		Argv0:      os.Args[0],
		NoNewPrivs: noNewPrivsSet(),
		Shadow:     probeRead("/etc/shadow"),
		Canary:     probeRead(os.Getenv(probeCanaryEnv)),
		Artifact:   probeRead(os.Getenv(probeArtifactEnv)),
		TCP:        probeDial(os.Getenv(probeTCPEnv)),
		UnixSocket: probeUnixSocket(),
		DataDir:    probeDataDir(),
		EtcHosts:   probeAppend("/etc/hosts"),
		HomeWrite:  probeAppend(filepath.Join(os.Getenv("HOME"), ".sforum-sandbox-probe")),
		HostDir:    probeCreateIn(os.Getenv(probeHostDirEnv)),
	}
	// 私有 TMPDIR 必须可写：go-plugin 的 unix socket 与插件自己的临时文件都落在这里。
	if file, err := os.CreateTemp("", "sandbox-probe"); err != nil {
		result.TempFile = probeErrno(err)
	} else {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		result.TempFile = "ok"
	}
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		result.CgroupLine = strings.TrimSpace(string(data))
	}
	// 报告 fd 不该漏进插件进程。
	for fd := 3; fd <= 5; fd++ {
		if _, err := os.Stat(fmt.Sprintf("/proc/self/fd/%d", fd)); err == nil {
			if target, linkErr := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd)); linkErr == nil {
				result.ExtraFDs = append(result.ExtraFDs, fmt.Sprintf("%d=%s", fd, target))
			}
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sandbox probe: encode result: %v\n", err)
		return 1
	}
	fmt.Printf("%s%s\n", probeResultPrefix, payload)
	// 探针在退出前等 stdin EOF：给父进程一个确定的「插件还活着」窗口，用来从宿主
	// 侧读取 cgroup.procs 这类「进程存在才有意义」的证据。
	if os.Getenv(probeHoldEnv) != "" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	return 0
}

func noNewPrivsSet() bool {
	value, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0, 0)
	if errno != 0 {
		return false
	}
	return value == 1
}

func probeRead(path string) string {
	if strings.TrimSpace(path) == "" {
		return "unset"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return probeErrno(err)
	}
	return "ok:" + strings.TrimSpace(string(data))
}

// probeUnixSocket 验证 go-plugin 的进程控制面：插件在 os.TempDir() 里建 unix
// socket 并把地址打到 stdout（宿主再去 dial）。沙箱给 TMPDIR 的 MAKE_SOCK 必须够用，
// 否则插件连握手都到不了——这一条比握手本身更早暴露问题。
func probeUnixSocket() string {
	path := filepath.Join(os.TempDir(), "sforum-sandbox-probe.sock")
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return probeErrno(err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
		accepted <- struct{}{}
	}()
	conn, err := net.Dial("unix", path)
	if err != nil {
		return probeErrno(err)
	}
	_ = conn.Close()
	<-accepted
	_ = os.Remove(path)
	return "ok"
}

// probeDataDir 模拟 sforum-storage-fs 的落盘方式：MkdirAll 子目录 + 写对象 +
// CreateTemp 临时文件。它写的是「运维为该插件配置的目录」，不是任意路径。
func probeDataDir() string {
	dir := strings.TrimSpace(os.Getenv(probeDataDirEnv))
	if dir == "" {
		return "unset"
	}
	sub := filepath.Join(dir, "objects", "2026", "11")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		return probeErrno(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "probe.bin"), []byte("ok"), 0o640); err != nil {
		return probeErrno(err)
	}
	file, err := os.CreateTemp(sub, ".sforum-put-*")
	if err != nil {
		return probeErrno(err)
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return "ok"
}

// probeAppend 尝试向一个已存在的文件追加：用来观察「沙箱外的写」。
func probeAppend(path string) string {
	if strings.TrimSpace(path) == "" || path == string(filepath.Separator) {
		return "unset"
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return probeErrno(err)
	}
	_ = file.Close()
	return "ok"
}

// probeCreateIn 在宿主目录里建一个新文件：宿主（不受限）能写、沙箱里必须 EACCES，
// 这是本用例真正的判别式（/etc/hosts 与 $HOME 对非 root 本来就常被 DAC 拒绝）。
func probeCreateIn(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "unset"
	}
	return probeAppend(filepath.Join(dir, "sandbox-must-not-write"))
}

func probeDial(address string) string {
	if strings.TrimSpace(address) == "" {
		return "unset"
	}
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return probeErrno(err)
	}
	_ = conn.Close()
	return "ok"
}

// probeErrno 把错误收敛成稳定的 errno 名字，供父进程断言。
func probeErrno(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case unix.EACCES:
			return "EACCES"
		case unix.EPERM:
			return "EPERM"
		case unix.ECONNREFUSED:
			return "ECONNREFUSED"
		case unix.ENOENT:
			return "ENOENT"
		default:
			return errno.Error()
		}
	}
	return err.Error()
}

// --- 探针执行器 ---

type sandboxProbeRun struct {
	result    sandboxProbeResult
	report    Report
	hasReport bool
	pid       int
	stdout    string
	stderr    string
}

type sandboxProbeOptions struct {
	policy        SandboxPolicy
	abiOverride   *int
	cgroupRoot    string
	tcpTarget     string
	extraReadOnly []string
	memoryMax     string
	pidsMax       string
	entry         string
	extraFiles    []*os.File
	argv0         string
	// settingsEnv 是宿主注入插件的 SFORUM_SETTING_* 列表；可写面由同一个 helper
	// 从它推导（与生产接线完全同一条路），不是测试自己另算一份。
	settingsEnv []string
	// dataDir / hostDir 分别是「配置给插件的数据目录」与「宿主可写、沙箱不可写」的
	// 目录，用于可写面判别。
	dataDir string
	hostDir string
	// inspect 在「报告已读到、插件仍活着」的窗口里执行（例如核对 cgroup.procs）。
	inspect func(run sandboxProbeRun)
}

func runSandboxProbe(t *testing.T, options sandboxProbeOptions) sandboxProbeRun {
	t.Helper()
	artifactDir := filepath.Dir(os.Args[0])
	canaryDir := t.TempDir()
	canary := filepath.Join(canaryDir, "host-only-canary.txt")
	if err := os.WriteFile(canary, []byte("host-only"), 0o644); err != nil {
		t.Fatalf("stage host canary: %v", err)
	}
	// 前置条件：宿主（不受限）能读到这份 canary。否则「探针读不到」证明不了任何事。
	if _, err := os.ReadFile(canary); err != nil {
		t.Fatalf("fixture precondition: host must be able to read %s: %v", canary, err)
	}
	artifactCanary := filepath.Join(artifactDir, "sandbox-artifact-canary-"+sanitizeTestName(t.Name())+".txt")
	if err := os.WriteFile(artifactCanary, []byte("artifact"), 0o644); err != nil {
		t.Fatalf("stage artifact canary: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(artifactCanary) })

	entry := options.entry
	if entry == "" {
		entry = os.Args[0]
	}
	argv0 := options.argv0
	if argv0 == "" {
		argv0 = os.Args[0]
	}
	spec := SandboxSpec{
		PluginID:            "sandbox.probe",
		Entry:               entry,
		Argv0:               argv0,
		ArtifactDir:         artifactDir,
		PackageRoot:         artifactDir,
		ExtraReadOnly:       options.extraReadOnly,
		ExtraReadWrite:      SandboxWritableSettingPaths(options.settingsEnv),
		TempRoot:            shortTempRoot(t),
		Policy:              options.policy,
		MemoryMax:           firstNonEmpty(options.memoryMax, "64M"),
		PidsMax:             firstNonEmpty(options.pidsMax, "64"),
		LandlockABIOverride: options.abiOverride,
		CgroupRootOverride:  options.cgroupRoot,
	}
	reportRead, reportWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create report pipe: %v", err)
	}
	extraFiles := append([]*os.File(nil), options.extraFiles...)
	spec.ReportFD = 3 + len(extraFiles)
	payload, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("encode spec: %v", err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Args = []string{LauncherArgv0, string(payload), argv0}
	cmd.ExtraFiles = append(extraFiles, reportWrite)
	cmd.Env = append(os.Environ(),
		probeProcessEnv+"=1",
		probeHoldEnv+"=1",
		probeCanaryEnv+"="+canary,
		probeArtifactEnv+"="+artifactCanary,
		probeTCPEnv+"="+options.tcpTarget,
		probeDataDirEnv+"="+options.dataDir,
		probeHostDirEnv+"="+options.hostDir,
	)
	cmd.Env = append(cmd.Env, options.settingsEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create probe stdin pipe: %v", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sandbox launcher: %v", err)
	}
	_ = reportWrite.Close()
	run := sandboxProbeRun{pid: cmd.Process.Pid}
	_ = reportRead.SetReadDeadline(time.Now().Add(probeTimeout))
	line, readErr := bufio.NewReaderSize(io.LimitReader(reportRead, launcherReportLimit), 4096).ReadString('\n')
	_ = reportRead.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		t.Fatalf("read launcher report: %v", readErr)
	}
	if trimmed := strings.TrimSpace(line); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &run.report); err != nil {
			t.Fatalf("decode launcher report %q: %v", trimmed, err)
		}
		run.hasReport = true
	}
	if options.inspect != nil {
		options.inspect(run)
	}
	// 放行探针（它一直在等 stdin EOF）。
	_ = stdin.Close()
	waitErr := cmd.Wait()
	run.stdout = stdout.String()
	run.stderr = stderr.String()
	if waitErr != nil {
		t.Fatalf("sandboxed probe failed: %v\nreport: %s\nstdout:\n%s\nstderr:\n%s",
			waitErr, run.report.String(), run.stdout, run.stderr)
	}
	if err := json.Unmarshal([]byte(probeResultLine(run.stdout)), &run.result); err != nil {
		t.Fatalf("decode probe result %q: %v\nstderr:\n%s", probeResultLine(run.stdout), err, run.stderr)
	}
	return run
}

func probeResultLine(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, probeResultPrefix) {
			return strings.TrimPrefix(line, probeResultPrefix)
		}
	}
	return ""
}

func sanitizeTestName(name string) string {
	var cleaned strings.Builder
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			cleaned.WriteRune(char)
		} else {
			cleaned.WriteByte('-')
		}
	}
	return cleaned.String()
}

func requireReport(t *testing.T, run sandboxProbeRun) Report {
	t.Helper()
	if !run.hasReport {
		t.Fatalf("launcher did not write a capability report\nstderr:\n%s", run.stderr)
	}
	t.Logf("launcher report: %s", run.report.String())
	return run.report
}

// requireLandlock 区分「内核不支持（跳过）」与「策略要了却没生效（失败）」。
//
// 这条区分是刻意的：把 required 的一层摘掉时，报告会停在 disabled——如果这里也
// 跟着跳过，测试就永远不会红，探针也就成了装饰。
func requireLandlock(t *testing.T, report Report) {
	t.Helper()
	switch report.Filesystem {
	case StatusEnforced:
		t.Logf("landlock ABI=%d filesystem=%s network=%s", report.LandlockABI, report.Filesystem, report.Network)
	case StatusDegraded:
		t.Skipf("landlock filesystem confinement is not available here (status=%s reason=%s)",
			report.Filesystem, report.FSReason)
	default:
		t.Fatalf("filesystem policy was requested but the launcher reports %q (reason=%q)",
			report.Filesystem, report.FSReason)
	}
}

// 探针 ①：读 /etc/shadow 被拒。
//
// 同时断言的 host canary 才是真正的判别式：/etc/shadow 对非 root 本来就是 EACCES
// （DAC），而 canary 是宿主能读、沙箱外的普通文件——它被拒只能是 Landlock。
func TestSandboxProbeDeniesHostFileRead(t *testing.T) {
	run := runSandboxProbe(t, sandboxProbeOptions{policy: SandboxPolicy{Filesystem: true, Network: NetworkOff}})
	report := requireReport(t, run)
	requireLandlock(t, report)

	if run.result.Shadow == "ok" || strings.HasPrefix(run.result.Shadow, "ok:") {
		t.Fatalf("sandboxed plugin read /etc/shadow: %s", run.result.Shadow)
	}
	if run.result.Canary != "EACCES" {
		t.Fatalf("host canary read = %q, want EACCES (landlock must deny it; DAC allows it)", run.result.Canary)
	}
	t.Logf("probe: /etc/shadow=%s host-canary=%s", run.result.Shadow, run.result.Canary)
}

// 探针 ②：制品目录内文件可读 + 私有 TMPDIR 可写。
func TestSandboxProbeAllowsArtifactReadAndTempWrite(t *testing.T) {
	run := runSandboxProbe(t, sandboxProbeOptions{policy: SandboxPolicy{Filesystem: true, Network: NetworkOff}})
	report := requireReport(t, run)
	requireLandlock(t, report)

	if run.result.Artifact != "ok:artifact" {
		t.Fatalf("artifact read inside the package dir = %q, want ok:artifact", run.result.Artifact)
	}
	if run.result.TempFile != "ok" {
		t.Fatalf("sandboxed TMPDIR write = %q, want ok", run.result.TempFile)
	}
	if run.result.UnixSocket != "ok" {
		t.Fatalf("sandboxed unix socket (go-plugin handshake surface) = %q, want ok", run.result.UnixSocket)
	}
	if report.TempDir == "" {
		t.Fatal("launcher did not report a per-plugin temp dir")
	}
	requireNoHostFDs(t, run)
	t.Logf("probe: artifact=%s temp=%s unix-socket=%s tempdir=%s",
		run.result.Artifact, run.result.TempFile, run.result.UnixSocket, report.TempDir)
}

// 探针 ③：TCP connect 到本地监听被拒（Landlock ABI>=4 的网络规则）。
func TestSandboxProbeDeniesTCPConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot open a loopback listener on this host: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	target := listener.Addr().String()

	denied := runSandboxProbe(t, sandboxProbeOptions{
		policy:    SandboxPolicy{Filesystem: true, Network: NetworkAll},
		tcpTarget: target,
	})
	report := requireReport(t, denied)
	requireLandlock(t, report)
	if report.Network != StatusEnforced {
		if report.Network == StatusDegraded {
			t.Skipf("landlock network rules are not available here (status=%s reason=%s)", report.Network, report.NetworkReason)
		}
		t.Fatalf("network policy all was requested but the launcher reports %q", report.Network)
	}
	if denied.result.TCP != "EACCES" {
		t.Fatalf("sandboxed TCP connect to %s = %q, want EACCES", target, denied.result.TCP)
	}

	// 对照：同一条探针、同一个监听，在策略关闭时是能连上的——证明上面的 EACCES
	// 来自策略而不是「监听没开」。
	allowed := runSandboxProbe(t, sandboxProbeOptions{
		policy:    SandboxPolicy{Filesystem: true, Network: NetworkOff},
		tcpTarget: target,
	})
	if allowed.result.TCP != "ok" {
		t.Fatalf("control run could not connect to %s: %q", target, allowed.result.TCP)
	}
	t.Logf("probe: policy=all connect=%s ; control policy=off connect=%s", denied.result.TCP, allowed.result.TCP)
}

// 探针 ④：cgroup 的 memory.max / pids.max 真被写入，且进程真的在里面。
func TestSandboxProbeAppliesCgroupLimits(t *testing.T) {
	var liveProcs []int
	run := runSandboxProbe(t, sandboxProbeOptions{
		policy:    SandboxPolicy{Filesystem: true, Network: NetworkOff, Cgroup: true},
		memoryMax: "96M",
		pidsMax:   "48",
		inspect: func(run sandboxProbeRun) {
			if run.report.CgroupPath == "" {
				return
			}
			procs, err := cgroupProcs(run.report.CgroupPath)
			if err != nil {
				t.Fatalf("read cgroup.procs while the plugin runs: %v", err)
			}
			liveProcs = procs
		},
	})
	report := requireReport(t, run)
	if report.Cgroup != StatusEnforced {
		t.Skipf("cgroup delegation is unavailable on this host (status=%s reason=%s)", report.Cgroup, report.CgroupReason)
	}
	t.Cleanup(func() { _ = os.Remove(report.CgroupPath) })

	memoryMax := readCgroupValue(t, filepath.Join(report.CgroupPath, "memory.max"))
	pidsMax := readCgroupValue(t, filepath.Join(report.CgroupPath, "pids.max"))
	if memoryMax != "100663296" {
		t.Fatalf("memory.max = %q, want 100663296 (96M normalised by the kernel)", memoryMax)
	}
	if pidsMax != "48" {
		t.Fatalf("pids.max = %q, want 48", pidsMax)
	}
	found := false
	for _, pid := range liveProcs {
		if pid == run.pid {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("plugin pid %d is not in %s while it runs (found %v)", run.pid, report.CgroupPath, liveProcs)
	}
	// 交叉验证：插件自己从 /proc/self/cgroup 看到的路径必须与宿主读到的一致。
	mount, err := cgroup2MountPoint()
	if err != nil {
		t.Fatalf("resolve cgroup2 mount: %v", err)
	}
	wantLine := "0::" + strings.TrimPrefix(report.CgroupPath, mount)
	if run.result.CgroupLine != wantLine {
		t.Fatalf("plugin sees cgroup %q, want %q", run.result.CgroupLine, wantLine)
	}
	if report.CgroupLimits["memory.max"] != "100663296" || report.CgroupLimits["pids.max"] != "48" {
		t.Fatalf("reported limits = %#v, want the kernel read-back values", report.CgroupLimits)
	}
	t.Logf("probe: cgroup=%s memory.max=%s pids.max=%s live-procs=%v", report.CgroupPath, memoryMax, pidsMax, liveProcs)
}

// 探针 ⑥（P0 回归）：默认配置下，插件只能写「运维配置给它的数据目录」。
//
// 这是 sforum-storage-fs 的等价后端：它对 SFORUM_SETTING_ROOT_PATH 做
// MkdirAll + WriteFile + CreateTemp。默认策略（不设任何 SFORUM_PLUGIN_SANDBOX_*）
// 必须让这个目录可写，同时宿主 $HOME、/etc/hosts 与宿主任意目录仍然不可写。
func TestSandboxProbeWritesOnlyConfiguredDataDir(t *testing.T) {
	// 默认 env：用空环境解析策略，等价于一个没设任何 SFORUM_PLUGIN_SANDBOX_* 的宿主。
	options := SandboxOptionsFromEnv(func(string) string { return "" })
	if !options.Filesystem || !options.Cgroup || options.Network != NetworkOff {
		t.Fatalf("default policy drifted: %#v", options)
	}

	dataDir := filepath.Join(shortTempRoot(t), "storage-fs-root") // 故意先不存在
	hostDir := t.TempDir()                                        // 宿主可写、沙箱不可写
	run := runSandboxProbe(t, sandboxProbeOptions{
		policy: SandboxPolicy{Filesystem: options.Filesystem, Network: options.Network, Cgroup: options.Cgroup},
		settingsEnv: []string{
			"SFORUM_SETTING_ROOT_PATH=" + dataDir,
			// 非路径设置必须被过滤掉，不能变成可写面。
			"SFORUM_SETTING_PUBLIC_BASE_URL=https://cdn.example/base",
		},
		dataDir:   dataDir,
		hostDir:   hostDir,
		memoryMax: "64M",
		pidsMax:   "64",
	})
	report := requireReport(t, run)
	requireLandlock(t, report)

	if run.result.DataDir != "ok" {
		t.Fatalf("plugin could not write its configured data dir: %q", run.result.DataDir)
	}
	// 真落盘：不是「系统调用没报错」，而是对象文件确实在宿主的文件系统上。
	probeFile := filepath.Join(dataDir, "objects", "2026", "11", "probe.bin")
	if data, err := os.ReadFile(probeFile); err != nil || string(data) != "ok" {
		t.Fatalf("data dir object is missing on disk: data=%q err=%v", data, err)
	}
	if run.result.HostDir != "EACCES" {
		t.Fatalf("plugin wrote into a host-only directory: %q", run.result.HostDir)
	}
	if run.result.EtcHosts == "ok" {
		t.Fatal("plugin appended to /etc/hosts")
	}
	if run.result.HomeWrite == "ok" {
		t.Fatal("plugin wrote into the host HOME")
	}
	if report.Cgroup == StatusDisabled {
		t.Fatal("default policy must attempt cgroup limits, not skip them")
	}
	t.Logf("probe: data-dir=%s host-dir=%s etc-hosts=%s home=%s",
		run.result.DataDir, run.result.HostDir, run.result.EtcHosts, run.result.HomeWrite)
}

// 探针 ⑤：模拟 Landlock 不可用（注入 ABI=0）时降级而不是崩——插件照常启动。
func TestSandboxProbeDegradesWhenLandlockUnavailable(t *testing.T) {
	unavailable := 0
	run := runSandboxProbe(t, sandboxProbeOptions{
		policy:      SandboxPolicy{Filesystem: true, Network: NetworkAll},
		abiOverride: &unavailable,
	})
	report := requireReport(t, run)

	if report.Filesystem != StatusDegraded || report.Network != StatusDegraded {
		t.Fatalf("injected ABI=0 must degrade both layers, got filesystem=%s network=%s", report.Filesystem, report.Network)
	}
	if !strings.Contains(report.FSReason, "landlock") {
		t.Fatalf("degradation reason %q does not name landlock", report.FSReason)
	}
	// 关键：插件仍然启动了，而且（因为没有强制）能读宿主 canary。
	if run.result.Canary != "ok:host-only" {
		t.Fatalf("degraded run should not confine the plugin; canary read = %q", run.result.Canary)
	}
	if !report.NoNewPrivs {
		t.Fatal("PR_SET_NO_NEW_PRIVS must still be applied when landlock is unavailable")
	}
	t.Logf("probe: filesystem=%s reason=%q network=%s reason=%q temp=%s nnp=%t",
		report.Filesystem, report.FSReason, report.Network, report.NetworkReason, run.result.TempFile, report.NoNewPrivs)
}

// 附加探针：启动器必须原样穿过 RV-53 的「已校验 inode」——exec 目标仍是
// /proc/self/fd/3，argv[0] 仍是可读的入口名。
func TestSandboxLauncherExecutesPinnedInode(t *testing.T) {
	pinned, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatalf("open test binary as pinned inode: %v", err)
	}
	defer pinned.Close()
	run := runSandboxProbe(t, sandboxProbeOptions{
		policy:     SandboxPolicy{Filesystem: true, Network: NetworkOff},
		entry:      "/proc/self/fd/3",
		argv0:      "pinned-plugin",
		extraFiles: []*os.File{pinned},
	})
	report := requireReport(t, run)
	requireLandlock(t, report)

	if run.result.Argv0 != "pinned-plugin" {
		t.Fatalf("pinned exec argv[0] = %q, want pinned-plugin", run.result.Argv0)
	}
	if run.result.Artifact != "ok:artifact" {
		t.Fatalf("pinned exec could not read its artifact dir: %q", run.result.Artifact)
	}
	// 钉住的入口 fd 3 是**唯一的**宿主继承 fd；报告管道绝不该留在这里。
	requireNoHostFDs(t, run)
	foundPinned := false
	for _, entry := range run.result.ExtraFDs {
		if strings.HasPrefix(entry, "3=") && strings.Contains(entry, filepath.Base(os.Args[0])) {
			foundPinned = true
		}
	}
	if !foundPinned {
		t.Fatalf("pinned inode fd 3 is missing from the plugin fd table: %v", run.result.ExtraFDs)
	}
	t.Logf("probe: exec=/proc/self/fd/3 argv0=%s artifact=%s", run.result.Argv0, run.result.Artifact)
}

// shortTempRoot 提供一个长度接近生产（/tmp）的宿主临时目录。
//
// 必须短：per-plugin TMPDIR 会再拼上 sforum-plugin-sandbox/<slug>-<pid>，而 go-plugin
// 的 unix socket 路径受内核 108 字节 sun_path 限制——用 t.TempDir()（约 90 字节）
// 会让探针在 bind 上先撞 EINVAL，测到的就不是 Landlock 了。
func shortTempRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sfsbx-")
	if err != nil {
		t.Fatalf("create short temp root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// requireNoHostFDs 断言插件 fd 表里没有宿主塞进去的管道/文件。
//
// 只查「pipe:」是刻意的：Go 运行时会自己持有 epoll/eventfd（anon_inode），那不是
// 宿主泄漏；报告管道是 pipe，必须不在。
func requireNoHostFDs(t *testing.T, run sandboxProbeRun) {
	t.Helper()
	t.Logf("plugin fd table (3..5): %v", run.result.ExtraFDs)
	for _, entry := range run.result.ExtraFDs {
		if strings.Contains(entry, "pipe:") {
			t.Fatalf("host report pipe leaked into the plugin fd table: %v", run.result.ExtraFDs)
		}
		// 除探针自己的入口（fd 3，pinned 用例里才有）外，这里只应出现 Go 运行时
		// 自己持有的 anon_inode（epoll/eventfd）。
		target := entry[strings.Index(entry, "=")+1:]
		if !strings.HasPrefix(target, "anon_inode:") && !strings.HasSuffix(target, filepath.Base(os.Args[0])) {
			t.Fatalf("unexpected inherited fd in the plugin fd table: %v", run.result.ExtraFDs)
		}
	}
}

func readCgroupValue(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}
