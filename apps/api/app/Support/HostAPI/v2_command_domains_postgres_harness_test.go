package hostapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"google.golang.org/protobuf/proto"
)

const (
	postgresDomainCommandActorID       = int64(1001)
	postgresDomainCommandDeniedActorID = int64(1002)
)

type postgresDomainCommandHarness struct {
	ctx            context.Context
	pool           *pgxpool.Pool
	engine         *protocolV2CommandEngine
	authority      *ProtocolV2ActorDelegationAuthority
	identity       *protocolv2.ExtensionIdentity
	deniedIdentity *protocolv2.ExtensionIdentity
}

type postgresDomainCommandSpec struct {
	ID            string
	Version       string
	InputSchema   string
	SchemaVersion string
	Delegated     bool
}

func newPostgresDomainCommandHarness(t *testing.T) *postgresDomainCommandHarness {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open domain Host Command admin pool: %v", err)
	}
	schema := fmt.Sprintf("host_command_domains_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create domain Host Command schema: %v", err)
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
	installPostgresDomainCommandSchema(t, ctx, pool)
	seedPostgresDomainCommandActors(t, ctx, pool)

	identity := seedPostgresDomainCommandExtension(t, ctx, pool, "fixture.domain-command", "host_commands", "a")
	deniedIdentity := seedPostgresDomainCommandExtension(t, ctx, pool, "fixture.domain-command-denied", "own_schema", "b")
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewPostgresProtocolV2CommandRuntime(PostgresProtocolV2CommandRuntimeConfig{
		Pool: pool, ActorDelegations: authority,
		Jobs:                 supportjobs.NewDispatcher(postgresDomainCommandJobs{}),
		Moderation:           moderation.NewPostgresStore(pool),
		AttachmentStatuses:   postgresDomainAttachmentStatusMutator{},
		AttachmentExtensions: protocolV2AttachmentExtensionStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	installPostgresDomainCommandRollbackTrigger(t, ctx, pool)
	return &postgresDomainCommandHarness{
		ctx: ctx, pool: pool, engine: runtime.commandEngine(), authority: authority,
		identity: identity, deniedIdentity: deniedIdentity,
	}
}

func installPostgresDomainCommandSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	db := stdlib.OpenDB(*pool.Config().ConnConfig)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrations.Files(), ".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		// 该族迁移有意创建固定 Host schema（sforum_core_v1 全局）；六域命令不依赖它。
		if strings.Contains(name, "_stable_core_views") {
			continue
		}
		version, err := postgresDomainCommandMigrationVersion(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("install domain Host Command migration %s: %v", name, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE host_command_domain_jobs (
		  id BIGSERIAL PRIMARY KEY,
		  kind TEXT NOT NULL,
		  created_at TIMESTAMPTZ NOT NULL DEFAULT transaction_timestamp()
		)
	`); err != nil {
		t.Fatal(err)
	}
}

func postgresDomainCommandMigrationVersion(name string) (int64, error) {
	versionText, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf("migration %s has no version prefix", name)
	}
	return strconv.ParseInt(versionText, 10, 64)
}

func seedPostgresDomainCommandActors(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, actor := range []struct {
		id       int64
		username string
		role     string
	}{
		{id: postgresDomainCommandActorID, username: "domain-command-admin", role: "super_admin"},
		{id: postgresDomainCommandDeniedActorID, username: "domain-command-member", role: "member"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (
			  id, username, username_lower, email, email_lower, display_name, status
			) VALUES ($1, $2, $2, $2 || '@example.test', $2 || '@example.test', $2, 'active')
		`, actor.id, actor.username); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1, id FROM roles WHERE key = $2
		`, actor.id, actor.role); err != nil {
			t.Fatal(err)
		}
	}
}

func seedPostgresDomainCommandExtension(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	extensionID, authority, digestCharacter string,
) *protocolv2.ExtensionIdentity {
	t.Helper()
	digest := strings.Repeat(digestCharacter, 64)
	manifest := fmt.Sprintf(`{"database":{"grants":[%q]}}`, authority)
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system, is_deletable)
		VALUES ($1, 'plugin', $1, 'enabled', 'uploaded', false, true)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (
		  extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '1.0.0', $2::jsonb, '/tmp/domain-command', $3)
		RETURNING id
	`, extensionID, manifest, digest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE extensions SET active_version_id = $2 WHERE id = $1`, extensionID, versionID); err != nil {
		t.Fatal(err)
	}
	var grantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_trust_grants (
		  extension_id, extension_version, package_digest, action,
		  artifact_digests, impact_document, impact_digest, granted_by_user_id
		) VALUES ($1, '1.0.0', $2, 'enable', '{}'::jsonb, $3::jsonb, $4, $5)
		RETURNING id
	`, extensionID, digest, manifest, strings.Repeat(digestCharacter, 64), postgresDomainCommandActorID).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	return &protocolv2.ExtensionIdentity{
		ExtensionId: extensionID, ExtensionVersion: "1.0.0", ArtifactDigest: digest,
		TrustGrantId: fmt.Sprintf("%d", grantID), RuntimeEpoch: 1,
		InstanceId: extensionID + "-instance",
	}
}

