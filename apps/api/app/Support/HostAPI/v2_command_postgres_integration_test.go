package hostapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
)

const (
	postgresCommandTestID            = "core.command.test.write"
	postgresCommandTestVersion       = "1"
	postgresCommandInputSchema       = "sforum.test.command.input"
	postgresCommandOutputSchema      = "sforum.test.command.output"
	postgresCommandTestSchemaVersion = "1"
)

type postgresCommandHarness struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	backend   *PostgresProtocolV2HostCommandBackend
	identity  *protocolv2.ExtensionIdentity
	versionID int64
	grantID   int64
}

func TestPostgresProtocolV2CommandBackendConcurrentReplayConflictAndAuditRetention(t *testing.T) {
	h := newPostgresCommandHarness(t)
	engine := newPostgresCommandEngine(t, h, false)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)
	request := postgresCommandRequest(h.identity, "shared-key", "first")

	start := make(chan struct{})
	results := make([]*hostv2.CommandResult, 2)
	errorsByWorker := make([]error, 2)
	var workers sync.WaitGroup
	for index := range results {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			results[index], errorsByWorker[index] = engine.execute(ctx, proto.Clone(request).(*hostv2.CommandRequest))
		}(index)
	}
	close(start)
	workers.Wait()

	states := map[hostv2.CommandState]int{}
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("worker %d: %v", index, err)
		}
		states[results[index].GetState()]++
	}
	if states[hostv2.CommandState_COMMAND_STATE_COMMITTED] != 1 ||
		states[hostv2.CommandState_COMMAND_STATE_REPLAYED] != 1 {
		t.Fatalf("concurrent states = %#v", states)
	}
	assertPostgresCommandCounts(t, h, "shared-key", 1, 1, 1)
	assertPostgresCommandExactReceipt(t, h, "shared-key")

	if _, err := h.pool.Exec(h.ctx, `DELETE FROM audit_events`); err != nil {
		t.Fatalf("audit retention delete: %v", err)
	}
	replayed, err := engine.execute(ctx, proto.Clone(request).(*hostv2.CommandRequest))
	if err != nil || replayed.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED {
		t.Fatalf("replay after audit retention = %#v, %v", replayed, err)
	}
	assertPostgresCommandCounts(t, h, "shared-key", 1, 0, 1)

	conflictRequest := postgresCommandRequest(h.identity, "shared-key", "different")
	conflict, err := engine.execute(ctx, conflictRequest)
	if err != nil || conflict.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		conflict.GetError().GetReason() != "host.command_idempotency_conflict" {
		t.Fatalf("fingerprint conflict = %#v, %v", conflict, err)
	}
	assertPostgresCommandCounts(t, h, "shared-key", 1, 0, 1)
}

