package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	pluginv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2"
	pluginwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type contentServer struct {
	*pluginv2.Server
	store *articleStore
}

func (s *contentServer) InvokeRoute(
	callCtx context.Context,
	request *pluginwire.RouteRequest,
) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	switch request.GetRouteId() {
	case routeArticlesID:
		return s.handleList(callCtx, request)
	case routeArticleWriteID:
		return s.handleWrite(callCtx, request)
	case routeImportID:
		return s.handleImport(callCtx, request)
	case routeExportID:
		return s.handleExport(callCtx, request)
	case routeRenderID:
		return s.handleRender(request)
	case routeTaxonomyID, "sforum.custom-content.route.taxonomy-write":
		return s.handleTaxonomy(callCtx, request)
	case routeMigrateID:
		return s.handleMigrate(callCtx, request)
	case routeSearchID:
		return s.handleSearch(callCtx, request)
	default:
		return &pluginwire.RouteResponse{
			Context: routeResponseContext(ctx),
			Error: &protocolwire.ErrorDetail{
				Code: protocolwire.ErrorCode_ERROR_CODE_NOT_FOUND, Reason: "custom_content.route_unknown",
				Message: "unknown custom-content route",
			},
		}, nil
	}
}

func (s *contentServer) handleList(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	state := request.GetQueryParameters()["state"]
	items, err := s.store.list(callCtx, state, 0, 50)
	if err != nil {
		return routeError(ctx, "custom_content.list_failed", err.Error()), nil
	}
	rows := make([]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]any{
			"id": item.ID, "title": item.Title, "summary": item.Summary, "slug": item.Slug,
			"topicId": item.TopicID, "state": item.State,
		})
	}
	body, err := pluginv2.NewTypedDocument(articlesResponseSchema, map[string]any{
		"articles": rows, "databaseConnected": s.store.databaseConnected(),
		"schemaVersion": s.store.schemaVersion(), "source": "custom-content",
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *contentServer) handleWrite(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	values := map[string]any{}
	if request.GetBody() != nil && request.GetBody().GetValue() != nil {
		values = request.GetBody().GetValue().AsMap()
	}
	rec := articleRecord{
		ID:      stringField(values, "id"),
		Title:   stringField(values, "title"),
		Summary: stringField(values, "summary"),
		Slug:    stringField(values, "slug"),
		TopicID: stringField(values, "topicId"),
		Body:    stringField(values, "body"),
		State:   stringField(values, "state"),
	}
	if rec.ID == "" {
		rec.ID = "gen-" + time.Now().UTC().Format("150405.000")
	}
	if err := s.store.upsert(callCtx, rec); err != nil {
		return routeError(ctx, "custom_content.validation_failed", err.Error()), nil
	}
	body, err := pluginv2.NewTypedDocument(writeResponseSchema, map[string]any{
		"id": rec.ID, "slug": rec.Slug, "ok": true, "validated": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusCreated, Body: body,
	}, nil
}

func (s *contentServer) handleImport(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	raw := []byte(`{"articles":[]}`)
	if request.GetBody() != nil && request.GetBody().GetValue() != nil {
		// 允许 body.payload 为 JSON 字符串，或直接 articles 数组。
		m := request.GetBody().GetValue().AsMap()
		if payload, ok := m["payload"].(string); ok && payload != "" {
			raw = []byte(payload)
		} else {
			encoded, _ := json.Marshal(m)
			raw = encoded
		}
	}
	count, err := s.store.importJSON(callCtx, raw)
	if err != nil {
		return routeError(ctx, "custom_content.import_failed", err.Error()), nil
	}
	body, err := pluginv2.NewTypedDocument(importResponseSchema, map[string]any{
		"imported": count, "ok": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *contentServer) handleExport(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	path, count, err := s.store.exportJSON(callCtx)
	if err != nil {
		return routeError(ctx, "custom_content.export_failed", err.Error()), nil
	}
	body, err := pluginv2.NewTypedDocument(exportResponseSchema, map[string]any{
		"path": path, "count": count, "ok": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

// handleRender 服务端 fallback render：block / shortcode / embed 真实生成安全 HTML。
func (s *contentServer) handleRender(request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	handler := request.GetQueryParameters()["handler"]
	if handler == "" && request.GetBody() != nil && request.GetBody().GetValue() != nil {
		handler = stringField(request.GetBody().GetValue().AsMap(), "handler")
	}
	html, kind, plain := serverFallbackRender(handler, request)
	body, err := pluginv2.NewTypedDocument(renderResponseSchema, map[string]any{
		"handler": handler, "kind": kind, "html": html, "plainText": plain,
		"fallback": true, "sourcePreserved": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *contentServer) handleTaxonomy(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	if request.GetMethod() == http.MethodPost && request.GetBody() != nil && request.GetBody().GetValue() != nil {
		m := request.GetBody().GetValue().AsMap()
		node := taxonomyNode{
			ID: stringField(m, "id"), Label: stringField(m, "label"), ParentID: stringField(m, "parentId"),
		}
		if err := s.store.putTaxonomy(callCtx, node); err != nil {
			return routeError(ctx, "custom_content.taxonomy_failed", err.Error()), nil
		}
	}
	nodes, err := s.store.listTaxonomy(callCtx)
	if err != nil {
		return routeError(ctx, "custom_content.taxonomy_list_failed", err.Error()), nil
	}
	rows := make([]any, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, map[string]any{"id": n.ID, "label": n.Label, "parentId": n.ParentID})
	}
	body, err := pluginv2.NewTypedDocument(taxonomyResponseSchema, map[string]any{
		"taxonomy": rows, "hierarchical": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *contentServer) handleMigrate(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	ver, err := s.store.migrateSchema(callCtx)
	if err != nil {
		return routeError(ctx, "custom_content.migrate_failed", err.Error()), nil
	}
	body, err := pluginv2.NewTypedDocument(migrateResponseSchema, map[string]any{
		"schemaVersion": ver, "migrated": ver >= 2, "ok": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

func (s *contentServer) handleSearch(callCtx context.Context, request *pluginwire.RouteRequest) (*pluginwire.RouteResponse, error) {
	ctx := request.GetContext()
	q := request.GetQueryParameters()["q"]
	items, err := s.store.search(callCtx, q, 20)
	if err != nil {
		return routeError(ctx, "custom_content.search_failed", err.Error()), nil
	}
	rows := make([]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]any{
			"id": item.ID, "title": item.Title, "summary": item.Summary, "slug": item.Slug,
		})
	}
	body, err := pluginv2.NewTypedDocument(searchResponseSchema, map[string]any{
		"results": rows, "q": q, "indexed": true,
	})
	if err != nil {
		return nil, err
	}
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx), StatusCode: http.StatusOK, Body: body,
	}, nil
}

// serverFallbackRender 生成 Host-safe 服务端 HTML（禁用插件后仍可稳定 fallback）。
func serverFallbackRender(handler string, request *pluginwire.RouteRequest) (html, kind, plain string) {
	attrs := map[string]any{}
	if request.GetBody() != nil && request.GetBody().GetValue() != nil {
		attrs = request.GetBody().GetValue().AsMap()
	}
	switch handler {
	case blockVoteHandler:
		score := stringField(attrs, "score")
		if score == "" {
			score = "0"
		}
		return `<div class="sf-cc-vote" data-score="` + htmlEscape(score) + `"><span>vote</span><span>` + htmlEscape(score) + `</span></div>`,
			"block", "vote " + score
	case blockProductCardHandler:
		title := stringField(attrs, "title")
		if title == "" {
			title = "product"
		}
		return `<article class="sf-cc-product-card"><h3>` + htmlEscape(title) + `</h3></article>`,
			"block", title
	case embedMediaHandler:
		href := stringField(attrs, "href")
		if href == "" {
			href = "https://example.com/media"
		}
		return `<figure class="sf-cc-embed"><a href="` + htmlEscape(href) + `">media</a></figure>`,
			"embed", "media"
	case shortcodeBadgeHandler:
		label := stringField(attrs, "label")
		if label == "" {
			label = "badge"
		}
		return `<span class="sf-cc-badge">` + htmlEscape(label) + `</span>`,
			"shortcode", label
	case blockWorkflowFormHandler:
		return `<form class="sf-cc-workflow-form"><label>Reason</label><button type="submit">Send</button></form>`,
			"block", "Reason Send"
	default:
		// 未知 handler：稳定 fallback，不丢失源属性。
		return `<div class="sf-cc-fallback" data-handler="` + htmlEscape(handler) + `">unavailable</div>`,
			"fallback", "unavailable"
	}
}

func htmlEscape(value string) string {
	replacer := strings.NewReplacer(
		`&`, "&amp;", `<`, "&lt;", `>`, "&gt;", `"`, "&quot;", `'`, "&#39;",
	)
	return replacer.Replace(value)
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	if v, ok := values[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func routeResponseContext(request *protocolwire.RequestContext) *protocolwire.ResponseContext {
	if request == nil {
		return &protocolwire.ResponseContext{ServerTime: timestamppb.New(time.Now().UTC())}
	}
	return &protocolwire.ResponseContext{
		RequestId:  request.GetRequestId(),
		Trace:      proto.Clone(request.GetTrace()).(*protocolwire.TraceContext),
		Extension:  proto.Clone(request.GetExtension()).(*protocolwire.ExtensionIdentity),
		ServerTime: timestamppb.New(time.Now().UTC()),
	}
}

func routeError(ctx *protocolwire.RequestContext, reason, message string) *pluginwire.RouteResponse {
	return &pluginwire.RouteResponse{
		Context: routeResponseContext(ctx),
		Error: &protocolwire.ErrorDetail{
			Code: protocolwire.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: reason, Message: message,
		},
	}
}
