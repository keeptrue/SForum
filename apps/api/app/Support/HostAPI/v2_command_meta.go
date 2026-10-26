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
	entitymeta "github.com/keeptrue/sforum/apps/api/app/Models/EntityMeta"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandEntityMetaValuesUpsertID       = "sforum.entity_meta.values.upsert"
	CommandEntityMetaValuesUpsertVersion  = "1"
	CommandEntityMetaValuesInputSchemaID  = "sforum.entity_meta.values.input"
	CommandEntityMetaValuesOutputSchemaID = "sforum.entity_meta.values.result"
	CommandEntityMetaValuesSchemaVersion  = "1"
)

type protocolV2EntityMetaValuesInput struct {
	EntityType string                           `json:"entityType"`
	EntityID   string                           `json:"entityId"`
	Values     []protocolV2EntityMetaValueInput `json:"values"`
}

type protocolV2EntityMetaValueInput struct {
	FieldKey string `json:"fieldKey"`
	Value    any    `json:"value"`
}

type protocolV2EntityMetaMutation struct {
	entityType string
	entityID   int64
	values     []entitymeta.UpsertValueInput
}

func newProtocolV2EntityMetaCommandDefinition(pool *pgxpool.Pool) protocolV2CommandDefinition {
	store := entitymeta.NewPostgresStore(pool)
	definition := protocolV2CommandDefinition{
		ID: CommandEntityMetaValuesUpsertID, Version: CommandEntityMetaValuesUpsertVersion,
		InputSchemaID: CommandEntityMetaValuesInputSchemaID, InputSchemaVersion: CommandEntityMetaValuesSchemaVersion,
		OutputSchemaID: CommandEntityMetaValuesOutputSchemaID, OutputSchemaVersion: CommandEntityMetaValuesSchemaVersion,
		ActorMode:           protocolV2CommandActorDelegated,
		RequiredPermissions: []string{identity.PermissionEntityMetaManage},
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2EntityMetaMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2EntityMetaPreparation(mutation)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2EntityMetaMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
		if !ok || runtime == nil || strings.TrimSpace(runtime.GetExtensionId()) == "" {
			return nil, invalidProtocolV2CommandActorDelegation()
		}
		if err := store.ValidateExtensionValuesTx(
			ctx, tx, runtime.GetExtensionId(), actorUserID,
			mutation.entityType, mutation.entityID, mutation.values,
		); err != nil {
			return nil, protocolV2EntityMetaCommandError(err)
		}
		return protocolV2EntityMetaPreparation(mutation)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2EntityMetaMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
		if !ok || runtime == nil || strings.TrimSpace(runtime.GetExtensionId()) == "" {
			return nil, newProtocolV2CommandError(
				protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED,
				"host.command_actor_delegation_invalid",
				"The Host-signed actor delegation is invalid or stale.",
				false,
			)
		}
		values, err := store.UpsertExtensionValuesTx(
			ctx, tx, runtime.GetExtensionId(), actorUserID,
			mutation.entityType, mutation.entityID, mutation.values,
		)
		if err != nil {
			return nil, protocolV2EntityMetaCommandError(err)
		}
		var revision time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&revision); err != nil {
			return nil, fmt.Errorf("read entity meta command revision: %w", err)
		}
		output, err := protocolV2EntityMetaDocument(mutation, values, revision, false)
		if err != nil {
			return nil, fmt.Errorf("encode entity meta command result: %w", err)
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: revision.UTC().Format(time.RFC3339Nano)}, nil
	}
	return definition
}

