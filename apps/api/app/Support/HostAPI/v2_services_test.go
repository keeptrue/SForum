package hostapi

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type v2ServiceProvider struct {
	invokeCalls   int
	invokeContext context.Context
	context       *protocolv2.RequestContext
	serviceID     string
	version       string
	operation     string
	input         *protocolv2.TypedDocument
	output        *protocolv2.TypedDocument
	remoteError   *protocolv2.ErrorDetail
	transport     error

	streamMessages  []*protocolv2.TypedDocument
	streamSawEOF    bool
	streamFinal     *protocolv2.TypedDocument
	streamError     *protocolv2.ErrorDetail
	streamTransport error
	streamContext   context.Context
	adapterContext  context.Context
}

func (p *v2ServiceProvider) Invoke(ctx context.Context, requestContext *protocolv2.RequestContext, serviceID, version, operation string, input *protocolv2.TypedDocument) (*protocolv2.TypedDocument, *protocolv2.ErrorDetail, error) {
	p.invokeCalls++
	p.invokeContext = ctx
	p.context = requestContext
	p.serviceID = serviceID
	p.version = version
	p.operation = operation
	p.input = input
	return p.output, p.remoteError, p.transport
}

func (p *v2ServiceProvider) Stream(ctx context.Context, requestContext *protocolv2.RequestContext, serviceID, version, operation string, stream ServiceBidiStream) (*protocolv2.ErrorDetail, error) {
	p.streamContext = ctx
	p.adapterContext = stream.Context()
	p.context = requestContext
	p.serviceID = serviceID
	p.version = version
	p.operation = operation
	if p.streamTransport != nil || p.streamError != nil {
		return p.streamError, p.streamTransport
	}
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			p.streamSawEOF = true
			if p.streamFinal != nil {
				if err := stream.Send(p.streamFinal); err != nil {
					return nil, err
				}
			}
			return nil, stream.CloseSend()
		}
		if err != nil {
			return nil, err
		}
		p.streamMessages = append(p.streamMessages, message)
		if err := stream.Send(v2ServiceDocument(serviceID+".response", "1")); err != nil {
			return nil, err
		}
	}
}

