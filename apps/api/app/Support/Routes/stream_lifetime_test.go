package routes

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestStreamDispatcherUsesDefaultBudgetWhenTimeoutIsZero(t *testing.T) {
	dispatch := &RouteStreamDispatch{step: RouteExecutionStep{TimeoutMS: 0}}
	if got := dispatch.streamBudgetDuration(); got != routeStreamDefaultBudget {
		t.Fatalf("budget=%s want %s", got, routeStreamDefaultBudget)
	}
	dispatch.step.TimeoutMS = 1500
	if got := dispatch.streamBudgetDuration(); got != 1500*time.Millisecond {
		t.Fatalf("budget=%s", got)
	}
}

func TestStreamDispatcherHostBudgetCoversGuardPreflightOpenAndStream(t *testing.T) {
	step := streamPluginGuardStep("stream.budget.shared", extensionmanifest.RouteActionAdd, false)
	step.TimeoutMS = 40
	step.Mode = extensionmanifest.RouteModeSSE
	var observedDeadline time.Time
	var observedOK bool
	var streamCtx context.Context
	guard := &budgetObservingStreamGuard{}
	invoker := &budgetObservingStreamInvoker{
		onOpen: func(ctx context.Context) {
			streamCtx = ctx
			observedDeadline, observedOK = ctx.Deadline()
		},
	}
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/budget-stream", nil, []RouteExecutionStep{step}, 0)},
		Guard: guard, Steps: invoker, Trace: NewRouteTraceRing(4),
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/budget-stream"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	start, err := prepared.Dispatch.Open(context.Background())
	if err != nil || start.Session == nil {
		t.Fatalf("start=%#v err=%v", start, err)
	}
	if !guard.deadlineOK || !observedOK {
		t.Fatal("guard and open did not share a deadline")
	}
	// Guard, open, and bound session must share one Host total budget deadline.
	if !guard.deadline.Equal(observedDeadline) {
		t.Fatalf("guard deadline=%s open deadline=%s", guard.deadline, observedDeadline)
	}
	if source, ok := start.Session.(RouteStreamLifetimeSource); !ok {
		t.Fatal("bound session does not expose lifetime source")
	} else {
		_ = source
	}
	// Wait for Host budget to expire and prove Recv surfaces the budget cause.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if streamCtx.Err() != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !errors.Is(context.Cause(streamCtx), ErrRouteStreamBudgetExceeded) {
		t.Fatalf("stream context cause=%v", context.Cause(streamCtx))
	}
	_, err = start.Session.Recv()
	if !errors.Is(err, ErrRouteStreamBudgetExceeded) {
		t.Fatalf("recv after budget err=%v", err)
	}
	source, ok := start.Session.(RouteStreamLifetimeSource)
	if !ok {
		t.Fatal("bound session missing lifetime source")
	}
	// Recv after budget must not close Done; adapter Cancel does.
	select {
	case <-source.Done():
		t.Fatal("budget Recv closed Done before adapter Cancel")
	default:
	}
	start.Session.Cancel()
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("lifetime Done was not closed after Cancel")
	}
}

func TestStreamDispatcherHostBudgetTimeoutFailsClosed(t *testing.T) {
	step := streamPluginGuardStep("stream.budget.timeout", extensionmanifest.RouteActionAdd, false)
	step.TimeoutMS = 15
	step.Mode = extensionmanifest.RouteModeSSE
	blocker := make(chan struct{})
	invoker := &blockingStreamInvoker{block: blocker}
	traces := NewRouteTraceRing(4)
	dispatcher := NewDispatcher(DispatcherConfig{
		Plans: dispatchPlanResolver{plan: dispatchPlan(http.MethodGet, "/budget-timeout", nil, []RouteExecutionStep{step}, 0)},
		Guard: allowStreamGuard{}, Steps: invoker, Trace: traces,
	})
	prepared, err := dispatcher.PrepareStream(
		context.Background(), DispatchRequest{Method: http.MethodGet, Path: "/budget-timeout"},
	)
	if err != nil || prepared.Dispatch == nil {
		t.Fatalf("prepared=%#v err=%v", prepared, err)
	}
	_, err = prepared.Dispatch.Open(context.Background())
	close(blocker)
	records := traces.RouteTraces(0)
	if !errors.Is(err, ErrRouteStreamBudgetExceeded) || !errors.Is(err, ErrDispatchTransport) ||
		len(records) != 1 || records[0].Outcome != RouteTraceTransportFailed {
		t.Fatalf("error=%v traces=%#v", err, records)
	}
}

