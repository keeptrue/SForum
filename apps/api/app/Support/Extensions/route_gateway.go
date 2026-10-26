package extensionsruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

var ErrRuntimeRouteIncident = errors.New("extension runtime quarantined after route execution incident")

type RouteGateway struct {
	client *http.Client
}

func NewRouteGateway() *RouteGateway {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// 插件路由只允许直连握手校验过的 loopback target，不继承部署代理。
	transport.Proxy = nil
	return &RouteGateway{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (g *RouteGateway) Proxy(input *ProxyInput) error {
	if g == nil || g.client == nil || input == nil || input.Request == nil || input.Response == nil {
		return fmt.Errorf("extension route proxy input is invalid")
	}
	target, err := url.Parse(input.TargetBase)
	if err != nil {
		return err
	}
	relative, err := url.Parse(input.TargetPath)
	if err != nil || relative.IsAbs() || relative.Host != "" {
		return fmt.Errorf("extension route target path is invalid")
	}
	target.Path = relative.Path
	target.RawPath = relative.RawPath
	target.RawQuery = relative.RawQuery
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, string(input.Request.Header.Method()), target.String(), bytes.NewReader(input.Request.Body()))
	if err != nil {
		return err
	}
	connectionHeaders := routeConnectionHeaderTokens(input.Request.Header.PeekAll("Connection"))
	input.Request.Header.VisitAll(func(key, value []byte) {
		name := string(key)
		if routeRequestHeaderAllowed(name, connectionHeaders) {
			request.Header.Add(name, string(value))
		}
	})
	request.Header.Set("X-SForum-Extension-ID", input.ExtensionID)
	if input.ActorID != "" {
		request.Header.Set("X-SForum-Actor-ID", input.ActorID)
	}
	if input.Locale != "" {
		request.Header.Set("X-SForum-Locale", input.Locale)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	input.Response.Reset()
	input.Response.SetStatusCode(response.StatusCode)
	for name, values := range routes.FilterPluginResponseHeaders(response.Header) {
		for _, value := range values {
			input.Response.Header.Add(name, value)
		}
	}
	input.Response.SetBodyRaw(body)
	return nil
}

func routeRequestHeaderAllowed(name string, connectionHeaders map[string]struct{}) bool {
	canonical := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(canonical, "x-sforum-") {
		return false
	}
	if _, blocked := connectionHeaders[canonical]; blocked {
		return false
	}
	switch canonical {
	case "", "host", "content-length", "cookie", "authorization", "proxy-authorization",
		"x-api-key", "x-auth-token", "x-csrf-token", "connection", "keep-alive", "proxy-authenticate",
		"proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return false
	default:
		return true
	}
}

func routeConnectionHeaderTokens(values [][]byte) map[string]struct{} {
	blocked := make(map[string]struct{})
	for _, value := range values {
		for _, token := range strings.Split(string(value), ",") {
			if canonical := strings.ToLower(strings.TrimSpace(token)); canonical != "" {
				blocked[canonical] = struct{}{}
			}
		}
	}
	return blocked
}
