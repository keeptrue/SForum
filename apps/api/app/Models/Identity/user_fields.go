package identity

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

const (
	IdentityUserFieldValueStateActive = "active"
	IdentityUserFieldValueStateErased = "erased"

	IdentityUserFieldValueActionSet   = "set"
	IdentityUserFieldValueActionErase = "erase"

	maximumIdentityUserFieldValueBytes = 1 << 20
	maximumIdentityUserFieldScopeBytes = 256

	identityUserFieldValueDigestDomain = "sforum.identity.user-field.value-digest@1"
	identityUserFieldReceiptKeyDomain  = "sforum.identity.user-field.receipt-key@1"
)

var (
	ErrIdentityUserFieldValueInvalid             = errors.New("identity: user-field value input is invalid")
	ErrIdentityUserFieldValueNotFound            = errors.New("identity: user-field value was not found")
	ErrIdentityUserFieldValueStateConflict       = errors.New("identity: user-field value state changed")
	ErrIdentityUserFieldValueIdempotencyConflict = errors.New("identity: user-field value idempotency fingerprint changed")
	ErrIdentityUserFieldDeclarationStale         = errors.New("identity: user-field declaration is stale")
	ErrIdentityUserFieldPermissionDenied         = errors.New("identity: user-field permission denied")
	ErrIdentityUserFieldSchemaUnavailable        = errors.New("identity: user-field schema is unavailable")
	ErrIdentityUserFieldSchemaInvalid            = errors.New("identity: user-field schema rejected the value")
	ErrIdentityUserFieldValueStoreUnavailable    = errors.New("identity: user-field value store is unavailable")
	ErrIdentityUserFieldDigestKeyInvalid         = errors.New("identity: user-field digest key is invalid")
	ErrIdentityUserFieldTransactionRetry         = errors.New("identity: user-field transaction must be retried")
	ErrIdentityUserFieldTransactionIsolation     = errors.New("identity: user-field transaction must be serializable")
)

