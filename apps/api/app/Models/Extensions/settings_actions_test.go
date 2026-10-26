package extensions

import (
	"context"
	"errors"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
	settingslifecycle "github.com/keeptrue/sforum/apps/api/app/Support/SettingsLifecycle"
)

func TestSettingsActionRunsIsolatedProbeWithExplicitSecretSemantics(t *testing.T) {
	item := installedExtension("probe.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Status = StatusDisabled
	item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
	item.Manifest.Settings = []ManifestSetting{
		{Key: "host", Label: LocalizedText{Default: "Host"}, Type: "text", Default: "localhost"},
		{Key: "password", Label: LocalizedText{Default: "Password"}, Type: "secret"},
	}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{
		SchemaVersion: 1,
		UI:            extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"},
		Fields:        item.Manifest.Settings,
		Actions:       []extensionmanifest.SettingsAction{{ID: "probe", Kind: "provider_probe", Label: LocalizedText{Default: "Probe"}, Placement: "footer", UseDraftValues: true}},
		Explicit:      true,
	}
	store := &fakeExtensionStore{
		items:    map[string]Extension{item.ID: item},
		settings: map[string]map[string]string{item.ID: {"password": "stored-secret"}},
	}
	runtime := &recordingSettingsActionRuntime{result: SettingsActionProbeResult{OK: true, Reason: "ok", Message: "connected"}}
	auditor := &recordingAuditor{}
	service := NewService(store, t.TempDir())
	WithSettingsActionRuntime(runtime)(service)
	WithAuditor(auditor)(service)

	result, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values:  map[string]string{"host": "smtp.example.com"},
		Secrets: map[string]SettingsActionSecretInput{"password": {Mode: "preserve"}},
	})
	if err != nil || !result.Success {
		t.Fatalf("probe failed: result=%#v err=%v", result, err)
	}
	if runtime.values["host"] != "smtp.example.com" || runtime.values["password"] != "stored-secret" {
		t.Fatalf("unexpected probe values: %#v", runtime.values)
	}
	if len(auditor.events) != 1 {
		t.Fatalf("expected action audit, got %#v", auditor.events)
	}
	for _, value := range auditor.events[0].Metadata {
		if value == "stored-secret" {
			t.Fatal("secret leaked into audit metadata")
		}
	}
}

func TestSettingsActionUsesLifecycleSecretAndKeepsSecretSetVisible(t *testing.T) {
	item := installedExtension("probe.lifecycle", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Status = StatusDisabled
	item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
	item.Manifest.Settings = []ManifestSetting{
		{Key: "host", Label: LocalizedText{Default: "Host"}, Type: "text", Default: "localhost"},
		{Key: "password", Label: LocalizedText{Default: "Password"}, Type: "secret"},
	}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{
		SchemaVersion: 1,
		UI:            extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"},
		Fields:        item.Manifest.Settings,
		Actions:       []extensionmanifest.SettingsAction{{ID: "probe", Kind: "provider_probe", Label: LocalizedText{Default: "Probe"}, Placement: "footer", UseDraftValues: true}},
		Explicit:      true,
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := settingslifecycle.NewWithStore(settingslifecycle.NewMemoryDocumentStore(), secrets)
	runtime := &recordingSettingsActionRuntime{result: SettingsActionProbeResult{OK: true, Reason: "ok", Message: "connected"}}
	service := NewService(store, t.TempDir())
	WithSettingsLifecycle(lifecycle)(service)
	WithSettingsActionRuntime(runtime)(service)

	updated, err := service.UpdateSettings(context.Background(), extensionManager(), item.ID, UpdateSettingsInput{
		Values: map[string]string{"host": "smtp.example.com", "password": "stored-secret"},
	}, "zh-CN")
	if err != nil {
		t.Fatalf("UpdateSettings returned error: %v", err)
	}
	if settingValue(updated, "password") != "" || !updated.Items[1].SecretSet {
		t.Fatalf("saved secret should remain masked and visible as configured: %#v", updated.Items[1])
	}

	result, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values:  map[string]string{"host": "smtp.example.com"},
		Secrets: map[string]SettingsActionSecretInput{"password": {Mode: "preserve"}},
	})
	if err != nil || !result.Success {
		t.Fatalf("probe failed: result=%#v err=%v", result, err)
	}
	if runtime.values["password"] != "stored-secret" {
		t.Fatalf("probe must receive resolved secret plaintext, got %#v", runtime.values)
	}
	if strings.HasPrefix(runtime.values["password"], "sforum.secret://") {
		t.Fatalf("probe received secret reference instead of plaintext: %#v", runtime.values)
	}
}

