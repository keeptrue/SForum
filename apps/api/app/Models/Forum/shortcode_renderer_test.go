package forum

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
)

type publicShortcodeReadStore struct {
	mu sync.Mutex
	Store
	topic         TopicDetail
	comments      CommentList
	sources       map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource
	sourceCalls   int
	lastResources []contentregistry.ShortcodeResourceKey
}

func (s *publicShortcodeReadStore) GetTopic(context.Context, int64) (TopicDetail, error) {
	return s.topic, nil
}

func (s *publicShortcodeReadStore) GetTopicBySlug(context.Context, string) (TopicDetail, error) {
	return s.topic, nil
}

func (s *publicShortcodeReadStore) ListComments(context.Context, CommentListInput) (CommentList, error) {
	return s.comments, nil
}

func (s *publicShortcodeReadStore) LoadPublicShortcodeSources(
	_ context.Context,
	resources []contentregistry.ShortcodeResourceKey,
) (map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sourceCalls++
	s.lastResources = append([]contentregistry.ShortcodeResourceKey(nil), resources...)
	return s.sources, nil
}

type publicShortcodeCaptureDispatcher struct {
	calls     int
	documents [][]contentregistry.ForumShortcodeRenderDocument
}

type protectedShortcodeCaptureDispatcher struct {
	mu    sync.Mutex
	calls []contentregistry.ForumProtectedShortcodeRenderRequest
}

func (d *protectedShortcodeCaptureDispatcher) RenderProtectedFragments(_ context.Context, requests []contentregistry.ForumProtectedShortcodeRenderRequest, _ string) ([]contentregistry.ForumProtectedShortcodeRenderResult, error) {
	d.mu.Lock()
	d.calls = append(d.calls, requests...)
	d.mu.Unlock()
	results := make([]contentregistry.ForumProtectedShortcodeRenderResult, len(requests))
	for index := range requests {
		results[index] = contentregistry.ForumProtectedShortcodeRenderResult{HTML: "<p>AUTHORIZED_FRAGMENT</p>", Rendered: true}
	}
	return results, nil
}

func (d *publicShortcodeCaptureDispatcher) RenderDocuments(
	_ context.Context,
	documents []contentregistry.ForumShortcodeRenderDocument,
	locale string,
) ([]contentregistry.ForumShortcodeRenderResult, error) {
	d.calls++
	d.documents = append(d.documents, documents)
	results := make([]contentregistry.ForumShortcodeRenderResult, len(documents))
	for index, document := range documents {
		results[index].HTML = `<p>runtime ` + document.Resource.String() + ` ` + locale + `</p>`
	}
	return results, nil
}

func publicShortcodeSourceJSON(targetID int64) string {
	return `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"M6_HOST_ONLY_SOURCE"}]},{"type":"sforumShortcodeRef","attrs":{"id":"sforum-shortcodes.topic","contractVersion":"sforum-shortcodes.topic@1","arguments":{"topicId":` + fmt.Sprint(targetID) + `}}}]}`
}

func publicProtectedShortcodeSourceJSON(secret string) string {
	return `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"public"}]},{"type":"sforumShortcodeBlock","attrs":{"id":"sforum-shortcodes.login","contractVersion":"sforum-shortcodes.login@1","arguments":{}},"content":[{"type":"paragraph","content":[{"type":"text","text":"` + secret + `"}]}]}]}`
}

