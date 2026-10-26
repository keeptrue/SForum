package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type fakeQueryClient struct {
	queriesPerCall int8
	rows           map[string][]map[string]any
	batchQueries   map[string]int
	listQueries    map[string]int
	batchErr       error
	listErr        error
}

func (f *fakeQueryClient) batchQuery(_ context.Context, _ *pluginv2.ContentCall, queryID string, _ []int64, _ []string) ([]map[string]any, error) {
	f.queriesPerCall++
	if f.batchQueries == nil {
		f.batchQueries = map[string]int{}
	}
	f.batchQueries[queryID]++
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	return f.rows[queryID], nil
}

func (f *fakeQueryClient) listQuery(_ context.Context, _ *pluginv2.ContentCall, queryID string, _ []string) ([]map[string]any, error) {
	f.queriesPerCall++
	if f.listQueries == nil {
		f.listQueries = map[string]int{}
	}
	f.listQueries[queryID]++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows[queryID], nil
}

func userCall(t *testing.T, userID any) *pluginv2.ContentCall {
	t.Helper()
	node := map[string]any{
		"type": shortcodeRefNodeType,
		"attrs": map[string]any{
			"id": shortcodeUserID, "contractVersion": shortcodeUserVersion,
			"arguments": map[string]any{"userId": userID},
		},
	}
	return testCall(t, node)
}

func categoryCall(t *testing.T, categoryID any) *pluginv2.ContentCall {
	t.Helper()
	node := map[string]any{
		"type": shortcodeRefNodeType,
		"attrs": map[string]any{
			"id": shortcodeCategoryID, "contractVersion": shortcodeCategoryVersion,
			"arguments": map[string]any{"categoryId": categoryID},
		},
	}
	return testCall(t, node)
}

func topicCall(t *testing.T, topicID any) *pluginv2.ContentCall {
	t.Helper()
	node := map[string]any{
		"type": shortcodeRefNodeType,
		"attrs": map[string]any{
			"id": shortcodeTopicID, "contractVersion": shortcodeTopicVersion,
			"arguments": map[string]any{"topicId": topicID},
		},
	}
	return testCall(t, node)
}

func commentCall(t *testing.T, commentID any) *pluginv2.ContentCall {
	t.Helper()
	node := map[string]any{
		"type": shortcodeRefNodeType,
		"attrs": map[string]any{
			"id": shortcodeCommentID, "contractVersion": shortcodeCommentVersion,
			"arguments": map[string]any{"commentId": commentID},
		},
	}
	return testCall(t, node)
}

func friendLinksCall(t *testing.T) *pluginv2.ContentCall {
	t.Helper()
	node := map[string]any{
		"type": shortcodeRefNodeType,
		"attrs": map[string]any{
			"id": shortcodeFriendLinksID, "contractVersion": shortcodeFriendLinksVer,
			"arguments": map[string]any{},
		},
	}
	return testCall(t, node)
}

func testCall(t *testing.T, node map[string]any) *pluginv2.ContentCall {
	t.Helper()
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	declaredID, _ := node["attrs"].(map[string]any)["id"].(string)
	return &pluginv2.ContentCall{
		Context: &protocolwire.RequestContext{RequestId: "unit", Locale: "zh-CN"},
		Target:  pluginv2.ContentDeclaration{ID: declaredID, ContractVersion: declaredID + "@1"},
		Document: pluginv2.ContentEditorDocument{
			SchemaVersion: pluginv2.ContentEditorDocumentSchema, ContentID: declaredID,
			ContractVersion: declaredID + "@1", Schema: declaredID + ".schema@1",
			StorageVersion: "1", Value: encoded,
		},
		Locale: "zh-CN", Scope: "public",
	}
}

func segmentHTML(t *testing.T, result pluginv2.ContentResult) string {
	t.Helper()
	if result.Render == nil {
		t.Fatal("render result has no segments")
	}
	var builder strings.Builder
	for _, segment := range result.Render.Segments {
		builder.WriteString(segment.HTML)
	}
	return builder.String()
}

