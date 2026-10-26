package pluginv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
	"time"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestHostRequestContextRebindsRuntimeOwnedFields(t *testing.T) {
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.v2", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
		TrustGrantId: "grant", RuntimeEpoch: 8, InstanceId: "instance-8",
	}
	authority := []*protocolwire.AuthorityGrant{{Key: "settings.own", ContractVersion: HostAPIVersion}}
	host := &Host{identity: identity, authority: authority, instance: identity.InstanceId}
	parent := &protocolwire.RequestContext{
		RequestId: "plugin-call", Locale: "zh-CN", Deadline: timestamppb.New(time.Now().Add(time.Hour)),
		Trace: &protocolwire.TraceContext{TraceId: "trace-1"}, Actor: &protocolwire.Actor{UserId: 42},
		Extension:        &protocolwire.ExtensionIdentity{ExtensionId: "forged"},
		GrantedAuthority: []*protocolwire.AuthorityGrant{{Key: "raw.database"}},
		HostCommandDelegations: []*protocolwire.HostCommandDelegation{{
			CommandId: "sforum.demo", CommandVersion: "1", IdempotencyKey: "request-42", Token: "secret-token",
		}},
		HostQueryDelegations: []*protocolwire.HostQueryDelegation{{
			QueryId: "sforum.demo.query", ContractVersion: "sforum.demo.query@1",
			PlanVersion: "sforum.demo.query.plan@1", Token: "query-token",
		}},
	}

	result := host.RequestContext(parent)
	if result.GetRequestId() != "instance-8-host-1" || !proto.Equal(result.GetExtension(), identity) ||
		!equalAuthority(result.GetGrantedAuthority(), authority) {
		t.Fatalf("runtime binding = %#v", result)
	}
	if result.GetActor() != nil || len(result.GetHostCommandDelegations()) != 0 || len(result.GetHostQueryDelegations()) != 0 ||
		result.GetLocale() != "zh-CN" || result.GetTrace().GetTraceId() != "trace-1" {
		t.Fatalf("request context was not propagated: %#v", result)
	}
	if remaining := time.Until(result.GetDeadline().AsTime()); remaining <= 0 || remaining > 6*time.Second {
		t.Fatalf("deadline was not bounded: %s", remaining)
	}
	if parent.GetActor().GetUserId() != 42 || parent.GetExtension().GetExtensionId() != "forged" || parent.GetGrantedAuthority()[0].GetKey() != "raw.database" {
		t.Fatalf("parent context was mutated: %#v", parent)
	}
}

func TestHostDelegatedQueryRequestSelectsExactTokenWithoutMutatingParent(t *testing.T) {
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.v2", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
		TrustGrantId: "grant", RuntimeEpoch: 8, InstanceId: "instance-8",
	}
	host := &Host{identity: identity, instance: identity.InstanceId}
	parent := &protocolwire.RequestContext{
		Locale: "zh-CN", Trace: &protocolwire.TraceContext{TraceId: "trace-1"},
		HostQueryDelegations: []*protocolwire.HostQueryDelegation{
			{QueryId: "sforum.other", ContractVersion: "sforum.other@1", PlanVersion: "sforum.other.plan@1", ResultSchemaId: "other", ResultSchemaVersion: "1", Token: "other-token"},
			{QueryId: "sforum.demo", ContractVersion: "sforum.demo@1", PlanVersion: "sforum.demo.plan@1", ResultSchemaId: "sforum.demo.result", ResultSchemaVersion: "1", Scope: "admin.demo", Token: "demo-token"},
		},
	}
	request, err := host.DelegatedQueryRequest(parent, " sforum.demo ", "sforum.demo@1", "sforum.demo.plan@1")
	if err != nil {
		t.Fatal(err)
	}
	if request.GetQueryId() != "sforum.demo" || request.GetContractVersion() != "sforum.demo@1" ||
		request.GetPlanVersion() != "sforum.demo.plan@1" || request.GetResultSchemaId() != "sforum.demo.result" ||
		request.GetResultSchemaVersion() != "1" || request.GetScope() != "admin.demo" ||
		request.GetActorDelegation() != "demo-token" || request.GetContext().GetActor() != nil ||
		len(request.GetContext().GetHostQueryDelegations()) != 0 || !proto.Equal(request.GetContext().GetExtension(), identity) {
		t.Fatalf("query request = %#v", request)
	}
	if len(parent.GetHostQueryDelegations()) != 2 || parent.GetHostQueryDelegations()[1].GetToken() != "demo-token" {
		t.Fatal("delegated query request mutated its parent")
	}
	for _, input := range []struct{ queryID, contractVersion, planVersion string }{
		{}, {queryID: "sforum.missing", contractVersion: "sforum.demo@1", planVersion: "sforum.demo.plan@1"},
		{queryID: "sforum.demo", contractVersion: "sforum.demo@2", planVersion: "sforum.demo.plan@1"},
	} {
		if _, err := host.DelegatedQueryRequest(parent, input.queryID, input.contractVersion, input.planVersion); !errors.Is(err, ErrHostQueryDelegationUnavailable) {
			t.Fatalf("input %#v error = %v", input, err)
		}
	}
	document, err := NewHostQueryFilterValue("42")
	if err != nil || !DocumentMatchesSchema(document, HostQueryFilterValueSchemaRef) || TypedDocumentValues(document)["value"] != "42" {
		t.Fatalf("filter document = %#v, %v", document, err)
	}
}

