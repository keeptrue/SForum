package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
)

func TestManagerIdentityProviderInvocationKeepsExactLeaseThroughAccept(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	callerInput := map[string]any{
		"risk":   true,
		"nested": map[string]any{"source": "host"},
	}
	transportOutput := map[string]any{"disposition": "allow"}
	fixture.starter.invoke = func(
		_ context.Context,
		identity RuntimeInstanceIdentity,
		extension extensions.Extension,
		request VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error) {
		if identity.InstanceID != fixture.publication.Artifact.RuntimeInstanceID ||
			extension.ActiveVersionID != fixture.publication.Artifact.VersionID ||
			request.ProviderID != fixture.provider.ID || request.Operation != fixture.operation.Name ||
			request.InputSchema != fixture.operation.InputSchema ||
			request.InputSchemaWireReference != fixture.operation.InputSchemaWireReference ||
			request.OutputSchema != fixture.operation.OutputSchema ||
			request.OutputSchemaWireReference != fixture.operation.OutputSchemaWireReference ||
			request.ActorUserID != 42 {
			t.Fatalf("exact identity request = %#v, identity=%#v extension=%#v", request, identity, extension)
		}
		snapshot, err := fixture.manager.InspectRuntimeInstance(identity)
		if err != nil || snapshot.Admission.ActiveByClass[RuntimeCallProvider] != 1 {
			t.Fatalf("transport lease snapshot=%#v err=%v", snapshot, err)
		}
		request.Input["risk"] = false
		request.Input["nested"].(map[string]any)["source"] = "plugin"
		return VersionedIdentityProviderResponse{Output: transportOutput}, nil
	}

	accepted := false
	result, err := fixture.runtime.Invoke(
		t.Context(),
		IdentityProviderInvocation{
			ProviderID: fixture.provider.ID, Operation: fixture.operation.Name,
			ActorUserID: 42, Input: callerInput,
		},
		func(_ context.Context, proposal IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			accepted = true
			snapshot, inspectErr := fixture.manager.InspectRuntimeInstance(RuntimeInstanceIdentity{
				ExtensionID: proposal.Provider.Artifact.ExtensionID,
				InstanceID:  proposal.Provider.Artifact.RuntimeInstanceID,
			})
			if inspectErr != nil || snapshot.Admission.ActiveByClass[RuntimeCallProvider] != 1 {
				t.Fatalf("accept lease snapshot=%#v err=%v", snapshot, inspectErr)
			}
			if err := fence(); err != nil {
				return err
			}
			proposal.Output["disposition"] = "deny"
			proposal.Provider.Operations[0].Name = "mutated"
			return nil
		},
	)
	if err != nil || !accepted || result.RegistryRevision != fixture.registry.Revision() ||
		result.RegistryDigest != fixture.registry.Snapshot().Digest || result.RegistryDigest == "" ||
		result.Output["disposition"] != "allow" || result.Operation.Name != fixture.operation.Name ||
		result.Provider.Operations[0].Name != fixture.operation.Name {
		t.Fatalf("identity result=%#v accepted=%t err=%v", result, accepted, err)
	}
	if callerInput["risk"] != true || callerInput["nested"].(map[string]any)["source"] != "host" {
		t.Fatalf("caller input was mutated = %#v", callerInput)
	}
	transportOutput["disposition"] = "deny"
	if result.Output["disposition"] != "allow" {
		t.Fatalf("transport output alias escaped = %#v", result.Output)
	}
	snapshot, err := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
	if err != nil || snapshot.Admission.ActiveTotal != 0 {
		t.Fatalf("released identity lease snapshot=%#v err=%v", snapshot, err)
	}
}

func TestManagerIdentityProviderRejectsInvalidInputBeforeRuntimeCall(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	_, err := fixture.runtime.Invoke(
		t.Context(),
		IdentityProviderInvocation{
			ProviderID: fixture.provider.ID, Operation: fixture.operation.Name,
			Input: map[string]any{"risk": "not-a-boolean"},
		},
		func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
	)
	if !errors.Is(err, identityregistry.ErrSchemaValueInvalid) || fixture.starter.calls.Load() != 0 {
		t.Fatalf("invalid input calls=%d err=%v", fixture.starter.calls.Load(), err)
	}
}

