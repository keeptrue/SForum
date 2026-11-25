package identity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIdentityUserFieldPostgresJSONBCanonicalDigest(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()
	tx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	exponential, err := prepareIdentityUserFieldSet(SetIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID, Value: []byte(`1e2`),
		IdempotencyScope: identityPersistenceFieldScope,
		IdempotencyKey:   "user-field:canonical:number",
	})
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	plain := exponential
	plain.canonicalValue = []byte(`100`)
	plain.value = nil
	exponential, err = fixture.userFields.canonicalizeIdentityUserFieldSet(ctx, tx, exponential)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	plain, err = fixture.userFields.canonicalizeIdentityUserFieldSet(ctx, tx, plain)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	exponentialFingerprint, _ := identityUserFieldSetFingerprint(exponential)
	plainFingerprint, _ := identityUserFieldSetFingerprint(plain)
	if string(exponential.canonicalValue) != "100" ||
		exponential.valueDigest != plain.valueDigest || exponentialFingerprint != plainFingerprint {
		_ = tx.Rollback(context.Background())
		t.Fatalf(
			"PostgreSQL numeric canonicalization exponential=%q/%s plain=%q/%s",
			exponential.canonicalValue, exponential.valueDigest,
			plain.canonicalValue, plain.valueDigest,
		)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	input := fixture.userFieldSetInput(
		fixture.targetUserID, `"member<code"`, "user-field:canonical", 0,
	)
	mutation, err := fixture.userFields.Set(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	read, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID,
	})
	if err != nil || mutation.Value.ValueDigest != read.ValueDigest ||
		mutation.Event.NextValueDigest != read.ValueDigest || string(read.Value) != `"member<code"` {
		t.Fatalf("canonical mutation=%#v read=%#v error=%v", mutation, read, err)
	}
	replay, err := fixture.userFields.Set(ctx, input)
	if err != nil || !replay.Replayed || replay.Event.ID != mutation.Event.ID ||
		replay.Value.ValueDigest != read.ValueDigest {
		t.Fatalf("canonical replay=%#v error=%v", replay, err)
	}
	fixture.assertUserFieldPIIRedacted(nil, "member<code")
}

func TestIdentityUserFieldPostgresConcurrentSameKeyReplay(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	input := fixture.userFieldSetInput(
		fixture.targetUserID, `"concurrent-code"`, "user-field:race:replay", 0,
	)

	const workers = 6
	start := make(chan struct{})
	results := make(chan identityUserFieldAttempt, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			mutation, err := fixture.userFields.Set(ctx, input)
			results <- identityUserFieldAttempt{mutation: mutation, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	fresh := 0
	var eventID int64
	for result := range results {
		if result.err != nil {
			t.Fatalf("same-key replay error=%v", result.err)
		}
		if !result.mutation.Replayed {
			fresh++
		}
		if eventID == 0 {
			eventID = result.mutation.Event.ID
		} else if result.mutation.Event.ID != eventID {
			t.Fatalf("same-key event=%d want=%d", result.mutation.Event.ID, eventID)
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh same-key mutations=%d", fresh)
	}
	fixture.assertUserFieldCounts(1, 1, 1)
}

func TestIdentityUserFieldPostgresConcurrentAbsentCAS(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()

	start := make(chan struct{})
	results := make(chan identityUserFieldAttempt, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for index, value := range []string{`"first-code"`, `"second-code"`} {
		go func(index int, value string) {
			defer wait.Done()
			<-start
			mutation, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
				fixture.targetUserID, value, "user-field:race:absent:"+string(rune('a'+index)), 0,
			))
			results <- identityUserFieldAttempt{mutation: mutation, err: err}
		}(index, value)
	}
	close(start)
	wait.Wait()
	close(results)
	assertOneIdentityUserFieldWinner(t, results)
	fixture.assertUserFieldCounts(1, 1, 1)
}

func TestIdentityUserFieldPostgresConcurrentUpdateEraseCAS(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"initial-code"`, "user-field:race:initial", 0,
	)); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan identityUserFieldAttempt, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		mutation, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
			fixture.targetUserID, `"updated-code"`, "user-field:race:update", 1,
		))
		results <- identityUserFieldAttempt{mutation: mutation, err: err}
	}()
	go func() {
		defer wait.Done()
		<-start
		mutation, err := fixture.userFields.Erase(ctx, EraseIdentityUserFieldValueInput{
			ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
			FieldID: identityPersistenceFieldID, ExpectedRevision: 1,
			IdempotencyScope: identityPersistenceFieldScope,
			IdempotencyKey:   "user-field:race:erase",
		})
		results <- identityUserFieldAttempt{mutation: mutation, err: err}
	}()
	close(start)
	wait.Wait()
	close(results)
	assertOneIdentityUserFieldWinner(t, results)
	fixture.assertUserFieldCounts(1, 2, 2)
}

func TestIdentityUserFieldPostgresPermissionRevocationRace(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()

	start := make(chan struct{})
	writeResult := make(chan error, 1)
	revokeResult := make(chan error, 1)
	go func() {
		<-start
		_, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
			fixture.targetUserID, `"race-code"`, "user-field:race:revoke", 0,
		))
		writeResult <- err
	}()
	go func() {
		<-start
		_, err := fixture.pool.Exec(ctx, `
			DELETE FROM role_permissions WHERE permission_key = $1
		`, identityPersistencePermissionKey)
		revokeResult <- err
	}()
	close(start)
	writeErr := <-writeResult
	if err := <-revokeResult; err != nil {
		t.Fatal(err)
	}
	if writeErr != nil && !errors.Is(writeErr, ErrIdentityUserFieldPermissionDenied) &&
		!errors.Is(writeErr, ErrIdentityUserFieldValueStateConflict) {
		t.Fatalf("revocation race write error=%v", writeErr)
	}
	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.otherUserID, `"after-revoke"`, "user-field:race:after-revoke", 0,
	)); !errors.Is(err, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("post-revocation write error=%v", err)
	}
}

func TestIdentityUserFieldPostgresPrivacyEraseUserDeletionRace(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"privacy-race"`, "user-field:race:privacy:set", 0,
	)); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	eraseResult := make(chan error, 1)
	deleteResult := make(chan error, 1)
	go func() {
		<-start
		_, err := fixture.userFields.EraseForPrivacy(ctx, EraseIdentityUserFieldValueInput{
			UserID: fixture.targetUserID, FieldID: identityPersistenceFieldID,
			ExpectedRevision: 1, IdempotencyScope: identityPersistencePrivacyScope,
			IdempotencyKey: "user-field:race:privacy:erase",
		})
		eraseResult <- err
	}()
	go func() {
		<-start
		_, err := fixture.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetUserID)
		deleteResult <- err
	}()
	close(start)
	eraseErr := <-eraseResult
	if err := <-deleteResult; err != nil {
		t.Fatal(err)
	}
	if eraseErr != nil && !errors.Is(eraseErr, ErrIdentityUserFieldValueNotFound) {
		t.Fatalf("privacy erase/user deletion race error=%v", eraseErr)
	}

	var users, values, events int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, fixture.targetUserID).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM identity_user_field_values WHERE user_id = $1`, fixture.targetUserID).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM identity_user_field_value_events WHERE user_id = $1`, fixture.targetUserID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	wantEvents := 1
	if eraseErr == nil {
		wantEvents = 2
	}
	if users != 0 || values != 0 || events != wantEvents {
		t.Fatalf(
			"privacy erase/user deletion result users=%d values=%d events=%d/%d erase_error=%v",
			users, values, events, wantEvents, eraseErr,
		)
	}
}

