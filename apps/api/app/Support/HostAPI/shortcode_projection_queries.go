package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
)

const (
	ShortcodeProjectionExtensionID = "sforum-shortcodes"
	ShortcodeProjectionMaxBatch    = 32

	QueryShortcodePublicUsersBatch      = "sforum.core.shortcode.public_users.batch"
	QueryShortcodePublicTopicsBatch     = "sforum.core.shortcode.public_topics.batch"
	QueryShortcodePublicCommentsBatch   = "sforum.core.shortcode.public_comments.batch"
	QueryShortcodePublicCategoriesBatch = "sforum.core.shortcode.public_categories.batch"
	QueryShortcodeFriendLinksList       = "sforum.core.shortcode.friend_links.list"
	QueryShortcodeAuthorDecisionsBatch  = "sforum.core.shortcode.author_decisions.batch"
	QueryShortcodeReplyEligibilityBatch = "sforum.core.shortcode.reply_eligibility.batch"

	QueryShortcodePublicUserSchema       = "sforum.core.shortcode.public_user"
	QueryShortcodePublicTopicSchema      = "sforum.core.shortcode.public_topic"
	QueryShortcodePublicCommentSchema    = "sforum.core.shortcode.public_comment"
	QueryShortcodePublicCategorySchema   = "sforum.core.shortcode.public_category"
	QueryShortcodeFriendLinkSchema       = "sforum.core.shortcode.friend_link"
	QueryShortcodeAuthorDecisionSchema   = "sforum.core.shortcode.author_decision"
	QueryShortcodeReplyEligibilitySchema = "sforum.core.shortcode.reply_eligibility"

	ShortcodeProjectionContractVersion = "1"
	ShortcodeProjectionSchemaVersion   = "1"

	shortcodeProjectionActorSQLToken       = "{{sforum_actor_user_id}}"
	shortcodeProjectionActorPolicy         = "sforum.shortcode.actor_decision"
)

var ErrShortcodeProjectionActorUnavailable = errors.New("hostapi: shortcode projection actor is unavailable")

type shortcodeProjectionActorContextKey struct{}

func contextWithShortcodeProjectionActor(ctx context.Context, actorUserID int64) context.Context {
	return context.WithValue(ctx, shortcodeProjectionActorContextKey{}, actorUserID)
}

func shortcodeProjectionActorFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	value, ok := ctx.Value(shortcodeProjectionActorContextKey{}).(int64)
	return value, ok && value >= 0
}

// ShortcodeProjectionQueryIDs is the complete frozen M4 grant set. Returning a
// copy prevents later milestones from mutating the Host-owned allowlist.
func ShortcodeProjectionQueryIDs() []string {
	return []string{
		"core.query.shortcode.public_users.batch",
		"core.query.shortcode.public_topics.batch",
		"core.query.shortcode.public_comments.batch",
		"core.query.shortcode.public_categories.batch",
		"core.query.shortcode.friend_links.list",
		"core.query.shortcode.author_decisions.batch",
		"core.query.shortcode.reply_eligibility.batch",
	}
}

// IsShortcodeProjectionActorPolicy identifies the sealed Host-only decision
// policy. It is not a grantable RBAC permission and must never enter role data.
func IsShortcodeProjectionActorPolicy(value string) bool {
	return value == shortcodeProjectionActorPolicy
}

