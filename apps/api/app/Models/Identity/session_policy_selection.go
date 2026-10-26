package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	IdentitySessionPolicyCoreDefault    = "core.session.default"
	IdentitySessionPolicySourceCore     = "core_default"
	IdentitySessionPolicySourcePlugin   = "selected"
	IdentitySessionPolicySourceSafeMode = "safe_mode"

	IdentitySessionPolicyActionSelect     = "select"
	IdentitySessionPolicyActionReset      = "reset"
	IdentitySessionPolicyActionInvalidate = "invalidate"
)

var (
	ErrIdentitySessionPolicyInvalid          = errors.New("identity: session policy input is invalid")
	ErrIdentitySessionPolicyRevisionConflict = errors.New("identity: session policy revision changed")
	ErrIdentitySessionPolicyDeclarationStale = errors.New("identity: session policy declaration is stale")
	ErrIdentitySessionPolicyPermissionDenied = errors.New("identity: session policy permission denied")
	ErrIdentitySessionPolicySafeMode         = errors.New("identity: session policy selection is unavailable in safe mode")
	ErrIdentitySessionPolicyStoreUnavailable = errors.New("identity: session policy store is unavailable")
)

// IdentitySessionPolicyEvidence is the immutable, metadata-only claim stored
// in the append-only event ledger. Actor, audit, timestamp, and runtime data
// deliberately live outside this JSON object.
type IdentitySessionPolicyEvidence struct {
	PolicyID                string `json:"policyId"`
	ProviderContractVersion string `json:"providerContractVersion,omitempty"`
	OwnerExtensionID        string `json:"ownerExtensionId,omitempty"`
	OwnerExtensionVersionID int64  `json:"ownerExtensionVersionId,omitempty"`
	OwnerExtensionVersion   string `json:"ownerExtensionVersion,omitempty"`
	OwnerPackageDigest      string `json:"ownerPackageDigest,omitempty"`
	DeclarationRevision     int64  `json:"declarationRevision,omitempty"`
}

type IdentitySessionPolicySelection struct {
	IdentitySessionPolicyEvidence
	Revision              int64     `json:"revision"`
	SelectedByUserID      int64     `json:"selectedByUserId,omitempty"`
	SelectionAuditEventID int64     `json:"selectionAuditEventId,omitempty"`
	SelectedAt            time.Time `json:"selectedAt,omitempty"`
	UpdatedAt             time.Time `json:"updatedAt,omitempty"`
	Implicit              bool      `json:"implicit,omitempty"`
}

type IdentitySessionPolicyEvent struct {
	ID                int64                          `json:"id"`
	Action            string                         `json:"action"`
	PreviousSelection *IdentitySessionPolicyEvidence `json:"previousSelection,omitempty"`
	SelectedSelection *IdentitySessionPolicyEvidence `json:"selectedSelection,omitempty"`
	ActorUserID       int64                          `json:"actorUserId,omitempty"`
	AuditEventID      int64                          `json:"auditEventId"`
	ReasonCode        string                         `json:"reasonCode,omitempty"`
	SelectionRevision int64                          `json:"selectionRevision"`
	CreatedAt         time.Time                      `json:"createdAt"`
}

type IdentitySessionPolicyMutation struct {
	Selection IdentitySessionPolicySelection `json:"selection"`
	Event     *IdentitySessionPolicyEvent    `json:"event,omitempty"`
	Changed   bool                           `json:"changed"`
}

// IdentitySessionPolicyResolution is one coherent effective lookup claim, not
// final execution authority. Consumers must hold exact runtime admission and
// recheck Selection revision immediately before the Host effect. Current stays
// the separately inspectable durable desired state; Safe Mode returns Core here
// without reading or rewriting that desired selection.
type IdentitySessionPolicyResolution struct {
	PolicyID         string                                 `json:"policyId"`
	Source           string                                 `json:"source"`
	Selection        *IdentitySessionPolicySelection        `json:"selection,omitempty"`
	Provider         *identityregistry.ProviderContribution `json:"provider,omitempty"`
	RegistryRevision uint64                                 `json:"registryRevision"`
	RegistryDigest   string                                 `json:"registryDigest"`
}

type SelectIdentitySessionPolicyInput struct {
	Candidate        IdentitySessionPolicyEvidence
	ExpectedRevision int64
	ActorUserID      int64
}

type ResetIdentitySessionPolicyInput struct {
	ExpectedRevision int64
	ActorUserID      int64
	ReasonCode       string
}

type IdentitySessionPolicyStore interface {
	// Current returns the durable desired selection, including in Safe Mode.
	Current(context.Context) (IdentitySessionPolicySelection, error)
	Candidate(context.Context, string) (IdentitySessionPolicyEvidence, error)
	Resolve(context.Context) (IdentitySessionPolicyResolution, error)
	Select(context.Context, SelectIdentitySessionPolicyInput) (IdentitySessionPolicyMutation, error)
	// Reset is an explicit Host recovery action and remains available in Safe
	// Mode; the automatic Safe Mode override itself never calls it.
	Reset(context.Context, ResetIdentitySessionPolicyInput) (IdentitySessionPolicyMutation, error)
	ListEvents(context.Context, int) ([]IdentitySessionPolicyEvent, error)
}

// IdentitySessionPolicyEffectStore is the additive production acceptance
// boundary. It keeps the exact durable provider/selection authority locked
// until effect returns. Read-only/legacy Store implementations may still
// support inspection through IdentitySessionPolicyStore, but issue/renew fail
// closed unless this interface is available. The effect must not synchronously
// call Select, Reset, Registry mutation, or lifecycle reconciliation because
// those paths require the exclusive authority currently held by this call.
type IdentitySessionPolicyEffectStore interface {
	RunIfCurrent(
		context.Context,
		IdentitySessionPolicyResolution,
		IdentitySessionAuthority,
		func(context.Context) error,
	) error
}

