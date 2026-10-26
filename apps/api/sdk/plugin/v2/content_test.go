package pluginv2

import (
	"context"
	"testing"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestContentRegistryDispatchesExactTypedActorlessCall(t *testing.T) {
	var observed *ContentCall
	registry, err := NewContentRegistry(ContentDefinition{
		ID: "demo.content.filter", ContractVersion: "demo.content.filter@1",
		Kind: ContentKindRenderFilter, Handler: "demo.filter",
		Schema: "demo.content.filter.schema@1",
		Execute: func(_ context.Context, call *ContentCall) (ContentResult, error) {
			observed = call
			result := call.Render
			return ContentResult{Render: &result}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	values, err := strictContentMap(contentCallRequest{
		Target: ContentDeclaration{
			ID: "core.content.target", ContractVersion: "core.content.target@1",
			Kind: ContentKindBlock, Handler: "host.target", Schema: "core.content.schema@1",
		},
		Provider: contentDeclarationFor(registry.order[0]),
		Action:   ContentActionFilter, Operation: ContentOperationFilter,
		Document: ContentEditorDocument{
			SchemaVersion: ContentEditorDocumentSchema,
			ContentID:     "core.content.target", ContractVersion: "core.content.target@1",
			Schema: "core.content.schema@1", StorageVersion: "1", Value: []byte(`{"safe":true}`),
		},
		Render: ContentRenderSegments{
			SchemaVersion: ContentRenderSegmentsSchema,
			ContentID:     "core.content.target", ContractVersion: "core.content.target@1",
			Segments: []ContentRenderSegment{{Kind: ContentSegmentText, Text: "safe"}},
		},
		ResourceID: "topic:42", Locale: "zh-CN", Scope: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := NewTypedDocument(contentCallRequestSchema, values)
	if err != nil {
		t.Fatal(err)
	}
	requestContext := &protocolwire.RequestContext{
		RequestId: "content-1", Deadline: timestamppb.New(time.Now().Add(time.Second)),
		Extension: &protocolwire.ExtensionIdentity{ExtensionId: "demo.content", InstanceId: "runtime-1"},
	}
	response, err := registry.ProviderCall(t.Context(), &pluginwire.ProviderCallRequest{
		Context: requestContext, SlotId: ContentRuntimeProviderSlot,
		DeclarationId: "demo.content.filter", ContractVersion: "demo.content.filter@1",
		Operation: ContentOperationFilter, Input: document,
	})
	if err != nil || response.GetError() != nil || response.GetOutput().GetSchemaId() != "sforum.content.call.response" {
		t.Fatalf("content response=%#v err=%v", response, err)
	}
	if observed == nil || observed.Context.GetActor() != nil || observed.Provider.ID != "demo.content.filter" ||
		observed.ResourceID != "topic:42" || observed.Render.Segments[0].Text != "safe" {
		t.Fatalf("content call=%#v", observed)
	}

	requestContext.GrantedAuthority = []*protocolwire.AuthorityGrant{{Key: "host.users.read"}}
	response, err = registry.ProviderCall(t.Context(), &pluginwire.ProviderCallRequest{
		Context: requestContext, SlotId: ContentRuntimeProviderSlot,
		DeclarationId: "demo.content.filter", ContractVersion: "demo.content.filter@1",
		Operation: ContentOperationFilter, Input: document,
	})
	if err != nil || response.GetError().GetReason() != "content.context_authority_forbidden" {
		t.Fatalf("unsafe authority response=%#v err=%v", response, err)
	}
}
