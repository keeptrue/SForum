package pluginv2

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestServiceRegistryValidatesDefinitionsAndBuildsDescriptors(t *testing.T) {
	registry, err := NewServiceRegistry(
		ServiceDefinition{
			ServiceID: "zeta.lookup", Version: "2.0.0",
			RequestSchemaID: "zeta.lookup.request@1", ResponseSchemaID: "zeta.lookup.response@1",
			RequiredAuthority: []string{"settings.read", "audit.append"},
			Operations:        []ServiceOperation{{Name: "watch", Stream: serviceEchoStream}},
		},
		ServiceDefinition{
			ServiceID: "alpha.lookup", Version: "1.0.0",
			RequestSchemaID: "alpha.lookup.request@1", ResponseSchemaID: "alpha.lookup.response@1",
			Operations: []ServiceOperation{{Name: "get", Unary: serviceEchoUnary}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 2 || descriptors[0].GetServiceId() != "alpha.lookup" || descriptors[1].GetServiceId() != "zeta.lookup" {
		t.Fatalf("descriptors are not deterministic: %#v", descriptors)
	}
	if descriptors[0].GetClientStreaming() || descriptors[0].GetServerStreaming() ||
		!descriptors[1].GetClientStreaming() || !descriptors[1].GetServerStreaming() {
		t.Fatalf("streaming shape was not derived from handlers: %#v", descriptors)
	}
	if !reflect.DeepEqual(descriptors[1].GetRequiredAuthority(), []string{"audit.append", "settings.read"}) {
		t.Fatalf("authority order = %#v", descriptors[1].GetRequiredAuthority())
	}
	descriptors[0].ServiceId = "mutated"
	if registry.Descriptors()[0].GetServiceId() != "alpha.lookup" {
		t.Fatal("Descriptors returned mutable registry state")
	}
}

func TestServiceRegistryRejectsInvalidDefinitions(t *testing.T) {
	valid := serviceTestDefinition()
	tests := []struct {
		name        string
		definitions []ServiceDefinition
	}{
		{"invalid id", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.ServiceID = "Invalid" })}},
		{"loose semver", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.Version = "1.2" })}},
		{"schema without version", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.RequestSchemaID = "demo.lookup.request" })}},
		{"no operations", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.Operations = nil })}},
		{"invalid operation", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.Operations[0].Name = "Get" })}},
		{"operation without handler", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.Operations[0].Unary = nil })}},
		{"duplicate operation", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.Operations = append(value.Operations, value.Operations[0]) })}},
		{"duplicate authority", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.RequiredAuthority = []string{"settings.read", "settings.read"} })}},
		{"invalid authority", []ServiceDefinition{changeServiceDefinition(valid, func(value *ServiceDefinition) { value.RequiredAuthority = []string{"settings read"} })}},
		{"duplicate service", []ServiceDefinition{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewServiceRegistry(test.definitions...); !errors.Is(err, ErrInvalidServiceDefinition) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestServiceRegistryDispatchesUnaryCallsAndPublishesHandshake(t *testing.T) {
	definition := serviceTestDefinition()
	registry, err := NewServiceRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer().WithServiceRegistry(registry)
	handshake := validHandshakeRequest()
	response, err := server.Handshake(context.Background(), handshake)
	if err != nil || response.GetError() != nil {
		t.Fatalf("handshake error response=%#v err=%v", response, err)
	}
	if len(response.GetServices()) != 1 || !proto.Equal(response.GetServices()[0], registry.Descriptors()[0]) {
		t.Fatalf("handshake services = %#v", response.GetServices())
	}

	// required_authority is enforced by the Host broker against the consumer.
	// The provider receives its own authenticated runtime authority envelope.
	request := serviceTestRequest(handshake.GetContext().GetExtension())
	result, err := server.InvokeService(context.Background(), request)
	if err != nil || result.GetError() != nil {
		t.Fatalf("invoke response=%#v err=%v", result, err)
	}
	if result.GetContext().GetRequestId() != request.GetContext().GetRequestId() ||
		result.GetOutput().GetSchemaId() != "demo.lookup.response" {
		t.Fatalf("invoke result = %#v", result)
	}
}

func TestServiceRegistryFreezesDeclarationsAfterHandshake(t *testing.T) {
	registry, err := NewServiceRegistry(serviceTestDefinition())
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer().WithServiceRegistry(registry)
	request := validHandshakeRequest()
	first, err := server.Handshake(context.Background(), request)
	if err != nil || first.GetError() != nil {
		t.Fatalf("handshake response=%#v err=%v", first, err)
	}
	server.WithServiceRegistry(nil).WithServices(&protocolwire.ServiceDescriptor{
		ServiceId: "mutated.service", Version: "1.0.0",
		RequestSchemaId: "mutated.request@1", ResponseSchemaId: "mutated.response@1",
	})
	repeated, err := server.Handshake(context.Background(), request)
	if err != nil || repeated.GetError() != nil || len(repeated.GetServices()) != 1 ||
		!proto.Equal(first.GetServices()[0], repeated.GetServices()[0]) {
		t.Fatalf("post-handshake mutation changed snapshot: first=%#v repeated=%#v err=%v", first, repeated, err)
	}
	response, err := server.InvokeService(context.Background(), serviceTestRequest(request.GetContext().GetExtension()))
	if err != nil || response.GetError() != nil {
		t.Fatalf("frozen dispatcher response=%#v err=%v", response, err)
	}
}

func TestServiceRegistryUnaryFailuresAreTypedAndSanitized(t *testing.T) {
	definition := serviceTestDefinition()
	registry, err := NewServiceRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	base := serviceTestRequest(serviceTestIdentity(), "settings.read")
	tests := []struct {
		name   string
		change func(*pluginwire.ServiceRequest)
		code   protocolwire.ErrorCode
		reason string
	}{
		{"loose version", func(request *pluginwire.ServiceRequest) { request.ServiceVersion = "1.0" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "service.version_invalid"},
		{"unknown operation", func(request *pluginwire.ServiceRequest) { request.Operation = "missing" }, protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, "service.operation_not_found"},
		{"input schema", func(request *pluginwire.ServiceRequest) { request.Input.SchemaVersion = "2" }, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "service.schema_mismatch"},
		{"expired deadline", func(request *pluginwire.ServiceRequest) {
			request.Context.Deadline = timestamppb.New(time.Now().Add(-time.Second))
		}, protocolwire.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, "service.deadline_expired"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := proto.Clone(base).(*pluginwire.ServiceRequest)
			test.change(request)
			response, err := registry.InvokeService(context.Background(), request)
			if err != nil || response.GetError().GetCode() != test.code || response.GetError().GetReason() != test.reason {
				t.Fatalf("response=%#v err=%v", response, err)
			}
		})
	}

	typedDefinition := serviceTestDefinition()
	typedDefinition.Operations[0].Unary = func(context.Context, *ServiceCall) (*protocolwire.TypedDocument, error) {
		return nil, &ServiceError{Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT, Reason: "demo.conflict", Message: "Already exists."}
	}
	typedRegistry, _ := NewServiceRegistry(typedDefinition)
	typed, err := typedRegistry.InvokeService(context.Background(), base)
	if err != nil || typed.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_CONFLICT || typed.GetError().GetReason() != "demo.conflict" {
		t.Fatalf("typed handler error response=%#v err=%v", typed, err)
	}

	internalDefinition := serviceTestDefinition()
	internalDefinition.Operations[0].Unary = func(context.Context, *ServiceCall) (*protocolwire.TypedDocument, error) {
		return nil, errors.New("secret implementation detail")
	}
	internalRegistry, _ := NewServiceRegistry(internalDefinition)
	internal, err := internalRegistry.InvokeService(context.Background(), base)
	if err != nil || internal.GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_INTERNAL ||
		internal.GetError().GetMessage() != "Plugin service handler failed." {
		t.Fatalf("internal handler error response=%#v err=%v", internal, err)
	}
}

func TestServiceRegistryStreamConsumesOpenAndDispatchesTypedMessages(t *testing.T) {
	definition := serviceTestDefinition()
	definition.Operations[0].Stream = serviceEchoStream
	registry, err := NewServiceRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	stream := &serviceTestBidiStream{
		ctx: context.Background(),
		recv: []*pluginwire.ServiceStreamFrame{
			serviceOpenFrame(serviceTestContext(serviceTestIdentity())),
			serviceMessageFrame(serviceTestDocument("demo.lookup.request", "1")),
		},
	}
	if err := registry.StreamService(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetMessage().GetSchemaId() != "demo.lookup.response" {
		t.Fatalf("stream output = %#v", stream.sent)
	}
	descriptor := registry.Descriptors()[0]
	if !descriptor.GetClientStreaming() || !descriptor.GetServerStreaming() {
		t.Fatalf("stream descriptor = %#v", descriptor)
	}
}

func TestServiceRegistryStreamRejectsUnsafeFramesWithTypedErrors(t *testing.T) {
	definition := serviceTestDefinition()
	definition.Operations[0].Stream = serviceEchoStream
	registry, err := NewServiceRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	validOpen := serviceOpenFrame(serviceTestContext(serviceTestIdentity()))
	tests := []struct {
		name   string
		frames []*pluginwire.ServiceStreamFrame
		code   protocolwire.ErrorCode
		reason string
	}{
		{"message before open", []*pluginwire.ServiceStreamFrame{serviceMessageFrame(serviceTestDocument("demo.lookup.request", "1"))}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "service.open_required"},
		{"duplicate open", []*pluginwire.ServiceStreamFrame{validOpen, validOpen}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "service.stream_frame_invalid"},
		{"wrong input schema", []*pluginwire.ServiceStreamFrame{validOpen, serviceMessageFrame(serviceTestDocument("demo.lookup.request", "2"))}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "service.schema_mismatch"},
		{"invalid idle timeout", []*pluginwire.ServiceStreamFrame{changeServiceOpenFrame(validOpen, func(open *pluginwire.ServiceStreamOpen) { open.IdleTimeout = durationpb.New(-time.Second) })}, protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "service.idle_timeout_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := &serviceTestBidiStream{ctx: context.Background(), recv: cloneServiceFrames(test.frames)}
			if err := registry.StreamService(stream); err != nil {
				t.Fatal(err)
			}
			if len(stream.sent) != 1 || stream.sent[0].GetError().GetCode() != test.code || stream.sent[0].GetError().GetReason() != test.reason {
				t.Fatalf("stream output = %#v", stream.sent)
			}
		})
	}
}

func TestServiceRegistryStreamPropagatesCancellation(t *testing.T) {
	started := make(chan struct{})
	definition := serviceTestDefinition()
	definition.Operations[0].Stream = func(stream *ServiceStream) error {
		close(started)
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	registry, err := NewServiceRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream := &serviceTestBidiStream{ctx: ctx, recv: []*pluginwire.ServiceStreamFrame{
		serviceOpenFrame(serviceTestContext(serviceTestIdentity())),
	}}
	result := make(chan error, 1)
	go func() { result <- registry.StreamService(stream) }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("stream cancellation error = %v", err)
	}
	if len(stream.sent) != 0 {
		t.Fatalf("cancellation emitted frames: %#v", stream.sent)
	}
}

func TestServiceRegistryStreamEnforcesIdleTimeout(t *testing.T) {
	definition := serviceTestDefinition()
	definition.Operations[0].Stream = serviceEchoStream
	registry, err := NewServiceRegistry(definition)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	stream := &serviceTestBidiStream{
		ctx: context.Background(), recv: []*pluginwire.ServiceStreamFrame{
			changeServiceOpenFrame(serviceOpenFrame(serviceTestContext(serviceTestIdentity())), func(open *pluginwire.ServiceStreamOpen) {
				open.IdleTimeout = durationpb.New(10 * time.Millisecond)
			}),
		},
		recvBlock: release,
	}
	if err := registry.StreamService(stream); err != nil {
		t.Fatal(err)
	}
	close(release)
	if len(stream.sent) != 1 || stream.sent[0].GetError().GetReason() != "service.idle_timeout_exceeded" {
		t.Fatalf("idle timeout output = %#v", stream.sent)
	}
}

func TestServiceRegistryStreamPreservesRemoteTypedErrorAndValidatesOutput(t *testing.T) {
	definition := serviceTestDefinition()
	definition.Operations[0].Stream = func(stream *ServiceStream) error {
		_, err := stream.Recv()
		return err
	}
	registry, _ := NewServiceRegistry(definition)
	remote := &serviceTestBidiStream{ctx: context.Background(), recv: []*pluginwire.ServiceStreamFrame{
		serviceOpenFrame(serviceTestContext(serviceTestIdentity())),
		{Frame: &pluginwire.ServiceStreamFrame_Error{Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_CONFLICT, Reason: "caller.closed", Message: "Caller stopped.",
		}}},
	}}
	if err := registry.StreamService(remote); err != nil || len(remote.sent) != 0 {
		t.Fatalf("remote terminal error was echoed: sent=%#v err=%v", remote.sent, err)
	}

	definition.Operations[0].Stream = func(stream *ServiceStream) error {
		return stream.Send(serviceTestDocument("wrong.output", "1"))
	}
	registry, _ = NewServiceRegistry(definition)
	invalidOutput := &serviceTestBidiStream{ctx: context.Background(), recv: []*pluginwire.ServiceStreamFrame{
		serviceOpenFrame(serviceTestContext(serviceTestIdentity())),
	}}
	if err := registry.StreamService(invalidOutput); err != nil {
		t.Fatal(err)
	}
	if len(invalidOutput.sent) != 1 || invalidOutput.sent[0].GetError().GetCode() != protocolwire.ErrorCode_ERROR_CODE_INTERNAL {
		t.Fatalf("invalid stream output = %#v", invalidOutput.sent)
	}
}

type serviceCustomServer struct{ *Server }

func (s *serviceCustomServer) InvokeService(context.Context, *pluginwire.ServiceRequest) (*pluginwire.ServiceResponse, error) {
	return &pluginwire.ServiceResponse{Output: serviceTestDocument("custom.response", "1")}, nil
}

func TestServiceRegistryDoesNotBreakCustomGeneratedServerOverrides(t *testing.T) {
	server := &serviceCustomServer{Server: NewServer()}
	var generated pluginwire.PluginRuntimeServiceServer = server
	response, err := generated.InvokeService(context.Background(), &pluginwire.ServiceRequest{})
	if err != nil || response.GetOutput().GetSchemaId() != "custom.response" {
		t.Fatalf("custom generated server response=%#v err=%v", response, err)
	}
	_, err = NewServer().InvokeService(context.Background(), &pluginwire.ServiceRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("server without registry error = %v", err)
	}
}

func serviceTestDefinition() ServiceDefinition {
	return ServiceDefinition{
		ServiceID: "demo.lookup", Version: "1.0.0",
		RequestSchemaID: "demo.lookup.request@1", ResponseSchemaID: "demo.lookup.response@1",
		RequiredAuthority: []string{"settings.read"},
		Operations:        []ServiceOperation{{Name: "get", Unary: serviceEchoUnary}},
	}
}

func serviceEchoUnary(_ context.Context, call *ServiceCall) (*protocolwire.TypedDocument, error) {
	if call.Input.GetSchemaId() != "demo.lookup.request" {
		return nil, errors.New("unvalidated service input")
	}
	return serviceTestDocument("demo.lookup.response", "1"), nil
}

func serviceEchoStream(stream *ServiceStream) error {
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(serviceTestDocument("demo.lookup.response", message.GetSchemaVersion())); err != nil {
			return err
		}
	}
}

