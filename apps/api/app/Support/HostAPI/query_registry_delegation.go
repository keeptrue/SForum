package hostapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	ProtocolV2QueryDelegationAudience  = "sforum.host-query.v2"
	protocolV2QueryDelegationTTL       = 2 * time.Minute
	protocolV2QueryDelegationMaxTTL    = 5 * time.Minute
	protocolV2QueryDelegationMaxBytes  = 8192
	protocolV2QueryDelegationKeyBytes  = 32
	protocolV2QueryFingerprintMax      = 128
	protocolV2QueryExtensionIDMax      = 121
	protocolV2QueryExtensionVersionMax = 128
	protocolV2QueryTrustGrantIDMax     = 128
	protocolV2QueryInstanceIDMax       = 512
	protocolV2QueryLocaleMax           = 32
	protocolV2QueryScopeMax            = 128
	protocolV2QueryReplayMaximum       = 65_536
)

var (
	ErrProtocolV2QueryDelegationInvalid   = errors.New("hostapi: query actor delegation is invalid")
	ErrProtocolV2QueryDelegationReplayed  = errors.New("hostapi: query actor delegation was already consumed")
	ErrProtocolV2QueryReplayUnavailable   = errors.New("hostapi: query delegation replay ledger is unavailable")
	ErrProtocolV2QueryActorDenied         = errors.New("hostapi: query actor is denied")
	ErrProtocolV2QueryCallerStale         = errors.New("hostapi: query caller runtime is stale")
	ErrProtocolV2QueryRegistryUnavailable = errors.New("hostapi: Query Registry outlet is unavailable")
)

// ProtocolV2QueryActorProjection is derived by Host identity policy. The two
// fingerprints are opaque cache/authority isolation material, never bearer
// credentials and never values supplied by a plugin.
type ProtocolV2QueryActorProjection struct {
	ActorUserID       int64
	Authenticated     bool
	ActorFingerprint  string
	PolicyFingerprint string
}

// ProtocolV2QueryActorAuthority resolves the actor at issuance and performs
// the authoritative live permission check at every Query Registry fence.
type ProtocolV2QueryActorAuthority interface {
	ResolveProtocolV2QueryActor(context.Context, int64) (ProtocolV2QueryActorProjection, error)
	AuthorizeProtocolV2QueryActor(context.Context, int64, queryregistry.PermissionClaim) (ProtocolV2QueryActorProjection, error)
}

// ProtocolV2QueryCallerAdmission rechecks the authenticated broker runtime.
// Implementations must fail while the exact artifact is revoked, quarantined,
// draining, disabled, or no longer the active runtime instance.
type ProtocolV2QueryCallerAdmission interface {
	AuthorizeProtocolV2QueryCaller(context.Context, *protocolv2.ExtensionIdentity) error
}

type protocolV2QueryDelegationBinding struct {
	Actor    ProtocolV2QueryActorProjection
	Runtime  *protocolv2.ExtensionIdentity
	Query    queryregistry.QueryContribution
	Registry queryregistry.CacheState
	Locale   string
	Scope    string
	MaxCost  int
}

type protocolV2QueryDelegationClaims struct {
	ExtensionID      string `json:"extension_id"`
	ExtensionVersion string `json:"extension_version"`
	ArtifactDigest   string `json:"artifact_digest"`
	TrustGrantID     string `json:"trust_grant_id"`
	RuntimeEpoch     uint64 `json:"runtime_epoch"`
	InstanceID       string `json:"instance_id"`

	QueryID          string `json:"query_id"`
	ContractVersion  string `json:"contract_version"`
	PlanVersion      string `json:"plan_version"`
	ResultSchema     string `json:"result_schema"`
	PermissionPolicy string `json:"permission_policy"`
	QueryExtensionID string `json:"query_extension_id"`
	QueryVersion     string `json:"query_extension_version"`
	QueryArtifact    string `json:"query_artifact_digest"`
	QueryVersionID   int64  `json:"query_version_id"`
	QueryInstanceID  string `json:"query_instance_id"`
	QueryCore        bool   `json:"query_core"`
	RegistryRevision uint64 `json:"registry_revision"`
	RegistryDigest   string `json:"registry_digest"`
	Locale           string `json:"locale,omitempty"`
	Scope            string `json:"scope,omitempty"`
	MaxCost          int    `json:"max_cost,omitempty"`
	jwt.RegisteredClaims
}

