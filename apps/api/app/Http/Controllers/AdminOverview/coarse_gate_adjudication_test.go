package adminoverviewcontroller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// 本文件是 RV-46 的裁决落点与回归钉子：后台 7 条「只有 admin.access 粗门」的
// 路由逐条定案，再把定案钉住。
//
// 为什么在这里：本包持有其中 2 条（overview / resources），其余 5 条所在包不在
// 本轮写权限内；这里只只读引用生成目录（Support/Routes/core_catalog_gen.go）与
// 种子角色模板（Models/Identity/seeds.go），不新增依赖方向。
//
// 裁决口径是「同一读面一致」，不是「按内容敏感度逐条猜」：
//   - 后台页外壳只要求 admin.access，所以同一张页上的首屏数据与实时面板都跟外壳
//     同门；单独收窄一条只会在 200 页面里塞一个 403 面板。
//   - 读面刻意比写面宽的（system-updates / trust-levels）保留差额，并在
//     TestAdminCoarseGateReadFaceStaysWiderThanWriteFace 里钉死，防止后人「统一收紧」。
//   - admin-surfaces 的 admin.access 是下界：目录按 surface.Permission 逐条过滤、
//     invoke 复检 contract.Permission，路由门不是唯一授权。
//
// 因此 7 条全部保留 admin.access。真正的风险不是「门太宽」，而是有人把其中一条
// 收成细粒度键，让现在能打开后台页的内置岗位掉成 403——下面第 3 个测试就是那条
// 防线（operator / tech_admin / moderator / super_admin 的既有可达性）。
var adminCoarseGateVerdicts = []struct {
	routeID   string
	rationale string
}{
	{
		routeID:   "core.route.extensions.list_admin_surfaces",
		rationale: "目录门是下界：listAdminSurfaces 按 surface.Permission 逐条过滤（无声明才对全体后台主体可见）；收成 extension.view 会整片挡掉 operator 的仪表盘小部件与列表列",
	},
	{
		routeID:   "core.route.extensions.invoke_admin_surface",
		rationale: "invoke 复检 contract.Permission 并把 actor 权限整包转发给插件；路由门加细粒度键等于和 surface 声明打架",
	},
	{
		routeID:   "core.route.admin_overview.overview",
		rationale: "后台仪表盘外壳同门（admin.access）；Overview 是这张页的首屏聚合数据",
	},
	{
		routeID:   "core.route.admin_overview.resources",
		rationale: "同一张仪表盘的实时面板（useAdminOverviewLive 轮询）；唯一更细键 settings.site.manage 会造成运营拿宿主遥测、技术运维反而看不到的权限倒挂",
	},
	{
		routeID:   "core.route.system_updates.status",
		rationale: "Service.Status 注释明写 available to every actor allowed into the admin area；出站检查 CheckNow 才是 settings.site.manage",
	},
	{
		routeID:   "core.route.trust_levels.admin_get_trust_levels",
		rationale: "读面刻意比写面宽，TestAdminRoutesRequirePermissions 已断言「有 admin.access 无 trust.manage → 读 200、写 403」",
	},
	{
		routeID:   "core.route.trust_levels.admin_list_rule_conditions",
		rationale: "与信任等级读面同一张编辑器页；写面（PUT / restore-defaults / rule-impact）才是 trust.manage",
	},
}

// 读面刻意保留的差额：写面键必须比读面窄，且读面必须正好是 admin.access。
var adminCoarseGateNarrowWrites = []struct {
	readRouteID  string
	writeRouteID string
	writeKey     string
}{
	{
		readRouteID:  "core.route.system_updates.status",
		writeRouteID: "core.route.system_updates.check",
		writeKey:     "settings.site.manage",
	},
	{
		readRouteID:  "core.route.trust_levels.admin_get_trust_levels",
		writeRouteID: "core.route.trust_levels.admin_update_trust_levels",
		writeKey:     "trust.manage",
	},
	{
		readRouteID:  "core.route.trust_levels.admin_list_rule_conditions",
		writeRouteID: "core.route.trust_levels.admin_restore_trust_level_defaults",
		writeKey:     "trust.manage",
	},
	{
		readRouteID:  "core.route.trust_levels.admin_list_rule_conditions",
		writeRouteID: "core.route.trust_levels.admin_estimate_rule_impact",
		writeKey:     "trust.manage",
	},
}

