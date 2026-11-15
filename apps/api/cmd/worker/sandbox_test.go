package main

import (
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Extensions/pluginprocess"
)

// worker 也会拉起插件，因此同样必须声明自己可作为沙箱启动器：否则 worker 侧启动的
// 插件会静默失去内核级约束（接口面完全相同，容易漏）。
func TestEnterPluginSandboxLauncherDeclaresLauncherSupport(t *testing.T) {
	enterPluginSandboxLauncher()
	if !pluginprocess.LauncherEnabled() {
		t.Fatal("main 的第一条语句必须声明本二进制可作为沙箱启动器")
	}
}
