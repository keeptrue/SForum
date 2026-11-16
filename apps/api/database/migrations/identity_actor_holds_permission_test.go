package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

const (
	actorHoldsPermissionMigration   = "202611170001_identity_actor_holds_permission.sql"
	actorManageRolesMigration       = "202607160029_identity_role_approvals.sql"
	actorManageRolesRepairMigration = "202607170034_identity_role_approvals_schema_repair.sql"
)

// 授权不变量的判定语义只有一份权威实现：extension_identity_actor_can_manage_roles。
// 新函数 extension_identity_actor_holds_permission 换成参数化版本，必须与它逐字同构，
// 否则「role.manage 放行、目标权限不放行」这类分歧会让审批与批准两条路径的授权语义打架。
// 注释靠不住，所以这里把同构变成可执行约束：谁改老函数没同步改新函数，这条测试直接红。
//
// 比较范围是**整段函数定义**：函数头（RETURNS / LANGUAGE / volatile 类别 / SECURITY）
// 与函数体分开比。只比函数体会漏掉 RETURNS TEXT、SECURITY DEFINER 这类纯安全语义改动
// ——那类改动不碰函数体却直接改变谁能执行、以谁的权限执行。
//
// 两条实现约束（刻意不做得更聪明）：
//   - sqlFunctionParts 取每个文件里**第一次**出现的同名函数定义；一个文件里重复定义
//     同一函数会让后一条被静默跳过，所以迁移里目标函数必须只出现一次（当前三份文件都满足）。
//   - 比较在折叠空白之后按字节区分大小写：只要一边把 `LANGUAGE sql` 改成 `LANGUAGE SQL`
//     就会红。这是有意为之 —— 同构要求两边同步编辑，不做大小写无关的模糊匹配。
func TestIdentityActorHoldsPermissionStaysIsomorphicToManageRoles(t *testing.T) {
	holdsHeader, holdsBody := sqlFunctionParts(t, actorHoldsPermissionMigration, holdsFunctionName)
	// 归一化规则：① 两侧都折叠全部空白（换行/缩进/参数间距不参与比较）；
	// ② 函数名与参数列表映射回老函数；③ 函数体里的参数名还原成老函数硬编码的键。
	normalizedHoldsHeader := normalizeSQL(holdsHeader)
	normalizedHoldsHeader = strings.ReplaceAll(normalizedHoldsHeader, holdsFunctionName, legacyFunctionName)
	normalizedHoldsHeader = strings.ReplaceAll(normalizedHoldsHeader,
		"( candidate_user_id BIGINT, candidate_permission_key TEXT )", "(candidate_user_id BIGINT)")
	normalizedHoldsBody := strings.ReplaceAll(normalizeSQL(holdsBody), "candidate_permission_key", "'role.manage'")

	for _, legacyPath := range []string{actorManageRolesMigration, actorManageRolesRepairMigration} {
		legacyHeader, legacyBody := sqlFunctionParts(t, legacyPath, legacyFunctionName)
		if got := normalizeSQL(legacyHeader); normalizedHoldsHeader != got {
			t.Fatalf("%s 与 %s 的函数头已漂移（RETURNS/LANGUAGE/volatility/SECURITY 都算）；"+
				"改一边必须同步改另一边\nholds = %s\nlegacy = %s",
				actorHoldsPermissionMigration, legacyPath, normalizedHoldsHeader, got)
		}
		if got := normalizeSQL(legacyBody); normalizedHoldsBody != got {
			t.Fatalf("%s 与 %s 的函数体已漂移；改一边必须同步改另一边\nholds = %s\nlegacy = %s",
				actorHoldsPermissionMigration, legacyPath, normalizedHoldsBody, got)
		}
	}
}

// 上面那条测试比的是两侧是否一致：如果有人把 SECURITY DEFINER 同时加到两份函数里，
// 同构仍然成立但安全语义已经变坏。这里把函数头的四项硬约束单独钉一次。
func TestIdentityActorHoldsPermissionHeaderStaysPlainSQL(t *testing.T) {
	header, body := sqlFunctionParts(t, actorHoldsPermissionMigration, holdsFunctionName)
	normalizedHeader := normalizeSQL(header)
	normalizedBody := normalizeSQL(body)

	if !strings.Contains(normalizedHeader,
		"FUNCTION extension_identity_actor_holds_permission( candidate_user_id BIGINT, candidate_permission_key TEXT )") {
		t.Fatalf("新函数必须显式接收候选权限键:\n%s", normalizedHeader)
	}
	for _, want := range []string{"RETURNS BOOLEAN", "LANGUAGE sql", "STABLE"} {
		if !strings.Contains(normalizedHeader, want) {
			t.Fatalf("函数头缺少 %q:\n%s", want, normalizedHeader)
		}
	}
	// SECURITY DEFINER 会让函数以属主身份运行，等于绕过调用者的权限边界 —— 这份判定
	// 必须永远以调用者的权限求值。
	if strings.Contains(normalizedHeader, "SECURITY DEFINER") {
		t.Fatalf("授权判定函数不得声明 SECURITY DEFINER:\n%s", normalizedHeader)
	}

	for _, want := range []string{
		"AND permission_key = candidate_permission_key AND effect = 'deny'",
		"AND permission_key = candidate_permission_key AND effect = 'allow'",
		"AND role_permissions.permission_key = candidate_permission_key",
	} {
		if !strings.Contains(normalizedBody, want) {
			t.Fatalf("函数体缺少参数化使用点 %q:\n%s", want, normalizedBody)
		}
	}
	if strings.Contains(normalizedBody, "'role.manage'") {
		t.Fatalf("新函数体不得硬编码 'role.manage':\n%s", normalizedBody)
	}
}

const (
	holdsFunctionName  = "extension_identity_actor_holds_permission"
	legacyFunctionName = "extension_identity_actor_can_manage_roles"
)

// sqlFunctionParts 把一份函数定义拆成「函数头」（从 FUNCTION <name>( 到 AS $$）
// 与「函数体」（AS $$ 与闭合 $$ 之间）。函数头覆盖 RETURNS / LANGUAGE /
// IMMUTABLE|STABLE|VOLATILE / SECURITY DEFINER|INVOKER / PARALLEL 等全部属性。
//
// 只取文件里第一次出现的同名定义（见上方测试注释里的约束）。
func sqlFunctionParts(t *testing.T, path string, functionName string) (header string, body string) {
	t.Helper()
	raw, err := fs.ReadFile(Files(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(raw)
	start := strings.Index(src, "FUNCTION "+functionName+"(")
	if start < 0 {
		t.Fatalf("%s 没有定义 %s", path, functionName)
	}
	asStart := strings.Index(src[start:], "AS $$")
	if asStart < 0 {
		t.Fatalf("%s 的 %s 缺少 AS $$ 函数体", path, functionName)
	}
	headerEnd := start + asStart
	bodyStart := headerEnd + len("AS $$")
	end := strings.Index(src[bodyStart:], "$$")
	if end < 0 {
		t.Fatalf("%s 的 %s 函数体没有闭合 $$", path, functionName)
	}
	return src[start:headerEnd], src[bodyStart : bodyStart+end]
}

// normalizeSQL 折叠全部空白，让缩进/换行不参与逐字比较。
func normalizeSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
