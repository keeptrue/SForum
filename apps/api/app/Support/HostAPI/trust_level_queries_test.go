package hostapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 本文件是两条信任等级 HostQuery 的 allowed / denied 判据。
//
// 核心不是「能读」，是「计算形态没有走出自己的第二条路」：投影、归一化、分页、
// 过滤条件校验、值类型拒绝全部由引擎/执行器那份共享实现做。所以下面每条断言
// 都对照一条 SQL 查询的同一行为，而不是自己定义一套。

// fakeTrustLevelSource 是计算读面的替身，记录被问到的 user_id。
type fakeTrustLevelSource struct {
	catalogRows  []map[string]any
	userRows     []map[string]any
	userRowsErr  error
	askedUserIDs []int64
}

func (f *fakeTrustLevelSource) TrustLevelCatalog(context.Context) ([]map[string]any, error) {
	return f.catalogRows, nil
}

func (f *fakeTrustLevelSource) UserTrustLevel(_ context.Context, userID int64) ([]map[string]any, error) {
	f.askedUserIDs = append(f.askedUserIDs, userID)
	// 收严：真实实现里 incomplete / disabled 的 level 与 tier_id 恒为 nil。
	// 若 fake 允许摆出「state=incomplete 且 level=3」这种真实源永远产不出的行，
	// 用它写的用例就会给下一个人假信号（task-34 正是 fake 比真实源宽松的后果）。
	for _, row := range f.userRows {
		state, _ := row["state"].(string)
		if state != "" && state != "resolved" && (row["level"] != nil || row["tier_id"] != nil) {
			return nil, errors.New("fake trust level source: state=" + state + " 必须 level/tier_id 为 nil（与真实源口径不一致）")
		}
	}
	return f.userRows, f.userRowsErr
}

func stableQueryDefinition(t *testing.T, queryID string) protocolV2QueryDefinition {
	t.Helper()
	for _, definition := range stableCoreProtocolV2QueryDefinitions() {
		if definition.ID == queryID {
			return definition
		}
	}
	t.Fatalf("%s 没有进 stable 定义表", queryID)
	return protocolV2QueryDefinition{}
}

func catalogRow(rank int64, tierID string) map[string]any {
	return map[string]any{
		"tier_id": tierID, "rank": rank, "name_zh": "档", "name_en": "Tier",
		"icon": "leaf", "color": "#123456",
		"days_visited": int64(0), "topic_count": int64(0), "comment_count": int64(0),
		"like_received_count": int64(0), "like_given_count": int64(0), "bookmark_count": int64(0),
	}
}

// TestPublicTrustLevelCatalogQueryProjectsAndNormalizes 是 allowed 侧：
// 目录查询匿名可读，行被按 Fields 投影、int64 被规范成 decimal string（与 SQL 侧同一函数）。
func TestPublicTrustLevelCatalogQueryProjectsAndNormalizes(t *testing.T) {
	source := &fakeTrustLevelSource{catalogRows: []map[string]any{
		catalogRow(1, "member"), catalogRow(2, "resident"), catalogRow(3, "veteran"),
	}}
	executor := &postgresProtocolV2QueryExecutor{trustLevels: source}
	definition := stableQueryDefinition(t, QueryPublicTrustLevelCatalogList)
	plan, detail := buildProtocolV2QueryPlan(definition, &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicTrustLevelCatalogList,
		PlanVersion:    QueryStableCorePlanVersion,
		ResultSchemaId: QueryPublicTrustLevelCatalogSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
	})
	if detail != nil {
		t.Fatalf("allowed 请求被拒：%v", detail)
	}
	rows, err := executor.ExecuteProtocolV2Query(context.Background(), plan)
	if err != nil {
		t.Fatalf("catalog query: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if got := rows[0]["rank"]; got != "1" {
		t.Fatalf("rank = %#v, want \"1\"（int64 必须走 SQL 侧同一个 normalize）", got)
	}
	if _, exists := rows[0]["tier_id"]; !exists {
		t.Fatalf("行缺少 tier_id：%#v", rows[0])
	}
	// 公开面结构上不含 rule / limits：Fields 里没有，行里也不许冒出来。
	for _, field := range plan.Fields {
		if field.Name == "rule" || field.Name == "limits" {
			t.Fatalf("公开目录出现了 %q —— rule/limits 不进公开面", field.Name)
		}
	}
}

