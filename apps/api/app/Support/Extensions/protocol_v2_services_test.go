package extensionsruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestProtocolV2ServiceRegistrationsMatchManifest(t *testing.T) {
	client := &protocolV2Client{
		identity: &protocolv2.ExtensionIdentity{ExtensionId: "demo.plugin", InstanceId: "runtime-1"},
		services: []*protocolv2.ServiceDescriptor{{
			ServiceId: "demo.lookup", Version: "1.2.0",
			RequestSchemaId: "demo.lookup.request@1", ResponseSchemaId: "demo.lookup.response@1",
			RequiredAuthority: []string{"users.read"},
		}},
	}
	extension := extensions.Extension{ID: "demo.plugin", Manifest: extensions.Manifest{Services: []extensions.ManifestService{{
		ID: "demo.lookup", ContractVersion: "demo.lookup@1", Action: "replace", TargetID: "shared.lookup",
		Handler: "lookup", RequestSchema: "demo.lookup.request@1", ResponseSchema: "demo.lookup.response@1", Priority: 50,
	}}}}

	registrations, err := client.serviceRegistrations(extension)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 1 || registrations[0].ExtensionID != "demo.plugin" ||
		registrations[0].InstanceID != "runtime-1" || registrations[0].TargetID != "shared.lookup" ||
		registrations[0].Priority != 50 || registrations[0].Provider != client {
		t.Fatalf("registrations = %#v", registrations)
	}
	client.services[0].ServiceId = "mutated"
	if registrations[0].Descriptor.GetServiceId() != "demo.lookup" {
		t.Fatalf("registration descriptor was not cloned: %#v", registrations[0].Descriptor)
	}
}

func TestProtocolV2ServiceRegistrationsRejectDrift(t *testing.T) {
	manifest := extensions.Manifest{Services: []extensions.ManifestService{{
		ID: "demo.lookup", ContractVersion: "demo.lookup@1", Action: "add", Handler: "lookup",
		RequestSchema: "demo.request@1", ResponseSchema: "demo.response@1",
	}}}
	base := func() *protocolV2Client {
		return &protocolV2Client{
			identity: &protocolv2.ExtensionIdentity{InstanceId: "runtime-1"},
			services: []*protocolv2.ServiceDescriptor{{
				ServiceId: "demo.lookup", Version: "1.0.0",
				RequestSchemaId: "demo.request@1", ResponseSchemaId: "demo.response@1",
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*protocolV2Client)
		text   string
	}{
		{"undeclared", func(client *protocolV2Client) { client.services[0].ServiceId = "other.lookup" }, "not declared"},
		{"schema", func(client *protocolV2Client) { client.services[0].RequestSchemaId = "other@1" }, "schema"},
		{"major", func(client *protocolV2Client) { client.services[0].Version = "2.0.0" }, "does not match"},
		{"loose semver", func(client *protocolV2Client) { client.services[0].Version = "1.0" }, "strict SemVer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := base()
			test.mutate(client)
			_, err := client.serviceRegistrations(extensions.Extension{ID: "demo.plugin", Manifest: manifest})
			if err == nil || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("error = %v, want %q", err, test.text)
			}
		})
	}
}

func TestProtocolV2ForwardedServiceContextPreservesCallerData(t *testing.T) {
	client := &protocolV2Client{
		identity: &protocolv2.ExtensionIdentity{
			ExtensionId: "provider.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
			TrustGrantId: "grant", RuntimeEpoch: 1, InstanceId: "provider-runtime",
		},
		authority: []*protocolv2.AuthorityGrant{{Key: "settings.own"}},
		instance:  "provider-runtime",
	}
	caller := &protocolv2.RequestContext{
		RequestId: "caller-request", Trace: &protocolv2.TraceContext{TraceId: "trace-1"},
		Actor: &protocolv2.Actor{UserId: 42}, Locale: "zh-CN", IdempotencyKey: "idem-1",
		Extension:        &protocolv2.ExtensionIdentity{ExtensionId: "caller.plugin"},
		GrantedAuthority: []*protocolv2.AuthorityGrant{{Key: "raw.database"}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	forwarded := client.forwardedServiceContext(ctx, caller)
	if forwarded.GetActor() != nil || forwarded.GetLocale() != "zh-CN" ||
		forwarded.GetTrace().GetTraceId() != "trace-1" || forwarded.GetIdempotencyKey() != "idem-1" {
		t.Fatalf("caller context not propagated: %#v", forwarded)
	}
	if forwarded.GetExtension().GetExtensionId() != "provider.plugin" ||
		len(forwarded.GetGrantedAuthority()) != 1 || forwarded.GetGrantedAuthority()[0].GetKey() != "settings.own" {
		t.Fatalf("provider runtime binding not enforced: %#v", forwarded)
	}
	if forwarded.GetDeadline() == nil || !forwarded.GetDeadline().IsValid() || !forwarded.GetDeadline().AsTime().After(time.Now()) {
		t.Fatalf("deadline = %#v", forwarded.GetDeadline())
	}
}
