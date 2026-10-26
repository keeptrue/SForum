package routes

import (
	"net/http"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ValidTerminalResponseStatus keeps informational responses out of terminal
// HTTP paths. WebSocket is the sole exception and must use the exact 101
// upgrade response declared by its route mode.
func ValidTerminalResponseStatus(mode string, status int) bool {
	if mode == extensionmanifest.RouteModeWebSocket {
		return status == http.StatusSwitchingProtocols
	}
	switch mode {
	case extensionmanifest.RouteModeHTTP, extensionmanifest.RouteModeMultipart,
		extensionmanifest.RouteModeSSE, extensionmanifest.RouteModeStream:
		return status >= http.StatusOK && status <= 599
	default:
		return false
	}
}