func TestIdentityUserFieldPostgresOrdinaryReplayRequiresLiveTarget(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()
	input := fixture.userFieldSetInput(
		fixture.targetUserID, `"deleted-target"`, "user-field:replay:deleted-target", 0,
	)
	if _, err := fixture.userFields.Set(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Set(ctx, input); !errors.Is(err, ErrIdentityUserFieldValueStateConflict) {
		t.Fatalf("ordinary replay after target deletion error=%v", err)
	}
}

func TestIdentityUserFieldPostgresOrdinaryReplayActorDeletionRace(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1, id FROM roles WHERE key = 'identity_reviewer'
	`, fixture.targetUserID); err != nil {
		t.Fatal(err)
	}
	input := fixture.userFieldSetInput(
		fixture.targetUserID, `"actor-delete"`, "user-field:replay:actor-delete", 0,
	)
	input.ActorUserID = fixture.targetUserID
	mutation, err := fixture.userFields.Set(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	replayResult := make(chan error, 1)
	deleteResult := make(chan error, 1)
	go func() {
		<-start
		_, err := fixture.userFields.Set(ctx, input)
		replayResult <- err
	}()
	go func() {
		<-start
		_, err := fixture.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetUserID)
		deleteResult <- err
	}()
	close(start)
	replayErr := <-replayResult
	if err := <-deleteResult; err != nil {
		t.Fatal(err)
	}
	if replayErr != nil && !errors.Is(replayErr, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("ordinary replay/actor deletion race error=%v", replayErr)
	}

	var actorCleared bool
	if err := fixture.pool.QueryRow(ctx, `
		SELECT actor_user_id IS NULL
		FROM identity_user_field_value_events WHERE id = $1
	`, mutation.Event.ID).Scan(&actorCleared); err != nil {
		t.Fatal(err)
	}
	if !actorCleared {
		t.Fatal("user deletion did not clear retained event actor")
	}
	for _, query := range []string{
		`UPDATE identity_user_field_value_events SET audit_event_id = audit_event_id + 1 WHERE id = $1`,
		`DELETE FROM identity_user_field_value_events WHERE id = $1`,
	} {
		if _, err := fixture.pool.Exec(ctx, query, mutation.Event.ID); err == nil {
			t.Fatalf("append-only event mutation %q succeeded", query)
		}
	}
}

type identityUserFieldAttempt struct {
	mutation IdentityUserFieldValueMutation
	err      error
}

func assertOneIdentityUserFieldWinner(t *testing.T, results <-chan identityUserFieldAttempt) {
	t.Helper()
	succeeded, conflicted := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, ErrIdentityUserFieldValueStateConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent user-field error=%v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent user-field success=%d conflict=%d", succeeded, conflicted)
	}
}
