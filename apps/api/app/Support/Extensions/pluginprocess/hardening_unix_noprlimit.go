//go:build unix && !linux

package pluginprocess

import "os/exec"

// Limit 在「unix 但没有 prlimit(2)」的平台（darwin、*BSD 等）上是显式 no-op：
// 插件子进程照常启动，只是拿不到 Limit 这层 rlimit 收紧。
//
// 为什么不降级成 setrlimit：setrlimit(2) 只作用于**调用它的进程**。宿主在子进程
// 已经 fork 出来之后调它，改的是宿主自己的限额，插件子进程一个字节都不受影响；
// 而「按 pid 改另一个进程」的能力来自 prlimit(2)，只有 Linux 有。用一个改宿主、
// 不改插件的调用去顶替，比空实现更糟——它会让宿主自己去吃插件的 fd 预算，
// 把一个加固缺口伪装成已完成。
//
// 当前平台的真实边界：这里只有进程组语义（Apply 的独立进程组、GroupID、
// KillGroup 的整组回收）与 Linux 一致；插件子进程的 fd 上限、core dump 与进程数
// 上限完全继承宿主环境脚本/launchd 的设定，capability 报告里也不会出现这些项
// （非 Linux 的 DrainLaunchReport 同样是空实现，见 sandbox_other.go）。因此
// 「Limit 返回了」在这些平台上不构成任何加固证据。
//
// 解困方向是自内而外，而不是宿主侧换 API：让即将 exec 的那个进程自己收紧限额，
// 再由 execve 把限额带给插件。Linux 已按这条路实现（launcher 重新 exec 宿主入口，
// 在 execve 前 setrlimit 自己，见 sandbox_linux.go 的 applyLauncherRlimits），
// 限额因此从插件第一条指令起生效。把同一条路径移植到 darwin 需要的是同样的一次
// self-exec 启动器，而不是把这里的 prlimit 换成 setrlimit。
func Limit(cmd *exec.Cmd) {}
