package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
)

func TestAPIRuntimeCoordinatorSupervisorClosesAdmissionBeforeReporting(t *testing.T) {
	release := make(chan struct{})
	coordinator := startWorkerRuntimeOwnerTestCoordinator(t, func(context.Context) error {
		<-release
		return errWorkerRuntimeCoordinatorTerminalTest
	})
	var closed atomic.Bool
	failures, done := superviseAPIPluginRuntimeCoordinator(coordinator, func() { closed.Store(true) })
	close(release)

	select {
	case runtimeErr, ok := <-failures:
		if !ok || !errors.Is(runtimeErr, errWorkerRuntimeCoordinatorTerminalTest) {
			t.Fatalf("terminal failure = %v, open=%v", runtimeErr, ok)
		}
		if !closed.Load() {
			t.Fatal("runtime admission remained open when failure became observable")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for API runtime failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("API runtime supervisor did not finish")
	}
}

func TestAPIRuntimeCoordinatorSupervisorSafeModeHasNoFailureSource(t *testing.T) {
	failures, done := superviseAPIPluginRuntimeCoordinator(newInactivePluginRuntimeCoordinatorRuntime(), func() {
		t.Fatal("Safe Mode closed a runtime that was never started")
	})
	if failures != nil || done != nil {
		t.Fatalf("Safe Mode supervisor channels = %#v, %#v", failures, done)
	}
}

func TestMergeAPIRuntimeFailureSourcesReportsTerminalFailure(t *testing.T) {
	closed := make(chan error)
	source := make(chan error, 1)
	merged, cancel := mergeAPIRuntimeFailureSources(closed, source)
	t.Cleanup(cancel)
	close(closed)
	source <- errWorkerRuntimeCoordinatorTerminalTest

	select {
	case runtimeErr, ok := <-merged:
		if !ok || !errors.Is(runtimeErr, errWorkerRuntimeCoordinatorTerminalTest) {
			t.Fatalf("merged runtime failure=%v open=%t", runtimeErr, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for merged runtime failure")
	}
	select {
	case _, ok := <-merged:
		if ok {
			t.Fatal("merged failure source remained open after terminal failure")
		}
	case <-time.After(time.Second):
		t.Fatal("merged failure source did not close")
	}
}

func TestMergeAPIRuntimeFailureSourcesHandlesConcurrentShutdown(t *testing.T) {
	const sourceCount = 64
	sources := make([]<-chan error, 0, sourceCount)
	writable := make([]chan error, 0, sourceCount)
	for range sourceCount {
		source := make(chan error, 1)
		writable = append(writable, source)
		sources = append(sources, source)
	}
	merged, cancel := mergeAPIRuntimeFailureSources(sources...)
	for index, source := range writable {
		if index == sourceCount/2 {
			source <- errWorkerRuntimeCoordinatorTerminalTest
		}
		close(source)
	}
	select {
	case runtimeErr, ok := <-merged:
		if !ok || !errors.Is(runtimeErr, errWorkerRuntimeCoordinatorTerminalTest) {
			t.Fatalf("concurrent merged failure=%v open=%t", runtimeErr, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for concurrent merged failure")
	}
	cancel()
}

func TestMergeAPIRuntimeFailureSourcesWithoutSourcesIsInactive(t *testing.T) {
	merged, cancel := mergeAPIRuntimeFailureSources(nil, nil)
	defer cancel()
	if merged != nil {
		t.Fatalf("inactive merge returned failure source %#v", merged)
	}
}

func TestPluginRuntimeRecoveryMessageDoesNotExposePrivateDiagnostics(t *testing.T) {
	cause := errors.Join(
		pluginbootstrap.ErrBootstrapABIIncompatible,
		errors.New("/private/operator/path/backend/plugin"),
	)
	if message := pluginRuntimeRecoveryMessage(cause); message != "plugin process bootstrap ABI is incompatible" {
		t.Fatalf("public recovery message=%q", message)
	}
}