func TestManagerIdentityProviderBoundsInputBeforeRuntimeCall(t *testing.T) {
	deep := map[string]any{"leaf": true}
	for range maximumComponentDocumentDepth + 2 {
		deep = map[string]any{"nested": deep}
	}
	for _, test := range []struct {
		name  string
		input map[string]any
	}{
		{
			name: "oversize",
			input: map[string]any{
				"risk":   true,
				"nested": map[string]any{"value": strings.Repeat("x", maximumIdentityProviderDocumentBytes)},
			},
		},
		{name: "deep", input: map[string]any{"risk": true, "nested": deep}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
			_, err := fixture.runtime.Invoke(
				t.Context(),
				IdentityProviderInvocation{
					ProviderID: fixture.provider.ID, Operation: fixture.operation.Name, Input: test.input,
				},
				func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
			)
			if !errors.Is(err, ErrIdentityProviderInvocationInvalid) || fixture.starter.calls.Load() != 0 {
				t.Fatalf("%s bounded input calls=%d err=%v", test.name, fixture.starter.calls.Load(), err)
			}
		})
	}
}

func TestManagerIdentityProviderBoundsOutputBeforeAccept(t *testing.T) {
	deep := map[string]any{"leaf": true}
	for range maximumComponentDocumentDepth + 2 {
		deep = map[string]any{"nested": deep}
	}
	for _, test := range []struct {
		name   string
		output map[string]any
	}{
		{
			name: "oversize",
			output: map[string]any{
				"disposition": "allow", "value": strings.Repeat("x", maximumIdentityProviderDocumentBytes),
			},
		},
		{name: "deep", output: map[string]any{"disposition": "allow", "value": deep}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
			fixture.starter.invoke = func(
				context.Context, RuntimeInstanceIdentity, extensions.Extension, VersionedIdentityProviderRequest,
			) (VersionedIdentityProviderResponse, error) {
				return VersionedIdentityProviderResponse{Output: test.output}, nil
			}
			accepted := false
			_, err := fixture.runtime.Invoke(
				t.Context(), fixture.invocation(),
				func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error {
					accepted = true
					return nil
				},
			)
			if !errors.Is(err, ErrIdentityProviderInvocationInvalid) || accepted || fixture.starter.calls.Load() != 1 {
				t.Fatalf("%s bounded output calls=%d accepted=%t err=%v", test.name, fixture.starter.calls.Load(), accepted, err)
			}
			snapshot, inspectErr := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
			if inspectErr != nil || snapshot.Admission.ActiveTotal != 0 {
				t.Fatalf("%s output lease snapshot=%#v err=%v", test.name, snapshot, inspectErr)
			}
		})
	}
}

func TestManagerIdentityProviderRejectsArtifactAndRegistryDrift(t *testing.T) {
	t.Run("managed artifact", func(t *testing.T) {
		fixture := newManagerIdentityTransportFixture(t, strings.Repeat("b", 64))
		_, err := fixture.runtime.Invoke(
			t.Context(), fixture.invocation(),
			func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
		)
		if !errors.Is(err, ErrIdentityProviderStale) || fixture.starter.calls.Load() != 0 {
			t.Fatalf("artifact drift calls=%d err=%v", fixture.starter.calls.Load(), err)
		}
	})

	t.Run("unrelated registry revision before accept", func(t *testing.T) {
		fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
		committed := false
		result, err := fixture.runtime.Invoke(
			t.Context(), fixture.invocation(),
			func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
				artifact, artifactErr := identityregistry.NewCoreArtifact(
					"core.identity.audit", "1.0.0", strings.Repeat("c", 64),
				)
				if artifactErr != nil {
					return artifactErr
				}
				if _, publishErr := fixture.registry.Publish(identityregistry.Publication{Artifact: artifact}); publishErr != nil {
					return publishErr
				}
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
				committed = true
				return nil
			},
		)
		if err != nil || !committed || fixture.starter.calls.Load() != 1 ||
			result.Provider.ID != fixture.provider.ID {
			t.Fatalf("unrelated drift result=%#v calls=%d committed=%t err=%v", result, fixture.starter.calls.Load(), committed, err)
		}
	})
}

func TestManagerIdentityProviderDoesNotRejectAcceptedEffectAfterFence(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	acceptedRevision := fixture.registry.Revision()
	committed := false
	result, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			if fenceErr := fence(); fenceErr != nil {
				return fenceErr
			}
			artifact, artifactErr := identityregistry.NewCoreArtifact(
				"core.identity.post_commit", "1.0.0", strings.Repeat("c", 64),
			)
			if artifactErr != nil {
				return artifactErr
			}
			if _, publishErr := fixture.registry.Publish(identityregistry.Publication{Artifact: artifact}); publishErr != nil {
				return publishErr
			}
			committed = true
			return nil
		},
	)
	if err != nil || !committed || result.RegistryRevision != acceptedRevision ||
		fixture.registry.Revision() == acceptedRevision {
		t.Fatalf(
			"post-fence result=%#v committed=%t current_revision=%d err=%v",
			result, committed, fixture.registry.Revision(), err,
		)
	}
}

