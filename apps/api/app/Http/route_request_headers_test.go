package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

func TestCopyRouteRequestHeadersStripsSensitiveAndConnectionNamedHeaders(t *testing.T) {
	source := http.Header{
		"Accept-Language":     {"zh-CN"},
		"Authorization":       {"Bearer browser-secret", "Bearer second"},
		"Connection":          {"keep-alive, X-Hop-Secret"},
		"connection":          {"X-Lower-Hop"},
		"CONNECTION":          {"X-Upper-Hop"},
		"Cookie":              {"session=browser-secret", "preference=second"},
		"Host":                {"forum.example.com"},
		"Proxy-Authorization": {"Basic proxy-secret"},
		"Proxy-Connection":    {"keep-alive"},
		"X-Api-Key":           {"api-key-secret", "api-key-second"},
		"X-Auth-Token":        {"auth-token-secret", "auth-token-second"},
		"X-Csrf-Token":        {"csrf-secret"},
		"X-Hop-Secret":        {"hop-secret"},
		"X-Lower-Hop":         {"lower-hop-secret"},
		"X-SForum-Forged":     {"forged"},
		"X-Trace-ID":          {"trace-1"},
		"X-Upper-Hop":         {"upper-hop-secret"},
	}
	for _, test := range []struct {
		name      string
		authority routes.ResolvedRequestAuthority
		raw       bool
	}{
		{name: "filtered", authority: routes.ResolvedRequestAuthority{
			Mode: routes.RequestAuthorityFiltered, GuardKind: routes.RequestGuardHost,
		}},
		{name: "raw", authority: routes.ResolvedRequestAuthority{
			Mode: routes.RequestAuthorityRaw, GuardKind: routes.RequestGuardRawRequest,
		}, raw: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := make(http.Header)
			if err := copyRouteRequestHeaders(target, source, test.authority); err != nil {
				t.Fatal(err)
			}
			if target.Get("Accept-Language") != "zh-CN" || target.Get("X-Trace-ID") != "trace-1" {
				t.Fatalf("ordinary headers were removed: %#v", target)
			}
			for _, name := range []string{
				"Connection", "Host", "Proxy-Authorization", "Proxy-Connection", "X-Csrf-Token",
				"X-Hop-Secret", "X-Lower-Hop", "X-SForum-Forged", "X-Upper-Hop",
			} {
				if values := target.Values(name); len(values) != 0 {
					t.Fatalf("blocked header %s survived: %#v", name, values)
				}
			}
			for _, name := range []string{"Authorization", "Cookie", "X-API-Key", "X-Auth-Token"} {
				if got := target.Values(name); test.raw && !reflect.DeepEqual(got, source.Values(name)) {
					t.Fatalf("raw header %s = %#v", name, got)
				} else if !test.raw && len(got) != 0 {
					t.Fatalf("filtered header %s survived: %#v", name, got)
				}
			}
		})
	}
}

func TestBufferedRouteStepInvokerDoesNotFollowRedirects(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	t.Cleanup(destination.Close)
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", destination.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)

	response, err := NewBufferedRouteStepInvoker(nil).Client.Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || destinationCalls.Load() != 0 {
		t.Fatalf("status=%d destinationCalls=%d", response.StatusCode, destinationCalls.Load())
	}
}
