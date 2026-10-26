package hostapi

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testServiceProviderAdmission struct {
	mu       sync.Mutex
	calls    int
	identity ServiceProviderIdentity
	err      error
	acquire  func(context.Context, ServiceProviderIdentity) (ServiceProviderAdmissionLease, error)
}

func (a *testServiceProviderAdmission) AcquireServiceProvider(ctx context.Context, identity ServiceProviderIdentity) (ServiceProviderAdmissionLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	a.mu.Lock()
	a.calls++
	a.identity = identity
	err := a.err
	acquire := a.acquire
	a.mu.Unlock()
	if acquire != nil {
		return acquire(ctx, identity)
	}
	if err != nil {
		return nil, err
	}
	return &testServiceProviderLease{ctx: ctx}, nil
}

func (a *testServiceProviderAdmission) snapshot() (int, ServiceProviderIdentity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.identity
}

type testServiceProviderLease struct {
	ctx     context.Context
	failure error

	mu       sync.Mutex
	released bool
}

func (l *testServiceProviderLease) Context() context.Context { return l.ctx }
func (l *testServiceProviderLease) Release() {
	l.mu.Lock()
	l.released = true
	l.mu.Unlock()
}
func (l *testServiceProviderLease) ServiceProviderAdmissionFailure() error {
	if l.failure != nil {
		return l.failure
	}
	return context.Cause(l.ctx)
}
func (l *testServiceProviderLease) wasReleased() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.released
}

type serviceLeaseContextKey struct{}

func newProtocolV2ServiceTestServer(registry *ServiceRegistry, admission ServiceProviderAdmission) *protocolV2ServiceDiscoveryServer {
	if admission == nil {
		admission = &testServiceProviderAdmission{}
	}
	// 默认授予 extensions.call，模拟已启用且声明该进程能力的消费者插件。
	return newProtocolV2ServiceTestServerWithCaps(registry, admission, fakeCaps{
		set: capabilities.NewSet([]string{capabilities.ExtensionsCall}),
	})
}

func newProtocolV2ServiceTestServerWithCaps(
	registry *ServiceRegistry,
	admission ServiceProviderAdmission,
	caps CapabilitySource,
) *protocolV2ServiceDiscoveryServer {
	if admission == nil {
		admission = &testServiceProviderAdmission{}
	}
	return &protocolV2ServiceDiscoveryServer{core: &protocolV2Core{
		service: New(Config{ServiceAdmission: admission, Capabilities: caps}), services: registry,
	}}
}

func TestProtocolV2ServiceListAndResolveDoNotAcquireProviderLease(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	admission := &testServiceProviderAdmission{err: ErrServiceProviderDraining}
	server := newProtocolV2ServiceTestServer(registry, admission)
	requestContext := v2ServiceRequestContext("consumer.plugin", "instance-consumer")
	listed, err := server.List(context.Background(), &hostv2.ServiceListRequest{Context: requestContext})
	if err != nil || listed.GetError() != nil || len(listed.GetServices()) != 1 {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	resolved, err := server.Resolve(context.Background(), &hostv2.ServiceResolveRequest{
		Context: requestContext, ServiceId: "demo.lookup", VersionConstraint: "1.0.0",
	})
	if err != nil || resolved.GetError() != nil {
		t.Fatalf("resolve = %#v, %v", resolved, err)
	}
	if calls, _ := admission.snapshot(); calls != 0 {
		t.Fatalf("discovery acquired %d provider leases", calls)
	}
}

func TestProtocolV2ServiceInvokeUsesExactWinnerLeaseContext(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	lease := &testServiceProviderLease{ctx: context.WithValue(context.Background(), serviceLeaseContextKey{}, "lease")}
	admission := &testServiceProviderAdmission{acquire: func(context.Context, ServiceProviderIdentity) (ServiceProviderAdmissionLease, error) {
		return lease, nil
	}}
	response, err := newProtocolV2ServiceTestServer(registry, admission).Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
		Context:   v2ServiceRequestContext("consumer.plugin", "instance-consumer"),
		ServiceId: "demo.lookup", Version: "1.0.0", Operation: "find",
		Input: v2ServiceDocument("demo.lookup.request", "1"),
	})
	if err != nil || response.GetError() != nil {
		t.Fatalf("invoke = %#v, %v", response, err)
	}
	calls, identity := admission.snapshot()
	if calls != 1 || identity != (ServiceProviderIdentity{ExtensionID: "provider.plugin", InstanceID: "instance-provider"}) {
		t.Fatalf("admission calls=%d identity=%#v", calls, identity)
	}
	if provider.invokeContext.Value(serviceLeaseContextKey{}) != "lease" || !lease.wasReleased() {
		t.Fatalf("provider context=%#v released=%v", provider.invokeContext, lease.wasReleased())
	}
}

