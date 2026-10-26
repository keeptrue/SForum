package hostapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// S9 of the frozen plugin session-state contract (Revision 2 of
// knowledge/decisions/2026-10-16-extension-authoring-contracts-freeze.md).
//
// Why this file exists: the plugin→Host call channel attests the *runtime*, not
// the request (`protocolV2HostBinding` is built per long-lived plugin process),
// so the Host cannot tell from that channel alone which Core session a `Set`
// belongs to. The session scope therefore travels the same way Protocol V2
// already carries this kind of authority: a Host-signed, single-invocation
// delegation.
//
// Three properties define the shape below:
//
//  1. The session fingerprint lives **only** in the Host-side ledger record,
//     never in the token. A plugin that decodes the JWT learns its own runtime
//     binding and nothing about the session — `Actor.session_id` stays
//     unpopulated (S8) and no correlation handle leaks.
//  2. The delegation is retired when the invocation that minted it settles, so
//     a plugin cannot hoard it and write into a session after the request that
//     justified it is over. Reuse therefore fails closed.
//  3. Everything is fail closed: a token that was not minted by this authority
//     (wrong key, unknown jti, retired, expired, tampered) resolves to
//     ErrProtocolV2SessionDelegationInvalid, and the service turns that into a
//     denial rather than an empty success.

const (
	// ProtocolV2SessionDelegationAudience is distinct from the command and query
	// audiences so a token minted for one capability can never be replayed at
	// another service.
	ProtocolV2SessionDelegationAudience   = "sforum.host-session.v2"
	protocolV2SessionDelegationTTL        = 2 * time.Minute
	protocolV2SessionDelegationMaxTTL     = 5 * time.Minute
	protocolV2SessionDelegationMaxBytes   = 4096
	protocolV2SessionDelegationKeyBytes   = 32
	protocolV2SessionDelegationMaxPending = 65_536
)

var (
	ErrProtocolV2SessionDelegationInvalid     = errors.New("hostapi: session delegation is invalid")
	ErrProtocolV2SessionDelegationRetired     = errors.New("hostapi: session delegation was already retired")
	ErrProtocolV2SessionDelegationUnavailable = errors.New("hostapi: session delegation ledger is unavailable")
)

// ProtocolV2SessionScope is the Host-resolved Core session scope of one HTTP
// request. It is Host-only data: it is never placed on the wire and never
// reaches a plugin. Both deadlines are zero when the Host cannot bound that
// side of the session lifetime; the store treats an unknown bound as "cannot
// tighten further", never as "unlimited".
type ProtocolV2SessionScope struct {
	// SessionFingerprint is sha256hex of the session identifier the Host chose
	// for this session (the stable `sid` when authenticated, the cookie session
	// id for an anonymous session). The raw identifier never leaves AuthSession.
	SessionFingerprint string
	// IdleExpiresAt / AbsoluteExpiresAt are the Core session's two lifetime
	// boundaries. Plugin state can never outlive them.
	IdleExpiresAt     time.Time
	AbsoluteExpiresAt time.Time
	// ActorUserID is 0 for an anonymous session.
	ActorUserID int64
}

// ProtocolV2SessionDelegationIssuer is the narrow in-process capability the
// Host-owned route/admin transports use to obtain a session delegation for one
// invocation. It is never registered as a plugin RPC: a plugin cannot mint one.
type ProtocolV2SessionDelegationIssuer interface {
	MintProtocolV2SessionDelegation(context.Context, *protocolv2.ExtensionIdentity, ProtocolV2SessionScope) (string, error)
	RetireProtocolV2SessionDelegation(string) error
}

type protocolV2SessionDelegationClaims struct {
	ExtensionID      string `json:"extension_id"`
	ExtensionVersion string `json:"extension_version"`
	ArtifactDigest   string `json:"artifact_digest"`
	TrustGrantID     string `json:"trust_grant_id"`
	RuntimeEpoch     uint64 `json:"runtime_epoch"`
	InstanceID       string `json:"instance_id"`
	ActorUserID      int64  `json:"actor_user_id"`
	jwt.RegisteredClaims
}

