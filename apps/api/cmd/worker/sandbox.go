package main

import (
	"os"

	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/pluginprocess"
)

// enterPluginSandboxLauncher 必须是 main 的第一条语句（与 cmd/api 同一契约）。
//
// worker 进程同样会拉起插件（worker 内嵌运行时），因此也必须能作为沙箱启动器被
// 重新 exec；否则在 worker 侧启动的插件会静默失去内核级约束。
func enterPluginSandboxLauncher() {
	if pluginprocess.IsLauncherInvocation(os.Args) {
		os.Exit(pluginprocess.RunLauncher(os.Args))
	}
	pluginprocess.EnableLauncher()
}
