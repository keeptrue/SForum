package extensionsruntime

import (
	"context"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestHookBusInvokesOnlyEnabledDeclaredHooks(t *testing.T) {
	calls := []string{}
	bus := NewHookBus(HookBusConfig{
		Invoker: HookInvokerFunc(func(_ context.Context, extension extensions.Extension, input HookInput) HookResult {
			calls = append(calls, extension.ID+":"+input.Name)
			return HookResult{OK: true}
		}),
	})
	extension := runtimeExtension("demo.plugin")
	extension.Manifest.Hooks = []extensions.ManifestHook{{Name: "extension.enabled"}}
	bus.Register(extension)
	bus.Emit(context.Background(), HookInput{Name: "extension.enabled"})
	bus.Emit(context.Background(), HookInput{Name: "topic.created"})
	if len(calls) != 1 || calls[0] != "demo.plugin:extension.enabled" {
		t.Fatalf("unexpected hook calls: %#v", calls)
	}
}
