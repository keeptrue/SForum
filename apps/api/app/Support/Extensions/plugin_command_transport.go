package extensionsruntime

// 本文件是「插件子进程边界」的落点：宿主怎么启动一个插件、给它什么环境、允许它
// 回报什么样的 HTTP 目标，以及进程怎么被回收。
//
// 信任模型与残余风险写在文件末尾的 verifyPluginExecutableArtifact 与
// knowledge/decisions/2026-11-15-ex2-01-plugin-process-trust-boundary.md 里。
// 一句话：exec 前内容级复核 + 进程级加固；文件系统/网络/子进程仍不受限。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-plugin"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/pluginprocess"
)

func (s *ProtocolStarter) InvokePluginCommand(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	contract PluginCommandContract,
	input map[string]any,
) (map[string]any, error) {
	if s == nil || ctx == nil {
		return nil, ErrPluginCommandRegistryInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := normalizeRuntimeInstanceIdentity(identity)
	if err != nil {
		return nil, err
	}
	var v2 *protocolV2Client
	if err := func() error {
		unlock, err := s.lockExtensionLifecycleContext(ctx, identity.ExtensionID)
		if err != nil {
			return err
		}
		defer unlock()
		instance := s.protocolInstance(identity)
		if instance == nil {
			return protocolInstanceNotFound(identity)
		}
		if instance.protocolVersion != 2 {
			return ErrProtocolInstanceUnsupported
		}
		var ok bool
		v2, ok = instance.protocol.(*protocolV2Client)
		if !ok || v2.identity == nil || v2.identity.GetExtensionId() != identity.ExtensionID ||
			v2.identity.GetInstanceId() != identity.InstanceID ||
			contract.ExtensionID != identity.ExtensionID || contract.InstanceID != identity.InstanceID ||
			contract.ExtensionVersion != instance.extensionVersion || contract.ArtifactDigest != instance.artifactDigest {
			return fmt.Errorf("%w: exact runtime identity drift", ErrPluginCommandRuntimeStale)
		}
		s.mu.Lock()
		s.recordProtocolCallLocked(identity.ExtensionID)
		s.mu.Unlock()
		return nil
	}(); err != nil {
		return nil, err
	}
	return v2.invokePluginCommand(ctx, contract, input)
}

var _ pluginCommandRuntime = (*ProtocolStarter)(nil)

// --- 插件子进程环境构建（自 protocol.go 抽出；职责：进程 env 组装） ---

// 插件子进程环境白名单：不继承 DATABASE_URL / SESSION_* 等宿主密钥。
var pluginEnvAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true,
	// go-plugin / 测试 helper 需要的变量由 go-plugin 自行注入；宿主仅保留基础运行环境。
	"SFORUM_PLUGIN_HELPER": true,
	// APP_ENV 非密钥；插件用其判断是否允许测试端点覆盖（生产一律官方端点）。
	"APP_ENV": true,
}

// githubAuthEndpointOverrideEnvKeys 仅在非 production 宿主环境可注入插件子进程。
// 生产路径即使宿主误设这些变量也不得透传，避免 OAuth code/secret/token 打到任意端点。
var githubAuthEndpointOverrideEnvKeys = map[string]bool{
	"SFORUM_AUTH_GITHUB_AUTH_URL":  true,
	"SFORUM_AUTH_GITHUB_TOKEN_URL": true,
	"SFORUM_AUTH_GITHUB_API_URL":   true,
}

// 插件子进程 GC 调优默认值。GOGC=50 让小堆插件更积极归还内存；
// GOMEMLIMIT 是软上限——实测插件常驻 RSS 约 25-30MB，20MiB 会贴着 live heap
// 造成 GC 连续抖动。默认值按「最大单条 AI 输入」重新定标：Host 允许 12MiB 正文，
// 经 JSON/proto 序列化后峰值可达数十 MiB，96MiB 留足余量仍能压住空闲水位。
// 已部署实例零配置即生效；宿主进程环境里若存在 SFORUM_PLUGIN_GOMEMLIMIT 则覆盖。
const (
	pluginProcessGOGCDefault           = "GOGC=50"
	pluginProcessGOMEMLIMITDefault     = "GOMEMLIMIT=96MiB"
	pluginProcessGOMEMLIMITOverrideEnv = "SFORUM_PLUGIN_GOMEMLIMIT"
)

