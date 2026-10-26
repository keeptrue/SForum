package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	ExternalIdentityLinkStatusActive   = "active"
	ExternalIdentityLinkStatusUnlinked = "unlinked"
	ExternalIdentityLinkStatusErased   = "erased"

	ExternalIdentityLinkActionLink   = "link"
	ExternalIdentityLinkActionUnlink = "unlink"
	ExternalIdentityLinkActionErase  = "erase"
)

var (
	ErrExternalIdentityLinkInvalid             = errors.New("identity: external link input is invalid")
	ErrExternalIdentityLinkNotFound            = errors.New("identity: external link was not found")
	ErrExternalIdentityLinkStateConflict       = errors.New("identity: external link state changed")
	ErrExternalIdentityLinkIdempotencyConflict = errors.New("identity: external link idempotency fingerprint changed")
	ErrExternalIdentitySubjectConflict         = errors.New("identity: external subject is already linked")
	ErrExternalIdentityProviderStale           = errors.New("identity: external provider declaration is stale")
	ErrExternalIdentityLinkStoreUnavailable    = errors.New("identity: external link store is unavailable")
)

type ExternalIdentityLink struct {
	ID                      int64      `json:"id"`
	UserID                  int64      `json:"userId"`
	ProviderID              string     `json:"providerId"`
	ProviderContractVersion string     `json:"providerContractVersion"`
	OwnerExtensionID        string     `json:"ownerExtensionId"`
	OwnerExtensionVersionID int64      `json:"ownerExtensionVersionId"`
	OwnerExtensionVersion   string     `json:"ownerExtensionVersion"`
	OwnerPackageDigest      string     `json:"ownerPackageDigest"`
	DeclarationRevision     int64      `json:"declarationRevision"`
	Status                  string     `json:"status"`
	Revision                int64      `json:"revision"`
	LinkedAt                time.Time  `json:"linkedAt"`
	UnlinkedAt              *time.Time `json:"unlinkedAt,omitempty"`
	ErasedAt                *time.Time `json:"erasedAt,omitempty"`
	ActorUserID             int64      `json:"actorUserId,omitempty"`
	AuditEventID            int64      `json:"auditEventId"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

type ExternalIdentityLinkEvent struct {
	ID                      int64     `json:"id"`
	LinkID                  int64     `json:"linkId"`
	ProviderID              string    `json:"providerId"`
	ProviderContractVersion string    `json:"providerContractVersion"`
	OwnerExtensionID        string    `json:"ownerExtensionId"`
	Action                  string    `json:"action"`
	IdempotencyKey          string    `json:"idempotencyKey"`
	RequestFingerprint      string    `json:"requestFingerprint"`
	PreviousRevision        int64     `json:"previousRevision,omitempty"`
	NextRevision            int64     `json:"nextRevision"`
	PreviousStatus          string    `json:"previousStatus,omitempty"`
	NextStatus              string    `json:"nextStatus"`
	ActorUserID             int64     `json:"actorUserId,omitempty"`
	AuditEventID            int64     `json:"auditEventId"`
	CreatedAt               time.Time `json:"createdAt"`
}

type ExternalIdentityLinkMutation struct {
	Link     ExternalIdentityLink      `json:"link"`
	Event    ExternalIdentityLinkEvent `json:"event"`
	Replayed bool                      `json:"replayed"`
}

// ExternalIdentityLinkCommitFence is supplied by the exact runtime resolver.
// The standalone PostgreSQL Link method calls it after all writes and durable
// provider checks, immediately before COMMIT. Tx callers own that final call.
type ExternalIdentityLinkCommitFence func() error

type LinkExternalIdentityInput struct {
	UserID                int64
	Provider              identityregistry.ProviderContribution
	ProviderOperation     string
	ProviderSubjectDigest string
	// 已认证用户绑定（link.complete 或 login.complete continuation）必须等于
	// 目标用户；registration.complete 必须为 0。
	ActorUserID    int64
	IdempotencyKey string
}

type TransitionExternalIdentityLinkInput struct {
	LinkID           int64
	ExpectedRevision int64
	ActorUserID      int64
	IdempotencyKey   string
}

type ExternalIdentityLinkStore interface {
	Link(
		context.Context,
		LinkExternalIdentityInput,
		ExternalIdentityLinkCommitFence,
	) (ExternalIdentityLinkMutation, error)
	Unlink(context.Context, TransitionExternalIdentityLinkInput) (ExternalIdentityLinkMutation, error)
	Erase(context.Context, TransitionExternalIdentityLinkInput) (ExternalIdentityLinkMutation, error)
	Get(context.Context, int64) (ExternalIdentityLink, error)
	FindActive(context.Context, string, string) (ExternalIdentityLink, error)
	ListUser(context.Context, int64) ([]ExternalIdentityLink, error)
}

type ExternalIdentityLinkCommitUnknownError struct {
	CommitError       error
	VerificationError error
}

func (e *ExternalIdentityLinkCommitUnknownError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("identity: external link commit outcome is unknown: %v; verification: %v", e.CommitError, e.VerificationError)
}

func (e *ExternalIdentityLinkCommitUnknownError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.CommitError, e.VerificationError}
}

type preparedExternalIdentityLink struct {
	userID                int64
	provider              identityregistry.ProviderContribution
	providerOperation     string
	providerSubjectDigest string
	actorUserID           int64
	idempotencyKey        string
	fingerprint           string
}

type preparedExternalIdentityTransition struct {
	action           string
	linkID           int64
	expectedRevision int64
	actorUserID      int64
	idempotencyKey   string
	fingerprint      string
}

type externalIdentityArtifactFingerprint struct {
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	PackageDigest    string `json:"packageDigest"`
	VersionID        int64  `json:"versionId"`
}

type externalIdentityLinkFingerprint struct {
	Action                  string                              `json:"action"`
	UserID                  int64                               `json:"userId"`
	ProviderID              string                              `json:"providerId"`
	ProviderContractVersion string                              `json:"providerContractVersion"`
	ProviderOperation       string                              `json:"providerOperation"`
	ProviderArtifact        externalIdentityArtifactFingerprint `json:"providerArtifact"`
	ProviderSubjectDigest   string                              `json:"providerSubjectDigest"`
	ActorUserID             int64                               `json:"actorUserId,omitempty"`
}

type externalIdentityTransitionFingerprint struct {
	Action           string `json:"action"`
	LinkID           int64  `json:"linkId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	ActorUserID      int64  `json:"actorUserId,omitempty"`
}

