package identity

import "strings"

// IsAdminClassPermission 判定一个权限键是否属于「管理类」。
//
// 这是 D6 安全不变量的唯一分类函数：信任等级**永远**不能授予管理能力。它被
// trustlevels 的配置校验与结构测试共用——两处各写一份分类就是「两套语义」。
//
// 判定刻意偏保守：命名约定覆盖现有目录的绝大多数，剩下没有这些后缀但同样是管理
// 能力的键（*_any / lock / pin / permission_override）单独列出。新增权限如果两边
// 都不像，本函数会说「不是管理类」——所以把新键放进 earnable 之前仍要人工过一遍。
func IsAdminClassPermission(key string) bool {
	if key == "" {
		return false
	}
	if key == PermissionAdminAccess {
		return true
	}
	for _, suffix := range []string{
		".manage", ".ban", ".review", ".view", ".view_ip",
		"_any", ".lock", ".pin", ".override", "_override",
	} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

// IsLegacyParentPermission 判定一个权限键是否是「父权限」。
//
// 父键在 Can() 里会隐式展开出一串子权限（permission_compat.go），所以它绝不能
// 进入 earnable 集合：否则一次等级判定会同时打开整组管理能力（D15）。
func IsLegacyParentPermission(key string) bool {
	_, ok := legacyPermissionChildren[key]
	return ok
}
