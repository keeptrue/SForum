package health

import (
	"context"
	"encoding/json"
	"strings"
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

// 匿名 /ready 不回显插件运行时错误的原始文本：LastError 是插件进程启动失败的
// err.Error() 原文，可能带制品路径 / 版本 / digest。探针只需要知道「这个组件失败了」。
type fakeRuntimeWithError struct {
	state     string
	lastError string
}

func (f fakeRuntimeWithError) Status(context.Context, extensions.Extension) extensions.RuntimeStatus {
	return extensions.RuntimeStatus{State: f.state, LastError: f.lastError}
}

func TestExtensionRuntimeHealthDoesNotEchoRawRuntimeError(t *testing.T) {
	payload, _ := json.Marshal(extensionmanifest.HealthCheckContributionPayload{
		Type: "extensionRuntime", Component: "plugin.secret.runtime", Required: false,
	})
	source := fakeContribs{items: []extensions.EffectiveContribution{
		{ExtensionID: "secret.plugin", Point: extensionmanifest.PointSystemHealthChecks, ID: "rt", Payload: payload},
	}}
	digest := "d1e5" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	report := EvaluateWithExtensionContributions(
		context.Background(),
		[]Checker{FuncChecker{ComponentName: "postgres", IsRequired: true, Fn: func(context.Context) error { return nil }}},
		source,
		fakeRuntimeWithError{
			state:     extensions.RuntimeFailed,
			lastError: "fork/exec /app/storage/extensions/secret.plugin/1.2.3/" + digest + "/plugin: permission denied",
		},
	)
	var component ComponentResult
	for _, candidate := range report.Components {
		if candidate.Name == "plugin.secret.runtime" {
			component = candidate
		}
	}
	if component.Name == "" {
		t.Fatalf("runtime component missing: %#v", report.Components)
	}
	if component.Status != StatusError {
		t.Fatalf("expected error status, got %q", component.Status)
	}
	if component.Error != "extension runtime reported a failure" {
		t.Fatalf("anonymous readiness must carry a summary, got %q", component.Error)
	}
	serialized, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(serialized), digest) || strings.Contains(string(serialized), "/app/storage/extensions") {
		t.Fatalf("runtime error fingerprint leaked into readiness: %s", serialized)
	}
}