func TestManagerIdentityProviderRejectsNonPluginManagedArtifact(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	fixture.manager.mu.Lock()
	running := fixture.manager.running[fixture.extension.ID]
	running.Type = extensions.TypeTheme
	running.Manifest.Type = extensions.TypeTheme
	fixture.manager.running[fixture.extension.ID] = running
	managed := fixture.manager.runtimeInstances[fixture.extension.ID][fixture.publication.Artifact.RuntimeInstanceID]
	managed.extension.Type = extensions.TypeTheme
	managed.extension.Manifest.Type = extensions.TypeTheme
	fixture.manager.mu.Unlock()

	_, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
	)
	if !errors.Is(err, ErrIdentityProviderStale) || fixture.starter.calls.Load() != 0 {
		t.Fatalf("non-plugin managed artifact calls=%d err=%v", fixture.starter.calls.Load(), err)
	}
}

func TestManagerIdentityProviderNeverFallsBackToActiveReplacement(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	oldIdentity := RuntimeInstanceIdentity{
		ExtensionID: fixture.extension.ID,
		InstanceID:  fixture.publication.Artifact.RuntimeInstanceID,
	}
	retained := acquireManagerRuntimeCall(t, fixture.manager, oldIdentity, RuntimeCallRoute)
	replacement := managerRuntimeExtension(fixture.extension.ID, "2.0.0", strings.Repeat("d", 64))
	replacement.ActiveVersionID = fixture.extension.ActiveVersionID + 1
	replacement.Manifest.Backend.ProtocolVersion = 2
	if err := fixture.manager.Start(t.Context(), replacement); err != nil {
		t.Fatal(err)
	}
	retained.Release()

	_, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
	)
	if !errors.Is(err, ErrRuntimeInstanceNotActive) || fixture.starter.calls.Load() != 0 {
		t.Fatalf("replacement fallback calls=%d err=%v", fixture.starter.calls.Load(), err)
	}
	active, activeErr := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
	if activeErr != nil || active.Identity.InstanceID != "identity-runtime-replacement" {
		t.Fatalf("active replacement=%#v err=%v", active, activeErr)
	}
}

func TestManagerIdentityProviderInvokeExactRejectsSameIDArtifactReplacementBeforeTransport(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	expected := fixture.provider
	replacement := fixture.publication
	replacement.Artifact.ExtensionVersion = "2.0.0"
	replacement.Artifact.PackageDigest = strings.Repeat("d", 64)
	replacement.Artifact.VersionID++
	replacement.Artifact.RuntimeInstanceID = "identity-runtime-replacement"
	if _, removed, err := fixture.registry.Remove(fixture.publication.Artifact); err != nil || !removed {
		t.Fatalf("remove source publication removed=%t err=%v", removed, err)
	}
	if _, err := fixture.registry.Publish(replacement); err != nil {
		t.Fatalf("publish replacement: %v", err)
	}

	_, err := fixture.runtime.InvokeExact(
		t.Context(), expected, fixture.invocation(),
		func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error {
			t.Fatal("accept must not run for a stale exact claim")
			return nil
		},
	)
	if !errors.Is(err, ErrIdentityProviderStale) || fixture.starter.calls.Load() != 0 {
		t.Fatalf("same-id replacement transport calls=%d err=%v", fixture.starter.calls.Load(), err)
	}
}

