package plugin

import (
	"sort"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// P7 冻结的六个扩展家族：hooks / services / providers / jobs / schedules / commands。
// 目录与文档必须从这些源常量派生，禁止在 SDK 中另起一套枚举。
const (
	FamilyHooks         = "hooks"
	FamilyServices      = "services"
	FamilyProviderSlots = "providers"
	FamilyJobs          = "jobs"
	FamilySchedules     = "schedules"
	FamilyCommands      = "commands"
)

// FamilySurface 描述一个冻结家族的权威源、可调用边界与作者入口。
// CallableTransport 为 false 时，SDK 只提供声明/目录辅助，不得伪装成可执行客户端。
type FamilySurface struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	ManifestField     string   `json:"manifestField"`
	SourceAuthorities []string `json:"sourceAuthorities"`
	HostToPluginRPC   string   `json:"hostToPluginRpc,omitempty"`
	PluginToHostRPC   string   `json:"pluginToHostRpc,omitempty"`
	CallableTransport bool     `json:"callableTransport"`
	SDKAuthorEntry    string   `json:"sdkAuthorEntry"`
	Boundary          string   `json:"boundary"`
	CatalogDoc        string   `json:"catalogDoc"`
}

// FrozenFamilySurfaces 返回稳定排序的六个家族边界说明（源生派生，非运行时注册表）。
func FrozenFamilySurfaces() []FamilySurface {
	surfaces := []FamilySurface{
		{
			ID:            FamilyHooks,
			Title:         "Hooks",
			ManifestField: "hooks",
			SourceAuthorities: []string{
				"app/Support/Events (host event name catalog)",
				"app/Support/ExtensionManifest.ManifestHook + V3 hook validation",
				"sforum.plugin.v2.PluginRuntimeService.InvokeHook",
			},
			HostToPluginRPC:   "sforum.plugin.v2.PluginRuntimeService/InvokeHook",
			CallableTransport: true,
			SDKAuthorEntry:    "sdk/plugin/v2.HookRegistry + Host event catalog helpers",
			Boundary:          "Host invokes declared listeners. Plugins do not call other plugins' hooks directly; composition stays host-owned.",
			CatalogDoc:        DocHooks,
		},
		{
			ID:            FamilyServices,
			Title:         "Services",
			ManifestField: "services",
			SourceAuthorities: []string{
				"app/Support/ExtensionManifest.ManifestService + V3 service validation",
				"app/Support/HostAPI ServiceRegistry + ServiceDiscoveryService",
				"sforum.plugin.v2.PluginRuntimeService.InvokeService/StreamService",
				"sforum.host.v2.ServiceDiscoveryService.List/Resolve/Invoke/Stream",
			},
			HostToPluginRPC:   "sforum.plugin.v2.PluginRuntimeService/InvokeService|StreamService",
			PluginToHostRPC:   "sforum.host.v2.ServiceDiscoveryService/List|Resolve|Invoke|Stream",
			CallableTransport: true,
			SDKAuthorEntry:    "sdk/plugin/v2.ServiceRegistry + Host.List/Resolve/InvokeService helpers",
			Boundary:          "Service discovery and invoke are host-brokered. before/after/wrap composition remains fail-closed until a host chain exists.",
			CatalogDoc:        DocServices,
		},
		{
			ID:            FamilyProviderSlots,
			Title:         "Provider slots",
			ManifestField: "providers",
			SourceAuthorities: []string{
				"Known host provider slot constants (mail/search/storage/…)",
				"app/Support/ExtensionManifest.ManifestProvider + V3 provider validation",
				"sforum.host.v2.ServiceDiscoveryService.InvokeProvider",
				"sforum.plugin.v2.PluginRuntimeService.ProviderCall",
			},
			HostToPluginRPC:   "sforum.plugin.v2.PluginRuntimeService/ProviderCall",
			PluginToHostRPC:   "sforum.host.v2.ServiceDiscoveryService/InvokeProvider",
			CallableTransport: true,
			SDKAuthorEntry:    "sdk/plugin/v2.ProviderRegistry + Host.InvokeProvider (invoke only)",
			Boundary:          "Selection, fallback, probe, and restore-defaults stay host-owned. Versioned ProviderRegistry and Host.InvokeProvider accept only operation invoke. Legacy known-slot probe/send must override ProviderCall or use provider-specific compatibility APIs.",
			CatalogDoc:        DocProviderSlots,
		},
		{
			ID:            FamilyJobs,
			Title:         "Dynamic jobs",
			ManifestField: "jobs",
			SourceAuthorities: []string{
				"app/Support/ExtensionManifest.ManifestJob + job policy constants",
				"app/Support/Jobs.PluginJobContract",
				"sforum.host.v2.JobService.Enqueue",
				"sforum.plugin.v2.PluginRuntimeService.ExecuteJob",
			},
			HostToPluginRPC:   "sforum.plugin.v2.PluginRuntimeService/ExecuteJob",
			PluginToHostRPC:   "sforum.host.v2.JobService/Enqueue",
			CallableTransport: true,
			SDKAuthorEntry:    "sdk/plugin/v2.JobRegistry + Host.EnqueueJob helper",
			Boundary:          "Host-brokered Enqueue and host-to-plugin ExecuteJob are the callable path. Cancel and Watch exist on the wire but currently return host.job_*_unavailable. Enqueue requires exact payload schema and live artifact admission.",
			CatalogDoc:        DocJobs,
		},
		{
			ID:            FamilySchedules,
			Title:         "Schedules",
			ManifestField: "schedules",
			SourceAuthorities: []string{
				"app/Support/Jobs.CoreScheduleDefinitions",
				"app/Support/ExtensionManifest.ManifestSchedule + V3 schedule validation",
				"app/Support/Jobs.PluginScheduleDeclaration (host-owned periodic trigger)",
			},
			// ScheduleService is generated on the Host client, but the current host
			// process does not register a ScheduleService server. Do not treat List/
			// Trigger as a callable author transport until the host registers it.
			// 公开 SDK 也没有 ScheduleService 包装或 Trigger helper。
			PluginToHostRPC:   "sforum.host.v2.ScheduleService/List|Trigger (wire only; host server not registered)",
			CallableTransport: false,
			SDKAuthorEntry:    "CoreSchedules catalog + Manifest schedules[] declaration (no List/Trigger helper; Host.Schedules is wire-only)",
			Boundary:          "Plugins declare cron schedules that the host admits and triggers into declared jobs. Plugins must not start private cron loops. Host.Schedules is only the generated wire client field; the host process does not register ScheduleService, and the public SDK provides no List/Trigger helper methods.",
			CatalogDoc:        DocSchedules,
		},
		{
			ID:            FamilyCommands,
			Title:         "Plugin commands",
			ManifestField: "commands",
			SourceAuthorities: []string{
				"app/Support/ExtensionManifest.ManifestCommand + V3 command validation",
				"app/Support/Extensions.PluginCommandRegistry",
				"sforum.plugin.v2.PluginRuntimeService.InvokeCommand",
			},
			HostToPluginRPC:   "sforum.plugin.v2.PluginRuntimeService/InvokeCommand",
			CallableTransport: true,
			SDKAuthorEntry:    "sdk/plugin/v2.CommandRegistry (CLI host invokes; plugins do not self-call)",
			Boundary:          "Plugin commands are CLI/host-invoked against the exact runtime. There is no plugin-to-host RPC for running a peer plugin command.",
			CatalogDoc:        DocCommands,
		},
	}
	sort.Slice(surfaces, func(i, j int) bool { return surfaces[i].ID < surfaces[j].ID })
	return surfaces
}

