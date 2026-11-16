package extensionsruntime

import (
	"context"
	"encoding/base64"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// attachment.storage.provider known-slot：经 ProviderCall 承载分块 Put/Open。
// 二进制块以 base64 放进 TypedDocument（structpb 不接受 []byte）。
const (
	storageProviderSlot          = "attachment.storage.provider"
	storageLegacyContractVersion = "1"
	storageOpProbe               = "probe"
	storageOpPutBegin            = "put_begin"
	storageOpPutChunk            = "put_chunk"
	storageOpOpen                = "open"
	storageOpGetChunk            = "get_chunk"
	storageOpClose               = "close"
	storageOpDelete              = "delete"
	storageOpStat                = "stat"
	storageOpExists              = "exists"
	storageOpPublicURL           = "public_url"
	storageOpSignedURL           = "signed_url"
	storageOpConfigureInstance   = "configure_instance"
	storageOpRemoveInstance      = "remove_instance"
	storageOpProbeConfig         = "probe_config"
)

func (s *ProtocolStarter) StorageConfigureInstance(ctx context.Context, extensionID string, request StorageConfigureInstanceRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StorageConfigureInstance(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage ConfigureInstance exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageRemoveInstance(ctx context.Context, extensionID string, request StorageRemoveInstanceRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StorageRemoveInstance(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage RemoveInstance exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageProbeConfig(ctx context.Context, extensionID string, request StorageProbeConfigRequest) (StorageProbeResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageProbeResponse, error) {
		return p.StorageProbeConfig(request)
	}, StorageProbeResponse{Reason: "extension.hook_timeout", Message: "Storage ProbeConfig exceeded the host timeout."})
}

func (c *protocolV2Client) StorageProbe(input StorageProbeRequest) (StorageProbeResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpProbe, map[string]any{"instanceId": input.InstanceID})
	if err != nil {
		return StorageProbeResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return StorageProbeResponse{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) StoragePutBegin(input StoragePutBeginRequest) (StorageSessionResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpPutBegin, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key, "contentType": input.ContentType, "size": input.Size,
	})
	if err != nil {
		return StorageSessionResponse{}, err
	}
	return storageSessionFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StoragePutChunk(input StoragePutChunkRequest) (StorageResult, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpPutChunk, map[string]any{
		"sessionId": input.SessionID,
		"data":      base64.StdEncoding.EncodeToString(input.Data),
		"final":     input.Final,
	})
	if err != nil {
		return StorageResult{}, err
	}
	return storageResultFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageOpen(input StorageOpenRequest) (StorageSessionResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpOpen, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key,
	})
	if err != nil {
		return StorageSessionResponse{}, err
	}
	return storageSessionFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageGetChunk(input StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpGetChunk, map[string]any{
		"sessionId": input.SessionID, "maxBytes": int64(input.MaxBytes),
	})
	if err != nil {
		return StorageGetChunkResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	data, decodeErr := base64.StdEncoding.DecodeString(stringValue(values, "data"))
	if decodeErr != nil && stringValue(values, "data") != "" {
		return StorageGetChunkResponse{
			Reason: "storage.v2.invalid_chunk_encoding", Message: decodeErr.Error(),
		}, nil
	}
	return StorageGetChunkResponse{
		OK: booleanValue(values, "ok"), Data: data, EOF: booleanValue(values, "eof"),
		Reason: stringValue(values, "reason"), Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) StorageClose(input StorageCloseRequest) (StorageResult, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpClose, map[string]any{
		"sessionId": input.SessionID,
	})
	if err != nil {
		return StorageResult{}, err
	}
	return storageResultFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageDelete(input StorageObjectRequest) (StorageResult, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpDelete, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key,
	})
	if err != nil {
		return StorageResult{}, err
	}
	return storageResultFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageStat(input StorageStatRequest) (StorageStatResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpStat, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key,
	})
	if err != nil {
		return StorageStatResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return StorageStatResponse{
		OK: booleanValue(values, "ok"), Exists: booleanValue(values, "exists"),
		Size: int64Value(values, "size"), ContentType: stringValue(values, "contentType"),
		ModifiedUnix: int64Value(values, "modifiedUnix"),
		Reason:       stringValue(values, "reason"), Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) StorageExists(input StorageExistsRequest) (StorageExistsResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpExists, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key,
	})
	if err != nil {
		return StorageExistsResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return StorageExistsResponse{
		OK: booleanValue(values, "ok"), Exists: booleanValue(values, "exists"),
		Reason: stringValue(values, "reason"), Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) StoragePublicURL(input StoragePublicURLRequest) (StorageURLResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpPublicURL, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key,
	})
	if err != nil {
		return StorageURLResponse{}, err
	}
	return storageURLFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageSignedURL(input StorageSignedURLRequest) (StorageURLResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpSignedURL, map[string]any{
		"instanceId": input.InstanceID, "key": input.Key, "ttlSeconds": input.TTLSeconds,
	})
	if err != nil {
		return StorageURLResponse{}, err
	}
	return storageURLFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageConfigureInstance(input StorageConfigureInstanceRequest) (StorageResult, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpConfigureInstance, map[string]any{
		"instanceId": input.InstanceID, "settings": input.Settings,
	})
	if err != nil {
		return StorageResult{}, err
	}
	return storageResultFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageRemoveInstance(input StorageRemoveInstanceRequest) (StorageResult, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpRemoveInstance, map[string]any{"instanceId": input.InstanceID})
	if err != nil {
		return StorageResult{}, err
	}
	return storageResultFromValues(protocolV2Values(response.GetOutput())), nil
}

