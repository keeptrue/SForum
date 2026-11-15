//go:build linux

package pluginprocess

// Landlock 是这里唯一真正「由内核执行」的文件系统与网络边界。
//
// 为什么不是 namespace：本机 unshare(CLONE_NEWNS/CLONE_NEWNET/CLONE_NEWUSER) 与
// 写 /proc/self/uid_map 都是 EPERM（AppArmor），所以 mount/net/user namespace
// 不能作为必需路径。Landlock 不需要特权、可降级、失败只影响本进程，适合作为
// 「第三方二进制没有能力声明」阶段能上的硬边界。
//
// 边界诚实说明：
//   - 只处理我们列出的 access 位。LANDLOCK_ACCESS_FS_IOCTL_DEV（ABI 5）与
//     LANDLOCK_ACCESS_FS_RESOLVE_UNIX（ABI 8）刻意不处理，因此设备 ioctl 与
//     unix socket 路径解析不受限——处理它们会打断 /dev/null 之类既有用法，
//     而收益在「插件已有任意读」的前提下并不成立；
//   - LANDLOCK_SCOPE_SIGNAL（ABI 6）可以进一步切断同 uid 互发信号，但它会让插件
//     无法向自己域外的任何进程发信号（含运维脚本），本轮不打开，写在决策记录里。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	landlockRulePathBeneath = 1
)

// landlockReadAccess 是「读一个目录/文件」所需的最小位集合。
const landlockReadAccess = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

// landlockWritableAccess 是「独占一个私有临时目录」所需的位集合：读、写、建、删。
// REFER 只在 ABI>=2 存在，由 clampLandlockAccess 按 ABI 裁剪。
const landlockWritableAccess = landlockReadAccess |
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
	unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
	unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
	unix.LANDLOCK_ACCESS_FS_MAKE_REG |
	unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
	unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
	unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
	unix.LANDLOCK_ACCESS_FS_REFER |
	unix.LANDLOCK_ACCESS_FS_TRUNCATE

// landlockOutcome 是 Landlock 这一层的能力结论。
type landlockOutcome struct {
	ABI        int
	Filesystem string
	FSReason   string
	Network    string
	NetReason  string
	Notes      []string
}

// sandboxReadOnlyPaths 是插件进程被允许读的内核/运行时路径。
//
// 取舍标准：Go 运行时、TLS/DNS、时区、以及 /proc/self 这类自省路径必须可读；
// 其余一律不给。运维可以用 SFORUM_PLUGIN_SANDBOX_EXTRA_RO 追加（只读）。
var sandboxReadOnlyPaths = []string{
	"/usr", "/lib", "/lib64", "/bin", "/sbin",
	"/etc/ssl", "/etc/pki", "/etc/ca-certificates",
	"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf",
	"/etc/passwd", "/etc/group", "/etc/localtime",
	"/proc/self",
	"/sys/devices/system/cpu", "/sys/kernel/mm/transparent_hugepage",
}

// sandboxExecutablePaths 是动态链接器（ld.so）与共享库所在目录：它们需要 EXECUTE，
// 否则一个 CGO 构建的插件（含 Go 自己的 test 二进制）在 execve 时就会 EACCES——
// 内核对「程序解释器」的检查发生在 execve 里，读权限不够。
//
// 刻意只给库目录、不给 /bin 与 /usr/bin：那等于放开「执行任意系统工具」这项能力
// （`sh -c` 就是一条现成的通用执行器）。插件确实需要 exec 外部工具时用
// SFORUM_PLUGIN_SANDBOX_EXTRA_EXEC 显式追加目录。
var sandboxExecutablePaths = []string{
	"/lib", "/lib64", "/usr/lib", "/usr/lib64",
}

// sandboxDevicePaths 是需要同时读写的字符设备：Go 运行时与 TLS 随机源。
var sandboxDevicePaths = []string{
	"/dev/null", "/dev/zero", "/dev/full", "/dev/urandom", "/dev/random",
}

type landlockGrant struct {
	Path   string
	Access uint64
}

// landlockABI 返回内核支持的 Landlock ABI 版本；不支持时返回原因。
func landlockABI(spec SandboxSpec) (int, error) {
	if spec.LandlockABIOverride != nil {
		if *spec.LandlockABIOverride < 1 {
			return 0, errors.New("landlock is unavailable (injected ABI 0)")
		}
		return *spec.LandlockABIOverride, nil
	}
	version, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION,
	)
	if errno != 0 {
		return 0, fmt.Errorf("landlock_create_ruleset(VERSION): %w", errno)
	}
	if version < 1 {
		return 0, fmt.Errorf("landlock reported unsupported ABI %d", int(version))
	}
	return int(version), nil
}