func TestProtocolStarterIdentityProviderUsesExactRetainedInstance(t *testing.T) {
	var exactCalls, activeCalls atomic.Int32
	exactClient := newProtocolV2IdentityProviderTestClient(t, func(
		_ context.Context,
		request *pluginwire.ProviderCallRequest,
	) (*pluginwire.ProviderCallResponse, error) {
		exactCalls.Add(1)
		return protocolV2IdentityProviderTestResponse(
			request, "plugin.identity.runtime.risk.output@1", map[string]any{"source": "exact"},
		), nil
	})
	activeClient := newProtocolV2IdentityProviderTestClient(t, func(
		_ context.Context,
		request *pluginwire.ProviderCallRequest,
	) (*pluginwire.ProviderCallResponse, error) {
		activeCalls.Add(1)
		return protocolV2IdentityProviderTestResponse(
			request, "plugin.identity.runtime.risk.output@1", map[string]any{"source": "active"},
		), nil
	})
	exactIdentity := RuntimeInstanceIdentity{ExtensionID: "plugin.identity.runtime", InstanceID: "runtime-exact"}
	activeIdentity := RuntimeInstanceIdentity{ExtensionID: exactIdentity.ExtensionID, InstanceID: "runtime-active"}
	exactClient.identity.InstanceId = exactIdentity.InstanceID
	activeClient.identity.InstanceId = activeIdentity.InstanceID
	extension := extensions.Extension{
		ID: exactIdentity.ExtensionID, Version: "1.0.0", PackageDigest: "digest-v1", Type: extensions.TypePlugin,
		Manifest: extensions.Manifest{
			ID: exactIdentity.ExtensionID, Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{ProtocolVersion: 2},
		},
	}
	manifestDigest, err := protocolRuntimeManifestDigest(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	starter := &ProtocolStarter{
		protocols:              map[string]PluginProtocol{exactIdentity.ExtensionID: activeClient},
		activeRuntimeInstances: map[string]string{exactIdentity.ExtensionID: activeIdentity.InstanceID},
		runtimeInstances: map[string]map[string]*protocolRuntimeInstance{
			exactIdentity.ExtensionID: {
				exactIdentity.InstanceID: {
					identity: exactIdentity, extensionVersion: "1.0.0", artifactDigest: "digest-v1",
					manifestDigest: manifestDigest, protocolVersion: 2, protocol: exactClient,
				},
				activeIdentity.InstanceID: {
					identity: activeIdentity, extensionVersion: "1.0.0", artifactDigest: "digest-v1",
					manifestDigest: manifestDigest, protocolVersion: 2, protocol: activeClient,
				},
			},
		},
	}
	result, err := starter.InvokeIdentityProviderInstance(
		t.Context(), exactIdentity, extension, protocolV2IdentityProviderTestRequest(),
	)
	if err != nil || result.Output["source"] != "exact" || exactCalls.Load() != 1 || activeCalls.Load() != 0 {
		t.Fatalf("exact result=%#v exact=%d active=%d err=%v", result, exactCalls.Load(), activeCalls.Load(), err)
	}
	missing := RuntimeInstanceIdentity{ExtensionID: exactIdentity.ExtensionID, InstanceID: "runtime-missing"}
	if _, err := starter.InvokeIdentityProviderInstance(
		t.Context(), missing, extension, protocolV2IdentityProviderTestRequest(),
	); !errors.Is(err, ErrIdentityProviderUnavailable) || activeCalls.Load() != 0 {
		t.Fatalf("missing exact runtime active=%d err=%v", activeCalls.Load(), err)
	}
	starter.runtimeInstances[exactIdentity.ExtensionID][exactIdentity.InstanceID].manifestDigest = "drifted"
	if _, err := starter.InvokeIdentityProviderInstance(
		t.Context(), exactIdentity, extension, protocolV2IdentityProviderTestRequest(),
	); !errors.Is(err, ErrIdentityProviderStale) || exactCalls.Load() != 1 || activeCalls.Load() != 0 {
		t.Fatalf("manifest drift exact=%d active=%d err=%v", exactCalls.Load(), activeCalls.Load(), err)
	}
}

func TestManagerIdentityProviderForceDrainCancelsCallAndReleasesLease(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	started := make(chan struct{})
	fixture.starter.invoke = func(
		ctx context.Context, _ RuntimeInstanceIdentity, _ extensions.Extension, _ VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error) {
		close(started)
		<-ctx.Done()
		return VersionedIdentityProviderResponse{}, ctx.Err()
	}
	type outcome struct{ err error }
	finished := make(chan outcome, 1)
	var accepted atomic.Bool
	go func() {
		_, err := fixture.runtime.Invoke(
			context.Background(), fixture.invocation(),
			func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error {
				accepted.Store(true)
				return nil
			},
		)
		finished <- outcome{err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("identity call did not start")
	}
	cause := errors.New("forced identity shutdown")
	identity := RuntimeInstanceIdentity{
		ExtensionID: fixture.extension.ID,
		InstanceID:  fixture.publication.Artifact.RuntimeInstanceID,
	}
	if _, err := fixture.manager.ForceDrain(identity, cause); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-finished:
		if !errors.Is(result.err, cause) || accepted.Load() {
			t.Fatalf("forced identity result accepted=%t err=%v", accepted.Load(), result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("force-cancelled identity call did not exit")
	}
	snapshot, err := fixture.manager.InspectRuntimeInstance(identity)
	if err != nil || snapshot.Admission.ActiveTotal != 0 || !snapshot.Admission.Forced {
		t.Fatalf("forced released snapshot=%#v err=%v", snapshot, err)
	}
}

func TestManagerIdentityProviderRetainsLeaseUntilNonCooperativeTimeoutCallReturns(t *testing.T) {
	fixture := newManagerIdentityTransportFixtureWithTimeout(t, strings.Repeat("a", 64), 20)
	started := make(chan struct{})
	deadlineObserved := make(chan struct{})
	releaseInvocation := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseInvocation) }) }
	t.Cleanup(release)
	fixture.starter.invoke = func(
		ctx context.Context, _ RuntimeInstanceIdentity, _ extensions.Extension, _ VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error) {
		close(started)
		<-ctx.Done()
		close(deadlineObserved)
		<-releaseInvocation
		return VersionedIdentityProviderResponse{Output: map[string]any{"disposition": "allow"}}, nil
	}
	type outcome struct{ err error }
	finished := make(chan outcome, 1)
	go func() {
		_, err := fixture.runtime.Invoke(
			context.Background(), fixture.invocation(),
			func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error {
				return errors.New("accept must not run after timeout")
			},
		)
		finished <- outcome{err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("identity timeout call did not start")
	}
	select {
	case <-deadlineObserved:
	case <-time.After(time.Second):
		t.Fatal("identity timeout was not delivered to invoker")
	}
	select {
	case result := <-finished:
		t.Fatalf("non-cooperative identity call returned before invoker exit: %v", result.err)
	default:
	}
	identity := RuntimeInstanceIdentity{
		ExtensionID: fixture.extension.ID,
		InstanceID:  fixture.publication.Artifact.RuntimeInstanceID,
	}
	snapshot, err := fixture.manager.InspectRuntimeInstance(identity)
	if err != nil || snapshot.Admission.ActiveByClass[RuntimeCallProvider] != 1 {
		t.Fatalf("timeout lease snapshot=%#v err=%v", snapshot, err)
	}
	release()
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("identity timeout error = %v", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("identity timeout call did not finish after invoker exit")
	}
	snapshot, err = fixture.manager.InspectRuntimeInstance(identity)
	if err != nil || snapshot.Admission.ActiveTotal != 0 {
		t.Fatalf("released timeout lease snapshot=%#v err=%v", snapshot, err)
	}
}

