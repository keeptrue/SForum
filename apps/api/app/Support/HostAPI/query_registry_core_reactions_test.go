package hostapi

import (
	"context"
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func testProtocolV2ReactionsQuery(t *testing.T) *hostv2.QueryRequest {
	t.Helper()
	return &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicReactionsList, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{
			{Field: "target_type", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "topic")},
			{Field: "target_id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, "42")},
		},
	}
}

// TestProtocolV2ReactionsQueryAllowsTrustedPublicRead 覆盖允许侧：可信制品 +
// CoreViews 的公开查询能执行，行形状按 BIGINT→string 归一，默认排序是最近点赞在前。
func TestProtocolV2ReactionsQueryAllowsTrustedPublicRead(t *testing.T) {
	identity := testProtocolV2QueryIdentity()
	var plan protocolV2QueryPlan
	executor := protocolV2QueryExecutorFunc(func(_ context.Context, got protocolV2QueryPlan) ([]map[string]any, error) {
		plan = got
		return []map[string]any{{
			"id": "2", "target_type": "topic", "target_id": "42", "reaction": "like",
			"user_id": "7", "username": "alice", "display_name": "Alice",
			"created_at": "2026-10-29T00:00:00Z",
		}}, nil
	})
	authority := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, authority)
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)

	response := engine.execute(ctx, testProtocolV2ReactionsQuery(t))
	if response.GetError() != nil || len(response.GetRows()) != 1 {
		t.Fatalf("reactions response = %#v", response)
	}
	row := response.GetRows()[0].GetValue().AsMap()
	if row["target_id"] != "42" || row["user_id"] != "7" || row["username"] != "alice" {
		t.Fatalf("reactions row = %#v", row)
	}
	if _, leaked := row["email"]; leaked {
		t.Fatalf("reactions row leaked email: %#v", row)
	}
	if plan.Definition.ID != QueryPublicReactionsList {
		t.Fatalf("plan definition = %q", plan.Definition.ID)
	}
	if len(plan.Sorts) != 2 ||
		plan.Sorts[0].Field != "created_at" || !plan.Sorts[0].Descending ||
		plan.Sorts[1].Field != "id" || !plan.Sorts[1].Descending {
		t.Fatalf("reactions default sorts = %#v", plan.Sorts)
	}
}

// TestProtocolV2ReactionsQueryRejectsUntrustedAuthorityAndBadShape 覆盖拒绝侧：
// 未授予 CoreViews、缺必填过滤、越界字段都必须在执行前失败。
func TestProtocolV2ReactionsQueryRejectsUntrustedAuthorityAndBadShape(t *testing.T) {
	identity := testProtocolV2QueryIdentity()
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		t.Fatal("denied reactions query executed")
		return nil, nil
	})
	trusted := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})

	t.Run("core views denied", func(t *testing.T) {
		denied := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
			return ProtocolV2QueryAuthority{ExactArtifact: true}, nil
		})
		ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
		response := testProtocolV2QueryEngine(t, executor, denied).execute(ctx, testProtocolV2ReactionsQuery(t))
		if response.GetError().GetReason() != "host.query_core_views_denied" {
			t.Fatalf("denied response = %#v", response)
		}
	})

	t.Run("missing required target filter", func(t *testing.T) {
		request := testProtocolV2ReactionsQuery(t)
		request.Filters = request.Filters[:1]
		ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
		response := testProtocolV2QueryEngine(t, executor, trusted).execute(ctx, request)
		if response.GetError().GetReason() != "host.query_filter_invalid" {
			t.Fatalf("missing filter response = %#v", response)
		}
	})

	t.Run("unsupported field", func(t *testing.T) {
		request := testProtocolV2ReactionsQuery(t)
		request.Fields = []string{"id", "email"}
		ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), identity)
		response := testProtocolV2QueryEngine(t, executor, trusted).execute(ctx, request)
		if response.GetError().GetReason() != "host.query_field_unsupported" {
			t.Fatalf("unsupported field response = %#v", response)
		}
	})
}
