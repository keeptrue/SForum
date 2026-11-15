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
)

const (
	cgroupMountPoint    = "/sys/fs/cgroup"
	cgroupPluginPrefix  = "sforum-plugin-"
	cgroupMaxNameLength = 200
)

// requiredCgroupControllers 是限额真正生效所需的最小控制器集合。
var requiredCgroupControllers = []string{"memory", "pids"}

// cgroupOutcome 是 cgroup 这一层的能力结论。
type cgroupOutcome struct {
	Status string
	Path   string
	Reason string
	Limits map[string]string
}

// applySandboxCgroup 建立 per-plugin cgroup 并把当前进程（= 即将 exec 的插件）
// 搬进去。任何一步失败都只降级，不拒绝启动。
func applySandboxCgroup(spec SandboxSpec) cgroupOutcome {
	outcome := cgroupOutcome{Status: StatusDisabled}
	if !spec.Policy.Cgroup {
		outcome.Reason = "cgroup policy disabled"
		return outcome
	}
	parent, reason := cgroupParent(spec)
	if parent == "" {
		outcome.Status = StatusDegraded
		outcome.Reason = reason
		return outcome
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
// 返回空字符串时第二个返回值是给人看的结构化原因（会进审计事件）。
func cgroupParent(spec SandboxSpec) (string, string) {
	if override := strings.TrimSpace(spec.CgroupRootOverride); override != "" {
		if delegated, reason := cgroupDelegates(override, true); !delegated {
			return "", reason
		}
		return override, ""
	}
	mount, err := cgroup2MountPoint()
	if err != nil {
		return "", err.Error()
	}
	relative, err := ownCgroupRelativePath()
	if err != nil {
		return "", err.Error()
	}
	own := filepath.Join(mount, relative)
	delegated, ownReason := cgroupDelegates(own, true)
	if delegated {
		return own, ""
	}
	// 自己的 cgroup 拿不到控制器时，自下而上找第一个「已经由 init 系统委派好」的
	// 祖先（systemd 的 Delegate=yes 模型就是这样把 memory/pids 放进 subtree_control）。
	for dir := filepath.Dir(own); dir != mount && strings.HasPrefix(dir, mount+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if delegated, _ := cgroupDelegates(dir, false); delegated {
			return dir, ""
		}
	}
	if ownReason == "" {
		ownReason = "no delegated cgroup ancestor provides " + strings.Join(requiredCgroupControllers, ",")
	}
	return "", ownReason
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
	base := filepath.Join(parent, cgroupPluginPrefix+sandboxNameSuffix(pluginID))
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
