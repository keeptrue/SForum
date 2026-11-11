package identity

// PermissionOrigin 解释一个能力当前由哪个来源授予。
//
// 它是 Actor.Can 的**只读派生视图**：只能解释结论，不能反过来影响结论。
// 需要区分来源的只有两类消费者——前端解释「为什么我不能发帖」、后台显示
// 「这个能力谁能给、从哪来」。任何裁决点仍然只问 Can()。
//
// 命名刻意避开 CapabilitySource：`hostapi.CapabilitySource` 管的是插件能力集合与
// secret 写门禁，同名会让「按名字搜索」的人把两者混为一谈（D30）。
type PermissionOrigin string

const (
	// PermissionOriginNone 表示没有任何来源授予该能力。
	PermissionOriginNone PermissionOrigin = "none"
	// PermissionOriginSuperAdmin 表示 super_admin 绕过（Can 无条件为真）。
	PermissionOriginSuperAdmin PermissionOrigin = "super_admin"
	// PermissionOriginStatic 表示静态授予：角色授予或用户 allow 覆盖。
	PermissionOriginStatic PermissionOrigin = "static"
	// PermissionOriginEarned 表示由信任等级解锁（已挣得来源，纯加性）。
	PermissionOriginEarned PermissionOrigin = "earned"
)

// GrantSource 返回该能力的来源。
//
// 与 Can() 严格同构：origin != PermissionOriginNone 当且仅当 Can() 为真。
// 这条等价关系由 policy_test.go 的表驱动断言守住——两处判定一旦分叉，前端就会
// 解释出一个后端并不认的结论。
//
// 静态来源刻意不区分「角色授予」与「用户 allow 覆盖」：DB 层已经把两者合并成
// 同一份权限列表（并且已经排除了 deny），要区分必须多一次查询，不值得放在
// 每个已认证请求的热路径上。
//
// 顺序：super_admin → 静态/已挣得。已挣得键也并进了 Permissions（D13），所以两个
// 来源共用同一个外层分支，靠 `Actor.Earned`（只记「没有静态授予也生效」的键）区分。
func (a Actor) GrantSource(permission string) PermissionOrigin {
	if permission == "" || !a.IsActive() {
		return PermissionOriginNone
	}
	if a.IsSuperAdmin() {
		return PermissionOriginSuperAdmin
	}
	if a.Permissions != nil && hasExpandedPermission(a.Permissions, permission) {
		// 这个分支**可达**，而且是 earned 来源的唯一出口：已挣得键在装载期被并进
		// Permissions，所以它也满足 hasExpandedPermission；只有它在 Earned 里时才说明
		// 「没有静态授予，是靠等级挣到的」。删掉它会退化成所有能力都报 static。
		// 可达性有两处证据，**不要**因为「看着不可达」删掉这个内层 if：
		//   - permission_origin_test.go 的 TestGrantSourceLiteralOrigin
		//     用例「等级挣得（Earned 独占）→ earned」：Permissions 与 Earned 同时含该键
		//     （装载期不变量的真实形状），删掉内层 if 它会红；
		//   - trust_levels_merge_test.go 的「已挣得独占」用例（Can 侧同构）。
		// 另外 TestGrantSourceIsStaticForDefaultInstallWhitelistKeys 证明默认安装下
		// 这条分支**不**命中（七键都是 static），两个方向都有断言。
		if a.Earned[permission] {
			return PermissionOriginEarned
		}
		return PermissionOriginStatic
	}
	return PermissionOriginNone
}
