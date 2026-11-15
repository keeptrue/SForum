package routes

import extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"

// globalMiddlewareScopeApplies 判断一条 global_middleware contribution 是否作用于该
// terminal。三个调用点（registry 匹配、执行计划校验、策略快照）共用这一份判定，
// 不允许各自复制一遍规则。
//
// 作用域是显式声明出来的，不是「匹配到就算」：
//   - TargetID 为空 → 只覆盖插件自己的 terminal（ProviderPlugin）；
//   - TargetID 非空 → 精确覆盖声明的那个 terminal。
//
// 因此 Core 的 /api/v1 terminal 默认落在插件 global middleware 的作用域之外。想覆盖
// 某一条 Core 路由，必须在 route 里写明 TargetID —— 这是唯一入口，也是可审计、可
// 测试的声明；否则一条 global middleware 会悄悄改变整站的核心请求链。
func globalMiddlewareScopeApplies(terminal, contribution Route) bool {
	if contribution.Action != extensionmanifest.RouteActionGlobalMiddleware {
		return false
	}
	if contribution.Provider.Kind != ProviderPlugin {
		return false
	}
	if contribution.TargetID == "" {
		return terminal.Provider.Kind == ProviderPlugin
	}
	return contribution.TargetID == globalMiddlewareTerminalID(terminal)
}

// globalMiddlewareTerminalID 解析 terminal 的稳定身份：replace 的 terminal 用被替换的
// 目标 ID，其余用自身 ID。
func globalMiddlewareTerminalID(terminal Route) string {
	if terminal.Action == extensionmanifest.RouteActionReplace && terminal.TargetID != "" {
		return terminal.TargetID
	}
	return terminal.ID
}