type protocolV2VerifiedQueryDelegation struct {
	Binding            protocolV2QueryDelegationBinding
	DelegationIDDigest string
	IssuedAt           time.Time
	NotBefore          time.Time
	ExpiresAt          time.Time
}

// ProtocolV2QueryDelegationAuthority owns a boot-scoped signing key and replay
// ledger. Tokens cannot survive a Host restart or move to another Gateway.
type ProtocolV2QueryDelegationAuthority struct {
	key []byte
	now func() time.Time
	ttl time.Duration

	replayMu sync.Mutex
	consumed map[string]time.Time
	pending  map[string]protocolV2VerifiedQueryDelegation
}

func NewProtocolV2QueryDelegationAuthority() (*ProtocolV2QueryDelegationAuthority, error) {
	key := make([]byte, protocolV2QueryDelegationKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("hostapi: generate query delegation signing key: %w", err)
	}
	return newProtocolV2QueryDelegationAuthority(key, time.Now, protocolV2QueryDelegationTTL)
}

func newProtocolV2QueryDelegationAuthority(
	key []byte,
	now func() time.Time,
	ttl time.Duration,
) (*ProtocolV2QueryDelegationAuthority, error) {
	if len(key) < protocolV2QueryDelegationKeyBytes || now == nil || ttl <= 0 || ttl > protocolV2QueryDelegationMaxTTL {
		return nil, ErrProtocolV2QueryDelegationInvalid
	}
	return &ProtocolV2QueryDelegationAuthority{
		key: append([]byte(nil), key...), now: now, ttl: ttl,
		consumed: make(map[string]time.Time), pending: make(map[string]protocolV2VerifiedQueryDelegation),
	}, nil
}

func (a *ProtocolV2QueryDelegationAuthority) issue(
	ctx context.Context,
	binding protocolV2QueryDelegationBinding,
) (string, error) {
	if a == nil || ctx == nil || len(a.key) < protocolV2QueryDelegationKeyBytes {
		return "", ErrProtocolV2QueryDelegationInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	binding, err := normalizeProtocolV2QueryDelegationBinding(binding)
	if err != nil {
		return "", err
	}
	jti, err := newProtocolV2ActorDelegationID()
	if err != nil {
		return "", fmt.Errorf("hostapi: create query delegation id: %w", err)
	}
	now := a.now().UTC().Truncate(time.Second)
	queryArtifact := binding.Query.Artifact
	claims := protocolV2QueryDelegationClaims{
		ExtensionID: binding.Runtime.GetExtensionId(), ExtensionVersion: binding.Runtime.GetExtensionVersion(),
		ArtifactDigest: binding.Runtime.GetArtifactDigest(), TrustGrantID: binding.Runtime.GetTrustGrantId(),
		RuntimeEpoch: binding.Runtime.GetRuntimeEpoch(), InstanceID: binding.Runtime.GetInstanceId(),
		QueryID: binding.Query.ID, ContractVersion: binding.Query.ContractVersion,
		PlanVersion: binding.Query.PlanVersion, ResultSchema: binding.Query.ResultSchema,
		PermissionPolicy: binding.Query.PermissionPolicy,
		QueryExtensionID: queryArtifact.ExtensionID, QueryVersion: queryArtifact.ExtensionVersion,
		QueryArtifact: queryArtifact.PackageDigest, QueryVersionID: queryArtifact.VersionID,
		QueryInstanceID: queryArtifact.RuntimeInstanceID, QueryCore: queryArtifact.Core,
		RegistryRevision: binding.Registry.Revision, RegistryDigest: binding.Registry.Digest,
		Locale: binding.Locale, Scope: binding.Scope, MaxCost: binding.MaxCost,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    protocolV2ActorDelegationIssuer,
			Subject:   "query-delegation",
			Audience:  jwt.ClaimStrings{ProtocolV2QueryDelegationAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)), NotBefore: jwt.NewNumericDate(now),
			IssuedAt: jwt.NewNumericDate(now), ID: jti,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("hostapi: sign query delegation: %w", err)
	}
	if len(signed) > protocolV2QueryDelegationMaxBytes {
		return "", ErrProtocolV2QueryDelegationInvalid
	}
	digest := sha256.Sum256([]byte(jti))
	verified := protocolV2VerifiedQueryDelegation{
		Binding: binding, DelegationIDDigest: hex.EncodeToString(digest[:]),
		IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(a.ttl),
	}
	if err := a.remember(verified); err != nil {
		return "", err
	}
	return signed, nil
}

