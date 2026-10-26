package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensionopenapi "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionOpenAPI"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestLifecycleRoutePolicyReconcilePreservesExactBindingWithConcurrentUnrelatedCAS(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	registry := routes.NewRegistry()
	schemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: registry, RouteSchemas: schemas,
	})
	source := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "b", 911, "/policy-cas-source", "runtime-policy-cas-source",
	)
	target := lifecycleRoutePolicyMaterial(
		t, "2.0.0", "c", 912, "/policy-cas-target", "runtime-policy-cas-target",
	)
	publishLifecycleRoutePolicyMaterial(t, schemas, registry, source)
	if _, err := schemas.Publish([]extensionopenapi.Artifact{target.routeSchema}); err != nil {
		t.Fatal(err)
	}

	const readers = 64
	var workers sync.WaitGroup
	var readersReady sync.WaitGroup
	readersReady.Add(readers)
	errorsSeen := make(chan error, readers+2)
	targetObserved := make(chan struct{})
	var targetOnce sync.Once
	var sourceReads atomic.Int64
	var targetReads atomic.Int64
	var firstTargetRevision atomic.Uint64
	for range readers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			first := true
			for {
				snapshot := registry.PublicationSnapshot()
				runtimeID, err := validateLifecycleRoutePolicyCASSnapshot(
					snapshot, source.binding.RuntimeInstanceID, target.binding.RuntimeInstanceID,
				)
				if first {
					readersReady.Done()
					first = false
				}
				if err != nil {
					select {
					case errorsSeen <- err:
					default:
					}
					return
				}
				switch runtimeID {
				case source.binding.RuntimeInstanceID:
					if targetRevision := firstTargetRevision.Load(); targetRevision != 0 && snapshot.Revision >= targetRevision {
						select {
						case errorsSeen <- fmt.Errorf(
							"revision %d restored stale source runtime after target revision %d",
							snapshot.Revision, targetRevision,
						):
						default:
						}
						return
					}
					sourceReads.Add(1)
				case target.binding.RuntimeInstanceID:
					firstTargetRevision.CompareAndSwap(0, snapshot.Revision)
					targetReads.Add(1)
					targetOnce.Do(func() { close(targetObserved) })
				}
				select {
				case <-ctx.Done():
					return
				default:
					runtime.Gosched()
				}
			}
		}()
	}
	readersReady.Wait()
	select {
	case err := <-errorsSeen:
		cancel()
		workers.Wait()
		t.Fatal(err)
	default:
	}

	writerStarted := make(chan struct{})
	writerContinue := make(chan struct{})
	var writerPublications atomic.Int64
	workers.Add(1)
	go func() {
		defer workers.Done()
		for iteration := 0; iteration < 8; iteration++ {
			if err := publishLifecycleRoutePolicyCASCore(ctx, registry, iteration); err != nil {
				if !errors.Is(err, context.Canceled) {
					select {
					case errorsSeen <- err:
					default:
					}
				}
				return
			}
			if writerPublications.Add(1) == 1 {
				close(writerStarted)
				select {
				case <-writerContinue:
				case <-ctx.Done():
					return
				}
			}
			runtime.Gosched()
		}
	}()
	<-writerStarted

	reconcileStarted := make(chan struct{})
	reconcileResult := make(chan error, 1)
	go func() {
		close(reconcileStarted)
		reconcileResult <- boundary.reconcileRoutes(
			ctx, target.extension.ID, &source, &target, &target,
		)
	}()
	<-reconcileStarted
	close(writerContinue)
	if err := <-reconcileResult; err != nil {
		cancel()
		workers.Wait()
		t.Fatalf("reconcile target route policies under unrelated CAS: %v", err)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-targetObserved:
	case err := <-errorsSeen:
		cancel()
		workers.Wait()
		t.Fatal(err)
	case <-timer.C:
		cancel()
		workers.Wait()
		t.Fatal("readers never observed the target route policy publication")
	}
	for writerPublications.Load() < 2 {
		select {
		case err := <-errorsSeen:
			cancel()
			workers.Wait()
			t.Fatal(err)
		case <-timer.C:
			cancel()
			workers.Wait()
			t.Fatal("unrelated writer did not publish after reconciliation started")
		default:
			runtime.Gosched()
		}
	}

	cancel()
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
	if sourceReads.Load() == 0 || targetReads.Load() == 0 {
		t.Fatalf("readers missed a lifecycle revision: source=%d target=%d", sourceReads.Load(), targetReads.Load())
	}
	assertLifecycleRoutePolicyPublication(
		t, registry, target.extension, target.binding.RuntimeInstanceID, "GET", "/policy-cas-target/api",
		lifecycleDisabledRoutePolicy,
	)
	publication := registry.PublicationSnapshot().Publication
	expectedCore := lifecycleRoutePolicyCASCoreRoute(int(writerPublications.Load() - 1))
	if len(publication.Core) != 1 || publication.Core[0].ID != expectedCore.ID ||
		publication.Core[0].ContractVersion != expectedCore.ContractVersion ||
		publication.Core[0].Method != expectedCore.Method || publication.Core[0].Path != expectedCore.Path {
		t.Fatalf("latest unrelated Core publication was lost: got=%#v want=%#v", publication.Core, expectedCore)
	}
}

