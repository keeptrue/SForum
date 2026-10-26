package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"golang.org/x/net/http/httpguts"
)

var ErrRouteMutation = errors.New("routes: route mutation rejected")

const (
	routeMutationPatchMaximumBytes    = 4 << 20
	routeMutationBodyMaximumBytes     = 8 << 20
	routeMutationDocumentMaximumBytes = routeMutationBodyMaximumBytes + 256<<10
	routeMutationMetadataMaximumBytes = 1 << 20
)

type RoutePatchOperationKind string

const (
	RoutePatchAdd     RoutePatchOperationKind = "add"
	RoutePatchReplace RoutePatchOperationKind = "replace"
	RoutePatchRemove  RoutePatchOperationKind = "remove"
)

// RoutePatchOperation is the Host-supported RFC 6902 subset. Value is required
// for add/replace and omitted for remove; callers must never log Value.
type RoutePatchOperation struct {
	Kind  RoutePatchOperationKind
	Path  string
	Value json.RawMessage
}

type routeJSONPatchOperation struct {
	Operation string          `json:"op"`
	Path      string          `json:"path"`
	Value     json.RawMessage `json:"value,omitempty"`
}

func applyRouteRequestPatch(
	current DispatchRequest,
	operations []RoutePatchOperation,
	allowed []string,
	rawCredentialAuthority bool,
) (DispatchRequest, error) {
	if len(operations) == 0 {
		return cloneDispatchRequest(current), nil
	}
	for _, pointer := range allowed {
		if !extensionmanifest.ValidRouteMutableRequestPointer(pointer, rawCredentialAuthority) {
			return DispatchRequest{}, fmt.Errorf("%w: invalid frozen request allowlist", ErrRouteMutation)
		}
	}
	patchOperations, touched, err := prepareRouteMutationPatch(operations, allowed)
	if err != nil {
		return DispatchRequest{}, err
	}
	connectionHeaders := map[string]struct{}{}
	if touched["headers"] {
		connectionHeaders, err = routeMutationConnectionHeaderTokens(current.Headers)
		if err != nil {
			return DispatchRequest{}, fmt.Errorf("%w: request headers exceed their budget", ErrRouteMutation)
		}
	}
	for _, operation := range operations {
		tokens, err := routeMutationPointerTokens(operation.Path)
		if err != nil || !routeRequestMutationPathAllowed(operation.Path, tokens, connectionHeaders, rawCredentialAuthority) {
			return DispatchRequest{}, fmt.Errorf("%w: request path %q is not mutable", ErrRouteMutation, operation.Path)
		}
		touched[tokens[0]] = true
	}
	document, err := routeRequestMutationDocument(current, touched)
	if err != nil {
		return DispatchRequest{}, fmt.Errorf("%w: encode request document", ErrRouteMutation)
	}
	candidate, err := applyRouteMutationDocument(document, patchOperations)
	if err != nil {
		return DispatchRequest{}, err
	}
	result := cloneDispatchRequest(current)
	if touched["query"] {
		result.Query = ""
		if value, exists := candidate["query"]; exists {
			query, err := routeMutationStringSliceMap(value)
			if err != nil {
				return DispatchRequest{}, fmt.Errorf("%w: request query must map names to string arrays", ErrRouteMutation)
			}
			values := make(url.Values, len(query))
			for key, items := range query {
				values[key] = append([]string(nil), items...)
			}
			result.Query = values.Encode()
		}
		if len(result.Query) > routeMutationMetadataMaximumBytes {
			return DispatchRequest{}, fmt.Errorf("%w: request query exceeds its budget", ErrRouteMutation)
		}
	}
	if touched["params"] {
		result.Params = map[string]string{}
		if value, exists := candidate["params"]; exists {
			params, err := routeMutationStringMap(value)
			if err != nil {
				return DispatchRequest{}, fmt.Errorf("%w: request params must be a string map", ErrRouteMutation)
			}
			result.Params = params
		}
	}
	if touched["headers"] {
		headers, err := routeMutationHTTPHeaders(candidate["headers"])
		if err != nil {
			return DispatchRequest{}, fmt.Errorf("%w: request headers are invalid", ErrRouteMutation)
		}
		result.Headers = headers
	}
	if touched["body"] {
		body, err := routeMutationBody(candidate, "body")
		if err != nil {
			return DispatchRequest{}, fmt.Errorf("%w: request body is invalid", ErrRouteMutation)
		}
		result.Body = body
	}
	return result, nil
}

