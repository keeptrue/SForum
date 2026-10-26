package extensionsruntime

import (
	"context"
	"fmt"

	search "github.com/keeptrue/sforum/apps/api/app/Support/Search"
)

// search.provider known-slot：经 ProviderCall 承载 index/delete/search/ensure/probe。
// 仅 Protocol V2；未实现时返回不可用。

func (c *protocolV2Client) SearchEngineProbe(SearchEngineProbeRequest) (SearchEngineProbeResponse, error) {
	response, err := c.providerCall(searchProviderSlot, searchOpProbe, map[string]any{})
	if err != nil {
		return SearchEngineProbeResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return SearchEngineProbeResponse{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
	}, nil
}

// SearchEngineProbeRequest 预留；当前无输入。
type SearchEngineProbeRequest struct{}

func (c *protocolV2Client) SearchEngineEnsure() (SearchEngineResult, error) {
	response, err := c.providerCall(searchProviderSlot, searchOpEnsure, map[string]any{
		"indexUid": search.IndexUID,
	})
	if err != nil {
		return SearchEngineResult{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return SearchEngineResult{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) SearchEngineIndex(input SearchEngineIndexRequest) (SearchEngineResult, error) {
	response, err := c.providerCall(searchProviderSlot, searchOpIndex, map[string]any{
		"document": topicSearchDocToMap(input.Document),
	})
	if err != nil {
		return SearchEngineResult{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return SearchEngineResult{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) SearchEngineDelete(input SearchEngineDeleteRequest) (SearchEngineResult, error) {
	response, err := c.providerCall(searchProviderSlot, searchOpDelete, map[string]any{
		"topicId": input.TopicID,
	})
	if err != nil {
		return SearchEngineResult{}, err
	}
	values := protocolV2Values(response.GetOutput())
	return SearchEngineResult{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
	}, nil
}

func (c *protocolV2Client) SearchEngineSearch(input SearchEngineSearchRequest) (SearchEngineSearchResponse, error) {
	response, err := c.providerCall(searchProviderSlot, searchOpSearch, map[string]any{
		"query":        input.Query,
		"categorySlug": input.CategorySlug,
		"tagSlug":      input.TagSlug,
		"page":         input.Page,
		"perPage":      input.PerPage,
		"indexUid":     search.IndexUID,
	})
	if err != nil {
		return SearchEngineSearchResponse{}, err
	}
	values := protocolV2Values(response.GetOutput())
	items := make([]search.TopicSearchDoc, 0)
	for _, raw := range anySliceValue(values, "items") {
		itemMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, mapToTopicSearchDoc(itemMap))
	}
	return SearchEngineSearchResponse{
		OK: booleanValue(values, "ok"), Reason: stringValue(values, "reason"),
		Message: stringValue(values, "message"),
		Items:   items,
		Total:   int64Value(values, "total"),
		Page:    int(int64Value(values, "page")),
		PerPage: int(int64Value(values, "perPage")),
	}, nil
}

// ProtocolStarter 搜索引擎方法：仅 Protocol V2 客户端。

func (s *ProtocolStarter) SearchEngineProbe(ctx context.Context, extensionID string, request SearchEngineProbeRequest) (SearchEngineProbeResponse, error) {
	return callSearchEngine(ctx, s.protocolFor(extensionID), func(c *protocolV2Client) (SearchEngineProbeResponse, error) {
		return c.SearchEngineProbe(request)
	}, SearchEngineProbeResponse{Reason: "extension.hook_timeout", Message: "Search Probe exceeded the host timeout."})
}

func (s *ProtocolStarter) SearchEngineEnsure(ctx context.Context, extensionID string) (SearchEngineResult, error) {
	return callSearchEngine(ctx, s.protocolFor(extensionID), func(c *protocolV2Client) (SearchEngineResult, error) {
		return c.SearchEngineEnsure()
	}, SearchEngineResult{Reason: "extension.hook_timeout", Message: "Search Ensure exceeded the host timeout."})
}

func (s *ProtocolStarter) SearchEngineIndex(ctx context.Context, extensionID string, request SearchEngineIndexRequest) (SearchEngineResult, error) {
	return callSearchEngine(ctx, s.protocolFor(extensionID), func(c *protocolV2Client) (SearchEngineResult, error) {
		return c.SearchEngineIndex(request)
	}, SearchEngineResult{Reason: "extension.hook_timeout", Message: "Search Index exceeded the host timeout."})
}

func (s *ProtocolStarter) SearchEngineDelete(ctx context.Context, extensionID string, request SearchEngineDeleteRequest) (SearchEngineResult, error) {
	return callSearchEngine(ctx, s.protocolFor(extensionID), func(c *protocolV2Client) (SearchEngineResult, error) {
		return c.SearchEngineDelete(request)
	}, SearchEngineResult{Reason: "extension.hook_timeout", Message: "Search Delete exceeded the host timeout."})
}

func (s *ProtocolStarter) SearchEngineSearch(ctx context.Context, extensionID string, request SearchEngineSearchRequest) (SearchEngineSearchResponse, error) {
	return callSearchEngine(ctx, s.protocolFor(extensionID), func(c *protocolV2Client) (SearchEngineSearchResponse, error) {
		return c.SearchEngineSearch(request)
	}, SearchEngineSearchResponse{Reason: "extension.hook_timeout", Message: "Search Query exceeded the host timeout."})
}

func callSearchEngine[T any](ctx context.Context, protocol PluginProtocol, fn func(*protocolV2Client) (T, error), timeoutVal T) (T, error) {
	var zero T
	if protocol == nil {
		return zero, fmt.Errorf("search provider runtime unavailable")
	}
	client, ok := protocol.(*protocolV2Client)
	if !ok {
		return zero, fmt.Errorf("search.provider requires Protocol V2")
	}
	type outcome struct {
		val T
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		val, err := fn(client)
		ch <- outcome{val: val, err: err}
	}()
	select {
	case <-ctx.Done():
		return timeoutVal, ctx.Err()
	case out := <-ch:
		return out.val, out.err
	}
}
