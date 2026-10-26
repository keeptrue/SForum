package extensionsruntime

import (
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

func TestProtocolV2IdentityRuntimeHandshakeRequiresExactFeature(t *testing.T) {
	exact := &protocolwire.ProtocolFeature{
		Name: protocolV2IdentityRuntimeFeatureName, Version: protocolV2IdentityRuntimeFeatureVersion,
	}
	for _, test := range []struct {
		name       string
		identity   *extensions.ManifestIdentity
		selected   []*protocolwire.ProtocolFeature
		wantOffer  bool
		wantReason string
	}{
		{name: "absent"},
		{name: "inert", identity: protocolV2IdentityRuntimeManifest(false)},
		{name: "later executable provider", identity: protocolV2IdentityRuntimeManifestWithLaterExecutableProvider(), selected: []*protocolwire.ProtocolFeature{exact}, wantOffer: true},
		{name: "exact", identity: protocolV2IdentityRuntimeManifest(true), selected: []*protocolwire.ProtocolFeature{exact}, wantOffer: true},
		{name: "missing", identity: protocolV2IdentityRuntimeManifest(true), wantOffer: true, wantReason: "identity_runtime.feature_required"},
		{name: "wrong", identity: protocolV2IdentityRuntimeManifest(true), selected: []*protocolwire.ProtocolFeature{{Name: protocolV2IdentityRuntimeFeatureName, Version: "2"}}, wantOffer: true, wantReason: "protocol.feature_mismatch"},
		{name: "duplicate", identity: protocolV2IdentityRuntimeManifest(true), selected: []*protocolwire.ProtocolFeature{exact, exact}, wantOffer: true, wantReason: "protocol.feature_duplicate"},
		{name: "unoffered", identity: protocolV2IdentityRuntimeManifest(false), selected: []*protocolwire.ProtocolFeature{exact}, wantReason: "protocol.feature_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &protocolV2QueryHandshakeTestServer{selected: test.selected}
			client := newProtocolV2QueryHandshakeTestClient(
				t, server, protocolV2IdentityRuntimeHandshakeConfig(test.identity),
			)
			err := client.Handshake(t.Context())
			if test.wantReason == "" {
				if err != nil {
					t.Fatalf("Handshake error = %v", err)
				}
			} else {
				var protocolErr *ProtocolV2Error
				if !errors.As(err, &protocolErr) || protocolErr.Reason != test.wantReason {
					t.Fatalf("Handshake error = %v, want reason %q", err, test.wantReason)
				}
			}

			offered := false
			offerCount := 0
			for _, feature := range server.offered {
				if feature.GetName() != protocolV2IdentityRuntimeFeatureName {
					continue
				}
				offerCount++
				offered = feature.GetVersion() == protocolV2IdentityRuntimeFeatureVersion
				if !feature.GetRequired() {
					t.Fatal("identity.runtime offer was not marked required")
				}
			}
			if offered != test.wantOffer || offerCount > 1 {
				t.Fatalf("identity.runtime offered=%v count=%d, want %v: %#v", offered, offerCount, test.wantOffer, server.offered)
			}
		})
	}
}

func TestProtocolV2QueryAndIdentityRuntimeHandshakeRequireExactFeatures(t *testing.T) {
	queryExact := &protocolwire.ProtocolFeature{
		Name: protocolV2QueryRuntimeFeatureName, Version: protocolV2QueryRuntimeFeatureVersion,
	}
	identityExact := &protocolwire.ProtocolFeature{
		Name: protocolV2IdentityRuntimeFeatureName, Version: protocolV2IdentityRuntimeFeatureVersion,
	}
	for _, test := range []struct {
		name       string
		selected   []*protocolwire.ProtocolFeature
		wantReason string
	}{
		{name: "exact", selected: []*protocolwire.ProtocolFeature{queryExact, identityExact}},
		{name: "missing query", selected: []*protocolwire.ProtocolFeature{identityExact}, wantReason: "query_runtime.feature_required"},
		{name: "missing identity", selected: []*protocolwire.ProtocolFeature{queryExact}, wantReason: "identity_runtime.feature_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &protocolV2QueryHandshakeTestServer{selected: test.selected}
			config := protocolV2IdentityRuntimeHandshakeConfig(protocolV2IdentityRuntimeManifest(true))
			config.queries = []extensions.ManifestQuery{{Handler: "plugin.identity.runtime.query"}}
			client := newProtocolV2QueryHandshakeTestClient(t, server, config)

			err := client.Handshake(t.Context())
			if test.wantReason == "" {
				if err != nil {
					t.Fatalf("Handshake error = %v", err)
				}
			} else {
				var protocolErr *ProtocolV2Error
				if !errors.As(err, &protocolErr) || protocolErr.Reason != test.wantReason {
					t.Fatalf("Handshake error = %v, want reason %q", err, test.wantReason)
				}
			}

			for _, feature := range []*protocolwire.ProtocolFeature{queryExact, identityExact} {
				count := 0
				for _, offered := range server.offered {
					if offered.GetName() == feature.GetName() && offered.GetVersion() == feature.GetVersion() {
						count++
						if !offered.GetRequired() {
							t.Fatalf("%s@%s offer was not marked required", feature.GetName(), feature.GetVersion())
						}
					}
				}
				if count != 1 {
					t.Fatalf("%s@%s offer count = %d, want 1: %#v", feature.GetName(), feature.GetVersion(), count, server.offered)
				}
			}
		})
	}
}

