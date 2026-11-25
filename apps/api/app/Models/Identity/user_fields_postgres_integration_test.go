package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIdentityUserFieldPostgresLifecycleAndRedaction(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()

	initialInput := fixture.userFieldSetInput(
		fixture.targetUserID, `"member-code"`, "user-field:lifecycle:set", 0,
	)
	initial, err := fixture.userFields.Set(ctx, initialInput)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Replayed || !initial.CurrentAvailable || initial.Value.State != IdentityUserFieldValueStateActive ||
		initial.Value.Revision != 1 || initial.Value.FieldID != identityPersistenceFieldID ||
		initial.Value.OwnerExtensionID != fixture.extensionID ||
		initial.Value.FieldContractVersion != fixture.field.ContractVersion ||
		initial.Value.FieldSchemaDigest != fixture.field.SchemaDigest ||
		initial.Value.DeclarationRevision <= 0 ||
		initial.Event.IdempotencyKey == initialInput.IdempotencyKey ||
		!validIdentityUserFieldDigest(initial.Event.IdempotencyKey) ||
		initial.Event.NextValueDigest != initial.Value.ValueDigest ||
		initial.Event.PreviousValueDigest != "" {
		t.Fatalf("initial mutation=%#v", initial)
	}
	if initial.Value.ValueDigest == identityUserFieldSHA256([]byte(`"member-code"`)) {
		t.Fatal("value event used an enumerable unkeyed digest")
	}

	replay, err := fixture.userFields.Set(ctx, initialInput)
	if err != nil || !replay.Replayed || replay.Event.ID != initial.Event.ID ||
		replay.Value.Revision != initial.Value.Revision {
		t.Fatalf("initial replay=%#v error=%v", replay, err)
	}
	changedReplay := initialInput
	changedReplay.Value = json.RawMessage(`"another-code"`)
	if _, err := fixture.userFields.Set(ctx, changedReplay); !errors.Is(err, ErrIdentityUserFieldValueIdempotencyConflict) {
		t.Fatalf("changed replay error=%v", err)
	}

	read, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID,
	})
	if err != nil || !bytes.Equal(read.Value, []byte(`"member-code"`)) ||
		read.Revision != 1 || read.ValueDigest != initial.Value.ValueDigest {
		t.Fatalf("initial read=%#v error=%v", read, err)
	}

	updated, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"updated-code"`, "user-field:lifecycle:update", 1,
	))
	if err != nil || updated.Value.Revision != 2 ||
		updated.Event.PreviousValueDigest != initial.Value.ValueDigest ||
		updated.Event.NextValueDigest != updated.Value.ValueDigest {
		t.Fatalf("updated=%#v error=%v", updated, err)
	}
	erased, err := fixture.userFields.Erase(ctx, EraseIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID, ExpectedRevision: 2,
		IdempotencyScope: identityPersistenceFieldScope,
		IdempotencyKey:   "user-field:lifecycle:erase",
	})
	if err != nil || erased.Value.State != IdentityUserFieldValueStateErased ||
		erased.Value.Revision != 3 || erased.Value.ValueDigest != "" ||
		erased.Event.PreviousValueDigest != updated.Value.ValueDigest ||
		erased.Event.NextValueDigest != "" {
		t.Fatalf("erased=%#v error=%v", erased, err)
	}
	if _, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID,
	}); !errors.Is(err, ErrIdentityUserFieldValueNotFound) {
		t.Fatalf("read erased value error=%v", err)
	}
	oldReceipt, err := fixture.userFields.Set(ctx, initialInput)
	if err != nil || !oldReceipt.Replayed || !oldReceipt.CurrentAvailable ||
		oldReceipt.Event.ID != initial.Event.ID || oldReceipt.Value.State != IdentityUserFieldValueStateErased ||
		oldReceipt.Value.Revision != 3 {
		t.Fatalf("old receipt with current tombstone=%#v error=%v", oldReceipt, err)
	}

	restored, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"restored-code"`, "user-field:lifecycle:restore", 3,
	))
	if err != nil || restored.Value.State != IdentityUserFieldValueStateActive ||
		restored.Value.Revision != 4 || restored.Event.PreviousValueDigest != "" {
		t.Fatalf("restored=%#v error=%v", restored, err)
	}
	fixture.assertUserFieldCounts(1, 4, 4)

	encoded, err := json.Marshal([]IdentityUserFieldValueMutation{initial, updated, erased, restored})
	if err != nil {
		t.Fatal(err)
	}
	fixture.assertUserFieldPIIRedacted(
		encoded, "member-code", "another-code", "updated-code", "restored-code",
	)
}