func TestSettingsActionRejectsUnknownFieldsAndImplicitSecrets(t *testing.T) {
	item := installedExtension("probe-invalid.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
	item.Manifest.Settings = []ManifestSetting{{Key: "password", Label: LocalizedText{Default: "Password"}, Type: "secret"}}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{SchemaVersion: 1, UI: extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"}, Fields: item.Manifest.Settings, Actions: []extensionmanifest.SettingsAction{{ID: "probe", Kind: "provider_probe", Label: LocalizedText{Default: "Probe"}, Placement: "footer", UseDraftValues: true}}, Explicit: true}
	service := NewService(&fakeExtensionStore{items: map[string]Extension{item.ID: item}}, t.TempDir())
	WithSettingsActionRuntime(&recordingSettingsActionRuntime{})(service)

	_, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{Values: map[string]string{"unknown": "x"}})
	if !errors.Is(err, ErrSettingsActionInvalid) {
		t.Fatalf("expected invalid unknown field, got %v", err)
	}
	_, err = service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{})
	if !errors.Is(err, ErrSettingsActionInvalid) {
		t.Fatalf("expected explicit secret semantics, got %v", err)
	}
}

func TestSettingsActionPermissionAndUnknownAction(t *testing.T) {
	item := installedExtension("probe-policy.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{SchemaVersion: 1, UI: extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"}, Actions: []extensionmanifest.SettingsAction{{ID: "probe", Kind: "provider_probe", Label: LocalizedText{Default: "Probe"}, Placement: "footer"}}, Explicit: true}
	service := NewService(&fakeExtensionStore{items: map[string]Extension{item.ID: item}}, t.TempDir())
	WithSettingsActionRuntime(&recordingSettingsActionRuntime{})(service)
	if _, err := service.ExecuteSettingsAction(context.Background(), identity.Actor{ID: 99, Status: identity.UserStatusActive}, item.ID, "probe", ExecuteSettingsActionInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("expected permission denial, got %v", err)
	}
	if _, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "missing", ExecuteSettingsActionInput{}); !errors.Is(err, ErrSettingsActionInvalid) {
		t.Fatalf("expected unknown action rejection, got %v", err)
	}
}

func TestSettingsActionRejectsOversizedInputAndBoundsPluginFailure(t *testing.T) {
	item := installedExtension("probe-bounds.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
	item.Manifest.Settings = []ManifestSetting{{Key: "host", Label: LocalizedText{Default: "Host"}, Type: "text"}}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{
		SchemaVersion: 1, UI: extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"}, Fields: item.Manifest.Settings,
		Actions: []extensionmanifest.SettingsAction{{ID: "probe", Kind: "provider_probe", Label: LocalizedText{Default: "Probe"}, Placement: "footer", UseDraftValues: true}}, Explicit: true,
	}
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	runtime := &recordingSettingsActionRuntime{err: errors.New(strings.Repeat("provider failed ", 600))}
	service := NewService(store, t.TempDir())
	WithSettingsActionRuntime(runtime)(service)

	if _, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values: map[string]string{"host": strings.Repeat("x", maxSettingsActionInputBytes+1)},
	}); !errors.Is(err, ErrSettingsActionInvalid) {
		t.Fatalf("oversized draft must be rejected, got %v", err)
	}
	if _, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values: map[string]string{"host": "smtp.example.com"},
	}); !errors.Is(err, ErrSettingsActionUnavailable) {
		t.Fatalf("plugin failure must be host-owned unavailable result, got %v", err)
	}
}

