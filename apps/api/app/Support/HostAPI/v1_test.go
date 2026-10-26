package hostapi

import (
	"context"
	"errors"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type fakeCaps struct {
	set  capabilities.Set
	jobs []string
	err  error
}

func (f fakeCaps) CapabilitiesFor(context.Context, string) (capabilities.Set, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.set, nil
}

func (f fakeCaps) DeclaredJobKinds(context.Context, string) ([]string, error) {
	return f.jobs, f.err
}

func (f fakeCaps) PluginJobContract(_ context.Context, extensionID, jobName string) (supportjobs.PluginJobContract, error) {
	if f.err != nil {
		return supportjobs.PluginJobContract{}, f.err
	}
	return supportjobs.PluginJobContract{
		ExtensionID: extensionID, ExtensionVersion: "1.0.0", ArtifactDigest: "artifact",
		JobName: jobName, JobContract: "demo.plugin.job.sync@1",
		PayloadSchemaID: "demo.sync.payload", PayloadSchemaVersion: "1",
	}, nil
}

type fakeSettings struct {
	values map[string]string
}

func (f fakeSettings) ListSettings(context.Context, string) (map[string]string, error) {
	return f.values, nil
}

type fakePerms struct {
	allowed bool
}

func (f fakePerms) HasPermission(context.Context, int64, string) (bool, error) {
	return f.allowed, nil
}

type fakeJobs struct {
	lastKind string
	lastExt  string
	contract supportjobs.PluginJobContract
	grant    string
	ctx      context.Context
	err      error
}

func (f *fakeJobs) EnqueueVersionedPluginJob(ctx context.Context, contract supportjobs.PluginJobContract, grant string, _ map[string]any) error {
	f.lastExt = contract.ExtensionID
	f.lastKind = contract.JobName
	f.contract = contract
	f.grant = grant
	f.ctx = ctx
	return f.err
}

func (f *fakeJobs) EnqueuePluginJob(_ context.Context, extensionID, kind string, _ map[string]any) error {
	f.lastExt = extensionID
	f.lastKind = kind
	return f.err
}

type fakeAudit struct {
	events []audit.Event
}

func (f *fakeAudit) Append(_ context.Context, event audit.Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestPingRequiresHostAPI(t *testing.T) {
	svc := New(Config{Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.SettingsOwn})}})
	resp := svc.Call(context.Background(), Request{Method: MethodPing, ExtensionID: "demo.plugin"})
	if resp.OK || resp.Reason != "host.capability_denied" {
		t.Fatalf("expected capability denied, got %#v", resp)
	}

	svc = New(Config{Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})}})
	resp = svc.Call(context.Background(), Request{Method: MethodPing, ExtensionID: "demo.plugin"})
	if !resp.OK || resp.Data["version"] != Version {
		t.Fatalf("expected ping ok, got %#v", resp)
	}
}

func TestGetSettings(t *testing.T) {
	svc := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.SettingsOwn})},
		Settings:     fakeSettings{values: map[string]string{"host": "smtp.example"}},
	})
	resp := svc.Call(context.Background(), Request{Method: MethodGetSettings, ExtensionID: "sforum.smtp"})
	if !resp.OK {
		t.Fatalf("expected ok: %#v", resp)
	}
	settings, _ := resp.Data["settings"].(map[string]any)
	if settings["host"] != "smtp.example" {
		t.Fatalf("settings: %#v", settings)
	}
}

func TestEnqueueOwnJobRejectsUndeclaredKind(t *testing.T) {
	jobs := &fakeJobs{}
	svc := New(Config{
		Capabilities: fakeCaps{
			set:  capabilities.NewSet([]string{capabilities.JobsEnqueue}),
			jobs: []string{"demo.sync"},
		},
		Jobs: jobs,
	})
	resp := svc.Call(context.Background(), Request{
		Method:      MethodEnqueueOwnJob,
		ExtensionID: "demo.plugin",
		Payload:     map[string]any{"kind": "evil.wipe"},
	})
	if resp.OK || resp.Reason != "host.job_kind_forbidden" {
		t.Fatalf("expected forbidden, got %#v", resp)
	}

	resp = svc.Call(context.Background(), Request{
		Method:      MethodEnqueueOwnJob,
		ExtensionID: "demo.plugin",
		Payload:     map[string]any{"kind": "demo.sync", "payload": map[string]any{"n": 1}},
	})
	if !resp.OK {
		t.Fatalf("expected enqueue ok: %#v", resp)
	}
	if jobs.lastKind != "demo.sync" || jobs.lastExt != "demo.plugin" {
		t.Fatalf("jobs: %#v", jobs)
	}
}

func TestAppendAuditNamespacesAction(t *testing.T) {
	writer := &fakeAudit{}
	svc := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.AuditAppend})},
		Auditor:      writer,
	})
	resp := svc.Call(context.Background(), Request{
		Method:      MethodAppendAudit,
		ExtensionID: "demo.plugin",
		Payload:     map[string]any{"action": "custom.event", "actorUserId": float64(9)},
	})
	if !resp.OK {
		t.Fatalf("expected ok: %#v", resp)
	}
	if len(writer.events) != 1 {
		t.Fatalf("events: %#v", writer.events)
	}
	if writer.events[0].Action != "extension.demo.plugin.custom.event" {
		t.Fatalf("action = %s", writer.events[0].Action)
	}
	if writer.events[0].ActorUserID != 9 {
		t.Fatalf("actor = %d", writer.events[0].ActorUserID)
	}
	if writer.events[0].Metadata["via"] != Version {
		t.Fatalf("via = %#v", writer.events[0].Metadata["via"])
	}
}

func TestCheckPermission(t *testing.T) {
	svc := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.PermissionsCheck})},
		Permissions:  fakePerms{allowed: true},
	})
	resp := svc.Call(context.Background(), Request{
		Method:      MethodCheckPermission,
		ExtensionID: "demo.plugin",
		Payload:     map[string]any{"userId": float64(1), "permission": "topic.create"},
	})
	if !resp.OK || resp.Data["allowed"] != true {
		t.Fatalf("expected allowed: %#v", resp)
	}
}

func TestCapabilitySourceError(t *testing.T) {
	svc := New(Config{Capabilities: fakeCaps{err: errors.New("boom")}})
	resp := svc.Call(context.Background(), Request{Method: MethodPing, ExtensionID: "x"})
	if resp.OK || resp.Reason != "host.extension_unavailable" {
		t.Fatalf("expected unavailable, got %#v", resp)
	}
}
