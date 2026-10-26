package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandIdentityUserStatusSetID          = "sforum.identity.user.status.set"
	CommandIdentityUserStatusSetVersion     = "1"
	CommandIdentityUserStatusInputSchemaID  = "sforum.identity.user.status.input"
	CommandIdentityUserStatusOutputSchemaID = "sforum.identity.user.status.result"
	CommandIdentityUserStatusSchemaVersion  = "1"
)

type protocolV2IdentityUserStatusInput struct {
	UserID string `json:"userId"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type protocolV2IdentityUserStatusMutation struct {
	userID           int64
	status           identity.UserStatus
	expectedRevision int64
	reason           string
}

func newProtocolV2IdentityUserStatusCommandDefinition() protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandIdentityUserStatusSetID, Version: CommandIdentityUserStatusSetVersion,
		InputSchemaID: CommandIdentityUserStatusInputSchemaID, InputSchemaVersion: CommandIdentityUserStatusSchemaVersion,
		OutputSchemaID: CommandIdentityUserStatusOutputSchemaID, OutputSchemaVersion: CommandIdentityUserStatusSchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionUserManage},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2IdentityUserStatusMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2IdentityUserStatusPreparation(mutation, nil)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2IdentityUserStatusMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		plan, err := identity.PrepareUserStatusTx(ctx, tx, identity.UserStatusMutationInput{
			ActorUserID: actorUserID, TargetUserID: mutation.userID, Status: mutation.status,
			ExpectedRevision: mutation.expectedRevision, Reason: mutation.reason,
		})
		if err != nil {
			return nil, protocolV2IdentityCommandError(err)
		}
		return protocolV2IdentityUserStatusPreparation(mutation, &plan)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2IdentityUserStatusMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		result, err := identity.SetUserStatusTx(ctx, tx, identity.UserStatusMutationInput{
			ActorUserID: actorUserID, TargetUserID: mutation.userID, Status: mutation.status,
			ExpectedRevision: mutation.expectedRevision, Reason: mutation.reason,
		})
		if err != nil {
			return nil, protocolV2IdentityCommandError(err)
		}
		output, err := protocolV2IdentityUserStatusDocument(result, false)
		if err != nil {
			return nil, fmt.Errorf("encode user status command result: %w", err)
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: strconv.FormatInt(result.Revision, 10)}, nil
	}
	return definition
}

func protocolV2IdentityUserStatusMutationFromRequest(request *hostv2.CommandRequest) (protocolV2IdentityUserStatusMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2IdentityUserStatusInput](request)
	if err != nil {
		return protocolV2IdentityUserStatusMutation{}, err
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(input.UserID), 10, 64)
	revision, revisionErr := strconv.ParseInt(strings.TrimSpace(request.GetExpectedRevision()), 10, 64)
	status := identity.UserStatus(strings.TrimSpace(input.Status))
	reason := strings.TrimSpace(input.Reason)
	if err != nil || userID <= 0 || revisionErr != nil || revision < 0 ||
		(status != identity.UserStatusActive && status != identity.UserStatusDisabled) || len(reason) > 200 {
		return protocolV2IdentityUserStatusMutation{}, invalidProtocolV2DomainCommandInput()
	}
	return protocolV2IdentityUserStatusMutation{userID: userID, status: status, expectedRevision: revision, reason: reason}, nil
}

func protocolV2IdentityUserStatusPreparation(
	mutation protocolV2IdentityUserStatusMutation,
	plan *identity.UserStatusMutationPlan,
) (*protocolV2CommandPreparation, error) {
	projected := identity.UserStatusMutationResult{
		UserID: mutation.userID, Status: mutation.status, Revision: mutation.expectedRevision,
	}
	if plan != nil {
		projected.PreviousStatus = plan.PreviousStatus
		projected.Revision = plan.CurrentRevision
	}
	document, err := protocolV2IdentityUserStatusDocument(projected, true)
	if err != nil {
		return nil, err
	}
	resourceID := strconv.FormatInt(mutation.userID, 10)
	impacts := []*hostv2.ImpactItem{{
		Module: "identity", Action: "set_status", ResourceType: "user", ResourceId: resourceID,
		Summary: "Change one user account status and token revision.", Reversible: true,
	}}
	if mutation.status == identity.UserStatusDisabled {
		impacts = append(impacts, &hostv2.ImpactItem{
			Module: "identity", Action: "revoke_sessions", ResourceType: "user_sessions", ResourceId: resourceID,
			Summary: "Revoke all active sessions for the disabled account.", Reversible: false,
		})
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.identity.user_status@1", ResourceId: resourceID, Allowed: true,
			Reason: "The delegated actor must retain user.manage and protected-account rules are rechecked in the transaction.",
		}},
		Impact: impacts, ProjectedResult: document,
	}, nil
}

func protocolV2IdentityUserStatusDocument(result identity.UserStatusMutationResult, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandIdentityUserStatusOutputSchemaID, CommandIdentityUserStatusSchemaVersion, map[string]any{
		"planned": planned, "userId": strconv.FormatInt(result.UserID, 10),
		"previousStatus": string(result.PreviousStatus), "status": string(result.Status),
		"revision":        strconv.FormatInt(result.Revision, 10),
		"revokedSessions": strconv.FormatInt(result.RevokedSessions, 10),
	})
}

func protocolV2IdentityCommandError(err error) error {
	switch {
	case errors.Is(err, identity.ErrUserNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.user_not_found", "The user does not exist.", false)
	case errors.Is(err, identity.ErrUserStatusRevisionConflict):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_CONFLICT, "host.user_status_revision_conflict", "The user status revision changed before execution.", false)
	case errors.Is(err, identity.ErrSelfStatusChange), errors.Is(err, identity.ErrInitialSuperAdminLocked), errors.Is(err, identity.ErrSuperAdminSessionLocked):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.user_status_protected", "The protected user account cannot be changed by this actor.", false)
	case errors.Is(err, identity.ErrInvalidUserUpdate):
		return invalidProtocolV2DomainCommandInput()
	default:
		return err
	}
}
