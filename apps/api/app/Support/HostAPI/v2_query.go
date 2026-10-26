package hostapi

import (
	"context"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
)

type protocolV2QueryServer struct {
	hostv2.UnimplementedHostQueryServiceServer
	core *protocolV2Core
}

func (s *protocolV2QueryServer) Execute(ctx context.Context, request *hostv2.QueryRequest) (*hostv2.QueryResponse, error) {
	switch request.GetQueryId() {
	case QueryOwnSettingsID:
		return s.executeOwnSettingsQuery(ctx, request), nil
	case QueryExtensionInventoryID:
		return s.executeExtensionInventoryQuery(ctx, request), nil
	}
	if isStableProtocolV2QueryID(request.GetQueryId()) {
		if s == nil || s.core == nil || s.core.queries == nil {
			response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{}}
			response.Error = queryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.query_backend_unavailable", "Stable Host Queries are not configured.", true)
			return response, nil
		}
		return s.core.queries.execute(ctx, request), nil
	}
	if isProtocolV2QueryRegistryRequest(request) {
		if s == nil || s.core == nil || s.core.queryRegistry == nil {
			response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{}}
			response.Error = queryRegistryProtocolV2Error(ErrProtocolV2QueryRegistryUnavailable)
			return response, nil
		}
		return s.core.queryRegistry.execute(ctx, request), nil
	}
	response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext())}
	response.Error = protocolV2Unsupported("host.query_unsupported", "The query id or plan version is not supported.")
	return response, nil
}

func (s *protocolV2QueryServer) executeOwnSettingsQuery(ctx context.Context, request *hostv2.QueryRequest) *hostv2.QueryResponse {
	response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext())}
	if request.GetPlanVersion() != QueryOwnSettingsVersion {
		response.Error = protocolV2Unsupported("host.query_unsupported", "The query id or plan version is not supported.")
		return response
	}
	if (request.GetResultSchemaId() != "" && request.GetResultSchemaId() != QueryOwnSettingsSchemaID) ||
		(request.GetResultSchemaVersion() != "" && request.GetResultSchemaVersion() != QueryOwnSettingsSchemaV1) {
		response.Error = protocolV2Unsupported("host.query_schema_mismatch", "The requested result schema is not supported.")
		return response
	}
	if len(request.GetFilters()) > 0 || len(request.GetSorts()) > 0 || request.GetPage().GetCursor() != "" ||
		request.GetOffset() != 0 || hasProtocolV2QueryRegistryContractFields(request) {
		response.Error = protocolV2Unsupported("host.query_shape_unsupported", "Own-settings compatibility query does not support filters, sorts, pagination, or Query Registry contract fields.")
		return response
	}
	result := s.core.call(ctx, request.GetContext(), MethodGetSettings, nil)
	if !result.OK {
		response.Error = protocolV2Failure(result.Reason, result.Message)
		return response
	}
	settings, _ := result.Data["settings"].(map[string]any)
	if len(request.GetFields()) > 0 {
		filtered := make(map[string]any, len(request.GetFields()))
		for _, field := range request.GetFields() {
			if value, ok := settings[field]; ok {
				filtered[field] = value
			}
		}
		settings = filtered
	}
	document, err := protocolV2Document(QueryOwnSettingsSchemaID, QueryOwnSettingsSchemaV1, map[string]any{"settings": settings})
	if err != nil {
		response.Error = protocolV2Failure("host.query_encode_failed", err.Error())
		return response
	}
	response.Rows = []*protocolv2.TypedDocument{document}
	response.Page = &protocolv2.PageInfo{}
	return response
}

