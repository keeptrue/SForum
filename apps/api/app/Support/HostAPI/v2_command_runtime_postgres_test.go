package hostapi

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	extensionreference "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/ExtensionReference"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestPostgresProtocolV2CommandRuntimeRequiresCompleteDependencies(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	config := PostgresProtocolV2CommandRuntimeConfig{
		Pool: new(pgxpool.Pool), ActorDelegations: authority,
		Jobs: &supportjobs.Dispatcher{}, Moderation: moderation.NewPostgresStore(new(pgxpool.Pool)),
		AttachmentStatuses:   protocolV2AttachmentMutatorStub{},
		AttachmentExtensions: protocolV2AttachmentExtensionStub{},
	}
	fields := []func(*PostgresProtocolV2CommandRuntimeConfig){
		func(value *PostgresProtocolV2CommandRuntimeConfig) { value.Pool = nil },
		func(value *PostgresProtocolV2CommandRuntimeConfig) { value.ActorDelegations = nil },
		func(value *PostgresProtocolV2CommandRuntimeConfig) { value.Jobs = nil },
		func(value *PostgresProtocolV2CommandRuntimeConfig) { value.Moderation = nil },
		func(value *PostgresProtocolV2CommandRuntimeConfig) { value.AttachmentStatuses = nil },
		func(value *PostgresProtocolV2CommandRuntimeConfig) { value.AttachmentExtensions = nil },
	}
	for index, clear := range fields {
		invalid := config
		clear(&invalid)
		if _, err := NewPostgresProtocolV2CommandRuntime(invalid); err == nil {
			t.Fatalf("missing dependency case %d accepted", index)
		}
	}
}

func TestPostgresProtocolV2CommandRuntimePublishesDomainCommands(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewPostgresProtocolV2CommandRuntime(PostgresProtocolV2CommandRuntimeConfig{
		Pool: new(pgxpool.Pool), ActorDelegations: authority,
		Jobs: &supportjobs.Dispatcher{}, Moderation: moderation.NewPostgresStore(new(pgxpool.Pool)),
		AttachmentStatuses:   protocolV2AttachmentMutatorStub{},
		AttachmentExtensions: protocolV2AttachmentExtensionStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := runtime.commandEngine()
	if engine.queryInvalidationJobs == nil {
		t.Fatal("production command runtime omitted Query invalidation dispatcher")
	}
	if len(engine.definitions) != 16 {
		t.Fatalf("command count = %d", len(engine.definitions))
	}
	for _, command := range []string{
		CommandIdentityUserStatusSetID, CommandTopicVisibilitySetID,
		CommandEntityMetaValuesUpsertID, CommandModerationDecisionSubmitID,
		CommandEntitlementsMutateID, CommandAttachmentStatusSetID,
		CommandExtensionPluginDisableID, CommandExtensionSettingsResetID,
		CommandExtensionSettingsUpdateID, CommandExtensionSettingsActionID,
		CommandNotificationsEmitID, CommandEventsEmitID, CommandAICompleteID,
		CommandAttachmentExtensionClaimID, CommandAttachmentExtensionReleaseID,
		CommandAttachmentExtensionListID,
	} {
		if _, ok := engine.definitions[protocolV2CommandKey{id: command, version: "1"}]; !ok {
			t.Fatalf("missing command %s", command)
		}
	}
	disable := engine.definitions[protocolV2CommandKey{id: CommandExtensionPluginDisableID, version: "1"}]
	if disable.RequiredCapability != "extensions.manage" || disable.ActorMode != protocolV2CommandActorDelegated {
		t.Fatalf("disable command contract = %#v", disable)
	}
	reset := engine.definitions[protocolV2CommandKey{id: CommandExtensionSettingsResetID, version: "1"}]
	if reset.RequiredCapability != "extensions.manage" || reset.ActorMode != protocolV2CommandActorDelegated {
		t.Fatalf("settings reset command contract = %#v", reset)
	}
	update := engine.definitions[protocolV2CommandKey{id: CommandExtensionSettingsUpdateID, version: "1"}]
	if update.RequiredCapability != "extensions.manage" || update.ActorMode != protocolV2CommandActorDelegated {
		t.Fatalf("settings update command contract = %#v", update)
	}
	action := engine.definitions[protocolV2CommandKey{id: CommandExtensionSettingsActionID, version: "1"}]
	if action.RequiredCapability != "extensions.manage" || action.ActorMode != protocolV2CommandActorDelegated {
		t.Fatalf("settings action command contract = %#v", action)
	}
	gateway := NewGateway(New(Config{}))
	if gateway.ProtocolV2ActorDelegationIssuer() != nil {
		t.Fatal("issuer must be unavailable before command binding")
	}
	if err := gateway.BindProtocolV2CommandRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if gateway.ProtocolV2ActorDelegationIssuer() != authority {
		t.Fatal("gateway did not expose the exact boot-scoped issuer")
	}
}

type protocolV2AttachmentMutatorStub struct{}

func (protocolV2AttachmentMutatorStub) MutateProtocolV2AttachmentStatus(context.Context, pgx.Tx, int64, string) (ProtocolV2AttachmentStatusResult, error) {
	return ProtocolV2AttachmentStatusResult{}, nil
}

type protocolV2AttachmentExtensionStub struct{}

func (protocolV2AttachmentExtensionStub) ExtensionAttachmentPolicy(context.Context) (extensionreference.Policy, error) {
	return extensionreference.Policy{}, nil
}

func (protocolV2AttachmentExtensionStub) PrepareClaim(context.Context, pgx.Tx, extensionreference.ClaimInput) (*extensionreference.ClaimPlan, error) {
	return nil, errors.New("stub")
}

func (protocolV2AttachmentExtensionStub) ApplyClaim(context.Context, pgx.Tx, *extensionreference.ClaimPlan) (extensionreference.Claim, error) {
	return extensionreference.Claim{}, errors.New("stub")
}

func (protocolV2AttachmentExtensionStub) Count(context.Context, pgx.Tx, extensionreference.ReleaseInput) (int, error) {
	return 0, errors.New("stub")
}

func (protocolV2AttachmentExtensionStub) Release(context.Context, pgx.Tx, extensionreference.ReleaseInput) (extensionreference.Release, error) {
	return extensionreference.Release{}, errors.New("stub")
}

func (protocolV2AttachmentExtensionStub) List(context.Context, pgx.Tx, extensionreference.ListInput) (extensionreference.List, error) {
	return extensionreference.List{}, errors.New("stub")
}