func (a *ProtocolV2QueryDelegationAuthority) remember(delegation protocolV2VerifiedQueryDelegation) error {
	a.replayMu.Lock()
	defer a.replayMu.Unlock()
	now := a.now().UTC()
	for digest, item := range a.pending {
		if !item.ExpiresAt.After(now) {
			delete(a.pending, digest)
		}
	}
	for digest, expiresAt := range a.consumed {
		if !expiresAt.After(now) {
			delete(a.consumed, digest)
		}
	}
	if len(a.pending) >= protocolV2QueryReplayMaximum {
		return ErrProtocolV2QueryReplayUnavailable
	}
	a.pending[delegation.DelegationIDDigest] = delegation
	return nil
}

func (a *ProtocolV2QueryDelegationAuthority) lookup(token string) (protocolV2VerifiedQueryDelegation, error) {
	claims, err := a.parse(token)
	if err != nil {
		return protocolV2VerifiedQueryDelegation{}, err
	}
	digest := sha256.Sum256([]byte(claims.ID))
	key := hex.EncodeToString(digest[:])
	a.replayMu.Lock()
	delegation, ok := a.pending[key]
	a.replayMu.Unlock()
	if !ok {
		return protocolV2VerifiedQueryDelegation{}, ErrProtocolV2QueryDelegationInvalid
	}
	return delegation, nil
}

func (a *ProtocolV2QueryDelegationAuthority) verify(
	token string,
	expected protocolV2QueryDelegationBinding,
) (protocolV2VerifiedQueryDelegation, error) {
	expected, err := normalizeProtocolV2QueryDelegationBinding(expected)
	if err != nil {
		return protocolV2VerifiedQueryDelegation{}, ErrProtocolV2QueryDelegationInvalid
	}
	stored, err := a.lookup(token)
	if err != nil || !reflect.DeepEqual(stored.Binding, expected) {
		return protocolV2VerifiedQueryDelegation{}, ErrProtocolV2QueryDelegationInvalid
	}
	claims, err := a.parse(token)
	if err != nil || validateProtocolV2QueryDelegationClaims(claims, expected) != nil {
		return protocolV2VerifiedQueryDelegation{}, ErrProtocolV2QueryDelegationInvalid
	}
	digest := sha256.Sum256([]byte(claims.ID))
	return protocolV2VerifiedQueryDelegation{
		Binding: expected, DelegationIDDigest: hex.EncodeToString(digest[:]),
		IssuedAt: claims.IssuedAt.Time.UTC(), NotBefore: claims.NotBefore.Time.UTC(), ExpiresAt: claims.ExpiresAt.Time.UTC(),
	}, nil
}

