package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	stdhttp "net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gofiber/fiber/v3"

	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

var (
	ErrRouteLoginRequired     = errors.New("route dispatcher login required")
	ErrRouteGuestRequired     = errors.New("route dispatcher guest required")
	ErrRoutePermissionDenied  = errors.New("route dispatcher permission denied")
	ErrRouteGuardUnavailable  = errors.New("route dispatcher guard unavailable")
	ErrRouteSchemaUnavailable = errors.New("route dispatcher schema unavailable")
	ErrRouteRuntimeArtifact   = errors.New("route dispatcher runtime artifact mismatch")
	ErrRouteRuntimeTarget     = errors.New("route dispatcher runtime target is invalid")
	ErrRouteResponseTooLarge  = errors.New("route dispatcher response exceeds buffer limit")
)

const defaultRouteResponseLimit int64 = 8 << 20

type RouteActorLoader func(fiber.Ctx) (identity.Actor, error)

// RouteSchemaCatalog owns compiled JSON Schema contracts. A missing catalog
// deliberately rejects declared schemas instead of treating their ids as prose.
type RouteSchemaCatalog interface {
	ValidateRouteSchema(context.Context, routes.PluginArtifact, string, string, string, string, string, string, string, string, int, []byte) error
}

type ExactRouteRuntime interface {
	InspectRuntimeInstance(extensionsruntime.RuntimeInstanceIdentity) (extensionsruntime.RuntimeInstanceSnapshot, error)
	AcquireRuntimeCall(context.Context, extensionsruntime.RuntimeInstanceIdentity, extensionsruntime.RuntimeCallClass) (*extensionsruntime.RuntimeAdmissionLease, error)
}

type HostRouteGuardAuthorizer struct{}

func (HostRouteGuardAuthorizer) Authorize(
	_ context.Context,
	_ routes.RouteExecutionPlan,
	step routes.RouteExecutionStep,
	request routes.DispatchRequest,
) error {
	switch step.Guard {
	case extensionmanifest.GuardCorePublic:
		return nil
	case extensionmanifest.GuardCoreLogin:
		if request.Authenticated {
			return nil
		}
		return ErrRouteLoginRequired
	case extensionmanifest.GuardCoreGuest:
		if !request.Authenticated {
			return nil
		}
		return ErrRouteGuestRequired
	case extensionmanifest.GuardCorePermission:
		if !request.Authenticated {
			return ErrRouteLoginRequired
		}
		if request.Permissions["*"] || request.Permissions[step.Permission] {
			return nil
		}
		return ErrRoutePermissionDenied
	case extensionmanifest.GuardCoreInherit:
		// The current core catalog has stable ids but no executable guard handle.
		// Running a plugin before that guard would expose data, so remain closed.
		return ErrRouteGuardUnavailable
	case extensionmanifest.GuardCoreRaw:
		return ErrRouteGuardUnavailable
	default:
		// Custom/raw guard execution needs the separately trusted guard runtime.
		return ErrRouteGuardUnavailable
	}
}

type CatalogRouteSchemaValidator struct {
	Catalog RouteSchemaCatalog
}

func (v CatalogRouteSchemaValidator) ValidateRequest(ctx context.Context, step routes.RouteExecutionStep, request routes.DispatchRequest) error {
	return v.validate(ctx, step, "request", step.RequestSchema, request.Method, request.Headers, 0, request.Body)
}

func (v CatalogRouteSchemaValidator) ValidateResponse(ctx context.Context, step routes.RouteExecutionStep, request routes.DispatchRequest, response routes.DispatchResponse) error {
	return v.validate(ctx, step, "response", step.ResponseSchema, request.Method, response.Headers, response.Status, response.Body)
}

