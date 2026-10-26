package extensionmanifest

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	timezone "github.com/keeptrue/sforum/apps/api/app/Support/Timezone"
)

var (
	contractVersionPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*@[1-9][0-9]*$`)
	sha256Pattern                = regexp.MustCompile(`^[0-9a-f]{64}$`)
	databaseNamePattern          = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	databaseOperationNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,254}$`)
	positiveIntegerPattern       = regexp.MustCompile(`^[1-9][0-9]*$`)
)

type v3Validator struct {
	manifest Manifest
	ids      map[string]string
}

func validateV3Manifest(manifest Manifest) error {
	if _, err := semver.StrictNewVersion(manifest.Version); err != nil {
		return ErrInvalidManifest
	}
	if _, err := semver.NewConstraint(manifest.SForumVersion); err != nil {
		return ErrInvalidManifest
	}
	validator := v3Validator{manifest: manifest, ids: map[string]string{}}
	for _, validate := range []func() error{
		validator.validateBackendAndMigrations,
		validator.validateGuardsAndRoutes,
		validator.validateHooksEventsJobsAndProviders,
		validator.validateUIAndPackage,
		validator.validatePlatform,
	} {
		if err := validate(); err != nil {
			return err
		}
	}
	return nil
}

func (v *v3Validator) versionedID(id string, contractVersion string, family string) error {
	if !manifestIDPattern.MatchString(id) || !strings.HasPrefix(id, v.manifest.ID+".") ||
		!validContractVersion(contractVersion) {
		return fmt.Errorf("%w: versionedID pattern/ownership failed (family=%s id=%s)", ErrInvalidManifest, family, id)
	}
	if _, duplicate := v.ids[id]; duplicate {
		return fmt.Errorf("%w: duplicate versioned id %s (family=%s)", ErrInvalidManifest, id, family)
	}
	v.ids[id] = family
	return nil
}

func (v *v3Validator) validateBackendAndMigrations() error {
	backend := v.manifest.Backend
	if backend.Entry == "" {
		if backend.RPC != "" || backend.ProtocolVersion != 0 || backend.Digest != "" || backend.HostAPIVersion != "" {
			return fmt.Errorf("%w: v3 validateBackendAndMigrations rule #1", ErrInvalidManifest)
		}
	} else {
		if !validPackagePath(backend.Entry) || backend.RPC != "hashicorp-go-plugin" || !validDigest(backend.Digest) {
			return fmt.Errorf("%w: v3 validateBackendAndMigrations rule #2", ErrInvalidManifest)
		}
		if backend.ProtocolVersion != 2 {
			return fmt.Errorf("%w: v3 validateBackendAndMigrations rule #3", ErrInvalidManifest)
		}
		if !validContractVersion(backend.HostAPIVersion) {
			return fmt.Errorf("%w: v3 validateBackendAndMigrations rule #4", ErrInvalidManifest)
		}
	}
	for _, migration := range v.manifest.Migrations {
		if err := v.versionedID(migration.ID, migration.ContractVersion, "migration"); err != nil {
			return err
		}
		if !validPackagePath(migration.Path) || !strings.HasSuffix(migration.Path, ".sql") || !validDigest(migration.Digest) {
			return fmt.Errorf("%w: v3 validateBackendAndMigrations rule #5", ErrInvalidManifest)
		}
		switch migration.Transaction {
		case "required", "forbidden", "auto":
		default:
			return fmt.Errorf("%w: v3 validateBackendAndMigrations rule #6", ErrInvalidManifest)
		}
	}
	return nil
}