type protocolV2SessionDelegationRecord struct {
	Runtime     *protocolv2.ExtensionIdentity
	ActorUserID int64
	Scope       ProtocolV2SessionScope
	IssuedAt    time.Time
	NotBefore   time.Time
	ExpiresAt   time.Time
}

// ProtocolV2SessionDelegationAuthority owns one boot-scoped signing key and the
// mint ledger. A Host restart intentionally invalidates every outstanding
// delegation: they are short-lived and the plugin contract states that session
// state is ephemeral and a miss must be tolerated (S2).
type ProtocolV2SessionDelegationAuthority struct {
	key []byte
	now func() time.Time
	ttl time.Duration

	mu      sync.Mutex
	pending map[string]protocolV2SessionDelegationRecord
}

func NewProtocolV2SessionDelegationAuthority() (*ProtocolV2SessionDelegationAuthority, error) {
	key := make([]byte, protocolV2SessionDelegationKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("hostapi: generate session delegation signing key: %w", err)
	}
	return newProtocolV2SessionDelegationAuthority(key, time.Now, protocolV2SessionDelegationTTL)
}

func newProtocolV2SessionDelegationAuthority(
	key []byte,
	now func() time.Time,
	ttl time.Duration,
) (*ProtocolV2SessionDelegationAuthority, error) {
	if len(key) < protocolV2SessionDelegationKeyBytes || now == nil || ttl <= 0 || ttl > protocolV2SessionDelegationMaxTTL {
		return nil, ErrProtocolV2SessionDelegationInvalid
	}
	return &ProtocolV2SessionDelegationAuthority{
		key: append([]byte(nil), key...), now: now, ttl: ttl,
		pending: make(map[string]protocolV2SessionDelegationRecord),
	}, nil
}

// MintProtocolV2SessionDelegation signs one delegation and records the scope it
// stands for. Callers must retire the returned token when the invocation that
// carries it settles; `defer` in the transport is the intended shape.
func (a *ProtocolV2SessionDelegationAuthority) MintProtocolV2SessionDelegation(
	ctx context.Context,
	runtime *protocolv2.ExtensionIdentity,
	scope ProtocolV2SessionScope,
) (string, error) {
	if a == nil || ctx == nil || len(a.key) < protocolV2SessionDelegationKeyBytes {
		return "", ErrProtocolV2SessionDelegationInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runtime, scope, err := normalizeProtocolV2SessionDelegation(runtime, scope)
	if err != nil {
		return "", err
	}
	jti, err := newProtocolV2ActorDelegationID()
	if err != nil {
		return "", fmt.Errorf("hostapi: create session delegation id: %w", err)
	}
	now := a.now().UTC().Truncate(time.Second)
	claims := protocolV2SessionDelegationClaims{
		ExtensionID: runtime.GetExtensionId(), ExtensionVersion: runtime.GetExtensionVersion(),
		ArtifactDigest: runtime.GetArtifactDigest(), TrustGrantID: runtime.GetTrustGrantId(),
		RuntimeEpoch: runtime.GetRuntimeEpoch(), InstanceID: runtime.GetInstanceId(),
		ActorUserID: scope.ActorUserID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: protocolV2ActorDelegationIssuer, Subject: "session-delegation",
			Audience:  jwt.ClaimStrings{ProtocolV2SessionDelegationAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)), NotBefore: jwt.NewNumericDate(now),
			IssuedAt: jwt.NewNumericDate(now), ID: jti,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("hostapi: sign session delegation: %w", err)
	}
	if len(signed) > protocolV2SessionDelegationMaxBytes {
		return "", ErrProtocolV2SessionDelegationInvalid
	}
	digest := sha256.Sum256([]byte(jti))
	record := protocolV2SessionDelegationRecord{
		Runtime: runtime, ActorUserID: scope.ActorUserID, Scope: scope,
		IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(a.ttl),
	}
	if err := a.remember(hex.EncodeToString(digest[:]), record); err != nil {
		return "", err
	}
	return signed, nil
}