func TestRenderUserCardSingleBatchQuery(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	client := &fakeQueryClient{rows: map[string][]map[string]any{
		queryPublicUsersBatch: {{"id": float64(42), "username": "zhang-san", "display_name": "张三"}},
	}}
	result, err := plugin.renderUser(t.Context(), userCall(t, 42), client)
	if err != nil {
		t.Fatal(err)
	}
	rendered := segmentHTML(t, result)
	nodes := parseHTMLFragment(t, rendered)
	anchor := requireElement(t, nodes, "a", 1)[0]
	if attribute(anchor, "href") != "/u/zhang-san" || textContent(anchor) != "张三" ||
		!strings.Contains(textContent(nodes[0]), "@zhang-san") {
		t.Fatalf("user card DOM = %q", rendered)
	}
	if client.queriesPerCall != 1 || client.batchQueries[queryPublicUsersBatch] != 1 {
		t.Fatalf("expected exactly one batch query, got %#v", client.batchQueries)
	}
}

func TestRenderUserMissingRowUsesGenericUnavailableWithoutMetadata(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	client := &fakeQueryClient{rows: map[string][]map[string]any{queryPublicUsersBatch: {}}}
	result, err := plugin.renderUser(t.Context(), userCall(t, 99999), client)
	if err != nil {
		t.Fatal(err)
	}
	html := segmentHTML(t, result)
	if html != `<p>该内容暂不可用</p>` {
		t.Fatalf("unavailable output = %q", html)
	}
	if strings.Contains(html, "99999") || strings.Contains(html, "/u/") {
		t.Fatalf("unavailable output leaked target metadata: %q", html)
	}
}

func TestRenderCategoryCardSingleBatchQuery(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	client := &fakeQueryClient{rows: map[string][]map[string]any{
		queryPublicCategoriesBatch: {{"id": float64(7), "slug": "general", "name": "综合", "description": "默认分类"}},
	}}
	result, err := plugin.renderCategory(t.Context(), categoryCall(t, 7), client)
	if err != nil {
		t.Fatal(err)
	}
	rendered := segmentHTML(t, result)
	nodes := parseHTMLFragment(t, rendered)
	anchor := requireElement(t, nodes, "a", 1)[0]
	if attribute(anchor, "href") != "/c/general" || textContent(anchor) != "综合" ||
		!strings.Contains(textContent(nodes[0]), "默认分类") {
		t.Fatalf("category card DOM = %q", rendered)
	}
	if client.queriesPerCall != 1 {
		t.Fatalf("category render must issue exactly one query")
	}
}

func TestRenderTopicCardUsesOnlyPublicTopicBatchProjection(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	client := &fakeQueryClient{rows: map[string][]map[string]any{
		queryPublicTopicsBatch: {{
			"id": float64(42), "title": "Public topic", "slug": "public-topic",
			"category_name": "General", "excerpt": "Safe excerpt",
		}},
	}}
	result, err := plugin.renderTopic(t.Context(), topicCall(t, 42), client)
	if err != nil {
		t.Fatal(err)
	}
	rendered := segmentHTML(t, result)
	anchor := requireElement(t, parseHTMLFragment(t, rendered), "a", 1)[0]
	if attribute(anchor, "href") != "/t/42/public-topic" || textContent(anchor) != "Public topic" ||
		strings.Contains(rendered, "rawContent") || strings.Contains(rendered, "actor") {
		t.Fatalf("topic card DOM = %q", rendered)
	}
	if client.queriesPerCall != 1 || client.batchQueries[queryPublicTopicsBatch] != 1 {
		t.Fatalf("topic renderer query calls = %#v", client.batchQueries)
	}
}

func TestRenderCommentCardReconfirmsOwningTopicVisibility(t *testing.T) {
	t.Parallel()
	rows := []map[string]any{{
		"id": float64(91), "topic_id": float64(42), "topic_slug": "public-topic",
		"topic_title": "Public topic", "excerpt": "Safe comment", "created_at": "2026-08-23T00:00:00Z",
		"owning_topic_public": true,
	}}
	plugin := &shortcodePlugin{}
	client := &fakeQueryClient{rows: map[string][]map[string]any{queryPublicCommentsBatch: rows}}
	result, err := plugin.renderComment(t.Context(), commentCall(t, 91), client)
	if err != nil {
		t.Fatal(err)
	}
	rendered := segmentHTML(t, result)
	anchor := requireElement(t, parseHTMLFragment(t, rendered), "a", 1)[0]
	if attribute(anchor, "href") != "/t/42/public-topic#comment-91" ||
		client.queriesPerCall != 1 || client.batchQueries[queryPublicCommentsBatch] != 1 {
		t.Fatalf("comment card DOM=%q queries=%#v", rendered, client.batchQueries)
	}

	rows[0]["owning_topic_public"] = false
	denied, err := plugin.renderComment(t.Context(), commentCall(t, 91), client)
	if err != nil {
		t.Fatal(err)
	}
	deniedHTML := segmentHTML(t, denied)
	if deniedHTML != `<p>该评论不存在或暂不可见</p>` || strings.Contains(deniedHTML, "91") || strings.Contains(deniedHTML, "/t/") {
		t.Fatalf("owning-topic denial leaked metadata: %q", deniedHTML)
	}
}