func installPostgresDomainCommandRollbackTrigger(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_domain_command_rollback_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.idempotency_key LIKE 'rollback-%' THEN
		    RAISE EXCEPTION 'injected domain Host Command receipt failure';
		  END IF;
		  RETURN NEW;
		END
		$$;
		CREATE TRIGGER reject_domain_command_rollback_receipt_trigger
		BEFORE INSERT ON extension_host_command_receipts
		FOR EACH ROW EXECUTE FUNCTION reject_domain_command_rollback_receipt();
	`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
}

func (h *postgresDomainCommandHarness) request(
	t *testing.T,
	identity *protocolv2.ExtensionIdentity,
	spec postgresDomainCommandSpec,
	key string,
	input map[string]any,
	expectedRevision string,
	actorUserID int64,
) *hostv2.CommandRequest {
	t.Helper()
	document, err := protocolV2Document(spec.InputSchema, spec.SchemaVersion, input)
	if err != nil {
		t.Fatal(err)
	}
	request := &hostv2.CommandRequest{
		Context: &protocolv2.RequestContext{
			RequestId: "request-" + key, Extension: proto.Clone(identity).(*protocolv2.ExtensionIdentity),
			IdempotencyKey: key,
		},
		CommandId: spec.ID, CommandVersion: spec.Version, IdempotencyKey: key,
		ExpectedRevision: expectedRevision, Input: document,
	}
	if actorUserID > 0 {
		token, err := h.authority.IssueActorDelegation(h.ctx, ProtocolV2ActorDelegationRequest{
			ActorUserID: actorUserID, Runtime: identity, CommandID: spec.ID,
			CommandVersion: spec.Version, IdempotencyKey: key,
		})
		if err != nil {
			t.Fatal(err)
		}
		request.ActorDelegation = token
	}
	return request
}

func (h *postgresDomainCommandHarness) execute(
	identity *protocolv2.ExtensionIdentity,
	request *hostv2.CommandRequest,
) (*hostv2.CommandResult, error) {
	return h.engine.execute(ContextWithProtocolV2RuntimeIdentity(h.ctx, identity), request)
}

func (h *postgresDomainCommandHarness) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var result int
	if err := h.pool.QueryRow(h.ctx, query, args...).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

type postgresDomainAttachmentStatusMutator struct{}

func (m postgresDomainAttachmentStatusMutator) MutateProtocolV2AttachmentStatus(
	ctx context.Context,
	tx pgx.Tx,
	attachmentID int64,
	status string,
) (ProtocolV2AttachmentStatusResult, error) {
	var result ProtocolV2AttachmentStatusResult
	err := tx.QueryRow(ctx, `
		UPDATE attachments
		SET status = $2, deleted_at = NULL, updated_at = transaction_timestamp()
		WHERE id = $1
		RETURNING id, status, reference_count, updated_at
	`, attachmentID, status).Scan(&result.ID, &result.Status, &result.ReferenceCount, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProtocolV2AttachmentStatusResult{}, ErrProtocolV2AttachmentNotFound
	}
	if err != nil {
		return ProtocolV2AttachmentStatusResult{}, err
	}
	return result, nil
}

type postgresDomainCommandJobs struct{}

func (postgresDomainCommandJobs) Insert(context.Context, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return nil, errors.New("domain Host Command jobs must use the caller transaction")
}

func (postgresDomainCommandJobs) InsertTx(
	ctx context.Context,
	tx pgx.Tx,
	args river.JobArgs,
	_ *river.InsertOpts,
) (*rivertype.JobInsertResult, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO host_command_domain_jobs (kind) VALUES ($1)`, args.Kind()); err != nil {
		return nil, err
	}
	return &rivertype.JobInsertResult{}, nil
}

func (postgresDomainCommandJobs) InsertMany(context.Context, []river.InsertManyParams) ([]*rivertype.JobInsertResult, error) {
	return nil, errors.New("domain Host Command jobs must use the caller transaction")
}

var _ supportjobs.RiverClient = postgresDomainCommandJobs{}
var _ ProtocolV2AttachmentStatusMutator = postgresDomainAttachmentStatusMutator{}