// RetireProtocolV2SessionDelegation drops the mint record. It is idempotent: an
// unknown or already-retired token is not an error, because every transport
// defers it and a failed invocation must not turn into a second failure.
func (a *ProtocolV2SessionDelegationAuthority) RetireProtocolV2SessionDelegation(token string) error {
	if a == nil || len(token) == 0 || len(token) > protocolV2SessionDelegationMaxBytes {
		return nil
	}
	claims, err := a.parse(token)
	if err != nil {
		// An unverifiable token cannot name one of our records.
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pending, protocolV2SessionDelegationDigest(claims.ID))
	return nil
}

// Resolve validates one echoed delegation against the broker-attested runtime
// and returns the Host's own record of the scope it stands for. It does not
// remove the record: one invocation may legitimately perform several
// get/set/delete operations, and the transport retires the delegation when the
// invocation settles.
func (a *ProtocolV2SessionDelegationAuthority) Resolve(
	token string,
	runtime *protocolv2.ExtensionIdentity,
) (ProtocolV2SessionScope, error) {
	if a == nil || len(a.key) < protocolV2SessionDelegationKeyBytes {
		return ProtocolV2SessionScope{}, ErrProtocolV2SessionDelegationInvalid
	}
	if !validProtocolV2SessionRuntimeBinding(runtime) {
		return ProtocolV2SessionScope{}, ErrProtocolV2SessionDelegationInvalid
	}
	claims, err := a.parse(token)
	if err != nil {
		return ProtocolV2SessionScope{}, err
	}
	if err := validateProtocolV2SessionDelegationClaims(claims, runtime); err != nil {
		return ProtocolV2SessionScope{}, err
	}
	a.mu.Lock()
	record, ok := a.pending[protocolV2SessionDelegationDigest(claims.ID)]
	a.mu.Unlock()
	if !ok {
		// Never minted here, already retired, or evicted. Retired and unknown are
		// reported distinctly so a caller can tell reuse from forgery in tests and
		// traces; both deny.
		return ProtocolV2SessionScope{}, ErrProtocolV2SessionDelegationRetired
	}
	if record.ActorUserID != claims.ActorUserID || record.Scope.SessionFingerprint == "" ||
		!protocolV2ExtensionIdentityEqual(record.Runtime, runtime) {
		return ProtocolV2SessionScope{}, ErrProtocolV2SessionDelegationInvalid
	}
	now := a.now().UTC()
	if now.Before(record.NotBefore) || !now.Before(record.ExpiresAt) {
		return ProtocolV2SessionScope{}, ErrProtocolV2SessionDelegationInvalid
	}
	return record.Scope, nil
}

func (a *ProtocolV2SessionDelegationAuthority) remember(
	digest string,
	record protocolV2SessionDelegationRecord,
) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now().UTC()
	for key, item := range a.pending {
		if !item.ExpiresAt.After(now) {
			delete(a.pending, key)
		}
	}
	if len(a.pending) >= protocolV2SessionDelegationMaxPending {
		return ErrProtocolV2SessionDelegationUnavailable
	}
	a.pending[digest] = record
	return nil
}

