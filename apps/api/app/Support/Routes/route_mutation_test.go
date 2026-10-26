package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

func TestApplyRouteRequestPatchMutatesOnlyDeclaredFields(t *testing.T) {
	request := DispatchRequest{
		Method: "POST", Path: "/topics/7", Query: "page=1", Params: map[string]string{"id": "7"},
		Headers: http.Header{"Content-Type": {"application/json"}, "X-Trace": {"old"}},
		Body:    []byte(`{"title":"before","meta":{"a/b":"old"}}`), ActorID: 42, Authenticated: true,
		Permissions: map[string]bool{"forum.topic.update": true}, ClientIP: "127.0.0.1",
	}
	operations := []RoutePatchOperation{
		{Kind: RoutePatchReplace, Path: "/query/page/0", Value: routePatchValue(t, "2")},
		{Kind: RoutePatchReplace, Path: "/params/id", Value: routePatchValue(t, "8")},
		{Kind: RoutePatchReplace, Path: "/headers/x-trace/0", Value: routePatchValue(t, "new")},
		{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")},
		{Kind: RoutePatchReplace, Path: "/body/meta/a~1b", Value: routePatchValue(t, "changed")},
		{Kind: RoutePatchAdd, Path: "/body/tags", Value: routePatchValue(t, []string{"p6"})},
	}
	allowed := routePatchPaths(operations)

	result, err := applyRouteRequestPatch(request, operations, allowed, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "page=2" || result.Params["id"] != "8" || result.Headers.Get("X-Trace") != "new" ||
		result.Headers.Get("Content-Type") != "application/json" || result.ActorID != 42 ||
		!result.Authenticated || !result.Permissions["forum.topic.update"] || result.ClientIP != "127.0.0.1" {
		t.Fatalf("patched request = %#v", result)
	}
	assertRouteMutationJSON(t, result.Body, map[string]any{
		"title": "after", "meta": map[string]any{"a/b": "changed"}, "tags": []any{"p6"},
	})
	if request.Query != "page=1" || request.Params["id"] != "7" || request.Headers.Get("X-Trace") != "old" ||
		string(request.Body) != `{"title":"before","meta":{"a/b":"old"}}` {
		t.Fatalf("source request mutated: %#v", request)
	}
}

func TestApplyRouteResponsePatchMutatesOnlyDeclaredFields(t *testing.T) {
	response := DispatchResponse{
		Status:        http.StatusOK,
		Headers:       http.Header{"Content-Type": {"application/json"}, "X-Result": {"old"}},
		Body:          []byte(`{"title":"before","obsolete":true}`),
		CanonicalPath: "/host-canonical",
	}
	operations := []RoutePatchOperation{
		{Kind: RoutePatchReplace, Path: "/status", Value: routePatchValue(t, http.StatusCreated)},
		{Kind: RoutePatchReplace, Path: "/headers/x-result/0", Value: routePatchValue(t, "new")},
		{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")},
		{Kind: RoutePatchRemove, Path: "/body/obsolete"},
	}
	result, err := applyRouteResponsePatch(response, operations, routePatchPaths(operations))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusCreated || result.Headers.Get("X-Result") != "new" ||
		result.Headers.Get("Content-Type") != "application/json" || result.CanonicalPath != "/host-canonical" {
		t.Fatalf("patched response = %#v", result)
	}
	assertRouteMutationJSON(t, result.Body, map[string]any{"title": "after"})
	if response.Status != http.StatusOK || response.Headers.Get("X-Result") != "old" ||
		string(response.Body) != `{"title":"before","obsolete":true}` {
		t.Fatalf("source response mutated: %#v", response)
	}
}

func TestRouteMutationRequiresExactAllowlistAndValidOperations(t *testing.T) {
	request := DispatchRequest{Body: []byte(`{"title":"before"}`)}
	tests := []struct {
		name       string
		operations []RoutePatchOperation
		allowed    []string
	}{
		{"undeclared child", []RoutePatchOperation{{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")}}, []string{"/body"}},
		{"root", []RoutePatchOperation{{Kind: RoutePatchReplace, Path: "", Value: routePatchValue(t, map[string]any{})}}, []string{""}},
		{"invalid escape", []RoutePatchOperation{{Kind: RoutePatchReplace, Path: "/body/~2", Value: routePatchValue(t, "after")}}, []string{"/body/~2"}},
		{"duplicate path", []RoutePatchOperation{
			{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "one")},
			{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "two")},
		}, []string{"/body/title"}},
		{"add without value", []RoutePatchOperation{{Kind: RoutePatchAdd, Path: "/body/title"}}, []string{"/body/title"}},
		{"replace invalid value", []RoutePatchOperation{{Kind: RoutePatchReplace, Path: "/body/title", Value: json.RawMessage(`{`)}}, []string{"/body/title"}},
		{"remove with value", []RoutePatchOperation{{Kind: RoutePatchRemove, Path: "/body/title", Value: routePatchValue(t, nil)}}, []string{"/body/title"}},
		{"unsupported operation", []RoutePatchOperation{{Kind: "move", Path: "/body/title"}}, []string{"/body/title"}},
		{"missing target", []RoutePatchOperation{{Kind: RoutePatchReplace, Path: "/body/missing", Value: routePatchValue(t, "after")}}, []string{"/body/missing"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := applyRouteRequestPatch(request, test.operations, test.allowed, false)
			if !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("mutation error = %v", err)
			}
			if !reflect.DeepEqual(result, DispatchRequest{}) || string(request.Body) != `{"title":"before"}` {
				t.Fatalf("rejected mutation changed state: result=%#v source=%s", result, request.Body)
			}
		})
	}

	over := make([]RoutePatchOperation, extensionmanifest.RouteMutableFieldsMaximumCount+1)
	allowed := make([]string, len(over))
	for index := range over {
		path := fmt.Sprintf("/body/f%d", index)
		over[index] = RoutePatchOperation{Kind: RoutePatchAdd, Path: path, Value: routePatchValue(t, index)}
		allowed[index] = path
	}
	if _, err := applyRouteRequestPatch(request, over, allowed, false); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("operation count error = %v", err)
	}
	operation := []RoutePatchOperation{{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")}}
	if _, err := applyRouteRequestPatch(request, operation, []string{"/body/title", "/body/title"}, false); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("duplicate allowlist error = %v", err)
	}
	tooManyAllowed := make([]string, extensionmanifest.RouteMutableFieldsMaximumCount+1)
	for index := range tooManyAllowed {
		tooManyAllowed[index] = fmt.Sprintf("/body/a%d", index)
	}
	if _, err := applyRouteRequestPatch(request, operation, tooManyAllowed, false); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("allowlist count error = %v", err)
	}
}

func TestRouteRequestMutationRejectsUndeclaredDocumentRoots(t *testing.T) {
	request := DispatchRequest{Headers: http.Header{"X-Test": {"old"}}, Body: []byte(`{}`)}
	for _, path := range []string{"/method", "/path", "/status", "/headers"} {
		t.Run(path, func(t *testing.T) {
			operation := RoutePatchOperation{Kind: RoutePatchReplace, Path: path, Value: routePatchValue(t, "forged")}
			if _, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{path}, false); !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("request path %q error = %v", path, err)
			}
		})
	}
	response := DispatchResponse{Status: http.StatusOK, Headers: http.Header{}, Body: []byte(`{}`)}
	for _, path := range []string{"/query", "/params", "/method", "/headers"} {
		t.Run("response "+path, func(t *testing.T) {
			operation := RoutePatchOperation{Kind: RoutePatchReplace, Path: path, Value: routePatchValue(t, "forged")}
			if _, err := applyRouteResponsePatch(response, []RoutePatchOperation{operation}, []string{path}); !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("response path %q error = %v", path, err)
			}
		})
	}
}

