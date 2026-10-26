package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	seoregistry "github.com/keeptrue/sforum/apps/api/app/Support/SEORegistry"
)

func TestProtocolV2SEOExecutionUsesExactRuntimeAndFallsBackAfterDisable(t *testing.T) {
	starter := &seoExecutionStarter{}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("plugin.seo.reference", "1.0.0", strings.Repeat("a", 64))
	extension.ActiveVersionID = 17
	declaration := extensions.ManifestSEO{
		ID: extension.ID + ".title", ContractVersion: extension.ID + ".title@1",
		Scope: "core.page.topic", Kind: seoregistry.KindTitle, Action: seoregistry.ActionFilter,
		Handler: extension.ID + ".title", FailurePolicy: seoregistry.FailurePolicyFallback, TimeoutMS: 500,
	}
	extension.Manifest.SEO = []extensions.ManifestSEO{declaration}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	active, err := manager.ActiveRuntimeInstance(extension.ID)
	if err != nil {
		t.Fatal(err)
	}
	publication := seoregistry.Publication{
		Artifact: seoregistry.Artifact{
			ExtensionID: extension.ID, ExtensionVersion: extension.Version,
			PackageDigest: extension.PackageDigest, ImpactDigest: strings.Repeat("b", 64),
			VersionID: extension.ActiveVersionID, RuntimeInstanceID: active.Identity.InstanceID,
		},
		Contributions: []seoregistry.Declaration{{
			ID: declaration.ID, ContractVersion: declaration.ContractVersion, Scope: declaration.Scope,
			Kind: declaration.Kind, Action: declaration.Action, Handler: declaration.Handler,
			FailurePolicy: declaration.FailurePolicy, Timeout: 500 * time.Millisecond,
		}},
	}
	registry := seoregistry.New()
	if _, err := registry.Publish(publication); err != nil {
		t.Fatal(err)
	}
	trace := seoregistry.NewExecutionTraceRing(8)
	runtime, err := seoregistry.NewExecutionRuntime(seoregistry.ExecutionConfig{
		Registry: registry, Resolver: NewProtocolV2SEOProviderResolver(manager),
		Admission:   NewSEOExecutionAdmission(manager),
		FinalPolicy: seoregistry.FinalPolicyFunc(func(context.Context, seoregistry.FinalPolicyRequest) error { return nil }),
		Trace:       trace,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := seoregistry.Document{Title: "Core title"}
	result, err := runtime.Execute(context.Background(), seoregistry.ExecuteRequest{Scope: declaration.Scope, Base: base})
	if err != nil || result.Document.Title != "Plugin title" || len(result.Applied) != 1 || starter.calls.Load() != 1 {
		t.Fatalf("active SEO result=%#v calls=%d err=%v", result, starter.calls.Load(), err)
	}
	starter.fail.Store(true)
	result, err = runtime.Execute(context.Background(), seoregistry.ExecuteRequest{Scope: declaration.Scope, Base: base})
	if err != nil || result.Document.Title != base.Title || len(result.Fallbacks) != 1 ||
		result.Fallbacks[0].Reason != "provider_failed" || starter.calls.Load() != 2 {
		t.Fatalf("failed SEO fallback=%#v calls=%d err=%v", result, starter.calls.Load(), err)
	}
	starter.fail.Store(false)
	if err := manager.Stop(context.Background(), extension); err != nil {
		t.Fatal(err)
	}
	result, err = runtime.Execute(context.Background(), seoregistry.ExecuteRequest{Scope: declaration.Scope, Base: base})
	if err != nil || result.Document.Title != base.Title || len(result.Applied) != 0 ||
		len(result.Fallbacks) != 1 || result.Fallbacks[0].Reason != "runtime_unavailable" || starter.calls.Load() != 2 {
		t.Fatalf("disabled SEO fallback=%#v calls=%d err=%v", result, starter.calls.Load(), err)
	}
	traces := trace.SEOExecutionTraces(3)
	if len(traces) != 3 || traces[0].Fallbacks != 1 || traces[0].Calls[0].ExtensionID != extension.ID ||
		traces[1].Fallbacks != 1 || traces[1].Calls[0].Outcome != seoregistry.TraceCallFailed || traces[2].Applied != 1 {
		t.Fatalf("SEO attribution traces=%#v", traces)
	}
}

type seoExecutionStarter struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (*seoExecutionStarter) Start(context.Context, extensions.Extension) (RouteTarget, error) {
	return RouteTarget{InstanceID: "seo-reference-runtime"}, nil
}

func (*seoExecutionStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *seoExecutionStarter) InvokeVersionedSEO(
	_ context.Context,
	_ extensions.Extension,
	request VersionedSEORequest,
) (VersionedSEOResponse, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		return VersionedSEOResponse{}, errors.New("reference SEO provider failed")
	}
	input := ProtocolV2SEOApplyRequest{}
	if err := decodeSEOTransportValue(request.Input, &input); err != nil {
		return VersionedSEOResponse{}, err
	}
	if request.DeclarationID != input.Contribution.ID || request.Handler != input.Contribution.Handler ||
		request.ContractVersion != input.Contribution.ContractVersion ||
		request.Timeout != time.Duration(input.Contribution.TimeoutMS)*time.Millisecond {
		return VersionedSEOResponse{}, errors.New("SEO exact declaration drift")
	}
	input.Current.Title = "Plugin title"
	output, err := encodeSEOTransportValue(ProtocolV2SEOApplyResponse{Document: input.Current})
	return VersionedSEOResponse{Output: output}, err
}
