//go:build linux

package pluginprocess

import (
	"os"
	"os/exec"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Limit 在子进程出现之后立刻收紧它的资源上限。
//
// 只实现在 Linux：把限额施加到**插件子进程**（而不是宿主自己）需要 prlimit(2)，
// 而 x/sys/unix 只在 Linux 上提供 unix.Prlimit。其它 unix 平台的诚实 no-op 见
// hardening_unix_noprlimit.go。
//
// 诚实说明：Go 的 exec.Cmd 没有 pre-exec hook，rlimit 只能在 fork/exec 之后由
// 父进程用 prlimit(2) 施加，所以限制的是稳态而不是最初几微秒。要拿到「从第一条
// 指令起就受限」，需要 launcher 或 namespace 级隔离：Linux 上已有这条独立路径
// （sandbox_linux.go 的 applyLauncherRlimits 在 launcher 进程里 setrlimit，随后
// execve 让插件继承），本轮不改动它。
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
