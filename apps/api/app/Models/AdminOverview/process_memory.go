package adminoverview

import (
	processmemory "github.com/keeptrue/sforum/apps/api/app/Support/ProcessMemory"
)

// ProcessSample 描述一次可测试的进程采样结果（兼容旧测试与注入点）。
type ProcessSample = processmemory.Sample

// RuntimeUsage 是 API/Worker/插件进程资源汇总。
type RuntimeUsage = processmemory.RuntimeUsage

// ProcessSampler 读取本机进程列表；失败时 Snapshot 仍返回 Go MemStats。
type ProcessSampler = processmemory.Sampler

// defaultProcessSampler 使用 OS 相关实现。
var defaultProcessSampler ProcessSampler = processmemory.DefaultSampler

// IsBackendPluginCommand 判断命令行是否为扩展 storage 下的 backend plugin 二进制。
func IsBackendPluginCommand(command string) bool {
	return processmemory.IsBackendPluginCommand(command)
}

// IsSforumAPICommand 判断命令是否为本仓库 API 进程。
func IsSforumAPICommand(command string) bool {
	return processmemory.IsSforumAPICommand(command)
}

// AggregateProcessMemory 从进程样本计算父进程 RSS 与「当前 API 拥有的」插件子进程全家内存。
func AggregateProcessMemory(selfPID int, samples []ProcessSample) (selfRSS uint64, familyRSS uint64, pluginChildren int, selfFound bool) {
	return processmemory.AggregateFamily(selfPID, samples)
}

func AggregateRuntimeUsage(selfPID int, samples []ProcessSample) RuntimeUsage {
	return processmemory.AggregateRuntimeUsage(selfPID, samples)
}

func sampleRuntimeUsage(sampler ProcessSampler) (RuntimeUsage, bool) {
	return processmemory.SampleRuntimeUsage(sampler)
}

// sampleSelfAndFamily 使用采样器读取本进程 RSS 与 owned 插件全家内存。
func sampleSelfAndFamily(sampler ProcessSampler) (selfRSS uint64, familyRSS uint64, pluginChildren int, ok bool) {
	return processmemory.SampleSelfAndFamily(sampler)
}

func readSelfRSSFallback() (uint64, bool) {
	return processmemory.ReadSelfRSSFallback()
}
