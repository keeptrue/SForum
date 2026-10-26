package hostapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	CommandNotificationsEmitID            = "notifications.emit"
	CommandNotificationsEmitVersion       = "1"
	CommandNotificationsEmitInputSchema   = "sforum.notifications.emit.input"
	CommandNotificationsEmitOutputSchema  = "sforum.notifications.emit.result"
	CommandNotificationsEmitSchemaVersion = "1"

	ProtocolV2NotificationEmitMaxRecipients     = 50
	ProtocolV2NotificationEmitMaxPayloadBytes   = 16 * 1024
	ProtocolV2NotificationEmitMaxSchemaBytes    = 64 * 1024
	ProtocolV2NotificationEmitRequestsPerMinute = 60
)

var protocolV2NotificationTypePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,160}$`)

type protocolV2NotificationEmitInput struct {
	Type             string                        `json:"type"`
	PayloadVersion   int                           `json:"payloadVersion"`
	Payload          map[string]any                `json:"payload"`
	RecipientUserIDs []string                      `json:"recipientUserIds"`
	Target           *protocolV2NotificationTarget `json:"target,omitempty"`
}

type protocolV2NotificationTarget struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type protocolV2PreparedNotificationEmit struct {
	input          protocolV2NotificationEmitInput
	recipients     []int64
	declaration    extensionmanifest.ManifestNotificationType
	extensionID    string
	artifactDigest string
	idempotencyKey string
	schemaBody     []byte
	schemaDigest   string
	payloadBody    []byte
}

func newProtocolV2NotificationEmitCommandDefinition(pool *pgxpool.Pool, jobs *supportjobs.Dispatcher) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandNotificationsEmitID, Version: CommandNotificationsEmitVersion,
		InputSchemaID: CommandNotificationsEmitInputSchema, InputSchemaVersion: CommandNotificationsEmitSchemaVersion,
		OutputSchemaID: CommandNotificationsEmitOutputSchema, OutputSchemaVersion: CommandNotificationsEmitSchemaVersion,
		ActorMode: protocolV2CommandActorService,
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, recipients, payload, err := protocolV2NotificationEmitInputFromRequest(request)
		if err != nil {
			return nil, err
		}
		return protocolV2NotificationEmitPreparation(input.Type, len(recipients), len(payload), nil)
	}
	definition.Prepare = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		input, recipients, payload, err := protocolV2NotificationEmitInputFromRequest(request)
		if err != nil {
			return nil, protocolV2RejectNotificationEmit(ctx, pool, request, "notification.payload_invalid", err)
		}
		prepared, err := protocolV2PrepareNotificationEmit(ctx, tx, request, input, recipients, payload)
		if err != nil {
			return nil, protocolV2RejectNotificationEmit(ctx, pool, request, protocolV2NotificationRejectReason(err), err)
		}
		return protocolV2NotificationEmitPreparation(input.Type, len(recipients), len(payload), prepared)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		prepared, ok := preparation.private.(*protocolV2PreparedNotificationEmit)
		if !ok || prepared == nil {
			return nil, newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "notification.preparation_invalid", "The notification emission preparation is unavailable.", false)
		}
		created, skipped, revision, err := protocolV2ExecuteNotificationEmit(ctx, tx, jobs, prepared)
		if err != nil {
			return nil, err
		}
		output, err := protocolV2NotificationEmitDocument(prepared.input.Type, len(prepared.recipients), created, skipped, false)
		if err != nil {
			return nil, err
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: revision}, nil
	}
	return definition
}

func protocolV2NotificationEmitInputFromRequest(request *hostv2.CommandRequest) (protocolV2NotificationEmitInput, []int64, []byte, error) {
	input, err := decodeProtocolV2CommandInput[protocolV2NotificationEmitInput](request)
	if err != nil {
		return input, nil, nil, protocolV2NotificationError("notification.payload_invalid")
	}
	input.Type = strings.TrimSpace(input.Type)
	if !protocolV2NotificationTypePattern.MatchString(input.Type) || input.PayloadVersion <= 0 || input.Payload == nil ||
		len(input.RecipientUserIDs) == 0 || len(input.RecipientUserIDs) > ProtocolV2NotificationEmitMaxRecipients {
		return input, nil, nil, protocolV2NotificationError("notification.payload_invalid")
	}
	recipients := make([]int64, 0, len(input.RecipientUserIDs))
	seen := make(map[int64]struct{}, len(input.RecipientUserIDs))
	for _, raw := range input.RecipientUserIDs {
		if raw != strings.TrimSpace(raw) || raw == "" {
			return input, nil, nil, protocolV2NotificationError("notification.recipient_invalid")
		}
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
			return input, nil, nil, protocolV2NotificationError("notification.recipient_invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return input, nil, nil, protocolV2NotificationError("notification.recipient_invalid")
		}
		seen[id] = struct{}{}
		recipients = append(recipients, id)
	}
	slices.Sort(recipients)
	payload, err := json.Marshal(input.Payload)
	if err != nil || len(payload) == 0 || len(payload) > ProtocolV2NotificationEmitMaxPayloadBytes {
		return input, nil, nil, protocolV2NotificationError("notification.payload_invalid")
	}
	return input, recipients, payload, nil
}

func protocolV2PrepareNotificationEmit(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, input protocolV2NotificationEmitInput, recipients []int64, payload []byte) (*protocolV2PreparedNotificationEmit, error) {
	runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
	if tx == nil || runtime == nil || request == nil || request.GetContext().GetExtension().GetExtensionId() != runtime.GetExtensionId() {
		return nil, protocolV2NotificationError("notification.type_inactive")
	}
	if !strings.HasPrefix(input.Type, runtime.GetExtensionId()+".") {
		return nil, protocolV2NotificationError("notification.type_not_owned")
	}
	// The per-extension lock makes the rolling request rate bound authoritative
	// across concurrent API/worker brokers on every node.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "sforum:notification-emit@1:"+runtime.GetExtensionId()); err != nil {
		return nil, fmt.Errorf("lock notification emission rate: %w", err)
	}
	var recent int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM extension_host_command_receipts
		WHERE extension_id=$1 AND command_id=$2 AND command_version=$3
		  AND committed_at >= statement_timestamp() - interval '1 minute'`, runtime.GetExtensionId(), CommandNotificationsEmitID, CommandNotificationsEmitVersion).Scan(&recent); err != nil {
		return nil, fmt.Errorf("read notification emission rate: %w", err)
	}
	if recent >= ProtocolV2NotificationEmitRequestsPerMinute {
		return nil, protocolV2NotificationError("notification.rate_limited")
	}

	var manifestBody []byte
	var packagePath string
	err := tx.QueryRow(ctx, `
		SELECT version.manifest, version.package_path
		FROM extensions extension
		JOIN extension_versions version ON version.id=extension.active_version_id
		WHERE extension.id=$1 AND extension.status='enabled'
		  AND version.version=$2 AND version.package_digest=$3
		FOR SHARE OF extension, version`, runtime.GetExtensionId(), runtime.GetExtensionVersion(), runtime.GetArtifactDigest()).Scan(&manifestBody, &packagePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, protocolV2NotificationError("notification.type_inactive")
	}
	if err != nil {
		return nil, fmt.Errorf("resolve notification type artifact: %w", err)
	}
	var manifest extensionmanifest.Manifest
	if json.Unmarshal(manifestBody, &manifest) != nil || manifest.ID != runtime.GetExtensionId() {
		return nil, protocolV2NotificationError("notification.type_inactive")
	}
	var declaration *extensionmanifest.ManifestNotificationType
	for i := range manifest.NotificationTypes {
		if manifest.NotificationTypes[i].ID == input.Type {
			declaration = &manifest.NotificationTypes[i]
			break
		}
	}
	if declaration == nil {
		return nil, protocolV2NotificationError("notification.type_unknown")
	}
	if declaration.Required {
		return nil, protocolV2NotificationError("notification.type_not_owned")
	}
	if declaration.ContractVersion != declaration.ID+"@1" || declaration.PayloadVersion != input.PayloadVersion {
		return nil, protocolV2NotificationError("notification.payload_invalid")
	}
	if !protocolV2NotificationTargetMatches(*declaration, input.Target) {
		return nil, protocolV2NotificationError("notification.target_invalid")
	}
	var publishedPayloadVersion int
	var publishedCategory, publishedOwner, publishedArtifact, publishedSchema string
	var publishedTarget []byte
	err = tx.QueryRow(ctx, `
		SELECT payload_version,category,owner_extension_id,owner_artifact_digest,
		       COALESCE(payload_schema->>'contract',''),target_contract
		FROM notification_type_descriptors
		WHERE type=$1 AND active=TRUE
		FOR SHARE`, declaration.ID).Scan(
		&publishedPayloadVersion, &publishedCategory, &publishedOwner, &publishedArtifact,
		&publishedSchema, &publishedTarget,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, protocolV2NotificationError("notification.type_inactive")
	}
	if err != nil {
		return nil, fmt.Errorf("resolve active notification descriptor: %w", err)
	}
	var targetContract protocolV2NotificationTarget
	if json.Unmarshal(publishedTarget, &targetContract) != nil || publishedOwner != runtime.GetExtensionId() ||
		publishedArtifact != runtime.GetArtifactDigest() || publishedPayloadVersion != declaration.PayloadVersion ||
		publishedCategory != declaration.Category || publishedSchema != declaration.PayloadSchema ||
		targetContract.Kind != declaration.TargetKind || targetContract.ID != declaration.TargetID {
		return nil, protocolV2NotificationError("notification.type_not_owned")
	}
	schemaFile, ok := protocolV2NotificationSchemaFile(manifest.PackageFiles, declaration.PayloadSchema)
	if !ok {
		return nil, protocolV2NotificationError("notification.payload_invalid")
	}
	schemaBody, digest, err := protocolV2LoadNotificationSchema(packagePath, schemaFile)
	if err != nil || protocolV2ValidateNotificationPayload(schemaBody, digest, payload) != nil {
		return nil, protocolV2NotificationError("notification.payload_invalid")
	}
	var activeCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=ANY($1) AND status='active'`, recipients).Scan(&activeCount); err != nil {
		return nil, fmt.Errorf("validate notification recipients: %w", err)
	}
	if activeCount != len(recipients) {
		return nil, protocolV2NotificationError("notification.recipient_invalid")
	}
	return &protocolV2PreparedNotificationEmit{
		input: input, recipients: recipients, declaration: *declaration,
		extensionID: runtime.GetExtensionId(), artifactDigest: runtime.GetArtifactDigest(),
		idempotencyKey: request.GetIdempotencyKey(), schemaBody: schemaBody, schemaDigest: digest, payloadBody: payload,
	}, nil
}

func protocolV2NotificationSchemaFile(files []extensionmanifest.ManifestPackageFile, ref string) (extensionmanifest.ManifestPackageFile, bool) {
	separator := strings.LastIndexByte(ref, '@')
	if separator <= 0 || separator == len(ref)-1 {
		return extensionmanifest.ManifestPackageFile{}, false
	}
	for _, file := range files {
		if file.Kind == "schema" && file.ID == ref[:separator] && file.Version == ref[separator+1:] {
			return file, true
		}
	}
	return extensionmanifest.ManifestPackageFile{}, false
}

func protocolV2LoadNotificationSchema(root string, file extensionmanifest.ManifestPackageFile) ([]byte, string, error) {
	root = filepath.Clean(root)
	path := filepath.Join(root, filepath.FromSlash(file.Path))
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", errors.New("notification schema path escaped artifact")
	}
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 || len(body) > ProtocolV2NotificationEmitMaxSchemaBytes {
		return nil, "", errors.New("notification schema unavailable")
	}
	digest := sha256.Sum256(body)
	encoded := hex.EncodeToString(digest[:])
	if encoded != strings.ToLower(file.Digest) {
		return nil, "", errors.New("notification schema digest mismatch")
	}
	return body, encoded, nil
}

func protocolV2ValidateNotificationPayload(schemaBody []byte, digest string, payload []byte) error {
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBody))
	if err != nil || protocolV2NotificationSchemaHasExternalRef(document, 0) {
		return errors.New("notification payload schema invalid")
	}
	resource := "https://sforum.invalid/notification-payload/" + digest + ".json"
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.AssertFormat()
	if err := compiler.AddResource(resource, document); err != nil {
		return err
	}
	validator, err := compiler.Compile(resource)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	return validator.Validate(value)
}

func protocolV2NotificationSchemaHasExternalRef(value any, depth int) bool {
	if depth > 64 {
		return true
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#") {
					return true
				}
			}
			if protocolV2NotificationSchemaHasExternalRef(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if protocolV2NotificationSchemaHasExternalRef(child, depth+1) {
				return true
			}
		}
	}
	return false
}

func protocolV2NotificationTargetMatches(declaration extensionmanifest.ManifestNotificationType, target *protocolV2NotificationTarget) bool {
	if declaration.TargetKind == "none" {
		return target == nil || strings.TrimSpace(target.Kind) == "none" && strings.TrimSpace(target.ID) == ""
	}
	return target != nil && target.Kind == declaration.TargetKind && target.ID == declaration.TargetID
}

func protocolV2ExecuteNotificationEmit(ctx context.Context, tx pgx.Tx, jobs *supportjobs.Dispatcher, prepared *protocolV2PreparedNotificationEmit) (created, skipped int, revision string, err error) {
	declaration := prepared.declaration

	store := notifications.NewPostgresStore(nil)
	outbox := notifications.NewOutbox(nil, store, jobs)
	for _, recipientID := range prepared.recipients {
		keyDigest := sha256.Sum256([]byte(prepared.extensionID + "\x00" + prepared.idempotencyKey + "\x00" + prepared.input.Type + "\x00" + strconv.FormatInt(recipientID, 10)))
		input := notifications.CreateInput{
			RecipientUserID: recipientID, Type: declaration.ID, Category: declaration.Category,
			TypeVersion: 1, PayloadVersion: declaration.PayloadVersion,
			TargetType: declaration.TargetKind, TargetID: 0, Payload: prepared.payloadBody,
			DedupeKey: "plugin:" + hex.EncodeToString(keyDigest[:]),
		}
		projection, err := outbox.CreateProjectionsResultTx(ctx, tx, notifications.CreateBundleInput{
			Notification: input,
			Channels:     declaration.Channels,
		})
		if err != nil {
			return 0, 0, "", fmt.Errorf("create plugin notification projections: %w", err)
		}
		if projection.InApp {
			targetMeta, _ := json.Marshal(prepared.input.Target)
			if _, err := tx.Exec(ctx, `UPDATE notifications SET target_meta=$2::jsonb WHERE dedupe_key=$1`, input.DedupeKey, targetMeta); err != nil {
				return 0, 0, "", fmt.Errorf("bind plugin notification target: %w", err)
			}
		}
		if !projection.InApp && !projection.Email && !projection.WebPush {
			skipped++
			continue
		}
		created++
	}
	return created, skipped, strconv.Itoa(created), nil
}

func protocolV2NotificationEmitPreparation(typeID string, recipients, payloadBytes int, private any) (*protocolV2CommandPreparation, error) {
	document, err := protocolV2NotificationEmitDocument(typeID, recipients, 0, 0, true)
	if err != nil {
		return nil, err
	}
	return &protocolV2CommandPreparation{
		Policy:          []*hostv2.PolicyDecision{{PolicyId: "sforum.notifications.emit@1", ResourceId: typeID, Allowed: true, Reason: "The Host validates exact type ownership, recipients, payload, target, policy, and rate at execution time."}},
		Impact:          []*hostv2.ImpactItem{{Module: "notifications", Action: "emit", ResourceType: "notification_type", ResourceId: typeID, Summary: fmt.Sprintf("Create at most %d recipient-owned notification projections from a %d-byte structured payload.", recipients, payloadBytes), Reversible: false}},
		ProjectedResult: document, private: private,
	}, nil
}

func protocolV2NotificationEmitDocument(typeID string, recipients, created, skipped int, planned bool) (*protocolv2.TypedDocument, error) {
	return protocolV2Document(CommandNotificationsEmitOutputSchema, CommandNotificationsEmitSchemaVersion, map[string]any{
		"planned": planned, "type": typeID, "recipientCount": strconv.Itoa(recipients),
		"createdCount": strconv.Itoa(created), "skippedCount": strconv.Itoa(skipped),
	})
}

func protocolV2NotificationError(reason string) error {
	code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	message := "The notification emission request is invalid."
	retryable := false
	switch reason {
	case "notification.type_inactive":
		code, message = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "The notification type is not active for this exact runtime."
	case "notification.type_not_owned":
		code, message = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "The notification type is not owned by this exact extension artifact."
	case "notification.type_unknown":
		code, message = protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "The notification type is unavailable."
	case "notification.payload_invalid":
		message = "The notification payload does not satisfy its active schema and bounds."
	case "notification.target_invalid":
		message = "The notification target does not match the declared safe target."
	case "notification.recipient_invalid":
		message = "One or more notification recipients are unavailable."
	case "notification.rate_limited":
		code, message, retryable = protocolv2.ErrorCode_ERROR_CODE_RATE_LIMITED, "The notification emission rate limit was reached.", true
	}
	return newProtocolV2CommandError(code, reason, message, retryable)
}

func protocolV2NotificationRejectReason(err error) string {
	var commandErr *protocolV2CommandError
	if errors.As(err, &commandErr) && commandErr.detail != nil && strings.HasPrefix(commandErr.detail.GetReason(), "notification.") {
		return commandErr.detail.GetReason()
	}
	return "notification.payload_invalid"
}

func protocolV2RejectNotificationEmit(ctx context.Context, pool *pgxpool.Pool, request *hostv2.CommandRequest, reason string, rejection error) error {
	if pool == nil {
		return rejection
	}
	runtime := ProtocolV2RuntimeIdentityFromContext(ctx)
	if runtime == nil {
		return rejection
	}
	typeID, recipientCount, payloadBytes := "", 0, 0
	if request != nil && request.GetInput() != nil && request.GetInput().GetValue() != nil {
		values := request.GetInput().GetValue().AsMap()
		typeID, _ = values["type"].(string)
		if values, ok := values["recipientUserIds"].([]any); ok {
			recipientCount = len(values)
		}
		if payload, ok := values["payload"]; ok {
			body, _ := json.Marshal(payload)
			payloadBytes = len(body)
		}
	}
	metadata, _ := json.Marshal(map[string]any{
		"schemaVersion": "sforum.notification-emit-rejection@1", "extensionId": runtime.GetExtensionId(),
		"extensionVersion": runtime.GetExtensionVersion(), "packageDigest": runtime.GetArtifactDigest(),
		"commandId": CommandNotificationsEmitID, "commandVersion": CommandNotificationsEmitVersion,
		"type": typeID, "recipientCount": recipientCount, "payloadBytes": payloadBytes, "reason": reason,
	})
	if _, err := pool.Exec(ctx, `INSERT INTO audit_events (action,metadata) VALUES ('extension.notification_emit.rejected',$1::jsonb)`, metadata); err != nil {
		return newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "notification.audit_unavailable", "The notification emission audit is unavailable.", true)
	}
	return rejection
}
