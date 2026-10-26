package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

func TestIdentityUserFieldMisconfiguredStoreFailsUnavailable(t *testing.T) {
	ctx := context.Background()
	setInput := SetIdentityUserFieldValueInput{
		ActorUserID: 7, UserID: 11, FieldID: "fixture.identity.field",
		Value: json.RawMessage(`"member-code"`), IdempotencyScope: "fixture.identity.write@1",
		IdempotencyKey: "field:set:misconfigured",
	}
	eraseInput := EraseIdentityUserFieldValueInput{
		ActorUserID: 7, UserID: 11, FieldID: "fixture.identity.field",
		ExpectedRevision: 1, IdempotencyScope: "fixture.identity.write@1",
		IdempotencyKey: "field:erase:misconfigured",
	}
	stores := []*PostgresIdentityUserFieldValueStore{
		nil,
		{},
		{registry: identityregistry.New(), digestKey: bytes.Repeat([]byte{'k'}, 32)},
		{pool: &pgxpool.Pool{}, digestKey: bytes.Repeat([]byte{'k'}, 32)},
		{pool: &pgxpool.Pool{}, registry: identityregistry.New(), digestKey: []byte("short")},
		{pool: &pgxpool.Pool{}, registry: identityregistry.New(), digestKey: bytes.Repeat([]byte{'k'}, 33)},
	}
	for index, store := range stores {
		if _, err := store.Set(ctx, setInput); !errors.Is(err, ErrIdentityUserFieldValueStoreUnavailable) {
			t.Fatalf("misconfigured store %d set error=%v", index, err)
		}
		if _, err := store.Erase(ctx, eraseInput); !errors.Is(err, ErrIdentityUserFieldValueStoreUnavailable) {
			t.Fatalf("misconfigured store %d erase error=%v", index, err)
		}
		eraseInput.ActorUserID = 0
		if _, err := store.EraseForPrivacy(ctx, eraseInput); !errors.Is(err, ErrIdentityUserFieldValueStoreUnavailable) {
			t.Fatalf("misconfigured store %d privacy erase error=%v", index, err)
		}
		eraseInput.ActorUserID = 7
		if _, err := store.Get(ctx, ReadIdentityUserFieldValueInput{
			ActorUserID: 7, UserID: 11, FieldID: "fixture.identity.field",
		}); !errors.Is(err, ErrIdentityUserFieldValueStoreUnavailable) {
			t.Fatalf("misconfigured store %d get error=%v", index, err)
		}
	}
}

func TestNewPostgresIdentityUserFieldValueStoreCopiesDigestKey(t *testing.T) {
	if _, err := NewPostgresIdentityUserFieldValueStore(nil, identityregistry.New(), bytes.Repeat([]byte{'k'}, 32)); !errors.Is(err, ErrIdentityUserFieldValueStoreUnavailable) {
		t.Fatalf("nil pool constructor error=%v", err)
	}
	if _, err := NewPostgresIdentityUserFieldValueStore(&pgxpool.Pool{}, nil, bytes.Repeat([]byte{'k'}, 32)); !errors.Is(err, ErrIdentityUserFieldValueStoreUnavailable) {
		t.Fatalf("nil Registry constructor error=%v", err)
	}
	for _, size := range []int{0, 31, 33} {
		if _, err := NewPostgresIdentityUserFieldValueStore(
			&pgxpool.Pool{}, identityregistry.New(), bytes.Repeat([]byte{'k'}, size),
		); !errors.Is(err, ErrIdentityUserFieldDigestKeyInvalid) {
			t.Fatalf("digest key size %d constructor error=%v", size, err)
		}
	}
	key := bytes.Repeat([]byte{'k'}, 32)
	store, err := NewPostgresIdentityUserFieldValueStore(&pgxpool.Pool{}, identityregistry.New(), key)
	if err != nil {
		t.Fatal(err)
	}
	key[0] = 'x'
	if store.digestKey[0] != 'k' {
		t.Fatal("user-field Store retained the caller's mutable digest key")
	}
}

