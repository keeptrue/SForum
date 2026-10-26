package pluginv2

import (
	"context"
	"fmt"
	"strings"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// EnqueueJob 通过已注册的 JobService.Enqueue 入队本扩展声明的 versioned job。
// Cancel/Watch 不提供包装：当前 Host 适配器返回 unavailable，避免假装可用。
func (h *Host) EnqueueJob(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	jobKind string,
	payload *protocolwire.TypedDocument,
) (*hostwire.JobEnqueueResponse, error) {
	if h == nil || h.Jobs == nil {
		return nil, ErrHostUnavailable
	}
	jobKind = strings.TrimSpace(jobKind)
	if jobKind == "" {
		return nil, fmt.Errorf("pluginv2: job kind is required")
	}
	if payload == nil || payload.GetSchemaId() == "" || payload.GetSchemaVersion() == "" {
		return nil, fmt.Errorf("pluginv2: job payload schema id and version are required")
	}
	request := &hostwire.JobEnqueueRequest{
		Context: h.RequestContext(parent), JobKind: jobKind,
		PayloadVersion: payload.GetSchemaVersion(), Payload: cloneTypedDocument(payload),
	}
	return h.Jobs.Enqueue(ctx, request)
}

// ListServices 列出当前运行时可发现且授权的服务描述符。
func (h *Host) ListServices(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	serviceID, versionConstraint string,
	page *protocolwire.PageRequest,
) (*hostwire.ServiceListResponse, error) {
	if h == nil || h.Services == nil {
		return nil, ErrHostUnavailable
	}
	return h.Services.List(ctx, &hostwire.ServiceListRequest{
		Context: h.RequestContext(parent), ServiceId: strings.TrimSpace(serviceID),
		VersionConstraint: strings.TrimSpace(versionConstraint), Page: page,
	})
}

// ResolveService 按 SemVer 约束解析一个服务赢家。
func (h *Host) ResolveService(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	serviceID, versionConstraint string,
) (*hostwire.ServiceResolveResponse, error) {
	if h == nil || h.Services == nil {
		return nil, ErrHostUnavailable
	}
	serviceID = strings.TrimSpace(serviceID)
	if serviceID == "" {
		return nil, fmt.Errorf("pluginv2: service id is required")
	}
	return h.Services.Resolve(ctx, &hostwire.ServiceResolveRequest{
		Context: h.RequestContext(parent), ServiceId: serviceID,
		VersionConstraint: strings.TrimSpace(versionConstraint),
	})
}

// InvokeService 通过 Host 经纪调用另一插件的 unary 服务。
func (h *Host) InvokeService(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	serviceID, version, operation string,
	input *protocolwire.TypedDocument,
) (*hostwire.ServiceInvokeResponse, error) {
	if h == nil || h.Services == nil {
		return nil, ErrHostUnavailable
	}
	serviceID = strings.TrimSpace(serviceID)
	version = strings.TrimSpace(version)
	operation = strings.TrimSpace(operation)
	if serviceID == "" || version == "" || operation == "" {
		return nil, fmt.Errorf("pluginv2: service id, version, and operation are required")
	}
	return h.Services.Invoke(ctx, &hostwire.ServiceInvokeRequest{
		Context: h.RequestContext(parent), ServiceId: serviceID, Version: version,
		Operation: operation, Input: cloneTypedDocument(input),
	})
}

// InvokeProvider 通过 Host 经纪调用已选择的 versioned provider slot。
// 仅接受 operation "invoke"（与 Host InvokeVersionedProvider 一致）。
// 遗留 probe/send 不得经此 helper；须覆盖 ProviderCall 或使用兼容 API。
func (h *Host) InvokeProvider(
	ctx context.Context,
	parent *protocolwire.RequestContext,
	slotID, contractVersion, operation string,
	input *protocolwire.TypedDocument,
) (*hostwire.ProviderInvokeResponse, error) {
	slotID = strings.TrimSpace(slotID)
	contractVersion = strings.TrimSpace(contractVersion)
	operation = strings.TrimSpace(operation)
	if slotID == "" || contractVersion == "" {
		return nil, fmt.Errorf("pluginv2: provider slot and contract version are required")
	}
	// operation 校验先于 broker 可用性，避免把非法操作伪装成 host unavailable。
	if operation != VersionedProviderOperationInvoke {
		return nil, fmt.Errorf("%w: %q", ErrProviderOperationRejected, operation)
	}
	if h == nil || h.Services == nil {
		return nil, ErrHostUnavailable
	}
	return h.Services.InvokeProvider(ctx, &hostwire.ProviderInvokeRequest{
		Context: h.RequestContext(parent), SlotId: slotID, ContractVersion: contractVersion,
		Operation: VersionedProviderOperationInvoke, Input: cloneTypedDocument(input),
	})
}

// 有意不提供 ScheduleService List/Trigger helper 方法：
// Host.Schedules 仅是生成的 wire 客户端字段；当前 Host 进程未注册
// ScheduleService。插件应通过 Manifest schedules[] + 宿主 River 周期触发。
