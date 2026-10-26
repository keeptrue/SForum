package extensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const LifecycleAuthoritySnapshotSchemaV1 = "sforum.lifecycle.authority@1"

// LifecycleAuthoritySnapshot 是写入 Host lifecycle ledger 的不可变执行授权。
type LifecycleAuthoritySnapshot struct {
	SchemaVersion string      `json:"schemaVersion"`
	AuthorityType string      `json:"authorityType"`
	ActorUserID   int64       `json:"actorUserId"`
	Impact        TrustImpact `json:"impact"`
	Grant         *TrustGrant `json:"grant,omitempty"`
}

type LifecycleOperationIntent struct {
	Operation       LifecycleMachineOperation
	IdempotencyKey  string
	SourceExtension *Extension
	ActionInputs    map[LifecycleMachineAction]json.RawMessage
	RemovalMode     string
	Forced          bool
	Retry           bool
	SkipFailedStep  bool
	SkipReason      string
	EscalateForced  bool
	RecoveryReason  string
	AuditEventID    int64
	// FrozenAuthority 允许 disable/rollback/uninstall 复用目标制品最后一次成功授权。
	// 当前请求 actor 仍写入 operation/audit；授权 actor 保留在 immutable snapshot。
	FrozenAuthority bool
}

// ConfirmLifecycleAuthority 消费或复用 exact-artifact grant，并返回可持久化的授权快照。
func (s *ExecutableTrustService) ConfirmLifecycleAuthority(
	ctx context.Context,
	actor identity.Actor,
	extension Extension,
	confirmationToken string,
) (LifecycleAuthoritySnapshot, error) {
	if s == nil || s.store == nil {
		return LifecycleAuthoritySnapshot{}, ErrTrustChallengeInvalid
	}
	impact, err := buildTrustImpact(extension, TrustActionEnable)
	if err != nil {
		return LifecycleAuthoritySnapshot{}, err
	}
	authority := LifecycleAuthoritySnapshot{
		SchemaVersion: LifecycleAuthoritySnapshotSchemaV1,
		ActorUserID:   actor.ID,
		Impact:        impact,
	}
	if extension.Source == SourceBuiltin && !RequiresExecutableTrust(extension) {
		authority.AuthorityType = LifecycleAuthorityBuiltin
		return authority, nil
	}
	if !RequiresExecutableTrust(extension) {
		return LifecycleAuthoritySnapshot{}, fmt.Errorf("%w: uploaded lifecycle code requires an exact grant", ErrLifecycleCoordinatorInvalid)
	}
	if err := s.ConfirmEnable(ctx, actor, extension, confirmationToken); err != nil {
		return LifecycleAuthoritySnapshot{}, err
	}
	grant, err := s.store.LiveGrant(ctx, trustIdentity(impact))
	if err != nil {
		return LifecycleAuthoritySnapshot{}, err
	}
	if grant.ID <= 0 || grant.ExtensionID != extension.ID || grant.ExtensionVersion != extension.Version ||
		grant.PackageDigest != extension.PackageDigest || grant.ImpactDigest != impact.Digest {
		return LifecycleAuthoritySnapshot{}, ErrTrustChallengeStale
	}
	authority.AuthorityType = LifecycleAuthorityTrustGrant
	authority.Grant = &grant
	return authority, nil
}

