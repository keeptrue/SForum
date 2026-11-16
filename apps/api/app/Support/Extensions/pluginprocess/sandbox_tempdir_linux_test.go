//go:build linux

package pluginprocess

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeProc struct {
	state byte
	start uint64
}

// fakeProcTree 在临时目录里造一棵最小 /proc：boot_id + 指定的 pid stat。
//
// stat 只填我们真正解析的两个字段（第 3 的 state、第 22 的 starttime），其余补 0；
// comm 里故意带空格，确保解析按最后一个 ')' 切分。
func fakeProcTree(t *testing.T, bootID string, procs map[int]fakeProc) string {
	t.Helper()
	root := t.TempDir()
	if bootID != "" {
		dir := filepath.Join(root, "sys", "kernel", "random")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("stage boot_id dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "boot_id"), []byte(bootID+"\n"), 0o644); err != nil {
			t.Fatalf("stage boot_id: %v", err)
		}
	}
	for pid, proc := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("stage pid dir: %v", err)
		}
		fields := make([]string, 20)
		fields[0] = string(proc.state)
		for index := 1; index < 19; index++ {
			fields[index] = "0"
		}
		fields[19] = strconv.FormatUint(proc.start, 10)
		line := fmt.Sprintf("%d (fake proc) %s\n", pid, strings.Join(fields, " "))
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(line), 0o644); err != nil {
			t.Fatalf("stage stat: %v", err)
		}
	}
	return root
}

func writeTempDir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("stage temp dir %s: %v", dir, err)
	}
	return dir
}

func ownerDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), sandboxOwnerSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("stage owner dir: %v", err)
	}
	return dir
}

func stageOwner(t *testing.T, owners, name string, owner sandboxTempOwner) {
	t.Helper()
	if err := writeSandboxTempOwner(owners, name, owner); err != nil {
		t.Fatalf("write owner: %v", err)
	}
}

