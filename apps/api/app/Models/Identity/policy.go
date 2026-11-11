package identity

func (a Actor) IsActive() bool {
	return a.Status == UserStatusActive
}

func (a Actor) IsSuperAdmin() bool {
	if !a.IsActive() {
		return false
	}
	for _, roleKey := range a.RoleKeys {
		if roleKey == RoleSuperAdmin {
			return true
		}
	}
	return false
}

func (a Actor) Can(permission string) bool {
	if !a.IsActive() {
		return false
	}
	// 注意（L6）：super_admin 在此处直接返回 true，绕过 DB 层的 deny 覆盖逻辑。
	// 这是设计决策——super_admin 在权限系统内是全能的，无法通过 deny 覆盖限制。
	// 运维若想限制某账户，应改用非 super_admin 角色 + deny 覆盖，而非对 super_admin 设 deny。
	if a.IsSuperAdmin() {
		return true
	}
	if a.Permissions == nil {
		return false
	}
	// 细粒度子权限可通过旧父权限继承（settings.manage → settings.mail.manage 等）。
	//
	// 信任等级的已挣得来源不在这里合并：它在装载期并进 Permissions
	// （postgres_store_roles.go 的 applyTrustLevel，D13），所以这个函数零改动，
	// 也不存在「先命中 Permissions == nil 早退」的坑（T13）。
	return hasExpandedPermission(a.Permissions, permission)
}

func CanEditPost(actor Actor, post PostSummary) bool {
	if actor.Can(PermissionPostEditAny) {
		return true
	}
	return post.AuthorUserID == actor.ID && actor.Can(PermissionPostEditOwn)
}
