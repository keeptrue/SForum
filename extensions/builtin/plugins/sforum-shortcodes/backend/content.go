package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const (
	shortcodeUserID            = "sforum-shortcodes.user"
	shortcodeUserVersion       = "sforum-shortcodes.user@1"
	shortcodeUserSchema        = "sforum-shortcodes.user.schema@1"
	shortcodeTopicID           = "sforum-shortcodes.topic"
	shortcodeTopicVersion      = "sforum-shortcodes.topic@1"
	shortcodeTopicSchema       = "sforum-shortcodes.topic.schema@1"
	shortcodeCommentID         = "sforum-shortcodes.comment"
	shortcodeCommentVersion    = "sforum-shortcodes.comment@1"
	shortcodeCommentSchema     = "sforum-shortcodes.comment.schema@1"
	shortcodeCategoryID        = "sforum-shortcodes.category"
	shortcodeCategoryVersion   = "sforum-shortcodes.category@1"
	shortcodeCategorySchema    = "sforum-shortcodes.category.schema@1"
	shortcodeFriendLinksID     = "sforum-shortcodes.friend-links"
	shortcodeFriendLinksVer    = "sforum-shortcodes.friend-links@1"
	shortcodeFriendLinksSchema = "sforum-shortcodes.friend-links.schema@1"
	shortcodeLoginID           = "sforum-shortcodes.login"
	shortcodeLoginVersion      = "sforum-shortcodes.login@1"
	shortcodeLoginSchema       = "sforum-shortcodes.login.schema@1"
	shortcodeReplyID           = "sforum-shortcodes.reply"
	shortcodeReplyVersion      = "sforum-shortcodes.reply@1"
	shortcodeReplySchema       = "sforum-shortcodes.reply.schema@1"
	shortcodeOnlyAuthorID      = "sforum-shortcodes.only-author"
	shortcodeOnlyAuthorVersion = "sforum-shortcodes.only-author@1"
	shortcodeOnlyAuthorSchema  = "sforum-shortcodes.only-author.schema@1"

	// 绑定在冻结 M4 投影上的稳定查询 ID，常量与 SDK 冻结助手一致。
	queryPublicUsersBatch      = pluginv2.ShortcodePublicUsersQueryID
	queryPublicTopicsBatch     = pluginv2.ShortcodePublicTopicsQueryID
	queryPublicCommentsBatch   = pluginv2.ShortcodePublicCommentsQueryID
	queryPublicCategoriesBatch = pluginv2.ShortcodePublicCategoriesQueryID
	queryFriendLinksList       = pluginv2.ShortcodeFriendLinksQueryID

	shortcodeRefNodeType = "sforumShortcodeRef"
)

var (
	errShortcodeCallInput = errors.New("sforum-shortcodes: invalid shortcode call input")
	errShortcodeQuery     = errors.New("sforum-shortcodes: shortcode projection unavailable")
)

// shortcodePlugin is the runtime-scoped renderer. The embedded Server owns the
// Host broker after handshake; handlers resolve it per call so no external
// state is shared across runtime identities.
type shortcodePlugin struct {
	server *pluginv2.Server
}

// contentDefinition freezes one exact Manifest content declaration into the
// SDK Content Registry. The handler value document is the accepted canonical
// sforumShortcodeRef node.
func (p *shortcodePlugin) contentDefinition(id, contractVersion, schema string) pluginv2.ContentDefinition {
	return pluginv2.ContentDefinition{
		ID:              id,
		ContractVersion: contractVersion,
		Kind:            pluginv2.ContentKindShortcode,
		Handler:         id,
		Schema:          schema,
		Execute: func(ctx context.Context, call *pluginv2.ContentCall) (pluginv2.ContentResult, error) {
			return p.execute(ctx, call)
		},
	}
}

