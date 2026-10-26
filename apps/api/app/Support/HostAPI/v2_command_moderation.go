package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandModerationDecisionSubmitID       = "sforum.moderation.decision.submit"
	CommandModerationDecisionSubmitVersion  = "1"
	CommandModerationDecisionInputSchemaID  = "sforum.moderation.decision.input"
	CommandModerationDecisionOutputSchemaID = "sforum.moderation.decision.result"
	CommandModerationDecisionSchemaVersion  = "1"
)

type protocolV2ModerationDecisionInput struct {
	Source     string `json:"source"`
	TargetType string `json:"targetType"`
	TargetID   string `json:"targetId"`
	ReportID   string `json:"reportId,omitempty"`
	Action     string `json:"action"`
	ReviewNote string `json:"reviewNote,omitempty"`
}

func newProtocolV2ModerationCommandDefinition(store *moderation.PostgresStore, jobs *supportjobs.Dispatcher) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandModerationDecisionSubmitID, Version: CommandModerationDecisionSubmitVersion,
		InputSchemaID: CommandModerationDecisionInputSchemaID, InputSchemaVersion: CommandModerationDecisionSchemaVersion,
		OutputSchemaID: CommandModerationDecisionOutputSchemaID, OutputSchemaVersion: CommandModerationDecisionSchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionModerationReview},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, err := protocolV2ModerationDecisionFromRequest(request, 0)
		if err != nil {
			return nil, err
		}
		return protocolV2ModerationPreparation(input)
	}
	definition.Prepare = func(ctx context.Context, _ pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		input, err := protocolV2ModerationDecisionFromRequest(request, actorUserID)
		if err != nil {
			return nil, err
		}
		return protocolV2ModerationPreparation(input)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		if store == nil || jobs == nil {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
				"host.moderation_runtime_unavailable",
				"The moderation command runtime is unavailable.",
				true,
			)
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		input, err := protocolV2ModerationDecisionFromRequest(request, actorUserID)
		if err != nil {
			return nil, err
		}
		decision, err := store.SubmitDecisionTx(ctx, tx, input)
		if err != nil {
			return nil, protocolV2ModerationCommandError(err)
		}
		if err := enqueueProtocolV2ModerationSearch(ctx, tx, jobs, input); err != nil {
			return nil, err
		}
		output, err := protocolV2ModerationDecisionDocument(decision, false)
		if err != nil {
			return nil, fmt.Errorf("encode moderation command result: %w", err)
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: strconv.FormatInt(decision.ID, 10)}, nil
	}
	return definition
}

func protocolV2ModerationDecisionFromRequest(request *hostv2.CommandRequest, actorUserID int64) (moderation.DecisionInput, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2ModerationDecisionInput](request)
	if err != nil || strings.TrimSpace(request.GetExpectedRevision()) != "" {
		return moderation.DecisionInput{}, invalidProtocolV2DomainCommandInput()
	}
	targetID, err := strconv.ParseInt(strings.TrimSpace(input.TargetID), 10, 64)
	if err != nil || targetID <= 0 {
		return moderation.DecisionInput{}, invalidProtocolV2DomainCommandInput()
	}
	reportID := int64(0)
	if strings.TrimSpace(input.ReportID) != "" {
		reportID, err = strconv.ParseInt(strings.TrimSpace(input.ReportID), 10, 64)
		if err != nil || reportID <= 0 {
			return moderation.DecisionInput{}, invalidProtocolV2DomainCommandInput()
		}
	}
	result := moderation.DecisionInput{
		Source: strings.TrimSpace(input.Source), TargetType: strings.TrimSpace(input.TargetType),
		TargetID: targetID, ReportID: reportID, Action: strings.TrimSpace(input.Action),
		ReviewNote: input.ReviewNote, ReviewerUserID: actorUserID,
	}
	if err := moderation.ValidateDecision(result); err != nil {
		return moderation.DecisionInput{}, protocolV2ModerationCommandError(err)
	}
	return result, nil
}

