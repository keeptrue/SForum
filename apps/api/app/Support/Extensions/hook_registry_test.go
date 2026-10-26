package extensionsruntime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

func TestVersionedHookRegistryPublishesPluginDefinitionAndDeterministicConsumers(t *testing.T) {
	registry := NewVersionedHookRegistry()
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(50))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); err != nil {
		t.Fatal(err)
	}
	contract, listeners, err := registry.Resolve("hooks.provider.transform", "hooks.provider.transform@1")
	if err != nil {
		t.Fatal(err)
	}
	if contract.Name != "hooks.provider.content.transform" || contract.InputSchema != "hooks.provider.content@1" ||
		contract.ResultSchema != "hooks.provider.content.result@1" {
		t.Fatalf("contract = %#v", contract)
	}
	if got := []string{listeners[0].ID, listeners[1].ID}; !reflect.DeepEqual(got, []string{"hooks.consumer.transform", "hooks.provider.transform"}) {
		t.Fatalf("listener order = %#v", got)
	}

	snapshot := registry.Snapshot()
	provider.Manifest.Hooks[0].MutableFields[0] = "forged"
	snapshot.Contracts[0].MutableFields[0] = "also-forged"
	again, _, _ := registry.Resolve(contract.ID, contract.ContractVersion)
	if !reflect.DeepEqual(again.MutableFields, []string{"title"}) {
		t.Fatalf("immutable snapshot followed caller mutation: %#v", again.MutableFields)
	}
}

func TestVersionedHookRegistryRejectsUndeclaredDependencyAndContractDrift(t *testing.T) {
	registry := NewVersionedHookRegistry()
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(20))
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); !errors.Is(err, ErrHookRegistryDependency) {
		t.Fatalf("undeclared dependency = %v", err)
	}
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	consumer.Manifest.Hooks[0].InputSchema = "hooks.provider.content@2"
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); !errors.Is(err, ErrHookRegistryConflict) {
		t.Fatalf("contract drift = %v", err)
	}
}

func TestVersionedHookRegistryEnforcesProviderVersionConstraint(t *testing.T) {
	registry := NewVersionedHookRegistry()
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}
	required := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(20))
	required.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^2.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(required, "required-runtime"); !errors.Is(err, ErrHookRegistryDependency) {
		t.Fatalf("required version mismatch = %v", err)
	}

	optional := required
	optional.ID, optional.Manifest.ID = "hooks.optional", "hooks.optional"
	optional.Manifest.Hooks[0].ID = "hooks.optional.transform"
	optional.Manifest.Dependencies[0].Kind = "optional"
	if err := registry.ReplaceRuntime(optional, "optional-runtime"); err != nil {
		t.Fatalf("optional mismatch should fall back: %v", err)
	}
	_, listeners, err := registry.Resolve(provider.Manifest.Hooks[0].ID, provider.Manifest.Hooks[0].ContractVersion)
	if err != nil || len(listeners) != 1 || listeners[0].Artifact.ExtensionID != provider.ID {
		t.Fatalf("optional incompatible listener was published: %#v, %v", listeners, err)
	}
}

func TestVersionedHookRegistryOptionalConsumerFallsBackAcrossProviderDisable(t *testing.T) {
	registry := NewVersionedHookRegistry()
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(20))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "optional"}}
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); err != nil {
		t.Fatal(err)
	}
	if removed, err := registry.RemoveRuntime(provider.ID, "provider-runtime"); err != nil || !removed {
		t.Fatalf("optional provider disable = %t, %v", removed, err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Listeners) != 0 {
		t.Fatalf("optional fallback snapshot = %#v", snapshot)
	}
	if err := registry.ReplaceRuntime(provider, "provider-runtime-2"); err != nil {
		t.Fatalf("provider republish: %v", err)
	}
	_, listeners, err := registry.Resolve(provider.Manifest.Hooks[0].ID, provider.Manifest.Hooks[0].ContractVersion)
	if err != nil || len(listeners) != 2 || listeners[0].Artifact.ExtensionID != consumer.ID {
		t.Fatalf("optional listener did not converge after republish: %#v, %v", listeners, err)
	}
}

