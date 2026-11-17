//go:build linux

package extensionsruntime

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestProtocolStartLowersOpenFileLimit 盯住 applyPluginProcessLimits 的接线：
// 摘掉它，子进程的 RLIMIT_NOFILE 就还是宿主的（通常 1024/1048576 且与本断言无关）。
//
// 为什么这条断言在 Linux 专属文件里：读回**另一个进程**的限额只有 prlimit(2)，
// 而 x/sys/unix 只在 Linux 上提供 unix.Prlimit。darwin/*BSD 上 pluginprocess.Limit
// 是显式 no-op（见 pluginprocess/hardening_unix_noprlimit.go），这条断言在那里本来
// 就不成立；留在 `//go:build unix` 的文件里会让那些平台的测试编译直接失败。
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

// pluginProcessOpenFileCeiling 与 pluginprocess.MaxOpenFiles 对齐；这里单独抄一份，
// 是为了让测试断言的是「生效的上限」而不是被测试代码里的常量。
const pluginProcessOpenFileCeiling = 4096
