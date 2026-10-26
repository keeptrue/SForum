package hosthttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	outboundhttp "github.com/keeptrue/sforum/apps/api/app/Support/OutboundHTTP"
	secretstore "github.com/keeptrue/sforum/apps/api/app/Support/SecretStore"
)

// Client is the Host outbound HTTP runtime.
type Client struct {
	mu             sync.Mutex
	safeClient     *http.Client
	rawClient      *http.Client
	secrets        *secretstore.Service
	allowHTTP      bool
	allowRaw       bool
	defaultTimeout time.Duration

	requests, successes, failures, ssrfDenies, retries, bytesIn atomic.Uint64
	traces                                                      []Trace
}

const maxTraceRing = 64

// Options configure a Host HTTP client.
type Options struct {
	// AllowHTTP permits http:// under safe authority (dev only).
	AllowHTTP bool
	// AllowRaw enables AuthorityRaw for fully trusted Host processes.
	AllowRaw bool
	// Timeout is the default request timeout.
	Timeout time.Duration
	// Secrets is optional; required when Request.SecretRef is set.
	Secrets *secretstore.Service
	// SafeClient overrides the SSRF-safe client (tests).
	SafeClient *http.Client
	// RawClient overrides the raw client (tests).
	RawClient *http.Client
}

// New builds a Host HTTP client.
func New(opts Options) *Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	safe := opts.SafeClient
	if safe == nil {
		safe = outboundhttp.NewSafeClient(outboundhttp.Options{
			AllowHTTP: opts.AllowHTTP,
			Timeout:   timeout,
		})
	}
	raw := opts.RawClient
	if raw == nil {
		raw = &http.Client{Timeout: timeout}
	}
	return &Client{
		safeClient:     safe,
		rawClient:      raw,
		secrets:        opts.Secrets,
		allowHTTP:      opts.AllowHTTP,
		allowRaw:       opts.AllowRaw,
		defaultTimeout: timeout,
	}
}

// Do executes a Host-mediated HTTP request with retry, body limits, and tracing.
func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if c == nil {
		return Response{}, ErrInvalid
	}
	started := time.Now()
	c.requests.Add(1)

	request.Method = strings.ToUpper(strings.TrimSpace(request.Method))
	if request.Method == "" {
		request.Method = http.MethodGet
	}
	request.URL = strings.TrimSpace(request.URL)
	request.Authority = strings.ToLower(strings.TrimSpace(request.Authority))
	if request.Authority == "" {
		request.Authority = AuthoritySafe
	}
	if request.Authority != AuthoritySafe && request.Authority != AuthorityRaw {
		c.failures.Add(1)
		return Response{}, ErrInvalid
	}
	if request.Authority == AuthorityRaw && !c.allowRaw {
		c.failures.Add(1)
		return Response{}, ErrRawDenied
	}

	parsed, err := url.Parse(request.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		c.failures.Add(1)
		return Response{}, ErrInvalid
	}
	if request.Authority == AuthoritySafe {
		if err := outboundhttp.ValidatePublicURL(request.URL, outboundhttp.Options{AllowHTTP: c.allowHTTP}); err != nil {
			c.ssrfDenies.Add(1)
			c.failures.Add(1)
			trace := c.finishTrace(request, started, 0, 0, "ssrf")
			return Response{Trace: trace, Attempts: 0}, fmt.Errorf("%w: %v", ErrSSRF, err)
		}
	}

	maxBody := request.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	if maxBody > MaxMaxBodyBytes {
		maxBody = MaxMaxBodyBytes
	}
	retries := request.MaxRetries
	if retries < 0 {
		retries = 0
	}
	if retries > DefaultMaxRetries {
		retries = DefaultMaxRetries
	}

	headers := cloneHeaders(request.Headers)
	secretUsed := false
	if ref := strings.TrimSpace(request.SecretRef); ref != "" {
		if c.secrets == nil {
			c.failures.Add(1)
			return Response{}, ErrSecret
		}
		purpose := strings.TrimSpace(request.SecretPurpose)
		if purpose == "" {
			purpose = "http.credential"
		}
		parsedRef, parseErr := secretstore.ParseReference(ref)
		if parseErr != nil {
			c.failures.Add(1)
			return Response{}, fmt.Errorf("%w: %v", ErrSecret, parseErr)
		}
		lease, resolveErr := c.secrets.Resolve(ctx, secretstore.Caller{
			ExtensionID: request.ExtensionID, Actor: request.Actor,
		}, parsedRef, purpose, secretstore.DefaultResolveTTL)
		if resolveErr != nil {
			c.failures.Add(1)
			return Response{}, fmt.Errorf("%w: %v", ErrSecret, resolveErr)
		}
		headers["Authorization"] = "Bearer " + string(lease.Value)
		secretUsed = true
	}

	httpClient := c.safeClient
	if request.Authority == AuthorityRaw {
		httpClient = c.rawClient
	}

	var lastErr error
	var lastStatus int
	attempts := 0
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			c.retries.Add(1)
			select {
			case <-ctx.Done():
				c.failures.Add(1)
				trace := c.finishTrace(request, started, lastStatus, attempts, "canceled")
				return Response{Trace: trace, Attempts: attempts}, ctx.Err()
			case <-time.After(DefaultRetryBackoff * time.Duration(attempt)):
			}
		}
		attempts++
		status, body, hdrs, doErr := c.oneAttempt(ctx, httpClient, request, headers, maxBody)
		lastStatus = status
		if doErr == nil {
			// 5xx / 429 are transport-retryable; 2xx/3xx/4xx (except 429) complete.
			if attempt < retries && retryable(nil, status) {
				lastErr = fmt.Errorf("retryable status %d", status)
				continue
			}
			c.successes.Add(1)
			c.bytesIn.Add(uint64(len(body)))
			trace := c.finishTraceWithSecret(request, started, status, attempts, "", secretUsed)
			return Response{
				StatusCode: status, Headers: hdrs, Body: body,
				Attempts: attempts, Duration: time.Since(started), Trace: trace,
			}, nil
		}
		lastErr = doErr
		if !retryable(doErr, status) {
			break
		}
	}
	c.failures.Add(1)
	class := errorClass(lastErr)
	trace := c.finishTraceWithSecret(request, started, lastStatus, attempts, class, secretUsed)
	if lastErr == nil {
		lastErr = ErrRetriesExhausted
	}
	return Response{StatusCode: lastStatus, Attempts: attempts, Duration: time.Since(started), Trace: trace}, lastErr
}

