package identityregistry

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// R8-BE-06 剩余一半：角色建议批准者必须自己已持有被建议的权限。
//
// 这里的五类用例对应不变量的五个面：allowed / denied / super_admin /
// deny 覆盖 / reject 仍可用。它们都打真 PostgreSQL：判定在 SQL 函数
// extension_identity_actor_holds_permission 里，Go 侧假 store 证明不了。

const roleSuggestionApprovalPermissionKey = "fixture.identity.profile"

// newRoleSuggestionApprovalFixture 准备一条可批准的建议所需的最小权威状态：
// owner + active declaration + catalog。seedPermissionCatalog 同时把该权限
// 授给 fixture 评审者（见夹具注释），所以默认 actor 是「已持有它的人」。
func newRoleSuggestionApprovalFixture(t *testing.T) *identityRegistryStoreFixture {
	t.Helper()
	fixture := newIdentityRegistryStoreFixture(t)
	fixture.seedOwner(t, "permission", roleSuggestionApprovalPermissionKey)
	fixture.seedDeclaration(t, "permission", roleSuggestionApprovalPermissionKey, 1,
		RegistryStateActive, roleSuggestionApprovalPermissionKey+"@1", "c")
	fixture.seedPermissionCatalog(t, roleSuggestionApprovalPermissionKey,
		roleSuggestionApprovalPermissionKey+"@1", "c", 1)
	return fixture
}

// seedRoleManageOnlyActor 建一个只持 role.manage 的操作者，角色独立于
// identity_reviewer —— 这样它不会顺手继承夹具授予评审者的目标权限。
func seedRoleManageOnlyActor(t *testing.T, fixture *identityRegistryStoreFixture) int64 {
	t.Helper()
	roleKey := fmt.Sprintf("narrow_reviewer_%d", time.Now().UnixNano())
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO roles (key) VALUES ($1)
	`, roleKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, 'role.manage' FROM roles WHERE key = $1
	`, roleKey); err != nil {
		t.Fatal(err)
	}
	var actorID int64
	username := fmt.Sprintf("narrow_reviewer_user_%d", time.Now().UnixNano())
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'Narrow reviewer', 'active')
		RETURNING id
	`, username, username+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = $2
	`, actorID, roleKey); err != nil {
		t.Fatal(err)
	}
	return actorID
}

// seedSuperAdminActor 建一个生效 super_admin 操作者；同时写一条 role.manage 的
// deny 覆盖，证明超级管的「全能」不经过 override 判定（与现有 trigger 语义一致）。
func seedSuperAdminActor(t *testing.T, fixture *identityRegistryStoreFixture) int64 {
	t.Helper()
	var actorID int64
	username := fmt.Sprintf("super_reviewer_%d", time.Now().UnixNano())
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
		VALUES ($1, $1, $2, $2, 'Super reviewer', 'active')
		RETURNING id
	`, username, username+"@example.test").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'super_admin'
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_permission_overrides (user_id, permission_key, effect)
		VALUES ($1, 'role.manage', 'deny')
	`, actorID); err != nil {
		t.Fatal(err)
	}
	return actorID
}

