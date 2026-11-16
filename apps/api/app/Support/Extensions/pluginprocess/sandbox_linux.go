//go:build linux

package pluginprocess

// 沙箱启动器：宿主重新 exec 自己，先施加内核级约束，再 exec 真实插件。
//
// 为什么必须是「再 exec 一次自己」而不是在宿主的 fork 之后做：
// Go 的 exec.Cmd 没有 pre-exec hook，父进程只能事后 prlimit；而 Landlock 的
// restrict_self 与 PR_SET_NO_NEW_PRIVS 必须**在同一个线程上**先于 execve 完成。
// 重新 exec 一个宿主自己的入口，让我们拿到一个「只做加固然后 exec」的干净起点：
// 加固从插件的第一条指令起生效，而不是从它跑了几毫秒之后。
//
// 线程语义：runLandlock 按线程生效。RunLauncher 第一件事是 LockOSThread，之后
// restrict 与 exec 都在同一条线程上完成；execve 会替换整个进程，域随之被插件继承。

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// launcherReportLimit 限制报告读取字节数（管道内容由我们自己的启动器写）。
	launcherReportLimit = 64 << 10
	// launchReportLifetime 是报告管道的兜底寿命：启动失败路径不会走到
	// DrainLaunchReport，靠这个定时器回收 fd 与注册项。
	launchReportLifetime = 10 * time.Minute
	// launchReportReadTimeout 是一次性读取报告的上限（数据在 exec 之前就写好了）。
	launchReportReadTimeout = 2 * time.Second

	launcherExitUsage = 2
	launcherExitExec  = 126

	// sunPathLimit 是 unix socket 路径的内核上限；unixSocketPathHeadroom 是
	// go-plugin 在 TMPDIR 下追加的 "plugin<random>" 段长度估计。
	sunPathLimit           = 108
	unixSocketPathHeadroom = 16
)

// WrapCommand 把 cmd 改写成「经沙箱启动器 exec」的形式。
//
// 前提：cmd.Path 已经指向最终要执行的插件入口（生产路径是 /proc/self/fd/3 的
// 已校验 inode）。返回 error 时 cmd 未被修改，调用方应直接 exec（降级）：
// 沙箱不可用不该让插件起不来。
func WrapCommand(cmd *exec.Cmd, spec SandboxSpec) error {
	if cmd == nil {
		return ErrLauncherUnsupported
	}
	if !LauncherEnabled() {
		return ErrLauncherNotRegistered
	}
	return wrapCommand(cmd, spec)
}

func wrapCommand(cmd *exec.Cmd, spec SandboxSpec) error {
	if strings.TrimSpace(spec.Entry) == "" || strings.TrimSpace(spec.ArtifactDir) == "" {
		return errors.New("sandbox spec requires entry and artifact dir")
	}
	executable, err := hostExecutable()
	if err != nil {
		return err
	}
	if artifact := strings.TrimSpace(spec.ArtifactDir); artifact != "" {
		if absolute, err := filepath.Abs(artifact); err == nil {
			spec.ArtifactDir = absolute
		}
	}
	if root := strings.TrimSpace(spec.PackageRoot); root != "" {
		if absolute, err := filepath.Abs(root); err == nil {
			spec.PackageRoot = absolute
		}
	}
	pluginArgs, err := pluginArgumentVector(cmd, spec)
	if err != nil {
		return err
	}
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create sandbox report pipe: %w", err)
	}
	spec.ReportFD = 3 + len(cmd.ExtraFiles)
	payload, err := json.Marshal(spec)
	if err != nil {
		_ = readEnd.Close()
		_ = writeEnd.Close()
		return fmt.Errorf("encode sandbox spec: %w", err)
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, writeEnd)
	cmd.Path = executable
	cmd.Args = append([]string{LauncherArgv0, string(payload)}, pluginArgs...)
	trackLaunchReport(cmd, readEnd, writeEnd)
	return nil
}

