package hostapi

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
)

func TestProtocolV2IdentityAuthorityGateRunsBeforeCommandTransaction(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	entered := make(chan struct{})
	release := make(chan struct{})
	definition := testProtocolV2CommandDefinition(t, func(
		context.Context,
		pgx.Tx,
		*hostv2.CommandRequest,
		*protocolV2CommandPreparation,
	) (*protocolV2CommandExecution, error) {
		return testProtocolV2CommandExecution(t), nil
	})
	definition.RunAuthorityMutation = func(_ context.Context, run func() error) error {
		close(entered)
		<-release
		return run()
	}
	engine, err := newProtocolV2CommandEngine(backend, definition)
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := engine.plan(t.Context(), testProtocolV2CommandRequest(t, "authority-plan", "next")); err != nil || plan.GetError() != nil {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	select {
	case <-entered:
		t.Fatal("read-only command planning entered the authority gate")
	default:
	}

	type executionResult struct {
		result *hostv2.CommandResult
		err    error
	}
	result := make(chan executionResult, 1)
	go func() {
		value, executeErr := engine.execute(t.Context(), testProtocolV2CommandRequest(t, "authority-execute", "next"))
		result <- executionResult{result: value, err: executeErr}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("identity authority gate was not entered")
	}
	if begins := backend.beginCount(); begins != 0 {
		t.Fatalf("command borrowed %d transactions before authority admission", begins)
	}
	close(release)
	select {
	case value := <-result:
		if value.err != nil || value.result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED {
			t.Fatalf("result=%#v err=%v", value.result, value.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not finish after authority admission")
	}
	if begins := backend.beginCount(); begins != 1 {
		t.Fatalf("command transactions=%d", begins)
	}
}

func TestProtocolV2IdentityAuthorityGateFailureAndTerminalResult(t *testing.T) {
	wantErr := errors.New("authority gate unavailable")
	tests := []struct {
		name      string
		runner    func(context.Context, func() error) error
		wantState hostv2.CommandState
		wantBegin int
	}{
		{
			name: "deny before callback",
			runner: func(context.Context, func() error) error {
				return wantErr
			},
			wantState: hostv2.CommandState_COMMAND_STATE_REJECTED,
		},
		{
			name: "post callback error cannot rewrite commit",
			runner: func(_ context.Context, run func() error) error {
				if err := run(); err != nil {
					return err
				}
				return wantErr
			},
			wantState: hostv2.CommandState_COMMAND_STATE_COMMITTED,
			wantBegin: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2CommandBackend()
			definition := testProtocolV2CommandDefinition(t, func(
				context.Context,
				pgx.Tx,
				*hostv2.CommandRequest,
				*protocolV2CommandPreparation,
			) (*protocolV2CommandExecution, error) {
				return testProtocolV2CommandExecution(t), nil
			})
			definition.RunAuthorityMutation = test.runner
			engine, err := newProtocolV2CommandEngine(backend, definition)
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.execute(t.Context(), testProtocolV2CommandRequest(t, "authority-result", "next"))
			if err != nil || result.GetState() != test.wantState || backend.beginCount() != test.wantBegin {
				t.Fatalf("result=%#v err=%v begins=%d", result, err, backend.beginCount())
			}
		})
	}
}

func TestProtocolV2IdentityAuthorityGateRejectsDuplicateAndEscapedCallbacks(t *testing.T) {
	tests := []struct {
		name string
		gate func(context.Context, func() error) error
	}{
		{
			name: "sequential duplicate",
			gate: func(_ context.Context, run func() error) error {
				_ = run()
				return run()
			},
		},
		{
			name: "concurrent duplicate",
			gate: func(_ context.Context, run func() error) error {
				start := make(chan struct{})
				results := make(chan error, 2)
				for range 2 {
					go func() {
						<-start
						results <- run()
					}()
				}
				close(start)
				_ = <-results
				return <-results
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newFakeProtocolV2CommandBackend()
			definition := testProtocolV2CommandDefinition(t, func(
				context.Context,
				pgx.Tx,
				*hostv2.CommandRequest,
				*protocolV2CommandPreparation,
			) (*protocolV2CommandExecution, error) {
				return testProtocolV2CommandExecution(t), nil
			})
			definition.RunAuthorityMutation = test.gate
			engine, err := newProtocolV2CommandEngine(backend, definition)
			if err != nil {
				t.Fatal(err)
			}
			result, err := engine.execute(t.Context(), testProtocolV2CommandRequest(t, "authority-duplicate", "next"))
			if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_COMMITTED || backend.beginCount() != 1 {
				t.Fatalf("result=%#v err=%v begins=%d", result, err, backend.beginCount())
			}
		})
	}

	backend := newFakeProtocolV2CommandBackend()
	definition := testProtocolV2CommandDefinition(t, func(
		context.Context,
		pgx.Tx,
		*hostv2.CommandRequest,
		*protocolV2CommandPreparation,
	) (*protocolV2CommandExecution, error) {
		return testProtocolV2CommandExecution(t), nil
	})
	var escaped func() error
	definition.RunAuthorityMutation = func(_ context.Context, run func() error) error {
		escaped = run
		return nil
	}
	engine, err := newProtocolV2CommandEngine(backend, definition)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.execute(t.Context(), testProtocolV2CommandRequest(t, "authority-escaped", "next"))
	if err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_REJECTED ||
		result.GetError().GetReason() != "host.command_authority_contract_invalid" || backend.beginCount() != 0 {
		t.Fatalf("escaped result=%#v err=%v begins=%d", result, err, backend.beginCount())
	}
	if escaped == nil || !errors.Is(escaped(), errProtocolV2CommandAuthorityGateContract) || backend.beginCount() != 0 {
		t.Fatalf("late callback err=%v begins=%d", escaped(), backend.beginCount())
	}
}

func TestProtocolV2IdentityAuthorityGateDrainsInflightCallback(t *testing.T) {
	backend := newFakeProtocolV2CommandBackend()
	started := make(chan struct{})
	release := make(chan struct{})
	callbackDone := make(chan error, 1)
	definition := testProtocolV2CommandDefinition(t, func(
		context.Context,
		pgx.Tx,
		*hostv2.CommandRequest,
		*protocolV2CommandPreparation,
	) (*protocolV2CommandExecution, error) {
		close(started)
		<-release
		return nil, errors.New("command effect failed")
	})
	definition.RunAuthorityMutation = func(_ context.Context, run func() error) error {
		go func() { callbackDone <- run() }()
		<-started
		return nil
	}
	engine, err := newProtocolV2CommandEngine(backend, definition)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan *hostv2.CommandResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, executeErr := engine.execute(t.Context(), testProtocolV2CommandRequest(t, "authority-inflight", "next"))
		resultCh <- result
		errCh <- executeErr
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("authority command callback did not start")
	}
	close(release)
	result := <-resultCh
	if err := <-errCh; err != nil || result.GetState() != hostv2.CommandState_COMMAND_STATE_ROLLED_BACK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if err := <-callbackDone; err != nil {
		t.Fatalf("callback result=%v", err)
	}
}

func TestProtocolV2IdentityAuthorityGatePreservesCallbackPanic(t *testing.T) {
	panicValue := "identity authority callback panic"
	var callbackErr error
	defer func() {
		if recovered := recover(); recovered != panicValue {
			t.Fatalf("recovered=%#v", recovered)
		}
		if !errors.Is(callbackErr, errProtocolV2CommandAuthorityGateContract) {
			t.Fatalf("callback err=%v", callbackErr)
		}
	}()
	_, _, _ = runProtocolV2CommandAuthorityMutation(
		t.Context(),
		func(_ context.Context, run func() error) error {
			callbackErr = run()
			return nil
		},
		func() (*hostv2.CommandResult, error) { panic(panicValue) },
	)
}

func TestProtocolV2IdentityAuthorityGateTransfersAsyncCallbackPanic(t *testing.T) {
	panicValue := "async identity authority callback panic"
	entered := make(chan struct{})
	defer func() {
		if recovered := recover(); recovered != panicValue {
			t.Fatalf("recovered=%#v", recovered)
		}
	}()
	_, _, _ = runProtocolV2CommandAuthorityMutation(
		t.Context(),
		func(_ context.Context, run func() error) error {
			go func() { _ = run() }()
			<-entered
			return nil
		},
		func() (*hostv2.CommandResult, error) {
			close(entered)
			panic(panicValue)
		},
	)
}

func TestProtocolV2IdentityAuthorityGateRejectsCanceledAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var calls atomic.Int32
	_, _, gateErr := runProtocolV2CommandAuthorityMutation(
		ctx,
		func(_ context.Context, run func() error) error { return run() },
		func() (*hostv2.CommandResult, error) {
			calls.Add(1)
			return &hostv2.CommandResult{}, nil
		},
	)
	if !errors.Is(gateErr, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("calls=%d gateErr=%v", calls.Load(), gateErr)
	}
}