func TestProtocolV2ServiceInvocationFailsClosedAndNeverFallsBack(t *testing.T) {
	registry := NewServiceRegistry()
	stale := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	fallback := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	staleRegistration := v2ServiceRegistration("stale.plugin", "instance-stale", "demo.lookup", "1.0.0", stale)
	staleRegistration.Priority = 100
	if err := registry.ReplaceExtension("stale.plugin", []ServiceRegistration{staleRegistration}); err != nil {
		t.Fatal(err)
	}
	if err := registry.ReplaceExtension("fallback.plugin", []ServiceRegistration{
		v2ServiceRegistration("fallback.plugin", "instance-active", "demo.lookup", "1.0.0", fallback),
	}); err != nil {
		t.Fatal(err)
	}
	request := &hostv2.ServiceInvokeRequest{
		Context:   v2ServiceRequestContext("consumer.plugin", "instance-consumer"),
		ServiceId: "demo.lookup", Version: "1.0.0", Operation: "find",
		Input: v2ServiceDocument("demo.lookup.request", "1"),
	}
	// 有 extensions.call 但无 ServiceAdmission：证明 admission 缺失仍 fail-closed。
	withoutAdmission := &protocolV2ServiceDiscoveryServer{core: &protocolV2Core{
		services: registry,
		service: New(Config{Capabilities: fakeCaps{
			set: capabilities.NewSet([]string{capabilities.ExtensionsCall}),
		}}),
	}}
	response, err := withoutAdmission.Invoke(context.Background(), request)
	if err != nil || response.GetError().GetReason() != "host.service_provider_admission_unavailable" {
		t.Fatalf("fail closed = %#v, %v", response, err)
	}

	admission := &testServiceProviderAdmission{acquire: func(_ context.Context, identity ServiceProviderIdentity) (ServiceProviderAdmissionLease, error) {
		if identity.ExtensionID == "stale.plugin" {
			return nil, ErrServiceProviderStale
		}
		return &testServiceProviderLease{ctx: context.Background()}, nil
	}}
	response, err = newProtocolV2ServiceTestServer(registry, admission).Invoke(context.Background(), request)
	if err != nil || response.GetError().GetReason() != "host.service_provider_stale" || stale.invokeCalls != 0 || fallback.invokeCalls != 0 {
		t.Fatalf("stale winner response=%#v err=%v stale=%d fallback=%d", response, err, stale.invokeCalls, fallback.invokeCalls)
	}
}