// pluginArgumentVector 计算真实插件进程的 argv。
//
// argv[0] 用可读的制品入口名而不是 /proc/self/fd/3：exec 目标被钉在已校验 inode
// 上，但 ps 里看到的应该是「这个插件」，不是宿主的 fd 表。
func pluginArgumentVector(cmd *exec.Cmd, spec SandboxSpec) ([]string, error) {
	argv0 := strings.TrimSpace(spec.Argv0)
	if argv0 == "" {
		if len(cmd.Args) > 0 {
			argv0 = cmd.Args[0]
		} else {
			argv0 = spec.Entry
		}
	}
	argv := make([]string, 0, 1+len(cmd.Args))
	argv = append(argv, argv0)
	if len(cmd.Args) > 1 {
		argv = append(argv, cmd.Args[1:]...)
	}
	return argv, nil
}

// hostExecutable 解析「重新 exec 的那个自己」。
//
// 显式拒绝已删除的二进制：air 之类的热重载会替换文件，但若路径本身消失，
// 重新 exec 会 ENOENT——那会把一次加固动作变成插件启动失败。
func hostExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve host executable: %w", err)
	}
	if strings.HasSuffix(executable, " (deleted)") {
		return "", fmt.Errorf("host executable %q is no longer on disk; refusing to re-exec it", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("stat host executable %q: %w", executable, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("host executable %q is a directory", executable)
	}
	return executable, nil
}

// --- 能力报告管道 ---

type launchReport struct {
	read      *os.File
	write     *os.File
	timer     *time.Timer
	once      sync.Once
	writeOnce sync.Once
}

var launchReports sync.Map // *exec.Cmd -> *launchReport

func trackLaunchReport(cmd *exec.Cmd, read, write *os.File) {
	report := &launchReport{read: read, write: write}
	launchReports.Store(cmd, report)
	report.timer = time.AfterFunc(launchReportLifetime, func() {
		launchReports.Delete(cmd)
		report.close()
	})
}

// closeWrite 关掉宿主这一端的写端。
//
// 取报告前必须先关：命令可能从未 Start（启动失败路径），此时管道里既没有数据也
// 不会有 EOF，读端会一直等到超时；关掉宿主写端后 EOF 立刻到达。已经 Start 过时，
// 子进程有自己的副本，关宿主这一端不影响它的写入。
func (r *launchReport) closeWrite() { r.writeOnce.Do(func() { _ = r.write.Close() }) }

func (r *launchReport) close() {
	r.once.Do(func() {
		if r.timer != nil {
			r.timer.Stop()
		}
		r.closeWrite()
		_ = r.read.Close()
	})
}

// pendingLaunchReports 报告还有多少条报告管道没被取走（fd 泄漏的探针）。
func pendingLaunchReports() int {
	count := 0
	launchReports.Range(func(any, any) bool {
		count++
		return true
	})
	return count
}

// TakeLaunchReport 取走启动器写回的能力报告并释放管道，但不记录日志。
//
// 调用点在「子进程已经出现」之后（插件握手成功时），此时报告一定已经在管道里：
// 启动器在 exec 之前就把报告写完并关闭了写端。返回 false 只表示没有报告可读，
// 不影响插件本身。
func TakeLaunchReport(cmd *exec.Cmd) (Report, bool) {
	if cmd == nil {
		return Report{}, false
	}
	value, ok := launchReports.LoadAndDelete(cmd)
	if !ok {
		return Report{}, false
	}
	report := value.(*launchReport)
	report.closeWrite()
	payload, readErr := readLaunchReport(report.read)
	report.close()
	if readErr != nil {
		slog.Warn("plugin sandbox: launcher report is unreadable", "error", readErr)
		return Report{}, false
	}
	if payload == "" {
		return Report{}, false
	}
	var decoded Report
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		slog.Warn("plugin sandbox: launcher report is malformed", "error", err)
		return Report{}, false
	}
	return decoded, true
}

// DrainLaunchReport 取走能力报告并按宿主现有 slog 落点记录（不新增审计通道）。
//
// 生产接线只调这一个函数：它可以被忽略返回值地当作一条语句使用。
func DrainLaunchReport(cmd *exec.Cmd) {
	if report, ok := TakeLaunchReport(cmd); ok {
		logLaunchReport(report)
	}
}