func TestRenderFriendLinksListWithRowsSortsAndEscapes(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	rows := []map[string]any{
		{"id": float64(2), "name": `<b>Second</b>`, "url": "https://second.example.com", "description": "", "position": float64(20)},
		{"id": float64(1), "name": "First", "url": "javascript:alert(1)", "description": "第一条", "position": float64(10)},
	}
	client := &fakeQueryClient{rows: map[string][]map[string]any{queryFriendLinksList: rows}}
	result, err := plugin.renderFriendLinks(t.Context(), friendLinksCall(t), client)
	if err != nil {
		t.Fatal(err)
	}
	rendered := segmentHTML(t, result)
	nodes := parseHTMLFragment(t, rendered)
	items := requireElement(t, nodes, "li", 2)
	anchors := requireElement(t, nodes, "a", 1)
	if attribute(anchors[0], "href") != "https://second.example.com" || textContent(anchors[0]) != "<b>Second</b>" {
		t.Fatalf("valid friend link DOM = %q", rendered)
	}
	if len(elements(items[0], "a")) != 0 || !strings.Contains(textContent(items[0]), "First") ||
		strings.Contains(rendered, "javascript:") {
		t.Fatalf("invalid friend link URL was not quarantined: %q", rendered)
	}
	if client.listQueries[queryFriendLinksList] != 1 {
		t.Fatalf("friend links must issue exactly one list query")
	}
}

func TestMalformedProjectionRowsFailClosed(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}

	userClient := &fakeQueryClient{rows: map[string][]map[string]any{
		queryPublicUsersBatch: {{"id": float64(42), "username": "", "display_name": "Name"}},
	}}
	userResult, err := plugin.renderUser(t.Context(), userCall(t, 42), userClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := segmentHTML(t, userResult); got != `<p>该内容暂不可用</p>` {
		t.Fatalf("malformed user projection = %q", got)
	}

	categoryClient := &fakeQueryClient{rows: map[string][]map[string]any{
		queryPublicCategoriesBatch: {{"id": float64(7), "slug": "general", "name": ""}},
	}}
	categoryResult, err := plugin.renderCategory(t.Context(), categoryCall(t, 7), categoryClient)
	if err != nil {
		t.Fatal(err)
	}
	if got := segmentHTML(t, categoryResult); got != `<p>该内容暂不可用</p>` {
		t.Fatalf("malformed category projection = %q", got)
	}

	linksClient := &fakeQueryClient{rows: map[string][]map[string]any{
		queryFriendLinksList: {{"id": float64(1), "name": " ", "url": "https://example.com"}},
	}}
	linksResult, err := plugin.renderFriendLinks(t.Context(), friendLinksCall(t), linksClient)
	if err != nil {
		t.Fatal(err)
	}
	if linksResult.Render == nil || len(linksResult.Render.Segments) != 0 {
		t.Fatalf("malformed friend links produced public output: %#v", linksResult.Render)
	}
}

func TestProjectionQueryFailuresDoNotLeakInternalErrors(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	internal := errors.New("postgres password=secret plugin/path leaked")
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "user", run: func() error {
			_, err := plugin.renderUser(t.Context(), userCall(t, 42), &fakeQueryClient{batchErr: internal})
			return err
		}},
		{name: "category", run: func() error {
			_, err := plugin.renderCategory(t.Context(), categoryCall(t, 7), &fakeQueryClient{batchErr: internal})
			return err
		}},
		{name: "friend links", run: func() error {
			_, err := plugin.renderFriendLinks(t.Context(), friendLinksCall(t), &fakeQueryClient{listErr: internal})
			return err
		}},
	}
	for _, test := range tests {
		err := test.run()
		if !errors.Is(err, errShortcodeQuery) || err.Error() != errShortcodeQuery.Error() {
			t.Fatalf("%s query error leaked internal detail: %v", test.name, err)
		}
	}
}

