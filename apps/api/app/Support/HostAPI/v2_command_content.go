package hostapi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	searchjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/Search"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandTopicVisibilitySetID          = "sforum.forum.topic.visibility.set"
	CommandTopicVisibilitySetVersion     = "1"
	CommandTopicVisibilityInputSchemaID  = "sforum.forum.topic.visibility.input"
	CommandTopicVisibilityOutputSchemaID = "sforum.forum.topic.visibility.result"
	CommandTopicVisibilitySchemaVersion  = "1"
)

type protocolV2TopicVisibilityInput struct {
	TopicID string `json:"topicId"`
	Action  string `json:"action"`
}

type protocolV2TopicVisibilityMutation struct {
	topicID          int64
	action           string
	expectedRevision time.Time
}

func newProtocolV2TopicVisibilityCommandDefinition(pool *pgxpool.Pool, jobs *supportjobs.Dispatcher) protocolV2CommandDefinition {
	store := forum.NewPostgresStore(pool)
	definition := protocolV2CommandDefinition{
		ID: CommandTopicVisibilitySetID, Version: CommandTopicVisibilitySetVersion,
		InputSchemaID: CommandTopicVisibilityInputSchemaID, InputSchemaVersion: CommandTopicVisibilitySchemaVersion,
		OutputSchemaID: CommandTopicVisibilityOutputSchemaID, OutputSchemaVersion: CommandTopicVisibilitySchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionTopicDeleteAny},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2TopicVisibilityMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2TopicVisibilityPreparation(mutation)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2TopicVisibilityMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		var status string
		var revision time.Time
		err = tx.QueryRow(ctx, `
			SELECT status, updated_at
			FROM topics
			WHERE id = $1
			FOR UPDATE
		`, mutation.topicID).Scan(&status, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, protocolV2ContentCommandError(forum.ErrTopicNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("lock topic visibility command: %w", err)
		}
		if !revision.Equal(mutation.expectedRevision) {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
				"host.topic_revision_conflict",
				"The topic revision changed before the command executed.",
				false,
			)
		}
		switch mutation.action {
		case forum.TopicActionHide:
			if status == forum.TopicStatusHidden || status == forum.TopicStatusDeleted {
				return nil, protocolV2ContentCommandError(forum.ErrTopicNotFound)
			}
		case forum.TopicActionRestore:
			if status != forum.TopicStatusHidden && status != forum.TopicStatusDeleted {
				return nil, newProtocolV2CommandError(
					protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
					"host.topic_state_conflict",
					"The topic is not hidden or deleted.",
					false,
				)
			}
		}
		return protocolV2TopicVisibilityPreparation(mutation)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2TopicVisibilityMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		result, err := store.ApplyTopicActionTx(ctx, tx, forum.TopicLifecycleInput{TopicID: mutation.topicID, Action: mutation.action})
		if err != nil {
			return nil, protocolV2ContentCommandError(err)
		}
		if jobs == nil {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
				"host.topic_index_job_unavailable",
				"The topic search update queue is unavailable.",
				true,
			)
		}
		if mutation.action == forum.TopicActionHide {
			args := searchjobs.DeleteTopicArgs{TopicID: mutation.topicID}
			if _, err := jobs.EnqueueTx(ctx, tx, args, args.QueueOpts()); err != nil {
				return nil, fmt.Errorf("enqueue topic index deletion: %w", err)
			}
		} else {
			args := searchjobs.IndexTopicArgs{TopicID: mutation.topicID}
			if _, err := jobs.EnqueueTx(ctx, tx, args, args.QueueOpts()); err != nil {
				return nil, fmt.Errorf("enqueue topic reindex: %w", err)
			}
		}
		var revision time.Time
		if err := tx.QueryRow(ctx, `SELECT updated_at FROM topics WHERE id = $1`, mutation.topicID).Scan(&revision); err != nil {
			return nil, fmt.Errorf("read topic command revision: %w", err)
		}
		output, err := protocolV2TopicVisibilityDocument(result, revision, false)
		if err != nil {
			return nil, fmt.Errorf("encode topic visibility command result: %w", err)
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: revision.UTC().Format(time.RFC3339Nano)}, nil
	}
	return definition
}

func protocolV2TopicVisibilityMutationFromRequest(request *hostv2.CommandRequest) (protocolV2TopicVisibilityMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2TopicVisibilityInput](request)
	if err != nil {
		return protocolV2TopicVisibilityMutation{}, err
	}
	topicID, err := strconv.ParseInt(strings.TrimSpace(input.TopicID), 10, 64)
	if err != nil || topicID <= 0 {
		return protocolV2TopicVisibilityMutation{}, invalidProtocolV2DomainCommandInput()
	}
	action := strings.TrimSpace(input.Action)
	if action != forum.TopicActionHide && action != forum.TopicActionRestore {
		return protocolV2TopicVisibilityMutation{}, invalidProtocolV2DomainCommandInput()
	}
	revision, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(request.GetExpectedRevision()))
	if err != nil || revision.IsZero() {
		return protocolV2TopicVisibilityMutation{}, invalidProtocolV2DomainCommandInput()
	}
	return protocolV2TopicVisibilityMutation{topicID: topicID, action: action, expectedRevision: revision.UTC()}, nil
}

func protocolV2TopicVisibilityPreparation(mutation protocolV2TopicVisibilityMutation) (*protocolV2CommandPreparation, error) {
	status := forum.TopicStatusHidden
	if mutation.action == forum.TopicActionRestore {
		status = forum.TopicStatusActive
	}
	projected, err := protocolV2TopicVisibilityDocument(forum.TopicLifecycleRecord{
		TopicID: mutation.topicID, Status: status,
	}, mutation.expectedRevision, true)
	if err != nil {
		return nil, err
	}
	resourceID := strconv.FormatInt(mutation.topicID, 10)
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.forum.topic.visibility@1", ResourceId: resourceID, Allowed: true,
			Reason: "The delegated actor must retain topic.delete_any at execution time.",
		}},
		Impact: []*hostv2.ImpactItem{
			{Module: "forum", Action: mutation.action, ResourceType: "topic", ResourceId: resourceID, Summary: "Change topic public visibility.", Reversible: true},
			{Module: "search", Action: "reconcile", ResourceType: "topic_index", ResourceId: resourceID, Summary: "Queue the matching search index update atomically.", Reversible: true},
		},
		ProjectedResult: projected,
	}, nil
}

func protocolV2TopicVisibilityDocument(result forum.TopicLifecycleRecord, revision time.Time, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandTopicVisibilityOutputSchemaID, CommandTopicVisibilitySchemaVersion, map[string]any{
		"planned": planned, "topicId": strconv.FormatInt(result.TopicID, 10),
		"status": result.Status, "pinned": result.IsPinned,
		"revision": revision.UTC().Format(time.RFC3339Nano),
	})
}

func protocolV2ContentCommandError(err error) error {
	switch {
	case errors.Is(err, forum.ErrTopicNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.topic_not_found", "The topic does not exist.", false)
	case errors.Is(err, forum.ErrInvalidAction), errors.Is(err, forum.ErrInvalidTopic):
		return invalidProtocolV2DomainCommandInput()
	default:
		return err
	}
}
