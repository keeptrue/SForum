package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type postgresProtocolV2QueryExecutor struct {
	pool *pgxpool.Pool
}

type postgresProtocolV2QueryAuthorityResolver struct {
	pool *pgxpool.Pool
}

func NewPostgresProtocolV2QueryAuthorityResolver(pool *pgxpool.Pool) ProtocolV2QueryAuthorityResolver {
	return &postgresProtocolV2QueryAuthorityResolver{pool: pool}
}

func NewPostgresProtocolV2QueryRuntime(
	pool *pgxpool.Pool,
	authority ProtocolV2QueryAuthorityResolver,
	options ...ProtocolV2QueryRuntimeOption,
) (ProtocolV2QueryRuntime, error) {
	if pool == nil || authority == nil {
		return nil, errors.New("hostapi: PostgreSQL query pool and authority resolver are required")
	}
	runtime, err := newProtocolV2QueryRuntime(
		&postgresProtocolV2QueryExecutor{pool: pool},
		authority,
		stableCoreProtocolV2QueryDefinitions()...,
	)
	if err != nil {
		return nil, err
	}
	configured := protocolV2QueryRuntimeOptions{}
	for _, option := range options {
		if option != nil {
			option.applyProtocolV2QueryRuntime(&configured)
		}
	}
	runtime.queryEngine().traceSink = configured.traceSink
	return runtime, nil
}

