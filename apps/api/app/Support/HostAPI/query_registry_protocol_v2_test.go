package hostapi

import (
	"context"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestProtocolV2QueryRegistryOutletAllowedAndOffsetBound(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	request := h.issueRequest(t)
	response := h.service.execute(h.runtimeContext(context.Background()), request)
	if response.GetError() != nil || len(response.GetRows()) != 1 || !response.GetPage().GetHasMore() ||
		response.GetNextOffset() != 1 || response.GetPage().GetNextCursor() != "" {
		t.Fatalf("response = %#v", response)
	}
	row := response.GetRows()[0]
	if row.GetSchemaId() != h.schemaID || row.GetSchemaVersion() != h.schemaVersion ||
		row.GetValue().AsMap()["id"] != "1" || h.providerCalls.Load() != 1 {
		t.Fatalf("row = %#v provider calls = %d", row, h.providerCalls.Load())
	}
	if h.actors.authorizeCalls.Load() < 3 {
		t.Fatalf("live permission checks = %d, want plan/provider/release fences", h.actors.authorizeCalls.Load())
	}
}

func TestProtocolV2QueryDelegationDerivesActorAndUniqueReplayIDs(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	first, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), h.issueInput())
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), h.issueInput())
	if err != nil {
		t.Fatal(err)
	}
	firstClaims, err := h.service.delegations.parse(first.Token)
	if err != nil {
		t.Fatal(err)
	}
	secondClaims, err := h.service.delegations.parse(second.Token)
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || firstClaims.ID == secondClaims.ID ||
		firstClaims.QueryID != h.query.ID || firstClaims.ContractVersion != h.query.ContractVersion ||
		firstClaims.PlanVersion != h.query.PlanVersion || firstClaims.QueryArtifact != h.query.Artifact.PackageDigest ||
		firstClaims.ExtensionID != h.runtime.GetExtensionId() || firstClaims.InstanceID != h.runtime.GetInstanceId() ||
		firstClaims.Issuer != protocolV2ActorDelegationIssuer || len(firstClaims.Audience) != 1 ||
		firstClaims.Audience[0] != ProtocolV2QueryDelegationAudience {
		t.Fatalf("first claims = %#v second claims = %#v", firstClaims, secondClaims)
	}
	if strings.Contains(first.Token, h.actors.projection.ActorFingerprint) ||
		strings.Contains(first.Token, h.actors.projection.PolicyFingerprint) {
		t.Fatal("query delegation token exposed actor authority material")
	}
	stored, err := h.service.delegations.lookup(first.Token)
	if err != nil || stored.Binding.Actor != h.actors.projection {
		t.Fatalf("server-side actor binding = %#v, %v", stored.Binding.Actor, err)
	}

	const workers = 32
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			grant, issueErr := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), h.issueInput())
			if issueErr != nil {
				errs <- issueErr
				return
			}
			claims, parseErr := h.service.delegations.parse(grant.Token)
			if parseErr != nil {
				errs <- parseErr
				return
			}
			ids <- claims.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for issueErr := range errs {
		t.Fatal(issueErr)
	}
	seen := map[string]struct{}{firstClaims.ID: {}, secondClaims.ID: {}}
	for id := range ids {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate query delegation jti %q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != workers+2 {
		t.Fatalf("unique delegation ids = %d, want %d", len(seen), workers+2)
	}
}

