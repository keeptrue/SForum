package plugin

import (
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// 协议类型别名：插件实现 Protocol，经 Serve 暴露给宿主。

type (
	// Protocol 是 go-plugin 后端插件必须实现的 RPC 面。
	Protocol = extensionsruntime.PluginProtocol

	// Health 是 Health() RPC 的返回值。
	Health = extensionsruntime.PluginHealth

	// RouteTarget 是 RouteTarget() RPC 的返回值。
	// 纯 provider 插件可返回空 BaseURL（或 "none"/"disabled"）。
	RouteTarget = extensionsruntime.PluginRouteTarget

	// HookRequest / HookResponse 是事件/filter 钩子载荷。
	HookRequest           = extensionsruntime.PluginHookRequest
	HookResponse          = extensionsruntime.PluginHookResponse
	ProviderProbeRequest  = extensionsruntime.ProviderProbeRequest
	ProviderProbeResponse = extensionsruntime.ProviderProbeResponse

	// MailRequest / MailResponse 是 mail.provider 投递载荷。
	MailRequest  = extensionsruntime.MailProviderRequest
	MailResponse = extensionsruntime.MailProviderResponse

	// 附件存储槽 attachment.storage.provider（E6.2 分块 RPC）。
	StoragePutBeginRequest          = extensionsruntime.StoragePutBeginRequest
	StoragePutChunkRequest          = extensionsruntime.StoragePutChunkRequest
	StorageOpenRequest              = extensionsruntime.StorageOpenRequest
	StorageGetChunkRequest          = extensionsruntime.StorageGetChunkRequest
	StorageGetChunkResponse         = extensionsruntime.StorageGetChunkResponse
	StorageCloseRequest             = extensionsruntime.StorageCloseRequest
	StorageObjectRequest            = extensionsruntime.StorageObjectRequest
	StorageStatRequest              = extensionsruntime.StorageStatRequest
	StorageStatResponse             = extensionsruntime.StorageStatResponse
	StorageExistsRequest            = extensionsruntime.StorageExistsRequest
	StorageExistsResponse           = extensionsruntime.StorageExistsResponse
	StoragePublicURLRequest         = extensionsruntime.StoragePublicURLRequest
	StorageSignedURLRequest         = extensionsruntime.StorageSignedURLRequest
	StorageURLResponse              = extensionsruntime.StorageURLResponse
	StorageProbeRequest             = extensionsruntime.StorageProbeRequest
	StorageProbeResponse            = extensionsruntime.StorageProbeResponse
	StorageSessionResponse          = extensionsruntime.StorageSessionResponse
	StorageResult                   = extensionsruntime.StorageResult
	StorageConfigureInstanceRequest = extensionsruntime.StorageConfigureInstanceRequest
	StorageRemoveInstanceRequest    = extensionsruntime.StorageRemoveInstanceRequest
	StorageProbeConfigRequest       = extensionsruntime.StorageProbeConfigRequest
)

// Noop 提供全部 RPC 的默认实现，便于只覆盖需要的方法。
// 嵌入后可只实现 Health / InvokeHook / SendMail / Storage* 中的子集。
type Noop struct {
	extensionsruntime.ProtocolNoop
}
