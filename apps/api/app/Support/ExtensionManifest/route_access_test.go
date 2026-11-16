package extensionmanifest

import "testing"

// TestDeriveRouteAccess 固定「声明守卫 → 授权结论」的唯一推导（P1-2）。
//
// legacy 代理前缀与 declared 路由视图都读这个函数，所以这里同时覆盖两侧关心的
// 形状：core guard 的可代理结论、guest 的收紧、以及非 core 守卫的 fail-closed。
func TestDeriveRouteAccess(t *testing.T) {
	tests := []struct {
		name       string
		route      ManifestRoute
		access     string
		permission string
		proxyable  bool
	}{
		{
			name:   "public guard",
			route:  ManifestRoute{Guard: GuardCorePublic},
			access: RouteAccessPublic, proxyable: true,
		},
		{
			name:   "public guard with matching access",
			route:  ManifestRoute{Guard: GuardCorePublic, Access: RouteAccessPublic},
			access: RouteAccessPublic, proxyable: true,
		},
		{
			name:   "login guard",
			route:  ManifestRoute{Guard: GuardCoreLogin},
			access: RouteAccessLogin, proxyable: true,
		},
		{
			name:   "permission guard",
			route:  ManifestRoute{Guard: GuardCorePermission, Permission: "topic.create"},
			access: RouteAccessPermission, permission: "topic.create", proxyable: true,
		},
		{
			// 没有权限键的 permission 路由无法裁决：不能降级成「登录即可」。
			name:      "permission guard without a permission key",
			route:     ManifestRoute{Guard: GuardCorePermission},
			proxyable: false,
		},
		{
			// guest 在 legacy 前缀上收紧成「必须已登录」：不放开匿名。
			name:   "guest guard tightens to login",
			route:  ManifestRoute{Guard: GuardCoreGuest},
			access: RouteAccessLogin, proxyable: true,
		},
		{
			name:      "guest guard cannot carry an access value",
			route:     ManifestRoute{Guard: GuardCoreGuest, Access: RouteAccessLogin},
			proxyable: false,
		},
		{
			name:      "inherit guard",
			route:     ManifestRoute{Guard: GuardCoreInherit},
			proxyable: false,
		},
		{
			name:      "raw request core guard",
			route:     ManifestRoute{Guard: GuardCoreRaw},
			proxyable: false,
		},
		{
			// 包自定义守卫（custom / raw_request）：授权由插件守卫代码拥有。
			name:      "package custom guard",
			route:     ManifestRoute{Guard: "acme.guard.custom", Permission: "topic.create"},
			proxyable: false,
		},
		{
			name:      "package custom guard cannot smuggle access",
			route:     ManifestRoute{Guard: "acme.guard.custom", Access: RouteAccessPublic},
			proxyable: false,
		},
		{
			// 早期清单只写 access、不写守卫：access 本身就是声明。
			name:   "legacy access only",
			route:  ManifestRoute{Access: RouteAccessLogin},
			access: RouteAccessLogin, proxyable: true,
		},
		{
			name:   "legacy access only with permission",
			route:  ManifestRoute{Access: RouteAccessPermission, Permission: "topic.create"},
			access: RouteAccessPermission, permission: "topic.create", proxyable: true,
		},
		{
			// 既没有守卫也没有 access：没有声明就没有可信结论（旧行为默认 login）。
			name:      "no guard and no access",
			route:     ManifestRoute{},
			proxyable: false,
		},
		{
			name:      "unknown access value",
			route:     ManifestRoute{Access: "admin"},
			proxyable: false,
		},
		{
			// 守卫与 access 矛盾：声明不可信（v3_validate 要求两者同值）。
			name:      "guard and access disagree",
			route:     ManifestRoute{Guard: GuardCorePublic, Access: RouteAccessLogin},
			proxyable: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			access, permission, proxyable := DeriveRouteAccess(test.route)
			if access != test.access || permission != test.permission || proxyable != test.proxyable {
				t.Fatalf("DeriveRouteAccess(%+v) = (%q, %q, %t); want (%q, %q, %t)",
					test.route, access, permission, proxyable, test.access, test.permission, test.proxyable)
			}
		})
	}
}
