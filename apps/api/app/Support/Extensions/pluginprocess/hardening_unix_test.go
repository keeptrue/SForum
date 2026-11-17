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

// Limit 的签名契约：nil 与尚未 start 的 Cmd 必须安全返回、不得 panic。
// 现唯一调用点 applyPluginProcessLimits 在 client.Client() 成功之后才调，没有更早的
// 调用路径；这条用例守的是「将来若有人提前调用，也不至于把宿主打崩」。平台差异见 hardening_linux_test.go。
func TestLimitToleratesUnstartedCommand(t *testing.T) {
	Limit(nil)
	Limit(&exec.Cmd{})
}