func (v CatalogRouteSchemaValidator) validate(
	ctx context.Context,
	step routes.RouteExecutionStep,
	direction string,
	reference string,
	actualMethod string,
	headers stdhttp.Header,
	responseStatus int,
	body []byte,
) error {
	if strings.TrimSpace(reference) == "" {
		return nil
	}
	if v.Catalog == nil || step.Provider.Kind != routes.ProviderPlugin {
		return ErrRouteSchemaUnavailable
	}
	mediaType, err := normalizedRouteSchemaMediaType(headers)
	if err != nil {
		return err
	}
	method := step.Method
	if step.Action == extensionmanifest.RouteActionGlobalMiddleware {
		method = "*"
	}
	return v.Catalog.ValidateRouteSchema(
		ctx, step.Provider.Artifact, direction, step.RouteID, method, actualMethod,
		step.ContractVersion, step.Action, reference, mediaType, responseStatus, append([]byte(nil), body...),
	)
}

func normalizedRouteSchemaMediaType(headers stdhttp.Header) (string, error) {
	values := headers.Values("Content-Type")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", fmt.Errorf("%w: exactly one Content-Type is required", ErrRouteSchemaUnavailable)
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	mediaType = strings.ToLower(mediaType)
	jsonMediaType := mediaType == "application/json" ||
		strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json") && len(mediaType) > len("application/+json")
	if err != nil || strings.Contains(mediaType, "*") || !jsonMediaType {
		return "", fmt.Errorf("%w: invalid Content-Type", ErrRouteSchemaUnavailable)
	}
	return mediaType, nil
}

type BufferedRouteStepInvoker struct {
	Runtime       ExactRouteRuntime
	Client        *stdhttp.Client
	ResponseLimit int64
}

// routeTransportEvidence only records facts observed by the Host transport.
// Once request headers leave the Host, the plugin may have acted; fallback is
// no longer safe even when the connection dies before returning a response.
type routeTransportEvidence struct {
	requestStarted  atomic.Bool
	responseStarted atomic.Bool
	commit          *routes.RouteCommitObserver
}

func (e *routeTransportEvidence) markRequestStarted() {
	if e.requestStarted.CompareAndSwap(false, true) && e.commit != nil {
		e.commit.SideEffectStarted()
	}
}

func (e *routeTransportEvidence) markResponseStarted() {
	if e.responseStarted.CompareAndSwap(false, true) && e.commit != nil {
		e.commit.ResponseStarted()
	}
}

func (e *routeTransportEvidence) result() routes.RouteInvocationResult {
	return routes.RouteInvocationResult{
		SideEffectStarted: e.requestStarted.Load(), ResponseStarted: e.responseStarted.Load(),
	}
}

func NewBufferedRouteStepInvoker(runtime ExactRouteRuntime) *BufferedRouteStepInvoker {
	transport := stdhttp.DefaultTransport.(*stdhttp.Transport).Clone()
	transport.Proxy = nil
	return &BufferedRouteStepInvoker{
		Runtime: runtime,
		Client: &stdhttp.Client{
			Transport: transport,
			CheckRedirect: func(*stdhttp.Request, []*stdhttp.Request) error {
				return stdhttp.ErrUseLastResponse
			},
		},
		ResponseLimit: defaultRouteResponseLimit,
	}
}

func (*BufferedRouteStepInvoker) SupportsMode(mode string) bool {
	return mode == extensionmanifest.RouteModeHTTP
}

