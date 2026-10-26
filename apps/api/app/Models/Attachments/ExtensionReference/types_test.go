package extensionreference

import (
	"testing"
	"time"

	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
)

func TestPolicyFromValues(t *testing.T) {
	for _, test := range []struct {
		name          string
		values        map[string]string
		wantBytes     int64
		wantReference int
	}{
		{name: "defaults", wantBytes: MaxImageBytes, wantReference: DefaultLimit},
		{
			// 站点上限以 MB 计，最小 1 MB 也高于 512 KiB 硬上限，因此有效上限
			// 恒为 512 KiB；这里保留 min() 是为了「上限只会更严」的语义不被改坏。
			name:      "any site limit clamps to the 512KiB hard cap",
			values:    map[string]string{options.NameAttachmentMaxFileSizeMB: "20"},
			wantBytes: MaxImageBytes, wantReference: DefaultLimit,
		},
		{
			name:      "reference limit override",
			values:    map[string]string{options.NameAttachmentExtensionReferenceLimit: "12"},
			wantBytes: MaxImageBytes, wantReference: 12,
		},
		{
			name:      "reference limit clamped to hard maximum",
			values:    map[string]string{options.NameAttachmentExtensionReferenceLimit: "999999"},
			wantBytes: MaxImageBytes, wantReference: LimitMax,
		},
		{
			name:      "reference limit below one falls back to default",
			values:    map[string]string{options.NameAttachmentExtensionReferenceLimit: "0"},
			wantBytes: MaxImageBytes, wantReference: DefaultLimit,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy := PolicyFromValues(test.values)
			if policy.MaxBytes != test.wantBytes || policy.ReferenceLimit != test.wantReference {
				t.Fatalf("policy = %#v", policy)
			}
		})
	}
}

func TestScopeValidation(t *testing.T) {
	if _, scope, err := Scope("example.oauth2", "client:sfo_1"); err != nil || scope != "example.oauth2:client:sfo_1" {
		t.Fatalf("scope = %q err = %v", scope, err)
	}
	for _, test := range []struct {
		name        string
		extensionID string
		context     string
	}{
		{name: "empty extension", extensionID: "", context: "icon"},
		{name: "uppercase extension", extensionID: "Example.OAuth2", context: "icon"},
		{name: "slash in extension", extensionID: "example/oauth2", context: "icon"},
		{name: "empty context", extensionID: "example.oauth2", context: ""},
		{name: "uppercase context", extensionID: "example.oauth2", context: "Icon"},
		{name: "context too long", extensionID: "example.oauth2", context: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "context with space", extensionID: "example.oauth2", context: "a b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := Scope(test.extensionID, test.context); err == nil {
				t.Fatal("unexpectedly accepted")
			}
		})
	}
}

func TestContentTypeAllowlist(t *testing.T) {
	for _, contentType := range []string{"image/png", "image/jpeg", "image/webp", " IMAGE/PNG "} {
		if !IsContentType(contentType) {
			t.Fatalf("%q should be allowed", contentType)
		}
	}
	// SVG/HTML 一旦可匿名内联读取就是同源 XSS 面，必须永远被拒。
	for _, contentType := range []string{"image/svg+xml", "text/html", "application/xhtml+xml", "image/gif", ""} {
		if IsContentType(contentType) {
			t.Fatalf("%q should be rejected", contentType)
		}
	}
}

func TestURLPathIsHostRelative(t *testing.T) {
	if got := URLPath("abc123"); got != "/media/attachments/abc123" {
		t.Fatalf("url = %q", got)
	}
}

func TestLockKeyIsNamespacedPerExtension(t *testing.T) {
	if got := lockKey("example.oauth2"); got != "sforum:attachment-extension@1:example.oauth2" {
		t.Fatalf("lock key = %q", got)
	}
	if lockKey("a") == lockKey("b") {
		t.Fatal("lock key must be scoped per extension")
	}
}

func TestNormalizePage(t *testing.T) {
	if page, perPage := normalizePage(0, 0); page != 1 || perPage != 20 {
		t.Fatalf("defaults = %d/%d", page, perPage)
	}
	if _, perPage := normalizePage(1, 100000); perPage != maxListPerPage {
		t.Fatalf("perPage = %d", perPage)
	}
}

func TestLatestRevision(t *testing.T) {
	earlier := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Minute)
	latest := latestRevision(map[int64]referenceCount{
		1: {Count: 1, UpdatedAt: earlier},
		2: {Count: 0, UpdatedAt: later},
	})
	if !latest.Equal(later) {
		t.Fatalf("latest = %v", latest)
	}
	if !latestRevision(nil).IsZero() {
		t.Fatal("empty input must yield the zero time")
	}
}