func TestVersionedHookRegistryRequiredConsumerBlocksProviderDisable(t *testing.T) {
	registry := NewVersionedHookRegistry()
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(20))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); err != nil {
		t.Fatal(err)
	}
	if removed, err := registry.RemoveRuntime(provider.ID, "provider-runtime"); removed || !errors.Is(err, ErrHookRegistryConflict) {
		t.Fatalf("required provider disable = %t, %v", removed, err)
	}
}

func TestVersionedHookRegistrySupportsPassiveDefinition(t *testing.T) {
	registry := NewVersionedHookRegistry()
	definition := versionedHookDefinition()
	definition.Handler = ""
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), definition)
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(20))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := registry.ReplaceRuntime(provider, "provider-runtime"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(consumer, "consumer-runtime"); err != nil {
		t.Fatal(err)
	}
	_, listeners, err := registry.Resolve(definition.ID, definition.ContractVersion)
	if err != nil || len(listeners) != 1 || listeners[0].ID != consumer.Manifest.Hooks[0].ID {
		t.Fatalf("passive definition listeners = %#v, %v", listeners, err)
	}
}

func TestVersionedHookRegistryRejectsAsyncFailClosed(t *testing.T) {
	registry := NewVersionedHookRegistry()
	hook := versionedHookDefinition()
	hook.Kind = "action"
	hook.MutableFields = nil
	hook.Execution = "async"
	hook.FailurePolicy = "fail_closed"
	extension := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), hook)
	if err := registry.ReplaceRuntime(extension, "provider-runtime"); !errors.Is(err, ErrHookRegistryInvalid) {
		t.Fatalf("async fail_closed registry publication = %v", err)
	}
}

func TestVersionedHookRegistryRejectsTimeoutAboveHostDeadline(t *testing.T) {
	registry := NewVersionedHookRegistry()
	hook := versionedHookDefinition()
	hook.TimeoutMS = extensionmanifest.HookMaximumTimeoutMS + 1
	extension := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), hook)
	if err := registry.ReplaceRuntime(extension, "provider-runtime"); !errors.Is(err, ErrHookRegistryInvalid) {
		t.Fatalf("oversized hook timeout publication = %v", err)
	}
}

func TestManagerStartRollsBackRuntimeWhenHookPublicationFails(t *testing.T) {
	starter := newManagerStagedStarter()
	manager := NewManager(ManagerConfig{Starter: starter})
	hook := versionedHookDefinition()
	hook.TimeoutMS = extensionmanifest.HookMaximumTimeoutMS + 1
	extension := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), hook)
	if err := manager.Start(context.Background(), extension); !errors.Is(err, ErrHookRegistryInvalid) {
		t.Fatalf("manager start publication error = %v", err)
	}
	if _, err := manager.ActiveRuntimeInstance(extension.ID); !errors.Is(err, ErrRuntimeInstanceNotFound) {
		t.Fatalf("failed hook runtime remained active: %v", err)
	}
	if snapshot := manager.HookBus().VersionedRegistry().Snapshot(); len(snapshot.Contracts) != 0 {
		t.Fatalf("failed hook publication remained visible: %#v", snapshot)
	}
}

func TestManagerStopDoesNotStopRequiredHookProvider(t *testing.T) {
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(20))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background(), provider); !errors.Is(err, ErrHookRegistryConflict) {
		t.Fatalf("required provider stop = %v", err)
	}
	if _, err := manager.ActiveRuntimeInstance(provider.ID); err != nil {
		t.Fatalf("required provider runtime stopped despite publication rejection: %v", err)
	}
}