func shortcodeProjectionProtocolV2QueryDefinitions() []protocolV2QueryDefinition {
	ids := protocolV2QueryFilterDefinition{
		Field: "ids", Operator: "eq", Expression: "stable.id",
		SchemaID: QueryTextParameterSchemaID, Kind: protocolV2QueryInt64ListFilterKind,
	}
	publicTopic := `topics.status IN ('active', 'locked') AND topics.deleted_at IS NULL
		AND categories.visibility = 'public' AND category_groups.visibility = 'public'`
	return []protocolV2QueryDefinition{
		{
			ID: QueryShortcodePublicUsersBatch, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryShortcodePublicUserSchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
			From: `(
				SELECT users.id, users.username, users.display_name
				FROM users WHERE users.status = 'active'
			) AS stable`,
			Fields: []protocolV2QueryField{
				{Name: "id", Expression: "stable.id"}, {Name: "username", Expression: "stable.username"},
				{Name: "display_name", Expression: "stable.display_name"},
			},
			Filters: []protocolV2QueryFilterDefinition{ids}, RequiredFilters: []string{"ids"},
			Sorts:        []protocolV2QuerySortDefinition{{Field: "id", Expression: "stable.id"}},
			DefaultSorts: []protocolV2QuerySort{{Field: "id", Expression: "stable.id"}},
		},
		{
			ID: QueryShortcodePublicTopicsBatch, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryShortcodePublicTopicSchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
			From: `(
				SELECT topics.id, topics.title, topics.slug, categories.slug AS category_slug,
					categories.name AS category_name, LEFT(posts.plain_text, 240) AS excerpt
				FROM topics
				JOIN categories ON categories.id = topics.category_id
				JOIN category_groups ON category_groups.id = categories.group_id
				JOIN posts ON posts.id = topics.content_id
				WHERE ` + publicTopic + `
			) AS stable`,
			Fields: []protocolV2QueryField{
				{Name: "id", Expression: "stable.id"}, {Name: "title", Expression: "stable.title"},
				{Name: "slug", Expression: "stable.slug"}, {Name: "category_slug", Expression: "stable.category_slug"},
				{Name: "category_name", Expression: "stable.category_name"}, {Name: "excerpt", Expression: "stable.excerpt"},
			},
			Filters: []protocolV2QueryFilterDefinition{ids}, RequiredFilters: []string{"ids"},
			Sorts:        []protocolV2QuerySortDefinition{{Field: "id", Expression: "stable.id"}},
			DefaultSorts: []protocolV2QuerySort{{Field: "id", Expression: "stable.id"}},
		},
		{
			ID: QueryShortcodePublicCommentsBatch, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryShortcodePublicCommentSchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
			From: `(
				SELECT comments.id, comments.topic_id, topics.slug AS topic_slug, topics.title AS topic_title,
					LEFT(posts.plain_text, 240) AS excerpt,
					-- 展示用时间固定为 Y-m-d H:i:s：投影契约声明 created_at 为 string，
					-- 直接返回 timestamptz 会让 Go 驱动序列化成 RFC3339 毫秒串。
					TO_CHAR(comments.created_at, 'YYYY-MM-DD HH24:MI:SS') AS created_at,
					TRUE AS owning_topic_public
				FROM comments
				JOIN topics ON topics.id = comments.topic_id
				JOIN categories ON categories.id = topics.category_id
				JOIN category_groups ON category_groups.id = categories.group_id
				JOIN posts ON posts.id = comments.content_id
				WHERE comments.status = 'active' AND comments.deleted_at IS NULL AND ` + publicTopic + `
			) AS stable`,
			Fields: []protocolV2QueryField{
				{Name: "id", Expression: "stable.id"}, {Name: "topic_id", Expression: "stable.topic_id"},
				{Name: "topic_slug", Expression: "stable.topic_slug"}, {Name: "topic_title", Expression: "stable.topic_title"},
				{Name: "excerpt", Expression: "stable.excerpt"}, {Name: "created_at", Expression: "stable.created_at"},
				{Name: "owning_topic_public", Expression: "stable.owning_topic_public"},
			},
			Filters: []protocolV2QueryFilterDefinition{ids}, RequiredFilters: []string{"ids"},
			Sorts:        []protocolV2QuerySortDefinition{{Field: "id", Expression: "stable.id"}},
			DefaultSorts: []protocolV2QuerySort{{Field: "id", Expression: "stable.id"}},
		},
		{
			ID: QueryShortcodePublicCategoriesBatch, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryShortcodePublicCategorySchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
			From: `(
				SELECT categories.id, categories.slug, categories.name, categories.description,
					categories.icon, categories.icon_color
				FROM categories JOIN category_groups ON category_groups.id = categories.group_id
				WHERE categories.visibility = 'public' AND category_groups.visibility = 'public'
			) AS stable`,
			Fields: []protocolV2QueryField{
				{Name: "id", Expression: "stable.id"}, {Name: "slug", Expression: "stable.slug"},
				{Name: "name", Expression: "stable.name"}, {Name: "description", Expression: "stable.description"},
				{Name: "icon", Expression: "stable.icon"}, {Name: "icon_color", Expression: "stable.icon_color"},
			},
			Filters: []protocolV2QueryFilterDefinition{ids}, RequiredFilters: []string{"ids"},
			Sorts:        []protocolV2QuerySortDefinition{{Field: "id", Expression: "stable.id"}},
			DefaultSorts: []protocolV2QuerySort{{Field: "id", Expression: "stable.id"}},
		},
		{
			ID: QueryShortcodeFriendLinksList, PlanVersion: QueryStableCorePlanVersion,
			ResultSchemaID: QueryShortcodeFriendLinkSchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
			From: `(
				SELECT site_friend_links.id, site_friend_links.name, site_friend_links.url,
					site_friend_links.description, site_friend_links.logo_url, site_friend_links.position
				FROM site_friend_links WHERE site_friend_links.enabled = TRUE
			) AS stable`,
			Fields: []protocolV2QueryField{
				{Name: "id", Expression: "stable.id"}, {Name: "name", Expression: "stable.name"},
				{Name: "url", Expression: "stable.url"}, {Name: "description", Expression: "stable.description"},
				{Name: "logo_url", Expression: "stable.logo_url"}, {Name: "position", Expression: "stable.position"},
			},
			Sorts: []protocolV2QuerySortDefinition{
				{Field: "position", Expression: "stable.position"}, {Field: "id", Expression: "stable.id"},
			},
			DefaultSorts: []protocolV2QuerySort{
				{Field: "position", Expression: "stable.position"}, {Field: "id", Expression: "stable.id"},
			},
		},
		shortcodeAuthorDecisionDefinition(ids, publicTopic),
		shortcodeReplyEligibilityDefinition(ids, publicTopic),
	}
}

