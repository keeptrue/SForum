package pluginv2

import (
	"context"
	"strings"
	"testing"
	"time"

	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSEORegistryDispatchesExactActorlessTypedContribution(t *testing.T) {
	definition := SEODefinition{
		ID: "plugin.seo.reference.title", ContractVersion: "plugin.seo.reference.title@1",
		Scope: "core.page.topic", Kind: seoregistry.KindTitle, Action: seoregistry.ActionFilter,
		Handler: "plugin.seo.reference.title", FailurePolicy: seoregistry.FailurePolicyFallback, TimeoutMS: 500,
		Execute: func(_ context.Context, call *SEOCall) (seoregistry.Document, error) {
			if call.Context.GetActor() != nil || call.Scope != "core.page.topic" {
				t.Fatal("SEO call received actor/session authority or wrong scope")
			}
			call.Current.Title = "Reference SEO title"
			return call.Current, nil
		},
	}
	registry, err := NewSEORegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	contribution := seoregistry.Contribution{
		Declaration: seoregistry.Declaration{
			ID: definition.ID, ContractVersion: definition.ContractVersion, Scope: definition.Scope,
			Kind: definition.Kind, Action: definition.Action, Handler: definition.Handler,
			FailurePolicy: definition.FailurePolicy, Timeout: 500 * time.Millisecond,
		},
		Artifact: seoregistry.Artifact{
			ExtensionID: "plugin.seo.reference", ExtensionVersion: "1.0.0",
			PackageDigest: strings.Repeat("a", 64), ImpactDigest: strings.Repeat("b", 64),
			VersionID: 9, RuntimeInstanceID: "seo-reference-runtime",
		},
	}
	values, err := strictSEOMap(seoApplyRequest{
		Scope: contribution.Scope,
		Contribution: SEOContribution{
			ID: definition.ID, ContractVersion: definition.ContractVersion, Scope: definition.Scope,
			Kind: definition.Kind, Action: definition.Action, Handler: definition.Handler,
			FailurePolicy: definition.FailurePolicy, TimeoutMS: definition.TimeoutMS,
		},
		Current: seoregistry.Document{Title: "Core title"},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := NewTypedDocument(seoRequestSchema, values)
	if err != nil {
		t.Fatal(err)
	}
	requestContext := &protocolwire.RequestContext{
		RequestId: "seo-1", Deadline: timestamppb.New(time.Now().Add(time.Second)),
		Extension: &protocolwire.ExtensionIdentity{
			ExtensionId: contribution.Artifact.ExtensionID, ExtensionVersion: contribution.Artifact.ExtensionVersion,
			ArtifactDigest: contribution.Artifact.PackageDigest, InstanceId: contribution.Artifact.RuntimeInstanceID,
		},
	}
	server := NewServer().WithSEORegistry(registry)
	server.started = true
	server.identity = cloneV2IdentityForSEOTest(requestContext.GetExtension())
	response, err := server.ProviderCall(context.Background(), &pluginwire.ProviderCallRequest{
		Context: requestContext, SlotId: seoProviderSlot,
		Operation: seoProviderOperation, DeclarationId: definition.ID,
		ContractVersion: definition.ContractVersion, Input: document,
	})
	if err != nil || response.GetError() != nil || !DocumentMatchesSchema(response.GetOutput(), seoResponseSchema) {
		t.Fatalf("SEO response=%#v err=%v", response, err)
	}
	result := seoApplyResponse{}
	if err := strictSEODecode(TypedDocumentValues(response.GetOutput()), &result); err != nil || result.Document.Title != "Reference SEO title" {
		t.Fatalf("SEO result=%#v err=%v", result, err)
	}

	requestContext.Actor = &protocolwire.Actor{UserId: 42}
	denied, err := server.ProviderCall(context.Background(), &pluginwire.ProviderCallRequest{
		Context: requestContext, SlotId: seoProviderSlot,
		Operation: seoProviderOperation, DeclarationId: definition.ID,
		ContractVersion: definition.ContractVersion, Input: document,
	})
	if err != nil || denied.GetError().GetReason() != "seo.actor_forbidden" {
		t.Fatalf("actor-bearing SEO response=%#v err=%v", denied, err)
	}
}

func cloneV2IdentityForSEOTest(value *protocolwire.ExtensionIdentity) *protocolwire.ExtensionIdentity {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolwire.ExtensionIdentity)
}
