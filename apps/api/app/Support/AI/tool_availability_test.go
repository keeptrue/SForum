package ai_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	ai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

// metadataTool 只实现必备接口（Definition + Invoke）。登记表必须容忍这种工具：
// Core 内置工具与旧版插件工具都只有这两项声明。
type metadataTool struct {
	definition ai.ToolDefinition
}

func (t *metadataTool) Definition() ai.ToolDefinition { return t.definition }

func (t *metadataTool) Invoke(context.Context, ai.ToolCall, ai.ToolContext) (ai.ToolResult, error) {
	return ai.ToolResult{Content: "ok"}, nil
}

// pluginMetadataTool 模拟新版插件工具：来源、展示文案、超时、参数四项声明齐全。
// DisplayDescriptions 故意直接返回自己的 map——登记表必须拷贝，否则调用方能改到它。
type pluginMetadataTool struct {
	metadataTool
	origin       ai.ToolOrigin
	display      map[string]string
	timeoutMS    int
	settingsPath string
}

func (t *pluginMetadataTool) Origin() ai.ToolOrigin { return t.origin }

func (t *pluginMetadataTool) DisplayDescriptions() map[string]string { return t.display }

func (t *pluginMetadataTool) TimeoutMS() int { return t.timeoutMS }

func (t *pluginMetadataTool) SettingsPath() string { return t.settingsPath }

func newMetadataTool(name, description, parameters string) *metadataTool {
	return &metadataTool{definition: ai.ToolDefinition{
		Name:        name,
		Description: description,
		Parameters:  json.RawMessage(parameters),
	}}
}

// Core 工具没有插件清单可依：来源是 core、没有本地化文案、没有插件超时声明。
// 参数摘要仍然要填——运营者判断「这个工具要传什么」只有这一处来源。
func TestToolRegistryAvailableFillsCoreMetadata(t *testing.T) {
	registry := ai.NewToolRegistry()
	tool := newMetadataTool("forum-search", "检索主题与评论",
		`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`)
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	available := registry.Available()
	if len(available) != 1 {
		t.Fatalf("available = %#v", available)
	}
	entry := available[0]
	// Description 是模型收到的那一份，必须逐字节原样；展示文案不得覆盖它。
	if entry.Name != "forum-search" || entry.Description != "检索主题与评论" {
		t.Fatalf("core entry drifted: %#v", entry)
	}
	if entry.Origin != (ai.ToolOrigin{Kind: "core"}) {
		t.Fatalf("core origin = %#v", entry.Origin)
	}
	if entry.DescriptionI18n != nil {
		t.Fatalf("core tool without display descriptions must stay nil: %#v", entry.DescriptionI18n)
	}
	if entry.TimeoutMS != 0 {
		t.Fatalf("core tool has no plugin timeout declaration, got %d", entry.TimeoutMS)
	}
	if entry.SettingsPath != "" {
		t.Fatalf("core tool has no plugin settings page, got %q", entry.SettingsPath)
	}
	parameters := entry.Parameters
	if parameters == nil {
		t.Fatal("schema with properties must produce a parameter summary")
	}
	if !reflect.DeepEqual(parameters.Names, []string{"limit", "query"}) {
		t.Fatalf("parameter names must be sorted: %#v", parameters.Names)
	}
	if !reflect.DeepEqual(parameters.Required, []string{"query"}) {
		t.Fatalf("required = %#v", parameters.Required)
	}
	if parameters.Truncated {
		t.Fatal("two parameters must not be marked truncated")
	}
}