func (v *v3Validator) validateGuardsAndRoutes() error {
	// 决定 R1（2026-10-16）：Host 拥有的 core.guard.raw_request 不再作为路由守卫接受。
	// 它以前允许「零守卫代码」的路由拿到 raw 请求权限（cookie/authorization 等凭证会被
	// 转发给插件处理器），运维方判定这是漏洞。要 raw 权限必须由包内**显式声明**一个
	// kind=raw_request 的守卫（带 entry + digest + contract，并在信任期披露）。
	guards := map[string]bool{
		GuardCorePublic: true, GuardCoreLogin: true, GuardCorePermission: true,
		GuardCoreGuest: true, GuardCoreInherit: true,
	}
	rawRequestGuards := map[string]bool{}
	for _, guard := range v.manifest.Guards {
		if err := v.versionedID(guard.ID, guard.ContractVersion, "guard"); err != nil {
			return err
		}
		if guards[guard.ID] || !validPackagePath(guard.Entry) || !validDigest(guard.Digest) {
			return ErrInvalidManifest
		}
		switch guard.Kind {
		case GuardKindCustom, GuardKindRawRequest:
		default:
			return ErrInvalidManifest
		}
		for _, permission := range guard.Permissions {
			if !manifestHasPermission(v.manifest, permission) {
				return ErrInvalidManifest
			}
		}
		guards[guard.ID] = true
		rawRequestGuards[guard.ID] = guard.Kind == GuardKindRawRequest
	}

	claims := map[string]bool{}
	for _, route := range v.manifest.Routes {
		if err := v.versionedID(route.ID, route.ContractVersion, "route"); err != nil {
			return err
		}
		if route.Guard == GuardCoreRaw {
			// 作者向的早期失败：给出替代方案，而不是让人以为「写错了 id」。
			return fmt.Errorf(
				"%w: %s is no longer accepted as a route guard; declare a guard with kind=%s "+
					"(entry + digest + contractVersion) and reference that guard id instead",
				ErrInvalidManifest, GuardCoreRaw, GuardKindRawRequest,
			)
		}
		if !validRouteAction(route.Action) || !validRouteMode(route.Mode) || !guards[route.Guard] || route.TimeoutMS < 0 {
			return ErrInvalidManifest
		}
		if route.Action != RouteActionAdd && route.Action != RouteActionReplace && route.Mode != RouteModeHTTP {
			return ErrInvalidManifest
		}
		if !ValidRouteMutableFields(route, rawRequestGuards[route.Guard]) {
			return ErrInvalidManifest
		}
		if route.Access != "" {
			guard, ok := coreGuardForRouteAccess(route.Access)
			if !ok || route.Guard != guard {
				return ErrInvalidManifest
			}
		}
		if route.Guard == GuardCorePermission && (route.Permission == "" || !manifestHasPermission(v.manifest, route.Permission)) {
			return ErrInvalidManifest
		}
		if route.Guard == GuardCoreInherit && !routeTargetsExisting(route.Action) {
			return ErrInvalidManifest
		}
		if route.Action == RouteActionGlobalMiddleware {
			if route.Path != "" || route.TargetID != "" || len(route.Methods) != 0 || !validHandler(route.Handler) || !validSchemaRef(route.RequestSchema) || !validSchemaRef(route.ResponseSchema) || !validFallback(route.Fallback) {
				return ErrInvalidManifest
			}
			continue
		}
		if !validRoutePath(route.Path) || len(route.Methods) == 0 {
			return ErrInvalidManifest
		}
		for _, method := range route.Methods {
			if !validHTTPMethod(method) || !validRouteModeMethod(route.Mode, method) {
				return ErrInvalidManifest
			}
			claim := ""
			switch route.Action {
			case RouteActionAdd, RouteActionAlias, RouteActionRedirect, RouteActionRewrite:
				claim = route.Action + "\x00" + method + "\x00" + route.Path
			case RouteActionReplace:
				claim = route.Action + "\x00" + method + "\x00" + route.TargetID
			}
			if claim != "" {
				if claims[claim] {
					return ErrInvalidManifest
				}
				claims[claim] = true
			}
		}
		if route.TargetID != "" && !manifestIDPattern.MatchString(route.TargetID) {
			return ErrInvalidManifest
		}
		if routeTargetsExisting(route.Action) && route.TargetID == "" {
			return ErrInvalidManifest
		}
		if route.TargetID == "core.route.system.health" || route.TargetID == "core.route.system.ready" ||
			route.TargetID == "core.route.notifications.stream" {
			return ErrInvalidManifest
		}
		if route.Action == RouteActionRedirect {
			if !validRouteDestination(route.Destination) || !validRedirectStatusCode(route.StatusCode) {
				return ErrInvalidManifest
			}
		} else if route.Destination != "" || route.StatusCode != 0 {
			return ErrInvalidManifest
		}
		if routeNeedsHandler(route.Action) && !validHandler(route.Handler) {
			return ErrInvalidManifest
		}
		if !validFallback(route.Fallback) {
			return ErrInvalidManifest
		}
		if route.RequestSchema != "" && !validSchemaRef(route.RequestSchema) ||
			route.ResponseSchema != "" && !validSchemaRef(route.ResponseSchema) {
			return ErrInvalidManifest
		}
		// Non-HTTP route bodies are opaque Protocol V2 chunks. Legacy schema refs
		// remain valid documentation metadata, but only HTTP bodies are Host-
		// validated JSON documents and therefore require these declarations.
		if route.Mode == RouteModeHTTP && routeNeedsSchemas(route.Action) {
			if !validSchemaRef(route.ResponseSchema) || (hasUnsafeMethod(route.Methods) && !validSchemaRef(route.RequestSchema)) {
				return fmt.Errorf("%w: v3 validateHooksEventsJobsAndProviders rule #1", ErrInvalidManifest)
			}
		}
	}
	return nil
}

