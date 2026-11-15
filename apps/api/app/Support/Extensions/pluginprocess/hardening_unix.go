//go:build unix

// Package pluginprocess 承载插件子进程的平台级启动约束：独立进程组、可用资源
// 上限与整组回收。
//
// 它刻意只依赖标准库与 x/sys/unix，不引用 Host 运行时包：这是「子进程怎么被
// 启动和回收」的机制层，任何信任判定都留在调用方（extensionsruntime）。
//
// 能力边界（诚实说明，不是承诺）：这里提供的是进程级加固，不是沙箱。插件仍以
// 宿主 uid 运行，文件系统、网络与派生出的进程都不受这些调用的限制。详见
// knowledge/decisions/2026-11-15-ex2-01-plugin-process-trust-boundary.md。
package pluginprocess

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	// MaxOpenFiles 是插件子进程的 fd 软上限。插件跑 RPC 服务，同时打开的
	// socket/文件不会接近四位数；压在这里可以挡住连接泄漏把宿主的 fd 表一起拖垮。
	MaxOpenFiles = 4096
	// NPROCEnv 显式开启子进程数上限。默认不设：Linux 的 RLIMIT_NPROC 按 real
	// uid 计数，对「与宿主同 uid」的插件施加它就是给整个用户设上限，会误伤本机
	// 其它工作负载。运维明确知道自己的 uid 预算时才通过该变量 opt-in。
	NPROCEnv = "SFORUM_PLUGIN_NPROC"
)

// Apply 在 exec 之前把插件放进独立进程组。
//
// 两个作用：宿主终端/进程组的信号广播不再顺带打到插件；整组回收有了句柄
// （Stop/Discard 与启动失败兜底都按 -pgid 回收，见 KillGroup）。
func Apply(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// GroupID 返回子进程所在的进程组 id。
//
// Apply 用的是 Setpgid 且 Pgid=0，内核把组 id 设成子进程自己的 pid；没有进程
// （还没 exec 或已经回收）时返回 0，调用方据此跳过整组回收。
func GroupID(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 1 {
		return 0
	}
	return cmd.Process.Pid
}

// Limit 在子进程出现之后立刻收紧它的资源上限。
//
// 诚实说明：Go 的 exec.Cmd 没有 pre-exec hook，rlimit 只能在 fork/exec 之后由
// 父进程用 prlimit(2) 施加，所以限制的是稳态而不是最初几微秒。要拿到「从第一条
// 指令起就受限」，需要 launcher 或 namespace 级隔离，不在本轮范围。
//
// 内存不在这里限制：Go 运行时会预留大段虚拟地址空间，RLIMIT_AS 会直接把插件
// 打死，RLIMIT_RSS 在 Linux 上不生效。内存走的是宿主注入的 GOMEMLIMIT 软上限
// （见 extensionsruntime 的插件 env 白名单）。
func Limit(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	lowerSoftLimit(pid, unix.RLIMIT_NOFILE, MaxOpenFiles)
	// core dump：崩溃时不把插件内存（含 host API token）写进磁盘。
	lowerSoftLimit(pid, unix.RLIMIT_CORE, 0)
	if limit, ok := nprocLimit(); ok {
		lowerSoftLimit(pid, unix.RLIMIT_NPROC, limit)
	}
}

// lowerSoftLimit 只下调软上限、保留硬上限，并且绝不抬高现有软上限：把限制施加
// 到一个已经更严格的进程上不该变成放宽。
func lowerSoftLimit(pid int, resource int, ceiling uint64) {
	var current unix.Rlimit
	if err := unix.Prlimit(pid, resource, nil, &current); err != nil {
		return
	}
	want := current
	if want.Cur > ceiling {
		want.Cur = ceiling
	}
	if want.Cur == current.Cur {
		return
	}
	// 失败不改变启动结果：这是一层尽力而为的加固，拒绝启动反而会让限额配置成为
	// 新的可用性故障点。
	_ = unix.Prlimit(pid, resource, &want, nil)
}

func nprocLimit() (uint64, bool) {
	raw := strings.TrimSpace(os.Getenv(NPROCEnv))
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, false
	}
	return value, true
}

// KillGroup 按进程组回收插件及其派生的子进程。
//
// 调用方必须已经调用过 Apply 设置独立进程组。ESRCH 表示整组已经退出，属正常情况；
// 其它错误也无处上报（这是收尾路径），因此统一忽略。
func KillGroup(processGroup int) {
	if processGroup <= 1 || processGroup == os.Getpid() {
		return
	}
	_ = syscall.Kill(-processGroup, syscall.SIGKILL)
}
