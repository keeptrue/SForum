package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestExternalIdentityLinkPostgresLifecycleAndPrivacy(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()

	subjectDigest := strings.Repeat("d", 64)
	input := fixture.linkInput(fixture.targetUserID, "external-link:lifecycle", subjectDigest)
	var fenceCalls atomic.Int32
	mutation, err := fixture.externalLinks.Link(ctx, input, func() error {
		fenceCalls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutation.Replayed || mutation.Link.Status != ExternalIdentityLinkStatusActive ||
		mutation.Link.Revision != 1 || mutation.Link.OwnerExtensionVersionID != fixture.versionID ||
		mutation.Link.OwnerPackageDigest != fixture.provider.Artifact.PackageDigest ||
		mutation.Link.DeclarationRevision <= 0 || mutation.Event.Action != ExternalIdentityLinkActionLink {
		t.Fatalf("unexpected link mutation: %#v", mutation)
	}

	replay, err := fixture.externalLinks.Link(ctx, input, func() error {
		fenceCalls.Add(1)
		return nil
	})
	if err != nil || !replay.Replayed || replay.Link.ID != mutation.Link.ID || replay.Event.ID != mutation.Event.ID {
		t.Fatalf("link replay=%#v error=%v", replay, err)
	}
	if fenceCalls.Load() != 2 {
		t.Fatalf("final fence calls=%d want 2", fenceCalls.Load())
	}
	changed := input
	changed.ProviderSubjectDigest = strings.Repeat("e", 64)
	if _, err := fixture.externalLinks.Link(ctx, changed, func() error { return nil }); !errors.Is(err, ErrExternalIdentityLinkIdempotencyConflict) {
		t.Fatalf("changed replay error=%v", err)
	}

	found, err := fixture.externalLinks.FindActive(ctx, fixture.provider.ID, subjectDigest)
	if err != nil || found.ID != mutation.Link.ID {
		t.Fatalf("active lookup=%#v error=%v", found, err)
	}
	listed, err := fixture.externalLinks.ListUser(ctx, fixture.targetUserID)
	if err != nil || len(listed) != 1 || listed[0].ID != mutation.Link.ID {
		t.Fatalf("user links=%#v error=%v", listed, err)
	}

	unlinked, err := fixture.externalLinks.Unlink(ctx, TransitionExternalIdentityLinkInput{
		LinkID: mutation.Link.ID, ExpectedRevision: 1, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "external-link:lifecycle:unlink",
	})
	if err != nil || unlinked.Link.Status != ExternalIdentityLinkStatusUnlinked || unlinked.Link.Revision != 2 {
		t.Fatalf("unlink=%#v error=%v", unlinked, err)
	}
	unlinkedReplay, err := fixture.externalLinks.Unlink(ctx, TransitionExternalIdentityLinkInput{
		LinkID: mutation.Link.ID, ExpectedRevision: 1, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "external-link:lifecycle:unlink",
	})
	if err != nil || !unlinkedReplay.Replayed || unlinkedReplay.Event.ID != unlinked.Event.ID {
		t.Fatalf("unlink replay=%#v error=%v", unlinkedReplay, err)
	}

	if err := fixture.retireProvider(); err != nil {
		t.Fatal(err)
	}
	retiredReplay, err := fixture.externalLinks.Link(ctx, input, func() error {
		fenceCalls.Add(1)
		return nil
	})
	if err != nil || !retiredReplay.Replayed || retiredReplay.Event.ID != mutation.Event.ID ||
		retiredReplay.Link.Status != ExternalIdentityLinkStatusUnlinked || retiredReplay.Link.Revision != 2 {
		t.Fatalf("retired provider replay=%#v error=%v", retiredReplay, err)
	}
	if fenceCalls.Load() != 3 {
		t.Fatalf("final fence calls after retired replay=%d want 3", fenceCalls.Load())
	}
	erased, err := fixture.externalLinks.Erase(ctx, TransitionExternalIdentityLinkInput{
		LinkID: mutation.Link.ID, ExpectedRevision: 2, ActorUserID: fixture.actorUserID,
		IdempotencyKey: "external-link:lifecycle:erase",
	})
	if err != nil || erased.Link.Status != ExternalIdentityLinkStatusErased || erased.Link.Revision != 3 {
		t.Fatalf("erase after provider retirement=%#v error=%v", erased, err)
	}
	var storedDigest *string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT provider_subject_digest FROM identity_external_links WHERE id = $1
	`, mutation.Link.ID).Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	if storedDigest != nil {
		t.Fatal("privacy erase retained the external subject digest")
	}
	var auditMetadata string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT COALESCE(string_agg(metadata::text, ' '), '')
		FROM audit_events WHERE action LIKE 'identity.external_link.%'
	`).Scan(&auditMetadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditMetadata, subjectDigest) || strings.Contains(auditMetadata, "vendor-token") {
		t.Fatalf("external subject or token leaked into audit metadata: %s", auditMetadata)
	}
	fixture.assertExternalLinkCounts(1, 3, 3)
}

func TestExternalIdentityLinkPostgresRegistrationTransactionBoundary(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()

	standalone := fixture.linkInput(
		fixture.targetUserID, "external-link:registration:standalone", strings.Repeat("1", 64),
	)
	standalone.ProviderOperation = "registration.complete"
	standalone.ActorUserID = 0
	if _, err := fixture.externalLinks.Link(ctx, standalone, func() error { return nil }); !errors.Is(err, ErrExternalIdentityLinkInvalid) {
		t.Fatalf("standalone registration error=%v", err)
	}
	fixture.assertExternalLinkCounts(0, 0, 0)

	rollbackUsername := "identity_registration_rollback_" + fixture.schema
	rollbackTx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	rollbackUserID, err := insertExternalIdentityRegistrationUser(ctx, rollbackTx, rollbackUsername)
	if err != nil {
		_ = rollbackTx.Rollback(context.Background())
		t.Fatal(err)
	}
	rollbackInput := fixture.linkInput(
		rollbackUserID, "external-link:registration:rollback", strings.Repeat("2", 64),
	)
	rollbackInput.ProviderOperation = "registration.complete"
	rollbackInput.ActorUserID = 0
	if _, err := fixture.externalLinks.LinkTx(ctx, rollbackTx, rollbackInput); err != nil {
		_ = rollbackTx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var rollbackUsers int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username = $1`, rollbackUsername).Scan(&rollbackUsers); err != nil {
		t.Fatal(err)
	}
	if rollbackUsers != 0 {
		t.Fatal("registration rollback retained the newly created user")
	}
	fixture.assertExternalLinkCounts(0, 0, 0)

	commitUsername := "identity_registration_commit_" + fixture.schema
	commitTx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	commitUserID, err := insertExternalIdentityRegistrationUser(ctx, commitTx, commitUsername)
	if err != nil {
		_ = commitTx.Rollback(context.Background())
		t.Fatal(err)
	}
	commitInput := fixture.linkInput(
		commitUserID, "external-link:registration:commit", strings.Repeat("3", 64),
	)
	commitInput.ProviderOperation = "registration.complete"
	commitInput.ActorUserID = 0
	committed, err := fixture.externalLinks.LinkTx(ctx, commitTx, commitInput)
	if err != nil {
		_ = commitTx.Rollback(context.Background())
		t.Fatal(err)
	}
	// 生产 caller 在这里执行 exact runtime fence；通过后立即提交同一事务。
	if err := commitTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if committed.Link.UserID != commitUserID || committed.Link.ActorUserID != 0 ||
		committed.Link.Status != ExternalIdentityLinkStatusActive {
		t.Fatalf("registration mutation=%#v", committed)
	}
	fixture.assertExternalLinkCounts(1, 1, 1)
}

func insertExternalIdentityRegistrationUser(ctx context.Context, tx pgx.Tx, username string) (int64, error) {
	var userID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name, locale, status)
		VALUES ($1, $1, $2, $2, 'Identity Registration', 'zh-CN', 'active')
		RETURNING id
	`, username, username+"@example.test").Scan(&userID)
	return userID, err
}

func TestExternalIdentityLinkPostgresRejectsDriftAndRollsBack(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()

	fenceErr := errors.New("runtime lease changed")
	if _, err := fixture.externalLinks.Link(
		ctx,
		fixture.linkInput(fixture.targetUserID, "external-link:fence", strings.Repeat("a", 64)),
		func() error { return fenceErr },
	); !errors.Is(err, fenceErr) {
		t.Fatalf("fence failure error=%v", err)
	}
	fixture.assertExternalLinkCounts(0, 0, 0)

	invalidActor := fixture.linkInput(fixture.targetUserID, "external-link:invalid-actor", strings.Repeat("b", 64))
	invalidActor.ActorUserID = 9_999_999_999
	if _, err := fixture.externalLinks.Link(ctx, invalidActor, func() error { return nil }); !errors.Is(err, ErrExternalIdentityLinkInvalid) {
		t.Fatalf("invalid actor error=%v", err)
	}
	fixture.assertExternalLinkCounts(0, 0, 0)

	tx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.externalLinks.LinkTx(
		ctx, tx,
		fixture.linkInput(fixture.targetUserID, "external-link:outer-rollback", strings.Repeat("c", 64)),
	); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.assertExternalLinkCounts(0, 0, 0)

	drifted := fixture.linkInput(fixture.targetUserID, "external-link:artifact-drift", strings.Repeat("d", 64))
	drifted.Provider.Artifact.PackageDigest = strings.Repeat("f", 64)
	if _, err := fixture.externalLinks.Link(ctx, drifted, func() error { return nil }); !errors.Is(err, ErrExternalIdentityProviderStale) {
		t.Fatalf("artifact drift error=%v", err)
	}
	if err := fixture.retireProvider(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.externalLinks.Link(
		ctx,
		fixture.linkInput(fixture.targetUserID, "external-link:retired", strings.Repeat("e", 64)),
		func() error { return nil },
	); !errors.Is(err, ErrExternalIdentityProviderStale) {
		t.Fatalf("retired provider error=%v", err)
	}
	fixture.assertExternalLinkCounts(0, 0, 0)
}

func TestExternalIdentityLinkPostgresConcurrentSameKeyReplay(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	input := fixture.linkInput(fixture.targetUserID, "external-link:concurrent-replay", strings.Repeat("6", 64))

	const workers = 6
	start := make(chan struct{})
	results := make(chan externalIdentityLinkAttempt, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			mutation, err := fixture.externalLinks.Link(ctx, input, func() error { return nil })
			results <- externalIdentityLinkAttempt{mutation: mutation, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	fresh := 0
	var linkID int64
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent replay error=%v", result.err)
		}
		if !result.mutation.Replayed {
			fresh++
		}
		if linkID == 0 {
			linkID = result.mutation.Link.ID
		} else if result.mutation.Link.ID != linkID {
			t.Fatalf("replay returned link id=%d want %d", result.mutation.Link.ID, linkID)
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh mutations=%d want 1", fresh)
	}
	fixture.assertExternalLinkCounts(1, 1, 1)
}

func TestExternalIdentityLinkPostgresConcurrentSubjectUniqueness(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	subjectDigest := strings.Repeat("7", 64)

	start := make(chan struct{})
	results := make(chan externalIdentityLinkAttempt, 2)
	users := []int64{fixture.targetUserID, fixture.otherUserID}
	var wait sync.WaitGroup
	wait.Add(len(users))
	for index, userID := range users {
		go func(index int, userID int64) {
			defer wait.Done()
			<-start
			mutation, err := fixture.externalLinks.Link(ctx, fixture.linkInput(
				userID, fmt.Sprintf("external-link:subject-race:%d", index), subjectDigest,
			), func() error { return nil })
			results <- externalIdentityLinkAttempt{mutation: mutation, err: err}
		}(index, userID)
	}
	close(start)
	wait.Wait()
	close(results)

	succeeded, conflicted := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, ErrExternalIdentitySubjectConflict):
			conflicted++
		default:
			t.Fatalf("subject race error=%v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("subject race success=%d conflict=%d", succeeded, conflicted)
	}
	fixture.assertExternalLinkCounts(1, 1, 1)
}

func TestExternalIdentityLinkPostgresTransitionCAS(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	linked, err := fixture.externalLinks.Link(
		ctx,
		fixture.linkInput(fixture.targetUserID, "external-link:cas:link", strings.Repeat("8", 64)),
		func() error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan externalIdentityLinkAttempt, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		mutation, err := fixture.externalLinks.Unlink(ctx, TransitionExternalIdentityLinkInput{
			LinkID: linked.Link.ID, ExpectedRevision: 1, ActorUserID: fixture.actorUserID,
			IdempotencyKey: "external-link:cas:unlink",
		})
		results <- externalIdentityLinkAttempt{mutation: mutation, err: err}
	}()
	go func() {
		defer wait.Done()
		<-start
		mutation, err := fixture.externalLinks.Erase(ctx, TransitionExternalIdentityLinkInput{
			LinkID: linked.Link.ID, ExpectedRevision: 1, ActorUserID: fixture.actorUserID,
			IdempotencyKey: "external-link:cas:erase",
		})
		results <- externalIdentityLinkAttempt{mutation: mutation, err: err}
	}()
	close(start)
	wait.Wait()
	close(results)

	succeeded, conflicted := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, ErrExternalIdentityLinkStateConflict):
			conflicted++
		default:
			t.Fatalf("transition race error=%v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("transition race success=%d conflict=%d", succeeded, conflicted)
	}
	current, err := fixture.externalLinks.Get(ctx, linked.Link.ID)
	if err != nil || current.Revision != 2 ||
		(current.Status != ExternalIdentityLinkStatusUnlinked && current.Status != ExternalIdentityLinkStatusErased) {
		t.Fatalf("current link=%#v error=%v", current, err)
	}
	fixture.assertExternalLinkCounts(1, 2, 2)
}

func TestExternalIdentityLinkPostgresInvalidTransitionActorRollsBack(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()
	linked, err := fixture.externalLinks.Link(
		ctx,
		fixture.linkInput(fixture.targetUserID, "external-link:invalid-transition:link", strings.Repeat("9", 64)),
		func() error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.externalLinks.Unlink(ctx, TransitionExternalIdentityLinkInput{
		LinkID: linked.Link.ID, ExpectedRevision: 1, ActorUserID: 9_999_999_999,
		IdempotencyKey: "external-link:invalid-transition:unlink",
	}); !errors.Is(err, ErrExternalIdentityLinkInvalid) {
		t.Fatalf("invalid transition actor error=%v", err)
	}
	current, err := fixture.externalLinks.Get(ctx, linked.Link.ID)
	if err != nil || current.Status != ExternalIdentityLinkStatusActive || current.Revision != 1 {
		t.Fatalf("failed audit changed link=%#v error=%v", current, err)
	}
	fixture.assertExternalLinkCounts(1, 1, 1)
}

type externalIdentityLinkAttempt struct {
	mutation ExternalIdentityLinkMutation
	err      error
}
