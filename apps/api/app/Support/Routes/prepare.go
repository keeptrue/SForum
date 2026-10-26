package routes

import (
	"fmt"
	pathpkg "path"
	"regexp"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

var (
	routeIDPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,80}$`)
	contractPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	extensionVersionExpr = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+\-~]{0,63}$`)
	packageDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	runtimeInstanceExpr  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)
)

func prepareCoreRoute(input CoreRoute) ([]preparedRoute, error) {
	if !strings.HasPrefix(input.ID, "core.route.") || !routeIDPattern.MatchString(input.ID) ||
		!contractPattern.MatchString(input.ContractVersion) || !validMethod(input.Method) ||
		input.Method != strings.ToUpper(input.Method) {
		return nil, fmt.Errorf("%w: invalid core route identity", ErrInvalidRoute)
	}
	// Compatibility publications may omit policy metadata, but an inherited
	// plugin guard can never resolve such a route and therefore remains closed.
	if !coreGuardDescriptorIsZero(input.Guard) {
		if err := validateCoreGuardDescriptor(input); err != nil {
			return nil, err
		}
	}
	compiled, err := compileRoutePath(input.Path)
	if err != nil {
		return nil, err
	}
	return []preparedRoute{{route: Route{
		ID: input.ID, ContractVersion: input.ContractVersion, Action: extensionmanifest.RouteActionAdd,
		Path: input.Path, Method: input.Method, Priority: input.Priority, Mode: extensionmanifest.RouteModeHTTP,
		PathSignature: compiled.signature, Provider: Provider{Kind: ProviderCore},
		CoreGuard: cloneCoreGuardDescriptor(input.Guard),
	}, path: compiled}}, nil
}

func preparePluginRoute(
	artifact PluginArtifact,
	declaration extensionmanifest.ManifestRoute,
	guardBindings map[string]PluginGuardBinding,
) ([]preparedRoute, error) {
	if declaration.Action == extensionmanifest.RouteActionRedirect && declaration.StatusCode == 0 {
		declaration.StatusCode = extensionmanifest.RouteRedirectStatusDefault
	}
	if !routeIDPattern.MatchString(declaration.ID) || !strings.HasPrefix(declaration.ID, artifact.ExtensionID+".") ||
		!contractPattern.MatchString(declaration.ContractVersion) || !validAction(declaration.Action) ||
		!validRouteMode(declaration.Mode) || !validFallback(declaration.Fallback) || declaration.TimeoutMS < 0 {
		return nil, fmt.Errorf("%w: invalid plugin route identity", ErrInvalidRoute)
	}
	if err := validatePluginRouteContract(artifact, declaration); err != nil {
		return nil, err
	}
	guardBinding, err := pluginGuardBindingForRoute(artifact.ExtensionID, declaration.Guard, guardBindings)
	if err != nil {
		return nil, err
	}
	// 决定 R1：raw 权限只能来自包内显式声明的 kind=raw_request 守卫。
	rawRequestAuthority := guardBinding.Kind == "raw_request"
	if !extensionmanifest.ValidRouteMutableFields(declaration, rawRequestAuthority) {
		return nil, fmt.Errorf("%w: invalid mutable route fields", ErrInvalidRoute)
	}
	if declaration.Action == extensionmanifest.RouteActionGlobalMiddleware {
		if declaration.Path != "" || declaration.TargetID != "" || len(declaration.Methods) != 0 {
			return nil, fmt.Errorf("%w: invalid global middleware declaration", ErrInvalidRoute)
		}
		return []preparedRoute{{route: routeFromManifest(artifact, declaration, guardBinding, "", "")}}, nil
	}
	if len(declaration.Methods) == 0 {
		return nil, fmt.Errorf("%w: route methods are required", ErrInvalidRoute)
	}
	compiled, err := compileRoutePath(declaration.Path)
	if err != nil {
		return nil, err
	}
	if routeTargetsDeclaredTarget(declaration.Action, declaration.TargetID) && !routeIDPattern.MatchString(declaration.TargetID) {
		return nil, fmt.Errorf("%w: target route is required", ErrInvalidRoute)
	}
	if !routeTargetsDeclaredTarget(declaration.Action, declaration.TargetID) && declaration.TargetID != "" {
		return nil, fmt.Errorf("%w: action %q cannot target another route", ErrInvalidRoute, declaration.Action)
	}
	if declaration.TargetID != "" && declaration.TargetID == declaration.ID {
		return nil, fmt.Errorf("%w: route cannot target itself", ErrInvalidRoute)
	}
	items := make([]preparedRoute, 0, len(declaration.Methods))
	methodSeen := make(map[string]struct{}, len(declaration.Methods))
	for _, method := range declaration.Methods {
		if !validMethod(method) || method != strings.ToUpper(method) {
			return nil, fmt.Errorf("%w: invalid method %q", ErrInvalidRoute, method)
		}
		if !validModeMethod(declaration.Mode, method) {
			return nil, fmt.Errorf("%w: mode %q cannot use method %q", ErrInvalidRoute, declaration.Mode, method)
		}
		if _, duplicate := methodSeen[method]; duplicate {
			return nil, fmt.Errorf("%w: duplicate method %q", ErrInvalidRoute, method)
		}
		methodSeen[method] = struct{}{}
		items = append(items, preparedRoute{
			route: routeFromManifest(artifact, declaration, guardBinding, method, compiled.signature), path: compiled,
		})
	}
	return items, nil
}

