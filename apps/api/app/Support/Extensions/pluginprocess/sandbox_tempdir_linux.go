//go:build linux

package pluginprocess

// per-plugin TMPDIR 的归属判定与回收。
//
// 旧实现只看 24h mtime，两个方向都错：一个**长跑但不写临时文件**的插件，其目录
// mtime 一直不变，会被下一个插件的启动器删掉（新 CreateTemp / unix socket 直接
// 失败）；反过来，崩溃留下的目录要白占一整天。这里改成按**进程实例存活**判定：
//
//   - 启动器建目录时在**宿主侧**写一份 owner 记录（pid + /proc/<pid>/stat 的
//     starttime + boot_id）。starttime 与 boot_id 一起把「同一个 pid 号」钉到
//     「同一个进程实例」：pid 被复用后 starttime 必然不同，于是上一代的目录立刻
//     可回收，不会因为「这个号又活了」而留下垃圾。
//   - owner 记录**不能**放在插件自己的 TMPDIR 里：那个目录对插件可写（Landlock
//     只给它这一处写权限），插件可以写一个 `{"pid":1,...}` 把自己的目录永久钉成
//     「活着」，崩溃后谁也不回收。所以记录落在同级的宿主私有目录
//     （0700、属主校验、插件不可写），按目录名做键。
//   - 僵尸（state=Z/X）不算活着：它已经不持有任何临时文件，目录只是残留。
//   - 没有 owner 记录的目录（上一版留下的）退回「名字里的 pid + TTL」：pid 活着
//     只按**硬上限**保留（无法核实身份就不能无限期占用磁盘），死了立刻删，名字里
//     根本没有 pid 才等 24h TTL。
//   - 已核实的活实例**不受时间限制**，并顺手刷新目录 mtime：对「长跑插件」做时间
//     上限等于把本轮修掉的缺陷原样装回去。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// sandboxTempSubdir 是 per-plugin TMPDIR 的父目录（位于宿主临时目录下）。
	sandboxTempSubdir = "sforum-plugin-sandbox"
	// sandboxOwnerSubdir 是宿主侧 owner 记录的目录（与插件目录同级、插件不可写）。
	sandboxOwnerSubdir = "sforum-sandbox-owners"
	// sandboxTempTTL 只用于「无法归属到进程」的目录：能归属的按存活判定，不等 TTL。
	sandboxTempTTL = 24 * time.Hour
	// sandboxTempUnverifiedTTL 是「看起来活着但无法核实身份」的硬上限。
	//
	// 典型来源：上一版没有 owner 记录的目录，只能按名字里的 pid 判活；pid 可能已被
	// 无关进程复用。核实不了就不能无限期占盘，所以给一个远大于任何合理升级窗口的
	// 硬上限（7d）。已核实（starttime + boot_id 都对上）的实例不适用这条。
	sandboxTempUnverifiedTTL = 7 * 24 * time.Hour
	// sandboxTempOwnerFile 是目录里的旧版归属记录文件名（只读兼容，不再写入）。
	sandboxTempOwnerFile = ".sforum-owner"
	// sandboxTempOwnerVersion 让记录格式可演进：未知版本一律当作「无法归属」。
	sandboxTempOwnerVersion = 1
)

// sandboxTempOwner 把「这个目录属于哪个进程实例」写进宿主侧记录。
//
// StartTime 是 /proc/<pid>/stat 的第 22 字段（自 boot 起的 clock ticks），
// 与 BootID 一起构成「实例身份」，比单独 pid 强的地方正是 pid 复用。
type sandboxTempOwner struct {
	Version   int    `json:"version"`
	PID       int    `json:"pid"`
	StartTime uint64 `json:"start_time"`
	BootID    string `json:"boot_id,omitempty"`
}

// tempDirVerdict 是回收决策。
type tempDirVerdict int

const (
	// tempDirLive：已核实的活实例（starttime + boot_id 都对上），必须保留。
	tempDirLive tempDirVerdict = iota
	// tempDirLiveUnverified：看起来活着，但身份核实不了（旧目录只有 pid）。
	tempDirLiveUnverified
	// tempDirStale：归属进程已退出（含僵尸/pid 复用），可以立刻回收。
	tempDirStale
	// tempDirUnattributable：没有可用的归属信息，只能按 TTL 兜底。
	tempDirUnattributable
)

// sandboxTempPaths 是一次启动用到的两个根目录。
type sandboxTempPaths struct {
	// Root 是 per-plugin 目录的父目录（插件只能写自己的子目录）。
	Root string
	// Owners 是宿主侧 owner 记录的目录（插件不可写）。
	Owners string
}

