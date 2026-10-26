package hostapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	ProtocolV2ActorDelegationAudience = "sforum.host-command.v2"
	protocolV2ActorDelegationIssuer   = "sforum.host"
	protocolV2ActorDelegationTTL      = 2 * time.Minute
	protocolV2ActorDelegationMaxTTL   = 5 * time.Minute
	protocolV2ActorDelegationMaxBytes = 4096
	protocolV2ActorDelegationKeyBytes = 32
)

var ErrProtocolV2ActorDelegationInvalid = errors.New("hostapi: actor delegation is invalid")

// ProtocolV2ActorDelegationRequest is created only from a Host-authenticated
// route or admin invocation. Issuance does not replace the transaction-time
// actor and permission check performed when the command executes.
type ProtocolV2ActorDelegationRequest struct {
	ActorUserID    int64
	Runtime        *protocolv2.ExtensionIdentity
	CommandID      string
	CommandVersion string
	IdempotencyKey string
}

// ProtocolV2ActorDelegationIssuer is the narrow in-process capability exposed
// to Host-owned route/admin adapters. It is never registered as a plugin RPC.
type ProtocolV2ActorDelegationIssuer interface {
	IssueActorDelegation(context.Context, ProtocolV2ActorDelegationRequest) (string, error)
}

// ProtocolV2ActorDelegationAuthority owns one boot-scoped signing key. A Host
// restart intentionally invalidates outstanding short-lived delegations.
type ProtocolV2ActorDelegationAuthority struct {
	key []byte
	now func() time.Time
	ttl time.Duration
}

type protocolV2ActorDelegationClaims struct {
	ActorUserID      int64  `json:"actor_user_id"`
	ExtensionID      string `json:"extension_id"`
	ExtensionVersion string `json:"extension_version"`
	ArtifactDigest   string `json:"artifact_digest"`
	TrustGrantID     string `json:"trust_grant_id"`
	RuntimeEpoch     uint64 `json:"runtime_epoch"`
	InstanceID       string `json:"instance_id"`
	CommandID        string `json:"command_id"`
	CommandVersion   string `json:"command_version"`
	IdempotencyKey   string `json:"idempotency_key"`
	jwt.RegisteredClaims
}

type protocolV2VerifiedActorDelegation struct {
	ActorUserID        int64
	DelegationIDDigest string
	RuntimeEpoch       int64
	RuntimeInstanceID  string
	IssuedAt           time.Time
	NotBefore          time.Time
	ExpiresAt          time.Time
}

func NewProtocolV2ActorDelegationAuthority() (*ProtocolV2ActorDelegationAuthority, error) {
	key := make([]byte, protocolV2ActorDelegationKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("hostapi: generate actor delegation signing key: %w", err)
	}
	return newProtocolV2ActorDelegationAuthority(key, time.Now, protocolV2ActorDelegationTTL)
}

func newProtocolV2ActorDelegationAuthority(
	key []byte,
	now func() time.Time,
	ttl time.Duration,
) (*ProtocolV2ActorDelegationAuthority, error) {
	if len(key) < protocolV2ActorDelegationKeyBytes || now == nil || ttl <= 0 || ttl > protocolV2ActorDelegationMaxTTL {
		return nil, ErrProtocolV2ActorDelegationInvalid
	}
	return &ProtocolV2ActorDelegationAuthority{key: append([]byte(nil), key...), now: now, ttl: ttl}, nil
}