func TestStreamLifetimeDetachCallerStopsRequestCancel(t *testing.T) {
	caller, cancel := context.WithCancel(context.Background())
	lifetime := newRouteStreamOpenLifetime(caller, time.Hour)
	inner := &lifetimeInnerSession{recv: make(chan recvResult, 1)}
	session := bindRouteStreamLifetime(inner, lifetime)
	detacher, ok := session.(RouteStreamCallerDetacher)
	if !ok {
		t.Fatal("session does not implement DetachCaller")
	}
	if err := detacher.DetachCaller(); err != nil {
		t.Fatalf("detach error=%v", err)
	}
	cancel()
	// Caller cancel after detach must not finish the outer lifetime.
	select {
	case <-session.(RouteStreamLifetimeSource).Done():
		t.Fatal("caller cancel after detach closed the stream lifetime")
	case <-time.After(30 * time.Millisecond):
	}
	if err := lifetime.Context().Err(); err != nil {
		t.Fatalf("lifetime context after detach cancel=%v", err)
	}
	// Host budget still terminates after detach; adapters Cancel after Fail.
	lifetime.cancelOpen(ErrRouteStreamBudgetExceeded)
	select {
	case <-lifetime.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("budget did not cancel the open context after detach")
	}
	session.Cancel()
	select {
	case <-session.(RouteStreamLifetimeSource).Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel after budget did not finish lifetime")
	}
	if cause := session.(RouteStreamLifetimeSource).Cause(); !errors.Is(cause, ErrRouteStreamBudgetExceeded) {
		t.Fatalf("cause after budget=%v", cause)
	}
}

func TestStreamLifetimeInnerCompletionReleasesResourcesBeforeAdapterCancel(t *testing.T) {
	caller, cancelCaller := context.WithCancel(context.Background())
	lifetime := newRouteStreamOpenLifetime(caller, time.Hour)
	forceCause := errors.New("exact runtime force cancel")
	inner := &lifetimeInnerSession{recv: make(chan recvResult)}
	session := bindRouteStreamLifetime(inner, lifetime)
	source := session.(RouteStreamLifetimeSource)

	inner.finish(forceCause)
	select {
	case <-lifetime.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("inner completion did not release the outer timer/caller lifetime")
	}
	select {
	case <-source.Done():
		t.Fatal("inner completion published public Done before adapter Cancel")
	default:
	}
	cancelCaller()
	session.Cancel()
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("adapter Cancel did not publish public Done")
	}
	if !errors.Is(source.Cause(), forceCause) {
		t.Fatalf("late caller cancellation replaced force cause: %v", source.Cause())
	}
}

func TestStreamLifetimePropagatesExactHostCauseToInner(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel func(*routeStreamOpenLifetime, context.CancelCauseFunc, error)
	}{
		{name: "caller", cancel: func(_ *routeStreamOpenLifetime, cancel context.CancelCauseFunc, cause error) { cancel(cause) }},
		{name: "budget", cancel: func(lifetime *routeStreamOpenLifetime, _ context.CancelCauseFunc, cause error) {
			lifetime.cancelOpen(cause)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			caller, cancelCaller := context.WithCancelCause(context.Background())
			lifetime := newRouteStreamOpenLifetime(caller, time.Hour)
			inner := &lifetimeInnerSession{recv: make(chan recvResult)}
			session := bindRouteStreamLifetime(inner, lifetime)
			source := session.(RouteStreamLifetimeSource)
			cause := errors.New("exact " + test.name + " cancel")

			test.cancel(lifetime, cancelCaller, cause)
			select {
			case <-inner.Done():
			case <-time.After(time.Second):
				t.Fatal("Host cancellation did not finish inner session")
			}
			if !errors.Is(inner.Cause(), cause) {
				t.Fatalf("inner cause=%v want %v", inner.Cause(), cause)
			}
			select {
			case <-source.Done():
				t.Fatal("Host cancellation published public Done before adapter Cancel")
			default:
			}
			session.Cancel()
			if !errors.Is(source.Cause(), cause) {
				t.Fatalf("outer cause=%v want %v", source.Cause(), cause)
			}
		})
	}
}

