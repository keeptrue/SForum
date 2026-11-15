//go:build !linux

package pluginprocess

// 非 Linux 平台没有 Landlock / cgroup v2，也就没有启动器要施加的内核级约束。
// 这里保持签名一致、行为显式失败，让调用方按「降级为直接 exec」处理：
// 发布目标见 .github/workflows/release.yml（linux 与 darwin 都是 unix）。

import (
	"fmt"
	"os"
	"os/exec"
)

// WrapCommand 在非 Linux 上不做任何改写，并报告启动器不可用。
func WrapCommand(cmd *exec.Cmd, spec SandboxSpec) error { return ErrLauncherUnsupported }

// DrainLaunchReport 在非 Linux 上没有报告可读；签名与 Linux 实现保持一致，
// 这样 plugin_command_transport.go 的调用点不需要按平台分叉。
func DrainLaunchReport(cmd *exec.Cmd) {}

// RunLauncher 在非 Linux 上不可达；返回用法错误码而不是静默继续。
func RunLauncher(argv []string) int {
	fmt.Fprintln(os.Stderr, "sforum plugin sandbox: launcher is only implemented on Linux")
	return 2
}