func TestGatewaySharesProtocolV2ServiceRegistry(t *testing.T) {
	gateway := NewGateway(nil)
	provider := &v2ServiceProvider{}
	registration := v2ServiceRegistration("demo.plugin", "instance-1", "demo.lookup", "1.0.0", provider)
	if err := gateway.ReplaceProtocolV2Services("demo.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	registry := gateway.ProtocolV2ServiceRegistry()
	if registry == nil || registry.Revision() != 1 {
		t.Fatalf("registry = %#v revision=%d", registry, registry.Revision())
	}
	if _, err := registry.Resolve("demo.lookup", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if _, ok := server.GetServiceInfo()["sforum.host.v2.ServiceDiscoveryService"]; !ok {
		t.Fatal("ServiceDiscoveryService was not registered")
	}

	gateway.UnregisterExtension("demo.plugin")
	if _, err := registry.Resolve("demo.lookup", ""); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("legacy unregister left v2 service active: %v", err)
	}
	if err := gateway.ReplaceProtocolV2Services("demo.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	gateway.UnregisterProtocolV2Services("demo.plugin")
	if _, err := registry.Resolve("demo.lookup", ""); !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("v2 unregister left service active: %v", err)
	}
}

func TestProtocolV2ServiceListResolvePaginationAndAuthority(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{}
	alpha := v2ServiceRegistration("catalog.plugin", "instance-1", "catalog.alpha", "1.0.0", provider)
	beta := v2ServiceRegistration("catalog.plugin", "instance-1", "catalog.beta", "2.0.0", provider)
	hidden := v2ServiceRegistration("catalog.plugin", "instance-1", "catalog.hidden", "1.0.0", provider)
	hidden.Descriptor.RequiredAuthority = []string{"service.secret"}
	if err := registry.ReplaceExtension("catalog.plugin", []ServiceRegistration{beta, hidden, alpha}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-caller", "service.call")

	first, err := server.List(context.Background(), &hostv2.ServiceListRequest{
		Context: requestContext, Page: &protocolv2.PageRequest{Limit: 1},
	})
	if err != nil || first.GetError() != nil {
		t.Fatalf("first page error=%v detail=%#v", err, first.GetError())
	}
	if len(first.GetServices()) != 1 || first.GetServices()[0].GetServiceId() != "catalog.alpha" || !first.GetPage().GetHasMore() || first.GetPage().GetNextCursor() == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := server.List(context.Background(), &hostv2.ServiceListRequest{
		Context: requestContext, Page: &protocolv2.PageRequest{Limit: 1, Cursor: first.GetPage().GetNextCursor()},
	})
	if err != nil || second.GetError() != nil || len(second.GetServices()) != 1 || second.GetServices()[0].GetServiceId() != "catalog.beta" || second.GetPage().GetHasMore() {
		t.Fatalf("unexpected second page: %#v err=%v", second, err)
	}

	resolved, err := server.Resolve(context.Background(), &hostv2.ServiceResolveRequest{
		Context: requestContext, ServiceId: "catalog.beta", VersionConstraint: "^2.0.0",
	})
	if err != nil || resolved.GetError() != nil || resolved.GetProviderExtensionId() != "catalog.plugin" || resolved.GetRegistryRevision() != 1 || resolved.GetService().GetVersion() != "2.0.0" {
		t.Fatalf("unexpected resolution: %#v err=%v", resolved, err)
	}
	denied, _ := server.Resolve(context.Background(), &hostv2.ServiceResolveRequest{
		Context: requestContext, ServiceId: "catalog.hidden", VersionConstraint: "1.0.0",
	})
	if denied.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED || denied.GetError().GetMetadata()["missingAuthority"] != "service.secret" {
		t.Fatalf("authority denial = %#v", denied.GetError())
	}
	invalid, _ := server.List(context.Background(), &hostv2.ServiceListRequest{
		Context: requestContext, VersionConstraint: "^1",
	})
	if invalid.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Fatalf("loose constraint error = %#v", invalid.GetError())
	}

	if err := registry.ReplaceExtension("other.plugin", []ServiceRegistration{
		v2ServiceRegistration("other.plugin", "instance-2", "catalog.gamma", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	stale, _ := server.List(context.Background(), &hostv2.ServiceListRequest{
		Context: requestContext, Page: &protocolv2.PageRequest{Limit: 1, Cursor: first.GetPage().GetNextCursor()},
	})
	if stale.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION || stale.GetError().GetReason() != "host.service_cursor_stale" {
		t.Fatalf("stale cursor error = %#v", stale.GetError())
	}
}

func TestProtocolV2ServiceInvokeValidatesContractAndPropagatesContext(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.2.3", provider)
	registration.Descriptor.RequiredAuthority = []string{"service.call"}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer", "service.call")
	requestContext.Locale = "zh-CN"
	requestContext.Trace = &protocolv2.TraceContext{TraceId: "trace-1", SpanId: "span-1"}
	request := &hostv2.ServiceInvokeRequest{
		Context: requestContext, ServiceId: "demo.lookup", Version: "1.2.3", Operation: "find",
		Input: v2ServiceDocument("demo.lookup.request", "1"),
	}
	response, err := server.Invoke(context.Background(), request)
	if err != nil || response.GetError() != nil || response.GetOutput().GetSchemaId() != "demo.lookup.response" {
		t.Fatalf("invoke response=%#v err=%v", response, err)
	}
	if provider.invokeCalls != 1 || provider.serviceID != "demo.lookup" || provider.version != "1.2.3" || provider.operation != "find" || !proto.Equal(provider.context, requestContext) {
		t.Fatalf("provider call was not preserved: %#v", provider)
	}
	provider.context.Locale = "mutated"
	provider.input.SchemaId = "mutated.input"
	if requestContext.GetLocale() != "zh-CN" || request.GetInput().GetSchemaId() != "demo.lookup.request" {
		t.Fatal("provider mutated caller-owned request data")
	}

	badInput := proto.Clone(request).(*hostv2.ServiceInvokeRequest)
	badInput.Input.SchemaVersion = "2"
	badResponse, _ := server.Invoke(context.Background(), badInput)
	if badResponse.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT || provider.invokeCalls != 1 {
		t.Fatalf("bad input was invoked: detail=%#v calls=%d", badResponse.GetError(), provider.invokeCalls)
	}
	denied := proto.Clone(request).(*hostv2.ServiceInvokeRequest)
	denied.Context.GrantedAuthority = nil
	deniedResponse, _ := server.Invoke(context.Background(), denied)
	if deniedResponse.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED || provider.invokeCalls != 1 {
		t.Fatalf("denied input was invoked: detail=%#v calls=%d", deniedResponse.GetError(), provider.invokeCalls)
	}
}

func TestProtocolV2ServiceInvokePreservesTypedErrorsAndMapsProviderFailures(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	request := &hostv2.ServiceInvokeRequest{
		Context:   v2ServiceRequestContext("consumer.plugin", "instance-consumer"),
		ServiceId: "demo.lookup", Version: "1.0.0", Operation: "find",
		Input: v2ServiceDocument("demo.lookup.request", "1"),
	}

	typed := &protocolv2.ErrorDetail{
		Code: protocolv2.ErrorCode_ERROR_CODE_CONFLICT, Reason: "demo.conflict", Message: "Already exists.",
		Metadata: map[string]string{"record": "42"},
	}
	provider.remoteError = typed
	typedResponse, _ := server.Invoke(context.Background(), request)
	if !proto.Equal(typedResponse.GetError(), typed) {
		t.Fatalf("typed error changed: %#v", typedResponse.GetError())
	}
	typed.Metadata["record"] = "mutated"
	if typedResponse.GetError().GetMetadata()["record"] != "42" {
		t.Fatal("response retained provider-owned typed error")
	}

	provider.remoteError = nil
	provider.transport = io.ErrUnexpectedEOF
	transportResponse, _ := server.Invoke(context.Background(), request)
	if transportResponse.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE || !transportResponse.GetError().GetRetryable() {
		t.Fatalf("transport error = %#v", transportResponse.GetError())
	}

	provider.transport = nil
	provider.output = v2ServiceDocument("wrong.response", "1")
	contractResponse, _ := server.Invoke(context.Background(), request)
	if contractResponse.GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION || contractResponse.GetError().GetReason() != "host.service_output_schema_mismatch" {
		t.Fatalf("output contract error = %#v", contractResponse.GetError())
	}
}

func TestProtocolV2ServiceReplacementPublishesTargetButInvokesContribution(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("provider.lookup.response", "1")}
	replacement := v2ServiceRegistration("provider.plugin", "instance-provider", "provider.lookup", "1.0.0", provider)
	replacement.Action = ServiceActionReplace
	replacement.TargetID = "shared.lookup"
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{replacement}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")
	resolved, _ := server.Resolve(context.Background(), &hostv2.ServiceResolveRequest{
		Context: requestContext, ServiceId: "shared.lookup", VersionConstraint: "1.0.0",
	})
	if resolved.GetError() != nil || resolved.GetService().GetServiceId() != "shared.lookup" {
		t.Fatalf("published replacement = %#v", resolved)
	}
	invoked, _ := server.Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
		Context: requestContext, ServiceId: "shared.lookup", Version: "1.0.0", Operation: "find",
		Input: v2ServiceDocument("provider.lookup.request", "1"),
	})
	if invoked.GetError() != nil || provider.serviceID != "provider.lookup" {
		t.Fatalf("replacement invocation response=%#v providerService=%q", invoked, provider.serviceID)
	}
}

func TestProtocolV2ServiceStreamForwardsMessagesAndPreservesHalfClose(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{streamFinal: v2ServiceDocument("demo.stream.response", "1")}
	registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
	registration.Descriptor.ClientStreaming = true
	registration.Descriptor.ServerStreaming = true
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")
	stream := &fakeV2HostServiceStream{
		ctx: context.Background(),
		recv: []*hostv2.ServiceStreamFrame{
			v2ServiceOpenFrame(requestContext, "demo.stream", "1.0.0", "chat"),
			v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
			v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
		},
	}
	if err := server.Stream(stream); err != nil {
		t.Fatal(err)
	}
	if !provider.streamSawEOF || len(provider.streamMessages) != 2 || len(stream.sent) != 3 {
		t.Fatalf("half-close forwarding provider=%#v sent=%#v", provider, stream.sent)
	}
	for _, frame := range stream.sent {
		if frame.GetMessage().GetSchemaId() != "demo.stream.response" {
			t.Fatalf("unexpected output frame: %#v", frame)
		}
	}
	if !proto.Equal(provider.context, requestContext) || provider.operation != "chat" {
		t.Fatalf("stream context/operation changed: %#v", provider)
	}
}

func TestProtocolV2ServiceStreamRejectsFrameAndSchemaViolations(t *testing.T) {
	newServer := func() (*protocolV2ServiceDiscoveryServer, *v2ServiceProvider) {
		registry := NewServiceRegistry()
		provider := &v2ServiceProvider{}
		registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
		registration.Descriptor.ClientStreaming = true
		if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
			t.Fatal(err)
		}
		return newProtocolV2ServiceTestServer(registry, nil), provider
	}
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")
	tests := []struct {
		name   string
		frames []*hostv2.ServiceStreamFrame
		reason string
	}{
		{
			name:   "missing open",
			frames: []*hostv2.ServiceStreamFrame{v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1"))},
			reason: "host.service_stream_open_required",
		},
		{
			name: "second open",
			frames: []*hostv2.ServiceStreamFrame{
				v2ServiceOpenFrame(requestContext, "demo.stream", "1.0.0", "chat"),
				v2ServiceOpenFrame(requestContext, "demo.stream", "1.0.0", "chat"),
			},
			reason: "host.service_stream_frame_invalid",
		},
		{
			name: "wrong schema",
			frames: []*hostv2.ServiceStreamFrame{
				v2ServiceOpenFrame(requestContext, "demo.stream", "1.0.0", "chat"),
				v2ServiceMessageFrame(v2ServiceDocument("wrong.request", "1")),
			},
			reason: "host.service_input_schema_mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newServer()
			stream := &fakeV2HostServiceStream{ctx: context.Background(), recv: test.frames}
			if err := server.Stream(stream); err != nil {
				t.Fatal(err)
			}
			if len(stream.sent) != 1 || stream.sent[0].GetError().GetReason() != test.reason {
				t.Fatalf("sent frames = %#v", stream.sent)
			}
		})
	}
}

