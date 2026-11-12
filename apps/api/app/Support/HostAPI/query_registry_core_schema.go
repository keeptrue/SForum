package hostapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// queryRegistryCoreResultSchemaDocument builds a Draft 2020-12 schema matching
// Host Query row shapes after normalizeProtocolV2QueryValue:
// BIGINT → string, INTEGER → number, BOOLEAN → bool, timestamps/text → string,
// nullable columns allow null. Field selection is a subset, so required is empty.
func queryRegistryCoreResultSchemaDocument(definition protocolV2QueryDefinition) ([]byte, string, error) {
	properties, err := queryRegistryCoreResultSchemaProperties(definition)
	if err != nil {
		return nil, "", err
	}
	document := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://sforum.invalid/query-result/" + definition.ResultSchemaID + "@" + definition.ResultSchemaVersion,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	body, err := json.Marshal(document)
	if err != nil {
		return nil, "", fmt.Errorf("%w: marshal result schema for %s", ErrQueryRegistryCoreInvalid, definition.ID)
	}
	digest := sha256.Sum256(body)
	return body, hex.EncodeToString(digest[:]), nil
}

func queryRegistryCoreResultSchemaProperties(definition protocolV2QueryDefinition) (map[string]any, error) {
	var properties map[string]any
	switch strings.TrimSpace(definition.ResultSchemaID) {
	case QuerySafeUserResultSchemaID:
		properties = map[string]any{
			"id":           schemaString(),
			"username":     schemaString(),
			"display_name": schemaString(),
			"created_at":   schemaString(),
			"updated_at":   schemaString(),
		}
	case QueryPublicTopicResultSchemaID:
		properties = map[string]any{
			"id":               schemaString(),
			"category_id":      schemaString(),
			"category_slug":    schemaString(),
			"author_user_id":   schemaStringOrNull(),
			"title":            schemaString(),
			"slug":             schemaString(),
			"status":           schemaString(),
			"is_pinned":        schemaBoolean(),
			"comment_count":    schemaString(),
			"view_count":       schemaString(),
			"last_activity_at": schemaString(),
			"created_at":       schemaString(),
			"updated_at":       schemaString(),
			"html_content":     schemaString(),
			"plain_text":       schemaString(),
			"source_format":    schemaString(),
			"render_version":   schemaString(),
			"content_hash":     schemaString(),
		}
	case QueryPublicAttachmentSchemaID:
		properties = map[string]any{
			"id":              schemaString(),
			"public_id":       schemaString(),
			"owner_user_id":   schemaStringOrNull(),
			"original_name":   schemaString(),
			"content_type":    schemaString(),
			"extension":       schemaString(),
			"size_bytes":      schemaString(),
			"sha256":          schemaString(),
			"image_width":     schemaNumberOrNull(),
			"image_height":    schemaNumberOrNull(),
			"reference_count": schemaNumber(),
			"created_at":      schemaString(),
			"updated_at":      schemaString(),
		}
	case QuerySiteStatsResultSchemaID:
		// COUNT(*) returns bigint, which normalize maps to a decimal string.
		properties = map[string]any{
			"topic_count":             schemaString(),
			"comment_count":           schemaString(),
			"user_count":              schemaString(),
			"public_attachment_count": schemaString(),
		}
	case QueryPublicCommentResultSchemaID:
		properties = map[string]any{
			"id":                schemaString(),
			"topic_id":          schemaString(),
			"author_user_id":    schemaStringOrNull(),
			"parent_comment_id": schemaStringOrNull(),
			"root_comment_id":   schemaStringOrNull(),
			"path_key":          schemaString(),
			"depth":             schemaNumber(),
			"reply_count":       schemaString(),
			"created_at":        schemaString(),
			"updated_at":        schemaString(),
			"html_content":      schemaString(),
			"plain_text":        schemaString(),
			"source_format":     schemaString(),
			"render_version":    schemaString(),
			"content_hash":      schemaString(),
		}
	case QuerySafeUserAvatarSchemaID:
		properties = map[string]any{
			"user_id":             schemaString(),
			"username":            schemaString(),
			"display_name":        schemaString(),
			"avatar_public_id":    schemaStringOrNull(),
			"avatar_content_type": schemaStringOrNull(),
			"avatar_image_width":  schemaNumberOrNull(),
			"avatar_image_height": schemaNumberOrNull(),
		}
	case QueryPublicCategorySchemaID:
		properties = map[string]any{
			"id":          schemaString(),
			"slug":        schemaString(),
			"name":        schemaString(),
			"description": schemaStringOrNull(),
			"icon":        schemaStringOrNull(),
			"icon_color":  schemaStringOrNull(),
		}
	case QueryPublicReactionSchemaID:
		// 点赞关系面只有「谁赞了哪个可见目标」，不含 reaction 以外的行为字段。
		properties = map[string]any{
			"id":           schemaString(),
			"target_type":  schemaString(),
			"target_id":    schemaString(),
			"reaction":     schemaString(),
			"user_id":      schemaString(),
			"username":     schemaString(),
			"display_name": schemaString(),
			"created_at":   schemaString(),
		}
	case QueryPublicBookmarkSchemaID:
		// 收藏公开读面：只含公开行（visibility 恒为 public），不含 remind_at /
		// reminded_at——提醒是行主私有计划数据，视图本身就不输出。
		properties = map[string]any{
			"id":           schemaString(),
			"target_type":  schemaString(),
			"target_id":    schemaString(),
			"visibility":   schemaString(),
			"user_id":      schemaString(),
			"username":     schemaString(),
			"display_name": schemaString(),
			"created_at":   schemaString(),
			"updated_at":   schemaString(),
		}
	case QueryPublicBadgeSchemaID:
		// 徽章目录：含 grant_mode / metric / threshold，插件要只靠稳定数据面重建
		// 「目录 + 获得条件」。metric 与 threshold 对手动徽章为 NULL。
		properties = map[string]any{
			"id":          schemaString(),
			"slug":        schemaString(),
			"name":        schemaString(),
			"description": schemaString(),
			"group_key":   schemaStringOrNull(),
			"tier":        schemaString(),
			"icon":        schemaString(),
			"position":    schemaNumber(),
			"grant_mode":  schemaString(),
			"metric":      schemaStringOrNull(),
			"threshold":   schemaStringOrNull(),
		}
	case QueryPublicUserBadgeSchemaID:
		// 授予关系面：只有 active 用户 × 启用徽章。granted_by / grant_reason
		// 是审计数据，视图不输出，schema 也不声明。
		properties = map[string]any{
			"id":           schemaString(),
			"user_id":      schemaString(),
			"badge_id":     schemaString(),
			"granted_at":   schemaString(),
			"username":     schemaString(),
			"display_name": schemaString(),
			"slug":         schemaString(),
			"tier":         schemaString(),
		}
	case QueryPublicUserActivitySchemaID:
		// 活动统计面：刻意不含 posts_read_count / read_time_seconds——
		// v1 不采集，输出 0 会让调用方误以为是真实读数。
		properties = map[string]any{
			"user_id":             schemaString(),
			"username":            schemaString(),
			"display_name":        schemaString(),
			"topic_count":         schemaString(),
			"comment_count":       schemaString(),
			"like_received_count": schemaString(),
			"like_given_count":    schemaString(),
			"bookmark_count":      schemaString(),
			"days_visited":        schemaString(),
		}
	case QueryPublicUserBadgeFactSchemaID:
		// 事实累计面：只输出 active 用户的 total。delta 行是写入流水，dedupe_key
		// 是写入方私有幂等约定，source_extension_id 属于扩展间信息，三者都不进 schema。
		properties = map[string]any{
			"user_id":      schemaString(),
			"username":     schemaString(),
			"display_name": schemaString(),
			"fact_key":     schemaString(),
			"variant":      schemaString(),
			"total":        schemaString(),
		}
	case QuerySelfEmailSchemaID:
		// 只暴露展示形态的 email、布尔验证位与更新时间；email_lower 是唯一性/
		// 登录匹配列，永远不进 schema。属性数必须与 Host 字段数严格相等。
		properties = map[string]any{
			"email":          schemaString(),
			"email_verified": schemaBoolean(),
			"updated_at":     schemaString(),
		}
	default:
		if shortcode, ok := shortcodeProjectionSchemaProperties(definition.ResultSchemaID); ok {
			properties = shortcode
			break
		}
		return nil, fmt.Errorf(
			"%w: no Host-aligned JSON result schema for %q",
			ErrQueryRegistryCoreUnsupported, definition.ResultSchemaID,
		)
	}
	if len(definition.Fields) != len(properties) {
		return nil, fmt.Errorf(
			"%w: Host fields do not exactly match schema %q",
			ErrQueryRegistryCoreUnsupported, definition.ResultSchemaID,
		)
	}
	for _, field := range definition.Fields {
		if _, exists := properties[strings.TrimSpace(field.Name)]; !exists {
			return nil, fmt.Errorf(
				"%w: Host field %q is absent from schema %q",
				ErrQueryRegistryCoreUnsupported, field.Name, definition.ResultSchemaID,
			)
		}
	}
	return properties, nil
}

func schemaString() map[string]any {
	return map[string]any{"type": "string"}
}

func schemaStringOrNull() map[string]any {
	return map[string]any{"type": []any{"string", "null"}}
}

func schemaBoolean() map[string]any {
	return map[string]any{"type": "boolean"}
}

func schemaNumber() map[string]any {
	return map[string]any{"type": "number"}
}

func schemaNumberOrNull() map[string]any {
	return map[string]any{"type": []any{"number", "null"}}
}
