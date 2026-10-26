package hostapi

import (
	"context"
	"errors"
	"io"
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type cardinalityServiceProvider struct {
	v2ServiceProvider
	stream func(ServiceBidiStream) (*protocolv2.ErrorDetail, error)
}

func (p *cardinalityServiceProvider) Stream(_ context.Context, _ *protocolv2.RequestContext, _, _, _ string, stream ServiceBidiStream) (*protocolv2.ErrorDetail, error) {
	return p.stream(stream)
}

func TestGatewayInstanceBoundServiceUnregister(t *testing.T) {
	gateway := NewGateway(nil)
	provider := &v2ServiceProvider{}
	registration := v2ServiceRegistration("demo.plugin", "runtime-new", "demo.lookup", "1.0.0", provider)
	if err := gateway.ReplaceProtocolV2Services("demo.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	if gateway.UnregisterProtocolV2ServiceInstance("demo.plugin", "runtime-old") {
		t.Fatal("stale gateway runtime removed replacement services")
	}
	if _, err := gateway.ProtocolV2ServiceRegistry().ResolveExact("demo.lookup", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if !gateway.UnregisterProtocolV2ServiceInstance("demo.plugin", "runtime-new") {
		t.Fatal("current gateway runtime was not removed")
	}
}

func TestProtocolV2InvokeAndStreamMatchExactBuildVersion(t *testing.T) {
	registry := NewServiceRegistry()
	alpha := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	beta := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0+alpha", alpha),
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0+beta", beta),
	}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")
	requestContext.Actor = &protocolv2.Actor{UserId: 42, SessionId: "unattested"}
	response, err := server.Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
		Context:   requestContext,
		ServiceId: "demo.lookup", Version: "1.0.0+beta", Operation: "find",
		Input: v2ServiceDocument("demo.lookup.request", "1"),
	})
	if err != nil || response.GetError() != nil || beta.invokeCalls != 1 || alpha.invokeCalls != 0 || beta.context.GetActor() != nil {
		t.Fatalf("exact invoke response=%#v err=%v alpha=%d beta=%d", response, err, alpha.invokeCalls, beta.invokeCalls)
	}
	if requestContext.GetActor().GetUserId() != 42 {
		t.Fatal("provider context sanitization mutated caller-owned context")
	}

	streamAlpha := &v2ServiceProvider{}
	streamBeta := &v2ServiceProvider{}
	alphaRegistration := v2ServiceRegistration("stream.plugin", "instance-stream", "demo.stream", "1.0.0+alpha", streamAlpha)
	betaRegistration := v2ServiceRegistration("stream.plugin", "instance-stream", "demo.stream", "1.0.0+beta", streamBeta)
	alphaRegistration.Descriptor.ServerStreaming = true
	betaRegistration.Descriptor.ServerStreaming = true
	if err := registry.ReplaceExtension("stream.plugin", []ServiceRegistration{alphaRegistration, betaRegistration}); err != nil {
		t.Fatal(err)
	}
	streamContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")
	streamContext.Actor = &protocolv2.Actor{UserId: 43, SessionId: "unattested-stream"}
	stream := &fakeV2HostServiceStream{ctx: context.Background(), recv: []*hostv2.ServiceStreamFrame{
		v2ServiceOpenFrame(streamContext, "demo.stream", "1.0.0+beta", "watch"),
		v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
	}}
	if err := server.Stream(stream); err != nil {
		t.Fatal(err)
	}
	if len(streamBeta.streamMessages) != 1 || len(streamAlpha.streamMessages) != 0 || streamBeta.version != "1.0.0+beta" || streamBeta.context.GetActor() != nil {
		t.Fatalf("exact stream alpha=%#v beta=%#v sent=%#v", streamAlpha, streamBeta, stream.sent)
	}
	if streamContext.GetActor().GetUserId() != 43 {
		t.Fatal("stream context sanitization mutated caller-owned context")
	}
}