func prepareExternalIdentityLink(input LinkExternalIdentityInput) (preparedExternalIdentityLink, error) {
	result := preparedExternalIdentityLink{
		userID: input.UserID, provider: cloneExternalIdentityProvider(input.Provider),
		providerOperation:     strings.ToLower(strings.TrimSpace(input.ProviderOperation)),
		providerSubjectDigest: strings.ToLower(strings.TrimSpace(input.ProviderSubjectDigest)),
		actorUserID:           input.ActorUserID,
		idempotencyKey:        input.IdempotencyKey,
	}
	artifact := result.provider.Artifact
	if result.userID <= 0 || result.actorUserID < 0 || result.provider.ID == "" ||
		result.provider.ID != strings.ToLower(strings.TrimSpace(result.provider.ID)) ||
		result.provider.ContractVersion == "" || result.provider.Kind != identityregistry.ProviderKindAuth ||
		artifact.Core || artifact.VersionID <= 0 || artifact.RuntimeInstanceID == "" ||
		artifact.ExtensionID == "" || artifact.ExtensionID != strings.ToLower(strings.TrimSpace(artifact.ExtensionID)) ||
		artifact.ExtensionVersion == "" || artifact.ExtensionVersion != strings.TrimSpace(artifact.ExtensionVersion) ||
		!validExternalIdentityDigest(artifact.PackageDigest) || !validExternalIdentityDigest(result.providerSubjectDigest) ||
		!externalIdentityLinkOperationAllowed(result.provider, result.providerOperation) ||
		!validExternalIdentityIdempotencyKey(result.idempotencyKey) {
		return preparedExternalIdentityLink{}, ErrExternalIdentityLinkInvalid
	}
	if ((result.providerOperation == "link.complete" || result.providerOperation == "login.complete") &&
		(result.actorUserID <= 0 || result.actorUserID != result.userID)) ||
		(result.providerOperation == "registration.complete" && result.actorUserID != 0) {
		return preparedExternalIdentityLink{}, ErrExternalIdentityLinkInvalid
	}
	fingerprint, err := externalIdentityRequestFingerprint(externalIdentityLinkFingerprint{
		Action: ExternalIdentityLinkActionLink, UserID: result.userID,
		ProviderID: result.provider.ID, ProviderContractVersion: result.provider.ContractVersion,
		ProviderOperation: result.providerOperation,
		ProviderArtifact: externalIdentityArtifactFingerprint{
			ExtensionID: artifact.ExtensionID, ExtensionVersion: artifact.ExtensionVersion,
			PackageDigest: artifact.PackageDigest, VersionID: artifact.VersionID,
		},
		ProviderSubjectDigest: result.providerSubjectDigest, ActorUserID: result.actorUserID,
	})
	if err != nil {
		return preparedExternalIdentityLink{}, err
	}
	result.fingerprint = fingerprint
	return result, nil
}

func prepareExternalIdentityTransition(
	action string,
	input TransitionExternalIdentityLinkInput,
) (preparedExternalIdentityTransition, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	result := preparedExternalIdentityTransition{
		action: action, linkID: input.LinkID, expectedRevision: input.ExpectedRevision,
		actorUserID: input.ActorUserID, idempotencyKey: input.IdempotencyKey,
	}
	if (action != ExternalIdentityLinkActionUnlink && action != ExternalIdentityLinkActionErase) ||
		result.linkID <= 0 || result.expectedRevision <= 0 || result.actorUserID < 0 ||
		!validExternalIdentityIdempotencyKey(result.idempotencyKey) {
		return preparedExternalIdentityTransition{}, ErrExternalIdentityLinkInvalid
	}
	fingerprint, err := externalIdentityRequestFingerprint(externalIdentityTransitionFingerprint{
		Action: action, LinkID: result.linkID, ExpectedRevision: result.expectedRevision,
		ActorUserID: result.actorUserID,
	})
	if err != nil {
		return preparedExternalIdentityTransition{}, err
	}
	result.fingerprint = fingerprint
	return result, nil
}

func externalIdentityLinkOperationAllowed(provider identityregistry.ProviderContribution, operation string) bool {
	if operation != "registration.complete" && operation != "link.complete" && operation != "login.complete" {
		return false
	}
	for _, declared := range provider.Operations {
		if declared.Name == operation {
			return true
		}
	}
	return false
}

func validExternalIdentityDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validExternalIdentityIdempotencyKey(value string) bool {
	if value == "" || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for index := range len(value) {
		if value[index] < '!' || value[index] > '~' {
			return false
		}
	}
	return true
}

func externalIdentityRequestFingerprint(input any) (string, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("%w: encode fingerprint: %v", ErrExternalIdentityLinkInvalid, err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func cloneExternalIdentityProvider(input identityregistry.ProviderContribution) identityregistry.ProviderContribution {
	result := input
	result.Operations = append([]identityregistry.ProviderOperation(nil), input.Operations...)
	return result
}

var _ ExternalIdentityLinkStore = (*PostgresExternalIdentityLinkStore)(nil)
