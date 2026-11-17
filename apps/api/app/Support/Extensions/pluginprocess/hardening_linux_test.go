//go:build linux

package pluginprocess

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

// Limit 只下调软上限：这里用一个真实子进程验证 NOFILE 被压到 MaxOpenFiles 以内，
// 且硬上限没有被改成不可恢复的小值。
//
// 这是 Linux 专属断言：读回限额用的是 unix.Prlimit，其它 unix 平台没有该调用，
// 那里的 Limit 是显式 no-op（见 hardening_unix_noprlimit.go）。
func TestLimitLowersOpenFileSoftLimit(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 5")
	if err := cmd.Start(); err != nil {
		// 这条断言依赖一个真实可 exec 的 shell：起不来就是环境不满足，不能静默
		// 跳过——跳过会让「限额真的被施加了」这件事失去唯一的验证点。
		t.Fatalf("cannot start the helper process this assertion needs: %v", err)
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