func TestSettingsActionAIToolProbeForwardsDeclarationAndDraftValues(t *testing.T) {
	item := aiToolProbeExtension("probe-ai.plugin", "probe-ai.plugin.ai.lookup")
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	runtime := &recordingSettingsActionRuntime{result: SettingsActionProbeResult{OK: true, Reason: "extension.settings_action_ai_tool_ok", Message: "lookup ok"}}
	service := NewService(store, t.TempDir())
	WithSettingsActionRuntime(runtime)(service)

	result, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values: map[string]string{"endpoint": "https://api.example.com"},
	})
	if err != nil || !result.Success {
		t.Fatalf("ai tool probe failed: result=%#v err=%v", result, err)
	}
	if runtime.tool != "probe-ai.plugin.ai.lookup" || runtime.slot != "" {
		t.Fatalf("ai tool probe must target the declared tool, got tool=%q slot=%q", runtime.tool, runtime.slot)
	}
	if runtime.values["endpoint"] != "https://api.example.com" {
		t.Fatalf("draft values must reach the probe, got %#v", runtime.values)
	}
	if result.Reason != "extension.settings_action_ai_tool_ok" || result.Message != "lookup ok" {
		t.Fatalf("result must come from the tool result, got %#v", result)
	}
	if len(result.Details) != 0 || len(result.Suggestions) != 0 {
		t.Fatalf("ai tool path carries no details/suggestions, got %#v", result)
	}

	runtime.result = SettingsActionProbeResult{OK: false, Reason: "extension.settings_action_ai_tool_failed", Message: "bad endpoint"}
	failed, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values: map[string]string{"endpoint": "https://api.example.com"},
	})
	if err != nil || failed.Success || failed.Message != "bad endpoint" {
		t.Fatalf("tool-reported failure must be a bounded non-success result: result=%#v err=%v", failed, err)
	}

	// 传输层失败（探测 runtime 起不来、制品不受信任、调用超时）必须是宿主自己的
	// 不可用结果，而不是一条伪造的「工具说失败了」：两者的运营者动作完全不同——
	// 前者去查制品与运行日志，后者去改设置值。
	runtime.result = SettingsActionProbeResult{}
	runtime.err = errors.New("probe runtime is unavailable")
	if _, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
		Values: map[string]string{"endpoint": "https://api.example.com"},
	}); !errors.Is(err, ErrSettingsActionUnavailable) {
		t.Fatalf("transport failure must be a host-owned unavailable result, got %v", err)
	}
}

func TestSettingsActionAIToolProbeRequiresDeclaredTool(t *testing.T) {
	cases := []struct {
		name   string
		toolID string
		mutate func(*Extension)
	}{
		{name: "tool not declared", toolID: "probe-ai-missing.plugin.ai.lookup", mutate: func(item *Extension) { item.Manifest.AITools = nil }},
		{name: "tool not namespaced under extension", toolID: "other.plugin.ai.lookup"},
		{name: "tool empty", toolID: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			item := aiToolProbeExtension("probe-ai-missing.plugin", testCase.toolID)
			if testCase.mutate != nil {
				testCase.mutate(&item)
			}
			store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
			runtime := &recordingSettingsActionRuntime{result: SettingsActionProbeResult{OK: true, Reason: "ok", Message: "should not run"}}
			service := NewService(store, t.TempDir())
			WithSettingsActionRuntime(runtime)(service)

			if _, err := service.ExecuteSettingsAction(context.Background(), extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{
				Values: map[string]string{"endpoint": "https://api.example.com"},
			}); !errors.Is(err, ErrSettingsActionUnavailable) {
				t.Fatalf("an undeclared/foreign tool must be unavailable, got %v", err)
			}
			if runtime.values != nil {
				t.Fatalf("undeclared tool must not start a probe runtime, got %#v", runtime.values)
			}
		})
	}
}

