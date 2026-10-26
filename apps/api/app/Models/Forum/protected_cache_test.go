package forum

import (
	"context"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

func TestProtectedShortcodeProcessCacheContainsOnlyPublicBase(t *testing.T) {
	t.Parallel()

	const marker = "M8_PROCESS_CACHE_SECRET_MARKER_4E18C7"
	rendered, err := RenderContentWithExcerptLimit(ContentInput{
		RawContent: publicProtectedShortcodeSourceJSON(marker), SourceFormat: SourceFormatEditorDocument, EditorType: EditorTypeTiptap, Locale: "en-US",
	}, RecommendedExcerptRuneLimit)
	if err != nil {
		t.Fatal(err)
	}
	resource := contentregistry.ShortcodeResourceKey{Type: "topic", ID: 909}
	inner := &publicShortcodeReadStore{
		Store: newServiceFakeStore(),
		topic: TopicDetail{
			TopicSummary: TopicSummary{ID: resource.ID, Slug: "m8-process-cache", Status: TopicStatusActive},
			Content:      ToPublicRenderedContent(rendered),
		},
		sources: map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource{
			resource: {Resource: resource, RawContent: publicProtectedShortcodeSourceJSON(marker), SourceFormat: SourceFormatEditorDocument},
		},
	}
	memory := cache.NewMemoryCache()
	reads := NewPublicReadService(NewService(ServiceConfig{Store: NewCachedStore(inner, memory)})).WithProtectedShortcodes(
		ProtectedShortcodeAuthorizerFunc(func(_ context.Context, request ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error) {
			return ProtectedShortcodeDecision{Allowed: request.Viewer.ID == 81001}, nil
		}),
		&protectedShortcodeCaptureDispatcher{},
	)

	for _, viewer := range []identity.Actor{{ID: 81002}, {ID: 81001}} {
		if _, err := reads.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), resource.ID, viewer); err != nil {
			t.Fatal(err)
		}
	}
	value, found, err := memory.Get(t.Context(), prefixTopicDetail+"909")
	if err != nil || !found {
		t.Fatalf("process cache base missing: found=%v err=%v", found, err)
	}
	serialized := string(value)
	for _, forbidden := range []string{marker, "AUTHORIZED_FRAGMENT", "rawContent", "contentHash", "viewer", "authorization"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("process cache contains request-only %q: %s", forbidden, serialized)
		}
	}
}
