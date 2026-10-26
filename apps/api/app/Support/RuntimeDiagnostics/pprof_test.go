package runtimediagnostics_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	runtimediagnostics "github.com/keeptrue/sforum/apps/api/app/Support/RuntimeDiagnostics"
)

func TestStartPprofDisabledCreatesNoListener(t *testing.T) {
	server, err := runtimediagnostics.StartPprof(t.Context(), false, "0.0.0.0:0", nil)
	if err != nil || server != nil {
		t.Fatalf("disabled server=%v err=%v", server, err)
	}
}

func TestStartPprofRejectsNonLoopback(t *testing.T) {
	if _, err := runtimediagnostics.StartPprof(t.Context(), true, "0.0.0.0:6060", nil); err == nil {
		t.Fatal("expected non-loopback address rejection")
	}
}

func TestStartPprofServesHeapOnLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	server, err := runtimediagnostics.StartPprof(ctx, true, "127.0.0.1:0", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	response, err := http.Get("http://" + server.Addr() + "/debug/pprof/heap?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	cancel()
}
