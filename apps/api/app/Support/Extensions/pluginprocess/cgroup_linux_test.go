//go:build linux

package pluginprocess

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCgroupRoot 造一个「已委派 memory+pids 的空父组」：等价于本机 app.slice 的形态，
// 也是 CgroupRootOverride 的合法输入。
func fakeCgroupRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "cgroup.controllers"), "memory pids")
	writeFile(t, filepath.Join(root, "cgroup.subtree_control"), "memory pids")
	return root
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func cgroupTestSpec(t *testing.T, root string) SandboxSpec {
	t.Helper()
	return SandboxSpec{
		PluginID:           "demo.plugin",
		TempRoot:           t.TempDir(),
		Policy:             SandboxPolicy{Cgroup: true},
		MemoryMax:          "64M",
		PidsMax:            "16",
		CgroupRootOverride: root,
	}
}

// 服务 scope 自己已经委派好控制器时，per-plugin 组必须建在服务 scope 里。
func TestCgroupParentUnderPrefersServiceScope(t *testing.T) {
	mount := t.TempDir()
	relative := "/app.slice/sforum.service"
	own := filepath.Join(mount, relative)
	if err := os.MkdirAll(own, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(own, "cgroup.controllers"), "memory pids")
	writeFile(t, filepath.Join(own, "cgroup.subtree_control"), "")

	parent, ownPath, reason := cgroupParentUnder(mount, relative)
	if parent != own || ownPath != own || reason != "" {
		t.Fatalf("parent=%q own=%q reason=%q, want the service scope %q", parent, ownPath, reason, own)
	}
	if note := cgroupScopeNote(parent); note != "" {
		t.Fatalf("landing in the service scope must not warn: %q", note)
	}
}

// 自己的 scope 开不出控制器（真实环境里是 EBUSY）时，退到最近的已委派祖先，
// 并且必须给出「落点偏移」的提示。
func TestCgroupParentUnderFallsBackToDelegatedAncestor(t *testing.T) {
	mount := t.TempDir()
	relative := "/app.slice/sforum.service"
	own := filepath.Join(mount, relative)
	ancestor := filepath.Join(mount, "/app.slice")
	for _, dir := range []string{own, ancestor} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "cgroup.controllers"), "memory pids")
	}
	// 用自己的 cgroup 模拟「有进程的 cgroup」：subtree_control 写成目录，写入必然失败。
	if err := os.Mkdir(filepath.Join(own, "cgroup.subtree_control"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ancestor, "cgroup.subtree_control"), "memory pids")

	parent, ownPath, reason := cgroupParentUnder(mount, relative)
	if parent != ancestor || ownPath != own {
		t.Fatalf("parent=%q own=%q reason=%q, want ancestor %q", parent, ownPath, reason, ancestor)
	}
	note := cgroupScopeNote(parent)
	if !strings.Contains(note, "DelegateSubgroup") || !strings.Contains(note, ancestor) {
		t.Fatalf("scope offset note = %q, want an actionable DelegateSubgroup hint", note)
	}
}

// F1 的形状：unit 声明 Delegate=yes + DelegateSubgroup=payload 之后，
// own=`<unit>/payload`（有进程、开不了控制器），建组落在 `<unit>`——**不是** slice，
// 所以既不降级也不该告警。
func TestCgroupParentUnderLandsInUnitWithDelegateSubgroup(t *testing.T) {
	mount := t.TempDir()
	unit := filepath.Join(mount, "system.slice", "sforum.service")
	payload := filepath.Join(unit, "payload")
	for _, dir := range []string{payload, unit} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "cgroup.controllers"), "memory pids")
	}
	// payload 里有主进程：subtree_control 写成目录，写入必然失败（等价于 EBUSY）。
	if err := os.Mkdir(filepath.Join(payload, "cgroup.subtree_control"), 0o755); err != nil {
		t.Fatal(err)
	}
	// unit 自己已被 systemd 委派且没有进程。
	writeFile(t, filepath.Join(unit, "cgroup.subtree_control"), "memory pids")

	parent, own, reason := cgroupParentUnder(mount, "/system.slice/sforum.service/payload")
	if parent != unit || own != payload || reason != "" {
		t.Fatalf("parent=%q own=%q reason=%q, want the unit cgroup %q", parent, own, reason, unit)
	}
	if note := cgroupScopeNote(parent); note != "" {
		t.Fatalf("landing in the unit cgroup must not warn: %q", note)
	}
}

// 没有任何委派祖先：降级并给出结构化原因，而不是硬失败。
func TestCgroupParentUnderDegradesWithoutDelegation(t *testing.T) {
	mount := t.TempDir()
	relative := "/app.slice/sforum.service"
	own := filepath.Join(mount, relative)
	for _, dir := range []string{own, filepath.Join(mount, "/app.slice")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "cgroup.controllers"), "cpu")
	}
	parent, _, reason := cgroupParentUnder(mount, relative)
	if parent != "" || reason == "" {
		t.Fatalf("parent=%q reason=%q, want a degraded reason", parent, reason)
	}
}

