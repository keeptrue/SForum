//go:build linux

package pluginprocess

// cgroup v2 是这里唯一能「按插件」限制资源的内核机制；rlimit 做不到（它按 real uid
// 计数，同 uid 的所有插件共享一份预算）。
//
// 本机实测的委派模型（不要重猜）：
//   - 本进程所在 scope 的 cgroup.controllers 是 "memory pids"，但 subtree_control
//     为空：往有进程的 cgroup 里开启控制器会得到 EBUSY（no-internal-process 规则）。
//     所以「在我们自己的 scope 下建子组」拿不到 memory.max / pids.max；
//   - 最近的祖先 app.slice 已由 systemd 把 memory+pids 放进 subtree_control，且自身
//     没有进程、属主是当前用户：在它下面建组可以真正写入并生效（实测
//     memory.max=67108864、pids.max=16 均读回一致，进程也被搬进去了）。
//
// 因此建组策略是「就近找已经委派好控制器的祖先」：先试自己的 cgroup（能就地开启
// 控制器时最自然），再自下而上找第一个已经委派好控制器的祖先。找不到就降级并给出
// 结构化原因——限额配置不能变成可用性故障。

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	cgroupMountPoint    = "/sys/fs/cgroup"
	cgroupPluginPrefix  = "sforum-plugin-"
	cgroupMaxNameLength = 200
	// cgroupLockSubdir 是宿主侧建组互斥锁的目录（cgroupfs 里不能建普通文件）。
	cgroupLockSubdir = "sforum-cgroup-locks"
	// cgroupLockWaitBudget 是等锁的上限：cgroupfs 只有 mkdir/rmdir，建组本身是
	// 微秒级动作；等不到就说明有人持锁不放（本地用户预建锁文件、或对端卡死），
	// 继续阻塞等于让插件启动被永久挂住——降级为「不加锁」比挂住安全。
	cgroupLockWaitBudget = 2 * time.Second
	// cgroupLockRetryInterval 是 LOCK_NB 的重试间隔。
	cgroupLockRetryInterval = 20 * time.Millisecond
)

// requiredCgroupControllers 是限额真正生效所需的最小控制器集合。
var requiredCgroupControllers = []string{"memory", "pids"}

// cgroupOutcome 是 cgroup 这一层的能力结论。
type cgroupOutcome struct {
	Status string
	Path   string
	Reason string
	Limits map[string]string
	// Notes 是不影响三态的可诊断信息（落点偏移、锁不可用等），进报告 notes。
	Notes []string
}

// applySandboxCgroup 建立 per-plugin cgroup 并把当前进程（= 即将 exec 的插件）
// 搬进去。任何一步失败都只降级，不拒绝启动。
func applySandboxCgroup(spec SandboxSpec) cgroupOutcome {
	outcome := cgroupOutcome{Status: StatusDisabled}
	if !spec.Policy.Cgroup {
		outcome.Reason = "cgroup policy disabled"
		return outcome
	}
	parent, _, reason := cgroupParent(spec)
	if parent == "" {
		outcome.Status = StatusDegraded
		outcome.Reason = reason
		return outcome
	}
	if note := cgroupScopeNote(parent); note != "" {
		outcome.Notes = append(outcome.Notes, note)
	}
	// 建组序列必须跨进程串行：两个实例同时 rmdir 一个空组再 mkdir，会互相删掉对方
	// 刚建好的组（见 createPluginCgroup）。锁拿不到只降级为「尽力而为」，不拒绝启动。
	unlock, lockErr := lockPluginCgroupCreation(spec, parent, pluginCgroupBaseName(spec.PluginID))
	if lockErr != nil {
		outcome.Notes = append(outcome.Notes, "cgroup create lock unavailable: "+lockErr.Error())
	} else {
		defer unlock()
	}
	target, err := createPluginCgroup(parent, spec.PluginID)
	if err != nil {
		outcome.Status = StatusDegraded
		outcome.Reason = fmt.Sprintf("create cgroup under %s: %v", parent, err)
		return outcome
	}
	limits, err := writeCgroupLimits(target, spec.MemoryMax, spec.PidsMax)
	if err != nil {
		removeCgroup(target)
		outcome.Status = StatusDegraded
		outcome.Reason = err.Error()
		return outcome
	}
	if err := enterCgroup(target); err != nil {
		removeCgroup(target)
		outcome.Status = StatusDegraded
		outcome.Reason = err.Error()
		return outcome
	}
	if spec.CgroupRootOverride == "" {
		if err := verifyCgroupPlacement(target); err != nil {
			outcome.Status = StatusDegraded
			outcome.Reason = err.Error()
			outcome.Path = target
			return outcome
		}
	}
	outcome.Status = StatusEnforced
	outcome.Path = target
	outcome.Limits = limits
	return outcome
}

