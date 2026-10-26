package extensionsruntime_test

import (
	"context"
	"errors"
	"testing"

	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtocolV2ExactLifecycleRunsOnStagedAndRetainedInstances(t *testing.T) {
	extension := p4LifecycleExtension(t)
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{Trust: p4LifecycleTrust{}})
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })

	firstTarget, err := starter.StartInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	first := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: firstTarget.InstanceID}
	result, err := starter.RunLifecycleInstance(
		context.Background(), first, extension,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionInstallPlan),
	)
	if err != nil || result.State != extensionsruntime.LifecycleProgressSucceeded {
		t.Fatalf("staged install.plan result = %#v, %v", result, err)
	}
	if _, err := starter.PublishInstance(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	secondTarget, err := starter.StartInstance(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	second := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: secondTarget.InstanceID}
	result, err = starter.RunLifecycleInstance(
		context.Background(), second, extension,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionInstall),
	)
	if err != nil || result.State != extensionsruntime.LifecycleProgressSucceeded {
		t.Fatalf("staged install result = %#v, %v", result, err)
	}
	if _, err := starter.PublishInstance(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := starter.InspectInstance(first)
	if err != nil || firstSnapshot.State != extensionsruntime.ProtocolRuntimeRetained {
		t.Fatalf("old instance was not retained: %#v, %v", firstSnapshot, err)
	}
	result, err = starter.RunLifecycleInstance(
		context.Background(), first, extension,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionUpgradeBefore),
	)
	if err != nil || result.State != extensionsruntime.LifecycleProgressSucceeded {
		t.Fatalf("retained upgrade.before result = %#v, %v", result, err)
	}

	stale := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: "missing-instance"}
	if _, err := starter.RunLifecycleInstance(
		context.Background(), stale, extension,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionUpgradeBefore),
	); !errors.Is(err, extensionsruntime.ErrRuntimeInstanceNotFound) {
		t.Fatalf("stale lifecycle identity error = %v", err)
	}

	wrongVersion := extension
	wrongVersion.Version = "9.9.9"
	if _, err := starter.RunLifecycleInstance(
		context.Background(), first, wrongVersion,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionUpgradeBefore),
	); !errors.Is(err, extensionsruntime.ErrInvalidLifecycleRun) {
		t.Fatalf("wrong version lifecycle error = %v", err)
	}
	wrongDigest := extension
	wrongDigest.PackageDigest = "wrong-digest"
	if _, err := starter.RunLifecycleInstance(
		context.Background(), first, wrongDigest,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionUpgradeBefore),
	); !errors.Is(err, extensionsruntime.ErrInvalidLifecycleRun) {
		t.Fatalf("wrong digest lifecycle error = %v", err)
	}
	driftedManifest := extension
	driftedLifecycle := *extension.Manifest.Lifecycle
	driftedLifecycle.ContractVersion = "forged.lifecycle@9"
	driftedManifest.Manifest.Lifecycle = &driftedLifecycle
	if _, err := starter.RunLifecycleInstance(
		context.Background(), first, driftedManifest,
		p4ExactLifecycleInvocation(t, extensionsruntime.LifecycleActionUpgradeBefore),
	); !errors.Is(err, extensionsruntime.ErrInvalidLifecycleRun) {
		t.Fatalf("drifted manifest lifecycle error = %v", err)
	}
}

func p4ExactLifecycleInvocation(t *testing.T, action extensionsruntime.LifecycleAction) extensionsruntime.LifecycleInvocation {
	t.Helper()
	invocation := p4LifecycleInvocation(t, "success")
	invocation.Action = action
	values := invocation.Input.GetValue().AsMap()
	values["expectedAction"] = float64(action)
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	invocation.Input.Value = value
	return invocation
}