// buildPluginProcessEnv 只保留插件白名单环境；production 不透传 fake-GitHub
// 端点覆盖。disablethp 避免 Linux 为每个小型 Go 插件堆分配多余透明大页。
func buildPluginProcessEnv(hostEnv []string) []string {
	production := hostEnvIsProduction(hostEnv)
	out := []string{"GODEBUG=disablethp=1", pluginProcessGOGCDefault}
	if override, ok := lookupHostEnv(hostEnv, pluginProcessGOMEMLIMITOverrideEnv); ok && strings.TrimSpace(override) != "" {
		out = append(out, "GOMEMLIMIT="+strings.TrimSpace(override))
	} else {
		out = append(out, pluginProcessGOMEMLIMITDefault)
	}
	for _, entry := range hostEnv {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if githubAuthEndpointOverrideEnvKeys[key] {
			// 生产边界：拒绝透传测试端点覆盖。
			if production {
				continue
			}
			out = append(out, entry)
			continue
		}
		if pluginEnvAllowlist[key] || strings.HasPrefix(key, "SFORUM_SETTING_") {
			out = append(out, entry)
		}
	}
	return out
}

// lookupHostEnv 从宿主环境切片精确匹配键，返回原始值（不做 trim，
// 与白名单透传语义一致：值原样交给插件进程）。
func lookupHostEnv(hostEnv []string, key string) (string, bool) {
	for _, entry := range hostEnv {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == key {
			return value, true
		}
	}
	return "", false
}

// hostEnvIsProduction 从宿主环境切片读取 APP_ENV（大小写不敏感）。
func hostEnvIsProduction(hostEnv []string) bool {
	for _, entry := range hostEnv {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "APP_ENV" {
			return strings.EqualFold(strings.TrimSpace(value), "production")
		}
	}
	return false
}

func pluginSettingEnvName(key string) string {
	var value strings.Builder
	value.WriteString("SFORUM_SETTING_")
	for _, char := range strings.ToUpper(key) {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			value.WriteRune(char)
		} else {
			value.WriteByte('_')
		}
	}
	return value.String()
}

// --- 插件子进程的 exec 前信任门与启动约束（EX2-01 / EX2-04b） ---

// ErrPluginExecutableUntrusted 表示 exec 被拒绝：制品与受信结论不一致，或启动
// 环境落在不该允许的位置。
var ErrPluginExecutableUntrusted = errors.New("plugin executable is not the trusted artifact")

// 插件按设计拥有的能力（这是声明过的契约，不是漏洞）：
//   - 与宿主同 uid 的完整文件系统读写；
//   - 任意出站网络连接；
//   - 派生子进程；
//   - 通过 host API token 使用 Host 已注册的具体服务，以及自己的数据库 schema。
//
// 残余风险（本轮不消除）：文件系统、网络、子进程仍然不受限。内核级隔离
// （namespace / seccomp / 用户命名空间）不在本轮范围，因此一个已启用的插件仍能
// 读写其它插件的制品目录——正是这一点让「伪造 size+mtime」的缓存投毒在理论上
// 可达，也是 exec 前必须按内容重算摘要的理由。
//
// 已实现的信任前提：
//  1. 上传/下载只写入惰性制品，不执行包内代码；
//  2. 第一次 enable/upgrade 之前，super_admin 必须对 exact artifact 做信任确认，
//     运行时启动要求存在 exact live grant（见 protocolV2ClientConfig）；
//  3. 本函数把「授权依据的摘要」与「按内容重算的真哈希」对齐后才允许 exec。
func verifyPluginExecutableArtifact(extension extensions.Extension) error {
	if strings.TrimSpace(extension.PackageDigest) == "" {
		return fmt.Errorf("%w: extension %s has no package digest", ErrPluginExecutableUntrusted, extension.ID)
	}
	digest, err := extensions.VerifyRuntimeExecutableDigest(extension)
	if err != nil {
		return errors.Join(ErrPluginExecutableUntrusted, err)
	}
	if strings.TrimSpace(digest) == "" {
		return fmt.Errorf("%w: extension %s produced an empty backend digest", ErrPluginExecutableUntrusted, extension.ID)
	}
	return nil
}

