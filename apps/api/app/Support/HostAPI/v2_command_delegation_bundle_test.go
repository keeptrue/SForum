package hostapi

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
)

func TestProtocolV2ActorDelegationBundleUsesImmutableCommandCatalogAndPermissions(t *testing.T) {
	authority := testProtocolV2ActorDelegationAuthority(t, protocolV2DelegationBundleNow)
	definition := func(id, permission string, mode protocolV2CommandActorMode) protocolV2CommandDefinition {
		value := testProtocolV2CommandDefinition(t, func(context.Context, pgx.Tx, *hostv2.CommandRequest, *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
			return testProtocolV2CommandExecution(t), nil
		})
		value.ID = id
		value.ActorMode = mode
		if permission != "" {
			value.RequiredPermissions = []string{permission}
		}
		return value
	}
	engine, err := newProtocolV2CommandEngineWithActorDelegation(
		newFakeProtocolV2CommandBackend(), authority,
		definition("sforum.z.allowed", "topic.manage", protocolV2CommandActorDelegated),
		definition("sforum.a.denied", "user.manage", protocolV2CommandActorDelegated),
		definition("sforum.service", "", protocolV2CommandActorService),
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(nil)
	if err := gateway.BindProtocolV2CommandRuntime(newProtocolV2CommandRuntime(engine)); err != nil {
		t.Fatal(err)
	}
	request := ProtocolV2ActorDelegationBundleRequest{
		ActorUserID: 42, PermissionKeys: []string{" topic.manage ", "topic.manage", ""},
		Runtime: testProtocolV2ActorDelegationRequest().Runtime, IdempotencyKey: "route-request-42",
	}
	grants, err := gateway.IssueProtocolV2ActorDelegations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].CommandID != "sforum.z.allowed" ||
		grants[0].CommandVersion != testCommandVersion || grants[0].IdempotencyKey != request.IdempotencyKey || grants[0].Token == "" {
		t.Fatalf("grants = %#v", grants)
	}
	if _, err := authority.verifyActorDelegation(grants[0].Token, ProtocolV2ActorDelegationRequest{
		ActorUserID: request.ActorUserID, Runtime: request.Runtime, CommandID: grants[0].CommandID,
		CommandVersion: grants[0].CommandVersion, IdempotencyKey: grants[0].IdempotencyKey,
	}); err != nil {
		t.Fatalf("verify issued grant: %v", err)
	}

	request.PermissionKeys = []string{"*"}
	grants, err = gateway.IssueProtocolV2ActorDelegations(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("super-admin grants = %#v", grants)
	}
	ids := []string{grants[0].CommandID, grants[1].CommandID}
	if !reflect.DeepEqual(ids, []string{"sforum.a.denied", "sforum.z.allowed"}) {
		t.Fatalf("super-admin grants = %#v", grants)
	}
}

func TestProtocolV2ActorDelegationBundleRejectsInvalidInvocationEvenWithoutAllowedScopes(t *testing.T) {
	authority := testProtocolV2ActorDelegationAuthority(t, protocolV2DelegationBundleNow)
	engine, err := newProtocolV2CommandEngineWithActorDelegation(newFakeProtocolV2CommandBackend(), authority)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []ProtocolV2ActorDelegationBundleRequest{
		{ActorUserID: 0, Runtime: testProtocolV2ActorDelegationRequest().Runtime, IdempotencyKey: "request-1"},
		{ActorUserID: 42, Runtime: testProtocolV2ActorDelegationRequest().Runtime, IdempotencyKey: "has whitespace"},
		{ActorUserID: 42, Runtime: nil, IdempotencyKey: "request-1"},
	} {
		if grants, err := engine.issueProtocolV2ActorDelegations(context.Background(), request); len(grants) != 0 ||
			!errors.Is(err, ErrProtocolV2ActorDelegationInvalid) {
			t.Fatalf("invalid bundle = %#v, %v", grants, err)
		}
	}
}

func protocolV2DelegationBundleNow() time.Time {
	return time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
}

var _ ProtocolV2ActorDelegationBundleIssuer = (*Gateway)(nil)
