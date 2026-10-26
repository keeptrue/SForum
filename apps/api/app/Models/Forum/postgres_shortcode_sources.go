package forum

import (
	"context"
	"fmt"

	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

const maxPublicShortcodeSourceResources = 256

func (s *PostgresStore) LoadPublicShortcodeSources(
	ctx context.Context,
	resources []contentregistry.ShortcodeResourceKey,
) (map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource, error) {
	result := make(map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource)
	if s == nil || s.pool == nil || len(resources) == 0 {
		return result, nil
	}
	topicIDs := make([]int64, 0, len(resources))
	commentIDs := make([]int64, 0, len(resources))
	seen := make(map[contentregistry.ShortcodeResourceKey]struct{}, len(resources))
	for _, resource := range resources {
		if len(seen) >= maxPublicShortcodeSourceResources {
			break
		}
		if resource.ID <= 0 || (resource.Type != "topic" && resource.Type != "comment") {
			continue
		}
		if _, duplicate := seen[resource]; duplicate {
			continue
		}
		seen[resource] = struct{}{}
		if resource.Type == "topic" {
			topicIDs = append(topicIDs, resource.ID)
		} else {
			commentIDs = append(commentIDs, resource.ID)
		}
	}
	if len(topicIDs) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT topics.id, posts.raw_content, posts.source_format
			FROM topics
			JOIN posts ON posts.id = topics.content_id
			JOIN categories ON categories.id = topics.category_id
			JOIN category_groups ON category_groups.id = categories.group_id
			WHERE topics.id = ANY($1::bigint[])
			  AND topics.status IN ('active', 'locked')
			  AND topics.deleted_at IS NULL
			  AND categories.visibility = 'public'
			  AND category_groups.visibility = 'public'
		`, topicIDs)
		if err != nil {
			return nil, fmt.Errorf("load public topic shortcode sources: %w", err)
		}
		for rows.Next() {
			var source PublicShortcodeSource
			source.Resource.Type = "topic"
			if err := rows.Scan(&source.Resource.ID, &source.RawContent, &source.SourceFormat); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan public topic shortcode source: %w", err)
			}
			result[source.Resource] = source
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate public topic shortcode sources: %w", err)
		}
		rows.Close()
	}
	if len(commentIDs) > 0 {
		rows, err := s.pool.Query(ctx, `
			SELECT comments.id, posts.raw_content, posts.source_format
			FROM comments
			JOIN posts ON posts.id = comments.content_id
			JOIN topics ON topics.id = comments.topic_id
			JOIN categories ON categories.id = topics.category_id
			JOIN category_groups ON category_groups.id = categories.group_id
			WHERE comments.id = ANY($1::bigint[])
			  AND comments.status = 'active'
			  AND comments.deleted_at IS NULL
			  AND topics.status IN ('active', 'locked')
			  AND topics.deleted_at IS NULL
			  AND categories.visibility = 'public'
			  AND category_groups.visibility = 'public'
		`, commentIDs)
		if err != nil {
			return nil, fmt.Errorf("load public comment shortcode sources: %w", err)
		}
		for rows.Next() {
			var source PublicShortcodeSource
			source.Resource.Type = "comment"
			if err := rows.Scan(&source.Resource.ID, &source.RawContent, &source.SourceFormat); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan public comment shortcode source: %w", err)
			}
			result[source.Resource] = source
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate public comment shortcode sources: %w", err)
		}
		rows.Close()
	}
	return result, nil
}

var _ PublicShortcodeSourceStore = (*PostgresStore)(nil)