func (a *ProtocolV2QueryDelegationAuthority) consume(delegation protocolV2VerifiedQueryDelegation) error {
	if a == nil || !protocolV2SHA256Hex(delegation.DelegationIDDigest) {
		return ErrProtocolV2QueryDelegationInvalid
	}
	now := a.now().UTC()
	if now.Before(delegation.NotBefore) || !now.Before(delegation.ExpiresAt) {
		return ErrProtocolV2QueryDelegationInvalid
	}
	a.replayMu.Lock()
	defer a.replayMu.Unlock()
	stored, pending := a.pending[delegation.DelegationIDDigest]
	if !pending || !reflect.DeepEqual(stored.Binding, delegation.Binding) {
		return ErrProtocolV2QueryDelegationInvalid
	}
	if _, exists := a.consumed[delegation.DelegationIDDigest]; exists {
		return ErrProtocolV2QueryDelegationReplayed
	}
	a.consumed[delegation.DelegationIDDigest] = delegation.ExpiresAt
	return nil
}

func (a *ProtocolV2QueryDelegationAuthority) parse(token string) (*protocolV2QueryDelegationClaims, error) {
	if a == nil || len(a.key) < protocolV2QueryDelegationKeyBytes || len(token) == 0 || len(token) > protocolV2QueryDelegationMaxBytes {
		return nil, ErrProtocolV2QueryDelegationInvalid
	}
	claims := &protocolV2QueryDelegationClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(candidate *jwt.Token) (any, error) {
		if candidate.Method != jwt.SigningMethodHS256 {
			return nil, ErrProtocolV2QueryDelegationInvalid
		}
		return a.key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(protocolV2ActorDelegationIssuer),
		jwt.WithAudience(ProtocolV2QueryDelegationAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(2*time.Second),
		jwt.WithTimeFunc(a.now),
		jwt.WithStrictDecoding(),
	)
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, ErrProtocolV2QueryDelegationInvalid
	}
	return claims, nil
}

func normalizeProtocolV2QueryDelegationBinding(
	binding protocolV2QueryDelegationBinding,
) (protocolV2QueryDelegationBinding, error) {
	binding.Actor.ActorFingerprint = strings.TrimSpace(binding.Actor.ActorFingerprint)
	binding.Actor.PolicyFingerprint = strings.TrimSpace(binding.Actor.PolicyFingerprint)
	locale, scope, err := queryregistry.NormalizeExecutionContext(binding.Locale, binding.Scope)
	if err != nil {
		return protocolV2QueryDelegationBinding{}, ErrProtocolV2QueryDelegationInvalid
	}
	binding.Locale = locale
	binding.Scope = scope
	if !validProtocolV2QueryActorProjection(binding.Actor) || !validProtocolV2QueryRuntimeBinding(binding.Runtime) ||
		binding.Query.ID == "" || len(binding.Query.ID) > 200 ||
		binding.Query.ContractVersion == "" || len(binding.Query.ContractVersion) > 200 ||
		binding.Query.PlanVersion == "" || len(binding.Query.PlanVersion) > 200 ||
		binding.Query.ResultSchema == "" || len(binding.Query.ResultSchema) > 256 ||
		binding.Query.PermissionPolicy == "" || len(binding.Query.PermissionPolicy) > 200 ||
		binding.Query.Artifact.ExtensionID == "" || !protocolV2SHA256Hex(binding.Query.Artifact.PackageDigest) ||
		binding.Registry.Revision == 0 || !protocolV2SHA256Hex(binding.Registry.Digest) || binding.Registry.SafeMode ||
		binding.MaxCost < 0 || binding.MaxCost > math.MaxInt32 ||
		len(binding.Locale) > protocolV2QueryLocaleMax || len(binding.Scope) > protocolV2QueryScopeMax {
		return protocolV2QueryDelegationBinding{}, ErrProtocolV2QueryDelegationInvalid
	}
	binding.Runtime = cloneProtocolV2ExtensionIdentity(binding.Runtime)
	return binding, nil
}

