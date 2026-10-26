package hosthttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

func TestDoSuccessAndTrace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test") != "1" {
			t.Fatalf("missing header")
		}
		w.Header().Set("X-Reply", "ok")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := New(Options{
		AllowHTTP:  true,
		SafeClient: server.Client(), // httptest is loopback; inject client to skip SSRF dial
	})
	// Safe path still validates URL via ValidatePublicURL — loopback fails.
	// Use AuthorityRaw for httptest fixtures when AllowRaw is set.
	client = New(Options{
		AllowHTTP: true, AllowRaw: true,
		RawClient: server.Client(),
	})
	resp, err := client.Do(context.Background(), Request{
		Method: "GET", URL: server.URL + "/v1", Authority: AuthorityRaw,
		Headers: map[string]string{"X-Test": "1"}, TraceID: "t-1", Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || string(resp.Body) != `{"ok":true}` || resp.Attempts != 1 {
		t.Fatalf("resp = %#v body=%q", resp, resp.Body)
	}
	if resp.Trace.TraceID != "t-1" || resp.Trace.Authority != AuthorityRaw || resp.Headers["X-Reply"] != "ok" {
		t.Fatalf("trace/headers = %#v %#v", resp.Trace, resp.Headers)
	}
	if client.Metrics().Successes != 1 {
		t.Fatalf("metrics = %#v", client.Metrics())
	}
}

func TestSSRFDeniesPrivateURL(t *testing.T) {
	client := New(Options{AllowHTTP: true})
	_, err := client.Do(context.Background(), Request{
		Method: "GET", URL: "http://127.0.0.1:9/secret", Authority: AuthoritySafe,
	})
	if !errors.Is(err, ErrSSRF) && (err == nil || !strings.Contains(err.Error(), "unsafe")) {
		t.Fatalf("expected SSRF deny, got %v", err)
	}
	if client.Metrics().SSRFDenies < 1 {
		t.Fatalf("ssrf metrics = %#v", client.Metrics())
	}
}

func TestRawAuthorityDeniedWithoutGrant(t *testing.T) {
	client := New(Options{AllowRaw: false})
	_, err := client.Do(context.Background(), Request{
		Method: "GET", URL: "http://example.com/", Authority: AuthorityRaw,
	})
	if !errors.Is(err, ErrRawDenied) {
		t.Fatalf("raw deny = %v", err)
	}
}

func TestResponseBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer server.Close()
	client := New(Options{AllowRaw: true, RawClient: server.Client()})
	_, err := client.Do(context.Background(), Request{
		Method: "GET", URL: server.URL, Authority: AuthorityRaw, MaxBodyBytes: 16,
	})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("body limit = %v", err)
	}
}

func TestRetryOn5xx(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client := New(Options{AllowRaw: true, RawClient: server.Client()})
	resp, err := client.Do(context.Background(), Request{
		Method: "GET", URL: server.URL, Authority: AuthorityRaw, MaxRetries: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "ok" || resp.Attempts != 3 {
		t.Fatalf("resp = %#v body=%q hits=%d", resp, resp.Body, hits.Load())
	}
	if client.Metrics().Retries < 2 {
		t.Fatalf("retry metrics = %#v", client.Metrics())
	}
}

func TestSecretRefInjection(t *testing.T) {
	secrets, err := secretstore.New(secretstore.NewMemoryStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := secrets.Put(ctx, secretstore.Ref{Namespace: "demo.http", SecretID: "token"},
		[]byte("tok-123"), secretstore.PutOptions{Actor: "admin", Purposes: []string{"http.credential"}}); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := New(Options{AllowRaw: true, RawClient: server.Client(), Secrets: secrets})
	resp, err := client.Do(ctx, Request{
		Method: "GET", URL: server.URL, Authority: AuthorityRaw,
		SecretRef: "sforum.secret://demo.http/token", ExtensionID: "demo.http",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-123" || !resp.Trace.SecretUsed {
		t.Fatalf("auth=%q trace=%#v", gotAuth, resp.Trace)
	}
	// Cross-namespace secret denied.
	_, err = client.Do(ctx, Request{
		Method: "GET", URL: server.URL, Authority: AuthorityRaw,
		SecretRef: "sforum.secret://demo.http/token", ExtensionID: "other.plugin",
	})
	if err == nil || !errors.Is(err, ErrSecret) && !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("cross-ns secret = %v", err)
	}
}

func TestInvalidURL(t *testing.T) {
	client := New(Options{})
	if _, err := client.Do(context.Background(), Request{URL: "not-a-url"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid url = %v", err)
	}
}

func TestTimeoutContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()
	client := New(Options{AllowRaw: true, RawClient: server.Client()})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Do(ctx, Request{URL: server.URL, Authority: AuthorityRaw})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