// TestComputedQueryPaginatesThroughSharedPlan 钉住「分页真生效」：
// Offset/FetchLimit 与 SQL 的 LIMIT/OFFSET 同义，计算形态也必须真截断。
func TestComputedQueryPaginatesThroughSharedPlan(t *testing.T) {
	source := &fakeTrustLevelSource{catalogRows: []map[string]any{
		catalogRow(1, "member"), catalogRow(2, "resident"),
		catalogRow(3, "veteran"), catalogRow(4, "leader"),
	}}
	executor := &postgresProtocolV2QueryExecutor{trustLevels: source}
	definition := stableQueryDefinition(t, QueryPublicTrustLevelCatalogList)
	plan := protocolV2QueryPlan{
		Definition: definition, Fields: definition.Fields,
		FetchLimit: 2, Limit: 2, Offset: 2,
	}
	rows, err := executor.ExecuteProtocolV2Query(context.Background(), plan)
	if err != nil {
		t.Fatalf("catalog query: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2（FetchLimit 必须真截断）", len(rows))
	}
	if got := rows[0]["rank"]; got != "3" {
		t.Fatalf("offset 2 的首行 rank = %#v, want \"3\"（Offset 必须真生效）", got)
	}
}

// TestPublicUserTrustLevelQueryReadsRequiredFilter 是 allowed 侧的单用户面：
// user_id 过滤条件被真的传进读面，结果按 Fields 投影。
func TestPublicUserTrustLevelQueryReadsRequiredFilter(t *testing.T) {
	source := &fakeTrustLevelSource{userRows: []map[string]any{{
		"user_id": int64(42), "level": int64(1), "tier_id": "member", "state": "resolved",
	}}}
	executor := &postgresProtocolV2QueryExecutor{trustLevels: source}
	definition := stableQueryDefinition(t, QueryPublicUserTrustLevelGet)
	plan, detail := buildProtocolV2QueryPlan(definition, &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicUserTrustLevelGet,
		PlanVersion:    QueryStableCorePlanVersion,
		ResultSchemaId: QueryPublicTrustLevelSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
		Filters: []*hostv2.QueryFilter{
			{Field: "user_id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, "42")},
		},
	})
	if detail != nil {
		t.Fatalf("allowed 请求被拒：%v", detail)
	}
	rows, err := executor.ExecuteProtocolV2Query(context.Background(), plan)
	if err != nil {
		t.Fatalf("user trust level query: %v", err)
	}
	if len(source.askedUserIDs) != 1 || source.askedUserIDs[0] != 42 {
		t.Fatalf("读面收到的 user_id = %v, want [42]", source.askedUserIDs)
	}
	if len(rows) != 1 || rows[0]["level"] != "1" {
		t.Fatalf("rows = %#v", rows)
	}
}

