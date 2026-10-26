package hostapi

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type fakeInventory struct {
	items []map[string]any
	err   error
}

func (f fakeInventory) ListRedactedInventory(context.Context) ([]map[string]any, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func TestProtocolV2ExtensionInventoryRequiresExtensionsRead(t *testing.T) {
	items := []map[string]any{
		{"id": "alpha.plugin", "name": "Alpha", "type": "plugin", "status": "enabled", "packageDigest": "aaa"},
		{"id": "beta.theme", "name": "Beta", "type": "theme", "status": "enabled"},
	}
	service := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.ExtensionsRead})},
		Inventory:    fakeInventory{items: items},
	})
	core := &protocolV2Core{service: service}
	server := &protocolV2QueryServer{core: core}
	requestContext := testProtocolV2RequestContext()

	response, err := server.Execute(context.Background(), &hostv2.QueryRequest{
		Context: requestContext, QueryId: QueryExtensionInventoryID, PlanVersion: QueryExtensionInventoryVersion,
		ResultSchemaId: QueryExtensionInventorySchemaID, ResultSchemaVersion: QueryExtensionInventorySchemaV1,
		Page: &protocolv2.PageRequest{Limit: 10},
	})
	if err != nil || response.GetError() != nil || len(response.GetRows()) != 2 {
		t.Fatalf("inventory=%#v err=%v", response, err)
	}
	first := response.GetRows()[0].GetValue().AsMap()
	if first["id"] != "alpha.plugin" || first["packageDigest"] != "aaa" {
		t.Fatalf("first row=%#v", first)
	}

	// 缺少能力：拒绝。
	denied := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})},
		Inventory:    fakeInventory{items: items},
	})
	deniedResponse, _ := (&protocolV2QueryServer{core: &protocolV2Core{service: denied}}).Execute(context.Background(), &hostv2.QueryRequest{
		Context: requestContext, QueryId: QueryExtensionInventoryID, PlanVersion: QueryExtensionInventoryVersion,
	})
	if deniedResponse.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED {
		t.Fatalf("denial=%#v", deniedResponse.GetError())
	}

	// type 过滤。
	filterValue, err := protocolV2Document(QueryTextParameterSchemaID, QueryStableCoreParameterSchemaV1, map[string]any{"value": "plugin"})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := server.Execute(context.Background(), &hostv2.QueryRequest{
		Context: requestContext, QueryId: QueryExtensionInventoryID, PlanVersion: QueryExtensionInventoryVersion,
		Filters: []*hostv2.QueryFilter{{Field: "type", Operator: "eq", Value: filterValue}},
	})
	if err != nil || filtered.GetError() != nil || len(filtered.GetRows()) != 1 {
		t.Fatalf("filtered=%#v err=%v", filtered, err)
	}
	if filtered.GetRows()[0].GetValue().AsMap()["id"] != "alpha.plugin" {
		t.Fatalf("filtered row=%#v", filtered.GetRows()[0].GetValue().AsMap())
	}
}