func (s *protocolV2QueryServer) executeExtensionInventoryQuery(ctx context.Context, request *hostv2.QueryRequest) *hostv2.QueryResponse {
	response := &hostv2.QueryResponse{Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{}}
	if request.GetPlanVersion() != QueryExtensionInventoryVersion {
		response.Error = protocolV2Unsupported("host.query_unsupported", "The query id or plan version is not supported.")
		return response
	}
	if (request.GetResultSchemaId() != "" && request.GetResultSchemaId() != QueryExtensionInventorySchemaID) ||
		(request.GetResultSchemaVersion() != "" && request.GetResultSchemaVersion() != QueryExtensionInventorySchemaV1) {
		response.Error = protocolV2Unsupported("host.query_schema_mismatch", "The requested result schema is not supported.")
		return response
	}
	if len(request.GetSorts()) > 0 || request.GetPage().GetCursor() != "" || hasProtocolV2QueryRegistryContractFields(request) {
		response.Error = protocolV2Unsupported("host.query_shape_unsupported", "Extension inventory query does not support sorts, cursors, or Query Registry contract fields.")
		return response
	}
	// 仅允许 type 等值过滤；其他过滤形状失败关闭。
	for _, filter := range request.GetFilters() {
		if filter == nil || filter.GetField() != "type" || filter.GetOperator() != "eq" {
			response.Error = protocolV2Unsupported("host.query_shape_unsupported", "Extension inventory only supports type eq filters.")
			return response
		}
	}
	result := s.core.call(ctx, request.GetContext(), MethodListExtensionInventory, nil)
	if !result.OK {
		response.Error = protocolV2Failure(result.Reason, result.Message)
		return response
	}
	rawItems, _ := result.Data["extensions"].([]map[string]any)
	typeFilter := ""
	for _, filter := range request.GetFilters() {
		if filter == nil || filter.GetField() != "type" {
			continue
		}
		document := filter.GetValue()
		if document == nil || document.GetValue() == nil {
			response.Error = protocolV2Unsupported("host.query_shape_unsupported", "Extension inventory type filter requires a text value.")
			return response
		}
		values := document.GetValue().AsMap()
		text, ok := values["value"].(string)
		if !ok || text == "" || (text != "plugin" && text != "theme") {
			response.Error = protocolV2Unsupported("host.query_shape_unsupported", "Extension inventory type filter must be plugin or theme.")
			return response
		}
		typeFilter = text
	}
	items := make([]map[string]any, 0, len(rawItems))
	for _, item := range rawItems {
		if typeFilter != "" {
			if typed, _ := item["type"].(string); typed != typeFilter {
				continue
			}
		}
		if len(request.GetFields()) > 0 {
			filtered := make(map[string]any, len(request.GetFields()))
			for _, field := range request.GetFields() {
				if value, ok := item[field]; ok {
					filtered[field] = value
				}
			}
			item = filtered
		}
		items = append(items, item)
	}
	// 偏移分页：Host 持有完整去敏页，limit 默认 50、上限 100。
	limit := int(request.GetPage().GetLimit())
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := int(request.GetOffset())
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	page := items[offset:end]
	response.Rows = make([]*protocolv2.TypedDocument, 0, len(page))
	for _, item := range page {
		document, err := protocolV2Document(QueryExtensionInventorySchemaID, QueryExtensionInventorySchemaV1, item)
		if err != nil {
			response.Error = protocolV2Failure("host.query_encode_failed", err.Error())
			response.Rows = nil
			return response
		}
		response.Rows = append(response.Rows, document)
	}
	if end < len(items) {
		response.Page.HasMore = true
		response.NextOffset = uint64(end)
	}
	return response
}

func hasProtocolV2QueryRegistryContractFields(request *hostv2.QueryRequest) bool {
	return request.GetContractVersion() != "" || len(request.GetRelations()) > 0 ||
		request.GetScope() != "" || request.GetActorDelegation() != ""
}

func isProtocolV2QueryRegistryRequest(request *hostv2.QueryRequest) bool {
	return request.GetContractVersion() != "" || request.GetActorDelegation() != ""
}

func (s *protocolV2QueryServer) Stream(request *hostv2.QueryRequest, stream grpc.ServerStreamingServer[hostv2.QueryRow]) error {
	response, err := s.Execute(stream.Context(), request)
	if err != nil {
		return err
	}
	if response.GetError() != nil {
		row := &hostv2.QueryRow{Context: response.GetContext(), Error: response.GetError()}
		if isProtocolV2QueryRegistryRequest(request) {
			row.Sequence = 1
			row.Final = true
		}
		return stream.Send(row)
	}
	for index, row := range response.GetRows() {
		if err := stream.Send(&hostv2.QueryRow{Context: response.GetContext(), Sequence: uint64(index + 1), Value: row}); err != nil {
			return err
		}
	}
	if isProtocolV2QueryRegistryRequest(request) {
		return stream.Send(&hostv2.QueryRow{
			Context: response.GetContext(), Sequence: uint64(len(response.GetRows()) + 1),
			Page: response.GetPage(), NextOffset: response.GetNextOffset(), Final: true,
		})
	}
	return nil
}
