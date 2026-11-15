package main

import (
	"os"

	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/pluginprocess"
)

// enterPluginSandboxLauncher 必须是 main 的第一条语句。
//
// 插件子进程用 os.Executable() 重新 exec 本二进制来获得「只做加固然后 exec」的
// 干净起点（EX2-01）。那一次 exec 的 argv 不是普通启动参数：argv[0] 是保留标记
// __sforum_plugin_sandbox_exec，argv[1] 是沙箱 spec JSON。所以必须在任何 flag/
// config/env 解析之前接管进程，否则它会被当成一次带奇怪参数的正常启动。
//
// 普通启动时它只做一件事：声明「本二进制可以作为沙箱启动器被重新 exec」。
// 没有这个声明的二进制（例如 go test 的测试二进制）会让插件启动时降级为直接
// exec，而不是把 spec JSON 当参数解释出一堆难查的启动失败。
func enterPluginSandboxLauncher() {
	if pluginprocess.IsLauncherInvocation(os.Args) {
		os.Exit(pluginprocess.RunLauncher(os.Args))
	}
	pluginprocess.EnableLauncher()
}