func TestPrepareIdentityUserFieldInputsRejectAuthorityAndInvalidJSON(t *testing.T) {
	valid := SetIdentityUserFieldValueInput{
		ActorUserID: 7, UserID: 11, FieldID: " Fixture.Identity.Field ",
		Value: json.RawMessage(` {"z":2,"a":1} `), IdempotencyScope: "fixture.identity.write@1",
		IdempotencyKey: "field:set:1",
	}
	prepared, err := prepareIdentityUserFieldSet(valid)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.fieldID != "fixture.identity.field" ||
		!bytes.Equal(prepared.canonicalValue, []byte(`{"a":1,"z":2}`)) || prepared.valueDigest != "" {
		t.Fatalf("prepared set=%#v", prepared)
	}

	tests := []SetIdentityUserFieldValueInput{
		{},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(``), IdempotencyScope: "fixture.write@1", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(`{} {}`), IdempotencyScope: "fixture.write@1", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(`{`), IdempotencyScope: "fixture.write@1", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(`{}`), ExpectedRevision: -1, IdempotencyScope: "fixture.write@1", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(`{}`), IdempotencyScope: "fixture.write@1", IdempotencyKey: "has space"},
		{ActorUserID: 7, UserID: 11, FieldID: "bad/field", Value: json.RawMessage(`{}`), IdempotencyScope: "fixture.write@1", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "a" + strings.Repeat("b", 121), Value: json.RawMessage(`{}`), IdempotencyScope: "fixture.write@1", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(`{}`), IdempotencyScope: "has space", IdempotencyKey: "k"},
		{ActorUserID: 7, UserID: 11, FieldID: "x.field", Value: json.RawMessage(`{}`), IdempotencyScope: strings.Repeat("s", maximumIdentityUserFieldScopeBytes+1), IdempotencyKey: "k"},
	}
	for index, input := range tests {
		if _, err := prepareIdentityUserFieldSet(input); !errors.Is(err, ErrIdentityUserFieldValueInvalid) {
			t.Fatalf("invalid set %d error=%v", index, err)
		}
	}
	if _, err := prepareIdentityUserFieldErase(EraseIdentityUserFieldValueInput{
		UserID: 11, FieldID: "fixture.identity.field", ExpectedRevision: 0,
		IdempotencyScope: "host.identity.privacy@1", IdempotencyKey: "erase",
	}); !errors.Is(err, ErrIdentityUserFieldValueInvalid) {
		t.Fatalf("zero-revision erase error=%v", err)
	}
}