func TestLifecycleRoutePolicyReconcileRetriesConflictAndRebindsLiveSchema(t *testing.T) {
	ctx := t.Context()
	registry := routes.NewRegistry()
	schemas := lifecycleRouteSchemaPublication(t)
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{
		Routes: registry, RouteSchemas: schemas,
	})
	source := lifecycleRoutePolicyMaterial(
		t, "1.0.0", "d", 921, "/policy-rebind-source", "runtime-policy-rebind-source",
	)
	targetExtension := lifecycleRegistryRequiredRoutePolicy(t, lifecycleRegistryTestExtension(
		t, "2.0.0", strings.Repeat("e", 64), 922, "/policy-rebind-target",
	))
	target, err := buildLifecycleRegistryMaterial(
		targetExtension, lifecycleRegistryBinding(targetExtension, "runtime-policy-rebind-target"),
	)
	if err != nil {
		t.Fatal(err)
	}
	publishLifecycleRoutePolicyMaterial(t, schemas, registry, source)

	writer := &conflictOnceLifecycleRoutePublicationCAS{registry: registry}
	writer.beforeFirst = func() error {
		if _, err := schemas.Publish([]extensionopenapi.Artifact{target.routeSchema}); err != nil {
			return err
		}
		snapshot := registry.PublicationSnapshot()
		publication := snapshot.Publication
		publication.Core = []routes.CoreRoute{lifecycleRoutePolicyCASCoreRoute(1)}
		_, err := registry.PublishIfRevision(publication, snapshot.Revision)
		return err
	}
	boundary.routePublisher = writer
	if err := boundary.reconcileRoutes(ctx, target.extension.ID, &source, &target, &target); err != nil {
		t.Fatal(err)
	}
	// First CAS attempt runs before target OpenAPI is published (beforeFirst),
	// so BindRouteExecutionPolicies synthesizes bare disabled (0 size / empty
	// CORS = platform default). Second attempt resolves required policy with
	// Host request-size + CORS defaults from the published operation.
	synthesizedDisabled := routes.RouteExecutionPolicy{
		RateLimit: "disabled", Idempotency: "disabled",
	}
	if writer.calls != 2 || len(writer.policies) != 2 ||
		writer.policies[0] != synthesizedDisabled ||
		writer.policies[1] != lifecycleRequiredRoutePolicy {
		t.Fatalf("route CAS attempts=%d policies=%#v", writer.calls, writer.policies)
	}
	assertLifecycleRoutePolicyPublication(
		t, registry, target.extension, target.binding.RuntimeInstanceID, "POST", "/policy-rebind-target/api",
		lifecycleRequiredRoutePolicy,
	)
	publication := registry.PublicationSnapshot().Publication
	expectedCore := lifecycleRoutePolicyCASCoreRoute(1)
	if len(publication.Core) != 1 || publication.Core[0].ID != expectedCore.ID ||
		publication.Core[0].ContractVersion != expectedCore.ContractVersion || publication.Core[0].Path != expectedCore.Path {
		t.Fatalf("concurrent Core publication = %#v, want %#v", publication.Core, expectedCore)
	}
}

