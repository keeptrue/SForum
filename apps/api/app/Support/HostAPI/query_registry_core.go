package hostapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

const (
	// QueryRegistryCoreExtensionID is the sealed Host publication owner for
	// stable Protocol V2 queries projected into Query Registry.
	QueryRegistryCoreExtensionID      = "core.query"
	QueryRegistryCoreExtensionVersion = "1.0.0"
	queryRegistryCoreCatalogSchema    = "sforum.query-registry.core-catalog@1"

	// 保守成本权重：对齐 Protocol V2 的 limit/sort 边界，避免开放产品模型前放宽预算。
	queryRegistryCoreCostBase            = 10
	queryRegistryCoreCostPerField        = 1
	queryRegistryCoreCostPerFilter       = 3
	queryRegistryCoreCostPerSort         = 2
	queryRegistryCoreCostPerPageRow      = 1
	queryRegistryCoreDefaultCostMaximum  = 500
	queryRegistryCoreAbsoluteCostMaximum = 2_000
)

var (
	// ErrQueryRegistryCoreUnsupported 表示某条 stable Host Query 无法在不发明
	// Manifest 字段或 relation 支持的前提下投影进 Query Registry。
	ErrQueryRegistryCoreUnsupported = errors.New("hostapi: core Query Registry definition is unsupported")
	// ErrQueryRegistryCoreInvalid 表示目录构造输入（例如 cursor secret）无效。
	ErrQueryRegistryCoreInvalid = errors.New("hostapi: core Query Registry catalog is invalid")
)

// QueryRegistryCoreCatalog is the Host-owned production projection of the
// stable Protocol V2 query definitions into Query Registry primitives.
type QueryRegistryCoreCatalog struct {
	artifact    queryregistry.Artifact
	publication queryregistry.Publication
	bindings    []QueryRegistryProtocolV2Binding
	schemas     []queryregistry.JSONResultSchemaBinding
	costPolicy  queryregistry.CostPolicy
	costMaximum int
}

// QueryRegistryCoreOptions configures catalog construction. Cursor secret is
// never generated or persisted here; callers supply it from Host config.
type QueryRegistryCoreOptions struct {
	// CursorSecret is optional HMAC key material for cursor-capable registries.
	// When empty, NewQueryRegistryCoreRegistry omits the cursor codec and
	// cursor pagination stays fail-closed.
	CursorSecret []byte
	// CostMaximum is the site-owned plan budget. Zero selects the recommended
	// default; positive values may not exceed the Host hard maximum.
	CostMaximum int
}

// Artifact returns the exact sealed Core catalog identity.
func (c *QueryRegistryCoreCatalog) Artifact() queryregistry.Artifact {
	if c == nil {
		return queryregistry.Artifact{}
	}
	return c.artifact
}

// Publication returns a detached copy so callers cannot mutate the catalog
// after its artifact digest has been sealed.
func (c *QueryRegistryCoreCatalog) Publication() queryregistry.Publication {
	if c == nil {
		return queryregistry.Publication{}
	}
	return cloneQueryRegistryCorePublication(c.publication)
}

// Bindings returns a detached Protocol V2 provider mapping snapshot.
func (c *QueryRegistryCoreCatalog) Bindings() []QueryRegistryProtocolV2Binding {
	if c == nil {
		return nil
	}
	return append([]QueryRegistryProtocolV2Binding(nil), c.bindings...)
}

// Schemas returns detached bindings, including copies of every schema body.
func (c *QueryRegistryCoreCatalog) Schemas() []queryregistry.JSONResultSchemaBinding {
	if c == nil {
		return nil
	}
	return cloneQueryRegistryCoreSchemas(c.schemas)
}

// CostPolicy returns the immutable Host-owned policy sealed by this catalog.
func (c *QueryRegistryCoreCatalog) CostPolicy() queryregistry.CostPolicy {
	if c == nil {
		return nil
	}
	return c.costPolicy
}

// CostMaximum returns the normalized site budget sealed by this catalog.
func (c *QueryRegistryCoreCatalog) CostMaximum() int {
	if c == nil {
		return 0
	}
	return c.costMaximum
}

// NewQueryRegistryCoreCatalog builds sealed Core publications, explicit
// Protocol V2 bindings, JSON result schemas, and the deterministic cost policy
// from stableCoreProtocolV2QueryDefinitions. It does not publish or execute.
func NewQueryRegistryCoreCatalog() (*QueryRegistryCoreCatalog, error) {
	return newQueryRegistryCoreCatalog(0)
}

