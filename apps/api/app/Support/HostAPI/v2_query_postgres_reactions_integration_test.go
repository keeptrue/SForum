package hostapi

import (
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// TestPostgresReactionsQueryOnlyExposesVisibleTargets 在真实 PostgreSQL 上验证
// 公开点赞查询：只返回稳定视图里可见的目标、禁用账号的点赞不出现、默认按最近
// 点赞排序、分页游标不重不漏，且行里没有 email 之类的关系面外字段。
func TestPostgresReactionsQueryOnlyExposesVisibleTargets(t *testing.T) {
	h := newPostgresQueryHarness(t)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)

	reactions := func(targetType, targetID string) *hostv2.QueryRequest {
		return &hostv2.QueryRequest{
			Context: testProtocolV2RequestContext(), QueryId: QueryPublicReactionsList, PlanVersion: QueryStableCorePlanVersion,
			Fields: []string{"id", "target_type", "target_id", "reaction", "user_id", "username", "display_name", "created_at"},
			Filters: []*hostv2.QueryFilter{
				{Field: "target_type", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, targetType)},
				{Field: "target_id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, targetID)},
			},
		}
	}

	first := reactions("topic", "1")
	first.Page = &protocolv2.PageRequest{Limit: 1}
	firstPage := h.engine.execute(ctx, first)
	if firstPage.GetError() != nil || len(firstPage.GetRows()) != 1 || !firstPage.GetPage().GetHasMore() {
		t.Fatalf("first reactions page = %#v", firstPage)
	}
	firstRow := firstPage.GetRows()[0].GetValue().AsMap()
	// 最近的点赞在前：bob（1 小时前）先于 alice（2 小时前）。
	if firstRow["username"] != "bob" || firstRow["user_id"] != "2" || firstRow["target_id"] != "1" {
		t.Fatalf("first reactions row = %#v", firstRow)
	}
	for _, forbidden := range []string{"email", "email_lower", "password_hash"} {
		if _, leaked := firstRow[forbidden]; leaked {
			t.Fatalf("reactions row leaked %s: %#v", forbidden, firstRow)
		}
	}

	second := reactions("topic", "1")
	second.Page = &protocolv2.PageRequest{Limit: 1, Cursor: firstPage.GetPage().GetNextCursor()}
	secondPage := h.engine.execute(ctx, second)
	if secondPage.GetError() != nil || len(secondPage.GetRows()) != 1 || secondPage.GetPage().GetHasMore() {
		t.Fatalf("second reactions page = %#v", secondPage)
	}
	// 禁用账号 carol 的点赞被视图过滤，第二行必须是 alice。
	if username := secondPage.GetRows()[0].GetValue().AsMap()["username"]; username != "alice" {
		t.Fatalf("second reactions row username = %#v", username)
	}

	hiddenTopic := h.engine.execute(ctx, reactions("topic", "99"))
	if hiddenTopic.GetError() != nil || len(hiddenTopic.GetRows()) != 0 {
		t.Fatalf("hidden topic reactions = %#v", hiddenTopic)
	}
	visibleComment := h.engine.execute(ctx, reactions("comment", "1"))
	if visibleComment.GetError() != nil || len(visibleComment.GetRows()) != 1 {
		t.Fatalf("visible comment reactions = %#v", visibleComment)
	}
	hiddenComment := h.engine.execute(ctx, reactions("comment", "99"))
	if hiddenComment.GetError() != nil || len(hiddenComment.GetRows()) != 0 {
		t.Fatalf("hidden comment reactions = %#v", hiddenComment)
	}
}
