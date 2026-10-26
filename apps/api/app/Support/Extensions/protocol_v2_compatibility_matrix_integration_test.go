package extensionsruntime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-plugin"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	p3CompatMatrixHelperEnv = "p3-protocol-compat-matrix"
	p3CompatMatrixInput     = "p3.compat.matrix.hook-input"
	p3CompatMatrixResult    = "p3.compat.matrix.hook-result"
)

func TestProtocolV2AcceptsHistoricalBootstrapABIV1(t *testing.T) {
	starter, extension, _, _ := p3CompatMatrixStart(t, "historical-bootstrap-v1")
	p3CompatMatrixStop(t, starter, extension)
}

func TestP3ProtocolCompatibilityMatrixGoPluginTransportGaps(t *testing.T) {
	t.Run("caller cancellation reaches plugin handler", func(t *testing.T) {
		starter, extension, client, signal := p3CompatMatrixStart(t, "wait")
		ctx, cancel := context.WithCancel(context.Background())
		request := p3CompatMatrixHookRequest(t, client, ctx, map[string]any{"case": "cancel"})
		result := make(chan error, 1)
		go func() {
			_, err := client.client.InvokeHook(ctx, request)
			result <- err
		}()
		p3CompatMatrixWaitForSignal(t, signal, result)
		cancel()
		if code := status.Code(<-result); code != codes.Canceled {
			t.Fatalf("cancelled hook code = %s, want %s", code, codes.Canceled)
		}
		p3CompatMatrixStop(t, starter, extension)
	})

	t.Run("caller deadline reaches plugin handler", func(t *testing.T) {
		starter, extension, client, signal := p3CompatMatrixStart(t, "wait")
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		request := p3CompatMatrixHookRequest(t, client, ctx, map[string]any{"case": "deadline"})
		_, err := client.client.InvokeHook(ctx, request)
		if code := status.Code(err); code != codes.DeadlineExceeded {
			t.Fatalf("deadline hook code = %s, want %s (%v)", code, codes.DeadlineExceeded, err)
		}
		if _, err := os.Stat(signal); err != nil {
			t.Fatalf("deadline did not reach plugin handler: %v", err)
		}
		p3CompatMatrixStop(t, starter, extension)
	})

	t.Run("oversized host request", func(t *testing.T) {
		starter, extension, client, _ := p3CompatMatrixStart(t, "echo")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		request := p3CompatMatrixHookRequest(t, client, ctx, map[string]any{
			"blob": strings.Repeat("x", DefaultProtocolV2MaxMessageBytes+1024),
		})
		_, err := client.client.InvokeHook(ctx, request)
		if code := status.Code(err); code != codes.ResourceExhausted {
			t.Fatalf("oversized request code = %s, want %s (%v)", code, codes.ResourceExhausted, err)
		}
		p3CompatMatrixStop(t, starter, extension)
	})

	t.Run("oversized plugin response", func(t *testing.T) {
		starter, extension, client, _ := p3CompatMatrixStart(t, "oversized-response")
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		request := p3CompatMatrixHookRequest(t, client, ctx, map[string]any{"case": "response"})
		_, err := client.client.InvokeHook(ctx, request)
		if code := status.Code(err); code != codes.ResourceExhausted {
			t.Fatalf("oversized response code = %s, want %s (%v)", code, codes.ResourceExhausted, err)
		}
		p3CompatMatrixStop(t, starter, extension)
	})

	t.Run("handshake selected protocol mismatch", func(t *testing.T) {
		extension, _ := p3CompatMatrixExtension(t, "protocol-mismatch")
		starter := NewProtocolStarter(ProtocolStarterConfig{Trust: p3CompatMatrixTrust{}})
		_, err := starter.Start(context.Background(), extension)
		var protocolError *ProtocolV2Error
		if !errors.As(err, &protocolError) || protocolError.Code != protocolwire.ErrorCode_ERROR_CODE_PROTOCOL_MISMATCH {
			t.Fatalf("handshake mismatch error = %#v", err)
		}
	})
}

