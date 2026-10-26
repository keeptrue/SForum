package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
	toolplugin "github.com/keeptrue/sforum/apps/api/app/Support/AI/toolplugin"
)

type bootstrapPluginToolInvoker struct {
	calls []toolplugin.PluginToolCall
}

func (i *bootstrapPluginToolInvoker) InvokeVersionedAITool(
	_ context.Context,
	_ extensions.Extension,
	call toolplugin.PluginToolCall,
) (toolplugin.PluginToolResult, error) {
	i.calls = append(i.calls, call)
	return toolplugin.PluginToolResult{Content: "ok"}, nil
}

func bootstrapAIToolPlugin(id, name string) extensions.Extension {
	extension := extensions.Extension{
		ID: id, Name: id, Version: "1.0.0", Type: extensions.TypePlugin,
		Status: extensions.StatusEnabled, PackageDigest: "digest",
	}
	extension.Manifest.AITools = []extensions.ManifestAITool{{
		ID: id + ".lookup", ContractVersion: id + ".lookup@1", Name: name,
		Description: "Look something up.",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Handler:     id + ".lookup", TimeoutMS: 1000,
	}}
	return extension
}

func TestRegisterPluginAIToolsRegistersOnlyValidPluginDeclarations(t *testing.T) {
	registry := supportai.NewToolRegistry()
	invoker := &bootstrapPluginToolInvoker{}

	valid := bootstrapAIToolPlugin("plugin.ai-tool.valid", "valid-order-lookup")
	invalid := bootstrapAIToolPlugin("plugin.ai-tool.invalid", "invalid-order-lookup")
	invalid.Manifest.AITools[0].Name = "invalid.order.lookup" // 点号：供应商会直接 400
	theme := bootstrapAIToolPlugin("theme.ai-tool", "theme-order-lookup")
	theme.Type = extensions.TypeTheme
	undeclared := extensions.Extension{ID: "plugin.plain", Type: extensions.TypePlugin}
	disabled := bootstrapAIToolPlugin("plugin.ai-tool.disabled", "disabled-order-lookup")
	disabled.Status = extensions.StatusDisabled

	failures := registerPluginAITools(registry, invoker, []extensions.Extension{valid, invalid, theme, undeclared, disabled})
	if len(failures) != 1 {
		t.Fatalf("invalid declaration should be reported once, got %#v", failures)
	}
	if _, ok := registry.Lookup("valid-order-lookup"); !ok {
		t.Fatal("valid plugin tool must be registered in the host tool registry")
	}
	for _, absent := range []string{"invalid-order-lookup", "theme-order-lookup", "disabled-order-lookup"} {
		if _, ok := registry.Lookup(absent); ok {
			t.Fatalf("%s must not be registered", absent)
		}
	}
	// 登记后必须能被模型侧白名单枚举到：Descriptors 只回白名单内的工具。
	descriptors := registry.Descriptors([]string{"valid-order-lookup", "missing-tool"})
	if len(descriptors) != 1 || descriptors[0].Name != "valid-order-lookup" ||
		descriptors[0].Description != "Look something up." {
		t.Fatalf("descriptors = %#v", descriptors)
	}
}

func TestRegisterPluginAIToolsIsInertWithoutRegistryOrInvoker(t *testing.T) {
	plugin := bootstrapAIToolPlugin("plugin.ai-tool.valid", "valid-order-lookup")
	if failures := registerPluginAITools(nil, &bootstrapPluginToolInvoker{}, []extensions.Extension{plugin}); failures != nil {
		t.Fatalf("nil registry must be inert, got %#v", failures)
	}
	registry := supportai.NewToolRegistry()
	if failures := registerPluginAITools(registry, nil, []extensions.Extension{plugin}); failures != nil {
		t.Fatalf("nil invoker must be inert, got %#v", failures)
	}
	if _, ok := registry.Lookup("valid-order-lookup"); ok {
		t.Fatal("nothing may be registered without an invoker")
	}
}

// —— 登记表热重建：期望集应用成功后不必重启进程 ——

