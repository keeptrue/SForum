package hostapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

type postgresQueryHarness struct {
	ctx      context.Context
	pool     *pgxpool.Pool
	engine   *protocolV2QueryEngine
	identity *protocolv2.ExtensionIdentity
	grantID  int64
}

func TestPostgresProtocolV2QueryRuntimeStableRowsPaginationAndPIIAbsence(t *testing.T) {
	h := newPostgresQueryHarness(t)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)

	user := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QuerySafeUserByID, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{{Field: "id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, "1")}},
	}
	user.Context.GrantedAuthority = []*protocolv2.AuthorityGrant{{Key: "database.raw_core", Source: "forged"}}
	response := h.engine.execute(ctx, user)
	if response.GetError() != nil || len(response.GetRows()) != 1 {
		t.Fatalf("safe user response = %#v", response)
	}
	userRow := response.GetRows()[0].GetValue().AsMap()
	for _, forbidden := range []string{"email", "password_hash", "locale", "is_admin", "token"} {
		if _, exists := userRow[forbidden]; exists {
			t.Fatalf("safe user leaked %s: %#v", forbidden, userRow)
		}
	}
	if userRow["id"] != "1" || userRow["username"] != "alice" {
		t.Fatalf("safe user row = %#v", userRow)
	}

	topics := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicTopicsList, PlanVersion: QueryStableCorePlanVersion,
		Fields: []string{"id", "title", "last_activity_at"}, Page: &protocolv2.PageRequest{Limit: 2},
	}
	first := h.engine.execute(ctx, topics)
	if first.GetError() != nil || len(first.GetRows()) != 2 || !first.GetPage().GetHasMore() || first.GetPage().GetNextCursor() == "" {
		t.Fatalf("topic first page = %#v", first)
	}
	secondRequest := proto.Clone(topics).(*hostv2.QueryRequest)
	secondRequest.Page = &protocolv2.PageRequest{Limit: 2, Cursor: first.GetPage().GetNextCursor()}
	second := h.engine.execute(ctx, secondRequest)
	if second.GetError() != nil || len(second.GetRows()) != 1 || second.GetPage().GetHasMore() {
		t.Fatalf("topic second page = %#v", second)
	}

	topic := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicTopicByID, PlanVersion: QueryStableCorePlanVersion,
		Fields:  []string{"id", "title"},
		Filters: []*hostv2.QueryFilter{{Field: "id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, "2")}},
	}
	topicResponse := h.engine.execute(ctx, topic)
	if topicResponse.GetError() != nil || len(topicResponse.GetRows()) != 1 || topicResponse.GetRows()[0].GetValue().AsMap()["title"] != "topic two" {
		t.Fatalf("topic by id = %#v", topicResponse)
	}

	attachment := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicAttachmentByPublicID, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{{Field: "public_id", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "public-1")}},
	}
	attachmentResponse := h.engine.execute(ctx, attachment)
	if attachmentResponse.GetError() != nil || len(attachmentResponse.GetRows()) != 1 {
		t.Fatalf("attachment response = %#v", attachmentResponse)
	}
	attachmentRow := attachmentResponse.GetRows()[0].GetValue().AsMap()
	for _, forbidden := range []string{"provider", "object_key", "storage_key", "quarantine_reason"} {
		if _, exists := attachmentRow[forbidden]; exists {
			t.Fatalf("attachment leaked %s: %#v", forbidden, attachmentRow)
		}
	}
}

