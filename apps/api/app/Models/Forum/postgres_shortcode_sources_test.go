package forum

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	contentregistry "github.com/keeptrue/sforum/apps/api/app/Support/ContentRegistry"
)

type shortcodeSourceQueryTracer struct {
	queries atomic.Int64
}

func (t *shortcodeSourceQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	t.queries.Add(1)
	return ctx
}

func (*shortcodeSourceQueryTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPostgresShortcodeSourcesVisibilityAndNoNPlusOne(t *testing.T) {
	ctx, store, tracer := newPostgresShortcodeSourceHarness(t)
	resources := make([]contentregistry.ShortcodeResourceKey, 0, 64)
	for repeat := 0; repeat < 4; repeat++ {
		for id := int64(10); id <= 18; id++ {
			resources = append(resources, contentregistry.ShortcodeResourceKey{Type: "topic", ID: id})
		}
		for id := int64(100); id <= 109; id++ {
			resources = append(resources, contentregistry.ShortcodeResourceKey{Type: "comment", ID: id})
		}
	}

	tracer.queries.Store(0)
	sources, err := store.LoadPublicShortcodeSources(ctx, resources)
	if err != nil {
		t.Fatal(err)
	}
	want := map[contentregistry.ShortcodeResourceKey]bool{
		{Type: "topic", ID: 10}: true, {Type: "topic", ID: 18}: true,
		{Type: "comment", ID: 100}: true, {Type: "comment", ID: 109}: true,
	}
	if len(sources) != len(want) {
		t.Fatalf("visible sources=%#v", sources)
	}
	for key := range want {
		if source, ok := sources[key]; !ok || source.Resource != key || source.SourceFormat != SourceFormatEditorDocument ||
			strings.TrimSpace(source.RawContent) == "" {
			t.Fatalf("source %s=%#v found=%v", key.String(), source, ok)
		}
	}
	if got := tracer.queries.Load(); got != 2 {
		t.Fatalf("duplicate IDs executed %d SQL queries, want exactly one topic batch and one comment batch", got)
	}
}

func newPostgresShortcodeSourceHarness(t *testing.T) (context.Context, *PostgresStore, *shortcodeSourceQueryTracer) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("forum_shortcode_sources_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+identifier+" CASCADE")
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	tracer := &shortcodeSourceQueryTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	statements := []string{
		`CREATE TABLE category_groups (id BIGINT PRIMARY KEY, visibility TEXT)`,
		`CREATE TABLE categories (id BIGINT PRIMARY KEY, group_id BIGINT, visibility TEXT)`,
		`CREATE TABLE posts (id BIGINT PRIMARY KEY, raw_content TEXT, source_format TEXT)`,
		`CREATE TABLE topics (id BIGINT PRIMARY KEY, category_id BIGINT, content_id BIGINT, status TEXT, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE comments (id BIGINT PRIMARY KEY, topic_id BIGINT, content_id BIGINT, status TEXT, deleted_at TIMESTAMPTZ)`,
		`INSERT INTO category_groups VALUES (1,'public'),(2,'hidden')`,
		`INSERT INTO categories VALUES (1,1,'public'),(2,1,'hidden'),(3,2,'public')`,
		`INSERT INTO posts SELECT id, '{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"source ' || id || '"}]}]}', 'editor-document' FROM generate_series(1,30) AS id`,
		`INSERT INTO topics VALUES
			(10,1,1,'active',NULL),(11,1,2,'hidden',NULL),(12,1,3,'deleted',now()),
			(13,1,4,'pending',NULL),(14,1,5,'rejected',NULL),(15,2,6,'active',NULL),
			(16,3,7,'active',NULL),(17,1,8,'active',now()),(18,1,9,'locked',NULL)`,
		`INSERT INTO comments VALUES
			(100,10,10,'active',NULL),(101,10,11,'hidden',NULL),(102,10,12,'deleted',now()),
			(103,10,13,'pending',NULL),(104,10,14,'rejected',NULL),(105,11,15,'active',NULL),
			(106,15,16,'active',NULL),(107,16,17,'active',NULL),(108,10,18,'active',now()),
			(109,18,19,'active',NULL)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("setup shortcode source schema: %v\n%s", err, statement)
		}
	}
	return ctx, NewPostgresStore(pool), tracer
}