func (c *Client) oneAttempt(
	ctx context.Context,
	httpClient *http.Client,
	request Request,
	headers map[string]string,
	maxBody int64,
) (int, []byte, map[string]string, error) {
	var bodyReader io.Reader
	if len(request.Body) > 0 {
		bodyReader = bytes.NewReader(request.Body)
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bodyReader)
	if err != nil {
		return 0, nil, nil, ErrInvalid
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if request.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, request.Timeout)
		defer cancel()
		req = req.WithContext(ctx)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "unsafe") {
			return 0, nil, nil, fmt.Errorf("%w: %v", ErrSSRF, err)
		}
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return resp.StatusCode, nil, nil, err
	}
	if int64(len(body)) > maxBody {
		return resp.StatusCode, nil, nil, ErrResponseTooLarge
	}
	return resp.StatusCode, body, flattenHeaders(resp.Header), nil
}

// Metrics returns process-local counters.
func (c *Client) Metrics() Metrics {
	if c == nil {
		return Metrics{}
	}
	return Metrics{
		Requests: c.requests.Load(), Successes: c.successes.Load(), Failures: c.failures.Load(),
		SSRFDenies: c.ssrfDenies.Load(), Retries: c.retries.Load(), BytesIn: c.bytesIn.Load(),
	}
}

// RecentTraces returns recent outbound traces (no bodies/secrets).
func (c *Client) RecentTraces() []Trace {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Trace(nil), c.traces...)
}

func (c *Client) finishTrace(request Request, started time.Time, status, attempts int, class string) Trace {
	return c.finishTraceWithSecret(request, started, status, attempts, class, false)
}

func (c *Client) finishTraceWithSecret(request Request, started time.Time, status, attempts int, class string, secretUsed bool) Trace {
	host := ""
	if parsed, err := url.Parse(request.URL); err == nil {
		host = parsed.Hostname()
	}
	trace := Trace{
		SchemaVersion: SchemaVersion,
		TraceID:       strings.TrimSpace(request.TraceID),
		Method:        request.Method,
		Host:          host,
		Authority:     request.Authority,
		StatusCode:    status,
		Attempts:      attempts,
		Duration:      time.Since(started),
		ErrorClass:    class,
		SecretUsed:    secretUsed,
		ExtensionID:   strings.TrimSpace(request.ExtensionID),
		Actor:         strings.TrimSpace(request.Actor),
	}
	c.mu.Lock()
	c.traces = append(c.traces, trace)
	if len(c.traces) > maxTraceRing {
		c.traces = append([]Trace(nil), c.traces[len(c.traces)-maxTraceRing:]...)
	}
	c.mu.Unlock()
	return trace
}

func retryable(err error, status int) bool {
	if err != nil {
		if errorsIs(err, ErrSSRF) || errorsIs(err, ErrResponseTooLarge) || errorsIs(err, ErrInvalid) ||
			errorsIs(err, ErrSecret) || errorsIs(err, ErrRawDenied) {
			return false
		}
		// Network / temporary errors are retryable.
		return true
	}
	return status == http.StatusTooManyRequests || status >= 500
}

func errorsIs(err, target error) bool {
	return err != nil && target != nil && (err == target || strings.Contains(err.Error(), target.Error()))
}

func cloneHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func flattenHeaders(header http.Header) map[string]string {
	out := make(map[string]string, len(header))
	size := 0
	for key, values := range header {
		if len(values) == 0 {
			continue
		}
		value := values[0]
		size += len(key) + len(value)
		if size > MaxHeaderBytesStored {
			break
		}
		out[key] = value
	}
	return out
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errorsIs(err, ErrSSRF):
		return "ssrf"
	case errorsIs(err, ErrResponseTooLarge):
		return "response_too_large"
	case errorsIs(err, ErrSecret):
		return "secret"
	case errorsIs(err, ErrRawDenied):
		return "raw_denied"
	default:
		return "transport"
	}
}