func TestRouteMutationRejectsImpossibleOrHostOwnedPointerShapes(t *testing.T) {
	request := DispatchRequest{Query: "tag=one", Params: map[string]string{"id": "7"}, Headers: http.Header{"X-Test": {"old"}}, Body: []byte(`{}`)}
	requestPaths := []string{
		"/query/tag/0/child", "/query/tag/01", "/params/id/child", "/headers/X-Test",
		"/headers/x-test/01", "/headers/x-test/0/child", "/headers/idempotency-key",
	}
	for _, path := range requestPaths {
		t.Run("request "+path, func(t *testing.T) {
			operation := RoutePatchOperation{Kind: RoutePatchReplace, Path: path, Value: routePatchValue(t, "forged")}
			if _, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{path}, true); !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("request path %q error = %v", path, err)
			}
		})
	}

	response := DispatchResponse{Status: http.StatusOK, Headers: http.Header{"Cache-Control": {"private"}}, Body: []byte(`{}`)}
	responsePaths := []string{
		"/status/code", "/headers/Location", "/headers/location", "/headers/cache-control/01",
		"/headers/cache-control/0/child", "/headers/set-cookie", "/headers/link",
	}
	for _, path := range responsePaths {
		t.Run("response "+path, func(t *testing.T) {
			operation := RoutePatchOperation{Kind: RoutePatchReplace, Path: path, Value: routePatchValue(t, "forged")}
			if _, err := applyRouteResponsePatch(response, []RoutePatchOperation{operation}, []string{path}); !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("response path %q error = %v", path, err)
			}
		})
	}
}

