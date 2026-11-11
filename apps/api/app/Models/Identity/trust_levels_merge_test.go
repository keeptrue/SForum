package identity

import (
	"fmt"
	"slices"
	"testing"

	store "github.com/keeptrue/sforum/apps/api/database/sqlc"
)

// TestDeniedPermissionKeysIncludesDenyOnlyKeys 锁定 D28 的 SQL 形状前提：
// 被 deny 但**没有任何静态授予**的键必须出现在决策集里。
//
// 这条是安全公式 `effective = Expand(static) ∪ (earned ∖ denied)` 的地基：
// 如果只拿到「候选集内」的 deny，信任等级挣得该键时 deny 会被绕过。
func TestDeniedPermissionKeysIncludesDenyOnlyKeys(t *testing.T) {
	decisions := []store.ListUserPermissionsRow{
		{Key: "topic.create", Effective: false},    // 只被 deny，无任何静态授予
		{Key: "post.create", Effective: true},      // 角色授予
		{Key: "attachment.upload", Effective: false}, // 同上，deny-only
	}
	denied := deniedPermissionKeys(decisions)
	for _, key := range []string{"topic.create", "attachment.upload"} {
		if !denied[key] {
			t.Fatalf("deny-only 键 %q 没进 denied 集合（D28 的 SQL 会漏掉它）", key)
		}
	}
	if denied["post.create"] {
		t.Fatal("生效的键被当成 denied")
	}
	if got := effectivePermissionKeys(decisions); !slices.Equal(got, []string{"post.create"}) {
		t.Fatalf("effectivePermissionKeys = %v", got)
	}
}

// TestMergeEarnedPermissionsHonoursDeny 是 T4 的核心断言：
// 被 deny 的键不能被已挣得来源复活。
//
// 构造刻意用「deny 一个没有任何静态授予、只靠 earned 才可能拿到的键」：
// 用「角色授予 + deny」构造会假绿——那种键在 DB 层就被删出候选集，走不到合并。
func TestMergeEarnedPermissionsHonoursDeny(t *testing.T) {
	decisions := []store.ListUserPermissionsRow{
		{Key: "topic.create", Effective: false},
	}
	current := &CurrentUser{ID: 7, Permissions: []string{"post.create"}}
	current.denied = deniedPermissionKeys(decisions)

	earned := mergeEarnedPermissions(current, []string{"topic.create", "attachment.upload"})

	if slices.Contains(current.Permissions, "topic.create") {
		t.Fatal("被 deny 的 topic.create 被已挣得集合复活了")
	}
	if earned["topic.create"] {
		t.Fatal("被 deny 的键被记成 earned 独占来源")
	}
	if !slices.Contains(current.Permissions, "attachment.upload") {
		t.Fatalf("未被 deny 的 attachment.upload 没有并入有效集：%v", current.Permissions)
	}
	if !earned["attachment.upload"] {
		t.Fatal("attachment.upload 应被记成 earned 独占来源")
	}
	// earned 只记独占键：静态已授予的 post.create 不在其中。
	if earned["post.create"] {
		t.Fatal("静态已授予的键被记成 earned 来源")
	}
	// 有效集保持稳定排序。
	if !slices.IsSorted(current.Permissions) {
		t.Fatalf("有效集未排序：%v", current.Permissions)
	}
}

// TestMergeEarnedPermissionsSkipsStaticKeys 锁定 D23：同一键既静态授予又可由等级
// 解锁时，来源记 static（没有等级也能用），Earned 里不出现。
func TestMergeEarnedPermissionsSkipsStaticKeys(t *testing.T) {
	current := &CurrentUser{ID: 7, Permissions: []string{"topic.create"}}
	current.denied = map[string]bool{}
	earned := mergeEarnedPermissions(current, []string{"topic.create"})
	if earned != nil {
		t.Fatalf("静态已授予的键被记成 earned：%v", earned)
	}
	if len(current.Permissions) != 1 {
		t.Fatalf("有效集被重复追加：%v", current.Permissions)
	}
}