func serviceTestRequest(identity *protocolwire.ExtensionIdentity, authority ...string) *pluginwire.ServiceRequest {
	return &pluginwire.ServiceRequest{
		Context:   serviceTestContext(identity, authority...),
		ServiceId: "demo.lookup", ServiceVersion: "1.0.0", Operation: "get",
		Input: serviceTestDocument("demo.lookup.request", "1"),
	}
}

func serviceTestContext(identity *protocolwire.ExtensionIdentity, authority ...string) *protocolwire.RequestContext {
	grants := make([]*protocolwire.AuthorityGrant, 0, len(authority))
	for _, key := range authority {
		grants = append(grants, &protocolwire.AuthorityGrant{Key: key, ContractVersion: HostAPIVersion})
	}
	return &protocolwire.RequestContext{
		RequestId: "service-request-1", Extension: cloneIdentity(identity), GrantedAuthority: grants,
		Deadline: timestamppb.New(time.Now().Add(time.Minute)), Locale: "zh-CN",
	}
}

func serviceTestIdentity() *protocolwire.ExtensionIdentity {
	return &protocolwire.ExtensionIdentity{
		ExtensionId: "demo.plugin", ExtensionVersion: "1.0.0", ArtifactDigest: "sha256:demo",
		TrustGrantId: "grant-1", RuntimeEpoch: 1, InstanceId: "runtime-1",
	}
}