func TestRouteRequestMutationCredentialAndHopHeaderPolicy(t *testing.T) {
	request := DispatchRequest{Headers: http.Header{
		"Authorization": {"Bearer original"}, "Cookie": {"session=original"},
		"Connection": {"keep-alive, X-Dynamic-Hop"}, "X-Dynamic-Hop": {"secret"},
	}}
	credentials := []string{"authorization", "cookie", "x-api-key", "x-auth-token"}
	for _, name := range credentials {
		path := "/headers/" + name
		operation := RoutePatchOperation{Kind: RoutePatchAdd, Path: path, Value: routePatchValue(t, []string{"forged"})}
		t.Run("filtered "+name, func(t *testing.T) {
			if _, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{path}, false); !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("filtered credential error = %v", err)
			}
		})
		t.Run("raw "+name, func(t *testing.T) {
			result, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{path}, true)
			if err != nil || result.Headers.Get(name) != "forged" {
				t.Fatalf("raw credential result=%#v err=%v", result.Headers, err)
			}
		})
	}
	for _, name := range []string{
		"connection", "x-dynamic-hop", "x-csrf-token", "host", "content-length", "idempotency-key",
		"Idempotency-Key", "x-sforum-actor-id", "X-Test", " x-test",
	} {
		path := "/headers/" + name
		operation := RoutePatchOperation{Kind: RoutePatchAdd, Path: path, Value: routePatchValue(t, []string{"forged"})}
		if _, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{path}, true); !errors.Is(err, ErrRouteMutation) {
			t.Fatalf("reserved request header %q error = %v", name, err)
		}
	}
}

