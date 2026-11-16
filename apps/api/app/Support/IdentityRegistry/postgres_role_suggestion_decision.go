package identityregistry

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// decideRoleSuggestionOnce owns one serializable role-suggestion decision.
// The retry policy and ambiguous-commit readback remain with the repository.
func (s *PostgresStore) decideRoleSuggestionOnce(
	ctx context.Context,
	input DecideRoleSuggestionInput,
	approvalState string,
	action string,
) (RoleSuggestion, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return RoleSuggestion{}, mapStoreError(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Suggestion is the CAS root. Exact artifact/owner locks then serialize this
	// approval with disable, upgrade, and uninstall publication.
	current, err := lockRoleSuggestion(ctx, tx, input.ID)
	if err != nil {
		return RoleSuggestion{}, err
	}

	// Terminal rows: exact replay returns the durable result; legacy approved
	// without grant evidence may be applied with expected revision 2.
	if current.ApprovalState != RoleSuggestionPending {
		updated, terminalErr := decideTerminalRoleSuggestion(ctx, tx, current, input, approvalState, action)
		if terminalErr != nil {
			return RoleSuggestion{}, terminalErr
		}
		if err := tx.Commit(ctx); err != nil {
			return s.readbackRoleSuggestionDecision(ctx, input, approvalState, err)
		}
		return updated, nil
	}

	if current.Revision != input.ExpectedRevision {
		return RoleSuggestion{}, ErrRevisionConflict
	}
	// Approval consumes live extension authority; rejection only closes a Host
	// recommendation and must remain possible after disable or uninstall.
	if approvalState == RoleSuggestionApproved {
		if err := lockActiveRoleSuggestionArtifact(ctx, tx, current); err != nil {
			return RoleSuggestion{}, err
		}
	}
	// Actor uses KEY SHARE only and is checked before role_permissions writes so
	// concurrent role replacement cannot deadlock on users then mappings.
	if err := lockAndAuthorizeRoleSuggestionActor(ctx, tx, input.ActorUserID); err != nil {
		return RoleSuggestion{}, err
	}

	rolePermissionAdded := false
	roleGrantApplied := false
	var roleID int64
	if approvalState == RoleSuggestionApproved {
		roleID, err = lockRoleSuggestionTarget(ctx, tx, current.RoleKey)
		if err != nil {
			return RoleSuggestion{}, err
		}
		if err := requireRoleSuggestionCatalog(ctx, tx, current); err != nil {
			return RoleSuggestion{}, err
		}
		// 授予不变量：批准者必须自己已持有该权限（R8-BE-06）。位置刻意放在目标角色与
		// catalog 校验之后、写 role_permissions 之前 —— 无效/未登记目标仍以 target
		// conflict 收场，不把「请求不成立」改写成「无权限」；而失败路径到此为止，
		// 不写 role_permissions、不写 grant、不写 audit，建议行保持 pending。
		// 驳回不走这里：驳回不授予任何权限，必须永远可用。
		if err := requireRoleSuggestionActorHoldsPermission(
			ctx, tx, input.ActorUserID, current.PermissionKey,
		); err != nil {
			return RoleSuggestion{}, err
		}
		rolePermissionAdded, err = addRoleSuggestionPermission(ctx, tx, roleID, current.PermissionKey)
		if err != nil {
			return RoleSuggestion{}, err
		}
		roleGrantApplied = true
	}

	auditEventID, err := insertRoleSuggestionAuditEvent(
		ctx, tx, action, input.ActorUserID, current, approvalState,
		input.ExpectedRevision, rolePermissionAdded, roleGrantApplied,
	)
	if err != nil {
		return RoleSuggestion{}, err
	}
	if roleGrantApplied {
		if err := insertRoleSuggestionGrant(
			ctx, tx, current, roleID, input.ActorUserID, auditEventID,
		); err != nil {
			return RoleSuggestion{}, err
		}
	}

	updated, err := casDecideRoleSuggestion(
		ctx,
		tx,
		input.ID,
		input.ExpectedRevision,
		approvalState,
		input.ActorUserID,
		auditEventID,
	)
	if err != nil {
		return RoleSuggestion{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return s.readbackRoleSuggestionDecision(ctx, input, approvalState, err)
	}
	return updated, nil
}
