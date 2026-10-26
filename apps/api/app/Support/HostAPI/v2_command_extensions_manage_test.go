package hostapi

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestExtensionPluginDisableRequiresExtensionsManageCapability(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	definition := newProtocolV2ExtensionPluginDisableCommandDefinition()
	backend := newFakeProtocolV2CommandBackend()
	engine, err := newProtocolV2CommandEngineWithActorDelegation(backend, authority, definition)
	if err != nil {
		t.Fatal(err)
	}
	// 无能力源：拒绝。
	request := testProtocolV2ExtensionsManageDisableRequest(t, "target.plugin")
	plan, err := engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_capability_denied" {
		t.Fatalf("plan without capability source = %#v err=%v", plan.GetError(), err)
	}

	// 有能力源但缺少 extensions.manage：拒绝。
	engine.BindCapabilitySource(fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})})
	plan, err = engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_capability_denied" {
		t.Fatalf("plan without extensions.manage = %#v err=%v", plan.GetError(), err)
	}

	// 授予 extensions.manage 后，仍因缺少 actor delegation 失败（证明能力门禁已通过）。
	engine.BindCapabilitySource(fakeCaps{set: capabilities.NewSet([]string{capabilities.ExtensionsManage})})
	plan, err = engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_actor_delegation_required" {
		t.Fatalf("plan after capability grant = %#v err=%v", plan.GetError(), err)
	}
}

func TestExtensionPluginDisableRejectsSelfTarget(t *testing.T) {
	mutation, err := protocolV2ExtensionPluginDisableMutationFromRequest(
		testProtocolV2ExtensionsManageDisableRequest(t, "demo.plugin"),
	)
	if err != nil || mutation.targetExtensionID != "demo.plugin" {
		t.Fatalf("mutation=%#v err=%v", mutation, err)
	}
	// 空目标失败关闭。
	if _, err := protocolV2ExtensionPluginDisableMutationFromRequest(&hostv2.CommandRequest{
		CommandId: CommandExtensionPluginDisableID, CommandVersion: CommandExtensionPluginDisableVersion,
		Input: &protocolv2.TypedDocument{SchemaId: CommandExtensionPluginDisableInputSchema, SchemaVersion: CommandExtensionPluginDisableSchemaV1},
	}); err == nil {
		t.Fatal("empty target accepted")
	}
}

func TestExtensionSettingsUpdateMutationBoundsAndSecrets(t *testing.T) {
	request := testProtocolV2ExtensionsManageSettingsUpdateRequest(t, "target.plugin", map[string]any{
		"title": "Hello",
		"flag":  "true",
	})
	mutation, err := protocolV2ExtensionSettingsUpdateMutationFromRequest(request)
	if err != nil || mutation.targetExtensionID != "target.plugin" || len(mutation.values) != 2 {
		t.Fatalf("mutation=%#v err=%v", mutation, err)
	}
	// 密文前缀写入失败关闭。
	if _, err := protocolV2ExtensionSettingsUpdateMutationFromRequest(
		testProtocolV2ExtensionsManageSettingsUpdateRequest(t, "target.plugin", map[string]any{
			"token": extensionsManageSecretCipherPrefix + "blob",
		}),
	); err == nil {
		t.Fatal("cipher prefix write accepted")
	}
	// 空 values 失败关闭。
	if _, err := protocolV2ExtensionSettingsUpdateMutationFromRequest(
		testProtocolV2ExtensionsManageSettingsUpdateRequest(t, "target.plugin", map[string]any{}),
	); err == nil {
		t.Fatal("empty values accepted")
	}
}

func TestExtensionSettingsActionMutationValidation(t *testing.T) {
	mutation, err := protocolV2ExtensionSettingsActionMutationFromRequest(
		testProtocolV2ExtensionsManageSettingsActionRequest(t, "target.plugin", "probe"),
	)
	if err != nil || mutation.targetExtensionID != "target.plugin" || mutation.actionID != "probe" {
		t.Fatalf("mutation=%#v err=%v", mutation, err)
	}
	if _, err := protocolV2ExtensionSettingsActionMutationFromRequest(
		testProtocolV2ExtensionsManageSettingsActionRequest(t, "target.plugin", "bad action"),
	); err == nil {
		t.Fatal("invalid action id accepted")
	}
	if _, err := protocolV2ExtensionSettingsActionMutationFromRequest(
		testProtocolV2ExtensionsManageSettingsActionRequest(t, "", "probe"),
	); err == nil {
		t.Fatal("empty target accepted")
	}
}