func applyRouteResponsePatch(
	current DispatchResponse,
	operations []RoutePatchOperation,
	allowed []string,
) (DispatchResponse, error) {
	if len(operations) == 0 {
		return cloneDispatchResponse(current), nil
	}
	for _, pointer := range allowed {
		if !extensionmanifest.ValidRouteMutableResponsePointer(pointer) {
			return DispatchResponse{}, fmt.Errorf("%w: invalid frozen response allowlist", ErrRouteMutation)
		}
	}
	patchOperations, touched, err := prepareRouteMutationPatch(operations, allowed)
	if err != nil {
		return DispatchResponse{}, err
	}
	connectionHeaders := map[string]struct{}{}
	if touched["headers"] {
		connectionHeaders, err = routeMutationConnectionHeaderTokens(current.Headers)
		if err != nil {
			return DispatchResponse{}, fmt.Errorf("%w: response headers exceed their budget", ErrRouteMutation)
		}
	}
	for _, operation := range operations {
		tokens, err := routeMutationPointerTokens(operation.Path)
		if err != nil || !routeResponseMutationPathAllowed(operation.Path, tokens, connectionHeaders) {
			return DispatchResponse{}, fmt.Errorf("%w: response path %q is not mutable", ErrRouteMutation, operation.Path)
		}
		touched[tokens[0]] = true
	}
	document, err := routeResponseMutationDocument(current, touched)
	if err != nil {
		return DispatchResponse{}, fmt.Errorf("%w: encode response document", ErrRouteMutation)
	}
	candidate, err := applyRouteMutationDocument(document, patchOperations)
	if err != nil {
		return DispatchResponse{}, err
	}
	result := cloneDispatchResponse(current)
	if touched["status"] {
		status, err := routeMutationStatus(candidate["status"])
		if err != nil {
			return DispatchResponse{}, fmt.Errorf("%w: response status is invalid", ErrRouteMutation)
		}
		result.Status = status
	}
	if touched["headers"] {
		headers, err := routeMutationHTTPHeaders(candidate["headers"])
		if err != nil {
			return DispatchResponse{}, fmt.Errorf("%w: response headers are invalid", ErrRouteMutation)
		}
		result.Headers = headers
	}
	if touched["body"] {
		body, err := routeMutationBody(candidate, "body")
		if err != nil {
			return DispatchResponse{}, fmt.Errorf("%w: response body is invalid", ErrRouteMutation)
		}
		result.Body = body
	}
	return result, nil
}

