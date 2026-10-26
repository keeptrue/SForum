package main

import (
	"context"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 站内搜索由 Host 进程内 PostgresSiteEngine 短路；本插件仅声明 search.provider 槽位，
// 并在被直接调用时返回明确的 host-managed 结果，便于契约测试与目录展示。

const (
	searchSlot                  = "search.provider"
	searchLegacyContractVersion = "1"
)

type siteSearchPluginV2 struct {
	*pluginv2.Server
}

func newSiteSearchPluginV2() *siteSearchPluginV2 {
	return &siteSearchPluginV2{Server: pluginv2.NewServer()}
}

func (p *siteSearchPluginV2) ProviderCall(ctx context.Context, request *pluginwire.ProviderCallRequest) (*pluginwire.ProviderCallResponse, error) {
	response := &pluginwire.ProviderCallResponse{
		Context: &protocolwire.ResponseContext{
			RequestId: request.GetContext().GetRequestId(),
			Extension: request.GetContext().GetExtension(),
		},
	}
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
	if request.GetSlotId() != searchSlot {
		response.Error = &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "provider.slot_unsupported",
			Message: "unsupported provider slot",
		}
		return response, nil
	}
	if request.GetContractVersion() != "" && request.GetContractVersion() != searchLegacyContractVersion {
		response.Error = &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "provider.contract_mismatch",
			Message: "search.provider contract must be version 1",
		}
		return response, nil
	}

	switch request.GetOperation() {
	case "probe", "ensure":
		return p.okResult(response, true, "site.host_managed", "Host short-circuits site search in-process")
	case "index", "delete", "search":
		// Host 不应走到此处；若走到则明确失败，避免 silent no-op 掩盖装配错误。
		return p.okResult(response, false, "site.host_short_circuit_required",
			"site search is host-managed; index/delete/search must use Host PostgresSiteEngine")
	default:
		response.Error = &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "provider.operation_unsupported",
			Message: "unsupported search.provider operation",
		}
		return response, nil
	}
}

func (p *siteSearchPluginV2) okResult(response *pluginwire.ProviderCallResponse, ok bool, reason, message string) (*pluginwire.ProviderCallResponse, error) {
	output, err := pluginv2.NewTypedDocument("sforum.provider.search.provider.result@1", map[string]any{
		"ok": ok, "reason": reason, "message": message,
	})
	if err != nil {
		response.Error = &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INTERNAL, Reason: "provider.output_encode", Message: err.Error(),
		}
		return response, nil
	}
	response.Output = output
	return response, nil
}
