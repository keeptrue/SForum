package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"sync/atomic"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

// Invalid loopback runtime targets must fail before admission or HTTP. The existing
// TestBufferedRouteStepInvokerUsesHostObservedCommitEvidence and
// TestRouteDispatcherMiddlewareExecutesSelectedExactReplacementWithoutCoreWriter
// cover those handler paths; modifiers must fail before runtime admission or HTTP.
func TestRouteDispatcherInvalidTargetModifiersFailBeforeAdmissionAndHTTP(t *testing.T) {
	tests := []struct {
		name          string
		action        string
		wantCoreCalls int32
	}{
		{name: "global", action: extensionmanifest.RouteActionGlobalMiddleware},
		{name: "before", action: extensionmanifest.RouteActionBefore},
		{name: "filter", action: extensionmanifest.RouteActionFilter},
		{name: "wrap", action: extensionmanifest.RouteActionWrap},
		{name: "after", action: extensionmanifest.RouteActionAfter, wantCoreCalls: 1},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := "/api/v1/invalid-target-modifier-" + test.name
			coreID := "core.route.test.invalid_target_modifier_" + test.name
			artifact := routeDispatcherArtifact("invalid.target.modifier."+test.name, byte('a'+index))
			modifier := routeDispatcherManifestRoute(
				artifact.ExtensionID+".route", test.action, path, stdhttp.MethodGet,
			)
			if test.action == extensionmanifest.RouteActionGlobalMiddleware {
				modifier.Path = ""
				modifier.Methods = nil
				modifier.RequestSchema = modifier.ID + ".request@1"
			} else {
				modifier.TargetID = coreID
			}

			registry := routes.NewRegistry()
			if _, err := registry.Publish(routes.Publication{
				Core: []routes.CoreRoute{{
					ID: coreID, ContractVersion: "sforum.route.test.invalid_target_modifier_" + test.name + "@1",
					Method: stdhttp.MethodGet, Path: path,
				}},
				Plugins: []routes.PluginRouteSet{{
					Artifact: artifact, Routes: []extensionmanifest.ManifestRoute{modifier},
				}},
			}); err != nil {
				t.Fatalf("publish legal %s modifier: %v", test.action, err)
			}

			baseRuntime, server := newRouteDispatcherRuntime(t, artifact)
			var serverCalls atomic.Int32
			server.Config.Handler = stdhttp.HandlerFunc(func(writer stdhttp.ResponseWriter, _ *stdhttp.Request) {
				serverCalls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(`{"source":"invalid-target"}`))
			})
			runtime := &invalidTargetModifierFenceRuntime{inner: baseRuntime}
			core := &invalidTargetModifierFenceCore{}
			dispatcher := routes.NewDispatcher(routes.DispatcherConfig{
				Plans: routeRegistryPlanResolver{registry: registry}, Steps: NewBufferedRouteStepInvoker(runtime),
				Guard: HostRouteGuardAuthorizer{}, Schemas: CatalogRouteSchemaValidator{Catalog: acceptRouteSchemaCatalog{}},
			})

			result, err := dispatcher.Dispatch(context.Background(), routes.DispatchRequest{
				Method: stdhttp.MethodGet, Path: path,
				Headers: stdhttp.Header{"Content-Type": {"application/json"}}, Body: []byte(`{}`),
			}, core)
			if !errors.Is(err, routes.ErrDispatchTransport) || !errors.Is(err, ErrRouteRuntimeTarget) || result.Handled {
				t.Fatalf("dispatch result=%#v err=%v", result, err)
			}
			if got := runtime.inspectCalls.Load(); got != 1 {
				t.Fatalf("runtime inspections=%d want=1", got)
			}
			if got := runtime.acquireCalls.Load(); got != 0 {
				t.Fatalf("runtime admissions=%d want=0", got)
			}
			if got := serverCalls.Load(); got != 0 {
				t.Fatalf("loopback HTTP calls=%d want=0", got)
			}
			if got := core.calls.Load(); got != test.wantCoreCalls {
				t.Fatalf("core calls=%d want=%d", got, test.wantCoreCalls)
			}
		})
	}
}

type invalidTargetModifierFenceRuntime struct {
	inner        ExactRouteRuntime
	inspectCalls atomic.Int32
	acquireCalls atomic.Int32
}

func (r *invalidTargetModifierFenceRuntime) InspectRuntimeInstance(
	identity extensionsruntime.RuntimeInstanceIdentity,
) (extensionsruntime.RuntimeInstanceSnapshot, error) {
	r.inspectCalls.Add(1)
	return r.inner.InspectRuntimeInstance(identity)
}

func (r *invalidTargetModifierFenceRuntime) AcquireRuntimeCall(
	ctx context.Context,
	identity extensionsruntime.RuntimeInstanceIdentity,
	class extensionsruntime.RuntimeCallClass,
) (*extensionsruntime.RuntimeAdmissionLease, error) {
	r.acquireCalls.Add(1)
	return r.inner.AcquireRuntimeCall(ctx, identity, class)
}

type invalidTargetModifierFenceCore struct {
	calls atomic.Int32
}

func (c *invalidTargetModifierFenceCore) InvokeCore(
	_ context.Context,
	_ routes.RouteExecutionStep,
	_ routes.DispatchRequest,
) (routes.DispatchResponse, error) {
	c.calls.Add(1)
	return routes.DispatchResponse{
		Status: stdhttp.StatusOK,
		Headers: stdhttp.Header{
			"Content-Type": {"application/json"},
		},
		Body: []byte(`{"source":"core"}`),
	}, nil
}