func newQueryRegistryCoreCatalog(requestedCostMaximum int) (*QueryRegistryCoreCatalog, error) {
	definitions := stableCoreProtocolV2QueryDefinitions()
	if len(definitions) == 0 {
		return nil, fmt.Errorf("%w: empty stable Protocol V2 query catalog", ErrQueryRegistryCoreUnsupported)
	}
	costMaximum, err := normalizeQueryRegistryCoreCostMaximum(requestedCostMaximum)
	if err != nil {
		return nil, err
	}
	entries := make([]queryRegistryCoreMappedEntry, 0, len(definitions))
	seenHost := make(map[string]struct{}, len(definitions))
	seenQuery := make(map[string]struct{}, len(definitions))

	for _, definition := range definitions {
		entry, mapErr := mapStableCoreDefinitionToQueryRegistry(definition)
		if mapErr != nil {
			return nil, mapErr
		}
		hostKey := entry.Binding.HostQueryID + "\x00" + entry.Binding.HostPlanVersion
		if _, exists := seenHost[hostKey]; exists {
			return nil, fmt.Errorf("%w: duplicate Host query %s@%s", ErrQueryRegistryCoreUnsupported, definition.ID, definition.PlanVersion)
		}
		if _, exists := seenQuery[entry.Declaration.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate Query Registry id %s", ErrQueryRegistryCoreUnsupported, entry.Declaration.ID)
		}
		seenHost[hostKey] = struct{}{}
		seenQuery[entry.Declaration.ID] = struct{}{}
		entries = append(entries, entry)
	}

	packageDigest, err := queryRegistryCorePackageDigest(entries, costMaximum)
	if err != nil {
		return nil, err
	}
	artifact, err := queryregistry.NewCoreArtifact(
		QueryRegistryCoreExtensionID, QueryRegistryCoreExtensionVersion, packageDigest,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: seal core query artifact: %v", ErrQueryRegistryCoreInvalid, err)
	}

	publication := queryregistry.Publication{Artifact: artifact}
	bindings := make([]QueryRegistryProtocolV2Binding, 0, len(entries))
	schemas := make([]queryregistry.JSONResultSchemaBinding, 0, len(entries))
	for _, entry := range entries {
		entry.Binding.Artifact = artifact
		entry.Schema.Artifact = artifact
		publication.Queries = append(publication.Queries, entry.Declaration)
		bindings = append(bindings, entry.Binding)
		schemas = append(schemas, entry.Schema)
	}

	// Core Schema 与声明进入同一密封 publication；生产 Registry 后续可直接
	// 作为 ResultSchemaValidator，而无需维护一个会与 lifecycle 分裂的 sidecar。
	publication, err = queryregistry.BindResultSchemas(publication, schemas)
	if err != nil {
		return nil, fmt.Errorf("%w: compile core result schemas: %v", ErrQueryRegistryCoreInvalid, err)
	}

	return &QueryRegistryCoreCatalog{
		artifact: artifact, publication: cloneQueryRegistryCorePublication(publication),
		bindings: append([]QueryRegistryProtocolV2Binding(nil), bindings...),
		schemas:  cloneQueryRegistryCoreSchemas(schemas), costPolicy: newQueryRegistryCoreCostPolicy(costMaximum),
		costMaximum: costMaximum,
	}, nil
}

// NewQueryRegistryCoreCostPolicy returns the deterministic Host-owned cost
// policy with conservative weights and absolute bounds.
func NewQueryRegistryCoreCostPolicy() queryregistry.CostPolicy {
	return newQueryRegistryCoreCostPolicy(queryRegistryCoreDefaultCostMaximum)
}

func newQueryRegistryCoreCostPolicy(maximum int) queryregistry.CostPolicy {
	return queryregistry.CostPolicyFunc(func(input queryregistry.QueryCostInput) (queryregistry.QueryCost, error) {
		return estimateQueryRegistryCoreCost(input, maximum)
	})
}

// NewQueryRegistryCoreCursorCodec wraps NewHMACCursorCodec with caller-supplied
// secret material. This package never invents or stores the secret.
func NewQueryRegistryCoreCursorCodec(secret []byte) (queryregistry.CursorCodec, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("%w: cursor secret is required", ErrQueryRegistryCoreInvalid)
	}
	codec, err := queryregistry.NewHMACCursorCodec(secret)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrQueryRegistryCoreInvalid, err)
	}
	return codec, nil
}

