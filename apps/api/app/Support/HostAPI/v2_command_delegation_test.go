package hostapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestProtocolV2ActorDelegationBindsExactInvocation(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	authority := testProtocolV2ActorDelegationAuthority(t, func() time.Time { return now })
	request := testProtocolV2ActorDelegationRequest()
	token, err := authority.IssueActorDelegation(context.Background(), request)
	if err != nil || token == "" {
		t.Fatalf("issue actor delegation = %q, %v", token, err)
	}
	verified, err := authority.verifyActorDelegation(token, request)
	if err != nil || verified.ActorUserID != request.ActorUserID || len(verified.DelegationIDDigest) != 64 ||
		verified.RuntimeEpoch != int64(request.Runtime.GetRuntimeEpoch()) || verified.RuntimeInstanceID != request.Runtime.GetInstanceId() ||
		!verified.IssuedAt.Equal(now) || !verified.NotBefore.Equal(now) || !verified.ExpiresAt.Equal(now.Add(protocolV2ActorDelegationTTL)) {
		t.Fatalf("verified actor delegation = %#v, %v", verified, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*ProtocolV2ActorDelegationRequest)
	}{
		{name: "actor", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.ActorUserID++ }},
		{name: "extension", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.ExtensionId = "other.plugin" }},
		{name: "version", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.ExtensionVersion = "2.0.0" }},
		{name: "artifact", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.ArtifactDigest = strings.Repeat("b", 64) }},
		{name: "trust", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.TrustGrantId = "42" }},
		{name: "epoch", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.RuntimeEpoch++ }},
		{name: "instance", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.InstanceId = "runtime-other" }},
		{name: "command", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.CommandID = "sforum.other" }},
		{name: "command version", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.CommandVersion = "2" }},
		{name: "idempotency", mutate: func(value *ProtocolV2ActorDelegationRequest) { value.IdempotencyKey = "other-key" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			candidate.Runtime = cloneProtocolV2ExtensionIdentity(request.Runtime)
			test.mutate(&candidate)
			if _, err := authority.verifyActorDelegation(token, candidate); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
				t.Fatalf("binding error = %v", err)
			}
		})
	}
}

func TestProtocolV2ActorDelegationRejectsExpiredForgedAndWrongAlgorithm(t *testing.T) {
	now := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	authority := testProtocolV2ActorDelegationAuthority(t, func() time.Time { return now })
	request := testProtocolV2ActorDelegationRequest()
	token, err := authority.IssueActorDelegation(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(protocolV2ActorDelegationTTL + 3*time.Second)
	if _, err := authority.verifyActorDelegation(token, request); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("expired error = %v", err)
	}

	now = now.Add(-protocolV2ActorDelegationTTL - 3*time.Second)
	parts := strings.Split(token, ".")
	parts[2] = strings.Repeat("a", len(parts[2]))
	if _, err := authority.verifyActorDelegation(strings.Join(parts, "."), request); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("forged error = %v", err)
	}
	claims := jwt.MapClaims{"iss": protocolV2ActorDelegationIssuer, "aud": ProtocolV2ActorDelegationAudience}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.verifyActorDelegation(unsigned, request); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("none algorithm error = %v", err)
	}
}

func TestProtocolV2ActorDelegationIssuesUniqueReplayIdentifiersConcurrently(t *testing.T) {
	authority := testProtocolV2ActorDelegationAuthority(t, time.Now)
	request := testProtocolV2ActorDelegationRequest()
	const workers = 64
	digests := make(chan string, workers)
	errorsByWorker := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			token, err := authority.IssueActorDelegation(context.Background(), request)
			if err != nil {
				errorsByWorker <- err
				return
			}
			verified, err := authority.verifyActorDelegation(token, request)
			if err != nil {
				errorsByWorker <- err
				return
			}
			digests <- verified.DelegationIDDigest
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	close(digests)
	for err := range errorsByWorker {
		t.Fatal(err)
	}
	seen := make(map[string]bool, workers)
	for digest := range digests {
		if seen[digest] {
			t.Fatalf("duplicate delegation digest %q", digest)
		}
		seen[digest] = true
	}
	if len(seen) != workers {
		t.Fatalf("delegations = %d, want %d", len(seen), workers)
	}
}

func TestProtocolV2ActorDelegationRejectsInvalidIssuerInputs(t *testing.T) {
	if _, err := newProtocolV2ActorDelegationAuthority(make([]byte, 31), time.Now, time.Minute); !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
		t.Fatalf("short key error = %v", err)
	}
	authority := testProtocolV2ActorDelegationAuthority(t, time.Now)
	request := testProtocolV2ActorDelegationRequest()
	for _, mutate := range []func(*ProtocolV2ActorDelegationRequest){
		func(value *ProtocolV2ActorDelegationRequest) { value.ActorUserID = 0 },
		func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.ArtifactDigest = "invalid" },
		func(value *ProtocolV2ActorDelegationRequest) { value.Runtime.RuntimeEpoch = 0 },
		func(value *ProtocolV2ActorDelegationRequest) { value.CommandID = "" },
		func(value *ProtocolV2ActorDelegationRequest) { value.IdempotencyKey = "has whitespace" },
	} {
		candidate := request
		candidate.Runtime = cloneProtocolV2ExtensionIdentity(request.Runtime)
		mutate(&candidate)
		if token, err := authority.IssueActorDelegation(context.Background(), candidate); token != "" || !errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
			t.Fatalf("invalid issue = %q, %v", token, err)
		}
	}
}

func testProtocolV2ActorDelegationAuthority(t *testing.T, now func() time.Time) *ProtocolV2ActorDelegationAuthority {
	t.Helper()
	authority, err := newProtocolV2ActorDelegationAuthority([]byte("0123456789abcdef0123456789abcdef"), now, protocolV2ActorDelegationTTL)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func testProtocolV2ActorDelegationRequest() ProtocolV2ActorDelegationRequest {
	return ProtocolV2ActorDelegationRequest{
		ActorUserID: 42,
		Runtime: &protocolv2.ExtensionIdentity{
			ExtensionId: "fixture.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: strings.Repeat("a", 64),
			TrustGrantId: "41", RuntimeEpoch: 7, InstanceId: "runtime-fixture-7",
		},
		CommandID: "sforum.user.update", CommandVersion: "1", IdempotencyKey: "request-42",
	}
}
