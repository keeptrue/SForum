package main

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"

	pluginsdk "github.com/keeptrue/sforum/apps/api/sdk/plugin/storageprovider"
	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 与 Host protocol_v2_storage.providerCall 对齐的 known-slot schema。
const (
	storageSlot                  = "attachment.storage.provider"
	storageLegacyContractVersion = "1"
	storageSchemaPrefix          = "sforum.provider.attachment.storage.provider."
)

// fsStoragePluginV2 覆盖 ProviderCall，承接 Host 遗留 known-slot 分块存储 RPC。
type fsStoragePluginV2 struct {
	*pluginv2.Server
	impl *fsStoragePlugin
}

func newFSStoragePluginV2() *fsStoragePluginV2 {
	return &fsStoragePluginV2{
		Server: pluginv2.NewServer(),
		impl:   newFSStoragePlugin(),
	}
}

func (p *fsStoragePluginV2) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(),
			Extension: request.GetContext().GetExtension(),
		},
	}
	// 复用 Server 的精确制品握手校验，避免绕过 stale-runtime 检查。
	health, err := p.Server.Health(ctx, &protocolwire.HealthRequest{Context: request.GetContext()})
	if err != nil {
		return nil, err
	}
	if health.GetError() != nil {
		response.Error = health.GetError()
		return response, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.GetSlotId() != storageSlot {
		response.Error = &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND,
			Reason:  "provider.slot_unsupported",
			Message: "unsupported provider slot",
		}
		return response, nil
	}
	if request.GetContractVersion() != "" && request.GetContractVersion() != storageLegacyContractVersion {
		response.Error = &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "provider.contract_mismatch",
			Message: "attachment.storage.provider contract must be version 1",
		}
		return response, nil
	}

	values := pluginv2.TypedDocumentValues(request.GetInput())
	switch request.GetOperation() {
	case "probe":
		return p.probe(response)
	case "put_begin":
		return p.putBegin(response, values)
	case "put_chunk":
		return p.putChunk(response, values)
	case "open":
		return p.open(response, values)
	case "get_chunk":
		return p.getChunk(response, values)
	case "close":
		return p.close(response, values)
	case "delete":
		return p.delete(response, values)
	case "stat":
		return p.stat(response, values)
	case "exists":
		return p.exists(response, values)
	case "public_url":
		return p.publicURL(response, values)
	case "signed_url":
		return p.signedURL(response, values)
	default:
		response.Error = &protocolwire.ErrorDetail{
			Code:    protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			Reason:  "provider.operation_unsupported",
			Message: "attachment.storage.provider operation is not supported",
		}
		return response, nil
	}
}

func (p *fsStoragePluginV2) probe(response *pluginwire.ProviderCallResponse) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageProbe(pluginsdk.StorageProbeRequest{})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "probe", map[string]any{
		"ok": result.OK, "reason": result.Reason, "message": result.Message,
	})
}

func (p *fsStoragePluginV2) putBegin(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StoragePutBegin(pluginsdk.StoragePutBeginRequest{
		Key: stringValue(values, "key"), ContentType: stringValue(values, "contentType"),
		Size: int64Value(values, "size"),
	})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "put_begin", sessionMap(result))
}

func (p *fsStoragePluginV2) putChunk(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	data, err := decodeChunkData(stringValue(values, "data"))
	if err != nil {
		return p.writeOutput(response, "put_chunk", map[string]any{
			"ok": false, "reason": "storage.v2.invalid_chunk_encoding", "message": err.Error(),
		})
	}
	result, callErr := p.impl.StoragePutChunk(pluginsdk.StoragePutChunkRequest{
		SessionID: stringValue(values, "sessionId"), Data: data, Final: booleanValue(values, "final"),
	})
	if callErr != nil {
		return nil, callErr
	}
	return p.writeOutput(response, "put_chunk", resultMap(result))
}

func (p *fsStoragePluginV2) open(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageOpen(pluginsdk.StorageOpenRequest{Key: stringValue(values, "key")})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "open", sessionMap(result))
}

func (p *fsStoragePluginV2) getChunk(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageGetChunk(pluginsdk.StorageGetChunkRequest{
		SessionID: stringValue(values, "sessionId"), MaxBytes: intValue(values, "maxBytes"),
	})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "get_chunk", map[string]any{
		"ok": result.OK, "data": base64.StdEncoding.EncodeToString(result.Data),
		"eof": result.EOF, "reason": result.Reason, "message": result.Message,
	})
}

func (p *fsStoragePluginV2) close(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageClose(pluginsdk.StorageCloseRequest{SessionID: stringValue(values, "sessionId")})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "close", resultMap(result))
}

func (p *fsStoragePluginV2) delete(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageDelete(pluginsdk.StorageObjectRequest{Key: stringValue(values, "key")})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "delete", resultMap(result))
}

func (p *fsStoragePluginV2) stat(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageStat(pluginsdk.StorageStatRequest{Key: stringValue(values, "key")})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "stat", map[string]any{
		"ok": result.OK, "exists": result.Exists, "size": result.Size,
		"contentType": result.ContentType, "modifiedUnix": result.ModifiedUnix,
		"reason": result.Reason, "message": result.Message,
	})
}

func (p *fsStoragePluginV2) exists(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageExists(pluginsdk.StorageExistsRequest{Key: stringValue(values, "key")})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "exists", map[string]any{
		"ok": result.OK, "exists": result.Exists, "reason": result.Reason, "message": result.Message,
	})
}

func (p *fsStoragePluginV2) publicURL(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StoragePublicURL(pluginsdk.StoragePublicURLRequest{Key: stringValue(values, "key")})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "public_url", urlMap(result))
}

func (p *fsStoragePluginV2) signedURL(response *pluginwire.ProviderCallResponse, values map[string]any) (*pluginwire.ProviderCallResponse, error) {
	result, err := p.impl.StorageSignedURL(pluginsdk.StorageSignedURLRequest{
		Key: stringValue(values, "key"), TTLSeconds: int64Value(values, "ttlSeconds"),
	})
	if err != nil {
		return nil, err
	}
	return p.writeOutput(response, "signed_url", urlMap(result))
}

func (p *fsStoragePluginV2) writeOutput(
	response *pluginwire.ProviderCallResponse,
	operation string,
	values map[string]any,
) (*pluginwire.ProviderCallResponse, error) {
	output, err := pluginv2.NewTypedDocument(storageSchemaPrefix+operation+".response@1", values)
	if err != nil {
		return nil, err
	}
	response.Output = output
	return response, nil
}

func sessionMap(result pluginsdk.StorageSessionResponse) map[string]any {
	return map[string]any{
		"ok": result.OK, "sessionId": result.SessionID, "size": result.Size,
		"contentType": result.ContentType, "reason": result.Reason, "message": result.Message,
	}
}

func resultMap(result pluginsdk.StorageResult) map[string]any {
	return map[string]any{
		"ok": result.OK, "reason": result.Reason, "message": result.Message,
	}
}

func urlMap(result pluginsdk.StorageURLResponse) map[string]any {
	return map[string]any{
		"ok": result.OK, "url": result.URL, "reason": result.Reason, "message": result.Message,
	}
}

func decodeChunkData(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(encoded)
}

func stringValue(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	switch raw := values[key].(type) {
	case string:
		return raw
	case float64:
		return strconv.FormatInt(int64(raw), 10)
	case int64:
		return strconv.FormatInt(raw, 10)
	default:
		return ""
	}
}

func booleanValue(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

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
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		return n
	default:
		return 0
	}
}

func intValue(values map[string]any, key string) int {
	return int(int64Value(values, key))
}
