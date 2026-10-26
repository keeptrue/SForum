package forum

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

func TestProtectedShortcodeRedisCachesOnlyActorIndependentBase(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("SFORUM_TEST_REDIS_ADDR"))
	if address == "" {
		t.Skip("SFORUM_TEST_REDIS_ADDR is required for protected cache Redis integration")
	}
	database := 0
	if raw := strings.TrimSpace(os.Getenv("SFORUM_TEST_REDIS_DB")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("invalid SFORUM_TEST_REDIS_DB %q", raw)
		}
		database = parsed
	}
	client := redis.NewClient(&redis.Options{
		Addr: address, Password: os.Getenv("SFORUM_TEST_REDIS_PASSWORD"), DB: database,
	})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("ping Redis: %v", err)
	}

	const marker = "M8_REDIS_SECRET_MARKER_51BA70"
	topicID := time.Now().UnixNano()
	slug := "m8-protected-redis-" + strconv.FormatInt(topicID, 10)
	rendered, err := RenderContentWithExcerptLimit(ContentInput{
		RawContent: publicProtectedShortcodeSourceJSON(marker), SourceFormat: SourceFormatEditorDocument, EditorType: EditorTypeTiptap, Locale: "en-US",
	}, RecommendedExcerptRuneLimit)
	if err != nil {
		t.Fatalf("render public base: %v", err)
	}
	resource := contentregistry.ShortcodeResourceKey{Type: "topic", ID: topicID}
	inner := &publicShortcodeReadStore{
		Store: newServiceFakeStore(),
		topic: TopicDetail{
			TopicSummary: TopicSummary{ID: topicID, Slug: slug, Status: TopicStatusActive},
			Content:      ToPublicRenderedContent(rendered),
		},
		sources: map[contentregistry.ShortcodeResourceKey]PublicShortcodeSource{
			resource: {Resource: resource, RawContent: publicProtectedShortcodeSourceJSON(marker), SourceFormat: SourceFormatEditorDocument},
		},
	}
	redisCache := cache.NewRedisCache(client)
	cached := NewCachedStore(inner, redisCache)
	reads := NewPublicReadService(NewService(ServiceConfig{Store: cached})).WithProtectedShortcodes(
		ProtectedShortcodeAuthorizerFunc(func(_ context.Context, request ProtectedShortcodeAuthorizationRequest) (ProtectedShortcodeDecision, error) {
			return ProtectedShortcodeDecision{Allowed: request.Viewer.ID == 71001}, nil
		}),
		&protectedShortcodeCaptureDispatcher{},
	)

	denied, err := reads.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), topicID, identity.Actor{ID: 71002})
	if err != nil || strings.Contains(denied.Content.HTMLContent, marker) || strings.Contains(denied.Content.HTMLContent, "AUTHORIZED_FRAGMENT") {
		t.Fatalf("denied Redis-backed read leaked: %#v err=%v", denied.Content, err)
	}
	allowed, err := reads.GetTopicForViewer(WithPublicRenderLocale(t.Context(), "en-US"), topicID, identity.Actor{ID: 71001})
	if err != nil || !strings.Contains(allowed.Content.HTMLContent, "AUTHORIZED_FRAGMENT") || strings.Contains(allowed.Content.HTMLContent, marker) {
		t.Fatalf("allowed Redis-backed read invalid: %#v err=%v", allowed.Content, err)
	}

	keys := []string{
		prefixTopicDetail + strconv.FormatInt(topicID, 10),
		prefixTopicBySlug + slug,
		prefixTopicIDSlug + strconv.FormatInt(topicID, 10),
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), keys...).Err() })
	for _, key := range keys {
		if strings.Contains(key, marker) || strings.Contains(strings.ToLower(key), "viewer") {
			t.Fatalf("Redis key contains request-only identity: %q", key)
		}
		value, getErr := client.Get(t.Context(), key).Result()
		if getErr == redis.Nil {
			continue
		}
		if getErr != nil {
			t.Fatalf("read Redis key %q: %v", key, getErr)
		}
		for _, forbidden := range []string{marker, "AUTHORIZED_FRAGMENT", "rawContent", "contentHash", "viewer", "authorization"} {
			if strings.Contains(value, forbidden) {
				t.Fatalf("Redis value %q contains request-only %q: %s", key, forbidden, value)
			}
		}
	}
}