// landlockHandledAccess 返回应当交给内核处理（即默认拒绝）的文件系统 access 位。
func landlockHandledAccess(abi int) uint64 {
	mask := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE) |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM
	if abi >= 2 {
		mask |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		mask |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return mask
}

// clampLandlockAccess 把一组 access 位裁剪到当前 ABI 支持的集合。
//
// 必须裁剪：把内核不认识的位交给 landlock_add_rule 会得到 EINVAL，整个规则集
// 就加不进去——那会让一台旧内核上的沙箱从「部分能力」直接变成「完全不可用」。
func clampLandlockAccess(access, handled uint64) uint64 { return access & handled }

// landlockFSGrants 组装文件系统白名单。
//
// 顺序与内容都刻意最小化：制品目录读+执行、包根只读、私有 TMPDIR 可写，其余白名单
// 一律只读。插件写自己二进制旁边（旧 cwd 语义）从此是 EACCES，这是有意的行为变更。
func landlockFSGrants(spec SandboxSpec, tempDir string) []landlockGrant {
	grants := make([]landlockGrant, 0, 8+len(sandboxReadOnlyPaths)+len(sandboxDevicePaths))
	if artifact := strings.TrimSpace(spec.ArtifactDir); artifact != "" {
		grants = append(grants, landlockGrant{Path: artifact, Access: landlockReadAccess | unix.LANDLOCK_ACCESS_FS_EXECUTE})
	}
	if root := strings.TrimSpace(spec.PackageRoot); root != "" {
		grants = append(grants, landlockGrant{Path: root, Access: landlockReadAccess})
	}
	if tempDir != "" {
		grants = append(grants, landlockGrant{Path: tempDir, Access: landlockWritableAccess})
	}
	for _, path := range sandboxReadOnlyPaths {
		grants = append(grants, landlockGrant{Path: path, Access: landlockReadAccess})
	}
	for _, path := range sandboxExecutablePaths {
		grants = append(grants, landlockGrant{Path: path, Access: landlockReadAccess | unix.LANDLOCK_ACCESS_FS_EXECUTE})
	}
	for _, path := range spec.ExtraExec {
		grants = append(grants, landlockGrant{Path: path, Access: landlockReadAccess | unix.LANDLOCK_ACCESS_FS_EXECUTE})
	}
	for _, path := range sandboxDevicePaths {
		grants = append(grants, landlockGrant{Path: path, Access: landlockReadAccess | unix.LANDLOCK_ACCESS_FS_WRITE_FILE})
	}
	for _, path := range spec.ExtraReadOnly {
		grants = append(grants, landlockGrant{Path: path, Access: landlockReadAccess})
	}
	// 运维显式配置的插件数据目录：读 + 写 + 建 + 删。这是 P0 的修复点——
	// sforum-storage-fs 的 root_path 就是通过这条规则拿到写权限的。
	for _, path := range spec.ExtraReadWrite {
		grants = append(grants, landlockGrant{Path: path, Access: landlockWritableAccess})
	}
	// 显式声明的 CA 路径：与 env 白名单的 SSL_CERT_FILE / SSL_CERT_DIR 透传配套——
	// 宿主把变量交给插件，这里保证它指到的文件真的可读（缺一边就是死路）。
	if bundle := strings.TrimSpace(os.Getenv("SSL_CERT_FILE")); bundle != "" {
		grants = append(grants, landlockGrant{Path: bundle, Access: unix.LANDLOCK_ACCESS_FS_READ_FILE})
	}
	for _, dir := range filepath.SplitList(os.Getenv("SSL_CERT_DIR")) {
		if dir = strings.TrimSpace(dir); dir != "" {
			grants = append(grants, landlockGrant{Path: dir, Access: landlockReadAccess})
		}
	}
	return grants
}