func (c *protocolV2Client) StorageProbeConfig(input StorageProbeConfigRequest) (StorageProbeResponse, error) {
	response, err := c.providerCall(storageProviderSlot, storageOpProbeConfig, map[string]any{"settings": input.Settings})
	if err != nil {
		return StorageProbeResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return StorageProbeResponse{OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"), Message: stringValue(values, "message")}, nil
}

func storageSessionFromValues(values map[string]any) StorageSessionResponse {
	return StorageSessionResponse{
		OK: booleanValue(values, "ok"), SessionID: stringValue(values, "sessionId"),
		Size: int64Value(values, "size"), ContentType: stringValue(values, "contentType"),
		Reason: stringValue(values, "reason"), Message: stringValue(values, "message"),
	}
}

func storageResultFromValues(values map[string]any) StorageResult {
	return StorageResult{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
	}
}

func storageURLFromValues(values map[string]any) StorageURLResponse {
	return StorageURLResponse{
		OK: booleanValue(values, "ok"), URL: stringValue(values, "url"),
		Reason: stringValue(values, "reason"), Message: stringValue(values, "message"),
	}
}

// int64Value 兼容 structpb AsMap 的 float64 与整型。
func int64Value(values map[string]any, key string) int64 {
	if values == nil {
		return 0
	}
	switch raw := values[key].(type) {
	case int64:
		return raw
	case int:
		return int64(raw)
	case int32:
		return int64(raw)
	case float64:
		return int64(raw)
	case float32:
		return int64(raw)
	default:
		return 0
	}
}

// callStorage 在 ctx 截止前执行一次存储 RPC。
// 超时返回 onTimeout（err=nil），与 SendMail 一致，便于宿主按 OK/Reason 处理。
func callStorage[T any](ctx context.Context, protocol PluginProtocol, fn func(PluginProtocol) (T, error), onTimeout T) (T, error) {
	var zero T
	if protocol == nil {
		return zero, extensions.ErrRuntimeUnavailable
	}
	type outcome struct {
		resp T
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := fn(protocol)
		done <- outcome{resp: resp, err: err}
	}()
	select {
	case <-ctx.Done():
		return onTimeout, nil
	case out := <-done:
		return out.resp, out.err
	}
}

func (s *ProtocolStarter) StoragePutBegin(ctx context.Context, extensionID string, request StoragePutBeginRequest) (StorageSessionResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageSessionResponse, error) {
		return p.StoragePutBegin(request)
	}, StorageSessionResponse{Reason: "extension.hook_timeout", Message: "Storage PutBegin exceeded the host timeout."})
}

func (s *ProtocolStarter) StoragePutChunk(ctx context.Context, extensionID string, request StoragePutChunkRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StoragePutChunk(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage PutChunk exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageOpen(ctx context.Context, extensionID string, request StorageOpenRequest) (StorageSessionResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageSessionResponse, error) {
		return p.StorageOpen(request)
	}, StorageSessionResponse{Reason: "extension.hook_timeout", Message: "Storage Open exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageGetChunk(ctx context.Context, extensionID string, request StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageGetChunkResponse, error) {
		return p.StorageGetChunk(request)
	}, StorageGetChunkResponse{Reason: "extension.hook_timeout", Message: "Storage GetChunk exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageClose(ctx context.Context, extensionID string, request StorageCloseRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StorageClose(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage Close exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageDelete(ctx context.Context, extensionID string, request StorageObjectRequest) (StorageResult, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageResult, error) {
		return p.StorageDelete(request)
	}, StorageResult{Reason: "extension.hook_timeout", Message: "Storage Delete exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageStat(ctx context.Context, extensionID string, request StorageStatRequest) (StorageStatResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageStatResponse, error) {
		return p.StorageStat(request)
	}, StorageStatResponse{Reason: "extension.hook_timeout", Message: "Storage Stat exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageExists(ctx context.Context, extensionID string, request StorageExistsRequest) (StorageExistsResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageExistsResponse, error) {
		return p.StorageExists(request)
	}, StorageExistsResponse{Reason: "extension.hook_timeout", Message: "Storage Exists exceeded the host timeout."})
}

func (s *ProtocolStarter) StoragePublicURL(ctx context.Context, extensionID string, request StoragePublicURLRequest) (StorageURLResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageURLResponse, error) {
		return p.StoragePublicURL(request)
	}, StorageURLResponse{Reason: "extension.hook_timeout", Message: "Storage PublicURL exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageSignedURL(ctx context.Context, extensionID string, request StorageSignedURLRequest) (StorageURLResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageURLResponse, error) {
		return p.StorageSignedURL(request)
	}, StorageURLResponse{Reason: "extension.hook_timeout", Message: "Storage SignedURL exceeded the host timeout."})
}

func (s *ProtocolStarter) StorageProbe(ctx context.Context, extensionID string, request StorageProbeRequest) (StorageProbeResponse, error) {
	return callStorage(ctx, s.protocolFor(extensionID), func(p PluginProtocol) (StorageProbeResponse, error) {
		return p.StorageProbe(request)
	}, StorageProbeResponse{Reason: "extension.hook_timeout", Message: "Storage Probe exceeded the host timeout."})
}