func TestManagerRuntimeStatusSeparatesHooksFromEvents(t *testing.T) {
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter()})
	extension := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	extension.Manifest.Events = []extensions.ManifestEvent{{
		ID: "hooks.provider.event", ContractVersion: "hooks.provider.event@1",
		Name: appevents.TopicCreated, Kind: appevents.KindObserve, Handler: "event.created",
		InputSchema: "hooks.provider.event.input@1", ResultSchema: "hooks.provider.event.result@1",
	}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(context.Background(), extension)
	if status.HookCount != 1 || status.EventCount != 1 {
		t.Fatalf("runtime counts = %#v", status)
	}
}

func TestVersionedHookCompositionRevalidatesEveryPatchAndHonorsPriority(t *testing.T) {
	calls := []string{}
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, input HookInput) HookResult {
		calls = append(calls, input.DeclarationID)
		switch input.DeclarationID {
		case "hooks.consumer.transform":
			return HookResult{OK: true, Patch: map[string]any{"title": "consumer"}}
		default:
			return HookResult{OK: true, Patch: map[string]any{"title": "provider"}}
		}
	})})
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter(), HookBus: bus})
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(50))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	revalidations := []string{}
	result := manager.InvokeVersionedHook(context.Background(), VersionedHookInvocation{
		HookID: provider.Manifest.Hooks[0].ID, ContractVersion: provider.Manifest.Hooks[0].ContractVersion,
		Payload: map[string]any{"title": "original"},
		Revalidate: func(_ context.Context, schema string, value map[string]any) error {
			revalidations = append(revalidations, schema+":"+value["title"].(string))
			return nil
		},
	})
	if !result.OK || result.Payload["title"] != "provider" {
		t.Fatalf("composition = %#v", result)
	}
	if !reflect.DeepEqual(calls, []string{"hooks.consumer.transform", "hooks.provider.transform"}) {
		t.Fatalf("calls = %#v", calls)
	}
	wantValidation := []string{
		"hooks.provider.content@1:original",
		"hooks.provider.content@1:consumer",
		"hooks.provider.content@1:provider",
	}
	if !reflect.DeepEqual(revalidations, wantValidation) {
		t.Fatalf("revalidation = %#v", revalidations)
	}
}

func TestVersionedHookCompositionIsolatesNestedMutationAndRejectsForbiddenPatch(t *testing.T) {
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, input HookInput) HookResult {
		metadata := input.Payload["metadata"].(map[string]any)
		if input.Payload["mode"] == "mutate" {
			metadata["secret"] = "mutated-out-of-band"
			return HookResult{OK: true, Patch: map[string]any{"title": input.DeclarationID}}
		}
		return HookResult{OK: true, Patch: map[string]any{"metadata": map[string]any{"secret": "forbidden"}}}
	})})
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter(), HookBus: bus})
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(50))
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	callerPayload := map[string]any{
		"mode": "mutate", "title": "original", "metadata": map[string]any{"secret": "safe"},
	}
	result := manager.InvokeVersionedHook(context.Background(), VersionedHookInvocation{
		HookID: provider.Manifest.Hooks[0].ID, ContractVersion: provider.Manifest.Hooks[0].ContractVersion,
		Payload: callerPayload,
		Revalidate: func(_ context.Context, _ string, value map[string]any) error {
			if value["metadata"].(map[string]any)["secret"] != "safe" {
				return errors.New("nested forbidden field was mutated")
			}
			return nil
		},
	})
	if !result.OK || result.Payload["metadata"].(map[string]any)["secret"] != "safe" ||
		callerPayload["metadata"].(map[string]any)["secret"] != "safe" {
		t.Fatalf("nested mutation escaped isolation: result=%#v caller=%#v", result, callerPayload)
	}

	callerPayload["mode"] = "forbidden_patch"
	result = manager.InvokeVersionedHook(context.Background(), VersionedHookInvocation{
		HookID: provider.Manifest.Hooks[0].ID, ContractVersion: provider.Manifest.Hooks[0].ContractVersion,
		Payload:    callerPayload,
		Revalidate: func(context.Context, string, map[string]any) error { return nil },
	})
	if result.OK || result.Reason != "extension.hook_registry_rejected" ||
		callerPayload["metadata"].(map[string]any)["secret"] != "safe" {
		t.Fatalf("forbidden nested patch = %#v caller=%#v", result, callerPayload)
	}
}