func (a *ProtocolV2SessionDelegationAuthority) parse(token string) (*protocolV2SessionDelegationClaims, error) {
	if a == nil || len(a.key) < protocolV2SessionDelegationKeyBytes ||
		len(token) == 0 || len(token) > protocolV2SessionDelegationMaxBytes {
		return nil, ErrProtocolV2SessionDelegationInvalid
	}
	claims := &protocolV2SessionDelegationClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(candidate *jwt.Token) (any, error) {
		if candidate.Method != jwt.SigningMethodHS256 {
			return nil, ErrProtocolV2SessionDelegationInvalid
		}
		return a.key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(protocolV2ActorDelegationIssuer),
		jwt.WithAudience(ProtocolV2SessionDelegationAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(2*time.Second),
		jwt.WithTimeFunc(a.now),
		jwt.WithStrictDecoding(),
	)
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, ErrProtocolV2SessionDelegationInvalid
	}
	return claims, nil
}

func protocolV2SessionDelegationDigest(id string) string {
	digest := sha256.Sum256([]byte(id))
	return hex.EncodeToString(digest[:])
}

func normalizeProtocolV2SessionDelegation(
	runtime *protocolv2.ExtensionIdentity,
	scope ProtocolV2SessionScope,
) (*protocolv2.ExtensionIdentity, ProtocolV2SessionScope, error) {
	if !validProtocolV2SessionRuntimeBinding(runtime) || !validProtocolV2SessionScope(scope) {
		return nil, ProtocolV2SessionScope{}, ErrProtocolV2SessionDelegationInvalid
	}
	return cloneProtocolV2ExtensionIdentity(runtime), scope, nil
}

// validProtocolV2SessionRuntimeBinding reuses the Query Registry's exact-runtime
// rules: a delegation must name the version, artifact digest, trust grant and
// runtime epoch of the live instance, not just its id.
func validProtocolV2SessionRuntimeBinding(runtime *protocolv2.ExtensionIdentity) bool {
	if !validProtocolV2QueryIdentity(runtime) {
		return false
	}
	for _, field := range []string{
		runtime.GetExtensionId(), runtime.GetExtensionVersion(),
		runtime.GetTrustGrantId(), runtime.GetInstanceId(),
	} {
		if field != strings.TrimSpace(field) || containsProtocolV2Control(field) {
			return false
		}
	}
	return true
}

func validProtocolV2SessionScope(scope ProtocolV2SessionScope) bool {
	return protocolV2SHA256Hex(scope.SessionFingerprint) && scope.ActorUserID >= 0
}

func validateProtocolV2SessionDelegationClaims(
	claims *protocolV2SessionDelegationClaims,
	runtime *protocolv2.ExtensionIdentity,
) error {
	if claims == nil || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil ||
		claims.Subject != "session-delegation" || claims.ActorUserID < 0 ||
		claims.ExtensionID != runtime.GetExtensionId() || claims.ExtensionVersion != runtime.GetExtensionVersion() ||
		claims.ArtifactDigest != runtime.GetArtifactDigest() || claims.TrustGrantID != runtime.GetTrustGrantId() ||
		claims.RuntimeEpoch != runtime.GetRuntimeEpoch() || claims.InstanceID != runtime.GetInstanceId() ||
		!protocolV2ActorDelegationID(claims.ID) || claims.NotBefore.Time.Before(claims.IssuedAt.Time) ||
		!claims.ExpiresAt.Time.After(claims.NotBefore.Time) ||
		claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) > protocolV2SessionDelegationMaxTTL {
		return ErrProtocolV2SessionDelegationInvalid
	}
	return nil
}

func protocolV2ExtensionIdentityEqual(left, right *protocolv2.ExtensionIdentity) bool {
	if left == nil || right == nil {
		return false
	}
	return left.GetExtensionId() == right.GetExtensionId() &&
		left.GetExtensionVersion() == right.GetExtensionVersion() &&
		left.GetArtifactDigest() == right.GetArtifactDigest() &&
		left.GetTrustGrantId() == right.GetTrustGrantId() &&
		left.GetRuntimeEpoch() == right.GetRuntimeEpoch() &&
		left.GetInstanceId() == right.GetInstanceId()
}

var _ ProtocolV2SessionDelegationIssuer = (*ProtocolV2SessionDelegationAuthority)(nil)
