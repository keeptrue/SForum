package pluginprocess

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
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

// Limit 只下调软上限：这里用一个真实子进程验证 NOFILE 被压到 MaxOpenFiles 以内，
// 且硬上限没有被改成不可恢复的小值。
func TestLimitLowersOpenFileSoftLimit(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 5")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a helper process on this host: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	Apply(cmd)
	Limit(cmd)

	var limit unix.Rlimit
	if err := unix.Prlimit(cmd.Process.Pid, unix.RLIMIT_NOFILE, nil, &limit); err != nil {
		t.Fatalf("read back file-descriptor limit: %v", err)
	}
	if limit.Cur > MaxOpenFiles {
		t.Fatalf("plugin fd soft limit = %d, want <= %d", limit.Cur, MaxOpenFiles)
	}
	if limit.Max < limit.Cur {
		t.Fatalf("hard limit %d is below soft limit %d", limit.Max, limit.Cur)
	}
}

func TestNPROCLimitIsOptIn(t *testing.T) {
	t.Setenv(NPROCEnv, "")
	if _, ok := nprocLimit(); ok {
		t.Fatal("process-count limit must stay off unless the operator opts in")
	}
	t.Setenv(NPROCEnv, "512")
	value, ok := nprocLimit()
	if !ok || value != 512 {
		t.Fatalf("explicit opt-in was ignored: value=%d ok=%t", value, ok)
	}
	t.Setenv(NPROCEnv, "not-a-number")
	if _, ok := nprocLimit(); ok {
		t.Fatal("garbage process-count limit must fall back to off")
	}
}