// IdentityUserFieldValue deliberately excludes the raw JSON value. Mutation,
// audit, event, and replay results may expose only metadata and digests.
type IdentityUserFieldValue struct {
	UserID               int64      `json:"userId"`
	FieldID              string     `json:"fieldId"`
	OwnerExtensionID     string     `json:"ownerExtensionId"`
	FieldContractVersion string     `json:"fieldContractVersion"`
	FieldSchemaDigest    string     `json:"fieldSchemaDigest"`
	DeclarationRevision  int64      `json:"declarationRevision"`
	State                string     `json:"state"`
	Revision             int64      `json:"revision"`
	ValueDigest          string     `json:"valueDigest,omitempty"`
	UpdatedByUserID      int64      `json:"updatedByUserId,omitempty"`
	UpdatedAuditEventID  int64      `json:"updatedAuditEventId"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	ErasedAt             *time.Time `json:"erasedAt,omitempty"`
	ErasedByUserID       int64      `json:"erasedByUserId,omitempty"`
	EraseAuditEventID    int64      `json:"eraseAuditEventId,omitempty"`
}

// IdentityUserFieldValueRead is returned only after a separate live read-
// permission check and exact Schema validation.
type IdentityUserFieldValueRead struct {
	IdentityUserFieldValue
	Value json.RawMessage `json:"value"`
}

type IdentityUserFieldValueEvent struct {
	ID                   int64     `json:"id"`
	UserID               int64     `json:"userId"`
	FieldID              string    `json:"fieldId"`
	OwnerExtensionID     string    `json:"ownerExtensionId"`
	FieldContractVersion string    `json:"fieldContractVersion"`
	FieldSchemaDigest    string    `json:"fieldSchemaDigest"`
	DeclarationRevision  int64     `json:"declarationRevision"`
	Action               string    `json:"action"`
	PreviousRevision     int64     `json:"previousRevision,omitempty"`
	NextRevision         int64     `json:"nextRevision"`
	PreviousValueDigest  string    `json:"previousValueDigest,omitempty"`
	NextValueDigest      string    `json:"nextValueDigest,omitempty"`
	IdempotencyKey       string    `json:"idempotencyKey"`
	RequestFingerprint   string    `json:"requestFingerprint"`
	ActorUserID          int64     `json:"actorUserId,omitempty"`
	AuditEventID         int64     `json:"auditEventId"`
	CreatedAt            time.Time `json:"createdAt"`
}

type IdentityUserFieldValueMutation struct {
	Value            IdentityUserFieldValue      `json:"value"`
	Event            IdentityUserFieldValueEvent `json:"event"`
	Replayed         bool                        `json:"replayed"`
	CurrentAvailable bool                        `json:"currentAvailable"`
}

type SetIdentityUserFieldValueInput struct {
	ActorUserID      int64
	UserID           int64
	FieldID          string
	Value            json.RawMessage
	ExpectedRevision int64
	// Host derives a stable command/authority scope. Actor and artifact are
	// fingerprinted separately so an exact retry can survive process replacement.
	IdempotencyScope string
	IdempotencyKey   string
}

type EraseIdentityUserFieldValueInput struct {
	ActorUserID      int64
	UserID           int64
	FieldID          string
	ExpectedRevision int64
	// Privacy workflows use a Host-owned scope distinct from extension commands.
	IdempotencyScope string
	IdempotencyKey   string
}

type ReadIdentityUserFieldValueInput struct {
	ActorUserID int64
	UserID      int64
	FieldID     string
}

// IdentityUserFieldCommitFence is returned by Tx mutations. Caller-owned
// transactions must be serializable and invoke it exactly once immediately
// before COMMIT.
type IdentityUserFieldCommitFence func() error

type IdentityUserFieldValueStore interface {
	Set(context.Context, SetIdentityUserFieldValueInput) (IdentityUserFieldValueMutation, error)
	Erase(context.Context, EraseIdentityUserFieldValueInput) (IdentityUserFieldValueMutation, error)
	Get(context.Context, ReadIdentityUserFieldValueInput) (IdentityUserFieldValueRead, error)
}

// IdentityUserFieldPrivacyStore is intentionally separate from the ordinary
// value dependency because privacy erasure is Host-owned and bypasses live
// extension declaration authority after retained data becomes inert.
type IdentityUserFieldPrivacyStore interface {
	EraseForPrivacy(context.Context, EraseIdentityUserFieldValueInput) (IdentityUserFieldValueMutation, error)
}

type IdentityUserFieldCommitUnknownError struct {
	CommitError       error
	VerificationError error
}

func (e *IdentityUserFieldCommitUnknownError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"identity: user-field value commit outcome is unknown: %v; verification: %v",
		e.CommitError, e.VerificationError,
	)
}

func (e *IdentityUserFieldCommitUnknownError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.CommitError, e.VerificationError}
}

type preparedIdentityUserFieldSet struct {
	actorUserID      int64
	userID           int64
	fieldID          string
	value            any
	canonicalValue   json.RawMessage
	valueDigest      string
	expectedRevision int64
	idempotencyKey   string
}

type preparedIdentityUserFieldErase struct {
	actorUserID      int64
	userID           int64
	fieldID          string
	expectedRevision int64
	idempotencyKey   string
}

type identityUserFieldMutationFingerprint struct {
	Action           string `json:"action"`
	Mode             string `json:"mode"`
	ActorUserID      int64  `json:"actorUserId,omitempty"`
	UserID           int64  `json:"userId"`
	FieldID          string `json:"fieldId"`
	ExpectedRevision int64  `json:"expectedRevision"`
	ValueDigest      string `json:"valueDigest,omitempty"`
}

func prepareIdentityUserFieldSet(input SetIdentityUserFieldValueInput) (preparedIdentityUserFieldSet, error) {
	prepared := preparedIdentityUserFieldSet{
		actorUserID: input.ActorUserID, userID: input.UserID,
		fieldID:          strings.ToLower(strings.TrimSpace(input.FieldID)),
		expectedRevision: input.ExpectedRevision,
	}
	if prepared.actorUserID <= 0 || prepared.userID <= 0 || !validIdentityUserFieldID(prepared.fieldID) ||
		prepared.expectedRevision < 0 || !validIdentityUserFieldIdempotencyScope(input.IdempotencyScope) ||
		!validIdentityUserFieldIdempotencyKey(input.IdempotencyKey) ||
		len(input.Value) == 0 || len(input.Value) > maximumIdentityUserFieldValueBytes {
		return preparedIdentityUserFieldSet{}, ErrIdentityUserFieldValueInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Value))
	decoder.UseNumber()
	if err := decoder.Decode(&prepared.value); err != nil {
		return preparedIdentityUserFieldSet{}, ErrIdentityUserFieldValueInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return preparedIdentityUserFieldSet{}, ErrIdentityUserFieldValueInvalid
	}
	canonical, err := json.Marshal(prepared.value)
	if err != nil || len(canonical) == 0 || len(canonical) > maximumIdentityUserFieldValueBytes {
		return preparedIdentityUserFieldSet{}, ErrIdentityUserFieldValueInvalid
	}
	prepared.canonicalValue = canonical
	prepared.idempotencyKey = identityUserFieldReceiptKey(input.IdempotencyScope, input.IdempotencyKey)
	return prepared, nil
}

func prepareIdentityUserFieldErase(input EraseIdentityUserFieldValueInput) (preparedIdentityUserFieldErase, error) {
	prepared := preparedIdentityUserFieldErase{
		actorUserID: input.ActorUserID, userID: input.UserID,
		fieldID:          strings.ToLower(strings.TrimSpace(input.FieldID)),
		expectedRevision: input.ExpectedRevision,
	}
	if prepared.actorUserID < 0 || prepared.userID <= 0 || !validIdentityUserFieldID(prepared.fieldID) ||
		prepared.expectedRevision <= 0 || !validIdentityUserFieldIdempotencyScope(input.IdempotencyScope) ||
		!validIdentityUserFieldIdempotencyKey(input.IdempotencyKey) {
		return preparedIdentityUserFieldErase{}, ErrIdentityUserFieldValueInvalid
	}
	prepared.idempotencyKey = identityUserFieldReceiptKey(input.IdempotencyScope, input.IdempotencyKey)
	return prepared, nil
}

func identityUserFieldSetFingerprint(input preparedIdentityUserFieldSet) (string, error) {
	return identityUserFieldRequestFingerprint(identityUserFieldMutationFingerprint{
		Action: IdentityUserFieldValueActionSet, Mode: "write",
		ActorUserID: input.actorUserID, UserID: input.userID, FieldID: input.fieldID,
		ExpectedRevision: input.expectedRevision, ValueDigest: input.valueDigest,
	})
}

func identityUserFieldEraseFingerprint(input preparedIdentityUserFieldErase, mode string) (string, error) {
	return identityUserFieldRequestFingerprint(identityUserFieldMutationFingerprint{
		Action: IdentityUserFieldValueActionErase, Mode: mode,
		ActorUserID: input.actorUserID, UserID: input.userID, FieldID: input.fieldID,
		ExpectedRevision: input.expectedRevision,
	})
}

func identityUserFieldRequestFingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", ErrIdentityUserFieldValueInvalid
	}
	return identityUserFieldSHA256(encoded), nil
}

func identityUserFieldSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func identityUserFieldHMAC(key []byte, userID int64, fieldID string, value []byte) string {
	material := make([]byte, 0, len(identityUserFieldValueDigestDomain)+1+8+4+len(fieldID)+4+len(value))
	material = append(material, identityUserFieldValueDigestDomain...)
	material = append(material, 0)
	material = binary.BigEndian.AppendUint64(material, uint64(userID))
	material = binary.BigEndian.AppendUint32(material, uint32(len(fieldID)))
	material = append(material, fieldID...)
	material = binary.BigEndian.AppendUint32(material, uint32(len(value)))
	material = append(material, value...)
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write(material)
	return hex.EncodeToString(digest.Sum(nil))
}

func identityUserFieldReceiptKey(scope string, key string) string {
	material := make([]byte, 0, len(identityUserFieldReceiptKeyDomain)+1+4+len(scope)+4+len(key))
	material = append(material, identityUserFieldReceiptKeyDomain...)
	material = append(material, 0)
	material = binary.BigEndian.AppendUint32(material, uint32(len(scope)))
	material = append(material, scope...)
	material = binary.BigEndian.AppendUint32(material, uint32(len(key)))
	material = append(material, key...)
	return identityUserFieldSHA256(material)
}

func validIdentityUserFieldDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validIdentityUserFieldIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}

func validIdentityUserFieldIdempotencyScope(value string) bool {
	if len(value) == 0 || len(value) > maximumIdentityUserFieldScopeBytes {
		return false
	}
	for _, character := range []byte(value) {
		if character < '!' || character > '~' {
			return false
		}
	}
	return true
}

func validIdentityUserFieldID(value string) bool {
	if len(value) < 2 || len(value) > 121 {
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

func identityUserFieldMatches(
	left identityregistry.UserFieldContribution,
	right identityregistry.UserFieldContribution,
) bool {
	return left.Artifact == right.Artifact && left.UserField == right.UserField
}

func newIdentityUserFieldCommitFence(
	registry *identityregistry.Registry,
	revision uint64,
	field identityregistry.UserFieldContribution,
) IdentityUserFieldCommitFence {
	var consumed atomic.Bool
	return func() error {
		if registry == nil || !consumed.CompareAndSwap(false, true) || registry.Revision() != revision {
			return ErrIdentityUserFieldDeclarationStale
		}
		current, err := registry.ResolveUserField(field.ID)
		if err != nil || registry.Revision() != revision || !identityUserFieldMatches(current, field) {
			return ErrIdentityUserFieldDeclarationStale
		}
		return nil
	}
}

func mapIdentityUserFieldSchemaError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, identityregistry.ErrSchemaUnavailable):
		return ErrIdentityUserFieldSchemaUnavailable
	case errors.Is(err, identityregistry.ErrSchemaValueInvalid):
		return ErrIdentityUserFieldSchemaInvalid
	default:
		return ErrIdentityUserFieldSchemaUnavailable
	}
}
