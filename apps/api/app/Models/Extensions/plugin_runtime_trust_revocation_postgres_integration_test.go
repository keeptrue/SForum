package extensions

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPublishPluginRuntimeTrustRevocationTxRemovesExactMemberAndSkipsAbsent(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "trust_revoke")
	revoked := pluginRuntimeTransitionTarget(t, fixture.firstMember(), "backend/plugin")
	unrelated := pluginRuntimeTransitionTarget(t, fixture.secondMember(), "backend/plugin")
	seed, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0,
		[]PluginRuntimeMember{transitionMember(revoked), transitionMember(unrelated)},
	)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	publication, published, err := PublishPluginRuntimeTrustRevocationTx(
		fixture.ctx, tx, revoked.ID, 17,
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if !published || publication.Revision <= seed.Revision || publication.Reason != PluginRuntimePublicationRecovery || publication.ActorUserID != 17 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("trust revoke publication=%+v", publication)
	}
	assertPluginRuntimePublicationMembers(t, publication, transitionMember(unrelated))
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	// Idempotent replay does not perturb the global full-set after the exact
	// member is already absent.
	tx, err = fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	replayed, published, err := PublishPluginRuntimeTrustRevocationTx(
		fixture.ctx, tx, revoked.ID, 18,
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if published || replayed.Revision != 0 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("replayed trust revoke=%+v prior=%+v", replayed, publication)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 2)
}

func TestPublishPluginRuntimeTrustRevocationTxRejectsMissingAuthorityAndIsolation(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "trust_revoke_invalid")
	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if publication, published, err := PublishPluginRuntimeTrustRevocationTx(fixture.ctx, tx, "missing.plugin", 1); err != nil || published || publication.Revision != 0 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("missing latest publication=%+v published=%t error=%v", publication, published, err)
	}
	if err := tx.Rollback(fixture.ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal(err)
	}

	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			tx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, _, err := PublishPluginRuntimeTrustRevocationTx(fixture.ctx, tx, "missing.plugin", 1); !errors.Is(err, ErrPluginRuntimePublicationConflict) {
				t.Fatalf("isolation=%q error=%v", isolation, err)
			}
		})
	}
}
