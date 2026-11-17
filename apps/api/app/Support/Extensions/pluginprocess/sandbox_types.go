package pluginprocess

// 本文件是「插件子进程内核级隔离」的契约层：配置（operator 开关）、启动载荷（spec）、
// 与可取证的能力报告（Report）。平台实现分别在 sandbox_linux.go / sandbox_other.go。
//
// 设计前提（2026-11-15 实测，不要重猜）：
//   - 本机内核支持 Landlock（ABI 8）与 cgroup v2；
//   - mount/net/user namespace 在本机是 EPERM（AppArmor），所以隔离只能走
//     Landlock + cgroup，不能要求 namespace 作为必需路径；
//   - 能力缺失时必须「降级 + 结构化原因」，绝不拒绝启动：限额/隔离配置不能变成
//     新的可用性故障点。
//
// 配置只由宿主解析后放进 spec：插件自己的 env 里不出现这些键，因此插件无法自行
// 放宽自己的沙箱。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// LauncherArgv0 是沙箱启动器的保留 argv[0]。
//
// 宿主用 os.Executable() 重新 exec 自己，并把 argv 重排成
// {LauncherArgv0, specJSON, pluginArgv...}；cmd/api 与 cmd/worker 的 main 在
// 第一条语句拦截该 argv 并调用 RunLauncher。
//
// 用 argv[0] 而不是环境变量做标记：env 会被插件完整继承，标记落在 argv 上则
// exec 真实插件时自然消失，插件看不到也不可篡改。
const LauncherArgv0 = "__sforum_plugin_sandbox_exec"

// 宿主配置键（只从宿主进程环境读取，不进插件 env 白名单）。
const (
	// SandboxEnv 是总开关：off 关闭本轮全部内核级加固（含 cgroup 与 Landlock）。
	SandboxEnv = "SFORUM_PLUGIN_SANDBOX"
	// SandboxFilesystemEnv 关闭文件系统 Landlock 规则（网络/ cgroup 不受影响）。
	SandboxFilesystemEnv = "SFORUM_PLUGIN_SANDBOX_FS"
	// SandboxNetworkEnv 取 off|bind|all，缺省 off（见 NetworkPolicy 的说明）。
	SandboxNetworkEnv = "SFORUM_PLUGIN_SANDBOX_NETWORK"
	// SandboxCgroupEnv 关闭 per-plugin cgroup 建组与限额。
	SandboxCgroupEnv = "SFORUM_PLUGIN_SANDBOX_CGROUP"
	// SandboxExtraReadOnlyEnv 是冒号分隔的额外只读路径（第三方插件的逃生口）。
	SandboxExtraReadOnlyEnv = "SFORUM_PLUGIN_SANDBOX_EXTRA_RO"
	// SandboxExtraExecEnv 是冒号分隔的额外「可读 + 可执行」目录：插件确实需要
	// exec 外部工具（例如 ffmpeg）时由运维显式放开，默认不给。
	SandboxExtraExecEnv = "SFORUM_PLUGIN_SANDBOX_EXTRA_EXEC"
	// SandboxExtraReadWriteEnv 是冒号分隔的额外「可读写」目录：插件确实要把数据
	// 落到自己的目录（例如 sforum-storage-fs 的对象根）时由运维显式放开。
	SandboxExtraReadWriteEnv = "SFORUM_PLUGIN_SANDBOX_EXTRA_RW"
	// SandboxMemoryMaxEnv / SandboxPidsMaxEnv 是 cgroup 限额，缺省 512M / 512。
	SandboxMemoryMaxEnv = "SFORUM_PLUGIN_MEMORY_MAX"
	SandboxPidsMaxEnv   = "SFORUM_PLUGIN_PIDS_MAX"
)

const (
	defaultMemoryMax = "512M"
	defaultPidsMax   = "512"
)

// 能力状态。三态是刻意的：disabled（策略关闭）与 degraded（内核/委派不支持）必须
// 在取证与日志里可区分，否则「没有沙箱」和「沙箱降级」会混为一谈。
const (
	StatusEnforced = "enforced"
	StatusDegraded = "degraded"
	StatusDisabled = "disabled"
)

// NetworkPolicy 是 Landlock 网络规则策略。
//
// 缺省是 off 而不是 deny，这是一个有证据的产品判断：SForum 的插件既需要
// **监听** loopback TCP（RouteTarget 契约要求插件自报 loopback http 地址，见
// validatePluginRouteTarget），也需要**出站**连接（内置的 smtp / storage-s3 /
// auth-github 三个插件都直接 dial 外部服务）。默认 deny 会让内置插件集体失效，
// 而本轮还没有「插件声明网络能力」的清单（decision 记录里列为 seccomp 的前置条件）。
//
// 因此机制完整、默认可取证地关闭，按部署意图显式开启：
//   - off：不加任何网络规则（现状）；
//   - bind：拒绝入站 TCP listen（适用于只走 RPC、不提供 HTTP RouteTarget 的插件集）；
//   - all：拒绝入站与出站 TCP（脱网/最大限制部署）。
type NetworkPolicy string