func (a *ProtocolV2ActorDelegationAuthority) IssueActorDelegation(
	ctx context.Context,
	request ProtocolV2ActorDelegationRequest,
) (string, error) {
	if a == nil || ctx == nil || len(a.key) < protocolV2ActorDelegationKeyBytes {
		return "", ErrProtocolV2ActorDelegationInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request, err := normalizeProtocolV2ActorDelegationRequest(request)
	if err != nil {
		return "", err
	}
	jti, err := newProtocolV2ActorDelegationID()
	if err != nil {
		return "", fmt.Errorf("hostapi: create actor delegation id: %w", err)
	}
	now := a.now().UTC().Truncate(time.Second)
	claims := protocolV2ActorDelegationClaims{
		ActorUserID: request.ActorUserID,
		ExtensionID: request.Runtime.GetExtensionId(), ExtensionVersion: request.Runtime.GetExtensionVersion(),
		ArtifactDigest: request.Runtime.GetArtifactDigest(), TrustGrantID: request.Runtime.GetTrustGrantId(),
		RuntimeEpoch: request.Runtime.GetRuntimeEpoch(), InstanceID: request.Runtime.GetInstanceId(),
		CommandID: request.CommandID, CommandVersion: request.CommandVersion, IdempotencyKey: request.IdempotencyKey,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: protocolV2ActorDelegationIssuer, Subject: strconv.FormatInt(request.ActorUserID, 10),
			Audience:  jwt.ClaimStrings{ProtocolV2ActorDelegationAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)), NotBefore: jwt.NewNumericDate(now),
			IssuedAt: jwt.NewNumericDate(now), ID: jti,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("hostapi: sign actor delegation: %w", err)
	}
	if len(signed) > protocolV2ActorDelegationMaxBytes {
		return "", ErrProtocolV2ActorDelegationInvalid
	}
	return signed, nil
}

func (a *ProtocolV2ActorDelegationAuthority) verifyActorDelegation(
	token string,
	expected ProtocolV2ActorDelegationRequest,
) (protocolV2VerifiedActorDelegation, error) {
	expected, err := normalizeProtocolV2ActorDelegationRequest(expected)
	if err != nil {
		return protocolV2VerifiedActorDelegation{}, ErrProtocolV2ActorDelegationInvalid
	}
	claims, err := a.parseActorDelegation(token)
	if err != nil || validateProtocolV2ActorDelegationClaims(claims, expected) != nil {
		return protocolV2VerifiedActorDelegation{}, ErrProtocolV2ActorDelegationInvalid
	}
	return verifiedProtocolV2ActorDelegation(claims), nil
}

func (a *ProtocolV2ActorDelegationAuthority) verifyActorDelegationForCommand(
	token string,
	runtime *protocolv2.ExtensionIdentity,
	commandID string,
	commandVersion string,
	idempotencyKey string,
) (protocolV2VerifiedActorDelegation, error) {
	claims, err := a.parseActorDelegation(token)
	if err != nil {
		return protocolV2VerifiedActorDelegation{}, ErrProtocolV2ActorDelegationInvalid
	}
	expected, err := normalizeProtocolV2ActorDelegationRequest(ProtocolV2ActorDelegationRequest{
		ActorUserID: claims.ActorUserID, Runtime: runtime, CommandID: commandID,
		CommandVersion: commandVersion, IdempotencyKey: idempotencyKey,
	})
	if err != nil || validateProtocolV2ActorDelegationClaims(claims, expected) != nil {
		return protocolV2VerifiedActorDelegation{}, ErrProtocolV2ActorDelegationInvalid
	}
	return verifiedProtocolV2ActorDelegation(claims), nil
}