func routeFromManifest(
	artifact PluginArtifact,
	value extensionmanifest.ManifestRoute,
	guard PluginGuardBinding,
	method, signature string,
) Route {
	return Route{
		ID: value.ID, ContractVersion: value.ContractVersion, Action: value.Action, TargetID: value.TargetID,
		Path: value.Path, Method: method, Guard: value.Guard, Permission: value.Permission,
		Priority: value.Priority, Fallback: value.Fallback, Mode: value.Mode, Destination: value.Destination,
		StatusCode: value.StatusCode,
		Handler:    value.Handler, RequestSchema: value.RequestSchema, ResponseSchema: value.ResponseSchema,
		MutableRequestFields:  append([]string(nil), value.MutableRequestFields...),
		MutableResponseFields: append([]string(nil), value.MutableResponseFields...),
		TimeoutMS:             value.TimeoutMS, PathSignature: signature,
		Provider: Provider{Kind: ProviderPlugin, Artifact: artifact}, PluginGuard: clonePluginGuardBinding(guard),
	}
}

func appendUniqueRoute(routes *[]preparedRoute, seen map[string]struct{}, item preparedRoute) error {
	provider := string(item.route.Provider.Kind)
	if item.route.Provider.Kind == ProviderPlugin {
		provider += "\x00" + item.route.Provider.Artifact.ExtensionID
	}
	key := provider + "\x00" + item.route.ID + "\x00" + item.route.ContractVersion + "\x00" + item.route.Method
	if _, duplicate := seen[key]; duplicate {
		return fmt.Errorf("%w: duplicate route %s %s", ErrInvalidRoute, item.route.Method, item.route.ID)
	}
	seen[key] = struct{}{}
	*routes = append(*routes, item)
	return nil
}

func validatePluginArtifact(artifact PluginArtifact) error {
	if !routeIDPattern.MatchString(artifact.ExtensionID) || !extensionVersionExpr.MatchString(artifact.ExtensionVersion) ||
		!packageDigestPattern.MatchString(artifact.PackageDigest) || !runtimeInstanceExpr.MatchString(artifact.RuntimeInstanceID) {
		return fmt.Errorf("%w: plugin artifact is not exact", ErrInvalidRoute)
	}
	if _, err := semver.StrictNewVersion(artifact.ExtensionVersion); err != nil {
		return fmt.Errorf("%w: plugin artifact is not exact", ErrInvalidRoute)
	}
	return nil
}