const (
	NetworkOff  NetworkPolicy = "off"
	NetworkBind NetworkPolicy = "bind"
	NetworkAll  NetworkPolicy = "all"
)

// SandboxOptions 是宿主从自身环境解析出的操作者配置。
type SandboxOptions struct {
	// Enabled 为 false 时宿主完全不改 exec 路径（直接 exec 插件）。
	Enabled bool
	// Filesystem 控制 Landlock 文件系统规则。
	Filesystem bool
	// Network 控制 Landlock 网络规则。
	Network NetworkPolicy
	// Cgroup 控制 per-plugin cgroup 建组与 memory/pids 限额。
	Cgroup bool
	// MemoryMax / PidsMax 是 cgroup 限额的原始文本（如 "512M" / "512"）。
	MemoryMax string
	PidsMax   string
	// ExtraReadOnly 是追加的只读路径。
	ExtraReadOnly []string
	// ExtraExec 是追加的「只读 + 可执行」目录。
	ExtraExec []string
	// ExtraReadWrite 是追加的「可读写」目录。
	ExtraReadWrite []string
}

// SandboxOptionsFromEnv 解析操作者配置。未知值一律取更严格的一侧：
// 拼错的文件系统开关不会静默关掉沙箱，拼错的网络策略不会静默放开网络。
func SandboxOptionsFromEnv(getenv func(string) string) SandboxOptions {
	options := SandboxOptions{
		Enabled:    !envOff(getenv(SandboxEnv), true),
		Filesystem: !envOff(getenv(SandboxFilesystemEnv), true),
		Network:    parseNetworkPolicy(getenv(SandboxNetworkEnv)),
		Cgroup:     !envOff(getenv(SandboxCgroupEnv), true),
		MemoryMax:  firstNonEmpty(strings.TrimSpace(getenv(SandboxMemoryMaxEnv)), defaultMemoryMax),
		PidsMax:    firstNonEmpty(strings.TrimSpace(getenv(SandboxPidsMaxEnv)), defaultPidsMax),
	}
	options.ExtraReadOnly = splitPathList(getenv(SandboxExtraReadOnlyEnv))
	options.ExtraExec = splitPathList(getenv(SandboxExtraExecEnv))
	options.ExtraReadWrite = sandboxWritablePaths(splitPathList(getenv(SandboxExtraReadWriteEnv)))
	return options
}

// envOff 报告某个开关是否被显式关闭；off=false 表示「默认开启」。
// 只有明确的关闭写法才算关闭：其余任何取值（含拼错）都保持开启。
func envOff(raw string, whenEmpty bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return !whenEmpty
	case "0", "off", "false", "no", "disabled":
		return true
	default:
		return false
	}
}

// parseNetworkPolicy 解析网络策略；任何无法识别的非空取值都落到最严格的 all。
func parseNetworkPolicy(raw string) NetworkPolicy {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "off", "none", "allow":
		return NetworkOff
	case "bind", "listen":
		return NetworkBind
	case "all", "deny":
		return NetworkAll
	default:
		return NetworkAll
	}
}