func TestManagerIdentityProviderRejectsQuarantineBeforeAccept(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	cause := errors.New("identity runtime incident")
	committed := false
	_, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			if _, quarantineErr := fixture.manager.QuarantineRuntimeInstance(RuntimeInstanceArtifactIdentity{
				RuntimeInstanceIdentity: RuntimeInstanceIdentity{
					ExtensionID: fixture.extension.ID,
					InstanceID:  fixture.publication.Artifact.RuntimeInstanceID,
				},
				ExtensionVersion: fixture.extension.Version,
				ArtifactDigest:   fixture.extension.PackageDigest,
			}, cause); quarantineErr != nil {
				return quarantineErr
			}
			if fenceErr := fence(); fenceErr != nil {
				return fenceErr
			}
			committed = true
			return nil
		},
	)
	if !errors.Is(err, ErrIdentityProviderStale) || committed {
		t.Fatalf("quarantined identity committed=%t err=%v", committed, err)
	}
	identity := RuntimeInstanceIdentity{
		ExtensionID: fixture.extension.ID,
		InstanceID:  fixture.publication.Artifact.RuntimeInstanceID,
	}
	snapshot, inspectErr := fixture.manager.InspectRuntimeInstance(identity)
	if inspectErr != nil || snapshot.Admission.ActiveTotal != 0 || !snapshot.Admission.Quarantined {
		t.Fatalf("quarantined released snapshot=%#v err=%v", snapshot, inspectErr)
	}
}

func TestManagerIdentityProviderAcceptFailureIsTypedAndReleasesLease(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	failure := errors.New("audit transaction failed")
	_, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			if err := fence(); err != nil {
				return err
			}
			return failure
		},
	)
	if !errors.Is(err, ErrIdentityProviderAcceptFailed) || !errors.Is(err, failure) {
		t.Fatalf("accept failure = %v", err)
	}
	snapshot, inspectErr := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
	if inspectErr != nil || snapshot.Admission.ActiveTotal != 0 {
		t.Fatalf("accept failure lease snapshot=%#v err=%v", snapshot, inspectErr)
	}
}

func TestManagerIdentityProviderRequiresExactlyOneCommitFence(t *testing.T) {
	for _, test := range []struct {
		name   string
		accept IdentityProviderAccept
	}{
		{
			name: "missing",
			accept: func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error {
				return nil
			},
		},
		{
			name: "duplicate",
			accept: func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
				if err := fence(); err != nil {
					return err
				}
				_ = fence()
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
			_, err := fixture.runtime.Invoke(t.Context(), fixture.invocation(), test.accept)
			if !errors.Is(err, ErrIdentityProviderAcceptFailed) ||
				!errors.Is(err, ErrIdentityProviderInvocationInvalid) {
				t.Fatalf("%s commit fence error = %v", test.name, err)
			}
			snapshot, inspectErr := fixture.manager.ActiveRuntimeInstance(fixture.extension.ID)
			if inspectErr != nil || snapshot.Admission.ActiveTotal != 0 {
				t.Fatalf("%s lease snapshot=%#v err=%v", test.name, snapshot, inspectErr)
			}
		})
	}
}