func TestProtocolV2QueryRegistryOutletRejectsDeniedAndRevokedActor(t *testing.T) {
	t.Run("denied at plan", func(t *testing.T) {
		h := newProtocolV2QueryRegistryHarness(t, nil)
		request := h.issueRequest(t)
		h.actors.authorize = func(int64, queryregistry.PermissionClaim, int64) (ProtocolV2QueryActorProjection, error) {
			return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
		}
		response := h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError().GetReason() != "host.query_actor_permission_denied" || h.providerCalls.Load() != 0 {
			t.Fatalf("response = %#v provider calls = %d", response, h.providerCalls.Load())
		}
		// A request rejected before the first successful permission fence has not
		// spent its token. Restoring Host policy may still admit the same request.
		h.actors.authorize = nil
		response = h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError() != nil || h.providerCalls.Load() != 1 {
			t.Fatalf("restored response = %#v provider calls = %d", response, h.providerCalls.Load())
		}
	})

	t.Run("revoked immediately before provider", func(t *testing.T) {
		h := newProtocolV2QueryRegistryHarness(t, nil)
		h.actors.authorize = func(_ int64, _ queryregistry.PermissionClaim, call int64) (ProtocolV2QueryActorProjection, error) {
			if call >= 2 {
				return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
			}
			return h.actors.projection, nil
		}
		request := h.issueRequest(t)
		response := h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError().GetReason() != "host.query_actor_permission_denied" || h.providerCalls.Load() != 0 ||
			h.actors.authorizeCalls.Load() != 2 {
			t.Fatalf("response = %#v provider=%d checks=%d", response, h.providerCalls.Load(), h.actors.authorizeCalls.Load())
		}
		h.actors.authorize = nil
		replayed := h.service.execute(h.runtimeContext(context.Background()), request)
		if replayed.GetError().GetReason() != "host.query_actor_delegation_replayed" || h.providerCalls.Load() != 0 {
			t.Fatalf("revoked actor replay = %#v provider=%d", replayed, h.providerCalls.Load())
		}
	})

	for _, field := range []string{"actor", "policy"} {
		t.Run(field+" fingerprint changed", func(t *testing.T) {
			h := newProtocolV2QueryRegistryHarness(t, nil)
			h.actors.authorize = func(_ int64, _ queryregistry.PermissionClaim, _ int64) (ProtocolV2QueryActorProjection, error) {
				projection := h.actors.projection
				if field == "actor" {
					projection.ActorFingerprint = "actor:42:session-v2"
				} else {
					projection.PolicyFingerprint = "policy:roles-v2"
				}
				return projection, nil
			}
			response := h.service.execute(h.runtimeContext(context.Background()), h.issueRequest(t))
			if response.GetError().GetReason() != "host.query_actor_permission_denied" || h.providerCalls.Load() != 0 {
				t.Fatalf("response = %#v provider calls = %d", response, h.providerCalls.Load())
			}
		})
	}
}

func TestProtocolV2QueryRegistryOutletEnforcesDelegatedCostMaximum(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	input := h.issueInput()
	input.MaxCost = 1
	grant, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	request := h.requestFromGrant(grant)
	response := h.service.execute(h.runtimeContext(context.Background()), request)
	if response.GetError().GetReason() != "host.query_cost_exceeded" || h.providerCalls.Load() != 0 {
		t.Fatalf("response = %#v provider calls = %d", response, h.providerCalls.Load())
	}
}

func TestProtocolV2QueryRegistryOutletConsumesOnlyAfterStaticPlanning(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	request := h.issueRequest(t)
	request.Fields = []string{"id", "id"}
	invalid := h.service.execute(h.runtimeContext(context.Background()), request)
	if invalid.GetError().GetReason() != "host.query_request_invalid" || h.providerCalls.Load() != 0 ||
		h.actors.authorizeCalls.Load() != 0 {
		t.Fatalf("invalid response = %#v provider=%d actor checks=%d", invalid, h.providerCalls.Load(), h.actors.authorizeCalls.Load())
	}
	request.Fields = nil
	allowed := h.service.execute(h.runtimeContext(context.Background()), request)
	if allowed.GetError() != nil || h.providerCalls.Load() != 1 {
		t.Fatalf("corrected response = %#v provider=%d", allowed, h.providerCalls.Load())
	}
}

func TestProtocolV2QueryDelegationRejectsInvalidExecutionContext(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	for _, mutate := range []func(*ProtocolV2QueryActorDelegationRequest){
		func(request *ProtocolV2QueryActorDelegationRequest) { request.Locale = "not a locale" },
		func(request *ProtocolV2QueryActorDelegationRequest) { request.Scope = "admin/users" },
	} {
		request := h.issueInput()
		mutate(&request)
		if grant, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), request); grant.Token != "" ||
			!errors.Is(err, ErrProtocolV2QueryDelegationInvalid) {
			t.Fatalf("invalid execution context grant=%#v err=%v", grant, err)
		}
	}
	request := h.issueInput()
	request.Locale = " zh-CN "
	request.Scope = " Admin.Users "
	grant, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), request)
	if err != nil || grant.Scope != "admin.users" || grant.Token == "" {
		t.Fatalf("normalized execution context grant=%#v err=%v", grant, err)
	}
}