func (i *BufferedRouteStepInvoker) Invoke(ctx context.Context, input routes.RouteInvocation) (routes.RouteInvocationResult, error) {
	if i == nil || i.Runtime == nil || i.Client == nil || ctx == nil || input.Step.Provider.Kind != routes.ProviderPlugin {
		return routes.RouteInvocationResult{}, routes.ErrDispatchTransport
	}
	authority, ok := input.RequestAuthority()
	if !ok {
		return routes.RouteInvocationResult{}, ErrRouteRuntimeTarget
	}
	artifact := input.Step.Provider.Artifact
	identity := extensionsruntime.RuntimeInstanceIdentity{
		ExtensionID: artifact.ExtensionID, InstanceID: artifact.RuntimeInstanceID,
	}
	snapshot, err := i.Runtime.InspectRuntimeInstance(identity)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	if !snapshot.Active || snapshot.Identity != identity || snapshot.ExtensionVersion != artifact.ExtensionVersion ||
		snapshot.ArtifactDigest != artifact.PackageDigest {
		return routes.RouteInvocationResult{}, ErrRouteRuntimeArtifact
	}
	if strings.TrimSpace(snapshot.Target.BaseURL) == "" {
		lease, err := i.Runtime.AcquireRuntimeCall(ctx, identity, extensionsruntime.RuntimeCallRoute)
		if err != nil {
			return routes.RouteInvocationResult{}, err
		}
		defer lease.Release()
		return i.invokeProtocolV2(lease.Context, identity, input, authority)
	}
	if input.Stage != routes.InvocationStageHandler ||
		input.Step.Action != extensionmanifest.RouteActionAdd && input.Step.Action != extensionmanifest.RouteActionReplace {
		// Loopback HTTP handlers cannot express bounded request/response patches,
		// so never treat a full HTTP response as a modifier result.
		return routes.RouteInvocationResult{}, ErrRouteRuntimeTarget
	}
	target, err := exactLoopbackRouteURL(snapshot.Target.BaseURL, input.Request.Path, input.Request.Query)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	lease, err := i.Runtime.AcquireRuntimeCall(ctx, identity, extensionsruntime.RuntimeCallRoute)
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	defer lease.Release()

	request, err := stdhttp.NewRequestWithContext(lease.Context, input.Request.Method, target, bytes.NewReader(input.Request.Body))
	if err != nil {
		return routes.RouteInvocationResult{}, err
	}
	if err := copyRouteRequestHeaders(request.Header, input.Request.Headers, authority); err != nil {
		return routes.RouteInvocationResult{}, err
	}
	request.Header.Set("X-SForum-Extension-ID", artifact.ExtensionID)
	request.Header.Set("X-SForum-Route-ID", input.Step.RouteID)
	request.Header.Set("X-SForum-Route-Contract", input.Step.ContractVersion)
	request.Header.Set("X-SForum-Route-Action", input.Step.Action)
	request.Header.Set("X-SForum-Route-Phase", string(input.Step.Phase))
	request.Header.Set("X-SForum-Route-Handler", input.Step.Handler)
	request.Header.Set("X-SForum-Route-Plan-Revision", strconv.FormatUint(input.PlanRevision, 10))
	if input.Request.ActorID > 0 {
		request.Header.Set("X-SForum-Actor-ID", strconv.FormatInt(input.Request.ActorID, 10))
	}
	if input.Response != nil {
		request.Header.Set("X-SForum-Route-Response-Status", strconv.Itoa(input.Response.Status))
	}
	evidence := &routeTransportEvidence{commit: input.Commit}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		WroteHeaders:         evidence.markRequestStarted,
		WroteRequest:         func(httptrace.WroteRequestInfo) { evidence.markRequestStarted() },
		GotFirstResponseByte: evidence.markResponseStarted,
	}))

	response, err := i.Client.Do(request)
	if err != nil {
		return evidence.result(), err
	}
	defer response.Body.Close()
	limit := i.ResponseLimit
	if limit <= 0 {
		limit = defaultRouteResponseLimit
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return evidence.result(), err
	}
	if int64(len(body)) > limit {
		return evidence.result(), ErrRouteResponseTooLarge
	}
	value := routes.DispatchResponse{Status: response.StatusCode, Headers: filteredRouteResponseHeaders(response.Header), Body: body}
	result := evidence.result()
	result.Response = &value
	return result, nil
}