// 插件工具的四个可选声明都要汇总进枚举条目，且枚举顺序仍按名字升序。
func TestToolRegistryAvailableFillsPluginMetadata(t *testing.T) {
	registry := ai.NewToolRegistry()
	if err := registry.Register(newMetadataTool("forum-search", "检索主题与评论", "")); err != nil {
		t.Fatalf("register core tool: %v", err)
	}
	plugin := &pluginMetadataTool{
		metadataTool: *newMetadataTool("web-search", "搜索网页",
			`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		origin:       ai.ToolOrigin{Kind: "plugin", ExtensionID: "sforum.web-search", Version: "1.2.0"},
		display:      map[string]string{"en-US": "Search the web"},
		timeoutMS:    15000,
		settingsPath: "/extensions/sforum.web-search/pages/settings",
	}
	if err := registry.Register(plugin); err != nil {
		t.Fatalf("register plugin tool: %v", err)
	}

	available := registry.Available()
	if len(available) != 2 {
		t.Fatalf("available = %#v", available)
	}
	if available[0].Name != "forum-search" || available[1].Name != "web-search" {
		t.Fatalf("enumeration must stay sorted by name: %#v", available)
	}
	entry := available[1]
	if entry.Origin.Kind != "plugin" || entry.Origin.ExtensionID != "sforum.web-search" ||
		entry.Origin.Version != "1.2.0" {
		t.Fatalf("plugin origin = %#v", entry.Origin)
	}
	// 本地化文案只服务控制台：模型面的 Description 不能变成 en-US 那一份。
	if entry.Description != "搜索网页" {
		t.Fatalf("model-facing description must stay untouched: %q", entry.Description)
	}
	if !reflect.DeepEqual(entry.DescriptionI18n, map[string]string{"en-US": "Search the web"}) {
		t.Fatalf("descriptionI18n = %#v", entry.DescriptionI18n)
	}
	if entry.TimeoutMS != 15000 {
		t.Fatalf("timeout = %d", entry.TimeoutMS)
	}
	if entry.SettingsPath != "/extensions/sforum.web-search/pages/settings" {
		t.Fatalf("settings path = %q", entry.SettingsPath)
	}
	if entry.Parameters == nil || !reflect.DeepEqual(entry.Parameters.Names, []string{"query"}) ||
		!reflect.DeepEqual(entry.Parameters.Required, []string{"query"}) {
		t.Fatalf("plugin parameters = %#v", entry.Parameters)
	}
}

// 调用方拿到的必须是副本：控制台改写返回的 map 不得污染下一次枚举，也不得改到
// 工具自己持有的那份文案。
func TestToolRegistryAvailableCopiesDisplayDescriptions(t *testing.T) {
	registry := ai.NewToolRegistry()
	plugin := &pluginMetadataTool{
		metadataTool: *newMetadataTool("web-search", "搜索网页", ""),
		origin:       ai.ToolOrigin{Kind: "plugin", ExtensionID: "sforum.web-search"},
		display:      map[string]string{"en-US": "Search the web"},
		timeoutMS:    5000,
	}
	if err := registry.Register(plugin); err != nil {
		t.Fatalf("register: %v", err)
	}

	first := registry.Available()
	if len(first) != 1 {
		t.Fatalf("available = %#v", first)
	}
	first[0].DescriptionI18n["en-US"] = "被调用方改写"
	first[0].DescriptionI18n["zh-CN"] = "调用方新增"

	second := registry.Available()
	if got := second[0].DescriptionI18n["en-US"]; got != "Search the web" {
		t.Fatalf("mutating the returned map polluted the next enumeration: %q", got)
	}
	if _, exists := second[0].DescriptionI18n["zh-CN"]; exists {
		t.Fatalf("mutating the returned map added a key to the next enumeration: %#v", second[0].DescriptionI18n)
	}
	if plugin.display["en-US"] != "Search the web" || len(plugin.display) != 1 {
		t.Fatalf("the provider's own map must not be handed out by reference: %#v", plugin.display)
	}
}

// 没有实现任何可选接口的工具仍然是合法工具：枚举不能 panic，缺的字段留零值。
func TestToolRegistryAvailableToleratesToolsWithoutOptionalInterfaces(t *testing.T) {
	registry := ai.NewToolRegistry()
	if err := registry.Register(newMetadataTool("time-now", "当前时间", "")); err != nil {
		t.Fatalf("register: %v", err)
	}

	available := registry.Available()
	if len(available) != 1 {
		t.Fatalf("available = %#v", available)
	}
	entry := available[0]
	if entry.Origin.Kind != "core" {
		t.Fatalf("origin = %#v", entry.Origin)
	}
	if entry.DescriptionI18n != nil {
		t.Fatalf("descriptionI18n must stay nil, got %#v", entry.DescriptionI18n)
	}
	if entry.TimeoutMS != 0 {
		t.Fatalf("timeout must stay zero, got %d", entry.TimeoutMS)
	}
	if entry.Parameters != nil {
		t.Fatalf("schema without properties must not produce a summary: %#v", entry.Parameters)
	}
}

// 空登记表回答「站内没有工具」，而不是一个空切片或一次 panic。
func TestToolRegistryAvailableReturnsNilWhenEmpty(t *testing.T) {
	if got := ai.NewToolRegistry().Available(); got != nil {
		t.Fatalf("empty registry must report no tools, got %#v", got)
	}
	var unassembled *ai.ToolRegistry
	if got := unassembled.Available(); got != nil {
		t.Fatalf("nil registry must report no tools, got %#v", got)
	}
}