func TestIdentityUserFieldValueDigestIsKeyedAndMutationIsRedacted(t *testing.T) {
	value := []byte(`"member-code"`)
	left := identityUserFieldHMAC([]byte(strings.Repeat("a", 32)), 9, "fixture.identity.field", value)
	replay := identityUserFieldHMAC([]byte(strings.Repeat("a", 32)), 9, "fixture.identity.field", value)
	right := identityUserFieldHMAC([]byte(strings.Repeat("b", 32)), 9, "fixture.identity.field", value)
	otherUser := identityUserFieldHMAC([]byte(strings.Repeat("a", 32)), 10, "fixture.identity.field", value)
	otherField := identityUserFieldHMAC([]byte(strings.Repeat("a", 32)), 9, "fixture.identity.other", value)
	if left != replay || left == right || left == identityUserFieldSHA256(value) ||
		left == otherUser || left == otherField || !validIdentityUserFieldDigest(left) {
		t.Fatalf(
			"keyed digests left=%q replay=%q right=%q other_user=%q other_field=%q",
			left, replay, right, otherUser, otherField,
		)
	}

	encoded, err := json.Marshal(IdentityUserFieldValueMutation{
		Value: IdentityUserFieldValue{UserID: 9, FieldID: "fixture.identity.field", ValueDigest: left},
		Event: IdentityUserFieldValueEvent{NextValueDigest: left},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, value) || bytes.Contains(encoded, []byte("member-code")) {
		t.Fatalf("mutation leaked raw value: %s", encoded)
	}
}

func TestIdentityUserFieldReceiptKeyIsScopedAndOpaque(t *testing.T) {
	left := identityUserFieldReceiptKey("fixture.plugin.command@1", "same-client-key")
	replay := identityUserFieldReceiptKey("fixture.plugin.command@1", "same-client-key")
	otherScope := identityUserFieldReceiptKey("fixture.other.command@1", "same-client-key")
	otherKey := identityUserFieldReceiptKey("fixture.plugin.command@1", "other-client-key")
	if left != replay || left == otherScope || left == otherKey ||
		left == "same-client-key" || !validIdentityUserFieldDigest(left) {
		t.Fatalf(
			"receipt keys left=%q replay=%q other_scope=%q other_key=%q",
			left, replay, otherScope, otherKey,
		)
	}
}

func TestIdentityUserFieldIDMatchesRegistryAndDatabaseShape(t *testing.T) {
	for _, value := range []string{"0x", "9.field", "a_b-c.d"} {
		if !validIdentityUserFieldID(value) {
			t.Fatalf("valid field id %q was rejected", value)
		}
	}
	for _, value := range []string{"a", "Upper.field", "bad/field", ".field", "a" + strings.Repeat("b", 121)} {
		if validIdentityUserFieldID(value) {
			t.Fatalf("invalid field id %q was accepted", value)
		}
	}
}

func TestIdentityUserFieldCallerTransactionPreservesRetry(t *testing.T) {
	mapped := mapIdentityUserFieldStoreError(&pgconn.PgError{
		Code: "23505", ConstraintName: "identity_user_field_values_pkey",
	})
	if !errors.Is(mapped, errIdentityUserFieldRetry) ||
		!errors.Is(callerIdentityUserFieldStoreError(mapped), ErrIdentityUserFieldTransactionRetry) ||
		!errors.Is(publicIdentityUserFieldStoreError(mapped), ErrIdentityUserFieldValueStateConflict) {
		t.Fatalf("retry mapping=%v", mapped)
	}
}

func TestIdentityUserFieldStorageConflictReadbackEligibility(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "serialization", err: &pgconn.PgError{Code: "40001"}, want: true},
		{name: "deadlock", err: &pgconn.PgError{Code: "40P01"}, want: true},
		{name: "idempotency unique", err: &pgconn.PgError{Code: "23505", ConstraintName: "identity_user_field_value_events_idempotency_key_key"}, want: true},
		{name: "revision unique", err: &pgconn.PgError{Code: "23505", ConstraintName: "identity_user_field_value_events_user_field_revision_key"}, want: true},
		{name: "foreign key", err: &pgconn.PgError{Code: "23503"}},
		{name: "check", err: &pgconn.PgError{Code: "23514"}},
		{name: "domain idempotency", err: ErrIdentityUserFieldValueIdempotencyConflict},
		{name: "domain state", err: ErrIdentityUserFieldValueStateConflict},
		{name: "permission", err: ErrIdentityUserFieldPermissionDenied},
		{name: "isolation", err: ErrIdentityUserFieldTransactionIsolation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := identityUserFieldErrorMayFollowConcurrentCommit(test.err); got != test.want {
				t.Fatalf("readback eligibility=%t want=%t error=%v", got, test.want, test.err)
			}
		})
	}
	if mapped := mapIdentityUserFieldStoreError(&pgconn.PgError{
		Code: "23505", ConstraintName: "identity_user_field_value_events_user_field_revision_key",
	}); !errors.Is(mapped, ErrIdentityUserFieldValueStateConflict) {
		t.Fatalf("revision unique mapping=%v", mapped)
	}
	if mapped := mapIdentityUserFieldStoreError(&pgconn.PgError{
		Code: "23505", ConstraintName: "identity_user_field_value_events_idempotency_key_key",
	}); !errors.Is(mapped, ErrIdentityUserFieldValueIdempotencyConflict) {
		t.Fatalf("idempotency unique mapping=%v", mapped)
	}
}

func TestIdentityUserFieldCommitFenceIsExactAndOneShot(t *testing.T) {
	publication, err := identityPersistenceProviderPublication(
		"fixture.identity.membership", 41, strings.Repeat("a", 64), "runtime-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(publication); err != nil {
		t.Fatal(err)
	}
	field, err := registry.ResolveUserField(identityPersistenceFieldID)
	if err != nil {
		t.Fatal(err)
	}
	fence := newIdentityUserFieldCommitFence(registry, registry.Revision(), field)
	if err := fence(); err != nil {
		t.Fatalf("first fence=%v", err)
	}
	if err := fence(); !errors.Is(err, ErrIdentityUserFieldDeclarationStale) {
		t.Fatalf("reused fence=%v", err)
	}

	driftFence := newIdentityUserFieldCommitFence(registry, registry.Revision(), field)
	if _, removed, err := registry.Remove(publication.Artifact); err != nil || !removed {
		t.Fatalf("remove exact publication removed=%t error=%v", removed, err)
	}
	if err := driftFence(); !errors.Is(err, ErrIdentityUserFieldDeclarationStale) {
		t.Fatalf("drift fence=%v", err)
	}
}
