package extensionmanifest

import "strings"

// DeriveRouteAccess 是「声明守卫 + 可选 access」→「授权结论」的唯一推导点。
//
// 之前这条推导在三个地方各写了一遍，而且三份结果互相矛盾：
//   - 声明派发（Route Registry 的步骤计划）按守卫本身执行；
//   - legacy 代理前缀 /api/v1/extensions/:id/* 在 access 为空时默认成 login；
//   - declared 路由视图（GuardPolicyCatalog.LookupDeclaredRoute）对非简单守卫返回 403。
//
// 于是 custom / raw_request 守卫路由虽然「有守卫」，却在 legacy 前缀上被当成 login
// 放行给任何已登录用户（P1-2）。收口到本函数之后，三处只有一份答案。
//
// proxyable=false 表示这条路由**不能**由 legacy 前缀服务，调用方必须应答 404
// （不是 403：403 会泄露「该路由存在」）。这类路由的授权由插件自己的守卫代码拥有，
// 只有可信 Route Registry 才能复现它的裁决，legacy 前缀没有这个能力。
func DeriveRouteAccess(route ManifestRoute) (access string, permission string, proxyable bool) {
	guard := strings.TrimSpace(route.Guard)
	declared := strings.TrimSpace(route.Access)

	// access 是声明侧的冗余字段：写了就必须落在封闭集里，未知值一律不可信。
	// 口径与 v3_validate.go 的 coreGuardForRouteAccess 一致。
	if declared != "" {
		if _, ok := coreGuardForRouteAccess(declared); !ok {
			return "", "", false
		}
	}

	resolved := ""
	switch guard {
	case "":
		// 早期清单只写 access、不写守卫：access 本身就是声明。
		// 两者都没有时 resolved 保持空，落到下面的 default 分支 fail-closed。
		resolved = declared
	case GuardCorePublic:
		resolved = RouteAccessPublic
	case GuardCoreLogin:
		resolved = RouteAccessLogin
	case GuardCorePermission:
		resolved = RouteAccessPermission
	case GuardCoreGuest:
		// legacy 前缀与 declared 视图都无法表达「仅匿名」，所以这里收紧成不可代理，
		// 而不是退化成 login：放宽到已登录用户会把声明的授权语义从「仅匿名」改成
		// 「已登录即可」，而放开匿名会把 guest 路由变成公开路由。两条路都是错的，
		// 正确回答是两边都拒绝——guest 路由只能走可信 Route Registry。
		//
		// 这一分支同时吞掉「guest + 显式 access」的矛盾声明：guest 没有合法的 access
		// 形状（coreGuardForRouteAccess 不产出 guest），所以带 access 也是 false。
		return "", "", false
	default:
		// core.guard.inherit、core.guard.raw_request，以及包自定义守卫
		// （kind=custom / kind=raw_request）的授权属于被包裹路由或插件代码本身。
		return "", "", false
	}
	if declared != "" && declared != resolved {
		// 守卫与 access 互相矛盾：声明不可信（v3_validate.go 要求两者同值）。
		return "", "", false
	}

	switch resolved {
	case RouteAccessPublic:
		return RouteAccessPublic, "", true
	case RouteAccessLogin:
		return RouteAccessLogin, "", true
	case RouteAccessPermission:
		permission = strings.TrimSpace(route.Permission)
		if permission == "" {
			// permission 路由没有权限键 = 无法裁决，不能当成「登录即可」。
			return "", "", false
		}
		return RouteAccessPermission, permission, true
	default:
		// 既没有守卫也没有 access：没有声明就没有可信的授权结论。
		return "", "", false
	}
}