// NewQueryRegistryCoreRegistry constructs a Query Registry preloaded with the
// Core catalog, Host cost policy, and optional HMAC cursor codec.
func NewQueryRegistryCoreRegistry(options QueryRegistryCoreOptions) (*queryregistry.Registry, *QueryRegistryCoreCatalog, error) {
	catalog, err := newQueryRegistryCoreCatalog(options.CostMaximum)
	if err != nil {
		return nil, nil, err
	}
	registryOptions := []queryregistry.Option{
		queryregistry.WithCostPolicy(catalog.CostPolicy()),
	}
	if len(options.CursorSecret) > 0 {
		codec, codecErr := NewQueryRegistryCoreCursorCodec(options.CursorSecret)
		if codecErr != nil {
			return nil, nil, codecErr
		}
		registryOptions = append(registryOptions, queryregistry.WithCursorCodec(codec))
	}
	registry := queryregistry.New(registryOptions...)
	if _, err := registry.Publish(catalog.Publication()); err != nil {
		return nil, nil, fmt.Errorf("%w: publish core catalog: %v", ErrQueryRegistryCoreInvalid, err)
	}
	return registry, catalog, nil
}

type queryRegistryCoreMappedEntry struct {
	Definition  protocolV2QueryDefinition
	Declaration queryregistry.QueryDeclaration
	Binding     QueryRegistryProtocolV2Binding
	Schema      queryregistry.JSONResultSchemaBinding
}

type queryRegistryCoreMapping struct {
	HostQueryID      string
	HostPlanVersion  string
	QueryID          string
	ContractVersion  string
	Entity           string
	PlanVersion      string
	ResultSchema     string
	PermissionPolicy string
	Pagination       string
}

func mapStableCoreDefinitionToQueryRegistry(definition protocolV2QueryDefinition) (queryRegistryCoreMappedEntry, error) {
	if err := validateProtocolV2QueryDefinition(definition); err != nil {
		return queryRegistryCoreMappedEntry{}, fmt.Errorf("%w: %v", ErrQueryRegistryCoreUnsupported, err)
	}
	mapping, err := queryRegistryCoreMappingFor(definition)
	if err != nil {
		return queryRegistryCoreMappedEntry{}, err
	}
	fields := make([]string, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			return queryRegistryCoreMappedEntry{}, fmt.Errorf("%w: empty field on %s", ErrQueryRegistryCoreUnsupported, definition.ID)
		}
		fields = append(fields, name)
	}
	filters, err := queryRegistryCoreFilterNames(definition)
	if err != nil {
		return queryRegistryCoreMappedEntry{}, err
	}
	sorts := make([]string, 0, len(definition.Sorts))
	for _, item := range definition.Sorts {
		name := strings.TrimSpace(item.Field)
		if name == "" {
			return queryRegistryCoreMappedEntry{}, fmt.Errorf("%w: empty sort on %s", ErrQueryRegistryCoreUnsupported, definition.ID)
		}
		sorts = append(sorts, name)
	}
	if definition.Single {
		if len(sorts) > 0 {
			// Single-row Host queries reject client sorts; do not advertise sort slots.
			return queryRegistryCoreMappedEntry{}, fmt.Errorf(
				"%w: single-row query %s declares sorts", ErrQueryRegistryCoreUnsupported, definition.ID,
			)
		}
	}
	// Host Query 的 relation/join 仍是开放边界；目录不得声明 relation 槽位。
	declaration := queryregistry.QueryDeclaration{
		ID: mapping.QueryID, ContractVersion: mapping.ContractVersion, Entity: mapping.Entity, PlanVersion: mapping.PlanVersion,
		Fields: fields, Filters: filters, Sort: sorts, Pagination: mapping.Pagination,
		ResultSchema: mapping.ResultSchema, PermissionPolicy: mapping.PermissionPolicy,
		// 尚无 entity-event invalidator；空标签让执行层禁用结果缓存。
		CacheTags: nil,
	}
	schemaBody, schemaDigest, err := queryRegistryCoreResultSchemaDocument(definition)
	if err != nil {
		return queryRegistryCoreMappedEntry{}, err
	}
	return queryRegistryCoreMappedEntry{
		Definition:  cloneProtocolV2QueryDefinition(definition),
		Declaration: declaration,
		Binding: QueryRegistryProtocolV2Binding{
			QueryID: mapping.QueryID, ContractVersion: mapping.ContractVersion, PlanVersion: mapping.PlanVersion,
			ResultSchema: mapping.ResultSchema,
			HostQueryID:  mapping.HostQueryID, HostPlanVersion: mapping.HostPlanVersion,
		},
		Schema: queryregistry.JSONResultSchemaBinding{
			QueryID: mapping.QueryID, ContractVersion: mapping.ContractVersion, PlanVersion: mapping.PlanVersion,
			ResultSchema: mapping.ResultSchema,
			SchemaDigest: schemaDigest, Schema: schemaBody,
		},
	}, nil
}