func TestProtocolV2ServiceStreamMapsTransportFailureAndCancellation(t *testing.T) {
	build := func(provider *v2ServiceProvider, ctx context.Context) (*protocolV2ServiceDiscoveryServer, *fakeV2HostServiceStream) {
		registry := NewServiceRegistry()
		registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
		registration.Descriptor.ServerStreaming = true
		if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
			t.Fatal(err)
		}
		server := newProtocolV2ServiceTestServer(registry, nil)
		stream := &fakeV2HostServiceStream{
			ctx: ctx,
			recv: []*hostv2.ServiceStreamFrame{
				v2ServiceOpenFrame(v2ServiceRequestContext("consumer.plugin", "instance-consumer"), "demo.stream", "1.0.0", "watch"),
				v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
			},
		}
		return server, stream
	}

	transportProvider := &v2ServiceProvider{streamTransport: io.ErrUnexpectedEOF}
	server, stream := build(transportProvider, context.Background())
	if err := server.Stream(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetError().GetCode() != protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE {
		t.Fatalf("transport stream response = %#v", stream.sent)
	}

	cancelProvider := &v2ServiceProvider{streamTransport: context.Canceled}
	server, stream = build(cancelProvider, context.Background())
	if code := status.Code(server.Stream(stream)); code != codes.Canceled {
		t.Fatalf("cancel status = %s", code)
	}
}