func (a *ProtocolV2ActorDelegationAuthority) parseActorDelegation(token string) (*protocolV2ActorDelegationClaims, error) {
	if a == nil || len(a.key) < protocolV2ActorDelegationKeyBytes || len(token) == 0 || len(token) > protocolV2ActorDelegationMaxBytes {
		return nil, ErrProtocolV2ActorDelegationInvalid
	}
	claims := &protocolV2ActorDelegationClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(candidate *jwt.Token) (any, error) {
		if candidate.Method != jwt.SigningMethodHS256 {
			return nil, ErrProtocolV2ActorDelegationInvalid
		}
		return a.key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(protocolV2ActorDelegationIssuer),
		jwt.WithAudience(ProtocolV2ActorDelegationAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(2*time.Second),
		jwt.WithTimeFunc(a.now),
		jwt.WithStrictDecoding(),
	)
	if err != nil || parsed == nil || !parsed.Valid {
		return nil, ErrProtocolV2ActorDelegationInvalid
	}
	return claims, nil
}

func verifiedProtocolV2ActorDelegation(claims *protocolV2ActorDelegationClaims) protocolV2VerifiedActorDelegation {
	digest := sha256.Sum256([]byte(claims.ID))
	return protocolV2VerifiedActorDelegation{
		ActorUserID: claims.ActorUserID, DelegationIDDigest: hex.EncodeToString(digest[:]),
		RuntimeEpoch: int64(claims.RuntimeEpoch), RuntimeInstanceID: claims.InstanceID,
		IssuedAt: claims.IssuedAt.Time.UTC(), NotBefore: claims.NotBefore.Time.UTC(), ExpiresAt: claims.ExpiresAt.Time.UTC(),
	}
}

func normalizeProtocolV2ActorDelegationRequest(request ProtocolV2ActorDelegationRequest) (ProtocolV2ActorDelegationRequest, error) {
	request.CommandID = strings.TrimSpace(request.CommandID)
	request.CommandVersion = strings.TrimSpace(request.CommandVersion)
	if !validProtocolV2ActorDelegationBinding(request.ActorUserID, request.Runtime, request.IdempotencyKey) ||
		request.CommandID == "" || len(request.CommandID) > 200 || request.CommandVersion == "" || len(request.CommandVersion) > 64 {
		return ProtocolV2ActorDelegationRequest{}, ErrProtocolV2ActorDelegationInvalid
	}
	request.Runtime = cloneProtocolV2ExtensionIdentity(request.Runtime)
	return request, nil
}

func validProtocolV2ActorDelegationBinding(actorUserID int64, runtime *protocolv2.ExtensionIdentity, idempotencyKey string) bool {
	return actorUserID > 0 && runtime != nil &&
		strings.TrimSpace(runtime.GetExtensionId()) != "" && strings.TrimSpace(runtime.GetExtensionVersion()) != "" &&
		protocolV2SHA256Hex(runtime.GetArtifactDigest()) && strings.TrimSpace(runtime.GetTrustGrantId()) != "" &&
		runtime.GetRuntimeEpoch() > 0 && runtime.GetRuntimeEpoch() <= math.MaxInt64 &&
		strings.TrimSpace(runtime.GetInstanceId()) != "" && len(runtime.GetInstanceId()) <= 512 &&
		validProtocolV2CommandIdempotencyKey(idempotencyKey)
}

func validateProtocolV2ActorDelegationClaims(
	claims *protocolV2ActorDelegationClaims,
	expected ProtocolV2ActorDelegationRequest,
) error {
	if claims == nil || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil ||
		claims.ActorUserID != expected.ActorUserID || claims.Subject != strconv.FormatInt(expected.ActorUserID, 10) ||
		claims.ExtensionID != expected.Runtime.GetExtensionId() || claims.ExtensionVersion != expected.Runtime.GetExtensionVersion() ||
		claims.ArtifactDigest != expected.Runtime.GetArtifactDigest() || claims.TrustGrantID != expected.Runtime.GetTrustGrantId() ||
		claims.RuntimeEpoch != expected.Runtime.GetRuntimeEpoch() || claims.InstanceID != expected.Runtime.GetInstanceId() ||
		claims.CommandID != expected.CommandID || claims.CommandVersion != expected.CommandVersion ||
		claims.IdempotencyKey != expected.IdempotencyKey || !protocolV2ActorDelegationID(claims.ID) ||
		claims.NotBefore.Time.Before(claims.IssuedAt.Time) || !claims.ExpiresAt.Time.After(claims.NotBefore.Time) ||
		claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) > protocolV2ActorDelegationMaxTTL {
		return ErrProtocolV2ActorDelegationInvalid
	}
	return nil
}

func newProtocolV2ActorDelegationID() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func protocolV2ActorDelegationID(value string) bool {
	return len(value) == 64 && protocolV2SHA256Hex(value)
}

func protocolV2SHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func cloneProtocolV2ExtensionIdentity(value *protocolv2.ExtensionIdentity) *protocolv2.ExtensionIdentity {
	if value == nil {
		return nil
	}
	return &protocolv2.ExtensionIdentity{
		ExtensionId: value.GetExtensionId(), ExtensionVersion: value.GetExtensionVersion(),
		ArtifactDigest: value.GetArtifactDigest(), TrustGrantId: value.GetTrustGrantId(),
		RuntimeEpoch: value.GetRuntimeEpoch(), InstanceId: value.GetInstanceId(),
	}
}

var _ ProtocolV2ActorDelegationIssuer = (*ProtocolV2ActorDelegationAuthority)(nil)