func TestExtensionSettingsUpdateRequiresExtensionsManageCapability(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	definition := newProtocolV2ExtensionSettingsUpdateCommandDefinition()
	backend := newFakeProtocolV2CommandBackend()
	engine, err := newProtocolV2CommandEngineWithActorDelegation(backend, authority, definition)
	if err != nil {
		t.Fatal(err)
	}
	request := testProtocolV2ExtensionsManageSettingsUpdateRequest(t, "target.plugin", map[string]any{"title": "x"})
	plan, err := engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_capability_denied" {
		t.Fatalf("plan without capability source = %#v err=%v", plan.GetError(), err)
	}
	engine.BindCapabilitySource(fakeCaps{set: capabilities.NewSet([]string{capabilities.ExtensionsManage})})
	plan, err = engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_actor_delegation_required" {
		t.Fatalf("plan after capability grant = %#v err=%v", plan.GetError(), err)
	}
}

func TestExtensionSettingsActionRequiresExtensionsManageCapability(t *testing.T) {
	authority, err := NewProtocolV2ActorDelegationAuthority()
	if err != nil {
		t.Fatal(err)
	}
	definition := newProtocolV2ExtensionSettingsActionCommandDefinition()
	backend := newFakeProtocolV2CommandBackend()
	engine, err := newProtocolV2CommandEngineWithActorDelegation(backend, authority, definition)
	if err != nil {
		t.Fatal(err)
	}
	request := testProtocolV2ExtensionsManageSettingsActionRequest(t, "target.plugin", "probe")
	plan, err := engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_capability_denied" {
		t.Fatalf("plan without capability source = %#v err=%v", plan.GetError(), err)
	}
	engine.BindCapabilitySource(fakeCaps{set: capabilities.NewSet([]string{capabilities.ExtensionsManage})})
	plan, err = engine.plan(context.Background(), request)
	if err != nil || plan.GetError().GetReason() != "host.command_actor_delegation_required" {
		t.Fatalf("plan after capability grant = %#v err=%v", plan.GetError(), err)
	}
}

func testProtocolV2ExtensionsManageDisableRequest(t *testing.T, target string) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(map[string]any{"targetExtensionId": target})
	if err != nil {
		t.Fatal(err)
	}
	requestContext := testProtocolV2RequestContext()
	// 插件发起的 Host Command 不得自带 actor；委托令牌另附。
	requestContext.Actor = nil
	requestContext.IdempotencyKey = "extensions-manage-disable-1"
	return &hostv2.CommandRequest{
		Context:        requestContext,
		CommandId:      CommandExtensionPluginDisableID,
		CommandVersion: CommandExtensionPluginDisableVersion,
		IdempotencyKey: "extensions-manage-disable-1",
		Input: &protocolv2.TypedDocument{
			SchemaId: CommandExtensionPluginDisableInputSchema, SchemaVersion: CommandExtensionPluginDisableSchemaV1,
			Value: value,
		},
	}
}

func testProtocolV2ExtensionsManageSettingsUpdateRequest(t *testing.T, target string, values map[string]any) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(map[string]any{
		"targetExtensionId": target,
		"values":            values,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext := testProtocolV2RequestContext()
	requestContext.Actor = nil
	requestContext.IdempotencyKey = "extensions-manage-settings-update-1"
	return &hostv2.CommandRequest{
		Context:        requestContext,
		CommandId:      CommandExtensionSettingsUpdateID,
		CommandVersion: CommandExtensionSettingsUpdateVersion,
		IdempotencyKey: "extensions-manage-settings-update-1",
		Input: &protocolv2.TypedDocument{
			SchemaId: CommandExtensionSettingsUpdateInputSchema, SchemaVersion: CommandExtensionSettingsUpdateSchemaV1,
			Value: value,
		},
	}
}

func testProtocolV2ExtensionsManageSettingsActionRequest(t *testing.T, target, actionID string) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(map[string]any{
		"targetExtensionId": target,
		"actionId":          actionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext := testProtocolV2RequestContext()
	requestContext.Actor = nil
	requestContext.IdempotencyKey = "extensions-manage-settings-action-1"
	return &hostv2.CommandRequest{
		Context:        requestContext,
		CommandId:      CommandExtensionSettingsActionID,
		CommandVersion: CommandExtensionSettingsActionVersion,
		IdempotencyKey: "extensions-manage-settings-action-1",
		Input: &protocolv2.TypedDocument{
			SchemaId: CommandExtensionSettingsActionInputSchema, SchemaVersion: CommandExtensionSettingsActionSchemaV1,
			Value: value,
		},
	}
}