// prepareSandboxTempDir 建 per-plugin 私有临时目录，并顺手回收遗留目录。
func prepareSandboxTempDir(spec SandboxSpec) (string, []string, error) {
	paths := sandboxTempPathsFor(spec)
	var notes []string
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return "", nil, fmt.Errorf("create sandbox temp root %s: %w", paths.Root, err)
	}
	// owner 目录的信任前提必须自己核：/tmp 下的目录可以被别人先建出来，
	// 那样 owner 记录就成了别人写的「存活证明」。
	ownersOK, ownersReason := ensurePrivateDir(paths.Owners)
	if !ownersOK {
		paths.Owners = ""
		// 静默降级是 F2 那类缺陷的根：记录不可用必须留在报告里。
		notes = append(notes, "sandbox temp owner records disabled: "+ownersReason)
	}
	pruneSandboxTempDirsAt(paths.Root, paths.Owners, defaultProcRoot, readProcBootID(defaultProcRoot), time.Now())
	dir := filepath.Join(paths.Root, fmt.Sprintf("%s-%d", sandboxNameSuffix(spec.PluginID), os.Getpid()))
	// pid 复用是真实存在的：上一代崩溃留下的目录必须先清掉，否则插件会看到别人的残留。
	_ = os.RemoveAll(dir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", notes, fmt.Errorf("create sandbox temp dir %s: %w", dir, err)
	}
	if paths.Owners == "" {
		return dir, notes, nil
	}
	// 归属记录必须和目录一起落地：exec 之后 pid 不变，所以这里记的就是插件进程本身。
	if err := writeSandboxTempOwner(paths.Owners, filepath.Base(dir), currentSandboxTempOwner()); err != nil {
		_ = os.RemoveAll(dir)
		return "", notes, fmt.Errorf("write sandbox temp owner for %s (%s): %w", dir, ownersReason, err)
	}
	return dir, notes, nil
}

// sandboxTempPathsFor 由 spec 推导两个根目录。
func sandboxTempPathsFor(spec SandboxSpec) sandboxTempPaths {
	base := firstNonEmpty(strings.TrimSpace(spec.TempRoot), os.TempDir())
	return sandboxTempPaths{
		Root:   filepath.Join(base, sandboxTempSubdir),
		Owners: filepath.Join(base, sandboxOwnerSubdir),
	}
}

// ensurePrivateDir 建出「只有当前用户可读写」的目录，并核验它确实是我们的。
//
// /tmp 是 world-writable：目录可能已被别人先建出来（甚至是指向别处的符号链接）。
// 核验失败时调用方必须降级（不写 owner / 不加锁），而不是继续用一个别人能写的目录。
func ensurePrivateDir(path string) (bool, string) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return false, fmt.Sprintf("create %s: %v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Sprintf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		return false, fmt.Sprintf("%s is not a directory", path)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return false, fmt.Sprintf("%s mode is %04o, want 0700", path, mode)
	}
	if owner, ok := fileOwnerUID(info); ok && owner != uint32(os.Geteuid()) {
		return false, fmt.Sprintf("%s is owned by uid %d, want %d", path, owner, os.Geteuid())
	}
	return true, ""
}

// fileOwnerUID 取目录属主；取不到时第二个返回值为 false（此时只靠 mode 判定）。
func fileOwnerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}

// currentSandboxTempOwner 描述「当前进程」这一代实例。
func currentSandboxTempOwner() sandboxTempOwner {
	owner := sandboxTempOwner{Version: sandboxTempOwnerVersion, PID: os.Getpid(), BootID: readProcBootID(defaultProcRoot)}
	if _, startTime, err := readProcStat(defaultProcRoot, owner.PID); err == nil {
		owner.StartTime = startTime
	}
	return owner
}

// sandboxTempOwnerPath 是 owner 记录在宿主目录里的路径：按目录名做键。
func sandboxTempOwnerPath(ownerDir, name string) string {
	return filepath.Join(ownerDir, name+".json")
}

func writeSandboxTempOwner(ownerDir, name string, owner sandboxTempOwner) error {
	payload, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	path := sandboxTempOwnerPath(ownerDir, name)
	// O_NOFOLLOW：记录文件同样不能被替换成符号链接（目录已核验，仍留一层）。
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// readSandboxTempOwner 读宿主侧记录；文件缺失/损坏/版本未知都算「没有记录」。
func readSandboxTempOwner(ownerDir, name string) (sandboxTempOwner, bool) {
	if ownerDir == "" {
		return sandboxTempOwner{}, false
	}
	body, err := os.ReadFile(sandboxTempOwnerPath(ownerDir, name))
	if err != nil {
		return sandboxTempOwner{}, false
	}
	var owner sandboxTempOwner
	if err := json.Unmarshal(body, &owner); err != nil {
		return sandboxTempOwner{}, false
	}
	if owner.Version != sandboxTempOwnerVersion || owner.PID <= 0 {
		return sandboxTempOwner{}, false
	}
	return owner, true
}

// defaultProcRoot 是真实进程表的挂载点；测试传自己的假 /proc。
const defaultProcRoot = "/proc"

// readProcStat 解析 /proc/<pid>/stat 的第 3 字段（状态）与第 22 字段（starttime）。
//
// comm 里可以出现空格与右括号，所以按**最后一个** ')' 切分，而不是按空格切。
func readProcStat(procRoot string, pid int) (state byte, startTime uint64, err error) {
	body, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, err
	}
	close := strings.LastIndexByte(string(body), ')')
	if close < 0 || close+2 > len(body) {
		return 0, 0, fmt.Errorf("malformed stat for pid %d", pid)
	}
	fields := strings.Fields(string(body)[close+1:])
	// fields[0] = state(3)，fields[19] = starttime(22)。
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, 0, fmt.Errorf("malformed stat for pid %d", pid)
	}
	startTime, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("malformed starttime for pid %d: %w", pid, err)
	}
	return fields[0][0], startTime, nil
}