func TestIdentityUserFieldPostgresPermissionLifecycleAndPrivacy(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	if _, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID, FieldID: "bad/field",
	}); !errors.Is(err, ErrIdentityUserFieldValueInvalid) {
		t.Fatalf("invalid read field id error=%v", err)
	}

	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"x"`, "user-field:denied:schema", 0,
	)); !errors.Is(err, ErrIdentityUserFieldSchemaInvalid) {
		t.Fatalf("invalid Schema value error=%v", err)
	}
	unauthorized := fixture.userFieldSetInput(
		fixture.targetUserID, `"member-code"`, "user-field:denied:actor", 0,
	)
	unauthorized.ActorUserID = fixture.otherUserID
	if _, err := fixture.userFields.Set(ctx, unauthorized); !errors.Is(err, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("unauthorized actor error=%v", err)
	}
	fixture.assertUserFieldCounts(0, 0, 0)

	input := fixture.userFieldSetInput(
		fixture.targetUserID, `"member-code"`, "user-field:authority:set", 0,
	)
	set, err := fixture.userFields.Set(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_permission_overrides (user_id, permission_key, effect)
		VALUES ($1, $2, 'deny')
	`, fixture.actorUserID, identityPersistencePermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID,
	}); !errors.Is(err, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("direct deny read error=%v", err)
	}
	// 普通回放仍要通过当前权限检查，旧回执不能绕过后来生效的 deny。
	if _, err := fixture.userFields.Set(ctx, input); !errors.Is(err, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("revoked receipt replay error=%v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		DELETE FROM user_permission_overrides
		WHERE user_id = $1 AND permission_key = $2
	`, fixture.actorUserID, identityPersistencePermissionKey); err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.userFields.Set(ctx, input)
	if err != nil || !replay.Replayed || replay.Event.ID != set.Event.ID {
		t.Fatalf("restored-authority receipt replay=%#v error=%v", replay, err)
	}

	if _, err := fixture.pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, fixture.targetUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID,
	}); err != nil {
		t.Fatalf("authorized disabled-target read error=%v", err)
	}
	updated, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"disabled-code"`, "user-field:authority:update", 1,
	))
	if err != nil || updated.Value.Revision != 2 {
		t.Fatalf("disabled-target update=%#v error=%v", updated, err)
	}

	if _, err := fixture.pool.Exec(ctx, `
		DELETE FROM role_permissions
		WHERE permission_key = $1
	`, identityPersistencePermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"denied-code"`, "user-field:authority:revoked", 2,
	)); !errors.Is(err, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("revoked write error=%v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, permission_key)
		SELECT id, $1 FROM roles WHERE key = 'identity_reviewer'
	`, identityPersistencePermissionKey); err != nil {
		t.Fatal(err)
	}

	if err := fixture.retireIdentityPublication(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Set(ctx, input); !errors.Is(err, ErrIdentityUserFieldDeclarationStale) {
		t.Fatalf("retired receipt replay error=%v", err)
	}
	if _, err := fixture.userFields.Get(ctx, ReadIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID,
	}); !errors.Is(err, ErrIdentityUserFieldDeclarationStale) {
		t.Fatalf("retired ordinary read error=%v", err)
	}
	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"retired-code"`, "user-field:authority:retired", 2,
	)); !errors.Is(err, ErrIdentityUserFieldDeclarationStale) {
		t.Fatalf("retired ordinary write error=%v", err)
	}

	erased, err := fixture.userFields.EraseForPrivacy(ctx, EraseIdentityUserFieldValueInput{
		UserID: fixture.targetUserID, FieldID: identityPersistenceFieldID,
		ExpectedRevision: 2, IdempotencyScope: identityPersistencePrivacyScope,
		IdempotencyKey: "user-field:privacy:erase",
	})
	if err != nil || erased.Value.State != IdentityUserFieldValueStateErased ||
		erased.Value.Revision != 3 || erased.Event.ActorUserID != 0 {
		t.Fatalf("privacy erase=%#v error=%v", erased, err)
	}
	var storedValue []byte
	if err := fixture.pool.QueryRow(ctx, `
		SELECT value_json FROM identity_user_field_values
		WHERE user_id = $1 AND field_id = $2
	`, fixture.targetUserID, identityPersistenceFieldID).Scan(&storedValue); err != nil {
		t.Fatal(err)
	}
	if len(storedValue) != 0 {
		t.Fatalf("privacy erase retained value=%q", storedValue)
	}
	fixture.assertUserFieldPIIRedacted(nil, "member-code", "disabled-code", "denied-code", "retired-code")

	if _, err := fixture.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetUserID); err != nil {
		t.Fatal(err)
	}
	var currentCount, eventCount int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM identity_user_field_values`).Scan(&currentCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM identity_user_field_value_events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if currentCount != 0 || eventCount != 3 {
		t.Fatalf("privacy deletion current=%d events=%d", currentCount, eventCount)
	}
	setReadback, found, err := fixture.userFields.readbackMutation(
		ctx, set.Event.IdempotencyKey, IdentityUserFieldValueActionSet, set.Event.RequestFingerprint,
	)
	if err != nil || !found || setReadback.CurrentAvailable ||
		setReadback.Value.State != IdentityUserFieldValueStateActive ||
		setReadback.Value.ValueDigest != set.Event.NextValueDigest ||
		setReadback.Event.ID != set.Event.ID {
		t.Fatalf("set receipt readback after user deletion=%#v found=%t error=%v", setReadback, found, err)
	}
	if _, found, err := fixture.userFields.readbackMutation(
		ctx, set.Event.IdempotencyKey, IdentityUserFieldValueActionErase, set.Event.RequestFingerprint,
	); !found || !errors.Is(err, ErrIdentityUserFieldValueIdempotencyConflict) {
		t.Fatalf("set receipt wrong-action readback found=%t error=%v", found, err)
	}
	if _, found, err := fixture.userFields.readbackMutation(
		ctx, set.Event.IdempotencyKey, IdentityUserFieldValueActionSet, strings.Repeat("f", 64),
	); !found || !errors.Is(err, ErrIdentityUserFieldValueIdempotencyConflict) {
		t.Fatalf("set receipt wrong-fingerprint readback found=%t error=%v", found, err)
	}
	privacyReplay, err := fixture.userFields.EraseForPrivacy(ctx, EraseIdentityUserFieldValueInput{
		UserID: fixture.targetUserID, FieldID: identityPersistenceFieldID,
		ExpectedRevision: 2, IdempotencyScope: identityPersistencePrivacyScope,
		IdempotencyKey: "user-field:privacy:erase",
	})
	if err != nil || !privacyReplay.Replayed || privacyReplay.CurrentAvailable ||
		privacyReplay.Value.State != IdentityUserFieldValueStateErased ||
		privacyReplay.Event.ID != erased.Event.ID {
		t.Fatalf("privacy replay after user deletion=%#v error=%v", privacyReplay, err)
	}
	if _, err := fixture.userFields.EraseForPrivacy(ctx, EraseIdentityUserFieldValueInput{
		UserID: fixture.targetUserID, FieldID: identityPersistenceFieldID,
		ExpectedRevision: 3, IdempotencyScope: identityPersistencePrivacyScope,
		IdempotencyKey: "user-field:privacy:fresh-after-delete",
	}); !errors.Is(err, ErrIdentityUserFieldValueNotFound) {
		t.Fatalf("fresh privacy erase after user deletion error=%v", err)
	}
}