func TestVersionedHookCompositionFailOpenContinuesWithoutPollution(t *testing.T) {
	calls := []string{}
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, _ extensions.Extension, input HookInput) HookResult {
		calls = append(calls, input.DeclarationID)
		if input.DeclarationID == "hooks.consumer.transform" {
			input.Payload["title"] = "failed-listener-mutation"
			return HookResult{
				OK: false, Reason: "consumer.failed",
				Patch:  map[string]any{"title": "failed-listener-patch"},
				Result: map[string]any{"status": "failed-listener-result"},
			}
		}
		if input.Payload["title"] != "original" {
			return HookResult{OK: false, Reason: "provider.received_polluted_payload"}
		}
		return HookResult{
			OK: true, Patch: map[string]any{"title": "provider"},
			Result: map[string]any{"status": "provider-result"},
		}
	})})
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter(), HookBus: bus})
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	provider.Manifest.Hooks[0].FailurePolicy = appevents.FailurePolicyFailOpen
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(50))
	consumer.Manifest.Hooks[0].FailurePolicy = appevents.FailurePolicyFailOpen
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	callerPayload := map[string]any{"title": "original"}
	revalidations := []string{}
	result := manager.InvokeVersionedHook(context.Background(), VersionedHookInvocation{
		HookID: provider.Manifest.Hooks[0].ID, ContractVersion: provider.Manifest.Hooks[0].ContractVersion,
		Payload: callerPayload,
		Revalidate: func(_ context.Context, schema string, document map[string]any) error {
			switch schema {
			case provider.Manifest.Hooks[0].InputSchema:
				revalidations = append(revalidations, "input:"+document["title"].(string))
			case provider.Manifest.Hooks[0].ResultSchema:
				revalidations = append(revalidations, "result:"+document["status"].(string))
			}
			return nil
		},
	})
	if !result.OK || result.Payload["title"] != "provider" || callerPayload["title"] != "original" ||
		len(result.Results) != 1 || result.Results[0]["status"] != "provider-result" {
		t.Fatalf("fail-open result=%#v caller=%#v", result, callerPayload)
	}
	if !reflect.DeepEqual(calls, []string{"hooks.consumer.transform", "hooks.provider.transform"}) {
		t.Fatalf("calls=%#v", calls)
	}
	wantRevalidations := []string{"input:original", "result:provider-result", "input:provider"}
	if !reflect.DeepEqual(revalidations, wantRevalidations) {
		t.Fatalf("revalidations=%#v", revalidations)
	}
}