func TestProtocolV2QueryDelegationRejectsNonCanonicalRuntimeBinding(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	for _, mutate := range []func(*protocolv2.ExtensionIdentity){
		func(identity *protocolv2.ExtensionIdentity) { identity.ExtensionId = " caller.plugin" },
		func(identity *protocolv2.ExtensionIdentity) {
			identity.InstanceId = strings.Repeat("x", protocolV2QueryInstanceIDMax+1)
		},
		func(identity *protocolv2.ExtensionIdentity) { identity.RuntimeEpoch = math.MaxUint64 },
	} {
		request := h.issueInput()
		request.Runtime = proto.Clone(h.runtime).(*protocolv2.ExtensionIdentity)
		mutate(request.Runtime)
		if grant, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), request); grant.Token != "" ||
			!errors.Is(err, ErrProtocolV2QueryDelegationInvalid) {
			t.Fatalf("non-canonical runtime grant=%#v err=%v", grant, err)
		}
	}
}

func TestProtocolV2QueryRegistryOutletRejectsForgedExpiredAndWrongBindings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocolV2QueryRegistryHarness, *hostv2.QueryRequest, *context.Context)
		reason string
	}{
		{name: "forged actor", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.Context.Actor = &protocolv2.Actor{UserId: 99}
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "smuggled command delegation", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.Context.HostCommandDelegations = []*protocolv2.HostCommandDelegation{{Token: "smuggled"}}
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "smuggled query delegation", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.Context.HostQueryDelegations = []*protocolv2.HostQueryDelegation{{Token: request.ActorDelegation}}
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "forged signature", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			parts := strings.Split(request.ActorDelegation, ".")
			parts[2] = strings.Repeat("a", len(parts[2]))
			request.ActorDelegation = strings.Join(parts, ".")
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong runtime", mutate: func(h *protocolV2QueryRegistryHarness, _ *hostv2.QueryRequest, ctx *context.Context) {
			other := proto.Clone(h.runtime).(*protocolv2.ExtensionIdentity)
			other.InstanceId = "other-instance"
			*ctx = ContextWithProtocolV2RuntimeIdentity(context.Background(), other)
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong caller artifact", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.Context.Extension.ArtifactDigest = strings.Repeat("f", 64)
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong attested artifact", mutate: func(h *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, ctx *context.Context) {
			other := proto.Clone(h.runtime).(*protocolv2.ExtensionIdentity)
			other.ArtifactDigest = strings.Repeat("f", 64)
			request.Context.Extension = proto.Clone(other).(*protocolv2.ExtensionIdentity)
			*ctx = ContextWithProtocolV2RuntimeIdentity(context.Background(), other)
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong query", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.QueryId = "core.outlet.other"
		}, reason: "host.query_request_invalid"},
		{name: "wrong contract", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.ContractVersion = "core.outlet.users@2"
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong plan", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.PlanVersion = "core.outlet.users.plan@2"
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong result schema", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.ResultSchemaVersion = "2"
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong locale", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.Context.Locale = "en-US"
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "wrong scope", mutate: func(_ *protocolV2QueryRegistryHarness, request *hostv2.QueryRequest, _ *context.Context) {
			request.Scope = "admin.other"
		}, reason: "host.query_actor_delegation_invalid"},
		{name: "expired", mutate: func(h *protocolV2QueryRegistryHarness, _ *hostv2.QueryRequest, _ *context.Context) {
			h.now = h.now.Add(protocolV2QueryDelegationTTL)
		}, reason: "host.query_actor_delegation_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newProtocolV2QueryRegistryHarness(t, nil)
			request := h.issueRequest(t)
			ctx := h.runtimeContext(context.Background())
			test.mutate(h, request, &ctx)
			response := h.service.execute(ctx, request)
			if response.GetError().GetReason() != test.reason || h.providerCalls.Load() != 0 {
				t.Fatalf("response = %#v provider calls = %d", response, h.providerCalls.Load())
			}
		})
	}
}