func roleSuggestionSuggestionState(t *testing.T, fixture *identityRegistryStoreFixture, id int64) (string, int64) {
	t.Helper()
	var state string
	var revision int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT approval_state, revision
		FROM extension_permission_role_suggestions
		WHERE id = $1
	`, id).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	return state, revision
}

// 类 1 allowed：操作者经生效角色持有目标权限 → 批准成功并留下完整授权证据。
func TestPostgresStoreDecideRoleSuggestionAllowedWhenActorHoldsPermission(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	suggestionID := fixture.seedSuggestion(t, "member")
	rolePermsBefore := fixture.countRolePermissions(t)
	auditBefore := fixture.countAuditEvents(t)

	approved, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 1,
		ApprovalState: RoleSuggestionApproved, ActorUserID: fixture.actorID,
	})
	if err != nil {
		t.Fatalf("actor that holds the permission must approve: %v", err)
	}
	if approved.ApprovalState != RoleSuggestionApproved || approved.Revision != 2 || !approved.Applied {
		t.Fatalf("approved = %#v", approved)
	}
	if got := fixture.countRolePermissionMapping(t, "member", roleSuggestionApprovalPermissionKey); got != 1 {
		t.Fatalf("member mapping = %d, want 1", got)
	}
	if got := fixture.countRolePermissions(t); got != rolePermsBefore+1 {
		t.Fatalf("role_permissions %d -> %d, want exactly +1", rolePermsBefore, got)
	}
	if got := fixture.countGrants(t); got != 1 {
		t.Fatalf("grants = %d, want 1", got)
	}
	if got := fixture.countAuditEvents(t); got != auditBefore+1 {
		t.Fatalf("audit %d -> %d, want exactly one decision audit", auditBefore, got)
	}
	if approved.DecisionAuditEventID <= 0 || approved.AppliedAuditEventID != approved.DecisionAuditEventID {
		t.Fatalf("approval must retain one decision/grant audit: %#v", approved)
	}
}

// 类 2 denied（核心用例）：只持 role.manage、不持目标权限 → ErrUnauthorized，
// 且事务回滚到「什么都没发生」：建议仍 pending、revision 不变、零 role_permissions、
// 零 grant、零 audit。
func TestPostgresStoreDecideRoleSuggestionDeniedWithoutTargetPermission(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	narrowActorID := seedRoleManageOnlyActor(t, fixture)
	suggestionID := fixture.seedSuggestion(t, "member")
	// 防假绿：先直问 SQL，确认这个操作者是真的不持有目标权限；否则夹具一改
	// （例如以后有人把窄操作者挂到评审者角色上），这条用例会静默变成假绿。
	if holdsRoleSuggestionPermission(t, fixture, narrowActorID) {
		t.Fatal("fixture 意外让窄操作者持有了目标权限，denied 用例会假绿")
	}
	rolePermsBefore := fixture.countRolePermissions(t)
	auditBefore := fixture.countAuditEvents(t)

	if _, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 1,
		ApprovalState: RoleSuggestionApproved, ActorUserID: narrowActorID,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("actor lacking the suggested permission error = %v, want ErrUnauthorized", err)
	}

	state, revision := roleSuggestionSuggestionState(t, fixture, suggestionID)
	if state != RoleSuggestionPending || revision != 1 {
		t.Fatalf("denied approval must leave suggestion pending at revision 1: state=%q revision=%d", state, revision)
	}
	if got := fixture.countRolePermissionMapping(t, "member", roleSuggestionApprovalPermissionKey); got != 0 {
		t.Fatalf("denied approval wrote role_permissions: member mapping=%d", got)
	}
	if got := fixture.countRolePermissions(t); got != rolePermsBefore {
		t.Fatalf("denied approval changed role_permissions: %d -> %d", rolePermsBefore, got)
	}
	if got := fixture.countGrants(t); got != 0 {
		t.Fatalf("denied approval wrote grants: %d", got)
	}
	if got := fixture.countAuditEvents(t); got != auditBefore {
		t.Fatalf("denied approval wrote audit: %d -> %d", auditBefore, got)
	}
}

// 类 3 super_admin：生效 super_admin 不受目标权限限制（即使带 role.manage deny 覆盖）。
func TestPostgresStoreDecideRoleSuggestionSuperAdminBypassesTargetPermission(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	superAdminID := seedSuperAdminActor(t, fixture)
	suggestionID := fixture.seedSuggestion(t, "member")

	approved, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 1,
		ApprovalState: RoleSuggestionApproved, ActorUserID: superAdminID,
	})
	if err != nil {
		t.Fatalf("active super_admin must approve regardless of target permission: %v", err)
	}
	if !approved.Applied || approved.DecidedByUserID != superAdminID {
		t.Fatalf("approved = %#v", approved)
	}
	if got := fixture.countRolePermissionMapping(t, "member", roleSuggestionApprovalPermissionKey); got != 1 {
		t.Fatalf("super_admin approval did not grant mapping: %d", got)
	}
}

// 类 4 deny 覆盖优先：经角色持有目标权限，但该键有 deny override → 拒绝。
func TestPostgresStoreDecideRoleSuggestionDenyOverrideBeatsRoleGrant(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	suggestionID := fixture.seedSuggestion(t, "member")
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_permission_overrides (user_id, permission_key, effect)
		VALUES ($1, $2, 'deny')
	`, fixture.actorID, roleSuggestionApprovalPermissionKey); err != nil {
		t.Fatal(err)
	}
	rolePermsBefore := fixture.countRolePermissions(t)
	auditBefore := fixture.countAuditEvents(t)

	if _, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 1,
		ApprovalState: RoleSuggestionApproved, ActorUserID: fixture.actorID,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("deny override must win over the role grant: error = %v", err)
	}
	state, revision := roleSuggestionSuggestionState(t, fixture, suggestionID)
	if state != RoleSuggestionPending || revision != 1 {
		t.Fatalf("deny-override rejection must leave suggestion pending: state=%q revision=%d", state, revision)
	}
	if got := fixture.countRolePermissions(t); got != rolePermsBefore {
		t.Fatalf("deny-override rejection changed role_permissions: %d -> %d", rolePermsBefore, got)
	}
	if got := fixture.countGrants(t); got != 0 {
		t.Fatalf("deny-override rejection wrote grants: %d", got)
	}
	if got := fixture.countAuditEvents(t); got != auditBefore {
		t.Fatalf("deny-override rejection wrote audit: %d -> %d", auditBefore, got)
	}
}