func routeDispatcherMiddleware(dispatcher *routes.Dispatcher, actors RouteActorLoader) fiber.Handler {
	return func(c fiber.Ctx) error {
		if dispatcher == nil {
			return c.Next()
		}
		actor := identity.Actor{}
		if actors != nil {
			var err error
			actor, err = actors(c)
			if err != nil {
				return err
			}
		}
		// CSRF/request-id/session 等 Host authority 已在 Dispatcher 前写入响应
		// 头。插件响应仍经过 allowlist；这里只恢复进入执行前的 Host 值。
		hostHeaders := hostRouteMiddlewareResponseHeaders(c)
		request := routeDispatchRequestMetadata(c, actor)
		prepared, err := dispatcher.PrepareStream(c.Context(), request)
		if err != nil {
			return mapRouteDispatchError(err)
		}
		if prepared.Handled {
			return serveRouteStream(c, prepared.Dispatch, hostHeaders)
		}
		request.Body = append([]byte(nil), c.Body()...)
		core := &fiberCoreRouteInvoker{ctx: c}
		result, err := dispatcher.Dispatch(c.Context(), request, core)
		if err != nil {
			return mapRouteDispatchError(err)
		}
		if !result.Handled {
			return c.Next()
		}
		writeRouteDispatchResponse(c, result.Response, hostHeaders)
		return nil
	}
}

type fiberCoreRouteInvoker struct {
	ctx    fiber.Ctx
	called bool
}

func (i *fiberCoreRouteInvoker) InvokeCore(_ context.Context, step routes.RouteExecutionStep, request routes.DispatchRequest) (routes.DispatchResponse, error) {
	if i == nil || i.ctx == nil || i.called {
		return routes.DispatchResponse{}, routes.ErrDispatchAlreadyCommitted
	}
	i.called = true
	applyRouteDispatchRequest(i.ctx, request)
	i.ctx.Response().Reset()
	var err error
	if step.Action == extensionmanifest.RouteActionAlias || step.Action == extensionmanifest.RouteActionRewrite {
		if step.TargetPath == "" {
			return routes.DispatchResponse{}, ErrRouteRuntimeTarget
		}
		originalPath := i.ctx.Path()
		i.ctx.Path(step.TargetPath)
		// 从当前全局中间件后继续匹配，避免 RestartRouting 重放认证、限流和审计。
		err = i.ctx.Next()
		i.ctx.Path(originalPath)
	} else {
		err = i.ctx.Next()
	}
	if err != nil {
		i.ctx.Response().Reset()
		return routes.DispatchResponse{}, err
	}
	response := routes.DispatchResponse{
		Status: i.ctx.Response().StatusCode(), Headers: fasthttpResponseHeaders(i.ctx),
		Body: append([]byte(nil), i.ctx.Response().Body()...),
	}
	i.ctx.Response().Reset()
	return response, nil
}

func routeDispatchRequest(c fiber.Ctx, actor identity.Actor) routes.DispatchRequest {
	request := routeDispatchRequestMetadata(c, actor)
	request.Body = append([]byte(nil), c.Body()...)
	return request
}

func routeDispatchRequestMetadata(c fiber.Ctx, actor identity.Actor) routes.DispatchRequest {
	permissions := make(map[string]bool, len(actor.Permissions)+1)
	for key, allowed := range actor.Permissions {
		permissions[key] = allowed
	}
	if actor.IsSuperAdmin() {
		permissions["*"] = true
	}
	credentialSource := routes.DispatchCredentialSource("")
	if actor.ID > 0 && actor.IsActive() {
		credentialSource = routes.DispatchCredentialCookie
		if apitokens.TokenIDFromContext(c.Context()) > 0 {
			credentialSource = routes.DispatchCredentialBearer
		}
	}
	return routes.DispatchRequest{
		Method: c.Method(), Path: c.Path(), Query: string(c.Request().URI().QueryString()),
		Headers: fasthttpRequestHeaders(c),
		ActorID: actor.ID, Authenticated: actor.ID > 0 && actor.IsActive(),
		CredentialSource: credentialSource, Permissions: permissions,
		ClientIP: clientip.FromCtx(c),
	}
}

