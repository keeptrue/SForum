package extensions

import (
	processmemory "github.com/keeptrue/sforum/apps/api/app/Support/ProcessMemory"
)

// sampleOwnedPluginMemory 一次 ps 采样，得到 extensionID → RSS 字节。
// 失败时返回空 map，列表仍可展示其余 runtime 字段。
func sampleOwnedPluginMemory(s *Service) map[string]uint64 {
	if s != nil && s.pluginMemorySampler != nil {
		return s.pluginMemorySampler()
	}
	return processmemory.SampleOwnedPluginRSS()
}

// applyPluginMemory 把已采样的 RSS 写进 runtime（仅插件且 runtime 已装饰时）。
func applyPluginMemory(item Extension, byID map[string]uint64) Extension {
	if item.Runtime == nil || len(byID) == 0 {
		return item
	}
	rss, ok := byID[item.ID]
	if !ok || rss == 0 {
		return item
	}
	status := *item.Runtime
	status.MemoryBytes = rss
	item.Runtime = &status
	return item
}
