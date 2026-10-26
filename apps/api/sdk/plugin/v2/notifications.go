package pluginv2

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	HostNotificationEmitCommandID       = "notifications.emit"
	HostNotificationEmitCommandVersion  = "1"
	HostNotificationEmitInputSchemaRef  = "sforum.notifications.emit.input@1"
	HostNotificationEmitResultSchemaRef = "sforum.notifications.emit.result@1"
	HostNotificationEmitMaxRecipients   = 50
	HostNotificationEmitMaxPayloadBytes = 16 * 1024
)

var (
	ErrHostNotificationEmitInvalid = errors.New("protocol v2 notification emission is invalid")
	hostNotificationTypePattern    = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,160}$`)
)

// NotificationTarget references only the safe target contract declared by the
// exact plugin artifact. It is not a URL and cannot carry session authority.
type NotificationTarget struct {
	Kind string
	ID   string
}

// NotificationEmitInput is the bounded author-facing form of
// notifications.emit@1. Recipient IDs are encoded as decimal strings on the
// wire so protobuf Struct never loses 64-bit integer precision.
type NotificationEmitInput struct {
	Type             string
	PayloadVersion   int
	Payload          map[string]any
	RecipientUserIDs []int64
	Target           *NotificationTarget
	IdempotencyKey   string
}

// NotificationEmitRequest builds an actorless, exact-runtime Host command.
// The Host remains authoritative for schema, ownership, recipient, policy,
// rate, target, deadline, and idempotency validation.
func (h *Host) NotificationEmitRequest(parent *protocolwire.RequestContext, input NotificationEmitInput) (*hostwire.CommandRequest, error) {
	input.Type = strings.TrimSpace(input.Type)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if h == nil || h.Commands == nil || !hostNotificationTypePattern.MatchString(input.Type) ||
		input.PayloadVersion <= 0 || input.Payload == nil || len(input.RecipientUserIDs) == 0 ||
		len(input.RecipientUserIDs) > HostNotificationEmitMaxRecipients || !validHostNotificationIdempotencyKey(input.IdempotencyKey) {
		return nil, ErrHostNotificationEmitInvalid
	}
	body, err := json.Marshal(input.Payload)
	if err != nil || len(body) == 0 || len(body) > HostNotificationEmitMaxPayloadBytes {
		return nil, ErrHostNotificationEmitInvalid
	}
	owner := strings.TrimSpace(h.identity.GetExtensionId())
	if owner == "" || !strings.HasPrefix(input.Type, owner+".") {
		return nil, ErrHostNotificationEmitInvalid
	}
	recipients := make([]any, 0, len(input.RecipientUserIDs))
	seen := make(map[int64]struct{}, len(input.RecipientUserIDs))
	for _, id := range input.RecipientUserIDs {
		if id <= 0 {
			return nil, ErrHostNotificationEmitInvalid
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrHostNotificationEmitInvalid
		}
		seen[id] = struct{}{}
		recipients = append(recipients, strconv.FormatInt(id, 10))
	}
	values := map[string]any{
		"type": input.Type, "payloadVersion": input.PayloadVersion,
		"payload": input.Payload, "recipientUserIds": recipients,
	}
	if input.Target != nil {
		kind, id := strings.TrimSpace(input.Target.Kind), strings.TrimSpace(input.Target.ID)
		if kind == "" || kind == "none" && id != "" {
			return nil, ErrHostNotificationEmitInvalid
		}
		values["target"] = map[string]any{"kind": kind, "id": id}
	}
	document, err := NewTypedDocument(HostNotificationEmitInputSchemaRef, values)
	if err != nil {
		return nil, ErrHostNotificationEmitInvalid
	}
	requestContext := h.RequestContext(parent)
	if requestContext == nil || parent != nil && parent.GetIdempotencyKey() != "" && parent.GetIdempotencyKey() != input.IdempotencyKey {
		return nil, ErrHostNotificationEmitInvalid
	}
	requestContext.IdempotencyKey = input.IdempotencyKey
	return &hostwire.CommandRequest{
		Context: requestContext, CommandId: HostNotificationEmitCommandID,
		CommandVersion: HostNotificationEmitCommandVersion,
		IdempotencyKey: input.IdempotencyKey, Input: document,
	}, nil
}

// EmitNotification executes notifications.emit@1 through the runtime-scoped
// Host broker. A rejected command is returned as a normal CommandResult with a
// stable ErrorDetail reason, matching all Host Command families.
func (h *Host) EmitNotification(ctx context.Context, parent *protocolwire.RequestContext, input NotificationEmitInput) (*hostwire.CommandResult, error) {
	request, err := h.NotificationEmitRequest(parent, input)
	if err != nil {
		return nil, err
	}
	return h.Commands.Execute(ctx, request)
}

func validHostNotificationIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}