func prepareRouteMutationPatch(
	operations []RoutePatchOperation,
	allowed []string,
) ([]routeJSONPatchOperation, map[string]bool, error) {
	if len(operations) > extensionmanifest.RouteMutableFieldsMaximumCount {
		return nil, nil, fmt.Errorf("%w: too many patch operations", ErrRouteMutation)
	}
	if len(allowed) > extensionmanifest.RouteMutableFieldsMaximumCount {
		return nil, nil, fmt.Errorf("%w: frozen allowlist exceeds its budget", ErrRouteMutation)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, pointer := range allowed {
		if !validRouteMutationPointer(pointer) {
			return nil, nil, fmt.Errorf("%w: invalid frozen allowlist", ErrRouteMutation)
		}
		if _, duplicate := allowedSet[pointer]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate frozen allowlist path", ErrRouteMutation)
		}
		allowedSet[pointer] = struct{}{}
	}
	seen := make(map[string]struct{}, len(operations))
	patchOperations := make([]routeJSONPatchOperation, 0, len(operations))
	touched := make(map[string]bool)
	valueBytes := 0
	for _, operation := range operations {
		if !validRouteMutationPointer(operation.Path) {
			return nil, nil, fmt.Errorf("%w: invalid patch path", ErrRouteMutation)
		}
		if _, ok := allowedSet[operation.Path]; !ok {
			return nil, nil, fmt.Errorf("%w: undeclared patch path %q", ErrRouteMutation, operation.Path)
		}
		if _, duplicate := seen[operation.Path]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate patch path %q", ErrRouteMutation, operation.Path)
		}
		seen[operation.Path] = struct{}{}
		tokens, _ := routeMutationPointerTokens(operation.Path)
		touched[tokens[0]] = true

		wire := routeJSONPatchOperation{Operation: string(operation.Kind), Path: operation.Path}
		switch operation.Kind {
		case RoutePatchAdd, RoutePatchReplace:
			if len(operation.Value) == 0 || !json.Valid(operation.Value) {
				return nil, nil, fmt.Errorf("%w: patch value is required", ErrRouteMutation)
			}
			valueBytes += len(operation.Value)
			if valueBytes > routeMutationPatchMaximumBytes {
				return nil, nil, fmt.Errorf("%w: patch values exceed their budget", ErrRouteMutation)
			}
			wire.Value = append(json.RawMessage(nil), operation.Value...)
		case RoutePatchRemove:
			if len(operation.Value) != 0 {
				return nil, nil, fmt.Errorf("%w: remove patch cannot carry a value", ErrRouteMutation)
			}
		default:
			return nil, nil, fmt.Errorf("%w: unsupported patch operation", ErrRouteMutation)
		}
		patchOperations = append(patchOperations, wire)
	}
	patchBody, err := json.Marshal(patchOperations)
	if err != nil || len(patchBody) > routeMutationPatchMaximumBytes {
		return nil, nil, fmt.Errorf("%w: patch operations exceed their budget", ErrRouteMutation)
	}
	return patchOperations, touched, nil
}

func applyRouteMutationDocument(
	document map[string]any,
	patchOperations []routeJSONPatchOperation,
) (map[string]any, error) {
	documentBody, err := json.Marshal(document)
	if err != nil || len(documentBody) > routeMutationDocumentMaximumBytes {
		return nil, fmt.Errorf("%w: mutation document exceeds its budget", ErrRouteMutation)
	}
	patchBody, err := json.Marshal(patchOperations)
	if err != nil || len(patchBody) > routeMutationPatchMaximumBytes {
		return nil, fmt.Errorf("%w: patch operations exceed their budget", ErrRouteMutation)
	}
	patch, err := jsonpatch.DecodePatch(patchBody)
	if err != nil {
		return nil, fmt.Errorf("%w: decode patch operations", ErrRouteMutation)
	}
	options := jsonpatch.NewApplyOptions()
	options.SupportNegativeIndices = false
	options.AllowMissingPathOnRemove = false
	options.EnsurePathExistsOnAdd = false
	candidateBody, err := patch.ApplyWithOptions(documentBody, options)
	if err != nil {
		return nil, fmt.Errorf("%w: apply patch operations", ErrRouteMutation)
	}
	if len(candidateBody) > routeMutationDocumentMaximumBytes {
		return nil, fmt.Errorf("%w: patched document exceeds its budget", ErrRouteMutation)
	}
	candidate := make(map[string]any)
	decoder := json.NewDecoder(bytes.NewReader(candidateBody))
	decoder.UseNumber()
	if err := decoder.Decode(&candidate); err != nil {
		return nil, fmt.Errorf("%w: decode patched document", ErrRouteMutation)
	}
	return candidate, nil
}