// cgroupParent 选定建组的父 cgroup。
//
// 返回值：父组路径、当前进程自己的 cgroup（服务 scope；用于报告落点偏移）、
// 以及父组为空时给人看的结构化原因（会进审计事件）。
func cgroupParent(spec SandboxSpec) (parent, own, reason string) {
	if override := strings.TrimSpace(spec.CgroupRootOverride); override != "" {
		if delegated, reason := cgroupDelegates(override, true); !delegated {
			return "", "", reason
		}
		return override, "", ""
	}
	mount, err := cgroup2MountPoint()
	if err != nil {
		return "", "", err.Error()
	}
	relative, err := ownCgroupRelativePath()
	if err != nil {
		return "", "", err.Error()
	}
	return cgroupParentUnder(mount, relative)
}

// cgroupParentUnder 是父组选择策略本体：mount 与相对路径注入，便于离线取证。
//
// 策略是「就地优先、就近兜底」：先试自己的 cgroup（systemd unit 声明 Delegate=yes
// 时它已经委派好控制器，per-plugin 组因此落在**服务 scope** 内），再自下而上找
// 第一个已委派好的祖先（多数机器上是 app.slice），都没有就降级。
func cgroupParentUnder(mount, relative string) (parent, own, reason string) {
	own = filepath.Join(mount, relative)
	delegated, ownReason := cgroupDelegates(own, true)
	if delegated {
		return own, own, ""
	}
	// 自己的 cgroup 拿不到控制器时，自下而上找第一个「已经由 init 系统委派好」的
	// 祖先（systemd 的 Delegate=yes 模型就是这样把 memory/pids 放进 subtree_control）。
	for dir := filepath.Dir(own); dir != mount && strings.HasPrefix(dir, mount+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if delegated, _ := cgroupDelegates(dir, false); delegated {
			return dir, own, ""
		}
	}
	if ownReason == "" {
		ownReason = "no delegated cgroup ancestor provides " + strings.Join(requiredCgroupControllers, ",")
	}
	return "", own, ownReason
}

// cgroupScopeNote 在组落到「共享 slice」时给出可执行的运维提示。
//
// 判定基准不能是「自己的 cgroup」：声明了 DelegateSubgroup= 之后，进程自己的
// cgroup 是 `<unit>/<subgroup>`，而建组落在 `<unit>`——两者不相等，但完全正确。
// 真正的偏移特征是落点是 systemd 的共享 slice（system.slice / app.slice /
// user.slice），即「不在任何 unit 内部」，那才是运维看不见按服务统计的地方。
func cgroupScopeNote(parent string) string {
	if parent == "" || !isSystemdSlice(parent) {
		return ""
	}
	return fmt.Sprintf(
		"cgroup lands in the shared slice %s, outside the API unit; declare Delegate=yes and DelegateSubgroup=<name> on the systemd unit (systemd >= 254) so per-plugin groups stay inside the service scope",
		parent,
	)
}

// isSystemdSlice 报告目录是否是 systemd 的 slice（unit 的容器），而不是 unit 自己。
func isSystemdSlice(dir string) bool {
	return strings.HasSuffix(filepath.Base(dir), ".slice")
}

// lockPluginCgroupCreation 取一把宿主级互斥锁，串行化「同插件、同父组」的建组序列。
//
// 为什么需要锁：cgroupfs 只有 mkdir/rmdir，没有 rename，也没有「建或复用」的原子
// 原语。两个实例同时启动时，A 刚 mkdir 出一个空组、还没写限额，B 的 rmdir 就能把
// 它删掉（空组可删），于是 A 的 writeCgroupLimits 拿到 ENOENT，白降级一次。锁把
// 「rmdir → mkdir → 写限额 → 搬进程」变成临界区：B 只会在 A 已经把进程搬进去之后
// 才尝试 rmdir，那时 EBUSY，B 自然退到带序号的备用名字。
//
// 锁文件放在宿主临时目录而不是 cgroupfs（kernfs 不支持普通文件），按
// parent+pluginID 分片，只有同插件实例互斥。
func lockPluginCgroupCreation(spec SandboxSpec, parent, cgroupName string) (func(), error) {
	root := filepath.Join(firstNonEmpty(strings.TrimSpace(spec.TempRoot), os.TempDir()), cgroupLockSubdir)
	// /tmp 是 world-writable：锁目录可能被别人先建出来（甚至是指向别处的链接）。
	// 核验失败时调用方降级为「不加锁」，绝不阻塞在别人的目录上。
	if ok, reason := ensurePrivateDir(root); !ok {
		return nil, fmt.Errorf("cgroup lock dir untrusted: %s", reason)
	}
	path := pluginCgroupLockPath(root, parent, cgroupName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open cgroup lock: %w", err)
	}
	deadline := time.Now().Add(cgroupLockWaitBudget)
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("flock cgroup lock: %w", err)
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out after %s waiting for %s", cgroupLockWaitBudget, path)
		}
		time.Sleep(cgroupLockRetryInterval)
	}
}

