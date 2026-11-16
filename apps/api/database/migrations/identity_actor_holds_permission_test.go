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
func TestIdentityActorHoldsPermissionStaysIsomorphicToManageRoles(t *testing.T) {
	holds := normalizeSQL(sqlFunctionBody(t, actorHoldsPermissionMigration, "extension_identity_actor_holds_permission"))
	// 归一化规则：① 两侧都折叠全部空白（换行/缩进/多余空格不参与比较）；
	// ② 把新函数的参数名还原成老函数里硬编码的键，使两份函数体落到同一坐标系。
	normalizedHolds := strings.ReplaceAll(holds, "candidate_permission_key", "'role.manage'")

	for _, legacyPath := range []string{actorManageRolesMigration, actorManageRolesRepairMigration} {
		legacy := normalizeSQL(sqlFunctionBody(t, legacyPath, "extension_identity_actor_can_manage_roles"))
		if normalizedHolds != legacy {
			t.Fatalf("%s 与 %s 的函数体已漂移；改一边必须同步改另一边\nholds = %s\nlegacy = %s",
				actorHoldsPermissionMigration, legacyPath, normalizedHolds, legacy)
		}
	}
}

// 上面那条测试只比函数体：如果实现忘了参数化（仍硬编码 'role.manage'），参数名替换
// 之后也能「同构」通过。所以签名与三个使用点要单独钉一次。
func TestIdentityActorHoldsPermissionTakesPermissionKeyParameter(t *testing.T) {
	body, err := fs.ReadFile(Files(), actorHoldsPermissionMigration)
	if err != nil {
		t.Fatal(err)
	}
	normalizedUp := normalizeSQL(upSection(string(body)))
	if !strings.Contains(normalizedUp,
		"FUNCTION extension_identity_actor_holds_permission( candidate_user_id BIGINT, candidate_permission_key TEXT )") {
		t.Fatalf("新函数必须显式接收候选权限键:\n%s", normalizedUp)
	}

	normalizedBody := normalizeSQL(sqlFunctionBody(t, actorHoldsPermissionMigration, "extension_identity_actor_holds_permission"))
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

// sqlFunctionBody 取某个 SQL 函数 AS $$ ... $$ 之间的函数体，空白不归一化
// （调用方决定归一化规则）。
func sqlFunctionBody(t *testing.T, path string, functionName string) string {
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
	bodyStart := start + asStart + len("AS $$")
	end := strings.Index(src[bodyStart:], "$$")
	if end < 0 {
		t.Fatalf("%s 的 %s 函数体没有闭合 $$", path, functionName)
	}
	return src[bodyStart : bodyStart+end]
}

func upSection(body string) string {
	if index := strings.Index(body, "-- +goose Down"); index >= 0 {
		return body[:index]
	}
	return body
}

// normalizeSQL 折叠全部空白，让缩进/换行不参与逐字比较。
func normalizeSQL(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