func TestProtocolV2QueryRegistryOutletRejectsRegistryAndQueryArtifactDrift(t *testing.T) {
	t.Run("registry revision", func(t *testing.T) {
		h := newProtocolV2QueryRegistryHarness(t, nil)
		request := h.issueRequest(t)
		otherArtifact, err := queryregistry.NewCoreArtifact("core.other", "1.0.0", strings.Repeat("c", 64))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.registry.Publish(queryregistry.Publication{Artifact: otherArtifact}); err != nil {
			t.Fatal(err)
		}
		response := h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError().GetReason() != "host.query_actor_delegation_invalid" || h.providerCalls.Load() != 0 {
			t.Fatalf("registry drift response = %#v", response)
		}
	})

	t.Run("query artifact", func(t *testing.T) {
		h := newProtocolV2QueryRegistryHarness(t, nil)
		request := h.issueRequest(t)
		replacement, err := queryregistry.NewCoreArtifact("core.outlet", "1.0.1", strings.Repeat("e", 64))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.registry.PublishIfArtifact(h.query.Artifact, queryregistry.Publication{
			Artifact: replacement, Queries: []queryregistry.QueryDeclaration{h.query.QueryDeclaration},
		}); err != nil {
			t.Fatal(err)
		}
		response := h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError().GetReason() != "host.query_actor_delegation_invalid" || h.providerCalls.Load() != 0 {
			t.Fatalf("query artifact drift response = %#v", response)
		}
	})

	t.Run("registry changes inside permission fence", func(t *testing.T) {
		h := newProtocolV2QueryRegistryHarness(t, nil)
		request := h.issueRequest(t)
		h.actors.authorize = func(_ int64, _ queryregistry.PermissionClaim, _ int64) (ProtocolV2QueryActorProjection, error) {
			otherArtifact, artifactErr := queryregistry.NewCoreArtifact("core.concurrent", "1.0.0", strings.Repeat("9", 64))
			if artifactErr != nil {
				return ProtocolV2QueryActorProjection{}, artifactErr
			}
			if _, publishErr := h.registry.Publish(queryregistry.Publication{Artifact: otherArtifact}); publishErr != nil {
				return ProtocolV2QueryActorProjection{}, publishErr
			}
			return h.actors.projection, nil
		}
		response := h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError().GetReason() != "host.query_runtime_stale" || h.providerCalls.Load() != 0 {
			t.Fatalf("concurrent registry drift response = %#v provider=%d", response, h.providerCalls.Load())
		}
	})
}

func TestProtocolV2QueryRegistryOutletConsumesDelegationOnceConcurrently(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	request := h.issueRequest(t)
	const workers = 24
	responses := make(chan *hostv2.QueryResponse, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- h.service.execute(h.runtimeContext(context.Background()), proto.Clone(request).(*hostv2.QueryRequest))
		}()
	}
	wait.Wait()
	close(responses)
	allowed, replayed := 0, 0
	for response := range responses {
		switch response.GetError().GetReason() {
		case "":
			allowed++
		case "host.query_actor_delegation_replayed":
			replayed++
		default:
			t.Fatalf("unexpected response = %#v", response)
		}
	}
	if allowed != 1 || replayed != workers-1 || h.providerCalls.Load() != 1 {
		t.Fatalf("allowed=%d replayed=%d provider=%d", allowed, replayed, h.providerCalls.Load())
	}
}

func TestProtocolV2QueryRegistryOutletCancellationAndCallerQuarantine(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		h := newProtocolV2QueryRegistryHarness(t, func(ctx context.Context, _ queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
			close(started)
			<-ctx.Done()
			return queryregistry.ProviderExecutionResult{}, ctx.Err()
		})
		ctx, cancel := context.WithCancel(h.runtimeContext(context.Background()))
		request := h.issueRequest(t)
		done := make(chan *hostv2.QueryResponse, 1)
		go func() { done <- h.service.execute(ctx, request) }()
		<-started
		cancel()
		response := <-done
		if response.GetError().GetReason() != "host.query_cancelled" {
			t.Fatalf("response = %#v", response)
		}
		replayed := h.service.execute(h.runtimeContext(context.Background()), request)
		if replayed.GetError().GetReason() != "host.query_actor_delegation_replayed" {
			t.Fatalf("cancelled execution replay = %#v", replayed)
		}
	})

	t.Run("caller quarantined before provider", func(t *testing.T) {
		h := newProtocolV2QueryRegistryHarness(t, nil)
		request := h.issueRequest(t)
		// Issue checks twice; Execute checks once; Plan checks caller/actor/caller;
		// fail the second caller check at the provider fence.
		h.callerAdmission.failAt.Store(7)
		response := h.service.execute(h.runtimeContext(context.Background()), request)
		if response.GetError().GetReason() != "host.query_runtime_stale" || h.providerCalls.Load() != 0 ||
			h.actors.authorizeCalls.Load() != 2 {
			t.Fatalf("response = %#v provider=%d admissions=%d actor checks=%d", response, h.providerCalls.Load(), h.callerAdmission.calls.Load(), h.actors.authorizeCalls.Load())
		}
		h.callerAdmission.failAt.Store(0)
		replayed := h.service.execute(h.runtimeContext(context.Background()), request)
		if replayed.GetError().GetReason() != "host.query_actor_delegation_replayed" || h.providerCalls.Load() != 0 {
			t.Fatalf("quarantined caller replay = %#v provider=%d", replayed, h.providerCalls.Load())
		}
	})
}