type bootstrapExtensionLister struct {
	items []extensions.Extension
	err   error
}

func (l *bootstrapExtensionLister) List(context.Context) ([]extensions.Extension, error) {
	if l.err != nil {
		return nil, l.err
	}
	return l.items, nil
}

// bootstrapStaticTool 代表一个 Core 内置工具：无状态、进程内，重建时原样复用。
type bootstrapStaticTool struct {
	name        string
	description string
}

func (t bootstrapStaticTool) Definition() supportai.ToolDefinition {
	return supportai.ToolDefinition{Name: t.name, Description: t.description}
}

func (t bootstrapStaticTool) Invoke(context.Context, supportai.ToolCall, supportai.ToolContext) (supportai.ToolResult, error) {
	return supportai.ToolResult{}, nil
}

func TestAIToolRefresherFollowsExtensionTogglesWithoutRestart(t *testing.T) {
	core := []supportai.Tool{bootstrapStaticTool{name: "core-lookup", description: "core"}}
	registry := supportai.NewToolRegistry()
	if err := registry.Replace(core); err != nil {
		t.Fatalf("seed core tools: %v", err)
	}
	plugin := bootstrapAIToolPlugin("plugin.ai-tool.valid", "valid-order-lookup")
	lister := &bootstrapExtensionLister{items: []extensions.Extension{plugin}}
	refresher := &aiToolRefresher{
		registry: registry,
		core:     core,
		invoker:  &bootstrapPluginToolInvoker{},
		store:    lister,
	}

	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after enable: %v", err)
	}
	if _, ok := registry.Lookup("valid-order-lookup"); !ok {
		t.Fatal("an enabled plugin tool must appear without restarting the API process")
	}
	if _, ok := registry.Lookup("core-lookup"); !ok {
		t.Fatal("core tools must survive the rebuild")
	}

	// 停用：下一次刷新必须把它摘掉，而不是留下「看得见、调不动」的工具。
	disabled := plugin
	disabled.Status = extensions.StatusDisabled
	lister.items = []extensions.Extension{disabled}
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after disable: %v", err)
	}
	if _, ok := registry.Lookup("valid-order-lookup"); ok {
		t.Fatal("a disabled plugin tool must disappear from the registry")
	}
	if _, ok := registry.Lookup("core-lookup"); !ok {
		t.Fatal("core tools must survive a disable rebuild")
	}

	// 升级到新版本：登记表里的来源版本必须跟着走，否则控制台会指向旧制品。
	upgraded := plugin
	upgraded.Version = "1.1.0"
	lister.items = []extensions.Extension{upgraded}
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after upgrade: %v", err)
	}
	available := registry.Available()
	if len(available) != 2 || available[1].Origin.Version != "1.1.0" {
		t.Fatalf("origin version must follow the upgrade: %#v", available)
	}
}

func TestAIToolRefresherKeepsPreviousRegistryWhenListingFails(t *testing.T) {
	core := []supportai.Tool{bootstrapStaticTool{name: "core-lookup", description: "core"}}
	registry := supportai.NewToolRegistry()
	plugin := bootstrapAIToolPlugin("plugin.ai-tool.valid", "valid-order-lookup")
	lister := &bootstrapExtensionLister{items: []extensions.Extension{plugin}}
	refresher := &aiToolRefresher{
		registry: registry,
		core:     core,
		invoker:  &bootstrapPluginToolInvoker{},
		store:    lister,
	}
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	lister.err = errors.New("inventory unavailable")
	if err := refresher.Refresh(context.Background()); err == nil {
		t.Fatal("a failed refresh must report the error")
	}
	// 换表失败不该退化成「站内没有工具」：旧表继续服务请求。
	if _, ok := registry.Lookup("valid-order-lookup"); !ok {
		t.Fatal("a failed refresh must keep the previous registry")
	}

	// 没有 invoker 时刷新是 no-op：它无法构建任何插件工具，因此不能拿空集合换表。
	refresher.invoker = nil
	if err := refresher.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh without invoker must be inert: %v", err)
	}
	if _, ok := registry.Lookup("valid-order-lookup"); !ok {
		t.Fatal("a refresh without invoker must not wipe the registry")
	}
}