func shortcodeAuthorDecisionDefinition(ids protocolV2QueryFilterDefinition, publicTopic string) protocolV2QueryDefinition {
	return protocolV2QueryDefinition{
		ID: QueryShortcodeAuthorDecisionsBatch, PlanVersion: QueryStableCorePlanVersion,
		ResultSchemaID: QueryShortcodeAuthorDecisionSchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
		From: `(
			SELECT 'topic'::text AS resource_type, topics.id,
				(actor.user_id > 0) AS authenticated,
				(actor.user_id > 0 AND topics.author_user_id = actor.user_id) AS is_resource_author,
				(actor.user_id > 0 AND topics.author_user_id = actor.user_id) AS is_topic_author
			FROM topics JOIN categories ON categories.id = topics.category_id
			JOIN category_groups ON category_groups.id = categories.group_id
			CROSS JOIN (SELECT ` + shortcodeProjectionActorSQLToken + `::bigint AS user_id) actor
			WHERE ` + publicTopic + `
			UNION ALL
			SELECT 'comment'::text, comments.id, (actor.user_id > 0),
				(actor.user_id > 0 AND comments.author_user_id = actor.user_id),
				(actor.user_id > 0 AND topics.author_user_id = actor.user_id)
			FROM comments JOIN topics ON topics.id = comments.topic_id
			JOIN categories ON categories.id = topics.category_id
			JOIN category_groups ON category_groups.id = categories.group_id
			CROSS JOIN (SELECT ` + shortcodeProjectionActorSQLToken + `::bigint AS user_id) actor
			WHERE comments.status = 'active' AND comments.deleted_at IS NULL AND ` + publicTopic + `
		) AS stable`,
		Fields: []protocolV2QueryField{
			{Name: "resource_type", Expression: "stable.resource_type"}, {Name: "resource_id", Expression: "stable.id"},
			{Name: "authenticated", Expression: "stable.authenticated"},
			{Name: "is_resource_author", Expression: "stable.is_resource_author"},
			{Name: "is_topic_author", Expression: "stable.is_topic_author"},
		},
		Filters: []protocolV2QueryFilterDefinition{
			{Field: "resource_type", Operator: "eq", Expression: "stable.resource_type", SchemaID: QueryTextParameterSchemaID, Kind: "text"},
			ids,
		},
		RequiredFilters: []string{"resource_type", "ids"},
		Sorts:           []protocolV2QuerySortDefinition{{Field: "resource_id", Expression: "stable.id"}},
		DefaultSorts:    []protocolV2QuerySort{{Field: "resource_id", Expression: "stable.id"}},
		ActorScoped:     true,
	}
}