// readProcBootID 读本次开机的唯一标识；读不到就返回空串（此时只靠 starttime 判活）。
func readProcBootID(procRoot string) string {
	body, err := os.ReadFile(filepath.Join(procRoot, "sys", "kernel", "random", "boot_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// sandboxTempDirVerdict 判定一个遗留目录该不该回收。
//
// ownerDir 为空表示宿主侧记录不可用（目录不可信），此时退回旧版目录内记录 + 名字。
func sandboxTempDirVerdict(ownerDir, procRoot, bootID, dir, name string) tempDirVerdict {
	if owner, ok := readSandboxTempOwner(ownerDir, name); ok {
		return ownerVerdict(procRoot, bootID, owner)
	}
	// 刻意**不**读插件目录里的旧版 `.sforum-owner`：那个目录对插件可写，记录可以
	// 被伪造成「pid 1 还活着」。旧目录一律按名字里的 pid 判活（未核实 → 硬上限）。
	// 完全没有记录：pid 活着只能算「未核实」，死了立刻删。
	pid, ok := sandboxTempPIDFromName(name)
	if !ok {
		return tempDirUnattributable
	}
	if sandboxProcessAlive(procRoot, pid) {
		return tempDirLiveUnverified
	}
	return tempDirStale
}

func ownerVerdict(procRoot, bootID string, owner sandboxTempOwner) tempDirVerdict {
	state, startTime, err := readProcStat(procRoot, owner.PID)
	if err != nil || !procStateAlive(state) {
		return tempDirStale
	}
	// 记录里的 boot_id 与当前 boot 不同：这是上一次开机的号，pid 号再像也不是它。
	if owner.BootID != "" && bootID != "" && owner.BootID != bootID {
		return tempDirStale
	}
	if owner.StartTime == 0 {
		return tempDirLiveUnverified
	}
	// starttime 不同 = pid 已被别的进程复用。
	if owner.StartTime != startTime {
		return tempDirStale
	}
	return tempDirLive
}

// sandboxProcessAlive 只按 pid 判活（没有 starttime 可用）。
func sandboxProcessAlive(procRoot string, pid int) bool {
	state, _, err := readProcStat(procRoot, pid)
	if err != nil {
		return false
	}
	return procStateAlive(state)
}

// procStateAlive 报告进程状态是否还「持有资源」。
//
// Z（zombie）与 X（dead）都已释放文件描述符：目录只是残留，不该因为 /proc 里还
// 留着条目就永远不回收。
func procStateAlive(state byte) bool {
	return state != 'Z' && state != 'X'
}

// sandboxTempPIDFromName 从 `<slug>-<pid>` 里取 pid；取不到就交给 TTL。
func sandboxTempPIDFromName(name string) (int, bool) {
	index := strings.LastIndexByte(name, '-')
	if index < 0 || index == len(name)-1 {
		return 0, false
	}
	pid, err := strconv.Atoi(name[index+1:])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// pruneSandboxTempDirsAt 是回收策略本体，参数化 /proc、boot_id 与「现在」，便于离线取证。
func pruneSandboxTempDirsAt(root, ownerDir, procRoot, bootID string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	live := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		switch sandboxTempDirVerdict(ownerDir, procRoot, bootID, dir, entry.Name()) {
		case tempDirLive:
			// 已核实的活实例：刷新 mtime，避免下一次回收把它当成「陈旧」。
			_ = os.Chtimes(dir, now, now)
			live[entry.Name()] = struct{}{}
			continue
		case tempDirLiveUnverified:
			// 身份核实不了：只给硬上限，不给无限期占盘。
			if !info.ModTime().Before(now.Add(-sandboxTempUnverifiedTTL)) {
				live[entry.Name()] = struct{}{}
				continue
			}
		case tempDirUnattributable:
			// 无法归属的目录按 TTL 兜底：不能因为「猜不出来」就删掉一个可能活着的目录。
			if !info.ModTime().Before(now.Add(-sandboxTempTTL)) {
				live[entry.Name()] = struct{}{}
				continue
			}
		}
		_ = os.RemoveAll(dir)
	}
	pruneOrphanOwnerRecords(ownerDir, live)
}

// pruneOrphanOwnerRecords 清掉没有对应目录的 owner 记录（插件自己删了 TMPDIR 等）。
func pruneOrphanOwnerRecords(ownerDir string, live map[string]struct{}) {
	if ownerDir == "" {
		return
	}
	entries, err := os.ReadDir(ownerDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if _, ok := live[name]; ok {
			continue
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(ownerDir), sandboxTempSubdir, name)); err == nil {
			continue
		}
		_ = os.Remove(filepath.Join(ownerDir, entry.Name()))
	}
}
