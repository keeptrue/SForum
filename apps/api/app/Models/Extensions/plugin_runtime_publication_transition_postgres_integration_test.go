package extensions

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPublishPluginRuntimePublicationTransitionTxNoLatestInserts(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "transition_no_latest")
	target := pluginRuntimeTransitionTarget(t, fixture.firstMember(), "backend/plugin")

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	publication, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Target: target, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 17,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if publication.Revision != 1 || publication.Reason != PluginRuntimePublicationEnable ||
		publication.ActorUserID != 17 {
		t.Fatalf("unexpected publication: %+v", publication)
	}
	assertPluginRuntimePublicationMembers(t, publication, transitionMember(target))

	// 事务未提交前，调用方外的连接看不到该 revision。
	if _, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx); !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		t.Fatalf("uncommitted publication leaked: %v", err)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, publication) {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 1)

	// 声明型 enable 在无历史时也必须插入空成员 revision，推进 lifecycle marker。
	emptyFixture := newPluginRuntimePublicationPGFixture(t, "transition_decl_empty")
	declarationOnly := pluginRuntimeTransitionTarget(t, emptyFixture.secondMember(), "")
	tx, err = emptyFixture.pool.Begin(emptyFixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	emptyPublication, err := PublishPluginRuntimePublicationTransitionTx(
		emptyFixture.ctx, tx, PluginRuntimePublicationTransition{
			Target: declarationOnly, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 5,
		},
	)
	if err != nil {
		_ = tx.Rollback(emptyFixture.ctx)
		t.Fatal(err)
	}
	if emptyPublication.Revision != 1 || emptyPublication.MemberCount != 0 ||
		emptyPublication.Reason != PluginRuntimePublicationEnable || emptyPublication.ActorUserID != 5 {
		_ = tx.Rollback(emptyFixture.ctx)
		t.Fatalf("empty declaration publication=%+v", emptyPublication)
	}
	assertPluginRuntimePublicationMembers(t, emptyPublication)
	if err := tx.Commit(emptyFixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationCount(t, emptyFixture, 1)
}

func TestPublishPluginRuntimePublicationTransitionTxAlwaysInsertsEvenWhenMembersUnchanged(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "transition_always_insert")
	source := pluginRuntimeTransitionTarget(t, fixture.firstMember(), "backend/plugin")
	// 成员行有 exact version FK；升级目标必须先落到 extension_versions，且落盘制品
	// 自洽（发布期闸门会逐个校验）。
	target := pluginRuntimeTransitionTarget(
		t, fixture.seedVersion("fixture.plugin", 104, "1.1.0", TypePlugin), "backend/plugin",
	)
	unrelated := pluginRuntimeTransitionTarget(t, fixture.secondMember(), "backend/plugin")

	seed, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0,
		[]PluginRuntimeMember{transitionMember(source), transitionMember(unrelated)},
	)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 成员未变也必须插入更新 revision（同 digest，新 reason/actor），不可复用 seed。
	replayed, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Target: source, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 99,
		},
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if replayed.Revision <= seed.Revision ||
		replayed.MembersDigest != seed.MembersDigest ||
		replayed.Reason != PluginRuntimePublicationEnable ||
		replayed.ActorUserID != 99 ||
		samePluginRuntimePublication(replayed, seed) {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("unchanged members must insert newer revision: seed=%+v replayed=%+v", seed, replayed)
	}
	assertPluginRuntimePublicationMembers(t, replayed, transitionMember(source), transitionMember(unrelated))
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 2)

	tx, err = fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Source: &source, Target: target, Activate: true,
			Reason: PluginRuntimePublicationUpgrade, ActorUserID: 21,
		},
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if upgraded.Revision <= replayed.Revision || upgraded.Reason != PluginRuntimePublicationUpgrade ||
		upgraded.ActorUserID != 21 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("upgrade publication mismatch: %+v", upgraded)
	}
	assertPluginRuntimePublicationMembers(t, upgraded, transitionMember(unrelated), transitionMember(target))
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 3)

	tx, err = fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Source: &unrelated, Target: unrelated, Activate: false,
			Reason: PluginRuntimePublicationDisable, ActorUserID: 22,
		},
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if disabled.Revision <= upgraded.Revision || disabled.Reason != PluginRuntimePublicationDisable {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("disable publication mismatch: %+v", disabled)
	}
	assertPluginRuntimePublicationMembers(t, disabled, transitionMember(target))
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	// 已 absent 的 disable：成员 digest 不变，仍插入新 revision。
	tx, err = fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Source: &unrelated, Target: unrelated, Activate: false,
			Reason: PluginRuntimePublicationDisable, ActorUserID: 23,
		},
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if again.Revision <= disabled.Revision ||
		again.MembersDigest != disabled.MembersDigest ||
		again.ActorUserID != 23 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("absent disable must still insert: disabled=%+v again=%+v", disabled, again)
	}
	assertPluginRuntimePublicationMembers(t, again, transitionMember(target))
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 5)

	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, again) {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestPublishPluginRuntimePublicationTransitionTxRollbackDropsInsert(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "transition_rollback")
	target := pluginRuntimeTransitionTarget(t, fixture.firstMember(), "backend/plugin")

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Target: target, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 3,
		},
	)
	if err != nil {
		_ = tx.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	if publication.Revision != 1 {
		_ = tx.Rollback(fixture.ctx)
		t.Fatalf("publication=%+v", publication)
	}
	// 调用方回滚后，immutable ledger 不得留下半截 revision。
	// PostgreSQL identity/sequence 在 ROLLBACK 后仍会前进，故不断言 revision=1。
	if err := tx.Rollback(fixture.ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatal(err)
	}
	if _, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx); !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
		t.Fatalf("rolled back publication persisted: %v", err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 0)

	// 回滚后可再次以空集为起点发布；可见行仍只有提交后的一条。
	tx, err = fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	republished, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Target: target, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 3,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if republished.Revision <= 0 {
		t.Fatalf("republished revision=%d", republished.Revision)
	}
	assertPluginRuntimePublicationMembers(t, republished, transitionMember(target))
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 1)
	latest, err := fixture.store.LatestPluginRuntimePublication(fixture.ctx)
	if err != nil || !samePluginRuntimePublication(latest, republished) {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestPublishPluginRuntimePublicationTransitionTxRejectsInvalidReasonAndArtifacts(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "transition_invalid_inputs")
	target := pluginRuntimeTransitionTarget(t, fixture.firstMember(), "backend/plugin")

	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	for _, transition := range []PluginRuntimePublicationTransition{
		{Target: target, Activate: true, Reason: "not-a-reason", ActorUserID: 1},
		{Target: target, Activate: true, Reason: PluginRuntimePublicationRecovery, ActorUserID: 1},
		{Target: target, Activate: true, Reason: PluginRuntimePublicationStartupReconcile, ActorUserID: 1},
		{Target: target, Activate: false, Reason: PluginRuntimePublicationEnable, ActorUserID: 1},
		{Target: target, Activate: true, Reason: PluginRuntimePublicationDisable, ActorUserID: 1},
	} {
		if _, err := PublishPluginRuntimePublicationTransitionTx(fixture.ctx, tx, transition); !errors.Is(err, ErrPluginRuntimePublicationConflict) {
			t.Fatalf("transition=%+v error=%v", transition, err)
		}
	}

	invalid := target
	invalid.ActiveVersionID = 0
	if _, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, PluginRuntimePublicationTransition{
			Target: invalid, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 1,
		},
	); !errors.Is(err, ErrPluginRuntimePublicationConflict) {
		t.Fatalf("invalid artifact error=%v", err)
	}
	assertPluginRuntimePublicationCount(t, fixture, 0)
}

func TestPublishPluginRuntimePublicationTransitionTxRequiresReadCommitted(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "transition_isolation")
	target := pluginRuntimeTransitionTarget(t, fixture.firstMember(), "backend/plugin")

	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			tx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()

			_, err = PublishPluginRuntimePublicationTransitionTx(
				fixture.ctx, tx, PluginRuntimePublicationTransition{
					Target: target, Activate: true,
					Reason: PluginRuntimePublicationEnable, ActorUserID: 1,
				},
			)
			if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
				t.Fatalf("isolation=%q error=%v", isolation, err)
			}
		})
	}
	assertPluginRuntimePublicationCount(t, fixture, 0)
}
