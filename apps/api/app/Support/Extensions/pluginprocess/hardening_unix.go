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
//
// 平台切分：本文件的进程组语义在全部 unix 上一致；资源上限（Limit）只有 Linux
// 能真正施加——改「另一个进程」的 rlimit 依赖 prlimit(2)，实现见
// hardening_linux.go 与 hardening_unix_noprlimit.go。
package pluginprocess

import (
	"os"
	"os/exec"
	"syscall"
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