func serviceTestDocument(schemaID, version string) *protocolwire.TypedDocument {
	return &protocolwire.TypedDocument{SchemaId: schemaID, SchemaVersion: version}
}

func serviceOpenFrame(request *protocolwire.RequestContext) *pluginwire.ServiceStreamFrame {
	return &pluginwire.ServiceStreamFrame{Frame: &pluginwire.ServiceStreamFrame_Open{Open: &pluginwire.ServiceStreamOpen{
		Context: request, ServiceId: "demo.lookup", ServiceVersion: "1.0.0", Operation: "get",
	}}}
}

func serviceMessageFrame(message *protocolwire.TypedDocument) *pluginwire.ServiceStreamFrame {
	return &pluginwire.ServiceStreamFrame{Frame: &pluginwire.ServiceStreamFrame_Message{Message: message}}
}

func changeServiceDefinition(value ServiceDefinition, change func(*ServiceDefinition)) ServiceDefinition {
	result := value
	result.RequiredAuthority = append([]string(nil), value.RequiredAuthority...)
	result.Operations = append([]ServiceOperation(nil), value.Operations...)
	change(&result)
	return result
}

func changeServiceOpenFrame(value *pluginwire.ServiceStreamFrame, change func(*pluginwire.ServiceStreamOpen)) *pluginwire.ServiceStreamFrame {
	result := proto.Clone(value).(*pluginwire.ServiceStreamFrame)
	change(result.GetOpen())
	return result
}