func TestRouteMutationDistinguishesJSONNullFromRemovedBody(t *testing.T) {
	request := DispatchRequest{Body: []byte(`{"title":"before"}`)}
	response := DispatchResponse{Status: http.StatusOK, Body: []byte(`{"title":"before"}`)}
	null := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/body", Value: routePatchValue(t, nil)}
	removed := RoutePatchOperation{Kind: RoutePatchRemove, Path: "/body"}

	nullRequest, err := applyRouteRequestPatch(request, []RoutePatchOperation{null}, []string{"/body"}, false)
	if err != nil || string(nullRequest.Body) != "null" {
		t.Fatalf("null request body = %q err=%v", nullRequest.Body, err)
	}
	removedRequest, err := applyRouteRequestPatch(request, []RoutePatchOperation{removed}, []string{"/body"}, false)
	if err != nil || len(removedRequest.Body) != 0 {
		t.Fatalf("removed request body = %q err=%v", removedRequest.Body, err)
	}
	nullResponse, err := applyRouteResponsePatch(response, []RoutePatchOperation{null}, []string{"/body"})
	if err != nil || string(nullResponse.Body) != "null" {
		t.Fatalf("null response body = %q err=%v", nullResponse.Body, err)
	}
	removedResponse, err := applyRouteResponsePatch(response, []RoutePatchOperation{removed}, []string{"/body"})
	if err != nil || len(removedResponse.Body) != 0 {
		t.Fatalf("removed response body = %q err=%v", removedResponse.Body, err)
	}
}

func TestRouteRequestMutationRootRemoveClearsMaps(t *testing.T) {
	request := DispatchRequest{Query: "tag=one&tag=two", Params: map[string]string{"id": "7"}, Body: []byte(`{"title":"before"}`)}
	operations := []RoutePatchOperation{
		{Kind: RoutePatchRemove, Path: "/query"},
		{Kind: RoutePatchRemove, Path: "/params"},
		{Kind: RoutePatchRemove, Path: "/body"},
	}
	result, err := applyRouteRequestPatch(request, operations, routePatchPaths(operations), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Query != "" || len(result.Params) != 0 || result.Params == nil || len(result.Body) != 0 {
		t.Fatalf("root remove result = %#v", result)
	}
	if request.Query == "" || request.Params["id"] != "7" || len(request.Body) == 0 {
		t.Fatalf("root remove mutated source = %#v", request)
	}

	status := RoutePatchOperation{Kind: RoutePatchRemove, Path: "/status"}
	if _, err := applyRouteResponsePatch(
		DispatchResponse{Status: http.StatusOK}, []RoutePatchOperation{status}, []string{status.Path},
	); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("removed status error = %v", err)
	}
}

func TestRouteMutationPreservesLargeJSONNumbers(t *testing.T) {
	const number = `9007199254740993123456789`
	request := DispatchRequest{Body: []byte(`{"source":` + number + `}`)}
	operation := RoutePatchOperation{Kind: RoutePatchAdd, Path: "/body/copied", Value: json.RawMessage(number)}
	result, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{operation.Path}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(result.Body); got != `{"copied":`+number+`,"source":`+number+`}` && got != `{"source":`+number+`,"copied":`+number+`}` {
		t.Fatalf("large-number body = %s", got)
	}
}

func TestRouteMutationRejectsMalformedSourceHeadersAtomically(t *testing.T) {
	operation := RoutePatchOperation{Kind: RoutePatchAdd, Path: "/headers/x-test", Value: routePatchValue(t, []string{"ok"})}
	for _, name := range []string{"", "   ", " x-invalid", "bad header"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			source := DispatchRequest{Headers: http.Header{name: {"untouched"}}}
			if _, err := applyRouteRequestPatch(source, []RoutePatchOperation{operation}, []string{operation.Path}, false); !errors.Is(err, ErrRouteMutation) {
				t.Fatalf("malformed source header error = %v", err)
			}
			if source.Headers[name][0] != "untouched" {
				t.Fatalf("malformed source header mutated = %#v", source.Headers)
			}
		})
	}
}