// F4：锁被别的进程长期持有时，建组必须**有界等待后降级**，而不是把插件启动挂住。
// 这是语义变更：旧实现（LOCK_EX 阻塞）把「等锁」当成正常路径，而 /tmp 下的锁文件
// 任何本地用户都能先建出来持住，等于一个远程可触发的启动挂死。
func TestApplySandboxCgroupDegradesAfterBoundedLockWait(t *testing.T) {
	root := fakeCgroupRoot(t)
	spec := cgroupTestSpec(t, root)
	unlock, err := lockPluginCgroupCreation(spec, root, pluginCgroupBaseName(spec.PluginID))
	if err != nil {
		t.Fatalf("acquire host lock: %v", err)
	}
	defer unlock()
	started := time.Now()
	outcome := applySandboxCgroup(spec)
	elapsed := time.Since(started)
	if outcome.Status != StatusEnforced {
		t.Fatalf("outcome = %#v, want enforced (degraded lock must not block startup)", outcome)
	}
	if elapsed < cgroupLockWaitBudget/2 {
		t.Fatalf("gave up after %s, want a bounded wait of about %s", elapsed, cgroupLockWaitBudget)
	}
	if elapsed > cgroupLockWaitBudget*3 {
		t.Fatalf("waited %s for the lock, want at most about %s", elapsed, cgroupLockWaitBudget)
	}
	joined := strings.Join(outcome.Notes, " | ")
	if !strings.Contains(joined, "cgroup create lock unavailable") || !strings.Contains(joined, "timed out") {
		t.Fatalf("degraded lock must be reported in notes, got %q", joined)
	}
}

// F4：锁目录是 /tmp 下可以被别人先建出来的路径，属主/权限不对就不能用。
func TestPluginCgroupLockRejectsUntrustedDir(t *testing.T) {
	spec := cgroupTestSpec(t, fakeCgroupRoot(t))
	lockRoot := filepath.Join(spec.TempRoot, cgroupLockSubdir)
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockRoot, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := lockPluginCgroupCreation(spec, "/sys/fs/cgroup", "sforum-plugin-demo"); err == nil ||
		!strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("world-writable lock dir must be rejected, got %v", err)
	}
}

// F8：锁必须按「解析后的 cgroup 名」分片，而不是按 pluginID——两个 pluginID 在
// slug+digest 上撞名时，它们要抢的是同一个目录，必须抢同一把锁。
func TestPluginCgroupLockShardsByCgroupName(t *testing.T) {
	same := pluginCgroupLockPath("/locks", "/sys/fs/cgroup", "sforum-plugin-demo-1234abcd")
	again := pluginCgroupLockPath("/locks", "/sys/fs/cgroup", "sforum-plugin-demo-1234abcd")
	if same != again {
		t.Fatalf("same cgroup name must share a lock: %s vs %s", same, again)
	}
	other := pluginCgroupLockPath("/locks", "/sys/fs/cgroup", "sforum-plugin-demo-deadbeef")
	if same == other {
		t.Fatal("different cgroup names must not share a lock")
	}
	if !strings.HasPrefix(filepath.Base(same), "") || !strings.HasSuffix(same, ".lock") {
		t.Fatalf("unexpected lock path %s", same)
	}
}

// F7：告警基准是「落点在不在 unit 里」，不是「parent 是否等于自己的 cgroup」。
// 声明 DelegateSubgroup 之后 own=`<unit>/<subgroup>`、parent=`<unit>`，这是正确形态。
func TestCgroupScopeNoteOnlyWarnsOutsideUnit(t *testing.T) {
	cases := []struct {
		parent string
		warn   bool
	}{
		{parent: "/sys/fs/cgroup/system.slice/sforum.service", warn: false},
		{parent: "/sys/fs/cgroup/system.slice/sforum.service/payload", warn: false},
		{parent: "/sys/fs/cgroup/system.slice", warn: true},
		{parent: "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice", warn: true},
		{parent: "", warn: false},
	}
	for _, test := range cases {
		note := cgroupScopeNote(test.parent)
		if test.warn && (note == "" || !strings.Contains(note, "DelegateSubgroup")) {
			t.Fatalf("%s must warn with an actionable hint, got %q", test.parent, note)
		}
		if !test.warn && note != "" {
			t.Fatalf("%s must not warn, got %q", test.parent, note)
		}
	}
}

// 同插件多实例并发建组：每个实例都要拿到自己的组，不能出现共享/互相覆盖。
//
// 3 个实例 = 升级重叠窗口（2 个）加一个掉队者；组名预算 4 个（base 与 -2/-3/-4），
// 因此这是「不降级」的上界，不是随手取的数。
func TestApplySandboxCgroupConcurrentInstances(t *testing.T) {
	root := fakeCgroupRoot(t)
	spec := cgroupTestSpec(t, root)
	const instances = 3
	results := make(chan cgroupOutcome, instances)
	var wait sync.WaitGroup
	for index := 0; index < instances; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- applySandboxCgroup(spec)
		}()
	}
	wait.Wait()
	close(results)

	seen := make(map[string]bool, instances)
	for outcome := range results {
		if outcome.Status != StatusEnforced {
			t.Fatalf("concurrent instance degraded: %#v", outcome)
		}
		if seen[outcome.Path] {
			t.Fatalf("two instances shared the same cgroup %s", outcome.Path)
		}
		seen[outcome.Path] = true
	}
	if len(seen) != instances {
		t.Fatalf("distinct cgroups = %d, want %d", len(seen), instances)
	}
}