func TestRenderFriendLinksEmptyProducesNoPublicOutput(t *testing.T) {
	t.Parallel()
	plugin := &shortcodePlugin{}
	client := &fakeQueryClient{rows: map[string][]map[string]any{queryFriendLinksList: {}}}
	result, err := plugin.renderFriendLinks(t.Context(), friendLinksCall(t), client)
	if err != nil {
		t.Fatal(err)
	}
	if result.Render == nil || len(result.Render.Segments) != 0 || result.Render.PlainText != "" {
		t.Fatalf("empty friend links must produce no public output: %#v", result.Render)
	}
}

func TestStrictCallValueRejectsDrift(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]any{
		"wrong type": {
			"type":  "sforumShortcodeBlock",
			"attrs": map[string]any{"id": shortcodeUserID, "contractVersion": shortcodeUserVersion, "arguments": map[string]any{"userId": 1}},
		},
		"wrong version": {
			"type":  shortcodeRefNodeType,
			"attrs": map[string]any{"id": shortcodeUserID, "contractVersion": "sforum-shortcodes.user@2", "arguments": map[string]any{"userId": 1}},
		},
		"extra attr": {
			"type":  shortcodeRefNodeType,
			"attrs": map[string]any{"id": shortcodeUserID, "contractVersion": shortcodeUserVersion, "arguments": map[string]any{"userId": 1}, "forged": true},
		},
		"extra argument": {
			"type":  shortcodeRefNodeType,
			"attrs": map[string]any{"id": shortcodeUserID, "contractVersion": shortcodeUserVersion, "arguments": map[string]any{"userId": 1, "admin": true}},
		},
		"zero id": {
			"type":  shortcodeRefNodeType,
			"attrs": map[string]any{"id": shortcodeUserID, "contractVersion": shortcodeUserVersion, "arguments": map[string]any{"userId": 0}},
		},
		"string id": {
			"type":  shortcodeRefNodeType,
			"attrs": map[string]any{"id": shortcodeUserID, "contractVersion": shortcodeUserVersion, "arguments": map[string]any{"userId": "42"}},
		},
	}
	for name, node := range cases {
		node := node
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeShortcodeCallValue(mustJSON(t, node), shortcodeUserID, shortcodeUserVersion); err == nil {
				t.Fatalf("invalid call value accepted")
			}
		})
	}
}

func TestStrictCallValueRejectsArgumentFromAnotherDeclaration(t *testing.T) {
	t.Parallel()
	for declaredID, arguments := range map[string]map[string]any{
		shortcodeTopicID:       {"userId": 42},
		shortcodeCommentID:     {"topicId": 42},
		shortcodeFriendLinksID: {"categoryId": 42},
	} {
		version := declaredID + "@1"
		node := map[string]any{
			"type": shortcodeRefNodeType,
			"attrs": map[string]any{
				"id": declaredID, "contractVersion": version, "arguments": arguments,
			},
		}
		if _, err := decodeShortcodeCallValue(mustJSON(t, node), declaredID, version); err == nil {
			t.Fatalf("%s accepted another declaration's argument", declaredID)
		}
	}
}

func TestInvalidCallErrorDoesNotEchoSourceOrPrivateMetadata(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"type":"sforumShortcodeRef","attrs":{"id":"sforum-shortcodes.user","contractVersion":"sforum-shortcodes.user@1","arguments":{"userId":1},"rawContent":"secret-marker","actor":"admin","session":"private"}}`)
	_, err := decodeShortcodeCallValue(raw, shortcodeUserID, shortcodeUserVersion)
	if !errors.Is(err, errShortcodeCallInput) || err.Error() != errShortcodeCallInput.Error() {
		t.Fatalf("invalid call leaked private input: %v", err)
	}
}

func TestCopyForLocaleFallbackChain(t *testing.T) {
	t.Parallel()
	if copyFor("zh-CN").genericUnavailable != "该内容暂不可用" {
		t.Fatal("exact zh-CN fell through")
	}
	if copyFor("zh").genericUnavailable != "该内容暂不可用" {
		t.Fatal("language prefix zh fell through")
	}
	if copyFor("en-US").genericUnavailable != "This content is currently unavailable" {
		t.Fatal("exact en-US fell through")
	}
	if copyFor("fr-FR").userCard != "用户" {
		t.Fatal("unknown locale must use the safe zh-CN default")
	}
	if copyFor("und").userCard != "用户" {
		t.Fatal("und must behave like the safe default")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