func TestStreamLifetimeCustomCallerCauseCarriesAbortDisposition(t *testing.T) {
	caller, cancelCaller := context.WithCancelCause(context.Background())
	lifetime := newRouteStreamOpenLifetime(caller, time.Hour)
	inner := &lifetimeInnerSession{recv: make(chan recvResult)}
	session := bindRouteStreamLifetime(inner, lifetime)
	source := session.(RouteStreamLifetimeSource)
	callerCause := errors.New("caller disconnected with custom cause")

	cancelCaller(callerCause)
	select {
	case <-inner.Done():
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not finish inner session")
	}
	for name, cause := range map[string]error{
		"inner":   inner.Cause(),
		"context": context.Cause(lifetime.Context()),
	} {
		class, record, classified := routeStreamFailureDisposition(cause)
		if !classified || record || class != "" || !errors.Is(cause, callerCause) {
			t.Fatalf("%s cause=%v class=%q record=%t classified=%t", name, cause, class, record, classified)
		}
	}
	select {
	case <-source.Done():
		t.Fatal("caller cancellation published public Done before adapter Cancel")
	default:
	}
	session.Cancel()
	if cause := source.Cause(); !errors.Is(cause, callerCause) {
		t.Fatalf("outer cause=%v", cause)
	}
}

func TestStreamLifetimeCallerCancelBeforeOpenHasNoInvoker(t *testing.T) {
	// Covered by TestStreamDispatcherCallerCancellationHasNoFailureEvidence; this
	// unit check proves the open lifetime itself refuses a pre-canceled caller.
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	lifetime := newRouteStreamOpenLifetime(caller, time.Hour)
	if err := lifetime.Context().Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled caller open context err=%v", err)
	}
	if !errors.Is(context.Cause(lifetime.Context()), context.Canceled) {
		t.Fatalf("cause=%v", context.Cause(lifetime.Context()))
	}
	lifetime.close(context.Cause(lifetime.Context()))
}

func TestStreamLifetimeTerminalWinsOverConcurrentCancel(t *testing.T) {
	lifetime := newRouteStreamOpenLifetime(context.Background(), time.Hour)
	inner := &racingInnerSession{}
	session := bindRouteStreamLifetime(inner, lifetime)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = session.Recv()
		}()
		go func() {
			defer wg.Done()
			session.Cancel()
		}()
		wg.Wait()
		source := session.(RouteStreamLifetimeSource)
		select {
		case <-source.Done():
		case <-time.After(time.Second):
			t.Fatal("lifetime Done not closed after terminal/cancel race")
		}
		response, ok := session.Response()
		// Cancel winner must never publish Response; terminal winner must.
		if source.Cause() == nil {
			if !ok || response.Status != http.StatusOK {
				t.Fatalf("terminal winner lost Response: ok=%t response=%#v", ok, response)
			}
		} else if ok {
			t.Fatalf("cancel winner published Response: %#v cause=%v", response, source.Cause())
		}
		// Reset for next iteration with a fresh pair.
		lifetime = newRouteStreamOpenLifetime(context.Background(), time.Hour)
		inner = &racingInnerSession{}
		session = bindRouteStreamLifetime(inner, lifetime)
	}
}

