//go:build unix

package pluginprocess

import (
	"os"
	"os/exec"
	"testing"
)

func TestApplyPutsPluginInItsOwnProcessGroup(t *testing.T) {
	cmd := exec.Command("true")
	Apply(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("plugin command was not placed in an independent process group: %#v", cmd.SysProcAttr)
	}
}

func TestGroupIDIsZeroBeforeTheProcessExists(t *testing.T) {
	cmd := exec.Command("true")
	if got := GroupID(cmd); got != 0 {
		t.Fatalf("GroupID before start = %d, want 0", got)
	}
	if got := GroupID(nil); got != 0 {
		t.Fatalf("GroupID(nil) = %d, want 0", got)
	}
}

// KillGroup 的收尾语义：非法组 id 和宿主自己的进程组必须被忽略，否则一次失败的
// 启动就能把宿主整组杀掉。
func TestKillGroupIgnoresInvalidTargets(t *testing.T) {
	for _, target := range []int{0, -1, 1, os.Getpid()} {
		KillGroup(target)
	}
}

// Limit 必须容忍「还没 start 的命令」：启动失败路径上 Limit 可能先于进程出现被
// 调用，这里守住它对 nil 与空 Cmd 都不 panic。平台差异部分见 hardening_linux_test.go。
func TestLimitToleratesUnstartedCommand(t *testing.T) {
	Limit(nil)
	Limit(&exec.Cmd{})
}
