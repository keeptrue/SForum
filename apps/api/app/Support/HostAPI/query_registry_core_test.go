package hostapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

func TestQueryRegistryCoreCatalogSealsPublicationsBindingsAndSchemas(t *testing.T) {
	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	definitions := stableCoreProtocolV2QueryDefinitions()
	artifact := catalog.Artifact()
	publication := catalog.Publication()
	bindings := catalog.Bindings()
	schemas := catalog.Schemas()
	if !artifact.Core || artifact.ExtensionID != QueryRegistryCoreExtensionID {
		t.Fatalf("artifact=%#v", artifact)
	}
	if len(publication.Queries) != len(definitions) ||
		len(bindings) != len(definitions) ||
		len(schemas) != len(definitions) {
		t.Fatalf("catalog sizes queries=%d bindings=%d schemas=%d definitions=%d",
			len(publication.Queries), len(bindings), len(schemas), len(definitions))
	}
	if publication.Artifact != artifact {
		t.Fatal("publication artifact drift")
	}
	if catalog.CostPolicy() == nil {
		t.Fatal("missing cost policy")
	}
	if catalog.CostMaximum() != queryRegistryCoreDefaultCostMaximum {
		t.Fatalf("cost maximum=%d", catalog.CostMaximum())
	}

	registry := queryregistry.New(queryregistry.WithCostPolicy(catalog.CostPolicy()))
	if _, err := registry.Publish(publication); err != nil {
		t.Fatalf("publish core catalog: %v", err)
	}
	schemaCatalog, err := queryregistry.NewJSONResultSchemaCatalog(schemas)
	if err != nil {
		t.Fatalf("schema catalog: %v", err)
	}
	schemaDigestByQuery := make(map[string]string, len(schemas))
	for _, schema := range schemas {
		schemaDigestByQuery[schema.QueryID] = schema.SchemaDigest
	}
	for _, query := range publication.Queries {
		if query.ResultSchemaDigest == "" || query.ResultSchemaDigest != schemaDigestByQuery[query.ID] {
			t.Fatalf("core publication omitted bound Schema digest: %#v", query)
		}
	}

	byHost := make(map[string]protocolV2QueryDefinition, len(definitions))
	for _, definition := range definitions {
		byHost[definition.ID] = definition
	}
	expected := map[string]queryRegistryCoreMapping{
		QuerySafeUserByID: {
			HostQueryID: QuerySafeUserByID, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.safe_user.by_id", ContractVersion: "core.query.safe_user.by_id@1",
			Entity: "core.query.safe_user", PlanVersion: "core.query.safe_user.by_id.plan@1",
			ResultSchema:     QuerySafeUserResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		},
		QueryPublicTopicsList: {
			HostQueryID: QueryPublicTopicsList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_topics.list", ContractVersion: "core.query.public_topics.list@1",
			Entity: "core.query.public_topic", PlanVersion: "core.query.public_topics.list.plan@1",
			ResultSchema:     QueryPublicTopicResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		},
		QueryPublicTopicByID: {
			HostQueryID: QueryPublicTopicByID, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_topic.by_id", ContractVersion: "core.query.public_topic.by_id@1",
			Entity: "core.query.public_topic", PlanVersion: "core.query.public_topic.by_id.plan@1",
			ResultSchema:     QueryPublicTopicResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		},
		QueryPublicAttachmentByPublicID: {
			HostQueryID: QueryPublicAttachmentByPublicID, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_attachment.by_public_id", ContractVersion: "core.query.public_attachment.by_public_id@1",
			Entity: "core.query.public_attachment_metadata", PlanVersion: "core.query.public_attachment.by_public_id.plan@1",
			ResultSchema:     QueryPublicAttachmentSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		},
		QuerySiteStats: {
			HostQueryID: QuerySiteStats, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.site_stats.current", ContractVersion: "core.query.site_stats.current@1",
			Entity: "core.query.site_stats", PlanVersion: "core.query.site_stats.current.plan@1",
			ResultSchema:     QuerySiteStatsResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		},
		QueryPublicCommentsList: {
			HostQueryID: QueryPublicCommentsList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_comments.list", ContractVersion: "core.query.public_comments.list@1",
			Entity: "core.query.public_comment", PlanVersion: "core.query.public_comments.list.plan@1",
			ResultSchema:     QueryPublicCommentResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		},
		QuerySafeUserByUsername: {
			HostQueryID: QuerySafeUserByUsername, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.safe_user.by_username", ContractVersion: "core.query.safe_user.by_username@1",
			Entity: "core.query.safe_user", PlanVersion: "core.query.safe_user.by_username.plan@1",
			ResultSchema:     QuerySafeUserResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		},
		QuerySafeUsersBatch: {
			HostQueryID: QuerySafeUsersBatch, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.safe_users.batch", ContractVersion: "core.query.safe_users.batch@1",
			Entity: "core.query.safe_user_avatar", PlanVersion: "core.query.safe_users.batch.plan@1",
			ResultSchema:     QuerySafeUserAvatarSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		},
		QueryPublicCategoriesList: {
			HostQueryID: QueryPublicCategoriesList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_categories.list", ContractVersion: "core.query.public_categories.list@1",
			Entity: "core.query.public_category", PlanVersion: "core.query.public_categories.list.plan@1",
			ResultSchema:     QueryPublicCategorySchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		},
		// 同意式读取：login 策略 + 单行分页；它只经 host 签发的委派执行。
		QuerySelfEmail: {
			HostQueryID: QuerySelfEmail, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: QuerySelfEmailRegistryID, ContractVersion: QuerySelfEmailRegistryContractVersion,
			Entity: "core.query.identity.self_email", PlanVersion: QuerySelfEmailRegistryPlanVersion,
			ResultSchema:     QuerySelfEmailSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyLogin, Pagination: queryregistry.PaginationNone,
		},
	}
	for _, definition := range shortcodeProjectionProtocolV2QueryDefinitions() {
		mapping, ok := shortcodeProjectionCoreMapping(definition)
		if !ok {
			t.Fatalf("missing shortcode mapping for %s", definition.ID)
		}
		expected[definition.ID] = mapping
	}
	if len(expected) != len(definitions) {
		t.Fatalf("explicit mapping table size=%d definitions=%d", len(expected), len(definitions))
	}

	for index, binding := range bindings {
		definition, ok := byHost[binding.HostQueryID]
		if !ok || binding.HostPlanVersion != definition.PlanVersion {
			t.Fatalf("binding host identity=%#v", binding)
		}
		mapping, ok := expected[binding.HostQueryID]
		if !ok {
			t.Fatalf("missing explicit mapping for host query %q", binding.HostQueryID)
		}
		if binding.QueryID != mapping.QueryID ||
			binding.ContractVersion != mapping.ContractVersion ||
			binding.PlanVersion != mapping.PlanVersion ||
			binding.ResultSchema != mapping.ResultSchema ||
			binding.HostQueryID != mapping.HostQueryID ||
			binding.HostPlanVersion != mapping.HostPlanVersion {
			t.Fatalf("binding drifted from explicit map got=%#v want=%#v", binding, mapping)
		}
		if binding.Artifact != artifact ||
			binding.ResultSchema != definition.ResultSchemaID+"@"+definition.ResultSchemaVersion {
			t.Fatalf("binding schema/artifact=%#v", binding)
		}
		if !strings.HasPrefix(binding.QueryID, QueryRegistryCoreExtensionID+".") {
			t.Fatalf("query id namespace=%q", binding.QueryID)
		}
		resolved, resolveErr := registry.Resolve(binding.QueryID)
		if resolveErr != nil || resolved.Artifact != artifact {
			t.Fatalf("resolve %s: %#v %v", binding.QueryID, resolved, resolveErr)
		}
		if resolved.ID != mapping.QueryID ||
			resolved.Entity != mapping.Entity ||
			resolved.PermissionPolicy != mapping.PermissionPolicy ||
			resolved.Pagination != mapping.Pagination {
			t.Fatalf("resolved declaration drift %#v want %#v", resolved, mapping)
		}
		// 尚无 invalidation contract：声明与解析结果都必须保持 CacheTags 为空。
		if resolved.CacheTags != nil && len(resolved.CacheTags) != 0 {
			t.Fatalf("cache tags must stay empty: %#v", resolved.CacheTags)
		}
		if publication.Queries[index].CacheTags != nil {
			t.Fatalf("publication cache tags must be nil: %#v", publication.Queries[index].CacheTags)
		}
		if resolved.Relations != nil && len(resolved.Relations) != 0 {
			t.Fatalf("core declaration must not invent relations: %#v", resolved)
		}
		if definition.Single && resolved.Pagination != queryregistry.PaginationNone {
			t.Fatalf("single pagination=%q", resolved.Pagination)
		}
		if !definition.Single && resolved.Pagination != queryregistry.PaginationOffset {
			t.Fatalf("list pagination=%q (must be offset, never keyset/cursor)", resolved.Pagination)
		}
		if resolved.Pagination == queryregistry.PaginationCursor {
			t.Fatal("core catalog must not claim cursor/keyset pagination")
		}
		schema := schemas[index]
		if schema.QueryID != binding.QueryID || schema.SchemaDigest == "" || len(schema.Schema) == 0 {
			t.Fatalf("schema binding=%#v", schema)
		}
		claim := queryregistry.ResultSchemaClaim{
			QueryID: schema.QueryID, ContractVersion: schema.ContractVersion,
			PlanVersion: schema.PlanVersion, ResultSchema: schema.ResultSchema, Artifact: artifact,
		}
		row := queryRegistryCoreFixtureRow(t, definition)
		if err := schemaCatalog.ValidateQueryResult(context.Background(), claim, row); err != nil {
			t.Fatalf("valid host row for %s: %v row=%#v", definition.ID, err, row)
		}
		if err := registry.ValidateQueryResult(context.Background(), claim, row); err != nil {
			t.Fatalf("Registry-bound host row for %s: %v row=%#v", definition.ID, err, row)
		}
	}
}