// BuildLifecycleCoordinatorRunInput 将授权、制品和操作意图绑定为可重放的 ledger 输入。
func BuildLifecycleCoordinatorRunInput(
	extension Extension,
	actor identity.Actor,
	authority LifecycleAuthoritySnapshot,
	intent LifecycleOperationIntent,
) (LifecycleCoordinatorRunInput, error) {
	if extension.Manifest.Lifecycle == nil || strings.TrimSpace(extension.Manifest.Lifecycle.ContractVersion) == "" {
		return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: lifecycle v2 contract is required", ErrLifecycleCoordinatorInvalid)
	}
	if _, err := RecommendedLifecyclePath(intent.Operation); err != nil {
		return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: %v", ErrLifecycleCoordinatorInvalid, err)
	}
	if err := validateLifecycleSourceArtifact(intent.Operation, extension, intent.SourceExtension); err != nil {
		return LifecycleCoordinatorRunInput{}, err
	}
	idempotencyKey := strings.TrimSpace(intent.IdempotencyKey)
	if idempotencyKey == "" || idempotencyKey != intent.IdempotencyKey || len(idempotencyKey) > 512 {
		return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: stable idempotency key is required", ErrLifecycleCoordinatorInvalid)
	}
	authorityActor := actor
	if intent.FrozenAuthority {
		if intent.Operation != LifecycleMachineDisable && intent.Operation != LifecycleMachineRollback &&
			intent.Operation != LifecycleMachineUninstall {
			return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: frozen authority is limited to disable, rollback, and uninstall", ErrLifecycleCoordinatorInvalid)
		}
		authorityActor.ID = authority.ActorUserID
	}
	if intent.Retry {
		if actor.ID <= 0 || intent.AuditEventID <= 0 {
			return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: recovery actor and audit identities are required", ErrLifecycleCoordinatorInvalid)
		}
		authorityActor.ID = authority.ActorUserID
	}
	if err := validateLifecycleAuthority(extension, authorityActor, authority); err != nil {
		return LifecycleCoordinatorRunInput{}, err
	}
	if err := validateLifecycleRemovalIntent(intent); err != nil {
		return LifecycleCoordinatorRunInput{}, err
	}
	if err := validateLifecycleRecoveryIntent(intent); err != nil {
		return LifecycleCoordinatorRunInput{}, err
	}
	actionInputs, fingerprintInputs, err := canonicalLifecycleActionInputs(intent.Operation, intent.ActionInputs)
	if err != nil {
		return LifecycleCoordinatorRunInput{}, err
	}
	var sourceExtension *Extension
	if intent.SourceExtension != nil {
		value := *intent.SourceExtension
		sourceExtension = &value
	}
	authorityJSON, err := json.Marshal(authority)
	if err != nil {
		return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: encode lifecycle authority: %v", ErrLifecycleCoordinatorInvalid, err)
	}
	artifactJSON, err := json.Marshal(authority.Impact.ArtifactDigests)
	if err != nil {
		return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: encode lifecycle artifacts: %v", ErrLifecycleCoordinatorInvalid, err)
	}
	fingerprintDocument := struct {
		ExtensionID   string                     `json:"extensionId"`
		Version       string                     `json:"version"`
		PackageDigest string                     `json:"packageDigest"`
		Operation     LifecycleMachineOperation  `json:"operation"`
		PlanVersion   string                     `json:"planVersion"`
		Authority     json.RawMessage            `json:"authority"`
		ActionInputs  map[string]json.RawMessage `json:"actionInputs"`
		RemovalMode   string                     `json:"removalMode,omitempty"`
		Forced        bool                       `json:"forced"`
		SourceBinding LifecycleRuntimeBinding    `json:"sourceBinding,omitempty"`
	}{
		ExtensionID: extension.ID, Version: extension.Version, PackageDigest: extension.PackageDigest,
		Operation: intent.Operation, PlanVersion: extension.Manifest.Lifecycle.ContractVersion,
		Authority: authorityJSON, ActionInputs: fingerprintInputs,
		RemovalMode: intent.RemovalMode, Forced: intent.Forced,
		SourceBinding: lifecycleOptionalRuntimeBinding(sourceExtension),
	}
	fingerprintJSON, err := json.Marshal(fingerprintDocument)
	if err != nil {
		return LifecycleCoordinatorRunInput{}, fmt.Errorf("%w: encode lifecycle fingerprint: %v", ErrLifecycleCoordinatorInvalid, err)
	}
	fingerprint := sha256.Sum256(fingerprintJSON)
	trustGrantID := int64(0)
	if authority.Grant != nil {
		trustGrantID = authority.Grant.ID
	}
	initialAuditEventID := intent.AuditEventID
	requestedByUserID := authority.ActorUserID
	if intent.FrozenAuthority && !intent.Retry {
		requestedByUserID = actor.ID
	}
	recoveryActorUserID := int64(0)
	recoveryAuditEventID := int64(0)
	if intent.Retry {
		initialAuditEventID = 0
		recoveryActorUserID = actor.ID
		recoveryAuditEventID = intent.AuditEventID
	}
	return LifecycleCoordinatorRunInput{
		Extension: extension, SourceExtension: sourceExtension,
		Acquire: AcquireLifecycleOperationInput{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, ArtifactDigests: artifactJSON,
			Operation: string(intent.Operation), PlanVersion: extension.Manifest.Lifecycle.ContractVersion,
			IdempotencyKey: idempotencyKey, RequestFingerprint: hex.EncodeToString(fingerprint[:]),
			AuthorityType: authority.AuthorityType, TrustGrantID: trustGrantID,
			AuthoritySnapshot: authorityJSON, RequestedByUserID: requestedByUserID,
			AuditEventID: initialAuditEventID,
			RemovalMode:  intent.RemovalMode, Forced: intent.Forced, ExistingOnly: intent.Retry,
		},
		ActionInputs: actionInputs, Retry: intent.Retry,
		SkipFailedStep: intent.SkipFailedStep, SkipReason: intent.SkipReason,
		RecoveryActorUserID:  recoveryActorUserID,
		RecoveryAuditEventID: recoveryAuditEventID,
		EscalateForced:       intent.EscalateForced, RecoveryReason: intent.RecoveryReason,
	}, nil
}