// HookKindValues 是 Manifest V3 接受的 hook.kind 集合。
func HookKindValues() []string {
	return []string{"action", "filter", "observe"}
}

// HookExecutionValues 是 Manifest V3 接受的 hook.execution 集合。
func HookExecutionValues() []string {
	return []string{"sync", "async"}
}

// HookFailurePolicyValues 是 Manifest V3 接受的 failurePolicy 集合。
func HookFailurePolicyValues() []string {
	return []string{appevents.FailurePolicyFailClosed, appevents.FailurePolicyFailOpen}
}

// JobRetryPolicyValues 是 Manifest V3 接受的 job.retryPolicy 集合。
func JobRetryPolicyValues() []string {
	return []string{
		supportjobs.PluginJobRetryNone,
		supportjobs.PluginJobRetryBounded,
		supportjobs.PluginJobRetryExponential,
	}
}

// ServiceActionValues 是当前 V3 服务声明允许的 action 集合（仅 add 可无 target）。
func ServiceActionValues() []string {
	return []string{"add", "before", "after", "wrap", "replace"}
}

// FamilyLimits 汇总各家族硬上限（与 ExtensionManifest / Jobs 常量对齐）。
type FamilyLimits struct {
	HookMaximumTimeoutMS           int `json:"hookMaximumTimeoutMs"`
	ProviderSlotMaximumTimeoutMS   int `json:"providerSlotMaximumTimeoutMs"`
	PluginCommandMaximumTimeoutMS  int `json:"pluginCommandMaximumTimeoutMs"`
	PluginJobDefaultConcurrency    int `json:"pluginJobDefaultConcurrency"`
	PluginJobMaximumConcurrency    int `json:"pluginJobMaximumConcurrency"`
	PluginJobMaximumAttempts       int `json:"pluginJobMaximumAttempts"`
	PluginJobDefaultBoundedAttempt int `json:"pluginJobDefaultBoundedAttempts"`
	PluginJobDefaultExponentialAtt int `json:"pluginJobDefaultExponentialAttempts"`
	PluginJobDefaultRetryDelaySec  int `json:"pluginJobDefaultRetryDelaySeconds"`
	PluginJobMaximumRetryDelaySec  int `json:"pluginJobMaximumRetryDelaySeconds"`
	DefaultSyncTimeoutMS           int `json:"defaultSyncTimeoutMs"`
	DefaultAsyncTimeoutMS          int `json:"defaultAsyncTimeoutMs"`
}

