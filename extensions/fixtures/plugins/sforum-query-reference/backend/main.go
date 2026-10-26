package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
)

const (
	publicQueryID    = "sforum.query-reference.items"
	privateQueryID   = "sforum.query-reference.private"
	cacheableQueryID = "sforum.query-reference.cacheable"
	filterID         = "sforum.query-reference.items.mask"
	crossFilterID    = "sforum.query-filter-reference.items.mask"
	handler          = "sforum.query-reference.items"
	filter           = "sforum.query-reference.items.mask"
	crossFilter      = "sforum.query-filter-reference.items.mask"
	totalItems       = 5
)

func main() {
	pluginv2.Serve(pluginv2.NewServer().
		WithFeatures(pluginv2.QueryRuntimeProtocolFeature()).
		WithQueryRuntimeHandlers(pluginv2.QueryRuntimeHandlers{
			InvokeQuery:       invokeReferenceItems,
			FilterQueryResult: filterReferenceItems,
		}),
	)
}

func invokeReferenceItems(ctx context.Context, call *pluginv2.QueryRuntimeCall) ([]json.RawMessage, error) {
	if call == nil || call.Binding == nil || call.Plan == nil {
		return nil, errors.New("missing query call")
	}
	if call.Context.GetActor() != nil || len(call.Context.GetGrantedAuthority()) != 0 {
		return nil, errors.New("authority projection leaked into query runtime")
	}
	// public/private 共用同一 handler 符号；Host 按 queryId 区分权限与分页语义。
	queryID := call.Binding.GetQueryId()
	if call.Binding.GetHandler() != handler ||
		(queryID != publicQueryID && queryID != privateQueryID && queryID != cacheableQueryID) {
		return nil, errors.New("query binding drifted")
	}
	// 测试触发：filter state=fail → 插件失败；state=bad-schema → 返回未声明字段。
	for _, item := range call.Plan.GetFilters() {
		switch item.GetField() {
		case "state":
			switch item.GetValue() {
			case "fail":
				return nil, errors.New("reference query failure")
			case "timeout":
				if err := markReferenceQueryReady(); err != nil {
					return nil, err
				}
				<-ctx.Done()
				return nil, context.Cause(ctx)
			case "bad-schema":
				return []json.RawMessage{json.RawMessage(`{"id":"1","title":"leak","secret":"no"}`)}, nil
			}
		}
	}
	if queryID == privateQueryID {
		return referenceRows([]int{1})
	}
	sorts := call.Plan.GetSorts()
	if len(sorts) != 1 || sorts[0].GetField() != "id" {
		return nil, errors.New("reference query sort contract drifted")
	}
	offset := int(call.Plan.GetPagination().GetOffset())
	limit := int(call.Plan.GetFetchLimit())
	if limit < 1 {
		limit = 1
	}
	ids := make([]int, 0, limit)
	for index := 0; index < limit && offset+index < totalItems; index++ {
		id := offset + index + 1
		if sorts[0].GetDescending() {
			id = totalItems - offset - index
		}
		ids = append(ids, id)
	}
	return referenceRows(ids)
}

func referenceRows(ids []int) ([]json.RawMessage, error) {
	rows := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		title := "item-" + strconv.Itoa(id)
		// 保留大整数词素，证明 Host 解码路径无损。
		body, err := json.Marshal(map[string]any{
			"id":    strconv.Itoa(id),
			"title": title,
			"score": json.Number("9007199254740993"),
		})
		if err != nil {
			return nil, err
		}
		rows = append(rows, body)
	}
	return rows, nil
}

func filterReferenceItems(ctx context.Context, call *pluginv2.QueryResultFilterRuntimeCall) ([]json.RawMessage, error) {
	if call == nil || call.Binding == nil {
		return nil, errors.New("missing filter call")
	}
	handlerID := call.Binding.GetHandler()
	filterBindingID := call.Binding.GetFilterId()
	if (handlerID != filter || filterBindingID != filterID) &&
		(handlerID != crossFilter || filterBindingID != crossFilterID) {
		return nil, errors.New("filter binding drifted")
	}
	if handlerID == crossFilter {
		for _, item := range call.Plan.GetFilters() {
			if item.GetField() == "state" && item.GetValue() == "filter-timeout" {
				if err := markReferenceQueryReady(); err != nil {
					return nil, err
				}
				<-ctx.Done()
				return nil, context.Cause(ctx)
			}
		}
	}
	suffix := " | masked"
	if handlerID == crossFilter {
		suffix = " | externally masked"
	}
	result := make([]json.RawMessage, 0, len(call.Rows))
	for _, row := range call.Rows {
		decoded, err := pluginv2.DecodeQueryRuntimeRow(row)
		if err != nil {
			return nil, err
		}
		title, _ := decoded["title"].(string)
		decoded["title"] = strings.TrimSpace(title) + suffix
		// 禁止新增 undeclared 字段；仅改写已选 title。
		body, err := json.Marshal(decoded)
		if err != nil {
			return nil, err
		}
		result = append(result, body)
	}
	return result, nil
}

func markReferenceQueryReady() error {
	path := strings.TrimSpace(os.Getenv("SFORUM_SETTING_READY_FILE"))
	if path == "" {
		return errors.New("missing query ready marker path")
	}
	return os.WriteFile(path, []byte("ready"), 0o600)
}