// IdentitySessionAuthority is the Host authority observed before authentication
// or registration completed. The final effect must still see this exact token
// revision while the active user row is locked.
type IdentitySessionAuthority struct {
	UserID       int64
	TokenVersion int64
}

type IdentitySessionPolicyCommitUnknownError struct {
	CommitError       error
	VerificationError error
}

func (e *IdentitySessionPolicyCommitUnknownError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"identity: session policy commit outcome is unknown: %v; verification: %v",
		e.CommitError,
		e.VerificationError,
	)
}

func (e *IdentitySessionPolicyCommitUnknownError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.CommitError, e.VerificationError}
}

type preparedIdentitySessionPolicySelect struct {
	candidate        IdentitySessionPolicyEvidence
	expectedRevision int64
	actorUserID      int64
}

type preparedIdentitySessionPolicyReset struct {
	expectedRevision int64
	actorUserID      int64
	reasonCode       string
}

func prepareIdentitySessionPolicySelect(
	input SelectIdentitySessionPolicyInput,
) (preparedIdentitySessionPolicySelect, error) {
	prepared := preparedIdentitySessionPolicySelect{
		candidate:        input.Candidate,
		expectedRevision: input.ExpectedRevision,
		actorUserID:      input.ActorUserID,
	}
	prepared.candidate.PolicyID = strings.ToLower(strings.TrimSpace(prepared.candidate.PolicyID))
	if prepared.candidate.PolicyID == IdentitySessionPolicyCoreDefault ||
		!validIdentitySessionPolicyEvidence(prepared.candidate) ||
		prepared.expectedRevision < 0 || prepared.actorUserID <= 0 {
		return preparedIdentitySessionPolicySelect{}, ErrIdentitySessionPolicyInvalid
	}
	return prepared, nil
}

func prepareIdentitySessionPolicyReset(
	input ResetIdentitySessionPolicyInput,
) (preparedIdentitySessionPolicyReset, error) {
	prepared := preparedIdentitySessionPolicyReset{
		expectedRevision: input.ExpectedRevision,
		actorUserID:      input.ActorUserID,
		reasonCode:       strings.ToLower(strings.TrimSpace(input.ReasonCode)),
	}
	if prepared.expectedRevision < 0 || prepared.actorUserID <= 0 ||
		!validIdentitySessionPolicyReason(prepared.reasonCode, false) {
		return preparedIdentitySessionPolicyReset{}, ErrIdentitySessionPolicyInvalid
	}
	return prepared, nil
}

func validIdentitySessionPolicyReason(value string, required bool) bool {
	if value == "" {
		return !required
	}
	if len(value) > 128 {
		return false
	}
	first := value[0]
	if (first < 'a' || first > 'z') && (first < '0' || first > '9') {
		return false
	}
	for _, character := range []byte(value[1:]) {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') &&
			character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func identitySessionPolicyCoreSelection(revision int64, implicit bool) IdentitySessionPolicySelection {
	return IdentitySessionPolicySelection{
		IdentitySessionPolicyEvidence: IdentitySessionPolicyEvidence{PolicyID: IdentitySessionPolicyCoreDefault},
		Revision:                      revision,
		Implicit:                      implicit,
	}
}

func identitySessionPolicyProviderHasEvaluate(provider identityregistry.ProviderContribution) bool {
	if provider.Kind != identityregistry.ProviderKindSession || provider.Artifact.Core ||
		provider.Artifact.VersionID <= 0 || provider.Artifact.RuntimeInstanceID == "" {
		return false
	}
	for _, operation := range provider.Operations {
		if operation.Name == "session.evaluate" &&
			operation.FailurePolicy == identityregistry.ProviderFailureFailClosed {
			return true
		}
	}
	return false
}

func identitySessionPolicyProviderMatches(
	left identityregistry.ProviderContribution,
	right identityregistry.ProviderContribution,
) bool {
	return left.Artifact == right.Artifact &&
		identitySessionPolicyProviderDefinitionMatches(left.Provider, right.Provider)
}

func identitySessionPolicyProviderDefinitionMatches(
	left identityregistry.Provider,
	right identityregistry.Provider,
) bool {
	if left.ID != right.ID || left.ContractVersion != right.ContractVersion || left.Kind != right.Kind ||
		left.Handler != right.Handler || left.Priority != right.Priority ||
		len(left.Operations) != len(right.Operations) {
		return false
	}
	for index := range left.Operations {
		leftOperation := left.Operations[index]
		rightOperation := right.Operations[index]
		if leftOperation.Name != rightOperation.Name ||
			leftOperation.InputSchema != rightOperation.InputSchema ||
			leftOperation.InputSchemaWireReference != rightOperation.InputSchemaWireReference ||
			leftOperation.InputSchemaDigest != rightOperation.InputSchemaDigest ||
			leftOperation.OutputSchema != rightOperation.OutputSchema ||
			leftOperation.OutputSchemaWireReference != rightOperation.OutputSchemaWireReference ||
			leftOperation.OutputSchemaDigest != rightOperation.OutputSchemaDigest ||
			leftOperation.TimeoutMS != rightOperation.TimeoutMS ||
			leftOperation.FailurePolicy != rightOperation.FailurePolicy {
			return false
		}
	}
	return true
}