func readLaunchReport(read *os.File) (string, error) {
	// 有界读取：写端是我们自己的启动器，但一个坏掉的 fd 不应让宿主永久阻塞。
	_ = read.SetReadDeadline(time.Now().Add(launchReportReadTimeout))
	line, err := bufio.NewReaderSize(io.LimitReader(read, launcherReportLimit), 4096).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// logLaunchReport 把能力结论落到宿主现有的 slog 审计落点上（不新造通道）。
func logLaunchReport(report Report) {
	if report.Degraded() {
		slog.Warn("plugin sandbox degraded",
			"plugin", report.PluginID, "pid", report.PID, "event", "plugin.sandbox.degraded",
			"landlock_abi", report.LandlockABI,
			"filesystem", report.Filesystem, "filesystem_reason", report.FSReason,
			"network", report.Network, "network_policy", report.NetworkPolicy, "network_reason", report.NetworkReason,
			"cgroup", report.Cgroup, "cgroup_path", report.CgroupPath, "cgroup_reason", report.CgroupReason,
			"no_new_privs", report.NoNewPrivs, "notes", report.Notes,
		)
		return
	}
	slog.Info("plugin sandbox enforced",
		"plugin", report.PluginID, "pid", report.PID, "event", "plugin.sandbox.enforced",
		"landlock_abi", report.LandlockABI,
		"filesystem", report.Filesystem, "network", report.Network, "network_policy", report.NetworkPolicy,
		"cgroup", report.Cgroup, "cgroup_path", report.CgroupPath,
		"no_new_privs", report.NoNewPrivs, "temp_dir", report.TempDir, "notes", report.Notes,
	)
}

// --- 启动器本体 ---

// RunLauncher 执行一次沙箱启动器调用，返回进程退出码（成功时不会返回：exec 替换
// 当前进程）。
//
// argv 是完整的进程 argv：argv[0] 是保留标记，argv[1] 是 spec JSON，argv[2:] 是
// 真实插件进程的 argv。主程序在 main 的第一条语句里用
// IsLauncherInvocation(os.Args) 判断是否进入这里。
func RunLauncher(argv []string) int {
	// Landlock 按线程生效：必须在同一条线程上 restrict_self 与 execve。
	runtime.LockOSThread()

	spec, pluginArgv, err := decodeLauncherInvocation(argv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sforum plugin sandbox: %v\n", err)
		return launcherExitUsage
	}
	report := Report{
		PluginID:      spec.PluginID,
		PID:           os.Getpid(),
		Entry:         spec.Entry,
		Filesystem:    StatusDisabled,
		FSReason:      "filesystem policy disabled",
		Network:       StatusDisabled,
		NetworkPolicy: string(spec.Policy.Network),
		NetworkReason: "network policy " + string(spec.Policy.Network),
		Cgroup:        StatusDisabled,
		CgroupReason:  "cgroup policy disabled",
	}

	tempDir, tempNotes, tempErr := prepareSandboxTempDir(spec)
	report.Notes = append(report.Notes, tempNotes...)
	if tempErr != nil {
		report.Notes = append(report.Notes, "temp dir: "+tempErr.Error())
	} else {
		report.TempDir = tempDir
		// go-plugin 的 unix socket 落在 os.TempDir() 里；把它钉到 per-plugin 私有
		// 目录，沙箱只需要给这一个目录写权限。
		_ = os.Setenv("TMPDIR", tempDir)
		// sun_path 只有 108 字节：宿主 TMPDIR 很深时，go-plugin 的 socket 路径会
		// 先撞上这个限制（bind 返回 EINVAL），表现成「插件握手失败」。这不是沙箱
		// 拒绝访问，但同样致命，所以留一条可诊断的提示。
		if len(tempDir)+unixSocketPathHeadroom > sunPathLimit {
			report.Notes = append(report.Notes, fmt.Sprintf(
				"sandbox temp dir is %d bytes; go-plugin unix socket paths may exceed the %d-byte sun_path limit",
				len(tempDir), sunPathLimit,
			))
		}
	}

	if spec.Policy.Cgroup {
		outcome := applySandboxCgroup(spec)
		report.Cgroup = outcome.Status
		report.CgroupPath = outcome.Path
		report.CgroupReason = outcome.Reason
		report.CgroupLimits = outcome.Limits
		report.Notes = append(report.Notes, outcome.Notes...)
	}

	applyLauncherRlimits()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		report.Notes = append(report.Notes, "PR_SET_NO_NEW_PRIVS: "+err.Error())
	} else {
		report.NoNewPrivs = true
	}

	// 可写面必须**先存在**再进 Landlock：规则挂在 inode 上，路径不存在就加不进去，
	// 而插件里的 MkdirAll(root) 又需要 root 的**父目录**可写——那正是我们不给的。
	// 所以在宿主还拥有完整权限时把运维配置的数据目录建出来（只建目录本身，
	// 内容仍由插件自己管）。
	if spec.Policy.Filesystem {
		prepareWritableRoots(spec)
	}

	if spec.Policy.Filesystem || spec.Policy.Network != NetworkOff {
		outcome := confineWithLandlock(spec, tempDir)
		report.LandlockABI = outcome.ABI
		report.Filesystem = outcome.Filesystem
		report.FSReason = outcome.FSReason
		report.Network = outcome.Network
		report.NetworkReason = outcome.NetReason
		report.Notes = append(report.Notes, outcome.Notes...)
	}

	// 报告必须在 exec 之前写完并关闭写端：插件进程里不该留宿主的报告 fd。
	writeLaunchReport(spec.ReportFD, report)

	if len(pluginArgv) == 0 {
		pluginArgv = []string{spec.Entry}
	}
	if pluginArgv[0] == "" {
		pluginArgv[0] = spec.Entry
	}
	if err := unix.Exec(spec.Entry, pluginArgv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "sforum plugin sandbox: exec %s: %v\n", spec.Entry, err)
		return launcherExitExec
	}
	return 0
}

