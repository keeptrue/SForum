//go:build unix

package extensionsruntime

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// 这组用例守的是 exec 路径上三个「接线点」：独立进程组、rlimit、整组回收顺序。
// 它们都是「摘掉就全绿」的典型位置——所以必须由测试直接盯住调用结果而不是存在性。

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

// TestProtocolStartLowersOpenFileLimit 盯住 applyPluginProcessLimits 的接线：
// 摘掉它，子进程的 RLIMIT_NOFILE 就还是宿主的（通常 1024/1048576 且与本断言无关）。
func TestProtocolStartLowersOpenFileLimit(t *testing.T) {
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
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	instance := starter.protocolInstance(RuntimeInstanceIdentity{
		ExtensionID: extension.ID, InstanceID: target.InstanceID,
	})
	if instance == nil || instance.processGroup <= 0 {
		t.Fatalf("started runtime instance has no process group: %#v", instance)
	}
	var limit unix.Rlimit
	if err := unix.Prlimit(instance.processGroup, unix.RLIMIT_NOFILE, nil, &limit); err != nil {
		t.Fatalf("read back plugin fd limit: %v", err)
	}
	if limit.Cur > pluginProcessOpenFileCeiling {
		t.Fatalf("plugin fd soft limit = %d, want <= %d", limit.Cur, pluginProcessOpenFileCeiling)
	}
	if limit.Max < limit.Cur {
		t.Fatalf("plugin fd hard limit %d is below soft limit %d", limit.Max, limit.Cur)
	}
}

// TestRuntimeInstanceKillReapsProcessGroupBeforeClient 盯住两件事：
//  1. instance.processGroup 被真正记录（否则 kill() 只杀组长）；
//  2. kill() 先整组、后 client —— 顺序反了先回收组长，组 id 就失去锚点。
//
// 「顺序」本身没有第二个可观测副作用，所以这里把整组入口换成探针，在探针里读
// client.Exited()：client 若已被回收，这个值是 true。
func TestRuntimeInstanceKillReapsProcessGroupBeforeClient(t *testing.T) {
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
	identity := RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}
	instance := starter.protocolInstance(identity)
	if instance == nil || instance.processGroup <= 0 {
		t.Fatalf("started runtime instance has no process group: %#v", instance)
	}
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
	instance.kill()

	if observedGroup != processGroup {
		t.Fatalf("kill() reclaimed process group %d, want %d", observedGroup, processGroup)
	}
	// 整组真的没了：向 -pgid 发 0 号信号应当得到 ESRCH。
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-processGroup, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugin process group %d survived kill(): err=%v", processGroup, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pluginProcessOpenFileCeiling 与 pluginprocess.MaxOpenFiles 对齐；这里单独抄一份，
// 是为了让测试断言的是「生效的上限」而不是被测试代码里的常量。
const pluginProcessOpenFileCeiling = 4096