func TestIdentityUserFieldPostgresOrdinaryEraseReplayRequiresLiveAuthority(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 30*time.Second)
	defer cancel()
	if _, err := fixture.userFields.Set(ctx, fixture.userFieldSetInput(
		fixture.targetUserID, `"erase-replay"`, "user-field:erase-replay:set", 0,
	)); err != nil {
		t.Fatal(err)
	}
	input := EraseIdentityUserFieldValueInput{
		ActorUserID: fixture.actorUserID, UserID: fixture.targetUserID,
		FieldID: identityPersistenceFieldID, ExpectedRevision: 1,
		IdempotencyScope: identityPersistenceFieldScope,
		IdempotencyKey:   "user-field:erase-replay:erase",
	}
	erased, err := fixture.userFields.Erase(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.userFields.Erase(ctx, input)
	if err != nil || !replay.Replayed || replay.Event.ID != erased.Event.ID {
		t.Fatalf("live erase replay=%#v error=%v", replay, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO user_permission_overrides (user_id, permission_key, effect)
		VALUES ($1, $2, 'deny')
	`, fixture.actorUserID, identityPersistencePermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Erase(ctx, input); !errors.Is(err, ErrIdentityUserFieldPermissionDenied) {
		t.Fatalf("denied erase replay error=%v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		DELETE FROM user_permission_overrides
		WHERE user_id = $1 AND permission_key = $2
	`, fixture.actorUserID, identityPersistencePermissionKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, fixture.targetUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.userFields.Erase(ctx, input); !errors.Is(err, ErrIdentityUserFieldValueStateConflict) {
		t.Fatalf("deleted-target erase replay error=%v", err)
	}
}

func TestIdentityUserFieldPostgresCallerTransactionAndFinalFence(t *testing.T) {
	t.Parallel()
	fixture := newIdentityPersistencePGFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 20*time.Second)
	defer cancel()
	readCommitted, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.userFields.SetTx(ctx, readCommitted, fixture.userFieldSetInput(
		fixture.targetUserID, `"wrong-isolation"`, "user-field:tx:isolation", 0,
	)); !errors.Is(err, ErrIdentityUserFieldTransactionIsolation) {
		_ = readCommitted.Rollback(context.Background())
		t.Fatalf("read-committed transaction error=%v", err)
	}
	if err := readCommitted.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.assertUserFieldCounts(0, 0, 0)

	tx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	mutation, fence, err := fixture.userFields.SetTx(ctx, tx, fixture.userFieldSetInput(
		fixture.targetUserID, `"transaction-code"`, "user-field:tx:rollback", 0,
	))
	if err != nil || mutation.Value.Revision != 1 || fence == nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("transaction mutation=%#v fence=%v error=%v", mutation, fence != nil, err)
	}
	if err := fence(); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.assertUserFieldCounts(0, 0, 0)

	driftTx, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	_, driftFence, err := fixture.userFields.SetTx(ctx, driftTx, fixture.userFieldSetInput(
		fixture.targetUserID, `"drift-code"`, "user-field:tx:drift", 0,
	))
	if err != nil {
		_ = driftTx.Rollback(context.Background())
		t.Fatal(err)
	}
	if _, removed, err := fixture.registry.Remove(fixture.publication.Artifact); err != nil || !removed {
		_ = driftTx.Rollback(context.Background())
		t.Fatalf("remove publication removed=%t error=%v", removed, err)
	}
	if err := driftFence(); !errors.Is(err, ErrIdentityUserFieldDeclarationStale) {
		_ = driftTx.Rollback(context.Background())
		t.Fatalf("drift fence error=%v", err)
	}
	if err := driftTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.assertUserFieldCounts(0, 0, 0)
}

