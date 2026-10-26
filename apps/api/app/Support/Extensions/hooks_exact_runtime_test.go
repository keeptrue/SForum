package extensionsruntime

import (
	"sync"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestHookBusStaleRuntimeCannotRemoveReplacementConcurrently(t *testing.T) {
	bus := NewHookBus(HookBusConfig{})
	extension := extensions.Extension{
		ID: "demo.hooks", Version: "2.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, PackageDigest: "digest-2",
		Manifest: extensions.Manifest{
			ID: "demo.hooks", Version: "2.0.0", Type: extensions.TypePlugin,
			Events: []extensions.ManifestEvent{{Name: "demo.changed"}},
		},
	}
	bus.RegisterRuntime(extension, "runtime-2")
	done := make(chan struct{})
	go func() {
		var group sync.WaitGroup
		for index := 0; index < 64; index++ {
			group.Add(2)
			go func() {
				defer group.Done()
				bus.UnregisterRuntime(extension.ID, "runtime-1")
			}()
			go func() {
				defer group.Done()
				_ = bus.Listeners("demo.changed")
			}()
		}
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HookBus exact runtime lock ordering deadlocked")
	}
	snapshot, ok := bus.RuntimeSnapshot(extension.ID)
	if !ok || snapshot.InstanceID != "runtime-2" || len(bus.Listeners("demo.changed")) != 1 {
		t.Fatalf("replacement hook snapshot = %#v, %t", snapshot, ok)
	}
}
