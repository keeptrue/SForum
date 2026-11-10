package hostapi

import (
	"context"
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 收藏公开读面的允许/拒绝契约：
//   - 允许：精确制品 + core_views 权限 → 计划落在 sforum_core_v1.forum_bookmarks，
//     只暴露公开行字段（没有 remind_at / reminded_at / 邮箱）。
//   - 拒绝：无 core_views 权限 → host.query_core_views_denied；制品陈旧 →
//     host.query_runtime_stale。两种情况都不能执行到 executor。

func newBookmarkQueryRequest(t *testing.T) *hostv2.QueryRequest {
	t.Helper()
	return &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicBookmarksList,
		PlanVersion:    QueryStableCorePlanVersion,
		ResultSchemaId: QueryPublicBookmarkSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
		Filters: []*hostv2.QueryFilter{
			{Field: "target_type", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "topic")},
			{Field: "target_id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, "42")},
		},
	}
}

func TestProtocolV2PublicBookmarksQueryRequiresCoreViews(t *testing.T) {
	executor := protocolV2QueryExecutorFunc(func(context.Context, protocolV2QueryPlan) ([]map[string]any, error) {
		t.Fatal("denied query executed")
		return nil, nil
	})
	denied := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: false}, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, denied)
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity())

	response := engine.execute(ctx, newBookmarkQueryRequest(t))
	if response.GetError().GetReason() != "host.query_core_views_denied" {
		t.Fatalf("response = %#v", response)
	}

	stale := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: false, CoreViews: true}, nil
	})
	staleResponse := testProtocolV2QueryEngine(t, executor, stale).execute(ctx, newBookmarkQueryRequest(t))
	if staleResponse.GetError().GetReason() != "host.query_runtime_stale" {
		t.Fatalf("stale response = %#v", staleResponse)
	}
}

func TestProtocolV2PublicBookmarksQueryBindsCoreViewAndPublicColumns(t *testing.T) {
	var captured protocolV2QueryPlan
	executor := protocolV2QueryExecutorFunc(func(_ context.Context, plan protocolV2QueryPlan) ([]map[string]any, error) {
		captured = plan
		return []map[string]any{{
			"id": "7", "target_type": "topic", "target_id": "42", "visibility": "public",
			"user_id": "9", "username": "alice", "display_name": "Alice",
			"created_at": "2026-11-10T00:00:00Z", "updated_at": "2026-11-10T00:00:00Z",
		}}, nil
	})
	authority := protocolV2QueryAuthorityFunc(func(context.Context, *protocolv2.ExtensionIdentity) (ProtocolV2QueryAuthority, error) {
		return ProtocolV2QueryAuthority{ExactArtifact: true, CoreViews: true}, nil
	})
	engine := testProtocolV2QueryEngine(t, executor, authority)
	ctx := ContextWithProtocolV2RuntimeIdentity(context.Background(), testProtocolV2QueryIdentity())

	response := engine.execute(ctx, newBookmarkQueryRequest(t))
	if response.GetError() != nil {
		t.Fatalf("response error = %#v", response.GetError())
	}
	if len(response.GetRows()) != 1 {
		t.Fatalf("rows = %#v", response.GetRows())
	}
	row := response.GetRows()[0].GetValue().AsMap()
	if row["visibility"] != "public" || row["username"] != "alice" {
		t.Fatalf("row = %#v", row)
	}
	for _, forbidden := range []string{"remind_at", "reminded_at", "email"} {
		if _, ok := row[forbidden]; ok {
			t.Fatalf("public bookmark query exposed %q: %#v", forbidden, row)
		}
	}
	if captured.Definition.From != "sforum_core_v1.forum_bookmarks AS stable" {
		t.Fatalf("plan from = %q", captured.Definition.From)
	}
	// 私密收藏永远不会出现：视图谓词固定 visibility='public'，查询面也不提供
	// 关闭该过滤的参数。
	for _, filter := range captured.Definition.Filters {
		if filter.Field == "visibility" {
			t.Fatalf("public bookmark query must not expose a visibility filter: %#v", filter)
		}
	}
}