func (p *shortcodePlugin) execute(ctx context.Context, call *pluginv2.ContentCall) (pluginv2.ContentResult, error) {
	if call == nil || call.Target.ID == "" || call.Target.ContractVersion == "" {
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
	client := &hostQueryClient{plugin: p}
	switch call.Target.ID {
	case shortcodeUserID:
		return p.renderUser(ctx, call, client)
	case shortcodeTopicID:
		return p.renderTopic(ctx, call, client)
	case shortcodeCommentID:
		return p.renderComment(ctx, call, client)
	case shortcodeCategoryID:
		return p.renderCategory(ctx, call, client)
	case shortcodeFriendLinksID:
		return p.renderFriendLinks(ctx, call, client)
	case shortcodeLoginID, shortcodeReplyID, shortcodeOnlyAuthorID:
		return p.renderProtected(call)
	default:
		return pluginv2.ContentResult{}, errShortcodeCallInput
	}
}

// shortcodeValue is the strict accepted value of a sforumShortcodeRef node.
// Unknown fields are rejected by decodeShortcodeCallValue.
type shortcodeValue struct {
	Type  string `json:"type"`
	Attrs struct {
		ID              string                     `json:"id"`
		ContractVersion string                     `json:"contractVersion"`
		Arguments       map[string]json.RawMessage `json:"arguments"`
	} `json:"attrs"`
}

// decodeShortcodeCallValue strictly decodes the frozen canonical node value and
// returns the single declared argument ID (positive int64). Zero means the
// declaration carries no arguments (friend-links).
func decodeShortcodeCallValue(raw json.RawMessage, declaredID, declaredVersion string) (int64, error) {
	if len(raw) == 0 {
		return 0, errShortcodeCallInput
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var value shortcodeValue
	if err := decoder.Decode(&value); err != nil {
		return 0, errShortcodeCallInput
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return 0, errShortcodeCallInput
	}
	if value.Type != shortcodeRefNodeType || value.Attrs.ID != declaredID ||
		value.Attrs.ContractVersion != declaredVersion {
		return 0, errShortcodeCallInput
	}
	identifier, err := shortcodeIDArgument(value.Attrs.Arguments, shortcodeArgumentName(declaredID))
	if err != nil {
		return 0, err
	}
	return identifier, nil
}

func shortcodeArgumentName(declaredID string) string {
	switch declaredID {
	case shortcodeUserID:
		return "userId"
	case shortcodeTopicID:
		return "topicId"
	case shortcodeCommentID:
		return "commentId"
	case shortcodeCategoryID:
		return "categoryId"
	case shortcodeFriendLinksID:
		return ""
	default:
		return "invalid"
	}
}

func shortcodeIDArgument(arguments map[string]json.RawMessage, expected string) (int64, error) {
	if expected == "" && len(arguments) == 0 {
		return 0, nil
	}
	if expected == "" || expected == "invalid" || len(arguments) != 1 {
		return 0, errShortcodeCallInput
	}
	for key, raw := range arguments {
		if key != expected {
			return 0, errShortcodeCallInput
		}
		value := string(raw)
		if strings.HasPrefix(value, "-") || strings.IndexByte(value, '.') >= 0 {
			return 0, errShortcodeCallInput
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			return 0, errShortcodeCallInput
		}
		return parsed, nil
	}
	return 0, nil
}

func (p *shortcodePlugin) renderTopic(ctx context.Context, call *pluginv2.ContentCall, client queryClient) (pluginv2.ContentResult, error) {
	topicID, err := decodeShortcodeCallValue(call.Document.Value, shortcodeTopicID, shortcodeTopicVersion)
	if err != nil {
		return pluginv2.ContentResult{}, err
	}
	rows, err := client.batchQuery(ctx, call, queryPublicTopicsBatch, []int64{topicID}, []string{
		"id", "title", "slug", "category_slug", "category_name", "excerpt",
	})
	if err != nil {
		return pluginv2.ContentResult{}, stableQueryError(err)
	}
	label := copyFor(call.Locale)
	topic, found := publicTopicRow(rows, topicID)
	if !found {
		return renderResult(call, renderUnavailable(label.topicUnavailable)), nil
	}
	return renderResult(call, renderTopicCard(topic, label)), nil
}

func (p *shortcodePlugin) renderComment(ctx context.Context, call *pluginv2.ContentCall, client queryClient) (pluginv2.ContentResult, error) {
	commentID, err := decodeShortcodeCallValue(call.Document.Value, shortcodeCommentID, shortcodeCommentVersion)
	if err != nil {
		return pluginv2.ContentResult{}, err
	}
	rows, err := client.batchQuery(ctx, call, queryPublicCommentsBatch, []int64{commentID}, []string{
		"id", "topic_id", "topic_slug", "topic_title", "excerpt", "created_at", "owning_topic_public",
	})
	if err != nil {
		return pluginv2.ContentResult{}, stableQueryError(err)
	}
	label := copyFor(call.Locale)
	comment, found := publicCommentRow(rows, commentID)
	if !found || !comment.OwningTopicPublic {
		return renderResult(call, renderUnavailable(label.commentUnavailable)), nil
	}
	return renderResult(call, renderCommentCard(comment, label)), nil
}

// renderResult seals a typed render response for the exact shortcode target.
func renderResult(call *pluginv2.ContentCall, segments ...string) pluginv2.ContentResult {
	return pluginv2.ContentResult{Render: &pluginv2.ContentRenderSegments{
		SchemaVersion:   pluginv2.ContentRenderSegmentsSchema,
		ContentID:       call.Target.ID,
		ContractVersion: call.Target.ContractVersion,
		Segments:        htmlSegments(segments...),
	}}
}

func (p *shortcodePlugin) renderUser(ctx context.Context, call *pluginv2.ContentCall, client queryClient) (pluginv2.ContentResult, error) {
	userID, err := decodeShortcodeCallValue(call.Document.Value, shortcodeUserID, shortcodeUserVersion)
	if err != nil {
		return pluginv2.ContentResult{}, err
	}
	rows, err := client.batchQuery(ctx, call, queryPublicUsersBatch, []int64{userID}, []string{"id", "username", "display_name"})
	if err != nil {
		return pluginv2.ContentResult{}, stableQueryError(err)
	}
	label := copyFor(call.Locale)
	user, found := publicUserRow(rows, userID)
	if !found {
		return renderResult(call, renderUnavailable(label.genericUnavailable)), nil
	}
	return renderResult(call, renderUserCard(user, label)), nil
}

func (p *shortcodePlugin) renderCategory(ctx context.Context, call *pluginv2.ContentCall, client queryClient) (pluginv2.ContentResult, error) {
	categoryID, err := decodeShortcodeCallValue(call.Document.Value, shortcodeCategoryID, shortcodeCategoryVersion)
	if err != nil {
		return pluginv2.ContentResult{}, err
	}
	rows, err := client.batchQuery(ctx, call, queryPublicCategoriesBatch, []int64{categoryID}, []string{"id", "slug", "name", "description"})
	if err != nil {
		return pluginv2.ContentResult{}, stableQueryError(err)
	}
	label := copyFor(call.Locale)
	category, found := publicCategoryRow(rows, categoryID)
	if !found {
		return renderResult(call, renderUnavailable(label.genericUnavailable)), nil
	}
	return renderResult(call, renderCategoryCard(category, label)), nil
}

func (p *shortcodePlugin) renderFriendLinks(ctx context.Context, call *pluginv2.ContentCall, client queryClient) (pluginv2.ContentResult, error) {
	_, err := decodeShortcodeCallValue(call.Document.Value, shortcodeFriendLinksID, shortcodeFriendLinksVer)
	if err != nil {
		return pluginv2.ContentResult{}, err
	}
	rows, err := client.listQuery(ctx, call, queryFriendLinksList, []string{"id", "name", "url", "description", "logo_url", "position"})
	if err != nil {
		return pluginv2.ContentResult{}, stableQueryError(err)
	}
	links := publicFriendLinks(rows)
	// 无数据 -> 不产生公开内容（HOST trace 仅记录成功且零段）；禁止占位文案泄漏。
	if len(links) == 0 {
		return renderResult(call), nil
	}
	label := copyFor(call.Locale)
	return renderResult(call, renderFriendLinksBlock(links, label)), nil
}

func stableQueryError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errShortcodeQuery
}