func validatePluginRouteContract(artifact PluginArtifact, route extensionmanifest.ManifestRoute) error {
	if protectedHostRoute(route) {
		return fmt.Errorf("%w: pre-plugin health routes are not overridable", ErrInvalidRoute)
	}
	if !validRouteGuard(artifact.ExtensionID, route.Guard) {
		return fmt.Errorf("%w: invalid guard %q", ErrInvalidRoute, route.Guard)
	}
	if route.Action != extensionmanifest.RouteActionAdd && route.Action != extensionmanifest.RouteActionReplace &&
		route.Mode != extensionmanifest.RouteModeHTTP {
		return fmt.Errorf("%w: action %q only supports buffered HTTP", ErrInvalidRoute, route.Action)
	}
	if route.Guard == extensionmanifest.GuardCoreInherit && !routeTargetsDeclaredTarget(route.Action, route.TargetID) {
		return fmt.Errorf("%w: inherited guard requires a target route", ErrInvalidRoute)
	}
	if route.Guard == extensionmanifest.GuardCorePermission && !routeIDPattern.MatchString(route.Permission) {
		return fmt.Errorf("%w: permission guard requires a permission", ErrInvalidRoute)
	}
	if routeNeedsHandler(route.Action) && !validRouteHandler(route.Handler) {
		return fmt.Errorf("%w: action %q requires a handler", ErrInvalidRoute, route.Action)
	}
	if routeNeedsSchemas(route.Action) {
		if !validSchemaReference(route.ResponseSchema) ||
			(route.Action == extensionmanifest.RouteActionGlobalMiddleware || hasUnsafeMethod(route.Methods)) &&
				!validSchemaReference(route.RequestSchema) {
			return fmt.Errorf("%w: action %q requires valid schemas", ErrInvalidRoute, route.Action)
		}
	}
	if route.Action == extensionmanifest.RouteActionRedirect {
		if route.TargetID != "" {
			if route.Destination != "" {
				return fmt.Errorf("%w: redirect target is ambiguous", ErrInvalidRoute)
			}
		} else if _, err := compileRoutePath(route.Destination); err != nil {
			return fmt.Errorf("%w: invalid legacy redirect destination", ErrInvalidRoute)
		}
		if route.StatusCode != 301 && route.StatusCode != extensionmanifest.RouteRedirectStatusDefault {
			return fmt.Errorf("%w: invalid redirect status", ErrInvalidRoute)
		}
	} else if route.Destination != "" || route.StatusCode != 0 {
		return fmt.Errorf("%w: action %q cannot declare redirect output", ErrInvalidRoute, route.Action)
	}
	return nil
}

func protectedHostRoute(route extensionmanifest.ManifestRoute) bool {
	switch route.TargetID {
	case "core.route.system.health", "core.route.system.ready":
		return true
	}
	switch route.Path {
	case "/health", "/ready", "/api/v1/health", "/api/v1/ready":
		return true
	default:
		return false
	}
}

func validRouteGuard(extensionID, guard string) bool {
	switch guard {
	case extensionmanifest.GuardCorePublic, extensionmanifest.GuardCoreLogin,
		extensionmanifest.GuardCorePermission, extensionmanifest.GuardCoreGuest,
		extensionmanifest.GuardCoreInherit:
		return true
	// 决定 R1：Host 拥有的 raw_request 守卫不再合法，作者必须显式声明自己的守卫。
	case extensionmanifest.GuardCoreRaw:
		return false
	default:
		return routeIDPattern.MatchString(guard) && strings.HasPrefix(guard, extensionID+".")
	}
}

func validRouteHandler(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.Contains(value, "://") && !strings.Contains(value, "..")
}

func validSchemaReference(value string) bool {
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	if contractPattern.MatchString(value) {
		return true
	}
	return strings.HasSuffix(value, ".json") && !strings.HasPrefix(value, "/") &&
		!strings.ContainsAny(value, "\\\x00") && !strings.Contains(value, "..") && pathpkg.Clean(value) == value
}

func validModeMethod(mode, method string) bool {
	if mode == extensionmanifest.RouteModeSSE || mode == extensionmanifest.RouteModeWebSocket {
		return method == "GET"
	}
	if mode == extensionmanifest.RouteModeMultipart {
		return method != "GET" && method != "HEAD" && method != "OPTIONS" && method != "*"
	}
	return true
}

func routeNeedsHandler(action string) bool {
	switch action {
	case extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionBefore,
		extensionmanifest.RouteActionAfter, extensionmanifest.RouteActionFilter,
		extensionmanifest.RouteActionWrap, extensionmanifest.RouteActionReplace,
		extensionmanifest.RouteActionGlobalMiddleware:
		return true
	default:
		return false
	}
}

func routeNeedsSchemas(action string) bool {
	switch action {
	case extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionFilter,
		extensionmanifest.RouteActionWrap, extensionmanifest.RouteActionReplace,
		extensionmanifest.RouteActionGlobalMiddleware:
		return true
	default:
		return false
	}
}

func hasUnsafeMethod(methods []string) bool {
	for _, method := range methods {
		if method != "GET" && method != "HEAD" && method != "OPTIONS" {
			return true
		}
	}
	return false
}