func serveRouteStream(c fiber.Ctx, dispatch *routes.RouteStreamDispatch, hostHeaders stdhttp.Header) error {
	if dispatch == nil {
		return mapRouteDispatchError(routes.ErrDispatchTransport)
	}
	if dispatch.Step().Mode == extensionmanifest.RouteModeWebSocket {
		return serveRouteWebSocket(c, dispatch, hostHeaders)
	}
	start, err := dispatch.Open(c.Context())
	if err != nil {
		// 浏览器文档导航命中登录守卫时改回跳转；其余情形仍是 401 JSON。
		if location, ok := streamLoginRedirect(c, dispatch, err); ok {
			c.Set(fiber.HeaderCacheControl, "no-store")
			return c.Redirect().Status(fiber.StatusFound).To(location)
		}
		return mapRouteDispatchError(err)
	}
	if err := validateRouteStreamPreflight(dispatch.Step().Mode, start.Response); err != nil {
		// Fail before Cancel so the transport trace lands before lifetime Done.
		failure := dispatch.StreamFailedAs(routes.RouteStreamFailureInvalidPreflight, err)
		start.Session.Cancel()
		return mapRouteDispatchError(failure)
	}
	requestBody := c.Request().BodyStream()
	if requestBody == nil {
		requestBody = bytes.NewReader(c.Body())
	}
	if err := pumpRouteStreamRequest(requestBody, start.Session); err != nil {
		failure := dispatch.StreamFailed(err)
		start.Session.Cancel()
		return mapRouteDispatchError(failure)
	}
	c.Response().Reset()
	c.Status(start.Response.Status)
	for name, values := range start.Response.Headers {
		for _, value := range values {
			c.Response().Header.Add(name, value)
		}
	}
	restoreHostRouteResponseHeaders(c, hostHeaders)
	dispatch.ResponseStarted()
	return c.SendStreamWriter(func(writer *bufio.Writer) {
		streamRouteResponse(writer, start.Session, dispatch)
	})
}

// streamLoginRedirect 把「浏览器访问受登录守卫保护的 HTML 流」翻译成跳转。
// 只有 stream 模式 + 浏览器文档导航 + 登录守卫失败才成立；其余情形交回 401 JSON。
func streamLoginRedirect(c fiber.Ctx, dispatch *routes.RouteStreamDispatch, err error) (string, bool) {
	if dispatch == nil || !errors.Is(err, ErrRouteLoginRequired) {
		return "", false
	}
	if dispatch.Step().Mode != extensionmanifest.RouteModeStream {
		return "", false
	}
	if c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead {
		return "", false
	}
	if !strings.Contains(c.Get(fiber.HeaderAccept), "text/html") {
		return "", false
	}
	target := c.Path()
	if query := string(c.Request().URI().QueryString()); query != "" {
		target += "?" + query
	}
	if !safeLoginReturnPath(target) {
		return "/login", true
	}
	return "/login?redirect=" + url.QueryEscape(target), true
}

// safeLoginReturnPath 保证回跳目标是站内绝对路径，挡住 open redirect 与 header 注入。
func safeLoginReturnPath(target string) bool {
	return strings.HasPrefix(target, "/") &&
		!strings.HasPrefix(target, "//") &&
		!strings.ContainsAny(target, "\\\r\n")
}

func validateRouteStreamPreflight(mode string, response routes.DispatchResponse) error {
	if mode != extensionmanifest.RouteModeSSE {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(response.Headers.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return fmt.Errorf("%w: SSE requires text/event-stream", ErrRouteRuntimeTarget)
	}
	return nil
}

func pumpRouteStreamRequest(reader io.Reader, session routes.RouteStreamSession) error {
	if reader == nil {
		return routes.WithRouteStreamAbort(routes.ErrDispatchTransport)
	}
	if session == nil {
		return routes.ErrDispatchTransport
	}
	buffer := make([]byte, extensionsruntime.MaxProtocolV2RouteChunkSize)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			if sendErr := session.Send(buffer[:read], false); sendErr != nil {
				return sendErr
			}
		}
		if errors.Is(err, io.EOF) {
			return session.CloseRequest()
		}
		if err != nil {
			return routes.WithRouteStreamAbort(err)
		}
		if read == 0 {
			return routes.WithRouteStreamAbort(io.ErrNoProgress)
		}
	}
}