func (v *v3Validator) validateHooksEventsJobsAndProviders() error {
	for _, hook := range v.manifest.Hooks {
		if err := v.versionedID(hook.ID, hook.ContractVersion, "hook"); err != nil {
			return err
		}
		nameAllowed := knownOrNamespacedContract(v.manifest.ID, hook.Name, appevents.Known(hook.Name))
		if hook.TargetID != "" {
			nameAllowed = manifestIDPattern.MatchString(hook.Name) && manifestIDPattern.MatchString(hook.TargetID)
		}
		if !nameAllowed || !validSchemaRef(hook.InputSchema) || hook.TimeoutMS <= 0 || hook.TimeoutMS > HookMaximumTimeoutMS ||
			(hook.Handler != "" && !validHandler(hook.Handler)) || (hook.TargetID != "" && !validHandler(hook.Handler)) {
			return ErrInvalidManifest
		}
		switch hook.Kind {
		case "action", "filter", "observe":
		default:
			return ErrInvalidManifest
		}
		if hook.Kind != "observe" && !validSchemaRef(hook.ResultSchema) {
			return ErrInvalidManifest
		}
		if hook.Execution != "sync" && hook.Execution != "async" {
			return ErrInvalidManifest
		}
		if hook.FailurePolicy != "fail_closed" && hook.FailurePolicy != "fail_open" {
			return ErrInvalidManifest
		}
		if hook.Execution == "async" && hook.Kind == "filter" {
			return ErrInvalidManifest
		}
		// Async listeners are durable eventual work; once one item is queued the
		// Host cannot atomically retract it when a later enqueue fails.
		if hook.Execution == "async" && hook.FailurePolicy != "fail_open" {
			return ErrInvalidManifest
		}
		seenFields := map[string]bool{}
		for _, field := range hook.MutableFields {
			if field == "" || seenFields[field] {
				return ErrInvalidManifest
			}
			seenFields[field] = true
		}
		if hook.Kind != "filter" && len(hook.MutableFields) != 0 {
			return ErrInvalidManifest
		}
	}
	for _, event := range v.manifest.Events {
		if err := v.versionedID(event.ID, event.ContractVersion, "event"); err != nil {
			return err
		}
		definition, known := appevents.FindDefinition(event.Name)
		if !knownOrNamespacedContract(v.manifest.ID, event.Name, known) || !validHandler(event.Handler) || !validSchemaRef(event.InputSchema) || event.TimeoutMS < 0 {
			return ErrInvalidManifest
		}
		if known && event.Kind != definition.Kind {
			return ErrInvalidManifest
		}
		if !known && event.Kind != "observe" && event.Kind != "filter" {
			return ErrInvalidManifest
		}
		if event.Kind == "filter" && !validSchemaRef(event.ResultSchema) {
			return ErrInvalidManifest
		}
	}
	jobIDs := map[string]bool{}
	for _, job := range v.manifest.Jobs {
		if err := v.versionedID(job.ID, job.ContractVersion, "job"); err != nil {
			return err
		}
		if job.Name == "" || !validHandler(job.Handler) || !validSchemaRef(job.PayloadSchema) {
			return ErrInvalidManifest
		}
		switch job.RetryPolicy {
		case "none", "bounded", "exponential":
		default:
			return ErrInvalidManifest
		}
		if job.MaxAttempts < 1 || job.MaxAttempts > PluginJobMaximumAttempts ||
			job.ConcurrencyLimit < 1 || job.ConcurrencyLimit > PluginJobMaximumConcurrencyLimit {
			return ErrInvalidManifest
		}
		// 队列是运营者的资源分配：插件只能选 Host 公开的两条道之一。
		switch job.Queue {
		case "", PluginJobQueueDefault, PluginJobQueueAI:
		default:
			return ErrInvalidManifest
		}
		switch job.RetryPolicy {
		case "none", "exponential":
			if job.RetryDelaySeconds != 0 {
				return ErrInvalidManifest
			}
		case "bounded":
			if job.RetryDelaySeconds < 1 || job.RetryDelaySeconds > PluginJobMaximumRetryDelaySeconds {
				return ErrInvalidManifest
			}
		}
		jobIDs[job.ID] = true
	}
	for _, schedule := range v.manifest.Schedules {
		if err := v.versionedID(schedule.ID, schedule.ContractVersion, "schedule"); err != nil {
			return err
		}
		if !jobIDs[schedule.JobID] || !validCron(schedule.Cron) {
			return ErrInvalidManifest
		}
		if schedule.Timezone != "" {
			if _, err := timezone.Load(schedule.Timezone); err != nil {
				return ErrInvalidManifest
			}
		}
	}
	for _, provider := range v.manifest.Providers {
		if err := v.versionedID(provider.ID, provider.ContractVersion, "provider"); err != nil {
			return err
		}
		slotAllowed := knownOrNamespacedContract(v.manifest.ID, provider.Slot, knownProviderSlot(provider.Slot))
		if provider.TargetID != "" {
			slotAllowed = manifestIDPattern.MatchString(provider.Slot) && manifestIDPattern.MatchString(provider.TargetID)
		}
		if provider.Label == "" || provider.TimeoutMS < 0 || !slotAllowed {
			return ErrInvalidManifest
		}
		versionedSlot := provider.TargetID != "" || provider.RequestSchema != "" || provider.ResponseSchema != ""
		if !versionedSlot {
			if !validHandler(provider.Handler) || provider.Fallback != "" {
				return ErrInvalidManifest
			}
			continue
		}
		if provider.TimeoutMS <= 0 || provider.TimeoutMS > ProviderSlotMaximumTimeoutMS ||
			!validSchemaRef(provider.RequestSchema) || !validSchemaRef(provider.ResponseSchema) ||
			(provider.Fallback != "next" && provider.Fallback != "closed") ||
			(provider.Handler != "" && !validHandler(provider.Handler)) ||
			(provider.TargetID != "" && (!manifestIDPattern.MatchString(provider.TargetID) || !validHandler(provider.Handler))) {
			return ErrInvalidManifest
		}
	}
	return nil
}