func TestPostgresProtocolV2CommandBackendResolvesServerIdentity(t *testing.T) {
	h := newPostgresCommandHarness(t)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)
	tx, err := h.backend.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := h.backend.ResolveScope(ctx, tx, protocolV2CommandScope{
		ExtensionID: h.identity.GetExtensionId(), ExtensionVersionID: 999,
		ExtensionVersion: "forged", PackageDigest: strings.Repeat("f", 64),
		AuthorityType: "builtin", CommandID: postgresCommandTestID,
		CommandVersion: postgresCommandTestVersion, IdempotencyKey: "server-identity",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ExtensionVersionID != h.versionID || resolved.ExtensionVersion != h.identity.GetExtensionVersion() ||
		resolved.PackageDigest != h.identity.GetArtifactDigest() || resolved.AuthorityType != "trust_grant" ||
		resolved.TrustGrantID != h.grantID {
		t.Fatalf("resolved untrusted request identity: %#v", resolved)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	expectStale := func(label string, callCtx context.Context) {
		t.Helper()
		candidate, beginErr := h.backend.Begin(h.ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer candidate.Rollback(h.ctx)
		_, resolveErr := h.backend.ResolveScope(callCtx, candidate, protocolV2CommandScope{
			ExtensionID: h.identity.GetExtensionId(), ExtensionVersionID: h.versionID,
			ExtensionVersion: h.identity.GetExtensionVersion(), PackageDigest: h.identity.GetArtifactDigest(),
			AuthorityType: "trust_grant", TrustGrantID: h.grantID,
			CommandID: postgresCommandTestID, CommandVersion: postgresCommandTestVersion,
			IdempotencyKey: label,
		})
		var commandErr *protocolV2CommandError
		if !errors.As(resolveErr, &commandErr) || commandErr.detail.GetReason() != "host.command_identity_stale" {
			t.Fatalf("%s resolved without exact server authority: %v", label, resolveErr)
		}
	}
	expectStale("request-only-identity", context.Background())

	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_trust_grants SET action = 'frontend_import' WHERE id = $1
	`, h.grantID); err != nil {
		t.Fatal(err)
	}
	expectStale("wrong-grant-action", ctx)
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_trust_grants
		SET action = 'enable', revoked_at = statement_timestamp()
		WHERE id = $1
	`, h.grantID); err != nil {
		t.Fatal(err)
	}
	expectStale("revoked-grant", ctx)
	if _, err := h.pool.Exec(h.ctx, `DELETE FROM extension_trust_grants WHERE id = $1`, h.grantID); err != nil {
		t.Fatal(err)
	}
	expectStale("missing-grant", ctx)
}

func TestPostgresProtocolV2HostCommandBackendRequiresExactDeclaredAuthority(t *testing.T) {
	h := newPostgresCommandHarness(t)
	resolve := func(label string, identity *protocolv2.ExtensionIdentity) (protocolV2CommandScope, error) {
		t.Helper()
		ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, identity)
		tx, err := h.backend.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(h.ctx)
		return h.backend.ResolveScope(ctx, tx, protocolV2CommandScope{
			ExtensionID: identity.GetExtensionId(), CommandID: postgresCommandTestID,
			CommandVersion: postgresCommandTestVersion, IdempotencyKey: label,
		})
	}
	assertReason := func(label string, err error, reason string) {
		t.Helper()
		var commandErr *protocolV2CommandError
		if !errors.As(err, &commandErr) || commandErr.detail.GetReason() != reason {
			t.Fatalf("%s error = %v, want %s", label, err, reason)
		}
	}
	setAuthority := func(manifest, impact string) {
		t.Helper()
		if _, err := h.pool.Exec(h.ctx, `
			UPDATE extension_versions SET manifest = $2::jsonb WHERE id = $1
		`, h.versionID, manifest); err != nil {
			t.Fatal(err)
		}
		if _, err := h.pool.Exec(h.ctx, `
			UPDATE extension_trust_grants SET impact_document = $2::jsonb WHERE id = $1
		`, h.grantID, impact); err != nil {
			t.Fatal(err)
		}
	}

	resolved, err := resolve("declared-authority", h.identity)
	if err != nil || resolved.AuthorityType != "trust_grant" || resolved.TrustGrantID != h.grantID {
		t.Fatalf("exact Host Command authority = %#v, %v", resolved, err)
	}

	setAuthority(`{"database":{"grants":["host_commands"]}}`, `{"database":{"grants":["host_commands"]}}`)
	if additive, additiveErr := resolve("additive-authority", h.identity); additiveErr != nil || additive.TrustGrantID != h.grantID {
		t.Fatalf("additive Host Command authority = %#v, %v", additive, additiveErr)
	}
	setAuthority(`{"database":{"authority":"raw_core"}}`, `{"database":{"authority":"raw_core"}}`)
	if cumulative, cumulativeErr := resolve("cumulative-legacy-authority", h.identity); cumulativeErr != nil || cumulative.TrustGrantID != h.grantID {
		t.Fatalf("cumulative legacy Host Command authority = %#v, %v", cumulative, cumulativeErr)
	}
	setAuthority(
		`{"database":{"grants":["host_commands"]}}`,
		`{"database":{"grants":["own_schema","host_commands"]}}`,
	)
	_, err = resolve("mismatched-additive-authority", h.identity)
	assertReason("mismatched additive authority", err, "host.command_authority_denied")

	setAuthority(`{"database":{"authority":"own_schema"}}`, `{"database":{"authority":"own_schema"}}`)
	request := postgresCommandRequest(h.identity, "forged-request-authority", "value")
	request.Context.GrantedAuthority = []*protocolv2.AuthorityGrant{{
		Key: protocolV2HostCommandLegacyAuthority, Source: "plugin-request",
	}}
	denied, err := newPostgresCommandEngine(t, h, false).execute(
		ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity), request,
	)
	if err != nil || denied.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED ||
		denied.GetError().GetReason() != "host.command_authority_denied" {
		t.Fatalf("forged request authority = %#v, %v", denied, err)
	}
	assertPostgresCommandCounts(t, h, "forged-request-authority", 0, 0, 0)

	setAuthority(`{}`, `{"database":{"authority":"host_commands"}}`)
	_, err = resolve("missing-manifest-authority", h.identity)
	assertReason("missing manifest authority", err, "host.command_authority_denied")

	setAuthority(`{"database":{"authority":"host_commands"}}`, `{"database":{"authority":"raw_core"}}`)
	_, err = resolve("mismatched-trust-authority", h.identity)
	assertReason("mismatched trust authority", err, "host.command_authority_denied")

	setAuthority(`{"database":{"authority":"host_commands"}}`, `{}`)
	_, err = resolve("missing-trust-authority", h.identity)
	assertReason("missing trust authority", err, "host.command_authority_denied")

	setAuthority(`{"database":{"authority":"host_commands"}}`, `{"database":{"authority":"host_commands"}}`)
	stale := proto.Clone(h.identity).(*protocolv2.ExtensionIdentity)
	stale.ArtifactDigest = strings.Repeat("f", 64)
	_, err = resolve("stale-artifact", stale)
	assertReason("stale artifact", err, "host.command_identity_stale")

	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_trust_grants SET revoked_at = statement_timestamp() WHERE id = $1
	`, h.grantID); err != nil {
		t.Fatal(err)
	}
	_, err = resolve("revoked-authority", h.identity)
	assertReason("revoked authority", err, "host.command_identity_stale")
}

func TestPostgresProtocolV2HostCommandBackendRequiresBuiltinManifestAuthority(t *testing.T) {
	h := newPostgresCommandHarness(t)
	const extensionID = "fixture.builtin-command"
	const version = "1.0.0"
	digest := strings.Repeat("b", 64)
	if _, err := h.pool.Exec(h.ctx, `
		INSERT INTO extensions (id, source, is_system) VALUES ($1, 'builtin', true)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := h.pool.QueryRow(h.ctx, `
		INSERT INTO extension_versions (extension_id, version, package_digest, manifest)
		VALUES ($1, $2, $3, '{"database":{"authority":"host_commands"}}'::jsonb)
		RETURNING id
	`, extensionID, version, digest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: extensionID, ExtensionVersion: version, ArtifactDigest: digest,
		TrustGrantId: "builtin", RuntimeEpoch: 1, InstanceId: "builtin-command-runtime",
	}
	resolve := func(label string) (protocolV2CommandScope, error) {
		t.Helper()
		ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, identity)
		tx, err := h.backend.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(h.ctx)
		return h.backend.ResolveScope(ctx, tx, protocolV2CommandScope{
			ExtensionID: extensionID, CommandID: postgresCommandTestID,
			CommandVersion: postgresCommandTestVersion, IdempotencyKey: label,
		})
	}
	resolved, err := resolve("builtin-allowed")
	if err != nil || resolved.ExtensionVersionID != versionID || resolved.AuthorityType != "builtin" || resolved.TrustGrantID != 0 {
		t.Fatalf("builtin Host Command authority = %#v, %v", resolved, err)
	}

	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions SET manifest = '{"database":{"authority":"core_views"}}'::jsonb
		WHERE id = $1
	`, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve("builtin-wrong-authority"); err == nil {
		t.Fatal("builtin without host_commands manifest authority was accepted")
	} else {
		var commandErr *protocolV2CommandError
		if !errors.As(err, &commandErr) || commandErr.detail.GetReason() != "host.command_authority_denied" {
			t.Fatalf("builtin wrong authority = %v", err)
		}
	}

	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extension_versions SET manifest = '{"database":{"authority":"host_commands"}}'::jsonb WHERE id = $1
	`, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `
		UPDATE extensions SET is_system = false WHERE id = $1
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve("builtin-non-system"); err == nil {
		t.Fatal("non-system builtin Host Command identity was accepted")
	} else {
		var commandErr *protocolV2CommandError
		if !errors.As(err, &commandErr) || commandErr.detail.GetReason() != "host.command_identity_stale" {
			t.Fatalf("non-system builtin = %v", err)
		}
	}
}

func TestPostgresProtocolV2CommandBackendRollsBackEveryFailureBoundary(t *testing.T) {
	tests := []struct {
		name     string
		business bool
		trigger  string
	}{
		{name: "business", business: true},
		{name: "audit", trigger: "audit_events"},
		{name: "receipt", trigger: "extension_host_command_receipts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newPostgresCommandHarness(t)
			if test.trigger != "" {
				installPostgresCommandFailureTrigger(t, h, test.trigger)
			}
			engine := newPostgresCommandEngine(t, h, test.business)
			ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)
			result, err := engine.execute(ctx, postgresCommandRequest(h.identity, test.name+"-failure", "value"))
			if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK {
				t.Fatalf("failure result = %#v, %v", result, err)
			}
			assertPostgresCommandCounts(t, h, test.name+"-failure", 0, 0, 0)
		})
	}
}

func newPostgresCommandHarness(t *testing.T) *postgresCommandHarness {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for destructive Host Command integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open Host Command integration admin pool: %v", err)
	}
	schema := fmt.Sprintf("host_command_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create Host Command integration schema: %v", err)
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
	installPostgresCommandPrerequisites(t, ctx, pool)
	installPostgresCommandMigrations(t, ctx, pool)

	const extensionID = "fixture.command"
	const extensionVersion = "1.0.0"
	packageDigest := strings.Repeat("a", 64)
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, source, is_system) VALUES ($1, 'uploaded', false)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (extension_id, version, package_digest, manifest)
		VALUES ($1, $2, $3, '{"database":{"authority":"host_commands"}}'::jsonb) RETURNING id
	`, extensionID, extensionVersion, packageDigest).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	var grantID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action, impact_document, revoked_at
		) VALUES ($1, $2, $3, 'enable', '{"database":{"authority":"host_commands"}}'::jsonb, NULL)
		RETURNING id
	`, extensionID, extensionVersion, packageDigest).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: extensionID, ExtensionVersion: extensionVersion,
		ArtifactDigest: packageDigest, TrustGrantId: fmt.Sprintf("%d", grantID),
		RuntimeEpoch: 1, InstanceId: "fixture-instance",
	}
	return &postgresCommandHarness{
		ctx: ctx, pool: pool, backend: NewPostgresProtocolV2HostCommandBackend(pool),
		identity: identity, versionID: versionID, grantID: grantID,
	}
}

func installPostgresCommandPrerequisites(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE extensions (id TEXT PRIMARY KEY, source TEXT NOT NULL, is_system BOOLEAN NOT NULL)`,
		`CREATE TABLE extension_versions (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL,
			version TEXT NOT NULL,
			package_digest TEXT NOT NULL,
			manifest JSONB NOT NULL,
			UNIQUE (extension_id, version, package_digest)
		)`,
		`CREATE TABLE extension_trust_grants (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL,
			extension_version TEXT NOT NULL,
			package_digest TEXT NOT NULL,
			action TEXT NOT NULL,
			impact_document JSONB NOT NULL,
			revoked_at TIMESTAMPTZ
		)`,
		`CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			status TEXT NOT NULL
		)`,
		`CREATE TABLE roles (
			id BIGINT PRIMARY KEY,
			key TEXT NOT NULL UNIQUE,
			is_enabled BOOLEAN NOT NULL DEFAULT TRUE
		)`,
		`CREATE TABLE permissions (key TEXT PRIMARY KEY)`,
		`CREATE TABLE user_roles (
			user_id BIGINT NOT NULL REFERENCES users(id),
			role_id BIGINT NOT NULL REFERENCES roles(id),
			PRIMARY KEY (user_id, role_id)
		)`,
		`CREATE TABLE role_permissions (
			role_id BIGINT NOT NULL REFERENCES roles(id),
			permission_key TEXT NOT NULL REFERENCES permissions(key),
			PRIMARY KEY (role_id, permission_key)
		)`,
		`CREATE TABLE user_permission_overrides (
			user_id BIGINT NOT NULL REFERENCES users(id),
			permission_key TEXT NOT NULL REFERENCES permissions(key),
			effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
			PRIMARY KEY (user_id, permission_key)
		)`,
		`CREATE TABLE audit_events (
			id BIGSERIAL PRIMARY KEY,
			actor_user_id BIGINT,
			action TEXT NOT NULL,
			metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
		)`,
		`CREATE TABLE command_business (
			command_key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	} {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("install Host Command prerequisite: %v", err)
		}
	}
}

func installPostgresCommandMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, filename := range []string{
		"202607140017_host_command_receipts.sql",
		"202607150024_host_command_actor_delegations.sql",
	} {
		body, err := fs.ReadFile(migrations.Files(), filename)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.SplitN(string(body), "-- +goose Down", 2)
		if len(parts) != 2 {
			t.Fatalf("migration %s has no Down boundary", filename)
		}
		if _, err := pool.Exec(ctx, parts[0], pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("install Host Command migration %s: %v", filename, err)
		}
	}
}

func newPostgresCommandEngine(t *testing.T, h *postgresCommandHarness, failBusiness bool) *protocolV2CommandEngine {
	t.Helper()
	definition := protocolV2CommandDefinition{
		ID: postgresCommandTestID, Version: postgresCommandTestVersion,
		InputSchemaID: postgresCommandInputSchema, InputSchemaVersion: postgresCommandTestSchemaVersion,
		OutputSchemaID: postgresCommandOutputSchema, OutputSchemaVersion: postgresCommandTestSchemaVersion,
	}
	prepare := func(request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		output, err := protocolV2Document(
			postgresCommandOutputSchema, postgresCommandTestSchemaVersion,
			map[string]any{"value": protocolV2DocumentValues(request.GetInput())["value"]},
		)
		if err != nil {
			return nil, err
		}
		return &protocolV2CommandPreparation{
			Policy:          []*hostv2.PolicyDecision{{PolicyId: "sforum.test@1", Allowed: true}},
			Impact:          []*hostv2.ImpactItem{{Module: "test", Action: "write", ResourceType: "fixture"}},
			ProjectedResult: output,
		}, nil
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		return prepare(request)
	}
	definition.Prepare = func(_ context.Context, _ pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		return prepare(request)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		value, _ := protocolV2DocumentValues(request.GetInput())["value"].(string)
		if _, err := tx.Exec(ctx, `
			INSERT INTO command_business (command_key, value) VALUES ($1, $2)
		`, request.GetIdempotencyKey(), value); err != nil {
			return nil, err
		}
		if failBusiness {
			return nil, errors.New("injected business failure")
		}
		output, err := protocolV2Document(
			postgresCommandOutputSchema, postgresCommandTestSchemaVersion,
			map[string]any{"value": value},
		)
		if err != nil {
			return nil, err
		}
		return &protocolV2CommandExecution{Output: output, CommittedRevision: "1"}, nil
	}
	engine, err := newProtocolV2CommandEngine(h.backend, definition)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func postgresCommandRequest(identity *protocolv2.ExtensionIdentity, key, value string) *hostv2.CommandRequest {
	input, _ := protocolV2Document(
		postgresCommandInputSchema, postgresCommandTestSchemaVersion,
		map[string]any{"value": value},
	)
	return &hostv2.CommandRequest{
		Context: &protocolv2.RequestContext{
			RequestId:      "request-" + key,
			Extension:      proto.Clone(identity).(*protocolv2.ExtensionIdentity),
			IdempotencyKey: key,
		},
		CommandId: postgresCommandTestID, CommandVersion: postgresCommandTestVersion,
		IdempotencyKey: key, Input: input,
	}
}

func installPostgresCommandFailureTrigger(t *testing.T, h *postgresCommandHarness, table string) {
	t.Helper()
	functionName := "reject_" + table
	if _, err := h.pool.Exec(h.ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  RAISE EXCEPTION 'injected %s failure';
		END
		$$
	`, pgx.Identifier{functionName}.Sanitize(), table)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, fmt.Sprintf(`
		CREATE TRIGGER %s BEFORE INSERT ON %s
		FOR EACH ROW EXECUTE FUNCTION %s()
	`, pgx.Identifier{functionName + "_trigger"}.Sanitize(), pgx.Identifier{table}.Sanitize(),
		pgx.Identifier{functionName}.Sanitize())); err != nil {
		t.Fatal(err)
	}
}

