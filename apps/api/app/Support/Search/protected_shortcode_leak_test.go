package search_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	editordocument "github.com/keeptrue/sforum/apps/api/app/Support/EditorDocument"
	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

const protectedSearchMarker = "M8_SEARCH_SECRET_MARKER_7F4C2D"

type protectedSearchTopicReader struct {
	doc search.TopicSearchDoc
}

func (r protectedSearchTopicReader) GetTopicForSearch(context.Context, int64) (search.TopicSearchDoc, error) {
	return r.doc, nil
}

func TestProtectedShortcodeNeverEntersSearchIndexOrRebuild(t *testing.T) {
	t.Parallel()

	raw := `{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"m8-public-search-token"}]},{"type":"sforumShortcodeBlock","attrs":{"id":"sforum-shortcodes.login","contractVersion":"sforum-shortcodes.login@1","arguments":{}},"content":[{"type":"paragraph","content":[{"type":"text","text":"` + protectedSearchMarker + `"}]}]}]}`
	rendered, err := forum.RenderContentWithExcerptLimitAndSchemaForResource(forum.ContentInput{
		RawContent: raw, SourceFormat: forum.SourceFormatEditorDocument, EditorType: forum.EditorTypeTiptap, Locale: "en-US",
	}, forum.RecommendedExcerptRuneLimit, editordocument.Schema{}, "topic")
	if err != nil {
		t.Fatalf("render protected editor document: %v", err)
	}
	for name, value := range map[string]string{
		"html": rendered.HTMLContent, "plain": rendered.PlainText, "excerpt": rendered.Excerpt,
	} {
		if strings.Contains(value, protectedSearchMarker) {
			t.Fatalf("%s projection leaked protected marker: %q", name, value)
		}
	}

	now := time.Now().UTC()
	doc := search.TopicSearchDoc{
		ID: 808, Title: "M8 search projection", Excerpt: rendered.Excerpt, PlainText: rendered.PlainText,
		Status: forum.TopicStatusActive, CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}
	engine := search.NewMemorySiteEngine()
	indexer := search.NewIndexer(engine, protectedSearchTopicReader{doc: doc}, nil)

	// A second upsert exercises the same path used by a rebuild: the authoritative
	// actor-independent public projection replaces the prior derived document.
	for attempt := 0; attempt < 2; attempt++ {
		if err := indexer.IndexTopic(t.Context(), doc.ID); err != nil {
			t.Fatalf("index attempt %d: %v", attempt+1, err)
		}
	}

	secretResult, err := engine.Search(t.Context(), search.SearchInput{Query: protectedSearchMarker, Page: 1, PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if secretResult.Total != 0 || len(secretResult.Items) != 0 {
		t.Fatalf("protected marker was searchable after rebuild: %#v", secretResult)
	}

	publicResult, err := search.NewService(engine, nil).Search(t.Context(), search.SearchInput{Query: "m8-public-search-token", Page: 1, PerPage: 10})
	if err != nil {
		t.Fatal(err)
	}
	if publicResult.Total != 1 || len(publicResult.Items) != 1 || publicResult.Items[0].ID != doc.ID {
		t.Fatalf("public projection was not searchable: %#v", publicResult)
	}
	body, err := json.Marshal(publicResult)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), protectedSearchMarker) || publicResult.Items[0].PlainText != "" {
		t.Fatalf("public search response leaked index-only or protected content: %s", body)
	}
}