func TestManagerIdentityProviderCannotIgnoreCommitFenceFailure(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	_, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			if _, replaceErr := fixture.registry.ReplaceAllIfRevision(
				fixture.registry.Revision(), []identityregistry.Publication{fixture.publication},
				fixture.registry.Snapshot().Tombstones, true,
			); replaceErr != nil {
				return replaceErr
			}
			_ = fence()
			return nil
		},
	)
	if !errors.Is(err, ErrIdentityProviderAcceptFailed) || !errors.Is(err, identityregistry.ErrSafeMode) {
		t.Fatalf("ignored fence failure err=%v", err)
	}
}

func TestManagerIdentityProviderPreservesFenceAndAcceptFailures(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	acceptFailure := errors.New("Host audit failed")
	_, err := fixture.runtime.Invoke(
		t.Context(), fixture.invocation(),
		func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
			if _, replaceErr := fixture.registry.ReplaceAllIfRevision(
				fixture.registry.Revision(), []identityregistry.Publication{fixture.publication},
				fixture.registry.Snapshot().Tombstones, true,
			); replaceErr != nil {
				return replaceErr
			}
			_ = fence()
			return acceptFailure
		},
	)
	if !errors.Is(err, ErrIdentityProviderAcceptFailed) ||
		!errors.Is(err, identityregistry.ErrSafeMode) || !errors.Is(err, acceptFailure) {
		t.Fatalf("combined fence/accept failure err=%v", err)
	}
}

func TestManagerIdentityProviderRejectsSafeModeAndCatalogOnlyProvider(t *testing.T) {
	t.Run("safe mode", func(t *testing.T) {
		fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
		if _, err := fixture.registry.ReplaceAllIfRevision(
			fixture.registry.Revision(), []identityregistry.Publication{fixture.publication}, nil, true,
		); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.runtime.Invoke(
			t.Context(), fixture.invocation(),
			func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
		)
		if !errors.Is(err, identityregistry.ErrSafeMode) || fixture.starter.calls.Load() != 0 {
			t.Fatalf("safe-mode calls=%d err=%v", fixture.starter.calls.Load(), err)
		}
	})

	t.Run("safe mode commit fence", func(t *testing.T) {
		fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
		committed := false
		_, err := fixture.runtime.Invoke(
			t.Context(), fixture.invocation(),
			func(_ context.Context, _ IdentityProviderInvocationResult, fence IdentityProviderCommitFence) error {
				if _, replaceErr := fixture.registry.ReplaceAllIfRevision(
					fixture.registry.Revision(), []identityregistry.Publication{fixture.publication}, nil, true,
				); replaceErr != nil {
					return replaceErr
				}
				if fenceErr := fence(); fenceErr != nil {
					return fenceErr
				}
				committed = true
				return nil
			},
		)
		if !errors.Is(err, identityregistry.ErrSafeMode) || committed {
			t.Fatalf("safe-mode fence committed=%t err=%v", committed, err)
		}
	})

	t.Run("catalog only", func(t *testing.T) {
		registry := identityregistry.New()
		publication := identityregistry.Publication{
			Artifact: identityregistry.Artifact{
				ExtensionID: "plugin.identity.catalog", ExtensionVersion: "1.0.0",
				PackageDigest: strings.Repeat("e", 64), VersionID: 9,
			},
			Identity: &identityregistry.IdentityDeclaration{
				ContractVersion: "plugin.identity.catalog@1",
				Providers: []identityregistry.Provider{{
					ID: "plugin.identity.catalog.profile", ContractVersion: "plugin.identity.catalog.profile@1",
					Kind: identityregistry.ProviderKindProfile, Handler: "identity.profile",
				}},
			},
		}
		if _, err := registry.Publish(publication); err != nil {
			t.Fatal(err)
		}
		starter := &managerIdentityTransportStarter{}
		manager := NewManager(ManagerConfig{Starter: starter})
		runtime, runtimeErr := NewIdentityProviderRuntime(manager, registry)
		if runtimeErr != nil {
			t.Fatal(runtimeErr)
		}
		_, err := runtime.Invoke(
			t.Context(),
			IdentityProviderInvocation{
				ProviderID: "plugin.identity.catalog.profile", Operation: "section.read", Input: map[string]any{},
			},
			func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
		)
		if !errors.Is(err, ErrIdentityProviderUnavailable) || starter.calls.Load() != 0 {
			t.Fatalf("catalog-only calls=%d err=%v", starter.calls.Load(), err)
		}
	})

	t.Run("Core executable provider", func(t *testing.T) {
		artifact, err := identityregistry.NewCoreArtifact(
			"core.identity.runtime", "1.0.0", strings.Repeat("f", 64),
		)
		if err != nil {
			t.Fatal(err)
		}
		publication := identityregistry.Publication{
			Artifact: artifact,
			Identity: &identityregistry.IdentityDeclaration{
				ContractVersion: "core.identity.runtime@1",
				Providers: []identityregistry.Provider{{
					ID: "core.identity.runtime.risk", ContractVersion: "core.identity.runtime.risk@1",
					Kind: identityregistry.ProviderKindRisk, Handler: "identity.risk",
					Operations: []identityregistry.ProviderOperation{{
						Name: "risk.evaluate", InputSchema: "schemas/input.json", OutputSchema: "schemas/output.json",
						TimeoutMS: 1_000, FailurePolicy: identityregistry.ProviderFailureFailClosed,
					}},
				}},
			},
		}
		bound, bindErr := identityregistry.BindJSONSchemas(
			publication, nil,
			[]identityregistry.ProviderOperationSchemaBinding{{
				ProviderID: "core.identity.runtime.risk", ContractVersion: "core.identity.runtime.risk@1",
				Operation: "risk.evaluate", Artifact: artifact,
				Input: managerIdentitySchemaMaterial(
					"schemas/input.json", "core.identity.runtime.risk.input@1", `{"type":"object"}`,
				),
				Output: managerIdentitySchemaMaterial(
					"schemas/output.json", "core.identity.runtime.risk.output@1", `{"type":"object"}`,
				),
			}},
		)
		if bindErr != nil {
			t.Fatal(bindErr)
		}
		registry := identityregistry.New()
		if _, err := registry.Publish(bound); err != nil {
			t.Fatal(err)
		}
		starter := &managerIdentityTransportStarter{}
		manager := NewManager(ManagerConfig{Starter: starter})
		runtime, runtimeErr := NewIdentityProviderRuntime(manager, registry)
		if runtimeErr != nil {
			t.Fatal(runtimeErr)
		}
		_, err = runtime.Invoke(
			t.Context(),
			IdentityProviderInvocation{
				ProviderID: "core.identity.runtime.risk", Operation: "risk.evaluate", Input: map[string]any{},
			},
			func(context.Context, IdentityProviderInvocationResult, IdentityProviderCommitFence) error { return nil },
		)
		if !errors.Is(err, ErrIdentityProviderUnavailable) || starter.calls.Load() != 0 {
			t.Fatalf("Core provider calls=%d err=%v", starter.calls.Load(), err)
		}
	})
}

