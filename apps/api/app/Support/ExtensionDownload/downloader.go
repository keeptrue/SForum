// Package extensiondownload fetches an extension ZIP from a public HTTPS URL.
// It is deliberately separate from extensionpackage: this package owns the
// outbound network boundary and byte-level limits, while extensionpackage owns
// archive/manifest parsing.
package extensiondownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	outboundhttp "github.com/keeptrue/sforum/apps/api/app/Support/OutboundHTTP"
)

const (
	// DefaultMaxBytes matches extensions.MaxArchiveBytes; callers should pass the
	// model constant explicitly so the two ceilings cannot drift silently.
	DefaultMaxBytes = 50 * 1024 * 1024
	// DefaultTimeout covers a slow 50 MB download without holding the admin
	// request open indefinitely.
	DefaultTimeout = 90 * time.Second

	maxURLLength      = 2048
	maxFileNameLength = 255
)

var (
	// ErrInvalidURL means the target is not an allowed public HTTPS URL.
	ErrInvalidURL = errors.New("extension download: invalid url")
	// ErrTooLarge means the response exceeded the configured byte ceiling.
	ErrTooLarge = errors.New("extension download: archive too large")
	// ErrNotArchive means the response body is not a ZIP archive.
	ErrNotArchive = errors.New("extension download: response is not a zip archive")
	// ErrFetchFailed means the request did not produce a usable 200 response.
	ErrFetchFailed = errors.New("extension download: fetch failed")
)

// Options configures a Downloader.
type Options struct {
	// MaxBytes is the hard ceiling for the downloaded ZIP. Defaults to
	// DefaultMaxBytes when zero.
	MaxBytes int64
	// Timeout is the whole-request timeout. Defaults to DefaultTimeout when zero.
	Timeout time.Duration
	// AllowHTTP, AllowPrivate and Client are test-only escape hatches. Production
	// wiring leaves them zero so only public HTTPS targets are reachable.
	AllowHTTP    bool
	AllowPrivate bool
	Client       *http.Client
}

// Archive is a downloaded, size-bounded ZIP plus provenance.
type Archive struct {
	FileName string
	Data     []byte
	// SourceURL is the exact URL that produced the bytes. Callers must sanitize
	// it before persisting or auditing it.
	SourceURL string
	// Digest is the lowercase SHA-256 of Data (the ZIP bytes), not the extracted
	// package tree digest.
	Digest string
}

// Downloader fetches extension archives through the shared SSRF-safe client.
type Downloader struct {
	maxBytes     int64
	allowHTTP    bool
	allowPrivate bool
	client       *http.Client
}

// New builds a Downloader. A nil/zero Client uses outboundhttp.NewSafeClient.
func New(opts Options) *Downloader {
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := opts.Client
	if client == nil {
		client = outboundhttp.NewSafeClient(outboundhttp.Options{
			AllowHTTP:    opts.AllowHTTP,
			AllowPrivate: opts.AllowPrivate,
			Timeout:      timeout,
		})
	}
	return &Downloader{
		maxBytes:     maxBytes,
		allowHTTP:    opts.AllowHTTP,
		allowPrivate: opts.AllowPrivate,
		client:       client,
	}
}

// Fetch downloads one archive. It validates the URL before any network call and
// re-validates every redirect through the safe client.
func (d *Downloader) Fetch(ctx context.Context, rawURL string) (Archive, error) {
	if d == nil || d.client == nil {
		return Archive{}, ErrFetchFailed
	}
	target := strings.TrimSpace(rawURL)
	if target == "" || len(target) > maxURLLength {
		return Archive{}, fmt.Errorf("%w: empty or too long", ErrInvalidURL)
	}
	if err := outboundhttp.ValidatePublicURL(target, outboundhttp.Options{
		AllowHTTP:    d.allowHTTP,
		AllowPrivate: d.allowPrivate,
	}); err != nil {
		// Keep the unsafe-target detail server-side; callers map this to a generic
		// validation reason so private addresses cannot be probed through errors.
		return Archive{}, fmt.Errorf("%w: target rejected", ErrInvalidURL)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Archive{}, fmt.Errorf("%w: build request", ErrInvalidURL)
	}
	request.Header.Set("Accept", "application/zip, application/octet-stream;q=0.9")
	request.Header.Set("User-Agent", "SForum-ExtensionInstaller/1.0")

	response, err := d.client.Do(request)
	if err != nil {
		return Archive{}, fmt.Errorf("%w: request failed", ErrFetchFailed)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return Archive{}, fmt.Errorf("%w: status %d", ErrFetchFailed, response.StatusCode)
	}
	if response.ContentLength > d.maxBytes {
		return Archive{}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, d.maxBytes+1))
	if err != nil {
		return Archive{}, fmt.Errorf("%w: read body", ErrFetchFailed)
	}
	if int64(len(data)) > d.maxBytes {
		return Archive{}, ErrTooLarge
	}
	if len(data) < 4 || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return Archive{}, ErrNotArchive
	}

	sum := sha256.Sum256(data)
	return Archive{
		FileName:  archiveFileName(response, target),
		Data:      data,
		SourceURL: target,
		Digest:    hex.EncodeToString(sum[:]),
	}, nil
}

func archiveFileName(response *http.Response, rawURL string) string {
	if disposition := response.Header.Get("Content-Disposition"); disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil {
			if name := sanitizeFileName(params["filename"]); name != "" {
				return name
			}
		}
	}
	if parsed, err := url.Parse(rawURL); err == nil {
		if name := sanitizeFileName(path.Base(parsed.Path)); name != "" {
			return name
		}
	}
	return "extension.zip"
}

func sanitizeFileName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = path.Base(name)
	if name == "" || name == "." || name == "/" {
		return ""
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if len(name) > maxFileNameLength {
		name = name[:maxFileNameLength]
	}
	return name
}