// confineWithLandlock 建立并应用 Landlock 规则集。
//
// 语义：任何一步失败都返回 degraded + 原因，绝不让插件起不来。调用方在返回后仍会
// exec，因此「降级」意味着该层不生效，而不是进程被拒绝。
func confineWithLandlock(spec SandboxSpec, tempDir string) landlockOutcome {
	outcome := landlockOutcome{
		Filesystem: StatusDisabled,
		Network:    StatusDisabled,
		FSReason:   "filesystem policy disabled",
		NetReason:  "network policy " + string(spec.Policy.Network),
	}
	abi, err := landlockABI(spec)
	if err != nil {
		outcome.Filesystem = StatusDegraded
		outcome.FSReason = err.Error()
		outcome.Network = StatusDegraded
		outcome.NetReason = err.Error()
		return outcome
	}
	outcome.ABI = abi

	handledFS := uint64(0)
	if spec.Policy.Filesystem {
		handledFS = landlockHandledAccess(abi)
	}
	handledNet := uint64(0)
	networkRequested := spec.Policy.Network == NetworkBind || spec.Policy.Network == NetworkAll
	if networkRequested {
		if abi < 4 {
			outcome.Network = StatusDegraded
			outcome.NetReason = fmt.Sprintf("landlock ABI %d < 4: TCP restrictions are unsupported", abi)
		} else {
			handledNet = unix.LANDLOCK_ACCESS_NET_BIND_TCP
			if spec.Policy.Network == NetworkAll {
				handledNet |= unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
			}
		}
	}
	if !spec.Policy.Filesystem && handledNet == 0 {
		return outcome
	}

	rulesetFD, err := createLandlockRuleset(handledFS, handledNet, abi)
	if err != nil {
		outcome.Filesystem = StatusDegraded
		outcome.FSReason = err.Error()
		if networkRequested {
			outcome.Network = StatusDegraded
			outcome.NetReason = err.Error()
		}
		return outcome
	}
	defer unix.Close(rulesetFD)

	added := 0
	skipped := 0
	if spec.Policy.Filesystem {
		for _, grant := range landlockFSGrants(spec, tempDir) {
			access := grantAccessForPath(grant.Path, clampLandlockAccess(grant.Access, handledFS))
			if access == 0 {
				skipped++
				continue
			}
			if err := addLandlockPathRule(rulesetFD, grant.Path, access); err != nil {
				// 单条路径失败不放弃整个规则集：起点缺失的路径（例如容器里没有
				// /lib64）本来就不该让沙箱整体降级。
				skipped++
				outcome.Notes = append(outcome.Notes, fmt.Sprintf("rule %s: %v", grant.Path, err))
				continue
			}
			added++
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		outcome.Filesystem = StatusDegraded
		outcome.FSReason = fmt.Sprintf("PR_SET_NO_NEW_PRIVS: %v", err)
		if networkRequested {
			outcome.Network = StatusDegraded
			outcome.NetReason = outcome.FSReason
		}
		return outcome
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0); errno != 0 {
		outcome.Filesystem = StatusDegraded
		outcome.FSReason = fmt.Sprintf("landlock_restrict_self: %v", errno)
		if networkRequested {
			outcome.Network = StatusDegraded
			outcome.NetReason = outcome.FSReason
		}
		return outcome
	}
	if spec.Policy.Filesystem {
		outcome.Filesystem = StatusEnforced
		outcome.FSReason = ""
		outcome.Notes = append(outcome.Notes, fmt.Sprintf("landlock fs rules: %d added, %d skipped", added, skipped))
	}
	if networkRequested {
		outcome.Network = StatusEnforced
		outcome.NetReason = ""
	} else {
		outcome.NetReason = "network policy " + string(spec.Policy.Network)
	}
	return outcome
}

// grantAccessForPath 把「目录权限集合」适配成路径实际类型允许的集合。
//
// Landlock 对非目录路径不接受 READ_DIR / REFER / REMOVE / MAKE_* 这些位，传了就
// 是 EINVAL。所以这里先 stat，再按 dir/file/其他 收窄。
func grantAccessForPath(path string, access uint64) uint64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if info.IsDir() {
		return access
	}
	fileAccess := uint64(0)
	for _, bit := range []uint64{
		unix.LANDLOCK_ACCESS_FS_READ_FILE,
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE,
		unix.LANDLOCK_ACCESS_FS_EXECUTE,
		unix.LANDLOCK_ACCESS_FS_TRUNCATE,
	} {
		if access&bit != 0 {
			fileAccess |= bit
		}
	}
	return fileAccess
}

func createLandlockRuleset(handledFS, handledNet uint64, abi int) (int, error) {
	attr := unix.LandlockRulesetAttr{Handled_access_fs: handledFS, Handled_access_net: handledNet}
	size := unsafe.Sizeof(attr.Handled_access_fs)
	if abi >= 4 {
		// ABI 4 才认识 handled_access_net；把更长的结构交给旧内核是 E2BIG。
		size = unsafe.Sizeof(attr.Handled_access_fs) + unsafe.Sizeof(attr.Handled_access_net)
	}
	fd, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), size, 0,
	)
	if errno != 0 {
		return -1, fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	return int(fd), nil
}

func addLandlockPathRule(rulesetFD int, path string, access uint64) error {
	parentFD, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(parentFD)}
	if _, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD), landlockRulePathBeneath,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0,
	); errno != 0 {
		return errno
	}
	return nil
}