func TestVersionedHookCompositionAppliesContractTimeoutFailurePolicy(t *testing.T) {
	for _, test := range []struct {
		name          string
		failurePolicy string
		wantOK        bool
		wantCalls     []string
	}{
		{name: "fail_closed", failurePolicy: appevents.FailurePolicyFailClosed, wantCalls: []string{"hooks.consumer.transform"}},
		{name: "fail_open", failurePolicy: appevents.FailurePolicyFailOpen, wantOK: true, wantCalls: []string{"hooks.consumer.transform", "hooks.provider.transform"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			timeouts := []time.Duration{}
			consumerDeadlinePresent := false
			var consumerDeadlineBudget time.Duration
			var consumerCause error
			bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(ctx context.Context, _ extensions.Extension, input HookInput) HookResult {
				calls = append(calls, input.DeclarationID)
				timeouts = append(timeouts, input.Timeout)
				if input.DeclarationID == "hooks.consumer.transform" {
					deadline, present := ctx.Deadline()
					consumerDeadlinePresent = present
					if present {
						consumerDeadlineBudget = time.Until(deadline)
					}
					<-ctx.Done()
					consumerCause = context.Cause(ctx)
					input.Payload["title"] = "timed-listener-mutation"
					return HookResult{
						OK:     true,
						Patch:  map[string]any{"title": "timed-listener-patch"},
						Result: map[string]any{"status": "timed-listener-result"},
					}
				}
				if input.Payload["title"] != "original" {
					return HookResult{OK: false, Reason: "provider.received_polluted_payload"}
				}
				return HookResult{
					OK:     true,
					Patch:  map[string]any{"title": "provider"},
					Result: map[string]any{"status": "provider-result"},
				}
			})})
			manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter(), HookBus: bus})
			provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
			provider.Manifest.Hooks[0].FailurePolicy = test.failurePolicy
			provider.Manifest.Hooks[0].TimeoutMS = 20
			consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(50))
			consumer.Manifest.Hooks[0].FailurePolicy = test.failurePolicy
			consumer.Manifest.Hooks[0].TimeoutMS = 20
			consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
			if err := manager.Start(context.Background(), provider); err != nil {
				t.Fatal(err)
			}
			if err := manager.Start(context.Background(), consumer); err != nil {
				t.Fatal(err)
			}

			callerPayload := map[string]any{"title": "original"}
			result := manager.InvokeVersionedHook(context.Background(), VersionedHookInvocation{
				HookID: provider.Manifest.Hooks[0].ID, ContractVersion: provider.Manifest.Hooks[0].ContractVersion,
				Payload:    callerPayload,
				Revalidate: func(context.Context, string, map[string]any) error { return nil },
			})
			if !consumerDeadlinePresent || !errors.Is(consumerCause, context.DeadlineExceeded) {
				t.Fatalf("consumer deadline present=%t cause=%v", consumerDeadlinePresent, consumerCause)
			}
			if consumerDeadlineBudget > 250*time.Millisecond {
				t.Fatalf("Manifest timeout was not applied to listener context: budget=%v", consumerDeadlineBudget)
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("calls=%#v", calls)
			}
			for _, timeout := range timeouts {
				if timeout != 20*time.Millisecond {
					t.Fatalf("listener timeout=%v", timeout)
				}
			}
			if callerPayload["title"] != "original" {
				t.Fatalf("caller payload was polluted: %#v", callerPayload)
			}
			if resilience := manager.resilience.snapshot(consumer.ID); resilience.LastFailureReason != "extension.hook_timeout" {
				t.Fatalf("consumer timeout reason=%#v", resilience)
			}
			if test.wantOK {
				if !result.OK || result.Payload["title"] != "provider" || len(result.Results) != 1 ||
					result.Results[0]["status"] != "provider-result" {
					t.Fatalf("fail-open timeout result=%#v", result)
				}
			} else if result.OK || result.Reason != "extension.hook_timeout" ||
				result.Payload["title"] != "original" || len(result.Results) != 0 {
				t.Fatalf("fail-closed timeout result=%#v", result)
			}
		})
	}
}

