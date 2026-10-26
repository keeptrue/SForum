package extensionsruntime

import (
	"context"
	"errors"
	"testing"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type recordingShortcodeProjectionIssuer struct {
	request hostapi.ProtocolV2ShortcodeProjectionDelegationRequest
	grants  []hostapi.ProtocolV2QueryActorDelegationGrant
	calls   int
}

func (i *recordingShortcodeProjectionIssuer) IssueProtocolV2ShortcodeProjectionDelegations(
	_ context.Context,
	request hostapi.ProtocolV2ShortcodeProjectionDelegationRequest,
) ([]hostapi.ProtocolV2QueryActorDelegationGrant, error) {
	i.calls++
	i.request = request
	return append([]hostapi.ProtocolV2QueryActorDelegationGrant(nil), i.grants...), nil
}

func TestContentRuntimeRequestContextGrantsOnlyExactShortcodeProjectionBundle(t *testing.T) {
	issuer := &recordingShortcodeProjectionIssuer{grants: shortcodeProjectionTestGrants()}
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: hostapi.ShortcodeProjectionExtensionID, ExtensionVersion: "1.0.0",
		ArtifactDigest: "digest", TrustGrantId: "grant", RuntimeEpoch: 1, InstanceId: "runtime",
	}
	client := newProtocolV2Client(nil, protocolV2ClientConfig{
		identity: identity, instance: "runtime", shortcodeQueries: issuer,
		authority: []*protocolwire.AuthorityGrant{{Key: "database.core_views"}},
	})
	request, err := client.contentRuntimeRequestContext(context.Background(), "sforum-shortcodes.user", 42)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 1 || issuer.request.ActorUserID != 42 || issuer.request.Scope != "content" ||
		issuer.request.Runtime.GetExtensionId() != hostapi.ShortcodeProjectionExtensionID {
		t.Fatalf("projection issue request = %#v calls=%d", issuer.request, issuer.calls)
	}
	if request.GetActor() != nil || len(request.GetGrantedAuthority()) != 0 ||
		len(request.GetHostCommandDelegations()) != 0 || len(request.GetHostQueryDelegations()) != 7 {
		t.Fatalf("content context exposed authority or wrong grants: %#v", request)
	}
	for index, queryID := range hostapi.ShortcodeProjectionQueryIDs() {
		delegation := request.GetHostQueryDelegations()[index]
		if delegation.GetQueryId() != queryID || delegation.GetContractVersion() != queryID+"@1" ||
			delegation.GetPlanVersion() != queryID+".plan@1" || delegation.GetScope() != "content" ||
			delegation.GetToken() == "" {
			t.Fatalf("delegation %d drifted: %#v", index, delegation)
		}
	}
}

func TestContentRuntimeRequestContextWithholdsProjectionBundleFromOtherExtensions(t *testing.T) {
	issuer := &recordingShortcodeProjectionIssuer{grants: shortcodeProjectionTestGrants()}
	client := newProtocolV2Client(nil, protocolV2ClientConfig{
		identity: &protocolwire.ExtensionIdentity{ExtensionId: "other.content", InstanceId: "runtime"},
		instance: "runtime", shortcodeQueries: issuer,
	})
	request, err := client.contentRuntimeRequestContext(context.Background(), "other.content.filter", 42)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 0 || request.GetActor() != nil || len(request.GetGrantedAuthority()) != 0 ||
		len(request.GetHostQueryDelegations()) != 0 || len(request.GetHostCommandDelegations()) != 0 {
		t.Fatalf("foreign content context received authority: %#v calls=%d", request, issuer.calls)
	}
}

func TestContentRuntimeRequestContextFailsClosedWithoutProjectionIssuer(t *testing.T) {
	client := newProtocolV2Client(nil, protocolV2ClientConfig{
		identity: &protocolwire.ExtensionIdentity{
			ExtensionId: hostapi.ShortcodeProjectionExtensionID, InstanceId: "runtime",
		},
		instance: "runtime",
	})
	if _, err := client.contentRuntimeRequestContext(context.Background(), "sforum-shortcodes.user", 0); !errors.Is(err, ErrProtocolV2ContentInvalid) {
		t.Fatalf("missing shortcode projection issuer error = %v", err)
	}
}

func shortcodeProjectionTestGrants() []hostapi.ProtocolV2QueryActorDelegationGrant {
	result := make([]hostapi.ProtocolV2QueryActorDelegationGrant, 0, 7)
	for _, queryID := range hostapi.ShortcodeProjectionQueryIDs() {
		result = append(result, hostapi.ProtocolV2QueryActorDelegationGrant{
			QueryID: queryID, ContractVersion: queryID + "@1", PlanVersion: queryID + ".plan@1",
			ResultSchemaID: "schema", ResultSchemaVersion: "1", Scope: "content", Token: "token-" + queryID,
		})
	}
	return result
}
