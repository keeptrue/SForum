package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/keeptrue/sforum/apps/api/config"
)

var (
	errWorkerLifecycleTerminalTest = errors.New("worker lease lost")
	errWorkerLifecycleStopTest     = errors.New("river stop failed")
)

func TestRunWorkerLifecycleTerminalFailureStopsAndClosesBeforeReturning(t *testing.T) {
	failures := make(chan error, 1)
	failures <- errWorkerLifecycleTerminalTest
	worker := &workerLifecycleTestDouble{failures: failures}

	err := runWorkerLifecycle(context.Background(), workerLifecycleTestConfig(), workerLifecycleTestLogger(), worker)
	if !errors.Is(err, errWorkerLifecycleTerminalTest) {
		t.Fatalf("terminal result = %v", err)
	}
	if got := worker.snapshot(); len(got) != 3 || got[0] != "start" || got[1] != "stop" || got[2] != "close" {
		t.Fatalf("lifecycle order = %#v", got)
	}
}

func TestRunWorkerLifecycleSignalShutdownWithNilFailureSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	worker := &workerLifecycleTestDouble{}

	if err := runWorkerLifecycle(ctx, workerLifecycleTestConfig(), workerLifecycleTestLogger(), worker); err != nil {
		t.Fatal(err)
	}
	if got := worker.snapshot(); len(got) != 3 || got[0] != "start" || got[1] != "stop" || got[2] != "close" {
		t.Fatalf("lifecycle order = %#v", got)
	}
}

func TestRunWorkerLifecycleClosedFailureSourceWaitsForSignal(t *testing.T) {
	failures := make(chan error)
	close(failures)
	ctx, cancel := context.WithCancel(context.Background())
	worker := &workerLifecycleTestDouble{failures: failures}
	done := make(chan error, 1)
	go func() {
		done <- runWorkerLifecycle(ctx, workerLifecycleTestConfig(), workerLifecycleTestLogger(), worker)
	}()

	select {
	case err := <-done:
		t.Fatalf("closed failure source stopped worker early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

func TestRunWorkerLifecycleJoinsTerminalAndRiverStopFailures(t *testing.T) {
	failures := make(chan error, 1)
	failures <- errWorkerLifecycleTerminalTest
	worker := &workerLifecycleTestDouble{failures: failures, stopErr: errWorkerLifecycleStopTest}

	err := runWorkerLifecycle(context.Background(), workerLifecycleTestConfig(), workerLifecycleTestLogger(), worker)
	if !errors.Is(err, errWorkerLifecycleTerminalTest) || !errors.Is(err, errWorkerLifecycleStopTest) {
		t.Fatalf("joined result = %v", err)
	}
	if worker.closeCalls != 1 {
		t.Fatalf("close calls = %d", worker.closeCalls)
	}
}

func workerLifecycleTestConfig() config.Config {
	return config.Config{WorkerShutdownTimeout: time.Second}
}

func workerLifecycleTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type workerLifecycleTestDouble struct {
	mu         sync.Mutex
	events     []string
	failures   <-chan error
	startErr   error
	stopErr    error
	closeCalls int
	closeOnce  sync.Once
}

func (worker *workerLifecycleTestDouble) Start(context.Context) error {
	worker.add("start")
	return worker.startErr
}

func (worker *workerLifecycleTestDouble) Stop(context.Context) error {
	worker.add("stop")
	return worker.stopErr
}

func (worker *workerLifecycleTestDouble) Failures() <-chan error {
	return worker.failures
}

func (worker *workerLifecycleTestDouble) Close() {
	worker.closeOnce.Do(func() {
		worker.mu.Lock()
		defer worker.mu.Unlock()
		worker.closeCalls++
		worker.events = append(worker.events, "close")
	})
}

func (worker *workerLifecycleTestDouble) add(event string) {
	worker.mu.Lock()
	worker.events = append(worker.events, event)
	worker.mu.Unlock()
}

func (worker *workerLifecycleTestDouble) snapshot() []string {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return append([]string(nil), worker.events...)
}