func assertPostgresCommandCounts(t *testing.T, h *postgresCommandHarness, key string, business, audit, receipts int) {
	t.Helper()
	checks := []struct {
		query string
		args  []any
		want  int
	}{
		{`SELECT COUNT(*) FROM command_business WHERE command_key = $1`, []any{key}, business},
		{`SELECT COUNT(*) FROM audit_events`, nil, audit},
		{`SELECT COUNT(*) FROM extension_host_command_receipts WHERE idempotency_key = $1`, []any{key}, receipts},
	}
	for _, check := range checks {
		var got int
		if err := h.pool.QueryRow(h.ctx, check.query, check.args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Fatalf("count for %q = %d, want %d", check.query, got, check.want)
		}
	}
}

func assertPostgresCommandExactReceipt(t *testing.T, h *postgresCommandHarness, key string) {
	t.Helper()
	var (
		versionID, trustGrantID int64
		version, digest         string
		authority               string
		resultSchema            string
		auditSchema             string
	)
	if err := h.pool.QueryRow(h.ctx, `
		SELECT receipts.extension_version_id, receipts.extension_version,
		       receipts.package_digest, receipts.authority_type, receipts.trust_grant_id,
		       receipts.result #>> '{output,schemaId}', audits.metadata->>'schemaVersion'
		FROM extension_host_command_receipts AS receipts
		JOIN audit_events AS audits ON audits.id = receipts.audit_event_id
		WHERE receipts.idempotency_key = $1
	`, key).Scan(&versionID, &version, &digest, &authority, &trustGrantID, &resultSchema, &auditSchema); err != nil {
		t.Fatal(err)
	}
	if versionID != h.versionID || version != h.identity.GetExtensionVersion() ||
		digest != h.identity.GetArtifactDigest() || authority != "trust_grant" || trustGrantID != h.grantID ||
		resultSchema != postgresCommandOutputSchema || auditSchema != protocolV2CommandAuditSchema {
		t.Fatalf("unexpected exact Host Command receipt")
	}
}