func TestProtectedShortcodeCompositionIsRequestOnlyAndFailClosed(t *testing.T) {
	t.Parallel()
	const secret = "M8_UNIQUE_SECRET_MARKER"
	key := contentregistry.ShortcodeResourceKey{Type: "topic", ID: 10}
	store := &publicShortcodeReadStore{
		Store:   newServiceFakeStore(),
		topic:   TopicDetail{TopicSummary: TopicSummary{ID: 10}, Content: PublicRenderedContent{HTMLContent: "stored", RenderVersion: RenderVersionEditorDocument}},
		sources: map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource{key: {Resource: key, RawContent: publicProtectedShortcodeSourceJSON(secret), SourceFormat: SourceFormatEditorDocument}},
	}
	denied := NewPublicReadService(NewService(ServiceConfig{Store: store})).WithShortcodeDispatcher(&publicShortcodeCaptureDispatcher{})
	deniedTopic, err := denied.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), 10, identity.Actor{})
	if err != nil || strings.Contains(deniedTopic.Content.HTMLContent, secret) || strings.Contains(deniedTopic.Content.HTMLContent, "AUTHORIZED_FRAGMENT") {
		t.Fatalf("denied protected content leaked: %#v err=%v", deniedTopic.Content, err)
	}

	protected := &protectedShortcodeCaptureDispatcher{}
	allowed := NewPublicReadService(NewService(ServiceConfig{Store: store})).WithProtectedShortcodes(
		ProtectedShortcodeAuthorizerFunc(func(_ context.Context, request ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error) {
			return ProtectedShortcodeDecision{Allowed: request.Viewer.ID == 42}, nil
		}), protected,
	)
	allowedTopic, err := allowed.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), 10, identity.Actor{ID: 42})
	if err != nil || !strings.Contains(allowedTopic.Content.HTMLContent, "AUTHORIZED_FRAGMENT") || strings.Contains(allowedTopic.Content.HTMLContent, secret) || !allowedTopic.ProtectedContent {
		t.Fatalf("allowed protected composition invalid: %#v err=%v", allowedTopic.Content, err)
	}
	if !strings.Contains(allowedTopic.Content.HTMLContent, `class="sf-shortcode sf-shortcode--block sf-shortcode--protected sf-shortcode--login"`) {
		t.Fatalf("protected shortcode presentation hook missing: %s", allowedTopic.Content.HTMLContent)
	}
	if len(protected.calls) != 1 || strings.Contains(string(protected.calls[0].AcceptedFragment), "public") {
		t.Fatalf("protected handler received surrounding source: %#v", protected.calls)
	}
}

func TestProtectedShortcodeConcurrentViewersDoNotShareComposition(t *testing.T) {
	t.Parallel()
	const secret = "M8_CONCURRENT_SECRET"
	key := contentregistry.ShortcodeResourceKey{Type: "topic", ID: 11}
	store := &publicShortcodeReadStore{
		Store:   newServiceFakeStore(),
		topic:   TopicDetail{TopicSummary: TopicSummary{ID: 11}, Content: PublicRenderedContent{HTMLContent: "stored", RenderVersion: RenderVersionEditorDocument}},
		sources: map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource{key: {Resource: key, RawContent: publicProtectedShortcodeSourceJSON(secret), SourceFormat: SourceFormatEditorDocument}},
	}
	reads := NewPublicReadService(NewService(ServiceConfig{Store: store})).WithProtectedShortcodes(
		ProtectedShortcodeAuthorizerFunc(func(_ context.Context, request ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error) {
			return ProtectedShortcodeDecision{Allowed: request.Viewer.ID == 99}, nil
		}), &protectedShortcodeCaptureDispatcher{},
	)
	type result struct{ html string }
	results := make(chan result, 2)
	go func() {
		topic, _ := reads.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), 11, identity.Actor{ID: 99})
		results <- result{topic.Content.HTMLContent}
	}()
	go func() {
		topic, _ := reads.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), 11, identity.Actor{ID: 100})
		results <- result{topic.Content.HTMLContent}
	}()
	first, second := <-results, <-results
	joined := first.html + "\n" + second.html
	if !strings.Contains(joined, "AUTHORIZED_FRAGMENT") || !strings.Contains(joined, "Protected content unavailable") || strings.Contains(joined, secret) {
		t.Fatalf("viewer composition crossed or leaked: %s", joined)
	}
}