func TestVersionedHookAsyncExactBinding(t *testing.T) {
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(_ context.Context, extension extensions.Extension, _ HookInput) HookResult {
		if extension.ID == "hooks.consumer" {
			return HookResult{OK: false, Reason: "consumer.failed"}
		}
		return HookResult{OK: true}
	})})
	dispatcher := &recordingEventDispatcher{}
	manager := NewManager(ManagerConfig{Starter: newManagerStagedStarter(), HookBus: bus, Dispatcher: dispatcher})
	provider := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	provider.Manifest.Hooks[0].Kind = "action"
	provider.Manifest.Hooks[0].MutableFields = nil
	provider.Manifest.Hooks[0].Execution = "async"
	provider.Manifest.Hooks[0].FailurePolicy = appevents.FailurePolicyFailOpen
	consumer := versionedHookExtension("hooks.consumer", strings.Repeat("b", 64), versionedHookConsumer(50))
	consumer.Manifest.Hooks[0].Kind = "action"
	consumer.Manifest.Hooks[0].MutableFields = nil
	consumer.Manifest.Hooks[0].Execution = "async"
	consumer.Manifest.Hooks[0].FailurePolicy = appevents.FailurePolicyFailOpen
	consumer.Manifest.Dependencies = []extensions.ManifestDependency{{ID: provider.ID, Version: "^1.0.0", Kind: "required"}}
	if err := manager.Start(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(context.Background(), consumer); err != nil {
		t.Fatal(err)
	}
	result := manager.InvokeVersionedHook(context.Background(), VersionedHookInvocation{
		HookID: provider.Manifest.Hooks[0].ID, ContractVersion: provider.Manifest.Hooks[0].ContractVersion,
		Payload: map[string]any{"title": "queued"},
	})
	if !result.OK || result.Queued != 2 || len(dispatcher.items) != 2 {
		t.Fatalf("async result=%#v jobs=%#v", result, dispatcher.items)
	}
	for _, item := range dispatcher.items {
		if item.HookID != provider.Manifest.Hooks[0].ID || item.ContractVersion != provider.Manifest.Hooks[0].ContractVersion ||
			item.PackageDigest == "" || item.RuntimeInstanceID == "" {
			t.Fatalf("async exact binding = %#v", item)
		}
	}
}

func TestVersionedHookRegistryConcurrentReadersSeeWholeSnapshots(t *testing.T) {
	registry := NewVersionedHookRegistry()
	extension := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	if err := registry.ReplaceRuntime(extension, "runtime-1"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for index := 0; index < 32; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for iteration := 0; iteration < 64; iteration++ {
				snapshot := registry.Snapshot()
				if len(snapshot.Contracts) != 1 || len(snapshot.Listeners) != 1 ||
					snapshot.Contracts[0].Artifact.RuntimeInstanceID != snapshot.Listeners[0].Artifact.RuntimeInstanceID {
					t.Errorf("partial snapshot = %#v", snapshot)
					return
				}
			}
		}()
	}
	group.Wait()
}

func TestVersionedHookRegistryUpgradeRollbackAndDisableAreExact(t *testing.T) {
	registry := NewVersionedHookRegistry()
	source := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	target := versionedHookExtension("hooks.provider", strings.Repeat("b", 64), versionedHookDefinition())
	target.Version, target.Manifest.Version = "2.0.0", "2.0.0"
	if err := registry.ReplaceRuntime(source, "runtime-source"); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceRuntime(target, "runtime-target"); err != nil {
		t.Fatal(err)
	}
	if removed, err := registry.RemoveRuntime(source.ID, "runtime-source"); err != nil || removed {
		t.Fatalf("stale source removed target: %t, %v", removed, err)
	}
	if err := registry.ReplaceRuntime(source, "runtime-source"); err != nil {
		t.Fatal(err)
	}
	contract, _, err := registry.Resolve(source.Manifest.Hooks[0].ID, source.Manifest.Hooks[0].ContractVersion)
	if err != nil || contract.Artifact.RuntimeInstanceID != "runtime-source" || contract.Artifact.PackageDigest != source.PackageDigest {
		t.Fatalf("rollback contract = %#v, %v", contract, err)
	}
	if removed, err := registry.RemoveRuntime(source.ID, "runtime-source"); err != nil || !removed {
		t.Fatalf("disable = %t, %v", removed, err)
	}
	if snapshot := registry.Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Listeners) != 0 {
		t.Fatalf("disabled snapshot = %#v", snapshot)
	}
}