func v2ServiceRegistration(extensionID, instanceID, serviceID, version string, provider ServiceProvider) ServiceRegistration {
	return ServiceRegistration{
		ExtensionID: extensionID, InstanceID: instanceID, Action: ServiceActionAdd,
		Descriptor: &protocolv2.ServiceDescriptor{
			ServiceId: serviceID, Version: version,
			RequestSchemaId: serviceID + ".request@1", ResponseSchemaId: serviceID + ".response@1",
		},
		Provider: provider,
	}
}

func v2ServiceRequestContext(extensionID, instanceID string, authority ...string) *protocolv2.RequestContext {
	grants := make([]*protocolv2.AuthorityGrant, 0, len(authority))
	for _, key := range authority {
		grants = append(grants, &protocolv2.AuthorityGrant{Key: key, ContractVersion: VersionV2})
	}
	return &protocolv2.RequestContext{
		RequestId: "request-1", Locale: "und", Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		Extension:        &protocolv2.ExtensionIdentity{ExtensionId: extensionID, InstanceId: instanceID},
		GrantedAuthority: grants,
	}
}

func v2ServiceDocument(schemaID, schemaVersion string) *protocolv2.TypedDocument {
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: schemaVersion}
}

func v2ServiceOpenFrame(requestContext *protocolv2.RequestContext, serviceID, version, operation string) *hostv2.ServiceStreamFrame {
	return &hostv2.ServiceStreamFrame{Frame: &hostv2.ServiceStreamFrame_Open{Open: &hostv2.ServiceStreamOpen{
		Context: requestContext, ServiceId: serviceID, Version: version, Operation: operation,
	}}}
}

