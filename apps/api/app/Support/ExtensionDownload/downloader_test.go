package extensiondownload_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	extensiondownload "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionDownload"
)

var zipPrefix = []byte("PK\x03\x04extension-fixture")

func TestFetchRejectsUnsafeURLs(t *testing.T) {
	downloader := extensiondownload.New(extensiondownload.Options{})
	tests := []string{
		"",
		"ftp://example.com/extension.zip",
		"http://example.com/extension.zip",
		"https://user:pass@example.com/extension.zip",
		"https://127.0.0.1/extension.zip",
		"https://[::1]/extension.zip",
		"https://169.254.169.254/latest/meta-data/",
		"https://example.com/" + strings.Repeat("a", 2048),
	}
	for _, target := range tests {
		if _, err := downloader.Fetch(context.Background(), target); !errors.Is(err, extensiondownload.ErrInvalidURL) {
			t.Fatalf("Fetch(%q) error = %v, want ErrInvalidURL", target, err)
		}
	}
}

func TestFetchDownloadsZipArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="demo-extension.zip"`)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipPrefix)
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	archive, err := downloader.Fetch(context.Background(), server.URL+"/downloads/extension.zip")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if string(archive.Data) != string(zipPrefix) {
		t.Fatalf("unexpected archive bytes: %q", archive.Data)
	}
	if archive.FileName != "demo-extension.zip" {
		t.Fatalf("FileName = %q, want demo-extension.zip", archive.FileName)
	}
	if archive.SourceURL != server.URL+"/downloads/extension.zip" {
		t.Fatalf("SourceURL = %q", archive.SourceURL)
	}
	sum := sha256.Sum256(zipPrefix)
	if archive.Digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("Digest = %q, want sha256 of the ZIP bytes", archive.Digest)
	}
}

func TestFetchFallsBackToURLFileName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipPrefix)
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	archive, err := downloader.Fetch(context.Background(), server.URL+"/downloads/demo.zip")
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if archive.FileName != "demo.zip" {
		t.Fatalf("FileName = %q, want demo.zip", archive.FileName)
	}
}

func TestFetchRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := append([]byte("PK\x03\x04"), []byte(strings.Repeat("x", 64))...)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		MaxBytes:     16,
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	if _, err := downloader.Fetch(context.Background(), server.URL+"/extension.zip"); !errors.Is(err, extensiondownload.ErrTooLarge) {
		t.Fatalf("Fetch error = %v, want ErrTooLarge", err)
	}
}

func TestFetchRejectsDeclaredContentLengthOverLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		MaxBytes:     32,
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	if _, err := downloader.Fetch(context.Background(), server.URL+"/extension.zip"); !errors.Is(err, extensiondownload.ErrTooLarge) {
		t.Fatalf("Fetch error = %v, want ErrTooLarge", err)
	}
}

func TestFetchRejectsNonZipResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>release page, not an asset</html>"))
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	if _, err := downloader.Fetch(context.Background(), server.URL+"/releases/tag/v1.0.0"); !errors.Is(err, extensiondownload.ErrNotArchive) {
		t.Fatalf("Fetch error = %v, want ErrNotArchive", err)
	}
}

func TestFetchRejectsNon200Response(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	if _, err := downloader.Fetch(context.Background(), server.URL+"/missing.zip"); !errors.Is(err, extensiondownload.ErrFetchFailed) {
		t.Fatalf("Fetch error = %v, want ErrFetchFailed", err)
	}
}

func TestFetchHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write(zipPrefix)
	}))
	defer server.Close()

	downloader := extensiondownload.New(extensiondownload.Options{
		AllowHTTP:    true,
		AllowPrivate: true,
		Client:       server.Client(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := downloader.Fetch(ctx, server.URL+"/extension.zip"); !errors.Is(err, extensiondownload.ErrFetchFailed) {
		t.Fatalf("Fetch error = %v, want ErrFetchFailed", err)
	}
}