func shortcodeReplyEligibilityDefinition(ids protocolV2QueryFilterDefinition, publicTopic string) protocolV2QueryDefinition {
	return protocolV2QueryDefinition{
		ID: QueryShortcodeReplyEligibilityBatch, PlanVersion: QueryStableCorePlanVersion,
		ResultSchemaID: QueryShortcodeReplyEligibilitySchema, ResultSchemaVersion: ShortcodeProjectionSchemaVersion,
		From: `(
			SELECT topics.id, (actor.user_id > 0) AS authenticated,
				(actor.user_id > 0 AND (
					topics.author_user_id = actor.user_id OR EXISTS (
						SELECT 1 FROM comments
						WHERE comments.topic_id = topics.id AND comments.author_user_id = actor.user_id
							AND comments.status = 'active' AND comments.deleted_at IS NULL
					)
				)) AS eligible
			FROM topics JOIN categories ON categories.id = topics.category_id
			JOIN category_groups ON category_groups.id = categories.group_id
			CROSS JOIN (SELECT ` + shortcodeProjectionActorSQLToken + `::bigint AS user_id) actor
			WHERE ` + publicTopic + `
		) AS stable`,
		Fields: []protocolV2QueryField{
			{Name: "topic_id", Expression: "stable.id"}, {Name: "authenticated", Expression: "stable.authenticated"},
			{Name: "eligible", Expression: "stable.eligible"},
		},
		Filters: []protocolV2QueryFilterDefinition{ids}, RequiredFilters: []string{"ids"},
		Sorts:        []protocolV2QuerySortDefinition{{Field: "topic_id", Expression: "stable.id"}},
		DefaultSorts: []protocolV2QuerySort{{Field: "topic_id", Expression: "stable.id"}},
		ActorScoped:  true,
	}
}

func parseShortcodeProjectionIDs(value string) ([]int64, error) {
	var ids []int64
	decoder := json.NewDecoder(strings.NewReader(value))
	if err := decoder.Decode(&ids); err != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		len(ids) == 0 || len(ids) > ShortcodeProjectionMaxBatch {
		return nil, fmt.Errorf("%w: ids must contain 1..%d canonical positive integers", queryregistry.ErrInvalid, ShortcodeProjectionMaxBatch)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: ids must be positive", queryregistry.ErrInvalid)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%w: ids must be unique", queryregistry.ErrInvalid)
		}
		seen[id] = struct{}{}
	}
	canonical, err := canonicalShortcodeProjectionIDs(ids)
	if err != nil || canonical != value {
		return nil, fmt.Errorf("%w: ids must be sorted canonical JSON", queryregistry.ErrInvalid)
	}
	return ids, nil
}

func canonicalShortcodeProjectionIDs(ids []int64) (string, error) {
	if len(ids) == 0 || len(ids) > ShortcodeProjectionMaxBatch {
		return "", queryregistry.ErrInvalid
	}
	value := "["
	for index, id := range ids {
		if id <= 0 || (index > 0 && id <= ids[index-1]) {
			return "", queryregistry.ErrInvalid
		}
		if index > 0 {
			value += ","
		}
		value += strconv.FormatInt(id, 10)
	}
	return value + "]", nil
}