func routeRequestMutationDocument(request DispatchRequest, touched map[string]bool) (map[string]any, error) {
	document := make(map[string]any, len(touched))
	if touched["query"] {
		if len(request.Query) > routeMutationMetadataMaximumBytes {
			return nil, ErrRouteMutation
		}
		queryValues, err := url.ParseQuery(request.Query)
		if err != nil {
			return nil, err
		}
		query := make(map[string][]string, len(queryValues))
		for key, values := range queryValues {
			query[key] = append([]string(nil), values...)
		}
		document["query"] = query
	}
	if touched["params"] {
		if routeMutationStringMapSize(request.Params) > routeMutationMetadataMaximumBytes {
			return nil, ErrRouteMutation
		}
		document["params"] = cloneRouteExecutionParams(request.Params)
	}
	if touched["headers"] {
		headers, err := routeMutationHeaderDocument(request.Headers)
		if err != nil {
			return nil, err
		}
		document["headers"] = headers
	}
	if touched["body"] {
		if len(request.Body) > routeMutationBodyMaximumBytes {
			return nil, ErrRouteMutation
		}
		body, err := decodeRouteMutationBody(request.Body)
		if err != nil {
			return nil, err
		}
		document["body"] = body
	}
	return document, nil
}

func routeResponseMutationDocument(response DispatchResponse, touched map[string]bool) (map[string]any, error) {
	document := make(map[string]any, len(touched))
	if touched["status"] {
		document["status"] = response.Status
	}
	if touched["headers"] {
		headers, err := routeMutationHeaderDocument(response.Headers)
		if err != nil {
			return nil, err
		}
		document["headers"] = headers
	}
	if touched["body"] {
		if len(response.Body) > routeMutationBodyMaximumBytes {
			return nil, ErrRouteMutation
		}
		body, err := decodeRouteMutationBody(response.Body)
		if err != nil {
			return nil, err
		}
		document["body"] = body
	}
	return document, nil
}

func decodeRouteMutationBody(body []byte) (any, error) {
	if len(body) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrRouteMutation
	}
	return value, nil
}

func routeMutationBody(document map[string]any, key string) ([]byte, error) {
	value, exists := document[key]
	if !exists {
		return nil, nil
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) > routeMutationBodyMaximumBytes {
		return nil, ErrRouteMutation
	}
	return body, nil
}

func routeMutationHeaderDocument(headers http.Header) (map[string]any, error) {
	result := make(map[string]any, len(headers))
	size := 0
	for name, values := range headers {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" || trimmed != name || !httpguts.ValidHeaderFieldName(trimmed) {
			return nil, ErrRouteMutation
		}
		canonical := strings.ToLower(trimmed)
		size += len(canonical)
		if size > routeMutationMetadataMaximumBytes {
			return nil, ErrRouteMutation
		}
		current, _ := result[canonical].([]string)
		result[canonical] = append(current, values...)
		for _, value := range values {
			size += len(value)
			if size > routeMutationMetadataMaximumBytes {
				return nil, ErrRouteMutation
			}
		}
	}
	return result, nil
}

func routeMutationHTTPHeaders(value any) (http.Header, error) {
	document, ok := value.(map[string]any)
	if !ok {
		return nil, ErrRouteMutation
	}
	result := make(http.Header, len(document))
	size := 0
	for name, rawValues := range document {
		name = strings.ToLower(strings.TrimSpace(name))
		if !httpguts.ValidHeaderFieldName(name) {
			return nil, ErrRouteMutation
		}
		size += len(name)
		if size > routeMutationMetadataMaximumBytes {
			return nil, ErrRouteMutation
		}
		values, ok := rawValues.([]any)
		if !ok {
			return nil, ErrRouteMutation
		}
		for _, rawValue := range values {
			value, ok := rawValue.(string)
			if !ok || !httpguts.ValidHeaderFieldValue(value) {
				return nil, ErrRouteMutation
			}
			result.Add(name, value)
			size += len(value)
			if size > routeMutationMetadataMaximumBytes {
				return nil, ErrRouteMutation
			}
		}
	}
	return result, nil
}

func routeMutationStringMap(value any) (map[string]string, error) {
	document, ok := value.(map[string]any)
	if !ok {
		return nil, ErrRouteMutation
	}
	result := make(map[string]string, len(document))
	size := 0
	for key, rawValue := range document {
		value, ok := rawValue.(string)
		if !ok {
			return nil, ErrRouteMutation
		}
		result[key] = value
		size += len(key) + len(value)
		if size > routeMutationMetadataMaximumBytes {
			return nil, ErrRouteMutation
		}
	}
	return result, nil
}

