package hostapi

import queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"

func shortcodeProjectionCoreMapping(definition protocolV2QueryDefinition) (queryRegistryCoreMapping, bool) {
	public := queryregistry.PermissionPolicyPublic
	actor := shortcodeProjectionActorPolicy
	mappings := map[string]queryRegistryCoreMapping{
		QueryShortcodePublicUsersBatch: shortcodeProjectionMapping(
			QueryShortcodePublicUsersBatch, "core.query.shortcode.public_users.batch", QueryShortcodePublicUserSchema, public,
		),
		QueryShortcodePublicTopicsBatch: shortcodeProjectionMapping(
			QueryShortcodePublicTopicsBatch, "core.query.shortcode.public_topics.batch", QueryShortcodePublicTopicSchema, public,
		),
		QueryShortcodePublicCommentsBatch: shortcodeProjectionMapping(
			QueryShortcodePublicCommentsBatch, "core.query.shortcode.public_comments.batch", QueryShortcodePublicCommentSchema, public,
		),
		QueryShortcodePublicCategoriesBatch: shortcodeProjectionMapping(
			QueryShortcodePublicCategoriesBatch, "core.query.shortcode.public_categories.batch", QueryShortcodePublicCategorySchema, public,
		),
		QueryShortcodeFriendLinksList: shortcodeProjectionMapping(
			QueryShortcodeFriendLinksList, "core.query.shortcode.friend_links.list", QueryShortcodeFriendLinkSchema, public,
		),
		QueryShortcodeAuthorDecisionsBatch: shortcodeProjectionMapping(
			QueryShortcodeAuthorDecisionsBatch, "core.query.shortcode.author_decisions.batch", QueryShortcodeAuthorDecisionSchema, actor,
		),
		QueryShortcodeReplyEligibilityBatch: shortcodeProjectionMapping(
			QueryShortcodeReplyEligibilityBatch, "core.query.shortcode.reply_eligibility.batch", QueryShortcodeReplyEligibilitySchema, actor,
		),
	}
	mapping, ok := mappings[definition.ID]
	return mapping, ok
}

func shortcodeProjectionMapping(hostID, queryID, schemaID, policy string) queryRegistryCoreMapping {
	return queryRegistryCoreMapping{
		HostQueryID: hostID, HostPlanVersion: QueryStableCorePlanVersion,
		QueryID: queryID, ContractVersion: queryID + "@" + ShortcodeProjectionContractVersion,
		Entity: queryID, PlanVersion: queryID + ".plan@" + ShortcodeProjectionContractVersion,
		ResultSchema:     schemaID + "@" + ShortcodeProjectionSchemaVersion,
		PermissionPolicy: policy, Pagination: queryregistry.PaginationOffset,
	}
}

func shortcodeProjectionSchemaProperties(schemaID string) (map[string]any, bool) {
	properties := map[string]map[string]any{
		QueryShortcodePublicUserSchema: {
			"id": schemaString(), "username": schemaString(), "display_name": schemaString(),
		},
		QueryShortcodePublicTopicSchema: {
			"id": schemaString(), "title": schemaString(), "slug": schemaString(),
			"category_slug": schemaString(), "category_name": schemaString(), "excerpt": schemaString(),
		},
		QueryShortcodePublicCommentSchema: {
			"id": schemaString(), "topic_id": schemaString(), "topic_slug": schemaString(),
			"topic_title": schemaString(), "excerpt": schemaString(), "created_at": schemaString(),
			"owning_topic_public": schemaBoolean(),
		},
		QueryShortcodePublicCategorySchema: {
			"id": schemaString(), "slug": schemaString(), "name": schemaString(),
			"description": schemaString(), "icon": schemaString(), "icon_color": schemaString(),
		},
		QueryShortcodeFriendLinkSchema: {
			"id": schemaString(), "name": schemaString(), "url": schemaString(),
			"description": schemaString(), "logo_url": schemaString(), "position": schemaNumber(),
		},
		QueryShortcodeAuthorDecisionSchema: {
			"resource_type": schemaString(), "resource_id": schemaString(), "authenticated": schemaBoolean(),
			"is_resource_author": schemaBoolean(), "is_topic_author": schemaBoolean(),
		},
		QueryShortcodeReplyEligibilitySchema: {
			"topic_id": schemaString(), "authenticated": schemaBoolean(), "eligible": schemaBoolean(),
		},
	}
	value, ok := properties[schemaID]
	return value, ok
}