func splitPathList(raw string) []string {
	var paths []string
	for _, entry := range strings.Split(raw, ":") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// SandboxWritableSettingPaths 从插件 env 里挑出「运维为该插件配置的绝对路径设置值」，
// 作为文件系统的可写面。
//
// 威胁模型要说清楚：这不是「按插件名/键名开洞」，而是把**运维通过设置项显式交给这个
// 插件的数据目录**变成可写面。值来自宿主设置表（经 SFORUM_SETTING_* 注入），插件
// 自己无法改写；它不授权任何「插件自选路径」。因此规则是通用的：不识别插件名，
// 也不识别键名（root_path / data_dir / 未来的键一视同仁）。
//
// 过滤是为了不把非路径设置（public_base_url、阈值、开关）误当成目录：必须是绝对
// 路径、不能是根目录、不能含 `..` 路径段、不能为空。
func SandboxWritableSettingPaths(env []string) []string {
	seen := make(map[string]bool, 4)
	var paths []string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "SFORUM_SETTING_") {
			continue
		}
		path, ok := sandboxWritableSettingPath(value)
		if !ok || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// sandboxWritablePaths 让 EXTRA_RW 与「设置项绝对路径」走同一份收敛规则：
// 两个消费者看到同一结果，不会出现「env 放行了、Landlock 却 stat 失败」的分叉。
func sandboxWritablePaths(candidates []string) []string {
	var paths []string
	for _, candidate := range candidates {
		if path, ok := sandboxWritableSettingPath(candidate); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

// sandboxWritableSettingPath 判定一个「候选可写目录」，并返回规范化后的路径。
//
// 这是启发式规则，不是权限模型：它要回答的是「这个值像不像一个数据目录」。
// 四条拒绝各自对应一种真实误判：
//   - `://`：URL 形态（`public_base_url`）在 Unix 下也能通过 IsAbs；
//   - 深度 < 2：`/files` 这种一级路径更像 URL 里的 path，而不是运维给的数据根；
//   - 已存在且不是目录：只读用途的文件设置（CA bundle、证书）会被误升格成可写；
//   - `..` 路径段 / 根目录 / 非绝对：路径穿越与「整盘可写」。
//
// stat 失败（不存在）时放行：首次创建数据目录是预期路径，启动器会 MkdirAll 出来。
// 代价：单组件数据根（例如 `/data`）两种入口都不接受，需要下钻一层（`/data/sforum`）。
func sandboxWritableSettingPath(raw string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" || !filepath.IsAbs(path) {
		return "", false
	}
	if strings.Contains(path, "://") {
		return "", false
	}
	// 双斜杠：POSIX 上等价于单斜杠，但它也是 URL 被冒号切碎后的残骸
	// （`EXTRA_RW=https://cdn/x` 会切成 `https` + `//cdn/x`，后者能过 IsAbs）。
	if strings.HasPrefix(path, "//") {
		return "", false
	}
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment == ".." {
			return "", false
		}
	}
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) || writablePathDepth(cleaned) < 2 {
		return "", false
	}
	if info, err := os.Stat(cleaned); err == nil && !info.IsDir() {
		return "", false
	}
	return cleaned, true
}

// writablePathDepth 数一条绝对路径上的有效组件数（跳过空段与 `.`）。
func writablePathDepth(path string) int {
	depth := 0
	for _, segment := range strings.Split(path, string(filepath.Separator)) {
		if segment != "" && segment != "." {
			depth++
		}
	}
	return depth
}

// SandboxPolicy 是真正下发给启动器的策略（已固化，不再读环境）。
type SandboxPolicy struct {
	Filesystem bool          `json:"filesystem"`
	Network    NetworkPolicy `json:"network"`
	Cgroup     bool          `json:"cgroup"`
}

// SandboxSpec 是宿主交给启动器的完整启动载荷，经 argv[1] 以 JSON 传递。
//
// 它必须是自包含的：启动器在 exec 真实插件之前不读宿主配置，也不与宿主通信。
type SandboxSpec struct {
	PluginID      string `json:"plugin_id"`
	PackageDigest string `json:"package_digest,omitempty"`
	// Entry 是 exec 目标。生产路径是 /proc/self/fd/3（已校验 inode，RV-53）。
	Entry string `json:"entry"`
	// Argv0 是真实插件进程看到的 argv[0]。
	//
	// 它不是纯展示：宿主只能靠进程表认领自己的插件子进程（AdminOverview 的
	// 「插件占用 N 个进程」、扩展列表的 RSS 归因都按
	// storage/extensions/<id>/<ver>/<digest>/backend/plugin 这类绝对路径匹配，
	// 见 processmemory.IsBackendPluginCommand）。因此这里必须填**绝对制品入口
	// 路径**，填清单里的相对入口（backend/plugin）会让采样器丢掉整个插件进程表。
	Argv0 string `json:"argv0,omitempty"`
	// ArtifactDir 是制品目录：允许读 + 执行。
	ArtifactDir string `json:"artifact_dir"`
	// PackageRoot 是包内容根：允许读（插件读自己的包内资源），不允许执行。
	PackageRoot string `json:"package_root,omitempty"`
	// ExtraReadOnly 是操作者追加的只读路径。
	ExtraReadOnly []string `json:"extra_read_only,omitempty"`
	// ExtraExec 是操作者追加的「只读 + 可执行」目录。
	ExtraExec []string `json:"extra_exec,omitempty"`
	// ExtraReadWrite 是「可读写」目录：运维显式配置的插件数据目录。
	ExtraReadWrite []string `json:"extra_read_write,omitempty"`
	// TempRoot 是宿主临时目录；启动器在其下建 per-plugin TMPDIR。
	TempRoot string `json:"temp_root,omitempty"`

	Policy    SandboxPolicy `json:"policy"`
	MemoryMax string        `json:"memory_max,omitempty"`
	PidsMax   string        `json:"pids_max,omitempty"`

	// ReportFD 是能力报告的写端 fd（宿主经 ExtraFiles 传入）。
	ReportFD int `json:"report_fd,omitempty"`

	// 以下两个字段是测试注入缝，宿主接线永不设置。
	//
	// LandlockABIOverride 模拟「Landlock 不可用」，用来证明降级路径不会打断启动；
	// CgroupRootOverride 把 cgroup 建组指向一个可写的委派根，用来在没有 systemd
	// 委派的环境里验证限额写入。
	LandlockABIOverride *int   `json:"landlock_abi_override,omitempty"`
	CgroupRootOverride  string `json:"cgroup_root_override,omitempty"`
}