func routeMutationStringSliceMap(value any) (map[string][]string, error) {
	document, ok := value.(map[string]any)
	if !ok {
		return nil, ErrRouteMutation
	}
	result := make(map[string][]string, len(document))
	size := 0
	for key, rawValues := range document {
		values, ok := rawValues.([]any)
		if !ok || len(values) == 0 {
			return nil, ErrRouteMutation
		}
		size += len(key)
		if size > routeMutationMetadataMaximumBytes {
			return nil, ErrRouteMutation
		}
		items := make([]string, len(values))
		for index, rawValue := range values {
			value, ok := rawValue.(string)
			if !ok {
				return nil, ErrRouteMutation
			}
			items[index] = value
			size += len(value)
			if size > routeMutationMetadataMaximumBytes {
				return nil, ErrRouteMutation
			}
		}
		result[key] = items
	}
	return result, nil
}

func routeMutationStringMapSize(values map[string]string) int {
	size := 0
	for key, value := range values {
		size += len(key) + len(value)
		if size > routeMutationMetadataMaximumBytes {
			return size
		}
	}
	return size
}

func routeMutationStatus(value any) (int, error) {
	var status int64
	switch value := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(string(value), 10, 32)
		if err != nil {
			return 0, err
		}
		status = parsed
	case int:
		status = int64(value)
	default:
		return 0, ErrRouteMutation
	}
	if !ValidTerminalResponseStatus(extensionmanifest.RouteModeHTTP, int(status)) {
		return 0, ErrRouteMutation
	}
	return int(status), nil
}

func validRouteMutationPointer(value string) bool {
	if value == "" || value != strings.TrimSpace(value) ||
		len(value) > extensionmanifest.RouteMutableFieldMaximumBytes || value[0] != '/' ||
		len(value) > 1 && (value[1] == '/' || value[1] == '\\') ||
		strings.Count(value, "/") > extensionmanifest.RouteMutableFieldMaximumTokens {
		return false
	}
	for index := 1; index < len(value); index++ {
		if value[index] != '~' {
			continue
		}
		index++
		if index >= len(value) || value[index] != '0' && value[index] != '1' {
			return false
		}
	}
	return true
}

func routeMutationPointerTokens(pointer string) ([]string, error) {
	if !validRouteMutationPointer(pointer) {
		return nil, ErrRouteMutation
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		parts[index] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func routeRequestMutationPathAllowed(pointer string, tokens []string, connectionHeaders map[string]struct{}, raw bool) bool {
	return extensionmanifest.ValidRouteMutableRequestPointer(pointer, raw) &&
		!routeMutationTargetsConnectionHeader(tokens, connectionHeaders)
}

func routeResponseMutationPathAllowed(pointer string, tokens []string, connectionHeaders map[string]struct{}) bool {
	return extensionmanifest.ValidRouteMutableResponsePointer(pointer) &&
		!routeMutationTargetsConnectionHeader(tokens, connectionHeaders)
}

func routeMutationTargetsConnectionHeader(tokens []string, connectionHeaders map[string]struct{}) bool {
	if len(tokens) < 2 || tokens[0] != "headers" {
		return false
	}
	_, blocked := connectionHeaders[tokens[1]]
	return blocked
}

func routeMutationConnectionHeaderTokens(headers http.Header) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	size := 0
	for name, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), "Connection") {
			continue
		}
		size += len(name)
		for _, value := range values {
			size += len(value)
			if size > routeMutationMetadataMaximumBytes {
				return nil, ErrRouteMutation
			}
			for _, token := range strings.Split(value, ",") {
				if canonical := strings.ToLower(strings.TrimSpace(token)); canonical != "" {
					result[canonical] = struct{}{}
				}
			}
		}
	}
	return result, nil
}