func queryRegistryCoreMappingFor(definition protocolV2QueryDefinition) (queryRegistryCoreMapping, error) {
	var mapping queryRegistryCoreMapping
	switch definition.ID {
	case QuerySafeUserByID:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QuerySafeUserByID, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.safe_user.by_id", ContractVersion: "core.query.safe_user.by_id@1",
			Entity: "core.query.safe_user", PlanVersion: "core.query.safe_user.by_id.plan@1",
			ResultSchema:     QuerySafeUserResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		}
	case QueryPublicTopicsList:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QueryPublicTopicsList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_topics.list", ContractVersion: "core.query.public_topics.list@1",
			Entity: "core.query.public_topic", PlanVersion: "core.query.public_topics.list.plan@1",
			ResultSchema:     QueryPublicTopicResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		}
	case QueryPublicTopicByID:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QueryPublicTopicByID, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_topic.by_id", ContractVersion: "core.query.public_topic.by_id@1",
			Entity: "core.query.public_topic", PlanVersion: "core.query.public_topic.by_id.plan@1",
			ResultSchema:     QueryPublicTopicResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		}
	case QueryPublicAttachmentByPublicID:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QueryPublicAttachmentByPublicID, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_attachment.by_public_id", ContractVersion: "core.query.public_attachment.by_public_id@1",
			Entity: "core.query.public_attachment_metadata", PlanVersion: "core.query.public_attachment.by_public_id.plan@1",
			ResultSchema:     QueryPublicAttachmentSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		}
	case QuerySiteStats:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QuerySiteStats, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.site_stats.current", ContractVersion: "core.query.site_stats.current@1",
			Entity: "core.query.site_stats", PlanVersion: "core.query.site_stats.current.plan@1",
			ResultSchema:     QuerySiteStatsResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		}
	case QueryPublicCommentsList:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QueryPublicCommentsList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_comments.list", ContractVersion: "core.query.public_comments.list@1",
			Entity: "core.query.public_comment", PlanVersion: "core.query.public_comments.list.plan@1",
			ResultSchema:     QueryPublicCommentResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		}
	case QuerySafeUserByUsername:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QuerySafeUserByUsername, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.safe_user.by_username", ContractVersion: "core.query.safe_user.by_username@1",
			Entity: "core.query.safe_user", PlanVersion: "core.query.safe_user.by_username.plan@1",
			ResultSchema:     QuerySafeUserResultSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationNone,
		}
	case QuerySafeUsersBatch:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QuerySafeUsersBatch, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.safe_users.batch", ContractVersion: "core.query.safe_users.batch@1",
			Entity: "core.query.safe_user_avatar", PlanVersion: "core.query.safe_users.batch.plan@1",
			ResultSchema:     QuerySafeUserAvatarSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		}
	case QueryPublicCategoriesList:
		mapping = queryRegistryCoreMapping{
			HostQueryID: QueryPublicCategoriesList, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: "core.query.public_categories.list", ContractVersion: "core.query.public_categories.list@1",
			Entity: "core.query.public_category", PlanVersion: "core.query.public_categories.list.plan@1",
			ResultSchema:     QueryPublicCategorySchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyPublic, Pagination: queryregistry.PaginationOffset,
		}
	case QuerySelfEmail:
		// 同意式读取：login 策略强制活 actor，真正的门在 consent gate（精确制品
		// 能力 + 限流）与 host 签发的单次委派上；永不走 public 策略。
		mapping = queryRegistryCoreMapping{
			HostQueryID: QuerySelfEmail, HostPlanVersion: QueryStableCorePlanVersion,
			QueryID: QuerySelfEmailRegistryID, ContractVersion: QuerySelfEmailRegistryContractVersion,
			Entity: "core.query.identity.self_email", PlanVersion: QuerySelfEmailRegistryPlanVersion,
			ResultSchema:     QuerySelfEmailSchemaID + "@" + QueryStableCoreResultSchemaV1,
			PermissionPolicy: queryregistry.PermissionPolicyLogin, Pagination: queryregistry.PaginationNone,
		}
	default:
		if shortcode, ok := shortcodeProjectionCoreMapping(definition); ok {
			mapping = shortcode
			break
		}
		return queryRegistryCoreMapping{}, fmt.Errorf(
			"%w: Host query %q has no explicit Query Registry mapping", ErrQueryRegistryCoreUnsupported, definition.ID,
		)
	}
	if definition.PlanVersion != mapping.HostPlanVersion ||
		definition.ResultSchemaID+"@"+definition.ResultSchemaVersion != mapping.ResultSchema ||
		definition.Single != (mapping.Pagination == queryregistry.PaginationNone) {
		return queryRegistryCoreMapping{}, fmt.Errorf(
			"%w: Host query %s drifted from its explicit Query Registry mapping", ErrQueryRegistryCoreUnsupported, definition.ID,
		)
	}
	return mapping, nil
}