// Report 是启动器在 exec 真实插件之前写回宿主的能力报告。
//
// 这是取证面：`enforced`/`degraded`/`disabled` 三态 + 原因字符串，全部落在宿主
// 现有 slog 审计落点上（见 DrainLaunchReport），不新增审计通道。
type Report struct {
	PluginID string `json:"plugin_id"`
	PID      int    `json:"pid"`
	Entry    string `json:"entry"`

	LandlockABI int    `json:"landlock_abi"`
	Filesystem  string `json:"filesystem"`
	FSReason    string `json:"filesystem_reason,omitempty"`

	Network       string `json:"network"`
	NetworkPolicy string `json:"network_policy"`
	NetworkReason string `json:"network_reason,omitempty"`

	Cgroup       string            `json:"cgroup"`
	CgroupPath   string            `json:"cgroup_path,omitempty"`
	CgroupReason string            `json:"cgroup_reason,omitempty"`
	CgroupLimits map[string]string `json:"cgroup_limits,omitempty"`

	NoNewPrivs bool     `json:"no_new_privs"`
	TempDir    string   `json:"temp_dir,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// Degraded 报告是否存在任何一项降级（用于宿主日志的严重级别判定）。
func (r Report) Degraded() bool {
	for _, status := range []string{r.Filesystem, r.Network, r.Cgroup} {
		if status == StatusDegraded {
			return true
		}
	}
	return false
}

func (r Report) String() string {
	payload, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf("plugin sandbox report for %s (unencodable: %v)", r.PluginID, err)
	}
	return string(payload)
}

// ErrLauncherNotRegistered 表示当前宿主二进制没有声明「我支持沙箱启动器」。
//
// 它是能力声明而不是错误条件：测试二进制、CLI 等不启动插件的进程都会走到这里，
// 由调用方降级为直接 exec。
var ErrLauncherNotRegistered = errors.New("plugin sandbox launcher is not registered by this host binary")

// ErrLauncherUnsupported 表示当前平台没有启动器实现（非 Linux）。
var ErrLauncherUnsupported = errors.New("plugin sandbox launcher is unsupported on this platform")

// launcherRegistered 由 cmd/api 与 cmd/worker 的 main 设置。
//
// 用显式声明而不是「猜自己在测试/生产」：启动器要求宿主二进制自己在 main 最前面
// 拦截保留 argv，而没有该拦截的二进制（例如 go test 的测试二进制）会把 specJSON
// 当成命令行参数解释。声明让这种错配变成可诊断的降级，而不是一个难查的启动失败。
var launcherRegistered atomic.Bool

// EnableLauncher 声明当前二进制支持沙箱启动器。幂等。
func EnableLauncher() { launcherRegistered.Store(true) }

// LauncherEnabled 报告当前二进制是否已声明支持沙箱启动器。
func LauncherEnabled() bool { return launcherRegistered.Load() }

// IsLauncherInvocation 报告 argv 是否是一次沙箱启动器调用。
func IsLauncherInvocation(argv []string) bool {
	return len(argv) >= 2 && argv[0] == LauncherArgv0
}

// reportEnabled 报告报告 fd 是否可用作写端（0/1/2 不可用）。
func reportEnabled(fd int) bool { return fd > 2 }

// parseLimitValue 校验 cgroup 限额文本：只接受内核认得的形态（十进制整数或
// 单个 K/M/G/T 单位），并拒绝控制字符/空白注入——这些值会被直接写进 /sys。
func parseLimitValue(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 32 || value != raw {
		return "", false
	}
	digits := value
	if last := value[len(value)-1]; strings.ContainsRune("KMGTPEkmgtpe", rune(last)) {
		digits = value[:len(value)-1]
	}
	if digits == "" {
		return "", false
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	if _, err := strconv.ParseUint(digits, 10, 64); err != nil {
		return "", false
	}
	return value, true
}