func (f *identityPersistencePGFixture) userFieldSetInput(
	userID int64,
	value string,
	key string,
	expectedRevision int64,
) SetIdentityUserFieldValueInput {
	return SetIdentityUserFieldValueInput{
		ActorUserID: f.actorUserID, UserID: userID, FieldID: identityPersistenceFieldID,
		Value: json.RawMessage(value), ExpectedRevision: expectedRevision,
		IdempotencyScope: identityPersistenceFieldScope, IdempotencyKey: key,
	}
}

func (f *identityPersistencePGFixture) retireIdentityPublication() error {
	if err := f.retireProvider(); err != nil {
		return err
	}
	_, _, err := f.registry.Remove(f.publication.Artifact)
	return err
}

func (f *identityPersistencePGFixture) assertUserFieldCounts(values, events, audits int) {
	f.t.Helper()
	var gotValues, gotEvents, gotAudits int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM identity_user_field_values`).Scan(&gotValues); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM identity_user_field_value_events`).Scan(&gotEvents); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		SELECT count(*) FROM audit_events WHERE action LIKE 'identity.user_field.%'
	`).Scan(&gotAudits); err != nil {
		f.t.Fatal(err)
	}
	if gotValues != values || gotEvents != events || gotAudits != audits {
		f.t.Fatalf(
			"user-field counts values=%d/%d events=%d/%d audits=%d/%d",
			gotValues, values, gotEvents, events, gotAudits, audits,
		)
	}
}

func (f *identityPersistencePGFixture) assertUserFieldPIIRedacted(extra []byte, rawValues ...string) {
	f.t.Helper()
	var auditMetadata, eventRows string
	if err := f.pool.QueryRow(f.ctx, `
		SELECT COALESCE(string_agg(metadata::text, ' '), '')
		FROM audit_events WHERE action LIKE 'identity.user_field.%'
	`).Scan(&auditMetadata); err != nil {
		f.t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `
		SELECT COALESCE(string_agg(row_to_json(event)::text, ' '), '')
		FROM identity_user_field_value_events AS event
	`).Scan(&eventRows); err != nil {
		f.t.Fatal(err)
	}
	combined := string(extra) + auditMetadata + eventRows
	for _, raw := range rawValues {
		if strings.Contains(combined, raw) {
			f.t.Fatalf("user-field PII %q leaked into mutation/audit/event: %s", raw, combined)
		}
	}
}
