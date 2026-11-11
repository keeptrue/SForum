package identity

import "testing"

// TestGrantSourceLiteralOrigin 是 A11/A12 的补测：来源判定用**字面期望**断言。
//
// 刻意不经过 Can() 反推：Can 与 GrantSource 是两条不同的路径（裁决点 vs 只读派生
// 视图），互相印证不能替代各自断言——两边一起错的时候「同构」断言仍然绿。
//
// 不构造「同一键既静态授予又在 Earned 里」的 Actor：装载期不变量（D23/D13，
// mergeEarnedPermissions）排除这个状态，Earned 只记「没有静态授予也生效」的键。
// 给不可能出现的状态写期望，等于把实现细节钉死成契约。
func TestGrantSourceLiteralOrigin(t *testing.T) {
	cases := []struct {
		name       string
		actor      Actor
		permission string
		want       PermissionOrigin
	}{
		{
			name:       "空权限键 → none",
			actor:      Actor{Status: UserStatusActive, Permissions: map[string]bool{PermissionTopicCreate: true}},
			permission: "",
			want:       PermissionOriginNone,
		},
		{
			name:       "未授予的键 → none",
			actor:      Actor{Status: UserStatusActive, Permissions: map[string]bool{PermissionPostCreate: true}},
			permission: PermissionTopicCreate,
			want:       PermissionOriginNone,
		},
		{
			name:       "静态授予 → static",
			actor:      Actor{Status: UserStatusActive, Permissions: map[string]bool{PermissionTopicCreate: true}},
			permission: PermissionTopicCreate,
			want:       PermissionOriginStatic,
		},
		{
			name: "等级挣得（Earned 独占）→ earned",
			actor: Actor{
				Status:      UserStatusActive,
				Permissions: map[string]bool{PermissionAttachmentUpload: true},
				Earned:      map[string]bool{PermissionAttachmentUpload: true},
			},
			permission: PermissionAttachmentUpload,
			want:       PermissionOriginEarned,
		},
		{
			// 装载期不变量（D13）：已挣得键必须并进 Permissions。只填 Earned 的 Actor
			// 是「装载没做完」的畸形状态，派生视图不得把它报成有来源。
			name: "Earned 有键但 Permissions 没有 → none",
			actor: Actor{
				Status:      UserStatusActive,
				Permissions: map[string]bool{},
				Earned:      map[string]bool{PermissionTopicCreate: true},
			},
			permission: PermissionTopicCreate,
			want:       PermissionOriginNone,
		},
		{
			name:       "super_admin 绕过 → super_admin",
			actor:      Actor{Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}},
			permission: PermissionSettingsManage,
			want:       PermissionOriginSuperAdmin,
		},
		{
			name:       "封禁账号 → none（哪怕静态授予了）",
			actor:      Actor{Status: UserStatusBanned, Permissions: map[string]bool{PermissionTopicCreate: true}},
			permission: PermissionTopicCreate,
			want:       PermissionOriginNone,
		},
		{
			name:       "封禁的 super_admin → none（IsActive 先于 IsSuperAdmin）",
			actor:      Actor{Status: UserStatusBanned, RoleKeys: []string{RoleSuperAdmin}},
			permission: PermissionSettingsManage,
			want:       PermissionOriginNone,
		},
		{
			name:       "父权限继承子权限 → static",
			actor:      Actor{Status: UserStatusActive, Permissions: map[string]bool{PermissionSettingsManage: true}},
			permission: PermissionSettingsMailManage,
			want:       PermissionOriginStatic,
		},
		{
			name:       "零值 Actor → none",
			actor:      Actor{},
			permission: PermissionTopicCreate,
			want:       PermissionOriginNone,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.actor.GrantSource(testCase.permission); got != testCase.want {
				t.Fatalf("GrantSource(%q) = %q，期望 %q", testCase.permission, got, testCase.want)
			}
		})
	}
}