func TestRouteResponseMutationRejectsHostAndHopHeaders(t *testing.T) {
	response := DispatchResponse{Status: http.StatusOK, Headers: http.Header{
		"Connection": {"X-Dynamic-Hop"}, "X-Dynamic-Hop": {"secret"},
	}, Body: []byte(`{}`)}
	for _, name := range []string{
		"set-cookie", "content-length", "connection", "proxy-connection", "x-dynamic-hop", "idempotency-replayed",
		"location", "link", "x-sforum-forged", "Proxy-Connection", "X-Test", " x-test",
	} {
		path := "/headers/" + name
		operation := RoutePatchOperation{Kind: RoutePatchAdd, Path: path, Value: routePatchValue(t, []string{"forged"})}
		if _, err := applyRouteResponsePatch(response, []RoutePatchOperation{operation}, []string{path}); !errors.Is(err, ErrRouteMutation) {
			t.Fatalf("reserved response header %q error = %v", name, err)
		}
	}
}

func TestRouteMutationRejectsInvalidUnusedFrozenPaths(t *testing.T) {
	requestOperation := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")}
	if _, err := applyRouteRequestPatch(
		DispatchRequest{Body: []byte(`{"title":"before"}`)}, []RoutePatchOperation{requestOperation},
		[]string{requestOperation.Path, "/headers/cookie"}, false,
	); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("invalid request allowlist error = %v", err)
	}
	responseOperation := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")}
	if _, err := applyRouteResponsePatch(
		DispatchResponse{Status: http.StatusOK, Body: []byte(`{"title":"before"}`)}, []RoutePatchOperation{responseOperation},
		[]string{responseOperation.Path, "/headers/location"},
	); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("invalid response allowlist error = %v", err)
	}
}

func TestRouteMutationLeavesUntouchedNonJSONAndRepeatedQueryFieldsOpaque(t *testing.T) {
	request := DispatchRequest{
		Query: "tag=one&tag=two", Headers: http.Header{
			"Content-Type": {"multipart/form-data; boundary=demo"}, "X-Trace": {"old"},
		}, Body: []byte("--demo\r\nbinary\x00payload\r\n--demo--"),
	}
	headerPatch := RoutePatchOperation{
		Kind: RoutePatchReplace, Path: "/headers/x-trace/0", Value: routePatchValue(t, "new"),
	}
	patchedRequest, err := applyRouteRequestPatch(request, []RoutePatchOperation{headerPatch}, []string{headerPatch.Path}, false)
	if err != nil || patchedRequest.Headers.Get("X-Trace") != "new" || patchedRequest.Query != request.Query ||
		!reflect.DeepEqual(patchedRequest.Body, request.Body) {
		t.Fatalf("opaque request patch=%#v err=%v", patchedRequest, err)
	}

	jsonRequest := request
	jsonRequest.Body = []byte(`{"title":"before"}`)
	bodyPatch := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after")}
	patchedBody, err := applyRouteRequestPatch(jsonRequest, []RoutePatchOperation{bodyPatch}, []string{bodyPatch.Path}, false)
	if err != nil || patchedBody.Query != request.Query {
		t.Fatalf("body patch with repeated query=%#v err=%v", patchedBody, err)
	}
	assertRouteMutationJSON(t, patchedBody.Body, map[string]any{"title": "after"})
	queryPatch := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/query/tag/1", Value: routePatchValue(t, "changed")}
	patchedQuery, err := applyRouteRequestPatch(jsonRequest, []RoutePatchOperation{queryPatch}, []string{queryPatch.Path}, false)
	if err != nil || patchedQuery.Query != "tag=one&tag=changed" || !reflect.DeepEqual(patchedQuery.Body, jsonRequest.Body) {
		t.Fatalf("repeated query patch=%#v err=%v", patchedQuery, err)
	}

	response := DispatchResponse{
		Status: http.StatusOK, Headers: http.Header{"Content-Type": {"text/html"}, "X-Trace": {"old"}},
		Body: []byte("<strong>opaque</strong>"),
	}
	responseOperations := []RoutePatchOperation{
		{Kind: RoutePatchReplace, Path: "/status", Value: routePatchValue(t, http.StatusCreated)},
		{Kind: RoutePatchReplace, Path: "/headers/x-trace/0", Value: routePatchValue(t, "new")},
	}
	patchedResponse, err := applyRouteResponsePatch(response, responseOperations, routePatchPaths(responseOperations))
	if err != nil || patchedResponse.Status != http.StatusCreated || patchedResponse.Headers.Get("X-Trace") != "new" ||
		!reflect.DeepEqual(patchedResponse.Body, response.Body) {
		t.Fatalf("opaque response patch=%#v err=%v", patchedResponse, err)
	}
}