func TestProtocolV2IdentityRuntimeManifestIsDeepCopied(t *testing.T) {
	manifestIdentity := protocolV2IdentityRuntimeManifest(true)
	extension := extensions.Extension{
		ID: "plugin.identity.copy", Type: extensions.TypePlugin, Version: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), Source: extensions.SourceBuiltin,
		Manifest: extensions.Manifest{Identity: manifestIdentity},
	}
	starter := &ProtocolStarter{}
	config, err := starter.protocolV2ClientConfig(t.Context(), extension, "identity-copy-runtime")
	if err != nil {
		t.Fatal(err)
	}

	manifestIdentity.UserFields[0].ID = "mutated.source.field"
	manifestIdentity.Providers[0].ID = "mutated.source.provider"
	manifestIdentity.Providers[0].Operations[0].Name = "mutated.source.operation"
	manifestIdentity.RiskHooks[0] = "mutated.source.risk"
	assertProtocolV2IdentityRuntimeManifest(t, config.manifestIdentity)

	client := newProtocolV2Client(nil, config)
	config.manifestIdentity.UserFields[0].ID = "mutated.config.field"
	config.manifestIdentity.Providers[0].ID = "mutated.config.provider"
	config.manifestIdentity.Providers[0].Operations[0].Name = "mutated.config.operation"
	config.manifestIdentity.RiskHooks[0] = "mutated.config.risk"
	assertProtocolV2IdentityRuntimeManifest(t, client.manifestIdentity)
}

func protocolV2IdentityRuntimeHandshakeConfig(identity *extensions.ManifestIdentity) protocolV2ClientConfig {
	return protocolV2ClientConfig{
		identity: &protocolwire.ExtensionIdentity{
			ExtensionId: "plugin.identity.runtime", ExtensionVersion: "1.0.0", ArtifactDigest: "digest-v1",
			TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "identity-runtime",
		},
		instance: "identity-runtime", manifestIdentity: identity,
	}
}

func protocolV2IdentityRuntimeManifest(executable bool) *extensions.ManifestIdentity {
	identity := &extensions.ManifestIdentity{
		ContractVersion: "plugin.identity.runtime@1",
		SessionPolicy:   "plugin.identity.runtime.session",
		RiskHooks:       []string{"plugin.identity.runtime.risk"},
		UserFields: []extensionmanifest.ManifestIdentityUserField{{
			ID: "plugin.identity.runtime.field", ContractVersion: "plugin.identity.runtime.field@1",
			Type: "string", Schema: "plugin.identity.runtime.field.schema@1",
		}},
		Providers: []extensions.ManifestIdentityProvider{{
			ID: "plugin.identity.runtime.provider", ContractVersion: "plugin.identity.runtime.provider@1",
			Kind: "risk", Handler: "identity.risk",
		}},
	}
	if executable {
		identity.Providers[0].Operations = []extensions.ManifestIdentityProviderOperation{{
			Name: "risk.evaluate", InputSchema: "plugin.identity.runtime.input@1",
			OutputSchema: "plugin.identity.runtime.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
		}}
	}
	return identity
}

func protocolV2IdentityRuntimeManifestWithLaterExecutableProvider() *extensions.ManifestIdentity {
	identity := protocolV2IdentityRuntimeManifest(false)
	provider := identity.Providers[0]
	provider.ID += ".second"
	provider.Operations = []extensions.ManifestIdentityProviderOperation{{
		Name: "risk.evaluate", InputSchema: "plugin.identity.runtime.input@1",
		OutputSchema: "plugin.identity.runtime.output@1", TimeoutMS: 1000, FailurePolicy: "fail_closed",
	}}
	identity.Providers = append(identity.Providers, provider)
	return identity
}

func assertProtocolV2IdentityRuntimeManifest(t *testing.T, identity *extensions.ManifestIdentity) {
	t.Helper()
	if identity == nil || len(identity.UserFields) != 1 || len(identity.Providers) != 1 ||
		len(identity.Providers[0].Operations) != 1 || len(identity.RiskHooks) != 1 ||
		identity.UserFields[0].ID != "plugin.identity.runtime.field" ||
		identity.Providers[0].ID != "plugin.identity.runtime.provider" ||
		identity.Providers[0].Operations[0].Name != "risk.evaluate" ||
		identity.RiskHooks[0] != "plugin.identity.runtime.risk" {
		t.Fatalf("frozen Manifest Identity = %#v", identity)
	}
}