func (r *postgresProtocolV2QueryAuthorityResolver) ResolveProtocolV2QueryAuthority(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
) (ProtocolV2QueryAuthority, error) {
	if r == nil || r.pool == nil || !validProtocolV2QueryIdentity(identity) {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	var source string
	var system bool
	var manifestDatabase []byte
	var manifestCapabilities []byte
	err := r.pool.QueryRow(ctx, `
		SELECT extensions.source, extensions.is_system,
		       COALESCE(extension_versions.manifest -> 'database', 'null'::jsonb),
		       COALESCE(extension_versions.manifest -> 'capabilities', 'null'::jsonb)
		FROM extensions
		JOIN extension_versions ON extension_versions.id = extensions.active_version_id
		WHERE extensions.id = $1 AND extensions.type = 'plugin' AND extensions.status = 'enabled'
		  AND extension_versions.version = $2 AND extension_versions.package_digest = $3
	`, identity.GetExtensionId(), identity.GetExtensionVersion(), identity.GetArtifactDigest()).Scan(
		&source, &system, &manifestDatabase, &manifestCapabilities,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	if err != nil {
		return ProtocolV2QueryAuthority{}, fmt.Errorf("resolve stable query artifact: %w", err)
	}
	capabilities, capabilitiesErr := protocolV2QueryManifestCapabilities(manifestCapabilities)
	if capabilitiesErr != nil {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	manifestGrants, grantsErr := protocolV2QueryDatabaseGrants(manifestDatabase)
	if grantsErr != nil {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	if identity.GetTrustGrantId() == "builtin" {
		if source != "builtin" || !system {
			return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
		}
		return ProtocolV2QueryAuthority{
			ExactArtifact: true, CoreViews: hasQueryCoreViewsGrant(manifestGrants), Capabilities: capabilities,
		}, nil
	}
	if source != "uploaded" || system {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	grantID, err := strconv.ParseInt(identity.GetTrustGrantId(), 10, 64)
	if err != nil || grantID <= 0 || strconv.FormatInt(grantID, 10) != identity.GetTrustGrantId() {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	var impactDatabase []byte
	var impactCapabilities []byte
	err = r.pool.QueryRow(ctx, `
		SELECT COALESCE(impact_document -> 'database', 'null'::jsonb),
		       COALESCE(impact_document -> 'capabilities', 'null'::jsonb)
		FROM extension_trust_grants
		WHERE id = $1 AND extension_id = $2 AND extension_version = $3
		  AND package_digest = $4 AND action = 'enable' AND revoked_at IS NULL
	`, grantID, identity.GetExtensionId(), identity.GetExtensionVersion(),
		identity.GetArtifactDigest()).Scan(&impactDatabase, &impactCapabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	if err != nil {
		return ProtocolV2QueryAuthority{}, fmt.Errorf("resolve stable query trust grant: %w", err)
	}
	impactGrants, grantsErr := protocolV2QueryDatabaseGrants(impactDatabase)
	if grantsErr != nil || !reflect.DeepEqual(impactGrants, manifestGrants) {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	impactCaps, capsErr := protocolV2QueryImpactCapabilities(impactCapabilities)
	if capsErr != nil || !reflect.DeepEqual(impactCaps, capabilities) {
		return ProtocolV2QueryAuthority{}, ErrProtocolV2QueryRuntimeStale
	}
	return ProtocolV2QueryAuthority{
		ExactArtifact: true, CoreViews: hasQueryCoreViewsGrant(manifestGrants), Capabilities: capabilities,
	}, nil
}

// protocolV2QueryDatabaseGrants expands a stored manifest/impact database
// block into the canonical cumulative grant set. Both shapes are accepted:
// the Manifest V3 `grants` array and the legacy `authority` string, matching
// extensionmanifest.DatabaseGrants expansion. A missing block yields no
// grants (the authority check then denies); a malformed or unknown grant
// yields an error (fail closed as stale).
func protocolV2QueryDatabaseGrants(document []byte) ([]string, error) {
	var database *extensionmanifest.ManifestDatabase
	if len(document) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(document, &database); err != nil {
		return nil, err
	}
	if database == nil {
		return nil, nil
	}
	grants := extensionmanifest.DatabaseGrants(database)
	seen := make(map[string]bool, len(grants))
	for _, grant := range grants {
		switch grant {
		case extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantHostCommands,
			extensionmanifest.DatabaseGrantRawCore,
			extensionmanifest.DatabaseGrantKernel:
		default:
			return nil, fmt.Errorf("unknown database grant %q", grant)
		}
		if seen[grant] {
			return nil, fmt.Errorf("duplicate database grant %q", grant)
		}
		seen[grant] = true
	}
	return grants, nil
}

func hasQueryCoreViewsGrant(grants []string) bool {
	for _, grant := range grants {
		if grant == extensionmanifest.DatabaseGrantCoreViews {
			return true
		}
	}
	return false
}

// protocolV2QueryManifestCapabilities 读取精确制品 manifest 的有效能力 key。
// manifest 已在 Normalize 阶段校验并排序；这里只做形状与去重校验，形状异常
// 一律按 stale 处理（fail closed）。
func protocolV2QueryManifestCapabilities(document []byte) ([]string, error) {
	if len(document) == 0 {
		return nil, nil
	}
	var values *[]string
	if err := json.Unmarshal(document, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, nil
	}
	result := make([]string, 0, len(*values))
	seen := make(map[string]bool, len(*values))
	for _, value := range *values {
		if value == "" || value != strings.TrimSpace(value) {
			return nil, errors.New("invalid manifest capability key")
		}
		if seen[value] {
			return nil, errors.New("duplicate manifest capability key")
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// protocolV2QueryImpactCapabilities 从启用影响评审文档取**显式声明**的能力 key
// 集合。影响文档存的是完整 Grant 对象（含宿主推断项），manifest 只存显式声明；
// 因此这里必须丢掉 implied 项再与 manifest 比较，否则任何带 backend/jobs 的
// 插件都会被误判成漂移。
func protocolV2QueryImpactCapabilities(document []byte) ([]string, error) {
	if len(document) == 0 {
		return nil, nil
	}
	var grants *[]struct {
		Key     string `json:"key"`
		Implied bool   `json:"implied"`
	}
	if err := json.Unmarshal(document, &grants); err != nil {
		return nil, err
	}
	if grants == nil {
		return nil, nil
	}
	result := make([]string, 0, len(*grants))
	seen := make(map[string]bool, len(*grants))
	for _, grant := range *grants {
		if grant.Implied {
			continue
		}
		key := strings.TrimSpace(grant.Key)
		if key == "" {
			return nil, errors.New("invalid impact capability key")
		}
		if seen[key] {
			return nil, errors.New("duplicate impact capability key")
		}
		seen[key] = true
		result = append(result, key)
	}
	sort.Strings(result)
	return result, nil
}

func validProtocolV2QueryIdentity(identity *protocolv2.ExtensionIdentity) bool {
	if identity == nil || strings.TrimSpace(identity.GetExtensionId()) == "" ||
		strings.TrimSpace(identity.GetExtensionVersion()) == "" ||
		len(identity.GetArtifactDigest()) != 64 || strings.Trim(identity.GetArtifactDigest(), "0123456789abcdef") != "" ||
		strings.TrimSpace(identity.GetTrustGrantId()) == "" || identity.GetRuntimeEpoch() == 0 ||
		strings.TrimSpace(identity.GetInstanceId()) == "" {
		return false
	}
	return true
}

func (e *postgresProtocolV2QueryExecutor) ExecuteProtocolV2Query(
	ctx context.Context,
	plan protocolV2QueryPlan,
) ([]map[string]any, error) {
	if e == nil || e.pool == nil {
		return nil, errors.New("Host Query PostgreSQL executor is unavailable")
	}
	tx, err := e.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	actorUserID, actorBound := shortcodeProjectionActorFromContext(ctx)
	if plan.Definition.ActorScoped && !actorBound {
		return nil, ErrShortcodeProjectionActorUnavailable
	}
	query, args := postgresProtocolV2Query(plan, actorUserID)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, plan.FetchLimit)
	for rows.Next() {
		values, valuesErr := rows.Values()
		if valuesErr != nil {
			rows.Close()
			return nil, valuesErr
		}
		if len(values) != len(plan.Fields) {
			rows.Close()
			return nil, errors.New("Host Query returned an unexpected column count")
		}
		item := make(map[string]any, len(plan.Fields))
		for index, field := range plan.Fields {
			value, normalizeErr := normalizeProtocolV2QueryValue(values[index])
			if normalizeErr != nil {
				rows.Close()
				return nil, normalizeErr
			}
			item[field.Name] = value
		}
		result = append(result, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func postgresProtocolV2Query(plan protocolV2QueryPlan, actorUserID int64) (string, []any) {
	selected := make([]string, 0, len(plan.Fields))
	for _, field := range plan.Fields {
		selected = append(selected, field.Expression+" AS "+pgx.Identifier{field.Name}.Sanitize())
	}
	var builder strings.Builder
	builder.WriteString("SELECT ")
	builder.WriteString(strings.Join(selected, ", "))
	builder.WriteString(" FROM ")
	from := plan.Definition.From
	args := make([]any, 0, len(plan.Filters)+3)
	if plan.Definition.ActorScoped {
		args = append(args, actorUserID)
		from = strings.ReplaceAll(from, shortcodeProjectionActorSQLToken, "$1")
	}
	builder.WriteString(from)
	if len(plan.Filters) > 0 {
		builder.WriteString(" WHERE ")
		for index, filter := range plan.Filters {
			if index > 0 {
				builder.WriteString(" AND ")
			}
			args = append(args, filter.Value)
			builder.WriteString(filter.Definition.Expression)
			if filter.Definition.Kind == protocolV2QueryInt64ListFilterKind {
				builder.WriteString(" = ANY($")
			} else {
				builder.WriteString(" = $")
			}
			builder.WriteString(strconv.Itoa(len(args)))
			if filter.Definition.Kind == protocolV2QueryInt64ListFilterKind {
				builder.WriteByte(')')
			}
		}
	}
	if len(plan.Sorts) > 0 {
		builder.WriteString(" ORDER BY ")
		for index, sort := range plan.Sorts {
			if index > 0 {
				builder.WriteString(", ")
			}
			builder.WriteString(sort.Expression)
			if sort.Descending {
				builder.WriteString(" DESC")
			} else {
				builder.WriteString(" ASC")
			}
		}
	}
	args = append(args, plan.FetchLimit)
	builder.WriteString(" LIMIT $")
	builder.WriteString(strconv.Itoa(len(args)))
	args = append(args, plan.Offset)
	builder.WriteString(" OFFSET $")
	builder.WriteString(strconv.Itoa(len(args)))
	return builder.String(), args
}

func normalizeProtocolV2QueryValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, string, bool, float64:
		return typed, nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case int32:
		return float64(typed), nil
	case int16:
		return float64(typed), nil
	case time.Time:
		return typed.UTC().Format(time.RFC3339Nano), nil
	case []byte:
		return string(typed), nil
	default:
		return nil, fmt.Errorf("Host Query result type %T is not supported", value)
	}
}

func stableCoreProtocolV2QueryDefinitions() []protocolV2QueryDefinition {
	safeUserFields := []protocolV2QueryField{
		{Name: "id", Expression: "stable.id"},
		{Name: "username", Expression: "stable.username"},
		{Name: "display_name", Expression: "stable.display_name"},
		{Name: "created_at", Expression: "stable.created_at"},
		{Name: "updated_at", Expression: "stable.updated_at"},
	}
	topicFields := []protocolV2QueryField{
		{Name: "id", Expression: "stable.id"},
		{Name: "category_id", Expression: "stable.category_id"},
		{Name: "category_slug", Expression: "stable.category_slug"},
		{Name: "author_user_id", Expression: "stable.author_user_id"},
		{Name: "title", Expression: "stable.title"},
		{Name: "slug", Expression: "stable.slug"},
		{Name: "status", Expression: "stable.status"},
		{Name: "is_pinned", Expression: "stable.is_pinned"},
		{Name: "comment_count", Expression: "stable.comment_count"},
		{Name: "view_count", Expression: "stable.view_count"},
		{Name: "last_activity_at", Expression: "stable.last_activity_at"},
		{Name: "created_at", Expression: "stable.created_at"},
		{Name: "updated_at", Expression: "stable.updated_at"},
		{Name: "html_content", Expression: "stable.html_content"},
		{Name: "plain_text", Expression: "stable.plain_text"},
		{Name: "source_format", Expression: "stable.source_format"},
		{Name: "render_version", Expression: "stable.render_version"},
		{Name: "content_hash", Expression: "stable.content_hash"},
	}
	attachmentFields := []protocolV2QueryField{
		{Name: "id", Expression: "stable.id"},
		{Name: "public_id", Expression: "stable.public_id"},
		{Name: "owner_user_id", Expression: "stable.owner_user_id"},
		{Name: "original_name", Expression: "stable.original_name"},
		{Name: "content_type", Expression: "stable.content_type"},
		{Name: "extension", Expression: "stable.extension"},
		{Name: "size_bytes", Expression: "stable.size_bytes"},
		{Name: "sha256", Expression: "stable.sha256"},
		{Name: "image_width", Expression: "stable.image_width"},
		{Name: "image_height", Expression: "stable.image_height"},
		{Name: "reference_count", Expression: "stable.reference_count"},
		{Name: "created_at", Expression: "stable.created_at"},
		{Name: "updated_at", Expression: "stable.updated_at"},
	}
	idFilter := protocolV2QueryFilterDefinition{
		Field: "id", Operator: "eq", Expression: "stable.id",
		SchemaID: QueryInt64ParameterSchemaID, Kind: "int64",
	}
	topicSorts := []protocolV2QuerySortDefinition{
		{Field: "id", Expression: "stable.id"},
		{Field: "created_at", Expression: "stable.created_at"},
		{Field: "updated_at", Expression: "stable.updated_at"},
		{Field: "last_activity_at", Expression: "stable.last_activity_at"},
	}
	commentFields := []protocolV2QueryField{
		{Name: "id", Expression: "stable.id"},
		{Name: "topic_id", Expression: "stable.topic_id"},
		{Name: "author_user_id", Expression: "stable.author_user_id"},
		{Name: "parent_comment_id", Expression: "stable.parent_comment_id"},
		{Name: "root_comment_id", Expression: "stable.root_comment_id"},
		{Name: "path_key", Expression: "stable.path_key"},
		{Name: "depth", Expression: "stable.depth"},
		{Name: "reply_count", Expression: "stable.reply_count"},
		{Name: "created_at", Expression: "stable.created_at"},
		{Name: "updated_at", Expression: "stable.updated_at"},
		{Name: "html_content", Expression: "stable.html_content"},
		{Name: "plain_text", Expression: "stable.plain_text"},
		{Name: "source_format", Expression: "stable.source_format"},
		{Name: "render_version", Expression: "stable.render_version"},
		{Name: "content_hash", Expression: "stable.content_hash"},
	}
	userIDListFilter := protocolV2QueryFilterDefinition{
		Field: "user_id", Operator: "eq", Expression: "stable.user_id",
		SchemaID: QueryTextParameterSchemaID, Kind: protocolV2QueryInt64ListFilterKind,
	}
	avatarFields := []protocolV2QueryField{
		{Name: "user_id", Expression: "stable.user_id"},
		{Name: "username", Expression: "stable.username"},
		{Name: "display_name", Expression: "stable.display_name"},
		{Name: "avatar_public_id", Expression: "stable.avatar_public_id"},
		{Name: "avatar_content_type", Expression: "stable.avatar_content_type"},
		{Name: "avatar_image_width", Expression: "stable.avatar_image_width"},
		{Name: "avatar_image_height", Expression: "stable.avatar_image_height"},
	}
	categoryFields := []protocolV2QueryField{
		{Name: "id", Expression: "stable.id"},
		{Name: "slug", Expression: "stable.slug"},
		{Name: "name", Expression: "stable.name"},
		{Name: "description", Expression: "stable.description"},
		{Name: "icon", Expression: "stable.icon"},
		{Name: "icon_color", Expression: "stable.icon_color"},
	}
	definitions := []protocolV2QueryDefinition{
		{
			ID: QuerySafeUserByID, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QuerySafeUserResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.safe_users AS stable", Fields: safeUserFields,
			Filters: []protocolV2QueryFilterDefinition{idFilter}, RequiredFilters: []string{"id"}, Single: true,
		},
		{
			ID: QueryPublicTopicsList, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryPublicTopicResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.forum_topics AS stable", Fields: topicFields,
			Filters: []protocolV2QueryFilterDefinition{
				{Field: "category_id", Operator: "eq", Expression: "stable.category_id", SchemaID: QueryInt64ParameterSchemaID, Kind: "int64"},
				{Field: "author_user_id", Operator: "eq", Expression: "stable.author_user_id", SchemaID: QueryInt64ParameterSchemaID, Kind: "int64"},
			},
			Sorts: topicSorts,
			DefaultSorts: []protocolV2QuerySort{
				{Field: "last_activity_at", Expression: "stable.last_activity_at", Descending: true},
				{Field: "id", Expression: "stable.id", Descending: true},
			},
		},
		{
			ID: QueryPublicTopicByID, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryPublicTopicResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.forum_topics AS stable", Fields: topicFields,
			Filters: []protocolV2QueryFilterDefinition{idFilter}, RequiredFilters: []string{"id"}, Single: true,
		},
		{
			ID: QueryPublicAttachmentByPublicID, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryPublicAttachmentSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.public_attachment_metadata AS stable", Fields: attachmentFields,
			Filters: []protocolV2QueryFilterDefinition{{
				Field: "public_id", Operator: "eq", Expression: "stable.public_id",
				SchemaID: QueryTextParameterSchemaID, Kind: "text",
			}},
			RequiredFilters: []string{"public_id"}, Single: true,
		},
		{
			ID: QuerySiteStats, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QuerySiteStatsResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.site_stats AS stable",
			Fields: []protocolV2QueryField{
				{Name: "topic_count", Expression: "stable.topic_count"},
				{Name: "comment_count", Expression: "stable.comment_count"},
				{Name: "user_count", Expression: "stable.user_count"},
				{Name: "public_attachment_count", Expression: "stable.public_attachment_count"},
			},
			Single: true,
		},
		{
			ID: QueryPublicCommentsList, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryPublicCommentResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.forum_comments AS stable", Fields: commentFields,
			Filters: []protocolV2QueryFilterDefinition{
				{Field: "topic_id", Operator: "eq", Expression: "stable.topic_id", SchemaID: QueryInt64ParameterSchemaID, Kind: "int64"},
				{Field: "author_user_id", Operator: "eq", Expression: "stable.author_user_id", SchemaID: QueryInt64ParameterSchemaID, Kind: "int64"},
			},
			RequiredFilters: []string{"topic_id"},
			Sorts: []protocolV2QuerySortDefinition{
				{Field: "id", Expression: "stable.id"},
				{Field: "created_at", Expression: "stable.created_at"},
				{Field: "updated_at", Expression: "stable.updated_at"},
				{Field: "depth", Expression: "stable.depth"},
			},
			// 与公开评论流一致的时间序：created_at ASC, id ASC。
			DefaultSorts: []protocolV2QuerySort{
				{Field: "created_at", Expression: "stable.created_at"},
				{Field: "id", Expression: "stable.id"},
			},
		},
		{
			ID: QuerySafeUserByUsername, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QuerySafeUserResultSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.safe_users AS stable", Fields: safeUserFields,
			Filters: []protocolV2QueryFilterDefinition{{
				Field: "username", Operator: "eq", Expression: "stable.username",
				SchemaID: QueryTextParameterSchemaID, Kind: "text",
			}},
			RequiredFilters: []string{"username"}, Single: true,
		},
		{
			ID: QuerySafeUsersBatch, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QuerySafeUserAvatarSchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.safe_user_avatars AS stable", Fields: avatarFields,
			Filters:         []protocolV2QueryFilterDefinition{userIDListFilter},
			RequiredFilters: []string{"user_id"},
			Sorts:           []protocolV2QuerySortDefinition{{Field: "user_id", Expression: "stable.user_id"}},
			DefaultSorts:    []protocolV2QuerySort{{Field: "user_id", Expression: "stable.user_id"}},
		},
		{
			ID: QueryPublicCategoriesList, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryPublicCategorySchemaID, ResultSchemaVersion: QueryStableCoreResultSchemaV1,
			From: "sforum_core_v1.public_categories AS stable", Fields: categoryFields,
			Filters: []protocolV2QueryFilterDefinition{{
				Field: "slug", Operator: "eq", Expression: "stable.slug",
				SchemaID: QueryTextParameterSchemaID, Kind: "text",
			}},
			Sorts:        []protocolV2QuerySortDefinition{{Field: "id", Expression: "stable.id"}},
			DefaultSorts: []protocolV2QuerySort{{Field: "id", Expression: "stable.id"}},
		},
	}
	// consent-gated 定义只在这里声明一次；actorless 稳定入口拒绝执行它，
	// 委派执行走它在 Query Registry 的显式投影。
	definitions = append(definitions, selfEmailQueryDefinition())
	return append(definitions, shortcodeProjectionProtocolV2QueryDefinitions()...)
}
