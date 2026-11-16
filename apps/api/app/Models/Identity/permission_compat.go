package identity

import "slices"

// 旧版“大包”权限 → 细粒度子权限。
// 持有父权限时：Can(子权限) 通过，且会话权限列表会展开出子权限（供前端菜单使用）。
// 不反向：仅有子权限时 Can(父权限) 仍为 false，避免邮件运营误得全站设置权。
var legacyPermissionChildren = map[string][]string{
	PermissionSettingsManage: {
		PermissionSettingsSiteManage,
		PermissionSettingsMailManage,
		PermissionSettingsAvatarManage,
		PermissionSettingsAppearanceManage,
		PermissionSettingsNotificationsManage,
		PermissionForumSettingsManage,
		PermissionEntityMetaManage,
	},
	PermissionExtensionManage: {
		PermissionExtensionView,
		PermissionExtensionPluginManage,
		PermissionExtensionThemeManage,
	},
	// user.manage 仅展开只读视图，不展开 user.permission_override：
	// 个人权限例外是高危能力，必须显式单独授予，不能由运营父权限继承。
	PermissionUserManage: {
		PermissionUserView,
	},
}

// ExpandEffectivePermissions 在角色/覆盖结果上展开父权限对应的子权限。
// 用于写入会话与后台展示的有效权限列表。
//
// denied 是被显式 deny 的键（CurrentUser.denied 的来源）。展开必须跳过它们：
// 先按 effective 过滤再无条件补全子权限，会把「管理员 deny 掉的子权限」重新加回
// 集合，覆盖写入成功、后台显示已保存，而 Can() 仍然放行（P1-1）。
func ExpandEffectivePermissions(keys []string, denied map[string]bool) []string {
	set := make(map[string]bool, len(keys)+16)
	for _, key := range keys {
		if key == "" {
			continue
		}
		set[key] = true
	}
	for parent, children := range legacyPermissionChildren {
		if !set[parent] {
			continue
		}
		for _, child := range children {
			if denied[child] {
				// 显式 deny 压过父权限展开。
				continue
			}
			set[child] = true
		}
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// applyDenied 把显式 deny 的键以 `false` 写进权限 map。
//
// 「显式 false 条目」是 deny 在 Actor.Permissions 里的唯一表示：**只写不删**。
// 删掉等于「从未授予」，父权限回退会把子权限重新放行（P1-1）。
//
// 传播面并不自动：按 value 语义读 map 的通道（core guard 片段、dispatch 快照、
// 协议 v2 调用者投影）会把 false 读成「被拒绝」，但**重建** map 的通道必须显式
// 保留 false 条目，否则 deny 在那一层被丢掉 —— APITokens.RestrictActor 就是
// 这样一个点（它只装命中 scope 的键，PAT 主体会只剩父权限键，Can(子权限) 又走
// 父回退放行，见该函数里的保留循环）。
func applyDenied(perms map[string]bool, denied map[string]bool) {
	for key := range denied {
		if key == "" {
			continue
		}
		perms[key] = false
	}
}

// hasExpandedPermission 判断 actor 是否直接持有 permission，或通过父权限继承。
func hasExpandedPermission(perms map[string]bool, permission string) bool {
	// 显式条目优先于一切继承：值为 false 的条目就是 deny，父权限不得把它盖回来。
	if allowed, ok := perms[permission]; ok {
		return allowed
	}
	for parent, children := range legacyPermissionChildren {
		if !perms[parent] {
			continue
		}
		if slices.Contains(children, permission) {
			return true
		}
	}
	return false
}