func TestAdminCoarseGateAdjudicationMatchesCatalog(t *testing.T) {
	catalog := make(map[string]routes.CoreRoute, len(routes.CoreRouteCatalog()))
	seen := make(map[string]int, len(routes.CoreRouteCatalog()))
	for _, route := range routes.CoreRouteCatalog() {
		seen[route.ID]++
		catalog[route.ID] = route
	}

	for _, verdict := range adminCoarseGateVerdicts {
		route, ok := catalog[verdict.routeID]
		if !ok {
			t.Fatalf("%s 不在生成目录里（裁决表与 core_catalog_gen.go 脱节）", verdict.routeID)
		}
		if seen[verdict.routeID] != 1 {
			t.Fatalf("%s 在目录里出现 %d 次，期望恰好 1 次", verdict.routeID, seen[verdict.routeID])
		}
		guard := route.Guard
		if guard.Kind != routes.CoreGuardPermissionAny {
			t.Fatalf("%s 的门类型 = %q，裁决为 permission_any（%s）", verdict.routeID, guard.Kind, verdict.rationale)
		}
		if len(guard.Permissions) != 1 || guard.Permissions[0] != identity.PermissionAdminAccess {
			t.Fatalf("%s 的门权限 = %v，裁决为 [admin.access]（%s）", verdict.routeID, guard.Permissions, verdict.rationale)
		}
		if guard.EvaluatorID != "" {
			t.Fatalf("%s 多了求值器 %q：裁决是纯 permission_any", verdict.routeID, guard.EvaluatorID)
		}
	}
}

// TestAdminCoarseGateReadFaceStaysWiderThanWriteFace 钉住「读宽写窄」这层刻意差额。
//
// 没有这条，把读面「统一收紧」成写面键会让 TestAdminRoutesRequirePermissions 之外
// 的读者（只读管理员）在没有任何报错的情况下丢掉整张页——那时才叫真回归。
func TestAdminCoarseGateReadFaceStaysWiderThanWriteFace(t *testing.T) {
	catalog := make(map[string]routes.CoreRoute, len(routes.CoreRouteCatalog()))
	for _, route := range routes.CoreRouteCatalog() {
		catalog[route.ID] = route
	}

	for _, pair := range adminCoarseGateNarrowWrites {
		read, ok := catalog[pair.readRouteID]
		if !ok {
			t.Fatalf("%s 不在生成目录里", pair.readRouteID)
		}
		write, ok := catalog[pair.writeRouteID]
		if !ok {
			t.Fatalf("%s 不在生成目录里", pair.writeRouteID)
		}
		if len(read.Guard.Permissions) != 1 || read.Guard.Permissions[0] != identity.PermissionAdminAccess {
			t.Fatalf("%s 读面门 = %v，期望 [admin.access]", pair.readRouteID, read.Guard.Permissions)
		}
		if len(write.Guard.Permissions) != 1 || write.Guard.Permissions[0] != pair.writeKey {
			t.Fatalf("%s 写面门 = %v，期望 [%s]", pair.writeRouteID, write.Guard.Permissions, pair.writeKey)
		}
	}
}

