package extensionopenapi

import (
	"fmt"
	"net/http"
	"strings"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const idempotencyHeader = "Idempotency-Key"

// HostRoutePolicies remains a compatibility bridge for older direct Build
// callers. Production lifecycle publication leaves Policies empty so Build
// derives the authoritative values from the validated operation itself.
func HostRoutePolicies(manifest extensionmanifest.Manifest) []RoutePolicy {
	if len(manifest.OpenAPI) == 0 {
		return nil
	}
	result := make([]RoutePolicy, 0, len(manifest.Routes))
	for _, route := range manifest.Routes {
		if !openAPIAddressableRouteAction(route.Action) {
			continue
		}
		for _, method := range route.Methods {
			method = strings.ToUpper(method)
			result = append(result, RoutePolicy{
				RouteID: route.ID, Method: method, RateLimit: PolicyDisabled,
				Idempotency: PolicyDisabled, Security: securityForDeclaredGuard(route.Guard),
				RequestSizeBytes: hostRequestSizeForRoute(route), CORSPolicy: PolicyCORSSameOrigin,
			})
		}
	}
	return result
}

func hostPolicyForOperation(
	route routeContract,
	operation map[string]any,
	artifact *loadedArtifact,
	sourcePath string,
) (RoutePolicy, error) {
	policy := RoutePolicy{
		RouteID: route.route.ID, Method: route.method,
		RateLimit: PolicyDisabled, Idempotency: PolicyDisabled,
		Security:         securityForDeclaredGuard(route.route.Guard),
		RequestSizeBytes: hostRequestSizeForRoute(route.route),
		CORSPolicy:       PolicyCORSSameOrigin,
	}
	if hostRateLimitedMethod(route.method) {
		policy.RateLimit = PolicyRateLimitIPWrite
	}
	required, err := operationRequiresIdempotency(operation, artifact, sourcePath)
	if err != nil {
		return RoutePolicy{}, err
	}
	if !required {
		return policy, nil
	}
	if !hostRateLimitedMethod(route.method) || route.route.Mode != extensionmanifest.RouteModeHTTP {
		return RoutePolicy{}, fmt.Errorf(
			"required %s is supported only for buffered unsafe HTTP routes", idempotencyHeader,
		)
	}
	policy.Idempotency = PolicyIdempotencyRequired24h
	return policy, nil
}

func hostRateLimitedMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func hostRequestSizeForRoute(route extensionmanifest.ManifestRoute) int64 {
	mode := strings.ToLower(strings.TrimSpace(route.Mode))
	if strings.Contains(mode, "multipart") || strings.Contains(mode, "upload") {
		return UploadRequestSizeBytes
	}
	return DefaultRequestSizeBytes
}

func operationRequiresIdempotency(operation map[string]any, artifact *loadedArtifact, sourcePath string) (bool, error) {
	values, exists := operation["parameters"]
	if !exists {
		return false, nil
	}
	parameters, ok := values.([]any)
	if !ok {
		return false, fmt.Errorf("parameters must be an array")
	}
	found := false
	required := false
	for _, value := range parameters {
		parameter, _, err := resolveObject(value, artifact, sourcePath, 0)
		if err != nil {
			return false, fmt.Errorf("resolve %s parameter: %w", idempotencyHeader, err)
		}
		location, locationOK := canonicalStringField(parameter, "in")
		name, nameOK := canonicalStringField(parameter, "name")
		if !locationOK || !nameOK || location != "header" || !strings.EqualFold(name, idempotencyHeader) {
			continue
		}
		if found {
			return false, fmt.Errorf("duplicate %s parameter", idempotencyHeader)
		}
		found = true
		required, _ = parameter["required"].(bool)
		if !required {
			continue
		}
		if _, usesContent := parameter["content"]; usesContent {
			return false, fmt.Errorf("required %s must use a string schema", idempotencyHeader)
		}
		schema, ok := parameter["schema"].(map[string]any)
		if !ok || schema["type"] != "string" {
			return false, fmt.Errorf("required %s must use type string", idempotencyHeader)
		}
		if maxLength, exists := schema["maxLength"]; exists && !validIdempotencyMaxLength(maxLength) {
			return false, fmt.Errorf("required %s maxLength must be between 1 and 128", idempotencyHeader)
		}
	}
	return required, nil
}

func validIdempotencyMaxLength(value any) bool {
	switch number := value.(type) {
	case int:
		return number > 0 && number <= 128
	case int64:
		return number > 0 && number <= 128
	case uint64:
		return number > 0 && number <= 128
	default:
		return false
	}
}

// CoreOperations converts the runtime catalog into aggregate collision
// reservations without publishing Host operations in the plugin document.
func CoreOperations(catalog []routes.CoreRoute) []CoreOperation {
	result := make([]CoreOperation, 0, len(catalog))
	for _, route := range catalog {
		methods := []string{route.Method}
		if route.Method == "*" {
			// OpenAPI has no wildcard method. Reserve every representable method so
			// a plugin fragment cannot overlap the legacy catch-all proxy route.
			methods = openAPIMethodOrder
		}
		for _, method := range methods {
			operationID := route.ID
			if route.Method == "*" {
				operationID += "." + strings.ToLower(method)
			}
			result = append(result, CoreOperation{
				RouteID: route.ID, Path: route.Path, Method: method, OperationID: operationID,
			})
		}
	}
	return result
}

func openAPIAddressableRouteAction(action string) bool {
	return action == extensionmanifest.RouteActionAdd || action == extensionmanifest.RouteActionAlias ||
		action == extensionmanifest.RouteActionRedirect || action == extensionmanifest.RouteActionRewrite ||
		action == extensionmanifest.RouteActionReplace
}

func securityForDeclaredGuard(guard string) string {
	switch guard {
	case extensionmanifest.GuardCorePublic, extensionmanifest.GuardCoreGuest:
		return SecurityPublic
	case extensionmanifest.GuardCoreLogin, extensionmanifest.GuardCorePermission:
		return SecurityAuthenticated
	case extensionmanifest.GuardCoreInherit:
		return SecurityHostInherited
	default:
		return SecurityPluginOwned
	}
}