// 类 5 reject 仍可用：驳回不授予任何权限，所以不持目标权限的操作者也必须能驳回。
func TestPostgresStoreDecideRoleSuggestionRejectWorksWithoutTargetPermission(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	narrowActorID := seedRoleManageOnlyActor(t, fixture)
	suggestionID := fixture.seedSuggestion(t, "member")
	rolePermsBefore := fixture.countRolePermissions(t)
	auditBefore := fixture.countAuditEvents(t)

	rejected, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 1,
		ApprovalState: RoleSuggestionRejected, ActorUserID: narrowActorID,
	})
	if err != nil {
		t.Fatalf("reject must stay available without the target permission: %v", err)
	}
	if rejected.ApprovalState != RoleSuggestionRejected || rejected.Revision != 2 || rejected.Applied {
		t.Fatalf("rejected = %#v", rejected)
	}
	if got := fixture.countRolePermissions(t); got != rolePermsBefore {
		t.Fatalf("reject changed role_permissions: %d -> %d", rolePermsBefore, got)
	}
	if got := fixture.countGrants(t); got != 0 {
		t.Fatalf("reject wrote grants: %d", got)
	}
	if got := fixture.countAuditEvents(t); got != auditBefore+1 {
		t.Fatalf("reject must write exactly one audit event: %d -> %d", auditBefore, got)
	}
	fixture.assertAudit(t, rejected.DecisionAuditEventID, rejected,
		"identity.role_suggestion.reject", boolExpectation(false), false)
}