func protocolV2EntityMetaMutationFromRequest(request *hostv2.CommandRequest) (protocolV2EntityMetaMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2EntityMetaValuesInput](request)
	if err != nil || strings.TrimSpace(request.GetExpectedRevision()) != "" {
		return protocolV2EntityMetaMutation{}, invalidProtocolV2DomainCommandInput()
	}
	input.EntityType = strings.TrimSpace(input.EntityType)
	if input.EntityType != entitymeta.EntityUser && input.EntityType != entitymeta.EntityTopic {
		return protocolV2EntityMetaMutation{}, invalidProtocolV2DomainCommandInput()
	}
	entityID, err := strconv.ParseInt(strings.TrimSpace(input.EntityID), 10, 64)
	if err != nil || entityID <= 0 || len(input.Values) == 0 || len(input.Values) > entitymeta.MaximumTransactionalValues {
		return protocolV2EntityMetaMutation{}, invalidProtocolV2DomainCommandInput()
	}
	seen := make(map[string]bool, len(input.Values))
	values := make([]entitymeta.UpsertValueInput, 0, len(input.Values))
	for _, inputValue := range input.Values {
		fieldKey := strings.TrimSpace(inputValue.FieldKey)
		if fieldKey == "" || seen[fieldKey] {
			return protocolV2EntityMetaMutation{}, invalidProtocolV2DomainCommandInput()
		}
		seen[fieldKey] = true
		values = append(values, entitymeta.UpsertValueInput{FieldKey: fieldKey, Value: inputValue.Value})
	}
	return protocolV2EntityMetaMutation{entityType: input.EntityType, entityID: entityID, values: values}, nil
}

func protocolV2EntityMetaPreparation(mutation protocolV2EntityMetaMutation) (*protocolV2CommandPreparation, error) {
	resourceID := mutation.entityType + ":" + strconv.FormatInt(mutation.entityID, 10)
	impacts := make([]*hostv2.ImpactItem, 0, len(mutation.values))
	projected := make([]entitymeta.MetaValue, 0, len(mutation.values))
	for _, value := range mutation.values {
		action := "upsert"
		if value.Value == nil {
			action = "delete"
		}
		impacts = append(impacts, &hostv2.ImpactItem{
			Module: "entity_meta", Action: action, ResourceType: "entity_meta_value",
			ResourceId: resourceID + ":" + value.FieldKey,
			Summary:    "Mutate one extension-owned entity meta value.", Reversible: true,
		})
		projected = append(projected, entitymeta.MetaValue{FieldKey: value.FieldKey, EntityType: mutation.entityType, EntityID: mutation.entityID, Value: value.Value})
	}
	document, err := protocolV2EntityMetaDocument(mutation, projected, time.Time{}, true)
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.entity_meta.extension_values@1", ResourceId: resourceID, Allowed: true,
			Reason: "The delegated actor must retain entity_meta.manage and fields must remain owned by the exact extension.",
		}},
		Impact: impacts, ProjectedResult: document,
	}, nil
}

func protocolV2EntityMetaDocument(mutation protocolV2EntityMetaMutation, values []entitymeta.MetaValue, revision time.Time, planned bool) (*protocolv2.TypedDocument, error) {
	items := make([]any, 0, len(values))
	for _, value := range values {
		item := map[string]any{
			"fieldKey": value.FieldKey, "value": value.Value,
			"valueType": value.ValueType, "visibility": value.Visibility,
			"deleted": value.Value == nil,
		}
		if !value.UpdatedAt.IsZero() {
			item["updatedAt"] = value.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		items = append(items, item)
	}
	result := map[string]any{
		"planned": planned, "entityType": mutation.entityType,
		"entityId": strconv.FormatInt(mutation.entityID, 10), "values": items,
	}
	if !revision.IsZero() {
		result["revision"] = revision.UTC().Format(time.RFC3339Nano)
	}
	return protocolV2Document(CommandEntityMetaValuesOutputSchemaID, CommandEntityMetaValuesSchemaVersion, result)
}

func protocolV2EntityMetaCommandError(err error) error {
	switch {
	case errors.Is(err, entitymeta.ErrEntityNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.entity_meta_target_not_found", "The entity meta target does not exist.", false)
	case errors.Is(err, entitymeta.ErrNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.entity_meta_field_not_found", "An entity meta field does not exist.", false)
	case errors.Is(err, entitymeta.ErrPermission):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "host.entity_meta_field_denied", "The field is not owned by this extension.", false)
	case errors.Is(err, entitymeta.ErrFieldDisabled):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.entity_meta_field_disabled", "An entity meta field is disabled.", false)
	case errors.Is(err, entitymeta.ErrInvalid):
		return invalidProtocolV2DomainCommandInput()
	default:
		return err
	}
}