func TestRouteMutationRejectsInvalidCandidateTypes(t *testing.T) {
	request := DispatchRequest{Query: "page=1", Headers: http.Header{"X-Test": {"old"}}, Body: []byte(`{}`)}
	requestTests := []RoutePatchOperation{
		{Kind: RoutePatchReplace, Path: "/query", Value: routePatchValue(t, []string{"invalid"})},
		{Kind: RoutePatchReplace, Path: "/query/page", Value: routePatchValue(t, 2)},
		{Kind: RoutePatchReplace, Path: "/headers/x-test", Value: routePatchValue(t, "invalid")},
		{Kind: RoutePatchReplace, Path: "/headers/x-test/0", Value: routePatchValue(t, "bad\r\nvalue")},
	}
	for _, operation := range requestTests {
		if _, err := applyRouteRequestPatch(request, []RoutePatchOperation{operation}, []string{operation.Path}, false); !errors.Is(err, ErrRouteMutation) {
			t.Fatalf("invalid request candidate %q error = %v", operation.Path, err)
		}
	}
	response := DispatchResponse{Status: http.StatusOK, Headers: http.Header{}, Body: []byte(`{}`)}
	for _, value := range []any{99, 100, 101, 103, 600, 200.5, "200"} {
		operation := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/status", Value: routePatchValue(t, value)}
		if _, err := applyRouteResponsePatch(response, []RoutePatchOperation{operation}, []string{"/status"}); !errors.Is(err, ErrRouteMutation) {
			t.Fatalf("invalid status %#v error = %v", value, err)
		}
	}
}