// TestGrantSourceMatchesCan 锁定派生视图与裁决点严格同构（D30）：
// origin != none 当且仅当 Can() 为真。
func TestGrantSourceMatchesCan(t *testing.T) {
	cases := []struct {
		name       string
		actor      Actor
		permission string
		wantCan    bool
		wantOrigin PermissionOrigin
	}{
		{
			name:       "零值 Actor 什么都不授予",
			actor:      Actor{},
			permission: "topic.create",
			wantOrigin: PermissionOriginNone,
		},
		{
			name:       "静态授予",
			actor:      Actor{Status: UserStatusActive, Permissions: map[string]bool{"topic.create": true}},
			permission: "topic.create",
			wantCan:    true,
			wantOrigin: PermissionOriginStatic,
		},
		{
			name: "已挣得独占",
			actor: Actor{
				Status:      UserStatusActive,
				Permissions: map[string]bool{"attachment.upload": true},
				Earned:      map[string]bool{"attachment.upload": true},
			},
			permission: "attachment.upload",
			wantCan:    true,
			wantOrigin: PermissionOriginEarned,
		},
		{
			name: "同时被静态与等级授予 → static",
			actor: Actor{
				Status:      UserStatusActive,
				Permissions: map[string]bool{"topic.create": true},
				Earned:      map[string]bool{},
			},
			permission: "topic.create",
			wantCan:    true,
			wantOrigin: PermissionOriginStatic,
		},
		{
			name:       "super_admin 绕过",
			actor:      Actor{Status: UserStatusActive, RoleKeys: []string{RoleSuperAdmin}},
			permission: "settings.manage",
			wantCan:    true,
			wantOrigin: PermissionOriginSuperAdmin,
		},
		{
			name:       "封禁账号什么都不授予",
			actor:      Actor{Status: UserStatusBanned, Permissions: map[string]bool{"topic.create": true}},
			permission: "topic.create",
			wantOrigin: PermissionOriginNone,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.actor.Can(testCase.permission); got != testCase.wantCan {
				t.Fatalf("Can() = %v，期望 %v", got, testCase.wantCan)
			}
			origin := testCase.actor.GrantSource(testCase.permission)
			if origin != testCase.wantOrigin {
				t.Fatalf("GrantSource() = %q，期望 %q", origin, testCase.wantOrigin)
			}
			// 同构断言：两处判定不得分叉。
			if (origin != PermissionOriginNone) != testCase.wantCan {
				t.Fatalf("GrantSource 与 Can 分叉：origin=%q can=%v", origin, testCase.wantCan)
			}
		})
	}
}

// TestActorZeroValueHasNoEarned 锁定 T2：零值 Actor 等价于「无已挣得权限」。
func TestActorZeroValueHasNoEarned(t *testing.T) {
	var actor Actor
	if len(actor.Earned) != 0 {
		t.Fatalf("零值 Actor 带 earned：%v", actor.Earned)
	}
	if actor.TrustLevelResolved {
		t.Fatal("零值 Actor 不应是「已解析」")
	}
	if actor.Can("topic.create") {
		t.Fatal("零值 Actor 不应授予任何能力")
	}
}

// TestCanTruthTable 是 A 组的 4×4 真值表：
// 静态授予 × 已挣得 × deny 覆盖 × super_admin，16 种组合全枚举。
//
// 构造遵循装载期的不变量（D23/D14）：Earned 只含「没有静态授予也生效」的键；
// 被 deny 的键既不在 Permissions 也不在 Earned。也就是说这 16 行都是**装载后可能
// 真实出现的状态**，不是拼出来的不可能状态。
func TestCanTruthTable(t *testing.T) {
	const permission = "topic.create"
	combinations := 0
	for _, isStatic := range []bool{false, true} {
		for _, isEarned := range []bool{false, true} {
			for _, isDenied := range []bool{false, true} {
				for _, isSuperAdmin := range []bool{false, true} {
					combinations++
					name := fmt.Sprintf("static=%v/earned=%v/deny=%v/super=%v", isStatic, isEarned, isDenied, isSuperAdmin)
					actor := Actor{Status: UserStatusActive, Permissions: map[string]bool{}}
					if isSuperAdmin {
						actor.RoleKeys = []string{RoleSuperAdmin}
					}
					// deny 压过一切：被 deny 的键在装载期就被排除出两个集合。
					effectiveStatic := isStatic && !isDenied
					effectiveEarned := isEarned && !isDenied && !isStatic
					if effectiveStatic || effectiveEarned {
						actor.Permissions[permission] = true
					}
					if effectiveEarned {
						actor.Earned = map[string]bool{permission: true}
					}

					wantCan := isSuperAdmin || effectiveStatic || effectiveEarned
					wantOrigin := PermissionOriginNone
					switch {
					case isSuperAdmin:
						wantOrigin = PermissionOriginSuperAdmin
					case effectiveStatic:
						wantOrigin = PermissionOriginStatic
					case effectiveEarned:
						wantOrigin = PermissionOriginEarned
					}

					if got := actor.Can(permission); got != wantCan {
						t.Fatalf("%s: Can() = %v，期望 %v", name, got, wantCan)
					}
					if got := actor.GrantSource(permission); got != wantOrigin {
						t.Fatalf("%s: GrantSource() = %q，期望 %q", name, got, wantOrigin)
					}
					if (wantOrigin != PermissionOriginNone) != wantCan {
						t.Fatalf("%s: 期望值自身不一致", name)
					}
				}
			}
		}
	}
	if combinations != 16 {
		t.Fatalf("枚举了 %d 种组合，期望 16", combinations)
	}
}
