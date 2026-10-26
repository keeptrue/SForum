package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

func TestIdentitySessionEvaluateInvokerUsesExactRuntime(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	invoker, err := NewIdentitySessionEvaluateInvoker(fixture.runtime)
	if err != nil {
		t.Fatalf("new invoker: %v", err)
	}
	var _ identity.SessionPolicyEvaluateInvoker = invoker

	var accepted map[string]any
	var fenceCalls int
	err = invoker.InvokeExact(
		t.Context(),
		fixture.provider,
		fixture.operation.Name,
		42,
		map[string]any{"risk": true},
		func(_ context.Context, output map[string]any, fence func() error) error {
			if err := fence(); err != nil {
				return err
			}
			fenceCalls++
			accepted = output
			return nil
		},
	)
	if err != nil || accepted["disposition"] != "allow" || fenceCalls != 1 || fixture.starter.calls.Load() != 1 {
		t.Fatalf("invoke exact accepted=%#v fenceCalls=%d starter=%d err=%v", accepted, fenceCalls, fixture.starter.calls.Load(), err)
	}
}

func TestIdentitySessionEvaluateInvokerRejectsNilRuntime(t *testing.T) {
	if _, err := NewIdentitySessionEvaluateInvoker(nil); !errors.Is(err, ErrIdentityProviderInvocationInvalid) {
		t.Fatalf("nil runtime err=%v", err)
	}
}

func TestIdentitySessionEvaluateInvokerPropagatesTransportFailure(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	fixture.starter.invoke = func(
		context.Context,
		RuntimeInstanceIdentity,
		extensions.Extension,
		VersionedIdentityProviderRequest,
	) (VersionedIdentityProviderResponse, error) {
		return VersionedIdentityProviderResponse{}, errors.New("transport failed")
	}
	invoker, err := NewIdentitySessionEvaluateInvoker(fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	err = invoker.InvokeExact(
		t.Context(),
		fixture.provider,
		fixture.operation.Name,
		1,
		map[string]any{"risk": true},
		func(context.Context, map[string]any, func() error) error {
			t.Fatal("accept must not run after transport failure")
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected transport failure")
	}
}

func TestIdentitySessionEvaluateInvokerRejectsSameIDReplacementBeforeRuntimeCall(t *testing.T) {
	fixture := newManagerIdentityTransportFixture(t, strings.Repeat("a", 64))
	invoker, err := NewIdentitySessionEvaluateInvoker(fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
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

	err = invoker.InvokeExact(
		t.Context(), fixture.provider, fixture.operation.Name, 1,
		map[string]any{"risk": "not-a-boolean"},
		func(context.Context, map[string]any, func() error) error {
			t.Fatal("accept must not run for stale exact claim")
			return nil
		},
	)
	if !errors.Is(err, ErrIdentityProviderStale) || fixture.starter.calls.Load() != 0 {
		t.Fatalf("same-id replacement calls=%d err=%v", fixture.starter.calls.Load(), err)
	}
}