func TestP3ProtocolCompatibilityMatrixHostAuthOverGRPC(t *testing.T) {
	token := []byte("01234567890123456789012345678901")
	identity := &protocolwire.ExtensionIdentity{
		ExtensionId: "p3.compat.matrix", ExtensionVersion: "1.0.0", ArtifactDigest: strings.Repeat("c", 64),
		TrustGrantId: "grant-p3", RuntimeEpoch: 17, InstanceId: "instance-p3",
	}
	authority := []*protocolwire.AuthorityGrant{{Key: "permissions.check", ContractVersion: hostAPIV2Version}}
	binding := newProtocolV2HostBinding(protocolV2ClientConfig{identity: identity, authority: authority, token: token})
	server := protocolV2HostGRPCServer(nil, p3CompatMatrixHostRegistrar{}, binding)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := hostwire.NewPermissionServiceClient(connection)

	valid := func() *hostwire.PermissionCheckRequest {
		return &hostwire.PermissionCheckRequest{Context: &protocolwire.RequestContext{
			RequestId: "p3-auth", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
			Extension: proto.Clone(identity).(*protocolwire.ExtensionIdentity),
			GrantedAuthority: []*protocolwire.AuthorityGrant{
				proto.Clone(authority[0]).(*protocolwire.AuthorityGrant),
			},
		}, UserId: 42, PermissionKey: "topic.create"}
	}
	call := func(tokens []string, request *hostwire.PermissionCheckRequest) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if len(tokens) > 0 {
			pairs := make([]string, 0, len(tokens)*2)
			for _, value := range tokens {
				pairs = append(pairs, ProtocolV2RuntimeTokenMetadataKey, value)
			}
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(pairs...))
		}
		_, err := client.Check(ctx, request)
		return err
	}
	if err := call([]string{string(token)}, valid()); err != nil {
		t.Fatalf("valid authenticated call: %v", err)
	}
	tests := []struct {
		name   string
		tokens []string
		mutate func(*hostwire.PermissionCheckRequest)
		code   codes.Code
	}{
		{name: "missing token", code: codes.Unauthenticated},
		{name: "stale token", tokens: []string{"stale-runtime-token"}, code: codes.Unauthenticated},
		{name: "duplicate token", tokens: []string{string(token), "stale-runtime-token"}, code: codes.Unauthenticated},
		{name: "unattested actor", tokens: []string{string(token)}, mutate: func(request *hostwire.PermissionCheckRequest) {
			request.Context.Actor = &protocolwire.Actor{UserId: 42, SessionId: "forged-session", PermissionKeys: []string{"admin"}}
		}, code: codes.PermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			if test.mutate != nil {
				test.mutate(request)
			}
			if code := status.Code(call(test.tokens, request)); code != test.code {
				t.Fatalf("auth code = %s, want %s", code, test.code)
			}
		})
	}
}

func TestP3CompatMatrixHelperProcess(t *testing.T) {
	if os.Getenv("SFORUM_PLUGIN_HELPER") != p3CompatMatrixHelperEnv {
		return
	}
	mode := os.Getenv("SFORUM_P3_COMPAT_MATRIX_MODE")
	server := &p3CompatMatrixPluginServer{mode: mode}
	if mode == "historical-bootstrap-v1" {
		// This helper intentionally does not import the current bootstrap contract.
		// It models an already-installed V3/P2 binary built by the historical SDK.
		plugin.Serve(&plugin.ServeConfig{
			HandshakeConfig: plugin.HandshakeConfig{
				ProtocolVersion:  1,
				MagicCookieKey:   "SFORUM_PLUGIN",
				MagicCookieValue: "sforum-plugin-v1",
			},
			VersionedPlugins: map[int]plugin.PluginSet{
				2: {"sforum-plugin-v2": &protocolV2Plugin{server: server}},
			},
			GRPCServer: protocolV2GRPCServerFactory(normalizeProtocolV2ServerConfig(ProtocolV2ServerConfig{})),
		})
		os.Exit(0)
	}
	ServeProtocolV2Plugin(server, ProtocolV2ServerConfig{})
	os.Exit(0)
}