func TestPublicReadServiceComposesTopicAndCommentShortcodesAfterBaseReads(t *testing.T) {
	t.Parallel()
	topicKey := contentregistry.ShortcodeResourceKey{Type: "topic", ID: 10}
	commentOne := contentregistry.ShortcodeResourceKey{Type: "comment", ID: 100}
	commentTwo := contentregistry.ShortcodeResourceKey{Type: "comment", ID: 101}
	store := &publicShortcodeReadStore{
		Store: newServiceFakeStore(),
		topic: TopicDetail{TopicSummary: TopicSummary{ID: 10, Status: TopicStatusActive}, Content: PublicRenderedContent{
			HTMLContent: `<p>stored topic fallback</p>`, PlainText: "stored", RenderVersion: RenderVersionEditorDocument,
		}},
		comments: CommentList{Items: []Comment{
			{ID: 100, TopicID: 10, Status: CommentStatusActive, Content: PublicRenderedContent{HTMLContent: `<p>stored comment one</p>`, RenderVersion: RenderVersionEditorDocument}},
			{ID: 101, TopicID: 10, Status: CommentStatusActive, Content: PublicRenderedContent{HTMLContent: `<p>stored comment two</p>`, RenderVersion: RenderVersionEditorDocument}},
		}, View: "tree"},
		sources: map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource{
			topicKey:   {Resource: topicKey, RawContent: publicShortcodeSourceJSON(20), SourceFormat: SourceFormatEditorDocument},
			commentOne: {Resource: commentOne, RawContent: publicShortcodeSourceJSON(20), SourceFormat: SourceFormatEditorDocument},
			commentTwo: {Resource: commentTwo, RawContent: publicShortcodeSourceJSON(20), SourceFormat: SourceFormatEditorDocument},
		},
	}
	dispatcher := &publicShortcodeCaptureDispatcher{}
	reads := NewPublicReadService(NewService(ServiceConfig{Store: store})).WithShortcodeDispatcher(dispatcher)
	ctx := WithPublicRenderLocale(t.Context(), "zh-CN")
	topic, err := reads.GetTopic(ctx, 10)
	if err != nil || topic.Content.HTMLContent != `<p>runtime topic:10 zh-CN</p>` {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	comments, err := reads.ListComments(ctx, CommentListInput{TopicID: 10, View: "tree"})
	if err != nil || len(comments.Items) != 2 || comments.Items[0].Content.HTMLContent != `<p>runtime comment:100 zh-CN</p>` ||
		comments.Items[1].Content.HTMLContent != `<p>runtime comment:101 zh-CN</p>` {
		t.Fatalf("comments=%#v err=%v", comments, err)
	}
	if store.sourceCalls != 2 || dispatcher.calls != 2 || len(store.lastResources) != 2 {
		t.Fatalf("sourceCalls=%d dispatcherCalls=%d lastResources=%#v", store.sourceCalls, dispatcher.calls, store.lastResources)
	}
	for _, batch := range dispatcher.documents {
		for _, document := range batch {
			for _, node := range document.Nodes {
				if strings.Contains(string(node.Value), "M6_HOST_ONLY_SOURCE") {
					t.Fatalf("plugin node value received surrounding raw source: %s", node.Value)
				}
			}
		}
	}
}

func TestPublicReadServiceWithoutDispatcherKeepsStoredFallback(t *testing.T) {
	t.Parallel()
	store := &publicShortcodeReadStore{
		Store: newServiceFakeStore(),
		topic: TopicDetail{TopicSummary: TopicSummary{ID: 10}, Content: PublicRenderedContent{
			HTMLContent: `<p>stored fallback</p>`, RenderVersion: RenderVersionEditorDocument,
		}},
	}
	topic, err := NewPublicReadService(NewService(ServiceConfig{Store: store})).GetTopic(t.Context(), 10)
	if err != nil || topic.Content.HTMLContent != `<p>stored fallback</p>` || store.sourceCalls != 0 {
		t.Fatalf("topic=%#v sourceCalls=%d err=%v", topic, store.sourceCalls, err)
	}
}

func TestMarkdownShortcodeImportConvertsCompleteDocument(t *testing.T) {
	t.Parallel()
	const secret = "M2_FORUM_PROTECTED_SECRET"
	input := ContentInput{
		RawContent: strings.Join([]string{
			"# Heading", "", "[user user_id=\"0042\"][/user]", "", "[login]", secret, "[/login]",
		}, "\n"),
		SourceFormat: SourceFormatMarkdown,
		EditorType:   EditorTypeMarkdown,
	}
	rendered, err := RenderContentWithExcerptLimitAndSchemaForResource(input, 160, editordocument.CoreSchema(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	if rendered.SourceFormat != SourceFormatEditorDocument || rendered.EditorType != EditorTypeTiptap {
		t.Fatalf("converted identity = %#v", rendered)
	}
	if !strings.Contains(rendered.RawContent, editordocument.ShortcodeBlockNode) {
		t.Fatalf("canonical editable source lost structure: %s", rendered.RawContent)
	}
	doc, err := editordocument.Parse(editordocument.Input{NativeJSON: []byte(rendered.RawContent)})
	if err != nil || !strings.Contains(editordocument.RenderMarkdown(doc), secret) {
		t.Fatalf("canonical export lost protected source: doc=%#v err=%v", doc, err)
	}
	for label, value := range map[string]string{"html": rendered.HTMLContent, "plain": rendered.PlainText, "excerpt": rendered.Excerpt} {
		if strings.Contains(value, secret) {
			t.Fatalf("%s leaked child: %q", label, value)
		}
	}
	if !strings.Contains(rendered.HTMLContent, "受保护内容暂不可用") || !strings.Contains(rendered.HTMLContent, "用户引用暂不可用") {
		t.Fatalf("missing fallback: %s", rendered.HTMLContent)
	}
	if !strings.Contains(rendered.HTMLContent, `class="sf-editor-fallback"`) {
		t.Fatalf("editor-document fallback presentation hook missing: %s", rendered.HTMLContent)
	}
}

func TestProtectedFallbackUsesHostDefaultLocale(t *testing.T) {
	rendered, err := RenderContentWithExcerptLimitAndSchemaForResource(ContentInput{
		RawContent: publicProtectedShortcodeSourceJSON("M8_LOCALE_SECRET"), SourceFormat: SourceFormatEditorDocument, Locale: "en-US",
	}, 160, editordocument.CoreSchema(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.HTMLContent, "Protected content unavailable") || strings.Contains(rendered.HTMLContent, "M8_LOCALE_SECRET") {
		t.Fatalf("locale or secrecy failed: %s", rendered.HTMLContent)
	}
}

func TestMarkdownWithoutAdmittedShortcodeKeepsExistingPath(t *testing.T) {
	t.Parallel()
	for name, markdown := range map[string]string{
		"unknown":    "alpha\n\n[unknown id=1][/unknown]\n\nomega",
		"escaped":    `\[user user_id="42"][/user]`,
		"inline":     "prefix [user user_id=1][/user] suffix",
		"mismatched": "alpha\n\n[login]\nkeep one\n[reply]\nkeep two\n[/login]\n[/reply]\n\nomega",
		"code":       "```\n[user user_id=1][/user]\n```",
	} {
		name, markdown := name, markdown
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rendered, err := RenderContentWithExcerptLimitAndSchemaForResource(ContentInput{
				RawContent: markdown, SourceFormat: SourceFormatMarkdown,
			}, 160, editordocument.CoreSchema(), "topic")
			if err != nil {
				t.Fatal(err)
			}
			if rendered.SourceFormat != SourceFormatMarkdown || rendered.RawContent != strings.TrimSpace(markdown) {
				t.Fatalf("unexpected conversion: %#v", rendered)
			}
		})
	}
}

func TestMarkdownShortcodeNormalizationDrivesNoOpIdentity(t *testing.T) {
	t.Parallel()
	first, err := RenderContentWithExcerptLimitAndSchemaForResource(ContentInput{
		RawContent: `[user user_id="0042"][/user]`, SourceFormat: SourceFormatMarkdown,
	}, 160, editordocument.CoreSchema(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderContentWithExcerptLimitAndSchemaForResource(ContentInput{
		RawContent:   `{"content":[{"attrs":{"arguments":{"userId":42},"contractVersion":"sforum-shortcodes.user@1","id":"sforum-shortcodes.user"},"type":"sforumShortcodeRef"}],"type":"doc"}`,
		SourceFormat: SourceFormatEditorDocument, EditorType: EditorTypeTiptap,
	}, 160, editordocument.CoreSchema(), "topic")
	if err != nil {
		t.Fatal(err)
	}
	if first.RawContent != second.RawContent || first.ContentHash != second.ContentHash || !sameRevisionContent(first, second) {
		t.Fatalf("normalized write is not a no-op:\n%#v\n%#v", first, second)
	}
}