func queryRegistryCoreFilterNames(definition protocolV2QueryDefinition) ([]string, error) {
	names := make([]string, 0, len(definition.Filters))
	seen := make(map[string]struct{}, len(definition.Filters))
	for _, filter := range definition.Filters {
		if strings.TrimSpace(filter.Operator) != "eq" {
			// 非 eq 过滤器需要额外契约；当前稳定目录只支持等值过滤。
			return nil, fmt.Errorf(
				"%w: filter %q on %s uses operator %q",
				ErrQueryRegistryCoreUnsupported, filter.Field, definition.ID, filter.Operator,
			)
		}
		name := strings.TrimSpace(filter.Field)
		if name == "" {
			return nil, fmt.Errorf("%w: empty filter on %s", ErrQueryRegistryCoreUnsupported, definition.ID)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("%w: duplicate filter %q on %s", ErrQueryRegistryCoreUnsupported, name, definition.ID)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, required := range definition.RequiredFilters {
		required = strings.TrimSpace(required)
		if _, ok := seen[required]; !ok {
			return nil, fmt.Errorf(
				"%w: required filter %q missing from %s", ErrQueryRegistryCoreUnsupported, required, definition.ID,
			)
		}
	}
	return names, nil
}

func normalizeQueryRegistryCoreCostMaximum(value int) (int, error) {
	if value == 0 {
		return queryRegistryCoreDefaultCostMaximum, nil
	}
	if value < 1 || value > queryRegistryCoreAbsoluteCostMaximum {
		return 0, fmt.Errorf(
			"%w: cost maximum must be between 1 and %d", ErrQueryRegistryCoreInvalid, queryRegistryCoreAbsoluteCostMaximum,
		)
	}
	return value, nil
}

func estimateQueryRegistryCoreCost(input queryregistry.QueryCostInput, siteMaximum int) (queryregistry.QueryCost, error) {
	if len(input.Relations) > 0 {
		// Host-owned joins/relations remain open; never invent join costs.
		return queryregistry.QueryCost{}, fmt.Errorf(
			"%w: core Query cost policy rejects relations", queryregistry.ErrContractInsufficient,
		)
	}
	if input.Pagination.Limit < 0 || input.Pagination.Offset < 0 {
		return queryregistry.QueryCost{}, queryregistry.ErrInvalid
	}
	units := queryRegistryCoreCostBase +
		len(input.Fields)*queryRegistryCoreCostPerField +
		len(input.Filters)*queryRegistryCoreCostPerFilter +
		len(input.Sorts)*queryRegistryCoreCostPerSort +
		input.Pagination.Limit*queryRegistryCoreCostPerPageRow
	if units < 0 {
		return queryregistry.QueryCost{}, queryregistry.ErrInvalid
	}
	if siteMaximum < 1 || siteMaximum > queryRegistryCoreAbsoluteCostMaximum {
		return queryregistry.QueryCost{}, queryregistry.ErrInvalid
	}
	maximum := siteMaximum
	if input.RequestedMaximum > 0 && input.RequestedMaximum < maximum {
		maximum = input.RequestedMaximum
	}
	return queryregistry.QueryCost{Units: units, Maximum: maximum}, nil
}

func queryRegistryCorePackageDigest(entries []queryRegistryCoreMappedEntry, costMaximum int) (string, error) {
	type bindingRef struct {
		QueryID         string `json:"queryId"`
		ContractVersion string `json:"contractVersion"`
		PlanVersion     string `json:"planVersion"`
		ResultSchema    string `json:"resultSchema"`
		HostQueryID     string `json:"hostQueryId"`
		HostPlanVersion string `json:"hostPlanVersion"`
	}
	type projectionRef struct {
		HostDefinition protocolV2QueryDefinition      `json:"hostDefinition"`
		Declaration    queryregistry.QueryDeclaration `json:"declaration"`
		Binding        bindingRef                     `json:"binding"`
		SchemaDigest   string                         `json:"schemaDigest"`
		Schema         json.RawMessage                `json:"schema"`
	}
	type costRef struct {
		Base            int `json:"base"`
		PerField        int `json:"perField"`
		PerFilter       int `json:"perFilter"`
		PerSort         int `json:"perSort"`
		PerReturnedRow  int `json:"perReturnedRow"`
		SiteMaximum     int `json:"siteMaximum"`
		AbsoluteMaximum int `json:"absoluteMaximum"`
	}
	document := struct {
		SchemaVersion string          `json:"schemaVersion"`
		ExtensionID   string          `json:"extensionId"`
		Version       string          `json:"extensionVersion"`
		Cost          costRef         `json:"cost"`
		Projections   []projectionRef `json:"projections"`
	}{
		SchemaVersion: queryRegistryCoreCatalogSchema,
		ExtensionID:   QueryRegistryCoreExtensionID,
		Version:       QueryRegistryCoreExtensionVersion,
		Cost: costRef{
			Base: queryRegistryCoreCostBase, PerField: queryRegistryCoreCostPerField,
			PerFilter: queryRegistryCoreCostPerFilter, PerSort: queryRegistryCoreCostPerSort,
			PerReturnedRow: queryRegistryCoreCostPerPageRow, SiteMaximum: costMaximum,
			AbsoluteMaximum: queryRegistryCoreAbsoluteCostMaximum,
		},
		Projections: make([]projectionRef, 0, len(entries)),
	}
	for _, entry := range entries {
		schemaDigest := sha256.Sum256(entry.Schema.Schema)
		if hex.EncodeToString(schemaDigest[:]) != entry.Schema.SchemaDigest {
			return "", fmt.Errorf("%w: core result schema digest mismatch", ErrQueryRegistryCoreInvalid)
		}
		document.Projections = append(document.Projections, projectionRef{
			HostDefinition: cloneProtocolV2QueryDefinition(entry.Definition),
			Declaration:    entry.Declaration,
			Binding: bindingRef{
				QueryID: entry.Binding.QueryID, ContractVersion: entry.Binding.ContractVersion,
				PlanVersion: entry.Binding.PlanVersion, ResultSchema: entry.Binding.ResultSchema,
				HostQueryID: entry.Binding.HostQueryID, HostPlanVersion: entry.Binding.HostPlanVersion,
			},
			SchemaDigest: entry.Schema.SchemaDigest,
			Schema:       append(json.RawMessage(nil), entry.Schema.Schema...),
		})
	}
	sort.SliceStable(document.Projections, func(i, j int) bool {
		if document.Projections[i].Binding.QueryID == document.Projections[j].Binding.QueryID {
			return document.Projections[i].Binding.HostQueryID < document.Projections[j].Binding.HostQueryID
		}
		return document.Projections[i].Binding.QueryID < document.Projections[j].Binding.QueryID
	})
	body, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%w: marshal core package digest", ErrQueryRegistryCoreInvalid)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func cloneQueryRegistryCorePublication(value queryregistry.Publication) queryregistry.Publication {
	value.Queries = append([]queryregistry.QueryDeclaration(nil), value.Queries...)
	for index := range value.Queries {
		value.Queries[index].Fields = append([]string(nil), value.Queries[index].Fields...)
		value.Queries[index].Relations = append([]string(nil), value.Queries[index].Relations...)
		value.Queries[index].Filters = append([]string(nil), value.Queries[index].Filters...)
		value.Queries[index].Sort = append([]string(nil), value.Queries[index].Sort...)
		value.Queries[index].CacheTags = append([]string(nil), value.Queries[index].CacheTags...)
	}
	return value
}

func cloneQueryRegistryCoreSchemas(values []queryregistry.JSONResultSchemaBinding) []queryregistry.JSONResultSchemaBinding {
	result := append([]queryregistry.JSONResultSchemaBinding(nil), values...)
	for index := range result {
		result[index].Schema = append([]byte(nil), result[index].Schema...)
	}
	return result
}