// holdsRoleSuggestionPermission 直接问 SQL 判定，用来给 denied 类用例做防假绿前置。
func holdsRoleSuggestionPermission(t *testing.T, fixture *identityRegistryStoreFixture, actorID int64) bool {
	t.Helper()
	var holds bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT extension_identity_actor_holds_permission($1, $2)
	`, actorID, roleSuggestionApprovalPermissionKey).Scan(&holds); err != nil {
		t.Fatal(err)
	}
	return holds
}

// actorRoleGrantCount 数操作者经角色得到的目标权限映射数（不含 override）。
func actorRoleGrantCount(t *testing.T, fixture *identityRegistryStoreFixture, actorID int64) int {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*)
		FROM user_roles
		JOIN role_permissions ON role_permissions.role_id = user_roles.role_id
		WHERE user_roles.user_id = $1 AND role_permissions.permission_key = $2
	`, actorID, roleSuggestionApprovalPermissionKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// seedLegacyApprovedSuggestion 造一条 028「只审不批」遗留行：approved、revision 2、
// 无 grant。写入路径与 TestPostgresStoreLegacyApprovedApplyAndPrivacy 一致 ——
// 先关掉不可变 trigger，落库后立刻恢复（cleanup 是失败时的兜底）。
func seedLegacyApprovedSuggestion(
	t *testing.T,
	fixture *identityRegistryStoreFixture,
	roleKey string,
) (suggestionID int64, legacyAuditID int64) {
	t.Helper()
	suggestionID = fixture.seedSuggestion(t, roleKey)
	legacyAuditID = fixture.insertLegacyReviewAudit(t, suggestionID, roleKey)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		ALTER TABLE extension_permission_role_suggestions
		  DISABLE TRIGGER extension_permission_role_suggestion_update_valid
	`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(fixture.ctx, `
			ALTER TABLE extension_permission_role_suggestions
			  ENABLE TRIGGER extension_permission_role_suggestion_update_valid
		`)
	})
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_permission_role_suggestions
		SET approval_state = 'approved',
		    revision = 2,
		    decided_by_user_id = $2,
		    decision_audit_event_id = $3,
		    decided_at = statement_timestamp(),
		    updated_at = statement_timestamp()
		WHERE id = $1
	`, suggestionID, fixture.actorID, legacyAuditID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		ALTER TABLE extension_permission_role_suggestions
		  ENABLE TRIGGER extension_permission_role_suggestion_update_valid
	`); err != nil {
		t.Fatal(err)
	}
	return suggestionID, legacyAuditID
}

type roleSuggestionRow struct {
	State           string
	Revision        int64
	Applied         bool
	DecisionAuditID int64
	AppliedAuditID  int64
	AppliedByUserID int64
}

func loadRoleSuggestionRow(
	t *testing.T,
	fixture *identityRegistryStoreFixture,
	id int64,
) roleSuggestionRow {
	t.Helper()
	var row roleSuggestionRow
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT suggestion.approval_state,
		       suggestion.revision,
		       (grant_row.suggestion_id IS NOT NULL) AS applied,
		       suggestion.decision_audit_event_id,
		       COALESCE(grant_row.applied_audit_event_id, 0),
		       COALESCE(grant_row.applied_by_user_id, 0)
		FROM extension_permission_role_suggestions AS suggestion
		LEFT JOIN extension_permission_role_grants AS grant_row
		  ON grant_row.suggestion_id = suggestion.id
		WHERE suggestion.id = $1
	`, id).Scan(
		&row.State, &row.Revision, &row.Applied,
		&row.DecisionAuditID, &row.AppliedAuditID, &row.AppliedByUserID,
	); err != nil {
		t.Fatal(err)
	}
	return row
}

