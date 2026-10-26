package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestComponentCompositionConcurrentPublicationNeverReleasesMixedArtifact(t *testing.T) {
	id := "composition.race"
	declaration := componentTestContribution(
		id, "replace", extensionmanifest.ComponentActionReplace, 10,
		componentTestCoreTarget, componentTestCoreContract,
	)
	base := componentTestExtension(t, id, extensions.TypePlugin, declaration)
	registry := NewComponentRegistry()
	if err := registry.ReplaceRuntime(base, "runtime-0"); err != nil {
		t.Fatal(err)
	}
	renderer := ComponentSSRRendererFunc(func(_ context.Context, call ComponentRenderCall) (ComponentRenderResponse, error) {
		html := call.Artifact.ExtensionVersion + ":" + call.Artifact.RuntimeInstanceID
		return ComponentRenderResponse{
			Artifact: call.Artifact, Document: map[string]any{"html": html},
			Fragments: []ComponentRenderFragment{{ReviewedHTML: "<main>" + html + "</main>", PrimaryContent: true}},
		}, nil
	})
	executor := componentCompositionTestExecutor(t, registry, renderer, nil, nil)

	const readers = 8
	const reads = 100
	errorsCh := make(chan error, readers+1)
	var wait sync.WaitGroup
	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range reads {
				result, err := executor.Compose(context.Background(), ComponentCompositionRequest{
					TargetID: componentTestCoreTarget, TargetContractVersion: componentTestCoreContract,
					Props: map[string]any{"scope": "home"}, Binding: componentCompositionTestBinding(),
				})
				if errors.Is(err, ErrComponentCompositionStale) {
					continue
				}
				if err != nil {
					errorsCh <- err
					return
				}
				if len(result.Segments) != 1 || result.Segments[0].Artifact == nil {
					errorsCh <- fmt.Errorf("missing exact segment artifact: %#v", result)
					return
				}
				artifact := result.Segments[0].Artifact
				want := artifact.ExtensionVersion + ":" + artifact.RuntimeInstanceID
				if result.Result["html"] != want || result.Segments[0].HTML != "<main>"+want+"</main>" {
					errorsCh <- fmt.Errorf("mixed artifact release: %#v", result)
					return
				}
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for version := 1; version <= 40; version++ {
			next := base
			next.Version = fmt.Sprintf("1.0.%d", version)
			next.Manifest.Version = next.Version
			next.PackageDigest = fmt.Sprintf("%064x", version+100)
			if err := registry.ReplaceRuntime(next, fmt.Sprintf("runtime-%d", version)); err != nil {
				errorsCh <- err
				return
			}
		}
	}()
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if len(executor.InspectorTraces()) == 0 {
		t.Fatal("concurrent execution produced no Inspector traces")
	}
}