// 设置页视图必须按 kind 计算可用性：AI 工具探测不需要 provider slot，
// 但 provider 探测仍然需要。
func TestSettingsViewAvailabilityIsKindAware(t *testing.T) {
	const toolID = "probe-view.plugin.ai.lookup"
	item := aiToolProbeExtension("probe-view.plugin", toolID)
	item.Manifest.SettingsDocument.Actions = append(item.Manifest.SettingsDocument.Actions, extensionmanifest.SettingsAction{
		ID: "provider", Kind: extensionmanifest.SettingsActionProviderProbe, Label: LocalizedText{Default: "Probe provider"}, Placement: "footer",
	})
	service := NewService(&fakeExtensionStore{items: map[string]Extension{item.ID: item}}, t.TempDir())

	settings, err := service.Settings(context.Background(), extensionManager(), item.ID, "zh-CN")
	if err != nil {
		t.Fatalf("Settings returned error: %v", err)
	}
	byID := map[string]ExtensionSettingsAction{}
	for _, action := range settings.Actions {
		byID[action.ID] = action
	}
	if tool := byID["probe"]; !tool.Available || tool.Tool != toolID {
		t.Fatalf("ai_tool_probe must be available and carry its tool: %#v", tool)
	}
	if provider := byID["provider"]; provider.Available || provider.UnavailableReason != "extension.settings_action_unavailable" {
		t.Fatalf("provider_probe without a provider slot must be unavailable: %#v", provider)
	}
}

// aiToolProbeExtension 构造一个只声明 AI 工具（没有 provider slot）的插件，
// 设置页动作 kind 为 ai_tool_probe。
func aiToolProbeExtension(id string, toolID string) Extension {
	item := installedExtension(id, TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Manifest.Settings = []ManifestSetting{
		{Key: "endpoint", Label: LocalizedText{Default: "Endpoint"}, Type: "text", Default: "https://api.example.com"},
	}
	item.Manifest.AITools = []ManifestAITool{{
		ID: toolID, ContractVersion: toolID + "@1", Name: "probe-ai-lookup", Description: "Look up a record.",
		Parameters: []byte(`{"type":"object"}`), Handler: toolID, TimeoutMS: 1000,
	}}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{
		SchemaVersion: 1,
		UI:            extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"},
		Fields:        item.Manifest.Settings,
		Actions: []extensionmanifest.SettingsAction{{
			ID: "probe", Kind: extensionmanifest.SettingsActionAIToolProbe, Label: LocalizedText{Default: "Probe"},
			Placement: "footer", UseDraftValues: true, Fields: []string{"endpoint"}, Tool: toolID,
		}},
		Explicit: true,
	}
	return item
}

func TestSettingsActionHonorsCanceledRequestContext(t *testing.T) {
	item := installedExtension("probe-timeout.plugin", TypePlugin, ManifestBackend{Entry: "backend/plugin"})
	item.Manifest.Providers = []ManifestProvider{{Slot: "mail.provider", Label: "Mail"}}
	item.Manifest.SettingsDocument = extensionmanifest.SettingsDocument{
		SchemaVersion: 1, UI: extensionmanifest.SettingsUI{Mode: "schema", Layout: "form"},
		Actions: []extensionmanifest.SettingsAction{{ID: "probe", Kind: "provider_probe", Label: LocalizedText{Default: "Probe"}, Placement: "footer"}}, Explicit: true,
	}
	service := NewService(&fakeExtensionStore{items: map[string]Extension{item.ID: item}}, t.TempDir())
	WithSettingsActionRuntime(settingsActionRuntimeFunc(func(ctx context.Context, _ Extension, _ SettingsActionProbeRequest) (SettingsActionProbeResult, error) {
		<-ctx.Done()
		return SettingsActionProbeResult{}, ctx.Err()
	}))(service)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.ExecuteSettingsAction(ctx, extensionManager(), item.ID, "probe", ExecuteSettingsActionInput{}); !errors.Is(err, ErrSettingsActionUnavailable) {
		t.Fatalf("canceled/timeout context must stop the probe, got %v", err)
	}
}

type settingsActionRuntimeFunc func(context.Context, Extension, SettingsActionProbeRequest) (SettingsActionProbeResult, error)

func (fn settingsActionRuntimeFunc) ProbeSettingsAction(ctx context.Context, extension Extension, request SettingsActionProbeRequest) (SettingsActionProbeResult, error) {
	return fn(ctx, extension, request)
}

type recordingSettingsActionRuntime struct {
	values map[string]string
	slot   string
	tool   string
	result SettingsActionProbeResult
	err    error
}

func (r *recordingSettingsActionRuntime) ProbeSettingsAction(_ context.Context, _ Extension, request SettingsActionProbeRequest) (SettingsActionProbeResult, error) {
	r.values = request.Values
	r.slot = request.ProviderSlot
	r.tool = request.AIToolID
	return r.result, r.err
}