// pluginCgroupLockPath 是「同父组 + 同 cgroup 名」的锁文件路径。
//
// 分片键用**解析后的 cgroup 名**：两个 pluginID 若在 slug+digest 上撞名，它们要抢的
// 正是同一个目录，锁键必须跟着目录名走，否则同名不同锁等于没锁。
func pluginCgroupLockPath(lockRoot, parent, cgroupName string) string {
	digest := sha256.Sum256([]byte(parent + "\x00" + cgroupName))
	return filepath.Join(lockRoot, hex.EncodeToString(digest[:8])+".lock")
}

// cgroupDelegates 报告 dir 是否能给子组提供所需控制器。
//
// mayEnable 只允许对自己的 cgroup 使用：往祖先的 subtree_control 里开启控制器是
// 一次影响整个层级的动作，机器上已经有 systemd 在管，不该由应用代劳。
func cgroupDelegates(dir string, mayEnable bool) (bool, string) {
	available, err := readCgroupList(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		return false, fmt.Sprintf("%s: %v", dir, err)
	}
	for _, controller := range requiredCgroupControllers {
		if !available[controller] {
			return false, fmt.Sprintf("%s does not expose controller %q", dir, controller)
		}
	}
	enabled, err := readCgroupList(filepath.Join(dir, "cgroup.subtree_control"))
	if err != nil {
		return false, fmt.Sprintf("%s: %v", dir, err)
	}
	missing := make([]string, 0, len(requiredCgroupControllers))
	for _, controller := range requiredCgroupControllers {
		if !enabled[controller] {
			missing = append(missing, "+"+controller)
		}
	}
	if len(missing) == 0 {
		return true, ""
	}
	if !mayEnable {
		return false, fmt.Sprintf("%s does not delegate %s", dir, strings.Join(missing, " "))
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte(strings.Join(missing, " ")), 0); err != nil {
		// 最常见的失败就是 EBUSY：该 cgroup 自己还有进程（no-internal-process）。
		return false, fmt.Sprintf("%s cannot delegate %s: %v", dir, strings.Join(missing, " "), err)
	}
	return true, ""
}

