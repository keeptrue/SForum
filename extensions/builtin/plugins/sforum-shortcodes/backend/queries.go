package main

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// queryClient is the frozen Host Query projection surface consumed by the
// renderers. The production binding goes through Host.DelegatedQueryRequest;
// unit tests inject a fake so every renderer is provably one-query-per-call.
type queryClient interface {
	batchQuery(ctx context.Context, call *pluginv2.ContentCall, queryID string, ids []int64, fields []string) ([]map[string]any, error)
	listQuery(ctx context.Context, call *pluginv2.ContentCall, queryID string, fields []string) ([]map[string]any, error)
}

type hostQueryClient struct {
	plugin *shortcodePlugin
}

func (c *hostQueryClient) batchQuery(ctx context.Context, call *pluginv2.ContentCall, queryID string, ids []int64, fields []string) ([]map[string]any, error) {
	filter, err := pluginv2.NewShortcodeProjectionIDsFilter(ids...)
	if err != nil {
		return nil, err
	}
	return c.execute(ctx, call, queryID, filter, fields, uint32(len(ids)))
}

func (c *hostQueryClient) listQuery(ctx context.Context, call *pluginv2.ContentCall, queryID string, fields []string) ([]map[string]any, error) {
	return c.execute(ctx, call, queryID, nil, fields, pluginv2.ShortcodeProjectionMaxBatch)
}

func (c *hostQueryClient) execute(
	ctx context.Context,
	call *pluginv2.ContentCall,
	queryID string,
	filter *hostwire.QueryFilter,
	fields []string,
	limit uint32,
) ([]map[string]any, error) {
	if c == nil || c.plugin == nil || c.plugin.server == nil {
		return nil, errShortcodeQuery
	}
	host, err := c.plugin.server.Host()
	if err != nil {
		return nil, errShortcodeQuery
	}
	request, err := host.DelegatedQueryRequest(
		call.Context,
		queryID,
		pluginv2.ShortcodeProjectionContractVersion(queryID),
		pluginv2.ShortcodeProjectionPlanVersion(queryID),
	)
	if err != nil {
		return nil, errShortcodeQuery
	}
	request.Fields = append([]string(nil), fields...)
	if filter != nil {
		request.Filters = []*hostwire.QueryFilter{filter}
	}
	if limit == 0 || limit > pluginv2.ShortcodeProjectionMaxBatch {
		limit = pluginv2.ShortcodeProjectionMaxBatch
	}
	request.Page = &protocolwire.PageRequest{Limit: limit}
	response, err := host.Queries.Execute(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errShortcodeQuery
	}
	if response.GetError() != nil {
		return nil, errShortcodeQuery
	}
	return responseRows(response), nil
}

// responseRows converts a bounded Query response into detached maps without
// trusting structpb float64 conversions (ids arrive as numbers on the wire).
func responseRows(response *hostwire.QueryResponse) []map[string]any {
	rows := make([]map[string]any, 0, len(response.GetRows()))
	for _, row := range response.GetRows() {
		if row == nil || row.GetValue() == nil {
			continue
		}
		encoded, err := json.Marshal(row.GetValue().AsMap())
		if err != nil {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(string(encoded)))
		decoder.UseNumber()
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			continue
		}
		rows = append(rows, value)
	}
	return rows
}

// ---------- frozen projection row interpretations ----------

type publicUser struct {
	ID          int64
	Username    string
	DisplayName string
}

type publicCategory struct {
	ID          int64
	Slug        string
	Name        string
	Description string
}

type publicTopic struct {
	ID           int64
	Title        string
	Slug         string
	CategoryName string
	Excerpt      string
}

type publicComment struct {
	ID                int64
	TopicID           int64
	TopicSlug         string
	TopicTitle        string
	Excerpt           string
	CreatedAt         string
	OwningTopicPublic bool
}

type publicFriendLink struct {
	ID          int64
	Name        string
	URL         string
	Description string
	Position    int64
}

func rowInt64(row map[string]any, key string) (int64, bool) {
	switch value := row[key].(type) {
	case float64:
		integer := int64(value)
		if float64(integer) != value {
			return 0, false
		}
		return integer, true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func rowString(row map[string]any, key string) string {
	if value, ok := row[key].(string); ok {
		return value
	}
	return ""
}

func rowBool(row map[string]any, key string) bool {
	value, _ := row[key].(bool)
	return value
}

func publicUserRow(rows []map[string]any, userID int64) (publicUser, bool) {
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id != userID || id <= 0 {
			continue
		}
		user := publicUser{
			ID: id, Username: rowString(row, "username"), DisplayName: rowString(row, "display_name"),
		}
		if profilePath(user.Username) == "" {
			continue
		}
		return user, true
	}
	return publicUser{}, false
}

func publicCategoryRow(rows []map[string]any, categoryID int64) (publicCategory, bool) {
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id != categoryID || id <= 0 {
			continue
		}
		category := publicCategory{
			ID: id, Slug: rowString(row, "slug"), Name: rowString(row, "name"),
			Description: rowString(row, "description"),
		}
		if categoryPath(category.Slug) == "" || strings.TrimSpace(category.Name) == "" {
			continue
		}
		return category, true
	}
	return publicCategory{}, false
}

func publicTopicRow(rows []map[string]any, topicID int64) (publicTopic, bool) {
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id != topicID || id <= 0 {
			continue
		}
		topic := publicTopic{
			ID: id, Title: rowString(row, "title"), Slug: rowString(row, "slug"),
			CategoryName: rowString(row, "category_name"), Excerpt: rowString(row, "excerpt"),
		}
		if topicPath(topic.ID, topic.Slug) == "" || strings.TrimSpace(topic.Title) == "" {
			continue
		}
		return topic, true
	}
	return publicTopic{}, false
}

func publicCommentRow(rows []map[string]any, commentID int64) (publicComment, bool) {
	for _, row := range rows {
		id, idOK := rowInt64(row, "id")
		topicID, topicOK := rowInt64(row, "topic_id")
		if !idOK || !topicOK || id != commentID || id <= 0 || topicID <= 0 {
			continue
		}
		comment := publicComment{
			ID: id, TopicID: topicID, TopicSlug: rowString(row, "topic_slug"),
			TopicTitle: rowString(row, "topic_title"), Excerpt: rowString(row, "excerpt"),
			CreatedAt: rowString(row, "created_at"), OwningTopicPublic: rowBool(row, "owning_topic_public"),
		}
		if !comment.OwningTopicPublic || commentPath(comment) == "" || strings.TrimSpace(comment.TopicTitle) == "" {
			continue
		}
		return comment, true
	}
	return publicComment{}, false
}

// publicFriendLinks projects validated rows in the frozen list order
// (position, id) and never fabricates fields.
func publicFriendLinks(rows []map[string]any) []publicFriendLink {
	links := make([]publicFriendLink, 0, len(rows))
	for _, row := range rows {
		id, ok := rowInt64(row, "id")
		if !ok || id <= 0 || strings.TrimSpace(rowString(row, "name")) == "" {
			continue
		}
		position, _ := rowInt64(row, "position")
		links = append(links, publicFriendLink{
			ID: id, Name: rowString(row, "name"), URL: rowString(row, "url"),
			Description: rowString(row, "description"), Position: position,
		})
	}
	sort.SliceStable(links, func(left, right int) bool {
		if links[left].Position != links[right].Position {
			return links[left].Position < links[right].Position
		}
		return links[left].ID < links[right].ID
	})
	return links
}