func v2ServiceMessageFrame(document *protocolv2.TypedDocument) *hostv2.ServiceStreamFrame {
	return &hostv2.ServiceStreamFrame{Frame: &hostv2.ServiceStreamFrame_Message{Message: document}}
}

type fakeV2HostServiceStream struct {
	ctx  context.Context
	recv []*hostv2.ServiceStreamFrame
	sent []*hostv2.ServiceStreamFrame
}

func (s *fakeV2HostServiceStream) Send(frame *hostv2.ServiceStreamFrame) error {
	s.sent = append(s.sent, proto.Clone(frame).(*hostv2.ServiceStreamFrame))
	return nil
}

func (s *fakeV2HostServiceStream) Recv() (*hostv2.ServiceStreamFrame, error) {
	if len(s.recv) == 0 {
		return nil, io.EOF
	}
	frame := proto.Clone(s.recv[0]).(*hostv2.ServiceStreamFrame)
	s.recv = s.recv[1:]
	return frame, nil
}

func (s *fakeV2HostServiceStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeV2HostServiceStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeV2HostServiceStream) SetTrailer(metadata.MD)       {}
func (s *fakeV2HostServiceStream) Context() context.Context     { return s.ctx }
func (s *fakeV2HostServiceStream) SendMsg(message any) error {
	frame, ok := message.(*hostv2.ServiceStreamFrame)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	return s.Send(frame)
}
func (s *fakeV2HostServiceStream) RecvMsg(message any) error {
	frame, err := s.Recv()
	if err != nil {
		return err
	}
	target, ok := message.(*hostv2.ServiceStreamFrame)
	if !ok {
		return io.ErrUnexpectedEOF
	}
	proto.Reset(target)
	proto.Merge(target, frame)
	return nil
}

var _ grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame] = (*fakeV2HostServiceStream)(nil)

func TestProtocolV2ServiceDiscoveryRequiresExtensionsCall(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")

	// 缺少进程能力：List / Resolve / Invoke 一律拒绝。
	deniedServer := newProtocolV2ServiceTestServerWithCaps(registry, nil, fakeCaps{
		set: capabilities.NewSet([]string{capabilities.HostAPI}),
	})
	listed, err := deniedServer.List(context.Background(), &hostv2.ServiceListRequest{Context: requestContext})
	if err != nil || listed.GetError().GetReason() != "host.extensions_call_denied" {
		t.Fatalf("list without extensions.call = %#v err=%v", listed.GetError(), err)
	}
	resolved, err := deniedServer.Resolve(context.Background(), &hostv2.ServiceResolveRequest{
		Context: requestContext, ServiceId: "demo.lookup", VersionConstraint: "1.0.0",
	})
	if err != nil || resolved.GetError().GetReason() != "host.extensions_call_denied" {
		t.Fatalf("resolve without extensions.call = %#v err=%v", resolved.GetError(), err)
	}
	invoked, err := deniedServer.Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
		Context: requestContext, ServiceId: "demo.lookup", Version: "1.0.0", Operation: "find",
		Input: v2ServiceDocument("demo.lookup.request", "1"),
	})
	if err != nil || invoked.GetError().GetReason() != "host.extensions_call_denied" || provider.invokeCalls != 0 {
		t.Fatalf("invoke without extensions.call = %#v err=%v calls=%d", invoked.GetError(), err, provider.invokeCalls)
	}

	// 无扩展身份同样拒绝。
	anonymous := proto.Clone(requestContext).(*protocolv2.RequestContext)
	anonymous.Extension = nil
	anonServer := newProtocolV2ServiceTestServer(registry, nil)
	anonList, _ := anonServer.List(context.Background(), &hostv2.ServiceListRequest{Context: anonymous})
	if anonList.GetError().GetReason() != "host.extensions_call_denied" {
		t.Fatalf("anonymous list = %#v", anonList.GetError())
	}

	// 能力源失败关闭（Safe Mode / 未启用）。
	unavailable := newProtocolV2ServiceTestServerWithCaps(registry, nil, fakeCaps{err: errors.New("disabled")})
	failClosed, _ := unavailable.List(context.Background(), &hostv2.ServiceListRequest{Context: requestContext})
	if failClosed.GetError().GetReason() != "host.extensions_call_denied" {
		t.Fatalf("capability source failure = %#v", failClosed.GetError())
	}
}