// TestBuiltinRolesRetainAdminCoarseGateReachability 是「后台页不许掉成 403」的防线。
//
// 7 条路由的门都是 admin.access，而四个内置岗位都持这个键，所以今天没有任何岗位
// 因为这条裁决失去可达性。谁把某条收成细粒度键，这里就会点名是哪个岗位掉页。
func TestBuiltinRolesRetainAdminCoarseGateReachability(t *testing.T) {
	type roleCase struct {
		roleKey string
		actor   identity.Actor
	}
	roles := make([]roleCase, 0, 4)
	for _, roleKey := range []string{identity.RoleModerator, identity.RoleOperator, identity.RoleTechAdmin} {
		roles = append(roles, roleCase{roleKey: roleKey, actor: builtinRoleActor(t, roleKey)})
	}
	// super_admin 不在种子模板里：它的可达性来自角色键直通。
	roles = append(roles, roleCase{
		roleKey: identity.RoleSuperAdmin,
		actor: identity.Actor{
			ID: 9, Status: identity.UserStatusActive, RoleKeys: []string{identity.RoleSuperAdmin},
		},
	})

	catalog := make(map[string]routes.CoreRoute, len(routes.CoreRouteCatalog()))
	for _, route := range routes.CoreRouteCatalog() {
		catalog[route.ID] = route
	}

	for _, role := range roles {
		if !role.actor.Can(identity.PermissionAdminAccess) {
			t.Fatalf("%s 连 admin.access 都没有，可达性基线不成立", role.roleKey)
		}
		for _, verdict := range adminCoarseGateVerdicts {
			route, ok := catalog[verdict.routeID]
			if !ok {
				t.Fatalf("%s 不在生成目录里", verdict.routeID)
			}
			if !coreGuardAllows(role.actor, route.Guard) {
				t.Fatalf("%s 失去 %s %s：裁决是保留 admin.access，收紧必须点名掉页岗位（%s）",
					role.roleKey, route.Method, route.Path, verdict.rationale)
			}
		}
	}
}

// TestHostTelemetryStaysReachableForTechnicalOps 是权限倒挂的运行期钉子。
//
// 若有人把 /admin/overview/resources 收成 settings.site.manage，技术运维（持
// jobs/database/extension 却没有站点设置权）会立刻 403，而运营拿到宿主遥测——
// 这正是本条裁决拒绝的方向。两条 overview 路由都要在 200 一侧。
func TestHostTelemetryStaysReachableForTechnicalOps(t *testing.T) {
	for _, roleKey := range []string{identity.RoleOperator, identity.RoleTechAdmin} {
		actor := builtinRoleActor(t, roleKey)
		app, cookie := newOverviewTestApp(actor, &overviewHTTPStore{})
		for _, path := range []string{"/api/v1/admin/overview", "/api/v1/admin/overview/resources"} {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.AddCookie(cookie)
			response, err := app.Test(request)
			if err != nil {
				t.Fatalf("%s %s: 请求失败: %v", roleKey, path, err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("%s %s: status = %d，期望 200（仪表盘读面与外壳同门 admin.access）", roleKey, path, response.StatusCode)
			}
		}
	}
}

// builtinRoleActor 用种子角色模板构造一个「真实生效权限」的主体。
// 走 ExpandEffectivePermissions 而不是原始键，父权限展开（settings.manage →
// settings.site.manage 等）才算进去，否则可达性会被人为压低。
func builtinRoleActor(t *testing.T, roleKey string) identity.Actor {
	t.Helper()
	template, ok := identity.RoleTemplateByKey(roleKey)
	if !ok {
		t.Fatalf("找不到内置角色模板 %s", roleKey)
	}
	expanded := identity.ExpandEffectivePermissions(append([]string(nil), template.PermissionKeys...))
	permissions := make(map[string]bool, len(expanded))
	for _, key := range expanded {
		permissions[key] = true
	}
	return identity.Actor{
		ID: 7, Status: identity.UserStatusActive, RoleKeys: []string{roleKey}, Permissions: permissions,
	}
}

// coreGuardAllows 只覆盖裁决表里会出现的门类型；遇到 contextual 之类需要
// 求值器参与的门就显式失败，而不是当成「放行」——静默放行等于把没人裁决过的
// 新门类型混进这条基线里。
func coreGuardAllows(actor identity.Actor, guard routes.CoreGuardDescriptor) bool {
	switch guard.Kind {
	case routes.CoreGuardPublic, routes.CoreGuardLogin, routes.CoreGuardGuest:
		return true
	case routes.CoreGuardSuperAdmin:
		return actor.IsSuperAdmin()
	case routes.CoreGuardPermissionAny:
		if len(guard.Permissions) == 0 {
			return true
		}
		for _, permission := range guard.Permissions {
			if actor.Can(permission) {
				return true
			}
		}
		return false
	}
	return false
}