type p3CompatMatrixPluginServer struct {
	pluginwire.UnimplementedPluginRuntimeServiceServer

	mu       sync.RWMutex
	identity *protocolwire.ExtensionIdentity
	mode     string
}

func (s *p3CompatMatrixPluginServer) Handshake(_ context.Context, request *protocolwire.HandshakeRequest) (*protocolwire.HandshakeResponse, error) {
	identity := proto.Clone(request.GetContext().GetExtension()).(*protocolwire.ExtensionIdentity)
	s.mu.Lock()
	s.identity = identity
	s.mu.Unlock()
	major := uint32(2)
	if s.mode == "protocol-mismatch" {
		major = 3
	}
	return &protocolwire.HandshakeResponse{
		Context:          &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: proto.Clone(identity).(*protocolwire.ExtensionIdentity)},
		SelectedProtocol: &protocolwire.ProtocolRange{Protocol: protocolV2Name, Major: major, MinMinor: 0, MaxMinor: 0},
		TokenExpiresAt:   timestamppb.New(time.Now().Add(time.Minute)),
	}, nil
}

func (s *p3CompatMatrixPluginServer) Health(_ context.Context, request *protocolwire.HealthRequest) (*protocolwire.HealthResponse, error) {
	return &protocolwire.HealthResponse{
		Context: &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: s.runtimeIdentity()},
		Healthy: true, Status: "healthy",
	}, nil
}

func (s *p3CompatMatrixPluginServer) Readiness(_ context.Context, request *protocolwire.ReadinessRequest) (*protocolwire.ReadinessResponse, error) {
	return &protocolwire.ReadinessResponse{
		Context: &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: s.runtimeIdentity()}, Ready: true,
	}, nil
}