// preparePluginCommand 组装插件子进程的 exec.Cmd：cwd 钉在制品目录、env 走白名单、
// 独立进程组。
func preparePluginCommand(extension extensions.Extension, entryPath string) (*exec.Cmd, error) {
	workDir, err := pluginWorkingDirectory(extension, entryPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(entryPath)
	cmd.Dir = workDir
	cmd.Env = buildPluginProcessEnv(os.Environ())
	pluginprocess.Apply(cmd)
	return cmd, nil
}

// pluginProcessGroupKill 是整组回收的入口。测试会替换它来断言回收顺序（见
// plugin_process_unix_test.go）：顺序本身没有第二个可观测副作用，但「组长先被
// 回收」会让组 id 提前失效，组内留存的派生进程就逃出这次回收。
var pluginProcessGroupKill = pluginprocess.KillGroup

// stopPluginProcess 是插件进程回收的**唯一**入口：先整组、后客户端。
//
// 顺序有实际后果：client.Kill() 会杀掉并 Wait 回收组长进程；先做这一步会让
// kill(-pgid) 失去锚点，插件派生的子进程就留成孤儿。
//
// 启动失败的 16 个错误分支、protocolRuntimeInstance.kill()（Stop/Discard 路径）
// 与 heartbeat 失败回调都调这一个函数——顺序只写一次，也只需要一条测试守住。
func stopPluginProcess(processGroup int, client *plugin.Client) {
	if processGroup > 0 {
		pluginProcessGroupKill(processGroup)
	}
	if client != nil {
		client.Kill()
	}
}

// applyPluginProcessLimits 在子进程出现之后收紧资源上限（见 pluginprocess.Limit）。
func applyPluginProcessLimits(cmd *exec.Cmd) {
	pluginprocess.Limit(cmd)
}

// pluginProcessGroupID 返回子进程的进程组 id；没有进程时返回 0。
func pluginProcessGroupID(cmd *exec.Cmd) int {
	return pluginprocess.GroupID(cmd)
}

// pluginWorkingDirectory 决定插件子进程的 cwd：钉在制品目录（后端入口所在目录），
// 而不是继承宿主进程的 cwd。
//
// 两条硬约束：
//   - 制品目录必须在包内容根之内（入口路径本身已由 installedFilePath 约束，这里
//     再用绝对路径确认一次，避免 cwd 落到包外的相邻目录）；
//   - 不得把插件的 cwd 设成宿主工作树本身或其祖先——那等于让插件的相对路径写入
//     直接落进源码树。
func pluginWorkingDirectory(extension extensions.Extension, entryPath string) (string, error) {
	root := extensions.PackageContentRoot(extension)
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: extension %s has no package content root", ErrPluginExecutableUntrusted, extension.ID)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve package root: %v", ErrPluginExecutableUntrusted, err)
	}
	entryAbs, err := filepath.Abs(entryPath)
	if err != nil {
		return "", fmt.Errorf("%w: resolve backend entry: %v", ErrPluginExecutableUntrusted, err)
	}
	dir := filepath.Dir(entryAbs)
	if !withinDirectory(rootAbs, dir) {
		return "", fmt.Errorf("%w: backend entry %s is outside its package root", ErrPluginExecutableUntrusted, entryAbs)
	}
	if dir == string(filepath.Separator) || dir == "." {
		return "", fmt.Errorf("%w: refusing to use %q as a plugin working directory", ErrPluginExecutableUntrusted, dir)
	}
	if hostWorkingDirectory, err := os.Getwd(); err == nil && hostWorkingDirectory != "" {
		if withinDirectory(dir, hostWorkingDirectory) {
			return "", fmt.Errorf(
				"%w: refusing to start a plugin with the host working tree (%s) inside its cwd %s",
				ErrPluginExecutableUntrusted, hostWorkingDirectory, dir,
			)
		}
	}
	return dir, nil
}

// withinDirectory 报告 child 是否等于 parent 或位于 parent 之下。
func withinDirectory(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == "" || child == "" {
		return false
	}
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// kill 回收一个已发布/已丢弃的运行时实例。
//
// 顺序只有一份：委托给 stopPluginProcess，和启动失败分支走同一条路径，避免
// 「两份相同顺序、却只被测试守住一份」。
func (i *protocolRuntimeInstance) kill() {
	if i == nil {
		return
	}
	stopPluginProcess(i.processGroup, i.client)
}

// --- 插件回报的 HTTP 目标约束（自 protocol.go 抽出；SSRF 边界） ---

// isPluginRouteTargetNone 表示插件不提供可代理的 HTTP BaseURL。
// 兼容旧哨兵 "disabled"（SSRF 加固前 SMTP 等插件使用）。
func isPluginRouteTargetNone(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "disabled", "none":
		return true
	default:
		return false
	}
}

// validatePluginRouteTarget 限制插件 RouteTarget 仅允许 loopback http(s)，阻断 SSRF。
// 调用方应先用 isPluginRouteTargetNone 处理无路由插件。
func validatePluginRouteTarget(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafePluginRouteTarget, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: scheme %q not allowed", ErrUnsafePluginRouteTarget, parsed.Scheme)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: userinfo not allowed", ErrUnsafePluginRouteTarget)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrUnsafePluginRouteTarget)
	}
	// 字面量 loopback 主机名直接放行，避免依赖解析器。
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrUnsafePluginRouteTarget, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: no addresses for %q", ErrUnsafePluginRouteTarget, host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: host %q resolves to non-loopback %s", ErrUnsafePluginRouteTarget, host, ip)
		}
		// 双保险：拒绝 link-local / 未指定地址（IsLoopback 通常已排除）。
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("%w: host %q resolves to disallowed address %s", ErrUnsafePluginRouteTarget, host, ip)
		}
	}
	return nil
}

// protocolInstanceNotFound 报告某 exact runtime instance 不存在。
func protocolInstanceNotFound(identity RuntimeInstanceIdentity) error {
	return fmt.Errorf("%w: %s/%s", ErrRuntimeInstanceNotFound, identity.ExtensionID, identity.InstanceID)
}
