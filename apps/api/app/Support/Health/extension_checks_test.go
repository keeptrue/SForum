package health

import (
	"context"
	"encoding/json"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type fakeContribs struct {
	items []extensions.EffectiveContribution
}

func (f fakeContribs) EffectiveContributions(context.Context) ([]extensions.EffectiveContribution, error) {
	return f.items, nil
}

type fakeRuntime struct {
	state string
}

func (f fakeRuntime) Status(context.Context, extensions.Extension) extensions.RuntimeStatus {
	return extensions.RuntimeStatus{State: f.state}
}

func TestEvaluateWithExtensionContributionsMergesStaticAndRuntime(t *testing.T) {
	staticPayload, _ := json.Marshal(extensionmanifest.HealthCheckContributionPayload{
		Type: "static", Component: "plugin.demo.static",
	})
	runtimePayload, _ := json.Marshal(extensionmanifest.HealthCheckContributionPayload{
		Type: "extensionRuntime", Component: "plugin.demo.runtime", Required: false,
	})
	source := fakeContribs{items: []extensions.EffectiveContribution{
		{ExtensionID: "demo.a", Point: extensionmanifest.PointSystemHealthChecks, ID: "static", Payload: staticPayload},
		{ExtensionID: "demo.b", Point: extensionmanifest.PointSystemHealthChecks, ID: "rt", Payload: runtimePayload},
		{ExtensionID: "demo.c", Point: extensionmanifest.PointForumTopicActions, ID: "ignored", Payload: staticPayload},
	}}
	report := EvaluateWithExtensionContributions(
		context.Background(),
		[]Checker{FuncChecker{ComponentName: "postgres", IsRequired: true, Fn: func(context.Context) error { return nil }}},
		source,
		fakeRuntime{state: extensions.RuntimeDegraded},
	)
	if !report.Ready {
		t.Fatalf("expected ready, got %#v", report)
	}
	if report.Status != StatusDegraded {
		t.Fatalf("expected degraded from runtime component, status=%s components=%#v", report.Status, report.Components)
	}
	names := map[string]string{}
	for _, c := range report.Components {
		names[c.Name] = c.Status
	}
	if names["postgres"] != StatusOK {
		t.Fatalf("postgres: %#v", names)
	}
	if names["plugin.demo.static"] != StatusOK {
		t.Fatalf("static: %#v", names)
	}
	if names["plugin.demo.runtime"] != StatusDegraded {
		t.Fatalf("runtime: %#v", names)
	}
}

func TestRequiredExtensionHealthCanFailReady(t *testing.T) {
	payload, _ := json.Marshal(extensionmanifest.HealthCheckContributionPayload{
		Type: "extensionRuntime", Component: "plugin.must", Required: true,
	})
	source := fakeContribs{items: []extensions.EffectiveContribution{
		{ExtensionID: "must.plugin", Point: extensionmanifest.PointSystemHealthChecks, ID: "must", Payload: payload},
	}}
	report := EvaluateWithExtensionContributions(
		context.Background(),
		[]Checker{FuncChecker{ComponentName: "postgres", IsRequired: true, Fn: func(context.Context) error { return nil }}},
		source,
		fakeRuntime{state: extensions.RuntimeFailed},
	)
	if report.Ready || report.Status != "not_ready" {
		t.Fatalf("expected not_ready, got %#v", report)
	}
}
