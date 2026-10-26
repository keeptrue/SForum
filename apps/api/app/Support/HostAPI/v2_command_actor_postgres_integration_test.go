package hostapi

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const postgresDelegatedCommandPermission = "topic.manage"

func TestPostgresProtocolV2DelegatedCommandCommitsReplaysAndRechecksActor(t *testing.T) {
	h := newPostgresCommandHarness(t)
	installPostgresCommandActor(t, h, 42, postgresDelegatedCommandPermission)
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	engine := newPostgresDelegatedCommandEngine(t, h, authority, nil, nil)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)
	request := postgresDelegatedCommandRequest(t, authority, h, 42, "delegated-commit", "value")

	committed, err := engine.execute(ctx, request)
	if err != nil || committed.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED || committed.GetError() != nil {
		t.Fatalf("delegated commit = %#v, %v", committed, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-commit", 42, 1, 1, 1, 1)

	retry := postgresDelegatedCommandRequest(t, authority, h, 42, "delegated-commit", "value")
	replayed, err := engine.execute(ctx, retry)
	if err != nil || replayed.GetState() != hostv2.CommandState_COMMAND_STATE_REPLAYED || replayed.GetError() != nil ||
		replayed.GetTransactionId() != committed.GetTransactionId() {
		t.Fatalf("delegated replay = %#v, %v", replayed, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-commit", 42, 1, 1, 1, 1)

	if _, err := h.pool.Exec(h.ctx, `UPDATE users SET status = 'disabled' WHERE id = 42`); err != nil {
		t.Fatal(err)
	}
	inactive := postgresDelegatedCommandRequest(t, authority, h, 42, "delegated-commit", "value")
	denied, err := engine.execute(ctx, inactive)
	if err != nil || denied.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		denied.GetError().GetReason() != "host.command_actor_inactive" {
		t.Fatalf("inactive replay = %#v, %v", denied, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-commit", 42, 1, 1, 1, 1)

	if _, err := h.pool.Exec(h.ctx, `UPDATE users SET status = 'active' WHERE id = 42`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(h.ctx, `DELETE FROM role_permissions WHERE permission_key = $1`, postgresDelegatedCommandPermission); err != nil {
		t.Fatal(err)
	}
	withoutPermission := postgresDelegatedCommandRequest(t, authority, h, 42, "delegated-commit", "value")
	denied, err = engine.execute(ctx, withoutPermission)
	if err != nil || denied.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		denied.GetError().GetReason() != "host.command_actor_permission_denied" {
		t.Fatalf("permission replay = %#v, %v", denied, err)
	}
}

func TestPostgresProtocolV2DelegationConsumptionRollsBackWithCommand(t *testing.T) {
	h := newPostgresCommandHarness(t)
	installPostgresCommandActor(t, h, 42, postgresDelegatedCommandPermission)
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	var failBusiness atomic.Bool
	failBusiness.Store(true)
	engine := newPostgresDelegatedCommandEngine(t, h, authority, &failBusiness, nil)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)
	request := postgresDelegatedCommandRequest(t, authority, h, 42, "delegated-rollback", "value")

	rolledBack, err := engine.execute(ctx, request)
	if err != nil || rolledBack.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK {
		t.Fatalf("delegated rollback = %#v, %v", rolledBack, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-rollback", 42, 0, 0, 0, 0)

	failBusiness.Store(false)
	committed, err := engine.execute(ctx, request)
	if err != nil || committed.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED || committed.GetError() != nil {
		t.Fatalf("delegated retry after rollback = %#v, %v", committed, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-rollback", 42, 1, 1, 1, 1)
}

func TestPostgresProtocolV2DelegationConsumptionRollsBackAfterPolicyDenial(t *testing.T) {
	h := newPostgresCommandHarness(t)
	installPostgresCommandActor(t, h, 42, postgresDelegatedCommandPermission)
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	var denyPolicy atomic.Bool
	denyPolicy.Store(true)
	engine := newPostgresDelegatedCommandEngine(t, h, authority, nil, &denyPolicy)
	ctx := ContextWithProtocolV2RuntimeIdentity(h.ctx, h.identity)
	request := postgresDelegatedCommandRequest(t, authority, h, 42, "delegated-policy", "value")

	denied, err := engine.execute(ctx, request)
	if err != nil || denied.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK ||
		denied.GetError().GetReason() != "host.command_policy_denied" {
		t.Fatalf("delegated policy denial = %#v, %v", denied, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-policy", 42, 0, 0, 0, 0)

	denyPolicy.Store(false)
	committed, err := engine.execute(ctx, request)
	if err != nil || committed.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
		t.Fatalf("delegated policy retry = %#v, %v", committed, err)
	}
	assertPostgresDelegatedCommandEvidence(t, h, "delegated-policy", 42, 1, 1, 1, 1)
}

func installPostgresCommandActor(t *testing.T, h *postgresCommandHarness, actorUserID int64, permission string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO users (id, status) VALUES ($1, 'active')`, args: []any{actorUserID}},
		{query: `INSERT INTO roles (id, key, is_enabled) VALUES ($1, $2, TRUE)`, args: []any{actorUserID, "role.actor"}},
		{query: `INSERT INTO permissions (key) VALUES ($1)`, args: []any{permission}},
		{query: `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $1)`, args: []any{actorUserID}},
		{query: `INSERT INTO role_permissions (role_id, permission_key) VALUES ($1, $2)`, args: []any{actorUserID, permission}},
	}
	for _, statement := range statements {
		if _, err := h.pool.Exec(h.ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func postgresDelegatedCommandRequest(
	t *testing.T,
	authority *ProtocolV2ActorDelegationAuthority,
	h *postgresCommandHarness,
	actorUserID int64,
	key string,
	value string,
) *hostv2.CommandRequest {
	t.Helper()
	request := postgresCommandRequest(h.identity, key, value)
	token, err := authority.IssueActorDelegation(context.Background(), ProtocolV2ActorDelegationRequest{
		ActorUserID: actorUserID, Runtime: h.identity, CommandID: postgresCommandTestID,
		CommandVersion: postgresCommandTestVersion, IdempotencyKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	request.ActorDelegation = token
	return request
}

func newPostgresDelegatedCommandEngine(
	t *testing.T,
	h *postgresCommandHarness,
	authority *ProtocolV2ActorDelegationAuthority,
	failBusiness *atomic.Bool,
	denyPolicy *atomic.Bool,
) *protocolV2CommandEngine {
	t.Helper()
	definition := protocolV2CommandDefinition{
		ID: postgresCommandTestID, Version: postgresCommandTestVersion,
		InputSchemaID: postgresCommandInputSchema, InputSchemaVersion: postgresCommandTestSchemaVersion,
		OutputSchemaID: postgresCommandOutputSchema, OutputSchemaVersion: postgresCommandTestSchemaVersion,
		ActorMode: protocolV2CommandActorDelegated, RequiredPermissions: []string{postgresDelegatedCommandPermission},
	}
	preview := func(request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		output, err := protocolV2Document(
			postgresCommandOutputSchema, postgresCommandTestSchemaVersion,
			map[string]any{"value": protocolV2DocumentValues(request.GetInput())["value"]},
		)
		if err != nil {
			return nil, err
		}
		allowed := denyPolicy == nil || !denyPolicy.Load()
		return &protocolV2CommandPreparation{
			Policy:          []*hostv2.PolicyDecision{{PolicyId: "sforum.test.delegated@1", Allowed: allowed}},
			Impact:          []*hostv2.ImpactItem{{Module: "test", Action: "write", ResourceType: "fixture"}},
			ProjectedResult: output,
		}, nil
	}
	definition.Preview = func(_ context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		return preview(request)
	}
	definition.Prepare = func(ctx context.Context, _ pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		actorUserID, ok := ProtocolV2CommandActorUserID(ctx)
		if !ok || actorUserID != 42 {
			t.Fatalf("delegated command actor = %d, %v", actorUserID, ok)
		}
		return preview(request)
	}
	definition.Execute = func(ctx context.Context, tx pgx.Tx, request *hostv2.CommandRequest, _ *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		if actorUserID, ok := ProtocolV2CommandActorUserID(ctx); !ok || actorUserID != 42 {
			t.Fatalf("delegated execute actor = %d, %v", actorUserID, ok)
		}
		value, _ := protocolV2DocumentValues(request.GetInput())["value"].(string)
		if _, err := tx.Exec(ctx, `INSERT INTO command_business (command_key, value) VALUES ($1, $2)`, request.GetIdempotencyKey(), value); err != nil {
			return nil, err
		}
		if failBusiness != nil && failBusiness.Load() {
			return nil, newProtocolV2CommandError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.command_test_failed", "The delegated test command failed.", false)
		}
		output, err := protocolV2Document(postgresCommandOutputSchema, postgresCommandTestSchemaVersion, map[string]any{"value": value})
		return &protocolV2CommandExecution{Output: output, CommittedRevision: "1"}, err
	}
	engine, err := newProtocolV2CommandEngineWithActorDelegation(h.backend, authority, definition)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func assertPostgresDelegatedCommandEvidence(
	t *testing.T,
	h *postgresCommandHarness,
	key string,
	actorUserID int64,
	wantBusiness int,
	wantAudit int,
	wantReceipt int,
	wantConsumption int,
) {
	t.Helper()
	var business, audit, receipt, consumption int
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM command_business WHERE command_key = $1`, key).Scan(&business); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM audit_events WHERE action = 'extension.host_command.committed' AND actor_user_id = $1`, actorUserID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM extension_host_command_receipts WHERE idempotency_key = $1`, key).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(h.ctx, `SELECT count(*) FROM extension_host_command_actor_delegation_consumptions WHERE idempotency_key = $1 AND actor_user_id = $2`, key, actorUserID).Scan(&consumption); err != nil {
		t.Fatal(err)
	}
	if business != wantBusiness || audit != wantAudit || receipt != wantReceipt || consumption != wantConsumption {
		t.Fatalf("delegated evidence business/audit/receipt/consumption = %d/%d/%d/%d, want %d/%d/%d/%d", business, audit, receipt, consumption, wantBusiness, wantAudit, wantReceipt, wantConsumption)
	}
}
