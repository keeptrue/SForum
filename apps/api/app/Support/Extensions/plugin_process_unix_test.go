//go:build unix

package extensionsruntime

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// 这组用例守的是 exec 路径上的「接线点」：独立进程组与整组回收顺序。它们都是
// 「摘掉就全绿」的典型位置——所以必须由测试直接盯住调用结果而不是存在性。
//
// rlimit 那条接线断言不在这里：读回另一个进程的限额依赖 prlimit(2)，只有 Linux 有，
// 因此它在 plugin_process_linux_test.go（本文件是 `//go:build unix`，darwin/*BSD 也
// 会编译它，把 unix.Prlimit 放进来会让那些平台的测试编译直接失败）。

// TestPreparePluginCommandUsesIndependentProcessGroup 盯住 pluginprocess.Apply 的接线：
// 摘掉它，SysProcAttr 就是 nil，整组回收会退化成「只杀组长」。
func TestPreparePluginCommandUsesIndependentProcessGroup(t *testing.T) {
	extension, _ := execSentinelExtension(t, "")
	entry, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
	if !ok {
		t.Fatal("resolve backend entry")
	}
	cmd, err := preparePluginCommand(extension, entry)
	if err != nil {
		t.Fatalf("prepare plugin command: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("plugin command is not in an independent process group: %#v", cmd.SysProcAttr)
	}
	if want := filepath.Dir(entry); cmd.Dir != want {
		t.Fatalf("plugin cwd = %q, want %q", cmd.Dir, want)
	}
}

// TestProtocolStartAcceptsRelativePackagePath 是**正向冒烟**，不是回归守卫：
// dev 的 PackagePath 是相对宿主 cwd 的（../../storage/extensions/<id>/<version>/<digest>），
// 这条用例确认这种形状的包能正常冷启动（握手成功、拿到 pgid）。
//
// 为什么它当不了守卫（2026-11-15 返工实测）：把 exec 单行还原成相对路径后，这条用例
// 在本树里**依然 PASS**。POSIX 路径解析会在 `/` 处钳制多余的 `..`：相对入口的 `..`
// 段数恒等于宿主 cwd 层数（本树 10，随 checkout 深度变化），而 fixture 的制品目录只有
// 8 层，多出的 `..` 被 `/` 吸收后，尾部的绝对段又拼回真实路径，文件照样找得到。只有
// 「制品目录层数 > `..` 段数」时才 ENOENT（实测：层数 18 → ENOENT，层数 8 → RESOLVED）。
// 也就是说这条用例的红绿取决于 checkout 深度，拿它当守卫等于用一个取决于环境的
// 断言冒充证据。真正的守卫是命令组装层面的
// TestPreparePluginCommandExecsAbsoluteEntryForRelativePackage：它在 `go test -overlay`
// 还原 exec 一行时稳定 FAIL，与路径深度无关。
func TestProtocolStartAcceptsRelativePackagePath(t *testing.T) {
	extension := protocolDatabaseLeaseExtension(t, true)
	extension.PackagePath = relativePackagePath(t, extension.PackagePath)

	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust:                          protocolDatabaseTrust{},
		DatabaseLeases:                 newProtocolDatabaseLeaseRegistry(),
		DatabaseLeaseHeartbeatInterval: 10 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target, err := starter.Start(ctx, extension)
	if err != nil {
		t.Fatalf("start plugin process from a relative package path: %v", err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	instance := starter.protocolInstance(RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: target.InstanceID,
	})
	if instance == nil || instance.processGroup <= 0 {
		t.Fatalf("started runtime instance has no process group: %#v", instance)
	}
}

// TestPluginProcessTeardownReapsGroupBeforeClient 用**一条**测试守住两个调用路径的
// 回收顺序：
//  1. protocolRuntimeInstance.kill()（Stop / Discard）；
//  2. stopPluginProcess(pgid, client)（启动失败的 16 个错误分支与 heartbeat 失败回调
//     调的就是它，pgid 由 pluginProcessGroupID(cmd) 给出）。
//
// 两条路径都委托给同一个 stopPluginProcess，所以「顺序」只有一处实现；测试同时断言：
//   - 探针确实拿到 pgid（否则说明某条路径绕开了统一函数）；
//   - 探针触发时 client.Exited() 仍是 false（先整组、后 client）；
//   - 调用之后向 -pgid 发 0 号信号最终返回 ESRCH（整组真的没了）。
func TestPluginProcessTeardownReapsGroupBeforeClient(t *testing.T) {
	t.Run("runtime instance kill", func(t *testing.T) {
		instance, stop := startPluginProcessForTeardown(t)
		defer stop()
		assertTeardownReapsGroupFirst(t, instance,
			func() { instance.kill() },
		)
	})
	t.Run("start failure branch", func(t *testing.T) {
		instance, stop := startPluginProcessForTeardown(t)
		defer stop()
		assertTeardownReapsGroupFirst(t, instance,
			// 错误分支拿到的 pgid 来自 pluginProcessGroupID(cmd)，与 instance.processGroup
			// 是同一个值（见 TestProtocolStartLowersOpenFileLimit）。
			func() { stopPluginProcess(instance.processGroup, instance.client) },
		)
	})
}

// startPluginProcessForTeardown 起一个真实插件进程并返回它的 runtime instance。
// 返回的 stop 只用于测试收尾（避免 t.Fatal 时留下进程）。
func startPluginProcessForTeardown(t *testing.T) (*protocolRuntimeInstance, func()) {
	t.Helper()
	starter := NewProtocolStarter(ProtocolStarterConfig{
		Trust:                          protocolDatabaseTrust{},
		DatabaseLeases:                 newProtocolDatabaseLeaseRegistry(),
		DatabaseLeaseHeartbeatInterval: 10 * time.Millisecond,
		DatabaseLeaseOperationTimeout:  time.Second,
	})
	extension := protocolDatabaseLeaseExtension(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target, err := starter.Start(ctx, extension)
	if err != nil {
		t.Fatalf("start plugin process: %v", err)
	}
	instance := starter.protocolInstance(RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: target.InstanceID,
	})
	if instance == nil || instance.processGroup <= 0 {
		t.Fatalf("started runtime instance has no process group: %#v", instance)
	}
	return instance, func() { _ = starter.Stop(context.Background(), extension) }
}

// assertTeardownReapsGroupFirst 用探针断言顺序，并在 teardown 返回后确认整组消失。
func assertTeardownReapsGroupFirst(
	t *testing.T,
	instance *protocolRuntimeInstance,
	teardown func(),
) {
	t.Helper()
	processGroup := instance.processGroup
	originalKill := pluginProcessGroupKill
	t.Cleanup(func() { pluginProcessGroupKill = originalKill })
	var observedGroup int
	pluginProcessGroupKill = func(pgid int) {
		observedGroup = pgid
		if instance.client != nil && instance.client.Exited() {
			t.Error("client was recycled before the process group was reclaimed")
		}
		originalKill(pgid)
	}
	teardown()
	// 立刻复原探针：defer 里的收尾 Stop 会再走一遍回收路径，那时 client 已回收，
	// 探针会把「第二次调用」误判成顺序颠倒。
	pluginProcessGroupKill = originalKill

	if observedGroup != processGroup {
		t.Fatalf("teardown reclaimed process group %d, want %d (did it bypass stopPluginProcess?)",
			observedGroup, processGroup)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugin process group %d survived teardown: err=%v", processGroup, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
