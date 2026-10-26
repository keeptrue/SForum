package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var ErrProtocolV2AttachmentNotFound = errors.New("hostapi: attachment not found")

const (
	protocolV2AttachmentStatusActive   = "active"
	protocolV2AttachmentStatusDisabled = "disabled"
	protocolV2AttachmentStatusDeleted  = "deleted"
)

const (
	CommandAttachmentStatusSetID          = "sforum.attachments.status.set"
	CommandAttachmentStatusSetVersion     = "1"
	CommandAttachmentStatusInputSchemaID  = "sforum.attachments.status.input"
	CommandAttachmentStatusOutputSchemaID = "sforum.attachments.status.result"
	CommandAttachmentStatusSchemaVersion  = "1"
)

type protocolV2AttachmentStatusInput struct {
	AttachmentID string `json:"attachmentId"`
	Status       string `json:"status"`
}

type protocolV2AttachmentStatusMutation struct {
	attachmentID     int64
	status           string
	expectedRevision time.Time
}

type ProtocolV2AttachmentStatusResult struct {
	ID             int64
	Status         string
	ReferenceCount int
	UpdatedAt      time.Time
}

type ProtocolV2AttachmentStatusMutator interface {
	MutateProtocolV2AttachmentStatus(context.Context, pgx.Tx, int64, string) (ProtocolV2AttachmentStatusResult, error)
}

func newProtocolV2AttachmentStatusCommandDefinition(mutator ProtocolV2AttachmentStatusMutator) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandAttachmentStatusSetID, Version: CommandAttachmentStatusSetVersion,
		InputSchemaID: CommandAttachmentStatusInputSchemaID, InputSchemaVersion: CommandAttachmentStatusSchemaVersion,
		OutputSchemaID: CommandAttachmentStatusOutputSchemaID, OutputSchemaVersion: CommandAttachmentStatusSchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionAttachmentManage},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2AttachmentStatusMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2AttachmentStatusPreparation(mutation, 0)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2AttachmentStatusMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		var currentStatus string
		var revision time.Time
		var referenceCount int64
		err = tx.QueryRow(ctx, `
			SELECT status, updated_at, reference_count
			FROM attachments
			WHERE id = $1
			FOR UPDATE
		`, mutation.attachmentID).Scan(&currentStatus, &revision, &referenceCount)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, protocolV2AttachmentCommandError(ErrProtocolV2AttachmentNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("lock attachment status command: %w", err)
		}
		if currentStatus == protocolV2AttachmentStatusDeleted {
			return nil, protocolV2AttachmentCommandError(ErrProtocolV2AttachmentNotFound)
		}
		if !revision.Equal(mutation.expectedRevision) {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
				"host.attachment_revision_conflict",
				"The attachment revision changed before the command executed.",
				false,
			)
		}
		return protocolV2AttachmentStatusPreparation(mutation, referenceCount)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2AttachmentStatusMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		if mutator == nil {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
				"host.attachment_runtime_unavailable",
				"The attachment command runtime is unavailable.",
				true,
			)
		}
		result, err := mutator.MutateProtocolV2AttachmentStatus(ctx, tx, mutation.attachmentID, mutation.status)
		if err != nil {
			return nil, protocolV2AttachmentCommandError(err)
		}
		output, err := protocolV2AttachmentStatusDocument(result, false)
		if err != nil {
			return nil, fmt.Errorf("encode attachment status command result: %w", err)
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: result.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	}
	return definition
}

func protocolV2AttachmentStatusMutationFromRequest(request *hostv2.CommandRequest) (protocolV2AttachmentStatusMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2AttachmentStatusInput](request)
	if err != nil {
		return protocolV2AttachmentStatusMutation{}, err
	}
	attachmentID, err := strconv.ParseInt(strings.TrimSpace(input.AttachmentID), 10, 64)
	status := strings.TrimSpace(input.Status)
	revision, revisionErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(request.GetExpectedRevision()))
	if err != nil || attachmentID <= 0 || revisionErr != nil || revision.IsZero() ||
		(status != protocolV2AttachmentStatusActive && status != protocolV2AttachmentStatusDisabled) {
		return protocolV2AttachmentStatusMutation{}, invalidProtocolV2DomainCommandInput()
	}
	return protocolV2AttachmentStatusMutation{attachmentID: attachmentID, status: status, expectedRevision: revision.UTC()}, nil
}

func protocolV2AttachmentStatusPreparation(mutation protocolV2AttachmentStatusMutation, referenceCount int64) (*protocolV2CommandPreparation, error) {
	projected, err := protocolV2AttachmentStatusDocument(ProtocolV2AttachmentStatusResult{
		ID: mutation.attachmentID, Status: mutation.status,
		ReferenceCount: int(referenceCount), UpdatedAt: mutation.expectedRevision,
	}, true)
	if err != nil {
		return nil, err
	}
	resourceID := strconv.FormatInt(mutation.attachmentID, 10)
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.attachments.status@1", ResourceId: resourceID, Allowed: true,
			Reason: "The delegated actor must retain attachment.manage at execution time.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "attachments", Action: "set_status", ResourceType: "attachment", ResourceId: resourceID,
			Summary: "Change attachment availability metadata without deleting the stored object.", Reversible: true,
		}},
		ProjectedResult: projected,
	}, nil
}

func protocolV2AttachmentStatusDocument(result ProtocolV2AttachmentStatusResult, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandAttachmentStatusOutputSchemaID, CommandAttachmentStatusSchemaVersion, map[string]any{
		"planned": planned, "attachmentId": strconv.FormatInt(result.ID, 10),
		"status": result.Status, "referenceCount": strconv.Itoa(result.ReferenceCount),
		"revision": result.UpdatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func protocolV2AttachmentCommandError(err error) error {
	switch {
	case errors.Is(err, ErrProtocolV2AttachmentNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.attachment_not_found", "The attachment does not exist.", false)
	default:
		return err
	}
}