func TestQueryRegistryCoreAccessorsCloneAgainstMutation(t *testing.T) {
	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	publication := catalog.Publication()
	if len(publication.Queries) == 0 {
		t.Fatal("empty publication")
	}
	publication.Queries[0].ID = "mutated.query"
	publication.Queries[0].Fields[0] = "mutated_field"
	publication.Queries[0].ResultSchemaDigest = strings.Repeat("f", 64)
	if catalog.Publication().Queries[0].ID == "mutated.query" {
		t.Fatal("publication accessor leaked mutable queries")
	}
	if catalog.Publication().Queries[0].Fields[0] == "mutated_field" {
		t.Fatal("publication accessor leaked mutable fields")
	}
	if catalog.Publication().Queries[0].ResultSchemaDigest == strings.Repeat("f", 64) {
		t.Fatal("publication accessor leaked mutable Schema metadata")
	}

	bindings := catalog.Bindings()
	bindings[0].QueryID = "mutated.binding"
	if catalog.Bindings()[0].QueryID == "mutated.binding" {
		t.Fatal("bindings accessor leaked mutable slice")
	}

	schemas := catalog.Schemas()
	if len(schemas[0].Schema) == 0 {
		t.Fatal("empty schema body")
	}
	originalByte := schemas[0].Schema[0]
	schemas[0].Schema[0] ^= 0xff
	schemas[0].QueryID = "mutated.schema"
	restored := catalog.Schemas()
	if restored[0].QueryID == "mutated.schema" {
		t.Fatal("schemas accessor leaked mutable binding")
	}
	if restored[0].Schema[0] != originalByte {
		t.Fatal("schema body mutation leaked into catalog")
	}
}