func validateTargets(routes []preparedRoute) error {
	bases := make(map[string][]preparedRoute)
	for _, item := range routes {
		if addressableAction(item.route.Action) {
			bases[item.route.ID] = append(bases[item.route.ID], item)
		}
	}
	graph := make(map[string][]string)
	for _, item := range routes {
		if !routeTargetsDeclaredTarget(item.route.Action, item.route.TargetID) {
			continue
		}
		matched := false
		for _, target := range bases[item.route.TargetID] {
			if !methodsOverlap(item.route.Method, target.route.Method) {
				continue
			}
			if item.route.Action != extensionmanifest.RouteActionAlias &&
				item.route.Action != extensionmanifest.RouteActionRedirect &&
				item.route.Action != extensionmanifest.RouteActionRewrite &&
				item.path.signature != target.path.signature {
				continue
			}
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("%w: route %q has unknown or incompatible target %q", ErrInvalidRoute, item.route.ID, item.route.TargetID)
		}
		graph[item.route.ID] = append(graph[item.route.ID], item.route.TargetID)
	}
	if routeTargetGraphHasCycle(graph) {
		return fmt.Errorf("%w: route target graph contains a cycle", ErrInvalidRoute)
	}
	return nil
}

func routeTargetGraphHasCycle(graph map[string][]string) bool {
	const (
		visiting uint8 = iota + 1
		visited
	)
	state := make(map[string]uint8, len(graph))
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case visiting:
			return true
		case visited:
			return false
		}
		state[id] = visiting
		for _, targetID := range graph[id] {
			if visit(targetID) {
				return true
			}
		}
		state[id] = visited
		return false
	}
	for id := range graph {
		if visit(id) {
			return true
		}
	}
	return false
}

func validMethod(method string) bool {
	if method == "" || method != strings.TrimSpace(method) {
		return false
	}
	for _, char := range method {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
}

func validAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionAlias,
		extensionmanifest.RouteActionRedirect, extensionmanifest.RouteActionRewrite,
		extensionmanifest.RouteActionBefore, extensionmanifest.RouteActionAfter,
		extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap,
		extensionmanifest.RouteActionReplace, extensionmanifest.RouteActionGlobalMiddleware:
		return true
	default:
		return false
	}
}

func validRouteMode(mode string) bool {
	switch mode {
	case extensionmanifest.RouteModeHTTP, extensionmanifest.RouteModeSSE,
		extensionmanifest.RouteModeWebSocket, extensionmanifest.RouteModeStream,
		extensionmanifest.RouteModeMultipart:
		return true
	default:
		return false
	}
}

func validFallback(fallback string) bool {
	return fallback == "closed" || fallback == "not_found" || fallback == "readonly_core"
}

func addressableAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionAdd, extensionmanifest.RouteActionAlias,
		extensionmanifest.RouteActionRedirect, extensionmanifest.RouteActionRewrite:
		return true
	default:
		return false
	}
}

func compositionAction(action string) bool {
	switch action {
	case extensionmanifest.RouteActionBefore, extensionmanifest.RouteActionAfter,
		extensionmanifest.RouteActionFilter, extensionmanifest.RouteActionWrap:
		return true
	default:
		return false
	}
}

func targetsExisting(action string) bool {
	return action == extensionmanifest.RouteActionAlias || action == extensionmanifest.RouteActionRewrite ||
		compositionAction(action) || action == extensionmanifest.RouteActionReplace
}

func routeTargetsDeclaredTarget(action, targetID string) bool {
	return targetsExisting(action) || action == extensionmanifest.RouteActionRedirect && targetID != ""
}

func routeMethodMatches(route Route, requestMethod string) bool {
	if route.Method == "*" || route.Method == requestMethod {
		return true
	}
	return requestMethod == "HEAD" && route.Method == "GET" && route.Mode == extensionmanifest.RouteModeHTTP
}

func methodsOverlap(left, right string) bool {
	return left == "*" || right == "*" || left == right ||
		left == "GET" && right == "HEAD" || left == "HEAD" && right == "GET"
}

func methodSpecificity(method string) int {
	if method == "*" {
		return 0
	}
	return 1
}

func requestMethodSpecificity(route Route, requestMethod string) int {
	if route.Method == requestMethod {
		return 2
	}
	if requestMethod == "HEAD" && route.Method == "GET" && route.Mode == extensionmanifest.RouteModeHTTP {
		return 1
	}
	return 0
}