func protocolV2ModerationPreparation(input moderation.DecisionInput) (*protocolV2CommandPreparation, error) {
	projected, err := protocolV2ModerationDecisionDocument(moderation.Decision{
		Source: input.Source, TargetType: input.TargetType, TargetID: input.TargetID,
		Action: input.Action, ReviewerUserID: input.ReviewerUserID, ReviewNote: strings.TrimSpace(input.ReviewNote),
	}, true)
	if err != nil {
		return nil, err
	}
	resourceID := input.TargetType + ":" + strconv.FormatInt(input.TargetID, 10)
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.moderation.review@1", ResourceId: resourceID, Allowed: true,
			Reason: "The delegated actor must retain moderation.review at execution time.",
		}},
		Impact: []*hostv2.ImpactItem{
			{Module: "moderation", Action: input.Action, ResourceType: "moderation_decision", ResourceId: resourceID, Summary: "Record an immutable moderation decision.", Reversible: false},
			{Module: "forum", Action: input.Action, ResourceType: input.TargetType, ResourceId: strconv.FormatInt(input.TargetID, 10), Summary: "Apply the reviewed content state and counters.", Reversible: input.Action == moderation.ActionApprove || input.Action == moderation.ActionKeepAndClose},
			{Module: "search", Action: "reconcile", ResourceType: "topic_index", ResourceId: resourceID, Summary: "Queue the matching search index update atomically.", Reversible: true},
		},
		ProjectedResult: projected,
	}, nil
}

func enqueueProtocolV2ModerationSearch(
	ctx context.Context,
	tx pgx.Tx,
	jobs *supportjobs.Dispatcher,
	input moderation.DecisionInput,
) error {
	topicID := input.TargetID
	if input.TargetType == moderation.TargetTypeComment {
		if err := tx.QueryRow(ctx, `SELECT topic_id FROM comments WHERE id = $1`, input.TargetID).Scan(&topicID); err != nil {
			return fmt.Errorf("resolve moderated comment topic: %w", err)
		}
	}
	deleteTopic := input.TargetType == moderation.TargetTypeTopic &&
		(input.Action == moderation.ActionReject || input.Action == moderation.ActionHideAndClose || input.Action == moderation.ActionDeleteAndClose)
	if deleteTopic {
		args := searchjobs.DeleteTopicArgs{TopicID: topicID}
		if _, err := jobs.EnqueueTx(ctx, tx, args, args.QueueOpts()); err != nil {
			return fmt.Errorf("enqueue moderated topic index deletion: %w", err)
		}
		return nil
	}
	args := searchjobs.IndexTopicArgs{TopicID: topicID}
	if _, err := jobs.EnqueueTx(ctx, tx, args, args.QueueOpts()); err != nil {
		return fmt.Errorf("enqueue moderated topic reindex: %w", err)
	}
	return nil
}

func protocolV2ModerationDecisionDocument(decision moderation.Decision, planned bool) (*protocolv2.TypedDocument, error) {
	values := map[string]any{
		"planned": planned, "decisionId": strconv.FormatInt(decision.ID, 10),
		"source": decision.Source, "targetType": decision.TargetType,
		"targetId": strconv.FormatInt(decision.TargetID, 10), "action": decision.Action,
		"reviewerUserId": strconv.FormatInt(decision.ReviewerUserID, 10),
	}
	if decision.ReportID != nil {
		values["reportId"] = strconv.FormatInt(*decision.ReportID, 10)
	}
	if !decision.CreatedAt.IsZero() {
		values["createdAt"] = decision.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return protocolV2Document(CommandModerationDecisionOutputSchemaID, CommandModerationDecisionSchemaVersion, values)
}

func protocolV2ModerationCommandError(err error) error {
	switch {
	case errors.Is(err, moderation.ErrDecisionInvalid):
		return invalidProtocolV2DomainCommandInput()
	case errors.Is(err, moderation.ErrTaskNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.moderation_task_not_found", "The moderation task does not exist.", false)
	case errors.Is(err, moderation.ErrTaskConflict):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_CONFLICT, "host.moderation_task_conflict", "The moderation task was already changed.", false)
	default:
		return err
	}
}
