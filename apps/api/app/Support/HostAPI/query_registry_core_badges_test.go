package hostapi

import (
	"strings"
	"testing"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

// TestBadgeQueriesAreStablePublicReads 固定三个徽章读面的稳定契约。
//
// AGENTS.md 要求新核心数据域「同变更交付稳定视图 + 至少一个稳定查询」。
// 这里逐条证明：ID 稳定、schema 版本对齐、权限策略是 public、分页是 offset
// （core 面不许声明 cursor），且 schema 里没有审计/未采集字段。
func TestBadgeQueriesAreStablePublicReads(t *testing.T) {
	queries := []struct {
		hostID     string
		schemaID   string
		queryID    string
		entity     string
		planSuffix string
	}{
		{QueryPublicBadgesList, QueryPublicBadgeSchemaID, "core.query.public_badges.list", "core.query.public_badge", "public_badges.list"},
		{QueryPublicUserBadgesList, QueryPublicUserBadgeSchemaID, "core.query.public_user_badges.list", "core.query.public_user_badge", "public_user_badges.list"},
		{QueryPublicUserActivityList, QueryPublicUserActivitySchemaID, "core.query.public_user_activity.list", "core.query.public_user_activity", "public_user_activity.list"},
	}

	definitions := map[string]protocolV2QueryDefinition{}
	for _, definition := range stableCoreProtocolV2QueryDefinitions() {
		definitions[definition.ID] = definition
	}

	for _, query := range queries {
		if !isStableProtocolV2QueryID(query.hostID) {
			t.Fatalf("%s must be a stable protocol v2 query id", query.hostID)
		}
		definition, ok := definitions[query.hostID]
		if !ok {
			t.Fatalf("%s missing from core query definitions", query.hostID)
		}
		if definition.ResultSchemaID != query.schemaID {
			t.Fatalf("%s schema = %q, want %q", query.hostID, definition.ResultSchemaID, query.schemaID)
		}
		if definition.PlanVersion != QueryStableCorePlanVersion {
			t.Fatalf("%s plan version = %q", query.hostID, definition.PlanVersion)
		}
		if !strings.HasPrefix(definition.From, "sforum_core_v1.") {
			t.Fatalf("%s must read from the stable view schema, got %q", query.hostID, definition.From)
		}

		mapping, err := queryRegistryCoreMappingFor(definition)
		if err != nil {
			t.Fatalf("%s has no explicit registry mapping: %v", query.hostID, err)
		}
		if mapping.QueryID != query.queryID || mapping.Entity != query.entity {
			t.Fatalf("%s mapping = %q/%q", query.hostID, mapping.QueryID, mapping.Entity)
		}
		if mapping.PermissionPolicy != queryregistry.PermissionPolicyPublic {
			t.Fatalf("%s permission policy = %q, want public", query.hostID, mapping.PermissionPolicy)
		}
		if mapping.Pagination != queryregistry.PaginationOffset {
			t.Fatalf("%s pagination = %q, want offset", query.hostID, mapping.Pagination)
		}
		if !strings.Contains(mapping.PlanVersion, query.planSuffix) {
			t.Fatalf("%s plan version %q must mention %q", query.hostID, mapping.PlanVersion, query.planSuffix)
		}
	}
}

// TestBadgeQuerySchemasExcludeAuditAndUncollectedFields 固定「公开面不泄露什么」。
//
// granted_by / grant_reason 是审计数据；posts_read_count / read_time_seconds
// 是 v1 未采集的阅读指标。四者都不得出现在稳定查询的 schema 里。
func TestBadgeQuerySchemasExcludeAuditAndUncollectedFields(t *testing.T) {
	forbidden := []string{"granted_by", "grant_reason", "posts_read_count", "read_time_seconds", "email", "email_lower"}
	for _, definition := range stableCoreProtocolV2QueryDefinitions() {
		switch definition.ResultSchemaID {
		case QueryPublicBadgeSchemaID, QueryPublicUserBadgeSchemaID, QueryPublicUserActivitySchemaID:
		default:
			continue
		}
		for _, field := range definition.Fields {
			for _, bad := range forbidden {
				if field.Name == bad {
					t.Fatalf("%s exposes forbidden field %q", definition.ID, bad)
				}
			}
		}
	}
}

// TestBadgeUserQueriesRequireUserFilter 断言「按用户读」的两条查询必须有必填过滤：
// 没有 user_id 就是全站枚举，对公开读面来说是不能接受的放大面。
func TestBadgeUserQueriesRequireUserFilter(t *testing.T) {
	for _, definition := range stableCoreProtocolV2QueryDefinitions() {
		switch definition.ID {
		case QueryPublicUserBadgesList, QueryPublicUserActivityList:
			if len(definition.RequiredFilters) != 1 || definition.RequiredFilters[0] != "user_id" {
				t.Fatalf("%s required filters = %v, want [user_id]", definition.ID, definition.RequiredFilters)
			}
		case QueryPublicBadgesList:
			// 目录是公开全量，不需要必填过滤。
			if len(definition.RequiredFilters) != 0 {
				t.Fatalf("%s must not require filters, got %v", definition.ID, definition.RequiredFilters)
			}
		}
	}
}