func TestProtocolV2InvokeRejectsStreamingDescriptor(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("demo.stream.response", "1")}
	registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
	registration.Descriptor.ServerStreaming = true
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	server := newProtocolV2ServiceTestServer(registry, nil)
	response, err := server.Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
		Context:   v2ServiceRequestContext("consumer.plugin", "instance-consumer"),
		ServiceId: "demo.stream", Version: "1.0.0", Operation: "watch",
		Input: v2ServiceDocument("demo.stream.request", "1"),
	})
	if err != nil || response.GetError().GetReason() != "host.service_stream_required" || provider.invokeCalls != 0 {
		t.Fatalf("streaming invoke response=%#v err=%v calls=%d", response, err, provider.invokeCalls)
	}
}

func TestProtocolV2ServerStreamRequiresExactlyOneInput(t *testing.T) {
	newServer := func() (*protocolV2ServiceDiscoveryServer, *v2ServiceProvider) {
		registry := NewServiceRegistry()
		provider := &v2ServiceProvider{}
		registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
		registration.Descriptor.ServerStreaming = true
		if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
			t.Fatal(err)
		}
		return newProtocolV2ServiceTestServer(registry, nil), provider
	}
	open := v2ServiceOpenFrame(v2ServiceRequestContext("consumer.plugin", "instance-consumer"), "demo.stream", "1.0.0", "watch")
	tests := []struct {
		name   string
		frames []*hostv2.ServiceStreamFrame
		reason string
	}{
		{name: "missing", frames: []*hostv2.ServiceStreamFrame{open}, reason: "host.service_input_message_required"},
		{name: "extra", frames: []*hostv2.ServiceStreamFrame{open, v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")), v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1"))}, reason: "host.service_input_message_limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, provider := newServer()
			stream := &fakeV2HostServiceStream{ctx: context.Background(), recv: test.frames}
			if err := server.Stream(stream); err != nil {
				t.Fatal(err)
			}
			if len(stream.sent) != 1 || stream.sent[0].GetError().GetReason() != test.reason || len(provider.streamMessages) != 0 {
				t.Fatalf("frames=%#v provider=%#v", stream.sent, provider)
			}
		})
	}
}

func TestProtocolV2ClientStreamPermitsExactlyOneOutput(t *testing.T) {
	build := func(provider ServiceProvider) *protocolV2ServiceDiscoveryServer {
		registry := NewServiceRegistry()
		registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
		registration.Descriptor.ClientStreaming = true
		if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
			t.Fatal(err)
		}
		return newProtocolV2ServiceTestServer(registry, nil)
	}
	frames := func() []*hostv2.ServiceStreamFrame {
		return []*hostv2.ServiceStreamFrame{
			v2ServiceOpenFrame(v2ServiceRequestContext("consumer.plugin", "instance-consumer"), "demo.stream", "1.0.0", "collect"),
			v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
			v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
		}
	}
	aggregate := &cardinalityServiceProvider{stream: func(stream ServiceBidiStream) (*protocolv2.ErrorDetail, error) {
		for {
			_, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return nil, stream.Send(v2ServiceDocument("demo.stream.response", "1"))
			}
			if err != nil {
				return nil, err
			}
		}
	}}
	stream := &fakeV2HostServiceStream{ctx: context.Background(), recv: frames()}
	if err := build(aggregate).Stream(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetMessage().GetSchemaId() != "demo.stream.response" {
		t.Fatalf("aggregate output frames = %#v", stream.sent)
	}

	tooMany := &v2ServiceProvider{}
	stream = &fakeV2HostServiceStream{ctx: context.Background(), recv: frames()}
	if err := build(tooMany).Stream(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 2 || stream.sent[1].GetError().GetReason() != "host.service_output_message_limit" {
		t.Fatalf("output limit frames = %#v", stream.sent)
	}

	silent := &cardinalityServiceProvider{stream: func(stream ServiceBidiStream) (*protocolv2.ErrorDetail, error) {
		for {
			_, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
		}
	}}
	stream = &fakeV2HostServiceStream{ctx: context.Background(), recv: frames()}
	if err := build(silent).Stream(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetError().GetReason() != "host.service_output_message_required" {
		t.Fatalf("missing output frames = %#v", stream.sent)
	}
}