func streamRouteResponse(writer *bufio.Writer, session routes.RouteStreamSession, dispatch *routes.RouteStreamDispatch) {
	if writer == nil || session == nil || dispatch == nil {
		if session != nil {
			session.Cancel()
		}
		return
	}
	// Cancel only after Complete/StreamFailed so lifetime Done cannot race ahead
	// of the Host commit/fail trace.
	defer session.Cancel()
	for {
		chunk, err := session.Recv()
		if errors.Is(err, io.EOF) {
			if _, ok := session.Response(); !ok {
				_ = dispatch.StreamFailedAs(routes.RouteStreamFailureMissingTerminal, routes.ErrDispatchTransport)
				return
			}
			_ = dispatch.Complete()
			return
		}
		if err != nil {
			_ = dispatch.StreamFailed(err)
			return
		}
		if len(chunk.Data) == 0 {
			continue
		}
		if _, err := writer.Write(chunk.Data); err != nil {
			_ = dispatch.StreamAborted(err)
			return
		}
		if err := writer.Flush(); err != nil {
			_ = dispatch.StreamAborted(err)
			return
		}
	}
}

func applyRouteDispatchRequest(c fiber.Ctx, request routes.DispatchRequest) {
	c.Request().Header.Reset()
	for name, values := range request.Headers {
		for _, value := range values {
			c.Request().Header.Add(name, value)
		}
	}
	c.Request().URI().SetQueryString(request.Query)
	c.Request().SetBodyRaw(append([]byte(nil), request.Body...))
}

func writeRouteDispatchResponse(c fiber.Ctx, response routes.DispatchResponse, hostHeaders stdhttp.Header) {
	c.Response().Reset()
	c.Status(response.Status)
	for name, values := range response.Headers {
		for _, value := range values {
			c.Response().Header.Add(name, value)
		}
	}
	restoreHostRouteResponseHeaders(c, hostHeaders)
	if canonical, ok := routeCanonicalLinkPath(response.CanonicalPath); ok {
		c.Response().Header.Set(fiber.HeaderLink, "<"+canonical+">; rel=\"canonical\"")
	}
	c.Response().SetBodyRaw(append([]byte(nil), response.Body...))
}

func routeCanonicalLinkPath(path string) (string, bool) {
	if path == "" || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\r\n") {
		return "", false
	}
	parsed, err := url.ParseRequestURI(path)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return parsed.EscapedPath(), true
}

func restoreHostRouteResponseHeaders(c fiber.Ctx, headers stdhttp.Header) {
	for name, values := range headers {
		if !strings.EqualFold(name, fiber.HeaderVary) {
			c.Response().Header.Del(name)
		}
		for _, value := range values {
			c.Response().Header.Add(name, value)
		}
	}
}

func hostRouteMiddlewareResponseHeaders(c fiber.Ctx) stdhttp.Header {
	result := make(stdhttp.Header)
	for name, values := range fasthttpResponseHeaders(c) {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "set-cookie", "vary", "x-request-id":
			for _, value := range values {
				result.Add(name, value)
			}
		}
	}
	return result
}

func fasthttpRequestHeaders(c fiber.Ctx) stdhttp.Header {
	headers := make(stdhttp.Header)
	for key, value := range c.Request().Header.All() {
		headers.Add(string(key), string(value))
	}
	return headers
}

func fasthttpResponseHeaders(c fiber.Ctx) stdhttp.Header {
	headers := make(stdhttp.Header)
	for key, value := range c.Response().Header.All() {
		headers.Add(string(key), string(value))
	}
	return headers
}