func decodeLauncherInvocation(argv []string) (SandboxSpec, []string, error) {
	if len(argv) < 3 || argv[0] != LauncherArgv0 {
		return SandboxSpec{}, nil, errors.New("not a plugin sandbox launcher invocation")
	}
	var spec SandboxSpec
	if err := json.Unmarshal([]byte(argv[1]), &spec); err != nil {
		return SandboxSpec{}, nil, fmt.Errorf("decode sandbox spec: %w", err)
	}
	if strings.TrimSpace(spec.Entry) == "" {
		return SandboxSpec{}, nil, errors.New("sandbox spec has no exec entry")
	}
	return spec, append([]string(nil), argv[2:]...), nil
}

// prepareWritableRoots 为「运维显式配置的插件数据目录」建出目录本身。
//
// 只 MkdirAll 目录、不写任何内容；失败只是让该目录在 Landlock 里缺一条规则
// （插件随后会拿到 EACCES，并且报告里能看到它不在可写面里），不影响启动。
func prepareWritableRoots(spec SandboxSpec) {
	for _, path := range spec.ExtraReadWrite {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			continue
		}
		_ = os.MkdirAll(path, 0o750)
	}
}

// applyLauncherRlimits 在 exec 之前收紧限额，因此限额从插件的第一条指令起生效
// （父进程的 prlimit 只能约束稳态，见 Limit 的说明）。
func applyLauncherRlimits() {
	lowerRLimit(unix.RLIMIT_NOFILE, MaxOpenFiles, false)
	// core dump 连硬上限一起清零：插件崩溃时不该把含 host token 的内存落盘，
	// 而「保留硬上限」等于把这条限制变成可撤销的。
	lowerRLimit(unix.RLIMIT_CORE, 0, true)
	if limit, ok := nprocLimit(); ok {
		lowerRLimit(unix.RLIMIT_NPROC, limit, false)
	}
}

// lowerRLimit 只下调、绝不抬高；lowerHard 为真时连硬上限一起收。
func lowerRLimit(resource int, ceiling uint64, lowerHard bool) {
	var current unix.Rlimit
	if err := unix.Getrlimit(resource, &current); err != nil {
		return
	}
	want := current
	if want.Cur > ceiling {
		want.Cur = ceiling
	}
	if lowerHard && want.Max > ceiling {
		want.Max = ceiling
		if want.Cur > want.Max {
			want.Cur = want.Max
		}
	}
	if want == current {
		return
	}
	_ = unix.Setrlimit(resource, &want)
}

// writeLaunchReport 把能力报告写给宿主，并关闭写端。
func writeLaunchReport(fd int, report Report) {
	if !reportEnabled(fd) {
		return
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return
	}
	file := os.NewFile(uintptr(fd), "sforum-sandbox-report")
	if file == nil {
		return
	}
	_, _ = file.Write(append(payload, '\n'))
	_ = file.Close()
}