func TestProtocolV2QueryRegistryErrorPrefersExactRuntimeStaleOverCancellationShape(t *testing.T) {
	detail := queryRegistryProtocolV2Error(errors.Join(queryregistry.ErrArtifactUnavailable, context.Canceled))
	if detail.GetReason() != "host.query_runtime_stale" || detail.GetRetryable() {
		t.Fatalf("ForceDrain error detail=%#v", detail)
	}
}

func TestProtocolV2QueryRegistryErrorPreservesCustomCancellationClass(t *testing.T) {
	cause := errors.New("caller closed the query view")
	detail := queryRegistryProtocolV2Error(errors.Join(context.Canceled, cause))
	if detail.GetReason() != "host.query_cancelled" || !detail.GetRetryable() {
		t.Fatalf("custom cancellation detail=%#v", detail)
	}
}

func TestProtocolV2QueryRegistryOutletStreamsUnderSameDelegation(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, func(context.Context, queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
		return queryregistry.ProviderExecutionResult{Rows: []queryregistry.QueryRow{
			{"id": "1", "name": "alice"}, {"id": "2", "name": "bob"},
		}}, nil
	})
	server := &protocolV2QueryServer{core: &protocolV2Core{queryRegistry: h.service}}
	request := h.issueRequest(t)
	stream := &protocolV2QueryRegistryTestStream{ctx: h.runtimeContext(context.Background())}
	if err := server.Stream(request, stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.rows) != 2 || stream.rows[0].GetSequence() != 1 || stream.rows[0].GetValue().GetSchemaId() != h.schemaID ||
		stream.rows[0].GetFinal() || !stream.rows[1].GetFinal() || stream.rows[1].GetSequence() != 2 ||
		stream.rows[1].GetValue() != nil || !stream.rows[1].GetPage().GetHasMore() || stream.rows[1].GetNextOffset() != 1 {
		t.Fatalf("stream rows = %#v", stream.rows)
	}
	replayed := &protocolV2QueryRegistryTestStream{ctx: h.runtimeContext(context.Background())}
	if err := server.Stream(request, replayed); err != nil {
		t.Fatal(err)
	}
	if len(replayed.rows) != 1 || !replayed.rows[0].GetFinal() || replayed.rows[0].GetSequence() != 1 ||
		replayed.rows[0].GetError().GetReason() != "host.query_actor_delegation_replayed" {
		t.Fatalf("replayed stream rows = %#v", replayed.rows)
	}
}

func TestGatewayBindsProtocolV2QueryRegistryOutletOnceAndFreezesIt(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	gateway := NewGateway(nil)
	if err := gateway.BindProtocolV2QueryRegistryService(&ProtocolV2QueryRegistryService{
		registry: h.registry, execution: h.service.execution,
	}); err == nil {
		t.Fatal("incomplete Query Registry service must not bind")
	}
	if err := gateway.BindProtocolV2QueryRegistryService(h.service); err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindProtocolV2QueryRegistryService(h.service); err == nil {
		t.Fatal("second Query Registry bind must fail")
	}
	grant, err := gateway.IssueProtocolV2QueryActorDelegation(context.Background(), h.issueInput())
	if err != nil || grant.Token == "" {
		t.Fatalf("grant = %#v, %v", grant, err)
	}
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if !gateway.protocolV2QueryRegistryFrozen || gateway.queryRegistry != h.service {
		t.Fatalf("gateway Query Registry state = %#v", gateway)
	}
	if err := gateway.BindProtocolV2QueryRegistryService(h.service); err == nil {
		t.Fatal("Query Registry bind after broker registration must fail")
	}
}