func TestSandboxTempOwnerRoundTrip(t *testing.T) {
	owners := ownerDir(t)
	stageOwner(t, owners, "demo-1234", sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: 1234, StartTime: 42, BootID: "boot-a"})
	owner, ok := readSandboxTempOwner(owners, "demo-1234")
	if !ok || owner.PID != 1234 || owner.StartTime != 42 || owner.BootID != "boot-a" {
		t.Fatalf("owner round trip = %#v ok=%v", owner, ok)
	}
	// 版本未知/内容损坏一律当作「没有记录」，由名字里的 pid 兜底。
	if err := os.WriteFile(sandboxTempOwnerPath(owners, "demo-1234"), []byte(`{"version":99,"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readSandboxTempOwner(owners, "demo-1234"); ok {
		t.Fatal("unknown owner version must be treated as missing")
	}
}

// 长跑插件的目录 mtime 可以非常旧，但只要它自己还活着就必须保留（且刷新 mtime）。
// 旧实现（只看 24h mtime）在这里会删掉活插件的 TMPDIR。
func TestSandboxTempDirVerdictKeepsLiveOwnerRegardlessOfMtime(t *testing.T) {
	_, start, err := readProcStat(defaultProcRoot, os.Getpid())
	if err != nil {
		t.Fatalf("read own stat: %v", err)
	}
	bootID := readProcBootID(defaultProcRoot)
	procRoot := fakeProcTree(t, bootID, map[int]fakeProc{os.Getpid(): {state: 'S', start: start}})
	owners := ownerDir(t)
	root := t.TempDir()
	name := fmt.Sprintf("demo-%d", os.Getpid())
	dir := writeTempDir(t, root, name)
	stageOwner(t, owners, name, sandboxTempOwner{
		Version: sandboxTempOwnerVersion, PID: os.Getpid(), StartTime: start, BootID: bootID,
	})
	if verdict := sandboxTempDirVerdict(owners, procRoot, bootID, dir, name); verdict != tempDirLive {
		t.Fatalf("live owner with a 72h-old mtime = %v, want tempDirLive", verdict)
	}
}

// 崩溃留下的目录不必等 24h：归属进程已经不在，立刻可回收。
func TestSandboxTempDirVerdictReclaimsDeadOwnerImmediately(t *testing.T) {
	const deadPID = 999999
	procRoot := fakeProcTree(t, "boot-a", nil)
	owners := ownerDir(t)
	root := t.TempDir()
	name := fmt.Sprintf("demo-%d", deadPID)
	dir := writeTempDir(t, root, name)
	stageOwner(t, owners, name, sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: deadPID, StartTime: 7, BootID: "boot-a"})
	if verdict := sandboxTempDirVerdict(owners, procRoot, "boot-a", dir, name); verdict != tempDirStale {
		t.Fatalf("dead owner with a fresh mtime = %v, want tempDirStale", verdict)
	}
}

// 僵尸进程已经释放了所有 fd：/proc 里还有条目，但目录只是残留。
func TestSandboxTempDirVerdictTreatsZombieAsDead(t *testing.T) {
	const zombiePID = 4242
	for _, state := range []byte{'Z', 'X'} {
		procRoot := fakeProcTree(t, "boot-a", map[int]fakeProc{zombiePID: {state: state, start: 11}})
		owners := ownerDir(t)
		root := t.TempDir()
		name := fmt.Sprintf("demo-%d", zombiePID)
		dir := writeTempDir(t, root, name)
		stageOwner(t, owners, name, sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: zombiePID, StartTime: 11, BootID: "boot-a"})
		if verdict := sandboxTempDirVerdict(owners, procRoot, "boot-a", dir, name); verdict != tempDirStale {
			t.Fatalf("owner in state %q = %v, want tempDirStale", state, verdict)
		}
	}
}

// pid 复用：号还是那个号，但 starttime 变了，说明是另一个进程实例。
func TestSandboxTempDirVerdictDetectsPIDReuse(t *testing.T) {
	const reusedPID = 5150
	procRoot := fakeProcTree(t, "boot-a", map[int]fakeProc{reusedPID: {state: 'S', start: 900}})
	owners := ownerDir(t)
	root := t.TempDir()
	name := fmt.Sprintf("demo-%d", reusedPID)
	dir := writeTempDir(t, root, name)
	stageOwner(t, owners, name, sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: reusedPID, StartTime: 100, BootID: "boot-a"})
	if verdict := sandboxTempDirVerdict(owners, procRoot, "boot-a", dir, name); verdict != tempDirStale {
		t.Fatalf("pid reuse (starttime mismatch) = %v, want tempDirStale", verdict)
	}
	// 上一次开机留下的记录：boot_id 不同，同样不是这个实例。
	stageOwner(t, owners, name, sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: reusedPID, StartTime: 900, BootID: "boot-previous"})
	if verdict := sandboxTempDirVerdict(owners, procRoot, "boot-a", dir, name); verdict != tempDirStale {
		t.Fatalf("previous boot owner = %v, want tempDirStale", verdict)
	}
}

// 没有宿主侧记录的旧目录：pid 活着只能算「未核实」，死了立刻删，取不到 pid 才等 TTL。
func TestSandboxTempDirVerdictLegacyDirsFallBackToName(t *testing.T) {
	procRoot := fakeProcTree(t, "boot-a", map[int]fakeProc{777: {state: 'S', start: 5}})
	owners := ownerDir(t)
	root := t.TempDir()
	cases := []struct {
		name string
		want tempDirVerdict
	}{
		{name: "demo-777", want: tempDirLiveUnverified},
		{name: "demo-778", want: tempDirStale},
		{name: "demo", want: tempDirUnattributable},
	}
	for _, test := range cases {
		dir := writeTempDir(t, root, test.name)
		if verdict := sandboxTempDirVerdict(owners, procRoot, "boot-a", dir, test.name); verdict != test.want {
			t.Fatalf("%s verdict = %v, want %v", test.name, verdict, test.want)
		}
	}
}

// 插件目录里那份旧版 `.sforum-owner` 对插件可写，不能作为存活证明：
// 伪造 pid 1 也不能让一个死掉插件的目录变成 live。
func TestSandboxTempDirVerdictIgnoresForgedInDirOwner(t *testing.T) {
	procRoot := fakeProcTree(t, "boot-a", map[int]fakeProc{1: {state: 'S', start: 1}})
	owners := ownerDir(t)
	root := t.TempDir()
	name := "demo-778" // 名字里的 pid 不存在
	dir := writeTempDir(t, root, name)
	forged := fmt.Sprintf(`{"version":%d,"pid":1,"start_time":1,"boot_id":"boot-a"}`, sandboxTempOwnerVersion)
	if err := os.WriteFile(filepath.Join(dir, sandboxTempOwnerFile), []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	if verdict := sandboxTempDirVerdict(owners, procRoot, "boot-a", dir, name); verdict != tempDirStale {
		t.Fatalf("forged in-dir owner must be ignored, verdict = %v, want tempDirStale", verdict)
	}
}

// 回收策略的端到端断言：核实过的活实例不受时间限制；未核实的只给硬上限；
// 无法归属的走 24h TTL。
func TestPruneSandboxTempDirsAt(t *testing.T) {
	const livePID = 8080
	procRoot := fakeProcTree(t, "boot-a", map[int]fakeProc{
		livePID: {state: 'S', start: 30},
		6060:    {state: 'S', start: 60},
		7070:    {state: 'S', start: 70}, // 活着但记录里没有 starttime → 只能算「未核实」
	})
	owners := ownerDir(t)
	tempRoot := t.TempDir()
	now := time.Now()

	liveOld := writeTempDir(t, tempRoot, fmt.Sprintf("demo-%d", livePID))
	stageOwner(t, owners, fmt.Sprintf("demo-%d", livePID), sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: livePID, StartTime: 30, BootID: "boot-a"})
	if err := os.Chtimes(liveOld, now.Add(-72*time.Hour), now.Add(-72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	deadFresh := writeTempDir(t, tempRoot, "demo-9091")
	stageOwner(t, owners, "demo-9091", sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: 9091, StartTime: 1, BootID: "boot-a"})
	unverifiedFresh := writeTempDir(t, tempRoot, "demo-6060")
	unverifiedOld := writeTempDir(t, tempRoot, "demo-7070")
	stageOwner(t, owners, "demo-7070", sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: 7070, StartTime: 0, BootID: "boot-a"})
	if err := os.Chtimes(unverifiedOld, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	unattributableFresh := writeTempDir(t, tempRoot, "orphan")
	unattributableOld := writeTempDir(t, tempRoot, "orphan-old")
	if err := os.Chtimes(unattributableOld, now.Add(-48*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	pruneSandboxTempDirsAt(tempRoot, owners, procRoot, "boot-a", now)

	if _, err := os.Stat(liveOld); err != nil {
		t.Fatalf("verified live plugin temp dir was reclaimed: %v", err)
	}
	if info, err := os.Stat(liveOld); err != nil || info.ModTime().Before(now.Add(-time.Hour)) {
		t.Fatalf("verified live dir mtime must be refreshed, got %v (%v)", info, err)
	}
	if _, err := os.Stat(unverifiedFresh); err != nil {
		t.Fatalf("unverified live dir inside the hard TTL must be kept: %v", err)
	}
	for _, removed := range []string{deadFresh, unverifiedOld, unattributableOld} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("%s should have been reclaimed (stat err = %v)", removed, err)
		}
	}
	if _, err := os.Stat(unattributableFresh); err != nil {
		t.Fatalf("unattributable dir inside the TTL window must be kept: %v", err)
	}
	// 回收目录时 owner 记录也要一起清掉，否则宿主侧会无界堆积。
	if _, err := os.Stat(sandboxTempOwnerPath(owners, "demo-9091")); !os.IsNotExist(err) {
		t.Fatalf("stale owner record survived pruning (err = %v)", err)
	}
	if _, err := os.Stat(sandboxTempOwnerPath(owners, fmt.Sprintf("demo-%d", livePID))); err != nil {
		t.Fatalf("live owner record must be kept: %v", err)
	}
}

// 建目录时把归属记录写在**宿主侧**：插件目录里不能出现可伪造的记录。
func TestPrepareSandboxTempDirWritesOwnerOutsidePluginDir(t *testing.T) {
	spec := SandboxSpec{PluginID: "demo.plugin", TempRoot: t.TempDir()}
	dir, notes, err := prepareSandboxTempDir(spec)
	if err != nil {
		t.Fatalf("prepareSandboxTempDir: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("healthy temp root must not degrade: %v", notes)
	}
	if _, err := os.Stat(filepath.Join(dir, sandboxTempOwnerFile)); !os.IsNotExist(err) {
		t.Fatalf("plugin-writable temp dir must not carry an owner record (err = %v)", err)
	}
	owners := filepath.Join(spec.TempRoot, sandboxOwnerSubdir)
	owner, ok := readSandboxTempOwner(owners, filepath.Base(dir))
	if !ok {
		t.Fatalf("host-side owner record missing for %s", dir)
	}
	if owner.PID != os.Getpid() || owner.StartTime == 0 {
		t.Fatalf("owner = %#v, want pid=%d with a start time", owner, os.Getpid())
	}
	if info, err := os.Stat(owners); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("owner dir must be 0700: %v %v", info, err)
	}
	if verdict := sandboxTempDirVerdict(owners, defaultProcRoot, readProcBootID(defaultProcRoot), dir, filepath.Base(dir)); verdict != tempDirLive {
		t.Fatalf("freshly created temp dir verdict = %v, want tempDirLive", verdict)
	}
}

// owner 目录被别人预建成可写时，记录通道整体降级，但插件照常启动——降级必须可观测。
func TestPrepareSandboxTempDirDegradesWhenOwnerDirIsUntrusted(t *testing.T) {
	spec := SandboxSpec{PluginID: "demo.plugin", TempRoot: t.TempDir()}
	owners := filepath.Join(spec.TempRoot, sandboxOwnerSubdir)
	if err := os.MkdirAll(owners, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(owners, 0o777); err != nil {
		t.Fatal(err)
	}
	dir, notes, err := prepareSandboxTempDir(spec)
	if err != nil {
		t.Fatalf("untrusted owner dir must not fail startup: %v", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("temp dir missing: %v", statErr)
	}
	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "owner records disabled") || !strings.Contains(joined, "0700") {
		t.Fatalf("degradation must be reported, got %q", joined)
	}
}
