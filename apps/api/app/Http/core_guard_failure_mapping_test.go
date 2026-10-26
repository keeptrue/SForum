package http

import (
	"errors"
	"testing"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestProductionRouteGuardErrorPreservesTypedPluginEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		kind     routes.PluginGuardFailureKind
		observed bool
		want     error
	}{
		{name: "denied", kind: routes.PluginGuardFailureDenied, observed: true, want: ErrRoutePermissionDenied},
		{name: "unavailable", kind: routes.PluginGuardFailureUnavailable, want: ErrRouteGuardUnavailable},
		{name: "crash", kind: routes.PluginGuardFailureCrash, observed: true, want: ErrRouteGuardUnavailable},
		{name: "timeout", kind: routes.PluginGuardFailureTimeout, observed: true, want: ErrRouteGuardUnavailable},
		{name: "protocol", kind: routes.PluginGuardFailureProtocol, observed: true, want: ErrRouteGuardUnavailable},
		{name: "canceled", kind: routes.PluginGuardFailureCanceled, observed: true, want: ErrRouteGuardUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := routes.NewPluginGuardFailure(test.kind, test.observed)
			err := productionRouteGuardError(input)
			var evidence *routes.PluginGuardFailure
			if !errors.Is(err, test.want) || !errors.As(err, &evidence) ||
				evidence.Kind() != test.kind || evidence.RuntimeExecutionObserved() != test.observed {
				t.Fatalf("error=%v evidence=%#v", err, evidence)
			}
		})
	}
}