func TestProtocolV2QueryRegistryOutletRejectsReservedStableIDsAtConstruction(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	artifact := queryregistry.Artifact{
		ExtensionID: "sforum.core", ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("d", 64),
		VersionID: 9, RuntimeInstanceID: "reserved-runtime",
	}
	declaration := h.query.QueryDeclaration
	declaration.ID = QueryPublicTopicsList
	registry := queryregistry.New(queryregistry.WithCostPolicy(NewQueryRegistryCoreCostPolicy()))
	if _, err := registry.Publish(queryregistry.Publication{Artifact: artifact, Queries: []queryregistry.QueryDeclaration{declaration}}); err != nil {
		t.Fatal(err)
	}
	query, err := registry.Resolve(declaration.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := queryregistry.NewStaticProviderResolver([]queryregistry.ExecutableProviderBinding{{
		QueryID: query.ID, ContractVersion: query.ContractVersion, PlanVersion: query.PlanVersion,
		ResultSchema: query.ResultSchema, Artifact: query.Artifact,
		Provider: queryregistry.ExecutableProviderFunc(func(context.Context, queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
			return queryregistry.ProviderExecutionResult{}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: resolver,
		Schemas: queryregistry.ResultSchemaValidatorFunc(func(context.Context, queryregistry.ResultSchemaClaim, queryregistry.QueryRow) error { return nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newProtocolV2QueryRegistryService(registry, execution, h.actors, h.callerAdmission, h.service.delegations); err == nil {
		t.Fatal("reserved stable Host Query id must make the outlet unbindable")
	}
}

func TestProtocolV2QueryServerKeepsStableQueryCompatibilityAheadOfRegistry(t *testing.T) {
	h := newProtocolV2QueryRegistryHarness(t, nil)
	resolver := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		return []map[string]any{{"id": int64(1), "title": "stable"}}, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, resolver)
	server := &protocolV2QueryServer{core: &protocolV2Core{queries: engine, queryRegistry: h.service}}
	request := testProtocolV2TopicsQuery(t)
	request.Fields = []string{"id", "title"}
	response, err := server.Execute(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()), request,
	)
	if err != nil || response.GetError() != nil || len(response.GetRows()) != 1 ||
		response.GetRows()[0].GetValue().AsMap()["title"] != "stable" || h.providerCalls.Load() != 0 {
		t.Fatalf("response = %#v err=%v registry provider=%d", response, err, h.providerCalls.Load())
	}
	stableStream := &protocolV2QueryRegistryTestStream{
		ctx: ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()),
	}
	if err := server.Stream(proto.Clone(request).(*hostv2.QueryRequest), stableStream); err != nil {
		t.Fatal(err)
	}
	if len(stableStream.rows) != 1 || stableStream.rows[0].GetFinal() || stableStream.rows[0].GetPage() != nil {
		t.Fatalf("stable compatibility stream = %#v", stableStream.rows)
	}
	request.Offset = 1
	response, err = server.Execute(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()), request,
	)
	if err != nil || response.GetError().GetReason() != "host.query_page_invalid" || h.providerCalls.Load() != 0 {
		t.Fatalf("legacy raw offset response = %#v err=%v", response, err)
	}
	request.Offset = 0
	request.ContractVersion = "core.outlet.users@1"
	response, err = server.Execute(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity()), request,
	)
	if err != nil || response.GetError().GetReason() != "host.query_shape_unsupported" || h.providerCalls.Load() != 0 {
		t.Fatalf("legacy registry-field response = %#v err=%v", response, err)
	}
	unknown := &hostv2.QueryRequest{Context: request.Context, QueryId: "sforum.unknown", PlanVersion: "1"}
	response, err = server.Execute(context.Background(), unknown)
	if err != nil || response.GetError().GetReason() != "host.query_unsupported" {
		t.Fatalf("legacy unknown response = %#v err=%v", response, err)
	}
	registryRequest := &hostv2.QueryRequest{
		Context: request.Context, QueryId: "core.outlet.users", ContractVersion: "core.outlet.users@1",
		PlanVersion: "core.outlet.users.plan@1", ActorDelegation: "host-signed-token",
	}
	response, err = (&protocolV2QueryServer{core: &protocolV2Core{}}).Execute(context.Background(), registryRequest)
	if err != nil || response.GetError().GetReason() != "host.query_registry_unavailable" {
		t.Fatalf("unbound Query Registry response = %#v err=%v", response, err)
	}
}

type protocolV2QueryRegistryHarness struct {
	service         *ProtocolV2QueryRegistryService
	registry        *queryregistry.Registry
	actors          *protocolV2QueryTestActors
	callerAdmission *protocolV2QueryTestCallerAdmission
	runtime         *protocolv2.ExtensionIdentity
	query           queryregistry.QueryContribution
	schemaID        string
	schemaVersion   string
	now             time.Time
	providerCalls   atomic.Int64
}

func newProtocolV2QueryRegistryHarness(
	t *testing.T,
	provider func(context.Context, queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error),
) *protocolV2QueryRegistryHarness {
	t.Helper()
	artifact, err := queryregistry.NewCoreArtifact("core.outlet", "1.0.0", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	registry := queryregistry.New(queryregistry.WithCostPolicy(queryregistry.CostPolicyFunc(func(input queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
		maximum := 100
		if input.RequestedMaximum > 0 && input.RequestedMaximum < maximum {
			maximum = input.RequestedMaximum
		}
		return queryregistry.QueryCost{Units: 10 + input.Pagination.Limit, Maximum: maximum}, nil
	})))
	declaration := queryregistry.QueryDeclaration{
		ID: "core.outlet.users", ContractVersion: "core.outlet.users@1", Entity: "user",
		PlanVersion: "core.outlet.users.plan@1", Fields: []string{"id", "name"},
		Filters: []string{"id"}, Sort: []string{"id"}, Pagination: queryregistry.PaginationOffset,
		ResultSchema: "core.outlet.user@1", PermissionPolicy: "user.read",
	}
	if _, err := registry.Publish(queryregistry.Publication{Artifact: artifact, Queries: []queryregistry.QueryDeclaration{declaration}}); err != nil {
		t.Fatal(err)
	}
	query, err := registry.Resolve(declaration.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := &protocolV2QueryRegistryHarness{
		registry: registry, query: query, schemaID: "core.outlet.user", schemaVersion: "1",
		now: time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC),
		runtime: &protocolv2.ExtensionIdentity{
			ExtensionId: "caller.plugin", ExtensionVersion: "1.2.3", ArtifactDigest: strings.Repeat("a", 64),
			TrustGrantId: "41", RuntimeEpoch: 7, InstanceId: "caller-instance-7",
		},
	}
	if provider == nil {
		provider = func(context.Context, queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
			return queryregistry.ProviderExecutionResult{Rows: []queryregistry.QueryRow{
				{"id": "1", "name": "alice"}, {"id": "2", "name": "bob"},
			}}, nil
		}
	}
	resolver, err := queryregistry.NewStaticProviderResolver([]queryregistry.ExecutableProviderBinding{{
		QueryID: query.ID, ContractVersion: query.ContractVersion, PlanVersion: query.PlanVersion,
		ResultSchema: query.ResultSchema, Artifact: query.Artifact,
		Provider: queryregistry.ExecutableProviderFunc(func(ctx context.Context, request queryregistry.ProviderExecutionRequest) (queryregistry.ProviderExecutionResult, error) {
			h.providerCalls.Add(1)
			return provider(ctx, request)
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: resolver,
		Schemas: queryregistry.ResultSchemaValidatorFunc(func(context.Context, queryregistry.ResultSchemaClaim, queryregistry.QueryRow) error { return nil }),
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.actors = &protocolV2QueryTestActors{projection: ProtocolV2QueryActorProjection{
		ActorUserID: 42, Authenticated: true, ActorFingerprint: "actor:42:session-v1", PolicyFingerprint: "policy:roles-v1",
	}}
	h.callerAdmission = &protocolV2QueryTestCallerAdmission{}
	delegations, err := newProtocolV2QueryDelegationAuthority(
		[]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return h.now }, protocolV2QueryDelegationTTL,
	)
	if err != nil {
		t.Fatal(err)
	}
	h.service, err = newProtocolV2QueryRegistryService(registry, execution, h.actors, h.callerAdmission, delegations)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *protocolV2QueryRegistryHarness) issueInput() ProtocolV2QueryActorDelegationRequest {
	return ProtocolV2QueryActorDelegationRequest{
		ActorUserID: 42, Runtime: h.runtime, QueryID: h.query.ID,
		Locale: "zh-CN", Scope: "admin.users", MaxCost: 100,
	}
}

func (h *protocolV2QueryRegistryHarness) issueRequest(t *testing.T) *hostv2.QueryRequest {
	t.Helper()
	grant, err := h.service.IssueProtocolV2QueryActorDelegation(context.Background(), h.issueInput())
	if err != nil {
		t.Fatal(err)
	}
	return h.requestFromGrant(grant)
}

func (h *protocolV2QueryRegistryHarness) requestFromGrant(grant ProtocolV2QueryActorDelegationGrant) *hostv2.QueryRequest {
	return &hostv2.QueryRequest{
		Context: &protocolv2.RequestContext{
			RequestId: "query-request-42", Locale: "zh-CN",
			Extension: proto.Clone(h.runtime).(*protocolv2.ExtensionIdentity),
		},
		QueryId: grant.QueryID, ContractVersion: grant.ContractVersion, PlanVersion: grant.PlanVersion,
		ResultSchemaId: grant.ResultSchemaID, ResultSchemaVersion: grant.ResultSchemaVersion,
		Scope: grant.Scope, ActorDelegation: grant.Token, Page: &protocolv2.PageRequest{Limit: 1},
	}
}

func (h *protocolV2QueryRegistryHarness) runtimeContext(parent context.Context) context.Context {
	return ContextWithProtocolV2RuntimeIdentity(parent, h.runtime)
}

type protocolV2QueryTestActors struct {
	projection     ProtocolV2QueryActorProjection
	authorizeCalls atomic.Int64
	authorize      func(int64, queryregistry.PermissionClaim, int64) (ProtocolV2QueryActorProjection, error)
}

func (a *protocolV2QueryTestActors) ResolveProtocolV2QueryActor(_ context.Context, actorUserID int64) (ProtocolV2QueryActorProjection, error) {
	if a == nil || actorUserID != a.projection.ActorUserID {
		return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
	}
	return a.projection, nil
}

func (a *protocolV2QueryTestActors) AuthorizeProtocolV2QueryActor(
	_ context.Context,
	actorUserID int64,
	claim queryregistry.PermissionClaim,
) (ProtocolV2QueryActorProjection, error) {
	call := a.authorizeCalls.Add(1)
	if a.authorize != nil {
		return a.authorize(actorUserID, claim, call)
	}
	if actorUserID != a.projection.ActorUserID || claim.PermissionPolicy != "user.read" {
		return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
	}
	return a.projection, nil
}

type protocolV2QueryTestCallerAdmission struct {
	calls  atomic.Int64
	failAt atomic.Int64
}

func (a *protocolV2QueryTestCallerAdmission) AuthorizeProtocolV2QueryCaller(_ context.Context, identity *protocolv2.ExtensionIdentity) error {
	call := a.calls.Add(1)
	if identity == nil || identity.GetExtensionId() != "caller.plugin" ||
		(a.failAt.Load() > 0 && call >= a.failAt.Load()) {
		return ErrProtocolV2QueryCallerStale
	}
	return nil
}

type protocolV2QueryRegistryTestStream struct {
	ctx  context.Context
	rows []*hostv2.QueryRow
}

func (s *protocolV2QueryRegistryTestStream) Send(row *hostv2.QueryRow) error {
	s.rows = append(s.rows, proto.Clone(row).(*hostv2.QueryRow))
	return nil
}
func (s *protocolV2QueryRegistryTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *protocolV2QueryRegistryTestStream) SendHeader(metadata.MD) error { return nil }
func (s *protocolV2QueryRegistryTestStream) SetTrailer(metadata.MD)       {}
func (s *protocolV2QueryRegistryTestStream) Context() context.Context     { return s.ctx }
func (s *protocolV2QueryRegistryTestStream) SendMsg(value any) error {
	row, ok := value.(*hostv2.QueryRow)
	if !ok {
		return errors.New("unexpected stream message")
	}
	return s.Send(row)
}
func (s *protocolV2QueryRegistryTestStream) RecvMsg(any) error { return io.EOF }

var _ grpc.ServerStreamingServer[hostv2.QueryRow] = (*protocolV2QueryRegistryTestStream)(nil)