func TestStreamLifetimeOuterDoesNotEraseInnerTypedCause(t *testing.T) {
	lifetime := newRouteStreamOpenLifetime(context.Background(), time.Hour)
	typed := errors.New("runtime transport closed")
	inner := &lifetimeInnerSession{
		recv:  make(chan recvResult, 1),
		cause: typed,
	}
	session := bindRouteStreamLifetime(inner, lifetime)
	inner.recv <- recvResult{err: typed}
	_, err := session.Recv()
	if !errors.Is(err, typed) {
		t.Fatalf("recv err=%v", err)
	}
	source := session.(RouteStreamLifetimeSource)
	// Recv must not close Done; adapters Fail then Cancel first.
	select {
	case <-source.Done():
		t.Fatal("Recv closed lifetime Done before adapter Cancel")
	default:
	}
	session.Cancel()
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Cancel")
	}
	if !errors.Is(source.Cause(), typed) {
		t.Fatalf("outer cause=%v want typed transport cause", source.Cause())
	}
}

func TestStreamLifetimeRecvDoesNotCloseDoneBeforeCancel(t *testing.T) {
	lifetime := newRouteStreamOpenLifetime(context.Background(), time.Hour)
	inner := &lifetimeInnerSession{
		recv: make(chan recvResult, 1), hasResp: true,
		response: DispatchResponse{Status: http.StatusOK},
	}
	session := bindRouteStreamLifetime(inner, lifetime)
	source := session.(RouteStreamLifetimeSource)
	inner.recv <- recvResult{err: io.EOF}
	_, err := session.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("recv err=%v", err)
	}
	select {
	case <-source.Done():
		t.Fatal("EOF Recv closed Done before Complete/Cancel")
	case <-time.After(20 * time.Millisecond):
	}
	session.Cancel()
	select {
	case <-source.Done():
	case <-time.After(time.Second):
		t.Fatal("Cancel did not close Done after EOF")
	}
}

type budgetObservingStreamGuard struct {
	allowStreamGuard
	deadline   time.Time
	deadlineOK bool
}

func (g *budgetObservingStreamGuard) AuthorizeRoute(
	ctx context.Context,
	plan RouteExecutionPlan,
	stepIndex int,
	step RouteExecutionStep,
	request DispatchRequest,
) (RouteGuardAuthorization, error) {
	g.deadline, g.deadlineOK = ctx.Deadline()
	return g.allowStreamGuard.AuthorizeRoute(ctx, plan, stepIndex, step, request)
}

type budgetObservingStreamInvoker struct {
	onOpen func(context.Context)
	calls  int
}

func (*budgetObservingStreamInvoker) SupportsMode(string) bool { return false }

func (*budgetObservingStreamInvoker) Invoke(context.Context, RouteInvocation) (RouteInvocationResult, error) {
	return RouteInvocationResult{}, ErrDispatchTransport
}

func (i *budgetObservingStreamInvoker) OpenStream(ctx context.Context, _ RouteInvocation) (RouteStreamStart, error) {
	i.calls++
	if i.onOpen != nil {
		i.onOpen(ctx)
	}
	return RouteStreamStart{
		Response: DispatchResponse{Status: http.StatusOK},
		Session:  &lifetimeInnerSession{recv: make(chan recvResult)},
	}, nil
}

type blockingStreamInvoker struct {
	block chan struct{}
}

func (*blockingStreamInvoker) SupportsMode(string) bool { return false }

func (*blockingStreamInvoker) Invoke(context.Context, RouteInvocation) (RouteInvocationResult, error) {
	return RouteInvocationResult{}, ErrDispatchTransport
}

func (i *blockingStreamInvoker) OpenStream(ctx context.Context, input RouteInvocation) (RouteStreamStart, error) {
	input.Commit.SideEffectStarted()
	select {
	case <-ctx.Done():
		return RouteStreamStart{}, context.Cause(ctx)
	case <-i.block:
		return RouteStreamStart{}, errors.New("block released without cancel")
	}
}

type recvResult struct {
	chunk RouteStreamChunk
	err   error
}

type lifetimeInnerSession struct {
	recv     chan recvResult
	response DispatchResponse
	hasResp  bool
	mu       sync.Mutex
	cause    error
	done     chan struct{}
	once     sync.Once
}

func (s *lifetimeInnerSession) Send([]byte, bool) error { return nil }
func (s *lifetimeInnerSession) CloseRequest() error     { return nil }