// 类 6（reviewer P2）：legacy 显式 apply 分支同样插 role_permissions，必须同受不变量约束。
func TestPostgresStoreDecideRoleSuggestionLegacyApplyDeniedWithoutTargetPermission(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	narrowActorID := seedRoleManageOnlyActor(t, fixture)
	suggestionID, legacyAuditID := seedLegacyApprovedSuggestion(t, fixture, "member")

	// 防假绿：窄操作者必须真的不持有目标权限，否则这条用例证明不了任何事。
	if holdsRoleSuggestionPermission(t, fixture, narrowActorID) {
		t.Fatal("fixture 意外让窄操作者持有了目标权限，denied 用例会假绿")
	}
	rolePermsBefore := fixture.countRolePermissions(t)
	auditBefore := fixture.countAuditEvents(t)

	if _, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 2,
		ApprovalState: RoleSuggestionApproved, ActorUserID: narrowActorID,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("legacy apply by actor lacking the permission error = %v, want ErrUnauthorized", err)
	}

	// ① 建议行未被改写：仍 approved / revision 2 / 未应用 / 决定审计仍指回 legacy 行。
	row := loadRoleSuggestionRow(t, fixture, suggestionID)
	if row.State != RoleSuggestionApproved || row.Revision != 2 || row.Applied ||
		row.DecisionAuditID != legacyAuditID || row.AppliedAuditID != 0 || row.AppliedByUserID != 0 {
		t.Fatalf("denied legacy apply rewrote the suggestion row: %#v", row)
	}
	// ② 零 role_permissions：总数不变，member 映射仍为 0（含窄操作者自己的角色）。
	if got := fixture.countRolePermissions(t); got != rolePermsBefore {
		t.Fatalf("denied legacy apply changed role_permissions: %d -> %d", rolePermsBefore, got)
	}
	if got := fixture.countRolePermissionMapping(t, "member", roleSuggestionApprovalPermissionKey); got != 0 {
		t.Fatalf("denied legacy apply granted the target role: member mapping=%d", got)
	}
	// ③ 零 grant。
	if got := fixture.countGrants(t); got != 0 {
		t.Fatalf("denied legacy apply wrote grants: %d", got)
	}
	// ④ 零新 audit（legacy review audit 本身不在增量里）。
	if got := fixture.countAuditEvents(t); got != auditBefore {
		t.Fatalf("denied legacy apply wrote audit: %d -> %d", auditBefore, got)
	}
}

// 类 7：effective-permission 的 override-allow 分支必须单独直测 —— 操作者没有任何
// 角色授予，只靠目标键的 allow override 取得批准资格。
func TestPostgresStoreDecideRoleSuggestionOverrideAllowGrantsApproval(t *testing.T) {
	fixture := newRoleSuggestionApprovalFixture(t)
	overrideActorID := seedRoleManageOnlyActor(t, fixture)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO user_permission_overrides (user_id, permission_key, effect)
		VALUES ($1, $2, 'allow')
	`, overrideActorID, roleSuggestionApprovalPermissionKey); err != nil {
		t.Fatal(err)
	}
	// 证据：资格只来自 override，不含任何角色授予（否则这条测的是角色路径）。
	if got := actorRoleGrantCount(t, fixture, overrideActorID); got != 0 {
		t.Fatalf("override 用例的操作者仍经角色持有目标权限: grants=%d", got)
	}
	if !holdsRoleSuggestionPermission(t, fixture, overrideActorID) {
		t.Fatal("allow override 未被 SQL 判定识别，批准会假红")
	}
	suggestionID := fixture.seedSuggestion(t, "member")
	auditBefore := fixture.countAuditEvents(t)

	approved, err := fixture.store.DecideRoleSuggestion(fixture.ctx, DecideRoleSuggestionInput{
		ID: suggestionID, ExpectedRevision: 1,
		ApprovalState: RoleSuggestionApproved, ActorUserID: overrideActorID,
	})
	if err != nil {
		t.Fatalf("allow-override actor must approve: %v", err)
	}
	if !approved.Applied || approved.DecidedByUserID != overrideActorID {
		t.Fatalf("approved = %#v", approved)
	}
	if got := fixture.countRolePermissionMapping(t, "member", roleSuggestionApprovalPermissionKey); got != 1 {
		t.Fatalf("override-allow approval did not grant the target role: %d", got)
	}
	if got := fixture.countGrants(t); got != 1 {
		t.Fatalf("override-allow approval grants = %d, want 1", got)
	}
	if got := fixture.countAuditEvents(t); got != auditBefore+1 {
		t.Fatalf("override-allow approval audit %d -> %d, want exactly one", auditBefore, got)
	}
}
