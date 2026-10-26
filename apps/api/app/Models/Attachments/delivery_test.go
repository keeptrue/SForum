package attachments

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	storage "github.com/keeptrue/sforum/apps/api/app/Support/Storage"
)

type deliveryTestAdapter struct {
	signedURL string
	signedErr error
	openCount int
}

func (a *deliveryTestAdapter) Put(context.Context, string, storage.PutInput) error { return nil }
func (a *deliveryTestAdapter) Open(context.Context, string) (io.ReadCloser, error) {
	a.openCount++
	return io.NopCloser(strings.NewReader("streamed")), nil
}
func (a *deliveryTestAdapter) Delete(context.Context, string) error { return nil }
func (a *deliveryTestAdapter) Stat(context.Context, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, nil
}
func (a *deliveryTestAdapter) Exists(context.Context, string) (bool, error) { return true, nil }
func (a *deliveryTestAdapter) PublicURL(string) string                      { return "" }
func (a *deliveryTestAdapter) SignedURL(context.Context, string, time.Duration) (string, error) {
	return a.signedURL, a.signedErr
}
func (a *deliveryTestAdapter) Probe(context.Context) error { return nil }

func TestStorageContentDeliveryRedirectsRemoteSafeMedia(t *testing.T) {
	adapter := &deliveryTestAdapter{signedURL: "https://objects.example.test/signed?token=abc"}
	delivery, err := storageContentDelivery(context.Background(), adapter, "instance:remote", "attachments/photo.jpg", "image/jpeg", "photo.jpg")
	if err != nil {
		t.Fatalf("resolve delivery: %v", err)
	}
	if delivery.RedirectURL != adapter.signedURL || delivery.Reader != nil {
		t.Fatalf("expected signed redirect, got %#v", delivery)
	}
	if adapter.openCount != 0 {
		t.Fatalf("redirect path opened object %d times", adapter.openCount)
	}
}

func TestStorageContentDeliveryStreamsWhenRedirectIsUnsafeOrUnsupported(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		content   string
		signedURL string
		signedErr error
	}{
		{name: "local", provider: storage.ProviderLocal, content: "image/jpeg", signedURL: "https://cdn.example/photo.jpg"},
		{name: "active content", provider: "instance:remote", content: "text/html", signedURL: "https://objects.example/signed"},
		{name: "unsupported", provider: "instance:remote", content: "image/jpeg", signedErr: errors.New("not supported")},
		{name: "unsafe scheme", provider: "instance:remote", content: "image/jpeg", signedURL: "javascript:alert(1)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &deliveryTestAdapter{signedURL: test.signedURL, signedErr: test.signedErr}
			delivery, err := storageContentDelivery(context.Background(), adapter, test.provider, "file", test.content, "file")
			if err != nil {
				t.Fatalf("resolve delivery: %v", err)
			}
			if delivery.RedirectURL != "" || delivery.Reader == nil {
				t.Fatalf("expected streamed delivery, got %#v", delivery)
			}
			if adapter.openCount != 1 {
				t.Fatalf("stream path opened object %d times", adapter.openCount)
			}
		})
	}
}
