package extensionopenapi

import (
	"encoding/json"
	"errors"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	ErrInvalidArtifact  = errors.New("extension openapi: invalid exact artifact")
	ErrInvalidDocument  = errors.New("extension openapi: invalid OpenAPI document")
	ErrUnsafeReference  = errors.New("extension openapi: unsafe package reference")
	ErrCollision        = errors.New("extension openapi: aggregate collision")
	ErrContractMismatch = errors.New("extension openapi: route contract mismatch")
	ErrResourceBudget   = errors.New("extension openapi: static resource budget exceeded")
)

const (
	SecurityPublic               = "public"
	SecurityAuthenticated        = "authenticated"
	SecurityHostInherited        = "host_inherited"
	SecurityPluginOwned          = "plugin_owned"
	PolicyDisabled               = "disabled"
	PolicyRateLimitIPWrite       = "host.ip_write@1"
	PolicyIdempotencyRequired24h = "required.24h@1"
	// PolicyCORSSameOrigin is the Host default CORS profile for plugin routes.
	PolicyCORSSameOrigin = "host.cors.same_origin@1"
	// DefaultRequestSizeBytes is the Host default max request body (1 MiB).
	DefaultRequestSizeBytes int64 = 1 << 20
	// UploadRequestSizeBytes is the Host max body for multipart/upload routes.
	UploadRequestSizeBytes       int64 = 32 << 20
	StreamContractOpaqueBytesV1        = "sforum.route.opaque_bytes@1"
	PayloadValidationPluginOwned       = "plugin_owned"
)

// Artifact is an immutable package snapshot. Host route policies are derived
// from validated route and OpenAPI declarations instead of caller-supplied prose.
type Artifact struct {
	Root          string
	ExtensionID   string
	Version       string
	PackageDigest string
	Manifest      extensionmanifest.Manifest
	// Policies is retained for source compatibility while Host policy derivation
	// moves into the aggregate. Production callers must not author these values.
	Policies []RoutePolicy
}

type RoutePolicy struct {
	RouteID     string
	Method      string
	RateLimit   string
	Idempotency string
	Security    string
	// RequestSizeBytes is the Host-enforced max request body (0 = platform default).
	RequestSizeBytes int64
	// CORSPolicy is a Host-named CORS profile (e.g. host.cors.same_origin@1).
	CORSPolicy string
}

// CoreOperation reserves a Host path/method and operation id during aggregation.
// A plugin may occupy it only through an explicit replace declaration targeting
// the same stable route id.
type CoreOperation struct {
	RouteID     string
	Path        string
	Method      string
	OperationID string
}

type BuildInput struct {
	Core      []CoreOperation
	Artifacts []Artifact
	// Loader optionally reuses artifacts already loaded and digest-verified by a
	// previous build over the same immutable set. Nil disables reuse and keeps
	// the previous one-verification-per-build behavior.
	Loader *ArtifactLoader
}

type SourceIdentity struct {
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	PackageDigest    string `json:"packageDigest"`
	FragmentID       string `json:"fragmentId"`
	ContractVersion  string `json:"contractVersion"`
	Path             string `json:"path"`
	Digest           string `json:"digest"`
	Namespace        string `json:"namespace"`
}

type GeneratedOperation struct {
	OperationID             string `json:"operationId"`
	RouteID                 string `json:"routeId"`
	ContractVersion         string `json:"contractVersion"`
	Path                    string `json:"path"`
	Method                  string `json:"method"`
	Action                  string `json:"action"`
	Mode                    string `json:"mode"`
	Guard                   string `json:"guard"`
	Permission              string `json:"permission,omitempty"`
	RequestSchema           string `json:"requestSchema,omitempty"`
	ResponseSchema          string `json:"responseSchema,omitempty"`
	StreamContract          string `json:"streamContract,omitempty"`
	PayloadValidation       string `json:"payloadValidation,omitempty"`
	RateLimit               string `json:"rateLimit"`
	Idempotency             string `json:"idempotency"`
	IdempotencyRequired     bool   `json:"idempotencyRequired"`
	IdempotencyHeader       string `json:"idempotencyHeader,omitempty"`
	IdempotencyKeyMaxLength int    `json:"idempotencyKeyMaxLength,omitempty"`
	IdempotencyTTLSeconds   int    `json:"idempotencyTtlSeconds,omitempty"`
	RateLimitScope          string `json:"rateLimitScope,omitempty"`
	// RequestSizeBytes is Host-enforced max request body for this operation.
	RequestSizeBytes int64 `json:"requestSizeBytes,omitempty"`
	// CORSPolicy is the Host CORS profile name.
	CORSPolicy       string `json:"corsPolicy,omitempty"`
	Security         string `json:"security"`
	ExtensionID      string `json:"extensionId"`
	ExtensionVersion string `json:"extensionVersion"`
	PackageDigest    string `json:"packageDigest"`
	FragmentID       string `json:"fragmentId"`
	Namespace        string `json:"namespace"`
}

// Snapshot exposes copies only. Callers cannot mutate the canonical aggregate
// or the metadata used to derive its revision.
type Snapshot struct {
	revision   string
	document   []byte
	sources    []SourceIdentity
	operations []GeneratedOperation
}

func (s Snapshot) Revision() string { return s.revision }

func (s Snapshot) Document() json.RawMessage {
	return append(json.RawMessage(nil), s.document...)
}

func (s Snapshot) Sources() []SourceIdentity {
	return append([]SourceIdentity(nil), s.sources...)
}

func (s Snapshot) GeneratedClientOperations() []GeneratedOperation {
	return append([]GeneratedOperation(nil), s.operations...)
}