type managerIdentityTransportFixture struct {
	manager     *Manager
	runtime     *IdentityProviderRuntime
	registry    *identityregistry.Registry
	starter     *managerIdentityTransportStarter
	extension   extensions.Extension
	publication identityregistry.Publication
	provider    identityregistry.ProviderContribution
	operation   identityregistry.ProviderOperation
}

func newManagerIdentityTransportFixture(
	t *testing.T,
	registryArtifactDigest string,
) managerIdentityTransportFixture {
	return newManagerIdentityTransportFixtureWithTimeout(t, registryArtifactDigest, 1_000)
}

func newManagerIdentityTransportFixtureWithTimeout(
	t *testing.T,
	registryArtifactDigest string,
	timeoutMS int,
) managerIdentityTransportFixture {
	t.Helper()
	starter := &managerIdentityTransportStarter{
		startIDs: []string{"identity-runtime", "identity-runtime-replacement"},
	}
	manager := NewManager(ManagerConfig{Starter: starter})
	extension := managerRuntimeExtension("plugin.identity.runtime", "1.0.0", strings.Repeat("a", 64))
	extension.ActiveVersionID = 7
	extension.Manifest.Backend.ProtocolVersion = 2
	if err := manager.Start(t.Context(), extension); err != nil {
		t.Fatal(err)
	}
	publication, operationBinding := managerIdentityTransportPublication(
		registryArtifactDigest, extension.ActiveVersionID, "identity-runtime", timeoutMS,
	)
	bound, err := identityregistry.BindJSONSchemas(publication, nil, []identityregistry.ProviderOperationSchemaBinding{operationBinding})
	if err != nil {
		t.Fatal(err)
	}
	registry := identityregistry.New()
	if _, err := registry.Publish(bound); err != nil {
		t.Fatal(err)
	}
	provider, err := registry.ResolveProvider("plugin.identity.runtime.risk")
	if err != nil {
		t.Fatal(err)
	}
	operation, found := exactIdentityProviderOperation(provider, "risk.evaluate")
	if !found {
		t.Fatal("bound Identity operation is missing")
	}
	starter.invoke = func(
		context.Context, RuntimeInstanceIdentity, extensions.Extension, VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error) {
		return VersionedIdentityProviderResponse{Output: map[string]any{"disposition": "allow"}}, nil
	}
	runtime, err := NewIdentityProviderRuntime(manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	return managerIdentityTransportFixture{
		manager: manager, runtime: runtime, registry: registry, starter: starter, extension: extension,
		publication: bound, provider: provider, operation: operation,
	}
}

func (f managerIdentityTransportFixture) invocation() IdentityProviderInvocation {
	return IdentityProviderInvocation{
		ProviderID: f.provider.ID, Operation: f.operation.Name,
		Input: map[string]any{"risk": true, "nested": map[string]any{"source": "host"}},
	}
}

type managerIdentityTransportStarter struct {
	mu       sync.Mutex
	startIDs []string
	starts   int
	calls    atomic.Int32
	invoke   func(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error)
}

func (s *managerIdentityTransportStarter) Start(
	context.Context,
	extensions.Extension,
) (RouteTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts >= len(s.startIDs) {
		return RouteTarget{}, errors.New("unexpected identity runtime start")
	}
	instanceID := s.startIDs[s.starts]
	s.starts++
	return RouteTarget{InstanceID: instanceID}, nil
}

func (*managerIdentityTransportStarter) Stop(context.Context, extensions.Extension) error { return nil }

func (s *managerIdentityTransportStarter) InvokeIdentityProviderInstance(
	ctx context.Context,
	identity RuntimeInstanceIdentity,
	extension extensions.Extension,
	request VersionedIdentityProviderRequest,
) (VersionedIdentityProviderResponse, error) {
	s.calls.Add(1)
	return s.invoke(ctx, identity, extension, request)
}

func managerIdentityTransportPublication(
	digest string,
	versionID int64,
	runtimeInstanceID string,
	timeoutMS int,
) (identityregistry.Publication, identityregistry.ProviderOperationSchemaBinding) {
	artifact := identityregistry.Artifact{
		ExtensionID: "plugin.identity.runtime", ExtensionVersion: "1.0.0",
		PackageDigest: digest, VersionID: versionID, RuntimeInstanceID: runtimeInstanceID,
	}
	publication := identityregistry.Publication{
		Artifact: artifact,
		Identity: &identityregistry.IdentityDeclaration{
			ContractVersion: "plugin.identity.runtime@1",
			Providers: []identityregistry.Provider{{
				ID: "plugin.identity.runtime.risk", ContractVersion: "plugin.identity.runtime.risk@1",
				Kind: identityregistry.ProviderKindRisk, Handler: "identity.risk", Priority: 10,
				Operations: []identityregistry.ProviderOperation{{
					Name: "risk.evaluate", InputSchema: "schemas/risk-input.json",
					OutputSchema: "schemas/risk-output.json", TimeoutMS: timeoutMS,
					FailurePolicy: identityregistry.ProviderFailureFailClosed,
				}},
			}},
		},
	}
	return publication, identityregistry.ProviderOperationSchemaBinding{
		ProviderID: "plugin.identity.runtime.risk", ContractVersion: "plugin.identity.runtime.risk@1",
		Operation: "risk.evaluate", Artifact: artifact,
		Input: managerIdentitySchemaMaterial(
			"schemas/risk-input.json", "plugin.identity.runtime.risk.input@1",
			`{"type":"object","required":["risk"],"properties":{"risk":{"type":"boolean"},"nested":{"type":"object"}},"additionalProperties":false}`,
		),
		Output: managerIdentitySchemaMaterial(
			"schemas/risk-output.json", "plugin.identity.runtime.risk.output@1",
			`{"type":"object","required":["disposition"],"properties":{"disposition":{"enum":["allow","deny","step_up"]}},"additionalProperties":false}`,
		),
	}
}

func managerIdentitySchemaMaterial(
	reference string,
	wireReference string,
	schema string,
) identityregistry.JSONSchemaMaterial {
	body := []byte(schema)
	digest := sha256.Sum256(body)
	return identityregistry.JSONSchemaMaterial{
		Reference: reference, WireReference: wireReference,
		Digest: hex.EncodeToString(digest[:]), Schema: body,
	}
}

var _ Starter = (*managerIdentityTransportStarter)(nil)
var _ exactIdentityProviderInvoker = (*managerIdentityTransportStarter)(nil)