func TestHostDelegatedCommandRequestSelectsOneTokenWithoutMutatingParent(t *testing.T) {
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.v2", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
		TrustGrantId: "grant", RuntimeEpoch: 8, InstanceId: "instance-8",
	}
	host := &Host{identity: identity, instance: identity.InstanceId}
	parent := &protocolwire.RequestContext{
		IdempotencyKey: "request-42", Trace: &protocolwire.TraceContext{TraceId: "trace-1"},
		HostCommandDelegations: []*protocolwire.HostCommandDelegation{
			{CommandId: "sforum.other", CommandVersion: "1", IdempotencyKey: "request-42", Token: "other-token"},
			{CommandId: "sforum.demo", CommandVersion: "1", IdempotencyKey: "request-42", Token: "demo-token"},
		},
	}
	input := &protocolwire.TypedDocument{SchemaId: "demo.input", SchemaVersion: "1"}
	request, err := host.DelegatedCommandRequest(parent, " sforum.demo ", "1", input)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetCommandId() != "sforum.demo" || request.GetCommandVersion() != "1" ||
		request.GetIdempotencyKey() != "request-42" || request.GetActorDelegation() != "demo-token" ||
		request.GetContext().GetIdempotencyKey() != "request-42" || len(request.GetContext().GetHostCommandDelegations()) != 0 ||
		request.GetContext().GetActor() != nil || request.GetContext().GetExtension().GetInstanceId() != identity.InstanceId {
		t.Fatalf("command request = %#v", request)
	}
	request.Input.SchemaId = "changed"
	if input.GetSchemaId() != "demo.input" || len(parent.GetHostCommandDelegations()) != 2 {
		t.Fatal("delegated command request mutated caller-owned values")
	}
	if err := host.BindCommandQueryInvalidationTags(request, " DEMO.V2.TOPICS ", "demo.v2.members"); err != nil {
		t.Fatalf("bind invalidation tags: %v", err)
	}
	if !slices.Equal(request.GetQueryInvalidationTags(), []string{"demo.v2.members", "demo.v2.topics"}) {
		t.Fatalf("query invalidation tags=%#v", request.GetQueryInvalidationTags())
	}
	overLimit := make([]string, hostCommandInvalidationMaxTags+1)
	for index := range overLimit {
		overLimit[index] = fmt.Sprintf("demo.v2.tag.%02d", index)
	}
	for _, tags := range [][]string{
		nil,
		{"other.plugin.topics"},
		{"demo.v2.topics", " DEMO.V2.TOPICS "},
		overLimit,
	} {
		before := slices.Clone(request.GetQueryInvalidationTags())
		if err := host.BindCommandQueryInvalidationTags(request, tags...); !errors.Is(err, ErrHostCommandInvalidationInvalid) {
			t.Fatalf("tags %#v error=%v", tags, err)
		}
		if !slices.Equal(before, request.GetQueryInvalidationTags()) {
			t.Fatalf("invalid tags mutated request: before=%#v after=%#v", before, request.GetQueryInvalidationTags())
		}
	}
	for _, command := range []string{"", "sforum.missing"} {
		if _, err := host.DelegatedCommandRequest(parent, command, "1", nil); !errors.Is(err, ErrHostActorDelegationUnavailable) {
			t.Fatalf("command %q error = %v", command, err)
		}
	}
}

func TestHostClientStreamKeepsReceiveSideAliveAfterCloseSend(t *testing.T) {
	cancelled := false
	stream := &hostClientStream{
		ClientStream: fakeHostClientStream{},
		cancel:       func() { cancelled = true },
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if cancelled {
		t.Fatal("CloseSend cancelled the server-streaming receive side")
	}
	if err := stream.RecvMsg(new(any)); err != io.EOF {
		t.Fatalf("RecvMsg error = %v", err)
	}
	if !cancelled {
		t.Fatal("terminal RecvMsg did not release the stream context")
	}
}

type fakeHostClientStream struct{}

func (fakeHostClientStream) Header() (metadata.MD, error) { return nil, nil }
func (fakeHostClientStream) Trailer() metadata.MD         { return nil }
func (fakeHostClientStream) CloseSend() error             { return nil }
func (fakeHostClientStream) Context() context.Context     { return context.Background() }
func (fakeHostClientStream) SendMsg(any) error            { return nil }
func (fakeHostClientStream) RecvMsg(any) error            { return io.EOF }

func equalAuthority(left, right []*protocolwire.AuthorityGrant) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !proto.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}
