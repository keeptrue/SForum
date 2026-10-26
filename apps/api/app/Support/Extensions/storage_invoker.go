package extensionsruntime

import (
	"context"
	"errors"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionruntime "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionRuntime"
)

// StorageProviderInvoker 是 ProtocolStarter / 测试替身对存储 RPC 的宿主入口。
// Manager 在闸门/熔断之后调用此接口。
type StorageProviderInvoker interface {
	StoragePutBegin(ctx context.Context, extensionID string, request StoragePutBeginRequest) (StorageSessionResponse, error)
	StoragePutChunk(ctx context.Context, extensionID string, request StoragePutChunkRequest) (StorageResult, error)
	StorageOpen(ctx context.Context, extensionID string, request StorageOpenRequest) (StorageSessionResponse, error)
	StorageGetChunk(ctx context.Context, extensionID string, request StorageGetChunkRequest) (StorageGetChunkResponse, error)
	StorageClose(ctx context.Context, extensionID string, request StorageCloseRequest) (StorageResult, error)
	StorageDelete(ctx context.Context, extensionID string, request StorageObjectRequest) (StorageResult, error)
	StorageStat(ctx context.Context, extensionID string, request StorageStatRequest) (StorageStatResponse, error)
	StorageExists(ctx context.Context, extensionID string, request StorageExistsRequest) (StorageExistsResponse, error)
	StoragePublicURL(ctx context.Context, extensionID string, request StoragePublicURLRequest) (StorageURLResponse, error)
	StorageSignedURL(ctx context.Context, extensionID string, request StorageSignedURLRequest) (StorageURLResponse, error)
	StorageProbe(ctx context.Context, extensionID string, request StorageProbeRequest) (StorageProbeResponse, error)
	StorageConfigureInstance(ctx context.Context, extensionID string, request StorageConfigureInstanceRequest) (StorageResult, error)
	StorageRemoveInstance(ctx context.Context, extensionID string, request StorageRemoveInstanceRequest) (StorageResult, error)
	StorageProbeConfig(ctx context.Context, extensionID string, request StorageProbeConfigRequest) (StorageProbeResponse, error)
}

// StorageRuntime 是 Attachments / PluginStorageAdapter 依赖的最小 runtime 面。
// Manager 实现本接口；bootstrap 把 extensionRuntime 注入附件服务。
type StorageRuntime interface {
	StoragePutBegin(ctx context.Context, extensionID string, request StoragePutBeginRequest) (StorageSessionResponse, error)
	StoragePutChunk(ctx context.Context, extensionID string, request StoragePutChunkRequest) (StorageResult, error)
	StorageOpen(ctx context.Context, extensionID string, request StorageOpenRequest) (StorageSessionResponse, error)
	StorageGetChunk(ctx context.Context, extensionID string, request StorageGetChunkRequest) (StorageGetChunkResponse, error)
	StorageClose(ctx context.Context, extensionID string, request StorageCloseRequest) (StorageResult, error)
	StorageDelete(ctx context.Context, extensionID string, request StorageObjectRequest) (StorageResult, error)
	StorageStat(ctx context.Context, extensionID string, request StorageStatRequest) (StorageStatResponse, error)
	StorageExists(ctx context.Context, extensionID string, request StorageExistsRequest) (StorageExistsResponse, error)
	StoragePublicURL(ctx context.Context, extensionID string, request StoragePublicURLRequest) (StorageURLResponse, error)
	StorageSignedURL(ctx context.Context, extensionID string, request StorageSignedURLRequest) (StorageURLResponse, error)
	StorageProbe(ctx context.Context, extensionID string, request StorageProbeRequest) (StorageProbeResponse, error)
}

type StorageInstanceRuntime interface {
	StorageConfigureInstance(ctx context.Context, extensionID string, request StorageConfigureInstanceRequest) (StorageResult, error)
	StorageRemoveInstance(ctx context.Context, extensionID string, request StorageRemoveInstanceRequest) (StorageResult, error)
	StorageProbeConfig(ctx context.Context, extensionID string, request StorageProbeConfigRequest) (StorageProbeResponse, error)
}