func TestProtocolV2ServiceAdmissionMapsPreCallFailures(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{output: v2ServiceDocument("demo.lookup.response", "1")}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		err    error
		code   protocolv2.ErrorCode
		reason string
	}{
		{name: "stale", err: ErrServiceProviderStale, code: protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, reason: "host.service_provider_stale"},
		{name: "draining", err: ErrServiceProviderDraining, code: protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, reason: "host.service_provider_draining"},
		{name: "unavailable", err: ErrServiceProviderAdmissionUnavailable, code: protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, reason: "host.service_provider_admission_unavailable"},
		{name: "cancelled", err: context.Canceled, code: protocolv2.ErrorCode_ERROR_CODE_CANCELLED, reason: "host.service_cancelled"},
		{name: "deadline", err: context.DeadlineExceeded, code: protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, reason: "host.service_deadline_exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admission := &testServiceProviderAdmission{err: tt.err}
			response, err := newProtocolV2ServiceTestServer(registry, admission).Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
				Context:   v2ServiceRequestContext("consumer.plugin", "instance-consumer"),
				ServiceId: "demo.lookup", Version: "1.0.0", Operation: "find",
				Input: v2ServiceDocument("demo.lookup.request", "1"),
			})
			if err != nil || response.GetError().GetCode() != tt.code || response.GetError().GetReason() != tt.reason || provider.invokeCalls != 0 {
				t.Fatalf("response=%#v err=%v calls=%d", response, err, provider.invokeCalls)
			}
		})
	}
}

type blockingServiceProvider struct {
	started chan struct{}
	calls   int
}

func (p *blockingServiceProvider) Invoke(ctx context.Context, _ *protocolv2.RequestContext, _, _, _ string, _ *protocolv2.TypedDocument) (*protocolv2.TypedDocument, *protocolv2.ErrorDetail, error) {
	p.calls++
	close(p.started)
	<-ctx.Done()
	return nil, nil, context.Cause(ctx)
}

func TestProtocolV2ServiceForcedDrainCancelsUnaryInvocation(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &blockingServiceProvider{started: make(chan struct{})}
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{
		v2ServiceRegistration("provider.plugin", "instance-provider", "demo.lookup", "1.0.0", provider),
	}); err != nil {
		t.Fatal(err)
	}
	leaseContext, cancel := context.WithCancelCause(context.Background())
	lease := &testServiceProviderLease{ctx: leaseContext, failure: ErrServiceProviderDraining}
	admission := &testServiceProviderAdmission{acquire: func(context.Context, ServiceProviderIdentity) (ServiceProviderAdmissionLease, error) {
		return lease, nil
	}}
	responseCh := make(chan *hostv2.ServiceInvokeResponse, 1)
	go func() {
		response, _ := newProtocolV2ServiceTestServer(registry, admission).Invoke(context.Background(), &hostv2.ServiceInvokeRequest{
			Context:   v2ServiceRequestContext("consumer.plugin", "instance-consumer"),
			ServiceId: "demo.lookup", Version: "1.0.0", Operation: "find",
			Input: v2ServiceDocument("demo.lookup.request", "1"),
		})
		responseCh <- response
	}()
	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	cancel(errors.New("forced lifecycle drain"))
	select {
	case response := <-responseCh:
		if response.GetError().GetReason() != "host.service_provider_draining" || !lease.wasReleased() {
			t.Fatalf("response=%#v released=%v", response, lease.wasReleased())
		}
	case <-time.After(time.Second):
		t.Fatal("forced drain did not interrupt provider")
	}
}

func TestProtocolV2ServiceStreamUsesLeaseContextForProviderAndAdapter(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{}
	registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
	registration.Descriptor.ClientStreaming = true
	registration.Descriptor.ServerStreaming = true
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	lease := &testServiceProviderLease{ctx: context.WithValue(context.Background(), serviceLeaseContextKey{}, "stream-lease")}
	admission := &testServiceProviderAdmission{acquire: func(context.Context, ServiceProviderIdentity) (ServiceProviderAdmissionLease, error) {
		return lease, nil
	}}
	stream := &fakeV2HostServiceStream{
		ctx: context.Background(),
		recv: []*hostv2.ServiceStreamFrame{
			v2ServiceOpenFrame(v2ServiceRequestContext("consumer.plugin", "instance-consumer"), "demo.stream", "1.0.0", "chat"),
			v2ServiceMessageFrame(v2ServiceDocument("demo.stream.request", "1")),
		},
	}
	if err := newProtocolV2ServiceTestServer(registry, admission).Stream(stream); err != nil {
		t.Fatal(err)
	}
	if provider.streamContext.Value(serviceLeaseContextKey{}) != "stream-lease" ||
		provider.adapterContext.Value(serviceLeaseContextKey{}) != "stream-lease" || !lease.wasReleased() {
		t.Fatalf("provider=%#v adapter=%#v released=%v", provider.streamContext, provider.adapterContext, lease.wasReleased())
	}
}

