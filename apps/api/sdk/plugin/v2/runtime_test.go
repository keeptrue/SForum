package pluginv2

import (
	"bytes"
	"context"
	"testing"
	"time"

	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestServerHandshakeBindsTokenAndExactIdentity(t *testing.T) {
	now := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	server := NewServer().WithFeatures(&protocolwire.ProtocolFeature{Name: "stream.routes", Version: "1"})
	server.now = func() time.Time { return now }
	request := validHandshakeRequest()

	response, err := server.Handshake(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetError() != nil || response.GetSelectedProtocol().GetMajor() != ProtocolMajor || len(response.GetSelectedFeatures()) != 1 {
		t.Fatalf("unexpected handshake response: %#v", response)
	}
	if !response.GetTokenExpiresAt().AsTime().Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("unexpected token expiry: %s", response.GetTokenExpiresAt().AsTime())
	}

	repeated, err := server.Handshake(context.Background(), request)
	if err != nil || repeated.GetError() != nil {
		t.Fatalf("same handshake must be idempotent: %#v, %v", repeated, err)
	}
	changed := validHandshakeRequest()
	changed.RuntimeToken = bytes.Repeat([]byte{0x44}, 32)
	stale, err := server.Handshake(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if stale.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_STALE_RUNTIME {
		t.Fatalf("changed token must be stale: %#v", stale)
	}
	changed = validHandshakeRequest()
	changed.HostBrokerId = 9
	stale, err = server.Handshake(context.Background(), changed)
	if err != nil || stale.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_STALE_RUNTIME {
		t.Fatalf("changed broker must be stale: %#v, %v", stale, err)
	}
}

func TestServerFreezesHostBuildIdentityFromHandshake(t *testing.T) {
	server := NewServer()
	if _, ok := server.HostBuild(); ok {
		t.Fatal("host build must be unknown before the handshake")
	}
	request := validHandshakeRequest()
	request.HostBuild = &protocolwire.HostBuild{CoreVersion: "3.0.16", BuildVersion: "dev-abc12"}
	if _, err := server.Handshake(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	build, ok := server.HostBuild()
	if !ok || build.CoreVersion != "3.0.16" || build.BuildVersion != "dev-abc12" {
		t.Fatalf("host build = %#v, ok=%v", build, ok)
	}

	drifted := validHandshakeRequest()
	drifted.HostBuild = &protocolwire.HostBuild{CoreVersion: "3.0.17"}
	response, err := server.Handshake(context.Background(), drifted)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_STALE_RUNTIME {
		t.Fatalf("drifted host build must be stale: %#v", response.GetError())
	}

	// A Host that predates the field sends nothing; plugins must see unknown
	// rather than an empty-but-usable identity.
	legacy := NewServer()
	if _, err := legacy.Handshake(context.Background(), validHandshakeRequest()); err != nil {
		t.Fatal(err)
	}
	if build, ok := legacy.HostBuild(); ok {
		t.Fatalf("legacy host build must be unknown: %#v", build)
	}
}

func TestServerRejectsIncompleteAndMismatchedHandshake(t *testing.T) {
	tests := []struct {
		name   string
		change func(*protocolwire.HandshakeRequest)
		code   protocolwire.ErrorCode
	}{
		{"missing grant", func(request *protocolwire.HandshakeRequest) { request.Context.Extension.TrustGrantId = "" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT},
		{"short token", func(request *protocolwire.HandshakeRequest) { request.RuntimeToken = []byte("short") }, protocolwire.ErrorCode_ERROR_CODE_UNAUTHENTICATED},
		{"host api", func(request *protocolwire.HandshakeRequest) { request.HostApiVersion = "sforum.host/v3" }, protocolwire.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH},
		{"protocol", func(request *protocolwire.HandshakeRequest) { request.HostProtocols[0].Major = 3 }, protocolwire.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validHandshakeRequest()
			test.change(request)
			response, err := NewServer().Handshake(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if response.GetError().GetCode() != test.code {
				t.Fatalf("error = %#v, want %s", response.GetError(), test.code)
			}
		})
	}
}

func TestServerHealthAndReadinessRequireCurrentIdentity(t *testing.T) {
	server := NewServer()
	request := validHandshakeRequest()
	if _, err := server.Handshake(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	ctx := request.Context
	health, err := server.Health(context.Background(), &protocolwire.HealthRequest{Context: ctx})
	if err != nil || !health.GetHealthy() || health.GetError() != nil {
		t.Fatalf("health: %#v, %v", health, err)
	}
	ready, err := server.Readiness(context.Background(), &protocolwire.ReadinessRequest{Context: ctx})
	if err != nil || !ready.GetReady() || ready.GetError() != nil {
		t.Fatalf("readiness: %#v, %v", ready, err)
	}

	staleContext := validHandshakeRequest().Context
	staleContext.Extension.RuntimeEpoch++
	stale, err := server.Health(context.Background(), &protocolwire.HealthRequest{Context: staleContext})
	if err != nil {
		t.Fatal(err)
	}
	if stale.GetHealthy() || stale.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_STALE_RUNTIME {
		t.Fatalf("stale health: %#v", stale)
	}
}

func validHandshakeRequest() *protocolwire.HandshakeRequest {
	return &protocolwire.HandshakeRequest{
		Context: &protocolwire.RequestContext{
			RequestId: "request-1",
			Deadline:  timestamppb.New(time.Now().Add(time.Minute)),
			Extension: &protocolwire.ExtensionIdentity{
				ExtensionId: "demo.v2", ExtensionVersion: "1.0.0",
				ArtifactDigest: "sha256:artifact", TrustGrantId: "41",
				RuntimeEpoch: 7, InstanceId: "instance-1",
			},
		},
		HostProtocols:  []*protocolwire.ProtocolRange{{Protocol: protocolName, Major: ProtocolMajor, MinMinor: 0, MaxMinor: 0}},
		HostFeatures:   []*protocolwire.ProtocolFeature{{Name: "stream.routes", Version: "1"}},
		Limits:         &protocolwire.RuntimeLimits{MaxReceiveBytes: 4 << 20, MaxSendBytes: 4 << 20, MaxConcurrentUnaryCalls: 16, MaxConcurrentStreams: 16},
		HostApiVersion: HostAPIVersion,
		RuntimeToken:   bytes.Repeat([]byte{0x2a}, 32),
	}
}
