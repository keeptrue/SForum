package entitlements

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestPostgresEntitlementGrantReplayEffectiveAndRevoke(t *testing.T) {
	fixture := newEntitlementPGFixture(t)
	validFrom := time.Now().UTC().Add(-time.Hour)
	validUntil := validFrom.Add(2 * time.Hour)
	input := fixture.capabilityGrant("entitlement:grant:replay", validFrom, &validUntil)

	granted, err := fixture.repository.Grant(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if granted.Replayed || granted.Entitlement.Status != StatusActive ||
		granted.Entitlement.Scope.Capability != input.Scope.Capability ||
		granted.Event.Action != ActionGrant || granted.Event.AuditEventID <= 0 {
		t.Fatalf("grant result = %#v", granted)
	}
	replayed, err := fixture.repository.Grant(fixture.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Entitlement.ID != granted.Entitlement.ID || replayed.Event.ID != granted.Event.ID {
		t.Fatalf("grant replay = %#v", replayed)
	}
	changed := input
	changed.Source.ID = "different-source"
	if _, err := fixture.repository.Grant(fixture.ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay error = %v", err)
	}

	effective, found, err := fixture.repository.Effective(fixture.ctx, EffectiveInput{
		Subject: input.Subject, Scope: input.Scope, At: time.Now().UTC(),
	})
	if err != nil || !found || effective.ID != granted.Entitlement.ID {
		t.Fatalf("effective entitlement = %#v found=%v err=%v", effective, found, err)
	}
	revoked, err := fixture.repository.Revoke(fixture.ctx, TransitionInput{
		EntitlementID: granted.Entitlement.ID, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "entitlement:revoke:replay",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Entitlement.Status != StatusRevoked || revoked.Entitlement.Revision != 2 ||
		revoked.Entitlement.RevokedAt == nil || revoked.Event.PreviousStatus != StatusActive {
		t.Fatalf("revoke result = %#v", revoked)
	}
	replayedRevoke, err := fixture.repository.Revoke(fixture.ctx, TransitionInput{
		EntitlementID: granted.Entitlement.ID, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "entitlement:revoke:replay",
	})
	if err != nil || !replayedRevoke.Replayed || replayedRevoke.Event.ID != revoked.Event.ID {
		t.Fatalf("revoke replay = %#v err=%v", replayedRevoke, err)
	}
	if _, found, err := fixture.repository.Effective(fixture.ctx, EffectiveInput{
		Subject: input.Subject, Scope: input.Scope, At: time.Now().UTC(),
	}); err != nil || found {
		t.Fatalf("revoked effective found=%v err=%v", found, err)
	}
	fixture.assertCounts(1, 2, 2)
}

func TestPostgresEntitlementExpireAndFutureFence(t *testing.T) {
	fixture := newEntitlementPGFixture(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	expiredGrant, err := fixture.repository.Grant(
		fixture.ctx, fixture.resourceGrant("entitlement:grant:expired", now.Add(-2*time.Hour), &past),
	)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := fixture.repository.Expire(fixture.ctx, TransitionInput{
		EntitlementID: expiredGrant.Entitlement.ID, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "entitlement:expire:past",
	})
	if err != nil {
		t.Fatal(err)
	}
	if expired.Entitlement.Status != StatusExpired || expired.Entitlement.ExpiredAt == nil ||
		expired.Event.NextStatus != StatusExpired {
		t.Fatalf("expired result = %#v", expired)
	}

	future := now.Add(time.Hour)
	futureGrant, err := fixture.repository.Grant(
		fixture.ctx, fixture.resourceGrant("entitlement:grant:future", now.Add(-time.Hour), &future),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Expire(fixture.ctx, TransitionInput{
		EntitlementID: futureGrant.Entitlement.ID, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "entitlement:expire:future",
	}); !errors.Is(err, ErrNotYetExpired) {
		t.Fatalf("future expire error = %v", err)
	}
	stored, err := fixture.repository.Get(fixture.ctx, futureGrant.Entitlement.ID)
	if err != nil || stored.Status != StatusActive || stored.Revision != 1 {
		t.Fatalf("future entitlement = %#v err=%v", stored, err)
	}
	fixture.assertCounts(2, 3, 3)
}

func TestPostgresEntitlementExternalTransactionRollback(t *testing.T) {
	fixture := newEntitlementPGFixture(t)
	validFrom := time.Now().UTC().Add(-time.Minute)
	input := fixture.capabilityGrant("entitlement:grant:outer-rollback", validFrom, nil)
	tx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.repository.GrantTx(fixture.ctx, tx, input)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	var insideEvents int
	if err := tx.QueryRow(fixture.ctx, `SELECT count(*) FROM entitlement_events WHERE idempotency_key=$1`, input.IdempotencyKey).Scan(&insideEvents); err != nil || insideEvents != 1 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("inside event count=%d err=%v", insideEvents, err)
	}
	if err := tx.Rollback(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.Get(fixture.ctx, result.Entitlement.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back entitlement error = %v", err)
	}
	fixture.assertCounts(0, 0, 0)
}

func TestPostgresEntitlementAuditFailureRollsBackState(t *testing.T) {
	fixture := newEntitlementPGFixture(t)
	input := fixture.capabilityGrant("entitlement:grant:bad-actor", time.Now().UTC(), nil)
	input.ActorUserID = fixture.actorUserID + 1000000
	if _, err := fixture.repository.Grant(fixture.ctx, input); err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("missing actor grant error = %v", err)
	}
	fixture.assertCounts(0, 0, 0)
}

func TestPostgresEntitlementConcurrentReplayCreatesOneFact(t *testing.T) {
	fixture := newEntitlementPGFixture(t)
	input := fixture.capabilityGrant("entitlement:grant:concurrent", time.Now().UTC(), nil)
	const workers = 8
	start := make(chan struct{})
	results := make(chan MutationResult, workers)
	errorsOut := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := fixture.repository.Grant(fixture.ctx, input)
			if err != nil {
				errorsOut <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		t.Fatalf("concurrent grant: %v", err)
	}
	var entitlementID int64
	fresh, replayed := 0, 0
	for result := range results {
		if entitlementID == 0 {
			entitlementID = result.Entitlement.ID
		}
		if result.Entitlement.ID != entitlementID {
			t.Fatalf("concurrent entitlement id=%d want=%d", result.Entitlement.ID, entitlementID)
		}
		if result.Replayed {
			replayed++
		} else {
			fresh++
		}
	}
	if fresh != 1 || replayed != workers-1 {
		t.Fatalf("fresh=%d replayed=%d", fresh, replayed)
	}
	fixture.assertCounts(1, 1, 1)
}

type entitlementPGFixture struct {
	t           *testing.T
	ctx         context.Context
	admin       *pgxpool.Pool
	pool        *pgxpool.Pool
	repository  *PostgresRepository
	schema      string
	actorUserID int64
}

func newEntitlementPGFixture(t *testing.T) *entitlementPGFixture {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("entitlements_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	removeSchema := func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	}

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	applyEntitlementTestMigrations(t, ctx, db, removeSchema)

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	fixture := &entitlementPGFixture{
		t: t, ctx: ctx, admin: admin, pool: pool,
		repository: NewPostgresRepository(pool), schema: schema,
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (
		  username, username_lower, email, email_lower, display_name, status
		) VALUES ($1, $1, $2, $2, 'Entitlement Actor', 'active')
		RETURNING id
	`, "entitlement_actor_"+schema, "entitlement_actor_"+schema+"@example.test").Scan(&fixture.actorUserID); err != nil {
		pool.Close()
		removeSchema()
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		removeSchema()
		admin.Close()
	})
	return fixture
}

func applyEntitlementTestMigrations(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	removeSchema func(),
) {
	t.Helper()
	defer db.Close()
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		removeSchema()
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 202607040001); err != nil {
		removeSchema()
		t.Fatalf("migrate isolated entitlement schema to identity: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, 202607150025, true); err != nil {
		removeSchema()
		t.Fatalf("apply isolated entitlement migration: %v", err)
	}
}

func (f *entitlementPGFixture) capabilityGrant(key string, from time.Time, until *time.Time) GrantInput {
	return GrantInput{
		Subject:   Subject{Type: "user", ID: fmt.Sprint(f.actorUserID)},
		Scope:     Scope{Kind: ScopeCapability, Capability: "forum.private.read"},
		Source:    Source{Type: "plugin", ID: "sforum.entitlement-test"},
		ValidFrom: from, ValidUntil: until, ActorUserID: f.actorUserID, IdempotencyKey: key,
	}
}

func (f *entitlementPGFixture) resourceGrant(key string, from time.Time, until *time.Time) GrantInput {
	input := f.capabilityGrant(key, from, until)
	input.Scope = Scope{Kind: ScopeResource, ResourceType: "topic", ResourceID: "42"}
	return input
}

func (f *entitlementPGFixture) assertCounts(entitlements, events, audits int) {
	f.t.Helper()
	var gotEntitlements, gotEvents, gotAudits int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM entitlements`).Scan(&gotEntitlements); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM entitlement_events`).Scan(&gotEvents); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'entitlement.%'`).Scan(&gotAudits); err != nil {
		f.t.Fatal(err)
	}
	if gotEntitlements != entitlements || gotEvents != events || gotAudits != audits {
		f.t.Fatalf(
			"counts entitlements=%d/%d events=%d/%d audits=%d/%d",
			gotEntitlements, entitlements, gotEvents, events, gotAudits, audits,
		)
	}
}