func TestQueryRegistryCoreCostPolicyConservativeLimitsAndRejectsRelations(t *testing.T) {
	policy := NewQueryRegistryCoreCostPolicy()
	base, err := policy.EstimateQueryCost(queryregistry.QueryCostInput{
		Fields: []string{"id", "title"}, Filters: []queryregistry.FilterValue{{Field: "category_id", Value: "1"}},
		Sorts: []queryregistry.SortValue{{Field: "last_activity_at"}}, Pagination: queryregistry.PaginationPlan{Limit: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedUnits := queryRegistryCoreCostBase + 2*queryRegistryCoreCostPerField +
		queryRegistryCoreCostPerFilter + queryRegistryCoreCostPerSort + 20*queryRegistryCoreCostPerPageRow
	if base.Units != expectedUnits || base.Maximum != queryRegistryCoreDefaultCostMaximum {
		t.Fatalf("cost=%#v expectedUnits=%d", base, expectedUnits)
	}

	// caller 只能降低 site maximum，不得抬高。
	capped, err := policy.EstimateQueryCost(queryregistry.QueryCostInput{
		Fields: []string{"id"}, Pagination: queryregistry.PaginationPlan{Limit: 1}, RequestedMaximum: 12,
	})
	if err != nil || capped.Maximum != 12 {
		t.Fatalf("requested maximum=%#v err=%v", capped, err)
	}
	raised, err := policy.EstimateQueryCost(queryregistry.QueryCostInput{
		Fields: []string{"id"}, Pagination: queryregistry.PaginationPlan{Limit: 1},
		RequestedMaximum: queryRegistryCoreDefaultCostMaximum + 100,
	})
	if err != nil || raised.Maximum != queryRegistryCoreDefaultCostMaximum {
		t.Fatalf("caller raised maximum=%#v err=%v", raised, err)
	}
	if _, err := policy.EstimateQueryCost(queryregistry.QueryCostInput{
		Fields: []string{"id"}, Relations: []string{"owner"}, Pagination: queryregistry.PaginationPlan{Limit: 1},
	}); !errors.Is(err, queryregistry.ErrContractInsufficient) {
		t.Fatalf("relations error=%v", err)
	}

	// 同一输入必须确定性返回相同结果。
	again, err := policy.EstimateQueryCost(queryregistry.QueryCostInput{
		Fields: []string{"id", "title"}, Filters: []queryregistry.FilterValue{{Field: "category_id", Value: "1"}},
		Sorts: []queryregistry.SortValue{{Field: "last_activity_at"}}, Pagination: queryregistry.PaginationPlan{Limit: 20},
	})
	if err != nil || again != base {
		t.Fatalf("nondeterministic cost first=%#v second=%#v err=%v", base, again, err)
	}
}

func TestQueryRegistryCoreCostMaximumBounds(t *testing.T) {
	if _, err := newQueryRegistryCoreCatalog(queryRegistryCoreAbsoluteCostMaximum + 1); !errors.Is(err, ErrQueryRegistryCoreInvalid) {
		t.Fatalf("above hard max=%v", err)
	}
	if _, err := newQueryRegistryCoreCatalog(-1); !errors.Is(err, ErrQueryRegistryCoreInvalid) {
		t.Fatalf("negative max=%v", err)
	}
	lowered, err := newQueryRegistryCoreCatalog(120)
	if err != nil || lowered.CostMaximum() != 120 {
		t.Fatalf("lowered site max=%#v err=%v", lowered, err)
	}
	// site maximum 变化必须进入 artifact package digest。
	defaultCatalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if lowered.Artifact().PackageDigest == defaultCatalog.Artifact().PackageDigest {
		t.Fatal("cost maximum must affect canonical package digest")
	}
	policy := lowered.CostPolicy()
	cost, err := policy.EstimateQueryCost(queryregistry.QueryCostInput{
		Fields: []string{"id"}, Pagination: queryregistry.PaginationPlan{Limit: 1}, RequestedMaximum: 200,
	})
	if err != nil || cost.Maximum != 120 {
		t.Fatalf("site max must dominate higher caller request=%#v err=%v", cost, err)
	}
}

func TestQueryRegistryCoreCursorRequiresCallerSecretAndUsesHMACCodec(t *testing.T) {
	if _, err := NewQueryRegistryCoreCursorCodec(nil); !errors.Is(err, ErrQueryRegistryCoreInvalid) {
		t.Fatalf("nil secret=%v", err)
	}
	if _, err := NewQueryRegistryCoreCursorCodec([]byte("short")); !errors.Is(err, ErrQueryRegistryCoreInvalid) {
		t.Fatalf("short secret=%v", err)
	}
	secret := []byte(strings.Repeat("core-query-cursor-secret-", 2))
	codec, err := NewQueryRegistryCoreCursorCodec(secret)
	if err != nil {
		t.Fatal(err)
	}
	claims := queryregistry.CursorClaims{
		SchemaVersion: "sforum.query-cursor@2",
		QueryID:       "core.query.public_topics.list", ContractVersion: "core.query.public_topics.list@1",
		PlanVersion: "core.query.public_topics.list.plan@1", ResultSchema: QueryPublicTopicResultSchemaID + "@1",
		ShapeDigest: strings.Repeat("a", 64), RegistryRevision: 1, RegistryDigest: strings.Repeat("b", 64),
		ArtifactDigest: strings.Repeat("c", 64), IsolationDigest: strings.Repeat("d", 64),
		ExecutionDigest: strings.Repeat("e", 64), Offset: 20, Limit: 20,
	}
	encoded, err := codec.EncodeQueryCursor(claims)
	if err != nil || encoded == "" {
		t.Fatalf("encode=%q err=%v", encoded, err)
	}
	decoded, err := codec.DecodeQueryCursor(encoded)
	if err != nil || decoded != claims {
		t.Fatalf("decode=%#v err=%v", decoded, err)
	}
	// 调用方提供的 secret 不得被本包改写或持久化到其它通道；仅验证编解码闭环。
	if _, err := codec.DecodeQueryCursor(encoded + "x"); !errors.Is(err, queryregistry.ErrCursorInvalid) {
		t.Fatalf("tampered cursor=%v", err)
	}
}

func TestQueryRegistryCoreRegistryPlansStableQueriesWithCostPolicy(t *testing.T) {
	secret := []byte(strings.Repeat("0123456789abcdef", 2))
	registry, catalog, err := NewQueryRegistryCoreRegistry(QueryRegistryCoreOptions{CursorSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if catalog == nil || registry.Revision() == 0 {
		t.Fatalf("registry revision=%d catalog=%v", registry.Revision(), catalog != nil)
	}

	listID := "core.query.public_topics.list"
	plan, err := registry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID: listID, Fields: []string{"id", "title"},
		Filters:    []queryregistry.FilterValue{{Field: "category_id", Value: "1"}},
		Sorts:      []queryregistry.SortValue{{Field: "last_activity_at", Descending: true}},
		Pagination: queryregistry.PaginationRequest{Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Query.ID != listID || plan.Pagination.Mode != queryregistry.PaginationOffset ||
		plan.Cost.Maximum != queryRegistryCoreDefaultCostMaximum || plan.Cost.Units <= 0 {
		t.Fatalf("plan=%#v", plan)
	}
	if len(plan.Relations) != 0 {
		t.Fatalf("plan relations=%#v", plan.Relations)
	}
	if plan.Query.PermissionPolicy != queryregistry.PermissionPolicyPublic {
		t.Fatalf("permission=%q", plan.Query.PermissionPolicy)
	}

	// 无 secret 时 cursor codec 缺席，cursor 模式保持 fail-closed。
	registryNoCursor, _, err := NewQueryRegistryCoreRegistry(QueryRegistryCoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// 当前 Core 列表声明为 offset；对 cursor 请求直接 invalid。
	if _, err := registryNoCursor.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID: listID, Pagination: queryregistry.PaginationRequest{Limit: 2, Cursor: "not-a-cursor"},
	}); err == nil {
		t.Fatal("offset query accepted cursor payload")
	}

	singleID := "core.query.safe_user.by_id"
	single, err := registry.Plan(context.Background(), queryregistry.PlanRequest{
		QueryID: singleID, Fields: []string{"id", "username"},
		Filters: []queryregistry.FilterValue{{Field: "id", Value: "1"}},
	})
	if err != nil || single.Pagination.Mode != queryregistry.PaginationNone {
		t.Fatalf("single plan=%#v err=%v", single, err)
	}
}

func TestQueryRegistryCoreBindingsCompatibleWithProtocolV2ProviderResolver(t *testing.T) {
	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	executor := protocolV2QueryExecutorFunc(func(_ context.Context, plan protocolV2QueryPlan) ([]map[string]any, error) {
		row := map[string]any{"id": "1", "title": "one"}
		if plan.Definition.Single {
			return []map[string]any{row}, nil
		}
		return []map[string]any{row, {"id": "2", "title": "two"}}, nil
	})
	runtime, err := newProtocolV2QueryRuntime(executor, allowedQueryAuthority(), stableCoreProtocolV2QueryDefinitions()...)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewProtocolV2QueryRegistryProviderResolver(runtime, catalog.Bindings())
	if err != nil {
		t.Fatal(err)
	}
	registry := queryregistry.New(queryregistry.WithCostPolicy(catalog.CostPolicy()))
	if _, err := registry.Publish(catalog.Publication()); err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers,
		Schemas: mustQueryRegistryCoreSchemaCatalog(t, catalog),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := execution.Execute(context.Background(), queryregistry.PlanRequest{
		QueryID: "core.query.public_topics.list",
		Fields:  []string{"id", "title"}, Pagination: queryregistry.PaginationRequest{Limit: 1},
	})
	if err != nil || len(result.Rows) != 1 || result.Rows[0]["id"] != "1" {
		t.Fatalf("execute=%#v err=%v", result, err)
	}
}

func TestQueryRegistryCoreUnsupportedDefinitionsFailClosed(t *testing.T) {
	base := stableCoreProtocolV2QueryDefinitions()[0]
	cases := map[string]protocolV2QueryDefinition{
		"foreign host id": func() protocolV2QueryDefinition {
			item := cloneProtocolV2QueryDefinition(base)
			item.ID = "plugin.other.query"
			return item
		}(),
		"non-eq filter": func() protocolV2QueryDefinition {
			item := cloneProtocolV2QueryDefinition(base)
			item.Filters[0].Operator = "gt"
			return item
		}(),
		"unknown result schema": func() protocolV2QueryDefinition {
			item := cloneProtocolV2QueryDefinition(base)
			item.ResultSchemaID = "sforum.core.unknown_entity"
			return item
		}(),
		"single with sorts": func() protocolV2QueryDefinition {
			item := cloneProtocolV2QueryDefinition(base)
			item.Single = true
			item.Sorts = []protocolV2QuerySortDefinition{{Field: "id", Expression: "stable.id"}}
			return item
		}(),
		"schema field removed": func() protocolV2QueryDefinition {
			item := cloneProtocolV2QueryDefinition(base)
			item.Fields = item.Fields[:len(item.Fields)-1]
			return item
		}(),
		"schema field added": func() protocolV2QueryDefinition {
			item := cloneProtocolV2QueryDefinition(base)
			item.Fields = append(item.Fields, protocolV2QueryField{Name: "undeclared", Expression: "stable.undeclared"})
			return item
		}(),
	}
	for name, definition := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := mapStableCoreDefinitionToQueryRegistry(definition); !errors.Is(err, ErrQueryRegistryCoreUnsupported) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestQueryRegistryCoreCatalogIsDeterministic(t *testing.T) {
	first, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if first.Artifact() != second.Artifact() {
		t.Fatalf("artifact drift first=%#v second=%#v", first.Artifact(), second.Artifact())
	}
	firstBindings := first.Bindings()
	secondBindings := second.Bindings()
	if len(firstBindings) != len(secondBindings) {
		t.Fatal("binding count drift")
	}
	firstSchemas := first.Schemas()
	secondSchemas := second.Schemas()
	for index := range firstBindings {
		if firstBindings[index] != secondBindings[index] {
			t.Fatalf("binding[%d] drift", index)
		}
		if firstSchemas[index].SchemaDigest != secondSchemas[index].SchemaDigest {
			t.Fatalf("schema digest[%d] drift", index)
		}
	}
	if first.CostMaximum() != second.CostMaximum() {
		t.Fatal("cost maximum drift")
	}
}

func queryRegistryCoreFixtureRow(t *testing.T, definition protocolV2QueryDefinition) queryregistry.QueryRow {
	t.Helper()
	row := queryregistry.QueryRow{}
	for _, field := range definition.Fields {
		switch field.Name {
		case "is_pinned", "owning_topic_public", "authenticated", "is_resource_author", "is_topic_author", "eligible",
			"email_verified":
			row[field.Name] = false
		case "image_width", "image_height", "avatar_image_width", "avatar_image_height":
			row[field.Name] = nil
		case "reference_count", "position", "depth":
			row[field.Name] = float64(1)
		case "author_user_id", "owner_user_id", "topic_id", "resource_id", "user_id":
			row[field.Name] = "1"
		default:
			// BIGINT/text/timestamp 在 Host 归一化后均为 string。
			row[field.Name] = "value"
		}
	}
	// id 字段使用规范数字字符串，贴近真实 Host 输出。
	if _, ok := row["id"]; ok {
		row["id"] = "1"
	}
	return row
}

func mustQueryRegistryCoreSchemaCatalog(t *testing.T, catalog *QueryRegistryCoreCatalog) *queryregistry.JSONResultSchemaCatalog {
	t.Helper()
	schemas, err := queryregistry.NewJSONResultSchemaCatalog(catalog.Schemas())
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}