func (s *lifetimeInnerSession) Recv() (RouteStreamChunk, error) {
	if s.recv == nil {
		return RouteStreamChunk{}, io.EOF
	}
	select {
	case result, ok := <-s.recv:
		if !ok {
			return RouteStreamChunk{}, io.EOF
		}
		if result.err != nil {
			s.finish(result.err)
			return RouteStreamChunk{}, result.err
		}
		return result.chunk, nil
	case <-s.Done():
		cause := s.Cause()
		if cause == nil {
			cause = context.Canceled
		}
		return RouteStreamChunk{}, cause
	}
}

func (s *lifetimeInnerSession) Response() (DispatchResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.response, s.hasResp
}

func (s *lifetimeInnerSession) Cancel() {
	s.CancelWithCause(context.Canceled)
}

func (s *lifetimeInnerSession) CancelWithCause(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	s.finish(cause)
}

func (s *lifetimeInnerSession) Done() <-chan struct{} {
	s.mu.Lock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	done := s.done
	s.mu.Unlock()
	return done
}

func (s *lifetimeInnerSession) Cause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

func (s *lifetimeInnerSession) finish(cause error) {
	s.once.Do(func() {
		s.mu.Lock()
		if s.cause == nil {
			s.cause = cause
		}
		if s.done == nil {
			s.done = make(chan struct{})
		}
		done := s.done
		s.mu.Unlock()
		close(done)
	})
}

var _ RouteStreamSession = (*lifetimeInnerSession)(nil)
var _ RouteStreamLifetimeSource = (*lifetimeInnerSession)(nil)
var _ RouteStreamCauseCanceler = (*lifetimeInnerSession)(nil)

// racingInnerSession lets terminal EOF and Cancel race on one session.
// State and cause are decided under one lock so a terminal winner cannot be
// observed with a cancel cause (or the reverse).
type racingInnerSession struct {
	mu       sync.Mutex
	state    int // 0 active, 1 terminal, 2 canceled
	response DispatchResponse
	cause    error
	done     chan struct{}
	once     sync.Once
}

func (s *racingInnerSession) Send([]byte, bool) error { return nil }
func (s *racingInnerSession) CloseRequest() error     { return nil }

func (s *racingInnerSession) Recv() (RouteStreamChunk, error) {
	s.mu.Lock()
	switch s.state {
	case 2:
		cause := s.cause
		s.mu.Unlock()
		if cause == nil {
			cause = context.Canceled
		}
		return RouteStreamChunk{}, cause
	case 1:
		s.mu.Unlock()
		return RouteStreamChunk{}, io.EOF
	default:
		s.state = 1
		s.response = DispatchResponse{Status: http.StatusOK}
		s.cause = nil
		s.mu.Unlock()
		s.publishDone()
		return RouteStreamChunk{}, io.EOF
	}
}

func (s *racingInnerSession) Response() (DispatchResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != 1 {
		return DispatchResponse{}, false
	}
	return s.response, true
}

func (s *racingInnerSession) Cancel() {
	s.CancelWithCause(context.Canceled)
}

func (s *racingInnerSession) CancelWithCause(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	s.mu.Lock()
	if s.state == 0 {
		s.state = 2
		s.cause = cause
		s.response = DispatchResponse{}
	}
	s.mu.Unlock()
	s.publishDone()
}

func (s *racingInnerSession) Done() <-chan struct{} {
	s.mu.Lock()
	if s.done == nil {
		s.done = make(chan struct{})
	}
	done := s.done
	s.mu.Unlock()
	return done
}

func (s *racingInnerSession) Cause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cause
}

func (s *racingInnerSession) publishDone() {
	s.once.Do(func() {
		s.mu.Lock()
		if s.done == nil {
			s.done = make(chan struct{})
		}
		done := s.done
		s.mu.Unlock()
		close(done)
	})
}

var _ RouteStreamSession = (*racingInnerSession)(nil)
var _ RouteStreamLifetimeSource = (*racingInnerSession)(nil)
var _ RouteStreamCauseCanceler = (*racingInnerSession)(nil)