type conflictOnceLifecycleRoutePublicationCAS struct {
	registry    *routes.Registry
	beforeFirst func() error
	calls       int
	policies    []routes.RouteExecutionPolicy
}

func (c *conflictOnceLifecycleRoutePublicationCAS) PublicationSnapshot() routes.PublicationSnapshot {
	return c.registry.PublicationSnapshot()
}

func (c *conflictOnceLifecycleRoutePublicationCAS) PublishIfRevision(
	publication routes.Publication,
	expectedRevision uint64,
) (routes.Snapshot, error) {
	c.calls++
	if len(publication.Policies) != 1 {
		return routes.Snapshot{}, fmt.Errorf("route CAS candidate policies = %#v", publication.Policies)
	}
	c.policies = append(c.policies, publication.Policies[0].Policy)
	if c.calls == 1 && c.beforeFirst != nil {
		if err := c.beforeFirst(); err != nil {
			return routes.Snapshot{}, err
		}
	}
	return c.registry.PublishIfRevision(publication, expectedRevision)
}

func publishLifecycleRoutePolicyCASCore(ctx context.Context, registry *routes.Registry, iteration int) error {
	for attempts := 0; attempts < 256; attempts++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot := registry.PublicationSnapshot()
		publication := snapshot.Publication
		publication.Core = []routes.CoreRoute{lifecycleRoutePolicyCASCoreRoute(iteration)}
		if _, err := registry.PublishIfRevision(publication, snapshot.Revision); err == nil {
			return nil
		} else if !errors.Is(err, routes.ErrRevisionConflict) {
			return err
		}
		runtime.Gosched()
	}
	return routes.ErrRevisionConflict
}

func lifecycleRoutePolicyCASCoreRoute(iteration int) routes.CoreRoute {
	suffix := "a"
	if iteration%2 != 0 {
		suffix = "b"
	}
	id := "core.route.lifecycle_policy_cas." + suffix
	contract := "sforum.route.lifecycle_policy_cas." + suffix + "@1"
	return routes.CoreRoute{
		ID: id, ContractVersion: contract, Method: "GET", Path: "/lifecycle-policy-cas-" + suffix,
		Guard: routes.CoreGuardDescriptor{
			RouteID: id, ContractVersion: contract, Method: "GET", Kind: routes.CoreGuardPublic,
		},
	}
}

func validateLifecycleRoutePolicyCASSnapshot(
	snapshot routes.PublicationSnapshot,
	sourceRuntimeID string,
	targetRuntimeID string,
) (string, error) {
	publication := snapshot.Publication
	if publication.SafeMode || len(publication.Plugins) != 1 || len(publication.Policies) != 1 {
		return "", fmt.Errorf("revision %d has partial route policy publication: %#v", snapshot.Revision, publication)
	}
	plugin := publication.Plugins[0]
	binding := publication.Policies[0]
	if len(plugin.Routes) != 1 || binding.Artifact != plugin.Artifact {
		return "", fmt.Errorf("revision %d artifact mismatch: plugin=%#v binding=%#v", snapshot.Revision, plugin, binding)
	}
	route := plugin.Routes[0]
	if binding.RouteID != route.ID || binding.ContractVersion != route.ContractVersion || binding.Method != "GET" ||
		binding.Policy != lifecycleDisabledRoutePolicy {
		return "", fmt.Errorf("revision %d route policy mismatch: route=%#v binding=%#v", snapshot.Revision, route, binding)
	}
	runtimeID := plugin.Artifact.RuntimeInstanceID
	if runtimeID != sourceRuntimeID && runtimeID != targetRuntimeID {
		return "", fmt.Errorf("revision %d exposed unknown runtime %q", snapshot.Revision, runtimeID)
	}
	return runtimeID, nil
}