func cloneServiceFrames(values []*pluginwire.ServiceStreamFrame) []*pluginwire.ServiceStreamFrame {
	result := make([]*pluginwire.ServiceStreamFrame, 0, len(values))
	for _, value := range values {
		result = append(result, proto.Clone(value).(*pluginwire.ServiceStreamFrame))
	}
	return result
}

type serviceTestBidiStream struct {
	ctx       context.Context
	recv      []*pluginwire.ServiceStreamFrame
	recvBlock <-chan struct{}

	mu   sync.Mutex
	sent []*pluginwire.ServiceStreamFrame
}

func (s *serviceTestBidiStream) Context() context.Context   { return s.ctx }
func (*serviceTestBidiStream) SetHeader(metadata.MD) error  { return nil }
func (*serviceTestBidiStream) SendHeader(metadata.MD) error { return nil }
func (*serviceTestBidiStream) SetTrailer(metadata.MD)       {}

func (s *serviceTestBidiStream) Send(frame *pluginwire.ServiceStreamFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, proto.Clone(frame).(*pluginwire.ServiceStreamFrame))
	return nil
}

func (s *serviceTestBidiStream) Recv() (*pluginwire.ServiceStreamFrame, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if len(s.recv) == 0 {
		if s.recvBlock != nil {
			select {
			case <-s.recvBlock:
			case <-s.ctx.Done():
				return nil, s.ctx.Err()
			}
		}
		return nil, io.EOF
	}
	result := s.recv[0]
	s.recv = s.recv[1:]
	return proto.Clone(result).(*pluginwire.ServiceStreamFrame), nil
}

func (s *serviceTestBidiStream) SendMsg(message any) error {
	frame, ok := message.(*pluginwire.ServiceStreamFrame)
	if !ok {
		return errors.New("unexpected service test send type")
	}
	return s.Send(frame)
}

func (s *serviceTestBidiStream) RecvMsg(message any) error {
	frame, err := s.Recv()
	if err != nil {
		return err
	}
	target, ok := message.(*pluginwire.ServiceStreamFrame)
	if !ok {
		return errors.New("unexpected service test receive type")
	}
	proto.Reset(target)
	proto.Merge(target, frame)
	return nil
}

var _ grpc.BidiStreamingServer[pluginwire.ServiceStreamFrame, pluginwire.ServiceStreamFrame] = (*serviceTestBidiStream)(nil)
