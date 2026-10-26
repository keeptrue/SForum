package hostapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	entitlements "github.com/keeptrue/sforum/apps/api/app/Models/Entitlements"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandEntitlementsMutateID               = "sforum.entitlements.mutate"
	CommandEntitlementsMutateVersion          = "1"
	CommandEntitlementsMutationInputSchemaID  = "sforum.entitlements.mutation.input"
	CommandEntitlementsMutationOutputSchemaID = "sforum.entitlements.mutation.result"
	CommandEntitlementsMutationSchemaVersion  = "1"
)

type protocolV2EntitlementMutationInput struct {
	Action        string                        `json:"action"`
	Subject       *protocolV2EntitlementSubject `json:"subject,omitempty"`
	Scope         *protocolV2EntitlementScope   `json:"scope,omitempty"`
	Source        *protocolV2EntitlementSource  `json:"source,omitempty"`
	ValidFrom     string                        `json:"validFrom,omitempty"`
	ValidUntil    string                        `json:"validUntil,omitempty"`
	EntitlementID string                        `json:"entitlementId,omitempty"`
}

type protocolV2EntitlementSubject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type protocolV2EntitlementScope struct {
	Kind         string `json:"kind"`
	ResourceType string `json:"resourceType,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
	Capability   string `json:"capability,omitempty"`
}

type protocolV2EntitlementSource struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type protocolV2EntitlementMutation struct {
	action           string
	grant            entitlements.GrantInput
	transition       entitlements.TransitionInput
	entitlementID    int64
	expectedRevision int64
}

func newProtocolV2EntitlementCommandDefinition(pool *pgxpool.Pool) protocolV2CommandDefinition {
	repository := entitlements.NewPostgresRepository(pool)
	definition := protocolV2CommandDefinition{
		ID: CommandEntitlementsMutateID, Version: CommandEntitlementsMutateVersion,
		InputSchemaID: CommandEntitlementsMutationInputSchemaID, InputSchemaVersion: CommandEntitlementsMutationSchemaVersion,
		OutputSchemaID: CommandEntitlementsMutationOutputSchemaID, OutputSchemaVersion: CommandEntitlementsMutationSchemaVersion,
		ActorMode: protocolV2CommandActorService,
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2EntitlementMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2EntitlementPreparation(mutation)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		mutation, err := protocolV2EntitlementMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		if mutation.action != entitlements.ActionGrant {
			var revision int64
			var status string
			err = tx.QueryRow(ctx, `
				SELECT revision, status
				FROM entitlements
				WHERE id = $1
				FOR UPDATE
			`, mutation.entitlementID).Scan(&revision, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, protocolV2EntitlementError(entitlements.ErrNotFound)
			}
			if err != nil {
				return nil, fmt.Errorf("lock entitlement lifecycle: %w", err)
			}
			if status != entitlements.StatusActive {
				return nil, protocolV2EntitlementError(entitlements.ErrStateConflict)
			}
			if mutation.expectedRevision != revision {
				return nil, newProtocolV2CommandError(
					protocolv2.ErrorCode_ERROR_CODE_CONFLICT,
					"host.entitlement_revision_conflict",
					"The entitlement revision changed before the command executed.",
					false,
				)
			}
		}
		return protocolV2EntitlementPreparation(mutation)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		mutation, err := protocolV2EntitlementMutationFromRequest(request)
		if err != nil {
			return nil, err
		}
		var result entitlements.MutationResult
		switch mutation.action {
		case entitlements.ActionGrant:
			result, err = repository.GrantTx(ctx, tx, mutation.grant)
		case entitlements.ActionRevoke:
			result, err = repository.RevokeTx(ctx, tx, mutation.transition)
		case entitlements.ActionExpire:
			result, err = repository.ExpireTx(ctx, tx, mutation.transition)
		}
		if err != nil {
			return nil, protocolV2EntitlementError(err)
		}
		output, err := protocolV2EntitlementResultDocument(result, false)
		if err != nil {
			return nil, fmt.Errorf("encode entitlement command result: %w", err)
		}
		return &protocolV2CommandExecution{
			Output: output, CommittedRevision: strconv.FormatInt(result.Entitlement.Revision, 10),
		}, nil
	}
	return definition
}

func protocolV2EntitlementMutationFromRequest(request *hostv2.CommandRequest) (protocolV2EntitlementMutation, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2EntitlementMutationInput](request)
	if err != nil {
		return protocolV2EntitlementMutation{}, err
	}
	input.Action = strings.TrimSpace(input.Action)
	key, err := protocolV2EntitlementIdempotencyKey(request)
	if err != nil {
		return protocolV2EntitlementMutation{}, err
	}
	switch input.Action {
	case entitlements.ActionGrant:
		if strings.TrimSpace(request.GetExpectedRevision()) != "" || input.Subject == nil || input.Scope == nil || input.Source == nil || strings.TrimSpace(input.EntitlementID) != "" {
			return protocolV2EntitlementMutation{}, invalidProtocolV2DomainCommandInput()
		}
		validFrom, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(input.ValidFrom))
		if err != nil {
			return protocolV2EntitlementMutation{}, invalidProtocolV2DomainCommandInput()
		}
		var validUntil *time.Time
		if strings.TrimSpace(input.ValidUntil) != "" {
			value, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(input.ValidUntil))
			if parseErr != nil {
				return protocolV2EntitlementMutation{}, invalidProtocolV2DomainCommandInput()
			}
			validUntil = &value
		}
		grant := entitlements.GrantInput{
			Subject:   entitlements.Subject{Type: input.Subject.Type, ID: input.Subject.ID},
			Scope:     entitlements.Scope{Kind: input.Scope.Kind, ResourceType: input.Scope.ResourceType, ResourceID: input.Scope.ResourceID, Capability: input.Scope.Capability},
			Source:    entitlements.Source{Type: input.Source.Type, ID: input.Source.ID},
			ValidFrom: validFrom, ValidUntil: validUntil, IdempotencyKey: key,
		}
		if err := entitlements.ValidateGrant(grant); err != nil {
			return protocolV2EntitlementMutation{}, protocolV2EntitlementError(err)
		}
		return protocolV2EntitlementMutation{action: input.Action, grant: grant}, nil
	case entitlements.ActionRevoke, entitlements.ActionExpire:
		if input.Subject != nil || input.Scope != nil || input.Source != nil || strings.TrimSpace(input.ValidFrom) != "" || strings.TrimSpace(input.ValidUntil) != "" {
			return protocolV2EntitlementMutation{}, invalidProtocolV2DomainCommandInput()
		}
		entitlementID, parseErr := strconv.ParseInt(strings.TrimSpace(input.EntitlementID), 10, 64)
		expectedRevision, revisionErr := strconv.ParseInt(strings.TrimSpace(request.GetExpectedRevision()), 10, 64)
		if parseErr != nil || entitlementID <= 0 || revisionErr != nil || expectedRevision <= 0 {
			return protocolV2EntitlementMutation{}, invalidProtocolV2DomainCommandInput()
		}
		transition := entitlements.TransitionInput{EntitlementID: entitlementID, IdempotencyKey: key}
		if err := entitlements.ValidateTransition(input.Action, transition); err != nil {
			return protocolV2EntitlementMutation{}, protocolV2EntitlementError(err)
		}
		return protocolV2EntitlementMutation{
			action: input.Action, transition: transition, entitlementID: entitlementID,
			expectedRevision: expectedRevision,
		}, nil
	default:
		return protocolV2EntitlementMutation{}, invalidProtocolV2DomainCommandInput()
	}
}

func protocolV2EntitlementIdempotencyKey(request *hostv2.CommandRequest) (string, error) {
	extensionID := strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId())
	key, detail := protocolV2CommandIdempotencyKey(request, true)
	if detail != nil {
		return "", &protocolV2CommandError{detail: detail}
	}
	digest := sha256.Sum256([]byte(extensionID + "\x00" + CommandEntitlementsMutateID + "\x00" + key))
	return "hostcmd:" + hex.EncodeToString(digest[:]), nil
}

func protocolV2EntitlementPreparation(mutation protocolV2EntitlementMutation) (*protocolV2CommandPreparation, error) {
	resourceID := strconv.FormatInt(mutation.entitlementID, 10)
	resourceType := "entitlement"
	if mutation.action == entitlements.ActionGrant {
		resourceID = mutation.grant.Subject.Type + ":" + mutation.grant.Subject.ID
	}
	projected := entitlements.MutationResult{Entitlement: entitlements.Entitlement{ID: mutation.entitlementID, Status: protocolV2EntitlementNextStatus(mutation.action)}}
	document, err := protocolV2EntitlementResultDocument(projected, true)
	if err != nil {
		return nil, fmt.Errorf("encode projected entitlement result: %w", err)
	}
	return &protocolV2CommandPreparation{
		Policy: []*hostv2.PolicyDecision{{
			PolicyId: "sforum.entitlements.command@1", ResourceId: resourceID, Allowed: true,
			Reason: "Exact-artifact Host Command authority is rechecked in the transaction.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module: "entitlements", Action: mutation.action, ResourceType: resourceType,
			ResourceId: resourceID, Summary: "Mutate one provider-neutral entitlement lifecycle.", Reversible: mutation.action == entitlements.ActionGrant,
		}},
		ProjectedResult: document,
	}, nil
}

func protocolV2EntitlementNextStatus(action string) string {
	switch action {
	case entitlements.ActionGrant:
		return entitlements.StatusActive
	case entitlements.ActionRevoke:
		return entitlements.StatusRevoked
	case entitlements.ActionExpire:
		return entitlements.StatusExpired
	default:
		return ""
	}
}

func protocolV2EntitlementResultDocument(result entitlements.MutationResult, planned bool) (*protocolv2.TypedDocument, error) {
	entitlement := result.Entitlement
	values := map[string]any{
		"planned":  planned,
		"replayed": result.Replayed,
		"entitlement": map[string]any{
			"id": strconv.FormatInt(entitlement.ID, 10), "status": entitlement.Status,
			"revision": strconv.FormatInt(entitlement.Revision, 10),
		},
	}
	if !entitlement.ValidFrom.IsZero() {
		values["entitlement"].(map[string]any)["subject"] = map[string]any{"type": entitlement.Subject.Type, "id": entitlement.Subject.ID}
		values["entitlement"].(map[string]any)["scope"] = map[string]any{
			"kind": entitlement.Scope.Kind, "resourceType": entitlement.Scope.ResourceType,
			"resourceId": entitlement.Scope.ResourceID, "capability": entitlement.Scope.Capability,
		}
		values["entitlement"].(map[string]any)["source"] = map[string]any{"type": entitlement.Source.Type, "id": entitlement.Source.ID}
		values["entitlement"].(map[string]any)["validFrom"] = entitlement.ValidFrom.UTC().Format(time.RFC3339Nano)
		if entitlement.ValidUntil != nil {
			values["entitlement"].(map[string]any)["validUntil"] = entitlement.ValidUntil.UTC().Format(time.RFC3339Nano)
		}
	}
	if result.Event.ID > 0 {
		values["event"] = map[string]any{
			"id": strconv.FormatInt(result.Event.ID, 10), "action": result.Event.Action,
			"createdAt": result.Event.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return protocolV2Document(CommandEntitlementsMutationOutputSchemaID, CommandEntitlementsMutationSchemaVersion, values)
}

func protocolV2EntitlementError(err error) error {
	switch {
	case errors.Is(err, entitlements.ErrInvalidInput):
		return invalidProtocolV2DomainCommandInput()
	case errors.Is(err, entitlements.ErrNotFound):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.entitlement_not_found", "The entitlement does not exist.", false)
	case errors.Is(err, entitlements.ErrStateConflict), errors.Is(err, entitlements.ErrIdempotencyConflict):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_CONFLICT, "host.entitlement_state_conflict", "The entitlement lifecycle changed concurrently.", false)
	case errors.Is(err, entitlements.ErrNotYetExpired):
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.entitlement_not_expired", "The entitlement validity window has not expired.", false)
	default:
		return err
	}
}