func TestHookBusVersionedToEmptyUpgradeAndRollbackReplaceWholeMembership(t *testing.T) {
	bus := NewHookBus(HookBusConfig{})
	withHooks := versionedHookExtension("hooks.provider", strings.Repeat("a", 64), versionedHookDefinition())
	empty := versionedHookExtension("hooks.provider", strings.Repeat("b", 64), extensions.ManifestHook{})
	empty.Version, empty.Manifest.Version, empty.Manifest.Hooks = "2.0.0", "2.0.0", nil
	if err := bus.RegisterRuntime(withHooks, "runtime-hooks"); err != nil {
		t.Fatal(err)
	}
	previous, ok := bus.RuntimeSnapshot(withHooks.ID)
	if !ok {
		t.Fatal("hook source snapshot missing")
	}
	if err := bus.RegisterRuntime(empty, "runtime-empty"); err != nil {
		t.Fatal(err)
	}
	if snapshot := bus.VersionedRegistry().Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Listeners) != 0 {
		t.Fatalf("versioned-to-empty upgrade retained hooks: %#v", snapshot)
	}
	if err := bus.restoreRuntime(empty.ID, "runtime-empty", previous, true); err != nil {
		t.Fatal(err)
	}
	contract, _, err := bus.VersionedRegistry().Resolve(withHooks.Manifest.Hooks[0].ID, withHooks.Manifest.Hooks[0].ContractVersion)
	if err != nil || contract.Artifact.RuntimeInstanceID != "runtime-hooks" {
		t.Fatalf("rollback from empty = %#v, %v", contract, err)
	}

	if err := bus.RegisterRuntime(empty, "runtime-empty"); err != nil {
		t.Fatal(err)
	}
	emptyPrevious, ok := bus.RuntimeSnapshot(empty.ID)
	if !ok {
		t.Fatal("empty source snapshot missing")
	}
	if err := bus.RegisterRuntime(withHooks, "runtime-hooks"); err != nil {
		t.Fatal(err)
	}
	if err := bus.restoreRuntime(withHooks.ID, "runtime-hooks", emptyPrevious, true); err != nil {
		t.Fatal(err)
	}
	if snapshot := bus.VersionedRegistry().Snapshot(); len(snapshot.Contracts) != 0 || len(snapshot.Listeners) != 0 {
		t.Fatalf("rollback to empty retained target hooks: %#v", snapshot)
	}
}

func versionedHookDefinition() extensions.ManifestHook {
	return extensions.ManifestHook{
		ID: "hooks.provider.transform", ContractVersion: "hooks.provider.transform@1",
		Name: "hooks.provider.content.transform", Kind: "filter", Handler: "hook.transform",
		InputSchema: "hooks.provider.content@1", ResultSchema: "hooks.provider.content.result@1",
		Execution: "sync", FailurePolicy: appevents.FailurePolicyFailClosed, TimeoutMS: 1000,
		MutableFields: []string{"title"}, Priority: 10,
	}
}

func versionedHookConsumer(priority int) extensions.ManifestHook {
	hook := versionedHookDefinition()
	hook.ID = "hooks.consumer.transform"
	hook.TargetID = "hooks.provider.transform"
	hook.Handler = "hook.consume"
	hook.Priority = priority
	return hook
}

func versionedHookExtension(id, digest string, hook extensions.ManifestHook) extensions.Extension {
	return extensions.Extension{
		ID: id, Version: "1.0.0", Type: extensions.TypePlugin, Status: extensions.StatusEnabled,
		PackageDigest: digest,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{Entry: "bin/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2},
			Hooks:   []extensions.ManifestHook{hook},
		},
	}
}

type recordingEventDispatcher struct{ items []EventDeliveryArgs }

func (d *recordingEventDispatcher) Enqueue(_ context.Context, args EventDeliveryArgs, _ supportjobs.EnqueueOptions) error {
	d.items = append(d.items, args)
	return nil
}