func TestPostgresProtocolV2QueryRuntimeE10Surface(t *testing.T) {
	h := newPostgresQueryHarness(t)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)

	stats := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QuerySiteStats, PlanVersion: QueryStableCorePlanVersion,
	}
	statsResponse := h.engine.execute(ctx, stats)
	if statsResponse.GetError() != nil || len(statsResponse.GetRows()) != 1 {
		t.Fatalf("site stats response = %#v", statsResponse)
	}
	statsRow := statsResponse.GetRows()[0].GetValue().AsMap()
	if statsRow["topic_count"] != "3" || statsRow["comment_count"] != "2" ||
		statsRow["user_count"] != "1" || statsRow["public_attachment_count"] != "1" {
		t.Fatalf("site stats row = %#v", statsRow)
	}

	comments := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicCommentsList, PlanVersion: QueryStableCorePlanVersion,
		Fields:  []string{"id", "depth", "reply_count", "author_user_id", "parent_comment_id"},
		Filters: []*hostv2.QueryFilter{{Field: "topic_id", Operator: "eq", Value: queryParameter(t, QueryInt64ParameterSchemaID, "1")}},
		Page:    &protocolv2.PageRequest{Limit: 1},
	}
	firstComments := h.engine.execute(ctx, comments)
	if firstComments.GetError() != nil || len(firstComments.GetRows()) != 1 || !firstComments.GetPage().GetHasMore() {
		t.Fatalf("comments first page = %#v", firstComments)
	}
	commentRow := firstComments.GetRows()[0].GetValue().AsMap()
	// 默认时间序（created_at ASC, id ASC）；INTEGER→number、BIGINT→string、可空 BIGINT→null。
	if commentRow["id"] != "1" || commentRow["depth"] != float64(0) ||
		commentRow["reply_count"] != "0" || commentRow["parent_comment_id"] != nil {
		t.Fatalf("comment row = %#v", commentRow)
	}
	secondComments := proto.Clone(comments).(*hostv2.QueryRequest)
	secondComments.Page = &protocolv2.PageRequest{Limit: 1, Cursor: firstComments.GetPage().GetNextCursor()}
	secondCommentsResponse := h.engine.execute(ctx, secondComments)
	if secondCommentsResponse.GetError() != nil || len(secondCommentsResponse.GetRows()) != 1 || secondCommentsResponse.GetPage().GetHasMore() {
		t.Fatalf("comments second page = %#v", secondCommentsResponse)
	}
	if secondCommentsResponse.GetRows()[0].GetValue().AsMap()["id"] != "2" {
		t.Fatalf("comments second page row = %#v", secondCommentsResponse)
	}

	byUsername := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QuerySafeUserByUsername, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{{Field: "username", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "alice")}},
	}
	usernameResponse := h.engine.execute(ctx, byUsername)
	if usernameResponse.GetError() != nil || len(usernameResponse.GetRows()) != 1 ||
		usernameResponse.GetRows()[0].GetValue().AsMap()["id"] != "1" {
		t.Fatalf("safe user by username = %#v", usernameResponse)
	}

	batch := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QuerySafeUsersBatch, PlanVersion: QueryStableCorePlanVersion,
		Fields:  []string{"user_id", "username", "avatar_public_id"},
		Filters: []*hostv2.QueryFilter{{Field: "user_id", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "[1,2]")}},
	}
	batchResponse := h.engine.execute(ctx, batch)
	if batchResponse.GetError() != nil || len(batchResponse.GetRows()) != 2 {
		t.Fatalf("safe users batch = %#v", batchResponse)
	}
	secondBatchRow := batchResponse.GetRows()[1].GetValue().AsMap()
	if secondBatchRow["user_id"] != "2" || secondBatchRow["avatar_public_id"] != nil {
		t.Fatalf("safe users batch second row = %#v", secondBatchRow)
	}

	categories := &hostv2.QueryRequest{
		Context: testProtocolV2RequestContext(), QueryId: QueryPublicCategoriesList, PlanVersion: QueryStableCorePlanVersion,
		Filters: []*hostv2.QueryFilter{{Field: "slug", Operator: "eq", Value: queryParameter(t, QueryTextParameterSchemaID, "general")}},
	}
	categoriesResponse := h.engine.execute(ctx, categories)
	if categoriesResponse.GetError() != nil || len(categoriesResponse.GetRows()) != 1 ||
		categoriesResponse.GetRows()[0].GetValue().AsMap()["slug"] != "general" {
		t.Fatalf("public categories = %#v", categoriesResponse)
	}
}