func TestRouteMutationNoopReturnsDetachedValues(t *testing.T) {
	request := DispatchRequest{Headers: http.Header{"X-Test": {"value"}}, Body: []byte(`{}`), Params: map[string]string{"id": "1"}}
	result, err := applyRouteRequestPatch(request, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	result.Headers.Set("X-Test", "changed")
	result.Body[0] = '['
	result.Params["id"] = "2"
	if request.Headers.Get("X-Test") != "value" || string(request.Body) != `{}` || request.Params["id"] != "1" {
		t.Fatalf("no-op clone leaked mutation: %#v", request)
	}
}

func TestRouteMutationBudgetsAndValidationRunBeforeDocumentDecode(t *testing.T) {
	request := DispatchRequest{Body: []byte("not-json")}
	undeclared := RoutePatchOperation{
		Kind: RoutePatchReplace, Path: "/body/title", Value: routePatchValue(t, "after"),
	}
	if _, err := applyRouteRequestPatch(request, []RoutePatchOperation{undeclared}, []string{"/headers/x-test"}, false); !errors.Is(err, ErrRouteMutation) || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("validation ordering error = %v", err)
	}

	oversizedValue := json.RawMessage(`"` + strings.Repeat("x", routeMutationPatchMaximumBytes) + `"`)
	oversizedPatch := RoutePatchOperation{Kind: RoutePatchAdd, Path: "/body/value", Value: oversizedValue}
	if _, err := applyRouteRequestPatch(
		DispatchRequest{Body: []byte(`{}`)}, []RoutePatchOperation{oversizedPatch}, []string{oversizedPatch.Path}, false,
	); !errors.Is(err, ErrRouteMutation) || strings.Contains(err.Error(), "xxx") {
		t.Fatalf("patch budget error = %v", err)
	}

	tooLargeBody := DispatchRequest{Body: []byte(`"` + strings.Repeat("b", routeMutationBodyMaximumBytes) + `"`)}
	bodyPatch := RoutePatchOperation{Kind: RoutePatchReplace, Path: "/body", Value: routePatchValue(t, nil)}
	if _, err := applyRouteRequestPatch(tooLargeBody, []RoutePatchOperation{bodyPatch}, []string{"/body"}, false); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("source body budget error = %v", err)
	}

	base := strings.Repeat("b", routeMutationBodyMaximumBytes-len(`{"base":""}`)-1024)
	expanding := DispatchRequest{Body: []byte(`{"base":"` + base + `"}`)}
	expandPatch := RoutePatchOperation{
		Kind: RoutePatchAdd, Path: "/body/extra", Value: routePatchValue(t, strings.Repeat("x", 2048)),
	}
	if _, err := applyRouteRequestPatch(expanding, []RoutePatchOperation{expandPatch}, []string{expandPatch.Path}, false); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("expanded body budget error = %v", err)
	}

	largeHeader := RoutePatchOperation{
		Kind: RoutePatchAdd, Path: "/headers/x-large",
		Value: routePatchValue(t, []string{strings.Repeat("h", routeMutationMetadataMaximumBytes)}),
	}
	if _, err := applyRouteResponsePatch(
		DispatchResponse{Status: http.StatusOK, Headers: http.Header{}, Body: []byte("opaque")},
		[]RoutePatchOperation{largeHeader}, []string{largeHeader.Path},
	); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("header budget error = %v", err)
	}

	oversizedConnection := DispatchRequest{Headers: http.Header{
		"Connection": {strings.Repeat("x", routeMutationMetadataMaximumBytes+1)},
	}}
	headerPatch := RoutePatchOperation{Kind: RoutePatchAdd, Path: "/headers/x-test", Value: routePatchValue(t, []string{"ok"})}
	if _, err := applyRouteRequestPatch(
		oversizedConnection, []RoutePatchOperation{headerPatch}, []string{headerPatch.Path}, false,
	); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("connection header budget error = %v", err)
	}

	oversizedEmptyHeader := DispatchResponse{Status: http.StatusOK, Headers: http.Header{
		strings.Repeat("x", routeMutationMetadataMaximumBytes+1): {},
	}}
	if _, err := applyRouteResponsePatch(
		oversizedEmptyHeader, []RoutePatchOperation{headerPatch}, []string{headerPatch.Path},
	); !errors.Is(err, ErrRouteMutation) {
		t.Fatalf("empty header key budget error = %v", err)
	}
}

func routePatchValue(t *testing.T, value any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func routePatchPaths(operations []RoutePatchOperation) []string {
	result := make([]string, len(operations))
	for index, operation := range operations {
		result[index] = operation.Path
	}
	return result
}

func assertRouteMutationJSON(t *testing.T, body []byte, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON body = %#v, want %#v", got, want)
	}
}

func TestValidRouteMutationPointerMatchesManifestBudgets(t *testing.T) {
	valid := []string{"/", "/body/title", "/body/a~1b", "/body/~0private",
		"/" + strings.Repeat("a", extensionmanifest.RouteMutableFieldMaximumBytes-1),
		strings.Repeat("/token", extensionmanifest.RouteMutableFieldMaximumTokens)}
	for _, pointer := range valid {
		if !validRouteMutationPointer(pointer) {
			t.Fatalf("valid pointer rejected: %q", pointer)
		}
	}
	invalid := []string{"", "body", " /body", "/body ", "//body", "/\\body", "/body/~2",
		"/" + strings.Repeat("a", extensionmanifest.RouteMutableFieldMaximumBytes),
		strings.Repeat("/token", extensionmanifest.RouteMutableFieldMaximumTokens+1)}
	for _, pointer := range invalid {
		if validRouteMutationPointer(pointer) {
			t.Fatalf("invalid pointer accepted: %q", pointer)
		}
	}
}