func exactLoopbackRouteURL(base, path, query string) (string, error) {
	target, err := url.Parse(strings.TrimSpace(base))
	if err != nil || target.Scheme != "http" && target.Scheme != "https" || target.User != nil || target.Host == "" {
		return "", ErrRouteRuntimeTarget
	}
	host := target.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) || target.Path != "" && target.Path != "/" || target.RawQuery != "" || target.Fragment != "" {
		return "", ErrRouteRuntimeTarget
	}
	relative, err := url.ParseRequestURI(path)
	if err != nil || !strings.HasPrefix(relative.Path, "/") {
		return "", ErrRouteRuntimeTarget
	}
	target.Path = relative.Path
	target.RawPath = relative.RawPath
	target.RawQuery = query
	return target.String(), nil
}

func copyRouteRequestHeaders(
	target, source stdhttp.Header,
	authority routes.ResolvedRequestAuthority,
) error {
	if _, err := protocolV2RequestAuthority(authority); err != nil {
		return err
	}
	connectionHeaders := routeConnectionHeaderTokens(source)
	for name, values := range source {
		if !routeRequestHeaderAllowed(name, connectionHeaders, rawRouteRequestAuthority(authority)) {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
	return nil
}

func routeRequestHeaderAllowed(name string, connectionHeaders map[string]struct{}, raw bool) bool {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(canonical, "x-sforum-") {
		return false
	}
	if _, blocked := connectionHeaders[canonical]; blocked {
		return false
	}
	switch canonical {
	case "", "host", "content-length", "proxy-authorization",
		"x-csrf-token", "connection", "keep-alive", "proxy-authenticate", "proxy-connection",
		"te", "trailer", "transfer-encoding", "upgrade":
		return false
	default:
		return !routeRequestCredentialHeader(canonical) || raw
	}
}

func routeRequestCredentialHeader(canonical string) bool {
	switch canonical {
	case "cookie", "authorization", "x-api-key", "x-auth-token":
		return true
	default:
		return false
	}
}

func routeConnectionHeaderTokens(headers stdhttp.Header) map[string]struct{} {
	blocked := make(map[string]struct{})
	for name, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), "Connection") {
			continue
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				if canonical := strings.ToLower(strings.TrimSpace(token)); canonical != "" {
					blocked[canonical] = struct{}{}
				}
			}
		}
	}
	return blocked
}

func filteredRouteResponseHeaders(source stdhttp.Header) stdhttp.Header {
	return routes.FilterPluginResponseHeaders(source)
}

func mapRouteDispatchError(err error) error {
	switch {
	case errors.Is(err, ErrRouteLoginRequired):
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	case errors.Is(err, routes.ErrDispatchDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, routes.ErrAmbiguousRoute), errors.Is(err, routes.ErrProviderSelectionStale):
		return fiber.NewError(fiber.StatusServiceUnavailable, "extensions.route_provider_unavailable")
	case errors.Is(err, routes.ErrDispatchSchema):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "extensions.route_schema_invalid")
	case errors.Is(err, routes.ErrDispatchIdempotencyKeyInvalid):
		return fiber.NewError(fiber.StatusBadRequest, "idempotency.key_invalid")
	case errors.Is(err, routes.ErrDispatchIdempotencyInProgress):
		return fiber.NewError(fiber.StatusConflict, "idempotency.in_progress")
	case errors.Is(err, routes.ErrDispatchIdempotencyConflict):
		return fiber.NewError(fiber.StatusConflict, "idempotency.key_conflict")
	case errors.Is(err, routes.ErrDispatchIdempotencyUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "idempotency.unavailable")
	case errors.Is(err, routes.ErrDispatchTransport):
		return fiber.NewError(fiber.StatusBadGateway, "extensions.route_unavailable")
	default:
		return fmt.Errorf("route dispatch: %w", err)
	}
}