func TestProtocolV2ServiceStreamMapsCallerCancellationAndDeadline(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{}
	registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
	registration.Descriptor.ServerStreaming = true
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	tests := []struct {
		name string
		ctx  context.Context
		code codes.Code
	}{
		{name: "cancelled", ctx: cancelled, code: codes.Canceled},
		{name: "deadline", ctx: expired, code: codes.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &fakeV2HostServiceStream{ctx: tt.ctx, recv: []*hostv2.ServiceStreamFrame{
				v2ServiceOpenFrame(v2ServiceRequestContext("consumer.plugin", "instance-consumer"), "demo.stream", "1.0.0", "watch"),
			}}
			if code := status.Code(newProtocolV2ServiceTestServer(registry, nil).Stream(stream)); code != tt.code {
				t.Fatalf("status = %s", code)
			}
		})
	}
	if provider.streamContext != nil {
		t.Fatal("cancelled stream reached provider")
	}
}

type blockingAfterOpenServiceStream struct {
	*fakeV2HostServiceStream
	open    *hostv2.ServiceStreamFrame
	unblock chan struct{}
	once    sync.Once
}

func (s *blockingAfterOpenServiceStream) Recv() (*hostv2.ServiceStreamFrame, error) {
	var frame *hostv2.ServiceStreamFrame
	delivered := false
	s.once.Do(func() {
		frame = s.open
		delivered = true
	})
	if delivered {
		return frame, nil
	}
	<-s.unblock
	return nil, io.EOF
}

func TestProtocolV2ServiceLeaseCoversStreamInputLifecycle(t *testing.T) {
	registry := NewServiceRegistry()
	provider := &v2ServiceProvider{}
	registration := v2ServiceRegistration("provider.plugin", "instance-provider", "demo.stream", "1.0.0", provider)
	registration.Descriptor.ServerStreaming = true
	if err := registry.ReplaceExtension("provider.plugin", []ServiceRegistration{registration}); err != nil {
		t.Fatal(err)
	}
	leaseContext, cancel := context.WithCancelCause(context.Background())
	lease := &testServiceProviderLease{ctx: leaseContext, failure: ErrServiceProviderDraining}
	acquired := make(chan struct{})
	admission := &testServiceProviderAdmission{acquire: func(context.Context, ServiceProviderIdentity) (ServiceProviderAdmissionLease, error) {
		close(acquired)
		return lease, nil
	}}
	base := &fakeV2HostServiceStream{ctx: context.Background()}
	stream := &blockingAfterOpenServiceStream{
		fakeV2HostServiceStream: base,
		open:                    v2ServiceOpenFrame(v2ServiceRequestContext("consumer.plugin", "instance-consumer"), "demo.stream", "1.0.0", "watch"),
		unblock:                 make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { done <- newProtocolV2ServiceTestServer(registry, admission).Stream(stream) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("stream admission was not acquired")
	}
	cancel(errors.New("forced lifecycle drain"))
	select {
	case err := <-done:
		close(stream.unblock)
		if err != nil || len(base.sent) != 1 || base.sent[0].GetError().GetReason() != "host.service_provider_draining" || !lease.wasReleased() {
			t.Fatalf("stream err=%v sent=%#v released=%v", err, base.sent, lease.wasReleased())
		}
		if provider.streamContext != nil {
			t.Fatal("provider started after stream input drain")
		}
	case <-time.After(time.Second):
		close(stream.unblock)
		t.Fatal("forced drain did not interrupt stream input")
	}
}