func validateLifecycleAuthority(extension Extension, actor identity.Actor, authority LifecycleAuthoritySnapshot) error {
	if authority.SchemaVersion != LifecycleAuthoritySnapshotSchemaV1 || authority.ActorUserID != actor.ID ||
		authority.Impact.ExtensionID != extension.ID || authority.Impact.ExtensionVersion != extension.Version ||
		authority.Impact.PackageDigest != extension.PackageDigest || authority.Impact.Action != TrustActionEnable ||
		authority.Impact.Digest == "" ||
		authority.Impact.ArtifactDigests["package"] != extension.PackageDigest {
		return fmt.Errorf("%w: lifecycle authority does not match exact actor and artifact", ErrLifecycleCoordinatorInvalid)
	}
	switch authority.AuthorityType {
	case LifecycleAuthorityBuiltin:
		if extension.Source != SourceBuiltin || authority.Grant != nil {
			return fmt.Errorf("%w: invalid builtin lifecycle authority", ErrLifecycleCoordinatorInvalid)
		}
	case LifecycleAuthorityTrustGrant:
		if authority.Grant == nil || authority.Grant.ID <= 0 || authority.Grant.ExtensionID != extension.ID ||
			authority.Grant.ExtensionVersion != extension.Version || authority.Grant.PackageDigest != extension.PackageDigest ||
			authority.Grant.Action != TrustActionEnable || authority.Grant.ImpactDigest != authority.Impact.Digest ||
			authority.Grant.RevokedAt != nil {
			return fmt.Errorf("%w: invalid lifecycle trust grant", ErrLifecycleCoordinatorInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported lifecycle authority", ErrLifecycleCoordinatorInvalid)
	}
	return nil
}

func validateLifecycleRemovalIntent(intent LifecycleOperationIntent) error {
	if intent.Operation == LifecycleMachineUninstall {
		switch intent.RemovalMode {
		case LifecycleRemovalPreserve, LifecycleRemovalExportThenRemove, LifecycleRemovalComplete:
		default:
			return fmt.Errorf("%w: uninstall removal mode is required", ErrLifecycleCoordinatorInvalid)
		}
	} else if intent.RemovalMode != "" || intent.Forced {
		return fmt.Errorf("%w: removal mode and force are uninstall-only", ErrLifecycleCoordinatorInvalid)
	}
	return nil
}

func validateLifecycleRecoveryIntent(intent LifecycleOperationIntent) error {
	if !intent.Retry {
		if intent.SkipFailedStep || intent.SkipReason != "" || intent.EscalateForced || intent.RecoveryReason != "" {
			return fmt.Errorf("%w: recovery controls require retry", ErrLifecycleCoordinatorInvalid)
		}
		return nil
	}
	if intent.SkipFailedStep {
		if intent.SkipReason == "" || intent.SkipReason != strings.TrimSpace(intent.SkipReason) || len(intent.SkipReason) > 4096 {
			return fmt.Errorf("%w: skip-step requires a stable reason", ErrLifecycleCoordinatorInvalid)
		}
	} else if intent.SkipReason != "" {
		return fmt.Errorf("%w: skip reason requires skip-step", ErrLifecycleCoordinatorInvalid)
	}
	if intent.RecoveryReason != strings.TrimSpace(intent.RecoveryReason) || len(intent.RecoveryReason) > 4096 {
		return fmt.Errorf("%w: recovery reason must be stable", ErrLifecycleCoordinatorInvalid)
	}
	if intent.EscalateForced && (intent.Operation != LifecycleMachineUninstall ||
		(intent.RecoveryReason == "" && intent.SkipReason == "")) {
		return fmt.Errorf("%w: forced recovery is uninstall-only and requires a reason", ErrLifecycleCoordinatorInvalid)
	}
	return nil
}

func canonicalLifecycleActionInputs(
	operation LifecycleMachineOperation,
	inputs map[LifecycleMachineAction]json.RawMessage,
) (map[LifecycleMachineAction]json.RawMessage, map[string]json.RawMessage, error) {
	path, err := RecommendedLifecyclePath(operation)
	if err != nil {
		return nil, nil, err
	}
	allowed := make(map[LifecycleMachineAction]bool)
	for _, step := range path {
		if step.Action != "" {
			allowed[step.Action] = true
		}
	}
	result := make(map[LifecycleMachineAction]json.RawMessage, len(inputs))
	fingerprint := make(map[string]json.RawMessage, len(inputs))
	for action, raw := range inputs {
		if !allowed[action] {
			return nil, nil, fmt.Errorf("%w: action input %q is outside operation %q", ErrLifecycleCoordinatorInvalid, action, operation)
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			continue
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil || document == nil {
			return nil, nil, fmt.Errorf("%w: action input %q must be a JSON object", ErrLifecycleCoordinatorInvalid, action)
		}
		canonical, err := json.Marshal(document)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: canonicalize action input %q: %v", ErrLifecycleCoordinatorInvalid, action, err)
		}
		result[action] = canonical
		fingerprint[string(action)] = canonical
	}
	return result, fingerprint, nil
}