// FrozenFamilyLimits 从已提交常量构造上限快照，供文档与漂移测试使用。
func FrozenFamilyLimits() FamilyLimits {
	return FamilyLimits{
		HookMaximumTimeoutMS:           extensionmanifest.HookMaximumTimeoutMS,
		ProviderSlotMaximumTimeoutMS:   extensionmanifest.ProviderSlotMaximumTimeoutMS,
		PluginCommandMaximumTimeoutMS:  extensionmanifest.PluginCommandMaximumTimeoutMS,
		PluginJobDefaultConcurrency:    extensionmanifest.PluginJobDefaultConcurrencyLimit,
		PluginJobMaximumConcurrency:    extensionmanifest.PluginJobMaximumConcurrencyLimit,
		PluginJobMaximumAttempts:       extensionmanifest.PluginJobMaximumAttempts,
		PluginJobDefaultBoundedAttempt: extensionmanifest.PluginJobDefaultBoundedAttempts,
		PluginJobDefaultExponentialAtt: extensionmanifest.PluginJobDefaultExponentialAttempts,
		PluginJobDefaultRetryDelaySec:  extensionmanifest.PluginJobDefaultRetryDelaySeconds,
		PluginJobMaximumRetryDelaySec:  extensionmanifest.PluginJobMaximumRetryDelaySeconds,
		DefaultSyncTimeoutMS:           appevents.DefaultSyncTimeoutMS,
		DefaultAsyncTimeoutMS:          appevents.DefaultAsyncTimeoutMS,
	}
}
