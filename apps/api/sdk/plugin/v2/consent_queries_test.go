package pluginv2

import (
	"errors"
	"testing"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func consentTestHost() *Host {
	return &Host{
		instance: "consent-instance",
		identity: &protocolwire.ExtensionIdentity{
			ExtensionId: "sforum.consented-claim-reference", ExtensionVersion: "1.0.0",
			ArtifactDigest: "digest", TrustGrantId: "1", RuntimeEpoch: 1, InstanceId: "consent-instance",
		},
	}
}

func TestSelfEmailQueryRequestBindsHostIssuedDelegation(t *testing.T) {
	host := consentTestHost()
	parent := host.RequestContext(nil)
	parent.HostQueryDelegations = []*protocolwire.HostQueryDelegation{{
		QueryId: SelfEmailQueryID, ContractVersion: SelfEmailContractVersion, PlanVersion: SelfEmailPlanVersion,
		ResultSchemaId: SelfEmailResultSchemaID, ResultSchemaVersion: SelfEmailResultSchemaVersion,
		Scope: SelfEmailScope, Token: "host-signed-token",
	}}
	request, err := host.SelfEmailQueryRequest(parent)
	if err != nil {
		t.Fatal(err)
	}
	if request.GetQueryId() != SelfEmailQueryID || request.GetContractVersion() != SelfEmailContractVersion ||
		request.GetPlanVersion() != SelfEmailPlanVersion ||
		request.GetResultSchemaId() != SelfEmailResultSchemaID ||
		request.GetResultSchemaVersion() != SelfEmailResultSchemaVersion ||
		request.GetScope() != SelfEmailScope || request.GetActorDelegation() != "host-signed-token" {
		t.Fatalf("request = %#v", request)
	}
	if request.GetPage().GetLimit() != SelfEmailQueryPageLimit {
		t.Fatalf("page = %#v", request.GetPage())
	}
	if request.GetContext().GetActor() != nil || len(request.GetContext().GetHostQueryDelegations()) != 0 {
		t.Fatalf("request context must stay actorless and delegation-free: %#v", request.GetContext())
	}
}

func TestSelfEmailQueryRequestFailsClosedWithoutDelegation(t *testing.T) {
	host := consentTestHost()
	if _, err := host.SelfEmailQueryRequest(host.RequestContext(nil)); !errors.Is(err, ErrHostQueryDelegationUnavailable) {
		t.Fatalf("missing delegation error = %v", err)
	}
	parent := host.RequestContext(nil)
	parent.HostQueryDelegations = []*protocolwire.HostQueryDelegation{{
		QueryId: SelfEmailQueryID, ContractVersion: SelfEmailQueryID + "@2", PlanVersion: SelfEmailPlanVersion,
		ResultSchemaId: SelfEmailResultSchemaID, ResultSchemaVersion: SelfEmailResultSchemaVersion,
		Scope: SelfEmailScope, Token: "other-version-token",
	}}
	if _, err := host.SelfEmailQueryRequest(parent); !errors.Is(err, ErrHostQueryDelegationUnavailable) {
		t.Fatalf("contract drift error = %v", err)
	}
}