func hasV3Declarations(manifest Manifest) bool {
	if len(manifest.Guards)+len(manifest.Schedules)+len(manifest.Components)+len(manifest.Templates)+len(manifest.Assets)+len(manifest.Content)+len(manifest.Editor)+len(manifest.Entities)+len(manifest.Cache)+len(manifest.SEO)+len(manifest.Services)+len(manifest.Commands)+len(manifest.AdminSurfaces)+len(manifest.Queries)+len(manifest.QueryResultFilters)+len(manifest.PermissionDefinitions)+len(manifest.Media)+len(manifest.Navigation)+len(manifest.Regions)+len(manifest.Dependencies)+len(manifest.OpenAPI)+len(manifest.PackageFiles) > 0 || manifest.Database != nil || manifest.Identity != nil || manifest.Lifecycle != nil {
		return true
	}
	if manifest.Backend.Digest != "" || manifest.Backend.HostAPIVersion != "" {
		return true
	}
	for _, item := range manifest.Migrations {
		if item.ID != "" || item.ContractVersion != "" || item.Digest != "" || item.Transaction != "" {
			return true
		}
	}
	for _, item := range manifest.Routes {
		if item.ID != "" || item.ContractVersion != "" || item.Action != "" || item.TargetID != "" ||
			item.Guard != "" || item.Mode != "" || item.Handler != "" || item.RequestSchema != "" ||
			item.ResponseSchema != "" || item.StatusCode != 0 || len(item.MutableRequestFields) > 0 || len(item.MutableResponseFields) > 0 {
			return true
		}
	}
	for _, item := range manifest.Hooks {
		if item.ID != "" || item.ContractVersion != "" || item.Kind != "" || item.Handler != "" || item.InputSchema != "" || item.ResultSchema != "" {
			return true
		}
	}
	for _, item := range manifest.Events {
		if item.ID != "" || item.ContractVersion != "" || item.Handler != "" || item.InputSchema != "" || item.ResultSchema != "" {
			return true
		}
	}
	for _, item := range manifest.Jobs {
		if item.ID != "" || item.ContractVersion != "" || item.Handler != "" || item.PayloadSchema != "" || item.RetryPolicy != "" {
			return true
		}
	}
	for _, item := range manifest.Providers {
		if item.ID != "" || item.ContractVersion != "" || item.Handler != "" || item.Priority != 0 {
			return true
		}
	}
	return false
}