func validProtocolV2QueryRuntimeBinding(runtime *protocolv2.ExtensionIdentity) bool {
	if !validProtocolV2QueryIdentity(runtime) || runtime.GetRuntimeEpoch() > math.MaxInt64 {
		return false
	}
	for _, field := range []struct {
		value string
		limit int
	}{
		{runtime.GetExtensionId(), protocolV2QueryExtensionIDMax},
		{runtime.GetExtensionVersion(), protocolV2QueryExtensionVersionMax},
		{runtime.GetTrustGrantId(), protocolV2QueryTrustGrantIDMax},
		{runtime.GetInstanceId(), protocolV2QueryInstanceIDMax},
	} {
		if field.value != strings.TrimSpace(field.value) || len(field.value) > field.limit || containsProtocolV2Control(field.value) {
			return false
		}
	}
	return true
}

func validProtocolV2QueryActorProjection(actor ProtocolV2QueryActorProjection) bool {
	validIdentity := (actor.Authenticated && actor.ActorUserID > 0) || (!actor.Authenticated && actor.ActorUserID == 0)
	return validIdentity &&
		len(actor.ActorFingerprint) > 0 && len(actor.ActorFingerprint) <= protocolV2QueryFingerprintMax &&
		len(actor.PolicyFingerprint) > 0 && len(actor.PolicyFingerprint) <= protocolV2QueryFingerprintMax &&
		!containsProtocolV2Control(actor.ActorFingerprint) && !containsProtocolV2Control(actor.PolicyFingerprint)
}

func normalizeProtocolV2QueryActorProjection(actor ProtocolV2QueryActorProjection) (ProtocolV2QueryActorProjection, error) {
	actor.ActorFingerprint = strings.TrimSpace(actor.ActorFingerprint)
	actor.PolicyFingerprint = strings.TrimSpace(actor.PolicyFingerprint)
	if !validProtocolV2QueryActorProjection(actor) {
		return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
	}
	return actor, nil
}

func containsProtocolV2Control(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func validateProtocolV2QueryDelegationClaims(
	claims *protocolV2QueryDelegationClaims,
	expected protocolV2QueryDelegationBinding,
) error {
	artifact := expected.Query.Artifact
	if claims == nil || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil ||
		claims.Subject != "query-delegation" ||
		claims.ExtensionID != expected.Runtime.GetExtensionId() || claims.ExtensionVersion != expected.Runtime.GetExtensionVersion() ||
		claims.ArtifactDigest != expected.Runtime.GetArtifactDigest() || claims.TrustGrantID != expected.Runtime.GetTrustGrantId() ||
		claims.RuntimeEpoch != expected.Runtime.GetRuntimeEpoch() || claims.InstanceID != expected.Runtime.GetInstanceId() ||
		claims.QueryID != expected.Query.ID || claims.ContractVersion != expected.Query.ContractVersion ||
		claims.PlanVersion != expected.Query.PlanVersion || claims.ResultSchema != expected.Query.ResultSchema ||
		claims.PermissionPolicy != expected.Query.PermissionPolicy ||
		claims.QueryExtensionID != artifact.ExtensionID || claims.QueryVersion != artifact.ExtensionVersion ||
		claims.QueryArtifact != artifact.PackageDigest || claims.QueryVersionID != artifact.VersionID ||
		claims.QueryInstanceID != artifact.RuntimeInstanceID || claims.QueryCore != artifact.Core ||
		claims.RegistryRevision != expected.Registry.Revision || claims.RegistryDigest != expected.Registry.Digest ||
		claims.Locale != expected.Locale || claims.Scope != expected.Scope || claims.MaxCost != expected.MaxCost ||
		!protocolV2ActorDelegationID(claims.ID) || claims.NotBefore.Time.Before(claims.IssuedAt.Time) ||
		!claims.ExpiresAt.Time.After(claims.NotBefore.Time) ||
		claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) > protocolV2QueryDelegationMaxTTL {
		return ErrProtocolV2QueryDelegationInvalid
	}
	return nil
}