// TestPublicUserTrustLevelQueryKeepsIncompleteNullable 钉住「算不出来」不等于 0。
//
// §4.3 的两个「不发」只约束事件；读面遇到 incomplete 是新问题：这里用 state
// 表达，level/tier_id 为 null。谁把它改成 0，这条就红。
func TestPublicUserTrustLevelQueryKeepsIncompleteNullable(t *testing.T) {
	source := &fakeTrustLevelSource{userRows: []map[string]any{{
		"user_id": int64(7), "level": nil, "tier_id": nil, "state": "incomplete",
	}}}
	executor := &postgresProtocolV2QueryExecutor{trustLevels: source}
	definition := stableQueryDefinition(t, QueryPublicUserTrustLevelGet)
	plan := protocolV2QueryPlan{
		Definition: definition, Fields: definition.Fields, FetchLimit: 1,
		Filters: []protocolV2QueryFilter{{
			Definition: definition.Filters[0], Value: int64(7),
		}},
	}
	rows, err := executor.ExecuteProtocolV2Query(context.Background(), plan)
	if err != nil {
		t.Fatalf("user trust level query: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0]["level"] != nil {
		t.Fatalf("incomplete 时 level = %#v，必须为 null（0 会与「等级是 0」混淆）", rows[0]["level"])
	}
	if rows[0]["state"] != "incomplete" {
		t.Fatalf("state = %#v", rows[0]["state"])
	}
}

// TestComputedQueryRejectsUnknownFilterLikeSQLQuery 是 denied 侧的核心：
// 非法过滤条件在**计划阶段**就被同一条实现拒掉，计算形态没有自己的错误码。
func TestComputedQueryRejectsUnknownFilterLikeSQLQuery(t *testing.T) {
	computed := stableQueryDefinition(t, QueryPublicUserTrustLevelGet)
	sqlBacked := stableQueryDefinition(t, QueryPublicUserActivityList)
	request := func(queryID string) *hostv2.QueryRequest {
		return &hostv2.QueryRequest{
			Context: testProtocolV2RequestContext(), QueryId: queryID,
			PlanVersion: QueryStableCorePlanVersion,
			Filters: []*hostv2.QueryFilter{{
				Field: "no_such_field", Operator: "eq",
				Value: queryParameter(t, QueryInt64ParameterSchemaID, "1"),
			}},
		}
	}
	_, computedDetail := buildProtocolV2QueryPlan(computed, request(computed.ID))
	_, sqlDetail := buildProtocolV2QueryPlan(sqlBacked, request(sqlBacked.ID))
	if computedDetail == nil || sqlDetail == nil {
		t.Fatalf("非法过滤条件必须在计划阶段被拒：computed=%v sql=%v", computedDetail, sqlDetail)
	}
	if computedDetail.GetCode() != sqlDetail.GetCode() || computedDetail.GetReason() != sqlDetail.GetReason() {
		t.Fatalf("计算形态与 SQL 形态的错误不一致：computed=(%s,%s) sql=(%s,%s)",
			computedDetail.GetCode(), computedDetail.GetReason(),
			sqlDetail.GetCode(), sqlDetail.GetReason())
	}
}

// TestComputedQueryRejectsMissingRequiredFilter 是另一条 denied：单用户面没有
// user_id 就是全站探测，必须与 SQL 查询同一条 RequiredFilters 规则拒绝。
func TestComputedQueryRejectsMissingRequiredFilter(t *testing.T) {
	definition := stableQueryDefinition(t, QueryPublicUserTrustLevelGet)
	_, detail := buildProtocolV2QueryPlan(definition, &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicUserTrustLevelGet,
		PlanVersion: QueryStableCorePlanVersion,
	})
	if detail == nil {
		t.Fatal("缺少 user_id 必须被拒（否则等于全站枚举等级）")
	}
	if detail.GetCode() != protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("code = %s", detail.GetCode())
	}
}

// TestComputedQueryRejectsUnsupportedValueType 钉住「输出行过 schema 校验」：
// 计算函数返回一个 normalize 认不出的类型时必须拒，不许透传。
func TestComputedQueryRejectsUnsupportedValueType(t *testing.T) {
	bad := catalogRow(1, "member")
	bad["rank"] = []string{"1", "2"}
	source := &fakeTrustLevelSource{catalogRows: []map[string]any{bad}}
	executor := &postgresProtocolV2QueryExecutor{trustLevels: source}
	definition := stableQueryDefinition(t, QueryPublicTrustLevelCatalogList)
	_, err := executor.ExecuteProtocolV2Query(context.Background(), protocolV2QueryPlan{
		Definition: definition, Fields: definition.Fields, FetchLimit: 1,
	})
	if err == nil {
		t.Fatal("不支持的字段类型必须报错，不能透传给调用方")
	}
	if !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("错误信息应来自共享的 normalize：%v", err)
	}
}

// TestComputedQueryFailsLoudlyWithoutSource 钉住装配缺失不是静默空表。
func TestComputedQueryFailsLoudlyWithoutSource(t *testing.T) {
	executor := &postgresProtocolV2QueryExecutor{}
	definition := stableQueryDefinition(t, QueryPublicTrustLevelCatalogList)
	if _, err := executor.ExecuteProtocolV2Query(context.Background(), protocolV2QueryPlan{
		Definition: definition, Fields: definition.Fields, FetchLimit: 1,
	}); err == nil {
		t.Fatal("没有注入读面时必须报错（注册了但没装配，静默空表会把故障藏起来）")
	}
}