func (s *p3CompatMatrixPluginServer) InvokeHook(ctx context.Context, request *pluginwire.HookRequest) (*pluginwire.HookResponse, error) {
	if s.mode == "wait" {
		if signal := os.Getenv("SFORUM_P3_COMPAT_MATRIX_SIGNAL"); signal != "" {
			if err := os.WriteFile(signal, []byte("entered"), 0o600); err != nil {
				return nil, err
			}
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	values := map[string]any{"reason": "", "message": ""}
	if s.mode == "oversized-response" {
		values["blob"] = strings.Repeat("x", DefaultProtocolV2MaxMessageBytes+1024)
	}
	result, err := structpb.NewStruct(values)
	if err != nil {
		return nil, err
	}
	return &pluginwire.HookResponse{
		Context:  &protocolwire.ResponseContext{RequestId: request.GetContext().GetRequestId(), Extension: s.runtimeIdentity()},
		Accepted: true, Result: &protocolwire.TypedDocument{SchemaId: p3CompatMatrixResult, SchemaVersion: "1", Value: result},
	}, nil
}

func (s *p3CompatMatrixPluginServer) runtimeIdentity() *protocolwire.ExtensionIdentity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.identity == nil {
		return nil
	}
	return proto.Clone(s.identity).(*protocolwire.ExtensionIdentity)
}

type p3CompatMatrixHostRegistrar struct{}

func (p3CompatMatrixHostRegistrar) RegisterProtocolV2(registrar grpc.ServiceRegistrar) {
	hostwire.RegisterPermissionServiceServer(registrar, p3CompatMatrixPermissionServer{})
}

type p3CompatMatrixPermissionServer struct {
	hostwire.UnimplementedPermissionServiceServer
}

func (p3CompatMatrixPermissionServer) Check(context.Context, *hostwire.PermissionCheckRequest) (*hostwire.PermissionCheckResponse, error) {
	return &hostwire.PermissionCheckResponse{Allowed: true}, nil
}

type p3CompatMatrixTrust struct{}

func (p3CompatMatrixTrust) RuntimeIdentity(context.Context, extensions.Extension) (extensions.RuntimeTrustIdentity, error) {
	return extensions.RuntimeTrustIdentity{TrustGrantID: "grant-p3", ImpactDigest: "impact-p3"}, nil
}

func p3CompatMatrixStart(t *testing.T, mode string) (*ProtocolStarter, extensions.Extension, *protocolV2Client, string) {
	t.Helper()
	extension, signal := p3CompatMatrixExtension(t, mode)
	starter := NewProtocolStarter(ProtocolStarterConfig{Trust: p3CompatMatrixTrust{}})
	// Start uses exec.CommandContext for the whole subprocess lifetime; its
	// context must remain live until Stop.
	if _, err := starter.Start(context.Background(), extension); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	t.Cleanup(func() { _ = starter.Stop(context.Background(), extension) })
	client, ok := starter.protocolFor(extension.ID).(*protocolV2Client)
	if !ok {
		t.Fatalf("%s helper did not negotiate protocol v2", mode)
	}
	return starter, extension, client, signal
}

func p3CompatMatrixStop(t *testing.T, starter *ProtocolStarter, extension extensions.Extension) {
	t.Helper()
	if err := starter.Stop(context.Background(), extension); err != nil {
		t.Fatalf("stop compatibility helper: %v", err)
	}
}

func p3CompatMatrixExtension(t *testing.T, mode string) (extensions.Extension, string) {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "p3-compat-matrix")
	backendRoot := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(backendRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	signal := filepath.Join(packageRoot, "handler-entered")
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\n" +
		"SFORUM_PLUGIN_HELPER=" + p3CompatMatrixShellQuote(p3CompatMatrixHelperEnv) + " " +
		"SFORUM_P3_COMPAT_MATRIX_MODE=" + p3CompatMatrixShellQuote(mode) + " " +
		"SFORUM_P3_COMPAT_MATRIX_SIGNAL=" + p3CompatMatrixShellQuote(signal) + " " +
		"exec " + p3CompatMatrixShellQuote(testBinary) + " -test.run='^TestP3CompatMatrixHelperProcess$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(backendRoot, "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	id := "p3.compat.matrix." + strings.ReplaceAll(mode, "_", "-")
	return extensions.Extension{
		ID: id, Name: "P3 Compatibility Matrix", Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, Source: extensions.SourceUploaded,
		PackageDigest: strings.Repeat("c", 64), PackagePath: packageRoot,
		Manifest: extensions.Manifest{
			ManifestVersion: 3, ID: id, Name: "P3 Compatibility Matrix", Version: "1.0.0", Type: extensions.TypePlugin,
			Backend: extensions.ManifestBackend{Entry: "backend/plugin", RPC: "hashicorp-go-plugin", ProtocolVersion: 2, HostAPIVersion: hostAPIV2Contract},
			Events: []extensions.ManifestEvent{{
				ID: "p3.compat.matrix.event", ContractVersion: "p3.compat.matrix.event@1", Name: "p3.compat.matrix", Kind: "filter",
				Handler: "p3.compat.matrix", InputSchema: p3CompatMatrixInput + "@1", ResultSchema: p3CompatMatrixResult + "@1",
			}},
		},
	}, signal
}

func p3CompatMatrixHookRequest(t *testing.T, client *protocolV2Client, ctx context.Context, values map[string]any) *pluginwire.HookRequest {
	t.Helper()
	payload, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return &pluginwire.HookRequest{
		Context: client.requestContext(ctx, "p3-compat-matrix"), HookId: "p3.compat.matrix.event",
		HookName: "p3.compat.matrix", HookKind: "filter", ContractVersion: "p3.compat.matrix.event@1",
		Payload: &protocolwire.TypedDocument{SchemaId: p3CompatMatrixInput, SchemaVersion: "1", Value: payload},
	}
}

func p3CompatMatrixWaitForSignal(t *testing.T, path string, result <-chan error) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("plugin call ended before handler entry: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("plugin handler did not receive the request")
}

func p3CompatMatrixShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
