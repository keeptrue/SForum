package bootstrap

import (
	"context"
	"errors"
	"testing"

	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type consentAuthorityResolverStub struct {
	authority hostapi.ProtocolV2QueryAuthority
	err       error
}

func (s consentAuthorityResolverStub) ResolveProtocolV2QueryAuthority(
	context.Context,
	*protocolv2.ExtensionIdentity,
) (hostapi.ProtocolV2QueryAuthority, error) {
	return s.authority, s.err
}

func TestProductionQueryCapabilityAuthorityAuthorizesOnlyExactDeclaredCapabilities(t *testing.T) {
	identity := &protocolv2.ExtensionIdentity{
		ExtensionId: "consent.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "digest",
		TrustGrantId: "7", RuntimeEpoch: 1, InstanceId: "instance",
	}
	for _, test := range []struct {
		name     string
		resolver consentAuthorityResolverStub
		want     error
	}{
		{
			name: "declared capability",
			resolver: consentAuthorityResolverStub{authority: hostapi.ProtocolV2QueryAuthority{
				ExactArtifact: true, CoreViews: true, Capabilities: []string{"host.api", "users.email.read"},
			}},
		},
		{
			name: "capability not declared",
			resolver: consentAuthorityResolverStub{authority: hostapi.ProtocolV2QueryAuthority{
				ExactArtifact: true, CoreViews: true, Capabilities: []string{"host.api"},
			}},
			want: hostapi.ErrProtocolV2QueryCapabilityDenied,
		},
		{
			name: "artifact no longer exact",
			resolver: consentAuthorityResolverStub{authority: hostapi.ProtocolV2QueryAuthority{
				Capabilities: []string{"users.email.read"},
			}},
			want: hostapi.ErrProtocolV2QueryRuntimeStale,
		},
		{
			name:     "resolver stale",
			resolver: consentAuthorityResolverStub{err: hostapi.ErrProtocolV2QueryRuntimeStale},
			want:     hostapi.ErrProtocolV2QueryRuntimeStale,
		},
		{
			name:     "resolver unavailable",
			resolver: consentAuthorityResolverStub{err: errors.New("database down")},
			want:     hostapi.ErrProtocolV2QueryConsentGateUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := &productionQueryCapabilityAuthority{resolver: test.resolver}
			err := authority.AuthorizeProtocolV2QueryCapability(context.Background(), identity, "users.email.read")
			if test.want == nil {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	// 空能力 / 空 resolver 一律 fail closed。
	if err := (&productionQueryCapabilityAuthority{}).AuthorizeProtocolV2QueryCapability(
		context.Background(), identity, "users.email.read",
	); !errors.Is(err, hostapi.ErrProtocolV2QueryCapabilityDenied) {
		t.Fatalf("nil resolver error = %v", err)
	}
}

func TestProductionQueryConsentGateComposition(t *testing.T) {
	if newProductionQueryConsentLimiter(nil) != nil {
		t.Fatal("missing Redis must stay an unconfigured limiter, not a silent allow-all object")
	}
	gate := newProductionQueryConsentGate(consentAuthorityResolverStub{authority: hostapi.ProtocolV2QueryAuthority{
		ExactArtifact: true, Capabilities: []string{"users.email.read"},
	}}, nil, nil)
	if gate.Capabilities == nil || gate.Limiter != nil || gate.Auditor != nil {
		t.Fatalf("gate = %#v", gate)
	}
	identity := &protocolv2.ExtensionIdentity{ExtensionId: "consent.plugin"}
	if err := gate.Authorize(context.Background(), identity, 42, hostapi.QuerySelfEmailRegistryID); err != nil {
		t.Fatalf("authorize = %v", err)
	}
	// 没有审计写入器时必须 fail closed：不返回无法追溯的同意数据。
	if err := gate.Record(context.Background(), hostapi.ProtocolV2ConsentQueryEvent{ActorUserID: 42}); !errors.Is(
		err, hostapi.ErrProtocolV2QueryConsentGateUnavailable,
	) {
		t.Fatalf("record without auditor = %v", err)
	}
}