// createPluginCgroup 在 parent 下建一个 per-plugin cgroup。
//
// 名字按插件 id 稳定生成，因此上一次（已退出）的组会在这次被 rmdir 回收；如果上
// 一代实例还活着（升级重叠窗口），rmdir 会 EBUSY，于是退到带序号的备用名字，两个
// 实例各自拥有独立限额。
func createPluginCgroup(parent, pluginID string) (string, error) {
	base := filepath.Join(parent, pluginCgroupBaseName(pluginID))
	for attempt := 0; attempt < 4; attempt++ {
		candidate := base
		if attempt > 0 {
			candidate = fmt.Sprintf("%s-%d", base, attempt+1)
		}
		err := os.Mkdir(candidate, 0o755)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		// 上一代的空组在这里被回收；有进程（EBUSY/ENOTEMPTY）就换名字。
		if removeErr := os.Remove(candidate); removeErr == nil {
			if err := os.Mkdir(candidate, 0o755); err == nil {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("refusing to reuse %s: previous generations are still populated", base)
}

// pluginCgroupBaseName 是 per-plugin 组的稳定基名（不含 parent）。
//
// 锁分片也用它：锁必须保护「同一个目录名」，而不是「同一个 pluginID」。
func pluginCgroupBaseName(pluginID string) string {
	return cgroupPluginPrefix + sandboxNameSuffix(pluginID)
}

// sandboxNameSuffix 生成可读且稳定的 cgroup 名字后缀。
func sandboxNameSuffix(pluginID string) string {
	slug := strings.Builder{}
	for _, char := range strings.ToLower(pluginID) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '.', char == '-', char == '_':
			slug.WriteRune(char)
		default:
			slug.WriteByte('-')
		}
		if slug.Len() >= 24 {
			break
		}
	}
	digest := sha256.Sum256([]byte(pluginID))
	name := strings.Trim(slug.String(), ".-")
	if name == "" {
		name = "plugin"
	}
	name = name + "-" + hex.EncodeToString(digest[:4])
	if len(name) > cgroupMaxNameLength {
		name = name[:cgroupMaxNameLength]
	}
	return name
}

// writeCgroupLimits 写入上限并读回实际值。
//
// 读回是刻意的：内核会把 "512M" 规范化成字节数，报告里给出的是**内核看到的**
// 值，而不是我们以为写入的值。
func writeCgroupLimits(dir, memoryMax, pidsMax string) (map[string]string, error) {
	limits := map[string]string{}
	for _, limit := range []struct {
		file  string
		value string
		label string
	}{
		{file: "memory.max", value: memoryMax, label: "memory.max"},
		{file: "pids.max", value: pidsMax, label: "pids.max"},
	} {
		value, ok := parseLimitValue(limit.value)
		if !ok {
			return nil, fmt.Errorf("invalid %s value %q", limit.label, limit.value)
		}
		path := filepath.Join(dir, limit.file)
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %v", path, err)
		}
		if actual, err := os.ReadFile(path); err == nil {
			limits[limit.label] = strings.TrimSpace(string(actual))
		}
	}
	// swap 关掉才能让 memory.max 成为真正的上限（否则可换出逃逸）。
	if path := filepath.Join(dir, "memory.swap.max"); fileExists(path) {
		if err := os.WriteFile(path, []byte("0"), 0o644); err == nil {
			limits["memory.swap.max"] = "0"
		}
	}
	return limits, nil
}

// enterCgroup 把当前进程搬进 dir。
//
// 写 "0" 到 cgroup.procs 等价于写自己的 pid；必须在写入限额之后、exec 之前完成，
// 这样插件从第一条指令起就在限额内。
func enterCgroup(dir string) error {
	path := filepath.Join(dir, "cgroup.procs")
	if err := os.WriteFile(path, []byte("0"), 0o644); err != nil {
		return fmt.Errorf("enter cgroup %s: %v", dir, err)
	}
	return nil
}

// verifyCgroupPlacement 用 /proc/self/cgroup 复核「真的搬进去了」。
func verifyCgroupPlacement(dir string) error {
	relative, err := ownCgroupRelativePath()
	if err != nil {
		return err
	}
	mount, err := cgroup2MountPoint()
	if err != nil {
		return err
	}
	if own := filepath.Join(mount, relative); own != dir {
		return fmt.Errorf("cgroup placement drifted: /proc/self/cgroup reports %s, want %s", own, dir)
	}
	return nil
}

func removeCgroup(dir string) {
	if dir == "" {
		return
	}
	_ = os.Remove(dir)
}

// cgroup2MountPoint 从 /proc/self/mountinfo 找出 cgroup2 的挂载点。
func cgroup2MountPoint() (string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("read mountinfo: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 0 || separator+1 >= len(fields) || fields[separator+1] != "cgroup2" {
			continue
		}
		return unescapeMountInfo(fields[4]), nil
	}
	return "", errors.New("no cgroup2 mount found (unified hierarchy is required)")
}

// unescapeMountInfo 还原 mountinfo 里 \040 之类的转义。
func unescapeMountInfo(raw string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(raw)
}

// ownCgroupRelativePath 读取当前进程在统一层级里的相对路径（"0::/x/y" 形式）。
func ownCgroupRelativePath() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "0::") {
			return line[3:], nil
		}
	}
	return "", errors.New("cgroup v2 (0::) entry is missing from /proc/self/cgroup")
}

type cgroupList map[string]bool

// readCgroupList 读取 "memory pids" 这类控制器列表文件。
func readCgroupList(path string) (cgroupList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	list := cgroupList{}
	for _, field := range strings.Fields(string(data)) {
		list[field] = true
	}
	return list, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// cgroupProcs 读取某个 cgroup 的进程列表（测试与取证用）。
func cgroupProcs(dir string) ([]int, error) {
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		var pid int
		if _, err := fmt.Sscanf(field, "%d", &pid); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}
