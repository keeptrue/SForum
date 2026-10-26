package pluginv2

import (
	"testing"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestShortcodeProjectionIDsFilterIsBoundedCanonicalAndDeduplicated(t *testing.T) {
	filter, err := NewShortcodeProjectionIDsFilter(9, 2, 9, 4)
	if err != nil {
		t.Fatal(err)
	}
	if filter.GetField() != "ids" || filter.GetOperator() != "eq" ||
		TypedDocumentValues(filter.GetValue())["value"] != "[2,4,9]" {
		t.Fatalf("filter=%#v", filter)
	}
	tooMany := make([]int64, ShortcodeProjectionMaxBatch+1)
	for index := range tooMany {
		tooMany[index] = int64(index + 1)
	}
	if _, err := NewShortcodeProjectionIDsFilter(tooMany...); err == nil {
		t.Fatal("over-limit filter was accepted")
	}
}

func TestContentProjectionDelegationsRequireExactFrozenSet(t *testing.T) {
	ctx := &protocolwire.RequestContext{Extension: &protocolwire.ExtensionIdentity{ExtensionId: ShortcodeProjectionExtensionID}}
	for _, contract := range shortcodeProjectionContracts {
		ctx.HostQueryDelegations = append(ctx.HostQueryDelegations, &protocolwire.HostQueryDelegation{
			QueryId: contract.QueryID, ContractVersion: ShortcodeProjectionContractVersion(contract.QueryID),
			PlanVersion: ShortcodeProjectionPlanVersion(contract.QueryID), ResultSchemaId: contract.ResultSchema,
			ResultSchemaVersion: "1", Scope: "content", Token: "opaque",
		})
	}
	if !validContentHostQueryDelegations(ctx, false) {
		t.Fatal("exact shortcode projection set was rejected")
	}
	ctx.HostQueryDelegations[0].QueryId = "core.query.unfrozen"
	if validContentHostQueryDelegations(ctx, false) {
		t.Fatal("unfrozen Host query was accepted")
	}
	foreign := &protocolwire.RequestContext{
		Extension:            &protocolwire.ExtensionIdentity{ExtensionId: "example.content"},
		HostQueryDelegations: []*protocolwire.HostQueryDelegation{{QueryId: ShortcodePublicUsersQueryID, Token: "opaque"}},
	}
	if validContentHostQueryDelegations(foreign, false) {
		t.Fatal("foreign content runtime received shortcode projections")
	}
}

func TestProtectedContentProjectionDelegationsRequireExactSuppression(t *testing.T) {
	ctx := &protocolwire.RequestContext{Extension: &protocolwire.ExtensionIdentity{ExtensionId: ShortcodeProjectionExtensionID}}
	if !preflightContentHostQueryDelegations(ctx) || !validContentHostQueryDelegations(ctx, true) {
		t.Fatal("protected shortcode call rejected an empty Host delegation set")
	}
	ctx.HostQueryDelegations = []*protocolwire.HostQueryDelegation{{
		QueryId: ShortcodePublicUsersQueryID, Token: "opaque",
	}}
	if validContentHostQueryDelegations(ctx, true) {
		t.Fatal("protected shortcode call accepted a partial Host delegation set")
	}
}