func validDigest(value string) bool { return sha256Pattern.MatchString(value) }

func validPackagePath(value string) bool {
	clean, ok := SafeArchivePath(value)
	return ok && clean == value && !strings.ContainsRune(value, '\x00')
}

func validSchemaRef(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > SchemaReferenceMaximumLength {
		return false
	}
	if validContractVersion(value) {
		return true
	}
	return validPackagePath(value) && strings.HasSuffix(value, ".json")
}

func validContractVersion(value string) bool {
	return len(value) <= ContractVersionMaximumLength && contractVersionPattern.MatchString(value)
}

func validHandler(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > HandlerReferenceMaximumLength ||
		strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func knownOrNamespacedContract(extensionID string, value string, known bool) bool {
	return known || (manifestIDPattern.MatchString(value) && strings.HasPrefix(value, extensionID+"."))
}

func validRoutePath(value string) bool {
	// Host 独占的流式/实时通道：SSE 协议桥接与宿主连接预算相关，
	// 插件不得声明这些路径（其余论坛读路由仍可被 Route Registry 认领）。
	if value == "/health" || value == "/ready" ||
		value == "/api/v1/notifications/stream" ||
		value == "/api/v1/topics/:topicID/comments/stream" {
		return false
	}
	return value != "" && strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.Contains(value, "://") && !strings.Contains(value, "..") && !strings.ContainsRune(value, '\x00')
}

func validRouteDestination(value string) bool {
	return value == "/" || validRoutePath(value)
}

func validHTTPMethod(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", char) {
			continue
		}
		return false
	}
	return true
}

func validRouteAction(value string) bool {
	switch value {
	case RouteActionAdd, RouteActionAlias, RouteActionRedirect, RouteActionRewrite, RouteActionBefore, RouteActionAfter, RouteActionFilter, RouteActionWrap, RouteActionReplace, RouteActionGlobalMiddleware:
		return true
	default:
		return false
	}
}

func validRouteMode(value string) bool {
	switch value {
	case RouteModeHTTP, RouteModeSSE, RouteModeWebSocket, RouteModeStream, RouteModeMultipart:
		return true
	default:
		return false
	}
}

func validRedirectStatusCode(value int) bool {
	return value == 301 || value == RouteRedirectStatusDefault
}

func validRouteModeMethod(mode string, method string) bool {
	if mode == RouteModeSSE || mode == RouteModeWebSocket {
		return method == "GET"
	}
	if mode == RouteModeMultipart {
		return method != "GET" && method != "HEAD" && method != "OPTIONS"
	}
	return true
}

func routeTargetsExisting(action string) bool {
	switch action {
	case RouteActionAlias, RouteActionRewrite, RouteActionBefore, RouteActionAfter, RouteActionFilter, RouteActionWrap, RouteActionReplace:
		return true
	default:
		return false
	}
}

func routeNeedsHandler(action string) bool {
	switch action {
	case RouteActionAdd, RouteActionBefore, RouteActionAfter, RouteActionFilter, RouteActionWrap, RouteActionReplace, RouteActionGlobalMiddleware:
		return true
	default:
		return false
	}
}

func routeNeedsSchemas(action string) bool {
	switch action {
	case RouteActionAdd, RouteActionFilter, RouteActionWrap, RouteActionReplace, RouteActionGlobalMiddleware:
		return true
	default:
		return false
	}
}

func validFallback(value string) bool {
	return value == "closed" || value == "not_found" || value == "readonly_core"
}

func hasUnsafeMethod(methods []string) bool {
	for _, method := range methods {
		if method != "GET" && method != "HEAD" && method != "OPTIONS" {
			return true
		}
	}
	return false
}

func validCron(value string) bool {
	if strings.HasPrefix(value, "@every ") {
		_, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(value, "@every ")))
		return err == nil
	}
	fields := strings.Fields(value)
	return len(fields) == 5 || len(fields) == 6
}

func validSemverConstraint(value string) bool {
	_, err := semver.NewConstraint(value)
	return err == nil
}

func validSemverVersion(value string) bool {
	_, err := semver.StrictNewVersion(value)
	return err == nil
}