func TestMergeAIToolSetsKeepsCoreOnNameCollision(t *testing.T) {
	core := []supportai.Tool{bootstrapStaticTool{name: "shared-lookup", description: "core"}}
	plugins := []supportai.Tool{
		bootstrapStaticTool{name: "shared-lookup", description: "plugin"},
		bootstrapStaticTool{name: "plugin-only", description: "plugin"},
	}
	merged := mergeAIToolSets(core, plugins)
	if len(merged) != 2 {
		t.Fatalf("merged = %#v", merged)
	}
	if merged[0].Definition().Description != "core" {
		t.Fatal("a plugin must never replace a core tool of the same name")
	}
	if merged[1].Definition().Name != "plugin-only" {
		t.Fatalf("plugin tool must stay in the merged set: %#v", merged)
	}
}

func TestAIToolRefreshHookRunsOnlyWhenArmed(t *testing.T) {
	hook := &aiToolRefreshHook{}
	// 未装配时是 no-op：第一个期望集应用之前，装配期那次登记就是当下状态的答案。
	hook.AfterApply(context.Background(), extensions.PluginRuntimePublication{Revision: 1})

	runs := 0
	hook.Arm(func(context.Context) error { runs++; return nil }, nil)
	hook.AfterApply(context.Background(), extensions.PluginRuntimePublication{Revision: 2})
	if runs != 1 {
		t.Fatalf("armed hook must run once, got %d", runs)
	}

	// 刷新失败只降级为告警，绝不冒泡到协调器。
	hook.Arm(func(context.Context) error { return errors.New("boom") }, nil)
	hook.AfterApply(context.Background(), extensions.PluginRuntimePublication{Revision: 3})
}

// bootstrapStubFullSetApplier 是协调器适配器的最小 double：它只回答「应用成功
// 还是失败」，用于钉住装饰器的 after-apply 时机。
type bootstrapStubFullSetApplier struct {
	applied []extensions.PluginRuntimeAppliedMember
	err     error
	calls   int
}

func (a *bootstrapStubFullSetApplier) ApplyPluginRuntimeFullSet(
	context.Context,
	extensions.PluginRuntimePublication,
) ([]extensions.PluginRuntimeAppliedMember, error) {
	a.calls++
	return a.applied, a.err
}

func TestAIToolRefreshApplierTriggersOnlyAfterSuccessfulApply(t *testing.T) {
	if got := withAIToolRefreshApplier(&bootstrapStubFullSetApplier{}, nil); got == nil {
		t.Fatal("a nil hook must keep the original applier")
	}

	refreshes := 0
	hook := &aiToolRefreshHook{}
	hook.Arm(func(context.Context) error { refreshes++; return nil }, nil)

	inner := &bootstrapStubFullSetApplier{err: errors.New("apply failed")}
	applier := withAIToolRefreshApplier(inner, hook)
	if _, err := applier.ApplyPluginRuntimeFullSet(context.Background(), extensions.PluginRuntimePublication{Revision: 7}); err == nil {
		t.Fatal("apply failure must be reported")
	}
	if inner.calls != 1 || refreshes != 0 {
		t.Fatalf("a failed apply must not rebuild the registry: calls=%d refreshes=%d", inner.calls, refreshes)
	}

	inner.err = nil
	inner.applied = []extensions.PluginRuntimeAppliedMember{{
		PluginRuntimeMember: extensions.PluginRuntimeMember{ExtensionID: "sforum.web-search"},
		RuntimeInstanceID:   "runtime-1",
	}}
	applied, err := applier.ApplyPluginRuntimeFullSet(context.Background(), extensions.PluginRuntimePublication{Revision: 8})
	if err != nil || len(applied) != 1 || applied[0].ExtensionID != "sforum.web-search" {
		t.Fatalf("applied evidence must pass through unchanged: %#v / %v", applied, err)
	}
	if refreshes != 1 {
		t.Fatalf("a successful apply must rebuild the registry exactly once, got %d", refreshes)
	}
}
