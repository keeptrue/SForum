package routes

import (
	"errors"
	"net/http"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestValidTerminalResponseStatusIsModeExact(t *testing.T) {
	for _, mode := range []string{
		extensionmanifest.RouteModeHTTP,
		extensionmanifest.RouteModeMultipart,
		extensionmanifest.RouteModeSSE,
		extensionmanifest.RouteModeStream,
	} {
		for _, status := range []int{http.StatusContinue, http.StatusSwitchingProtocols, http.StatusEarlyHints} {
			if ValidTerminalResponseStatus(mode, status) {
				t.Fatalf("mode %q accepted informational status %d", mode, status)
			}
		}
		for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusMovedPermanently, 599} {
			if !ValidTerminalResponseStatus(mode, status) {
				t.Fatalf("mode %q rejected terminal status %d", mode, status)
			}
		}
	}

	for _, status := range []int{http.StatusContinue, http.StatusOK, 599} {
		if ValidTerminalResponseStatus(extensionmanifest.RouteModeWebSocket, status) {
			t.Fatalf("websocket accepted status %d", status)
		}
	}
	if !ValidTerminalResponseStatus(extensionmanifest.RouteModeWebSocket, http.StatusSwitchingProtocols) {
		t.Fatal("websocket rejected status 101")
	}
	if ValidTerminalResponseStatus("forged", http.StatusOK) {
		t.Fatal("unknown mode accepted a terminal status")
	}
}

func TestRouteMutationRejectsInformationalTerminalStatus(t *testing.T) {
	response := DispatchResponse{Status: http.StatusOK, Headers: http.Header{}, Body: []byte(`{}`)}
	for _, status := range []int{http.StatusContinue, http.StatusSwitchingProtocols, http.StatusEarlyHints} {
		operation := RoutePatchOperation{
			Kind: RoutePatchReplace, Path: "/status", Value: routePatchValue(t, status),
		}
		if _, err := applyRouteResponsePatch(
			response, []RoutePatchOperation{operation}, []string{"/status"},
		); !errors.Is(err, ErrRouteMutation) {
			t.Fatalf("status=%d error=%v", status, err)
		}
	}
}
