package extensionsruntime_test

import (
	"context"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func TestProtocolV2IdempotentPublicationRebuildsCompensatedServices(t *testing.T) {
	extension := protocolV2TestExtension(t, "v2")
	gateway, _ := newProtocolV2HostGateway()
	t.Cleanup(func() { _ = gateway.Close() })
	starter := extensionsruntime.NewProtocolStarter(extensionsruntime.ProtocolStarterConfig{
		Trust: staticRuntimeTrust{identity: extensions.RuntimeTrustIdentity{TrustGrantID: "41", ImpactDigest: "impact-41"}}, HostAPI: gateway,
	})
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	target, err := starter.Start(context.Background(), extension)
	if err != nil {
		t.Fatal(err)
	}
	identity := extensionsruntime.RuntimeInstanceIdentity{ExtensionID: extension.ID, InstanceID: target.InstanceID}
	if !gateway.UnregisterProtocolV2ServiceInstance(extension.ID, identity.InstanceID) {
		t.Fatal("expected exact service compensation")
	}
	if _, err := starter.PublishInstance(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	resolved, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("runtime.v2.service.echo", "1.0.0")
	if err != nil || resolved.Winner.InstanceID != identity.InstanceID {
		t.Fatalf("reconciled service = %#v, %v", resolved, err)
	}
}