// StorageInstanceInvoker owns Host-side multi-instance storage calls without
// growing the legacy Manager or RuntimeInvoker facades.
type StorageInstanceInvoker struct{ *Manager }

// storageCall 在运行中的扩展上执行一次存储 RPC，并走 F2.3 闸门/熔断。
// success 由调用方根据响应 OK（及传输 err）判定。
func (m *managerCore) storageCall(
	ctx context.Context,
	extensionID string,
	fn func(ctx context.Context, invoker StorageProviderInvoker) (ok bool, reason string, err error),
) error {
	invoker, ok := m.starter.(StorageProviderInvoker)
	if !ok {
		return extensions.ErrRuntimeUnavailable
	}
	timeout := m.resilience.cfg.DefaultStorageTimeout
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline && timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	_, admission, err := m.host.AcquireActiveRuntimeCall(ctx, extensionID, RuntimeCallProvider)
	if err != nil {
		return errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	defer admission.Release()
	ctx = admission.Context

	release, rejected := m.resilience.tryEnter(ctx, extensionID)
	if rejected != "" {
		return storageRejectedError(rejected)
	}
	success, reason, err := fn(ctx, invoker)
	if err != nil {
		reason = "extension.storage_failed"
		success = false
	}
	if ctx.Err() != nil && !success {
		reason = "extension.hook_timeout"
	}
	release(success, reason)
	return err
}

func storageRejectedError(reason string) error {
	switch reason {
	case "extension.circuit_open":
		return ErrStorageCircuitOpen
	case "extension.hook_timeout":
		return ErrStorageTimeout
	default:
		return extensions.ErrRuntimeUnavailable
	}
}

// 宿主侧可识别的存储 RPC 拒绝（PluginStorageAdapter 映射为 fail-closed）。
var (
	ErrStorageCircuitOpen = extensionruntime.ErrStorageCircuitOpen
	ErrStorageTimeout     = extensionruntime.ErrStorageTimeout
)

// StorageRPCError 把插件返回的 !OK 转成带 Reason 的错误，供 Adapter 使用。
type StorageRPCError = extensionruntime.StorageRPCError

func storageRPCErr(reason, message string) error {
	if reason == "" {
		reason = "extension.storage_failed"
	}
	return &extensionruntime.StorageRPCError{Reason: reason, Message: message}
}

func (m *RuntimeInvoker) StoragePutBegin(ctx context.Context, extensionID string, request StoragePutBeginRequest) (StorageSessionResponse, error) {
	var response StorageSessionResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StoragePutBegin(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StoragePutChunk(ctx context.Context, extensionID string, request StoragePutChunkRequest) (StorageResult, error) {
	var response StorageResult
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StoragePutChunk(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageOpen(ctx context.Context, extensionID string, request StorageOpenRequest) (StorageSessionResponse, error) {
	var response StorageSessionResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageOpen(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageGetChunk(ctx context.Context, extensionID string, request StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	var response StorageGetChunkResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageGetChunk(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageClose(ctx context.Context, extensionID string, request StorageCloseRequest) (StorageResult, error) {
	// Close 失败不计入熔断（尽力清理会话）。
	invoker, ok := m.starter.(StorageProviderInvoker)
	if !ok {
		return StorageResult{}, extensions.ErrRuntimeUnavailable
	}
	_, admission, err := m.AcquireActiveRuntimeCall(ctx, extensionID, RuntimeCallProvider)
	if err != nil {
		return StorageResult{}, errors.Join(extensions.ErrRuntimeUnavailable, err)
	}
	defer admission.Release()
	return invoker.StorageClose(admission.Context, extensionID, request)
}

func (m *RuntimeInvoker) StorageDelete(ctx context.Context, extensionID string, request StorageObjectRequest) (StorageResult, error) {
	var response StorageResult
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageDelete(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageStat(ctx context.Context, extensionID string, request StorageStatRequest) (StorageStatResponse, error) {
	var response StorageStatResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageStat(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageExists(ctx context.Context, extensionID string, request StorageExistsRequest) (StorageExistsResponse, error) {
	var response StorageExistsResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageExists(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StoragePublicURL(ctx context.Context, extensionID string, request StoragePublicURLRequest) (StorageURLResponse, error) {
	var response StorageURLResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StoragePublicURL(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageSignedURL(ctx context.Context, extensionID string, request StorageSignedURLRequest) (StorageURLResponse, error) {
	var response StorageURLResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageSignedURL(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *RuntimeInvoker) StorageProbe(ctx context.Context, extensionID string, request StorageProbeRequest) (StorageProbeResponse, error) {
	var response StorageProbeResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageProbe(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *StorageInstanceInvoker) StorageConfigureInstance(ctx context.Context, extensionID string, request StorageConfigureInstanceRequest) (StorageResult, error) {
	var response StorageResult
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageConfigureInstance(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *StorageInstanceInvoker) StorageRemoveInstance(ctx context.Context, extensionID string, request StorageRemoveInstanceRequest) (StorageResult, error) {
	var response StorageResult
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageRemoveInstance(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

func (m *StorageInstanceInvoker) StorageProbeConfig(ctx context.Context, extensionID string, request StorageProbeConfigRequest) (StorageProbeResponse, error) {
	var response StorageProbeResponse
	err := m.storageCall(ctx, extensionID, func(ctx context.Context, invoker StorageProviderInvoker) (bool, string, error) {
		resp, err := invoker.StorageProbeConfig(ctx, extensionID, request)
		response = resp
		if err != nil {
			return false, "", err
		}
		return resp.OK, resp.Reason, nil
	})
	return response, err
}

// Compatibility facade: runtime logic is owned by focused collaborators.

func (m *Manager) StoragePutBegin(ctx context.Context, extensionID string, request StoragePutBeginRequest) (StorageSessionResponse, error) {
	return m.invoker.StoragePutBegin(ctx, extensionID, request)
}

func (m *Manager) StoragePutChunk(ctx context.Context, extensionID string, request StoragePutChunkRequest) (StorageResult, error) {
	return m.invoker.StoragePutChunk(ctx, extensionID, request)
}

func (m *Manager) StorageOpen(ctx context.Context, extensionID string, request StorageOpenRequest) (StorageSessionResponse, error) {
	return m.invoker.StorageOpen(ctx, extensionID, request)
}

func (m *Manager) StorageGetChunk(ctx context.Context, extensionID string, request StorageGetChunkRequest) (StorageGetChunkResponse, error) {
	return m.invoker.StorageGetChunk(ctx, extensionID, request)
}

func (m *Manager) StorageClose(ctx context.Context, extensionID string, request StorageCloseRequest) (StorageResult, error) {
	return m.invoker.StorageClose(ctx, extensionID, request)
}

func (m *Manager) StorageDelete(ctx context.Context, extensionID string, request StorageObjectRequest) (StorageResult, error) {
	return m.invoker.StorageDelete(ctx, extensionID, request)
}

func (m *Manager) StorageStat(ctx context.Context, extensionID string, request StorageStatRequest) (StorageStatResponse, error) {
	return m.invoker.StorageStat(ctx, extensionID, request)
}

func (m *Manager) StorageExists(ctx context.Context, extensionID string, request StorageExistsRequest) (StorageExistsResponse, error) {
	return m.invoker.StorageExists(ctx, extensionID, request)
}

func (m *Manager) StoragePublicURL(ctx context.Context, extensionID string, request StoragePublicURLRequest) (StorageURLResponse, error) {
	return m.invoker.StoragePublicURL(ctx, extensionID, request)
}

func (m *Manager) StorageSignedURL(ctx context.Context, extensionID string, request StorageSignedURLRequest) (StorageURLResponse, error) {
	return m.invoker.StorageSignedURL(ctx, extensionID, request)
}

func (m *Manager) StorageProbe(ctx context.Context, extensionID string, request StorageProbeRequest) (StorageProbeResponse, error) {
	return m.invoker.StorageProbe(ctx, extensionID, request)
}