func TestPostgresProtocolV2QueryRuntimeRejectsDeniedStaleAndRevokedAuthority(t *testing.T) {
	h := newPostgresQueryHarness(t)
	request := &hostv2.QueryRequest{Context: testProtocolV2RequestContext(), QueryId: QueryPublicTopicsList, PlanVersion: QueryStableCorePlanVersion}

	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions SET manifest = '{"database":{"authority":"own_schema"}}'::jsonb
		WHERE extension_id = $1 AND version = $2
	`, h.identity.GetExtensionId(), h.identity.GetExtensionVersion()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_trust_grants SET impact_document = '{"database":{"authority":"own_schema"}}'::jsonb
		WHERE id = $1
	`, h.grantID); err != nil {
		t.Fatal(err)
	}
	denied := h.engine.execute(ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity), request)
	if denied.GetError().GetReason() != "host.query_core_views_denied" {
		t.Fatalf("own-schema authority = %#v", denied)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions SET manifest = '{"database":{"authority":"core_views"}}'::jsonb
		WHERE extension_id = $1 AND version = $2
	`, h.identity.GetExtensionId(), h.identity.GetExtensionVersion()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_trust_grants SET impact_document = '{"database":{"authority":"core_views"}}'::jsonb
		WHERE id = $1
	`, h.grantID); err != nil {
		t.Fatal(err)
	}

	staleDigest := proto.Clone(h.identity).(*protocolv2.ExtensionIdentity)
	staleDigest.ArtifactDigest = strings.Repeat("f", 64)
	stale := h.engine.execute(ContextWithProtocolV2RuntimeIdentity(h.ctx, staleDigest), request)
	if stale.GetError().GetReason() != "host.query_runtime_stale" {
		t.Fatalf("stale digest = %#v", stale)
	}

	stagedDigest := strings.Repeat("b", 64)
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO extension_versions (extension_id, version, package_digest, manifest)
		VALUES ($1, '2.0.0', $2, '{"database":{"authority":"core_views"}}'::jsonb)
	`, h.identity.GetExtensionId(), stagedDigest); err != nil {
		t.Fatal(err)
	}
	staged := proto.Clone(h.identity).(*protocolv2.ExtensionIdentity)
	staged.ExtensionVersion = "2.0.0"
	staged.ArtifactDigest = stagedDigest
	stagedResponse := h.engine.execute(ContextWithProtocolV2RuntimeIdentity(h.ctx, staged), request)
	if stagedResponse.GetError().GetReason() != "host.query_runtime_stale" {
		t.Fatalf("inactive staged version = %#v", stagedResponse)
	}

	if _, err := h.pool.Exec(h.ctx, `UPDATE extension_trust_grants SET revoked_at = statement_timestamp() WHERE id = $1`, h.grantID); err != nil {
		t.Fatal(err)
	}
	revoked := h.engine.execute(ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity), request)
	if revoked.GetError().GetReason() != "host.query_runtime_stale" {
		t.Fatalf("revoked grant = %#v", revoked)
	}
}

func TestPostgresProtocolV2QueryAuthorityAcceptsOnlySystemBuiltinIdentity(t *testing.T) {
	h := newPostgresQueryHarness(t)
	digest := strings.Repeat("c", 64)
	var versionID int64
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO extension_versions (extension_id, version, package_digest, manifest)
		VALUES ('fixture.builtin-query', '1.0.0', $1, '{"database":{"authority":"core_views"}}'::jsonb)
		RETURNING id
	`, digest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO extensions (id, type, status, source, is_system, active_version_id)
		VALUES ('fixture.builtin-query', 'plugin', 'enabled', 'builtin', true, $1)
	`, versionID); err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "fixture.builtin-query", ExtensionVersion: "1.0.0", ArtifactDigest: digest,
		TrustGrantId: "builtin", RuntimeEpoch: 1, InstanceId: "builtin-instance",
	}
	resolver := NewPostgresProtocolV2QueryAuthorityResolver(h.pool)
	authority, err := resolver.ResolveProtocolV2QueryAuthority(h.ctx, identity)
	if err != nil || !authority.ExactArtifact || !authority.CoreViews {
		t.Fatalf("builtin authority = %#v, %v", authority, err)
	}
	if _, err := h.pool.Exec(h.ctx, `UPDATE extensions SET is_system = false WHERE id = 'fixture.builtin-query'`); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ResolveProtocolV2QueryAuthority(h.ctx, identity); err == nil {
		t.Fatal("non-system builtin identity must be stale")
	}
}

func newPostgresQueryHarness(t *testing.T) *postgresQueryHarness {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("host_query_%d", time.Now().UnixNano())
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
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	installPostgresQueryFixture(t, ctx, pool)

	const extensionID = "fixture.query"
	const extensionVersion = "1.0.0"
	digest := strings.Repeat("a", 64)
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, package_digest, manifest)
		VALUES ($1, $2, $3, '{"database":{"authority":"core_views"},"capabilities":["host.api","users.email.read"]}'::jsonb)
		RETURNING id
	`, extensionID, extensionVersion, digest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, status, source, is_system, active_version_id)
		VALUES ($1, 'plugin', 'enabled', 'uploaded', false, $2)
	`, extensionID, versionID); err != nil {
		t.Fatal(err)
	}
	var grantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_trust_grants (extension_id, extension_version, package_digest, action, impact_document)
		VALUES ($1, $2, $3, 'enable',
		        '{"database":{"authority":"core_views"},"capabilities":[{"key":"host.api"},{"key":"users.email.read"}]}'::jsonb)
		RETURNING id
	`, extensionID, extensionVersion, digest).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: extensionID, ExtensionVersion: extensionVersion, ArtifactDigest: digest,
		TrustGrantId: fmt.Sprintf("%d", grantID), RuntimeEpoch: 1, InstanceId: "query-fixture-instance",
	}
	definitions := stableCoreProtocolV2QueryDefinitions()
	for index := range definitions {
		definitions[index].From = rewritePostgresQueryFrom(definitions[index].From, identifier)
	}
	resolver := NewPostgresProtocolV2QueryAuthorityResolver(pool)
	engine, err := newProtocolV2QueryEngine(&postgresProtocolV2QueryExecutor{pool: pool}, resolver, definitions...)
	if err != nil {
		t.Fatal(err)
	}
	return &postgresQueryHarness{ctx: ctx, pool: pool, engine: engine, identity: identity, grantID: grantID}
}

func installPostgresQueryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`CREATE TABLE extension_versions (id BIGSERIAL PRIMARY KEY, extension_id TEXT NOT NULL, version TEXT NOT NULL, package_digest TEXT NOT NULL, manifest JSONB NOT NULL)`,
		`CREATE TABLE extensions (id TEXT PRIMARY KEY, type TEXT NOT NULL, status TEXT NOT NULL, source TEXT NOT NULL, is_system BOOLEAN NOT NULL, active_version_id BIGINT NOT NULL)`,
		`CREATE TABLE extension_trust_grants (id BIGSERIAL PRIMARY KEY, extension_id TEXT NOT NULL, extension_version TEXT NOT NULL, package_digest TEXT NOT NULL, action TEXT NOT NULL, impact_document JSONB NOT NULL, revoked_at TIMESTAMPTZ)`,
		`CREATE TABLE raw_users (id BIGINT, username TEXT, display_name TEXT, email TEXT, password_hash TEXT, locale TEXT, is_admin BOOLEAN, token TEXT, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
		`CREATE VIEW safe_users AS SELECT id, username, display_name, created_at, updated_at FROM raw_users`,
		`CREATE TABLE forum_topics (id BIGINT, category_id BIGINT, category_slug TEXT, author_user_id BIGINT, title TEXT, slug TEXT, status TEXT, is_pinned BOOLEAN, comment_count BIGINT, view_count BIGINT, last_activity_at TIMESTAMPTZ, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, html_content TEXT, plain_text TEXT, source_format TEXT, render_version TEXT, content_hash TEXT)`,
		`CREATE TABLE raw_attachments (id BIGINT, public_id TEXT, owner_user_id BIGINT, original_name TEXT, content_type TEXT, extension TEXT, size_bytes BIGINT, sha256 TEXT, image_width INTEGER, image_height INTEGER, reference_count BIGINT, provider TEXT, object_key TEXT, storage_key TEXT, quarantine_reason TEXT, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
		`CREATE VIEW public_attachment_metadata AS SELECT id, public_id, owner_user_id, original_name, content_type, extension, size_bytes, sha256, image_width, image_height, reference_count, created_at, updated_at FROM raw_attachments`,
		`INSERT INTO raw_users VALUES (1, 'alice', 'Alice', 'private@example.test', 'secret-hash', 'zh-CN', true, 'secret-token', now(), now()), (2, 'bob', 'Bob', 'bob@example.test', 'bob-hash', 'en-US', false, 'bob-token', now(), now())`,
		`INSERT INTO forum_topics VALUES (1, 1, 'general', 1, 'topic one', 'one', 'published', false, 0, 1, now() - interval '2 hours', now(), now(), '<p>one</p>', 'one', 'html', '1', 'h1'), (2, 1, 'general', 1, 'topic two', 'two', 'published', false, 0, 2, now() - interval '1 hour', now(), now(), '<p>two</p>', 'two', 'html', '1', 'h2'), (3, 1, 'general', 1, 'topic three', 'three', 'published', false, 0, 3, now(), now(), now(), '<p>three</p>', 'three', 'html', '1', 'h3')`,
		`INSERT INTO raw_attachments VALUES (1, 'public-1', 1, 'image.png', 'image/png', 'png', 42, 'abc', 10, 20, 1, 's3', 'private/object', 'storage-key', 'private-note', now(), now())`,
		`CREATE VIEW forum_comments AS SELECT * FROM (VALUES
			(1::bigint, 1::bigint, 1::bigint, NULL::bigint, NULL::bigint, '/1'::text, 0::int, 0::bigint, now(), now(), '<p>c1</p>'::text, 'c1', 'html', '1', 'h1'),
			(2::bigint, 1::bigint, 2::bigint, 1::bigint, 1::bigint, '/1/2'::text, 1::int, 0::bigint, now(), now(), '<p>c2</p>'::text, 'c2', 'html', '1', 'h2')
		) AS t(id, topic_id, author_user_id, parent_comment_id, root_comment_id, path_key, depth, reply_count, created_at, updated_at, html_content, plain_text, source_format, render_version, content_hash)`,
		`CREATE VIEW site_stats AS SELECT 3::bigint AS topic_count, 2::bigint AS comment_count, 1::bigint AS user_count, 1::bigint AS public_attachment_count OFFSET 0`,
		`CREATE TABLE user_profiles (user_id BIGINT PRIMARY KEY, avatar_attachment_id BIGINT)`,
		`INSERT INTO user_profiles VALUES (1, 1)`,
		`CREATE VIEW safe_user_avatars AS SELECT u.id AS user_id, u.username, u.display_name, a.public_id AS avatar_public_id, a.content_type AS avatar_content_type, a.image_width AS avatar_image_width, a.image_height AS avatar_image_height FROM raw_users u LEFT JOIN user_profiles p ON p.user_id = u.id LEFT JOIN raw_attachments a ON a.id = p.avatar_attachment_id`,
		`CREATE VIEW public_categories AS SELECT * FROM (VALUES (1::bigint, 'general'::text, 'General'::text, 'General discussion'::text, 'chat'::text, 'emerald'::text)) AS t(id, slug, name, description, icon, icon_color)`,
		`CREATE TABLE audit_events (id BIGSERIAL PRIMARY KEY, actor_user_id BIGINT, target_user_id BIGINT, action TEXT NOT NULL, metadata JSONB NOT NULL DEFAULT '{}'::jsonb, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE users (id BIGINT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT NOT NULL, email TEXT NOT NULL, email_lower TEXT NOT NULL UNIQUE, status TEXT NOT NULL, email_verified_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`INSERT INTO users VALUES
			(42, 'consent-a', 'Consent A', 'a.consent@example.test', 'a.consent@example.test', 'active', now(), now(), now()),
			(43, 'consent-b', 'Consent B', 'b.consent@example.test', 'b.consent@example.test', 'active', NULL, now(), now()),
			(44, 'consent-off', 'Consent Off', 'off.consent@example.test', 'off.consent@example.test', 'disabled', now(), now(), now())`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("install Host Query fixture: %v\n%s", err, statement)
		}
	}
}

// rewritePostgresQueryFrom 把 Host 定义里的真实 schema 前缀改写到隔离 fixture
// schema：`sforum_core_v1.*` 与 `public.*` 两种写法都要覆盖，后者用于 consent
// 读取面（public.users）。
func rewritePostgresQueryFrom(from string, identifier string) string {
	if strings.Contains(from, "public.") {
		return strings.ReplaceAll(from, "public.", identifier+".")
	}
	parts := strings.SplitN(from, ".", 2)
	if len(parts) == 2 {
		return identifier + "." + parts[1]
	}
	return from
}

func TestPostgresConsentSelfEmailReadsOnlyActorOwnEmail(t *testing.T) {
	h := newPostgresQueryHarness(t)
	catalog, err := NewQueryRegistryCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	registry, _, err := NewQueryRegistryCoreRegistry(QueryRegistryCoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewProtocolV2QueryRegistryProviderResolver(
		&protocolV2QueryRuntime{engine: h.engine}, catalog.Bindings(),
	)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := queryregistry.NewJSONResultSchemaCatalog(catalog.Schemas())
	if err != nil {
		t.Fatal(err)
	}
	execution, err := queryregistry.NewExecutionRuntime(queryregistry.ExecutionConfig{
		Registry: registry, Providers: providers, Schemas: schemas, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	delegations, err := newProtocolV2QueryDelegationAuthority(
		[]byte("0123456789abcdef0123456789abcdef"), time.Now, protocolV2QueryDelegationTTL,
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newProtocolV2QueryRegistryService(
		registry, execution, consentIntegrationActors{}, consentIntegrationAdmission{}, delegations,
		WithProtocolV2QueryConsentGate(&ProtocolV2QueryConsentGate{
			Capabilities: consentIntegrationCapabilities{},
			Auditor:      audit.NewPostgresWriter(h.pool),
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	read := func(actorUserID int64) *hostv2.QueryResponse {
		grant, issueErr := service.IssueProtocolV2QueryActorDelegation(
			context.Background(), ProtocolV2QueryActorDelegationRequest{
				ActorUserID: actorUserID, Runtime: h.identity, QueryID: QuerySelfEmailRegistryID,
				Locale: "zh-CN", Scope: ProtocolV2ConsentQueryScope, MaxCost: ProtocolV2ConsentQueryMaxCost,
			},
		)
		if issueErr != nil {
			t.Fatalf("issue actor %d: %v", actorUserID, issueErr)
		}
		request := &hostv2.QueryRequest{
			Context: &protocolv2.RequestContext{
				RequestId: fmt.Sprintf("consent-request-%d", actorUserID), Locale: "zh-CN",
				Extension: cloneProtocolV2ExtensionIdentity(h.identity),
			},
			QueryId: grant.QueryID, ContractVersion: grant.ContractVersion, PlanVersion: grant.PlanVersion,
			ResultSchemaId: grant.ResultSchemaID, ResultSchemaVersion: grant.ResultSchemaVersion,
			Scope: grant.Scope, ActorDelegation: grant.Token, Page: &protocolv2.PageRequest{Limit: 1},
		}
		return service.execute(ContextWithProtocolV2RuntimeIdentity(context.Background(), h.identity), request)
	}

	actorA := read(42)
	if actorA.GetError() != nil || len(actorA.GetRows()) != 1 {
		t.Fatalf("actor A response = %#v err=%v", actorA.GetRows(), actorA.GetError())
	}
	rowA := actorA.GetRows()[0].GetValue().AsMap()
	if rowA["email"] != "a.consent@example.test" || rowA["email_verified"] != true {
		t.Fatalf("actor A row = %#v", rowA)
	}
	if _, leaked := rowA["email_lower"]; leaked {
		t.Fatalf("result exposed email_lower: %#v", rowA)
	}

	// actor B 只能读到 B 自己的 email（且验证位来自 NULL email_verified_at）。
	actorB := read(43)
	if actorB.GetError() != nil || len(actorB.GetRows()) != 1 {
		t.Fatalf("actor B response = %#v", actorB)
	}
	rowB := actorB.GetRows()[0].GetValue().AsMap()
	if rowB["email"] != "b.consent@example.test" || rowB["email_verified"] != false {
		t.Fatalf("actor B row = %#v", rowB)
	}

	// 非 active 账号：空行集合，不报错（SQL 侧 defense in depth）。
	disabled := read(44)
	if disabled.GetError() != nil || len(disabled.GetRows()) != 0 {
		t.Fatalf("disabled account response = %#v err=%v", disabled.GetRows(), disabled.GetError())
	}

	// email_lower 不是可选字段：请求它必须被计划拒绝。
	lowerGrant, err := service.IssueProtocolV2QueryActorDelegation(
		context.Background(), ProtocolV2QueryActorDelegationRequest{
			ActorUserID: 42, Runtime: h.identity, QueryID: QuerySelfEmailRegistryID,
			Locale: "zh-CN", Scope: ProtocolV2ConsentQueryScope, MaxCost: ProtocolV2ConsentQueryMaxCost,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	lowerRequest := &hostv2.QueryRequest{
		Context: &protocolv2.RequestContext{
			RequestId: "consent-request-lower", Locale: "zh-CN",
			Extension: cloneProtocolV2ExtensionIdentity(h.identity),
		},
		QueryId: lowerGrant.QueryID, ContractVersion: lowerGrant.ContractVersion,
		PlanVersion: lowerGrant.PlanVersion, ResultSchemaId: lowerGrant.ResultSchemaID,
		ResultSchemaVersion: lowerGrant.ResultSchemaVersion, Scope: lowerGrant.Scope,
		ActorDelegation: lowerGrant.Token, Fields: []string{"email_lower"},
	}
	lower := service.execute(ContextWithProtocolV2RuntimeIdentity(context.Background(), h.identity), lowerRequest)
	if lower.GetError() == nil || len(lower.GetRows()) != 0 {
		t.Fatalf("email_lower request = %#v", lower)
	}

	// actorless 稳定入口仍然拒绝。
	stable := h.engine.execute(
		ContextWithProtocolV2RuntimeIdentity(context.Background(), h.identity),
		&hostv2.QueryRequest{Context: testProtocolV2RequestContext(), QueryId: QuerySelfEmail, PlanVersion: QueryStableCorePlanVersion},
	)
	if stable.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
		t.Fatalf("stable entry = %#v", stable.GetError())
	}

	// 真实审计行：动作正确、actor 正确、metadata 不含 email 值。
	var audits int
	var metadata string
	if err := h.pool.QueryRow(context.Background(), `
		SELECT count(*), COALESCE(max(metadata::text), '')
		FROM audit_events WHERE action = $1
	`, ProtocolV2ConsentQueryAuditAction).Scan(&audits, &metadata); err != nil {
		t.Fatal(err)
	}
	if audits != 3 {
		t.Fatalf("audit rows = %d", audits)
	}
	for _, secret := range []string{"a.consent@example.test", "b.consent@example.test"} {
		if strings.Contains(metadata, secret) {
			t.Fatalf("audit metadata leaked %s: %s", secret, metadata)
		}
	}
	if !strings.Contains(metadata, QuerySelfEmailRegistryID) {
		t.Fatalf("audit metadata missing query id: %s", metadata)
	}
}

type consentIntegrationActors struct{}

func (consentIntegrationActors) projection(actorUserID int64) ProtocolV2QueryActorProjection {
	return ProtocolV2QueryActorProjection{
		ActorUserID: actorUserID, Authenticated: true,
		ActorFingerprint: fmt.Sprintf("actor:%d", actorUserID), PolicyFingerprint: "policy:member",
	}
}

func (a consentIntegrationActors) ResolveProtocolV2QueryActor(_ context.Context, actorUserID int64) (ProtocolV2QueryActorProjection, error) {
	if actorUserID != 42 && actorUserID != 43 && actorUserID != 44 {
		return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
	}
	return a.projection(actorUserID), nil
}

func (a consentIntegrationActors) AuthorizeProtocolV2QueryActor(
	_ context.Context,
	actorUserID int64,
	claim queryregistry.PermissionClaim,
) (ProtocolV2QueryActorProjection, error) {
	if claim.PermissionPolicy != queryregistry.PermissionPolicyLogin ||
		(actorUserID != 42 && actorUserID != 43 && actorUserID != 44) {
		return ProtocolV2QueryActorProjection{}, ErrProtocolV2QueryActorDenied
	}
	return a.projection(actorUserID), nil
}

type consentIntegrationAdmission struct{}

func (consentIntegrationAdmission) AuthorizeProtocolV2QueryCaller(context.Context, *protocolv2.ExtensionIdentity) error {
	return nil
}

type consentIntegrationCapabilities struct{}

func (consentIntegrationCapabilities) AuthorizeProtocolV2QueryCapability(
	context.Context,
	*protocolv2.ExtensionIdentity,
	string,
) error {
	return nil
}
